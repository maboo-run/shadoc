package run

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Status string

const (
	Succeeded Status = "success"
	Partial   Status = "partial"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
	Skipped   Status = "skipped"
)

// ParseTerminalStatus accepts only the product's canonical persisted terminal
// vocabulary. Protocol and tool adapters must normalize their own aliases
// before crossing the persistence boundary.
func ParseTerminalStatus(value string) (Status, bool) {
	status := Status(value)
	switch status {
	case Succeeded, Partial, Failed, Cancelled, Skipped:
		return status, true
	default:
		return Failed, false
	}
}

// NormalizeTerminalStatus translates known external protocol spellings into
// the product vocabulary. The boolean is false for an empty or unknown value;
// callers should persist Failed and retain a diagnostic in that case.
func NormalizeTerminalStatus(value string) (Status, bool) {
	switch value {
	case "succeeded":
		return Succeeded, true
	case "canceled":
		return Cancelled, true
	default:
		return ParseTerminalStatus(value)
	}
}

type Request struct {
	TaskID       string
	RepositoryID string
	MaxAttempts  int
	RetryDelay   time.Duration
}

type Result struct {
	Status   Status
	Attempts int
	Error    error
}

type Operation func(context.Context) (Status, error)

type Controller struct {
	mu           sync.Mutex
	runningTasks map[string]bool
	repositories map[string]*sync.Mutex
	global       chan struct{}
}

func NewController(maxParallel int) *Controller {
	if maxParallel < 1 {
		maxParallel = 1
	}
	return &Controller{
		runningTasks: make(map[string]bool),
		repositories: make(map[string]*sync.Mutex),
		global:       make(chan struct{}, maxParallel),
	}
}

func (c *Controller) Execute(ctx context.Context, request Request, operation Operation) Result {
	if request.TaskID == "" || request.RepositoryID == "" || operation == nil {
		return Result{Status: Failed, Error: errors.New("task, repository and operation are required")}
	}
	c.mu.Lock()
	if c.runningTasks[request.TaskID] {
		c.mu.Unlock()
		return Result{Status: Skipped}
	}
	c.runningTasks[request.TaskID] = true
	repositoryLock := c.repositories[request.RepositoryID]
	if repositoryLock == nil {
		repositoryLock = &sync.Mutex{}
		c.repositories[request.RepositoryID] = repositoryLock
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.runningTasks, request.TaskID)
		c.mu.Unlock()
	}()

	select {
	case c.global <- struct{}{}:
		defer func() { <-c.global }()
	case <-ctx.Done():
		return Result{Status: Cancelled, Error: ctx.Err()}
	}
	repositoryLock.Lock()
	defer repositoryLock.Unlock()

	maxAttempts := request.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	delay := request.RetryDelay
	if delay <= 0 {
		delay = time.Second
	}
	var result Result
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		status, err := operation(ctx)
		result = Result{Status: status, Attempts: attempt, Error: err}
		if err == nil {
			return result
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			result.Status = Cancelled
			return result
		}
		if !IsTemporary(err) || attempt == maxAttempts {
			result.Status = Failed
			return result
		}
		timer := time.NewTimer(delay * time.Duration(1<<(attempt-1)))
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return Result{Status: Cancelled, Attempts: attempt, Error: ctx.Err()}
		}
	}
	return result
}

type temporaryError struct{ error }

func (temporaryError) Temporary() bool { return true }

func Temporary(err error) error {
	if err == nil {
		return nil
	}
	return temporaryError{err}
}

func IsTemporary(err error) bool {
	var temporary interface{ Temporary() bool }
	return errors.As(err, &temporary) && temporary.Temporary()
}
