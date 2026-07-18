package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
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

func TestAgentLeaseCompletionIsAcceptedExactlyOnce(t *testing.T) {
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
	if err := s.CompleteAgentLease(ctx, "lease-1", "agent-1", "succeeded", json.RawMessage(`{"status":"succeeded"}`), now); err != nil {
		t.Fatalf("complete lease: %v", err)
	}
	if err := s.CompleteAgentLease(ctx, "lease-1", "agent-1", "succeeded", json.RawMessage(`{"status":"succeeded"}`), now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second completion error = %v", err)
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
	result := json.RawMessage(`{"status":"succeeded","summary":{"path":"/srv","entries":[]}}`)
	if err := s.CompleteAgentFilesystemRequest(ctx, request.ID, "agent-1", "succeeded", result, now); err != nil {
		t.Fatal(err)
	}
	completed, err := s.AgentFilesystemRequestStatus(ctx, request.ID)
	if err != nil || completed.Status != "succeeded" || string(completed.Result) != string(result) || completed.CompletedAt == nil {
		t.Fatalf("completed=%+v err=%v", completed, err)
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
	result := json.RawMessage(`{"version":1,"assignmentId":"restore-1","agentId":"agent-restore","status":"succeeded"}`)
	if err := s.CompleteAgentRestoreRequest(ctx, request.ID, request.AgentID, "succeeded", result, now); err != nil {
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
	var stoppedAt, uninstalledAt sql.NullString
	if err := s.db.QueryRow(`SELECT stopped_at,uninstalled_at FROM agents WHERE id='agent-1'`).Scan(&stoppedAt, &uninstalledAt); err != nil {
		t.Fatalf("read migrated Agent lifecycle columns: %v", err)
	}
	if stoppedAt.Valid || uninstalledAt.Valid {
		t.Fatalf("legacy Agent unexpectedly stopped or uninstalled: stopped=%v uninstalled=%v", stoppedAt, uninstalledAt)
	}
}
