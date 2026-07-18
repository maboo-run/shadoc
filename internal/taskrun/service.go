// Package taskrun selects a task engine for its configured execution target.
package taskrun

import (
	"context"
	"errors"
	"fmt"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/store"
)

type TaskSource interface {
	ListTasks(context.Context) ([]domain.Task, error)
}

type Runner interface {
	Run(context.Context, string, string, string) (store.RunRecord, error)
}

type Observer interface {
	ObserveRun(context.Context, domain.Task, store.RunRecord) error
}

type Service struct {
	tasks     TaskSource
	locals    map[execution.EngineKind]Runner
	agent     Runner
	observers []Observer
}

func (s *Service) SetAgentRunner(runner Runner) { s.agent = runner }
func (s *Service) SetObserver(observer Observer) {
	s.observers = nil
	if observer != nil {
		s.observers = append(s.observers, observer)
	}
}
func (s *Service) AddObserver(observer Observer) {
	if observer != nil {
		s.observers = append(s.observers, observer)
	}
}

func New(tasks TaskSource, locals map[execution.EngineKind]Runner) *Service {
	registered := make(map[execution.EngineKind]Runner, len(locals))
	for kind, runner := range locals {
		if runner == nil {
			panic("task runner cannot register a nil local engine")
		}
		registered[kind] = runner
	}
	return &Service{tasks: tasks, locals: registered}
}

func (s *Service) Run(ctx context.Context, taskID, planID, trigger string) (store.RunRecord, error) {
	if s.tasks == nil {
		return store.RunRecord{}, errors.New("task source is required")
	}
	tasks, err := s.tasks.ListTasks(ctx)
	if err != nil {
		return store.RunRecord{}, err
	}
	for _, task := range tasks {
		if task.ID != taskID {
			continue
		}
		target := task.EffectiveExecutionTarget()
		var runner Runner
		if target.Kind == execution.Agent {
			if s.agent == nil {
				return store.RunRecord{}, fmt.Errorf("execution target %q is not available", target.Kind)
			}
			runner = s.agent
		} else if target.Kind != execution.Local {
			return store.RunRecord{}, fmt.Errorf("execution target %q is not available", target.Kind)
		} else {
			var ok bool
			runner, ok = s.locals[execution.EngineKind(task.EffectiveEngine())]
			if !ok {
				return store.RunRecord{}, fmt.Errorf("local engine %q is not available", task.EffectiveEngine())
			}
		}
		record, runErr := runner.Run(ctx, taskID, planID, trigger)
		if record.ID != "" {
			for _, observer := range s.observers {
				_ = observer.ObserveRun(context.WithoutCancel(ctx), task, record)
			}
		}
		return record, runErr
	}
	return store.RunRecord{}, fmt.Errorf("task %q was not found", taskID)
}
