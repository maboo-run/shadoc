package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/maboo-run/shadoc/internal/domain"
)

type RepositoryCapacityClaim struct {
	RepositoryID string
	Token        string
}

func (s *Store) ensureRepositoryCapacityPersistence(ctx context.Context) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin repository capacity migration: %w", err)
	}
	defer tx.Rollback()
	defaults := domain.DefaultRepositoryCapacityPolicy("migration", now)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO repository_capacity_policies(
			repository_id,enabled,probe_interval_minutes,minimum_available_bytes,minimum_available_percent,
			exhaustion_warning_days,next_probe_at,last_attempt_at,last_success_at,last_error,updated_at
		)
		SELECT r.id,CASE WHEN r.kind='s3' THEN 0 ELSE ? END,?,?,?,?, CASE WHEN r.kind='s3' THEN NULL ELSE ? END,c.checked_at,c.checked_at,'',?
		FROM repositories r
		LEFT JOIN repository_capacities c ON c.repository_id=r.id
		WHERE NOT EXISTS (SELECT 1 FROM repository_capacity_policies p WHERE p.repository_id=r.id)
	`, boolInt(defaults.Enabled), defaults.ProbeIntervalMinutes, int64(defaults.MinimumAvailableBytes), defaults.MinimumAvailablePercent,
		defaults.ExhaustionWarningDays, formatTime(now), formatTime(now)); err != nil {
		return fmt.Errorf("backfill repository capacity policies: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE repository_capacity_policies
		SET enabled=CASE WHEN (SELECT kind FROM repositories WHERE id=repository_id)='s3' THEN 0 ELSE 1 END,
			probe_interval_minutes=?,minimum_available_bytes=0,minimum_available_percent=0,exhaustion_warning_days=0
	`, defaults.ProbeIntervalMinutes); err != nil {
		return fmt.Errorf("normalize fixed repository capacity refresh: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO repository_capacity_samples(repository_id,total_bytes,available_bytes,checked_at,source_agent_id)
		SELECT c.repository_id,c.total_bytes,c.available_bytes,c.checked_at,c.source_agent_id
		FROM repository_capacities c
		WHERE NOT EXISTS (
			SELECT 1 FROM repository_capacity_samples s
			WHERE s.repository_id=c.repository_id AND s.total_bytes=c.total_bytes
			  AND s.available_bytes=c.available_bytes AND s.checked_at=c.checked_at
			  AND COALESCE(s.source_agent_id,'')=COALESCE(c.source_agent_id,'')
		)
	`); err != nil {
		return fmt.Errorf("backfill repository capacity samples: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit repository capacity migration: %w", err)
	}
	return nil
}

func insertDefaultRepositoryCapacityPolicy(ctx context.Context, tx *sql.Tx, repositoryID string, now time.Time, enabled bool) error {
	policy := domain.DefaultRepositoryCapacityPolicy(repositoryID, now)
	policy.Enabled = enabled
	if !enabled {
		policy.NextProbeAt = nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO repository_capacity_policies(
			repository_id,enabled,probe_interval_minutes,minimum_available_bytes,minimum_available_percent,
			exhaustion_warning_days,next_probe_at,last_error,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?)
	`, policy.RepositoryID, boolInt(policy.Enabled), policy.ProbeIntervalMinutes, int64(policy.MinimumAvailableBytes), policy.MinimumAvailablePercent,
		policy.ExhaustionWarningDays, nullableTimePointer(policy.NextProbeAt), "", formatTime(policy.UpdatedAt))
	return constraintError(err)
}

func (s *Store) RepositoryCapacityPolicy(ctx context.Context, repositoryID string) (domain.RepositoryCapacityPolicy, error) {
	return scanRepositoryCapacityPolicy(s.db.QueryRowContext(ctx, `
		SELECT repository_id,enabled,probe_interval_minutes,minimum_available_bytes,minimum_available_percent,
		       exhaustion_warning_days,next_probe_at,last_attempt_at,last_success_at,last_error,updated_at
		FROM repository_capacity_policies WHERE repository_id=?
	`, repositoryID))
}

func (s *Store) ListRepositoryCapacityPolicies(ctx context.Context) ([]domain.RepositoryCapacityPolicy, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT repository_id,enabled,probe_interval_minutes,minimum_available_bytes,minimum_available_percent,
		       exhaustion_warning_days,next_probe_at,last_attempt_at,last_success_at,last_error,updated_at
		FROM repository_capacity_policies ORDER BY repository_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.RepositoryCapacityPolicy, 0)
	for rows.Next() {
		policy, err := scanRepositoryCapacityPolicy(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, policy)
	}
	return items, rows.Err()
}

func (s *Store) SaveRepositoryCapacityPolicy(ctx context.Context, policy domain.RepositoryCapacityPolicy, now time.Time) error {
	policy.RepositoryID = strings.TrimSpace(policy.RepositoryID)
	if err := policy.Validate(); err != nil {
		return err
	}
	if now.IsZero() {
		return errors.New("repository capacity policy update time is required")
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var wasEnabled int
	var repositoryKind string
	var lastAttemptAt sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT p.enabled,p.last_attempt_at,r.kind FROM repository_capacity_policies p JOIN repositories r ON r.id=p.repository_id WHERE p.repository_id=?`, policy.RepositoryID).Scan(&wasEnabled, &lastAttemptAt, &repositoryKind); err != nil {
		return err
	}
	if domain.RepositoryKind(repositoryKind) == domain.S3Repository && policy.Enabled {
		return errors.New("S3 repository capacity policy cannot be enabled")
	}
	var nextProbe any
	if policy.Enabled {
		due := now
		if wasEnabled != 0 && lastAttemptAt.Valid {
			last, err := parseTime(lastAttemptAt.String)
			if err != nil {
				return err
			}
			due = last.Add(time.Duration(policy.ProbeIntervalMinutes) * time.Minute)
		}
		nextProbe = formatTime(due)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE repository_capacity_policies SET
			enabled=?,probe_interval_minutes=?,minimum_available_bytes=?,minimum_available_percent=?,
			exhaustion_warning_days=?,next_probe_at=?,
			claim_token=CASE WHEN ?=0 THEN '' ELSE claim_token END,
			claim_until=CASE WHEN ?=0 THEN NULL ELSE claim_until END,updated_at=?
		WHERE repository_id=?
	`, boolInt(policy.Enabled), policy.ProbeIntervalMinutes, int64(policy.MinimumAvailableBytes), policy.MinimumAvailablePercent,
		policy.ExhaustionWarningDays, nextProbe, boolInt(policy.Enabled), boolInt(policy.Enabled), formatTime(now), policy.RepositoryID)
	if err != nil {
		return constraintError(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) ClaimDueRepositoryCapacityPolicies(ctx context.Context, now time.Time, limit int, leaseDuration time.Duration) ([]RepositoryCapacityClaim, error) {
	if now.IsZero() || limit < 1 || limit > 100 || leaseDuration <= 0 || leaseDuration > 24*time.Hour {
		return nil, errors.New("invalid repository capacity claim request")
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT p.repository_id
		FROM repository_capacity_policies p
		JOIN repositories r ON r.id=p.repository_id
		WHERE p.enabled=1 AND p.next_probe_at IS NOT NULL AND p.next_probe_at<=?
		  AND (p.claim_until IS NULL OR p.claim_until<=?)
		  AND r.status NOT IN ('uninitialized','disconnected')
		ORDER BY next_probe_at,repository_id
		LIMIT ?
	`, formatTime(now), formatTime(now), limit)
	if err != nil {
		return nil, err
	}
	repositoryIDs := make([]string, 0, limit)
	for rows.Next() {
		var repositoryID string
		if err := rows.Scan(&repositoryID); err != nil {
			rows.Close()
			return nil, err
		}
		repositoryIDs = append(repositoryIDs, repositoryID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	claims := make([]RepositoryCapacityClaim, 0, len(repositoryIDs))
	for _, repositoryID := range repositoryIDs {
		token, err := newRepositoryCapacityClaimToken()
		if err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE repository_capacity_policies
			SET claim_token=?,claim_until=?,last_attempt_at=?
			WHERE repository_id=? AND enabled=1 AND next_probe_at IS NOT NULL AND next_probe_at<=?
			  AND (claim_until IS NULL OR claim_until<=?)
		`, token, formatTime(now.Add(leaseDuration)), formatTime(now), repositoryID, formatTime(now), formatTime(now))
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected == 1 {
			claims = append(claims, RepositoryCapacityClaim{RepositoryID: repositoryID, Token: token})
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *Store) ListRepositoryCapacitySamples(ctx context.Context, repositoryID string, limit int) ([]domain.RepositoryCapacitySample, error) {
	if strings.TrimSpace(repositoryID) == "" || limit < 1 || limit > 1000 {
		return nil, errors.New("invalid repository capacity sample query")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,repository_id,total_bytes,available_bytes,checked_at,COALESCE(source_agent_id,'')
		FROM repository_capacity_samples WHERE repository_id=?
		ORDER BY checked_at DESC,id DESC LIMIT ?
	`, repositoryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.RepositoryCapacitySample, 0)
	for rows.Next() {
		item, err := scanRepositoryCapacitySample(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) RepositoryCapacityForecast(ctx context.Context, repositoryID string) (domain.RepositoryCapacityForecast, error) {
	forecast := domain.RepositoryCapacityForecast{Status: domain.CapacityForecastInsufficientSamples}
	var firstAt, lastAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),MIN(checked_at),MAX(checked_at) FROM repository_capacity_samples WHERE repository_id=?`, repositoryID).Scan(&forecast.SampleCount, &firstAt, &lastAt); err != nil {
		return forecast, err
	}
	if forecast.SampleCount == 0 {
		return forecast, nil
	}
	firstTime, err := parseTime(firstAt.String)
	if err != nil {
		return forecast, err
	}
	lastTime, err := parseTime(lastAt.String)
	if err != nil {
		return forecast, err
	}
	forecast.ObservationStartedAt, forecast.ObservationEndedAt = &firstTime, &lastTime
	if forecast.SampleCount < 3 {
		return forecast, nil
	}
	if lastTime.Sub(firstTime) < 24*time.Hour {
		forecast.Status = domain.CapacityForecastInsufficientSpan
		return forecast, nil
	}
	first, err := queryRepositoryCapacitySample(ctx, s.db, repositoryID, "ASC")
	if err != nil {
		return forecast, err
	}
	last, err := queryRepositoryCapacitySample(ctx, s.db, repositoryID, "DESC")
	if err != nil {
		return forecast, err
	}
	usedGrowth := float64(last.UsedBytes) - float64(first.UsedBytes)
	forecast.GrowthBytesPerDay = usedGrowth / last.CheckedAt.Sub(first.CheckedAt).Hours() * 24
	if forecast.GrowthBytesPerDay <= 0 || math.IsNaN(forecast.GrowthBytesPerDay) || math.IsInf(forecast.GrowthBytesPerDay, 0) {
		forecast.Status = domain.CapacityForecastNonPositiveGrowth
		forecast.GrowthBytesPerDay = 0
		return forecast, nil
	}
	daysRemaining := float64(last.AvailableBytes) / forecast.GrowthBytesPerDay
	if daysRemaining > 36500 || math.IsNaN(daysRemaining) || math.IsInf(daysRemaining, 0) {
		forecast.Status = domain.CapacityForecastBeyondRange
		return forecast, nil
	}
	estimated := last.CheckedAt.Add(time.Duration(daysRemaining * float64(24*time.Hour)))
	forecast.Status = domain.CapacityForecastReady
	forecast.EstimatedExhaustionAt = &estimated
	return forecast, nil
}

func (s *Store) RecordRepositoryCapacityFailure(ctx context.Context, repositoryID string, attemptedAt time.Time, failure string) error {
	repositoryID = strings.TrimSpace(repositoryID)
	if repositoryID == "" || attemptedAt.IsZero() {
		return errors.New("invalid repository capacity failure")
	}
	failure = boundedRepositoryCapacityError(failure)
	if failure == "" {
		failure = "capacity probe failed"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var enabled int
	var interval int
	if err := tx.QueryRowContext(ctx, `SELECT enabled,probe_interval_minutes FROM repository_capacity_policies WHERE repository_id=?`, repositoryID).Scan(&enabled, &interval); err != nil {
		return err
	}
	var nextProbe any
	if enabled != 0 {
		nextProbe = formatTime(attemptedAt.UTC().Add(time.Duration(interval) * time.Minute))
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE repository_capacity_policies SET last_attempt_at=?,last_error=?,next_probe_at=?,claim_token='',claim_until=NULL
		WHERE repository_id=?
	`, formatTime(attemptedAt), failure, nextProbe, repositoryID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func saveRepositoryCapacity(ctx context.Context, tx *sql.Tx, repositoryID string, capacity domain.RepositoryCapacity) error {
	if err := validateRepositoryCapacity(repositoryID, capacity); err != nil {
		return err
	}
	if err := ensureDefaultCapacityPolicy(ctx, tx, repositoryID, capacity.CheckedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO repository_capacity_samples(repository_id,total_bytes,available_bytes,checked_at,source_agent_id)
		VALUES(?,?,?,?,?)
	`, repositoryID, int64(capacity.TotalBytes), int64(capacity.AvailableBytes), formatTime(capacity.CheckedAt), nullString(capacity.SourceAgentID)); err != nil {
		return constraintError(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO repository_capacities(repository_id,total_bytes,available_bytes,checked_at,source_agent_id)
		VALUES(?,?,?,?,?)
		ON CONFLICT(repository_id) DO UPDATE SET
			total_bytes=excluded.total_bytes,available_bytes=excluded.available_bytes,
			checked_at=excluded.checked_at,source_agent_id=excluded.source_agent_id
		WHERE excluded.checked_at >= repository_capacities.checked_at
	`, repositoryID, int64(capacity.TotalBytes), int64(capacity.AvailableBytes), formatTime(capacity.CheckedAt), nullString(capacity.SourceAgentID)); err != nil {
		return constraintError(err)
	}
	var enabled int
	var interval int
	if err := tx.QueryRowContext(ctx, `SELECT enabled,probe_interval_minutes FROM repository_capacity_policies WHERE repository_id=?`, repositoryID).Scan(&enabled, &interval); err != nil {
		return err
	}
	var nextProbe any
	if enabled != 0 {
		nextProbe = formatTime(capacity.CheckedAt.Add(time.Duration(interval) * time.Minute))
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE repository_capacity_policies SET last_attempt_at=?,last_success_at=?,last_error='',next_probe_at=?,claim_token='',claim_until=NULL
		WHERE repository_id=?
	`, formatTime(capacity.CheckedAt), formatTime(capacity.CheckedAt), nextProbe, repositoryID)
	return err
}

func ensureDefaultCapacityPolicy(ctx context.Context, tx *sql.Tx, repositoryID string, now time.Time) error {
	policy := domain.DefaultRepositoryCapacityPolicy(repositoryID, now)
	_, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO repository_capacity_policies(
			repository_id,enabled,probe_interval_minutes,minimum_available_bytes,minimum_available_percent,
			exhaustion_warning_days,next_probe_at,last_error,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?)
	`, repositoryID, boolInt(policy.Enabled), policy.ProbeIntervalMinutes, int64(policy.MinimumAvailableBytes), policy.MinimumAvailablePercent,
		policy.ExhaustionWarningDays, formatTime(*policy.NextProbeAt), "", formatTime(policy.UpdatedAt))
	return err
}

func validateRepositoryCapacity(repositoryID string, capacity domain.RepositoryCapacity) error {
	if strings.TrimSpace(repositoryID) == "" || capacity.TotalBytes == 0 || capacity.AvailableBytes > capacity.TotalBytes || capacity.TotalBytes > math.MaxInt64 || capacity.AvailableBytes > math.MaxInt64 || capacity.CheckedAt.IsZero() || len(capacity.SourceAgentID) > 256 || strings.ContainsRune(capacity.SourceAgentID, '\x00') {
		return errors.New("invalid repository capacity")
	}
	return nil
}

type capacitySampleScanner interface {
	Scan(...any) error
}

func scanRepositoryCapacityPolicy(scanner capacitySampleScanner) (domain.RepositoryCapacityPolicy, error) {
	var policy domain.RepositoryCapacityPolicy
	var enabled int
	var minimumBytes int64
	var nextProbeAt, lastAttemptAt, lastSuccessAt sql.NullString
	var updatedAt string
	err := scanner.Scan(&policy.RepositoryID, &enabled, &policy.ProbeIntervalMinutes, &minimumBytes, &policy.MinimumAvailablePercent,
		&policy.ExhaustionWarningDays, &nextProbeAt, &lastAttemptAt, &lastSuccessAt, &policy.LastError, &updatedAt)
	if err != nil {
		return policy, err
	}
	if minimumBytes < 0 {
		return policy, errors.New("stored repository capacity byte threshold is invalid")
	}
	policy.Enabled = enabled != 0
	policy.MinimumAvailableBytes = uint64(minimumBytes)
	if policy.NextProbeAt, err = parseOptionalCapacityTime(nextProbeAt); err != nil {
		return policy, err
	}
	if policy.LastAttemptAt, err = parseOptionalCapacityTime(lastAttemptAt); err != nil {
		return policy, err
	}
	if policy.LastSuccessAt, err = parseOptionalCapacityTime(lastSuccessAt); err != nil {
		return policy, err
	}
	policy.UpdatedAt, err = parseTime(updatedAt)
	return policy, err
}

func scanRepositoryCapacitySample(scanner capacitySampleScanner) (domain.RepositoryCapacitySample, error) {
	var sample domain.RepositoryCapacitySample
	var totalBytes, availableBytes int64
	var checkedAt string
	if err := scanner.Scan(&sample.ID, &sample.RepositoryID, &totalBytes, &availableBytes, &checkedAt, &sample.SourceAgentID); err != nil {
		return sample, err
	}
	if totalBytes <= 0 || availableBytes < 0 || availableBytes > totalBytes {
		return sample, errors.New("stored repository capacity sample is invalid")
	}
	sample.TotalBytes, sample.AvailableBytes = uint64(totalBytes), uint64(availableBytes)
	sample.UsedBytes = sample.TotalBytes - sample.AvailableBytes
	var err error
	sample.CheckedAt, err = parseTime(checkedAt)
	return sample, err
}

func queryRepositoryCapacitySample(ctx context.Context, db *sql.DB, repositoryID, direction string) (domain.RepositoryCapacitySample, error) {
	if direction != "ASC" && direction != "DESC" {
		return domain.RepositoryCapacitySample{}, errors.New("invalid capacity sample order")
	}
	return scanRepositoryCapacitySample(db.QueryRowContext(ctx, `
		SELECT id,repository_id,total_bytes,available_bytes,checked_at,COALESCE(source_agent_id,'')
		FROM repository_capacity_samples WHERE repository_id=? ORDER BY checked_at `+direction+`,id `+direction+` LIMIT 1
	`, repositoryID))
}

func parseOptionalCapacityTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func boundedRepositoryCapacityError(value string) string {
	value = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(value))
	if len(value) <= 512 {
		return value
	}
	cut := 512
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut]
}

func newRepositoryCapacityClaimToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate repository capacity claim token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}
