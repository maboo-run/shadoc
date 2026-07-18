package lifecycle

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeConfiguredCleaner struct{ calls atomic.Int32 }

func (f *fakeConfiguredCleaner) CleanupConfigured(context.Context, time.Time) (Report, error) {
	f.calls.Add(1)
	return Report{}, nil
}

func TestDispatcherCleansAtStartupAndOnInterval(t *testing.T) {
	cleaner := &fakeConfiguredCleaner{}
	dispatcher := NewDispatcher(cleaner)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		dispatcher.Run(ctx, 2*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for cleaner.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if cleaner.calls.Load() < 2 {
		t.Fatalf("calls=%d", cleaner.calls.Load())
	}
}
