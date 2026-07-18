package agenttask

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	runcontrol "github.com/maboo-run/shadoc/internal/run"
	"github.com/maboo-run/shadoc/internal/store"
)

type Storage interface {
	ListTasks(context.Context) ([]domain.Task, error)
	CreateAgentLease(context.Context, store.AgentLease) error
	AgentLeaseStatus(context.Context, string) (store.AgentLease, error)
	ExpireAgentLease(context.Context, string, string, time.Time) error
	StartRun(context.Context, store.RunRecord) error
	FinishRun(context.Context, string, string, time.Time, int, string, map[string]any, string) error
}

type Service struct {
	store Storage
	now   func() time.Time
	poll  time.Duration
}

func New(storage Storage, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: storage, now: now, poll: 250 * time.Millisecond}
}

func (s *Service) Run(ctx context.Context, taskID, planID, trigger string) (store.RunRecord, error) {
	tasks, err := s.store.ListTasks(ctx)
	if err != nil {
		return store.RunRecord{}, err
	}
	var task domain.Task
	for _, candidate := range tasks {
		if candidate.ID == taskID {
			task = candidate
			break
		}
	}
	if task.ID == "" {
		return store.RunRecord{}, sql.ErrNoRows
	}
	target := task.EffectiveExecutionTarget()
	if target.AgentID == "" {
		return store.RunRecord{}, errors.New("agent task has no target agent")
	}
	started := s.now().UTC()
	record := store.RunRecord{ID: fmt.Sprintf("run_%d", started.UnixNano()), TaskID: taskID, PlanID: planID, Trigger: trigger, Status: "running", StartedAt: started}
	if err := s.store.StartRun(ctx, record); err != nil {
		return store.RunRecord{}, err
	}
	leaseID := fmt.Sprintf("lease_%d", started.UnixNano())
	lease := store.AgentLease{ID: leaseID, AgentID: target.AgentID, TaskID: taskID, Engine: string(task.EffectiveEngine()), Definition: json.RawMessage(`{}`), ExpiresAt: started.Add(30 * time.Minute)}
	if err := s.store.CreateAgentLease(ctx, lease); err != nil {
		return record, err
	}
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.store.ExpireAgentLease(context.Background(), leaseID, ctx.Err().Error(), s.now().UTC())
			return s.finish(record, agentprotocol.Result{Status: "failed", Error: ctx.Err().Error()}, ctx.Err(), task.ScopeConfirmation)
		case <-ticker.C:
			current, err := s.store.AgentLeaseStatus(ctx, leaseID)
			if err != nil {
				return record, err
			}
			if current.CompletedAt == nil {
				if !current.ExpiresAt.After(s.now().UTC()) {
					_ = s.store.ExpireAgentLease(context.Background(), leaseID, "agent assignment expired", s.now().UTC())
					return s.finish(record, agentprotocol.Result{Status: "failed", Error: "agent assignment expired"}, errors.New("agent assignment expired"), task.ScopeConfirmation)
				}
				continue
			}
			var result agentprotocol.Result
			if err := json.Unmarshal(current.Result, &result); err != nil {
				return s.finish(record, agentprotocol.Result{Status: "failed", Error: "invalid agent result"}, err, task.ScopeConfirmation)
			}
			var runErr error
			if result.Status == "failed" {
				runErr = errors.New(result.Error)
			}
			return s.finish(record, result, runErr, task.ScopeConfirmation)
		}
	}
}

func (s *Service) finish(record store.RunRecord, result agentprotocol.Result, runErr error, confirmation domain.TaskScopeConfirmation) (store.RunRecord, error) {
	finished := s.now().UTC()
	status, validStatus := runcontrol.NormalizeTerminalStatus(result.Status)
	record.Status, record.SnapshotID, record.Summary, record.RawLog, record.AttemptCount, record.FinishedAt = string(status), result.SnapshotID, result.Summary, result.RawLog, 1, &finished
	if !validStatus && runErr == nil {
		runErr = fmt.Errorf("Agent returned unsupported terminal status %q", result.Status)
	}
	if record.Summary == nil {
		record.Summary = map[string]any{}
	}
	if result.Error != "" {
		record.Summary["error"] = result.Error
	}
	if confirmation.Present() {
		record.Summary["scopeConfirmation"] = confirmation
	}
	finishErr := s.store.FinishRun(context.Background(), record.ID, record.Status, finished, 1, record.SnapshotID, record.Summary, record.RawLog)
	return record, errors.Join(runErr, finishErr)
}
