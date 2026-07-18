package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/taskpreview"
)

func TestDirectoryTaskMustBeSavedAsDraftPreviewedAndConfirmedBeforeEnable(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-preview")
	source := t.TempDir()
	payload := map[string]any{
		"name": "photos", "kind": "directory", "repositoryId": "repo-preview", "enabled": true,
		"directory": map[string]any{"path": source, "exclusions": []string{}},
		"retention": map[string]any{}, "resources": map[string]any{"compression": "auto"},
		"scopeConfirmation": map[string]any{"previewId": "forged", "fingerprint": "forged", "confirmedBy": "admin", "confirmedAt": time.Now().UTC()},
	}
	blocked := requestJSON(t, srv, http.MethodPost, "/api/tasks", payload, cookie)
	if blocked.Code != http.StatusConflict {
		t.Fatalf("enabled create status=%d body=%s", blocked.Code, blocked.Body.String())
	}

	payload["enabled"] = false
	draftResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks", payload, cookie)
	if draftResponse.Code != http.StatusCreated {
		t.Fatalf("draft status=%d body=%s", draftResponse.Code, draftResponse.Body.String())
	}
	var draft domain.Task
	if err := json.Unmarshal(draftResponse.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.ScopeConfirmation.Present() {
		t.Fatalf("client forged confirmation persisted: %+v", draft.ScopeConfirmation)
	}

	previewer := &httpTaskPreviewer{storage: resources, now: time.Now}
	srv.taskPreviewer = previewer
	previewResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks/"+draft.ID+"/preview", map[string]any{}, cookie)
	if previewResponse.Code != http.StatusCreated {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview store.TaskScopePreview
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	payload["enabled"] = true
	payload["previewId"] = preview.ID
	delete(payload, "scopeConfirmation")
	enabled := requestJSON(t, srv, http.MethodPut, "/api/tasks/"+draft.ID, payload, cookie)
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", enabled.Code, enabled.Body.String())
	}
	var confirmed domain.Task
	if err := json.Unmarshal(enabled.Body.Bytes(), &confirmed); err != nil {
		t.Fatal(err)
	}
	if !confirmed.ScopeConfirmation.Present() || confirmed.ScopeConfirmation.PreviewID != preview.ID || confirmed.ScopeConfirmation.ConfirmedBy != "admin" {
		t.Fatalf("confirmation=%+v", confirmed.ScopeConfirmation)
	}

	delete(payload, "previewId")
	payload["name"] = "renamed photos"
	renamed := requestJSON(t, srv, http.MethodPut, "/api/tasks/"+draft.ID, payload, cookie)
	if renamed.Code != http.StatusOK || !containsJSONField(renamed.Body.Bytes(), "name", "renamed photos") {
		t.Fatalf("rename status=%d body=%s", renamed.Code, renamed.Body.String())
	}
	payload["directory"] = map[string]any{"path": source, "exclusions": []string{"**/.cache"}}
	changed := requestJSON(t, srv, http.MethodPut, "/api/tasks/"+draft.ID, payload, cookie)
	if changed.Code != http.StatusConflict {
		t.Fatalf("changed scope status=%d body=%s", changed.Code, changed.Body.String())
	}
}

func TestRsyncDeletePreviewRequiresSeparateConfirmation(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := srv.store.(*store.Store)
	now := time.Now().UTC()
	heartbeat := now
	if err := resources.SaveAgent(t.Context(), store.AgentRecord{ID: "agent-1", CertificateSerial: "serial-preview", Capabilities: []string{"rsync", "filesystem-scope-preview"}, BuildVersion: "test", ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64", Status: "online", LastHeartbeatAt: &heartbeat, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := resources.CreateRepository(t.Context(), domain.Repository{ID: "sync-repo", Name: "sync repo", Engine: domain.RsyncEngine, Kind: domain.LocalRepository, Path: "/mnt/target", Status: "ready", CreatedAt: now, UpdatedAt: now}, ""); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"name": "mirror", "engine": "rsync", "kind": "rsync", "repositoryId": "sync-repo", "enabled": false,
		"executionTarget": map[string]any{"kind": "agent", "agentId": "agent-1"},
		"rsync":           map[string]any{"path": "/mnt/source", "exclusions": []string{}, "delete": true},
		"retention":       map[string]any{}, "resources": map[string]any{},
	}
	draftResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks", payload, cookie)
	if draftResponse.Code != http.StatusCreated {
		t.Fatalf("draft status=%d body=%s", draftResponse.Code, draftResponse.Body.String())
	}
	var draft domain.Task
	_ = json.Unmarshal(draftResponse.Body.Bytes(), &draft)
	srv.taskPreviewer = &httpTaskPreviewer{storage: resources, now: time.Now, deletePreview: true}
	previewResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks/"+draft.ID+"/preview", map[string]any{}, cookie)
	var preview store.TaskScopePreview
	_ = json.Unmarshal(previewResponse.Body.Bytes(), &preview)
	payload["enabled"] = true
	payload["previewId"] = preview.ID
	withoutConfirmation := requestJSON(t, srv, http.MethodPut, "/api/tasks/"+draft.ID, payload, cookie)
	if withoutConfirmation.Code != http.StatusConflict {
		t.Fatalf("unconfirmed status=%d body=%s", withoutConfirmation.Code, withoutConfirmation.Body.String())
	}
	payload["rsyncDeleteConfirmed"] = true
	confirmed := requestJSON(t, srv, http.MethodPut, "/api/tasks/"+draft.ID, payload, cookie)
	if confirmed.Code != http.StatusOK || !containsJSONField(confirmed.Body.Bytes(), "enabled", true) {
		t.Fatalf("confirmed status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
}

func TestExpiredOrMismatchedTaskPreviewCannotEnableTask(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-stale")
	source := t.TempDir()
	draftResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks", map[string]any{
		"name": "stale", "kind": "directory", "repositoryId": "repo-stale", "enabled": false,
		"directory": map[string]any{"path": source, "exclusions": []string{}}, "retention": map[string]any{}, "resources": map[string]any{},
	}, cookie)
	var draft domain.Task
	_ = json.Unmarshal(draftResponse.Body.Bytes(), &draft)
	fingerprint, _ := taskpreview.Fingerprint(draft)
	now := time.Now().UTC()
	for _, preview := range []store.TaskScopePreview{
		{ID: "expired-preview", TaskID: draft.ID, Fingerprint: fingerprint, Summary: map[string]any{}, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute)},
		{ID: "mismatched-preview", TaskID: draft.ID, Fingerprint: "different", Summary: map[string]any{}, CreatedAt: now, ExpiresAt: now.Add(time.Minute)},
	} {
		if err := resources.CreateTaskScopePreview(t.Context(), preview); err != nil {
			t.Fatal(err)
		}
		response := requestJSON(t, srv, http.MethodPut, "/api/tasks/"+draft.ID, map[string]any{
			"name": draft.Name, "kind": "directory", "repositoryId": draft.RepositoryID, "enabled": true, "previewId": preview.ID,
			"directory": map[string]any{"path": source, "exclusions": []string{}}, "retention": map[string]any{}, "resources": map[string]any{},
		}, cookie)
		if response.Code != http.StatusConflict {
			t.Fatalf("preview=%s status=%d body=%s", preview.ID, response.Code, response.Body.String())
		}
	}
}

type httpTaskPreviewer struct {
	storage       *store.Store
	now           func() time.Time
	deletePreview bool
}

func (p *httpTaskPreviewer) Preview(ctx context.Context, taskID string) (store.TaskScopePreview, error) {
	tasks, err := p.storage.ListTasks(ctx)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	var task domain.Task
	for _, candidate := range tasks {
		if candidate.ID == taskID {
			task = candidate
		}
	}
	fingerprint, err := taskpreview.Fingerprint(task)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	now := p.now().UTC()
	preview := store.TaskScopePreview{ID: "http-preview-" + taskID, TaskID: taskID, Fingerprint: fingerprint, Summary: map[string]any{"includedFiles": 10, "deleteFiles": 2, "targetIdentity": "local:/mnt/target"}, RequiresDeleteConfirmation: p.deletePreview, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	return preview, p.storage.CreateTaskScopePreview(ctx, preview)
}

func containsJSONField(encoded []byte, key string, want any) bool {
	var value map[string]any
	if json.Unmarshal(encoded, &value) != nil {
		return false
	}
	return value[key] == want
}

var _ taskScopePreviewer = (*httpTaskPreviewer)(nil)
