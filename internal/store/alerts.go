package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type AlertSeverity string

const (
	AlertInfo     AlertSeverity = "info"
	AlertWarning  AlertSeverity = "warning"
	AlertCritical AlertSeverity = "critical"
)

type AlertStatus string

const (
	AlertActive   AlertStatus = "active"
	AlertResolved AlertStatus = "resolved"
)

type AlertTransition string

const (
	AlertRaised             AlertTransition = "raised"
	AlertRepeated           AlertTransition = "repeated"
	AlertResolvedTransition AlertTransition = "resolved"
)

type AlertSignal struct {
	StateKey          string        `json:"stateKey"`
	Kind              string        `json:"kind"`
	Severity          AlertSeverity `json:"severity"`
	ObjectType        string        `json:"objectType"`
	ObjectID          string        `json:"objectId"`
	ObjectName        string        `json:"objectName"`
	Reason            string        `json:"reason"`
	Message           string        `json:"message"`
	TargetPage        string        `json:"targetPage"`
	RecoveryCondition string        `json:"recoveryCondition"`
}

type AlertState struct {
	AlertSignal
	Status          AlertStatus `json:"status"`
	FirstAt         time.Time   `json:"firstAt"`
	LastAt          time.Time   `json:"lastAt"`
	ResolvedAt      *time.Time  `json:"resolvedAt,omitempty"`
	OccurrenceCount int         `json:"occurrenceCount"`
}

type AlertEvent struct {
	ID         int64           `json:"id"`
	OccurredAt time.Time       `json:"occurredAt"`
	Transition AlertTransition `json:"transition"`
	AlertSignal
	Status          AlertStatus `json:"status"`
	OccurrenceCount int         `json:"occurrenceCount"`
}

func (s *Store) RaiseAlert(ctx context.Context, signal AlertSignal, at time.Time) (AlertState, AlertTransition, error) {
	if err := validateAlertSignal(signal, at); err != nil {
		return AlertState{}, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AlertState{}, "", err
	}
	defer tx.Rollback()

	state, err := loadAlertState(tx.QueryRowContext(ctx, alertStateSelect+` WHERE state_key=?`, signal.StateKey))
	transition := AlertRepeated
	switch {
	case errors.Is(err, sql.ErrNoRows):
		state = AlertState{AlertSignal: signal, Status: AlertActive, FirstAt: at.UTC(), LastAt: at.UTC(), OccurrenceCount: 1}
		transition = AlertRaised
		if _, err := tx.ExecContext(ctx, `INSERT INTO alert_states(state_key,kind,severity,status,object_type,object_id,object_name,reason,message,target_page,recovery_condition,first_at,last_at,occurrence_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, alertStateArguments(state)...); err != nil {
			return AlertState{}, "", fmt.Errorf("create alert state: %w", err)
		}
	case err != nil:
		return AlertState{}, "", err
	default:
		if state.Status == AlertResolved {
			transition = AlertRaised
		}
		state.AlertSignal = signal
		state.Status = AlertActive
		state.LastAt = at.UTC()
		state.ResolvedAt = nil
		state.OccurrenceCount++
		if _, err := tx.ExecContext(ctx, `UPDATE alert_states SET kind=?,severity=?,status=?,object_type=?,object_id=?,object_name=?,reason=?,message=?,target_page=?,recovery_condition=?,last_at=?,resolved_at=NULL,occurrence_count=? WHERE state_key=?`, state.Kind, state.Severity, state.Status, state.ObjectType, state.ObjectID, state.ObjectName, state.Reason, state.Message, state.TargetPage, state.RecoveryCondition, formatTime(state.LastAt), state.OccurrenceCount, state.StateKey); err != nil {
			return AlertState{}, "", fmt.Errorf("update alert state: %w", err)
		}
	}
	if err := appendAlertEvent(ctx, tx, state, transition, at.UTC()); err != nil {
		return AlertState{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return AlertState{}, "", err
	}
	return state, transition, nil
}

func (s *Store) ResolveAlert(ctx context.Context, stateKey string, at time.Time) (AlertState, bool, error) {
	if strings.TrimSpace(stateKey) == "" || at.IsZero() {
		return AlertState{}, false, errors.New("alert resolution requires state key and time")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AlertState{}, false, err
	}
	defer tx.Rollback()
	state, err := loadAlertState(tx.QueryRowContext(ctx, alertStateSelect+` WHERE state_key=?`, stateKey))
	if errors.Is(err, sql.ErrNoRows) {
		return AlertState{}, false, nil
	}
	if err != nil {
		return AlertState{}, false, err
	}
	if state.Status == AlertResolved {
		return state, false, nil
	}
	resolvedAt := at.UTC()
	state.Status, state.ResolvedAt = AlertResolved, &resolvedAt
	if _, err := tx.ExecContext(ctx, `UPDATE alert_states SET status=?,resolved_at=? WHERE state_key=? AND status=?`, AlertResolved, formatTime(resolvedAt), stateKey, AlertActive); err != nil {
		return AlertState{}, false, fmt.Errorf("resolve alert state: %w", err)
	}
	if err := appendAlertEvent(ctx, tx, state, AlertResolvedTransition, resolvedAt); err != nil {
		return AlertState{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return AlertState{}, false, err
	}
	return state, true, nil
}

func (s *Store) ListActiveAlerts(ctx context.Context) ([]AlertState, error) {
	rows, err := s.db.QueryContext(ctx, alertStateSelect+` WHERE status='active' ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,last_at DESC,state_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make([]AlertState, 0)
	for rows.Next() {
		state, err := loadAlertState(rows)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func (s *Store) ListAlertEvents(ctx context.Context, limit int) ([]AlertEvent, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,occurred_at,state_key,transition,kind,severity,status,object_type,object_id,object_name,reason,message,target_page,recovery_condition,occurrence_count FROM alert_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]AlertEvent, 0)
	for rows.Next() {
		var event AlertEvent
		var occurred string
		if err := rows.Scan(&event.ID, &occurred, &event.StateKey, &event.Transition, &event.Kind, &event.Severity, &event.Status, &event.ObjectType, &event.ObjectID, &event.ObjectName, &event.Reason, &event.Message, &event.TargetPage, &event.RecoveryCondition, &event.OccurrenceCount); err != nil {
			return nil, err
		}
		event.OccurredAt, err = parseTime(occurred)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

const alertStateSelect = `SELECT state_key,kind,severity,status,object_type,object_id,object_name,reason,message,target_page,recovery_condition,first_at,last_at,resolved_at,occurrence_count FROM alert_states`

type alertScanner interface{ Scan(...any) error }

func loadAlertState(scanner alertScanner) (AlertState, error) {
	var state AlertState
	var firstAt, lastAt string
	var resolvedAt sql.NullString
	err := scanner.Scan(&state.StateKey, &state.Kind, &state.Severity, &state.Status, &state.ObjectType, &state.ObjectID, &state.ObjectName, &state.Reason, &state.Message, &state.TargetPage, &state.RecoveryCondition, &firstAt, &lastAt, &resolvedAt, &state.OccurrenceCount)
	if err != nil {
		return state, err
	}
	state.FirstAt, err = parseTime(firstAt)
	if err != nil {
		return state, err
	}
	state.LastAt, err = parseTime(lastAt)
	if err != nil {
		return state, err
	}
	if resolvedAt.Valid {
		value, parseErr := parseTime(resolvedAt.String)
		if parseErr != nil {
			return state, parseErr
		}
		state.ResolvedAt = &value
	}
	return state, nil
}

func alertStateArguments(state AlertState) []any {
	return []any{state.StateKey, state.Kind, state.Severity, state.Status, state.ObjectType, state.ObjectID, state.ObjectName, state.Reason, state.Message, state.TargetPage, state.RecoveryCondition, formatTime(state.FirstAt), formatTime(state.LastAt), state.OccurrenceCount}
}

func appendAlertEvent(ctx context.Context, tx *sql.Tx, state AlertState, transition AlertTransition, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO alert_events(occurred_at,state_key,transition,kind,severity,status,object_type,object_id,object_name,reason,message,target_page,recovery_condition,occurrence_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, formatTime(at), state.StateKey, transition, state.Kind, state.Severity, state.Status, state.ObjectType, state.ObjectID, state.ObjectName, state.Reason, state.Message, state.TargetPage, state.RecoveryCondition, state.OccurrenceCount)
	if err != nil {
		return fmt.Errorf("append alert event: %w", err)
	}
	return nil
}

func validateAlertSignal(signal AlertSignal, at time.Time) error {
	if at.IsZero() || strings.TrimSpace(signal.StateKey) == "" || strings.TrimSpace(signal.Kind) == "" || strings.TrimSpace(signal.ObjectType) == "" || strings.TrimSpace(signal.ObjectID) == "" || strings.TrimSpace(signal.ObjectName) == "" || strings.TrimSpace(signal.Reason) == "" || strings.TrimSpace(signal.Message) == "" || strings.TrimSpace(signal.TargetPage) == "" || strings.TrimSpace(signal.RecoveryCondition) == "" {
		return errors.New("alert signal requires identity, message, handling target, recovery condition, and time")
	}
	switch signal.Severity {
	case AlertInfo, AlertWarning, AlertCritical:
		return nil
	default:
		return errors.New("unsupported alert severity")
	}
}
