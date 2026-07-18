package store

import (
	"context"
	"testing"
	"time"
)

func TestOperationRecordsPersistTransitionsAndFilters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	created := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	record := OperationRecord{
		ID: "op-1", Kind: "directory_restore", Actor: "admin", RepositoryID: "repo", SnapshotID: "snap",
		Target: "/backup/restore", Status: "queued", Stage: "queued", CreatedAt: created, Detail: map[string]any{"mode": "new-target"},
	}
	if err := s.CreateOperation(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := s.Operation(ctx, record.ID)
	if err != nil || got.Status != "queued" || got.Actor != "admin" || got.Detail["mode"] != "new-target" {
		t.Fatalf("operation=%+v err=%v", got, err)
	}
	started := created.Add(time.Second)
	if err := s.StartOperation(ctx, record.ID, "preflight", started); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateOperationStage(ctx, record.ID, "restoring", map[string]any{"files": 3}); err != nil {
		t.Fatal(err)
	}
	finished := started.Add(2 * time.Second)
	if err := s.FinishOperation(ctx, record.ID, "cleanup_required", "cleanup", finished, "target requires cleanup", map[string]any{"residualPath": "/tmp/residual"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Operation(ctx, record.ID)
	if err != nil || got.Status != "cleanup_required" || got.Stage != "cleanup" || got.StartedAt == nil || got.FinishedAt == nil || got.ErrorSummary == "" || got.Detail["residualPath"] != "/tmp/residual" {
		t.Fatalf("finished operation=%+v err=%v", got, err)
	}
	items, err := s.ListOperations(ctx, 10, "directory_restore", "cleanup_required")
	if err != nil || len(items) != 1 || items[0].ID != record.ID {
		t.Fatalf("operations=%+v err=%v", items, err)
	}
}

func TestRecoverInterruptedOperationsFailsQueuedAndRunning(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	created := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	for _, item := range []OperationRecord{
		{ID: "queued", Kind: "initialize", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: created},
		{ID: "running", Kind: "directory_restore", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: created},
		{ID: "success", Kind: "initialize", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: created},
		{ID: "update-handoff", Kind: "application_update", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: created},
	} {
		if err := s.CreateOperation(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.StartOperation(ctx, "running", "restoring", created.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.StartOperation(ctx, "update-handoff", "rollback_verified", created.Add(59*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishOperation(ctx, "success", "success", "completed", created.Add(time.Second), "", nil); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.RecoverInterruptedOperations(ctx, created.Add(time.Hour))
	if err != nil || recovered != 2 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	for _, id := range []string{"queued", "running"} {
		operation, err := s.Operation(ctx, id)
		if err != nil || operation.Status != "failed" || operation.Stage != "interrupted" || operation.FinishedAt == nil {
			t.Fatalf("recovered operation=%+v err=%v", operation, err)
		}
	}
	success, _ := s.Operation(ctx, "success")
	if success.Status != "success" {
		t.Fatalf("completed operation changed: %+v", success)
	}
	handoff, _ := s.Operation(ctx, "update-handoff")
	if handoff.Status != "running" || handoff.Stage != "rollback_verified" {
		t.Fatalf("active updater handoff was interrupted: %+v", handoff)
	}
	recovered, err = s.RecoverInterruptedOperations(ctx, created.Add(70*time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("stale updater recovered=%d err=%v", recovered, err)
	}
}

func TestActiveApplicationUpdateReturnsOnlyQueuedOrRunningUpdate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	created := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	if err := s.CreateOperation(ctx, OperationRecord{ID: "completed-update", Kind: "application_update", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishOperation(ctx, "completed-update", "success", "completed", created.Add(time.Second), "", nil); err != nil {
		t.Fatal(err)
	}
	for _, record := range []OperationRecord{
		{ID: "active-update", Kind: "application_update", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: created.Add(time.Minute)},
		{ID: "other-operation", Kind: "directory_restore", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: created.Add(2 * time.Minute)},
	} {
		if err := s.CreateOperation(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	active, err := s.ActiveApplicationUpdate(ctx)
	if err != nil || active.ID != "active-update" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	if err := s.CreateOperation(ctx, OperationRecord{ID: "second-active-update", Kind: "application_update", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: created.Add(3 * time.Minute)}); err == nil {
		t.Fatal("store accepted a second active application update")
	}
}

func TestResolveOperationCleanupOnlyTransitionsCleanupRequired(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	created := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	for _, record := range []OperationRecord{
		{ID: "cleanup", Kind: "directory_restore", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: created, Detail: map[string]any{"residualPath": "/tmp/.target.restic-control-restore-123"}},
		{ID: "failed", Kind: "directory_restore", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: created},
	} {
		if err := s.CreateOperation(ctx, record); err != nil {
			t.Fatal(err)
		}
		if err := s.StartOperation(ctx, record.ID, "restoring", created.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		status := "cleanup_required"
		if record.ID == "failed" {
			status = "failed"
		}
		if err := s.FinishOperation(ctx, record.ID, status, "cleanup", created.Add(2*time.Second), "restore failed", nil); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.ResolveOperationCleanup(ctx, "cleanup", created.Add(3*time.Second), map[string]any{"cleanupResolution": "removed"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := s.Operation(ctx, "cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != "failed" || resolved.Stage != "cleanup_resolved" || resolved.Detail["cleanupResolution"] != "removed" {
		t.Fatalf("resolved operation=%+v", resolved)
	}
	if err := s.ResolveOperationCleanup(ctx, "failed", created.Add(3*time.Second), nil); err == nil {
		t.Fatal("expected transition conflict for operation without cleanup_required status")
	}
}
