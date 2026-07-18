package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type OperationRecord struct {
	ID           string         `json:"id"`
	Kind         string         `json:"kind"`
	Actor        string         `json:"actor"`
	RepositoryID string         `json:"repositoryId,omitempty"`
	TaskID       string         `json:"taskId,omitempty"`
	SnapshotID   string         `json:"snapshotId,omitempty"`
	Target       string         `json:"target,omitempty"`
	Status       string         `json:"status"`
	Stage        string         `json:"stage"`
	CreatedAt    time.Time      `json:"createdAt"`
	StartedAt    *time.Time     `json:"startedAt,omitempty"`
	FinishedAt   *time.Time     `json:"finishedAt,omitempty"`
	AttemptCount int            `json:"attemptCount"`
	ErrorSummary string         `json:"errorSummary,omitempty"`
	Detail       map[string]any `json:"detail,omitempty"`
}

func (s *Store) CreateOperation(ctx context.Context, operation OperationRecord) error {
	detail, _ := json.Marshal(nonNilMap(operation.Detail))
	_, err := s.db.ExecContext(ctx, `INSERT INTO operations(id,kind,actor,repository_id,task_id,snapshot_id,target,status,stage,created_at,attempt_count,error_summary,detail_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		operation.ID, operation.Kind, operation.Actor, operation.RepositoryID, operation.TaskID, operation.SnapshotID, operation.Target,
		operation.Status, operation.Stage, formatTime(operation.CreatedAt), operation.AttemptCount, operation.ErrorSummary, string(detail))
	return err
}

func (s *Store) StartOperation(ctx context.Context, id, stage string, started time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE operations SET status='running',stage=?,started_at=?,attempt_count=attempt_count+1 WHERE id=? AND status='queued'`, stage, formatTime(started), id)
	return operationUpdateResult(result, err)
}

func (s *Store) UpdateOperationStage(ctx context.Context, id, stage string, detail map[string]any) error {
	current, err := s.Operation(ctx, id)
	if err != nil {
		return err
	}
	merged := nonNilMap(current.Detail)
	for key, value := range detail {
		merged[key] = value
	}
	encoded, _ := json.Marshal(merged)
	result, err := s.db.ExecContext(ctx, `UPDATE operations SET stage=?,detail_json=? WHERE id=? AND status='running'`, stage, string(encoded), id)
	return operationUpdateResult(result, err)
}

func (s *Store) FinishOperation(ctx context.Context, id, status, stage string, finished time.Time, errorSummary string, detail map[string]any) error {
	current, err := s.Operation(ctx, id)
	if err != nil {
		return err
	}
	merged := nonNilMap(current.Detail)
	for key, value := range detail {
		merged[key] = value
	}
	encoded, _ := json.Marshal(merged)
	result, err := s.db.ExecContext(ctx, `UPDATE operations SET status=?,stage=?,finished_at=?,error_summary=?,detail_json=? WHERE id=? AND status IN ('queued','running')`, status, stage, formatTime(finished), errorSummary, string(encoded), id)
	return operationUpdateResult(result, err)
}

func (s *Store) ResolveOperationCleanup(ctx context.Context, id string, resolved time.Time, detail map[string]any) error {
	current, err := s.Operation(ctx, id)
	if err != nil {
		return err
	}
	merged := nonNilMap(current.Detail)
	for key, value := range detail {
		merged[key] = value
	}
	merged["cleanupResolvedAt"] = formatTime(resolved)
	encoded, _ := json.Marshal(merged)
	result, err := s.db.ExecContext(ctx, `UPDATE operations SET status='failed',stage='cleanup_resolved',detail_json=? WHERE id=? AND status='cleanup_required'`, string(encoded), id)
	return operationUpdateResult(result, err)
}

func (s *Store) Operation(ctx context.Context, id string) (OperationRecord, error) {
	row := s.db.QueryRowContext(ctx, operationSelect+` WHERE id=?`, id)
	return scanOperation(row)
}

func (s *Store) ListOperations(ctx context.Context, limit int, kind, status string) ([]OperationRecord, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	query := operationSelect
	args := make([]any, 0, 3)
	switch {
	case kind != "" && status != "":
		query += ` WHERE kind=? AND status=?`
		args = append(args, kind, status)
	case kind != "":
		query += ` WHERE kind=?`
		args = append(args, kind)
	case status != "":
		query += ` WHERE status=?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]OperationRecord, 0)
	for rows.Next() {
		item, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ActiveApplicationUpdate(ctx context.Context) (OperationRecord, error) {
	row := s.db.QueryRowContext(ctx, operationSelect+` WHERE kind='application_update' AND status IN ('queued','running') ORDER BY created_at DESC LIMIT 1`)
	return scanOperation(row)
}

func (s *Store) RecoverInterruptedOperations(ctx context.Context, at time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE operations
		SET status='failed',stage='interrupted',finished_at=?,error_summary='service restarted while operation was active'
		WHERE status IN ('queued','running')
		  AND NOT (
			kind='application_update'
			AND stage IN ('launching_updater','downloading_release','release_verified','saving_rollback','replacing_binary','restarting_service','verifying_health','rolling_back','verifying_rollback','rollback_verified')
			AND started_at>=?
		  )
	`, formatTime(at), formatTime(at.Add(-10*time.Minute)))
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

const operationSelect = `SELECT id,kind,actor,repository_id,task_id,snapshot_id,target,status,stage,created_at,started_at,finished_at,attempt_count,error_summary,detail_json FROM operations`

type operationScanner interface {
	Scan(...any) error
}

func scanOperation(scanner operationScanner) (OperationRecord, error) {
	var operation OperationRecord
	var created, detail string
	var started, finished sql.NullString
	if err := scanner.Scan(&operation.ID, &operation.Kind, &operation.Actor, &operation.RepositoryID, &operation.TaskID, &operation.SnapshotID, &operation.Target, &operation.Status, &operation.Stage, &created, &started, &finished, &operation.AttemptCount, &operation.ErrorSummary, &detail); err != nil {
		return OperationRecord{}, err
	}
	operation.CreatedAt, _ = parseTime(created)
	if started.Valid {
		value, _ := parseTime(started.String)
		operation.StartedAt = &value
	}
	if finished.Valid {
		value, _ := parseTime(finished.String)
		operation.FinishedAt = &value
	}
	_ = json.Unmarshal([]byte(detail), &operation.Detail)
	operation.Detail = nonNilMap(operation.Detail)
	return operation, nil
}

func operationUpdateResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("operation transition conflict")
	}
	return nil
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}
