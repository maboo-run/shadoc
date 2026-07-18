package store

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestTaskTrendsUseExplicitWindowDenominatorAndMetricCoverage(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 15, 0, 0, 0, time.UTC)
	fixtures := []struct {
		id       string
		age      time.Duration
		status   string
		attempts int
		duration time.Duration
		bytes    any
	}{
		{id: "recent-success", age: 2 * 24 * time.Hour, status: "success", attempts: 1, duration: 2 * time.Second, bytes: int64(100)},
		{id: "recent-partial", age: 3 * 24 * time.Hour, status: "partial", attempts: 2, duration: 4 * time.Second, bytes: int64(200)},
		{id: "recent-failed", age: 5 * 24 * time.Hour, status: "failed", attempts: 3, duration: 6 * time.Second, bytes: int64(300)},
		{id: "recent-cancelled", age: 6 * 24 * time.Hour, status: "cancelled", attempts: 1, duration: time.Second},
		{id: "month-success", age: 10 * 24 * time.Hour, status: "success", attempts: 1, duration: 8 * time.Second, bytes: int64(400)},
		{id: "quarter-success", age: 40 * 24 * time.Hour, status: "success", attempts: 1, duration: 10 * time.Second, bytes: int64(500)},
		{id: "outside", age: 100 * 24 * time.Hour, status: "success", attempts: 1, duration: 12 * time.Second, bytes: int64(600)},
	}
	for _, fixture := range fixtures {
		started := now.Add(-fixture.age)
		if err := s.StartRun(ctx, RunRecord{ID: fixture.id, TaskID: task.ID, Trigger: "schedule", Status: "running", StartedAt: started}); err != nil {
			t.Fatal(err)
		}
		summary := map[string]any{}
		if fixture.bytes != nil {
			summary = map[string]any{"bytesProcessed": fixture.bytes, "bytesChanged": fixture.bytes, "filesProcessed": int64(10), "filesChanged": int64(2)}
		}
		if err := s.FinishRun(ctx, fixture.id, fixture.status, started.Add(fixture.duration), fixture.attempts, "", summary, ""); err != nil {
			t.Fatal(err)
		}
	}

	report, err := s.TaskTrends(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if !report.GeneratedAt.Equal(now) || len(report.Tasks) != 1 || len(report.Tasks[0].Windows) != 3 {
		t.Fatalf("report=%+v", report)
	}
	trend := report.Tasks[0]
	if trend.TaskID != task.ID || trend.TaskName != task.Name || trend.LatestCompleteSuccessAt == nil || !trend.LatestCompleteSuccessAt.Equal(now.Add(-2*24*time.Hour).Add(2*time.Second)) {
		t.Fatalf("trend=%+v", trend)
	}
	week := trendWindow(t, trend, 7)
	if week.EligibleCount != 3 || week.CompleteSuccessCount != 1 || week.PartialCount != 1 || week.FailedCount != 1 || week.ExcludedCount != 1 || week.SuccessRate == nil || math.Abs(*week.SuccessRate-33.3) > 0.01 {
		t.Fatalf("week denominator=%+v", week)
	}
	if week.RetryCount != 3 || metricValue(week.AverageDurationMilliseconds) != 4000 || metricValue(week.P95DurationMilliseconds) != 6000 {
		t.Fatalf("week execution metrics=%+v", week)
	}
	if week.MetricCoverage.BytesProcessed != 3 || metricValue(week.BytesProcessed) != 600 || metricValue(week.BytesChanged) != 600 || metricValue(week.FilesChanged) != 6 {
		t.Fatalf("week data metrics=%+v", week)
	}
	month := trendWindow(t, trend, 30)
	if month.EligibleCount != 4 || month.CompleteSuccessCount != 2 || month.SuccessRate == nil || *month.SuccessRate != 50 || metricValue(month.BytesChanged) != 1000 {
		t.Fatalf("month=%+v", month)
	}
	quarter := trendWindow(t, trend, 90)
	if quarter.EligibleCount != 5 || quarter.CompleteSuccessCount != 3 || quarter.SuccessRate == nil || *quarter.SuccessRate != 60 || metricValue(quarter.BytesChanged) != 1500 {
		t.Fatalf("quarter=%+v", quarter)
	}
	if len(trend.Daily) != 6 {
		t.Fatalf("daily=%+v", trend.Daily)
	}
	if len(report.EligibleStatuses) != 3 || len(report.ExcludedStatuses) != 4 {
		t.Fatalf("status contract=%+v", report)
	}
}

func trendWindow(t *testing.T, trend TaskTrend, days int) TaskTrendWindow {
	t.Helper()
	for _, window := range trend.Windows {
		if window.WindowDays == days {
			return window
		}
	}
	t.Fatalf("window %d missing: %+v", days, trend.Windows)
	return TaskTrendWindow{}
}
