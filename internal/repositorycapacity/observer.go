package repositorycapacity

import (
	"context"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

type RunObserverStore interface {
	RepositoryCapacityPolicy(context.Context, string) (domain.RepositoryCapacityPolicy, error)
	RecordRepositoryCapacityFailure(context.Context, string, time.Time, string) error
}

type RunObserver struct {
	store  RunObserverStore
	prober interface {
		Probe(context.Context, string, StageReporter) (domain.RepositoryCapacity, error)
	}
	now func() time.Time
}

func NewRunObserver(storage RunObserverStore, prober interface {
	Probe(context.Context, string, StageReporter) (domain.RepositoryCapacity, error)
}, now func() time.Time) *RunObserver {
	if now == nil {
		now = time.Now
	}
	return &RunObserver{store: storage, prober: prober, now: now}
}

func (o *RunObserver) ObserveRun(ctx context.Context, task domain.Task, _ store.RunRecord) error {
	if o == nil || o.prober == nil || task.RepositoryID == "" {
		return nil
	}
	if o.store != nil {
		policy, err := o.store.RepositoryCapacityPolicy(ctx, task.RepositoryID)
		if err != nil {
			return err
		}
		if !policy.Enabled {
			return nil
		}
	}
	_, err := o.prober.Probe(ctx, task.RepositoryID, nil)
	if err != nil && o.store != nil {
		_ = o.store.RecordRepositoryCapacityFailure(context.WithoutCancel(ctx), task.RepositoryID, o.now().UTC(), err.Error())
	}
	return err
}
