package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentfilesystem"
	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/execution"
)

func TestExecuteRejectsExpiredAssignmentBeforeEngineRun(t *testing.T) {
	engine := &recordingEngine{kind: "rsync"}
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	runtime := New("agent-1", execution.NewRegistry(engine), func() time.Time { return now })
	result := runtime.Execute(context.Background(), agentprotocol.Assignment{Version: agentprotocol.Version, ID: "lease-1", AgentID: "agent-1", TaskID: "task-1", Engine: "rsync", Definition: json.RawMessage(`{}`), ExpiresAt: now})
	if result.Status != "failed" || result.Error == "" {
		t.Fatalf("result=%+v", result)
	}
	if engine.ran {
		t.Fatal("expired assignment reached engine")
	}
}

func TestExecuteDispatchesAssignmentAndMapsOutcome(t *testing.T) {
	engine := &recordingEngine{kind: "restic", outcome: execution.Outcome{Status: "succeeded", SnapshotID: "snapshot-1", Summary: map[string]any{"files": float64(3)}}}
	now := time.Now().UTC()
	runtime := New("agent-1", execution.NewRegistry(engine), func() time.Time { return now })
	result := runtime.Execute(context.Background(), agentprotocol.Assignment{Version: agentprotocol.Version, ID: "lease-1", AgentID: "agent-1", TaskID: "task-1", Engine: "restic", Definition: json.RawMessage(`{"source":"/srv"}`), ExpiresAt: now.Add(time.Minute)})
	if result.Status != "succeeded" || result.SnapshotID != "snapshot-1" || !engine.ran {
		t.Fatalf("result=%+v ran=%v", result, engine.ran)
	}
}

func TestExecutePreservesPartialAgentOutcome(t *testing.T) {
	engine := &recordingEngine{kind: "restic", outcome: execution.Outcome{
		Status: "partial", SnapshotID: "partial-1",
		Summary: map[string]any{"filesExpected": int64(5), "filesProcessed": int64(4)},
	}}
	now := time.Now().UTC()
	runtime := New("agent-1", execution.NewRegistry(engine), func() time.Time { return now })
	result := runtime.Execute(context.Background(), agentprotocol.Assignment{
		Version: agentprotocol.Version, ID: "lease-1", AgentID: "agent-1", TaskID: "task-1",
		Engine: "restic", Definition: json.RawMessage(`{}`), ExpiresAt: now.Add(time.Minute),
	})
	if result.Status != "partial" || result.SnapshotID != "partial-1" || result.Summary["filesExpected"] != int64(5) {
		t.Fatalf("result=%+v", result)
	}
}

func TestStepReportsStructuredRuntimeMetadataWithoutCapabilityParsing(t *testing.T) {
	now := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC)
	runtime := New("agent-1", execution.NewRegistry(), func() time.Time { return now })
	runtime.SetRuntimeInfo(agentprotocol.RuntimeInfo{
		BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64",
		ResticVersion: "0.18.0", RsyncVersion: "3.4.1", ServiceURL: "https://control.example:9443",
	})
	control := &heartbeatRecordingControl{}
	if err := runtime.Step(t.Context(), control, []string{"restic"}); err != nil {
		t.Fatal(err)
	}
	if control.heartbeat.Runtime.BuildVersion != "v1.4.0" || control.heartbeat.Runtime.ResticVersion != "0.18.0" || control.heartbeat.Runtime.ProtocolMin != 1 {
		t.Fatalf("heartbeat=%+v", control.heartbeat)
	}
}

func TestStepRenewsAssignmentWhileItsEngineIsStillRunning(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	engine := &blockingEngine{started: make(chan struct{}), release: make(chan struct{})}
	runtime := New("agent-1", execution.NewRegistry(engine), func() time.Time { return now })
	runtime.progressInterval = time.Millisecond
	control := &progressRecordingControl{assignment: agentprotocol.Assignment{
		Version: agentprotocol.Version, ID: "lease-long", AgentID: "agent-1", TaskID: "task-long", Engine: "rsync", Definition: json.RawMessage(`{}`), ExpiresAt: now.Add(time.Hour),
	}, progress: make(chan agentprotocol.AssignmentProgress, 1024)}
	done := make(chan error, 1)
	go func() {
		done <- runtime.Step(t.Context(), control, []string{"rsync", agentprotocol.AssignmentProgressCapability})
	}()
	<-engine.started
	select {
	case progress := <-control.progress:
		if progress.AssignmentID != "lease-long" || progress.Phase != "starting" {
			t.Fatalf("progress=%+v", progress)
		}
	case <-time.After(time.Second):
		t.Fatal("running assignment was not renewed")
	}
	select {
	case progress := <-control.progress:
		if progress.Phase != "transferring" || progress.BytesTransferred != 4096 || progress.TotalBytes != 8192 {
			t.Fatalf("transferred progress=%+v", progress)
		}
	case <-time.After(time.Second):
		t.Fatal("engine progress was not reported")
	}
	close(engine.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStepKeepsRenewingUntilTheServiceAcknowledgesTheResult(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	engine := &recordingEngine{kind: "rsync", outcome: execution.Outcome{Status: "succeeded"}}
	runtime := New("agent-1", execution.NewRegistry(engine), func() time.Time { return now })
	runtime.progressInterval = time.Millisecond
	control := &transientCompletionControl{assignment: agentprotocol.Assignment{
		Version: agentprotocol.Version, ID: "lease-result", AgentID: "agent-1", TaskID: "task-result",
		Engine: "rsync", Definition: json.RawMessage(`{}`), ExpiresAt: now.Add(time.Hour),
	}}

	if err := runtime.Step(t.Context(), control, []string{"rsync", agentprotocol.AssignmentProgressCapability}); err != nil {
		t.Fatal(err)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.completeCalls != 2 {
		t.Fatalf("complete calls=%d, want 2", control.completeCalls)
	}
	if control.progressCalls < 2 {
		t.Fatalf("progress calls=%d, renewal stopped before result acknowledgement", control.progressCalls)
	}
}

func TestStepStreamsBoundedFilesystemScopeEntriesBeforeSummaryResult(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "photos"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "photos", "a.jpg"), []byte("photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "skip.tmp"), []byte("tmp"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 25, 13, 0, 0, 0, time.UTC)
	definition, _ := json.Marshal(agentfilesystem.Definition{
		Operation: agentfilesystem.PreviewScope, Path: source, Exclusions: []string{"**/*.tmp"},
		Limit: agentfilesystem.MaxScopeItems, IncludeEntries: true,
	})
	control := &scopeStreamingControl{assignment: agentprotocol.Assignment{
		Version: agentprotocol.Version, ID: "scope-1", AgentID: "agent-1", TaskID: "filesystem",
		Engine: string(agentfilesystem.Kind), Definition: definition, ExpiresAt: now.Add(time.Minute),
	}}
	runtime := New("agent-1", execution.NewRegistry(agentfilesystem.New("posix", []string{source})), func() time.Time { return now })
	if err := runtime.Step(t.Context(), control, []string{agentprotocol.FilesystemScopeEntryCapability}); err != nil {
		t.Fatal(err)
	}
	if control.result.Status != "succeeded" || control.result.Summary["totalFiles"] != 2 || control.result.Summary["includedFiles"] != 1 {
		t.Fatalf("result=%+v", control.result)
	}
	if len(control.progress) == 0 || control.progress[0].AssignmentID != "scope-1" {
		t.Fatalf("filesystem assignment was not renewed: %+v", control.progress)
	}
	var entries []agentprotocol.FilesystemScopeEntry
	for index, chunk := range control.chunks {
		if chunk.Sequence != index || chunk.AssignmentID != "scope-1" {
			t.Fatalf("chunk=%+v", chunk)
		}
		entries = append(entries, chunk.Entries...)
	}
	if len(entries) != 3 || entries[0].Path != "photos" || entries[1].Path != "photos/a.jpg" || entries[2].Disposition != "excluded" {
		t.Fatalf("entries=%+v", entries)
	}
}

type heartbeatRecordingControl struct{ heartbeat agentprotocol.Heartbeat }

func (c *heartbeatRecordingControl) Heartbeat(_ context.Context, heartbeat agentprotocol.Heartbeat) error {
	c.heartbeat = heartbeat
	return nil
}
func (*heartbeatRecordingControl) Lease(context.Context) (agentprotocol.Assignment, bool, error) {
	return agentprotocol.Assignment{}, false, nil
}
func (*heartbeatRecordingControl) Complete(context.Context, agentprotocol.Result) error { return nil }

type progressRecordingControl struct {
	assignment agentprotocol.Assignment
	claimed    bool
	progress   chan agentprotocol.AssignmentProgress
	mu         sync.Mutex
	result     agentprotocol.Result
}

type transientCompletionControl struct {
	assignment    agentprotocol.Assignment
	claimed       bool
	mu            sync.Mutex
	progressCalls int
	completeCalls int
}

func (*transientCompletionControl) Heartbeat(context.Context, agentprotocol.Heartbeat) error {
	return nil
}
func (c *transientCompletionControl) Lease(context.Context) (agentprotocol.Assignment, bool, error) {
	if c.claimed {
		return agentprotocol.Assignment{}, false, nil
	}
	c.claimed = true
	return c.assignment, true, nil
}
func (c *transientCompletionControl) Progress(context.Context, agentprotocol.AssignmentProgress) error {
	c.mu.Lock()
	c.progressCalls++
	c.mu.Unlock()
	return nil
}
func (c *transientCompletionControl) Complete(context.Context, agentprotocol.Result) error {
	c.mu.Lock()
	c.completeCalls++
	attempt := c.completeCalls
	c.mu.Unlock()
	if attempt == 1 {
		time.Sleep(10 * time.Millisecond)
		return errors.New("temporary result delivery failure")
	}
	return nil
}

func (*progressRecordingControl) Heartbeat(context.Context, agentprotocol.Heartbeat) error {
	return nil
}
func (c *progressRecordingControl) Lease(context.Context) (agentprotocol.Assignment, bool, error) {
	if c.claimed {
		return agentprotocol.Assignment{}, false, nil
	}
	c.claimed = true
	return c.assignment, true, nil
}
func (c *progressRecordingControl) Progress(_ context.Context, progress agentprotocol.AssignmentProgress) error {
	c.progress <- progress
	return nil
}
func (c *progressRecordingControl) Complete(_ context.Context, result agentprotocol.Result) error {
	c.mu.Lock()
	c.result = result
	c.mu.Unlock()
	return nil
}

type blockingEngine struct {
	started chan struct{}
	release chan struct{}
}

func (*blockingEngine) Kind() execution.EngineKind     { return "rsync" }
func (*blockingEngine) Validate(json.RawMessage) error { return nil }
func (e *blockingEngine) Run(ctx context.Context, assignment execution.Assignment) (execution.Outcome, error) {
	close(e.started)
	if assignment.Progress != nil {
		assignment.Progress(execution.Progress{Phase: "transferring", BytesTransferred: 4096, TotalBytes: 8192})
	}
	select {
	case <-e.release:
		return execution.Outcome{Status: "succeeded"}, nil
	case <-ctx.Done():
		return execution.Outcome{Status: "failed"}, ctx.Err()
	}
}

type scopeStreamingControl struct {
	assignment agentprotocol.Assignment
	claimed    bool
	chunks     []agentprotocol.FilesystemScopeEntryChunk
	result     agentprotocol.Result
	progress   []agentprotocol.AssignmentProgress
}

func (*scopeStreamingControl) Heartbeat(context.Context, agentprotocol.Heartbeat) error { return nil }
func (*scopeStreamingControl) Lease(context.Context) (agentprotocol.Assignment, bool, error) {
	return agentprotocol.Assignment{}, false, nil
}
func (*scopeStreamingControl) Complete(context.Context, agentprotocol.Result) error { return nil }
func (c *scopeStreamingControl) Progress(_ context.Context, progress agentprotocol.AssignmentProgress) error {
	c.progress = append(c.progress, progress)
	return nil
}
func (c *scopeStreamingControl) ClaimFilesystem(context.Context) (agentprotocol.Assignment, bool, error) {
	if c.claimed {
		return agentprotocol.Assignment{}, false, nil
	}
	c.claimed = true
	return c.assignment, true, nil
}
func (c *scopeStreamingControl) UploadFilesystemScopeEntries(_ context.Context, chunk agentprotocol.FilesystemScopeEntryChunk) error {
	c.chunks = append(c.chunks, chunk)
	return nil
}
func (c *scopeStreamingControl) CompleteFilesystem(_ context.Context, result agentprotocol.Result) error {
	c.result = result
	return nil
}

type recordingEngine struct {
	kind    execution.EngineKind
	outcome execution.Outcome
	err     error
	ran     bool
}

func (e *recordingEngine) Kind() execution.EngineKind     { return e.kind }
func (e *recordingEngine) Validate(json.RawMessage) error { return nil }
func (e *recordingEngine) Run(context.Context, execution.Assignment) (execution.Outcome, error) {
	e.ran = true
	if e.err != nil {
		return execution.Outcome{}, e.err
	}
	return e.outcome, nil
}
