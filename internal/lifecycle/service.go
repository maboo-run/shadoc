package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
)

type Policy = store.LifecyclePolicy
type Report = store.LifecycleReport

type lifecycleStore interface {
	LoadLifecyclePolicy(context.Context) (store.LifecyclePolicy, error)
	SaveLifecyclePolicy(context.Context, store.LifecyclePolicy, time.Time) error
	CleanupExecutionData(context.Context, store.LifecyclePolicy, time.Time) (store.LifecycleReport, error)
	PreviewExecutionDataCleanup(context.Context, store.LifecyclePolicy, time.Time) (store.LifecycleReport, error)
	AppendAudit(context.Context, store.AuditRecord) error
}

type Service struct {
	store lifecycleStore
}

func New(s lifecycleStore) *Service {
	return &Service{store: s}
}

func (s *Service) Policy(ctx context.Context) (Policy, error) {
	return s.store.LoadLifecyclePolicy(ctx)
}

func (s *Service) SavePolicy(ctx context.Context, policy Policy, now time.Time) error {
	if err := Validate(policy); err != nil {
		return err
	}
	return s.store.SaveLifecyclePolicy(ctx, policy, now)
}

func (s *Service) Cleanup(ctx context.Context, policy Policy, now time.Time) (Report, error) {
	if err := Validate(policy); err != nil {
		return Report{}, err
	}
	report, err := s.store.CleanupExecutionData(ctx, policy, now)
	if err != nil {
		return report, err
	}
	if err := s.store.AppendAudit(ctx, store.AuditRecord{
		OccurredAt: now.UTC(), Action: "lifecycle.cleanup", TargetType: "application",
		Detail: map[string]any{
			"logsCleared": report.LogsCleared, "runsDeleted": report.RunsDeleted,
			"auditsDeleted": report.AuditsDeleted, "capacitySamplesDeleted": report.CapacitySamplesDeleted,
			"rawLogBytesAfter": report.RawLogBytesAfter,
		},
	}); err != nil {
		return report, err
	}
	return report, nil
}

func (s *Service) CleanupConfigured(ctx context.Context, now time.Time) (Report, error) {
	policy, err := s.Policy(ctx)
	if err != nil {
		return Report{}, err
	}
	return s.Cleanup(ctx, policy, now)
}

func (s *Service) PreviewConfigured(ctx context.Context, now time.Time) (Report, error) {
	policy, err := s.Policy(ctx)
	if err != nil {
		return Report{}, err
	}
	if err := Validate(policy); err != nil {
		return Report{}, err
	}
	return s.store.PreviewExecutionDataCleanup(ctx, policy, now)
}

func Validate(policy Policy) error {
	if policy.RunDays < 0 || policy.RawLogDays < 0 || policy.AuditDays < 0 || policy.RawLogMaxBytes < 0 {
		return errors.New("lifecycle values cannot be negative")
	}
	if policy.RunDays > 36500 || policy.RawLogDays > 36500 || policy.AuditDays > 36500 || policy.RawLogMaxBytes > 1<<40 {
		return errors.New("lifecycle values exceed safe limits")
	}
	return nil
}
