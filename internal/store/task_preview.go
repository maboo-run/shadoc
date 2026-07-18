package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

type TaskScopePreview struct {
	ID                         string         `json:"previewId"`
	TaskID                     string         `json:"taskId"`
	Fingerprint                string         `json:"fingerprint"`
	Summary                    map[string]any `json:"summary"`
	RequiresDeleteConfirmation bool           `json:"requiresDeleteConfirmation"`
	CreatedAt                  time.Time      `json:"createdAt"`
	ExpiresAt                  time.Time      `json:"expiresAt"`
	ConsumedAt                 *time.Time     `json:"consumedAt,omitempty"`
}

func (s *Store) CreateTaskScopePreview(ctx context.Context, preview TaskScopePreview) error {
	if preview.ID == "" || preview.TaskID == "" || preview.Fingerprint == "" || preview.CreatedAt.IsZero() || !preview.ExpiresAt.After(preview.CreatedAt) {
		return errors.New("task scope preview is incomplete")
	}
	summary, err := json.Marshal(preview.Summary)
	if err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM task_scope_previews WHERE expires_at<?`, formatTime(preview.CreatedAt.Add(-24*time.Hour)))
	_, err = s.db.ExecContext(ctx, `INSERT INTO task_scope_previews(id,task_id,fingerprint,summary_json,requires_delete_confirmation,created_at,expires_at) VALUES(?,?,?,?,?,?,?)`,
		preview.ID, preview.TaskID, preview.Fingerprint, string(summary), boolInt(preview.RequiresDeleteConfirmation), formatTime(preview.CreatedAt), formatTime(preview.ExpiresAt))
	return constraintError(err)
}

func (s *Store) TaskScopePreview(ctx context.Context, id string) (TaskScopePreview, error) {
	return scanTaskScopePreview(s.db.QueryRowContext(ctx, `SELECT id,task_id,fingerprint,summary_json,requires_delete_confirmation,created_at,expires_at,consumed_at FROM task_scope_previews WHERE id=?`, id))
}

func (s *Store) ConsumeTaskScopePreview(ctx context.Context, id, taskID, fingerprint, actor string, deleteConfirmed bool, now time.Time) (domain.TaskScopeConfirmation, error) {
	if strings.TrimSpace(actor) == "" {
		return domain.TaskScopeConfirmation{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.TaskScopeConfirmation{}, err
	}
	defer tx.Rollback()
	preview, err := scanTaskScopePreview(tx.QueryRowContext(ctx, `SELECT id,task_id,fingerprint,summary_json,requires_delete_confirmation,created_at,expires_at,consumed_at FROM task_scope_previews WHERE id=?`, id))
	if err != nil {
		return domain.TaskScopeConfirmation{}, err
	}
	if preview.TaskID != taskID || preview.Fingerprint != fingerprint || preview.ConsumedAt != nil || !now.Before(preview.ExpiresAt) || preview.RequiresDeleteConfirmation && !deleteConfirmed {
		return domain.TaskScopeConfirmation{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE task_scope_previews SET consumed_at=? WHERE id=? AND consumed_at IS NULL`, formatTime(now), id)
	if err != nil {
		return domain.TaskScopeConfirmation{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return domain.TaskScopeConfirmation{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return domain.TaskScopeConfirmation{}, err
	}
	return domain.TaskScopeConfirmation{
		PreviewID: preview.ID, Fingerprint: preview.Fingerprint, ConfirmedBy: actor, ConfirmedAt: now.UTC(),
		Summary: preview.Summary, DeleteConfirmed: preview.RequiresDeleteConfirmation && deleteConfirmed,
	}, nil
}

type taskScopePreviewScanner interface{ Scan(...any) error }

func scanTaskScopePreview(row taskScopePreviewScanner) (TaskScopePreview, error) {
	var preview TaskScopePreview
	var summary, created, expires string
	var requiresDelete int
	var consumed sql.NullString
	if err := row.Scan(&preview.ID, &preview.TaskID, &preview.Fingerprint, &summary, &requiresDelete, &created, &expires, &consumed); err != nil {
		return TaskScopePreview{}, err
	}
	if err := json.Unmarshal([]byte(summary), &preview.Summary); err != nil {
		return TaskScopePreview{}, err
	}
	preview.RequiresDeleteConfirmation = requiresDelete != 0
	preview.CreatedAt, _ = parseTime(created)
	preview.ExpiresAt, _ = parseTime(expires)
	if consumed.Valid {
		value, _ := parseTime(consumed.String)
		preview.ConsumedAt = &value
	}
	return preview, nil
}
