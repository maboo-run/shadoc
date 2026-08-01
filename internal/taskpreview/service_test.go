package taskpreview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestServiceInventoriesDraftExclusionsWithoutChangingStoredTask(t *testing.T) {
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	task := domain.Task{
		ID: "task-draft-rules", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo",
		Directory: &domain.DirectorySource{Path: "/srv/photos", Exclusions: []string{"old-rule"}},
	}
	storage := &previewStorage{tasks: []domain.Task{task}}
	scope := &previewEngine{
		kind: agentfilesystem.Kind,
		outcome: execution.Outcome{Status: "succeeded", Summary: map[string]any{
			"includedFiles": 7, "excludedFiles": 3, "truncated": false,
		}},
	}
	service := New(storage, previewSecrets{}, scope, nil, func() time.Time { return now })

	preview, err := service.InventoryWithExclusions(t.Context(), task.ID, []string{"draft-rule"})
	if err != nil {
		t.Fatal(err)
	}
	var definition agentfilesystem.Definition
	if err := json.Unmarshal(scope.assignment.Definition, &definition); err != nil {
		t.Fatal(err)
	}
	if len(definition.Exclusions) != 1 || definition.Exclusions[0] != "draft-rule" {
		t.Fatalf("definition exclusions=%v", definition.Exclusions)
	}
	proposed := task
	proposed.Directory = &domain.DirectorySource{
		Path: task.Directory.Path, Exclusions: []string{"draft-rule"},
		SkipIfUnchanged: task.Directory.SkipIfUnchanged,
	}
	wantFingerprint, _ := Fingerprint(proposed)
	if preview.Fingerprint != wantFingerprint {
		t.Fatalf("fingerprint=%q want=%q", preview.Fingerprint, wantFingerprint)
	}
	if got := storage.tasks[0].Directory.Exclusions; len(got) != 1 || got[0] != "old-rule" {
		t.Fatalf("stored task was changed by preview: %v", got)
	}
}

func TestServiceRejectsInvalidDraftExclusionsBeforeScanning(t *testing.T) {
	task := domain.Task{
		ID: "task-invalid-rules", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo",
		Directory: &domain.DirectorySource{Path: "/srv/photos"},
	}
	for name, exclusions := range map[string][]string{
		"empty":    {""},
		"newline":  {"safe\nunsafe"},
		"too long": {strings.Repeat("x", 1025)},
		"too many": make([]string, 257),
	} {
		t.Run(name, func(t *testing.T) {
			storage := &previewStorage{tasks: []domain.Task{task}}
			scope := &previewEngine{kind: agentfilesystem.Kind, outcome: execution.Outcome{Status: "succeeded"}}
			service := New(storage, previewSecrets{}, scope, nil, time.Now)
			if _, err := service.InventoryWithExclusions(t.Context(), task.ID, exclusions); err == nil {
				t.Fatal("invalid exclusions were accepted")
			}
			if scope.assignment.ID != "" || storage.preview.ID != "" {
				t.Fatalf("invalid exclusions started a scan: assignment=%+v preview=%+v", scope.assignment, storage.preview)
			}
		})
	}
}

func TestServiceRunsLocalRsyncDeleteDryRunAndMergesSafetySummary(t *testing.T) {
	now := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "task-1", Name: "mirror", Engine: domain.RsyncEngine, Kind: domain.RsyncTask, RepositoryID: "repo", Rsync: &domain.RsyncSource{Path: "/srv/photos", Exclusions: []string{}, Delete: true}}
	storage := &previewStorage{
		tasks:          []domain.Task{task},
		rsyncExecution: store.RsyncExecution{Task: task, Repository: domain.Repository{ID: "repo", Engine: domain.RsyncEngine, Kind: domain.SSHRepository, Path: "/backup/photos"}, Host: domain.RemoteHost{Host: "backup.example", Port: 22, Username: "backup", HostFingerprint: "known"}, PrivateKeySecretID: "key"},
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
	if definition.IncludeEntries {
		t.Fatal("ordinary activation preview unexpectedly required a detailed Agent inventory")
	}
	if preview.Summary["entriesAvailable"] != false {
		t.Fatalf("ordinary Agent preview entries=%v", preview.Summary["entriesAvailable"])
	}
}

func TestServiceInventoriesRemoteScopeThroughBoundedAgentChunks(t *testing.T) {
	now := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	heartbeat := now
	completed := now
	task := domain.Task{ID: "task-remote", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo", ExecutionTarget: execution.Target{Kind: execution.Agent, AgentID: "agent-1"}, Directory: &domain.DirectorySource{Path: "/srv/photos"}}
	storage := &previewStorage{
		tasks: []domain.Task{task},
		agents: []store.AgentRecord{{
			ID: "agent-1", Status: "online", LastHeartbeatAt: &heartbeat,
			Capabilities: []string{"filesystem-scope-preview", agentprotocol.FilesystemScopeEntryCapability, "restic"},
		}},
		filesystemStatus: store.AgentFilesystemRequest{
			Status: "succeeded", Result: mustAgentResult(map[string]any{"scannedItems": 1, "totalFiles": 1, "includedFiles": 1, "truncated": false}, now), CompletedAt: &completed,
		},
	}
	var service *Service
	storage.onCreateFilesystem = func(request store.AgentFilesystemRequest) {
		chunk := agentprotocol.FilesystemScopeEntryChunk{
			Version: agentprotocol.Version, AssignmentID: request.ID, AgentID: "agent-1",
			Entries: []agentprotocol.FilesystemScopeEntry{{
				Ordinal: 1, Path: "2026/a.jpg", Type: "file", Disposition: "included", Size: 5,
			}},
		}
		for attempt := 0; attempt < 2; attempt++ {
			if err := service.AppendFilesystemScopeEntries(t.Context(), "agent-1", chunk); err != nil {
				t.Errorf("append chunk attempt %d: %v", attempt+1, err)
			}
		}
	}
	var err error
	service, err = NewWithCatalogRoot(storage, previewSecrets{}, &previewEngine{kind: agentfilesystem.Kind, failIfRun: true}, nil, func() time.Time { return now }, filepath.Join(t.TempDir(), "catalogs"))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.Inventory(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var definition agentfilesystem.Definition
	if err := json.Unmarshal(storage.filesystemRequest.Definition, &definition); err != nil {
		t.Fatal(err)
	}
	if !definition.IncludeEntries || !storage.filesystemRequest.ExpiresAt.Equal(now.Add(agentScopeClaimTimeout)) || preview.Summary["entriesAvailable"] != true {
		t.Fatalf("definition=%+v preview=%+v", definition, preview)
	}
	page, err := service.Entries(t.Context(), preview.ID, EntryQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Path != "2026/a.jpg" || page.Items[0].Disposition != agentfilesystem.ScopeIncluded {
		t.Fatalf("page=%+v", page)
	}
}

func TestRemoteInventoryRejectsACompletedSummaryThatDidNotUploadEveryEntry(t *testing.T) {
	now := time.Date(2026, 7, 25, 8, 30, 0, 0, time.UTC)
	service, err := NewWithCatalogRoot(&previewStorage{}, previewSecrets{}, nil, nil, func() time.Time { return now }, filepath.Join(t.TempDir(), "catalogs"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.startRemoteInventory("scope-missing", "agent-1", "preview-missing"); err != nil {
		t.Fatal(err)
	}
	if err := service.AppendFilesystemScopeEntries(t.Context(), "agent-1", agentprotocol.FilesystemScopeEntryChunk{
		Version: agentprotocol.Version, AssignmentID: "scope-missing", AgentID: "agent-1",
		Entries: []agentprotocol.FilesystemScopeEntry{{Ordinal: 1, Path: "a.jpg", Type: "file", Disposition: "included"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.finishRemoteInventory("scope-missing", true, 2); err == nil {
		t.Fatal("incomplete Agent inventory was accepted")
	}
	if _, err := os.Stat(service.catalog.path("preview-missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete catalog was retained: %v", err)
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

func TestServiceCreatesBrowsableLocalInventoryAlongsideScopeConfirmation(t *testing.T) {
	now := time.Date(2026, 7, 25, 7, 0, 0, 0, time.UTC)
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "photos"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "photos", "a.jpg"), []byte("photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "skip.tmp"), []byte("tmp"), 0o600); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-inventory", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: source, Exclusions: []string{"**/*.tmp"}}}
	storage := &previewStorage{tasks: []domain.Task{task}}
	service, err := NewWithCatalogRoot(storage, previewSecrets{}, agentfilesystem.New("posix", []string{source}), nil, func() time.Time { return now }, filepath.Join(t.TempDir(), "catalogs"))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.Preview(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Summary["entriesAvailable"] != true || preview.Summary["totalFiles"] != 2 || preview.Summary["includedFiles"] != 1 || preview.Summary["excludedFiles"] != 1 {
		t.Fatalf("preview=%+v", preview)
	}
	page, err := service.Entries(t.Context(), preview.ID, EntryQuery{View: "all", Type: "file", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Path != "photos/a.jpg" || page.Items[1].Path != "skip.tmp" {
		t.Fatalf("page=%+v", page)
	}
}

type previewStorage struct {
	tasks              []domain.Task
	agents             []store.AgentRecord
	rsyncExecution     store.RsyncExecution
	preview            store.TaskScopePreview
	filesystemRequest  store.AgentFilesystemRequest
	filesystemStatus   store.AgentFilesystemRequest
	lease              store.AgentLease
	leaseStatus        store.AgentLease
	onCreateFilesystem func(store.AgentFilesystemRequest)
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
func (s *previewStorage) TaskScopePreview(context.Context, string) (store.TaskScopePreview, error) {
	if s.preview.ID == "" {
		return store.TaskScopePreview{}, errors.New("preview unavailable")
	}
	return s.preview, nil
}
func (s *previewStorage) CreateAgentFilesystemRequest(_ context.Context, request store.AgentFilesystemRequest) error {
	s.filesystemRequest = request
	if s.onCreateFilesystem != nil {
		s.onCreateFilesystem(request)
	}
	return nil
}
func (s *previewStorage) AgentFilesystemRequestStatus(context.Context, string) (store.AgentFilesystemRequest, error) {
	return s.filesystemStatus, nil
}
func (s *previewStorage) ExpireAgentFilesystemRequest(context.Context, string, string, time.Time) error {
	return nil
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
