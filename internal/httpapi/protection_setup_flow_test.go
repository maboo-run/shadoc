package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/protectionsetup"
	"github.com/maboo-run/shadoc/internal/store"
)

type flowRepositoryInitializer struct {
	storage *store.Store
	failFor string
}

func (i flowRepositoryInitializer) Initialize(ctx context.Context, id string) error {
	if id == i.failFor {
		return errors.New("internal command and password must not leak")
	}
	return i.storage.UpdateRepositoryStatus(ctx, id, "ready")
}

func attachProtectionSetup(t *testing.T, srv *Server, failFor string) {
	t.Helper()
	storage := srv.store.(*store.Store)
	srv.protectionSetup = protectionsetup.New(storage, srv.secrets, flowRepositoryInitializer{storage: storage, failFor: failFor}, func() time.Time {
		return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	}, nil)
}

func protectionTemplatePayload() map[string]any {
	return map[string]any{
		"name": "Daily database policy", "retention": map[string]any{"keepDaily": 7, "keepMonthly": 6},
		"resources": map[string]any{"compression": "auto"}, "health": map[string]any{"maxSuccessAgeHours": 30},
		"schedule": map[string]any{"kind": "daily", "timeOfDay": "02:00"}, "timezone": "UTC", "maxParallel": 2, "catchUpWindowMinutes": 120,
	}
}

func protectionDraftPayload(templateID, connectionID string) map[string]any {
	return map[string]any{
		"name": "Production databases", "templateId": templateID, "executionTarget": map[string]any{"kind": "local"}, "notificationMode": "configured",
		"items": []map[string]any{
			{"taskName": "Accounts", "database": map[string]any{"connectionId": connectionID, "database": "accounts"}, "repositoryName": "Accounts repository", "repositoryKind": "local", "repositoryPath": "/backup/accounts", "password": "accounts-secret", "passwordConfirmed": true},
			{"taskName": "Orders", "database": map[string]any{"connectionId": connectionID, "database": "orders"}, "repositoryName": "Orders repository", "repositoryKind": "local", "repositoryPath": "/backup/orders", "password": "orders-secret", "passwordConfirmed": true},
		},
	}
}

func createVerifiedBackupConnection(t *testing.T, srv *Server, cookie *http.Cookie) domain.DatabaseConnection {
	t.Helper()
	response := requestJSON(t, srv, http.MethodPost, "/api/database-connections", map[string]any{
		"name": "production", "engine": "mysql", "purpose": "backup", "network": "tcp", "host": "db.internal", "port": 3306,
		"username": "backup", "password": "database-secret-password", "tls": map[string]any{"mode": "preferred"},
		"toolPaths": map[string]string{"dump": "/usr/bin/mysqldump", "admin": "/usr/bin/mysql"},
	}, cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("create connection status=%d body=%s", response.Code, response.Body.String())
	}
	var item domain.DatabaseConnection
	if err := json.Unmarshal(response.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestProtectionSetupCreatesTemplateDraftAndItemizedResourcesWithoutLeakingSecrets(t *testing.T) {
	srv := newResourceTestServer(t)
	attachProtectionSetup(t, srv, "")
	cookie := setupSession(t, srv)
	connection := createVerifiedBackupConnection(t, srv, cookie)
	templateResponse := requestJSON(t, srv, http.MethodPost, "/api/protection-templates", protectionTemplatePayload(), cookie)
	if templateResponse.Code != http.StatusCreated {
		t.Fatalf("template status=%d body=%s", templateResponse.Code, templateResponse.Body.String())
	}
	var template protectionsetup.Template
	if err := json.Unmarshal(templateResponse.Body.Bytes(), &template); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"password", "privateKey", "repositoryId", "repositoryPath", "taskId"} {
		if strings.Contains(strings.ToLower(templateResponse.Body.String()), strings.ToLower(`"`+forbidden+`"`)) {
			t.Fatalf("template leaked binding %q: %s", forbidden, templateResponse.Body.String())
		}
	}
	draftResponse := requestJSON(t, srv, http.MethodPost, "/api/protection-drafts", protectionDraftPayload(template.ID, connection.ID), cookie)
	if draftResponse.Code != http.StatusCreated {
		t.Fatalf("draft status=%d body=%s", draftResponse.Code, draftResponse.Body.String())
	}
	if strings.Contains(draftResponse.Body.String(), "accounts-secret") || strings.Contains(draftResponse.Body.String(), "database-secret-password") || strings.Contains(draftResponse.Body.String(), "secret-") {
		t.Fatalf("draft response leaked a secret: %s", draftResponse.Body.String())
	}
	var draft protectionsetup.Draft
	if err := json.Unmarshal(draftResponse.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	if len(draft.Items) != 2 || draft.Items[0].RepositoryID == draft.Items[1].RepositoryID || !draft.Items[0].HasPassword {
		t.Fatalf("draft mapping=%+v", draft.Items)
	}
	accepted := requestJSON(t, srv, http.MethodPost, "/api/protection-drafts/"+draft.ID+"/apply", map[string]any{}, cookie)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("apply status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	var operation struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(accepted.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, srv, cookie, operation.OperationID, "success")
	loaded := requestJSON(t, srv, http.MethodGet, "/api/protection-drafts/"+draft.ID, nil, cookie)
	if loaded.Code != http.StatusOK || strings.Count(loaded.Body.String(), `"status":"ready"`) < 3 {
		t.Fatalf("loaded status=%d body=%s", loaded.Code, loaded.Body.String())
	}
	for resource, expected := range map[string]int{"repositories": 2, "tasks": 2, "plans": 1} {
		response := requestJSON(t, srv, http.MethodGet, "/api/"+resource, nil, cookie)
		var items []map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &items); err != nil || len(items) != expected {
			t.Fatalf("%s items=%v err=%v body=%s", resource, items, err, response.Body.String())
		}
		for _, item := range items {
			if enabled, exists := item["enabled"]; exists && enabled != false {
				t.Fatalf("%s must remain disabled pending explicit activation: %+v", resource, item)
			}
		}
	}
	checklist := requestJSON(t, srv, http.MethodGet, "/api/protection-drafts/"+draft.ID+"/checklist", nil, cookie)
	if checklist.Code != http.StatusOK || !strings.Contains(checklist.Body.String(), `"planStatus":"disabled"`) || !strings.Contains(checklist.Body.String(), `"notificationStatus":"not_configured"`) || strings.Count(checklist.Body.String(), `"maintenanceStatus":"pending_review"`) != 2 {
		t.Fatalf("checklist status=%d body=%s", checklist.Code, checklist.Body.String())
	}
}

func TestProtectionDraftCancelCleansUnownedCredentialsAndPartialFailureRemainsResumable(t *testing.T) {
	srv := newResourceTestServer(t)
	attachProtectionSetup(t, srv, "")
	cookie := setupSession(t, srv)
	connection := createVerifiedBackupConnection(t, srv, cookie)
	templateResponse := requestJSON(t, srv, http.MethodPost, "/api/protection-templates", protectionTemplatePayload(), cookie)
	var template protectionsetup.Template
	_ = json.Unmarshal(templateResponse.Body.Bytes(), &template)
	draftResponse := requestJSON(t, srv, http.MethodPost, "/api/protection-drafts", protectionDraftPayload(template.ID, connection.ID), cookie)
	var draft protectionsetup.Draft
	_ = json.Unmarshal(draftResponse.Body.Bytes(), &draft)
	cancelled := requestJSON(t, srv, http.MethodPost, "/api/protection-drafts/"+draft.ID+"/cancel", map[string]any{}, cookie)
	if cancelled.Code != http.StatusOK || !strings.Contains(cancelled.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	if response := requestJSON(t, srv, http.MethodPost, "/api/protection-drafts/"+draft.ID+"/apply", map[string]any{}, cookie); response.Code != http.StatusConflict {
		t.Fatalf("cancelled apply status=%d body=%s", response.Code, response.Body.String())
	}

	secondResponse := requestJSON(t, srv, http.MethodPost, "/api/protection-drafts", func() map[string]any {
		payload := protectionDraftPayload(template.ID, connection.ID)
		payload["name"] = "Partial databases"
		items := payload["items"].([]map[string]any)
		items[0]["repositoryName"], items[0]["repositoryPath"] = "Partial Accounts repository", "/backup/partial-accounts"
		items[1]["repositoryName"], items[1]["repositoryPath"] = "Partial Orders repository", "/backup/partial-orders"
		return payload
	}(), cookie)
	var second protectionsetup.Draft
	_ = json.Unmarshal(secondResponse.Body.Bytes(), &second)
	attachProtectionSetup(t, srv, second.Items[0].RepositoryID)
	accepted := requestJSON(t, srv, http.MethodPost, "/api/protection-drafts/"+second.ID+"/apply", map[string]any{}, cookie)
	var operation struct {
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(accepted.Body.Bytes(), &operation)
	waitForOperation(t, srv, cookie, operation.OperationID, "failed")
	partial := requestJSON(t, srv, http.MethodGet, "/api/protection-drafts/"+second.ID, nil, cookie)
	if !strings.Contains(partial.Body.String(), `"status":"partial"`) || !strings.Contains(partial.Body.String(), "独立仓库初始化失败") || strings.Contains(partial.Body.String(), "password must not leak") {
		t.Fatalf("partial body=%s", partial.Body.String())
	}
}

func TestProtectionSetupEndpointsRequireSession(t *testing.T) {
	srv := newResourceTestServer(t)
	attachProtectionSetup(t, srv, "")
	for method, path := range map[string]string{http.MethodGet: "/api/protection-drafts", http.MethodPost: "/api/protection-templates"} {
		if response := requestJSON(t, srv, method, path, map[string]any{}, nil); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d", method, path, response.Code)
		}
	}
}

func TestProtectionTemplateDeletionRequiresVersionedPreview(t *testing.T) {
	srv := newResourceTestServer(t)
	attachProtectionSetup(t, srv, "")
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/protection-templates", protectionTemplatePayload(), cookie)
	var template protectionsetup.Template
	if err := json.Unmarshal(created.Body.Bytes(), &template); err != nil {
		t.Fatal(err)
	}
	if response := requestJSON(t, srv, http.MethodDelete, "/api/protection-templates/"+template.ID, nil, cookie); response.Code != http.StatusPreconditionRequired {
		t.Fatalf("direct delete status=%d body=%s", response.Code, response.Body.String())
	}
	preview := requestJSON(t, srv, http.MethodGet, "/api/delete-previews/protection-templates/"+template.ID, nil, cookie)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	var details store.ResourceDeletePreview
	if err := json.Unmarshal(preview.Body.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	confirmed := requestJSON(t, srv, http.MethodPost, "/api/delete-previews/protection-templates/"+template.ID+"/confirm", map[string]any{"expectedUpdatedAt": details.UpdatedAt}, cookie)
	if confirmed.Code != http.StatusNoContent {
		t.Fatalf("confirmed status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
}
