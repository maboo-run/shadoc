package capacitymonitor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/repositorycapacity"
	"github.com/maboo-run/shadoc/internal/store"
)

const capacityClaimLease = 10 * time.Minute

type Source interface {
	ClaimDueRepositoryCapacityPolicies(context.Context, time.Time, int, time.Duration) ([]store.RepositoryCapacityClaim, error)
	RecordRepositoryCapacityFailure(context.Context, string, time.Time, string) error
}

type Runner interface {
	Probe(context.Context, string, repositorycapacity.StageReporter) (domain.RepositoryCapacity, error)
}

type Dispatcher struct {
	source      Source
	runner      Runner
	maxParallel int
	now         func() time.Time

	tickMu sync.Mutex
	mu     sync.Mutex
	active map[string]bool
	wg     sync.WaitGroup
}

func New(source Source, runner Runner, maxParallel int) *Dispatcher {
	if maxParallel < 1 {
		maxParallel = 2
	}
	return &Dispatcher{source: source, runner: runner, maxParallel: maxParallel, now: time.Now, active: map[string]bool{}}
}

func (d *Dispatcher) SetClock(now func() time.Time) {
	if now != nil {
		d.now = now
	}
}

func (d *Dispatcher) Tick(ctx context.Context, now time.Time) (int, error) {
	if d == nil || d.source == nil || now.IsZero() {
		return 0, errors.New("capacity monitor is not configured")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	d.tickMu.Lock()
	defer d.tickMu.Unlock()
	d.mu.Lock()
	available := d.maxParallel - len(d.active)
	d.mu.Unlock()
	if available <= 0 {
		return 0, nil
	}
	claims, err := d.source.ClaimDueRepositoryCapacityPolicies(ctx, now.UTC(), available, capacityClaimLease)
	if err != nil {
		return 0, err
	}
	started := 0
	for _, claim := range claims {
		if claim.RepositoryID == "" {
			continue
		}
		d.mu.Lock()
		if d.active[claim.RepositoryID] {
			d.mu.Unlock()
			continue
		}
		d.active[claim.RepositoryID] = true
		d.wg.Add(1)
		d.mu.Unlock()
		started++
		go d.probe(ctx, claim.RepositoryID)
	}
	return started, nil
}

func (d *Dispatcher) probe(ctx context.Context, repositoryID string) {
	defer func() {
		d.mu.Lock()
		delete(d.active, repositoryID)
		d.mu.Unlock()
		d.wg.Done()
	}()
	var err error
	if d.runner == nil {
		err = errors.New("repository capacity probe service is unavailable")
	} else {
		_, err = d.runner.Probe(ctx, repositoryID, nil)
	}
	if err != nil {
		_ = d.source.RecordRepositoryCapacityFailure(context.WithoutCancel(ctx), repositoryID, d.now().UTC(), err.Error())
	}
}

func (d *Dispatcher) Wait() {
	if d != nil {
		d.wg.Wait()
	}
}

func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) {
	defer d.Wait()
	if interval <= 0 {
		interval = 30 * time.Second
	}
	_, _ = d.Tick(ctx, d.now().UTC())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			_, _ = d.Tick(ctx, now)
		}
	}
}
