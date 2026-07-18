package repolock

import (
	"context"
	"sync"
)

type Registry struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func New() *Registry { return &Registry{locks: make(map[string]chan struct{})} }
func (r *Registry) With(ctx context.Context, id string, operation func() error) error {
	r.mu.Lock()
	lock := r.locks[id]
	if lock == nil {
		lock = make(chan struct{}, 1)
		r.locks[id] = lock
	}
	r.mu.Unlock()
	select {
	case lock <- struct{}{}:
		defer func() { <-lock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return operation()
}
