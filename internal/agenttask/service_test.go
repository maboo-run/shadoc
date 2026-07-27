package agenttask

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestServiceWaitsForAgentCompletionAndFinishesRun(t *testing.T) {
	now := time.Now().UTC()
	result, _ := json.Marshal(agentprotocol.Result{Version: 1, AssignmentID: "lease", AgentID: "agent-1", Status: "succeeded", SnapshotID: "snapshot-1"})
	storage := &fakeStorage{tasks: []domain.Task{{ID: "task-1", Engine: domain.ResticEngine, ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"}, ScopeConfirmation: domain.TaskScopeConfirmation{PreviewID: "preview-1", Fingerprint: "fingerprint-1", ConfirmedBy: "admin", ConfirmedAt: now, Summary: map[string]any{"includedFiles": 3}}}}, completed: result, now: now}
	service := New(storage, func() time.Time { return now })
	service.poll = time.Millisecond
	record, err := service.Run(context.Background(), "task-1", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "success" || record.SnapshotID != "snapshot-1" || storage.finished != "success" || storage.lease.AgentID != "agent-1" {
		t.Fatalf("record=%+v storage=%+v", record, storage)
	}
	confirmation, ok := storage.summary["scopeConfirmation"].(domain.TaskScopeConfirmation)
	if !ok || confirmation.Fingerprint != "fingerprint-1" {
		t.Fatalf("summary=%+v", storage.summary)
	}
}

func TestServicePersistsAgentPartialWithoutTreatingItAsFailure(t *testing.T) {
	now := time.Now().UTC()
	result, _ := json.Marshal(agentprotocol.Result{
		Version: 1, AssignmentID: "lease", AgentID: "agent-1", Status: "partial", SnapshotID: "partial-1",
		Summary: map[string]any{"filesExpected": int64(5), "filesProcessed": int64(4), "partialSnapshotProtected": true},
	})
	storage := &fakeStorage{
		tasks: []domain.Task{{
			ID: "task-1", Engine: domain.ResticEngine, RepositoryID: "repo-1",
			ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"},
		}},
		completed: result, now: now,
	}
	service := New(storage, func() time.Time { return now })
	service.poll = time.Millisecond
	record, err := service.Run(t.Context(), "task-1", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "partial" || storage.finished != "partial" || storage.summary["filesExpected"] != float64(5) || storage.summary["filesProcessed"] != float64(4) {
		t.Fatalf("record=%+v storage=%+v", record, storage)
	}
}

func TestServiceBlocksRepositoryUntilAgentCanProtectPartialSnapshot(t *testing.T) {
	now := time.Now().UTC()
	result, _ := json.Marshal(agentprotocol.Result{
		Version: 1, AssignmentID: "lease", AgentID: "agent-1", Status: "failed", SnapshotID: "partial-1",
		Error: "protect partial snapshot", Summary: map[string]any{"unprotectedPartialSnapshot": "partial-1"},
	})
	storage := &fakeStorage{
		tasks: []domain.Task{{
			ID: "task-1", Engine: domain.ResticEngine, RepositoryID: "repo-1",
			ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"},
		}},
		completed: result, now: now,
	}
	service := New(storage, func() time.Time { return now })
	service.poll = time.Millisecond
	if _, err := service.Run(t.Context(), "task-1", "", "manual"); err == nil {
		t.Fatal("protection failure was not returned")
	}
	if storage.repositoryID != "repo-1" || storage.repositoryStatus != "unprotected-partial:partial-1" {
		t.Fatalf("repository update=%q %q", storage.repositoryID, storage.repositoryStatus)
	}
}

type fakeStorage struct {
	tasks            []domain.Task
	lease            store.AgentLease
	completed        json.RawMessage
	now              time.Time
	finished         string
	summary          map[string]any
	repositoryID     string
	repositoryStatus string
}

func (s *fakeStorage) ListTasks(context.Context) ([]domain.Task, error) { return s.tasks, nil }
func (s *fakeStorage) CreateAgentLease(_ context.Context, lease store.AgentLease) error {
	s.lease = lease
	return nil
}
func (s *fakeStorage) AgentLeaseStatus(context.Context, string) (store.AgentLease, error) {
	completed := s.now
	s.lease.CompletedAt, s.lease.Result = &completed, s.completed
	return s.lease, nil
}
func (*fakeStorage) ExpireAgentLease(context.Context, string, string, time.Time) error { return nil }
func (*fakeStorage) StartRun(context.Context, store.RunRecord) error                   { return nil }
func (s *fakeStorage) UpdateRepositoryStatus(_ context.Context, id, status string) error {
	s.repositoryID, s.repositoryStatus = id, status
	return nil
}
func (s *fakeStorage) FinishRun(_ context.Context, _ string, status string, _ time.Time, _ int, _ string, summary map[string]any, _ string) error {
	s.finished = status
	s.summary = summary
	return nil
}
