package resticagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/restic"
)

type Definition struct {
	Repository restic.Repository      `json:"repository"`
	Directory  restic.DirectoryBackup `json:"directory"`
	Arguments  []string               `json:"arguments,omitempty"`
}

type Runner interface {
	Execute(context.Context, restic.Operation) (restic.Result, error)
}

type Engine struct{ runner Runner }

func New(runner Runner) *Engine            { return &Engine{runner: runner} }
func (*Engine) Kind() execution.EngineKind { return "restic" }

func (*Engine) Validate(raw json.RawMessage) error {
	definition, err := decode(raw)
	if err != nil {
		return err
	}
	if definition.Repository.Location == "" || definition.Repository.Password == "" || definition.Directory.Path == "" {
		return errors.New("restic agent definition is incomplete")
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
	result, err := e.runner.Execute(ctx, restic.Operation{Kind: restic.BackupDirectory, Repository: definition.Repository, Directory: &definition.Directory, Arguments: definition.Arguments})
	rawLog := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	for _, secret := range []string{definition.Repository.Password, string(definition.Repository.SSHPrivateKey), definition.Repository.S3AccessKey, definition.Repository.S3SecretKey} {
		if secret != "" {
			rawLog = strings.ReplaceAll(rawLog, secret, "[redacted]")
		}
	}
	summary := make(map[string]any, len(result.Summary)+2)
	for key, value := range result.Summary {
		summary[key] = value
	}
	summary["exitCode"], summary["outcome"] = result.ExitCode, result.Outcome
	outcome := execution.Outcome{Status: "succeeded", SnapshotID: result.SnapshotID, RawLog: rawLog, Summary: summary}
	if err != nil || result.Outcome == restic.Failure {
		outcome.Status = "failed"
	}
	return outcome, err
}

func decode(raw json.RawMessage) (Definition, error) {
	var definition Definition
	if !json.Valid(raw) || json.Unmarshal(raw, &definition) != nil {
		return definition, errors.New("valid restic agent definition is required")
	}
	return definition, nil
}
