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
	UpdateRepositoryStatus(context.Context, string, string) error
}

type Service struct {
	store Storage
	now   func() time.Time
	poll  time.Duration
}

const assignmentClaimLifetime = 5 * time.Minute

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
	if !task.Enabled {
		return store.RunRecord{}, errors.New("Agent task is disabled")
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
	lease := store.AgentLease{ID: leaseID, AgentID: target.AgentID, TaskID: taskID, Engine: string(task.EffectiveEngine()), Definition: json.RawMessage(`{}`), ExpiresAt: started.Add(assignmentClaimLifetime)}
	if err := s.store.CreateAgentLease(ctx, lease); err != nil {
		return record, err
	}
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.store.ExpireAgentLease(context.Background(), leaseID, ctx.Err().Error(), s.now().UTC())
			return s.finish(record, agentprotocol.Result{Status: "failed", Error: ctx.Err().Error()}, ctx.Err(), task)
		case <-ticker.C:
			current, err := s.store.AgentLeaseStatus(ctx, leaseID)
			if err != nil {
				return record, err
			}
			if current.CompletedAt == nil {
				if !current.ExpiresAt.After(s.now().UTC()) {
					reason := "agent did not claim assignment before it expired"
					if current.AcknowledgedAt != nil {
						reason = "agent stopped renewing assignment progress"
					}
					_ = s.store.ExpireAgentLease(context.Background(), leaseID, reason, s.now().UTC())
					return s.finish(record, agentprotocol.Result{Status: "failed", Error: reason}, errors.New(reason), task)
				}
				continue
			}
			var result agentprotocol.Result
			if err := json.Unmarshal(current.Result, &result); err != nil {
				return s.finish(record, agentprotocol.Result{Status: "failed", Error: "invalid agent result"}, err, task)
			}
			var runErr error
			if result.Status == "failed" {
				if result.Error == "" {
					runErr = errors.New("Agent task failed")
				} else {
					runErr = errors.New(result.Error)
				}
			}
			return s.finish(record, result, runErr, task)
		}
	}
}

func (s *Service) finish(record store.RunRecord, result agentprotocol.Result, runErr error, task domain.Task) (store.RunRecord, error) {
	finished := s.now().UTC()
	if protectionErr := s.applyRepositoryProtectionState(task, result); protectionErr != nil {
		result.Status = "failed"
		runErr = errors.Join(runErr, protectionErr)
	}
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
	if task.ScopeConfirmation.Present() {
		record.Summary["scopeConfirmation"] = task.ScopeConfirmation
	}
	finishErr := s.store.FinishRun(context.Background(), record.ID, record.Status, finished, 1, record.SnapshotID, record.Summary, record.RawLog)
	return record, errors.Join(runErr, finishErr)
}

func (s *Service) applyRepositoryProtectionState(task domain.Task, result agentprotocol.Result) error {
	if task.EffectiveEngine() != domain.ResticEngine || task.RepositoryID == "" || result.Summary == nil {
		return nil
	}
	if snapshotID, ok := result.Summary["unprotectedPartialSnapshot"].(string); ok {
		if snapshotID == "" || snapshotID != result.SnapshotID {
			return errors.New("Agent returned invalid partial snapshot protection state")
		}
		return s.store.UpdateRepositoryStatus(context.Background(), task.RepositoryID, "unprotected-partial:"+snapshotID)
	}
	if snapshotID, ok := result.Summary["pendingPartialProtected"].(string); ok {
		if snapshotID == "" {
			return errors.New("Agent returned invalid pending partial snapshot protection state")
		}
		return s.store.UpdateRepositoryStatus(context.Background(), task.RepositoryID, "ready")
	}
	return nil
}
