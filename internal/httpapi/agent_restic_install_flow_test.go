package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/maboo-run/shadoc/internal/agenttool"
	"github.com/maboo-run/shadoc/internal/store"
)

type fakeAgentResticInstaller struct {
	request agenttool.InstallRequest
	stages  []string
}

func (i *fakeAgentResticInstaller) InstallRestic(_ context.Context, request agenttool.InstallRequest, report agenttool.StageReporter) (agenttool.InstallResult, error) {
	i.request = request
	for _, stage := range []string{"probing", "downloading_agent_restic", "draining_agent", "staging_agent_restic", "activating_agent_restic", "waiting_for_agent_restic", "agent_restic_verified"} {
		i.stages = append(i.stages, stage)
		report(stage)
	}
	return agenttool.InstallResult{AgentID: request.AgentID, Version: request.Version, Platform: "linux/amd64"}, nil
}

func TestAgentResticInstallReturnsAuditedTrackedOperation(t *testing.T) {
	srv := newResourceTestServer(t)
	srv.installer = &asyncResticInstaller{}
	remoteInstaller := &fakeAgentResticInstaller{}
	srv.agentResticInstaller = remoteInstaller
	cookie := setupSession(t, srv)

	response := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-a/restic/install", map[string]any{"version": "0.19.1"}, cookie)
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
	if operation.Kind != "agent_restic_install" || operation.Target != "agent-a" || operation.Detail["version"] != "0.19.1" {
		t.Fatalf("operation=%+v", operation)
	}
	if remoteInstaller.request.AgentID != "agent-a" || remoteInstaller.request.Version != "0.19.1" || len(remoteInstaller.stages) != 7 {
		t.Fatalf("installer=%+v", remoteInstaller)
	}
	audits, err := srv.store.(*store.Store).ListAudits(t.Context(), 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		if audit.Action == "agent.restic.install.start" && audit.TargetID == "agent-a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("install audit missing: %+v", audits)
	}
}

func TestAgentResticInstallRequiresConfiguredInstallerAndOfficialVersion(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	if response := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-a/restic/install", map[string]any{}, cookie); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable response=%d %s", response.Code, response.Body.String())
	}
	srv.installer = &asyncResticInstaller{}
	srv.agentResticInstaller = &fakeAgentResticInstaller{}
	if response := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-a/restic/install", map[string]any{"version": "9.9.9"}, cookie); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unapproved version response=%d %s", response.Code, response.Body.String())
	}
}
