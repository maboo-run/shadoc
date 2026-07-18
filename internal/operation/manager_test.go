package operation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
)

type cleanupError struct{ error }

func (cleanupError) CleanupIsRequired() bool { return true }

type residualError struct {
	error
	path string
}

func (e residualError) RestoreResidualPath() string { return e.path }

func newOperationManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sequence := 0
	manager, err := New(s, context.Background(), func() time.Time {
		return time.Date(2026, 7, 12, 10, 0, sequence, 0, time.UTC)
	}, func() string {
		sequence++
		return "op-test"
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	return manager, s
}

func TestManagerPersistsQueuedBeforeWorkAndCompletesStages(t *testing.T) {
	manager, persistence := newOperationManager(t)
	workSawPersisted := false
	record, err := manager.Start(StartRequest{Kind: "initialize", Actor: "admin", RepositoryID: "repo"}, func(_ context.Context, reporter Reporter) error {
		persisted, readErr := persistence.Operation(context.Background(), "op-test")
		workSawPersisted = readErr == nil && persisted.Status == "running"
		if err := reporter.Stage("initializing", map[string]any{"repositoryName": "photos"}); err != nil {
			return err
		}
		return nil
	})
	if err != nil || record.ID != "op-test" || record.Status != "queued" {
		t.Fatalf("start record=%+v err=%v", record, err)
	}
	finished, err := manager.Wait(context.Background(), record.ID)
	if err != nil || !workSawPersisted || finished.Status != "success" || finished.Stage != "completed" || finished.Detail["repositoryName"] != "photos" {
		t.Fatalf("finished=%+v persistedBeforeWork=%v err=%v", finished, workSawPersisted, err)
	}
	audits, err := persistence.ListAudits(context.Background(), 10)
	if err != nil || len(audits) != 2 || audits[0].Action != "operation.finish" || audits[0].Actor != "admin" || audits[1].Action != "operation.start" {
		t.Fatalf("operation audits=%+v err=%v", audits, err)
	}
}

func TestManagerClassifiesCleanupAndResidualFailures(t *testing.T) {
	manager, _ := newOperationManager(t)
	record, err := manager.Start(StartRequest{Kind: "directory_restore", Actor: "admin"}, func(context.Context, Reporter) error {
		return errors.Join(cleanupError{errors.New("target polluted")}, residualError{error: errors.New("partial files"), path: "/tmp/residual"})
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := manager.Wait(context.Background(), record.ID)
	if err != nil || finished.Status != "cleanup_required" || finished.Stage != "cleanup" || finished.Detail["residualPath"] != "/tmp/residual" {
		t.Fatalf("cleanup operation=%+v err=%v", finished, err)
	}
}

func TestManagerCancelsActiveWorkIdempotently(t *testing.T) {
	manager, _ := newOperationManager(t)
	started := make(chan struct{})
	record, err := manager.Start(StartRequest{Kind: "directory_restore", Actor: "admin"}, func(ctx context.Context, _ Reporter) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := manager.Cancel(record.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Cancel(record.ID); err != nil {
		t.Fatalf("duplicate cancel: %v", err)
	}
	finished, err := manager.Wait(context.Background(), record.ID)
	if err != nil || finished.Status != "cancelled" {
		t.Fatalf("cancelled operation=%+v err=%v", finished, err)
	}
}

func TestManagerRecoversPanicsAsFailedOperations(t *testing.T) {
	manager, _ := newOperationManager(t)
	record, err := manager.Start(StartRequest{Kind: "initialize", Actor: "admin"}, func(context.Context, Reporter) error {
		panic("boom")
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := manager.Wait(context.Background(), record.ID)
	if err != nil || finished.Status != "failed" || finished.ErrorSummary == "" {
		t.Fatalf("panic operation=%+v err=%v", finished, err)
	}
}

func TestManagerStartUniqueReturnsExistingOperationInsteadOfDuplicatingWork(t *testing.T) {
	manager, _ := newOperationManager(t)
	started := make(chan struct{})
	release := make(chan struct{})
	first, existing, err := manager.StartUnique("repository:repo:rotate", StartRequest{Kind: "repository_password_rotation", Actor: "admin", RepositoryID: "repo"}, func(context.Context, Reporter) error {
		close(started)
		<-release
		return nil
	})
	if err != nil || existing {
		t.Fatalf("first=%+v existing=%v err=%v", first, existing, err)
	}
	<-started
	second, existing, err := manager.StartUnique("repository:repo:rotate", StartRequest{Kind: "repository_password_rotation", Actor: "admin", RepositoryID: "repo"}, func(context.Context, Reporter) error {
		t.Fatal("duplicate work executed")
		return nil
	})
	if err != nil || !existing || second.ID != first.ID {
		t.Fatalf("second=%+v existing=%v err=%v", second, existing, err)
	}
	close(release)
	if _, err := manager.Wait(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
}
