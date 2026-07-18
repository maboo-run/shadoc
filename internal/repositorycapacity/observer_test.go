package repositorycapacity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

type observerProbe struct {
	repositoryID string
	err          error
}

func (p *observerProbe) Probe(_ context.Context, repositoryID string, _ StageReporter) (domain.RepositoryCapacity, error) {
	p.repositoryID = repositoryID
	return domain.RepositoryCapacity{}, p.err
}

type observerFailureStore struct {
	repositoryID string
	failure      string
	enabled      bool
}

func (s *observerFailureStore) RepositoryCapacityPolicy(_ context.Context, repositoryID string) (domain.RepositoryCapacityPolicy, error) {
	return domain.RepositoryCapacityPolicy{RepositoryID: repositoryID, Enabled: s.enabled}, nil
}

func (s *observerFailureStore) RecordRepositoryCapacityFailure(_ context.Context, repositoryID string, _ time.Time, failure string) error {
	s.repositoryID, s.failure = repositoryID, failure
	return nil
}

func TestRunObserverRefreshesRepositoryCapacityAndRecordsFailure(t *testing.T) {
	probe := &observerProbe{err: errors.New("probe unavailable")}
	failures := &observerFailureStore{enabled: true}
	observer := NewRunObserver(failures, probe, func() time.Time { return time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC) })
	task := domain.Task{ID: "task", RepositoryID: "repo"}
	err := observer.ObserveRun(context.Background(), task, store.RunRecord{ID: "run", Status: "failed"})
	if !errors.Is(err, probe.err) || probe.repositoryID != "repo" || failures.repositoryID != "repo" || failures.failure != "probe unavailable" {
		t.Fatalf("err=%v probe=%q failure=%+v", err, probe.repositoryID, failures)
	}
}

func TestRunObserverSkipsRepositoriesWithoutCapacitySupport(t *testing.T) {
	probe := &observerProbe{}
	observer := NewRunObserver(&observerFailureStore{}, probe, time.Now)
	if err := observer.ObserveRun(context.Background(), domain.Task{RepositoryID: "repo-s3"}, store.RunRecord{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if probe.repositoryID != "" {
		t.Fatalf("disabled capacity policy was probed: %q", probe.repositoryID)
	}
}
