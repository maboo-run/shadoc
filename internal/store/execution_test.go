package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/database"
	"github.com/maboo-run/shadoc/internal/domain"
)

func TestRunAndAuditRecordsAreAppendOnlyAndQueryable(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 7, 11, 2, 30, 0, 0, time.UTC)
	if err := s.SaveSecret(context.Background(), "run-key", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(context.Background(), "run-pass", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRemoteHost(context.Background(), domain.RemoteHost{ID: "run-host", Name: "run-nas", Host: "nas", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, "run-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(context.Background(), domain.Repository{ID: "run-repo", Name: "run-repo", RemoteHostID: "run-host", Path: "/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "run-pass"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(context.Background(), domain.Task{ID: "task-a", Name: "run-task", Kind: domain.DirectoryTask, RepositoryID: "run-repo", Directory: &domain.DirectorySource{Path: "/srv"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	record := RunRecord{ID: "run-a", TaskID: "task-a", Trigger: "manual", Status: "running", StartedAt: now}
	if err := s.StartRun(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(context.Background(), "run-a", "success", now.Add(time.Minute), 1, "snap", map[string]any{"files": 3}, "safe log"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(context.Background(), AuditRecord{OccurredAt: now, Actor: "admin", Action: "run.start", TargetType: "task", TargetID: "task-a", Detail: map[string]any{"trigger": "manual"}}); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].SnapshotID != "snap" {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	audits, err := s.ListAudits(context.Background(), 10)
	if err != nil || len(audits) != 1 || audits[0].Action != "run.start" || audits[0].Actor != "admin" {
		t.Fatalf("audits=%+v err=%v", audits, err)
	}
}

func TestRecoverInterruptedRunsFailsOnlyRunningRecords(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	started := time.Date(2026, 7, 15, 2, 30, 0, 0, time.UTC)
	taskID, _ := createScheduleFixture(t, s, started)
	for _, record := range []RunRecord{
		{ID: "interrupted-run", TaskID: taskID, Trigger: "schedule", Status: "running", StartedAt: started},
		{ID: "completed-run", TaskID: taskID, Trigger: "manual", Status: "running", StartedAt: started.Add(time.Minute)},
	} {
		if err := s.StartRun(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	completedAt := started.Add(2 * time.Minute)
	if err := s.FinishRun(ctx, "completed-run", "success", completedAt, 1, "snapshot", map[string]any{"files": 1}, "safe"); err != nil {
		t.Fatal(err)
	}

	recoveredAt := started.Add(10 * time.Minute)
	count, err := s.RecoverInterruptedRuns(ctx, recoveredAt)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	interrupted, err := s.Run(ctx, "interrupted-run")
	if err != nil || interrupted.Status != "failed" || interrupted.FinishedAt == nil || !interrupted.FinishedAt.Equal(recoveredAt) || interrupted.Summary["error"] != "service restarted while run was active" {
		t.Fatalf("interrupted=%+v err=%v", interrupted, err)
	}
	completed, err := s.Run(ctx, "completed-run")
	if err != nil || completed.Status != "success" || completed.FinishedAt == nil || !completed.FinishedAt.Equal(completedAt) {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestFinishRunRejectsNonCanonicalTerminalStatus(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 4, 0, 0, 0, time.UTC)
	taskID, _ := createScheduleFixture(t, s, now)
	if err := s.StartRun(ctx, RunRecord{ID: "canonical-run", TaskID: taskID, Trigger: "manual", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "canonical-run", "succeeded", now.Add(time.Minute), 1, "", nil, ""); err == nil {
		t.Fatal("non-canonical succeeded status was accepted")
	}
	if err := s.FinishRun(ctx, "canonical-run", "success", now.Add(time.Minute), 1, "", nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestFinishRunPersistsCanonicalMetricsAndEndToEndDuration(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	started := time.Date(2026, 7, 15, 14, 0, 0, 0, time.UTC)
	if err := s.StartRun(ctx, RunRecord{ID: "metric-run", TaskID: task.ID, Trigger: "schedule", Status: "running", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	summary := map[string]any{
		"filesProcessed": float64(120), "filesChanged": 7, "bytesProcessed": int64(8192), "bytesChanged": uint64(1024),
		"unsafePath": "/must/not/become/an/indexed/metric",
	}
	if err := s.FinishRun(ctx, "metric-run", "success", started.Add(1500*time.Millisecond), 2, "snapshot", summary, ""); err != nil {
		t.Fatal(err)
	}
	record, err := s.Run(ctx, "metric-run")
	if err != nil {
		t.Fatal(err)
	}
	if record.Metrics == nil || metricValue(record.Metrics.DurationMilliseconds) != 1500 || metricValue(record.Metrics.FilesProcessed) != 120 || metricValue(record.Metrics.FilesChanged) != 7 || metricValue(record.Metrics.BytesProcessed) != 8192 || metricValue(record.Metrics.BytesChanged) != 1024 {
		t.Fatalf("metrics=%+v", record.Metrics)
	}

	if err := s.StartRun(ctx, RunRecord{ID: "invalid-metric-run", TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "invalid-metric-run", "failed", started.Add(time.Second), 1, "", map[string]any{"filesChanged": -1, "bytesChanged": "secret-like text"}, ""); err != nil {
		t.Fatal(err)
	}
	invalid, err := s.Run(ctx, "invalid-metric-run")
	if err != nil || invalid.Metrics == nil || invalid.Metrics.FilesChanged != nil || invalid.Metrics.BytesChanged != nil || metricValue(invalid.Metrics.DurationMilliseconds) != 1000 {
		t.Fatalf("invalid metrics=%+v err=%v", invalid.Metrics, err)
	}
}

func TestOpenAddsMetricColumnsToLegacyRunsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-run-metrics.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE runs DROP COLUMN bytes_changed`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	columns := map[string]bool{}
	rows, err := s.db.Query(`PRAGMA table_info(runs)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	_ = rows.Close()
	for _, name := range []string{"duration_ms", "files_processed", "files_changed", "bytes_processed", "bytes_changed"} {
		if !columns[name] {
			t.Fatalf("legacy migration omitted %s: %v", name, columns)
		}
	}
}

func metricValue(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}

func TestOpenMigratesLegacySucceededRunAndOccurrenceStatuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-status.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 4, 0, 0, 0, time.UTC)
	taskID, _ := createScheduleFixture(t, s, now)
	if err := s.StartRun(ctx, RunRecord{ID: "legacy-run", TaskID: taskID, Trigger: "manual", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE runs SET status='succeeded',finished_at=? WHERE id='legacy-run'`, formatTime(now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	occurrence := ScheduleOccurrence{ID: "legacy-occurrence", OwnerKind: "plan", OwnerID: "legacy-plan", ScheduledAt: now, ObservedAt: now, Mode: "on_time", Status: "success", TargetIDs: []string{taskID}, FinishedAt: timePointer(now)}
	if created, err := s.CreateScheduleOccurrence(ctx, occurrence); err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schedule_occurrences SET status='succeeded' WHERE id='legacy-occurrence'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run, err := s.Run(ctx, "legacy-run")
	if err != nil || run.Status != "success" {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	latest, err := s.LatestScheduleOccurrence(ctx, "plan", "legacy-plan", now.Add(-time.Minute))
	if err != nil || latest.Status != "success" {
		t.Fatalf("occurrence=%+v err=%v", latest, err)
	}
}

func TestSnapshotMetadataIsDurableAndRepositoryScoped(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 7, 11, 2, 30, 0, 0, time.UTC)
	if err := s.SaveSecret(context.Background(), "meta-key", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(context.Background(), "meta-pass", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRemoteHost(context.Background(), domain.RemoteHost{ID: "meta-host", Name: "meta-nas", Host: "nas", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, "meta-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(context.Background(), domain.Repository{ID: "repo-a", Name: "meta-repo", RemoteHostID: "meta-host", Path: "/meta", Status: "ready", CreatedAt: now, UpdatedAt: now}, "meta-pass"); err != nil {
		t.Fatal(err)
	}
	metadata := database.SnapshotMetadata{Engine: database.MySQL, Database: "gitea", Format: "sql", Filename: "gitea.sql", ServerVersion: "8.4.5", ClientVersion: "8.4.5", Encoding: "utf8mb4", Collation: "utf8mb4_0900_ai_ci"}
	if err := s.SaveSnapshotMetadata(context.Background(), "repo-a", "snapshot-a", metadata, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.SnapshotMetadata(context.Background(), "repo-a", "snapshot-a")
	if err != nil || got.Metadata != metadata || got.MetadataVersion != 1 {
		t.Fatalf("record=%+v err=%v", got, err)
	}
	if _, err := s.SnapshotMetadata(context.Background(), "repo-b", "snapshot-a"); err == nil {
		t.Fatal("snapshot metadata crossed repository boundary")
	}
}

func TestLatestSuccessfulRunRequiresACompleteSnapshot(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	for index, item := range []struct {
		id       string
		status   string
		snapshot string
	}{
		{id: "complete", status: "success", snapshot: "snapshot-complete"},
		{id: "partial", status: "partial", snapshot: "snapshot-partial"},
		{id: "empty-success", status: "success", snapshot: ""},
	} {
		started := now.Add(time.Duration(index) * time.Minute)
		if err := s.StartRun(ctx, RunRecord{ID: item.id, TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: started}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, item.id, item.status, started.Add(time.Second), 1, item.snapshot, map[string]any{}, ""); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := s.LatestSuccessfulRun(ctx, task.ID)
	if err != nil || latest.ID != "complete" || latest.SnapshotID != "snapshot-complete" {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}

func TestRepositoryPasswordRotationPersistsPendingOldKeyAtomically(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"old-secret", "new-secret", "another-secret"} {
		if err := s.SaveSecret(ctx, id, "repository-password", []byte("cipher"), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "repo", Kind: domain.LocalRepository, Path: "/backup/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "old-secret"); err != nil {
		t.Fatal(err)
	}

	if err := s.CommitRepositoryPasswordRotation(ctx, "repo", "new-secret", "old-key", "old-secret", now); err != nil {
		t.Fatal(err)
	}
	execution, err := s.LoadRepositoryExecution(ctx, "repo")
	if err != nil || execution.RepositoryPasswordSecretID != "new-secret" {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
	pending, ok, err := s.PendingRepositoryKeyRevocation(ctx, "repo")
	if err != nil || !ok || pending.KeyID != "old-key" || pending.SecretID != "old-secret" {
		t.Fatalf("pending=%+v ok=%v err=%v", pending, ok, err)
	}
	if err := s.CommitRepositoryPasswordRotation(ctx, "repo", "another-secret", "new-key", "new-secret", now); !errors.Is(err, ErrConflict) {
		t.Fatalf("second rotation error=%v", err)
	}
	execution, _ = s.LoadRepositoryExecution(ctx, "repo")
	if execution.RepositoryPasswordSecretID != "new-secret" {
		t.Fatalf("conflicting rotation changed managed secret: %+v", execution)
	}
	if err := s.CompleteRepositoryKeyRevocation(ctx, "repo", "old-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadSecret(ctx, "old-secret"); err == nil {
		t.Fatal("completed key revocation retained the old encrypted secret")
	}
	if _, ok, err := s.PendingRepositoryKeyRevocation(ctx, "repo"); err != nil || ok {
		t.Fatalf("pending revocation remained: ok=%v err=%v", ok, err)
	}
}

func TestLoadTaskExecutionIncludesSecretReferencesWithoutDecrypting(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	if err := s.SaveSecret(context.Background(), "key", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(context.Background(), "repo-pass", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	host := domain.RemoteHost{ID: "host", Name: "nas", Host: "nas.local", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRemoteHost(context.Background(), host, "key"); err != nil {
		t.Fatal(err)
	}
	repo := domain.Repository{ID: "repo", Name: "photos", RemoteHostID: "host", Path: "/backups/photos", Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRepository(context.Background(), repo, "repo-pass"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/srv/photos"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	aggregate, err := s.LoadTaskExecution(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.PrivateKeySecretID != "key" || aggregate.RepositoryPasswordSecretID != "repo-pass" || aggregate.Host.Host != "nas.local" {
		t.Fatalf("aggregate=%+v", aggregate)
	}
}
