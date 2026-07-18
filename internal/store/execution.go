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
	runcontrol "github.com/maboo-run/shadoc/internal/run"
)

type TaskExecution struct {
	Task                       domain.Task
	Repository                 domain.Repository
	Host                       domain.RemoteHost
	PrivateKeySecretID         string
	RepositoryPasswordSecretID string
	DatabaseConnection         *domain.DatabaseConnection
	DatabasePasswordSecretID   string
}

type RepositoryExecution struct {
	Repository                 domain.Repository
	Host                       domain.RemoteHost
	PrivateKeySecretID         string
	RepositoryPasswordSecretID string
}

type RemoteHostExecution struct {
	Host               domain.RemoteHost
	PrivateKeySecretID string
}

type RsyncExecution struct {
	Task               domain.Task
	Repository         domain.Repository
	Host               domain.RemoteHost
	PrivateKeySecretID string
}

func (s *Store) LoadRsyncExecution(ctx context.Context, taskID string) (RsyncExecution, error) {
	var result RsyncExecution
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return result, err
	}
	for _, task := range tasks {
		if task.ID == taskID {
			result.Task = task
			break
		}
	}
	if result.Task.ID == "" || result.Task.Rsync == nil {
		return result, sql.ErrNoRows
	}
	if result.Task.RepositoryID != "" {
		repository, err := s.LoadRepositoryExecution(ctx, result.Task.RepositoryID)
		if err != nil {
			return result, err
		}
		result.Repository, result.Host, result.PrivateKeySecretID = repository.Repository, repository.Host, repository.PrivateKeySecretID
		return result, nil
	}
	if result.Task.Rsync.EffectiveDestinationKind() == domain.RsyncDestinationLocal {
		return result, nil
	}
	var created, updated string
	err = s.db.QueryRowContext(ctx, `SELECT id,name,host,port,username,COALESCE(host_fingerprint,''),created_at,updated_at,private_key_secret_id FROM remote_hosts WHERE id=?`, result.Task.Rsync.DestinationHostID).Scan(&result.Host.ID, &result.Host.Name, &result.Host.Host, &result.Host.Port, &result.Host.Username, &result.Host.HostFingerprint, &created, &updated, &result.PrivateKeySecretID)
	if err != nil {
		return result, err
	}
	result.Host.CreatedAt, _ = parseTime(created)
	result.Host.UpdatedAt, _ = parseTime(updated)
	return result, nil
}

type RepositoryKeyRevocation struct {
	RepositoryID string
	KeyID        string
	SecretID     string
	CreatedAt    time.Time
}
type DatabaseConnectionExecution struct {
	Connection       domain.DatabaseConnection
	PasswordSecretID string
}

func (s *Store) LoadDatabaseConnectionExecution(ctx context.Context, id string) (DatabaseConnectionExecution, error) {
	var r DatabaseConnectionExecution
	var tlsJSON, toolsJSON, checked, created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,engine,purpose,network,COALESCE(host,''),COALESCE(port,0),COALESCE(socket_path,''),username,password_secret_id,tls_json,tool_paths_json,status,COALESCE(preflight_checked_at,''),preflight_client_version,preflight_server_version,preflight_error,created_at,updated_at FROM database_connections WHERE id=?`, id).Scan(&r.Connection.ID, &r.Connection.Name, &r.Connection.Engine, &r.Connection.Purpose, &r.Connection.Network, &r.Connection.Host, &r.Connection.Port, &r.Connection.SocketPath, &r.Connection.Username, &r.PasswordSecretID, &tlsJSON, &toolsJSON, &r.Connection.Status, &checked, &r.Connection.Preflight.ClientVersion, &r.Connection.Preflight.ServerVersion, &r.Connection.Preflight.Error, &created, &updated)
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(tlsJSON), &r.Connection.TLS)
	_ = json.Unmarshal([]byte(toolsJSON), &r.Connection.ToolPaths)
	r.Connection.CreatedAt, _ = parseTime(created)
	r.Connection.UpdatedAt, _ = parseTime(updated)
	if checked != "" {
		r.Connection.Preflight.CheckedAt, _ = parseTime(checked)
	}
	return r, nil
}

func (s *Store) LoadRepositoryExecution(ctx context.Context, id string) (RepositoryExecution, error) {
	var r RepositoryExecution
	var rc, ru, hc, hu, backendJSON, backendSecretID string
	err := s.db.QueryRowContext(ctx, `SELECT r.id,r.name,r.engine,r.kind,COALESCE(r.remote_host_id,''),r.path,r.backend_json,COALESCE(r.backend_secret_id,''),r.status,r.created_at,r.updated_at,COALESCE(r.password_secret_id,''),COALESCE(h.id,''),COALESCE(h.name,''),COALESCE(h.host,''),COALESCE(h.port,0),COALESCE(h.username,''),COALESCE(h.host_fingerprint,''),COALESCE(h.created_at,''),COALESCE(h.updated_at,''),COALESCE(h.private_key_secret_id,'') FROM repositories r LEFT JOIN remote_hosts h ON h.id=r.remote_host_id WHERE r.id=?`, id).Scan(&r.Repository.ID, &r.Repository.Name, &r.Repository.Engine, &r.Repository.Kind, &r.Repository.RemoteHostID, &r.Repository.Path, &backendJSON, &backendSecretID, &r.Repository.Status, &rc, &ru, &r.RepositoryPasswordSecretID, &r.Host.ID, &r.Host.Name, &r.Host.Host, &r.Host.Port, &r.Host.Username, &r.Host.HostFingerprint, &hc, &hu, &r.PrivateKeySecretID)
	if err != nil {
		return r, err
	}
	if err := decodeRepositoryBackend(&r.Repository, backendJSON, backendSecretID); err != nil {
		return RepositoryExecution{}, err
	}
	r.Repository.CreatedAt, _ = parseTime(rc)
	r.Repository.UpdatedAt, _ = parseTime(ru)
	if hc != "" {
		r.Host.CreatedAt, _ = parseTime(hc)
		r.Host.UpdatedAt, _ = parseTime(hu)
	}
	return r, nil
}

func (s *Store) LoadRemoteHostExecution(ctx context.Context, id string) (RemoteHostExecution, error) {
	var result RemoteHostExecution
	var created, updated string
	err := s.db.QueryRowContext(ctx, `
		SELECT id,name,host,port,username,COALESCE(host_fingerprint,''),created_at,updated_at,private_key_secret_id
		FROM remote_hosts WHERE id=?
	`, id).Scan(&result.Host.ID, &result.Host.Name, &result.Host.Host, &result.Host.Port, &result.Host.Username, &result.Host.HostFingerprint, &created, &updated, &result.PrivateKeySecretID)
	if err != nil {
		return result, err
	}
	result.Host.CreatedAt, _ = parseTime(created)
	result.Host.UpdatedAt, _ = parseTime(updated)
	return result, nil
}

func (s *Store) UpdateRepositoryStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE repositories SET status=?,updated_at=? WHERE id=?`, status, formatTime(time.Now()), id)
	return err
}
func (s *Store) CommitRepositoryPasswordRotation(ctx context.Context, id, newSecretID, oldKeyID, oldSecretID string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentSecretID string
	if err := tx.QueryRowContext(ctx, `SELECT password_secret_id FROM repositories WHERE id=?`, id).Scan(&currentSecretID); err != nil {
		return err
	}
	if currentSecretID != oldSecretID {
		return ErrConflict
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repository_key_revocations WHERE repository_id=?`, id).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repository_key_revocations(repository_id,key_id,secret_id,created_at) VALUES(?,?,?,?)`, id, oldKeyID, oldSecretID, formatTime(at)); err != nil {
		return constraintError(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE repositories SET password_secret_id=?,updated_at=? WHERE id=?`, newSecretID, formatTime(at), id)
	if err != nil {
		return constraintError(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) PendingRepositoryKeyRevocation(ctx context.Context, id string) (RepositoryKeyRevocation, bool, error) {
	var pending RepositoryKeyRevocation
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT repository_id,key_id,secret_id,created_at FROM repository_key_revocations WHERE repository_id=?`, id).Scan(&pending.RepositoryID, &pending.KeyID, &pending.SecretID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryKeyRevocation{}, false, nil
	}
	if err != nil {
		return RepositoryKeyRevocation{}, false, err
	}
	pending.CreatedAt, _ = parseTime(created)
	return pending, true, nil
}

func (s *Store) CompleteRepositoryKeyRevocation(ctx context.Context, id, keyID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var secretID string
	if err := tx.QueryRowContext(ctx, `SELECT secret_id FROM repository_key_revocations WHERE repository_id=? AND key_id=?`, id, keyID).Scan(&secretID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM repository_key_revocations WHERE repository_id=? AND key_id=?`, id, keyID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id=?`, secretID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LoadTaskExecution(ctx context.Context, taskID string) (TaskExecution, error) {
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return TaskExecution{}, err
	}
	var task domain.Task
	for _, candidate := range tasks {
		if candidate.ID == taskID {
			task = candidate
			break
		}
	}
	if task.ID == "" {
		return TaskExecution{}, sql.ErrNoRows
	}
	var result TaskExecution
	result.Task = task
	var created, updated, backendJSON, backendSecretID string
	var hostCreated, hostUpdated string
	err = s.db.QueryRowContext(ctx, `SELECT r.id,r.name,r.engine,r.kind,COALESCE(r.remote_host_id,''),r.path,r.backend_json,COALESCE(r.backend_secret_id,''),r.status,r.created_at,r.updated_at,COALESCE(r.password_secret_id,''),COALESCE(h.id,''),COALESCE(h.name,''),COALESCE(h.host,''),COALESCE(h.port,0),COALESCE(h.username,''),COALESCE(h.host_fingerprint,''),COALESCE(h.created_at,''),COALESCE(h.updated_at,''),COALESCE(h.private_key_secret_id,'') FROM repositories r LEFT JOIN remote_hosts h ON h.id=r.remote_host_id WHERE r.id=?`, task.RepositoryID).Scan(
		&result.Repository.ID, &result.Repository.Name, &result.Repository.Engine, &result.Repository.Kind, &result.Repository.RemoteHostID, &result.Repository.Path, &backendJSON, &backendSecretID, &result.Repository.Status, &created, &updated, &result.RepositoryPasswordSecretID,
		&result.Host.ID, &result.Host.Name, &result.Host.Host, &result.Host.Port, &result.Host.Username, &result.Host.HostFingerprint, &hostCreated, &hostUpdated, &result.PrivateKeySecretID)
	if err != nil {
		return TaskExecution{}, fmt.Errorf("load task repository: %w", err)
	}
	if err := decodeRepositoryBackend(&result.Repository, backendJSON, backendSecretID); err != nil {
		return TaskExecution{}, err
	}
	result.Repository.CreatedAt, _ = parseTime(created)
	result.Repository.UpdatedAt, _ = parseTime(updated)
	if hostCreated != "" {
		result.Host.CreatedAt, _ = parseTime(hostCreated)
		result.Host.UpdatedAt, _ = parseTime(hostUpdated)
	}
	if task.Database != nil {
		var c domain.DatabaseConnection
		var tlsJSON, toolsJSON, checked string
		err = s.db.QueryRowContext(ctx, `SELECT id,name,engine,purpose,network,COALESCE(host,''),COALESCE(port,0),COALESCE(socket_path,''),username,password_secret_id,tls_json,tool_paths_json,status,COALESCE(preflight_checked_at,''),preflight_client_version,preflight_server_version,preflight_error,created_at,updated_at FROM database_connections WHERE id=?`, task.Database.ConnectionID).Scan(&c.ID, &c.Name, &c.Engine, &c.Purpose, &c.Network, &c.Host, &c.Port, &c.SocketPath, &c.Username, &result.DatabasePasswordSecretID, &tlsJSON, &toolsJSON, &c.Status, &checked, &c.Preflight.ClientVersion, &c.Preflight.ServerVersion, &c.Preflight.Error, &created, &updated)
		if err != nil {
			return TaskExecution{}, fmt.Errorf("load task database connection: %w", err)
		}
		_ = json.Unmarshal([]byte(tlsJSON), &c.TLS)
		_ = json.Unmarshal([]byte(toolsJSON), &c.ToolPaths)
		c.CreatedAt, _ = parseTime(created)
		c.UpdatedAt, _ = parseTime(updated)
		if checked != "" {
			c.Preflight.CheckedAt, _ = parseTime(checked)
		}
		result.DatabaseConnection = &c
	}
	return result, nil
}

type RunRecord struct {
	ID            string         `json:"id"`
	TaskID        string         `json:"taskId"`
	PlanID        string         `json:"planId,omitempty"`
	Trigger       string         `json:"trigger"`
	Status        string         `json:"status"`
	SnapshotID    string         `json:"snapshotId,omitempty"`
	StartedAt     time.Time      `json:"startedAt"`
	FinishedAt    *time.Time     `json:"finishedAt,omitempty"`
	AttemptCount  int            `json:"attemptCount"`
	Summary       map[string]any `json:"summary"`
	RawLog        string         `json:"rawLog,omitempty"`
	RawLogExpired bool           `json:"rawLogExpired"`
	Metrics       *RunMetrics    `json:"metrics,omitempty"`
}

type RunMetrics struct {
	DurationMilliseconds *int64 `json:"durationMilliseconds,omitempty"`
	FilesProcessed       *int64 `json:"filesProcessed,omitempty"`
	FilesChanged         *int64 `json:"filesChanged,omitempty"`
	BytesProcessed       *int64 `json:"bytesProcessed,omitempty"`
	BytesChanged         *int64 `json:"bytesChanged,omitempty"`
}

type TaskRunHealth struct {
	Latest        *RunRecord `json:"latest,omitempty"`
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`
}
type AuditRecord struct {
	ID         int64          `json:"id"`
	OccurredAt time.Time      `json:"occurredAt"`
	Actor      string         `json:"actor,omitempty"`
	Action     string         `json:"action"`
	TargetType string         `json:"targetType"`
	TargetID   string         `json:"targetId,omitempty"`
	Detail     map[string]any `json:"detail"`
}
type AuditFilter struct {
	Action string
	From   *time.Time
	To     *time.Time
	Limit  int
	Offset int
}
type AuditPage struct {
	Items []AuditRecord `json:"items"`
	Total int           `json:"total"`
}

func (s *Store) StartRun(ctx context.Context, r RunRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO runs(id,task_id,plan_id,trigger,status,started_at,attempt_count) VALUES(?,?,?,?,?,?,?)`, r.ID, r.TaskID, nullString(r.PlanID), r.Trigger, r.Status, formatTime(r.StartedAt), r.AttemptCount)
	return err
}
func (s *Store) FinishRun(ctx context.Context, id, status string, finished time.Time, attempts int, snapshot string, summary map[string]any, rawLog string) error {
	if _, ok := runcontrol.ParseTerminalStatus(status); !ok {
		return fmt.Errorf("finish run with non-canonical terminal status %q", status)
	}
	var startedValue string
	if err := s.db.QueryRowContext(ctx, `SELECT started_at FROM runs WHERE id=?`, id).Scan(&startedValue); err != nil {
		return err
	}
	started, err := parseTime(startedValue)
	if err != nil {
		return err
	}
	duration := finished.Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	metrics := RunMetrics{
		DurationMilliseconds: &duration,
		FilesProcessed:       summaryMetric(summary, "filesProcessed"),
		FilesChanged:         summaryMetric(summary, "filesChanged"),
		BytesProcessed:       summaryMetric(summary, "bytesProcessed"),
		BytesChanged:         summaryMetric(summary, "bytesChanged"),
	}
	encoded, _ := json.Marshal(summary)
	_, err = s.db.ExecContext(ctx, `UPDATE runs SET status=?,finished_at=?,attempt_count=?,snapshot_id=?,summary_json=?,raw_log=?,raw_log_expired=0,duration_ms=?,files_processed=?,files_changed=?,bytes_processed=?,bytes_changed=? WHERE id=?`, status, formatTime(finished), attempts, nullString(snapshot), string(encoded), rawLog, nullableMetric(metrics.DurationMilliseconds), nullableMetric(metrics.FilesProcessed), nullableMetric(metrics.FilesChanged), nullableMetric(metrics.BytesProcessed), nullableMetric(metrics.BytesChanged), id)
	return err
}

func (s *Store) RecoverInterruptedRuns(ctx context.Context, at time.Time) (int, error) {
	summary, err := json.Marshal(map[string]any{"error": "service restarted while run was active"})
	if err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status='failed',finished_at=?,summary_json=? WHERE status='running'`, formatTime(at.UTC()), string(summary))
	if err != nil {
		return 0, fmt.Errorf("recover interrupted runs: %w", err)
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

func (s *Store) ListRuns(ctx context.Context, limit int) ([]RunRecord, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, runSelectColumns+` FROM runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RunRecord, 0)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Run(ctx context.Context, id string) (RunRecord, error) {
	return scanRun(s.db.QueryRowContext(ctx, runSelectColumns+` FROM runs WHERE id=?`, id))
}

func (s *Store) LatestSuccessfulRun(ctx context.Context, taskID string) (RunRecord, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM runs
		WHERE task_id=? AND status='success' AND COALESCE(snapshot_id,'')<>''
		ORDER BY COALESCE(finished_at,started_at) DESC,started_at DESC,id DESC
		LIMIT 1
	`, taskID).Scan(&id)
	if err != nil {
		return RunRecord{}, err
	}
	return s.Run(ctx, id)
}

func (s *Store) TaskRunHealth(ctx context.Context) (map[string]TaskRunHealth, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,task_id,COALESCE(plan_id,''),trigger,status,started_at,finished_at,attempt_count,COALESCE(snapshot_id,''),summary_json,raw_log,raw_log_expired,duration_ms,files_processed,files_changed,bytes_processed,bytes_changed
		FROM (
			SELECT runs.*,ROW_NUMBER() OVER(PARTITION BY task_id ORDER BY started_at DESC,id DESC) AS task_rank
			FROM runs
		) WHERE task_rank=1
	`)
	if err != nil {
		return nil, err
	}
	result := map[string]TaskRunHealth{}
	for rows.Next() {
		record, err := scanRun(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		result[record.TaskID] = TaskRunHealth{Latest: &record}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	successRows, err := s.db.QueryContext(ctx, `SELECT task_id,MAX(COALESCE(finished_at,started_at)) FROM runs WHERE status='success' GROUP BY task_id`)
	if err != nil {
		return nil, err
	}
	defer successRows.Close()
	for successRows.Next() {
		var taskID, value string
		if err := successRows.Scan(&taskID, &value); err != nil {
			return nil, err
		}
		at, err := parseTime(value)
		if err != nil {
			return nil, err
		}
		health := result[taskID]
		health.LastSuccessAt = &at
		result[taskID] = health
	}
	return result, successRows.Err()
}

const runSelectColumns = `SELECT id,task_id,COALESCE(plan_id,''),trigger,status,started_at,finished_at,attempt_count,COALESCE(snapshot_id,''),summary_json,raw_log,raw_log_expired,duration_ms,files_processed,files_changed,bytes_processed,bytes_changed`

type runScanner interface {
	Scan(...any) error
}

func scanRun(scanner runScanner) (RunRecord, error) {
	var record RunRecord
	var started, summary string
	var finished sql.NullString
	var duration, filesProcessed, filesChanged, bytesProcessed, bytesChanged sql.NullInt64
	if err := scanner.Scan(&record.ID, &record.TaskID, &record.PlanID, &record.Trigger, &record.Status, &started, &finished, &record.AttemptCount, &record.SnapshotID, &summary, &record.RawLog, &record.RawLogExpired, &duration, &filesProcessed, &filesChanged, &bytesProcessed, &bytesChanged); err != nil {
		return RunRecord{}, err
	}
	var err error
	record.StartedAt, err = parseTime(started)
	if err != nil {
		return RunRecord{}, err
	}
	if finished.Valid {
		value, err := parseTime(finished.String)
		if err != nil {
			return RunRecord{}, err
		}
		record.FinishedAt = &value
	}
	_ = json.Unmarshal([]byte(summary), &record.Summary)
	metrics := RunMetrics{
		DurationMilliseconds: nullMetric(duration), FilesProcessed: nullMetric(filesProcessed), FilesChanged: nullMetric(filesChanged),
		BytesProcessed: nullMetric(bytesProcessed), BytesChanged: nullMetric(bytesChanged),
	}
	if metrics.DurationMilliseconds != nil || metrics.FilesProcessed != nil || metrics.FilesChanged != nil || metrics.BytesProcessed != nil || metrics.BytesChanged != nil {
		record.Metrics = &metrics
	}
	return record, nil
}

func summaryMetric(summary map[string]any, key string) *int64 {
	if summary == nil {
		return nil
	}
	var value int64
	switch number := summary[key].(type) {
	case int:
		value = int64(number)
	case int32:
		value = int64(number)
	case int64:
		value = number
	case uint:
		if uint64(number) > uint64(^uint64(0)>>1) {
			return nil
		}
		value = int64(number)
	case uint64:
		if number > uint64(^uint64(0)>>1) {
			return nil
		}
		value = int64(number)
	case float64:
		if number < 0 || number > float64(^uint64(0)>>1) || number != float64(int64(number)) {
			return nil
		}
		value = int64(number)
	case json.Number:
		parsed, err := number.Int64()
		if err != nil {
			return nil
		}
		value = parsed
	default:
		return nil
	}
	if value < 0 {
		return nil
	}
	return &value
}

func nullableMetric(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullMetric(value sql.NullInt64) *int64 {
	if !value.Valid || value.Int64 < 0 {
		return nil
	}
	result := value.Int64
	return &result
}
func (s *Store) AppendAudit(ctx context.Context, a AuditRecord) error {
	encoded, _ := json.Marshal(a.Detail)
	_, err := s.db.ExecContext(ctx, `INSERT INTO audits(occurred_at,actor,action,target_type,target_id,detail_json) VALUES(?,?,?,?,?,?)`, formatTime(a.OccurredAt), a.Actor, a.Action, a.TargetType, nullString(a.TargetID), string(encoded))
	return err
}
func (s *Store) ListAudits(ctx context.Context, limit int) ([]AuditRecord, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,occurred_at,actor,action,target_type,COALESCE(target_id,''),detail_json FROM audits ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditRecord, 0)
	for rows.Next() {
		var a AuditRecord
		var occurred, detail string
		if err := rows.Scan(&a.ID, &occurred, &a.Actor, &a.Action, &a.TargetType, &a.TargetID, &detail); err != nil {
			return nil, err
		}
		a.OccurredAt, _ = parseTime(occurred)
		_ = json.Unmarshal([]byte(detail), &a.Detail)
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) FilterAudits(ctx context.Context, filter AuditFilter) (AuditPage, error) {
	where := make([]string, 0, 3)
	args := make([]any, 0, 5)
	if filter.Action != "" {
		where = append(where, "action=?")
		args = append(args, filter.Action)
	}
	if filter.From != nil {
		where = append(where, "occurred_at>=?")
		args = append(args, formatTime(*filter.From))
	}
	if filter.To != nil {
		where = append(where, "occurred_at<=?")
		args = append(args, formatTime(*filter.To))
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	var page AuditPage
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audits"+clause, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	queryArgs := append([]any(nil), args...)
	query := `SELECT id,occurred_at,actor,action,target_type,COALESCE(target_id,''),detail_json FROM audits` + clause + ` ORDER BY occurred_at DESC,id DESC`
	if filter.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		queryArgs = append(queryArgs, filter.Limit, filter.Offset)
	}
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	page.Items = make([]AuditRecord, 0)
	for rows.Next() {
		var item AuditRecord
		var occurred, detail string
		if err := rows.Scan(&item.ID, &occurred, &item.Actor, &item.Action, &item.TargetType, &item.TargetID, &detail); err != nil {
			return page, err
		}
		item.OccurredAt, _ = parseTime(occurred)
		_ = json.Unmarshal([]byte(detail), &item.Detail)
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}
func (s *Store) LastNotification(ctx context.Context, key string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT kind FROM notifications WHERE state_key=? ORDER BY id DESC LIMIT 1`, key).Scan(&status)
	return status, err
}
func (s *Store) RecordNotification(ctx context.Context, at time.Time, key, status, message string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO notifications(occurred_at,kind,severity,state_key,message,delivered_at) VALUES(?,?,?,?,?,?)`, formatTime(at), status, status, key, message, formatTime(at))
	return err
}
