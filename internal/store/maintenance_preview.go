package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

type MaintenancePreview struct {
	ID                string                 `json:"previewId"`
	RepositoryID      string                 `json:"repositoryId"`
	Retention         domain.RetentionPolicy `json:"retention"`
	PolicyFingerprint string                 `json:"policyFingerprint"`
	KeepCount         int                    `json:"keepCount"`
	RemoveCount       int                    `json:"removeCount"`
	CreatedAt         time.Time              `json:"createdAt"`
	ExpiresAt         time.Time              `json:"expiresAt"`
	ConsumedAt        *time.Time             `json:"consumedAt,omitempty"`
}

func (s *Store) CreateMaintenancePreview(ctx context.Context, preview MaintenancePreview) error {
	retention, _ := json.Marshal(preview.Retention)
	_, err := s.db.ExecContext(ctx, `INSERT INTO maintenance_previews(id,repository_id,retention_json,keep_count,remove_count,created_at,expires_at) VALUES(?,?,?,?,?,?,?)`,
		preview.ID, preview.RepositoryID, string(retention), preview.KeepCount, preview.RemoveCount, formatTime(preview.CreatedAt), formatTime(preview.ExpiresAt))
	return err
}

func (s *Store) MaintenancePreview(ctx context.Context, id string) (MaintenancePreview, error) {
	return scanMaintenancePreview(s.db.QueryRowContext(ctx, `SELECT id,repository_id,retention_json,keep_count,remove_count,created_at,expires_at,consumed_at FROM maintenance_previews WHERE id=?`, id))
}

func (s *Store) ConsumeMaintenancePreview(ctx context.Context, id, repositoryID string, retention domain.RetentionPolicy, now time.Time) (MaintenancePreview, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MaintenancePreview{}, err
	}
	defer tx.Rollback()
	preview, err := scanMaintenancePreview(tx.QueryRowContext(ctx, `SELECT id,repository_id,retention_json,keep_count,remove_count,created_at,expires_at,consumed_at FROM maintenance_previews WHERE id=?`, id))
	if err != nil {
		return MaintenancePreview{}, err
	}
	want, _ := json.Marshal(retention)
	got, _ := json.Marshal(preview.Retention)
	if preview.RepositoryID != repositoryID || string(got) != string(want) || preview.ConsumedAt != nil || now.After(preview.ExpiresAt) {
		return MaintenancePreview{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE maintenance_previews SET consumed_at=? WHERE id=? AND consumed_at IS NULL`, formatTime(now), id)
	if err != nil {
		return MaintenancePreview{}, err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return MaintenancePreview{}, ErrConflict
	}
	consumed := now.UTC()
	preview.ConsumedAt = &consumed
	if err := tx.Commit(); err != nil {
		return MaintenancePreview{}, err
	}
	return preview, nil
}

type maintenancePreviewScanner interface{ Scan(...any) error }

func scanMaintenancePreview(row maintenancePreviewScanner) (MaintenancePreview, error) {
	var preview MaintenancePreview
	var retention, created, expires string
	var consumed sql.NullString
	if err := row.Scan(&preview.ID, &preview.RepositoryID, &retention, &preview.KeepCount, &preview.RemoveCount, &created, &expires, &consumed); err != nil {
		return MaintenancePreview{}, err
	}
	_ = json.Unmarshal([]byte(retention), &preview.Retention)
	preview.PolicyFingerprint = preview.Retention.Fingerprint()
	preview.CreatedAt, _ = parseTime(created)
	preview.ExpiresAt, _ = parseTime(expires)
	if consumed.Valid {
		value, _ := parseTime(consumed.String)
		preview.ConsumedAt = &value
	}
	return preview, nil
}
