package restore

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/database"
)

type fakeConnector struct{ prepared database.PreparedRestore }

func (f fakeConnector) PrepareExport(context.Context, database.Connection, string) (database.PreparedCommand, database.SnapshotMetadata, error) {
	return database.PreparedCommand{}, database.SnapshotMetadata{}, errors.New("unused")
}
func (f fakeConnector) PrepareRestore(context.Context, database.Connection, string, string) (database.PreparedRestore, error) {
	return f.prepared, nil
}

type restoreExecutor struct {
	exists       command.Result
	inspect      command.Result
	importResult command.Result
	importError  error
	cleanupCheck command.Result
	dropResult   command.Result
	dropError    error
	imported     string
	calls        []string
}

func (e *restoreExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	e.calls = append(e.calls, spec.Program)
	switch spec.Program {
	case "exists":
		return e.exists, nil
	case "inspect":
		return e.inspect, nil
	case "create":
		return command.Result{ExitCode: 0}, nil
	case "mark":
		return command.Result{ExitCode: 0}, nil
	case "cleanup-check":
		return e.cleanupCheck, nil
	case "drop":
		return e.dropResult, e.dropError
	case "unmark":
		return command.Result{ExitCode: 0}, nil
	case "import":
		value, _ := io.ReadAll(spec.Stdin)
		e.imported = string(value)
		if e.importResult.ExitCode != 0 || e.importError != nil {
			return e.importResult, e.importError
		}
		return command.Result{ExitCode: 0}, nil
	default:
		return command.Result{ExitCode: -1}, errors.New("unexpected command")
	}
}

func TestServicePreservesImporterStderrWhenProcessReturnsExitError(t *testing.T) {
	executor := &restoreExecutor{
		exists:       command.Result{ExitCode: 0, Stdout: "0\n"},
		importResult: command.Result{ExitCode: 1, Stderr: "pg_restore: incompatible locale"},
		importError:  errors.New("exit status 1"),
	}
	service := New(executor, fakeDumper{value: "archive"})
	err := service.Restore(context.Background(), Request{Connector: fakeConnector{prepared: guardedPreparedRestore(executor.exists)}, SnapshotID: "snapshot", Filename: "database.dump"})
	if err == nil || !strings.Contains(err.Error(), "incompatible locale") || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("restore error = %v", err)
	}
}

func TestDatabasePreflightIsReadOnlyAndReportsCreateOrEmptyBehavior(t *testing.T) {
	missing := &restoreExecutor{exists: command.Result{ExitCode: 0, Stdout: "0\n"}}
	service := New(missing, fakeDumper{})
	result, err := service.Preflight(context.Background(), Request{Connector: fakeConnector{prepared: guardedPreparedRestore(missing.exists)}, SnapshotID: "snapshot", Filename: "database.dump"})
	if err != nil || result.TargetExists || result.Behavior != "create" || strings.Join(missing.calls, ",") != "exists" {
		t.Fatalf("missing result=%+v calls=%v err=%v", result, missing.calls, err)
	}
	existing := &restoreExecutor{exists: command.Result{ExitCode: 0, Stdout: "1\n"}, inspect: command.Result{ExitCode: 0, Stdout: "0\n"}}
	result, err = New(existing, fakeDumper{}).Preflight(context.Background(), Request{Connector: fakeConnector{prepared: guardedPreparedRestore(existing.exists)}, SnapshotID: "snapshot", Filename: "database.dump"})
	if err != nil || !result.TargetExists || result.Behavior != "restore_empty" || strings.Join(existing.calls, ",") != "exists,inspect" {
		t.Fatalf("existing result=%+v calls=%v err=%v", result, existing.calls, err)
	}
}

type fakeDumper struct {
	value string
	err   error
}

func (d fakeDumper) Dump(_ context.Context, _, _ string, output io.Writer) error {
	if _, err := io.Copy(output, strings.NewReader(d.value)); err != nil {
		return err
	}
	return d.err
}

type databaseRestoreFailure interface {
	error
	RestoreStage() string
	RestoreTargetWasCreated() bool
	RestoreTargetWasCleaned() bool
	CleanupIsRequired() bool
}

func guardedPreparedRestore(exists command.Result) database.PreparedRestore {
	return database.PreparedRestore{
		Exists: command.Spec{Program: "exists"}, Inspect: command.Spec{Program: "inspect"}, Create: command.Spec{Program: "create"},
		MarkCreated: command.Spec{Program: "mark"}, Import: command.Spec{Program: "import"}, CleanupCheck: command.Spec{Program: "cleanup-check"},
		DropCreated: command.Spec{Program: "drop"}, UnmarkCreated: command.Spec{Program: "unmark"},
		ExistsOutput: func(output string) bool { return strings.TrimSpace(output) == "1" }, MissingOutput: func(output string) bool { return strings.TrimSpace(output) == "0" },
		EmptyOutput: func(output string) bool { return strings.TrimSpace(output) == "0" }, CleanupAllowed: func(output string) bool { return strings.TrimSpace(output) == "1" }, Cleanup: func() {},
	}
}

func TestServiceNewDatabaseFailureDropsGuardedTarget(t *testing.T) {
	executor := &restoreExecutor{
		exists:       command.Result{ExitCode: 0, Stdout: "0\n"},
		importResult: command.Result{ExitCode: 1, Stderr: "import failed"},
		cleanupCheck: command.Result{ExitCode: 0, Stdout: "1\n"},
		dropResult:   command.Result{ExitCode: 0},
	}
	service := New(executor, fakeDumper{value: "partial archive"})
	err := service.Restore(context.Background(), Request{Connector: fakeConnector{prepared: guardedPreparedRestore(executor.exists)}, SnapshotID: "snap", Filename: "db.sql"})

	var restoreErr databaseRestoreFailure
	if !errors.As(err, &restoreErr) {
		t.Fatalf("restore error does not expose cleanup state: %v", err)
	}
	if !restoreErr.RestoreTargetWasCreated() || !restoreErr.RestoreTargetWasCleaned() || restoreErr.CleanupIsRequired() {
		t.Fatalf("created=%v cleaned=%v cleanupRequired=%v", restoreErr.RestoreTargetWasCreated(), restoreErr.RestoreTargetWasCleaned(), restoreErr.CleanupIsRequired())
	}
	if calls := strings.Join(executor.calls, ","); !strings.Contains(calls, "create,mark,import,cleanup-check,drop") {
		t.Fatalf("cleanup calls=%s", calls)
	}
}

func TestServiceNewDatabaseFailureRequiresCleanupWhenGuardRefusesDrop(t *testing.T) {
	executor := &restoreExecutor{
		exists:       command.Result{ExitCode: 0, Stdout: "0\n"},
		importResult: command.Result{ExitCode: 1, Stderr: "import failed"},
		cleanupCheck: command.Result{ExitCode: 0, Stdout: "0\n"},
	}
	service := New(executor, fakeDumper{value: "partial archive"})
	err := service.Restore(context.Background(), Request{Connector: fakeConnector{prepared: guardedPreparedRestore(executor.exists)}, SnapshotID: "snap", Filename: "db.sql"})

	var restoreErr databaseRestoreFailure
	if !errors.As(err, &restoreErr) || !restoreErr.CleanupIsRequired() || restoreErr.RestoreTargetWasCleaned() {
		t.Fatalf("unsafe cleanup classification: %v", err)
	}
	if strings.Contains(strings.Join(executor.calls, ","), "drop") {
		t.Fatalf("guarded cleanup still dropped target: %v", executor.calls)
	}
}

func TestServiceNewDatabaseSnapshotReadFailureDropsGuardedTarget(t *testing.T) {
	executor := &restoreExecutor{
		exists:       command.Result{ExitCode: 0, Stdout: "0\n"},
		cleanupCheck: command.Result{ExitCode: 0, Stdout: "1\n"},
		dropResult:   command.Result{ExitCode: 0},
	}
	service := New(executor, fakeDumper{value: "partial archive", err: errors.New("restic read failed")})
	err := service.Restore(context.Background(), Request{Connector: fakeConnector{prepared: guardedPreparedRestore(executor.exists)}, SnapshotID: "snap", Filename: "db.sql"})

	var restoreErr databaseRestoreFailure
	if !errors.As(err, &restoreErr) || restoreErr.RestoreStage() != "snapshot-read" || !restoreErr.RestoreTargetWasCleaned() {
		t.Fatalf("snapshot-read cleanup classification: %v", err)
	}
}

func TestServiceNewDatabaseFailureRequiresCleanupWhenDropFails(t *testing.T) {
	executor := &restoreExecutor{
		exists:       command.Result{ExitCode: 0, Stdout: "0\n"},
		importResult: command.Result{ExitCode: 1, Stderr: "import failed"},
		cleanupCheck: command.Result{ExitCode: 0, Stdout: "1\n"},
		dropResult:   command.Result{ExitCode: 1, Stderr: "database is in use"},
		dropError:    errors.New("exit status 1"),
	}
	service := New(executor, fakeDumper{value: "partial archive"})
	err := service.Restore(context.Background(), Request{Connector: fakeConnector{prepared: guardedPreparedRestore(executor.exists)}, SnapshotID: "snap", Filename: "db.sql"})

	var restoreErr databaseRestoreFailure
	if !errors.As(err, &restoreErr) || !restoreErr.CleanupIsRequired() || restoreErr.RestoreTargetWasCleaned() || !strings.Contains(err.Error(), "database is in use") {
		t.Fatalf("drop failure cleanup classification: %v", err)
	}
}

func TestServiceExistingEmptyDatabaseFailureRequiresManualCleanup(t *testing.T) {
	executor := &restoreExecutor{
		exists:       command.Result{ExitCode: 0, Stdout: "1\n"},
		inspect:      command.Result{ExitCode: 0, Stdout: "0\n"},
		importResult: command.Result{ExitCode: 1, Stderr: "import failed"},
	}
	service := New(executor, fakeDumper{value: "partial archive"})
	err := service.Restore(context.Background(), Request{Connector: fakeConnector{prepared: guardedPreparedRestore(executor.exists)}, SnapshotID: "snap", Filename: "db.sql"})

	var restoreErr databaseRestoreFailure
	if !errors.As(err, &restoreErr) || restoreErr.RestoreTargetWasCreated() || !restoreErr.CleanupIsRequired() {
		t.Fatalf("existing target cleanup classification: %v", err)
	}
	calls := strings.Join(executor.calls, ",")
	if strings.Contains(calls, "mark") || strings.Contains(calls, "drop") {
		t.Fatalf("existing target was treated as operation-created: %s", calls)
	}
}

func TestServiceStreamsSnapshotIntoNewOrEmptyDatabase(t *testing.T) {
	for _, exists := range []command.Result{{ExitCode: 0, Stdout: "1\n"}, {ExitCode: 0, Stdout: "0\n"}} {
		executor := &restoreExecutor{exists: exists, inspect: command.Result{ExitCode: 0, Stdout: "0\n"}}
		service := New(executor, fakeDumper{value: "database archive"})
		err := service.Restore(context.Background(), Request{
			Connector: fakeConnector{prepared: guardedPreparedRestore(exists)}, Connection: database.Connection{}, Database: "gitea", Format: "sql", SnapshotID: "abc", Filename: "gitea.sql",
		})
		if err != nil {
			t.Fatalf("restore: %v", err)
		}
		if executor.imported != "database archive" {
			t.Fatalf("imported %q", executor.imported)
		}
		if strings.TrimSpace(exists.Stdout) == "0" && !strings.Contains(strings.Join(executor.calls, ","), "create") {
			t.Fatal("missing database was not created")
		}
	}
}

func TestServiceRefusesNonEmptyDatabaseBeforeReadingSnapshot(t *testing.T) {
	executor := &restoreExecutor{exists: command.Result{ExitCode: 0, Stdout: "1\n"}, inspect: command.Result{ExitCode: 0, Stdout: "4\n"}}
	service := New(executor, fakeDumper{value: "must-not-import"})
	err := service.Restore(context.Background(), Request{Connector: fakeConnector{prepared: database.PreparedRestore{
		Exists: command.Spec{Program: "exists"}, Inspect: command.Spec{Program: "inspect"}, Create: command.Spec{Program: "create"}, Import: command.Spec{Program: "import"}, ExistsOutput: func(string) bool { return true }, MissingOutput: func(string) bool { return false }, EmptyOutput: func(string) bool { return false }, Cleanup: func() {},
	}}, SnapshotID: "abc", Filename: "db.sql"})
	if !errors.Is(err, ErrTargetNotEmpty) || executor.imported != "" {
		t.Fatalf("unsafe restore result: err=%v imported=%q", err, executor.imported)
	}
}

func TestServiceBlocksUnknownExistenceInsteadOfAttemptingCreate(t *testing.T) {
	executor := &restoreExecutor{exists: command.Result{ExitCode: 1, Stderr: "network failure"}}
	service := New(executor, fakeDumper{value: "archive"})
	err := service.Restore(context.Background(), Request{Connector: fakeConnector{prepared: database.PreparedRestore{Exists: command.Spec{Program: "exists"}, Create: command.Spec{Program: "create"}, Import: command.Spec{Program: "import"}, ExistsOutput: func(string) bool { return false }, MissingOutput: func(string) bool { return false }, Cleanup: func() {}}}, SnapshotID: "snap", Filename: "db.sql"})
	if err == nil || strings.Contains(strings.Join(executor.calls, ","), "create") {
		t.Fatalf("unsafe unknown-state restore: err=%v calls=%v", err, executor.calls)
	}
}

func TestServiceBlocksUnexpectedExistenceOutput(t *testing.T) {
	executor := &restoreExecutor{exists: command.Result{ExitCode: 0, Stdout: "2\n"}}
	service := New(executor, fakeDumper{value: "archive"})
	err := service.Restore(context.Background(), Request{Connector: fakeConnector{prepared: database.PreparedRestore{
		Exists: command.Spec{Program: "exists"}, Create: command.Spec{Program: "create"}, Import: command.Spec{Program: "import"},
		ExistsOutput: func(output string) bool { return strings.TrimSpace(output) == "1" }, MissingOutput: func(output string) bool { return strings.TrimSpace(output) == "0" }, Cleanup: func() {},
	}}, SnapshotID: "snap", Filename: "db.sql"})
	if err == nil || strings.Contains(strings.Join(executor.calls, ","), "create") {
		t.Fatalf("unexpected existence output was not blocked: err=%v calls=%v", err, executor.calls)
	}
}
