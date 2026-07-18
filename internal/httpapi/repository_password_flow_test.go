package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	repositoryservice "github.com/maboo-run/shadoc/internal/repository"
)

type passwordRepositoryManager struct {
	repositoryManager
	rotateCalls int
	revokeCalls int
	status      repositoryservice.PasswordRotationStatus
}

func (m *passwordRepositoryManager) RotatePassword(context.Context, string, string) error {
	m.rotateCalls++
	m.status = repositoryservice.PasswordRotationStatus{Pending: true, OldKeyID: "old-key", CreatedAt: time.Now().UTC()}
	return nil
}

func (m *passwordRepositoryManager) PasswordRotationStatus(context.Context, string) (repositoryservice.PasswordRotationStatus, error) {
	return m.status, nil
}

func (m *passwordRepositoryManager) RevokeOldPassword(context.Context, string) error {
	m.revokeCalls++
	return nil
}

func TestRepositoryCreationRequiresPasswordStorageConfirmation(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	rejected := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
		"name": "photos", "kind": "local", "path": "/backup/photos", "password": "repository-password-long",
	}, cookie)
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", rejected.Code, rejected.Body.String())
	}
}

func TestRepositoryRotationRequiresConfirmationAndExposesPendingRevocation(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &passwordRepositoryManager{}
	srv.repositories = manager
	cookie := setupSession(t, srv)

	rejected := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/rotate-password", map[string]any{
		"password": "new-repository-password-long",
	}, cookie)
	if rejected.Code != http.StatusUnprocessableEntity || manager.rotateCalls != 0 {
		t.Fatalf("unconfirmed rotation status=%d calls=%d body=%s", rejected.Code, manager.rotateCalls, rejected.Body.String())
	}

	rotated := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/rotate-password", map[string]any{
		"password": "new-repository-password-long", "passwordConfirmed": true, "administratorPassword": "correct horse battery staple",
	}, cookie)
	if rotated.Code != http.StatusAccepted {
		t.Fatalf("confirmed rotation status=%d calls=%d body=%s", rotated.Code, manager.rotateCalls, rotated.Body.String())
	}
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(rotated.Body.Bytes(), &accepted); err != nil || accepted.OperationID == "" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	if manager.rotateCalls != 1 {
		t.Fatalf("rotate calls=%d", manager.rotateCalls)
	}
	if body := rotated.Body.String(); body == "" || containsSecret(body) {
		t.Fatalf("rotation response leaked password: %s", body)
	}

	status := requestJSON(t, srv, http.MethodGet, "/api/repositories/repo/password-rotation", nil, cookie)
	if status.Code != http.StatusOK || status.Body.String() == "" {
		t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
	}
	denied := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/revoke-old-password", map[string]any{"password": "wrong-password"}, cookie)
	if denied.Code != http.StatusUnauthorized || manager.revokeCalls != 0 {
		t.Fatalf("denied revoke status=%d calls=%d body=%s", denied.Code, manager.revokeCalls, denied.Body.String())
	}
	revoked := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/revoke-old-password", map[string]any{"password": "correct horse battery staple"}, cookie)
	if revoked.Code != http.StatusOK || manager.revokeCalls != 1 {
		t.Fatalf("revoke status=%d calls=%d body=%s", revoked.Code, manager.revokeCalls, revoked.Body.String())
	}
}

func containsSecret(value string) bool {
	return strings.Contains(value, "new-repository-password-long")
}
