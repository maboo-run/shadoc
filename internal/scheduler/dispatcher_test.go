package scheduler

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestDispatcherCatchesUpAfterRestartWithinWindow(t *testing.T) {
	anchor := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	source := newDurablePlanSource(testPlan(anchor, domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:30"}, 60, 1, "task"))
	runner := &recordingRunner{}
	dispatcher := New(source, runner)

	count, err := dispatcher.Tick(context.Background(), time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC))
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	waitForRunnerCalls(t, runner, 1)
	occurrences := source.snapshot()
	if len(occurrences) != 1 || occurrences[0].Mode != "catch_up" || occurrences[0].Status != "success" {
		t.Fatalf("occurrences=%+v", occurrences)
	}
}

func TestDispatcherRecordsMissedOccurrenceOutsideWindow(t *testing.T) {
	anchor := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	source := newDurablePlanSource(testPlan(anchor, domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:30"}, 30, 1, "task"))
	runner := &recordingRunner{}
	dispatcher := New(source, runner)

	count, err := dispatcher.Tick(context.Background(), time.Date(2026, 7, 15, 4, 0, 0, 0, time.UTC))
	if err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	occurrences := source.snapshot()
	if len(occurrences) != 1 || occurrences[0].Status != "missed" {
		t.Fatalf("occurrences=%+v", occurrences)
	}
	if runner.callCount() != 0 {
		t.Fatalf("runner calls=%d", runner.callCount())
	}
}

func TestConcurrentDispatchersClaimOccurrenceOnce(t *testing.T) {
	anchor := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	source := newDurablePlanSource(testPlan(anchor, domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:30"}, 60, 1, "task"))
	runner := &recordingRunner{}
	first, second := New(source, runner), New(source, runner)
	now := time.Date(2026, 7, 15, 2, 30, 30, 0, time.UTC)

	var wg sync.WaitGroup
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	for _, dispatcher := range []*Dispatcher{first, second} {
		wg.Add(1)
		go func(dispatcher *Dispatcher) {
			defer wg.Done()
			count, err := dispatcher.Tick(context.Background(), now)
			counts <- count
			errs <- err
		}(dispatcher)
	}
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for count := range counts {
		total += count
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if total != 1 {
		t.Fatalf("launched=%d want=1", total)
	}
	waitForRunnerCalls(t, runner, 1)
	if len(source.snapshot()) != 1 || runner.callCount() != 1 {
		t.Fatalf("occurrences=%+v calls=%d", source.snapshot(), runner.callCount())
	}
}

func TestDispatcherRespectsPlanParallelLimit(t *testing.T) {
	anchor := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	source := newDurablePlanSource(testPlan(anchor, domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:30"}, 60, 1, "task-a", "task-b"))
	release := make(chan struct{})
	runner := &recordingRunner{started: make(chan string, 2), release: release}
	dispatcher := New(source, runner)

	count, err := dispatcher.Tick(context.Background(), time.Date(2026, 7, 15, 2, 30, 30, 0, time.UTC))
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}
	select {
	case task := <-runner.started:
		t.Fatalf("second task %s bypassed max parallel", task)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	waitForRunnerCalls(t, runner, 2)
	if runner.maximumActive() != 1 {
		t.Fatalf("max active=%d", runner.maximumActive())
	}
}

func TestDispatcherKeepsIntervalCadenceAcrossInstances(t *testing.T) {
	anchor := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	source := newDurablePlanSource(testPlan(anchor, domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 6}, 60, 1, "task"))
	runner := &recordingRunner{}

	if count, err := New(source, runner).Tick(context.Background(), time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)); err != nil || count != 1 {
		t.Fatalf("first count=%d err=%v", count, err)
	}
	waitForRunnerCalls(t, runner, 1)
	if count, err := New(source, runner).Tick(context.Background(), time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)); err != nil || count != 1 {
		t.Fatalf("second count=%d err=%v", count, err)
	}
	waitForRunnerCalls(t, runner, 2)
	occurrences := source.snapshot()
	if len(occurrences) != 2 || !occurrences[0].ScheduledAt.Equal(time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)) || !occurrences[1].ScheduledAt.Equal(time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("occurrences=%+v", occurrences)
	}
}

func testPlan(anchor time.Time, schedule domain.Schedule, catchUpMinutes, maxParallel int, taskIDs ...string) domain.Plan {
	return domain.Plan{
		ID:                   "plan",
		Name:                 "scheduled plan",
		Schedule:             schedule,
		Timezone:             "UTC",
		MaxParallel:          maxParallel,
		TaskIDs:              taskIDs,
		Enabled:              true,
		CatchUpWindowMinutes: catchUpMinutes,
		ScheduleAnchorAt:     anchor,
		CreatedAt:            anchor,
		UpdatedAt:            anchor,
	}
}

type durablePlanSource struct {
	mu          sync.Mutex
	plans       []domain.Plan
	occurrences map[string]store.ScheduleOccurrence
}

func newDurablePlanSource(plans ...domain.Plan) *durablePlanSource {
	return &durablePlanSource{plans: plans, occurrences: map[string]store.ScheduleOccurrence{}}
}

func (s *durablePlanSource) ListPlans(context.Context) ([]domain.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Plan(nil), s.plans...), nil
}

func (s *durablePlanSource) LatestScheduleOccurrence(_ context.Context, ownerKind, ownerID string, anchor time.Time) (store.ScheduleOccurrence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest store.ScheduleOccurrence
	for _, item := range s.occurrences {
		if item.OwnerKind == ownerKind && item.OwnerID == ownerID && !item.ScheduledAt.Before(anchor) && (latest.ID == "" || item.ScheduledAt.After(latest.ScheduledAt)) {
			latest = item
		}
	}
	if latest.ID == "" {
		return store.ScheduleOccurrence{}, sql.ErrNoRows
	}
	return latest, nil
}

func (s *durablePlanSource) CreateScheduleOccurrence(_ context.Context, occurrence store.ScheduleOccurrence) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.occurrences {
		if item.OwnerKind == occurrence.OwnerKind && item.OwnerID == occurrence.OwnerID && item.ScheduledAt.Equal(occurrence.ScheduledAt) {
			return false, nil
		}
	}
	s.occurrences[occurrence.ID] = occurrence
	return true, nil
}

func (s *durablePlanSource) ClaimScheduleOccurrence(_ context.Context, id string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.occurrences[id]
	if !ok || item.Status != "pending" {
		return false, nil
	}
	started := at
	item.Status, item.StartedAt = "running", &started
	s.occurrences[id] = item
	return true, nil
}

func (s *durablePlanSource) FinishScheduleOccurrence(_ context.Context, id, status string, runIDs []string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.occurrences[id]
	if !ok || item.Status != "running" {
		return sql.ErrNoRows
	}
	finished := at
	item.Status, item.RunIDs, item.FinishedAt = status, append([]string(nil), runIDs...), &finished
	s.occurrences[id] = item
	return nil
}

func (s *durablePlanSource) snapshot() []store.ScheduleOccurrence {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]store.ScheduleOccurrence, 0, len(s.occurrences))
	for _, item := range s.occurrences {
		items = append(items, item)
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].ScheduledAt.Before(items[i].ScheduledAt) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	return items
}

type recordingRunner struct {
	mu        sync.Mutex
	tasks     []string
	active    int
	maxActive int
	started   chan string
	release   chan struct{}
}

func (r *recordingRunner) Run(_ context.Context, task, plan, trigger string) (store.RunRecord, error) {
	r.mu.Lock()
	r.tasks = append(r.tasks, task+":"+plan+":"+trigger)
	r.active++
	if r.active > r.maxActive {
		r.maxActive = r.active
	}
	r.mu.Unlock()
	if r.started != nil {
		r.started <- task
	}
	if r.release != nil {
		<-r.release
	}
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return store.RunRecord{ID: "run-" + task, TaskID: task, PlanID: plan, Trigger: trigger, Status: "success"}, nil
}

func (r *recordingRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tasks)
}

func (r *recordingRunner) maximumActive() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxActive
}

func waitForRunnerCalls(t *testing.T, runner *recordingRunner, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for runner.callCount() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := runner.callCount(); got != want {
		t.Fatalf("runner calls=%d want=%d", got, want)
	}
}
