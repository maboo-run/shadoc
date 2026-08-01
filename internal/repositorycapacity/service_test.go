package repositorycapacity

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentfilesystem"
	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/store"
)

type serviceStorage struct {
	execution         store.RepositoryExecution
	tasks             []domain.Task
	agents            []store.AgentRecord
	lease             store.AgentLease
	filesystemRequest store.AgentFilesystemRequest
	filesystemResult  json.RawMessage
	saved             domain.RepositoryCapacity
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
func (s *serviceStorage) CreateAgentFilesystemRequest(_ context.Context, request store.AgentFilesystemRequest) error {
	request.CompletedAt = s.filesystemRequest.CompletedAt
	request.Result = s.filesystemRequest.Result
	s.filesystemRequest = request
	return nil
}
func (s *serviceStorage) AgentFilesystemRequestStatus(_ context.Context, _ string) (store.AgentFilesystemRequest, error) {
	return s.filesystemRequest, nil
}
func (s *serviceStorage) ExpireAgentFilesystemRequest(context.Context, string, string, time.Time) error {
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
		agents:    []store.AgentRecord{{ID: "agent-a", Status: "online", LastHeartbeatAt: &now, Capabilities: []string{string(Kind)}}},
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

func TestServiceUsesMatchingAgentLocalPathForRemoteRsync(t *testing.T) {
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	completed := now.Add(time.Second)
	resultJSON, _ := json.Marshal(agentprotocol.Result{Version: 1, AssignmentID: "request", AgentID: "agent-a", Status: "succeeded", Summary: map[string]any{"totalBytes": 8000, "availableBytes": 3000}})
	storage := &serviceStorage{
		execution:         store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Engine: domain.RsyncEngine, Kind: domain.SSHRepository, RemoteHostID: "host-a", Path: "/srv/sync"}},
		agents:            []store.AgentRecord{{ID: "agent-a", RemoteHostID: "host-a", Status: "online", LastHeartbeatAt: &now, Capabilities: []string{agentfilesystem.CapacityCapability}}},
		filesystemRequest: store.AgentFilesystemRequest{CompletedAt: &completed, Result: resultJSON},
	}
	service := NewService(storage, serviceSecrets{}, &fakeProbe{}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	capacity, err := service.Probe(context.Background(), "repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if storage.filesystemRequest.AgentID != "agent-a" || capacity.SourceAgentID != "agent-a" || storage.saved.AvailableBytes != 3000 {
		t.Fatalf("request=%+v capacity=%+v saved=%+v", storage.filesystemRequest, capacity, storage.saved)
	}
	var definition map[string]any
	if err := json.Unmarshal(storage.filesystemRequest.Definition, &definition); err != nil || definition["operation"] != "capacity" || definition["path"] != "/srv/sync" {
		t.Fatalf("definition=%s err=%v", storage.filesystemRequest.Definition, err)
	}
}

func TestServiceUsesPersistedOwnerForAgentLocalRepositoryCapacity(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	completed := now.Add(time.Second)
	resultJSON, _ := json.Marshal(agentprotocol.Result{Version: 1, AssignmentID: "request", AgentID: "agent-a", Status: "succeeded", Summary: map[string]any{"totalBytes": 9000, "availableBytes": 4000}})
	storage := &serviceStorage{
		execution: store.RepositoryExecution{Repository: domain.Repository{
			ID: "repo", Engine: domain.RsyncEngine, Kind: domain.LocalRepository, Path: "/srv/archive",
			LocalTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-a"},
		}},
		agents:            []store.AgentRecord{{ID: "agent-a", Status: "online", LastHeartbeatAt: &now, Capabilities: []string{agentfilesystem.CapacityCapability}}},
		filesystemRequest: store.AgentFilesystemRequest{CompletedAt: &completed, Result: resultJSON},
	}
	service := NewService(storage, nil, &fakeProbe{}, func() time.Time { return now })
	service.pollInterval = time.Millisecond
	capacity, err := service.Probe(context.Background(), "repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if storage.filesystemRequest.AgentID != "agent-a" || capacity.SourceAgentID != "agent-a" || storage.saved.AvailableBytes != 4000 {
		t.Fatalf("request=%+v capacity=%+v saved=%+v", storage.filesystemRequest, capacity, storage.saved)
	}
}

func TestResolveRemoteRsyncCapacityAgentReportsBindingAndCapabilityState(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	stale := now.Add(-3 * time.Minute)
	repository := domain.Repository{ID: "repo", Engine: domain.RsyncEngine, Kind: domain.SSHRepository, RemoteHostID: "host-a", Path: "/srv/sync"}
	for name, test := range map[string]struct {
		agents []store.AgentRecord
		want   RemoteRsyncCapacityAgentStatus
		id     string
	}{
		"unbound": {
			agents: []store.AgentRecord{{ID: "agent-a", Status: "online", LastHeartbeatAt: &now, Capabilities: []string{agentfilesystem.CapacityCapability}}},
			want:   RemoteRsyncCapacityAgentUnbound,
		},
		"offline": {
			agents: []store.AgentRecord{{ID: "agent-a", RemoteHostID: "host-a", Status: "online", LastHeartbeatAt: &stale, Capabilities: []string{agentfilesystem.CapacityCapability}}},
			want:   RemoteRsyncCapacityAgentOffline,
			id:     "agent-a",
		},
		"missing capacity capability": {
			agents: []store.AgentRecord{{ID: "agent-a", RemoteHostID: "host-a", Status: "online", LastHeartbeatAt: &now, Capabilities: []string{"repository-capacity"}}},
			want:   RemoteRsyncCapacityAgentMissingCapability,
			id:     "agent-a",
		},
		"available": {
			agents: []store.AgentRecord{{ID: "agent-a", RemoteHostID: "host-a", Status: "online", LastHeartbeatAt: &now, Capabilities: []string{agentfilesystem.CapacityCapability}}},
			want:   RemoteRsyncCapacityAgentAvailable,
			id:     "agent-a",
		},
	} {
		t.Run(name, func(t *testing.T) {
			resolved := ResolveRemoteRsyncCapacityAgent(repository, test.agents, now)
			if resolved.Status != test.want || resolved.AgentID != test.id {
				t.Fatalf("resolved=%+v want status=%q agent=%q", resolved, test.want, test.id)
			}
		})
	}
}

func TestServiceDoesNotUseLegacySFTPCapacityForRemoteRsync(t *testing.T) {
	storage := &serviceStorage{
		execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Engine: domain.RsyncEngine, Kind: domain.SSHRepository, RemoteHostID: "host-a", Path: "/srv/sync"}},
		agents:    []store.AgentRecord{{ID: "old-agent", RemoteHostID: "host-a", Status: "online", Capabilities: []string{string(Kind)}}},
	}
	probe := &fakeProbe{}
	_, err := NewService(storage, serviceSecrets{"ssh": []byte("must-not-be-read")}, probe, time.Now).Probe(context.Background(), "repo", nil)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err=%v", err)
	}
	if probe.definition.Kind != "" {
		t.Fatalf("unexpected fallback probe definition=%+v", probe.definition)
	}
}
