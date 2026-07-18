package lifecycle

import (
	"context"
	"log/slog"
	"time"
)

type configuredCleaner interface {
	CleanupConfigured(context.Context, time.Time) (Report, error)
}

type Dispatcher struct {
	cleaner configuredCleaner
	now     func() time.Time
}

func NewDispatcher(cleaner configuredCleaner) *Dispatcher {
	return &Dispatcher{cleaner: cleaner, now: time.Now}
}

func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	d.cleanup(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.cleanup(ctx)
		}
	}
}

func (d *Dispatcher) cleanup(ctx context.Context) {
	if _, err := d.cleaner.CleanupConfigured(ctx, d.now()); err != nil && ctx.Err() == nil {
		slog.Error("lifecycle cleanup", "error", err)
	}
}
