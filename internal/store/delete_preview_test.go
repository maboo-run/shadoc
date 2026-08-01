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

func TestResourceDeletePreviewIncludesNamedDependenciesAndVersion(t *testing.T) {
	s, task := createIdentityFixture(t)
	preview, err := s.ResourceDeletePreview(context.Background(), "repositories", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "repo" || preview.UpdatedAt == "" || len(preview.Dependencies) != 1 || preview.Dependencies[0].Type != "tasks" || preview.Dependencies[0].Count != 1 || preview.Dependencies[0].Names[0] != task.Name {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestRemoteHostDeletePreviewIncludesRsyncTaskDependency(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 31, 11, 0, 0, 0, time.UTC)
	if err := s.SaveSecret(ctx, "key", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	host := domain.RemoteHost{ID: "host", Name: "host", Host: "host.example", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRemoteHost(ctx, host, "key"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{
		ID: "rsync-task", Name: "rsync-task", Engine: domain.RsyncEngine, Kind: domain.RsyncTask,
		Rsync:     &domain.RsyncSource{Path: "/source", DestinationHostID: host.ID, DestinationPath: "/destination"},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	preview, err := s.ResourceDeletePreview(ctx, "remote-hosts", host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Deletable || len(preview.Dependencies) != 1 || preview.Dependencies[0].Type != "tasks" || preview.Dependencies[0].Names[0] != task.Name {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestDisabledTaskDeleteRemovesTaskDataAndUnlinksPlan(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if err := s.StartRun(ctx, RunRecord{ID: "run-before-delete", TaskID: task.ID, Trigger: "manual", Status: "success", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOperation(ctx, OperationRecord{ID: "task-operation", Kind: "task_scope_inventory", Actor: "admin", TaskID: task.ID, Status: "success", Stage: "completed", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTaskScopePreview(ctx, TaskScopePreview{ID: "task-preview", TaskID: task.ID, Fingerprint: "fingerprint", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePlan(ctx, domain.Plan{
		ID: "plan", Name: "plan", Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "03:00"}, Timezone: "UTC",
		MaxParallel: 1, TaskIDs: []string{task.ID}, CatchUpWindowMinutes: 60, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO protection_drafts(id,name,template_id,execution_target_json,retention_json,resources_json,health_json,schedule_json,timezone,max_parallel,catch_up_window_minutes,notification_mode,plan_id,status,created_at,updated_at)
		VALUES('draft','draft','','{"kind":"local"}','{}','{}','{}','{"kind":"daily","timeOfDay":"03:00"}','UTC',1,60,'none','plan','ready',?,?);
		INSERT INTO protection_draft_items(id,draft_id,position,task_name,source_kind,source_json,repository_id,repository_name,repository_kind,remote_host_id,repository_path,repository_password_secret_id,task_id,status,error_summary,updated_at)
		VALUES('draft-item','draft',0,'task','directory','{"path":"/source"}','draft-repo','draft repo','local','','/draft-repo','','task','ready','',?);`, formatTime(now), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	task.Enabled = false
	task.UpdatedAt = now.Add(time.Minute)
	if err := s.UpdateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	preview, err := s.ResourceDeletePreview(ctx, "tasks", task.ID)
	if err != nil || !preview.Deletable || len(preview.Dependencies) != 0 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err := s.DeleteResourceVersioned(ctx, "tasks", task.ID, preview.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	for table, query := range map[string]string{
		"tasks":                  `SELECT COUNT(*) FROM tasks WHERE id='task'`,
		"runs":                   `SELECT COUNT(*) FROM runs WHERE task_id='task'`,
		"operations":             `SELECT COUNT(*) FROM operations WHERE task_id='task'`,
		"task_scope_previews":    `SELECT COUNT(*) FROM task_scope_previews WHERE task_id='task'`,
		"plan_tasks":             `SELECT COUNT(*) FROM plan_tasks WHERE task_id='task'`,
		"protection_drafts":      `SELECT COUNT(*) FROM protection_drafts WHERE id='draft'`,
		"protection_draft_items": `SELECT COUNT(*) FROM protection_draft_items WHERE task_id='task'`,
	} {
		var count int
		if err := s.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows=%d", table, count)
		}
	}
	var plans int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plans WHERE id='plan'`).Scan(&plans); err != nil || plans != 0 {
		t.Fatalf("plan count=%d err=%v", plans, err)
	}
}

func TestDisabledTaskDeleteBlocksActiveWork(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 31, 12, 30, 0, 0, time.UTC)
	if err := s.StartRun(ctx, RunRecord{ID: "active-run", TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOperation(ctx, OperationRecord{ID: "active-operation", Kind: "task_scope_inventory", Actor: "admin", TaskID: task.ID, Status: "running", Stage: "running", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task.Enabled = false
	task.UpdatedAt = now.Add(time.Minute)
	if err := s.UpdateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	preview, err := s.ResourceDeletePreview(ctx, "tasks", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Deletable || len(preview.Dependencies) != 2 || preview.BlockedReason == "" {
		t.Fatalf("active task preview=%+v", preview)
	}
	if _, err := s.DeleteResourceVersioned(ctx, "tasks", task.ID, preview.UpdatedAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("active task delete error=%v", err)
	}

	if err := s.FinishRun(ctx, "active-run", "success", now.Add(2*time.Minute), 1, "", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishOperation(ctx, "active-operation", "success", "completed", now.Add(2*time.Minute), "", nil); err != nil {
		t.Fatal(err)
	}
	preview, err = s.ResourceDeletePreview(ctx, "tasks", task.ID)
	if err != nil || !preview.Deletable || len(preview.Dependencies) != 0 {
		t.Fatalf("completed task preview=%+v err=%v", preview, err)
	}
	if _, err := s.DeleteResourceVersioned(ctx, "tasks", task.ID, preview.UpdatedAt); err != nil {
		t.Fatal(err)
	}
}

func TestDisabledTaskDeletePreservesSharedPlanHistory(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 31, 13, 0, 0, 0, time.UTC)
	if err := s.SaveSecret(ctx, "other-password", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "other-repo", Name: "other repo", Kind: domain.LocalRepository, Path: "/other-repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "other-password"); err != nil {
		t.Fatal(err)
	}
	other := domain.Task{ID: "other-task", Name: "other task", Kind: domain.DirectoryTask, RepositoryID: "other-repo", Directory: &domain.DirectorySource{Path: "/other-source"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(ctx, other); err != nil {
		t.Fatal(err)
	}
	plan := domain.Plan{ID: "shared-plan", Name: "shared plan", Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "03:00"}, Timezone: "UTC", MaxParallel: 1, TaskIDs: []string{task.ID, other.ID}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRun(ctx, RunRecord{ID: "run-task", TaskID: task.ID, PlanID: plan.ID, Trigger: "schedule", Status: "success", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRun(ctx, RunRecord{ID: "run-other", TaskID: other.ID, PlanID: plan.ID, Trigger: "schedule", Status: "success", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	occurrence := ScheduleOccurrence{ID: "shared-occurrence", OwnerKind: "plan", OwnerID: plan.ID, ScheduledAt: now, ObservedAt: now, Mode: "on_time", Status: "pending", TargetIDs: []string{task.ID, other.ID}, RunIDs: []string{"run-task", "run-other"}}
	if created, err := s.CreateScheduleOccurrence(ctx, occurrence); err != nil || !created {
		t.Fatalf("create occurrence=%v err=%v", created, err)
	}
	if claimed, err := s.ClaimScheduleOccurrence(ctx, occurrence.ID, now.Add(time.Second)); err != nil || !claimed {
		t.Fatalf("claim occurrence=%v err=%v", claimed, err)
	}
	task.Enabled = false
	task.UpdatedAt = now.Add(time.Minute)
	if err := s.UpdateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	preview, err := s.ResourceDeletePreview(ctx, "tasks", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteResourceVersioned(ctx, "tasks", task.ID, preview.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	plans, err := s.ListPlans(ctx)
	if err != nil || len(plans) != 1 || len(plans[0].TaskIDs) != 1 || plans[0].TaskIDs[0] != other.ID {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	occurrences, err := s.ListScheduleOccurrences(ctx, "plan", plan.ID, 10)
	if err != nil || len(occurrences) != 1 || occurrences[0].Status != "cancelled" || len(occurrences[0].TargetIDs) != 1 || occurrences[0].TargetIDs[0] != other.ID || len(occurrences[0].RunIDs) != 1 || occurrences[0].RunIDs[0] != "run-other" {
		t.Fatalf("occurrences=%+v err=%v", occurrences, err)
	}
	if err := s.FinishScheduleOccurrence(ctx, occurrence.ID, "failed", []string{"run-task", "run-other"}, now.Add(2*time.Minute)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale occurrence finish error=%v", err)
	}
	occurrences, err = s.ListScheduleOccurrences(ctx, "plan", plan.ID, 10)
	if err != nil || len(occurrences) != 1 || occurrences[0].Status != "cancelled" || len(occurrences[0].RunIDs) != 1 || occurrences[0].RunIDs[0] != "run-other" {
		t.Fatalf("stale finish rewrote occurrence=%+v err=%v", occurrences, err)
	}
}

func TestVersionedDeleteRejectsResourceChangedAfterPreview(t *testing.T) {
	s, _ := createIdentityFixture(t)
	ctx := context.Background()
	preview, err := s.ResourceDeletePreview(ctx, "tasks", "task")
	if err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.ListTasks(ctx)
	changed := tasks[0]
	changed.Name = "changed"
	changed.UpdatedAt = time.Now().UTC().Add(time.Minute)
	if err := s.UpdateTask(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteResourceVersioned(ctx, "tasks", "task", preview.UpdatedAt); err != ErrConflict {
		t.Fatalf("versioned delete err=%v", err)
	}
	loaded, _ := s.ListTasks(ctx)
	if len(loaded) != 1 || loaded[0].Name != "changed" {
		t.Fatalf("tasks=%+v", loaded)
	}
}

func TestVersionedRemoteHostDeleteDetachesLegacyAgentBinding(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy-agent-host.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA foreign_keys=ON;
		CREATE TABLE remote_hosts (
			id TEXT PRIMARY KEY,
			private_key_secret_id TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE repositories (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			remote_host_id TEXT,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE tasks (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			engine TEXT NOT NULL,
			source_json TEXT NOT NULL,
			repository_id TEXT
		);
		CREATE TABLE agents (
			id TEXT PRIMARY KEY,
			remote_host_id TEXT REFERENCES remote_hosts(id)
		);
		INSERT INTO remote_hosts(id,private_key_secret_id,updated_at)
		VALUES('host-old','secret-old','2026-07-25T12:00:00Z');
		INSERT INTO agents(id,remote_host_id) VALUES('agent-a','host-old');
	`); err != nil {
		t.Fatal(err)
	}

	s := &Store{db: db}
	secrets, err := s.DeleteResourceVersioned(context.Background(), "remote-hosts", "host-old", "2026-07-25T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 || secrets[0] != "secret-old" {
		t.Fatalf("secrets=%v", secrets)
	}
	var binding sql.NullString
	if err := db.QueryRow(`SELECT remote_host_id FROM agents WHERE id='agent-a'`).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if binding.Valid {
		t.Fatalf("legacy Agent binding was not detached: %q", binding.String)
	}
}

func TestAgentDeletePreviewRequiresRevocationAndReportsTaskDependencies(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	if err := s.SaveAgent(ctx, AgentRecord{ID: "agent-a", CertificateSerial: "serial-a", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks(id,name,engine,kind,execution_target_json,repository_id,source_json,retention_json,resources_json,exclusions_json,enabled,created_at,updated_at)
		VALUES('task-a','Agent backup','restic','directory','{"kind":"agent","agentId":"agent-a"}',NULL,'{}','{}','{}','[]',0,?,?)`,
		formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}

	preview, err := s.ResourceDeletePreview(ctx, "agents", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "agent-a" || preview.UpdatedAt == "" || preview.Deletable || preview.BlockedReason == "" {
		t.Fatalf("active Agent preview=%+v", preview)
	}
	if len(preview.Dependencies) != 1 || preview.Dependencies[0].Type != "tasks" || preview.Dependencies[0].Names[0] != "Agent backup" {
		t.Fatalf("Agent dependencies=%+v", preview.Dependencies)
	}

	if err := s.RevokeAgent(ctx, "agent-a", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET execution_target_json='{"kind":"local"}',updated_at=? WHERE id='task-a'`, formatTime(now.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	preview, err = s.ResourceDeletePreview(ctx, "agents", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Deletable || preview.BlockedReason != "" || len(preview.Dependencies) != 0 {
		t.Fatalf("revoked unreferenced Agent preview=%+v", preview)
	}
}

func TestVersionedAgentDeleteRejectsActiveOrChangedIdentityAndCascadesCertificates(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	if err := s.SaveAgent(ctx, AgentRecord{ID: "agent-a", CertificateSerial: "serial-a", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	active, err := s.ResourceDeletePreview(ctx, "agents", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteResourceVersioned(ctx, "agents", "agent-a", active.UpdatedAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("active Agent deletion error=%v", err)
	}

	if err := s.RevokeAgent(ctx, "agent-a", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	stale, err := s.ResourceDeletePreview(ctx, "agents", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollAgent(ctx, AgentRecord{ID: "agent-a", CertificateSerial: "serial-b", CreatedAt: now.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteResourceVersioned(ctx, "agents", "agent-a", stale.UpdatedAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed Agent identity deletion error=%v", err)
	}

	if err := s.RevokeAgent(ctx, "agent-a", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.ResourceDeletePreview(ctx, "agents", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteResourceVersioned(ctx, "agents", "agent-a", fresh.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	var agents, certificates int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id='agent-a'`).Scan(&agents); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_certificates WHERE agent_id='agent-a'`).Scan(&certificates); err != nil {
		t.Fatal(err)
	}
	if agents != 0 || certificates != 0 {
		t.Fatalf("remaining Agent rows=%d certificates=%d", agents, certificates)
	}
}
