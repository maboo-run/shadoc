package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
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
