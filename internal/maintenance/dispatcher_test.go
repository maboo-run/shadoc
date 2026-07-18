package maintenance

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestMaintenanceDispatcherCatchesUpWithinWindow(t *testing.T) {
	anchor := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	source := newMaintenanceSource(testPolicy(anchor, domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Monday, TimeOfDay: "03:00"}, 120))
	runner := &maintenanceRunner{}
	dispatcher := New(source, runner)

	count, err := dispatcher.Tick(context.Background(), time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC))
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	waitForMaintenanceCalls(t, runner, 1)
	calls := runner.snapshot()
	if calls[0].repositoryID != "repo" || calls[0].dryRun || calls[0].retention.KeepLast != 3 {
		t.Fatalf("call=%+v", calls[0])
	}
	occurrences := source.snapshot()
	if len(occurrences) != 1 || occurrences[0].Mode != "catch_up" || occurrences[0].Status != "success" {
		t.Fatalf("occurrences=%+v", occurrences)
	}
}

func TestMaintenanceDispatcherRecordsMissedOutsideWindow(t *testing.T) {
	anchor := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	source := newMaintenanceSource(testPolicy(anchor, domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Monday, TimeOfDay: "03:00"}, 30))
	runner := &maintenanceRunner{}

	count, err := New(source, runner).Tick(context.Background(), time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC))
	if err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	occurrences := source.snapshot()
	if len(occurrences) != 1 || occurrences[0].Status != "missed" || runner.callCount() != 0 {
		t.Fatalf("occurrences=%+v calls=%d", occurrences, runner.callCount())
	}
}

func TestMaintenanceDispatcherFailsClosedOnPolicyConflict(t *testing.T) {
	anchor := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	policy := testPolicy(anchor, domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Monday, TimeOfDay: "03:00"}, 60)
	policy.RetentionConflict = true
	runner := &maintenanceRunner{}
	count, err := New(newMaintenanceSource(policy), runner).Tick(context.Background(), time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC))
	if err != nil || count != 0 || runner.callCount() != 0 {
		t.Fatalf("count=%d calls=%d err=%v", count, runner.callCount(), err)
	}
}

func TestConcurrentMaintenanceDispatchersClaimOnce(t *testing.T) {
	anchor := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	source := newMaintenanceSource(testPolicy(anchor, domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Monday, TimeOfDay: "03:00"}, 60))
	runner := &maintenanceRunner{}
	first, second := New(source, runner), New(source, runner)
	now := time.Date(2026, 7, 13, 3, 0, 30, 0, time.UTC)

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
		t.Fatalf("launched=%d", total)
	}
	waitForMaintenanceCalls(t, runner, 1)
	if len(source.snapshot()) != 1 || runner.callCount() != 1 {
		t.Fatalf("occurrences=%+v calls=%d", source.snapshot(), runner.callCount())
	}
}

func TestMaintenanceDispatcherKeepsIntervalCadenceAcrossInstances(t *testing.T) {
	anchor := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	source := newMaintenanceSource(testPolicy(anchor, domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 6}, 60))
	runner := &maintenanceRunner{}

	if count, err := New(source, runner).Tick(context.Background(), time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)); err != nil || count != 1 {
		t.Fatalf("first count=%d err=%v", count, err)
	}
	waitForMaintenanceCalls(t, runner, 1)
	if count, err := New(source, runner).Tick(context.Background(), time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)); err != nil || count != 1 {
		t.Fatalf("second count=%d err=%v", count, err)
	}
	waitForMaintenanceCalls(t, runner, 2)
	occurrences := source.snapshot()
	if len(occurrences) != 2 || !occurrences[0].ScheduledAt.Equal(time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)) || !occurrences[1].ScheduledAt.Equal(time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("occurrences=%+v", occurrences)
	}
}

func testPolicy(anchor time.Time, schedule domain.Schedule, catchUpMinutes int) domain.MaintenancePolicy {
	return domain.MaintenancePolicy{
		RepositoryID:         "repo",
		Schedule:             schedule,
		Timezone:             "UTC",
		Retention:            domain.RetentionPolicy{KeepLast: 3},
		Enabled:              true,
		CatchUpWindowMinutes: catchUpMinutes,
		ScheduleAnchorAt:     anchor,
		UpdatedAt:            anchor,
	}
}

type maintenanceSource struct {
	mu          sync.Mutex
	policies    []domain.MaintenancePolicy
	occurrences map[string]store.ScheduleOccurrence
}

func newMaintenanceSource(policies ...domain.MaintenancePolicy) *maintenanceSource {
	return &maintenanceSource{policies: policies, occurrences: map[string]store.ScheduleOccurrence{}}
}

func (s *maintenanceSource) ListMaintenancePolicies(context.Context) ([]domain.MaintenancePolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.MaintenancePolicy(nil), s.policies...), nil
}

func (s *maintenanceSource) LatestScheduleOccurrence(_ context.Context, ownerKind, ownerID string, anchor time.Time) (store.ScheduleOccurrence, error) {
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

func (s *maintenanceSource) CreateScheduleOccurrence(_ context.Context, occurrence store.ScheduleOccurrence) (bool, error) {
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

func (s *maintenanceSource) ClaimScheduleOccurrence(_ context.Context, id string, at time.Time) (bool, error) {
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

func (s *maintenanceSource) FinishScheduleOccurrence(_ context.Context, id, status string, runIDs []string, at time.Time) error {
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

func (s *maintenanceSource) snapshot() []store.ScheduleOccurrence {
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

type maintenanceCall struct {
	repositoryID string
	retention    domain.RetentionPolicy
	dryRun       bool
}

type maintenanceRunner struct {
	mu    sync.Mutex
	calls []maintenanceCall
	err   error
}

func (r *maintenanceRunner) Maintain(_ context.Context, repositoryID string, retention domain.RetentionPolicy, dryRun bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, maintenanceCall{repositoryID: repositoryID, retention: retention, dryRun: dryRun})
	return r.err
}

func (r *maintenanceRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *maintenanceRunner) snapshot() []maintenanceCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]maintenanceCall(nil), r.calls...)
}

func waitForMaintenanceCalls(t *testing.T, runner *maintenanceRunner, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for runner.callCount() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := runner.callCount(); got != want {
		t.Fatalf("maintenance calls=%d want=%d", got, want)
	}
}
