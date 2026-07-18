package agentruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

type heartbeatRecordingControl struct{ heartbeat agentprotocol.Heartbeat }

func (c *heartbeatRecordingControl) Heartbeat(_ context.Context, heartbeat agentprotocol.Heartbeat) error {
	c.heartbeat = heartbeat
	return nil
}
func (*heartbeatRecordingControl) Lease(context.Context) (agentprotocol.Assignment, bool, error) {
	return agentprotocol.Assignment{}, false, nil
}
func (*heartbeatRecordingControl) Complete(context.Context, agentprotocol.Result) error { return nil }

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
