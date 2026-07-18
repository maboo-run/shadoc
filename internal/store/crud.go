package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
)

func constraintError(err error) error {
	if err == nil {
		return nil
	}
	if isUniqueConstraint(err) || isForeignKeyConstraint(err) {
		return ErrConflict
	}
	return err
}
func isForeignKeyConstraint(err error) bool {
	return err != nil && (containsFold(err.Error(), "foreign key") || containsFold(err.Error(), "constraint failed"))
}
func containsFold(value, part string) bool {
	return len(value) >= len(part) && (func() bool {
		for i := 0; i+len(part) <= len(value); i++ {
			match := true
			for j := range part {
				a, b := value[i+j], part[j]
				if a >= 'A' && a <= 'Z' {
					a += 32
				}
				if b >= 'A' && b <= 'Z' {
					b += 32
				}
				if a != b {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
		return false
	})()
}

func (s *Store) UpdateRemoteHost(ctx context.Context, h domain.RemoteHost, newSecret string) (string, error) {
	var old string
	if err := s.db.QueryRowContext(ctx, `SELECT private_key_secret_id FROM remote_hosts WHERE id=?`, h.ID).Scan(&old); err != nil {
		return "", err
	}
	secret := old
	if newSecret != "" {
		secret = newSecret
	}
	_, err := s.db.ExecContext(ctx, `UPDATE remote_hosts SET name=?,host=?,port=?,username=?,private_key_secret_id=?,host_fingerprint=?,updated_at=? WHERE id=?`, h.Name, h.Host, h.Port, h.Username, secret, h.HostFingerprint, formatTime(h.UpdatedAt), h.ID)
	return old, constraintError(err)
}
func (s *Store) DeleteRemoteHost(ctx context.Context, id string) (string, error) {
	var secret string
	if err := s.db.QueryRowContext(ctx, `SELECT private_key_secret_id FROM remote_hosts WHERE id=?`, id).Scan(&secret); err != nil {
		return "", err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM remote_hosts WHERE id=?`, id)
	return secret, constraintError(err)
}

func (s *Store) UpdateRepository(ctx context.Context, r domain.Repository, newSecret string) ([]string, error) {
	var old, oldKind, oldHost, oldPath, oldBackendJSON, oldBackendSecret string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(password_secret_id,''),kind,COALESCE(remote_host_id,''),path,backend_json,COALESCE(backend_secret_id,'') FROM repositories WHERE id=?`, r.ID).Scan(&old, &oldKind, &oldHost, &oldPath, &oldBackendJSON, &oldBackendSecret); err != nil {
		return nil, err
	}
	if r.EffectiveKind() == domain.S3Repository && r.BackendSecretID == "" {
		r.BackendSecretID = oldBackendSecret
	}
	backendJSON, err := encodeRepositoryBackend(r)
	if err != nil {
		return nil, err
	}
	if oldKind != string(r.EffectiveKind()) || oldHost != r.RemoteHostID || oldPath != r.Path || oldBackendJSON != backendJSON {
		var references int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE repository_id=?`, r.ID).Scan(&references); err != nil {
			return nil, err
		}
		if references > 0 {
			return nil, ErrConflict
		}
	}
	secret := old
	if newSecret != "" {
		secret = newSecret
	}
	_, err = s.db.ExecContext(ctx, `UPDATE repositories SET name=?,engine=?,kind=?,remote_host_id=?,path=?,password_secret_id=?,backend_json=?,backend_secret_id=?,status=?,updated_at=? WHERE id=?`, r.Name, r.EffectiveEngine(), r.EffectiveKind(), nullString(r.RemoteHostID), r.Path, nullString(secret), backendJSON, nullString(r.BackendSecretID), r.Status, formatTime(r.UpdatedAt), r.ID)
	if err := constraintError(err); err != nil {
		return nil, err
	}
	obsolete := make([]string, 0, 2)
	if newSecret != "" && old != "" && old != secret {
		obsolete = append(obsolete, old)
	}
	if oldBackendSecret != "" && oldBackendSecret != r.BackendSecretID {
		obsolete = append(obsolete, oldBackendSecret)
	}
	return obsolete, nil
}
func (s *Store) DeleteRepository(ctx context.Context, id string) (string, error) {
	var secret string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(password_secret_id,'') FROM repositories WHERE id=?`, id).Scan(&secret); err != nil {
		return "", err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM repositories WHERE id=?`, id)
	return secret, constraintError(err)
}

func (s *Store) UpdateDatabaseConnection(ctx context.Context, c domain.DatabaseConnection, newSecret string) (string, error) {
	var old string
	var oldEngine, oldPurpose string
	if err := s.db.QueryRowContext(ctx, `SELECT password_secret_id,engine,purpose FROM database_connections WHERE id=?`, c.ID).Scan(&old, &oldEngine, &oldPurpose); err != nil {
		return "", err
	}
	if oldEngine != string(c.Engine) || oldPurpose != string(c.Purpose) {
		var references int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE kind='database' AND json_extract(source_json,'$.connectionId')=?`, c.ID).Scan(&references); err != nil {
			return "", err
		}
		if references > 0 {
			return "", ErrConflict
		}
	}
	secret := old
	if newSecret != "" {
		secret = newSecret
	}
	tlsJSON, _ := json.Marshal(c.TLS)
	toolsJSON, _ := json.Marshal(c.ToolPaths)
	_, err := s.db.ExecContext(ctx, `UPDATE database_connections SET name=?,engine=?,purpose=?,network=?,host=?,port=?,socket_path=?,username=?,password_secret_id=?,tls_json=?,tool_paths_json=?,status=?,preflight_checked_at=?,preflight_client_version=?,preflight_server_version=?,preflight_error=?,updated_at=? WHERE id=?`, c.Name, c.Engine, c.Purpose, c.Network, nullString(c.Host), nullInt(c.Port), nullString(c.SocketPath), c.Username, secret, string(tlsJSON), string(toolsJSON), c.Status, nullTime(c.Preflight.CheckedAt), c.Preflight.ClientVersion, c.Preflight.ServerVersion, c.Preflight.Error, formatTime(c.UpdatedAt), c.ID)
	return old, constraintError(err)
}
func (s *Store) DeleteDatabaseConnection(ctx context.Context, id string) (string, error) {
	var secret string
	err := s.db.QueryRowContext(ctx, `SELECT password_secret_id FROM database_connections WHERE id=?`, id).Scan(&secret)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM database_connections WHERE id=?`, id)
	return secret, constraintError(err)
}

func (s *Store) UpdateTask(ctx context.Context, t domain.Task) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldRepositoryID, oldKind, oldEngine, oldSource, oldTarget string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(repository_id,''),kind,engine,source_json,execution_target_json FROM tasks WHERE id=?`, t.ID).Scan(&oldRepositoryID, &oldKind, &oldEngine, &oldSource, &oldTarget); err != nil {
		return normalizeNotFound(err)
	}
	if t.EffectiveEngine() == domain.ResticEngine {
		var repositoryStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM repositories WHERE id=?`, t.RepositoryID).Scan(&repositoryStatus); err != nil || repositoryStatus != "ready" {
			return ErrConflict
		}
	}
	if t.EffectiveEngine() == domain.RsyncEngine && t.RepositoryID != "" {
		var repositoryStatus, repositoryEngine string
		if err := tx.QueryRowContext(ctx, `SELECT status,engine FROM repositories WHERE id=?`, t.RepositoryID).Scan(&repositoryStatus, &repositoryEngine); err != nil || repositoryStatus != "ready" || repositoryEngine != string(domain.RsyncEngine) {
			return ErrConflict
		}
	}
	if t.EffectiveEngine() == domain.ResticEngine && t.Database != nil {
		var purpose string
		err := tx.QueryRowContext(ctx, `SELECT purpose FROM database_connections WHERE id=?`, t.Database.ConnectionID).Scan(&purpose)
		if err != nil || purpose != string(domain.BackupConnection) {
			return ErrConflict
		}
	}
	if !t.Enabled {
		var enabledPlans int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plan_tasks pt JOIN plans p ON p.id=pt.plan_id WHERE pt.task_id=? AND p.enabled=1`, t.ID).Scan(&enabledPlans); err != nil {
			return err
		}
		if enabledPlans > 0 {
			return ErrConflict
		}
		var enabledVerification int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM restore_verification_policies WHERE task_id=? AND enabled=1`, t.ID).Scan(&enabledVerification); err != nil {
			return err
		}
		if enabledVerification > 0 {
			return ErrConflict
		}
	}
	var verificationPolicies int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM restore_verification_policies WHERE task_id=?`, t.ID).Scan(&verificationPolicies); err != nil {
		return err
	}
	if verificationPolicies > 0 && (t.EffectiveEngine() != domain.ResticEngine || t.Kind != domain.DirectoryTask || t.EffectiveExecutionTarget().Kind != execution.Local) {
		return ErrConflict
	}
	var source any
	switch t.EffectiveEngine() {
	case domain.RsyncEngine:
		source = t.Rsync
	case domain.ResticEngine:
		if t.Kind == domain.DirectoryTask {
			source = t.Directory
			break
		}
		source = t.Database
	default:
		return fmt.Errorf("unsupported task engine %q", t.Engine)
	}
	sourceJSON, _ := json.Marshal(source)
	targetJSON, _ := json.Marshal(t.EffectiveExecutionTarget())
	var successfulRuns int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE task_id=? AND status IN ('success','partial')`, t.ID).Scan(&successfulRuns); err != nil {
		return err
	}
	if successfulRuns > 0 && (oldRepositoryID != t.RepositoryID || oldKind != string(t.Kind) || oldEngine != string(t.EffectiveEngine()) || oldSource != string(sourceJSON) || oldTarget != string(targetJSON)) {
		return ErrConflict
	}
	if t.EffectiveEngine() == domain.ResticEngine {
		t.Retention = domain.RetentionPolicy{}
	}
	retention, _ := json.Marshal(t.Retention)
	resources, _ := json.Marshal(t.Resources)
	health, _ := json.Marshal(t.Health)
	exclusions := []byte("[]")
	if t.Directory != nil {
		exclusions, _ = json.Marshal(t.Directory.Exclusions)
	}
	confirmation := encodeTaskScopeConfirmation(t.ScopeConfirmation)
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET name=?,engine=?,kind=?,execution_target_json=?,repository_id=?,source_json=?,retention_json=?,resources_json=?,health_policy_json=?,exclusions_json=?,scope_confirmation_json=?,enabled=?,updated_at=? WHERE id=?`, t.Name, t.EffectiveEngine(), t.Kind, string(targetJSON), nullString(t.RepositoryID), string(sourceJSON), string(retention), string(resources), string(health), string(exclusions), confirmation, boolInt(t.Enabled), formatTime(t.UpdatedAt), t.ID)
	if err != nil {
		return constraintError(err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	var enabledPlans, cleanupRequired int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plan_tasks pt JOIN plans p ON p.id=pt.plan_id WHERE pt.task_id=? AND p.enabled=1`, id).Scan(&enabledPlans); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM restore_verifications WHERE task_id=? AND cleanup_status='required'`, id).Scan(&cleanupRequired); err != nil {
		return err
	}
	if enabledPlans > 0 || cleanupRequired > 0 {
		return ErrConflict
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id=?`, id)
	if err != nil {
		return constraintError(err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdatePlan(ctx context.Context, p domain.Plan) error {
	scheduleJSON, err := json.Marshal(p.Schedule)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previousSchedule, previousTimezone, previousAnchor string
	var previousEnabled int
	if err := tx.QueryRowContext(ctx, `SELECT schedule_json,timezone,enabled,schedule_anchor_at FROM plans WHERE id=?`, p.ID).Scan(&previousSchedule, &previousTimezone, &previousEnabled, &previousAnchor); err != nil {
		return err
	}
	anchor, err := parseTime(previousAnchor)
	if err != nil {
		return err
	}
	if previousSchedule != string(scheduleJSON) || previousTimezone != p.Timezone || (previousEnabled == 0 && p.Enabled) {
		anchor = p.UpdatedAt
	}
	if p.Enabled {
		for _, taskID := range p.TaskIDs {
			var enabled int
			if err := tx.QueryRowContext(ctx, `SELECT enabled FROM tasks WHERE id=?`, taskID).Scan(&enabled); err != nil || enabled == 0 {
				return ErrConflict
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE plans SET name=?,schedule_json=?,timezone=?,max_parallel=?,enabled=?,catch_up_window_minutes=?,schedule_anchor_at=?,updated_at=? WHERE id=?`, p.Name, string(scheduleJSON), p.Timezone, p.MaxParallel, boolInt(p.Enabled), p.CatchUpWindowMinutes, formatTime(anchor), formatTime(p.UpdatedAt), p.ID); err != nil {
		return constraintError(err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM plan_tasks WHERE plan_id=?`, p.ID); err != nil {
		return err
	}
	for i, id := range p.TaskIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO plan_tasks(plan_id,task_id,position) VALUES(?,?,?)`, p.ID, id, i); err != nil {
			return constraintError(err)
		}
	}
	return tx.Commit()
}
func (s *Store) DeletePlan(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM plans WHERE id=?`, id)
	if err != nil {
		return constraintError(err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func normalizeNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	return fmt.Errorf("resource operation: %w", err)
}

var _ = normalizeNotFound
