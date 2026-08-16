package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestAgentLeaseCanBeClaimedExactlyOnce(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if err := s.SaveAgent(ctx, AgentRecord{ID: "agent-1", CertificateSerial: "serial-1", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tasks(id,name,engine,kind,execution_target_json,repository_id,source_json,retention_json,resources_json,exclusions_json,enabled,created_at,updated_at) VALUES('task-1','task','restic','directory','{"kind":"agent","agentId":"agent-1"}',NULL,'{}','{}','{}','[]',1,?,?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	lease := AgentLease{ID: "lease-1", AgentID: "agent-1", TaskID: "task-1", Engine: "restic", Definition: json.RawMessage(`{"source":"/srv"}`), ExpiresAt: now.Add(5 * time.Minute)}
	if err := s.CreateAgentLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentLease(ctx, "agent-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != lease.ID || string(claimed.Definition) != string(lease.Definition) {
		t.Fatalf("claimed=%+v", claimed)
	}
	if _, err := s.ClaimAgentLease(ctx, "agent-1", now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second claim err=%v", err)
	}
}

func TestManagedAgentDataDirectoryPersistsAndCanBeUpdated(t *testing.T) {
	s := openTestStore(t)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	if err := s.SaveAgent(t.Context(), AgentRecord{ID: "agent-data", CertificateSerial: "serial-data", ManagedInstallation: true, AgentDataDir: "/volume1/docker/shadoc-agent", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agents, err := s.ListAgents(t.Context())
	if err != nil || len(agents) != 1 || agents[0].AgentDataDir != "/volume1/docker/shadoc-agent" {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	if err := s.SetManagedAgentDataDir(t.Context(), "agent-data", "/ssd/shadoc-agent"); err != nil {
		t.Fatal(err)
	}
	agents, err = s.ListAgents(t.Context())
	if err != nil || agents[0].AgentDataDir != "/ssd/shadoc-agent" {
		t.Fatalf("updated agents=%+v err=%v", agents, err)
	}
}

func TestAgentDrainBlocksNewWorkAndCountsAlreadyRunningAssignments(t *testing.T) {
	storage := openTestStore(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	if err := storage.SaveAgent(t.Context(), AgentRecord{ID: "agent-1", CertificateSerial: "serial-1", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"task-1", "task-2"} {
		if _, err := storage.db.ExecContext(t.Context(), `INSERT INTO tasks(id,name,engine,kind,execution_target_json,repository_id,source_json,retention_json,resources_json,exclusions_json,enabled,created_at,updated_at) VALUES(?,?,'rsync','rsync','{"kind":"agent","agentId":"agent-1"}',NULL,'{}','{}','{}','[]',1,?,?)`, id, id, formatTime(now), formatTime(now)); err != nil {
			t.Fatal(err)
		}
		if err := storage.CreateAgentLease(t.Context(), AgentLease{ID: "lease-" + id, AgentID: "agent-1", TaskID: id, Engine: "rsync", Definition: json.RawMessage(`{}`), ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := storage.ClaimAgentLease(t.Context(), "agent-1", now); err != nil {
		t.Fatal(err)
	}
	filesystem := AgentFilesystemRequest{ID: "filesystem-running", AgentID: "agent-1", Definition: json.RawMessage(`{"operation":"browse","path":"/srv"}`), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := storage.CreateAgentFilesystemRequest(t.Context(), filesystem); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ClaimAgentFilesystemRequest(t.Context(), "agent-1", now); err != nil {
		t.Fatal(err)
	}
	restore := AgentRestoreRequest{ID: "restore-running", AgentID: "agent-1", Definition: json.RawMessage(`{"snapshotId":"snapshot-1","target":"/srv/restore"}`), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := storage.CreateAgentRestoreRequest(t.Context(), restore); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ClaimAgentRestoreRequest(t.Context(), "agent-1", now); err != nil {
		t.Fatal(err)
	}
	if err := storage.BeginAgentDrain(t.Context(), "agent-1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ClaimAgentLease(t.Context(), "agent-1", now.Add(2*time.Second)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("draining Agent claimed queued lease: %v", err)
	}
	queuedFilesystem := AgentFilesystemRequest{ID: "filesystem-queued", AgentID: "agent-1", Definition: json.RawMessage(`{"operation":"browse","path":"/data"}`), ExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(time.Second)}
	if err := storage.CreateAgentFilesystemRequest(t.Context(), queuedFilesystem); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ClaimAgentFilesystemRequest(t.Context(), "agent-1", now.Add(2*time.Second)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("draining Agent claimed queued filesystem request: %v", err)
	}
	count, err := storage.AgentActiveWorkCount(t.Context(), "agent-1")
	if err != nil || count != 3 {
		t.Fatalf("active work=%d err=%v", count, err)
	}
	if err := storage.EndAgentDrain(t.Context(), "agent-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ClaimAgentLease(t.Context(), "agent-1", now.Add(3*time.Second)); err != nil {
		t.Fatalf("Agent did not resume queued work after drain: %v", err)
	}
}

func TestAgentLeaseCompletionIsIdempotentForTheSameResult(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.SaveAgent(ctx, AgentRecord{ID: "agent-1", CertificateSerial: "1", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tasks(id,name,engine,kind,execution_target_json,repository_id,source_json,retention_json,resources_json,exclusions_json,enabled,created_at,updated_at) VALUES('task-1','task','rsync','directory','{"kind":"agent","agentId":"agent-1"}',NULL,'{}','{}','{}','[]',1,?,?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgentLease(ctx, AgentLease{ID: "lease-1", AgentID: "agent-1", TaskID: "task-1", Engine: "rsync", Definition: json.RawMessage(`{}`), ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimAgentLease(ctx, "agent-1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAgentLease(ctx, "lease-1", "agent-1", "partial", json.RawMessage(`{"status":"partial"}`), now); err != nil {
		t.Fatalf("complete lease: %v", err)
	}
	completed, err := s.AgentLeaseStatus(ctx, "lease-1")
	if err != nil || completed.Status != "partial" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	if err := s.CompleteAgentLease(ctx, "lease-1", "agent-1", "partial", json.RawMessage(`{"status":"partial"}`), now.Add(time.Second)); err != nil {
		t.Fatalf("identical completion retry: %v", err)
	}
	if err := s.CompleteAgentLease(ctx, "lease-1", "agent-1", "succeeded", json.RawMessage(`{"status":"succeeded"}`), now.Add(2*time.Second)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("conflicting completion error = %v", err)
	}
}

func TestAgentLeaseProgressRenewsOnlyTheRunningAssignment(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	if err := s.SaveAgent(ctx, AgentRecord{ID: "agent-1", CertificateSerial: "1", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tasks(id,name,engine,kind,execution_target_json,repository_id,source_json,retention_json,resources_json,exclusions_json,enabled,created_at,updated_at) VALUES('task-progress','task','rsync','rsync','{"kind":"agent","agentId":"agent-1"}',NULL,'{}','{}','{}','[]',1,?,?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgentLease(ctx, AgentLease{ID: "lease-progress", AgentID: "agent-1", TaskID: "task-progress", Engine: "rsync", Definition: json.RawMessage(`{}`), ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimAgentLease(ctx, "agent-1", now); err != nil {
		t.Fatal(err)
	}
	progress := json.RawMessage(`{"phase":"transferring","bytesTransferred":4096,"totalBytes":8192,"percent":50}`)
	renewedAt, expiresAt := now.Add(30*time.Second), now.Add(90*time.Second)
	if err := s.UpdateAgentLeaseProgress(ctx, "lease-progress", "agent-1", progress, renewedAt, expiresAt); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AgentLeaseStatus(ctx, "lease-progress")
	if err != nil {
		t.Fatal(err)
	}
	if lease.RenewedAt == nil || !lease.RenewedAt.Equal(renewedAt) || !lease.ExpiresAt.Equal(expiresAt) || string(lease.Progress) != string(progress) {
		t.Fatalf("renewed lease=%+v", lease)
	}
	active, err := s.ActiveAgentLeaseForTask(ctx, "task-progress")
	if err != nil || active.ID != "lease-progress" || string(active.Progress) != string(progress) {
		t.Fatalf("active task lease=%+v err=%v", active, err)
	}
	if err := s.UpdateAgentLeaseProgress(ctx, "lease-progress", "agent-other", progress, renewedAt, expiresAt); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong Agent renewed assignment: %v", err)
	}
	if err := s.CompleteAgentLease(ctx, "lease-progress", "agent-1", "succeeded", json.RawMessage(`{"status":"succeeded"}`), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAgentLeaseProgress(ctx, "lease-progress", "agent-1", progress, now.Add(2*time.Minute), now.Add(3*time.Minute)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("completed assignment renewed: %v", err)
	}
}

func TestOpenAddsRenewableProgressToLegacyAgentLeases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-agent-lease.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE agent_leases (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  engine TEXT NOT NULL,
  definition_json TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued',
  expires_at TEXT NOT NULL,
  acknowledged_at TEXT,
  completed_at TEXT,
  result_json TEXT NOT NULL DEFAULT '{}'
);`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	storage, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer storage.Close()
	rows, err := storage.db.Query(`PRAGMA table_info(agent_leases)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if !columns["renewed_at"] || !columns["progress_json"] {
		t.Fatalf("migrated columns=%v", columns)
	}
}

func TestOpenAddsRenewalsToLegacyAgentRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-agent-requests.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE agent_filesystem_requests (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, definition_json TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'queued', result_json TEXT NOT NULL DEFAULT '{}', expires_at TEXT NOT NULL, created_at TEXT NOT NULL, completed_at TEXT);
CREATE TABLE agent_restore_requests (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, definition_json TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'queued', result_json TEXT NOT NULL DEFAULT '{}', expires_at TEXT NOT NULL, created_at TEXT NOT NULL, completed_at TEXT);`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	storage, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer storage.Close()
	for _, table := range []string{"agent_filesystem_requests", "agent_restore_requests"} {
		rows, err := storage.db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			found = found || name == "renewed_at"
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("%s missing renewed_at", table)
		}
	}
}

func TestAgentFilesystemRequestLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.SaveAgent(ctx, AgentRecord{ID: "agent-1", CertificateSerial: "1", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	request := AgentFilesystemRequest{ID: "fs-1", AgentID: "agent-1", Definition: json.RawMessage(`{"operation":"browse","path":"/srv"}`), ExpiresAt: now.Add(time.Minute), CreatedAt: now}
	if err := s.CreateAgentFilesystemRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentFilesystemRequest(ctx, "agent-1", now)
	if err != nil || claimed.ID != request.ID || claimed.Status != "running" {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	renewedAt, renewedExpiry := now.Add(10*time.Second), now.Add(2*time.Minute)
	if err := s.RenewAgentFilesystemRequest(ctx, request.ID, "agent-1", renewedAt, renewedExpiry); err != nil {
		t.Fatal(err)
	}
	renewed, err := s.AgentFilesystemRequestStatus(ctx, request.ID)
	if err != nil || renewed.RenewedAt == nil || !renewed.RenewedAt.Equal(renewedAt) || !renewed.ExpiresAt.Equal(renewedExpiry) {
		t.Fatalf("renewed filesystem request=%+v err=%v", renewed, err)
	}
	result := json.RawMessage(`{"status":"succeeded","summary":{"path":"/srv","entries":[]}}`)
	if err := s.CompleteAgentFilesystemRequest(ctx, request.ID, "agent-1", "succeeded", result, now.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	completed, err := s.AgentFilesystemRequestStatus(ctx, request.ID)
	if err != nil || completed.Status != "succeeded" || string(completed.Result) != string(result) || completed.CompletedAt == nil {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}

	expiring := AgentFilesystemRequest{ID: "fs-expiring", AgentID: "agent-1", Definition: request.Definition, ExpiresAt: now.Add(time.Minute), CreatedAt: now.Add(time.Second)}
	if err := s.CreateAgentFilesystemRequest(ctx, expiring); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimAgentFilesystemRequest(ctx, "agent-1", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireAgentFilesystemRequest(ctx, expiring.ID, "inventory timed out", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	expired, err := s.AgentFilesystemRequestStatus(ctx, expiring.ID)
	if err != nil || expired.Status != "failed" || expired.CompletedAt == nil || !strings.Contains(string(expired.Result), "inventory timed out") {
		t.Fatalf("expired=%+v err=%v", expired, err)
	}
}

func TestAgentRestoreRequestLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	if err := s.SaveAgent(ctx, AgentRecord{ID: "agent-restore", CertificateSerial: "restore-serial", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	request := AgentRestoreRequest{ID: "restore-1", AgentID: "agent-restore", Definition: json.RawMessage(`{"repositoryId":"repo","snapshotId":"snap","target":"/srv/new"}`), ExpiresAt: now.Add(time.Minute), CreatedAt: now}
	if err := s.CreateAgentRestoreRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentRestoreRequest(ctx, request.AgentID, now)
	if err != nil || claimed.Status != "running" {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	renewedAt, renewedExpiry := now.Add(10*time.Second), now.Add(2*time.Minute)
	if err := s.RenewAgentRestoreRequest(ctx, request.ID, request.AgentID, renewedAt, renewedExpiry); err != nil {
		t.Fatal(err)
	}
	renewed, err := s.AgentRestoreRequestStatus(ctx, request.ID)
	if err != nil || renewed.RenewedAt == nil || !renewed.RenewedAt.Equal(renewedAt) || !renewed.ExpiresAt.Equal(renewedExpiry) {
		t.Fatalf("renewed restore request=%+v err=%v", renewed, err)
	}
	result := json.RawMessage(`{"version":1,"assignmentId":"restore-1","agentId":"agent-restore","status":"succeeded"}`)
	if err := s.CompleteAgentRestoreRequest(ctx, request.ID, request.AgentID, "succeeded", result, now.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	completed, err := s.AgentRestoreRequestStatus(ctx, request.ID)
	if err != nil || completed.Status != "succeeded" || completed.CompletedAt == nil {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestEnrollmentTokenCanBeConsumedExactlyOnce(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	hash := []byte("token-hash")
	if err := s.SaveAgentEnrollmentToken(ctx, hash, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeAgentEnrollmentToken(ctx, hash, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeAgentEnrollmentToken(ctx, hash, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second consume err=%v", err)
	}
}

func TestAgentRemoteHostBindingReplacesThePriorAgentForTheHost(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, item := range []struct{ id, name string }{{"host-1", "Host 1"}, {"host-2", "Host 2"}} {
		secretID := "key-" + item.id
		if err := s.SaveSecret(ctx, secretID, "ssh-private-key", []byte("cipher"), now); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateRemoteHost(ctx, domain.RemoteHost{ID: item.id, Name: item.name, Host: item.id, Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, secretID); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"agent-1", "agent-2"} {
		if err := s.SaveAgent(ctx, AgentRecord{ID: id, CertificateSerial: "serial-" + id, Status: "online", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	binder, ok := any(s).(interface {
		BindAgentRemoteHost(context.Context, string, string) error
	})
	if !ok {
		t.Fatal("store does not support binding an Agent to its remote host")
	}
	if err := binder.BindAgentRemoteHost(ctx, "agent-1", "host-1"); err != nil {
		t.Fatal(err)
	}
	if err := binder.BindAgentRemoteHost(ctx, "agent-2", "host-1"); err != nil {
		t.Fatal(err)
	}
	var first, second sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT remote_host_id FROM agents WHERE id='agent-1'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT remote_host_id FROM agents WHERE id='agent-2'`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first.Valid || second.String != "host-1" {
		t.Fatalf("bindings agent-1=%v agent-2=%v", first, second)
	}
	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if agents[1].ManagedInstallation {
		t.Fatal("manual remote-host association was incorrectly marked as a managed installation")
	}
}

func TestManagedAgentRemoteHostBindingMarksOnlyDeployedAgentAsManaged(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.SaveSecret(ctx, "key-1", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRemoteHost(ctx, domain.RemoteHost{ID: "host-1", Name: "Host 1", Host: "host-1", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, "key-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAgent(ctx, AgentRecord{ID: "agent-1", CertificateSerial: "serial-1", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindManagedAgentRemoteHost(ctx, "agent-1", "host-1"); err != nil {
		t.Fatal(err)
	}
	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].RemoteHostID != "host-1" || !agents[0].ManagedInstallation {
		t.Fatalf("agent=%+v", agents)
	}
}

func TestEnrollmentResetsManagedHostOwnershipUntilServiceDeploysAgain(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	if err := s.SaveSecret(ctx, "key-1", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRemoteHost(ctx, domain.RemoteHost{ID: "host-1", Name: "Host 1", Host: "host-1", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, "key-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAgent(ctx, AgentRecord{ID: "agent-1", CertificateSerial: "serial-1", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindManagedAgentRemoteHost(ctx, "agent-1", "host-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAgentUninstall(ctx, "agent-1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollAgent(ctx, AgentRecord{ID: "agent-1", CertificateSerial: "serial-2", CreatedAt: now.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	agents, err := s.ListAgents(ctx)
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	if agents[0].RemoteHostID != "" || agents[0].ManagedInstallation {
		t.Fatalf("manual reenrollment retained managed host authority: %+v", agents[0])
	}
	if err := s.ensureAgentManagedInstallation(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureAgentRemoteHosts(ctx); err != nil {
		t.Fatal(err)
	}
	agents, err = s.ListAgents(ctx)
	if err != nil || len(agents) != 1 || agents[0].RemoteHostID != "" || agents[0].ManagedInstallation {
		t.Fatalf("reopen migration restored stale managed authority: agents=%+v err=%v", agents, err)
	}
}

func TestAgentStopAndUninstallStateIsPersistentAndResetByEnrollment(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 14, 47, 37, 0, time.UTC)
	if err := s.SaveAgent(ctx, AgentRecord{ID: "mini-debian", CertificateSerial: "serial-1", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	lifecycle, ok := any(s).(interface {
		MarkAgentStopped(context.Context, string, time.Time) error
		CompleteAgentUninstall(context.Context, string, time.Time) error
	})
	if !ok {
		t.Fatal("store does not persist Agent stop and uninstall state")
	}
	stoppedAt := now.Add(time.Second)
	if err := lifecycle.MarkAgentStopped(ctx, "mini-debian", stoppedAt); err != nil {
		t.Fatal(err)
	}
	var status string
	var stopped, uninstalled, revoked sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT status,stopped_at,uninstalled_at,revoked_at FROM agents WHERE id='mini-debian'`).Scan(&status, &stopped, &uninstalled, &revoked); err != nil {
		t.Fatal(err)
	}
	if status != "offline" || stopped.String != formatTime(stoppedAt) || uninstalled.Valid || revoked.Valid {
		t.Fatalf("stopped state status=%q stopped=%v uninstalled=%v revoked=%v", status, stopped, uninstalled, revoked)
	}

	uninstalledAt := stoppedAt.Add(time.Second)
	if err := lifecycle.CompleteAgentUninstall(ctx, "mini-debian", uninstalledAt); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT status,stopped_at,uninstalled_at,revoked_at FROM agents WHERE id='mini-debian'`).Scan(&status, &stopped, &uninstalled, &revoked); err != nil {
		t.Fatal(err)
	}
	if status != "revoked" || stopped.String != formatTime(stoppedAt) || uninstalled.String != formatTime(uninstalledAt) || revoked.String != formatTime(uninstalledAt) {
		t.Fatalf("uninstalled state status=%q stopped=%v uninstalled=%v revoked=%v", status, stopped, uninstalled, revoked)
	}

	if err := s.SaveAgent(ctx, AgentRecord{ID: "mini-debian", CertificateSerial: "serial-2", Status: "offline", CreatedAt: uninstalledAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT status,stopped_at,uninstalled_at,revoked_at FROM agents WHERE id='mini-debian'`).Scan(&status, &stopped, &uninstalled, &revoked); err != nil {
		t.Fatal(err)
	}
	if status != "offline" || stopped.Valid || uninstalled.Valid || revoked.Valid {
		t.Fatalf("reenrolled state status=%q stopped=%v uninstalled=%v revoked=%v", status, stopped, uninstalled, revoked)
	}
	if err := lifecycle.MarkAgentStopped(ctx, "mini-debian", uninstalledAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatAgent(ctx, "mini-debian", []string{"filesystem-browse"}, uninstalledAt.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT status,stopped_at,uninstalled_at,revoked_at FROM agents WHERE id='mini-debian'`).Scan(&status, &stopped, &uninstalled, &revoked); err != nil {
		t.Fatal(err)
	}
	if status != "online" || stopped.Valid || uninstalled.Valid || revoked.Valid {
		t.Fatalf("heartbeat state status=%q stopped=%v uninstalled=%v revoked=%v", status, stopped, uninstalled, revoked)
	}
}

func TestOpenBackfillsAgentRemoteHostFromSuccessfulDeployment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-agent.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE secrets (id TEXT PRIMARY KEY, purpose TEXT NOT NULL, ciphertext BLOB NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE remote_hosts (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, host TEXT NOT NULL, port INTEGER NOT NULL, username TEXT NOT NULL, private_key_secret_id TEXT NOT NULL REFERENCES secrets(id), host_fingerprint TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE agents (id TEXT PRIMARY KEY, certificate_serial TEXT NOT NULL UNIQUE, capabilities_json TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL DEFAULT 'offline', last_heartbeat_at TEXT, created_at TEXT NOT NULL, revoked_at TEXT);
CREATE TABLE operations (id TEXT PRIMARY KEY, kind TEXT NOT NULL, actor TEXT NOT NULL, repository_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '', snapshot_id TEXT NOT NULL DEFAULT '', target TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, stage TEXT NOT NULL, created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT, attempt_count INTEGER NOT NULL DEFAULT 0, error_summary TEXT NOT NULL DEFAULT '', detail_json TEXT NOT NULL DEFAULT '{}');
INSERT INTO secrets VALUES ('key','ssh-private-key',X'01','2026-07-13T00:00:00Z','2026-07-13T00:00:00Z');
INSERT INTO remote_hosts VALUES ('host-1','Host 1','host-1',22,'backup','key','known','2026-07-13T00:00:00Z','2026-07-13T00:00:00Z');
INSERT INTO agents VALUES ('agent-1','serial-1','["filesystem-browse"]','online','2026-07-13T00:02:00Z','2026-07-13T00:01:00Z',NULL);
INSERT INTO operations VALUES ('deploy-1','agent_deploy','admin','','','','agent-1','success','completed','2026-07-13T00:00:00Z','2026-07-13T00:00:10Z','2026-07-13T00:02:00Z',1,'','{"hostId":"host-1"}');
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
	var hostID sql.NullString
	if err := s.db.QueryRow(`SELECT remote_host_id FROM agents WHERE id='agent-1'`).Scan(&hostID); err != nil {
		t.Fatal(err)
	}
	if hostID.String != "host-1" {
		t.Fatalf("backfilled remote host=%v", hostID)
	}
	var managed bool
	if err := s.db.QueryRow(`SELECT managed_installation FROM agents WHERE id='agent-1'`).Scan(&managed); err != nil {
		t.Fatal(err)
	}
	if !managed {
		t.Fatal("historically deployed Agent was not marked as managed")
	}
	var stoppedAt, uninstalledAt sql.NullString
	if err := s.db.QueryRow(`SELECT stopped_at,uninstalled_at FROM agents WHERE id='agent-1'`).Scan(&stoppedAt, &uninstalledAt); err != nil {
		t.Fatalf("read migrated Agent lifecycle columns: %v", err)
	}
	if stoppedAt.Valid || uninstalledAt.Valid {
		t.Fatalf("legacy Agent unexpectedly stopped or uninstalled: stopped=%v uninstalled=%v", stoppedAt, uninstalledAt)
	}
}

func TestEnsureAgentRemoteHostsClearsDanglingBinding(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SaveAgent(ctx, AgentRecord{
		ID: "agent-a", CertificateSerial: "serial-a", Status: "offline",
		CreatedAt: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET remote_host_id='deleted-host' WHERE id='agent-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}

	if err := s.ensureAgentRemoteHosts(ctx); err != nil {
		t.Fatal(err)
	}
	var binding sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT remote_host_id FROM agents WHERE id='agent-a'`).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if binding.Valid {
		t.Fatalf("dangling remote host binding was not cleared: %q", binding.String)
	}
}
