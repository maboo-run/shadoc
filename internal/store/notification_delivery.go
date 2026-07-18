package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type DeliveryStatus string

const (
	DeliverySkippedDisabled DeliveryStatus = "skipped_disabled"
	DeliveryRateLimited     DeliveryStatus = "rate_limited"
	DeliveryRetrying        DeliveryStatus = "retrying"
	DeliveryFinalFailure    DeliveryStatus = "failed_final"
	DeliveryDelivered       DeliveryStatus = "delivered"
)

type NotificationDelivery struct {
	ID             int64          `json:"id"`
	NotificationID string         `json:"notificationId"`
	OccurredAt     time.Time      `json:"occurredAt"`
	Channel        string         `json:"channel"`
	StateKey       string         `json:"stateKey"`
	Transition     string         `json:"transition"`
	Attempt        int            `json:"attempt"`
	MaxAttempts    int            `json:"maxAttempts"`
	Status         DeliveryStatus `json:"status"`
	ErrorSummary   string         `json:"errorSummary,omitempty"`
	DeliveredAt    *time.Time     `json:"deliveredAt,omitempty"`
}

func (s *Store) NotificationChannelDelivered(ctx context.Context, channel, stateKey, transition string) (bool, error) {
	var latest string
	err := s.db.QueryRowContext(ctx, `
		SELECT transition FROM notification_deliveries
		WHERE channel=? AND state_key=? AND status=?
		ORDER BY id DESC LIMIT 1
	`, channel, stateKey, DeliveryDelivered).Scan(&latest)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return latest == transition, err
}

func (s *Store) CountNotificationEventsSince(ctx context.Context, channel string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT notification_id)
		FROM notification_deliveries
		WHERE channel=? AND occurred_at>=? AND attempt>0
	`, channel, formatTime(since.UTC())).Scan(&count)
	return count, err
}

func (s *Store) RecordNotificationDelivery(ctx context.Context, delivery NotificationDelivery) error {
	if err := validateNotificationDelivery(delivery); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification_deliveries(notification_id,occurred_at,channel,state_key,transition,attempt,max_attempts,status,error_summary,delivered_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, delivery.NotificationID, formatTime(delivery.OccurredAt.UTC()), delivery.Channel, delivery.StateKey, delivery.Transition, delivery.Attempt, delivery.MaxAttempts, delivery.Status, delivery.ErrorSummary, nullableTimePointer(delivery.DeliveredAt))
	return err
}

func (s *Store) ListNotificationDeliveries(ctx context.Context, limit int) ([]NotificationDelivery, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,notification_id,occurred_at,channel,state_key,transition,attempt,max_attempts,status,error_summary,delivered_at FROM notification_deliveries ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]NotificationDelivery, 0)
	for rows.Next() {
		var item NotificationDelivery
		var occurredAt string
		var deliveredAt sql.NullString
		if err := rows.Scan(&item.ID, &item.NotificationID, &occurredAt, &item.Channel, &item.StateKey, &item.Transition, &item.Attempt, &item.MaxAttempts, &item.Status, &item.ErrorSummary, &deliveredAt); err != nil {
			return nil, err
		}
		item.OccurredAt, err = parseTime(occurredAt)
		if err != nil {
			return nil, err
		}
		if deliveredAt.Valid {
			value, parseErr := parseTime(deliveredAt.String)
			if parseErr != nil {
				return nil, parseErr
			}
			item.DeliveredAt = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func validateNotificationDelivery(delivery NotificationDelivery) error {
	if delivery.NotificationID == "" || delivery.OccurredAt.IsZero() || delivery.Channel == "" || delivery.StateKey == "" || delivery.Transition == "" {
		return errors.New("notification delivery requires identity, channel, state, transition, and time")
	}
	switch delivery.Status {
	case DeliverySkippedDisabled:
		if delivery.Attempt != 0 || delivery.MaxAttempts != 0 || delivery.DeliveredAt != nil {
			return errors.New("disabled notification delivery cannot contain an attempt")
		}
	case DeliveryRateLimited:
		if delivery.Attempt != 0 || delivery.MaxAttempts != 0 || delivery.DeliveredAt != nil || delivery.ErrorSummary == "" {
			return errors.New("rate-limited notification delivery requires an error without an attempt")
		}
	case DeliveryRetrying, DeliveryFinalFailure:
		if delivery.Attempt < 1 || delivery.MaxAttempts < 1 || delivery.Attempt > delivery.MaxAttempts || delivery.DeliveredAt != nil || delivery.ErrorSummary == "" {
			return errors.New("failed notification delivery requires a bounded attempt and error")
		}
	case DeliveryDelivered:
		if delivery.Attempt < 1 || delivery.MaxAttempts < 1 || delivery.Attempt > delivery.MaxAttempts || delivery.DeliveredAt == nil {
			return errors.New("delivered notification requires a bounded attempt and delivery time")
		}
	default:
		return errors.New("unsupported notification delivery status")
	}
	return nil
}
