package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func ensureRepositoryRetentionPolicy(ctx context.Context, tx *sql.Tx, repositoryID string, initial domain.RetentionPolicy, updatedAt time.Time) error {
	if repositoryID == "" {
		return nil
	}
	retention, err := json.Marshal(initial)
	if err != nil {
		return err
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO repository_maintenance(
			repository_id,schedule_json,timezone,retention_json,enabled,catch_up_window_minutes,schedule_anchor_at,updated_at
		) VALUES(?, '{"kind":"weekly","dayOfWeek":0,"timeOfDay":"03:00"}', 'UTC', ?, 0, 60, ?, ?)
	`, repositoryID, string(retention), formatTime(updatedAt), formatTime(updatedAt))
	return err
}

func (s *Store) SaveMaintenancePolicy(ctx context.Context, p domain.MaintenancePolicy) error {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories WHERE id=?`, p.RepositoryID).Scan(&exists); err != nil || exists == 0 {
		return ErrConflict
	}
	schedule, _ := json.Marshal(p.Schedule)
	retention, _ := json.Marshal(p.Retention)
	anchor := p.ScheduleAnchorAt
	var previousSchedule, previousTimezone, previousAnchor string
	var previousEnabled int
	err := s.db.QueryRowContext(ctx, `SELECT schedule_json,timezone,enabled,schedule_anchor_at FROM repository_maintenance WHERE repository_id=?`, p.RepositoryID).Scan(&previousSchedule, &previousTimezone, &previousEnabled, &previousAnchor)
	if err == sql.ErrNoRows {
		anchor = p.UpdatedAt
	} else if err != nil {
		return err
	} else {
		anchor, err = parseTime(previousAnchor)
		if err != nil {
			return err
		}
		if previousSchedule != string(schedule) || previousTimezone != p.Timezone || (previousEnabled == 0 && p.Enabled) {
			anchor = p.UpdatedAt
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO repository_maintenance(repository_id,schedule_json,timezone,retention_json,enabled,catch_up_window_minutes,schedule_anchor_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(repository_id) DO UPDATE SET schedule_json=excluded.schedule_json,timezone=excluded.timezone,retention_json=excluded.retention_json,enabled=excluded.enabled,catch_up_window_minutes=excluded.catch_up_window_minutes,schedule_anchor_at=excluded.schedule_anchor_at,updated_at=excluded.updated_at`, p.RepositoryID, string(schedule), p.Timezone, string(retention), boolInt(p.Enabled), p.CatchUpWindowMinutes, formatTime(anchor), formatTime(p.UpdatedAt))
	return err
}
func (s *Store) ListMaintenancePolicies(ctx context.Context) ([]domain.MaintenancePolicy, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.repository_id,m.schedule_json,m.timezone,m.retention_json,m.enabled,m.catch_up_window_minutes,m.schedule_anchor_at,m.updated_at
		FROM repository_maintenance m
		ORDER BY m.repository_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MaintenancePolicy
	for rows.Next() {
		var p domain.MaintenancePolicy
		var schedule, retention, scheduleAnchor, updated string
		var enabled int
		if err := rows.Scan(&p.RepositoryID, &schedule, &p.Timezone, &retention, &enabled, &p.CatchUpWindowMinutes, &scheduleAnchor, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(schedule), &p.Schedule)
		_ = json.Unmarshal([]byte(retention), &p.Retention)
		p.RetentionSource = domain.RepositoryRetentionSource
		p.PolicyFingerprint = p.Retention.Fingerprint()
		p.Enabled = enabled != 0
		p.ScheduleAnchorAt, _ = parseTime(scheduleAnchor)
		p.UpdatedAt, _ = parseTime(updated)
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) MaintenancePolicy(ctx context.Context, id string) (domain.MaintenancePolicy, error) {
	items, err := s.ListMaintenancePolicies(ctx)
	if err != nil {
		return domain.MaintenancePolicy{}, err
	}
	for _, item := range items {
		if item.RepositoryID == id {
			return item, nil
		}
	}
	return domain.MaintenancePolicy{}, sql.ErrNoRows
}

func (s *Store) EffectiveMaintenanceRetention(ctx context.Context, repositoryID string, fallback domain.RetentionPolicy) (domain.RetentionPolicy, bool, error) {
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT retention_json FROM repository_maintenance WHERE repository_id=?`, repositoryID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, false, nil
	}
	if err != nil {
		return domain.RetentionPolicy{}, false, err
	}
	var policy domain.RetentionPolicy
	if err := json.Unmarshal([]byte(encoded), &policy); err != nil {
		return domain.RetentionPolicy{}, false, err
	}
	return policy, true, nil
}

func (s *Store) EffectiveRepositoryResources(ctx context.Context, repositoryID string) (domain.ResourcePolicy, bool, error) {
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT resources_json FROM tasks WHERE repository_id=? AND engine='restic'`, repositoryID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ResourcePolicy{}, false, nil
	}
	if err != nil {
		return domain.ResourcePolicy{}, false, err
	}
	var policy domain.ResourcePolicy
	if err := json.Unmarshal([]byte(encoded), &policy); err != nil {
		return domain.ResourcePolicy{}, false, err
	}
	return policy, true, nil
}
