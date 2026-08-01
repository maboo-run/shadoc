package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentfilesystem"
	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestAgentsExposeTheirBoundRemoteHost(t *testing.T) {
	srv := newAuthTestServer(t)
	setup := requestJSON(t, srv, http.MethodPost, "/api/setup", map[string]string{"username": "admin", "password": "correct horse battery staple"}, nil)
	cookie := sessionCookie(t, setup)
	resources := srv.store.(*store.Store)
	now := time.Now().UTC()
	if err := resources.SaveSecret(t.Context(), "host-key", "ssh-private-key", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := resources.CreateRemoteHost(t.Context(), domain.RemoteHost{ID: "host-1", Name: "Host 1", Host: "host-1", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, "host-key"); err != nil {
		t.Fatal(err)
	}
	if err := resources.SaveAgent(t.Context(), store.AgentRecord{ID: "agent-1", CertificateSerial: "1", Capabilities: []string{"filesystem-browse"}, Status: "online", LastHeartbeatAt: &now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := resources.BindAgentRemoteHost(t.Context(), "agent-1", "host-1"); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, srv, http.MethodGet, "/api/agents", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("list agents status=%d body=%s", response.Code, response.Body.String())
	}
	var agents []struct {
		ID                  string `json:"id"`
		RemoteHostID        string `json:"remoteHostId"`
		ManagedInstallation bool   `json:"managedInstallation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &agents); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "agent-1" || agents[0].RemoteHostID != "host-1" || agents[0].ManagedInstallation {
		t.Fatalf("agents=%+v", agents)
	}
}

func TestManualAgentAssociationKeepsDeploymentLifecycleSeparate(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := srv.store.(*store.Store)
	now := time.Now().UTC()
	for _, host := range []domain.RemoteHost{
		{ID: "host-manual", Name: "Manual host", Host: "manual.example", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now},
		{ID: "host-managed", Name: "Managed host", Host: "managed.example", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now},
	} {
		if err := resources.SaveSecret(t.Context(), "key-"+host.ID, "ssh-private-key", []byte("cipher"), now); err != nil {
			t.Fatal(err)
		}
		if err := resources.CreateRemoteHost(t.Context(), host, "key-"+host.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range []store.AgentRecord{
		{ID: "manual-agent", CertificateSerial: "manual-serial", Status: "online", LastHeartbeatAt: &now, CreatedAt: now},
		{ID: "managed-agent", CertificateSerial: "managed-serial", Status: "online", LastHeartbeatAt: &now, CreatedAt: now},
	} {
		if err := resources.SaveAgent(t.Context(), agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := resources.BindManagedAgentRemoteHost(t.Context(), "managed-agent", "host-managed"); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, srv, http.MethodPut, "/api/agents/manual-agent/remote-host", map[string]string{"remoteHostId": "host-manual"}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("association status=%d body=%s", response.Code, response.Body.String())
	}
	agents, err := resources.ListAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]store.AgentRecord{}
	for _, agent := range agents {
		byID[agent.ID] = agent
	}
	if agent := byID["manual-agent"]; agent.RemoteHostID != "host-manual" || agent.ManagedInstallation {
		t.Fatalf("manual Agent association=%+v", agent)
	}
	if response := requestJSON(t, srv, http.MethodPut, "/api/agents/manual-agent/remote-host", map[string]string{"remoteHostId": "host-managed"}, cookie); response.Code != http.StatusConflict {
		t.Fatalf("managed host takeover status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAgentListReportsRunningStoppedAndUnknownRuntimeStates(t *testing.T) {
	srv := newAuthTestServer(t)
	setup := requestJSON(t, srv, http.MethodPost, "/api/setup", map[string]string{"username": "admin", "password": "correct horse battery staple"}, nil)
	cookie := sessionCookie(t, setup)
	resources := srv.store.(*store.Store)
	now := time.Now().UTC()
	stale := now.Add(-2 * time.Minute)
	for _, agent := range []store.AgentRecord{
		{ID: "running", CertificateSerial: "1", Status: "online", LastHeartbeatAt: &now, CreatedAt: now},
		{ID: "unknown", CertificateSerial: "2", Status: "online", LastHeartbeatAt: &stale, CreatedAt: now},
		{ID: "stopped", CertificateSerial: "3", Status: "online", LastHeartbeatAt: &now, CreatedAt: now},
	} {
		if err := resources.SaveAgent(t.Context(), agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := resources.MarkAgentStopped(t.Context(), "stopped", now); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, srv, http.MethodGet, "/api/agents", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("list agents status=%d body=%s", response.Code, response.Body.String())
	}
	var agents []struct {
		ID            string `json:"id"`
		RuntimeStatus string `json:"runtimeStatus"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &agents); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"running": "running", "stopped": "stopped", "unknown": "unknown"}
	for _, agent := range agents {
		if want[agent.ID] != agent.RuntimeStatus {
			t.Fatalf("Agent %q runtime status=%q want=%q; all=%+v", agent.ID, agent.RuntimeStatus, want[agent.ID], agents)
		}
		delete(want, agent.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing agents: %v", want)
	}
}

func TestAgentFilesystemBrowseAndCreateDirectory(t *testing.T) {
	srv := newAuthTestServer(t)
	setup := requestJSON(t, srv, http.MethodPost, "/api/setup", map[string]string{"username": "admin", "password": "correct horse battery staple"}, nil)
	cookie := sessionCookie(t, setup)
	cookie.Raw = setup.Header().Get("X-CSRF-Token")
	resources := srv.store.(*store.Store)
	// A heartbeat inside the shared two-minute window must be usable by both
	// the Agent card and this filesystem operation. Previously this endpoint
	// used a separate one-minute cut-off and disagreed with the UI.
	now := time.Now().UTC().Add(-90 * time.Second)
	if err := resources.SaveAgent(t.Context(), store.AgentRecord{ID: "agent-1", CertificateSerial: "1", Capabilities: []string{"filesystem-browse", "filesystem-create-directory", "path-style:posix"}, Status: "online", LastHeartbeatAt: &now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			request, err := resources.ClaimAgentFilesystemRequest(ctx, "agent-1", time.Now().UTC())
			if err != nil {
				time.Sleep(time.Millisecond)
				continue
			}
			var definition agentfilesystem.Definition
			_ = json.Unmarshal(request.Definition, &definition)
			summary := map[string]any{"path": definition.Path}
			if definition.Operation == agentfilesystem.Browse {
				summary["entries"] = []agentfilesystem.Entry{{Name: "photos", Path: "/srv/photos", Directory: true}}
			} else {
				summary["created"] = true
			}
			encoded, _ := json.Marshal(agentprotocol.Result{Version: agentprotocol.Version, AssignmentID: request.ID, AgentID: "agent-1", Status: "succeeded", Summary: summary})
			_ = resources.CompleteAgentFilesystemRequest(ctx, request.ID, "agent-1", "succeeded", encoded, time.Now().UTC())
		}
	}()

	browse := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-1/filesystem/browse", map[string]string{"path": "/srv"}, cookie)
	if browse.Code != http.StatusOK || !json.Valid(browse.Body.Bytes()) {
		t.Fatalf("browse status=%d body=%s", browse.Code, browse.Body.String())
	}
	create := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-1/filesystem/directories", map[string]string{"path": "/srv/archive"}, cookie)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	audits, err := resources.ListAudits(t.Context(), 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		found = found || audit.Action == "agent.filesystem.create-directory" && audit.TargetID == "agent-1"
	}
	if !found {
		t.Fatalf("create-directory audit missing: %+v", audits)
	}
}

func TestAgentFilesystemRejectsUnsupportedAgent(t *testing.T) {
	srv := newAuthTestServer(t)
	setup := requestJSON(t, srv, http.MethodPost, "/api/setup", map[string]string{"username": "admin", "password": "correct horse battery staple"}, nil)
	cookie := sessionCookie(t, setup)
	cookie.Raw = setup.Header().Get("X-CSRF-Token")
	now := time.Now().UTC()
	if err := srv.store.(*store.Store).SaveAgent(t.Context(), store.AgentRecord{ID: "agent-1", CertificateSerial: "1", Status: "online", LastHeartbeatAt: &now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-1/filesystem/browse", map[string]string{"path": "/"}, cookie)
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
