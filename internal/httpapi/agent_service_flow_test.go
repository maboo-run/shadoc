package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
	"github.com/maboo-run/shadoc/internal/agentservice"
)

type fakeAgentServiceManager struct {
	status     agentservice.Status
	configured agentservice.Settings
}

func (m *fakeAgentServiceManager) Status() agentservice.Status { return m.status }

func (m *fakeAgentServiceManager) Configure(_ context.Context, settings agentservice.Settings) (agentservice.Status, error) {
	m.configured = settings
	m.status = agentservice.Status{
		Enabled: settings.Enabled, Running: settings.Enabled, Port: settings.Port,
		AdvertisedHost: settings.AdvertisedHost, ListenAddress: "0.0.0.0:10443",
		ServiceURL: "https://control.lan:10443",
	}
	return m.status, nil
}

func (*fakeAgentServiceManager) CreateEnrollmentToken(context.Context, time.Duration) (string, string, error) {
	return "token", "ca", nil
}

func (*fakeAgentServiceManager) Deploy(context.Context, agentdeploy.DeployRequest, agentdeploy.StageReporter) (agentdeploy.DeployResult, error) {
	return agentdeploy.DeployResult{}, nil
}

func TestAdministratorCanConfigureAndReadAgentService(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &fakeAgentServiceManager{status: agentservice.Status{Port: 9443}}
	srv.agentService = manager
	cookie := setupSession(t, srv)

	response := requestJSON(t, srv, http.MethodPut, "/api/agent-service", map[string]any{
		"enabled": true, "port": 10443, "advertisedHost": "control.lan",
	}, cookie)
	if response.Code != http.StatusOK || manager.configured.ListenHost != "0.0.0.0" || manager.configured.Port != 10443 {
		t.Fatalf("response=%d %s settings=%+v", response.Code, response.Body.String(), manager.configured)
	}
	read := requestJSON(t, srv, http.MethodGet, "/api/agent-service", nil, cookie)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"running":true`) || !strings.Contains(read.Body.String(), `"serviceUrl":"https://control.lan:10443"`) {
		t.Fatalf("read=%d %s", read.Code, read.Body.String())
	}
}

func TestAgentServiceConfigurationRejectsUnsafePortAndRequiresAuthentication(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &fakeAgentServiceManager{}
	srv.agentService = manager
	if response := requestJSON(t, srv, http.MethodGet, "/api/agent-service", nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.Code)
	}
	cookie := setupSession(t, srv)
	response := requestJSON(t, srv, http.MethodPut, "/api/agent-service", map[string]any{
		"enabled": true, "port": 443, "advertisedHost": "control.lan",
	}, cookie)
	if response.Code != http.StatusUnprocessableEntity || manager.configured.Enabled {
		t.Fatalf("response=%d %s settings=%+v", response.Code, response.Body.String(), manager.configured)
	}
}
