package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store persists application state in a single SQLite database. It owns schema
// migration and connection-level safety settings so callers never have to know
// SQLite details.
type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) IsInitialized(ctx context.Context) (bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = 'initialized'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read initialization state: %w", err)
	}
	return value == "true", nil
}

func (s *Store) MarkInitialized(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO metadata(key, value) VALUES('initialized', 'true')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`)
	if err != nil {
		return fmt.Errorf("write initialization state: %w", err)
	}
	return nil
}
func (s *Store) SetMetadata(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}
func (s *Store) Metadata(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key=?`, key).Scan(&value)
	return value, err
}

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA journal_mode = WAL;

CREATE TABLE IF NOT EXISTS metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS secrets (
    id TEXT PRIMARY KEY,
    purpose TEXT NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS administrators (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash BLOB PRIMARY KEY,
    csrf_hash BLOB NOT NULL,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS remote_hosts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    host TEXT NOT NULL,
    port INTEGER NOT NULL,
    username TEXT NOT NULL,
    private_key_secret_id TEXT NOT NULL REFERENCES secrets(id),
    host_fingerprint TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS repositories (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
	engine TEXT NOT NULL DEFAULT 'restic',
    kind TEXT NOT NULL DEFAULT 'sftp',
    remote_host_id TEXT REFERENCES remote_hosts(id),
	path TEXT NOT NULL,
	password_secret_id TEXT REFERENCES secrets(id),
	backend_json TEXT NOT NULL DEFAULT '',
	backend_secret_id TEXT REFERENCES secrets(id),
	status TEXT NOT NULL DEFAULT 'ready',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS repository_key_revocations (
    repository_id TEXT PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL,
    secret_id TEXT NOT NULL REFERENCES secrets(id),
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS repository_capacities (
    repository_id TEXT PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    total_bytes INTEGER NOT NULL,
    available_bytes INTEGER NOT NULL,
    checked_at TEXT NOT NULL,
    source_agent_id TEXT
);

CREATE TABLE IF NOT EXISTS repository_capacity_policies (
    repository_id TEXT PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    enabled INTEGER NOT NULL DEFAULT 1,
    probe_interval_minutes INTEGER NOT NULL DEFAULT 360,
    minimum_available_bytes INTEGER NOT NULL DEFAULT 0,
    minimum_available_percent REAL NOT NULL DEFAULT 10,
    exhaustion_warning_days INTEGER NOT NULL DEFAULT 30,
    next_probe_at TEXT,
    last_attempt_at TEXT,
    last_success_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    claim_token TEXT NOT NULL DEFAULT '',
    claim_until TEXT,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS repository_capacity_samples (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    repository_id TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    total_bytes INTEGER NOT NULL,
    available_bytes INTEGER NOT NULL,
    checked_at TEXT NOT NULL,
    source_agent_id TEXT
);
CREATE INDEX IF NOT EXISTS repository_capacity_samples_repository_time
ON repository_capacity_samples(repository_id, checked_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS repository_capacity_policies_due
ON repository_capacity_policies(enabled, next_probe_at, claim_until, repository_id);

CREATE TABLE IF NOT EXISTS database_connections (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    engine TEXT NOT NULL,
    purpose TEXT NOT NULL,
    network TEXT NOT NULL,
    host TEXT,
    port INTEGER,
    socket_path TEXT,
    username TEXT NOT NULL,
    password_secret_id TEXT NOT NULL REFERENCES secrets(id),
    tls_json TEXT NOT NULL DEFAULT '{}',
    tool_paths_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'draft',
    preflight_checked_at TEXT,
    preflight_client_version TEXT NOT NULL DEFAULT '',
    preflight_server_version TEXT NOT NULL DEFAULT '',
    preflight_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    engine TEXT NOT NULL DEFAULT 'restic',
    kind TEXT NOT NULL,
    execution_target_json TEXT NOT NULL DEFAULT '{"kind":"local"}',
    repository_id TEXT UNIQUE REFERENCES repositories(id),
    source_json TEXT NOT NULL,
    retention_json TEXT NOT NULL DEFAULT '{}',
    resources_json TEXT NOT NULL DEFAULT '{}',
	health_policy_json TEXT NOT NULL DEFAULT '{}',
    exclusions_json TEXT NOT NULL DEFAULT '[]',
	scope_confirmation_json TEXT NOT NULL DEFAULT '{}',
    enabled INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
    id TEXT PRIMARY KEY,
	remote_host_id TEXT REFERENCES remote_hosts(id) ON DELETE SET NULL,
    certificate_serial TEXT NOT NULL UNIQUE,
	certificate_not_after TEXT,
    capabilities_json TEXT NOT NULL DEFAULT '[]',
	build_version TEXT NOT NULL DEFAULT '',
	protocol_min INTEGER NOT NULL DEFAULT 0,
	protocol_max INTEGER NOT NULL DEFAULT 0,
	platform_os TEXT NOT NULL DEFAULT '',
	platform_arch TEXT NOT NULL DEFAULT '',
	restic_version TEXT NOT NULL DEFAULT '',
	rsync_version TEXT NOT NULL DEFAULT '',
	service_url TEXT NOT NULL DEFAULT '',
	renewal_status TEXT NOT NULL DEFAULT '',
	draining_at TEXT,
    status TEXT NOT NULL DEFAULT 'offline',
    last_heartbeat_at TEXT,
    created_at TEXT NOT NULL,
    revoked_at TEXT,
    stopped_at TEXT,
    uninstalled_at TEXT
);
CREATE TABLE IF NOT EXISTS agent_certificates (
	serial TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	not_before TEXT,
	not_after TEXT,
	status TEXT NOT NULL,
	issued_at TEXT NOT NULL,
	activated_at TEXT,
	retired_at TEXT
);
CREATE INDEX IF NOT EXISTS agent_certificates_agent_status
ON agent_certificates(agent_id,status,not_after);
CREATE TABLE IF NOT EXISTS agent_filesystem_requests (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    definition_json TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    result_json TEXT NOT NULL DEFAULT '{}',
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    completed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_agent_filesystem_requests_claim ON agent_filesystem_requests(agent_id,status,expires_at,created_at);
CREATE INDEX IF NOT EXISTS idx_agent_filesystem_requests_expiry ON agent_filesystem_requests(expires_at);

CREATE TABLE IF NOT EXISTS agent_restore_requests (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    definition_json TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    result_json TEXT NOT NULL DEFAULT '{}',
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    completed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_agent_restore_requests_claim ON agent_restore_requests(agent_id,status,expires_at,created_at);
CREATE INDEX IF NOT EXISTS idx_agent_restore_requests_expiry ON agent_restore_requests(expires_at);

CREATE TABLE IF NOT EXISTS agent_enrollment_tokens (
    token_hash BLOB PRIMARY KEY,
    expires_at TEXT NOT NULL,
    consumed_at TEXT
);

CREATE TABLE IF NOT EXISTS agent_service_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL,
    listen_host TEXT NOT NULL,
    port INTEGER NOT NULL,
    advertised_host TEXT NOT NULL,
    tls_names_json TEXT NOT NULL DEFAULT '[]',
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_leases (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    engine TEXT NOT NULL,
    definition_json TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    expires_at TEXT NOT NULL,
    acknowledged_at TEXT,
    completed_at TEXT,
    result_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS agent_leases_agent_active ON agent_leases(agent_id, expires_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS agent_leases_task_active ON agent_leases(task_id) WHERE completed_at IS NULL;

CREATE TABLE IF NOT EXISTS plans (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    schedule_json TEXT NOT NULL,
    timezone TEXT NOT NULL,
    max_parallel INTEGER NOT NULL DEFAULT 1,
    enabled INTEGER NOT NULL DEFAULT 0,
    catch_up_window_minutes INTEGER NOT NULL DEFAULT 60,
    schedule_anchor_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plan_tasks (
    plan_id TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(plan_id, task_id)
);

CREATE TABLE IF NOT EXISTS runs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    plan_id TEXT REFERENCES plans(id),
    trigger TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    snapshot_id TEXT,
    summary_json TEXT NOT NULL DEFAULT '{}',
    raw_log TEXT NOT NULL DEFAULT '',
    raw_log_expired INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER,
    files_processed INTEGER,
    files_changed INTEGER,
    bytes_processed INTEGER,
    bytes_changed INTEGER
);

CREATE TABLE IF NOT EXISTS operations (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    actor TEXT NOT NULL,
    repository_id TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL DEFAULT '',
    snapshot_id TEXT NOT NULL DEFAULT '',
    target TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    stage TEXT NOT NULL,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    error_summary TEXT NOT NULL DEFAULT '',
    detail_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS runs_activity_order ON runs(started_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS runs_activity_filter ON runs(task_id,status,trigger,started_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS runs_activity_status_trigger ON runs(status,trigger,started_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS runs_task_trends ON runs(task_id,started_at DESC,status);
CREATE INDEX IF NOT EXISTS operations_activity_order ON operations(created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS operations_activity_filter ON operations(task_id,repository_id,kind,status,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS operations_activity_kind_status ON operations(kind,status,created_at DESC,id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS operations_one_active_application_update
ON operations(kind) WHERE kind='application_update' AND status IN ('queued','running');
DROP INDEX IF EXISTS operations_created_at;
DROP INDEX IF EXISTS operations_kind_status;

CREATE TABLE IF NOT EXISTS audits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at TEXT NOT NULL,
    actor TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT,
    detail_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS lifecycle_policy (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    run_days INTEGER NOT NULL,
    raw_log_days INTEGER NOT NULL,
    audit_days INTEGER NOT NULL,
    raw_log_max_bytes INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT OR IGNORE INTO lifecycle_policy(id, run_days, raw_log_days, audit_days, raw_log_max_bytes, updated_at)
VALUES(1, 365, 30, 365, 1073741824, '1970-01-01T00:00:00Z');

CREATE TABLE IF NOT EXISTS notifications (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at TEXT NOT NULL,
    kind TEXT NOT NULL,
    severity TEXT NOT NULL,
    state_key TEXT NOT NULL,
    message TEXT NOT NULL,
    delivered_at TEXT
);
CREATE TABLE IF NOT EXISTS alert_states (
    state_key TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    severity TEXT NOT NULL,
    status TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    object_name TEXT NOT NULL,
    reason TEXT NOT NULL,
    message TEXT NOT NULL,
    target_page TEXT NOT NULL,
    recovery_condition TEXT NOT NULL,
    first_at TEXT NOT NULL,
    last_at TEXT NOT NULL,
    resolved_at TEXT,
    occurrence_count INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS alert_states_status_severity
ON alert_states(status, severity, last_at DESC);
CREATE TABLE IF NOT EXISTS alert_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at TEXT NOT NULL,
    state_key TEXT NOT NULL,
    transition TEXT NOT NULL,
    kind TEXT NOT NULL,
    severity TEXT NOT NULL,
    status TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    object_name TEXT NOT NULL,
    reason TEXT NOT NULL,
    message TEXT NOT NULL,
    target_page TEXT NOT NULL,
    recovery_condition TEXT NOT NULL,
    occurrence_count INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS alert_events_occurred_at
ON alert_events(occurred_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS notification_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    notification_id TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    channel TEXT NOT NULL,
    state_key TEXT NOT NULL,
    transition TEXT NOT NULL,
    attempt INTEGER NOT NULL,
    max_attempts INTEGER NOT NULL,
    status TEXT NOT NULL,
    error_summary TEXT NOT NULL DEFAULT '',
    delivered_at TEXT
);
CREATE INDEX IF NOT EXISTS notification_deliveries_occurred_at
ON notification_deliveries(occurred_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS repository_maintenance (
    repository_id TEXT PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    schedule_json TEXT NOT NULL,
    timezone TEXT NOT NULL,
    retention_json TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    catch_up_window_minutes INTEGER NOT NULL DEFAULT 60,
    schedule_anchor_at TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS schedule_occurrences (
    id TEXT PRIMARY KEY,
    owner_kind TEXT NOT NULL CHECK(owner_kind IN ('plan','maintenance','restore_verification')),
    owner_id TEXT NOT NULL,
    scheduled_at TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    mode TEXT NOT NULL CHECK(mode IN ('on_time','catch_up','missed')),
    status TEXT NOT NULL,
    target_ids_json TEXT NOT NULL DEFAULT '[]',
    run_ids_json TEXT NOT NULL DEFAULT '[]',
    started_at TEXT,
    finished_at TEXT,
    UNIQUE(owner_kind, owner_id, scheduled_at)
);
CREATE INDEX IF NOT EXISTS schedule_occurrences_owner_time
ON schedule_occurrences(owner_kind, owner_id, scheduled_at DESC);
CREATE TABLE IF NOT EXISTS maintenance_previews (
    id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    retention_json TEXT NOT NULL,
    keep_count INTEGER NOT NULL DEFAULT 0,
    remove_count INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    consumed_at TEXT
);
CREATE INDEX IF NOT EXISTS maintenance_previews_repository_created
ON maintenance_previews(repository_id, created_at DESC);
CREATE TABLE IF NOT EXISTS task_scope_previews (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    summary_json TEXT NOT NULL DEFAULT '{}',
    requires_delete_confirmation INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    consumed_at TEXT
);
CREATE INDEX IF NOT EXISTS task_scope_previews_task_created
ON task_scope_previews(task_id, created_at DESC);
CREATE TABLE IF NOT EXISTS controlplane_import_previews (
    id TEXT PRIMARY KEY,
    bundle_sha256 TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    consumed_at TEXT
);
CREATE INDEX IF NOT EXISTS controlplane_import_previews_expiry
ON controlplane_import_previews(expires_at);
CREATE TABLE IF NOT EXISTS restore_confirmations (
    id TEXT PRIMARY KEY,
    actor TEXT NOT NULL,
    kind TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    summary_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    authorized_at TEXT,
    consumed_at TEXT
);
CREATE TABLE IF NOT EXISTS snapshot_metadata (
    repository_id TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    snapshot_id TEXT NOT NULL,
    metadata_version INTEGER NOT NULL,
    engine TEXT NOT NULL,
    database_name TEXT NOT NULL,
    format TEXT NOT NULL,
    filename TEXT NOT NULL,
    server_version TEXT NOT NULL,
    client_version TEXT NOT NULL,
    encoding TEXT NOT NULL,
    collation TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(repository_id, snapshot_id)
);
CREATE TABLE IF NOT EXISTS restore_verification_policies (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    schedule_json TEXT NOT NULL,
    timezone TEXT NOT NULL,
    selection_path TEXT NOT NULL,
    maximum_bytes INTEGER NOT NULL,
    maximum_success_age_hours INTEGER NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 0,
    catch_up_window_minutes INTEGER NOT NULL DEFAULT 60,
    schedule_anchor_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS restore_verifications (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    repository_id TEXT NOT NULL,
    snapshot_id TEXT NOT NULL,
    selection_path TEXT NOT NULL,
    trigger TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    file_count INTEGER NOT NULL DEFAULT 0,
    byte_count INTEGER NOT NULL DEFAULT 0,
    manifest_sha256 TEXT NOT NULL DEFAULT '',
    cleanup_status TEXT NOT NULL DEFAULT 'pending',
    error_summary TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS restore_verifications_task_started
ON restore_verifications(task_id, started_at DESC);
CREATE TABLE IF NOT EXISTS protection_templates (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    retention_json TEXT NOT NULL,
    resources_json TEXT NOT NULL,
    health_json TEXT NOT NULL DEFAULT '{}',
    schedule_json TEXT NOT NULL,
    timezone TEXT NOT NULL,
    max_parallel INTEGER NOT NULL,
    catch_up_window_minutes INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS protection_drafts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    template_id TEXT NOT NULL DEFAULT '',
    execution_target_json TEXT NOT NULL,
    retention_json TEXT NOT NULL,
    resources_json TEXT NOT NULL,
    health_json TEXT NOT NULL DEFAULT '{}',
    schedule_json TEXT NOT NULL,
    timezone TEXT NOT NULL,
    max_parallel INTEGER NOT NULL,
    catch_up_window_minutes INTEGER NOT NULL,
    notification_mode TEXT NOT NULL,
    plan_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS protection_drafts_status_updated
ON protection_drafts(status, updated_at DESC);
CREATE TABLE IF NOT EXISTS protection_draft_items (
    id TEXT PRIMARY KEY,
    draft_id TEXT NOT NULL REFERENCES protection_drafts(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    task_name TEXT NOT NULL,
    source_kind TEXT NOT NULL,
    source_json TEXT NOT NULL,
    repository_id TEXT NOT NULL UNIQUE,
    repository_name TEXT NOT NULL,
    repository_kind TEXT NOT NULL,
    remote_host_id TEXT NOT NULL DEFAULT '',
    repository_path TEXT NOT NULL,
    repository_password_secret_id TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL,
    error_summary TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    UNIQUE(draft_id, position),
    UNIQUE(draft_id, repository_kind, remote_host_id, repository_path)
);
CREATE INDEX IF NOT EXISTS protection_draft_items_draft_position
ON protection_draft_items(draft_id, position);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate sqlite database: %w", err)
	}
	if err := s.ensureSchedulePersistence(ctx); err != nil {
		return err
	}
	if err := s.ensureRestoreVerificationScheduleKind(ctx); err != nil {
		return err
	}
	if err := s.ensureAuditActor(ctx); err != nil {
		return err
	}
	if err := s.ensureDatabasePreflightColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureRunLogExpired(ctx); err != nil {
		return err
	}
	if err := s.ensureRunMetricColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureCanonicalRunStatuses(ctx); err != nil {
		return err
	}
	if err := s.migrateRepositoryKinds(ctx); err != nil {
		return err
	}
	if err := s.ensureRepositoryBackendColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureRepositoryCapacityPersistence(ctx); err != nil {
		return err
	}
	if err := s.migrateTaskExecutions(ctx); err != nil {
		return err
	}
	if err := s.ensureRepositoryRetentionAuthority(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE restore_verification_policies SET enabled=0 WHERE enabled<>0`); err != nil {
		return fmt.Errorf("disable retired automatic restore verification: %w", err)
	}
	if err := s.ensureTaskScopeConfirmation(ctx); err != nil {
		return err
	}
	if err := s.ensureTaskHealthPolicy(ctx); err != nil {
		return err
	}
	if err := s.ensureAgentLeaseResult(ctx); err != nil {
		return err
	}
	if err := s.ensureAgentRemoteHosts(ctx); err != nil {
		return err
	}
	if err := s.ensureAgentLifecycle(ctx); err != nil {
		return err
	}
	if err := s.ensureAgentRuntimeMetadata(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
CREATE UNIQUE INDEX IF NOT EXISTS repositories_sftp_location
ON repositories(remote_host_id, path) WHERE kind = 'sftp';
CREATE UNIQUE INDEX IF NOT EXISTS repositories_local_path
ON repositories(path) WHERE kind = 'local';
CREATE UNIQUE INDEX IF NOT EXISTS repositories_s3_location
ON repositories(json_extract(backend_json,'$.endpoint'),json_extract(backend_json,'$.bucket'),path) WHERE kind = 's3';
`)
	if err != nil {
		return fmt.Errorf("create repository location indexes: %w", err)
	}
	return nil
}

func (s *Store) ensureAgentRuntimeMetadata(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(agents)`)
	if err != nil {
		return fmt.Errorf("inspect Agent runtime metadata columns: %w", err)
	}
	present := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		present[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	columns := []struct{ name, definition string }{
		{"certificate_not_after", "TEXT"},
		{"build_version", "TEXT NOT NULL DEFAULT ''"},
		{"protocol_min", "INTEGER NOT NULL DEFAULT 0"},
		{"protocol_max", "INTEGER NOT NULL DEFAULT 0"},
		{"platform_os", "TEXT NOT NULL DEFAULT ''"},
		{"platform_arch", "TEXT NOT NULL DEFAULT ''"},
		{"restic_version", "TEXT NOT NULL DEFAULT ''"},
		{"rsync_version", "TEXT NOT NULL DEFAULT ''"},
		{"service_url", "TEXT NOT NULL DEFAULT ''"},
		{"renewal_status", "TEXT NOT NULL DEFAULT ''"},
		{"draining_at", "TEXT"},
	}
	for _, column := range columns {
		if present[column.name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE agents ADD COLUMN `+column.name+` `+column.definition); err != nil {
			return fmt.Errorf("add Agent runtime metadata column %s: %w", column.name, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS agent_certificates (
	serial TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	not_before TEXT,
	not_after TEXT,
	status TEXT NOT NULL,
	issued_at TEXT NOT NULL,
	activated_at TEXT,
	retired_at TEXT
);
CREATE INDEX IF NOT EXISTS agent_certificates_agent_status
ON agent_certificates(agent_id,status,not_after);
INSERT INTO agent_certificates(serial,agent_id,not_after,status,issued_at,activated_at)
SELECT certificate_serial,id,certificate_not_after,'active',created_at,created_at
FROM agents
WHERE certificate_serial<>''
ON CONFLICT(serial) DO NOTHING;
`); err != nil {
		return fmt.Errorf("create and backfill Agent certificate history: %w", err)
	}
	return nil
}

func (s *Store) ensureRunMetricColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(runs)`)
	if err != nil {
		return fmt.Errorf("inspect run metric columns: %w", err)
	}
	present := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		present[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range []string{"duration_ms", "files_processed", "files_changed", "bytes_processed", "bytes_changed"} {
		if present[column] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN `+column+` INTEGER`); err != nil {
			return fmt.Errorf("add run metric column %s: %w", column, err)
		}
	}
	return nil
}

func (s *Store) ensureRepositoryRetentionAuthority(ctx context.Context) error {
	const migrationKey = "repository_retention_authority_v1"
	var complete string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key=?`, migrationKey).Scan(&complete)
	if err == nil && complete == "true" {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO repository_maintenance(repository_id,schedule_json,timezone,retention_json,enabled,catch_up_window_minutes,schedule_anchor_at,updated_at)
		SELECT t.repository_id,'{"kind":"weekly","dayOfWeek":0,"timeOfDay":"03:00"}','UTC',t.retention_json,0,60,t.updated_at,t.updated_at
		FROM tasks t
		WHERE t.engine='restic' AND t.repository_id IS NOT NULL
		ON CONFLICT(repository_id) DO UPDATE SET retention_json=excluded.retention_json
	`); err != nil {
		return fmt.Errorf("move task retention to repository maintenance: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET retention_json='{}' WHERE engine='restic'`); err != nil {
		return fmt.Errorf("clear retired task retention copies: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?, 'true') ON CONFLICT(key) DO UPDATE SET value='true'`, migrationKey); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureCanonicalRunStatuses(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status='success' WHERE status='succeeded'`); err != nil {
		return fmt.Errorf("normalize legacy run statuses: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE schedule_occurrences SET status='success' WHERE status='succeeded'`); err != nil {
		return fmt.Errorf("normalize legacy schedule statuses: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ensureTaskScopeConfirmation(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(tasks)`)
	if err != nil {
		return fmt.Errorf("inspect task scope confirmation column: %w", err)
	}
	present := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		present = present || name == "scope_confirmation_json"
	}
	if err := rows.Close(); err != nil || present {
		return err
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN scope_confirmation_json TEXT NOT NULL DEFAULT '{}'`)
	return err
}

func (s *Store) ensureTaskHealthPolicy(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(tasks)`)
	if err != nil {
		return fmt.Errorf("inspect task health policy column: %w", err)
	}
	present := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		present = present || name == "health_policy_json"
	}
	if err := rows.Close(); err != nil || present {
		return err
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN health_policy_json TEXT NOT NULL DEFAULT '{}'`)
	return err
}

func (s *Store) ensureSchedulePersistence(ctx context.Context) error {
	migrationTime := formatTime(time.Now().UTC())
	for _, table := range []string{"plans", "repository_maintenance"} {
		rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
		if err != nil {
			return fmt.Errorf("inspect %s schedule columns: %w", table, err)
		}
		present := map[string]bool{}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				return err
			}
			present[name] = true
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if !present["catch_up_window_minutes"] {
			if _, err := s.db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN catch_up_window_minutes INTEGER NOT NULL DEFAULT 60`); err != nil {
				return fmt.Errorf("add %s catch-up window: %w", table, err)
			}
		}
		if !present["schedule_anchor_at"] {
			if _, err := s.db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN schedule_anchor_at TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("add %s schedule anchor: %w", table, err)
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET schedule_anchor_at=? WHERE schedule_anchor_at IS NULL OR schedule_anchor_at=''`, migrationTime); err != nil {
			return fmt.Errorf("backfill %s schedule anchor: %w", table, err)
		}
	}
	return nil
}

func (s *Store) ensureRestoreVerificationScheduleKind(ctx context.Context) error {
	var definition string
	if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='schedule_occurrences'`).Scan(&definition); err != nil {
		return fmt.Errorf("inspect schedule occurrence owner kinds: %w", err)
	}
	if strings.Contains(strings.ToLower(definition), "restore_verification") {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE schedule_occurrences_v2 (
    id TEXT PRIMARY KEY,
    owner_kind TEXT NOT NULL CHECK(owner_kind IN ('plan','maintenance','restore_verification')),
    owner_id TEXT NOT NULL,
    scheduled_at TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    mode TEXT NOT NULL CHECK(mode IN ('on_time','catch_up','missed')),
    status TEXT NOT NULL,
    target_ids_json TEXT NOT NULL DEFAULT '[]',
    run_ids_json TEXT NOT NULL DEFAULT '[]',
    started_at TEXT,
    finished_at TEXT,
    UNIQUE(owner_kind, owner_id, scheduled_at)
);
INSERT INTO schedule_occurrences_v2(
    id,owner_kind,owner_id,scheduled_at,observed_at,mode,status,target_ids_json,run_ids_json,started_at,finished_at
)
SELECT id,owner_kind,owner_id,scheduled_at,observed_at,mode,status,target_ids_json,run_ids_json,started_at,finished_at
FROM schedule_occurrences;
DROP TABLE schedule_occurrences;
ALTER TABLE schedule_occurrences_v2 RENAME TO schedule_occurrences;
CREATE INDEX schedule_occurrences_owner_time
ON schedule_occurrences(owner_kind, owner_id, scheduled_at DESC);
`); err != nil {
		return fmt.Errorf("migrate schedule occurrence owner kinds: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ensureAgentLifecycle(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(agents)`)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		present[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range []string{"stopped_at", "uninstalled_at"} {
		if present[column] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE agents ADD COLUMN `+column+` TEXT`); err != nil {
			return fmt.Errorf("add Agent lifecycle column %s: %w", column, err)
		}
	}
	return nil
}

func (s *Store) ensureAgentRemoteHosts(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(agents)`)
	if err != nil {
		return err
	}
	present := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		present = present || name == "remote_host_id"
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !present {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE agents ADD COLUMN remote_host_id TEXT REFERENCES remote_hosts(id) ON DELETE SET NULL`); err != nil {
			return fmt.Errorf("add Agent remote host binding: %w", err)
		}
	}

	type deployment struct{ agentID, detail string }
	deploymentRows, err := s.db.QueryContext(ctx, `SELECT target,detail_json FROM operations WHERE kind='agent_deploy' AND status='success' ORDER BY COALESCE(finished_at,created_at) DESC,created_at DESC`)
	if err != nil {
		return err
	}
	deployments := make([]deployment, 0)
	for deploymentRows.Next() {
		var item deployment
		if err := deploymentRows.Scan(&item.agentID, &item.detail); err != nil {
			_ = deploymentRows.Close()
			return err
		}
		deployments = append(deployments, item)
	}
	if err := deploymentRows.Close(); err != nil {
		return err
	}

	existingRows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(remote_host_id,'') FROM agents`)
	if err != nil {
		return err
	}
	seenAgents, usedHosts := map[string]bool{}, map[string]bool{}
	for existingRows.Next() {
		var agentID, hostID string
		if err := existingRows.Scan(&agentID, &hostID); err != nil {
			_ = existingRows.Close()
			return err
		}
		if hostID != "" {
			seenAgents[agentID], usedHosts[hostID] = true, true
		}
	}
	if err := existingRows.Close(); err != nil {
		return err
	}
	for _, item := range deployments {
		if item.agentID == "" || seenAgents[item.agentID] {
			continue
		}
		seenAgents[item.agentID] = true
		var detail struct {
			HostID string `json:"hostId"`
		}
		if json.Unmarshal([]byte(item.detail), &detail) != nil || detail.HostID == "" || usedHosts[detail.HostID] {
			continue
		}
		result, err := s.db.ExecContext(ctx, `UPDATE agents SET remote_host_id=? WHERE id=? AND remote_host_id IS NULL AND EXISTS(SELECT 1 FROM remote_hosts WHERE id=?)`, detail.HostID, item.agentID, detail.HostID)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count == 1 {
			usedHosts[detail.HostID] = true
		}
	}
	if _, err := s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS agents_remote_host ON agents(remote_host_id) WHERE remote_host_id IS NOT NULL`); err != nil {
		return fmt.Errorf("create Agent remote host binding index: %w", err)
	}
	return nil
}

func (s *Store) ensureAgentLeaseResult(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(agent_leases)`)
	if err != nil {
		return err
	}
	present := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		present = present || name == "result_json"
	}
	if err := rows.Close(); err != nil || present {
		return err
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE agent_leases ADD COLUMN result_json TEXT NOT NULL DEFAULT '{}'`)
	return err
}

func (s *Store) migrateTaskExecutions(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(tasks)`)
	if err != nil {
		return fmt.Errorf("inspect task schema: %w", err)
	}
	hasEngine, hasTarget, repositoryRequired := false, false, false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan task schema: %w", err)
		}
		hasEngine = hasEngine || name == "engine"
		hasTarget = hasTarget || name == "execution_target_json"
		repositoryRequired = repositoryRequired || (name == "repository_id" && notNull != 0)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasEngine && hasTarget && !repositoryRequired {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer s.db.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE tasks_v3 (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    engine TEXT NOT NULL DEFAULT 'restic',
    kind TEXT NOT NULL,
    execution_target_json TEXT NOT NULL DEFAULT '{"kind":"local"}',
    repository_id TEXT UNIQUE REFERENCES repositories(id),
    source_json TEXT NOT NULL,
    retention_json TEXT NOT NULL DEFAULT '{}',
    resources_json TEXT NOT NULL DEFAULT '{}',
    exclusions_json TEXT NOT NULL DEFAULT '[]',
    enabled INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);`); err != nil {
		return fmt.Errorf("create task migration table: %w", err)
	}
	engine := "'restic'"
	if hasEngine {
		engine = "engine"
	}
	target := "'{\"kind\":\"local\"}'"
	if hasTarget {
		target = "execution_target_json"
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO tasks_v3(id,name,engine,kind,execution_target_json,repository_id,source_json,retention_json,resources_json,exclusions_json,enabled,created_at,updated_at)
SELECT id,name,`+engine+`,kind,`+target+`,repository_id,source_json,retention_json,resources_json,exclusions_json,enabled,created_at,updated_at FROM tasks`); err != nil {
		return fmt.Errorf("copy tasks during migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE tasks`); err != nil {
		return fmt.Errorf("replace task table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE tasks_v3 RENAME TO tasks`); err != nil {
		return fmt.Errorf("rename task migration table: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task migration: %w", err)
	}
	return nil
}

func (s *Store) ensureRunLogExpired(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(runs)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	present := false
	for rows.Next() {
		var cid int
		var name, kind string
		var notnull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		present = present || name == "raw_log_expired"
	}
	if present {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN raw_log_expired INTEGER NOT NULL DEFAULT 0`)
	return err
}

func (s *Store) ensureDatabasePreflightColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(database_connections)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, kind string
		var notnull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		present[name] = true
	}
	columns := []struct{ name, definition string }{
		{"status", `TEXT NOT NULL DEFAULT 'draft'`},
		{"preflight_checked_at", `TEXT`},
		{"preflight_client_version", `TEXT NOT NULL DEFAULT ''`},
		{"preflight_server_version", `TEXT NOT NULL DEFAULT ''`},
		{"preflight_error", `TEXT NOT NULL DEFAULT ''`},
	}
	for _, column := range columns {
		if present[column.name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE database_connections ADD COLUMN `+column.name+` `+column.definition); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureAuditActor(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(audits)`)
	if err != nil {
		return err
	}
	hasActor := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		hasActor = hasActor || name == "actor"
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasActor {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE audits ADD COLUMN actor TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migrate audit actor: %w", err)
	}
	return nil
}

func (s *Store) migrateRepositoryKinds(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(repositories)`)
	if err != nil {
		return fmt.Errorf("inspect repository schema: %w", err)
	}
	hasKind, hasEngine := false, false
	remoteHostRequired, passwordRequired := false, false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan repository schema: %w", err)
		}
		hasKind = hasKind || name == "kind"
		hasEngine = hasEngine || name == "engine"
		remoteHostRequired = remoteHostRequired || (name == "remote_host_id" && notNull != 0)
		passwordRequired = passwordRequired || (name == "password_secret_id" && notNull != 0)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasKind && hasEngine && !remoteHostRequired && !passwordRequired {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer s.db.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE repositories_v2 (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
	engine TEXT NOT NULL DEFAULT 'restic',
    kind TEXT NOT NULL DEFAULT 'sftp',
    remote_host_id TEXT REFERENCES remote_hosts(id),
    path TEXT NOT NULL,
	password_secret_id TEXT REFERENCES secrets(id),
    status TEXT NOT NULL DEFAULT 'ready',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);`); err != nil {
		return fmt.Errorf("create repository migration table: %w", err)
	}
	selectKind := "'sftp'"
	if hasKind {
		selectKind = "kind"
	}
	selectEngine := "'restic'"
	if hasEngine {
		selectEngine = "engine"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repositories_v2(id,name,engine,kind,remote_host_id,path,password_secret_id,status,created_at,updated_at) SELECT id,name,`+selectEngine+`,`+selectKind+`,remote_host_id,path,password_secret_id,status,created_at,updated_at FROM repositories`); err != nil {
		return fmt.Errorf("copy repositories during migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE repositories`); err != nil {
		return fmt.Errorf("replace repository table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE repositories_v2 RENAME TO repositories`); err != nil {
		return fmt.Errorf("rename repository migration table: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit repository migration: %w", err)
	}
	return nil
}
