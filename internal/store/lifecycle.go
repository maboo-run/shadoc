package store

import (
	"context"
	"fmt"
	"time"
)

type LifecyclePolicy struct {
	RunDays        int   `json:"runDays"`
	RawLogDays     int   `json:"rawLogDays"`
	AuditDays      int   `json:"auditDays"`
	RawLogMaxBytes int64 `json:"rawLogMaxBytes"`
}

type LifecycleReport struct {
	LogsCleared            int       `json:"logsCleared"`
	RunsDeleted            int       `json:"runsDeleted"`
	AuditsDeleted          int       `json:"auditsDeleted"`
	CapacitySamplesDeleted int       `json:"capacitySamplesDeleted"`
	RawLogBytesBefore      int64     `json:"rawLogBytesBefore"`
	RawLogBytesAfter       int64     `json:"rawLogBytesAfter"`
	CompletedAt            time.Time `json:"completedAt"`
}

func (s *Store) LoadLifecyclePolicy(ctx context.Context) (LifecyclePolicy, error) {
	var policy LifecyclePolicy
	err := s.db.QueryRowContext(ctx, `SELECT run_days,raw_log_days,audit_days,raw_log_max_bytes FROM lifecycle_policy WHERE id=1`).Scan(
		&policy.RunDays, &policy.RawLogDays, &policy.AuditDays, &policy.RawLogMaxBytes,
	)
	return policy, err
}

func (s *Store) SaveLifecyclePolicy(ctx context.Context, policy LifecyclePolicy, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO lifecycle_policy(id,run_days,raw_log_days,audit_days,raw_log_max_bytes,updated_at)
		VALUES(1,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			run_days=excluded.run_days,
			raw_log_days=excluded.raw_log_days,
			audit_days=excluded.audit_days,
			raw_log_max_bytes=excluded.raw_log_max_bytes,
			updated_at=excluded.updated_at
	`, policy.RunDays, policy.RawLogDays, policy.AuditDays, policy.RawLogMaxBytes, formatTime(now))
	if err != nil {
		return fmt.Errorf("save lifecycle policy: %w", err)
	}
	return nil
}

func (s *Store) CleanupExecutionData(ctx context.Context, policy LifecyclePolicy, now time.Time) (LifecycleReport, error) {
	return s.evaluateLifecycleCleanup(ctx, policy, now, true)
}

func (s *Store) PreviewExecutionDataCleanup(ctx context.Context, policy LifecyclePolicy, now time.Time) (LifecycleReport, error) {
	return s.evaluateLifecycleCleanup(ctx, policy, now, false)
}

func (s *Store) evaluateLifecycleCleanup(ctx context.Context, policy LifecyclePolicy, now time.Time, commit bool) (LifecycleReport, error) {
	report := LifecycleReport{CompletedAt: now.UTC()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("begin lifecycle cleanup: %w", err)
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(raw_log AS BLOB))),0) FROM runs`).Scan(&report.RawLogBytesBefore); err != nil {
		return report, err
	}

	logCutoff := formatTime(now.AddDate(0, 0, -policy.RawLogDays))
	runCutoff := formatTime(now.AddDate(0, 0, -policy.RunDays))
	result, err := tx.ExecContext(ctx, `UPDATE runs SET raw_log='',raw_log_expired=1 WHERE finished_at IS NOT NULL AND finished_at < ? AND finished_at >= ? AND raw_log <> ''`, logCutoff, runCutoff)
	if err != nil {
		return report, fmt.Errorf("clear expired raw logs: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil {
		report.LogsCleared += int(affected)
	}

	result, err = tx.ExecContext(ctx, `DELETE FROM runs WHERE finished_at IS NOT NULL AND finished_at < ?`, runCutoff)
	if err != nil {
		return report, fmt.Errorf("delete expired runs: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil {
		report.RunsDeleted = int(affected)
	}

	result, err = tx.ExecContext(ctx, `DELETE FROM repository_capacity_samples WHERE checked_at < ?`, runCutoff)
	if err != nil {
		return report, fmt.Errorf("delete expired repository capacity samples: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil {
		report.CapacitySamplesDeleted = int(affected)
	}

	auditCutoff := formatTime(now.AddDate(0, 0, -policy.AuditDays))
	result, err = tx.ExecContext(ctx, `DELETE FROM audits WHERE occurred_at < ?`, auditCutoff)
	if err != nil {
		return report, fmt.Errorf("delete expired audits: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil {
		report.AuditsDeleted = int(affected)
	}

	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(raw_log AS BLOB))),0) FROM runs`).Scan(&total); err != nil {
		return report, err
	}
	if total > policy.RawLogMaxBytes {
		rows, err := tx.QueryContext(ctx, `
			SELECT id,length(CAST(raw_log AS BLOB))
			FROM runs
			WHERE finished_at IS NOT NULL AND raw_log <> ''
			ORDER BY finished_at ASC, started_at ASC, id ASC
		`)
		if err != nil {
			return report, err
		}
		type logRow struct {
			id   string
			size int64
		}
		var logs []logRow
		for rows.Next() {
			var row logRow
			if err := rows.Scan(&row.id, &row.size); err != nil {
				_ = rows.Close()
				return report, err
			}
			logs = append(logs, row)
		}
		if err := rows.Close(); err != nil {
			return report, err
		}
		for _, row := range logs {
			if total <= policy.RawLogMaxBytes {
				break
			}
			result, err := tx.ExecContext(ctx, `UPDATE runs SET raw_log='',raw_log_expired=1 WHERE id=? AND finished_at IS NOT NULL AND raw_log <> ''`, row.id)
			if err != nil {
				return report, err
			}
			if affected, _ := result.RowsAffected(); affected == 1 {
				report.LogsCleared++
				total -= row.size
			}
		}
	}
	if total < 0 {
		total = 0
	}
	report.RawLogBytesAfter = total
	if !commit {
		return report, nil
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit lifecycle cleanup: %w", err)
	}
	return report, nil
}
