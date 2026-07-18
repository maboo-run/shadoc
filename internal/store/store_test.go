package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestOpenMigratesLegacyRemoteRepositoriesToSFTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE secrets (id TEXT PRIMARY KEY, purpose TEXT NOT NULL, ciphertext BLOB NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE remote_hosts (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, host TEXT NOT NULL, port INTEGER NOT NULL, username TEXT NOT NULL, private_key_secret_id TEXT NOT NULL REFERENCES secrets(id), host_fingerprint TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE repositories (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, remote_host_id TEXT NOT NULL REFERENCES remote_hosts(id), path TEXT NOT NULL, password_secret_id TEXT NOT NULL REFERENCES secrets(id), status TEXT NOT NULL DEFAULT 'ready', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(remote_host_id,path));
CREATE TABLE tasks (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, repository_id TEXT NOT NULL UNIQUE REFERENCES repositories(id), source_json TEXT NOT NULL, retention_json TEXT NOT NULL DEFAULT '{}', resources_json TEXT NOT NULL DEFAULT '{}', exclusions_json TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO secrets VALUES ('key','ssh-private-key',X'01','2026-07-11T00:00:00Z','2026-07-11T00:00:00Z');
INSERT INTO secrets VALUES ('pass','repository-password',X'02','2026-07-11T00:00:00Z','2026-07-11T00:00:00Z');
INSERT INTO remote_hosts VALUES ('host','nas','nas.local',22,'backup','key','known','2026-07-11T00:00:00Z','2026-07-11T00:00:00Z');
INSERT INTO repositories VALUES ('repo','photos','host','/backup/photos','pass','ready','2026-07-11T00:00:00Z','2026-07-11T00:00:00Z');
INSERT INTO tasks VALUES ('task','photos','directory','repo','{"path":"/source","skipIfUnchanged":true}','{}','{}','[]',1,'2026-07-11T00:00:00Z','2026-07-11T00:00:00Z');
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer s.Close()
	repositories, err := s.ListRepositories(context.Background())
	if err != nil || len(repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
	if repositories[0].Kind != domain.SFTPRepository || repositories[0].RemoteHostID != "host" {
		t.Fatalf("migrated repository=%+v", repositories[0])
	}
	if repositories[0].EffectiveEngine() != domain.ResticEngine {
		t.Fatalf("migrated repository engine=%q", repositories[0].EffectiveEngine())
	}
	var engine, target, confirmation, health string
	if err := s.db.QueryRow(`SELECT engine,execution_target_json,scope_confirmation_json,health_policy_json FROM tasks WHERE id='task'`).Scan(&engine, &target, &confirmation, &health); err != nil {
		t.Fatal(err)
	}
	if engine != "restic" || target != `{"kind":"local"}` || confirmation != "{}" || health != "{}" {
		t.Fatalf("migrated task engine=%q target=%q confirmation=%q health=%q", engine, target, confirmation, health)
	}
}

func TestTaskHealthPolicyRoundTripsThroughCreateAndUpdate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 6, 30, 0, 0, time.UTC)
	if err := s.SaveSecret(ctx, "password", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "repo", Kind: domain.LocalRepository, Path: "/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "password"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", Name: "task", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/srv"}, Health: domain.TaskHealthPolicy{MaxSuccessAgeHours: 48}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListTasks(ctx)
	if err != nil || len(items) != 1 || items[0].Health.MaxSuccessAgeHours != 48 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	task.Health.MaxSuccessAgeHours = 72
	task.UpdatedAt = now.Add(time.Minute)
	if err := s.UpdateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListTasks(ctx)
	if err != nil || len(items) != 1 || items[0].Health.MaxSuccessAgeHours != 72 {
		t.Fatalf("updated items=%+v err=%v", items, err)
	}
}

func TestTaskScopeConfirmationRoundTripsThroughTaskStore(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	confirmedAt := time.Date(2026, 7, 15, 6, 0, 0, 0, time.UTC)
	task.Name = "renamed"
	task.ScopeConfirmation = domain.TaskScopeConfirmation{
		PreviewID: "preview-1", Fingerprint: "fingerprint-1", ConfirmedBy: "admin", ConfirmedAt: confirmedAt,
		Summary: map[string]any{"includedFiles": 4}, DeleteConfirmed: false,
	}
	if err := s.UpdateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	got := tasks[0].ScopeConfirmation
	if got.PreviewID != "preview-1" || got.Fingerprint != "fingerprint-1" || got.ConfirmedBy != "admin" || !got.ConfirmedAt.Equal(confirmedAt) || got.Summary["includedFiles"] != float64(4) {
		t.Fatalf("confirmation=%+v", got)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "restic-control.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return s
}

func TestRepositoryCapacitySnapshotIsReturnedWithRepository(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	if err := s.SaveSecret(ctx, "password", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "repo", Kind: domain.LocalRepository, Path: "/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "password"); err != nil {
		t.Fatal(err)
	}
	checked := now.Add(time.Hour)
	if err := s.SaveRepositoryCapacity(ctx, "repo", domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: 400, CheckedAt: checked, SourceAgentID: "agent-a"}); err != nil {
		t.Fatal(err)
	}
	repositories, err := s.ListRepositories(ctx)
	if err != nil || len(repositories) != 1 || repositories[0].Capacity == nil {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
	capacity := repositories[0].Capacity
	if capacity.TotalBytes != 1000 || capacity.AvailableBytes != 400 || capacity.UsedBytes != 600 || !capacity.CheckedAt.Equal(checked) || capacity.SourceAgentID != "agent-a" {
		t.Fatalf("capacity=%+v", capacity)
	}
}

func TestUpdateTaskCannotDisableTaskInEnabledPlan(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.SaveSecret(ctx, "key", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(ctx, "password", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRemoteHost(ctx, domain.RemoteHost{ID: "host", Name: "host", Host: "nas", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, "key"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "repo", RemoteHostID: "host", Path: "/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "password"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", Name: "task", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/srv"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	plan := domain.Plan{ID: "plan", Name: "plan", Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:00"}, Timezone: "UTC", MaxParallel: 1, TaskIDs: []string{task.ID}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	task.Enabled = false
	if err := s.UpdateTask(ctx, task); !errors.Is(err, ErrConflict) {
		t.Fatalf("disable referenced task error = %v, want conflict", err)
	}
	items, err := s.ListTasks(ctx)
	if err != nil || len(items) != 1 || !items[0].Enabled {
		t.Fatalf("task changed after rejected update: items=%+v err=%v", items, err)
	}
}

func TestChangingTaskRetentionDoesNotChangeRepositoryOwnedPolicy(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.SaveSecret(ctx, "password", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "repo", Kind: domain.LocalRepository, Path: "/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "password"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", Name: "task", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/srv"}, Retention: domain.RetentionPolicy{KeepLast: 3}, Resources: domain.ResourcePolicy{DownloadKiBPerSecond: 128}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	policy := domain.MaintenancePolicy{RepositoryID: "repo", Schedule: domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: 0, TimeOfDay: "03:00"}, Timezone: "UTC", Retention: task.Retention, Enabled: true, UpdatedAt: now}
	if err := s.SaveMaintenancePolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	task.Retention.KeepLast = 4
	task.UpdatedAt = now.Add(time.Second)
	if err := s.UpdateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.MaintenancePolicy(ctx, "repo")
	if err != nil || !loaded.Enabled || loaded.Retention.KeepLast != 3 || loaded.RetentionSource != domain.RepositoryRetentionSource || loaded.RetentionConflict || loaded.ReviewedRetention != nil || loaded.PolicyFingerprint != policy.Retention.Fingerprint() {
		t.Fatalf("policy=%+v err=%v", loaded, err)
	}
	effective, bound, err := s.EffectiveMaintenanceRetention(ctx, "repo", domain.RetentionPolicy{KeepLast: 99})
	if err != nil || !bound || effective != policy.Retention {
		t.Fatalf("effective=%+v bound=%v err=%v", effective, bound, err)
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].Retention != (domain.RetentionPolicy{}) {
		t.Fatalf("task retention copy was not retired: tasks=%+v err=%v", tasks, err)
	}
	resources, bound, err := s.EffectiveRepositoryResources(ctx, "repo")
	if err != nil || !bound || resources != task.Resources {
		t.Fatalf("resources=%+v bound=%v err=%v", resources, bound, err)
	}

	loaded.Retention = task.Retention
	loaded.UpdatedAt = now.Add(2 * time.Second)
	if err := s.SaveMaintenancePolicy(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	reviewed, err := s.MaintenancePolicy(ctx, "repo")
	if err != nil || !reviewed.Enabled || reviewed.RetentionConflict || reviewed.ReviewedRetention != nil || reviewed.Retention != task.Retention || reviewed.RetentionSource != domain.RepositoryRetentionSource {
		t.Fatalf("reviewed policy=%+v err=%v", reviewed, err)
	}
}

func TestMaintenanceAuthorityKeepsUnboundRepositoryPolicy(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.SaveSecret(ctx, "password", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "repo", Kind: domain.LocalRepository, Path: "/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "password"); err != nil {
		t.Fatal(err)
	}
	fallback := domain.RetentionPolicy{KeepWithinDays: 30, KeepLast: 3}
	effective, bound, err := s.EffectiveMaintenanceRetention(ctx, "repo", fallback)
	if err != nil || bound || effective != fallback {
		t.Fatalf("effective=%+v bound=%v err=%v", effective, bound, err)
	}
	resources, resourceBound, err := s.EffectiveRepositoryResources(ctx, "repo")
	if err != nil || resourceBound || resources != (domain.ResourcePolicy{}) {
		t.Fatalf("resources=%+v bound=%v err=%v", resources, resourceBound, err)
	}
	policy := domain.MaintenancePolicy{RepositoryID: "repo", Schedule: domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: 0, TimeOfDay: "03:00"}, Timezone: "UTC", Retention: fallback, Enabled: true, UpdatedAt: now}
	if err := s.SaveMaintenancePolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.MaintenancePolicy(ctx, "repo")
	if err != nil || loaded.RetentionSource != domain.RepositoryRetentionSource || loaded.RetentionConflict || loaded.PolicyFingerprint != fallback.Fingerprint() {
		t.Fatalf("policy=%+v err=%v", loaded, err)
	}
}

func TestMaintenanceAuthorityMigrationMovesTaskRetentionIntoRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.SaveSecret(ctx, "password", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "repo", Kind: domain.LocalRepository, Path: "/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "password"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", Name: "task", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/srv"}, Retention: domain.RetentionPolicy{KeepLast: 9}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET retention_json='{"keepLast":9}' WHERE id='task'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE repository_maintenance SET retention_json='{"keepLast":1}',enabled=1 WHERE repository_id='repo'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM metadata WHERE key='repository_retention_authority_v1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.MaintenancePolicy(ctx, "repo")
	if err != nil || !loaded.Enabled || loaded.RetentionConflict || loaded.Retention.KeepLast != 9 || loaded.RetentionSource != domain.RepositoryRetentionSource {
		t.Fatalf("migrated policy=%+v err=%v", loaded, err)
	}
	tasks, err := reopened.ListTasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].Retention != (domain.RetentionPolicy{}) {
		t.Fatalf("migrated tasks=%+v err=%v", tasks, err)
	}
}

func TestStorePersistsInitializationState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	initialized, err := s.IsInitialized(ctx)
	if err != nil {
		t.Fatalf("read initial state: %v", err)
	}
	if initialized {
		t.Fatal("new store must not be initialized")
	}

	if err := s.MarkInitialized(ctx); err != nil {
		t.Fatalf("mark initialized: %v", err)
	}

	initialized, err = s.IsInitialized(ctx)
	if err != nil {
		t.Fatalf("read updated state: %v", err)
	}
	if !initialized {
		t.Fatal("expected initialized store")
	}
}

func TestEmptyRunAndAuditListsEncodeAsArrays(t *testing.T) {
	s := openTestStore(t)
	runs, err := s.ListRuns(context.Background(), 100)
	if err != nil || runs == nil {
		t.Fatalf("runs=%v err=%v; empty list must be non-nil", runs, err)
	}
	audits, err := s.ListAudits(context.Background(), 100)
	if err != nil || audits == nil {
		t.Fatalf("audits=%v err=%v; empty list must be non-nil", audits, err)
	}
}
