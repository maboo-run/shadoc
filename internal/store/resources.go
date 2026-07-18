package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatTime(value)
}

var ErrConflict = errors.New("resource conflict")

func (s *Store) CreateRemoteHost(ctx context.Context, host domain.RemoteHost, privateKeySecretID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO remote_hosts(
			id, name, host, port, username, private_key_secret_id,
			host_fingerprint, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, host.ID, host.Name, host.Host, host.Port, host.Username, privateKeySecretID,
		host.HostFingerprint, formatTime(host.CreatedAt), formatTime(host.UpdatedAt))
	if isUniqueConstraint(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("create remote host: %w", err)
	}
	return nil
}

func (s *Store) CreateRepository(ctx context.Context, repository domain.Repository, passwordSecretID string) error {
	kind := repository.EffectiveKind()
	backendJSON, err := encodeRepositoryBackend(repository)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create repository: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO repositories(id, name, engine, kind, remote_host_id, path, password_secret_id, backend_json, backend_secret_id, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, repository.ID, repository.Name, repository.EffectiveEngine(), kind, nullString(repository.RemoteHostID), repository.Path, nullString(passwordSecretID), backendJSON, nullString(repository.BackendSecretID),
		repository.Status, formatTime(repository.CreatedAt), formatTime(repository.UpdatedAt))
	if isUniqueConstraint(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("create repository: %w", err)
	}
	createdAt := repository.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	if err := insertDefaultRepositoryCapacityPolicy(ctx, tx, repository.ID, createdAt, kind != domain.S3Repository); err != nil {
		return fmt.Errorf("create repository capacity policy: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ListRepositories(ctx context.Context) ([]domain.Repository, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.name, r.engine, r.kind, COALESCE(r.remote_host_id, ''), r.path, r.backend_json, COALESCE(r.backend_secret_id,''), r.status, r.created_at, r.updated_at,
		       c.total_bytes, c.available_bytes, COALESCE(c.checked_at,''), COALESCE(c.source_agent_id,'')
		FROM repositories r LEFT JOIN repository_capacities c ON c.repository_id=r.id ORDER BY r.name, r.id
	`)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	defer rows.Close()
	result := make([]domain.Repository, 0)
	for rows.Next() {
		var item domain.Repository
		var createdAt, updatedAt, checkedAt, sourceAgentID string
		var totalBytes, availableBytes sql.NullInt64
		var backendJSON, backendSecretID string
		if err := rows.Scan(&item.ID, &item.Name, &item.Engine, &item.Kind, &item.RemoteHostID, &item.Path, &backendJSON, &backendSecretID, &item.Status, &createdAt, &updatedAt, &totalBytes, &availableBytes, &checkedAt, &sourceAgentID); err != nil {
			return nil, fmt.Errorf("scan repository: %w", err)
		}
		if err := decodeRepositoryBackend(&item, backendJSON, backendSecretID); err != nil {
			return nil, err
		}
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		item.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		if totalBytes.Valid && availableBytes.Valid && totalBytes.Int64 >= 0 && availableBytes.Int64 >= 0 && checkedAt != "" {
			checked, parseErr := parseTime(checkedAt)
			if parseErr != nil {
				return nil, parseErr
			}
			total, available := uint64(totalBytes.Int64), uint64(availableBytes.Int64)
			used := uint64(0)
			if total >= available {
				used = total - available
			}
			item.Capacity = &domain.RepositoryCapacity{TotalBytes: total, UsedBytes: used, AvailableBytes: available, CheckedAt: checked, SourceAgentID: sourceAgentID}
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) SaveRepositoryCapacity(ctx context.Context, repositoryID string, capacity domain.RepositoryCapacity) error {
	if err := validateRepositoryCapacity(repositoryID, capacity); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveRepositoryCapacity(ctx, tx, repositoryID, capacity); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateDatabaseConnection(ctx context.Context, connection domain.DatabaseConnection, passwordSecretID string) error {
	tlsJSON, err := json.Marshal(connection.TLS)
	if err != nil {
		return fmt.Errorf("encode tls config: %w", err)
	}
	toolsJSON, err := json.Marshal(connection.ToolPaths)
	if err != nil {
		return fmt.Errorf("encode tool paths: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO database_connections(
			id, name, engine, purpose, network, host, port, socket_path,
			username, password_secret_id, tls_json, tool_paths_json, status, preflight_checked_at,
			preflight_client_version, preflight_server_version, preflight_error, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, connection.ID, connection.Name, connection.Engine, connection.Purpose, connection.Network,
		nullString(connection.Host), nullInt(connection.Port), nullString(connection.SocketPath), connection.Username,
		passwordSecretID, string(tlsJSON), string(toolsJSON), connection.Status, nullTime(connection.Preflight.CheckedAt),
		connection.Preflight.ClientVersion, connection.Preflight.ServerVersion, connection.Preflight.Error,
		formatTime(connection.CreatedAt), formatTime(connection.UpdatedAt))
	if isUniqueConstraint(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("create database connection: %w", err)
	}
	return nil
}

func (s *Store) ListDatabaseConnections(ctx context.Context) ([]domain.DatabaseConnection, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, engine, purpose, network, COALESCE(host, ''), COALESCE(port, 0),
		       COALESCE(socket_path, ''), username, tls_json, tool_paths_json, status,
		       COALESCE(preflight_checked_at,''), preflight_client_version, preflight_server_version, preflight_error,
		       created_at, updated_at
		FROM database_connections ORDER BY name, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list database connections: %w", err)
	}
	defer rows.Close()
	result := make([]domain.DatabaseConnection, 0)
	for rows.Next() {
		var item domain.DatabaseConnection
		var tlsJSON, toolsJSON, checkedAt, createdAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.Name, &item.Engine, &item.Purpose, &item.Network,
			&item.Host, &item.Port, &item.SocketPath, &item.Username, &tlsJSON, &toolsJSON, &item.Status,
			&checkedAt, &item.Preflight.ClientVersion, &item.Preflight.ServerVersion, &item.Preflight.Error,
			&createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan database connection: %w", err)
		}
		if err := json.Unmarshal([]byte(tlsJSON), &item.TLS); err != nil {
			return nil, fmt.Errorf("decode tls config: %w", err)
		}
		if err := json.Unmarshal([]byte(toolsJSON), &item.ToolPaths); err != nil {
			return nil, fmt.Errorf("decode tool paths: %w", err)
		}
		if checkedAt != "" {
			item.Preflight.CheckedAt, err = parseTime(checkedAt)
			if err != nil {
				return nil, err
			}
		}
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		item.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) DeleteExpiredTemporaryDatabaseConnections(ctx context.Context, cutoff time.Time) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT d.id,d.password_secret_id
		FROM database_connections d
		WHERE substr(d.id,1,17)='temporary-dbconn_' AND d.created_at<?
		  AND NOT EXISTS (
			SELECT 1 FROM operations o
			WHERE json_extract(o.detail_json,'$.connectionId')=d.id
			  AND o.status IN ('queued','running','cleanup_required')
		  )
	`, formatTime(cutoff))
	if err != nil {
		return nil, err
	}
	type expired struct{ id, secret string }
	items := make([]expired, 0)
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.id, &item.secret); err != nil {
			_ = rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `DELETE FROM database_connections WHERE id=?`, item.id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	secrets := make([]string, 0, len(items))
	for _, item := range items {
		secrets = append(secrets, item.secret)
	}
	return secrets, nil
}

func (s *Store) CreateTask(ctx context.Context, task domain.Task) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var source any
	switch task.EffectiveEngine() {
	case domain.ResticEngine:
		var repositoryStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM repositories WHERE id = ?`, task.RepositoryID).Scan(&repositoryStatus); err != nil || repositoryStatus != "ready" {
			return ErrConflict
		}
		if task.Kind == domain.DirectoryTask {
			source = task.Directory
			break
		}
		var purpose string
		err := tx.QueryRowContext(ctx, `SELECT purpose FROM database_connections WHERE id = ?`, task.Database.ConnectionID).Scan(&purpose)
		if errors.Is(err, sql.ErrNoRows) || purpose != string(domain.BackupConnection) {
			return errors.New("database task requires an existing backup connection")
		}
		if err != nil {
			return fmt.Errorf("validate database connection: %w", err)
		}
		source = task.Database
	case domain.RsyncEngine:
		if task.RepositoryID != "" {
			var status, engine string
			if err := tx.QueryRowContext(ctx, `SELECT status,engine FROM repositories WHERE id=?`, task.RepositoryID).Scan(&status, &engine); err != nil || status != "ready" || engine != string(domain.RsyncEngine) {
				return ErrConflict
			}
		}
		source = task.Rsync
	default:
		return fmt.Errorf("unsupported task engine %q", task.Engine)
	}
	sourceJSON, err := json.Marshal(source)
	if err != nil {
		return fmt.Errorf("encode task source: %w", err)
	}
	retentionJSON, err := json.Marshal(task.Retention)
	if err != nil {
		return fmt.Errorf("encode retention policy: %w", err)
	}
	resourcesJSON, err := json.Marshal(task.Resources)
	if err != nil {
		return fmt.Errorf("encode resource policy: %w", err)
	}
	healthJSON, err := json.Marshal(task.Health)
	if err != nil {
		return fmt.Errorf("encode task health policy: %w", err)
	}
	exclusionsJSON := "[]"
	if task.Directory != nil {
		encoded, err := json.Marshal(task.Directory.Exclusions)
		if err != nil {
			return fmt.Errorf("encode exclusions: %w", err)
		}
		exclusionsJSON = string(encoded)
	}
	targetJSON, err := json.Marshal(task.EffectiveExecutionTarget())
	if err != nil {
		return fmt.Errorf("encode execution target: %w", err)
	}
	if task.EffectiveEngine() == domain.ResticEngine {
		if err := ensureRepositoryRetentionPolicy(ctx, tx, task.RepositoryID, task.Retention, task.UpdatedAt); err != nil {
			return err
		}
		task.Retention = domain.RetentionPolicy{}
		retentionJSON = []byte("{}")
	}
	confirmationJSON := encodeTaskScopeConfirmation(task.ScopeConfirmation)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO tasks(
			id, name, engine, kind, execution_target_json, repository_id, source_json, retention_json,
			resources_json, health_policy_json, exclusions_json, scope_confirmation_json, enabled, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, task.ID, task.Name, task.EffectiveEngine(), task.Kind, string(targetJSON), nullString(task.RepositoryID), string(sourceJSON), string(retentionJSON),
		string(resourcesJSON), string(healthJSON), exclusionsJSON, confirmationJSON, boolInt(task.Enabled), formatTime(task.CreatedAt), formatTime(task.UpdatedAt))
	if isUniqueConstraint(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("create task: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ListTasks(ctx context.Context) ([]domain.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, engine, kind, execution_target_json, COALESCE(repository_id,''), source_json, retention_json,
		       resources_json, health_policy_json, exclusions_json, scope_confirmation_json, enabled, created_at, updated_at
		FROM tasks ORDER BY name, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	result := make([]domain.Task, 0)
	for rows.Next() {
		var item domain.Task
		var sourceJSON, targetJSON, retentionJSON, resourcesJSON, healthJSON, exclusionsJSON, confirmationJSON, createdAt, updatedAt string
		var enabled int
		if err := rows.Scan(&item.ID, &item.Name, &item.Engine, &item.Kind, &targetJSON, &item.RepositoryID, &sourceJSON,
			&retentionJSON, &resourcesJSON, &healthJSON, &exclusionsJSON, &confirmationJSON, &enabled, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		if err := json.Unmarshal([]byte(targetJSON), &item.ExecutionTarget); err != nil {
			return nil, fmt.Errorf("decode execution target: %w", err)
		}
		switch item.EffectiveEngine() {
		case domain.RsyncEngine:
			item.Rsync = &domain.RsyncSource{}
			if err := json.Unmarshal([]byte(sourceJSON), item.Rsync); err != nil {
				return nil, fmt.Errorf("decode rsync source: %w", err)
			}
		case domain.ResticEngine:
			if item.Kind == domain.DirectoryTask {
				item.Directory = &domain.DirectorySource{}
				if err := json.Unmarshal([]byte(sourceJSON), item.Directory); err != nil {
					return nil, fmt.Errorf("decode directory source: %w", err)
				}
				if err := json.Unmarshal([]byte(exclusionsJSON), &item.Directory.Exclusions); err != nil {
					return nil, fmt.Errorf("decode exclusions: %w", err)
				}
			} else {
				item.Database = &domain.DatabaseSource{}
				if err := json.Unmarshal([]byte(sourceJSON), item.Database); err != nil {
					return nil, fmt.Errorf("decode database source: %w", err)
				}
			}
		default:
			return nil, fmt.Errorf("unsupported task engine %q", item.Engine)
		}
		if err := json.Unmarshal([]byte(retentionJSON), &item.Retention); err != nil {
			return nil, fmt.Errorf("decode retention policy: %w", err)
		}
		if err := json.Unmarshal([]byte(resourcesJSON), &item.Resources); err != nil {
			return nil, fmt.Errorf("decode resource policy: %w", err)
		}
		if err := json.Unmarshal([]byte(healthJSON), &item.Health); err != nil {
			return nil, fmt.Errorf("decode task health policy: %w", err)
		}
		if err := json.Unmarshal([]byte(confirmationJSON), &item.ScopeConfirmation); err != nil {
			return nil, fmt.Errorf("decode task scope confirmation: %w", err)
		}
		item.Enabled = enabled != 0
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		item.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func encodeTaskScopeConfirmation(confirmation domain.TaskScopeConfirmation) string {
	if !confirmation.Present() {
		return "{}"
	}
	encoded, err := json.Marshal(confirmation)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func (s *Store) CreatePlan(ctx context.Context, plan domain.Plan) error {
	scheduleJSON, err := json.Marshal(plan.Schedule)
	if err != nil {
		return fmt.Errorf("encode plan schedule: %w", err)
	}
	anchor := plan.ScheduleAnchorAt
	if anchor.IsZero() {
		anchor = plan.CreatedAt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create plan: %w", err)
	}
	defer tx.Rollback()
	if plan.Enabled {
		for _, taskID := range plan.TaskIDs {
			var enabled int
			if err := tx.QueryRowContext(ctx, `SELECT enabled FROM tasks WHERE id=?`, taskID).Scan(&enabled); err != nil || enabled == 0 {
				return ErrConflict
			}
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO plans(id, name, schedule_json, timezone, max_parallel, enabled, catch_up_window_minutes, schedule_anchor_at, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, plan.ID, plan.Name, string(scheduleJSON), plan.Timezone, plan.MaxParallel, boolInt(plan.Enabled),
		plan.CatchUpWindowMinutes, formatTime(anchor), formatTime(plan.CreatedAt), formatTime(plan.UpdatedAt))
	if isUniqueConstraint(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("create plan: %w", err)
	}
	for position, taskID := range plan.TaskIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO plan_tasks(plan_id, task_id, position) VALUES(?, ?, ?)
		`, plan.ID, taskID, position); err != nil {
			return fmt.Errorf("attach task to plan: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plan: %w", err)
	}
	return nil
}

func (s *Store) ListPlans(ctx context.Context) ([]domain.Plan, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, schedule_json, timezone, max_parallel, enabled, catch_up_window_minutes, schedule_anchor_at, created_at, updated_at
		FROM plans ORDER BY name, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	result := make([]domain.Plan, 0)
	for rows.Next() {
		var item domain.Plan
		var scheduleJSON, scheduleAnchorAt, createdAt, updatedAt string
		var enabled int
		if err := rows.Scan(&item.ID, &item.Name, &scheduleJSON, &item.Timezone, &item.MaxParallel, &enabled, &item.CatchUpWindowMinutes, &scheduleAnchorAt, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan plan: %w", err)
		}
		if err := json.Unmarshal([]byte(scheduleJSON), &item.Schedule); err != nil {
			return nil, fmt.Errorf("decode plan schedule: %w", err)
		}
		item.Enabled = enabled != 0
		item.ScheduleAnchorAt, err = parseTime(scheduleAnchorAt)
		if err != nil {
			return nil, err
		}
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		item.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range result {
		taskRows, err := s.db.QueryContext(ctx, `SELECT task_id FROM plan_tasks WHERE plan_id = ? ORDER BY position`, result[i].ID)
		if err != nil {
			return nil, fmt.Errorf("list plan tasks: %w", err)
		}
		for taskRows.Next() {
			var taskID string
			if err := taskRows.Scan(&taskID); err != nil {
				_ = taskRows.Close()
				return nil, err
			}
			result[i].TaskIDs = append(result[i].TaskIDs, taskID)
		}
		if err := taskRows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) ListRemoteHosts(ctx context.Context) ([]domain.RemoteHost, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, host, port, username, COALESCE(host_fingerprint, ''), created_at, updated_at
		FROM remote_hosts ORDER BY name, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list remote hosts: %w", err)
	}
	defer rows.Close()

	hosts := make([]domain.RemoteHost, 0)
	for rows.Next() {
		var host domain.RemoteHost
		var createdAt, updatedAt string
		if err := rows.Scan(&host.ID, &host.Name, &host.Host, &host.Port, &host.Username,
			&host.HostFingerprint, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan remote host: %w", err)
		}
		host.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		host.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate remote hosts: %w", err)
	}
	return hosts, nil
}

func (s *Store) RemoteHostPrivateKeySecretID(ctx context.Context, id string) (string, error) {
	var secretID string
	err := s.db.QueryRowContext(ctx, `SELECT private_key_secret_id FROM remote_hosts WHERE id=?`, id).Scan(&secretID)
	if err != nil {
		return "", err
	}
	return secretID, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored timestamp: %w", err)
	}
	return parsed, nil
}

func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
