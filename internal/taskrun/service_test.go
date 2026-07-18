package taskrun

import (
	"context"
	"errors"
	"testing"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestServiceRoutesLegacyResticTaskToLocalRunner(t *testing.T) {
	runner := &recordingRunner{record: store.RunRecord{ID: "run-1", Status: "success"}}
	service := New(fakeTaskSource{tasks: []domain.Task{{ID: "task-1", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo-1", Directory: &domain.DirectorySource{Path: "/source"}, Enabled: true}}}, map[execution.EngineKind]Runner{execution.EngineKind(domain.ResticEngine): runner})

	record, err := service.Run(context.Background(), "task-1", "plan-1", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != "run-1" || runner.taskID != "task-1" || runner.planID != "plan-1" || runner.trigger != "manual" {
		t.Fatalf("record=%+v runner=%+v", record, runner)
	}
}

func TestServiceRejectsAgentTaskUntilAgentTargetExists(t *testing.T) {
	task := domain.Task{ID: "task-1", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo-1", Directory: &domain.DirectorySource{Path: "/source"}, ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"}, Enabled: true}
	service := New(fakeTaskSource{tasks: []domain.Task{task}}, nil)

	if _, err := service.Run(context.Background(), task.ID, "", "manual"); err == nil {
		t.Fatal("agent task was accepted before an agent target exists")
	}
}

func TestServiceRoutesAgentTaskToGenericAgentRunner(t *testing.T) {
	task := domain.Task{ID: "task-1", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo-1", Directory: &domain.DirectorySource{Path: "/source"}, ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"}, Enabled: true}
	runner := &recordingRunner{record: store.RunRecord{ID: "remote-run"}}
	service := New(fakeTaskSource{tasks: []domain.Task{task}}, nil)
	service.SetAgentRunner(runner)
	record, err := service.Run(context.Background(), task.ID, "", "manual")
	if err != nil || record.ID != "remote-run" || runner.taskID != task.ID {
		t.Fatalf("record=%+v runner=%+v err=%v", record, runner, err)
	}
}

func TestServiceObservesEveryExecutionTargetWithoutChangingRunnerResult(t *testing.T) {
	tests := []struct {
		name   string
		target execution.Target
	}{
		{name: "local", target: execution.Target{Kind: execution.Local}},
		{name: "agent", target: execution.Target{Kind: execution.Agent, AgentID: "agent-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task := domain.Task{ID: "task-1", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo-1", Directory: &domain.DirectorySource{Path: "/source"}, ExecutionTarget: test.target, Enabled: true}
			runErr := errors.New("engine failed")
			runner := &recordingRunner{record: store.RunRecord{ID: "run-1", TaskID: task.ID, Status: "failed"}, err: runErr}
			observer := &recordingObserver{err: errors.New("observer failed")}
			service := New(fakeTaskSource{tasks: []domain.Task{task}}, map[execution.EngineKind]Runner{execution.EngineKind(domain.ResticEngine): runner})
			if test.target.Kind == execution.Agent {
				service.SetAgentRunner(runner)
			}
			service.SetObserver(observer)
			record, err := service.Run(context.Background(), task.ID, "", "manual")
			if !errors.Is(err, runErr) || record.ID != "run-1" {
				t.Fatalf("record=%+v err=%v", record, err)
			}
			if observer.calls != 1 || observer.task.ID != task.ID || observer.record.ID != record.ID {
				t.Fatalf("observer=%+v", observer)
			}
		})
	}
}

func TestServiceNotifiesAllPostRunObservers(t *testing.T) {
	task := domain.Task{ID: "task-1", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo-1", Directory: &domain.DirectorySource{Path: "/source"}, Enabled: true}
	runner := &recordingRunner{record: store.RunRecord{ID: "run-1", TaskID: task.ID, Status: "success"}}
	first, second := &recordingObserver{}, &recordingObserver{}
	service := New(fakeTaskSource{tasks: []domain.Task{task}}, map[execution.EngineKind]Runner{execution.EngineKind(domain.ResticEngine): runner})
	service.SetObserver(first)
	service.AddObserver(second)
	if _, err := service.Run(context.Background(), task.ID, "", "manual"); err != nil {
		t.Fatal(err)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("observer calls first=%d second=%d", first.calls, second.calls)
	}
}

type fakeTaskSource struct{ tasks []domain.Task }

func (f fakeTaskSource) ListTasks(context.Context) ([]domain.Task, error) { return f.tasks, nil }

type recordingRunner struct {
	taskID, planID, trigger string
	record                  store.RunRecord
	err                     error
}

func (r *recordingRunner) Run(_ context.Context, taskID, planID, trigger string) (store.RunRecord, error) {
	r.taskID, r.planID, r.trigger = taskID, planID, trigger
	return r.record, r.err
}

type recordingObserver struct {
	task   domain.Task
	record store.RunRecord
	calls  int
	err    error
}

func (o *recordingObserver) ObserveRun(_ context.Context, task domain.Task, record store.RunRecord) error {
	o.task, o.record = task, record
	o.calls++
	return o.err
}
