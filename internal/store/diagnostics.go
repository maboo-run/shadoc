package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DiagnosticSnapshot is deliberately limited to aggregate counts, fixed
// status fields, and timestamps. The queries that build it never select raw
// logs, errors, operation details, resource names, paths, hosts, usernames,
// URLs, topics, or secret identifiers.
type DiagnosticSnapshot struct {
	Resources         DiagnosticResourceCounts
	RecentFailures    []DiagnosticFailure
	FailuresTruncated bool
	ActiveAlerts      []DiagnosticAlert
	AlertsTruncated   bool
	Notifications     DiagnosticNotificationState
	Capacity          DiagnosticCapacityState
}

type DiagnosticResourceCounts struct {
	RemoteHosts         int
	Repositories        DiagnosticRepositoryCounts
	DatabaseConnections DiagnosticDatabaseCounts
	Tasks               DiagnosticTaskCounts
	Plans               DiagnosticPlanCounts
	Agents              DiagnosticAgentCounts
}

type DiagnosticRepositoryCounts struct {
	Total, Ready, Uninitialized, Disconnected, Abnormal, Local, SFTP, S3 int
}

type DiagnosticDatabaseCounts struct {
	Total, Ready, Draft, MySQL, PostgreSQL, Backup, Restore int
}

type DiagnosticTaskCounts struct {
	Total, Enabled, Restic, Rsync, Directory, Database int
}

type DiagnosticPlanCounts struct {
	Total, Enabled int
}

type DiagnosticAgentCounts struct {
	Total, Online, Offline, Revoked, Stopped, Uninstalled int
}

type DiagnosticFailure struct {
	RecordType   string
	Kind         string
	Status       string
	OccurredAt   time.Time
	AttemptCount int
}

type DiagnosticAlert struct {
	Kind            string
	Severity        string
	ObjectType      string
	FirstAt         time.Time
	LastAt          time.Time
	OccurrenceCount int
}

type DiagnosticNotificationState struct {
	Configured         bool
	Enabled            bool
	ConfiguredChannels int
	EnabledChannels    int
	Delivered          int
	Retrying           int
	FailedFinal        int
	RateLimited        int
	SkippedDisabled    int
	LastDeliveryAt     *time.Time
}

type DiagnosticCapacityState struct {
	Repositories         int
	MonitoringEnabled    int
	ReadyForMonitoring   int
	WithSuccessfulSample int
	Stale                int
	ProbeFailures        int
	BelowThreshold       int
	LowAlerts            int
	ForecastAlerts       int
}

func (s *Store) DiagnosticSnapshot(ctx context.Context, now time.Time, failureLimit, alertLimit int) (DiagnosticSnapshot, error) {
	var snapshot DiagnosticSnapshot
	if now.IsZero() {
		return snapshot, fmt.Errorf("diagnostic snapshot time is required")
	}
	failureLimit = boundedDiagnosticLimit(failureLimit)
	alertLimit = boundedDiagnosticLimit(alertLimit)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, err
	}
	defer tx.Rollback()
	if err := loadDiagnosticResourceCounts(ctx, tx, &snapshot.Resources); err != nil {
		return snapshot, err
	}
	if snapshot.RecentFailures, snapshot.FailuresTruncated, err = loadDiagnosticFailures(ctx, tx, failureLimit); err != nil {
		return snapshot, err
	}
	if snapshot.ActiveAlerts, snapshot.AlertsTruncated, err = loadDiagnosticAlerts(ctx, tx, alertLimit); err != nil {
		return snapshot, err
	}
	if err := loadDiagnosticNotificationState(ctx, tx, &snapshot.Notifications); err != nil {
		return snapshot, err
	}
	if err := loadDiagnosticCapacityState(ctx, tx, now.UTC(), &snapshot.Capacity); err != nil {
		return snapshot, err
	}
	if err := tx.Commit(); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func boundedDiagnosticLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func loadDiagnosticResourceCounts(ctx context.Context, tx *sql.Tx, counts *DiagnosticResourceCounts) error {
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_hosts`).Scan(&counts.RemoteHosts); err != nil {
		return fmt.Errorf("count diagnostic remote hosts: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(status='ready'),0),COALESCE(SUM(status='uninitialized'),0),
		       COALESCE(SUM(status='disconnected'),0),COALESCE(SUM(status='abnormal' OR status LIKE 'unprotected-partial:%'),0),
		       COALESCE(SUM(kind='local'),0),COALESCE(SUM(kind='sftp'),0),COALESCE(SUM(kind='s3'),0)
		FROM repositories
	`).Scan(&counts.Repositories.Total, &counts.Repositories.Ready, &counts.Repositories.Uninitialized, &counts.Repositories.Disconnected,
		&counts.Repositories.Abnormal, &counts.Repositories.Local, &counts.Repositories.SFTP, &counts.Repositories.S3); err != nil {
		return fmt.Errorf("count diagnostic repositories: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),COALESCE(SUM(status='ready'),0),COALESCE(SUM(status='draft'),0),
		       COALESCE(SUM(engine='mysql'),0),COALESCE(SUM(engine='postgresql'),0),
		       COALESCE(SUM(purpose='backup'),0),COALESCE(SUM(purpose='restore'),0)
		FROM database_connections
	`).Scan(&counts.DatabaseConnections.Total, &counts.DatabaseConnections.Ready, &counts.DatabaseConnections.Draft,
		&counts.DatabaseConnections.MySQL, &counts.DatabaseConnections.PostgreSQL, &counts.DatabaseConnections.Backup, &counts.DatabaseConnections.Restore); err != nil {
		return fmt.Errorf("count diagnostic database connections: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),COALESCE(SUM(enabled<>0),0),COALESCE(SUM(engine='restic'),0),COALESCE(SUM(engine='rsync'),0),
		       COALESCE(SUM(kind='directory'),0),COALESCE(SUM(kind='database'),0)
		FROM tasks
	`).Scan(&counts.Tasks.Total, &counts.Tasks.Enabled, &counts.Tasks.Restic, &counts.Tasks.Rsync, &counts.Tasks.Directory, &counts.Tasks.Database); err != nil {
		return fmt.Errorf("count diagnostic tasks: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(enabled<>0),0) FROM plans`).Scan(&counts.Plans.Total, &counts.Plans.Enabled); err != nil {
		return fmt.Errorf("count diagnostic plans: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),COALESCE(SUM(status='online'),0),COALESCE(SUM(status='offline'),0),
		       COALESCE(SUM(revoked_at IS NOT NULL),0),COALESCE(SUM(stopped_at IS NOT NULL),0),COALESCE(SUM(uninstalled_at IS NOT NULL),0)
		FROM agents
	`).Scan(&counts.Agents.Total, &counts.Agents.Online, &counts.Agents.Offline, &counts.Agents.Revoked, &counts.Agents.Stopped, &counts.Agents.Uninstalled); err != nil {
		return fmt.Errorf("count diagnostic agents: %w", err)
	}
	return nil
}

func loadDiagnosticFailures(ctx context.Context, tx *sql.Tx, limit int) ([]DiagnosticFailure, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT record_type,kind,status,occurred_at,attempt_count FROM (
			SELECT 'run' AS record_type,COALESCE(NULLIF(t.engine,''),'restic') AS kind,r.status AS status,
			       r.started_at AS occurred_at,r.attempt_count AS attempt_count
			FROM runs r LEFT JOIN tasks t ON t.id=r.task_id WHERE r.status IN ('failed','partial')
			UNION ALL
			SELECT 'operation',o.kind,o.status,o.created_at,o.attempt_count
			FROM operations o WHERE o.status IN ('failed','cleanup_required')
		) failures ORDER BY occurred_at DESC,record_type,kind LIMIT ?
	`, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("query diagnostic failures: %w", err)
	}
	defer rows.Close()
	items := make([]DiagnosticFailure, 0, limit+1)
	for rows.Next() {
		var item DiagnosticFailure
		var occurredAt string
		if err := rows.Scan(&item.RecordType, &item.Kind, &item.Status, &occurredAt, &item.AttemptCount); err != nil {
			return nil, false, err
		}
		item.OccurredAt, err = parseTime(occurredAt)
		if err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	return items, truncated, nil
}

func loadDiagnosticAlerts(ctx context.Context, tx *sql.Tx, limit int) ([]DiagnosticAlert, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT kind,severity,object_type,first_at,last_at,occurrence_count
		FROM alert_states WHERE status='active'
		ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,last_at DESC
		LIMIT ?
	`, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("query diagnostic alerts: %w", err)
	}
	defer rows.Close()
	items := make([]DiagnosticAlert, 0, limit+1)
	for rows.Next() {
		var item DiagnosticAlert
		var firstAt, lastAt string
		if err := rows.Scan(&item.Kind, &item.Severity, &item.ObjectType, &firstAt, &lastAt, &item.OccurrenceCount); err != nil {
			return nil, false, err
		}
		item.FirstAt, err = parseTime(firstAt)
		if err != nil {
			return nil, false, err
		}
		item.LastAt, err = parseTime(lastAt)
		if err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	return items, truncated, nil
}

func loadDiagnosticNotificationState(ctx context.Context, tx *sql.Tx, state *DiagnosticNotificationState) error {
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),COALESCE(SUM(
			CASE WHEN json_valid(value) THEN COALESCE(CAST(json_extract(value,'$.enabled') AS INTEGER),1) ELSE 0 END
		),0)
		FROM metadata WHERE key IN ('ntfy.config','webhook.config')
	`).Scan(&state.ConfiguredChannels, &state.EnabledChannels)
	if err != nil {
		return fmt.Errorf("read diagnostic notification state: %w", err)
	}
	state.Configured = state.ConfiguredChannels > 0
	state.Enabled = state.EnabledChannels > 0
	var lastDelivery sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(status='delivered'),0),COALESCE(SUM(status='retrying'),0),
		       COALESCE(SUM(status='failed_final'),0),COALESCE(SUM(status='rate_limited'),0),
		       COALESCE(SUM(status='skipped_disabled'),0),MAX(occurred_at)
		FROM notification_deliveries
	`).Scan(&state.Delivered, &state.Retrying, &state.FailedFinal, &state.RateLimited, &state.SkippedDisabled, &lastDelivery); err != nil {
		return fmt.Errorf("count diagnostic notification deliveries: %w", err)
	}
	if lastDelivery.Valid {
		parsed, err := parseTime(lastDelivery.String)
		if err != nil {
			return err
		}
		state.LastDeliveryAt = &parsed
	}
	return nil
}

func loadDiagnosticCapacityState(ctx context.Context, tx *sql.Tx, now time.Time, state *DiagnosticCapacityState) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT r.status,p.enabled,p.probe_interval_minutes,p.minimum_available_bytes,p.minimum_available_percent,
		       p.updated_at,p.last_success_at,CASE WHEN p.last_error<>'' THEN 1 ELSE 0 END,
		       c.total_bytes,c.available_bytes
		FROM repositories r
		JOIN repository_capacity_policies p ON p.repository_id=r.id
		LEFT JOIN repository_capacities c ON c.repository_id=r.id
	`)
	if err != nil {
		return fmt.Errorf("query diagnostic capacity state: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var repositoryStatus, updatedAt string
		var enabled, interval, hasError int
		var minimumBytes int64
		var minimumPercent float64
		var lastSuccess sql.NullString
		var totalBytes, availableBytes sql.NullInt64
		if err := rows.Scan(&repositoryStatus, &enabled, &interval, &minimumBytes, &minimumPercent, &updatedAt, &lastSuccess, &hasError, &totalBytes, &availableBytes); err != nil {
			return err
		}
		state.Repositories++
		if enabled == 0 {
			continue
		}
		state.MonitoringEnabled++
		if repositoryStatus == "uninitialized" || repositoryStatus == "disconnected" {
			continue
		}
		state.ReadyForMonitoring++
		if hasError != 0 {
			state.ProbeFailures++
		}
		baseline, err := parseTime(updatedAt)
		if err != nil {
			return err
		}
		if lastSuccess.Valid {
			baseline, err = parseTime(lastSuccess.String)
			if err != nil {
				return err
			}
		}
		if interval > 0 && !now.Before(baseline.Add(2*time.Duration(interval)*time.Minute)) {
			state.Stale++
		}
		if !totalBytes.Valid || !availableBytes.Valid || totalBytes.Int64 <= 0 || availableBytes.Int64 < 0 || availableBytes.Int64 > totalBytes.Int64 {
			continue
		}
		state.WithSuccessfulSample++
		belowBytes := minimumBytes > 0 && availableBytes.Int64 < minimumBytes
		availablePercent := float64(availableBytes.Int64) * 100 / float64(totalBytes.Int64)
		belowPercent := minimumPercent > 0 && availablePercent < minimumPercent
		if belowBytes || belowPercent {
			state.BelowThreshold++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(kind='repository_capacity_low'),0),COALESCE(SUM(kind='repository_capacity_forecast'),0)
		FROM alert_states WHERE status='active'
	`).Scan(&state.LowAlerts, &state.ForecastAlerts); err != nil {
		return fmt.Errorf("count diagnostic capacity alerts: %w", err)
	}
	return nil
}
