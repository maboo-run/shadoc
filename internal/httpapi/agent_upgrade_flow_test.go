package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
	"github.com/maboo-run/shadoc/internal/store"
)

type fakeAgentUpgrader struct {
	request agentdeploy.UpgradeRequest
	stages  []string
}

func (u *fakeAgentUpgrader) Upgrade(_ context.Context, request agentdeploy.UpgradeRequest, report agentdeploy.StageReporter) (agentdeploy.UpgradeResult, error) {
	u.request = request
	for _, stage := range []string{"draining_agent", "staging_agent_upgrade", "waiting_for_agent_upgrade", "agent_upgrade_verified"} {
		u.stages = append(u.stages, stage)
		report(stage)
	}
	return agentdeploy.UpgradeResult{AgentID: request.AgentID, FromVersion: "v1.3.0", ToVersion: request.TargetVersion}, nil
}

func TestAgentUpgradeReturnsAuditedTrackedOperationForApplicationVersion(t *testing.T) {
	srv := newResourceTestServer(t)
	upgrader := &fakeAgentUpgrader{}
	srv.agentUpgrader = upgrader
	srv.applicationVersion = "v1.4.0"
	cookie := setupSession(t, srv)

	response := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-a/upgrade", map[string]any{}, cookie)
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
	if operation.Kind != "agent_upgrade" || operation.Target != "agent-a" || operation.Detail["targetVersion"] != "v1.4.0" {
		t.Fatalf("operation=%+v", operation)
	}
	if upgrader.request.AgentID != "agent-a" || upgrader.request.TargetVersion != "v1.4.0" || len(upgrader.stages) != 4 {
		t.Fatalf("upgrader=%+v", upgrader)
	}
	audits, err := srv.store.(*store.Store).ListAudits(t.Context(), 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		if audit.Action == "agent.upgrade.start" && audit.TargetID == "agent-a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("upgrade audit missing: %+v", audits)
	}
}

func TestAgentUpgradeRequiresConfiguredUpgraderAndVersion(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	if response := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-a/upgrade", map[string]any{}, cookie); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable response=%d %s", response.Code, response.Body.String())
	}
	srv.agentUpgrader = &fakeAgentUpgrader{}
	if response := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-a/upgrade", map[string]any{}, cookie); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing version response=%d %s", response.Code, response.Body.String())
	}
}
