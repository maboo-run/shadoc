package schedule

import (
	"errors"
	"fmt"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func Next(schedule domain.Schedule, timezone string, after time.Time) (time.Time, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("load schedule timezone: %w", err)
	}
	after = after.In(location)
	switch schedule.Kind {
	case domain.DailySchedule:
		return nextDaily(schedule, location, after)
	case domain.WeeklySchedule:
		return nextWeekly(schedule, location, after)
	case domain.IntervalSchedule:
		if schedule.IntervalHours < 1 {
			return time.Time{}, errors.New("interval hours must be positive")
		}
		return after.Add(time.Duration(schedule.IntervalHours) * time.Hour), nil
	default:
		return time.Time{}, fmt.Errorf("unsupported schedule kind %q", schedule.Kind)
	}
}

// NextAnchored returns the first logical occurrence strictly after after. Fixed
// intervals are derived from anchor so process restarts cannot move their
// cadence. Calendar schedules use one occurrence per local date.
func NextAnchored(schedule domain.Schedule, timezone string, anchor, after time.Time) (time.Time, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("load schedule timezone: %w", err)
	}
	switch schedule.Kind {
	case domain.DailySchedule:
		return nextDaily(schedule, location, after.In(location))
	case domain.WeeklySchedule:
		return nextWeekly(schedule, location, after.In(location))
	case domain.IntervalSchedule:
		if schedule.IntervalHours < 1 {
			return time.Time{}, errors.New("interval hours must be positive")
		}
		interval := time.Duration(schedule.IntervalHours) * time.Hour
		if anchor.IsZero() {
			anchor = after
		}
		if after.Before(anchor) {
			return anchor.Add(interval), nil
		}
		steps := after.Sub(anchor)/interval + 1
		return anchor.Add(steps * interval), nil
	default:
		return time.Time{}, fmt.Errorf("unsupported schedule kind %q", schedule.Kind)
	}
}

func nextDaily(schedule domain.Schedule, location *time.Location, after time.Time) (time.Time, error) {
	hour, minute, err := parseClock(schedule.TimeOfDay)
	if err != nil {
		return time.Time{}, err
	}
	for offset := 0; offset < 370; offset++ {
		date := time.Date(after.Year(), after.Month(), after.Day()+offset, 12, 0, 0, 0, location)
		candidate, ok := localOccurrence(date.Year(), date.Month(), date.Day(), hour, minute, location)
		if ok && candidate.After(after) {
			return candidate, nil
		}
	}
	return time.Time{}, errors.New("cannot calculate daily schedule occurrence")
}

func nextWeekly(schedule domain.Schedule, location *time.Location, after time.Time) (time.Time, error) {
	hour, minute, err := parseClock(schedule.TimeOfDay)
	if err != nil {
		return time.Time{}, err
	}
	days := (int(schedule.DayOfWeek) - int(after.Weekday()) + 7) % 7
	for weeks := 0; weeks < 54; weeks++ {
		date := time.Date(after.Year(), after.Month(), after.Day()+days+7*weeks, 12, 0, 0, 0, location)
		candidate, ok := localOccurrence(date.Year(), date.Month(), date.Day(), hour, minute, location)
		if ok && candidate.After(after) {
			return candidate, nil
		}
	}
	return time.Time{}, errors.New("cannot calculate weekly schedule occurrence")
}

// localOccurrence returns the earliest copy of a valid wall-clock minute. If
// the requested minute is skipped by a timezone transition, it returns the
// first valid minute later on that local date.
func localOccurrence(year int, month time.Month, day, hour, minute int, location *time.Location) (time.Time, bool) {
	direct := time.Date(year, month, day, hour, minute, 0, 0, location)
	local := direct.In(location)
	if sameLocalMinute(local, year, month, day, hour, minute) {
		_, beforeOffset := direct.Add(-4 * time.Hour).Zone()
		_, afterOffset := direct.Add(4 * time.Hour).Zone()
		if beforeOffset == afterOffset {
			return direct, true
		}
		earliest := direct
		for candidate := direct.Add(-4 * time.Hour); !candidate.After(direct.Add(4 * time.Hour)); candidate = candidate.Add(time.Minute) {
			if sameLocalMinute(candidate.In(location), year, month, day, hour, minute) && candidate.Before(earliest) {
				earliest = candidate
			}
		}
		return earliest, true
	}

	start := time.Date(year, month, day, 0, 0, 0, 0, location).Add(-6 * time.Hour)
	requestedMinute := hour*60 + minute
	for offset := time.Duration(0); offset <= 36*time.Hour; offset += time.Minute {
		candidate := start.Add(offset)
		wall := candidate.In(location)
		if wall.Year() != year || wall.Month() != month || wall.Day() != day {
			continue
		}
		if wall.Hour()*60+wall.Minute() >= requestedMinute {
			return candidate, true
		}
	}
	return time.Time{}, false
}

func sameLocalMinute(value time.Time, year int, month time.Month, day, hour, minute int) bool {
	return value.Year() == year && value.Month() == month && value.Day() == day && value.Hour() == hour && value.Minute() == minute
}

func parseClock(value string) (int, int, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, 0, errors.New("time of day must use HH:MM")
	}
	return parsed.Hour(), parsed.Minute(), nil
}
