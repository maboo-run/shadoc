package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
)

type fakeAgentDeployer struct {
	request agentdeploy.DeployRequest
}

func (d *fakeAgentDeployer) Deploy(_ context.Context, request agentdeploy.DeployRequest, report agentdeploy.StageReporter) (agentdeploy.DeployResult, error) {
	d.request = request
	report("uploading")
	return agentdeploy.DeployResult{AgentID: request.AgentID, HostID: request.HostID}, nil
}

func TestAgentDeployReturnsTrackedOperationWithoutPersistingConnectionDetails(t *testing.T) {
	srv := newResourceTestServer(t)
	deployer := &fakeAgentDeployer{}
	srv.agentDeployer = deployer
	cookie := setupSession(t, srv)

	response := requestJSON(t, srv, http.MethodPost, "/api/agents/deploy", map[string]any{
		"hostId": "host-1", "agentId": "backup-node", "serviceUrl": "https://control.internal:9443",
	}, cookie)
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &accepted)
	if response.Code != http.StatusAccepted || accepted.OperationID == "" {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	operation := waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	if operation.Kind != "agent_deploy" || operation.Actor != "admin" || operation.Target != "backup-node" {
		t.Fatalf("operation=%+v", operation)
	}
	if operation.Detail["hostId"] != "host-1" || operation.Detail["serviceUrl"] != nil {
		t.Fatalf("unsafe operation detail=%+v", operation.Detail)
	}
	if deployer.request.ServiceURL != "https://control.internal:9443" {
		t.Fatalf("deployment request=%+v", deployer.request)
	}
}

func TestAgentDeployRequiresConfiguredDeployerAndCompleteRequest(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	if response := requestJSON(t, srv, http.MethodPost, "/api/agents/deploy", map[string]any{
		"hostId": "host-1", "agentId": "backup-node", "serviceUrl": "https://control.internal:9443",
	}, cookie); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable response=%d %s", response.Code, response.Body.String())
	}

	srv.agentDeployer = &fakeAgentDeployer{}
	if response := requestJSON(t, srv, http.MethodPost, "/api/agents/deploy", map[string]any{"hostId": "host-1"}, cookie); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid response=%d %s", response.Code, response.Body.String())
	}
}
