package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentfilesystem"
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

func TestTaskScopeInventoryRunsAsPersistentOperationAndPagesOnlyOnDemand(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-inventory")
	source := t.TempDir()
	draftResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks", map[string]any{
		"name": "inventory", "kind": "directory", "repositoryId": "repo-inventory", "enabled": false,
		"directory": map[string]any{"path": source, "exclusions": []string{}}, "retention": map[string]any{}, "resources": map[string]any{},
	}, cookie)
	var draft domain.Task
	if err := json.Unmarshal(draftResponse.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	previewer := &httpTaskPreviewer{storage: resources, now: time.Now}
	srv.taskPreviewer = previewer
	acceptedResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks/"+draft.ID+"/scope-inventory", map[string]any{}, cookie)
	if acceptedResponse.Code != http.StatusAccepted {
		t.Fatalf("accepted status=%d body=%s", acceptedResponse.Code, acceptedResponse.Body.String())
	}
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(acceptedResponse.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	operation, err := srv.operations.Wait(t.Context(), accepted.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != "success" || operation.Detail["previewId"] != "http-preview-"+draft.ID {
		t.Fatalf("operation=%+v", operation)
	}
	if !previewer.inventoryCalled {
		t.Fatal("scope inventory operation used the activation-only preview path")
	}

	previewResponse := requestJSON(t, srv, http.MethodGet, "/api/task-scope-previews/http-preview-"+draft.ID, nil, cookie)
	if previewResponse.Code != http.StatusOK || !containsJSONField(previewResponse.Body.Bytes(), "taskId", draft.ID) {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	entriesResponse := requestJSON(t, srv, http.MethodGet, "/api/task-scope-previews/http-preview-"+draft.ID+"/entries?view=attention&type=file&q=key&children=true&parent=ssh&limit=25", nil, cookie)
	if entriesResponse.Code != http.StatusOK || !strings.Contains(entriesResponse.Body.String(), "private.key") {
		t.Fatalf("entries status=%d body=%s", entriesResponse.Code, entriesResponse.Body.String())
	}
	if previewer.entryQuery.View != "attention" || previewer.entryQuery.Type != "file" || previewer.entryQuery.Search != "key" ||
		!previewer.entryQuery.Browse || previewer.entryQuery.Parent != "ssh" || previewer.entryQuery.Limit != 25 {
		t.Fatalf("query=%+v", previewer.entryQuery)
	}
}

func TestDraftScopeRulesPreviewInPlaceAndPersistOnlyAfterExplicitSave(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-draft-rules")
	source := t.TempDir()
	draftResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks", map[string]any{
		"name": "draft rules", "kind": "directory", "repositoryId": "repo-draft-rules", "enabled": false,
		"directory": map[string]any{"path": source, "exclusions": []string{"old-rule"}},
		"retention": map[string]any{}, "resources": map[string]any{},
	}, cookie)
	var task domain.Task
	if err := json.Unmarshal(draftResponse.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	// Scope rules are commonly adjusted on an already enabled task. Saving the
	// reviewed rules must preserve that state while replacing its confirmation.
	task.Enabled = true
	task.UpdatedAt = time.Now().UTC()
	if err := resources.UpdateTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	previewer := &httpTaskPreviewer{storage: resources, now: time.Now}
	srv.taskPreviewer = previewer

	acceptedResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/scope-inventory", map[string]any{
		"exclusions": []string{"draft-rule"},
	}, cookie)
	if acceptedResponse.Code != http.StatusAccepted {
		t.Fatalf("accepted status=%d body=%s", acceptedResponse.Code, acceptedResponse.Body.String())
	}
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(acceptedResponse.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	operation, err := srv.operations.Wait(t.Context(), accepted.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != "success" || len(previewer.inventoryExclusions) != 1 || previewer.inventoryExclusions[0] != "draft-rule" {
		t.Fatalf("operation=%+v exclusions=%v", operation, previewer.inventoryExclusions)
	}
	unchanged, err := loadTask(t.Context(), resources, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := unchanged.Directory.Exclusions; len(got) != 1 || got[0] != "old-rule" {
		t.Fatalf("preview persisted task rules: %v", got)
	}

	previewID := operation.Detail["previewId"].(string)
	savedResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/scope-rules", map[string]any{
		"previewId": previewID, "exclusions": []string{"draft-rule"},
	}, cookie)
	if savedResponse.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", savedResponse.Code, savedResponse.Body.String())
	}
	saved, err := loadTask(t.Context(), resources, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.Directory.Exclusions; len(got) != 1 || got[0] != "draft-rule" {
		t.Fatalf("saved rules=%v", got)
	}
	if saved.Enabled != task.Enabled || saved.ScopeConfirmation.PreviewID != previewID {
		t.Fatalf("saved task=%+v", saved)
	}
}

func TestScopeRuleSaveRejectsRulesThatDoNotMatchPreview(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-rule-mismatch")
	source := t.TempDir()
	response := requestJSON(t, srv, http.MethodPost, "/api/tasks", map[string]any{
		"name": "rule mismatch", "kind": "directory", "repositoryId": "repo-rule-mismatch", "enabled": false,
		"directory": map[string]any{"path": source, "exclusions": []string{"old-rule"}},
		"retention": map[string]any{}, "resources": map[string]any{},
	}, cookie)
	var task domain.Task
	_ = json.Unmarshal(response.Body.Bytes(), &task)
	proposed := task
	proposed.Directory = &domain.DirectorySource{Path: source, Exclusions: []string{"previewed-rule"}}
	fingerprint, _ := taskpreview.Fingerprint(proposed)
	now := time.Now().UTC()
	preview := store.TaskScopePreview{
		ID: "preview-rule-mismatch", TaskID: task.ID, Fingerprint: fingerprint, Summary: map[string]any{},
		CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	if err := resources.CreateTaskScopePreview(t.Context(), preview); err != nil {
		t.Fatal(err)
	}

	save := requestJSON(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/scope-rules", map[string]any{
		"previewId": preview.ID, "exclusions": []string{"different-rule"},
	}, cookie)
	if save.Code != http.StatusConflict {
		t.Fatalf("save status=%d body=%s", save.Code, save.Body.String())
	}
	unchanged, _ := loadTask(t.Context(), resources, task.ID)
	if got := unchanged.Directory.Exclusions; len(got) != 1 || got[0] != "old-rule" {
		t.Fatalf("mismatched preview changed rules: %v", got)
	}
}

type httpTaskPreviewer struct {
	storage             *store.Store
	now                 func() time.Time
	deletePreview       bool
	entryQuery          taskpreview.EntryQuery
	inventoryCalled     bool
	inventoryExclusions []string
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

func (p *httpTaskPreviewer) Inventory(ctx context.Context, taskID string) (store.TaskScopePreview, error) {
	p.inventoryCalled = true
	return p.Preview(ctx, taskID)
}

func (p *httpTaskPreviewer) InventoryWithExclusions(ctx context.Context, taskID string, exclusions []string) (store.TaskScopePreview, error) {
	p.inventoryCalled = true
	p.inventoryExclusions = append([]string(nil), exclusions...)
	tasks, err := p.storage.ListTasks(ctx)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	var task domain.Task
	for _, candidate := range tasks {
		if candidate.ID == taskID {
			task = candidate
			break
		}
	}
	if task.Directory != nil {
		copy := *task.Directory
		copy.Exclusions = append([]string(nil), exclusions...)
		task.Directory = &copy
	} else if task.Rsync != nil {
		copy := *task.Rsync
		copy.Exclusions = append([]string(nil), exclusions...)
		task.Rsync = &copy
	}
	fingerprint, err := taskpreview.Fingerprint(task)
	if err != nil {
		return store.TaskScopePreview{}, err
	}
	now := p.now().UTC()
	preview := store.TaskScopePreview{
		ID: "http-preview-draft-" + taskID, TaskID: taskID, Fingerprint: fingerprint,
		Summary:                    map[string]any{"includedFiles": 7, "excludedFiles": 3, "entriesAvailable": true},
		RequiresDeleteConfirmation: p.deletePreview, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	return preview, p.storage.CreateTaskScopePreview(ctx, preview)
}

func (p *httpTaskPreviewer) Entries(_ context.Context, _ string, query taskpreview.EntryQuery) (taskpreview.EntryPage, error) {
	p.entryQuery = query
	return taskpreview.EntryPage{Items: []taskpreview.EntryItem{{
		ScopeEntry: agentfilesystem.ScopeEntry{
			Ordinal: 1, Path: "ssh/private.key", Type: agentfilesystem.ScopeRegularFile,
			Disposition: agentfilesystem.ScopeUnreadable, ReasonCode: agentfilesystem.ScopeReasonPermission,
		},
	}}}, nil
}

func containsJSONField(encoded []byte, key string, want any) bool {
	var value map[string]any
	if json.Unmarshal(encoded, &value) != nil {
		return false
	}
	return value[key] == want
}

var _ taskScopePreviewer = (*httpTaskPreviewer)(nil)
var _ taskScopeInventorier = (*httpTaskPreviewer)(nil)
var _ taskScopeDraftInventorier = (*httpTaskPreviewer)(nil)
var _ taskScopeEntryReader = (*httpTaskPreviewer)(nil)
