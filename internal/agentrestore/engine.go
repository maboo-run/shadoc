package agentrestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/restic"
)

const Kind execution.EngineKind = "restic-restore"

type Definition struct {
	Repository           restic.Repository `json:"repository"`
	RepositoryID         string            `json:"repositoryId,omitempty"`
	SnapshotID           string            `json:"snapshotId"`
	SourcePath           string            `json:"sourcePath"`
	Target               string            `json:"target"`
	Includes             []string          `json:"includes,omitempty"`
	DownloadKiBPerSecond int               `json:"downloadKiBPerSecond,omitempty"`
}

type Runner interface {
	Execute(context.Context, restic.Operation) (restic.Result, error)
}

type Engine struct {
	style  string
	roots  []string
	runner Runner
}

func New(style string, roots []string, runner Runner) *Engine {
	return &Engine{style: style, roots: append([]string(nil), roots...), runner: runner}
}

func (*Engine) Kind() execution.EngineKind { return Kind }

func (e *Engine) Validate(raw json.RawMessage) error {
	definition, err := decode(raw)
	if err != nil {
		return err
	}
	if definition.Repository.Location == "" || definition.Repository.Password == "" || definition.SnapshotID == "" || definition.SourcePath == "" || definition.Target == "" {
		return errors.New("Agent restore definition is incomplete")
	}
	if definition.DownloadKiBPerSecond < 0 || !validTarget(definition.Target, e.style) || !withinRoots(definition.Target, e.roots, e.style) {
		return errors.New("Agent restore target is outside allowed roots")
	}
	if _, err := normalizeIncludes(definition.Includes); err != nil {
		return err
	}
	return nil
}

func (e *Engine) Run(ctx context.Context, assignment execution.Assignment) (execution.Outcome, error) {
	definition, err := decode(assignment.Definition)
	if err != nil {
		return execution.Outcome{Status: "failed"}, err
	}
	if err := e.Validate(assignment.Definition); err != nil {
		return execution.Outcome{Status: "failed"}, err
	}
	includes, _ := normalizeIncludes(definition.Includes)
	if err := validateTargetOnDisk(definition.Target, e.roots, e.style); err != nil {
		return execution.Outcome{Status: "failed"}, err
	}
	parent := filepath.Dir(definition.Target)
	staging, err := os.MkdirTemp(parent, ".shadoc-restore-")
	if err != nil {
		return execution.Outcome{Status: "failed"}, errors.New("create Agent restore staging directory")
	}
	args := make([]string, 0, 5+len(includes)*2)
	if definition.DownloadKiBPerSecond > 0 {
		args = append(args, "--limit-download", strconv.Itoa(definition.DownloadKiBPerSecond))
	}
	args = append(args, definition.SnapshotID+":"+filepath.ToSlash(filepath.Clean(definition.SourcePath)), "--target", staging)
	for _, include := range includes {
		args = append(args, "--include", include)
	}
	result, err := e.runner.Execute(ctx, restic.Operation{Kind: restic.RestoreDirectory, Repository: definition.Repository, Arguments: args})
	if err != nil || result.Outcome == restic.Failure {
		return execution.Outcome{Status: "failed"}, errors.New("Agent directory restore failed; staging cleanup may be required")
	}
	if _, err := os.Lstat(definition.Target); !errors.Is(err, os.ErrNotExist) {
		return execution.Outcome{Status: "failed"}, errors.New("Agent restore target changed while restore was running")
	}
	if err := os.Rename(staging, definition.Target); err != nil {
		return execution.Outcome{Status: "failed"}, errors.New("commit Agent restore target")
	}
	return execution.Outcome{Status: "succeeded", Summary: map[string]any{"includeCount": len(includes), "targetCreated": true}}, nil
}

func decode(raw json.RawMessage) (Definition, error) {
	var definition Definition
	if !json.Valid(raw) || json.Unmarshal(raw, &definition) != nil {
		return definition, errors.New("valid Agent restore definition is required")
	}
	return definition, nil
}

func normalizeIncludes(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
		if value == "" || value == "." || strings.HasPrefix(value, "/") || value == ".." || strings.HasPrefix(value, "../") || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("Agent restore selection must be a safe relative path")
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result, nil
}

func validTarget(value, style string) bool {
	if strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if style == "windows" {
		value = strings.ReplaceAll(value, "/", "\\")
		return len(value) >= 3 && isLetter(value[0]) && value[1] == ':' && value[2] == '\\' && !containsParent(value, "\\")
	}
	return filepath.IsAbs(value) && !containsParent(filepath.ToSlash(value), "/")
}

func withinRoots(value string, roots []string, style string) bool {
	for _, root := range roots {
		if style == "windows" {
			root = strings.ToLower(strings.TrimRight(strings.ReplaceAll(root, "/", "\\"), "\\"))
			candidate := strings.ToLower(strings.TrimRight(strings.ReplaceAll(value, "/", "\\"), "\\"))
			if candidate == root || strings.HasPrefix(candidate, root+"\\") {
				return true
			}
			continue
		}
		root, value = filepath.Clean(root), filepath.Clean(value)
		if root == string(filepath.Separator) || value == root || strings.HasPrefix(value, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func containsParent(value, separator string) bool {
	for _, part := range strings.Split(value, separator) {
		if part == ".." {
			return true
		}
	}
	return false
}

func isLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func validateTargetOnDisk(target string, roots []string, style string) error {
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return errors.New("Agent restore target must not already exist")
	}
	parent := filepath.Dir(target)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return errors.New("Agent restore target parent must be an existing directory")
	}
	resolved, err := filepath.EvalSymlinks(parent)
	resolvedRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		resolvedRoot, resolveErr := filepath.EvalSymlinks(root)
		if resolveErr == nil {
			resolvedRoots = append(resolvedRoots, resolvedRoot)
		}
	}
	if err != nil || !withinRoots(filepath.Join(resolved, filepath.Base(target)), resolvedRoots, style) {
		return fmt.Errorf("Agent restore target resolves outside allowed roots")
	}
	return nil
}
