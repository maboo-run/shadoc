package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
	"github.com/maboo-run/shadoc/internal/store"
)

type fakeAgentToolProber struct {
	agentID string
	stages  []string
}

func (p *fakeAgentToolProber) ReprobeTools(_ context.Context, agentID string, report agentdeploy.StageReporter) (agentdeploy.ToolProbeResult, error) {
	p.agentID = agentID
	for _, stage := range []string{"draining_agent", "restarting_agent_for_tool_probe", "waiting_for_agent_tool_probe", "agent_tool_probe_verified"} {
		p.stages = append(p.stages, stage)
		report(stage)
	}
	return agentdeploy.ToolProbeResult{AgentID: agentID, HostID: "host-a", Platform: "linux/amd64"}, nil
}

func TestAgentToolProbeReturnsAuditedTrackedOperation(t *testing.T) {
	srv := newResourceTestServer(t)
	prober := &fakeAgentToolProber{}
	srv.agentToolProber = prober
	cookie := setupSession(t, srv)

	response := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-a/tools/reprobe", map[string]any{}, cookie)
	if response.Code != http.StatusAccepted {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || accepted.OperationID == "" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	operation := waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	if operation.Kind != "agent_tool_probe" || operation.Target != "agent-a" || operation.Stage != "completed" {
		t.Fatalf("operation=%+v", operation)
	}
	if prober.agentID != "agent-a" || len(prober.stages) != 4 {
		t.Fatalf("prober=%+v", prober)
	}
	audits, err := srv.store.(*store.Store).ListAudits(t.Context(), 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range audits {
		if audit.Action == "agent.tools.reprobe.start" && audit.TargetID == "agent-a" {
			return
		}
	}
	t.Fatalf("tool probe audit missing: %+v", audits)
}

func TestAgentToolProbeRequiresConfiguredProber(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	if response := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-a/tools/reprobe", map[string]any{}, cookie); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable response=%d %s", response.Code, response.Body.String())
	}
}
