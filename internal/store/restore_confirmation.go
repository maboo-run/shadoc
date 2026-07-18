package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type RestoreConfirmation struct {
	ID           string         `json:"confirmationId"`
	Actor        string         `json:"-"`
	Kind         string         `json:"kind"`
	Fingerprint  string         `json:"-"`
	Summary      map[string]any `json:"summary"`
	CreatedAt    time.Time      `json:"createdAt"`
	ExpiresAt    time.Time      `json:"expiresAt"`
	AuthorizedAt *time.Time     `json:"authorizedAt,omitempty"`
	ConsumedAt   *time.Time     `json:"consumedAt,omitempty"`
}

func (s *Store) CreateRestoreConfirmation(ctx context.Context, record RestoreConfirmation) error {
	summary, _ := json.Marshal(nonNilMap(record.Summary))
	_, err := s.db.ExecContext(ctx, `INSERT INTO restore_confirmations(id,actor,kind,fingerprint,summary_json,created_at,expires_at) VALUES(?,?,?,?,?,?,?)`, record.ID, record.Actor, record.Kind, record.Fingerprint, string(summary), formatTime(record.CreatedAt), formatTime(record.ExpiresAt))
	return err
}

func (s *Store) RestoreConfirmation(ctx context.Context, id string) (RestoreConfirmation, error) {
	return scanRestoreConfirmation(s.db.QueryRowContext(ctx, restoreConfirmationSelect+` WHERE id=?`, id))
}

func (s *Store) AuthorizeRestoreConfirmation(ctx context.Context, id, actor string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE restore_confirmations SET authorized_at=? WHERE id=? AND actor=? AND authorized_at IS NULL AND consumed_at IS NULL AND expires_at>=?`, formatTime(now), id, actor, formatTime(now))
	return confirmationUpdateResult(result, err)
}

func (s *Store) ConsumeRestoreConfirmation(ctx context.Context, id, actor, fingerprint string, now time.Time) (RestoreConfirmation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RestoreConfirmation{}, err
	}
	defer tx.Rollback()
	record, err := scanRestoreConfirmation(tx.QueryRowContext(ctx, restoreConfirmationSelect+` WHERE id=?`, id))
	if err != nil {
		return RestoreConfirmation{}, err
	}
	if record.Actor != actor || record.Fingerprint != fingerprint || record.AuthorizedAt == nil || record.ConsumedAt != nil || now.After(record.ExpiresAt) || now.Sub(*record.AuthorizedAt) > 5*time.Minute {
		return RestoreConfirmation{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE restore_confirmations SET consumed_at=? WHERE id=? AND consumed_at IS NULL`, formatTime(now), id)
	if err != nil {
		return RestoreConfirmation{}, err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return RestoreConfirmation{}, ErrConflict
	}
	consumed := now.UTC()
	record.ConsumedAt = &consumed
	if err := tx.Commit(); err != nil {
		return RestoreConfirmation{}, err
	}
	return record, nil
}

const restoreConfirmationSelect = `SELECT id,actor,kind,fingerprint,summary_json,created_at,expires_at,authorized_at,consumed_at FROM restore_confirmations`

func scanRestoreConfirmation(row operationScanner) (RestoreConfirmation, error) {
	var record RestoreConfirmation
	var summary, created, expires string
	var authorized, consumed sql.NullString
	if err := row.Scan(&record.ID, &record.Actor, &record.Kind, &record.Fingerprint, &summary, &created, &expires, &authorized, &consumed); err != nil {
		return RestoreConfirmation{}, err
	}
	_ = json.Unmarshal([]byte(summary), &record.Summary)
	record.CreatedAt, _ = parseTime(created)
	record.ExpiresAt, _ = parseTime(expires)
	if authorized.Valid {
		value, _ := parseTime(authorized.String)
		record.AuthorizedAt = &value
	}
	if consumed.Valid {
		value, _ := parseTime(consumed.String)
		record.ConsumedAt = &value
	}
	return record, nil
}

func confirmationUpdateResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrConflict
	}
	return nil
}
