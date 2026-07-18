package schedule

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestRecorderMarksOnlineOccurrencePending(t *testing.T) {
	memory := newOccurrenceMemory()
	recorder := Recorder{Store: memory}
	definition := Definition{
		OwnerKind:     "plan",
		OwnerID:       "nightly",
		Schedule:      domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:30"},
		Timezone:      "UTC",
		AnchorAt:      time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC),
		CatchUpWindow: 0,
		TargetIDs:     []string{"task"},
	}
	now := time.Date(2026, 7, 15, 2, 30, 30, 0, time.UTC)
	pending, err := recorder.RecordDue(context.Background(), definition, now)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if pending[0].Mode != "on_time" || pending[0].Status != "pending" || !pending[0].ScheduledAt.Equal(time.Date(2026, 7, 15, 2, 30, 0, 0, time.UTC)) {
		t.Fatalf("occurrence=%+v", pending[0])
	}
}

func TestRecorderCatchesUpOnlyNewestOccurrenceAndMarksOlderOnesMissed(t *testing.T) {
	memory := newOccurrenceMemory()
	recorder := Recorder{Store: memory}
	definition := Definition{
		OwnerKind:     "plan",
		OwnerID:       "nightly",
		Schedule:      domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:30"},
		Timezone:      "UTC",
		AnchorAt:      time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
		CatchUpWindow: time.Hour,
		TargetIDs:     []string{"task-a", "task-b"},
	}
	now := time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC)
	pending, err := recorder.RecordDue(context.Background(), definition, now)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if pending[0].Mode != "catch_up" || !pending[0].ScheduledAt.Equal(time.Date(2026, 7, 13, 2, 30, 0, 0, time.UTC)) {
		t.Fatalf("catch-up=%+v", pending[0])
	}
	items := memory.snapshot()
	if len(items) != 4 {
		t.Fatalf("occurrences=%+v", items)
	}
	missed := 0
	for _, item := range items {
		if item.Status == "missed" {
			missed++
		}
	}
	if missed != 3 {
		t.Fatalf("missed=%d items=%+v", missed, items)
	}

	again, err := recorder.RecordDue(context.Background(), definition, now)
	if err != nil || len(again) != 0 || len(memory.snapshot()) != 4 {
		t.Fatalf("duplicate pending=%+v occurrences=%+v err=%v", again, memory.snapshot(), err)
	}
}

func TestRecorderMarksNewestOccurrenceMissedOutsideCatchUpWindow(t *testing.T) {
	memory := newOccurrenceMemory()
	recorder := Recorder{Store: memory}
	definition := Definition{
		OwnerKind:     "maintenance",
		OwnerID:       "repo",
		Schedule:      domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Monday, TimeOfDay: "03:00"},
		Timezone:      "UTC",
		AnchorAt:      time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC),
		CatchUpWindow: 30 * time.Minute,
		TargetIDs:     []string{"repo"},
	}
	pending, err := recorder.RecordDue(context.Background(), definition, time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC))
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	items := memory.snapshot()
	if len(items) != 1 || items[0].Status != "missed" || items[0].Mode != "missed" {
		t.Fatalf("items=%+v", items)
	}
}

func TestRecorderKeepsIntervalCadenceAcrossInstances(t *testing.T) {
	memory := newOccurrenceMemory()
	definition := Definition{
		OwnerKind:     "plan",
		OwnerID:       "interval",
		Schedule:      domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 6},
		Timezone:      "UTC",
		AnchorAt:      time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
		CatchUpWindow: 2 * time.Hour,
		TargetIDs:     []string{"task"},
	}
	first, err := (Recorder{Store: memory}).RecordDue(context.Background(), definition, time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC))
	if err != nil || len(first) != 1 || !first[0].ScheduledAt.Equal(time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := (Recorder{Store: memory}).RecordDue(context.Background(), definition, time.Date(2026, 7, 12, 6, 0, 0, 0, time.UTC))
	if err != nil || len(second) != 1 || !second[0].ScheduledAt.Equal(time.Date(2026, 7, 12, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestRecorderBoundsLongOfflineHistoryPerTick(t *testing.T) {
	memory := newOccurrenceMemory()
	definition := Definition{
		OwnerKind:     "plan",
		OwnerID:       "hourly",
		Schedule:      domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 1},
		Timezone:      "UTC",
		AnchorAt:      time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		CatchUpWindow: time.Hour,
		TargetIDs:     []string{"task"},
	}
	pending, err := (Recorder{Store: memory}).RecordDue(context.Background(), definition, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if got := len(memory.snapshot()); got != MaxOccurrencesPerTick {
		t.Fatalf("occurrences=%d want=%d", got, MaxOccurrencesPerTick)
	}
}

type occurrenceMemory struct {
	mu    sync.Mutex
	items map[string]store.ScheduleOccurrence
}

func newOccurrenceMemory() *occurrenceMemory {
	return &occurrenceMemory{items: map[string]store.ScheduleOccurrence{}}
}

func (m *occurrenceMemory) LatestScheduleOccurrence(_ context.Context, ownerKind, ownerID string, anchor time.Time) (store.ScheduleOccurrence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest store.ScheduleOccurrence
	for _, item := range m.items {
		if item.OwnerKind != ownerKind || item.OwnerID != ownerID || item.ScheduledAt.Before(anchor) {
			continue
		}
		if latest.ID == "" || item.ScheduledAt.After(latest.ScheduledAt) {
			latest = item
		}
	}
	if latest.ID == "" {
		return store.ScheduleOccurrence{}, sql.ErrNoRows
	}
	return latest, nil
}

func (m *occurrenceMemory) CreateScheduleOccurrence(_ context.Context, item store.ScheduleOccurrence) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := item.OwnerKind + "\x00" + item.OwnerID + "\x00" + item.ScheduledAt.UTC().Format(time.RFC3339Nano)
	for _, existing := range m.items {
		existingKey := existing.OwnerKind + "\x00" + existing.OwnerID + "\x00" + existing.ScheduledAt.UTC().Format(time.RFC3339Nano)
		if key == existingKey {
			return false, nil
		}
	}
	m.items[item.ID] = item
	return true, nil
}

func (m *occurrenceMemory) snapshot() []store.ScheduleOccurrence {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]store.ScheduleOccurrence, 0, len(m.items))
	for _, item := range m.items {
		items = append(items, item)
	}
	return items
}
