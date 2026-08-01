package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
)

func TestOpenBackfillsLocalRepositoryOwnerFromExistingAgentTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	storage, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := storage.SaveAgent(t.Context(), AgentRecord{ID: "agent-a", CertificateSerial: "serial-a", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateRepository(t.Context(), domain.Repository{ID: "repo-a", Name: "legacy", Engine: domain.RsyncEngine, Kind: domain.LocalRepository, LocalTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-a"}, Path: "/archive", Status: "ready", CreatedAt: now, UpdatedAt: now}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.Exec(`UPDATE repositories SET local_target_json='' WHERE id='repo-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.Exec(`INSERT INTO tasks(id,name,engine,kind,execution_target_json,repository_id,source_json,retention_json,resources_json,health_policy_json,exclusions_json,scope_confirmation_json,enabled,created_at,updated_at) VALUES('task-a','legacy task','rsync','rsync','{"kind":"agent","agentId":"agent-a"}','repo-a','{"path":"/source"}','{}','{}','{}','[]','{}',0,?,?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}

	storage, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	items, err := storage.ListRepositories(t.Context())
	if err != nil || len(items) != 1 || items[0].EffectiveLocalTarget() != (execution.Target{Kind: execution.Agent, AgentID: "agent-a"}) {
		t.Fatalf("repositories=%+v err=%v", items, err)
	}
}

func TestAgentLocalRepositoryRoundTripsItsFilesystemOwner(t *testing.T) {
	storage := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := storage.SaveAgent(ctx, AgentRecord{ID: "agent-a", CertificateSerial: "serial-a", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	repository := domain.Repository{
		ID: "repo-a", Name: "agent archive", Engine: domain.RsyncEngine, Kind: domain.LocalRepository,
		LocalTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-a"},
		Path:        "/srv/archive", Status: "ready", CreatedAt: now, UpdatedAt: now,
	}
	if err := storage.CreateRepository(ctx, repository, ""); err != nil {
		t.Fatal(err)
	}
	items, err := storage.ListRepositories(ctx)
	if err != nil || len(items) != 1 || items[0].EffectiveLocalTarget() != repository.LocalTarget {
		t.Fatalf("repositories=%+v err=%v", items, err)
	}
	executionRecord, err := storage.LoadRepositoryExecution(ctx, repository.ID)
	if err != nil || executionRecord.Repository.EffectiveLocalTarget() != repository.LocalTarget {
		t.Fatalf("execution=%+v err=%v", executionRecord, err)
	}
}

func TestLocalRepositoryPathUniquenessIsScopedToFilesystemOwner(t *testing.T) {
	storage := openTestStore(t)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"agent-a", "agent-b"} {
		if err := storage.SaveAgent(t.Context(), AgentRecord{ID: id, CertificateSerial: "serial-" + id, Status: "online", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		repository := domain.Repository{
			ID: "repo-" + id, Name: "archive " + id, Engine: domain.RsyncEngine, Kind: domain.LocalRepository,
			LocalTarget: execution.Target{Kind: execution.Agent, AgentID: id}, Path: "/archive", Status: "ready", CreatedAt: now, UpdatedAt: now,
		}
		if err := storage.CreateRepository(t.Context(), repository, ""); err != nil {
			t.Fatalf("create repository for %s: %v", id, err)
		}
	}
}

func TestAgentLocalRepositoryRequiresAnExistingLogicalAgentReference(t *testing.T) {
	storage := openTestStore(t)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	err := storage.CreateRepository(t.Context(), domain.Repository{
		ID: "repo-a", Name: "agent archive", Engine: domain.RsyncEngine, Kind: domain.LocalRepository,
		LocalTarget: execution.Target{Kind: execution.Agent, AgentID: "missing-agent"},
		Path:        "/srv/archive", Status: "ready", CreatedAt: now, UpdatedAt: now,
	}, "")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v", err)
	}
}

func TestTaskRepositoryBindingRequiresTheSameLocalFilesystemOwner(t *testing.T) {
	storage := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"agent-a", "agent-b"} {
		if err := storage.SaveAgent(ctx, AgentRecord{ID: id, CertificateSerial: "serial-" + id, Status: "online", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.CreateRepository(ctx, domain.Repository{
		ID: "repo-a", Name: "agent archive", Engine: domain.RsyncEngine, Kind: domain.LocalRepository,
		LocalTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-a"},
		Path:        "/srv/archive", Status: "ready", CreatedAt: now, UpdatedAt: now,
	}, ""); err != nil {
		t.Fatal(err)
	}
	base := domain.Task{
		ID: "task-a", Name: "sync", Engine: domain.RsyncEngine, Kind: domain.RsyncTask,
		ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-b"},
		RepositoryID:    "repo-a", Rsync: &domain.RsyncSource{Path: "/srv/source"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := storage.CreateTask(ctx, base); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched Agent error=%v", err)
	}
	base.ExecutionTarget.AgentID = "agent-a"
	if err := storage.CreateTask(ctx, base); err != nil {
		t.Fatalf("owning Agent task rejected: %v", err)
	}
}

func TestAgentDeletePreviewIncludesOwnedLocalRepositories(t *testing.T) {
	storage := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := storage.SaveAgent(ctx, AgentRecord{ID: "agent-a", CertificateSerial: "serial-a", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateRepository(ctx, domain.Repository{
		ID: "repo-a", Name: "agent archive", Engine: domain.RsyncEngine, Kind: domain.LocalRepository,
		LocalTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-a"},
		Path:        "/srv/archive", Status: "ready", CreatedAt: now, UpdatedAt: now,
	}, ""); err != nil {
		t.Fatal(err)
	}
	preview, err := storage.ResourceDeletePreview(ctx, "agents", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, dependency := range preview.Dependencies {
		found = found || dependency.Type == "repositories" && dependency.Count == 1 && dependency.Names[0] == "agent archive"
	}
	if !found {
		t.Fatalf("dependencies=%+v", preview.Dependencies)
	}
}
