package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/notificationconfig"
	"github.com/maboo-run/shadoc/internal/s3backend"
)

var ErrControlPlaneImportPreview = errors.New("control-plane import preview is invalid, expired, or already consumed")

type ControlPlaneImportPreview struct {
	ID           string
	BundleSHA256 string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

type ControlPlaneImportRequest struct {
	PreviewID                   string
	BundleSHA256                string
	ImportedAt                  time.Time
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

func (s *Store) SaveControlPlaneImportPreview(ctx context.Context, preview ControlPlaneImportPreview) error {
	if preview.ID == "" || !validSHA256(preview.BundleSHA256) || preview.CreatedAt.IsZero() || !preview.ExpiresAt.After(preview.CreatedAt) {
		return errors.New("invalid control-plane import preview")
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM controlplane_import_previews WHERE expires_at<?`, formatTime(preview.CreatedAt.Add(-24*time.Hour)))
	_, err := s.db.ExecContext(ctx, `INSERT INTO controlplane_import_previews(id,bundle_sha256,created_at,expires_at) VALUES(?,?,?,?)`, preview.ID, strings.ToLower(preview.BundleSHA256), formatTime(preview.CreatedAt), formatTime(preview.ExpiresAt))
	return constraintError(err)
}

func (s *Store) ImportControlPlane(ctx context.Context, request ControlPlaneImportRequest) error {
	if request.PreviewID == "" || !validSHA256(request.BundleSHA256) || request.ImportedAt.IsZero() {
		return ErrControlPlaneImportPreview
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin control-plane import: %w", err)
	}
	defer tx.Rollback()
	consumed, err := tx.ExecContext(ctx, `UPDATE controlplane_import_previews SET consumed_at=? WHERE id=? AND bundle_sha256=? AND consumed_at IS NULL AND expires_at>?`, formatTime(request.ImportedAt), request.PreviewID, strings.ToLower(request.BundleSHA256), formatTime(request.ImportedAt))
	if err != nil {
		return err
	}
	if affected, _ := consumed.RowsAffected(); affected != 1 {
		return ErrControlPlaneImportPreview
	}
	if err := importRemoteHosts(ctx, tx, request.RemoteHosts); err != nil {
		return err
	}
	if err := importRepositories(ctx, tx, request.Repositories, request.ImportedAt); err != nil {
		return err
	}
	if err := importDatabaseConnections(ctx, tx, request.DatabaseConnections); err != nil {
		return err
	}
	if err := importAgents(ctx, tx, request.Agents); err != nil {
		return err
	}
	if err := importTasks(ctx, tx, request.Tasks); err != nil {
		return err
	}
	if err := importPlans(ctx, tx, request.Plans); err != nil {
		return err
	}
	if err := importMaintenancePolicies(ctx, tx, request.MaintenancePolicies); err != nil {
		return err
	}
	if err := importRestoreVerificationPolicies(ctx, tx, request.RestoreVerificationPolicies); err != nil {
		return err
	}
	if err := importScheduleWatermarks(ctx, tx, request); err != nil {
		return err
	}
	if err := importAgentServiceSettings(ctx, tx, request.AgentServiceSettings, request.ImportedAt); err != nil {
		return err
	}
	if err := importNtfy(ctx, tx, request.Ntfy); err != nil {
		return err
	}
	if err := importWebhook(ctx, tx, request.Webhook); err != nil {
		return err
	}
	if err := importEmail(ctx, tx, request.Email); err != nil {
		return err
	}
	if err := importAudits(ctx, tx, request.Audits); err != nil {
		return err
	}
	if request.LifecyclePolicy.RunDays != 0 || request.LifecyclePolicy.RawLogDays != 0 || request.LifecyclePolicy.AuditDays != 0 || request.LifecyclePolicy.RawLogMaxBytes != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE lifecycle_policy SET run_days=?,raw_log_days=?,audit_days=?,raw_log_max_bytes=?,updated_at=? WHERE id=1`, request.LifecyclePolicy.RunDays, request.LifecyclePolicy.RawLogDays, request.LifecyclePolicy.AuditDays, request.LifecyclePolicy.RawLogMaxBytes, formatTime(request.ImportedAt)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit control-plane import: %w", constraintError(err))
	}
	return nil
}

func importRemoteHosts(ctx context.Context, tx *sql.Tx, items []ControlPlaneRemoteHost) error {
	for _, item := range items {
		if err := item.Host.Validate(); err != nil || item.Host.ID == "" || item.Host.HostFingerprint == "" {
			return fmt.Errorf("invalid imported remote host %q", item.Host.ID)
		}
		if err := requireSecret(ctx, tx, item.PrivateKeySecretID, "ssh-private-key"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO remote_hosts(id,name,host,port,username,private_key_secret_id,host_fingerprint,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, item.Host.ID, item.Host.Name, item.Host.Host, item.Host.Port, item.Host.Username, item.PrivateKeySecretID, item.Host.HostFingerprint, formatTime(item.Host.CreatedAt), formatTime(item.Host.UpdatedAt))
		if err != nil {
			return constraintError(err)
		}
	}
	return nil
}

func importRepositories(ctx context.Context, tx *sql.Tx, items []ControlPlaneRepository, importedAt time.Time) error {
	for _, item := range items {
		if err := item.Repository.Validate(); err != nil || item.Repository.ID == "" {
			return fmt.Errorf("invalid imported repository %q", item.Repository.ID)
		}
		if item.Repository.EffectiveEngine() == domain.ResticEngine {
			if err := requireSecret(ctx, tx, item.PasswordSecretID, "repository-password"); err != nil {
				return err
			}
		} else if item.PasswordSecretID != "" {
			return errors.New("rsync repository cannot import a password secret")
		}
		backendJSON, err := encodeRepositoryBackend(item.Repository)
		if err != nil {
			return err
		}
		if item.Repository.EffectiveKind() == domain.S3Repository {
			if err := requireSecret(ctx, tx, item.BackendSecretID, s3backend.CredentialPurpose); err != nil {
				return err
			}
		} else if item.BackendSecretID != "" {
			return errors.New("non-S3 repository cannot import a backend secret")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO repositories(id,name,engine,kind,remote_host_id,path,backend_json,backend_secret_id,password_secret_id,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, item.Repository.ID, item.Repository.Name, item.Repository.EffectiveEngine(), item.Repository.EffectiveKind(), nullString(item.Repository.RemoteHostID), item.Repository.Path, backendJSON, nullString(item.BackendSecretID), nullString(item.PasswordSecretID), "disconnected", formatTime(item.Repository.CreatedAt), formatTime(item.Repository.UpdatedAt))
		if err != nil {
			return constraintError(err)
		}
		if err := insertDefaultRepositoryCapacityPolicy(ctx, tx, item.Repository.ID, importedAt, item.Repository.EffectiveKind() != domain.S3Repository); err != nil {
			return err
		}
	}
	return nil
}

func importDatabaseConnections(ctx context.Context, tx *sql.Tx, items []ControlPlaneDatabaseConnection) error {
	for _, item := range items {
		if err := item.Connection.Validate(); err != nil || item.Connection.ID == "" {
			return fmt.Errorf("invalid imported database connection %q", item.Connection.ID)
		}
		purpose := "database-" + string(item.Connection.Purpose) + "-password"
		if err := requireSecret(ctx, tx, item.PasswordSecretID, purpose); err != nil {
			return err
		}
		tlsJSON, err := json.Marshal(item.Connection.TLS)
		if err != nil {
			return err
		}
		toolsJSON, err := json.Marshal(item.Connection.ToolPaths)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO database_connections(id,name,engine,purpose,network,host,port,socket_path,username,password_secret_id,tls_json,tool_paths_json,status,preflight_checked_at,preflight_client_version,preflight_server_version,preflight_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.Connection.ID, item.Connection.Name, item.Connection.Engine, item.Connection.Purpose, item.Connection.Network, nullString(item.Connection.Host), nullInt(item.Connection.Port), nullString(item.Connection.SocketPath), item.Connection.Username, item.PasswordSecretID, string(tlsJSON), string(toolsJSON), "draft", nil, "", "", "", formatTime(item.Connection.CreatedAt), formatTime(item.Connection.UpdatedAt))
		if err != nil {
			return constraintError(err)
		}
	}
	return nil
}

func importAgents(ctx context.Context, tx *sql.Tx, items []AgentRecord) error {
	for _, item := range items {
		if item.ID == "" || item.CertificateSerial == "" {
			return errors.New("invalid imported Agent identity")
		}
		status := "offline"
		if item.RevokedAt != nil {
			status = "revoked"
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO agents(id,remote_host_id,certificate_serial,certificate_not_after,capabilities_json,status,last_heartbeat_at,created_at,revoked_at,stopped_at,uninstalled_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, item.ID, nullString(item.RemoteHostID), item.CertificateSerial, nullableTime(item.CertificateNotAfter), "[]", status, nil, formatTime(item.CreatedAt), nullableTime(item.RevokedAt), nil, nil)
		if err != nil {
			return constraintError(err)
		}
		certificateStatus := "active"
		retiredAt := any(nil)
		if item.RevokedAt != nil {
			certificateStatus = "retired"
			retiredAt = formatTime(*item.RevokedAt)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_certificates(serial,agent_id,not_after,status,issued_at,activated_at,retired_at) VALUES(?,?,?,?,?,?,?)`, item.CertificateSerial, item.ID, nullableTime(item.CertificateNotAfter), certificateStatus, formatTime(item.CreatedAt), formatTime(item.CreatedAt), retiredAt); err != nil {
			return constraintError(err)
		}
	}
	return nil
}

func importTasks(ctx context.Context, tx *sql.Tx, items []domain.Task) error {
	for _, item := range items {
		if err := item.Validate(); err != nil || item.ID == "" {
			return fmt.Errorf("invalid imported task %q", item.ID)
		}
		targetJSON, sourceJSON, retentionJSON, resourcesJSON, healthJSON, exclusionsJSON, err := encodeImportedTask(item)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,name,engine,kind,execution_target_json,repository_id,source_json,retention_json,resources_json,health_policy_json,exclusions_json,scope_confirmation_json,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,0,?,?)`, item.ID, item.Name, item.EffectiveEngine(), item.Kind, targetJSON, nullString(item.RepositoryID), sourceJSON, retentionJSON, resourcesJSON, healthJSON, exclusionsJSON, "{}", formatTime(item.CreatedAt), formatTime(item.UpdatedAt))
		if err != nil {
			return constraintError(err)
		}
	}
	return nil
}

func encodeImportedTask(item domain.Task) (string, string, string, string, string, string, error) {
	target, err := json.Marshal(item.EffectiveExecutionTarget())
	if err != nil {
		return "", "", "", "", "", "", err
	}
	var source any
	if item.EffectiveEngine() == domain.RsyncEngine {
		source = item.Rsync
	} else if item.Kind == domain.DirectoryTask {
		source = item.Directory
	} else {
		source = item.Database
	}
	encodedSource, err := json.Marshal(source)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	retention, _ := json.Marshal(item.Retention)
	resources, _ := json.Marshal(item.Resources)
	health, _ := json.Marshal(item.Health)
	exclusions := []string{}
	if item.Directory != nil {
		exclusions = item.Directory.Exclusions
	}
	encodedExclusions, _ := json.Marshal(exclusions)
	return string(target), string(encodedSource), string(retention), string(resources), string(health), string(encodedExclusions), nil
}

func importPlans(ctx context.Context, tx *sql.Tx, items []domain.Plan) error {
	for _, item := range items {
		if err := item.Validate(); err != nil || item.ID == "" {
			return fmt.Errorf("invalid imported plan %q", item.ID)
		}
		scheduleJSON, _ := json.Marshal(item.Schedule)
		_, err := tx.ExecContext(ctx, `INSERT INTO plans(id,name,schedule_json,timezone,max_parallel,enabled,catch_up_window_minutes,schedule_anchor_at,created_at,updated_at) VALUES(?,?,?,?,?,0,?,?,?,?)`, item.ID, item.Name, string(scheduleJSON), item.Timezone, item.MaxParallel, item.CatchUpWindowMinutes, formatTime(item.ScheduleAnchorAt), formatTime(item.CreatedAt), formatTime(item.UpdatedAt))
		if err != nil {
			return constraintError(err)
		}
		for position, taskID := range item.TaskIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO plan_tasks(plan_id,task_id,position) VALUES(?,?,?)`, item.ID, taskID, position); err != nil {
				return constraintError(err)
			}
		}
	}
	return nil
}

func importMaintenancePolicies(ctx context.Context, tx *sql.Tx, items []domain.MaintenancePolicy) error {
	for _, item := range items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("invalid imported maintenance policy for %q", item.RepositoryID)
		}
		scheduleJSON, _ := json.Marshal(item.Schedule)
		retentionJSON, _ := json.Marshal(item.Retention)
		_, err := tx.ExecContext(ctx, `INSERT INTO repository_maintenance(repository_id,schedule_json,timezone,retention_json,enabled,catch_up_window_minutes,schedule_anchor_at,updated_at) VALUES(?,?,?,?,0,?,?,?)`, item.RepositoryID, string(scheduleJSON), item.Timezone, string(retentionJSON), item.CatchUpWindowMinutes, formatTime(item.ScheduleAnchorAt), formatTime(item.UpdatedAt))
		if err != nil {
			return constraintError(err)
		}
	}
	return nil
}

func importRestoreVerificationPolicies(ctx context.Context, tx *sql.Tx, items []domain.RestoreVerificationPolicy) error {
	for _, item := range items {
		if err := item.Validate(); err != nil || item.ScheduleAnchorAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("invalid imported restore verification policy for %q", item.TaskID)
		}
		var engine, kind, targetJSON string
		if err := tx.QueryRowContext(ctx, `SELECT engine,kind,execution_target_json FROM tasks WHERE id=?`, item.TaskID).Scan(&engine, &kind, &targetJSON); err != nil {
			return constraintError(err)
		}
		var target execution.Target
		if err := json.Unmarshal([]byte(targetJSON), &target); err != nil {
			return fmt.Errorf("decode imported restore verification target: %w", err)
		}
		if engine != string(domain.ResticEngine) || kind != string(domain.DirectoryTask) || target.Normalized().Kind != execution.Local {
			return fmt.Errorf("imported restore verification policy %q references an unsupported task", item.TaskID)
		}
		scheduleJSON, _ := json.Marshal(item.Schedule)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO restore_verification_policies(
				task_id,schedule_json,timezone,selection_path,maximum_bytes,maximum_success_age_hours,
				enabled,catch_up_window_minutes,schedule_anchor_at,updated_at
			) VALUES(?,?,?,?,?,?,0,?,?,?)
		`, item.TaskID, string(scheduleJSON), item.Timezone, item.SelectionPath, item.MaximumBytes, item.MaximumSuccessAgeHours, item.CatchUpWindowMinutes, formatTime(item.ScheduleAnchorAt), formatTime(item.UpdatedAt))
		if err != nil {
			return constraintError(err)
		}
	}
	return nil
}

func importScheduleWatermarks(ctx context.Context, tx *sql.Tx, request ControlPlaneImportRequest) error {
	for index, item := range request.ScheduleWatermarks {
		if item.OwnerID == "" || item.ScheduledAt.IsZero() || item.ObservedAt.IsZero() || !terminalScheduleStatus(item.Status) {
			return errors.New("invalid imported schedule watermark")
		}
		targets := []string{item.OwnerID}
		if item.OwnerKind == "plan" {
			for _, plan := range request.Plans {
				if plan.ID == item.OwnerID {
					targets = append([]string(nil), plan.TaskIDs...)
					break
				}
			}
		}
		targetJSON, _ := json.Marshal(targets)
		runJSON := "[]"
		identity := sha256.Sum256([]byte(request.BundleSHA256 + "\x00" + item.OwnerKind + "\x00" + item.OwnerID + "\x00" + formatTime(item.ScheduledAt)))
		id := fmt.Sprintf("cpwm_%d_%s", index, hex.EncodeToString(identity[:8]))
		_, err := tx.ExecContext(ctx, `INSERT INTO schedule_occurrences(id,owner_kind,owner_id,scheduled_at,observed_at,mode,status,target_ids_json,run_ids_json,started_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, item.OwnerKind, item.OwnerID, formatTime(item.ScheduledAt), formatTime(item.ObservedAt), item.Mode, item.Status, string(targetJSON), runJSON, nil, formatTime(item.ObservedAt))
		if err != nil {
			return constraintError(err)
		}
	}
	return nil
}

func importAgentServiceSettings(ctx context.Context, tx *sql.Tx, item *AgentServiceSettings, importedAt time.Time) error {
	if item == nil {
		return nil
	}
	names, err := json.Marshal(item.TLSNames)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_service_settings(id,enabled,listen_host,port,advertised_host,tls_names_json,updated_at) VALUES(1,0,?,?,?,?,?)`, item.ListenHost, item.Port, item.AdvertisedHost, string(names), formatTime(importedAt))
	return constraintError(err)
}

func importNtfy(ctx context.Context, tx *sql.Tx, item *ControlPlaneNtfy) error {
	if item == nil {
		return nil
	}
	if item.TokenSecretID != "" {
		if err := requireSecret(ctx, tx, item.TokenSecretID, "ntfy-token"); err != nil {
			return err
		}
	}
	enabled := false
	encoded, err := json.Marshal(struct {
		BaseURL       string `json:"baseUrl"`
		Topic         string `json:"topic"`
		TokenSecretID string `json:"tokenSecretId,omitempty"`
		Enabled       *bool  `json:"enabled"`
	}{BaseURL: item.BaseURL, Topic: item.Topic, TokenSecretID: item.TokenSecretID, Enabled: &enabled})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('ntfy.config',?)`, string(encoded))
	return constraintError(err)
}

func importWebhook(ctx context.Context, tx *sql.Tx, item *notificationconfig.Webhook) error {
	if item == nil {
		return nil
	}
	config := *item
	if config.SecretID != "" {
		if err := requireSecret(ctx, tx, config.SecretID, notificationconfig.WebhookSecretPurpose); err != nil {
			return err
		}
	}
	if err := config.Validate(); err != nil {
		return errors.New("invalid imported webhook configuration")
	}
	enabled := false
	config.Enabled = &enabled
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?)`, notificationconfig.WebhookMetadataKey, string(encoded))
	return constraintError(err)
}

func importEmail(ctx context.Context, tx *sql.Tx, item *notificationconfig.Email) error {
	if item == nil {
		return nil
	}
	config := *item
	config.To = append([]string(nil), item.To...)
	if config.PasswordSecretID != "" {
		if err := requireSecret(ctx, tx, config.PasswordSecretID, notificationconfig.EmailPasswordPurpose); err != nil {
			return err
		}
	}
	if err := config.Validate(); err != nil {
		return errors.New("invalid imported email configuration")
	}
	enabled := false
	config.Enabled = &enabled
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?)`, notificationconfig.EmailMetadataKey, string(encoded))
	return constraintError(err)
}

func importAudits(ctx context.Context, tx *sql.Tx, items []AuditRecord) error {
	for _, item := range items {
		detail, err := json.Marshal(item.Detail)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audits(occurred_at,actor,action,target_type,target_id,detail_json) VALUES(?,?,?,?,?,?)`, formatTime(item.OccurredAt), item.Actor, item.Action, item.TargetType, nullString(item.TargetID), string(detail)); err != nil {
			return err
		}
	}
	return nil
}

func requireSecret(ctx context.Context, tx *sql.Tx, id, purpose string) error {
	if id == "" {
		return ErrConflict
	}
	var actual string
	if err := tx.QueryRowContext(ctx, `SELECT purpose FROM secrets WHERE id=?`, id).Scan(&actual); err != nil {
		return ErrConflict
	}
	if actual != purpose {
		return errors.New("imported secret purpose mismatch")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
