package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type ScheduleOccurrence struct {
	ID          string     `json:"id"`
	OwnerKind   string     `json:"ownerKind"`
	OwnerID     string     `json:"ownerId"`
	ScheduledAt time.Time  `json:"scheduledAt"`
	ObservedAt  time.Time  `json:"observedAt"`
	Mode        string     `json:"mode"`
	Status      string     `json:"status"`
	TargetIDs   []string   `json:"targetIds"`
	RunIDs      []string   `json:"runIds"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

type ScheduleOccurrenceStats struct {
	Total       int `json:"total"`
	Success     int `json:"success"`
	Partial     int `json:"partial"`
	Missed      int `json:"missed"`
	Failed      int `json:"failed"`
	Cancelled   int `json:"cancelled"`
	Skipped     int `json:"skipped"`
	Interrupted int `json:"interrupted"`
}

func (s *Store) CreateScheduleOccurrence(ctx context.Context, occurrence ScheduleOccurrence) (bool, error) {
	if err := validateScheduleOccurrence(occurrence); err != nil {
		return false, err
	}
	targetIDs := occurrence.TargetIDs
	if targetIDs == nil {
		targetIDs = []string{}
	}
	runIDs := occurrence.RunIDs
	if runIDs == nil {
		runIDs = []string{}
	}
	encodedTargets, err := json.Marshal(targetIDs)
	if err != nil {
		return false, fmt.Errorf("encode schedule occurrence targets: %w", err)
	}
	encodedRuns, err := json.Marshal(runIDs)
	if err != nil {
		return false, fmt.Errorf("encode schedule occurrence runs: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO schedule_occurrences(
			id,owner_kind,owner_id,scheduled_at,observed_at,mode,status,target_ids_json,run_ids_json,started_at,finished_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?)
	`, occurrence.ID, occurrence.OwnerKind, occurrence.OwnerID, formatTime(occurrence.ScheduledAt.UTC()), formatTime(occurrence.ObservedAt.UTC()), occurrence.Mode, occurrence.Status, string(encodedTargets), string(encodedRuns), nullableTimePointer(occurrence.StartedAt), nullableTimePointer(occurrence.FinishedAt))
	if err != nil {
		return false, fmt.Errorf("create schedule occurrence: %w", err)
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *Store) ClaimScheduleOccurrence(ctx context.Context, id string, at time.Time) (bool, error) {
	if id == "" || at.IsZero() {
		return false, errors.New("schedule occurrence claim requires id and time")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE schedule_occurrences SET status='running',started_at=? WHERE id=? AND status='pending'`, formatTime(at.UTC()), id)
	if err != nil {
		return false, fmt.Errorf("claim schedule occurrence: %w", err)
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *Store) FinishScheduleOccurrence(ctx context.Context, id, status string, runIDs []string, at time.Time) error {
	if id == "" || at.IsZero() || !terminalScheduleStatus(status) {
		return errors.New("invalid terminal schedule occurrence")
	}
	if runIDs == nil {
		runIDs = []string{}
	}
	encodedRuns, err := json.Marshal(runIDs)
	if err != nil {
		return fmt.Errorf("encode schedule occurrence runs: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE schedule_occurrences SET status=?,run_ids_json=?,finished_at=? WHERE id=? AND status='running'`, status, string(encodedRuns), formatTime(at.UTC()), id)
	if err != nil {
		return fmt.Errorf("finish schedule occurrence: %w", err)
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

func (s *Store) LatestScheduleOccurrence(ctx context.Context, ownerKind, ownerID string, anchor time.Time) (ScheduleOccurrence, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id,owner_kind,owner_id,scheduled_at,observed_at,mode,status,target_ids_json,run_ids_json,started_at,finished_at
		FROM schedule_occurrences
		WHERE owner_kind=? AND owner_id=? AND scheduled_at>=?
		ORDER BY scheduled_at DESC LIMIT 1
	`, ownerKind, ownerID, formatTime(anchor.UTC()))
	return scanScheduleOccurrence(row)
}

func (s *Store) LatestScheduleOccurrences(ctx context.Context, ownerKind string) (map[string]ScheduleOccurrence, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,owner_kind,owner_id,scheduled_at,observed_at,mode,status,target_ids_json,run_ids_json,started_at,finished_at
		FROM schedule_occurrences WHERE owner_kind=? ORDER BY owner_id,scheduled_at DESC
	`, ownerKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]ScheduleOccurrence{}
	for rows.Next() {
		occurrence, err := scanScheduleOccurrence(rows)
		if err != nil {
			return nil, err
		}
		if _, exists := result[occurrence.OwnerID]; !exists {
			result[occurrence.OwnerID] = occurrence
		}
	}
	return result, rows.Err()
}

func (s *Store) ListScheduleOccurrences(ctx context.Context, ownerKind, ownerID string, limit int) ([]ScheduleOccurrence, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,owner_kind,owner_id,scheduled_at,observed_at,mode,status,target_ids_json,run_ids_json,started_at,finished_at
		FROM schedule_occurrences WHERE owner_kind=? AND owner_id=? ORDER BY scheduled_at DESC LIMIT ?
	`, ownerKind, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ScheduleOccurrence, 0)
	for rows.Next() {
		occurrence, err := scanScheduleOccurrence(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, occurrence)
	}
	return result, rows.Err()
}

func (s *Store) ScheduleOccurrenceStats(ctx context.Context, ownerKind, ownerID string, anchor time.Time) (ScheduleOccurrenceStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT status,COUNT(*) FROM schedule_occurrences
		WHERE owner_kind=? AND owner_id=? AND scheduled_at>=?
		GROUP BY status
	`, ownerKind, ownerID, formatTime(anchor.UTC()))
	if err != nil {
		return ScheduleOccurrenceStats{}, err
	}
	defer rows.Close()
	var stats ScheduleOccurrenceStats
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return ScheduleOccurrenceStats{}, err
		}
		stats.Total += count
		switch status {
		case "success":
			stats.Success += count
		case "partial":
			stats.Partial += count
		case "missed":
			stats.Missed += count
		case "failed":
			stats.Failed += count
		case "cancelled":
			stats.Cancelled += count
		case "skipped":
			stats.Skipped += count
		case "interrupted":
			stats.Interrupted += count
		}
	}
	return stats, rows.Err()
}

func (s *Store) RecoverInterruptedScheduleOccurrences(ctx context.Context, at time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE schedule_occurrences
		SET status='interrupted',finished_at=?
		WHERE status IN ('pending','running')
	`, formatTime(at.UTC()))
	if err != nil {
		return 0, fmt.Errorf("recover interrupted schedule occurrences: %w", err)
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

type scheduleOccurrenceScanner interface {
	Scan(...any) error
}

func scanScheduleOccurrence(scanner scheduleOccurrenceScanner) (ScheduleOccurrence, error) {
	var occurrence ScheduleOccurrence
	var scheduledAt, observedAt, targetIDs, runIDs string
	var startedAt, finishedAt sql.NullString
	err := scanner.Scan(&occurrence.ID, &occurrence.OwnerKind, &occurrence.OwnerID, &scheduledAt, &observedAt, &occurrence.Mode, &occurrence.Status, &targetIDs, &runIDs, &startedAt, &finishedAt)
	if err != nil {
		return occurrence, err
	}
	occurrence.ScheduledAt, err = parseTime(scheduledAt)
	if err != nil {
		return occurrence, err
	}
	occurrence.ObservedAt, err = parseTime(observedAt)
	if err != nil {
		return occurrence, err
	}
	if err := json.Unmarshal([]byte(targetIDs), &occurrence.TargetIDs); err != nil {
		return occurrence, fmt.Errorf("decode schedule occurrence targets: %w", err)
	}
	if err := json.Unmarshal([]byte(runIDs), &occurrence.RunIDs); err != nil {
		return occurrence, fmt.Errorf("decode schedule occurrence runs: %w", err)
	}
	if occurrence.TargetIDs == nil {
		occurrence.TargetIDs = []string{}
	}
	if occurrence.RunIDs == nil {
		occurrence.RunIDs = []string{}
	}
	if startedAt.Valid {
		value, parseErr := parseTime(startedAt.String)
		if parseErr != nil {
			return occurrence, parseErr
		}
		occurrence.StartedAt = &value
	}
	if finishedAt.Valid {
		value, parseErr := parseTime(finishedAt.String)
		if parseErr != nil {
			return occurrence, parseErr
		}
		occurrence.FinishedAt = &value
	}
	return occurrence, nil
}

func validateScheduleOccurrence(occurrence ScheduleOccurrence) error {
	if occurrence.ID == "" || occurrence.OwnerID == "" || occurrence.ScheduledAt.IsZero() || occurrence.ObservedAt.IsZero() {
		return errors.New("schedule occurrence requires identity and times")
	}
	if occurrence.OwnerKind != "plan" && occurrence.OwnerKind != "maintenance" && occurrence.OwnerKind != "restore_verification" {
		return errors.New("unsupported schedule occurrence owner")
	}
	if occurrence.Mode != "on_time" && occurrence.Mode != "catch_up" && occurrence.Mode != "missed" {
		return errors.New("unsupported schedule occurrence mode")
	}
	switch occurrence.Status {
	case "pending", "success", "partial", "failed", "cancelled", "skipped", "missed", "interrupted":
		return nil
	default:
		return errors.New("unsupported schedule occurrence status")
	}
}

func terminalScheduleStatus(status string) bool {
	switch status {
	case "success", "partial", "failed", "cancelled", "skipped", "missed", "interrupted":
		return true
	default:
		return false
	}
}

func nullableTimePointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(value.UTC())
}
