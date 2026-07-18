package agentrestore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

type serviceStorage struct {
	agent      store.AgentRecord
	repository domain.Repository
	fs         store.AgentFilesystemRequest
	restore    store.AgentRestoreRequest
}

func (s *serviceStorage) LoadRepositoryExecution(context.Context, string) (store.RepositoryExecution, error) {
	return store.RepositoryExecution{Repository: s.repository}, nil
}

type recordingLocker struct {
	repositoryID string
}

func (l *recordingLocker) With(_ context.Context, repositoryID string, operation func() error) error {
	l.repositoryID = repositoryID
	return operation()
}

func (s *serviceStorage) ListAgents(context.Context) ([]store.AgentRecord, error) {
	return []store.AgentRecord{s.agent}, nil
}
func (s *serviceStorage) CreateAgentFilesystemRequest(_ context.Context, request store.AgentFilesystemRequest) error {
	s.fs = request
	completed := request.CreatedAt
	result, _ := json.Marshal(agentprotocol.Result{Version: 1, AssignmentID: request.ID, AgentID: request.AgentID, Status: "succeeded"})
	s.fs.Status, s.fs.Result, s.fs.CompletedAt = "succeeded", result, &completed
	return nil
}
func (s *serviceStorage) AgentFilesystemRequestStatus(context.Context, string) (store.AgentFilesystemRequest, error) {
	return s.fs, nil
}
func (s *serviceStorage) CreateAgentRestoreRequest(_ context.Context, request store.AgentRestoreRequest) error {
	s.restore = request
	completed := request.CreatedAt
	result, _ := json.Marshal(agentprotocol.Result{Version: 1, AssignmentID: request.ID, AgentID: request.AgentID, Status: "succeeded"})
	s.restore.Status, s.restore.Result, s.restore.CompletedAt = "succeeded", result, &completed
	return nil
}
func (s *serviceStorage) AgentRestoreRequestStatus(context.Context, string) (store.AgentRestoreRequest, error) {
	return s.restore, nil
}
func (*serviceStorage) ExpireAgentRestoreRequest(context.Context, string, string, time.Time) error {
	return nil
}

func TestServicePreflightsAndQueuesSecretFreeAgentRestore(t *testing.T) {
	now := time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC)
	heartbeat := now
	storage := &serviceStorage{agent: store.AgentRecord{ID: "agent-1", Status: "online", LastHeartbeatAt: &heartbeat, Capabilities: []string{"restic-restore", "filesystem-restore-target"}}, repository: domain.Repository{ID: "repo-1", Engine: domain.ResticEngine, Kind: domain.SFTPRepository}}
	service := NewService(storage, func() time.Time { return now })
	locker := &recordingLocker{}
	service.SetLocker(locker)
	if err := service.PreflightTarget(t.Context(), "agent-1", "repo-1", "/srv/restored"); err != nil {
		t.Fatal(err)
	}
	if err := service.Restore(t.Context(), Request{AgentID: "agent-1", RepositoryID: "repo-1", SnapshotID: "snap", SourcePath: "/srv/photos", Target: "/srv/restored", Includes: []string{"one.jpg"}}); err != nil {
		t.Fatal(err)
	}
	var definition Definition
	if err := json.Unmarshal(storage.restore.Definition, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.RepositoryID != "repo-1" || definition.Repository.Password != "" || definition.Target != "/srv/restored" {
		t.Fatalf("persisted definition=%+v", definition)
	}
	if locker.repositoryID != "repo-1" {
		t.Fatalf("restore lock repository=%q", locker.repositoryID)
	}
}

func TestServiceRejectsAgentRestoreFromServiceLocalRepository(t *testing.T) {
	now := time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC)
	heartbeat := now
	storage := &serviceStorage{agent: store.AgentRecord{ID: "agent-1", Status: "online", LastHeartbeatAt: &heartbeat, Capabilities: []string{"restic-restore", "filesystem-restore-target"}}, repository: domain.Repository{ID: "repo-1", Engine: domain.ResticEngine, Kind: domain.LocalRepository}}
	service := NewService(storage, func() time.Time { return now })
	if err := service.PreflightTarget(t.Context(), "agent-1", "repo-1", "/srv/restored"); err == nil {
		t.Fatal("Service-local repository was accepted for Agent restore")
	}
}
