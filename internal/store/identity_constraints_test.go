package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestSuccessfulTaskCannotChangeProtectedObjectOrRepository(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.StartRun(ctx, RunRecord{ID: "run-1", TaskID: task.ID, Trigger: "manual", Status: "success", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	changed := task
	changed.Directory = &domain.DirectorySource{Path: "/different"}
	changed.UpdatedAt = now.Add(time.Second)
	if err := s.UpdateTask(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed source err=%v", err)
	}
	changed = task
	changed.Kind = domain.DatabaseTask
	changed.Directory = nil
	changed.Database = &domain.DatabaseSource{ConnectionID: "other", Database: "other"}
	if err := s.UpdateTask(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed kind err=%v", err)
	}
}

func TestReferencedRepositoryLocationCannotChangeAndEnabledPlanKeepsTask(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo := domain.Repository{ID: "repo", Name: "repo", Kind: domain.LocalRepository, Path: "/different", Status: "uninitialized", UpdatedAt: now}
	if _, err := s.UpdateRepository(ctx, repo, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("repository move err=%v", err)
	}
	plan := domain.Plan{ID: "plan", Name: "plan", Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "03:00"}, Timezone: "UTC", MaxParallel: 1, TaskIDs: []string{task.ID}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTask(ctx, task.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete enabled-plan task err=%v", err)
	}
	loaded, err := s.ListPlans(ctx)
	if err != nil || len(loaded) != 1 || len(loaded[0].TaskIDs) != 1 {
		t.Fatalf("plans=%+v err=%v", loaded, err)
	}
}

func createIdentityFixture(t *testing.T) (*Store, domain.Task) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.SaveSecret(ctx, "pass", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "repo", Kind: domain.LocalRepository, Path: "/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "pass"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", Name: "task", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/source"}, Retention: domain.RetentionPolicy{KeepLast: 3}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	return s, task
}
