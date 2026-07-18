package agentfilesystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maboo-run/shadoc/internal/execution"
)

const Kind execution.EngineKind = "agent-filesystem"

type Operation string

const (
	Browse                Operation = "browse"
	CreateDirectory       Operation = "create-directory"
	PreviewScope          Operation = "preview-scope"
	ValidateRestoreTarget Operation = "validate-restore-target"
)

type Definition struct {
	Operation  Operation `json:"operation"`
	Path       string    `json:"path"`
	Exclusions []string  `json:"exclusions,omitempty"`
	Limit      int       `json:"limit,omitempty"`
}

type Entry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
}

type Engine struct {
	style string
	roots []string
}

func New(style string, roots []string) *Engine {
	return &Engine{style: style, roots: append([]string(nil), roots...)}
}

func DefaultRoots(style string) []string {
	if style != "windows" {
		return []string{"/"}
	}
	roots := make([]string, 0)
	for drive := 'A'; drive <= 'Z'; drive++ {
		root := string(drive) + `:\`
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			roots = append(roots, root)
		}
	}
	return roots
}
func (e *Engine) Kind() execution.EngineKind  { return Kind }
func (e *Engine) Probe(context.Context) error { return nil }

func (e *Engine) Validate(raw json.RawMessage) error {
	definition, err := e.decode(raw)
	if err != nil {
		return err
	}
	if !e.allowed(definition.Path) {
		return errors.New("filesystem path is outside allowed roots")
	}
	return nil
}

func (e *Engine) Run(ctx context.Context, assignment execution.Assignment) (execution.Outcome, error) {
	definition, err := e.decode(assignment.Definition)
	if err != nil || !e.allowed(definition.Path) {
		return execution.Outcome{Status: "failed"}, errors.Join(err, errors.New("filesystem path is outside allowed roots"))
	}
	if e.style == "windows" && os.PathSeparator != '\\' {
		return execution.Outcome{Status: "failed"}, errors.New("Windows filesystem request requires a Windows Agent")
	}
	switch definition.Operation {
	case Browse:
		if err := e.ensureResolvedAllowed(definition.Path, false); err != nil {
			return execution.Outcome{Status: "failed"}, err
		}
		items, err := os.ReadDir(definition.Path)
		if err != nil {
			return execution.Outcome{Status: "failed"}, fmt.Errorf("browse directory: %w", err)
		}
		entries := make([]Entry, 0, len(items))
		for _, item := range items {
			if item.IsDir() {
				entries = append(entries, Entry{Name: item.Name(), Path: filepath.Join(definition.Path, item.Name()), Directory: true})
			}
		}
		sort.Slice(entries, func(i, j int) bool { return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name) })
		return execution.Outcome{Status: "succeeded", Summary: map[string]any{"path": definition.Path, "entries": entries}}, nil
	case CreateDirectory:
		if err := e.ensureResolvedAllowed(definition.Path, true); err != nil {
			return execution.Outcome{Status: "failed"}, err
		}
		if err := os.MkdirAll(definition.Path, 0o750); err != nil {
			return execution.Outcome{Status: "failed"}, fmt.Errorf("create directory: %w", err)
		}
		return execution.Outcome{Status: "succeeded", Summary: map[string]any{"path": definition.Path, "created": true}}, nil
	case PreviewScope:
		if err := e.ensureResolvedAllowed(definition.Path, false); err != nil {
			return execution.Outcome{Status: "failed"}, err
		}
		summary, err := scanScopeContext(ctx, definition.Path, definition.Exclusions, definition.Limit)
		if err != nil {
			return execution.Outcome{Status: "failed", Summary: scopeSummaryMap(summary)}, fmt.Errorf("preview source scope: %w", err)
		}
		return execution.Outcome{Status: "succeeded", Summary: scopeSummaryMap(summary)}, nil
	case ValidateRestoreTarget:
		if _, err := os.Lstat(definition.Path); !errors.Is(err, os.ErrNotExist) {
			return execution.Outcome{Status: "failed"}, errors.New("restore target must not already exist")
		}
		parent := filepath.Dir(definition.Path)
		info, err := os.Stat(parent)
		if err != nil || !info.IsDir() {
			return execution.Outcome{Status: "failed"}, errors.New("restore target parent must be an existing directory")
		}
		if err := e.ensureResolvedAllowed(definition.Path, true); err != nil {
			return execution.Outcome{Status: "failed"}, err
		}
		return execution.Outcome{Status: "succeeded", Summary: map[string]any{"targetAvailable": true}}, nil
	default:
		return execution.Outcome{Status: "failed"}, errors.New("unsupported filesystem operation")
	}
}

func (e *Engine) ensureResolvedAllowed(value string, allowMissing bool) error {
	if e.style == "windows" {
		// filepath.EvalSymlinks uses the host path syntax. Windows junctions and
		// symlinks are resolved when this code runs on a Windows Agent.
		if os.PathSeparator != '\\' {
			return nil
		}
	}
	candidate := value
	if allowMissing {
		for {
			if _, err := os.Lstat(candidate); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect directory path: %w", err)
			}
			parent := filepath.Dir(candidate)
			if parent == candidate {
				return errors.New("filesystem path has no existing parent")
			}
			candidate = parent
		}
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return fmt.Errorf("resolve directory path: %w", err)
	}
	for _, root := range e.roots {
		resolvedRoot, rootErr := filepath.EvalSymlinks(root)
		if rootErr == nil && containsPath(resolvedRoot, resolved, e.style == "windows") {
			return nil
		}
	}
	return errors.New("filesystem path resolves outside allowed roots")
}

func (e *Engine) decode(raw json.RawMessage) (Definition, error) {
	var definition Definition
	if !json.Valid(raw) || json.Unmarshal(raw, &definition) != nil || (definition.Operation != Browse && definition.Operation != CreateDirectory && definition.Operation != PreviewScope && definition.Operation != ValidateRestoreTarget) || strings.TrimSpace(definition.Path) == "" || strings.ContainsAny(definition.Path, "\x00\r\n") {
		return definition, errors.New("valid filesystem definition is required")
	}
	if definition.Operation != PreviewScope && (len(definition.Exclusions) != 0 || definition.Limit != 0) {
		return definition, errors.New("filesystem operation contains unsupported scope options")
	}
	if definition.Limit < 0 || definition.Limit > MaxScopeItems || len(definition.Exclusions) > 256 {
		return definition, errors.New("filesystem scope preview exceeds limits")
	}
	for _, exclusion := range definition.Exclusions {
		if strings.TrimSpace(exclusion) == "" || len(exclusion) > 1024 || strings.ContainsAny(exclusion, "\x00\r\n") {
			return definition, errors.New("filesystem scope exclusion is invalid")
		}
	}
	if e.style == "windows" {
		path := strings.ReplaceAll(definition.Path, "/", "\\")
		if len(path) < 3 || path[1] != ':' || path[2] != '\\' || !isLetter(path[0]) || containsParent(path, "\\") {
			return definition, errors.New("absolute Windows path without parent traversal is required")
		}
	} else if !filepath.IsAbs(definition.Path) || containsParent(definition.Path, "/") {
		return definition, errors.New("absolute POSIX path without parent traversal is required")
	}
	return definition, nil
}

func (e *Engine) allowed(value string) bool {
	for _, root := range e.roots {
		if containsPath(root, value, e.style == "windows") {
			return true
		}
	}
	return false
}

func containsPath(root, value string, windows bool) bool {
	if windows {
		root, value = strings.ToLower(strings.TrimRight(strings.ReplaceAll(root, "/", "\\"), "\\")), strings.ToLower(strings.TrimRight(strings.ReplaceAll(value, "/", "\\"), "\\"))
		return value == root || strings.HasPrefix(value, root+"\\")
	}
	root, value = filepath.Clean(root), filepath.Clean(value)
	if root == string(filepath.Separator) {
		return strings.HasPrefix(value, root)
	}
	return value == root || strings.HasPrefix(value, root+string(filepath.Separator))
}
func containsParent(value, separator string) bool {
	for _, part := range strings.Split(value, separator) {
		if part == ".." {
			return true
		}
	}
	return false
}
func isLetter(value byte) bool { return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' }
