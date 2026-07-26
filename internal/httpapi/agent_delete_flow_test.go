package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
)

func TestAgentDeletionRequiresRevocationAndVersionedPreview(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := srv.store.(*store.Store)
	now := time.Date(2026, 7, 25, 14, 30, 0, 0, time.UTC)
	if err := resources.SaveAgent(t.Context(), store.AgentRecord{
		ID: "agent-a", CertificateSerial: "serial-a", Status: "online", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	if response := requestJSON(t, srv, http.MethodDelete, "/api/agents/agent-a", nil, cookie); response.Code != http.StatusPreconditionRequired {
		t.Fatalf("direct delete status=%d body=%s", response.Code, response.Body.String())
	}
	activePreviewResponse := requestJSON(t, srv, http.MethodGet, "/api/delete-previews/agents/agent-a", nil, cookie)
	var activePreview store.ResourceDeletePreview
	if err := json.Unmarshal(activePreviewResponse.Body.Bytes(), &activePreview); err != nil {
		t.Fatal(err)
	}
	if activePreviewResponse.Code != http.StatusOK || activePreview.Deletable || activePreview.BlockedReason == "" {
		t.Fatalf("active preview status=%d preview=%+v", activePreviewResponse.Code, activePreview)
	}
	if response := requestJSON(t, srv, http.MethodPost, "/api/delete-previews/agents/agent-a/confirm", map[string]any{
		"expectedUpdatedAt": activePreview.UpdatedAt,
	}, cookie); response.Code != http.StatusConflict {
		t.Fatalf("active confirmation status=%d body=%s", response.Code, response.Body.String())
	}

	if response := requestJSON(t, srv, http.MethodPost, "/api/agents/agent-a/revoke", map[string]any{}, cookie); response.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", response.Code, response.Body.String())
	}
	previewResponse := requestJSON(t, srv, http.MethodGet, "/api/delete-previews/agents/agent-a", nil, cookie)
	var preview store.ResourceDeletePreview
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if previewResponse.Code != http.StatusOK || !preview.Deletable || preview.UpdatedAt == "" {
		t.Fatalf("revoked preview status=%d preview=%+v", previewResponse.Code, preview)
	}
	if response := requestJSON(t, srv, http.MethodPost, "/api/delete-previews/agents/agent-a/confirm", map[string]any{
		"expectedUpdatedAt": preview.UpdatedAt,
	}, cookie); response.Code != http.StatusNoContent {
		t.Fatalf("confirmation status=%d body=%s", response.Code, response.Body.String())
	}
	agents, err := resources.ListAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Fatalf("remaining Agents=%+v", agents)
	}
	audits, err := resources.ListAudits(t.Context(), 20)
	if err != nil {
		t.Fatal(err)
	}
	foundDelete := false
	for _, audit := range audits {
		if audit.Action == "agent.delete" && audit.TargetID == "agent-a" {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatalf("missing Agent deletion audit: %+v", audits)
	}
}
