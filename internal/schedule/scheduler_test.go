package schedule

import (
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestNextRunForStructuredSchedules(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		schedule domain.Schedule
		after    time.Time
		want     time.Time
	}{
		{
			name:     "daily later today",
			schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:30"},
			after:    time.Date(2026, 7, 11, 1, 0, 0, 0, shanghai),
			want:     time.Date(2026, 7, 11, 2, 30, 0, 0, shanghai),
		},
		{
			name:     "daily skips missed time",
			schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:30"},
			after:    time.Date(2026, 7, 11, 3, 0, 0, 0, shanghai),
			want:     time.Date(2026, 7, 12, 2, 30, 0, 0, shanghai),
		},
		{
			name:     "weekly monday",
			schedule: domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Monday, TimeOfDay: "03:00"},
			after:    time.Date(2026, 7, 11, 12, 0, 0, 0, shanghai),
			want:     time.Date(2026, 7, 13, 3, 0, 0, 0, shanghai),
		},
		{
			name:     "fixed interval",
			schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 6},
			after:    time.Date(2026, 7, 11, 12, 0, 0, 0, shanghai),
			want:     time.Date(2026, 7, 11, 18, 0, 0, 0, shanghai),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Next(tt.schedule, "Asia/Shanghai", tt.after)
			if err != nil {
				t.Fatalf("next run: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("next run = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNextAnchoredIntervalDoesNotDrift(t *testing.T) {
	anchor := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	got, err := NextAnchored(
		domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 6},
		"UTC",
		anchor,
		time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 12, 6, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestNextAnchoredHandlesDSTGapAndFoldOnce(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	daily := domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:30"}
	gap, err := NextAnchored(
		daily,
		zone.String(),
		time.Date(2026, 3, 7, 0, 0, 0, 0, zone),
		time.Date(2026, 3, 8, 0, 0, 0, 0, zone),
	)
	if err != nil {
		t.Fatal(err)
	}
	gapLocal := gap.In(zone)
	if gapLocal.Year() != 2026 || gapLocal.Month() != time.March || gapLocal.Day() != 8 || gapLocal.Hour() != 3 || gapLocal.Minute() != 0 {
		t.Fatalf("gap occurrence = %s, want first valid minute after 02:30", gapLocal)
	}

	foldSchedule := domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "01:30"}
	first, err := NextAnchored(
		foldSchedule,
		zone.String(),
		time.Date(2026, 10, 31, 0, 0, 0, 0, zone),
		time.Date(2026, 11, 1, 0, 0, 0, 0, zone),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NextAnchored(
		foldSchedule,
		zone.String(),
		time.Date(2026, 10, 31, 0, 0, 0, 0, zone),
		first,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.In(zone).Day() != 1 || second.In(zone).Day() != 2 {
		t.Fatalf("fold produced duplicate local-date occurrence: first=%s second=%s", first.In(zone), second.In(zone))
	}
}
