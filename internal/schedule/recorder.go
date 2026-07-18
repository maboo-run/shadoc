package schedule

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

const OnlineTolerance = 90 * time.Second
const MaxOccurrencesPerTick = 1000

type Definition struct {
	OwnerKind     string
	OwnerID       string
	Schedule      domain.Schedule
	Timezone      string
	AnchorAt      time.Time
	CatchUpWindow time.Duration
	TargetIDs     []string
}

type OccurrenceStore interface {
	LatestScheduleOccurrence(context.Context, string, string, time.Time) (store.ScheduleOccurrence, error)
	CreateScheduleOccurrence(context.Context, store.ScheduleOccurrence) (bool, error)
}

type Recorder struct {
	Store OccurrenceStore
}

func (r Recorder) RecordDue(ctx context.Context, definition Definition, now time.Time) ([]store.ScheduleOccurrence, error) {
	if r.Store == nil {
		return nil, errors.New("schedule occurrence store is required")
	}
	if definition.OwnerKind != "plan" && definition.OwnerKind != "maintenance" && definition.OwnerKind != "restore_verification" {
		return nil, errors.New("unsupported schedule owner kind")
	}
	if definition.OwnerID == "" || definition.Timezone == "" || definition.AnchorAt.IsZero() || now.IsZero() {
		return nil, errors.New("schedule definition requires owner, timezone, anchor, and observation time")
	}
	if definition.CatchUpWindow < 0 {
		return nil, errors.New("schedule catch-up window cannot be negative")
	}

	cursor := definition.AnchorAt
	latest, err := r.Store.LatestScheduleOccurrence(ctx, definition.OwnerKind, definition.OwnerID, definition.AnchorAt)
	if err == nil {
		cursor = latest.ScheduledAt
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	observedAt := now.UTC()
	runnable := make([]store.ScheduleOccurrence, 0, 1)
	for range MaxOccurrencesPerTick {
		scheduledAt, err := NextAnchored(definition.Schedule, definition.Timezone, definition.AnchorAt, cursor)
		if err != nil {
			return nil, err
		}
		if scheduledAt.After(now) {
			break
		}
		following, err := NextAnchored(definition.Schedule, definition.Timezone, definition.AnchorAt, scheduledAt)
		if err != nil {
			return nil, err
		}
		window := OnlineTolerance
		if definition.CatchUpWindow > window {
			window = definition.CatchUpWindow
		}
		age := now.Sub(scheduledAt)
		canRun := following.After(now) && age <= window
		mode, status := "missed", "missed"
		if canRun {
			mode, status = "on_time", "pending"
			if age > OnlineTolerance {
				mode = "catch_up"
			}
		}
		occurrence := store.ScheduleOccurrence{
			ID:          scheduleOccurrenceID(definition.OwnerKind, definition.OwnerID, scheduledAt),
			OwnerKind:   definition.OwnerKind,
			OwnerID:     definition.OwnerID,
			ScheduledAt: scheduledAt.UTC(),
			ObservedAt:  observedAt,
			Mode:        mode,
			Status:      status,
			TargetIDs:   append([]string(nil), definition.TargetIDs...),
			RunIDs:      []string{},
		}
		if status == "missed" {
			finishedAt := observedAt
			occurrence.FinishedAt = &finishedAt
		}
		created, err := r.Store.CreateScheduleOccurrence(ctx, occurrence)
		if err != nil {
			return nil, err
		}
		if created && status == "pending" {
			runnable = append(runnable, occurrence)
		}
		cursor = scheduledAt
	}
	return runnable, nil
}

func scheduleOccurrenceID(ownerKind, ownerID string, scheduledAt time.Time) string {
	sum := sha256.Sum256([]byte(ownerKind + "\x00" + ownerID + "\x00" + scheduledAt.UTC().Format(time.RFC3339Nano)))
	return "occ_" + hex.EncodeToString(sum[:16])
}
