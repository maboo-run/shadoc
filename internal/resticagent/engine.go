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
	Repository               restic.Repository      `json:"repository"`
	Directory                restic.DirectoryBackup `json:"directory"`
	Arguments                []string               `json:"arguments,omitempty"`
	PendingPartialSnapshotID string                 `json:"pendingPartialSnapshotId,omitempty"`
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
	if value := definition.PendingPartialSnapshotID; len(value) > 128 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n\t ") {
		return errors.New("pending partial snapshot id is invalid")
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
	summary := make(map[string]any)
	rawLogs := make([]string, 0, 3)
	if definition.PendingPartialSnapshotID != "" {
		protected, protectErr := e.runner.Execute(ctx, restic.Operation{
			Kind: restic.TagSnapshot, Repository: definition.Repository,
			Arguments: []string{"--add", "rc:protected-partial", definition.PendingPartialSnapshotID},
		})
		rawLogs = appendResultLog(rawLogs, protected)
		if protectErr != nil || protected.Outcome == restic.Failure {
			return execution.Outcome{
				Status: "failed", SnapshotID: definition.PendingPartialSnapshotID,
				RawLog: redactLog(strings.Join(rawLogs, "\n"), definition.Repository), Summary: summary,
			}, errors.New("protect pending partial snapshot")
		}
		summary["pendingPartialProtected"] = definition.PendingPartialSnapshotID
	}

	result, err := e.runner.Execute(ctx, restic.Operation{Kind: restic.BackupDirectory, Repository: definition.Repository, Directory: &definition.Directory, Arguments: definition.Arguments})
	rawLogs = appendResultLog(rawLogs, result)
	for key, value := range result.Summary {
		summary[key] = value
	}
	summary["exitCode"], summary["outcome"] = result.ExitCode, result.Outcome
	outcome := execution.Outcome{
		Status: "succeeded", SnapshotID: result.SnapshotID,
		RawLog: redactLog(strings.Join(rawLogs, "\n"), definition.Repository), Summary: summary,
	}
	if err != nil || result.Outcome == restic.Failure {
		outcome.Status = "failed"
	}
	if result.Outcome == restic.Partial {
		outcome.Status = "partial"
		if result.SnapshotID != "" {
			protected, protectErr := e.runner.Execute(ctx, restic.Operation{
				Kind: restic.TagSnapshot, Repository: definition.Repository,
				Arguments: []string{"--add", "rc:protected-partial", result.SnapshotID},
			})
			rawLogs = appendResultLog(rawLogs, protected)
			outcome.RawLog = redactLog(strings.Join(rawLogs, "\n"), definition.Repository)
			if protectErr != nil || protected.Outcome == restic.Failure {
				outcome.Status = "failed"
				outcome.Summary["unprotectedPartialSnapshot"] = result.SnapshotID
				return outcome, errors.New("protect partial snapshot")
			}
			outcome.Summary["partialSnapshotProtected"] = true
		}
	}
	return outcome, err
}

func appendResultLog(logs []string, result restic.Result) []string {
	if value := strings.TrimSpace(result.Stdout + "\n" + result.Stderr); value != "" {
		return append(logs, value)
	}
	return logs
}

func redactLog(value string, repository restic.Repository) string {
	value = strings.TrimSpace(value)
	for _, secret := range []string{repository.Password, string(repository.SSHPrivateKey), repository.S3AccessKey, repository.S3SecretKey} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

func decode(raw json.RawMessage) (Definition, error) {
	var definition Definition
	if !json.Valid(raw) || json.Unmarshal(raw, &definition) != nil {
		return definition, errors.New("valid restic agent definition is required")
	}
	return definition, nil
}
