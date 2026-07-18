package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
)

type fakeAgentUninstaller struct {
	agentID string
	stages  []string
}

func (u *fakeAgentUninstaller) Uninstall(_ context.Context, agentID string, report agentdeploy.StageReporter) (agentdeploy.RemovalResult, error) {
	u.agentID = agentID
	for _, stage := range []string{"probing", "stopping_agent", "removing_agent", "revoking_agent"} {
		u.stages = append(u.stages, stage)
		report(stage)
	}
	return agentdeploy.RemovalResult{AgentID: agentID, HostID: "host-1", Platform: "linux/amd64"}, nil
}

func TestAgentUninstallReturnsTrackedOperation(t *testing.T) {
	srv := newResourceTestServer(t)
	uninstaller := &fakeAgentUninstaller{}
	srv.agentUninstaller = uninstaller
	cookie := setupSession(t, srv)

	response := requestJSON(t, srv, http.MethodPost, "/api/agents/mini-debian/uninstall", map[string]any{}, cookie)
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
	if operation.Kind != "agent_uninstall" || operation.Target != "mini-debian" || uninstaller.agentID != "mini-debian" {
		t.Fatalf("operation=%+v uninstaller=%+v", operation, uninstaller)
	}
}
