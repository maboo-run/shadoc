package restoreverification

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/schedule"
	"github.com/maboo-run/shadoc/internal/store"
)

type DispatcherSource interface {
	schedule.OccurrenceStore
	ListRestoreVerificationPolicies(context.Context) ([]domain.RestoreVerificationPolicy, error)
	ClaimScheduleOccurrence(context.Context, string, time.Time) (bool, error)
	FinishScheduleOccurrence(context.Context, string, string, []string, time.Time) error
}

type DispatcherRunner interface {
	Run(context.Context, string, string) (store.RestoreVerificationRecord, error)
}

type Dispatcher struct {
	source DispatcherSource
	runner DispatcherRunner
	mu     sync.Mutex
	active map[string]bool
	wg     sync.WaitGroup
}

func NewDispatcher(source DispatcherSource, runner DispatcherRunner) *Dispatcher {
	return &Dispatcher{source: source, runner: runner, active: map[string]bool{}}
}

func (d *Dispatcher) Tick(ctx context.Context, now time.Time) (int, error) {
	policies, err := d.source.ListRestoreVerificationPolicies(ctx)
	if err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	launched := 0
	recorder := schedule.Recorder{Store: d.source}
	for _, policy := range policies {
		if !policy.Enabled || d.active[policy.TaskID] {
			continue
		}
		anchor := policy.ScheduleAnchorAt
		if anchor.IsZero() {
			anchor = policy.UpdatedAt
		}
		occurrences, err := recorder.RecordDue(ctx, schedule.Definition{
			OwnerKind:     "restore_verification",
			OwnerID:       policy.TaskID,
			Schedule:      policy.Schedule,
			Timezone:      policy.Timezone,
			AnchorAt:      anchor,
			CatchUpWindow: time.Duration(policy.CatchUpWindowMinutes) * time.Minute,
			TargetIDs:     []string{policy.TaskID},
		}, now)
		if err != nil {
			return launched, err
		}
		for _, occurrence := range occurrences {
			claimed, err := d.source.ClaimScheduleOccurrence(ctx, occurrence.ID, now.UTC())
			if err != nil {
				return launched, err
			}
			if !claimed {
				continue
			}
			d.active[policy.TaskID] = true
			launched++
			policy, occurrence := policy, occurrence
			d.wg.Add(1)
			go d.execute(ctx, policy.TaskID, occurrence)
			break
		}
	}
	return launched, nil
}

func (d *Dispatcher) execute(ctx context.Context, taskID string, occurrence store.ScheduleOccurrence) {
	defer d.wg.Done()
	defer func() {
		d.mu.Lock()
		delete(d.active, taskID)
		d.mu.Unlock()
	}()
	status := "failed"
	runIDs := []string{}
	if d.runner != nil {
		record, err := d.runner.Run(ctx, taskID, "scheduled")
		if record.ID != "" {
			runIDs = append(runIDs, record.ID)
		}
		status = restoreVerificationScheduleStatus(record.Status, err)
	}
	_ = d.source.FinishScheduleOccurrence(context.WithoutCancel(ctx), occurrence.ID, status, runIDs, time.Now().UTC())
}

func restoreVerificationScheduleStatus(status string, err error) string {
	if status == "success" && err == nil {
		return "success"
	}
	if status == "cancelled" || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	return "failed"
}

func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) {
	defer d.wg.Wait()
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	_, _ = d.Tick(ctx, time.Now())
	for {
		select {
		case now := <-ticker.C:
			_, _ = d.Tick(ctx, now)
		case <-ctx.Done():
			return
		}
	}
}
