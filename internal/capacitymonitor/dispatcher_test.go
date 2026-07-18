package capacitymonitor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/repositorycapacity"
	"github.com/maboo-run/shadoc/internal/store"
)

type blockingCapacityProbe struct {
	storage *store.Store
	started chan string
	release chan struct{}
	calls   atomic.Int32
	failure error
	checked time.Time
}

func (p *blockingCapacityProbe) Probe(ctx context.Context, repositoryID string, report repositorycapacity.StageReporter) (domain.RepositoryCapacity, error) {
	p.calls.Add(1)
	p.started <- repositoryID
	<-p.release
	if p.failure != nil {
		return domain.RepositoryCapacity{}, p.failure
	}
	capacity := domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: 400, UsedBytes: 600, CheckedAt: p.checked}
	if err := p.storage.SaveRepositoryCapacity(ctx, repositoryID, capacity); err != nil {
		return domain.RepositoryCapacity{}, err
	}
	return capacity, nil
}

func TestDispatcherProbesDuePolicyWithoutHTTPAndDoesNotOverlapRepository(t *testing.T) {
	storage := openCapacityMonitorStore(t)
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	createCapacityMonitorRepository(t, storage, "repo", now)
	probe := &blockingCapacityProbe{storage: storage, started: make(chan string, 2), release: make(chan struct{}), checked: now.Add(time.Minute)}
	dispatcher := New(storage, probe, 2)

	count, err := dispatcher.Tick(context.Background(), now)
	if err != nil || count != 1 {
		t.Fatalf("first tick count=%d err=%v", count, err)
	}
	select {
	case repositoryID := <-probe.started:
		if repositoryID != "repo" {
			t.Fatalf("repository=%q", repositoryID)
		}
	case <-time.After(time.Second):
		t.Fatal("background probe did not start")
	}
	count, err = dispatcher.Tick(context.Background(), now.Add(time.Minute))
	if err != nil || count != 0 || probe.calls.Load() != 1 {
		t.Fatalf("overlapping tick count=%d calls=%d err=%v", count, probe.calls.Load(), err)
	}

	close(probe.release)
	dispatcher.Wait()
	samples, err := storage.ListRepositoryCapacitySamples(context.Background(), "repo", 10)
	if err != nil || len(samples) != 1 || samples[0].AvailableBytes != 400 {
		t.Fatalf("samples=%+v err=%v", samples, err)
	}
}

func TestDispatcherPersistsBoundedProbeFailure(t *testing.T) {
	storage := openCapacityMonitorStore(t)
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	createCapacityMonitorRepository(t, storage, "repo", now)
	probe := &blockingCapacityProbe{
		storage: storage, started: make(chan string, 1), release: make(chan struct{}), checked: now,
		failure: errors.New("agent failed\x00\n" + string(make([]byte, 700))),
	}
	dispatcher := New(storage, probe, 1)
	dispatcher.SetClock(func() time.Time { return now.Add(2 * time.Minute) })
	if count, err := dispatcher.Tick(context.Background(), now); err != nil || count != 1 {
		t.Fatalf("tick count=%d err=%v", count, err)
	}
	<-probe.started
	close(probe.release)
	dispatcher.Wait()

	policy, err := storage.RepositoryCapacityPolicy(context.Background(), "repo")
	if err != nil || policy.LastError == "" || len(policy.LastError) > 512 || policy.LastAttemptAt == nil || !policy.LastAttemptAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
}

func openCapacityMonitorStore(t *testing.T) *store.Store {
	t.Helper()
	storage, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	return storage
}

func createCapacityMonitorRepository(t *testing.T, storage *store.Store, id string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	secretID := id + "-password"
	if err := storage.SaveSecret(ctx, secretID, "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateRepository(ctx, domain.Repository{ID: id, Name: id, Kind: domain.LocalRepository, Path: "/backup/" + id, Status: "ready", CreatedAt: now, UpdatedAt: now}, secretID); err != nil {
		t.Fatal(err)
	}
}
