package lifecycle

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func createExecutionTask(t *testing.T, s *store.Store, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := s.SaveSecret(ctx, "host-secret", "ssh-private-key", []byte("encrypted"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(ctx, "repo-secret", "repository-password", []byte("encrypted"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRemoteHost(ctx, domain.RemoteHost{ID: "host", Name: "host", Host: "nas", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, "host-secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "repo", RemoteHostID: "host", Path: "/backup", Status: "ready", CreatedAt: now, UpdatedAt: now}, "repo-secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(ctx, domain.Task{ID: "task", Name: "task", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/source"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupPreservesSummariesBeforeExpiryAndNeverTouchesRunning(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	createExecutionTask(t, s, now)
	finished := func(id string, ageDays int, rawLog string) {
		started := now.Add(-time.Duration(ageDays)*24*time.Hour - time.Hour)
		ended := now.Add(-time.Duration(ageDays) * 24 * time.Hour)
		if err := s.StartRun(ctx, store.RunRecord{ID: id, TaskID: "task", Trigger: "manual", Status: "running", StartedAt: started}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, id, "success", ended, 1, "snapshot", map[string]any{"files": 3}, rawLog); err != nil {
			t.Fatal(err)
		}
	}
	finished("expired-summary", 400, "old summary log")
	finished("expired-log", 60, "raw log to clear")
	if err := s.StartRun(ctx, store.RunRecord{ID: "running", TaskID: "task", Trigger: "schedule", Status: "running", StartedAt: now.Add(-500 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(ctx, store.AuditRecord{OccurredAt: now.Add(-400 * 24 * time.Hour), Action: "old", TargetType: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(ctx, store.AuditRecord{OccurredAt: now.Add(-time.Hour), Action: "recent", TargetType: "test"}); err != nil {
		t.Fatal(err)
	}

	report, err := New(s).Cleanup(ctx, Policy{RunDays: 365, RawLogDays: 30, AuditDays: 365, RawLogMaxBytes: 1024}, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.LogsCleared != 1 || report.RunsDeleted != 1 || report.AuditsDeleted != 1 {
		t.Fatalf("report=%+v", report)
	}
	runs, err := s.ListRuns(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]store.RunRecord{}
	for _, run := range runs {
		byID[run.ID] = run
	}
	if _, exists := byID["expired-summary"]; exists {
		t.Fatal("expired run summary retained")
	}
	if got := byID["expired-log"]; got.RawLog != "" || got.Summary["files"] != float64(3) {
		t.Fatalf("retained summary=%+v", got)
	}
	if got := byID["running"]; got.RawLog != "" || got.FinishedAt != nil {
		// StartRun initializes an empty log; capacity/age cleanup must leave that
		// unfinished row intact rather than deleting it.
		t.Fatalf("running row changed=%+v", got)
	}
}

func TestCleanupEnforcesRawLogCapacityOldestFirst(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	createExecutionTask(t, s, now)
	for index, id := range []string{"oldest", "middle", "newest"} {
		started := now.Add(time.Duration(index-3) * time.Hour)
		if err := s.StartRun(ctx, store.RunRecord{ID: id, TaskID: "task", Trigger: "manual", Status: "running", StartedAt: started}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, id, "success", started.Add(time.Minute), 1, "", nil, "12345"); err != nil {
			t.Fatal(err)
		}
	}
	report, err := New(s).Cleanup(ctx, Policy{RunDays: 365, RawLogDays: 365, AuditDays: 365, RawLogMaxBytes: 10}, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.LogsCleared != 1 || report.RawLogBytesAfter != 10 {
		t.Fatalf("report=%+v", report)
	}
	runs, _ := s.ListRuns(ctx, 100)
	for _, run := range runs {
		if run.ID == "oldest" && run.RawLog != "" {
			t.Fatal("oldest log was not cleared first")
		}
	}
}

func TestCleanupUsesRunRetentionForCapacitySamples(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	createExecutionTask(t, s, now)
	if err := s.SaveRepositoryCapacity(ctx, "repo", domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: 500, CheckedAt: now.Add(-40 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRepositoryCapacity(ctx, "repo", domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: 400, CheckedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	policy := Policy{RunDays: 30, RawLogDays: 30, AuditDays: 365, RawLogMaxBytes: 1024}
	preview, err := s.PreviewExecutionDataCleanup(ctx, policy, now)
	if err != nil || preview.CapacitySamplesDeleted != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	samples, err := s.ListRepositoryCapacitySamples(ctx, "repo", 10)
	if err != nil || len(samples) != 2 {
		t.Fatalf("preview mutated samples=%+v err=%v", samples, err)
	}
	report, err := New(s).Cleanup(ctx, policy, now)
	if err != nil || report.CapacitySamplesDeleted != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	samples, err = s.ListRepositoryCapacitySamples(ctx, "repo", 10)
	if err != nil || len(samples) != 1 || !samples[0].CheckedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("retained samples=%+v err=%v", samples, err)
	}
}
