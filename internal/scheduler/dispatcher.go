package scheduler

import (
	"context"
	"sync"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/schedule"
	"github.com/maboo-run/shadoc/internal/store"
)

type PlanSource interface {
	schedule.OccurrenceStore
	ListPlans(context.Context) ([]domain.Plan, error)
	ClaimScheduleOccurrence(context.Context, string, time.Time) (bool, error)
	FinishScheduleOccurrence(context.Context, string, string, []string, time.Time) error
}
type Runner interface {
	Run(context.Context, string, string, string) (store.RunRecord, error)
}
type Dispatcher struct {
	source PlanSource
	runner Runner
	mu     sync.Mutex
	wg     sync.WaitGroup
}

func New(source PlanSource, runner Runner) *Dispatcher {
	return &Dispatcher{source: source, runner: runner}
}

func (d *Dispatcher) Tick(ctx context.Context, now time.Time) (int, error) {
	plans, err := d.source.ListPlans(ctx)
	if err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	launched := 0
	recorder := schedule.Recorder{Store: d.source}
	for _, plan := range plans {
		if !plan.Enabled {
			continue
		}
		anchor := plan.ScheduleAnchorAt
		if anchor.IsZero() {
			anchor = plan.CreatedAt
		}
		occurrences, err := recorder.RecordDue(ctx, schedule.Definition{
			OwnerKind:     "plan",
			OwnerID:       plan.ID,
			Schedule:      plan.Schedule,
			Timezone:      plan.Timezone,
			AnchorAt:      anchor,
			CatchUpWindow: time.Duration(plan.CatchUpWindowMinutes) * time.Minute,
			TargetIDs:     plan.TaskIDs,
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
			launched += len(occurrence.TargetIDs)
			plan, occurrence := plan, occurrence
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				d.executeOccurrence(ctx, plan, occurrence)
			}()
		}
	}
	return launched, nil
}

func (d *Dispatcher) executeOccurrence(ctx context.Context, plan domain.Plan, occurrence store.ScheduleOccurrence) {
	limit := plan.MaxParallel
	if limit < 1 {
		limit = 1
	}
	semaphore := make(chan struct{}, limit)
	type taskResult struct {
		status string
		runID  string
	}
	results := make(chan taskResult, len(occurrence.TargetIDs))
	var tasks sync.WaitGroup
	for _, taskID := range occurrence.TargetIDs {
		taskID := taskID
		tasks.Add(1)
		go func() {
			defer tasks.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- taskResult{status: "cancelled"}
				return
			}
			if d.runner == nil {
				results <- taskResult{status: "failed"}
				return
			}
			record, err := d.runner.Run(ctx, taskID, plan.ID, "schedule")
			results <- taskResult{status: scheduleTaskStatus(record.Status, err), runID: record.ID}
		}()
	}
	tasks.Wait()
	close(results)
	statuses := make([]string, 0, len(occurrence.TargetIDs))
	runIDs := make([]string, 0, len(occurrence.TargetIDs))
	for result := range results {
		statuses = append(statuses, result.status)
		if result.runID != "" {
			runIDs = append(runIDs, result.runID)
		}
	}
	_ = d.source.FinishScheduleOccurrence(context.WithoutCancel(ctx), occurrence.ID, aggregateScheduleStatuses(statuses), runIDs, time.Now().UTC())
}

func scheduleTaskStatus(status string, err error) string {
	if err != nil {
		if status == "cancelled" {
			return "cancelled"
		}
		return "failed"
	}
	switch status {
	case "success":
		return "success"
	case "partial", "failed", "cancelled", "skipped":
		return status
	default:
		return "failed"
	}
}

func aggregateScheduleStatuses(statuses []string) string {
	if len(statuses) == 0 {
		return "failed"
	}
	counts := map[string]int{}
	for _, status := range statuses {
		counts[status]++
	}
	if counts["partial"] > 0 {
		return "partial"
	}
	if counts["success"] == len(statuses) {
		return "success"
	}
	if counts["cancelled"] == len(statuses) {
		return "cancelled"
	}
	if counts["skipped"] == len(statuses) {
		return "skipped"
	}
	if counts["failed"] == len(statuses) {
		return "failed"
	}
	if counts["success"] > 0 {
		return "partial"
	}
	if counts["failed"] > 0 {
		return "failed"
	}
	if counts["cancelled"] > 0 {
		return "cancelled"
	}
	return "skipped"
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
