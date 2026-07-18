package restore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/database"
)

var ErrTargetNotEmpty = errors.New("restore target database is not empty")

type DatabaseRestoreError struct {
	Stage           string
	TargetCreated   bool
	TargetCleaned   bool
	CleanupRequired bool
	Err             error
}

func (e *DatabaseRestoreError) Error() string {
	state := ""
	if e.TargetCleaned {
		state = "; operation-created target was cleaned"
	} else if e.CleanupRequired {
		state = "; target requires cleanup before retry"
	}
	return fmt.Sprintf("database restore %s failed%s: %v", e.Stage, state, e.Err)
}

func (e *DatabaseRestoreError) Unwrap() error { return e.Err }

func (e *DatabaseRestoreError) RestoreStage() string { return e.Stage }

func (e *DatabaseRestoreError) RestoreTargetWasCreated() bool { return e.TargetCreated }

func (e *DatabaseRestoreError) RestoreTargetWasCleaned() bool { return e.TargetCleaned }

func (e *DatabaseRestoreError) CleanupIsRequired() bool { return e.CleanupRequired }

type Dumper interface {
	Dump(context.Context, string, string, io.Writer) error
}

type Request struct {
	Connector  database.RestoreConnector
	Connection database.Connection
	Database   string
	Format     string
	SnapshotID string
	Filename   string
}

type Service struct {
	executor command.Executor
	dumper   Dumper
}

type PreflightResult struct {
	TargetExists bool   `json:"targetExists"`
	Behavior     string `json:"behavior"`
}

func New(executor command.Executor, dumper Dumper) *Service {
	return &Service{executor: executor, dumper: dumper}
}

func (s *Service) Preflight(ctx context.Context, request Request) (PreflightResult, error) {
	if request.Connector == nil || s.executor == nil || request.SnapshotID == "" || request.Filename == "" {
		return PreflightResult{}, errors.New("complete restore request is required")
	}
	prepared, err := request.Connector.PrepareRestore(ctx, request.Connection, request.Database, request.Format)
	if err != nil {
		return PreflightResult{}, err
	}
	defer prepared.Cleanup()
	exists, existsErr := s.executor.Run(ctx, prepared.Exists)
	if existsErr != nil || exists.ExitCode != 0 {
		return PreflightResult{}, fmt.Errorf("inspect restore database existence: %w", commandFailure(existsErr, exists))
	}
	if prepared.ExistsOutput(exists.Stdout) {
		inspected, inspectErr := s.executor.Run(ctx, prepared.Inspect)
		if inspectErr != nil || inspected.ExitCode != 0 {
			return PreflightResult{}, fmt.Errorf("inspect restore database contents: %w", commandFailure(inspectErr, inspected))
		}
		if !prepared.EmptyOutput(inspected.Stdout) {
			return PreflightResult{}, ErrTargetNotEmpty
		}
		return PreflightResult{TargetExists: true, Behavior: "restore_empty"}, nil
	}
	if prepared.MissingOutput != nil && prepared.MissingOutput(exists.Stdout) {
		return PreflightResult{Behavior: "create"}, nil
	}
	return PreflightResult{}, errors.New("restore database existence result is unknown")
}

func (s *Service) Restore(ctx context.Context, request Request) error {
	if request.Connector == nil || s.executor == nil || s.dumper == nil || request.SnapshotID == "" || request.Filename == "" {
		return errors.New("complete restore request is required")
	}
	prepared, err := request.Connector.PrepareRestore(ctx, request.Connection, request.Database, request.Format)
	if err != nil {
		return err
	}
	defer prepared.Cleanup()
	exists, existsErr := s.executor.Run(ctx, prepared.Exists)
	if existsErr != nil || exists.ExitCode != 0 {
		return fmt.Errorf("inspect restore database existence: %w", commandFailure(existsErr, exists))
	}
	createdTarget := false
	if prepared.ExistsOutput(exists.Stdout) {
		inspected, inspectErr := s.executor.Run(ctx, prepared.Inspect)
		if inspectErr != nil || inspected.ExitCode != 0 {
			return fmt.Errorf("inspect restore database contents: %w", commandFailure(inspectErr, inspected))
		}
		if !prepared.EmptyOutput(inspected.Stdout) {
			return ErrTargetNotEmpty
		}
	} else if prepared.MissingOutput != nil && prepared.MissingOutput(exists.Stdout) {
		created, createErr := s.executor.Run(ctx, prepared.Create)
		if createErr != nil || created.ExitCode != 0 {
			return fmt.Errorf("create restore database: %w", commandFailure(createErr, created))
		}
		createdTarget = true
		marked, markErr := s.executor.Run(ctx, prepared.MarkCreated)
		if markErr != nil || marked.ExitCode != 0 {
			return &DatabaseRestoreError{Stage: "mark", TargetCreated: true, CleanupRequired: true, Err: commandFailure(markErr, marked)}
		}
	} else {
		return errors.New("restore database existence result is unknown")
	}
	reader, writer := io.Pipe()
	importSpec := prepared.Import
	importSpec.Stdin = reader
	importDone := make(chan error, 1)
	go func() {
		result, runErr := s.executor.Run(ctx, importSpec)
		if result.ExitCode != 0 {
			stderrErr := error(nil)
			if result.Stderr != "" {
				stderrErr = errors.New(result.Stderr)
			}
			if runErr == nil && stderrErr == nil {
				runErr = errors.New("database import failed")
			} else {
				runErr = errors.Join(runErr, stderrErr)
			}
		}
		_ = reader.CloseWithError(runErr)
		importDone <- runErr
	}()
	dumpErr := s.dumper.Dump(ctx, request.SnapshotID, request.Filename, writer)
	_ = writer.CloseWithError(dumpErr)
	importErr := <-importDone
	if dumpErr != nil {
		return s.failedRestore(ctx, prepared, "snapshot-read", createdTarget, dumpErr)
	}
	if importErr != nil {
		return s.failedRestore(ctx, prepared, "import", createdTarget, importErr)
	}
	if createdTarget {
		unmarked, unmarkErr := s.executor.Run(ctx, prepared.UnmarkCreated)
		if unmarkErr != nil || unmarked.ExitCode != 0 {
			return &DatabaseRestoreError{Stage: "unmark", TargetCreated: true, CleanupRequired: true, Err: commandFailure(unmarkErr, unmarked)}
		}
	}
	return nil
}

func (s *Service) failedRestore(ctx context.Context, prepared database.PreparedRestore, stage string, createdTarget bool, restoreErr error) error {
	if !createdTarget {
		return &DatabaseRestoreError{Stage: stage, CleanupRequired: true, Err: restoreErr}
	}
	checked, checkErr := s.executor.Run(ctx, prepared.CleanupCheck)
	if checkErr != nil || checked.ExitCode != 0 {
		return &DatabaseRestoreError{Stage: stage, TargetCreated: true, CleanupRequired: true, Err: errors.Join(restoreErr, fmt.Errorf("verify automatic cleanup safety: %w", commandFailure(checkErr, checked)))}
	}
	if prepared.CleanupAllowed == nil || !prepared.CleanupAllowed(checked.Stdout) {
		return &DatabaseRestoreError{Stage: stage, TargetCreated: true, CleanupRequired: true, Err: errors.Join(restoreErr, errors.New("automatic cleanup guard did not prove ownership and inactivity"))}
	}
	dropped, dropErr := s.executor.Run(ctx, prepared.DropCreated)
	if dropErr != nil || dropped.ExitCode != 0 {
		return &DatabaseRestoreError{Stage: stage, TargetCreated: true, CleanupRequired: true, Err: errors.Join(restoreErr, fmt.Errorf("drop operation-created target: %w", commandFailure(dropErr, dropped)))}
	}
	return &DatabaseRestoreError{Stage: stage, TargetCreated: true, TargetCleaned: true, Err: restoreErr}
}

func commandFailure(runErr error, result command.Result) error {
	var stderrErr error
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		stderrErr = errors.New(stderr)
	}
	if combined := errors.Join(runErr, stderrErr); combined != nil {
		return combined
	}
	return errors.New("operation failed")
}
