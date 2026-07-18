package taskpreview

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
	"github.com/maboo-run/shadoc/internal/rsync"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestServicePreviewsLocalDirectoryAndPersistsFingerprint(t *testing.T) {
	now := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "task-1", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/srv/photos", Exclusions: []string{}}, Enabled: false}
	storage := &previewStorage{tasks: []domain.Task{task}}
	scope := &previewEngine{kind: agentfilesystem.Kind, outcome: execution.Outcome{Status: "succeeded", Summary: map[string]any{"includedFiles": 12, "excludedFiles": 0, "truncated": false}}}
	service := New(storage, previewSecrets{}, scope, nil, func() time.Time { return now })

	preview, err := service.Preview(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint, _ := Fingerprint(task)
	if preview.Fingerprint != wantFingerprint || preview.TaskID != task.ID || preview.Summary["includedFiles"] != 12 || preview.RequiresDeleteConfirmation || !preview.ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("preview=%+v", preview)
	}
	var definition agentfilesystem.Definition
	if err := json.Unmarshal(scope.assignment.Definition, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Operation != agentfilesystem.PreviewScope || definition.Path != "/srv/photos" || definition.Limit != agentfilesystem.MaxScopeItems || len(definition.Exclusions) != 0 {
		t.Fatalf("definition=%+v", definition)
	}
	if storage.preview.ID != preview.ID {
		t.Fatalf("stored=%+v", storage.preview)
	}
}

func TestServiceRunsLocalRsyncDeleteDryRunAndMergesSafetySummary(t *testing.T) {
	now := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "task-1", Name: "mirror", Engine: domain.RsyncEngine, Kind: domain.RsyncTask, RepositoryID: "repo", Rsync: &domain.RsyncSource{Path: "/srv/photos", Exclusions: []string{}, Delete: true}}
	storage := &previewStorage{
		tasks:          []domain.Task{task},
		rsyncExecution: store.RsyncExecution{Task: task, Repository: domain.Repository{ID: "repo", Kind: domain.SFTPRepository, Path: "/backup/photos"}, Host: domain.RemoteHost{Host: "backup.example", Port: 22, Username: "backup", HostFingerprint: "known"}, PrivateKeySecretID: "key"},
	}
	scope := &previewEngine{kind: agentfilesystem.Kind, outcome: execution.Outcome{Status: "succeeded", Summary: map[string]any{"includedFiles": 10, "truncated": false}}}
	dryRun := &previewEngine{kind: "rsync", run: func(assignment execution.Assignment) (execution.Outcome, error) {
		var definition rsync.Definition
		if err := json.Unmarshal(assignment.Definition, &definition); err != nil {
			t.Fatal(err)
		}
		if !definition.DryRun || !definition.Delete || definition.Destination.PrivateKey != "private-key" {
			t.Fatalf("definition=%+v", definition)
		}
		return execution.Outcome{Status: "succeeded", Summary: map[string]any{"deleteFiles": 2, "deleteDirectories": 1, "targetIdentity": "ssh://backup@backup.example:22/backup/photos", "truncated": false}}, nil
	}}
	service := New(storage, previewSecrets{"key": []byte("private-key")}, scope, dryRun, func() time.Time { return now })
	preview, err := service.Preview(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.RequiresDeleteConfirmation || preview.Summary["deleteFiles"] != 2 || preview.Summary["deleteDirectories"] != 1 || preview.Summary["targetIdentity"] == "" {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestServiceUsesDeclarativeAgentScopeRequest(t *testing.T) {
	now := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	heartbeat := now
	task := domain.Task{ID: "task-1", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo", ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"}, Directory: &domain.DirectorySource{Path: "/srv/photos"}}
	result, _ := json.Marshal(agentprotocol.Result{Version: agentprotocol.Version, Status: "succeeded", Summary: map[string]any{"includedFiles": 8, "truncated": false}})
	completed := now
	storage := &previewStorage{
		tasks: []domain.Task{task}, agents: []store.AgentRecord{{ID: "agent-1", Status: "online", LastHeartbeatAt: &heartbeat, Capabilities: []string{"filesystem-scope-preview", "restic"}}},
		filesystemStatus: store.AgentFilesystemRequest{Status: "succeeded", Result: result, CompletedAt: &completed},
	}
	service := New(storage, previewSecrets{}, &previewEngine{kind: agentfilesystem.Kind, failIfRun: true}, nil, func() time.Time { return now })
	preview, err := service.Preview(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Summary["includedFiles"] != float64(8) {
		t.Fatalf("preview=%+v", preview)
	}
	var definition agentfilesystem.Definition
	if err := json.Unmarshal(storage.filesystemRequest.Definition, &definition); err != nil {
		t.Fatal(err)
	}
	if storage.filesystemRequest.AgentID != "agent-1" || definition.Operation != agentfilesystem.PreviewScope || definition.Path != "/srv/photos" {
		t.Fatalf("request=%+v definition=%+v", storage.filesystemRequest, definition)
	}
}

func TestServiceStoresOnlyControlMarkerForAgentRsyncDeletePreview(t *testing.T) {
	now := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	heartbeat := now
	task := domain.Task{ID: "task-1", Name: "mirror", Engine: domain.RsyncEngine, Kind: domain.RsyncTask, RepositoryID: "repo", ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"}, Rsync: &domain.RsyncSource{Path: "/srv/photos", Delete: true}}
	result, _ := json.Marshal(agentprotocol.Result{Version: agentprotocol.Version, Status: "succeeded", Summary: map[string]any{"deleteFiles": 3, "deleteDirectories": 0, "targetIdentity": "local:/backup", "truncated": false}})
	completed := now
	storage := &previewStorage{
		tasks: []domain.Task{task}, agents: []store.AgentRecord{{ID: "agent-1", Status: "online", LastHeartbeatAt: &heartbeat, Capabilities: []string{"filesystem-scope-preview", "rsync"}}},
		filesystemStatus: store.AgentFilesystemRequest{Status: "succeeded", Result: mustAgentResult(map[string]any{"includedFiles": 9, "truncated": false}, now), CompletedAt: &completed},
		leaseStatus:      store.AgentLease{Status: "succeeded", Result: result, CompletedAt: &completed},
	}
	service := New(storage, previewSecrets{"key": []byte("must-not-be-read")}, &previewEngine{kind: agentfilesystem.Kind, failIfRun: true}, nil, func() time.Time { return now })
	preview, err := service.Preview(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Summary["deleteFiles"] != float64(3) {
		t.Fatalf("preview=%+v", preview)
	}
	var marker map[string]any
	if err := json.Unmarshal(storage.lease.Definition, &marker); err != nil {
		t.Fatal(err)
	}
	if marker["dryRun"] != true || len(marker) != 1 || string(storage.lease.Definition) == "" {
		t.Fatalf("persisted lease marker=%s", storage.lease.Definition)
	}
}

func TestServiceRejectsUnavailableAgentBeforeCreatingPreview(t *testing.T) {
	now := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "task-1", Kind: domain.DirectoryTask, ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"}, Directory: &domain.DirectorySource{Path: "/srv/photos"}}
	storage := &previewStorage{tasks: []domain.Task{task}, agents: []store.AgentRecord{{ID: "agent-1", Status: "offline"}}}
	service := New(storage, previewSecrets{}, &previewEngine{kind: agentfilesystem.Kind}, nil, func() time.Time { return now })
	if _, err := service.Preview(context.Background(), task.ID); !errors.Is(err, ErrAgentUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if storage.filesystemRequest.ID != "" || storage.preview.ID != "" {
		t.Fatalf("unexpected request=%+v preview=%+v", storage.filesystemRequest, storage.preview)
	}
}

type previewStorage struct {
	tasks             []domain.Task
	agents            []store.AgentRecord
	rsyncExecution    store.RsyncExecution
	preview           store.TaskScopePreview
	filesystemRequest store.AgentFilesystemRequest
	filesystemStatus  store.AgentFilesystemRequest
	lease             store.AgentLease
	leaseStatus       store.AgentLease
}

func (s *previewStorage) ListTasks(context.Context) ([]domain.Task, error) { return s.tasks, nil }
func (s *previewStorage) ListAgents(context.Context) ([]store.AgentRecord, error) {
	return s.agents, nil
}
func (s *previewStorage) LoadRsyncExecution(context.Context, string) (store.RsyncExecution, error) {
	return s.rsyncExecution, nil
}
func (s *previewStorage) CreateTaskScopePreview(_ context.Context, preview store.TaskScopePreview) error {
	s.preview = preview
	return nil
}
func (s *previewStorage) CreateAgentFilesystemRequest(_ context.Context, request store.AgentFilesystemRequest) error {
	s.filesystemRequest = request
	return nil
}
func (s *previewStorage) AgentFilesystemRequestStatus(context.Context, string) (store.AgentFilesystemRequest, error) {
	return s.filesystemStatus, nil
}
func (s *previewStorage) CreateAgentLease(_ context.Context, lease store.AgentLease) error {
	s.lease = lease
	return nil
}
func (s *previewStorage) AgentLeaseStatus(context.Context, string) (store.AgentLease, error) {
	return s.leaseStatus, nil
}
func (s *previewStorage) ExpireAgentLease(context.Context, string, string, time.Time) error {
	return nil
}

type previewSecrets map[string][]byte

func (s previewSecrets) Get(_ context.Context, id, _ string) ([]byte, error) {
	value, ok := s[id]
	if !ok {
		return nil, errors.New("secret unavailable")
	}
	return append([]byte(nil), value...), nil
}

type previewEngine struct {
	kind       execution.EngineKind
	outcome    execution.Outcome
	run        func(execution.Assignment) (execution.Outcome, error)
	assignment execution.Assignment
	failIfRun  bool
}

func (e *previewEngine) Kind() execution.EngineKind   { return e.kind }
func (*previewEngine) Validate(json.RawMessage) error { return nil }
func (e *previewEngine) Run(_ context.Context, assignment execution.Assignment) (execution.Outcome, error) {
	if e.failIfRun {
		return execution.Outcome{}, errors.New("local engine must not run")
	}
	e.assignment = assignment
	if e.run != nil {
		return e.run(assignment)
	}
	return e.outcome, nil
}

func mustAgentResult(summary map[string]any, now time.Time) json.RawMessage {
	encoded, _ := json.Marshal(agentprotocol.Result{Version: agentprotocol.Version, AssignmentID: "request", AgentID: "agent-1", Status: "succeeded", Summary: summary})
	return encoded
}
