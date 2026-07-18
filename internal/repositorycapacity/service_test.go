package repositorycapacity

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/store"
)

type serviceStorage struct {
	execution store.RepositoryExecution
	tasks     []domain.Task
	agents    []store.AgentRecord
	lease     store.AgentLease
	saved     domain.RepositoryCapacity
}

func (s *serviceStorage) LoadRepositoryExecution(context.Context, string) (store.RepositoryExecution, error) {
	return s.execution, nil
}
func (s *serviceStorage) ListTasks(context.Context) ([]domain.Task, error) { return s.tasks, nil }
func (s *serviceStorage) ListAgents(context.Context) ([]store.AgentRecord, error) {
	return s.agents, nil
}
func (s *serviceStorage) CreateAgentLease(_ context.Context, lease store.AgentLease) error {
	lease.CompletedAt, lease.Result = s.lease.CompletedAt, s.lease.Result
	s.lease = lease
	return nil
}
func (s *serviceStorage) AgentLeaseStatus(context.Context, string) (store.AgentLease, error) {
	return s.lease, nil
}
func (s *serviceStorage) ExpireAgentLease(context.Context, string, string, time.Time) error {
	return nil
}
func (s *serviceStorage) SaveRepositoryCapacity(_ context.Context, _ string, capacity domain.RepositoryCapacity) error {
	s.saved = capacity
	return nil
}

type serviceSecrets map[string][]byte

func (s serviceSecrets) Get(_ context.Context, id, _ string) ([]byte, error) { return s[id], nil }

func TestServiceProbesLocalExecutionAndPersistsCapacity(t *testing.T) {
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	storage := &serviceStorage{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/repo"}}}
	probe := &fakeProbe{capacity: Capacity{TotalBytes: 2000, AvailableBytes: 750}}
	result, err := NewService(storage, nil, probe, func() time.Time { return now }).Probe(context.Background(), "repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.UsedBytes != 1250 || result.SourceAgentID != "" || storage.saved.AvailableBytes != 750 || probe.definition.Kind != "local" {
		t.Fatalf("result=%+v saved=%+v definition=%+v", result, storage.saved, probe.definition)
	}
}

func TestServiceUsesBoundAgentAndPersistsReturnedCapacity(t *testing.T) {
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	completed := now.Add(time.Second)
	resultJSON, _ := json.Marshal(agentprotocol.Result{Version: 1, AssignmentID: "lease", AgentID: "agent-a", Status: "succeeded", Summary: map[string]any{"totalBytes": 4000, "availableBytes": 1000}})
	storage := &serviceStorage{
		execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.SFTPRepository, Path: "/repo"}},
		tasks:     []domain.Task{{ID: "task", RepositoryID: "repo", ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-a"}}},
		agents:    []store.AgentRecord{{ID: "agent-a", Status: "online", Capabilities: []string{string(Kind)}}},
		lease:     store.AgentLease{ID: "lease", AgentID: "agent-a", CompletedAt: &completed, Result: resultJSON},
	}
	service := NewService(storage, serviceSecrets{}, &fakeProbe{}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	capacity, err := service.Probe(context.Background(), "repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if storage.lease.Engine != string(Kind) || storage.lease.TaskID != "task" || capacity.SourceAgentID != "agent-a" || storage.saved.TotalBytes != 4000 {
		t.Fatalf("lease=%+v capacity=%+v saved=%+v", storage.lease, capacity, storage.saved)
	}
}
