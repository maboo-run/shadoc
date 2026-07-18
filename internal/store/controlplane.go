package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/notificationconfig"
)

type ControlPlaneRemoteHost struct {
	Host               domain.RemoteHost
	PrivateKeySecretID string
}

type ControlPlaneRepository struct {
	Repository       domain.Repository
	PasswordSecretID string
	BackendSecretID  string
}

type ControlPlaneDatabaseConnection struct {
	Connection       domain.DatabaseConnection
	PasswordSecretID string
}

type ControlPlaneScheduleWatermark struct {
	OwnerKind   string
	OwnerID     string
	ScheduledAt time.Time
	ObservedAt  time.Time
	Mode        string
	Status      string
}

type ControlPlaneNtfy struct {
	BaseURL       string
	Topic         string
	TokenSecretID string
	Enabled       bool
}

type ControlPlaneSnapshotData struct {
	RemoteHosts                 []ControlPlaneRemoteHost
	Repositories                []ControlPlaneRepository
	DatabaseConnections         []ControlPlaneDatabaseConnection
	Tasks                       []domain.Task
	Plans                       []domain.Plan
	MaintenancePolicies         []domain.MaintenancePolicy
	RestoreVerificationPolicies []domain.RestoreVerificationPolicy
	LifecyclePolicy             LifecyclePolicy
	ScheduleWatermarks          []ControlPlaneScheduleWatermark
	Agents                      []AgentRecord
	AgentServiceSettings        *AgentServiceSettings
	Ntfy                        *ControlPlaneNtfy
	Webhook                     *notificationconfig.Webhook
	Email                       *notificationconfig.Email
	Audits                      []AuditRecord
}

// ControlPlaneSnapshot returns a transactionally consistent view of durable
// control-plane configuration. Transient authority, active work, runtime
// observations, repository capacities, pending key rotation, and secret
// ciphertext are deliberately not represented by this type.
func (s *Store) ControlPlaneSnapshot(ctx context.Context) (ControlPlaneSnapshotData, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ControlPlaneSnapshotData{}, fmt.Errorf("begin control-plane snapshot: %w", err)
	}
	defer tx.Rollback()
	var result ControlPlaneSnapshotData
	if result.RemoteHosts, err = snapshotRemoteHosts(ctx, tx); err != nil {
		return result, err
	}
	if result.Repositories, err = snapshotRepositories(ctx, tx); err != nil {
		return result, err
	}
	if result.DatabaseConnections, err = snapshotDatabaseConnections(ctx, tx); err != nil {
		return result, err
	}
	if result.Tasks, err = snapshotTasks(ctx, tx); err != nil {
		return result, err
	}
	if result.Plans, err = snapshotPlans(ctx, tx); err != nil {
		return result, err
	}
	if result.MaintenancePolicies, err = snapshotMaintenance(ctx, tx); err != nil {
		return result, err
	}
	if result.RestoreVerificationPolicies, err = snapshotRestoreVerificationPolicies(ctx, tx); err != nil {
		return result, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT run_days,raw_log_days,audit_days,raw_log_max_bytes FROM lifecycle_policy WHERE id=1`).Scan(&result.LifecyclePolicy.RunDays, &result.LifecyclePolicy.RawLogDays, &result.LifecyclePolicy.AuditDays, &result.LifecyclePolicy.RawLogMaxBytes); err != nil {
		return result, fmt.Errorf("snapshot lifecycle policy: %w", err)
	}
	if result.ScheduleWatermarks, err = snapshotScheduleWatermarks(ctx, tx); err != nil {
		return result, err
	}
	if result.Agents, err = snapshotAgents(ctx, tx); err != nil {
		return result, err
	}
	if result.AgentServiceSettings, err = snapshotAgentServiceSettings(ctx, tx); err != nil {
		return result, err
	}
	if result.Ntfy, err = snapshotNtfy(ctx, tx); err != nil {
		return result, err
	}
	if result.Webhook, err = snapshotWebhook(ctx, tx); err != nil {
		return result, err
	}
	if result.Email, err = snapshotEmail(ctx, tx); err != nil {
		return result, err
	}
	if result.Audits, err = snapshotAudits(ctx, tx); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("finish control-plane snapshot: %w", err)
	}
	return result, nil
}

func snapshotRemoteHosts(ctx context.Context, tx *sql.Tx) ([]ControlPlaneRemoteHost, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,host,port,username,COALESCE(host_fingerprint,''),created_at,updated_at,private_key_secret_id FROM remote_hosts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("snapshot remote hosts: %w", err)
	}
	defer rows.Close()
	result := make([]ControlPlaneRemoteHost, 0)
	for rows.Next() {
		var item ControlPlaneRemoteHost
		var created, updated string
		if err := rows.Scan(&item.Host.ID, &item.Host.Name, &item.Host.Host, &item.Host.Port, &item.Host.Username, &item.Host.HostFingerprint, &created, &updated, &item.PrivateKeySecretID); err != nil {
			return nil, err
		}
		if item.Host.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if item.Host.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func snapshotRepositories(ctx context.Context, tx *sql.Tx) ([]ControlPlaneRepository, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,engine,kind,COALESCE(remote_host_id,''),path,backend_json,COALESCE(backend_secret_id,''),COALESCE(password_secret_id,''),status,created_at,updated_at FROM repositories ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("snapshot repositories: %w", err)
	}
	defer rows.Close()
	result := make([]ControlPlaneRepository, 0)
	for rows.Next() {
		var item ControlPlaneRepository
		var backendJSON, created, updated string
		if err := rows.Scan(&item.Repository.ID, &item.Repository.Name, &item.Repository.Engine, &item.Repository.Kind, &item.Repository.RemoteHostID, &item.Repository.Path, &backendJSON, &item.BackendSecretID, &item.PasswordSecretID, &item.Repository.Status, &created, &updated); err != nil {
			return nil, err
		}
		if err := decodeRepositoryBackend(&item.Repository, backendJSON, item.BackendSecretID); err != nil {
			return nil, fmt.Errorf("decode snapshot repository backend: %w", err)
		}
		if item.Repository.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if item.Repository.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func snapshotDatabaseConnections(ctx context.Context, tx *sql.Tx) ([]ControlPlaneDatabaseConnection, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id,name,engine,purpose,network,COALESCE(host,''),COALESCE(port,0),COALESCE(socket_path,''),username,password_secret_id,tls_json,tool_paths_json,status,COALESCE(preflight_checked_at,''),preflight_client_version,preflight_server_version,preflight_error,created_at,updated_at
		FROM database_connections WHERE id NOT LIKE 'temporary-dbconn_%' ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("snapshot database connections: %w", err)
	}
	defer rows.Close()
	result := make([]ControlPlaneDatabaseConnection, 0)
	for rows.Next() {
		var item ControlPlaneDatabaseConnection
		var tlsJSON, toolsJSON, checked, created, updated string
		if err := rows.Scan(&item.Connection.ID, &item.Connection.Name, &item.Connection.Engine, &item.Connection.Purpose, &item.Connection.Network, &item.Connection.Host, &item.Connection.Port, &item.Connection.SocketPath, &item.Connection.Username, &item.PasswordSecretID, &tlsJSON, &toolsJSON, &item.Connection.Status, &checked, &item.Connection.Preflight.ClientVersion, &item.Connection.Preflight.ServerVersion, &item.Connection.Preflight.Error, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tlsJSON), &item.Connection.TLS); err != nil {
			return nil, fmt.Errorf("decode snapshot database TLS: %w", err)
		}
		if err := json.Unmarshal([]byte(toolsJSON), &item.Connection.ToolPaths); err != nil {
			return nil, fmt.Errorf("decode snapshot database tools: %w", err)
		}
		if checked != "" {
			if item.Connection.Preflight.CheckedAt, err = parseTime(checked); err != nil {
				return nil, err
			}
		}
		if item.Connection.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if item.Connection.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func snapshotTasks(ctx context.Context, tx *sql.Tx) ([]domain.Task, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,engine,kind,execution_target_json,COALESCE(repository_id,''),source_json,retention_json,resources_json,health_policy_json,exclusions_json,enabled,created_at,updated_at FROM tasks ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("snapshot tasks: %w", err)
	}
	defer rows.Close()
	result := make([]domain.Task, 0)
	for rows.Next() {
		var item domain.Task
		var targetJSON, sourceJSON, retentionJSON, resourcesJSON, healthJSON, exclusionsJSON, created, updated string
		var enabled int
		if err := rows.Scan(&item.ID, &item.Name, &item.Engine, &item.Kind, &targetJSON, &item.RepositoryID, &sourceJSON, &retentionJSON, &resourcesJSON, &healthJSON, &exclusionsJSON, &enabled, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(targetJSON), &item.ExecutionTarget); err != nil {
			return nil, fmt.Errorf("decode snapshot task target: %w", err)
		}
		switch item.EffectiveEngine() {
		case domain.RsyncEngine:
			item.Rsync = &domain.RsyncSource{}
			if err := json.Unmarshal([]byte(sourceJSON), item.Rsync); err != nil {
				return nil, fmt.Errorf("decode snapshot rsync source: %w", err)
			}
		case domain.ResticEngine:
			if item.Kind == domain.DirectoryTask {
				item.Directory = &domain.DirectorySource{}
				if err := json.Unmarshal([]byte(sourceJSON), item.Directory); err != nil {
					return nil, fmt.Errorf("decode snapshot directory source: %w", err)
				}
				if err := json.Unmarshal([]byte(exclusionsJSON), &item.Directory.Exclusions); err != nil {
					return nil, fmt.Errorf("decode snapshot exclusions: %w", err)
				}
			} else {
				item.Database = &domain.DatabaseSource{}
				if err := json.Unmarshal([]byte(sourceJSON), item.Database); err != nil {
					return nil, fmt.Errorf("decode snapshot database source: %w", err)
				}
			}
		default:
			return nil, fmt.Errorf("unsupported task engine %q", item.Engine)
		}
		if err := json.Unmarshal([]byte(retentionJSON), &item.Retention); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(resourcesJSON), &item.Resources); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(healthJSON), &item.Health); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		item.ScopeConfirmation = domain.TaskScopeConfirmation{}
		if item.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if item.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func snapshotPlans(ctx context.Context, tx *sql.Tx) ([]domain.Plan, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,schedule_json,timezone,max_parallel,enabled,catch_up_window_minutes,schedule_anchor_at,created_at,updated_at FROM plans ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("snapshot plans: %w", err)
	}
	result := make([]domain.Plan, 0)
	for rows.Next() {
		var item domain.Plan
		var scheduleJSON, anchor, created, updated string
		var enabled int
		if err := rows.Scan(&item.ID, &item.Name, &scheduleJSON, &item.Timezone, &item.MaxParallel, &enabled, &item.CatchUpWindowMinutes, &anchor, &created, &updated); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(scheduleJSON), &item.Schedule); err != nil {
			_ = rows.Close()
			return nil, err
		}
		item.Enabled = enabled != 0
		if item.ScheduleAnchorAt, err = parseTime(anchor); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if item.CreatedAt, err = parseTime(created); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if item.UpdatedAt, err = parseTime(updated); err != nil {
			_ = rows.Close()
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range result {
		taskRows, err := tx.QueryContext(ctx, `SELECT task_id FROM plan_tasks WHERE plan_id=? ORDER BY position`, result[index].ID)
		if err != nil {
			return nil, err
		}
		for taskRows.Next() {
			var taskID string
			if err := taskRows.Scan(&taskID); err != nil {
				_ = taskRows.Close()
				return nil, err
			}
			result[index].TaskIDs = append(result[index].TaskIDs, taskID)
		}
		if err := taskRows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func snapshotMaintenance(ctx context.Context, tx *sql.Tx) ([]domain.MaintenancePolicy, error) {
	rows, err := tx.QueryContext(ctx, `SELECT repository_id,schedule_json,timezone,retention_json,enabled,catch_up_window_minutes,schedule_anchor_at,updated_at FROM repository_maintenance ORDER BY repository_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.MaintenancePolicy, 0)
	for rows.Next() {
		var item domain.MaintenancePolicy
		var scheduleJSON, retentionJSON, anchor, updated string
		var enabled int
		if err := rows.Scan(&item.RepositoryID, &scheduleJSON, &item.Timezone, &retentionJSON, &enabled, &item.CatchUpWindowMinutes, &anchor, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(scheduleJSON), &item.Schedule); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(retentionJSON), &item.Retention); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		if item.ScheduleAnchorAt, err = parseTime(anchor); err != nil {
			return nil, err
		}
		if item.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func snapshotRestoreVerificationPolicies(ctx context.Context, tx *sql.Tx) ([]domain.RestoreVerificationPolicy, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT task_id,schedule_json,timezone,selection_path,maximum_bytes,maximum_success_age_hours,
		       enabled,catch_up_window_minutes,schedule_anchor_at,updated_at
		FROM restore_verification_policies ORDER BY task_id
	`)
	if err != nil {
		return nil, fmt.Errorf("snapshot restore verification policies: %w", err)
	}
	defer rows.Close()
	result := make([]domain.RestoreVerificationPolicy, 0)
	for rows.Next() {
		policy, err := scanRestoreVerificationPolicy(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, policy)
	}
	return result, rows.Err()
}

func snapshotScheduleWatermarks(ctx context.Context, tx *sql.Tx) ([]ControlPlaneScheduleWatermark, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT owner_kind,owner_id,scheduled_at,observed_at,mode,status FROM (
			SELECT owner_kind,owner_id,scheduled_at,observed_at,mode,status,
			       ROW_NUMBER() OVER(PARTITION BY owner_kind,owner_id ORDER BY scheduled_at DESC,id DESC) AS position
			FROM schedule_occurrences WHERE status NOT IN ('pending','running')
		) WHERE position=1 ORDER BY owner_kind,owner_id
	`)
	if err != nil {
		return nil, fmt.Errorf("snapshot schedule watermarks: %w", err)
	}
	defer rows.Close()
	result := make([]ControlPlaneScheduleWatermark, 0)
	for rows.Next() {
		var item ControlPlaneScheduleWatermark
		var scheduled, observed string
		if err := rows.Scan(&item.OwnerKind, &item.OwnerID, &scheduled, &observed, &item.Mode, &item.Status); err != nil {
			return nil, err
		}
		if item.ScheduledAt, err = parseTime(scheduled); err != nil {
			return nil, err
		}
		if item.ObservedAt, err = parseTime(observed); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func snapshotAgents(ctx context.Context, tx *sql.Tx) ([]AgentRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(remote_host_id,''),certificate_serial,certificate_not_after,capabilities_json,status,created_at,revoked_at FROM agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AgentRecord, 0)
	for rows.Next() {
		var item AgentRecord
		var capabilities, created string
		var certificateNotAfter, revoked sql.NullString
		if err := rows.Scan(&item.ID, &item.RemoteHostID, &item.CertificateSerial, &certificateNotAfter, &capabilities, &item.Status, &created, &revoked); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(capabilities), &item.Capabilities); err != nil {
			return nil, err
		}
		if item.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if certificateNotAfter.Valid {
			value, err := parseTime(certificateNotAfter.String)
			if err != nil {
				return nil, err
			}
			item.CertificateNotAfter = &value
		}
		if revoked.Valid {
			value, err := parseTime(revoked.String)
			if err != nil {
				return nil, err
			}
			item.RevokedAt = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func snapshotAgentServiceSettings(ctx context.Context, tx *sql.Tx) (*AgentServiceSettings, error) {
	var item AgentServiceSettings
	var enabled int
	var namesJSON string
	err := tx.QueryRowContext(ctx, `SELECT enabled,listen_host,port,advertised_host,tls_names_json FROM agent_service_settings WHERE id=1`).Scan(&enabled, &item.ListenHost, &item.Port, &item.AdvertisedHost, &namesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(namesJSON), &item.TLSNames); err != nil {
		return nil, err
	}
	item.Enabled = enabled != 0
	return &item, nil
}

func snapshotNtfy(ctx context.Context, tx *sql.Tx) (*ControlPlaneNtfy, error) {
	var encoded string
	err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='ntfy.config'`).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stored struct {
		BaseURL       string `json:"baseUrl"`
		Topic         string `json:"topic"`
		TokenSecretID string `json:"tokenSecretId"`
		Enabled       *bool  `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(encoded), &stored); err != nil {
		return nil, fmt.Errorf("decode snapshot ntfy configuration: %w", err)
	}
	enabled := stored.Enabled == nil || *stored.Enabled
	return &ControlPlaneNtfy{BaseURL: stored.BaseURL, Topic: stored.Topic, TokenSecretID: stored.TokenSecretID, Enabled: enabled}, nil
}

func snapshotWebhook(ctx context.Context, tx *sql.Tx) (*notificationconfig.Webhook, error) {
	var encoded string
	err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key=?`, notificationconfig.WebhookMetadataKey).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var config notificationconfig.Webhook
	if err := json.Unmarshal([]byte(encoded), &config); err != nil || config.Validate() != nil {
		return nil, errors.New("decode snapshot webhook configuration")
	}
	return &config, nil
}

func snapshotEmail(ctx context.Context, tx *sql.Tx) (*notificationconfig.Email, error) {
	var encoded string
	err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key=?`, notificationconfig.EmailMetadataKey).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var config notificationconfig.Email
	if err := json.Unmarshal([]byte(encoded), &config); err != nil || config.Validate() != nil {
		return nil, errors.New("decode snapshot email configuration")
	}
	return &config, nil
}

func snapshotAudits(ctx context.Context, tx *sql.Tx) ([]AuditRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,occurred_at,actor,action,target_type,COALESCE(target_id,''),detail_json FROM audits ORDER BY occurred_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AuditRecord, 0)
	for rows.Next() {
		var item AuditRecord
		var occurred, detailJSON string
		if err := rows.Scan(&item.ID, &occurred, &item.Actor, &item.Action, &item.TargetType, &item.TargetID, &detailJSON); err != nil {
			return nil, err
		}
		if item.OccurredAt, err = parseTime(occurred); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(detailJSON), &item.Detail); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
