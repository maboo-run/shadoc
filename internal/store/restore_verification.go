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
	"github.com/maboo-run/shadoc/internal/execution"
)

type RestoreVerificationRecord struct {
	ID             string     `json:"id"`
	TaskID         string     `json:"taskId"`
	RepositoryID   string     `json:"repositoryId"`
	SnapshotID     string     `json:"snapshotId"`
	SelectionPath  string     `json:"selectionPath"`
	Trigger        string     `json:"trigger"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"startedAt"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
	FileCount      int        `json:"fileCount"`
	ByteCount      int64      `json:"byteCount"`
	ManifestSHA256 string     `json:"manifestSha256,omitempty"`
	CleanupStatus  string     `json:"cleanupStatus"`
	ErrorSummary   string     `json:"errorSummary,omitempty"`
}

type RestoreVerificationFinish struct {
	Status         string
	FinishedAt     time.Time
	FileCount      int
	ByteCount      int64
	ManifestSHA256 string
	CleanupStatus  string
	ErrorSummary   string
}

func (s *Store) SaveRestoreVerificationPolicy(ctx context.Context, policy domain.RestoreVerificationPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	var engine, kind, targetJSON string
	var taskEnabled int
	if err := s.db.QueryRowContext(ctx, `SELECT engine,kind,execution_target_json,enabled FROM tasks WHERE id=?`, policy.TaskID).Scan(&engine, &kind, &targetJSON, &taskEnabled); err != nil {
		return constraintError(err)
	}
	var target execution.Target
	if err := json.Unmarshal([]byte(targetJSON), &target); err != nil {
		return fmt.Errorf("decode restore verification target: %w", err)
	}
	if engine != string(domain.ResticEngine) || kind != string(domain.DirectoryTask) || target.Normalized().Kind != execution.Local || (policy.Enabled && taskEnabled == 0) {
		return ErrConflict
	}
	scheduleJSON, err := json.Marshal(policy.Schedule)
	if err != nil {
		return fmt.Errorf("encode restore verification schedule: %w", err)
	}
	anchor := policy.ScheduleAnchorAt
	var previousSchedule, previousTimezone, previousAnchor string
	var previousEnabled int
	err = s.db.QueryRowContext(ctx, `SELECT schedule_json,timezone,enabled,schedule_anchor_at FROM restore_verification_policies WHERE task_id=?`, policy.TaskID).Scan(&previousSchedule, &previousTimezone, &previousEnabled, &previousAnchor)
	if errors.Is(err, sql.ErrNoRows) {
		anchor = policy.UpdatedAt
	} else if err != nil {
		return err
	} else {
		anchor, err = parseTime(previousAnchor)
		if err != nil {
			return err
		}
		if previousSchedule != string(scheduleJSON) || previousTimezone != policy.Timezone || (previousEnabled == 0 && policy.Enabled) {
			anchor = policy.UpdatedAt
		}
	}
	if anchor.IsZero() || policy.UpdatedAt.IsZero() {
		return errors.New("restore verification policy requires an update time")
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO restore_verification_policies(
			task_id,schedule_json,timezone,selection_path,maximum_bytes,maximum_success_age_hours,
			enabled,catch_up_window_minutes,schedule_anchor_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(task_id) DO UPDATE SET
			schedule_json=excluded.schedule_json,timezone=excluded.timezone,selection_path=excluded.selection_path,
			maximum_bytes=excluded.maximum_bytes,maximum_success_age_hours=excluded.maximum_success_age_hours,
			enabled=excluded.enabled,catch_up_window_minutes=excluded.catch_up_window_minutes,
			schedule_anchor_at=excluded.schedule_anchor_at,updated_at=excluded.updated_at
	`, policy.TaskID, string(scheduleJSON), policy.Timezone, policy.SelectionPath, policy.MaximumBytes, policy.MaximumSuccessAgeHours, boolInt(policy.Enabled), policy.CatchUpWindowMinutes, formatTime(anchor), formatTime(policy.UpdatedAt))
	return constraintError(err)
}

func (s *Store) RestoreVerificationPolicy(ctx context.Context, taskID string) (domain.RestoreVerificationPolicy, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT task_id,schedule_json,timezone,selection_path,maximum_bytes,maximum_success_age_hours,
		       enabled,catch_up_window_minutes,schedule_anchor_at,updated_at
		FROM restore_verification_policies WHERE task_id=?
	`, taskID)
	return scanRestoreVerificationPolicy(row)
}

func (s *Store) ListRestoreVerificationPolicies(ctx context.Context) ([]domain.RestoreVerificationPolicy, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id,schedule_json,timezone,selection_path,maximum_bytes,maximum_success_age_hours,
		       enabled,catch_up_window_minutes,schedule_anchor_at,updated_at
		FROM restore_verification_policies ORDER BY task_id
	`)
	if err != nil {
		return nil, err
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

func (s *Store) DeleteRestoreVerificationPolicy(ctx context.Context, taskID string) error {
	var cleanupRequired int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM restore_verifications WHERE task_id=? AND cleanup_status='required'`, taskID).Scan(&cleanupRequired); err != nil {
		return err
	}
	if cleanupRequired > 0 {
		return ErrConflict
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM restore_verification_policies WHERE task_id=?`, taskID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

type restoreVerificationPolicyScanner interface{ Scan(...any) error }

func scanRestoreVerificationPolicy(scanner restoreVerificationPolicyScanner) (domain.RestoreVerificationPolicy, error) {
	var policy domain.RestoreVerificationPolicy
	var scheduleJSON, anchor, updated string
	var enabled int
	if err := scanner.Scan(&policy.TaskID, &scheduleJSON, &policy.Timezone, &policy.SelectionPath, &policy.MaximumBytes, &policy.MaximumSuccessAgeHours, &enabled, &policy.CatchUpWindowMinutes, &anchor, &updated); err != nil {
		return policy, err
	}
	if err := json.Unmarshal([]byte(scheduleJSON), &policy.Schedule); err != nil {
		return policy, fmt.Errorf("decode restore verification schedule: %w", err)
	}
	var err error
	policy.ScheduleAnchorAt, err = parseTime(anchor)
	if err != nil {
		return policy, err
	}
	policy.UpdatedAt, err = parseTime(updated)
	policy.Enabled = enabled != 0
	return policy, err
}

func (s *Store) CreateRestoreVerification(ctx context.Context, record RestoreVerificationRecord) error {
	if record.ID == "" || record.TaskID == "" || record.RepositoryID == "" || record.SnapshotID == "" || record.SelectionPath == "" || record.StartedAt.IsZero() || record.Status != "running" || (record.Trigger != "manual" && record.Trigger != "scheduled") {
		return errors.New("invalid restore verification record")
	}
	if record.CleanupStatus == "" {
		record.CleanupStatus = "pending"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO restore_verifications(
			id,task_id,repository_id,snapshot_id,selection_path,trigger,status,started_at,finished_at,
			file_count,byte_count,manifest_sha256,cleanup_status,error_summary
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, record.ID, record.TaskID, record.RepositoryID, record.SnapshotID, record.SelectionPath, record.Trigger, record.Status, formatTime(record.StartedAt), nil, 0, 0, "", record.CleanupStatus, "")
	return constraintError(err)
}

func (s *Store) FinishRestoreVerification(ctx context.Context, id string, finish RestoreVerificationFinish) error {
	if id == "" || finish.FinishedAt.IsZero() || finish.FileCount < 0 || finish.ByteCount < 0 || !terminalRestoreVerificationStatus(finish.Status) || (finish.CleanupStatus != "removed" && finish.CleanupStatus != "required") {
		return errors.New("invalid restore verification finish")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE restore_verifications
		SET status=?,finished_at=?,file_count=?,byte_count=?,manifest_sha256=?,cleanup_status=?,error_summary=?
		WHERE id=? AND status='running'
	`, finish.Status, formatTime(finish.FinishedAt), finish.FileCount, finish.ByteCount, finish.ManifestSHA256, finish.CleanupStatus, boundedRestoreVerificationError(finish.ErrorSummary), id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) RestoreVerification(ctx context.Context, id string) (RestoreVerificationRecord, error) {
	return scanRestoreVerification(s.db.QueryRowContext(ctx, restoreVerificationSelect+` WHERE id=?`, id))
}

func (s *Store) ListRestoreVerifications(ctx context.Context, taskID string, limit int) ([]RestoreVerificationRecord, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	query, args := restoreVerificationSelect, []any{}
	if taskID != "" {
		query += ` WHERE task_id=?`
		args = append(args, taskID)
	}
	query += ` ORDER BY started_at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RestoreVerificationRecord, 0)
	for rows.Next() {
		record, err := scanRestoreVerification(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) LatestRestoreVerifications(ctx context.Context) (map[string]RestoreVerificationRecord, error) {
	return s.latestRestoreVerifications(ctx, false)
}

func (s *Store) LatestSuccessfulRestoreVerifications(ctx context.Context) (map[string]RestoreVerificationRecord, error) {
	return s.latestRestoreVerifications(ctx, true)
}

func (s *Store) RestoreVerificationCleanupRequired(ctx context.Context) (map[string]RestoreVerificationRecord, error) {
	rows, err := s.db.QueryContext(ctx, restoreVerificationSelect+` WHERE cleanup_status='required' ORDER BY task_id,started_at DESC,id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]RestoreVerificationRecord{}
	for rows.Next() {
		record, err := scanRestoreVerification(rows)
		if err != nil {
			return nil, err
		}
		if _, exists := result[record.TaskID]; !exists {
			result[record.TaskID] = record
		}
	}
	return result, rows.Err()
}

func (s *Store) latestRestoreVerifications(ctx context.Context, successfulOnly bool) (map[string]RestoreVerificationRecord, error) {
	query := restoreVerificationSelect
	if successfulOnly {
		query += ` WHERE status='success'`
	}
	query += ` ORDER BY task_id,started_at DESC,id DESC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]RestoreVerificationRecord{}
	for rows.Next() {
		record, err := scanRestoreVerification(rows)
		if err != nil {
			return nil, err
		}
		if _, exists := result[record.TaskID]; !exists {
			result[record.TaskID] = record
		}
	}
	return result, rows.Err()
}

func (s *Store) RecoverInterruptedRestoreVerifications(ctx context.Context, at time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE restore_verifications
		SET status='interrupted',finished_at=?,cleanup_status='required',error_summary='control service restarted during restore verification'
		WHERE status='running'
	`, formatTime(at.UTC()))
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

func (s *Store) ResolveRestoreVerificationCleanup(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE restore_verifications SET cleanup_status='removed' WHERE id=? AND cleanup_status='required'`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

const restoreVerificationSelect = `
	SELECT id,task_id,repository_id,snapshot_id,selection_path,trigger,status,started_at,finished_at,
	       file_count,byte_count,manifest_sha256,cleanup_status,error_summary
	FROM restore_verifications`

type restoreVerificationScanner interface{ Scan(...any) error }

func scanRestoreVerification(scanner restoreVerificationScanner) (RestoreVerificationRecord, error) {
	var record RestoreVerificationRecord
	var started string
	var finished sql.NullString
	if err := scanner.Scan(&record.ID, &record.TaskID, &record.RepositoryID, &record.SnapshotID, &record.SelectionPath, &record.Trigger, &record.Status, &started, &finished, &record.FileCount, &record.ByteCount, &record.ManifestSHA256, &record.CleanupStatus, &record.ErrorSummary); err != nil {
		return record, err
	}
	var err error
	record.StartedAt, err = parseTime(started)
	if err != nil {
		return record, err
	}
	if finished.Valid {
		value, parseErr := parseTime(finished.String)
		if parseErr != nil {
			return record, parseErr
		}
		record.FinishedAt = &value
	}
	return record, nil
}

func terminalRestoreVerificationStatus(status string) bool {
	switch status {
	case "success", "failed", "cancelled", "interrupted", "cleanup_required":
		return true
	default:
		return false
	}
}

func boundedRestoreVerificationError(value string) string {
	value = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(value))
	if len(value) > 1024 {
		value = value[:1024]
	}
	return value
}
