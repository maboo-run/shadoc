package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/maboo-run/shadoc/internal/agentfilesystem"
	"github.com/maboo-run/shadoc/internal/auth"
	"github.com/maboo-run/shadoc/internal/compat"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/httpapi"
	repositoryservice "github.com/maboo-run/shadoc/internal/repository"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/taskpreview"
	"github.com/maboo-run/shadoc/internal/vault"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

type runner struct{}

func (runner) Run(context.Context, string, string, string) (store.RunRecord, error) {
	return store.RunRecord{}, nil
}

type repositories struct{ store *store.Store }

func (r repositories) Initialize(ctx context.Context, id string) error {
	return r.store.UpdateRepositoryStatus(ctx, id, "ready")
}
func (repositories) Snapshots(context.Context, string) ([]repositoryservice.Snapshot, error) {
	return []repositoryservice.Snapshot{{ID: "snapshot"}}, nil
}
func (repositories) Maintain(context.Context, string, domain.RetentionPolicy, bool) error { return nil }
func (repositories) RotatePassword(context.Context, string, string) error                 { return nil }
func (repositories) PasswordRotationStatus(context.Context, string) (repositoryservice.PasswordRotationStatus, error) {
	return repositoryservice.PasswordRotationStatus{}, nil
}
func (repositories) RevokeOldPassword(context.Context, string) error { return nil }
func (repositories) RestoreDirectory(context.Context, string, string, string, []string, int) error {
	return nil
}
func request(t *testing.T, server http.Handler, method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		value, _ := json.Marshal(body)
		reader = bytes.NewReader(value)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "127.0.0.1:12345"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}
func TestBlackBoxSetupCRUDMaintenanceDashboardAndAudit(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, _ := vault.New(bytes.Repeat([]byte{8}, 32))
	secrets := secret.New(s, v, time.Now)
	previewService := taskpreview.New(s, secrets, agentfilesystem.New("posix", []string{"/"}), nil, time.Now)
	server := httpapi.NewWithRuntime(s, auth.New(s, time.Now), secrets, httpapi.Runtime{Runner: runner{}, Repositories: repositories{store: s}, Paths: compat.ToolPaths{Restic: "/bin/echo"}, DataDir: t.TempDir(), TaskPreviewer: previewService})
	setup := request(t, server, "POST", "/api/setup", map[string]any{"username": "admin", "password": "correct horse battery staple"}, nil, "")
	if setup.Code != 201 {
		t.Fatalf("setup=%d %s", setup.Code, setup.Body.String())
	}
	cookie := setup.Result().Cookies()[0]
	csrf := setup.Header().Get("X-CSRF-Token")
	host := request(t, server, "POST", "/api/remote-hosts", map[string]any{"name": "nas", "host": "nas.local", "port": 22, "username": "backup", "privateKey": "key", "hostFingerprint": "nas.local ssh-ed25519 AAAA"}, cookie, csrf)
	if host.Code != 201 {
		t.Fatalf("host=%d %s", host.Code, host.Body.String())
	}
	var h struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(host.Body.Bytes(), &h)
	repo := request(t, server, "POST", "/api/repositories", map[string]any{"name": "photos", "remoteHostId": h.ID, "path": "/backup/photos", "password": "repository-password", "passwordConfirmed": true}, cookie, csrf)
	if repo.Code != 201 {
		t.Fatalf("repo=%d %s", repo.Code, repo.Body.String())
	}
	var rp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(repo.Body.Bytes(), &rp)
	initialized := request(t, server, "POST", "/api/repositories/"+rp.ID+"/initialize", map[string]any{}, cookie, csrf)
	if initialized.Code != http.StatusAccepted {
		t.Fatalf("initialize=%d %s", initialized.Code, initialized.Body.String())
	}
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(initialized.Body.Bytes(), &accepted)
	for deadline := time.Now().Add(time.Second); ; {
		operation := request(t, server, "GET", "/api/operations/"+accepted.OperationID, nil, cookie, "")
		if bytes.Contains(operation.Body.Bytes(), []byte(`"status":"success"`)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("initialize operation did not complete: %s", operation.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	sourceDirectory := t.TempDir()
	taskPayload := map[string]any{"name": "photos", "kind": "directory", "repositoryId": rp.ID, "directory": map[string]any{"path": sourceDirectory, "exclusions": []string{}, "skipIfUnchanged": true}, "retention": map[string]any{"keepWithinDays": 30}, "resources": map[string]any{"compression": "auto"}, "enabled": false}
	task := request(t, server, "POST", "/api/tasks", taskPayload, cookie, csrf)
	if task.Code != 201 {
		t.Fatalf("task=%d %s", task.Code, task.Body.String())
	}
	var createdTask domain.Task
	_ = json.Unmarshal(task.Body.Bytes(), &createdTask)
	scopePreviewResponse := request(t, server, "POST", "/api/tasks/"+createdTask.ID+"/preview", map[string]any{}, cookie, csrf)
	if scopePreviewResponse.Code != http.StatusCreated {
		t.Fatalf("scope preview=%d %s", scopePreviewResponse.Code, scopePreviewResponse.Body.String())
	}
	var scopePreview store.TaskScopePreview
	_ = json.Unmarshal(scopePreviewResponse.Body.Bytes(), &scopePreview)
	taskPayload["enabled"] = true
	taskPayload["previewId"] = scopePreview.ID
	task = request(t, server, "PUT", "/api/tasks/"+createdTask.ID, taskPayload, cookie, csrf)
	if task.Code != http.StatusOK {
		t.Fatalf("enable task=%d %s", task.Code, task.Body.String())
	}
	maintenancePreview := request(t, server, "POST", "/api/repositories/"+rp.ID+"/maintenance", map[string]any{"retention": map[string]any{"keepWithinDays": 30}, "dryRun": true}, cookie, csrf)
	var preview struct {
		ID string `json:"previewId"`
	}
	_ = json.Unmarshal(maintenancePreview.Body.Bytes(), &preview)
	maintenance := request(t, server, "PUT", "/api/repositories/"+rp.ID+"/maintenance-policy", map[string]any{"schedule": map[string]any{"kind": "weekly", "dayOfWeek": 0, "timeOfDay": "03:00"}, "timezone": "Asia/Shanghai", "retention": map[string]any{"keepWithinDays": 30}, "enabled": true, "previewId": preview.ID}, cookie, csrf)
	if maintenance.Code != 200 {
		t.Fatalf("maintenance=%d %s", maintenance.Code, maintenance.Body.String())
	}
	policy := request(t, server, "GET", "/api/repositories/"+rp.ID+"/maintenance-policy", nil, cookie, "")
	if policy.Code != 200 || !bytes.Contains(policy.Body.Bytes(), []byte(`"keepWithinDays":30`)) {
		t.Fatalf("maintenance did not use bound task retention: %d %s", policy.Code, policy.Body.String())
	}
	for _, path := range []string{"/api/dashboard", "/api/audits", "/api/audits/export"} {
		rec := request(t, server, "GET", path, nil, cookie, "")
		if rec.Code != 200 {
			t.Fatalf("GET %s=%d %s", path, rec.Code, rec.Body.String())
		}
		if path == "/api/dashboard" && bytes.Contains(rec.Body.Bytes(), []byte(`"alerts":null`)) {
			t.Fatalf("dashboard returned null collection: %s", rec.Body.String())
		}
	}
}
