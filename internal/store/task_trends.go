package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"
)

var trendWindows = []int{7, 30, 90}

type TaskTrendMetricCoverage struct {
	Duration       int `json:"duration"`
	FilesProcessed int `json:"filesProcessed"`
	FilesChanged   int `json:"filesChanged"`
	BytesProcessed int `json:"bytesProcessed"`
	BytesChanged   int `json:"bytesChanged"`
}

type TaskTrendWindow struct {
	WindowDays                  int                     `json:"windowDays"`
	WindowStart                 time.Time               `json:"windowStart"`
	WindowEnd                   time.Time               `json:"windowEnd"`
	EligibleCount               int                     `json:"eligibleCount"`
	CompleteSuccessCount        int                     `json:"completeSuccessCount"`
	PartialCount                int                     `json:"partialCount"`
	FailedCount                 int                     `json:"failedCount"`
	ExcludedCount               int                     `json:"excludedCount"`
	SuccessRate                 *float64                `json:"successRate,omitempty"`
	RetryCount                  int                     `json:"retryCount"`
	AverageDurationMilliseconds *int64                  `json:"averageDurationMilliseconds,omitempty"`
	P95DurationMilliseconds     *int64                  `json:"p95DurationMilliseconds,omitempty"`
	FilesProcessed              *int64                  `json:"filesProcessed,omitempty"`
	FilesChanged                *int64                  `json:"filesChanged,omitempty"`
	BytesProcessed              *int64                  `json:"bytesProcessed,omitempty"`
	BytesChanged                *int64                  `json:"bytesChanged,omitempty"`
	MetricCoverage              TaskTrendMetricCoverage `json:"metricCoverage"`
}

type TaskTrendDay struct {
	Date                        string                  `json:"date"`
	EligibleCount               int                     `json:"eligibleCount"`
	CompleteSuccessCount        int                     `json:"completeSuccessCount"`
	PartialCount                int                     `json:"partialCount"`
	FailedCount                 int                     `json:"failedCount"`
	ExcludedCount               int                     `json:"excludedCount"`
	RetryCount                  int                     `json:"retryCount"`
	AverageDurationMilliseconds *int64                  `json:"averageDurationMilliseconds,omitempty"`
	FilesProcessed              *int64                  `json:"filesProcessed,omitempty"`
	FilesChanged                *int64                  `json:"filesChanged,omitempty"`
	BytesProcessed              *int64                  `json:"bytesProcessed,omitempty"`
	BytesChanged                *int64                  `json:"bytesChanged,omitempty"`
	MetricCoverage              TaskTrendMetricCoverage `json:"metricCoverage"`
}

type TaskTrend struct {
	TaskID                  string            `json:"taskId"`
	TaskName                string            `json:"taskName"`
	Engine                  string            `json:"engine"`
	LatestCompleteSuccessAt *time.Time        `json:"latestCompleteSuccessAt,omitempty"`
	Windows                 []TaskTrendWindow `json:"windows"`
	Daily                   []TaskTrendDay    `json:"daily"`
}

type TaskTrendReport struct {
	GeneratedAt      time.Time   `json:"generatedAt"`
	EligibleStatuses []string    `json:"eligibleStatuses"`
	ExcludedStatuses []string    `json:"excludedStatuses"`
	Tasks            []TaskTrend `json:"tasks"`
}

func (s *Store) TaskTrends(ctx context.Context, at time.Time) (TaskTrendReport, error) {
	now := at.UTC()
	report := TaskTrendReport{
		GeneratedAt: now, EligibleStatuses: []string{"success", "partial", "failed"},
		ExcludedStatuses: []string{"queued", "running", "cancelled", "skipped"}, Tasks: []TaskTrend{},
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return report, err
	}
	indices := make(map[string]int, len(tasks))
	for _, task := range tasks {
		indices[task.ID] = len(report.Tasks)
		report.Tasks = append(report.Tasks, TaskTrend{TaskID: task.ID, TaskName: task.Name, Engine: string(task.EffectiveEngine()), Windows: []TaskTrendWindow{}, Daily: []TaskTrendDay{}})
	}
	if err := s.loadLatestCompleteSuccesses(ctx, indices, report.Tasks); err != nil {
		return report, err
	}
	if err := s.loadTrendWindows(ctx, now, indices, report.Tasks); err != nil {
		return report, err
	}
	if err := s.loadTrendPercentiles(ctx, now, indices, report.Tasks); err != nil {
		return report, err
	}
	if err := s.loadTrendDays(ctx, now, indices, report.Tasks); err != nil {
		return report, err
	}
	return report, nil
}

func (s *Store) loadLatestCompleteSuccesses(ctx context.Context, indices map[string]int, trends []TaskTrend) error {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id,MAX(COALESCE(finished_at,started_at)) FROM runs WHERE status='success' GROUP BY task_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var taskID, value string
		if err := rows.Scan(&taskID, &value); err != nil {
			return err
		}
		index, ok := indices[taskID]
		if !ok {
			continue
		}
		parsed, err := parseTime(value)
		if err != nil {
			return err
		}
		trends[index].LatestCompleteSuccessAt = &parsed
	}
	return rows.Err()
}

func (s *Store) loadTrendWindows(ctx context.Context, now time.Time, indices map[string]int, trends []TaskTrend) error {
	const effectiveDuration = `COALESCE(r.duration_ms,CASE WHEN r.finished_at IS NOT NULL THEN MAX(0,CAST(ROUND((julianday(r.finished_at)-julianday(r.started_at))*86400000) AS INTEGER)) END)`
	query := `WITH windows(window_days,window_start) AS (VALUES (7,?),(30,?),(90,?))
SELECT t.id,w.window_days,
	SUM(CASE WHEN r.status='success' THEN 1 ELSE 0 END),
	SUM(CASE WHEN r.status='partial' THEN 1 ELSE 0 END),
	SUM(CASE WHEN r.status='failed' THEN 1 ELSE 0 END),
	SUM(CASE WHEN r.id IS NOT NULL AND r.status NOT IN ('success','partial','failed') THEN 1 ELSE 0 END),
	SUM(CASE WHEN r.status IN ('success','partial','failed') THEN 1 ELSE 0 END),
	SUM(CASE WHEN r.status IN ('success','partial','failed') THEN MAX(r.attempt_count-1,0) ELSE 0 END),
	ROUND(AVG(CASE WHEN r.status IN ('success','partial','failed') THEN ` + effectiveDuration + ` END)),
	SUM(CASE WHEN r.status IN ('success','partial','failed') THEN r.files_processed END),
	SUM(CASE WHEN r.status IN ('success','partial','failed') THEN r.files_changed END),
	SUM(CASE WHEN r.status IN ('success','partial','failed') THEN r.bytes_processed END),
	SUM(CASE WHEN r.status IN ('success','partial','failed') THEN r.bytes_changed END),
	SUM(CASE WHEN r.status IN ('success','partial','failed') AND ` + effectiveDuration + ` IS NOT NULL THEN 1 ELSE 0 END),
	SUM(CASE WHEN r.status IN ('success','partial','failed') AND r.files_processed IS NOT NULL THEN 1 ELSE 0 END),
	SUM(CASE WHEN r.status IN ('success','partial','failed') AND r.files_changed IS NOT NULL THEN 1 ELSE 0 END),
	SUM(CASE WHEN r.status IN ('success','partial','failed') AND r.bytes_processed IS NOT NULL THEN 1 ELSE 0 END),
	SUM(CASE WHEN r.status IN ('success','partial','failed') AND r.bytes_changed IS NOT NULL THEN 1 ELSE 0 END)
FROM tasks t CROSS JOIN windows w
LEFT JOIN runs r ON r.task_id=t.id AND r.started_at>=w.window_start AND r.started_at<=?
GROUP BY t.id,w.window_days ORDER BY t.id,w.window_days`
	rows, err := s.db.QueryContext(ctx, query, formatTime(now.Add(-7*24*time.Hour)), formatTime(now.Add(-30*24*time.Hour)), formatTime(now.Add(-90*24*time.Hour)), formatTime(now))
	if err != nil {
		return fmt.Errorf("load task trend windows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var taskID string
		var window TaskTrendWindow
		var average, filesProcessed, filesChanged, bytesProcessed, bytesChanged sql.NullInt64
		if err := rows.Scan(&taskID, &window.WindowDays, &window.CompleteSuccessCount, &window.PartialCount, &window.FailedCount, &window.ExcludedCount, &window.EligibleCount, &window.RetryCount, &average, &filesProcessed, &filesChanged, &bytesProcessed, &bytesChanged, &window.MetricCoverage.Duration, &window.MetricCoverage.FilesProcessed, &window.MetricCoverage.FilesChanged, &window.MetricCoverage.BytesProcessed, &window.MetricCoverage.BytesChanged); err != nil {
			return err
		}
		window.WindowStart, window.WindowEnd = now.Add(-time.Duration(window.WindowDays)*24*time.Hour), now
		if window.EligibleCount > 0 {
			rate := math.Round(float64(window.CompleteSuccessCount)*1000/float64(window.EligibleCount)) / 10
			window.SuccessRate = &rate
		}
		window.AverageDurationMilliseconds = nullMetric(average)
		window.FilesProcessed, window.FilesChanged = nullMetric(filesProcessed), nullMetric(filesChanged)
		window.BytesProcessed, window.BytesChanged = nullMetric(bytesProcessed), nullMetric(bytesChanged)
		if index, ok := indices[taskID]; ok {
			trends[index].Windows = append(trends[index].Windows, window)
		}
	}
	return rows.Err()
}

func (s *Store) loadTrendPercentiles(ctx context.Context, now time.Time, indices map[string]int, trends []TaskTrend) error {
	query := `WITH windows(window_days,window_start) AS (VALUES (7,?),(30,?),(90,?)),
eligible AS (
	SELECT r.task_id,w.window_days,COALESCE(r.duration_ms,CASE WHEN r.finished_at IS NOT NULL THEN MAX(0,CAST(ROUND((julianday(r.finished_at)-julianday(r.started_at))*86400000) AS INTEGER)) END) AS duration
	FROM runs r JOIN windows w ON r.started_at>=w.window_start AND r.started_at<=?
	WHERE r.status IN ('success','partial','failed')
),
ranked AS (
	SELECT task_id,window_days,duration,
		ROW_NUMBER() OVER(PARTITION BY task_id,window_days ORDER BY duration) AS position,
		COUNT(*) OVER(PARTITION BY task_id,window_days) AS total
	FROM eligible WHERE duration IS NOT NULL
)
SELECT task_id,window_days,MIN(CASE WHEN position*100>=total*95 THEN duration END)
FROM ranked GROUP BY task_id,window_days`
	rows, err := s.db.QueryContext(ctx, query, formatTime(now.Add(-7*24*time.Hour)), formatTime(now.Add(-30*24*time.Hour)), formatTime(now.Add(-90*24*time.Hour)), formatTime(now))
	if err != nil {
		return fmt.Errorf("load task trend percentiles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var taskID string
		var days int
		var percentile sql.NullInt64
		if err := rows.Scan(&taskID, &days, &percentile); err != nil {
			return err
		}
		index, ok := indices[taskID]
		if !ok {
			continue
		}
		for windowIndex := range trends[index].Windows {
			if trends[index].Windows[windowIndex].WindowDays == days {
				trends[index].Windows[windowIndex].P95DurationMilliseconds = nullMetric(percentile)
				break
			}
		}
	}
	return rows.Err()
}

func (s *Store) loadTrendDays(ctx context.Context, now time.Time, indices map[string]int, trends []TaskTrend) error {
	const effectiveDuration = `COALESCE(duration_ms,CASE WHEN finished_at IS NOT NULL THEN MAX(0,CAST(ROUND((julianday(finished_at)-julianday(started_at))*86400000) AS INTEGER)) END)`
	query := `SELECT task_id,substr(started_at,1,10),
	SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),SUM(CASE WHEN status='partial' THEN 1 ELSE 0 END),SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),
	SUM(CASE WHEN status NOT IN ('success','partial','failed') THEN 1 ELSE 0 END),SUM(CASE WHEN status IN ('success','partial','failed') THEN 1 ELSE 0 END),
	SUM(CASE WHEN status IN ('success','partial','failed') THEN MAX(attempt_count-1,0) ELSE 0 END),
	ROUND(AVG(CASE WHEN status IN ('success','partial','failed') THEN ` + effectiveDuration + ` END)),
	SUM(CASE WHEN status IN ('success','partial','failed') THEN files_processed END),SUM(CASE WHEN status IN ('success','partial','failed') THEN files_changed END),
	SUM(CASE WHEN status IN ('success','partial','failed') THEN bytes_processed END),SUM(CASE WHEN status IN ('success','partial','failed') THEN bytes_changed END),
	SUM(CASE WHEN status IN ('success','partial','failed') AND ` + effectiveDuration + ` IS NOT NULL THEN 1 ELSE 0 END),
	SUM(CASE WHEN status IN ('success','partial','failed') AND files_processed IS NOT NULL THEN 1 ELSE 0 END),
	SUM(CASE WHEN status IN ('success','partial','failed') AND files_changed IS NOT NULL THEN 1 ELSE 0 END),
	SUM(CASE WHEN status IN ('success','partial','failed') AND bytes_processed IS NOT NULL THEN 1 ELSE 0 END),
	SUM(CASE WHEN status IN ('success','partial','failed') AND bytes_changed IS NOT NULL THEN 1 ELSE 0 END)
FROM runs WHERE started_at>=? AND started_at<=? GROUP BY task_id,substr(started_at,1,10) ORDER BY task_id,substr(started_at,1,10)`
	rows, err := s.db.QueryContext(ctx, query, formatTime(now.Add(-90*24*time.Hour)), formatTime(now))
	if err != nil {
		return fmt.Errorf("load daily task trends: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var taskID string
		var day TaskTrendDay
		var average, filesProcessed, filesChanged, bytesProcessed, bytesChanged sql.NullInt64
		if err := rows.Scan(&taskID, &day.Date, &day.CompleteSuccessCount, &day.PartialCount, &day.FailedCount, &day.ExcludedCount, &day.EligibleCount, &day.RetryCount, &average, &filesProcessed, &filesChanged, &bytesProcessed, &bytesChanged, &day.MetricCoverage.Duration, &day.MetricCoverage.FilesProcessed, &day.MetricCoverage.FilesChanged, &day.MetricCoverage.BytesProcessed, &day.MetricCoverage.BytesChanged); err != nil {
			return err
		}
		day.AverageDurationMilliseconds = nullMetric(average)
		day.FilesProcessed, day.FilesChanged = nullMetric(filesProcessed), nullMetric(filesChanged)
		day.BytesProcessed, day.BytesChanged = nullMetric(bytesProcessed), nullMetric(bytesChanged)
		if index, ok := indices[taskID]; ok {
			trends[index].Daily = append(trends[index].Daily, day)
		}
	}
	return rows.Err()
}
