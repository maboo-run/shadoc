package maintenance

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/schedule"
)

type Source interface {
	schedule.OccurrenceStore
	ListMaintenancePolicies(context.Context) ([]domain.MaintenancePolicy, error)
	ClaimScheduleOccurrence(context.Context, string, time.Time) (bool, error)
	FinishScheduleOccurrence(context.Context, string, string, []string, time.Time) error
}
type Runner interface {
	Maintain(context.Context, string, domain.RetentionPolicy, bool) error
}
type Dispatcher struct {
	source Source
	runner Runner
	mu     sync.Mutex
	wg     sync.WaitGroup
}

func New(source Source, runner Runner) *Dispatcher {
	return &Dispatcher{source: source, runner: runner}
}
func (d *Dispatcher) Tick(ctx context.Context, now time.Time) (int, error) {
	items, err := d.source.ListMaintenancePolicies(ctx)
	if err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	recorder := schedule.Recorder{Store: d.source}
	for _, item := range items {
		if !item.Enabled || item.RetentionConflict {
			continue
		}
		anchor := item.ScheduleAnchorAt
		if anchor.IsZero() {
			anchor = item.UpdatedAt
		}
		occurrences, err := recorder.RecordDue(ctx, schedule.Definition{
			OwnerKind:     "maintenance",
			OwnerID:       item.RepositoryID,
			Schedule:      item.Schedule,
			Timezone:      item.Timezone,
			AnchorAt:      anchor,
			CatchUpWindow: time.Duration(item.CatchUpWindowMinutes) * time.Minute,
			TargetIDs:     []string{item.RepositoryID},
		}, now)
		if err != nil {
			return count, err
		}
		for _, occurrence := range occurrences {
			claimed, err := d.source.ClaimScheduleOccurrence(ctx, occurrence.ID, now.UTC())
			if err != nil {
				return count, err
			}
			if !claimed {
				continue
			}
			count++
			item, occurrence := item, occurrence
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				status := "failed"
				if d.runner != nil {
					err := d.runner.Maintain(ctx, item.RepositoryID, item.Retention, false)
					if err == nil {
						status = "success"
					} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						status = "cancelled"
					}
				}
				_ = d.source.FinishScheduleOccurrence(context.WithoutCancel(ctx), occurrence.ID, status, []string{}, time.Now().UTC())
			}()
		}
	}
	return count, nil
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
