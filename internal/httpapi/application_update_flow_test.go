package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/appinstall"
	"github.com/maboo-run/shadoc/internal/auth"
	"github.com/maboo-run/shadoc/internal/store"
)

type applicationReleaseCatalogStub struct {
	info appinstall.ReleaseInfo
	err  error
}

func (s applicationReleaseCatalogStub) Latest(context.Context) (appinstall.ReleaseInfo, error) {
	return s.info, s.err
}

type applicationUpdateLauncherStub struct {
	managed   bool
	err       error
	operation string
	version   string
}

func (s *applicationUpdateLauncherStub) Managed() bool { return s.managed }
func (s *applicationUpdateLauncherStub) Launch(_ context.Context, operationID, version string) error {
	s.operation, s.version = operationID, version
	return s.err
}

func newApplicationUpdateTestServer(t *testing.T, launcher *applicationUpdateLauncherStub) (*Server, *store.Store) {
	t.Helper()
	storage, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	releases := applicationReleaseCatalogStub{info: appinstall.ReleaseInfo{
		Version: "v1.3.0", PublishedAt: time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC),
		Summary: "Security and reliability update", Compatible: true, Platform: "darwin_arm64",
	}}
	server := NewWithRuntime(storage, auth.New(storage, time.Now), nil, Runtime{
		ApplicationVersion: "v1.2.0", ApplicationReleases: releases, ApplicationUpdater: launcher,
	})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	return server, storage
}

func TestApplicationReleaseAndProtectedUpdateFlow(t *testing.T) {
	launcher := &applicationUpdateLauncherStub{managed: true}
	server, storage := newApplicationUpdateTestServer(t, launcher)
	cookie := setupSession(t, server)

	release := requestJSON(t, server, http.MethodGet, "/api/application/releases", nil, cookie)
	if release.Code != http.StatusOK || !strings.Contains(release.Body.String(), `"version":"v1.3.0"`) || !strings.Contains(release.Body.String(), `"managed":true`) || strings.Contains(release.Body.String(), "browser_download_url") {
		t.Fatalf("release=%d %s", release.Code, release.Body.String())
	}
	wrongPassword := requestJSON(t, server, http.MethodPost, "/api/application/update", map[string]any{
		"version": "v1.3.0", "administratorPassword": "wrong", "impactConfirmed": true,
	}, cookie)
	if wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password=%d %s", wrongPassword.Code, wrongPassword.Body.String())
	}
	unconfirmed := requestJSON(t, server, http.MethodPost, "/api/application/update", map[string]any{
		"version": "v1.3.0", "administratorPassword": "correct horse battery staple", "impactConfirmed": false,
	}, cookie)
	if unconfirmed.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed=%d %s", unconfirmed.Code, unconfirmed.Body.String())
	}
	nonLatest := requestJSON(t, server, http.MethodPost, "/api/application/update", map[string]any{
		"version": "v9.9.9", "administratorPassword": "correct horse battery staple", "impactConfirmed": true,
	}, cookie)
	if nonLatest.Code != http.StatusConflict {
		t.Fatalf("non-latest=%d %s", nonLatest.Code, nonLatest.Body.String())
	}

	started := requestJSON(t, server, http.MethodPost, "/api/application/update", map[string]any{
		"version": "v1.3.0", "administratorPassword": "correct horse battery staple", "impactConfirmed": true,
	}, cookie)
	if started.Code != http.StatusAccepted {
		t.Fatalf("started=%d %s", started.Code, started.Body.String())
	}
	var accepted struct {
		OperationID        string `json:"operationId"`
		Kind               string `json:"kind"`
		ExpectedDisconnect bool   `json:"expectedDisconnect"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &accepted); err != nil || !appinstall.ValidManagedOperationID(accepted.OperationID) || accepted.Kind != "application_update" || !accepted.ExpectedDisconnect {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	if launcher.operation != accepted.OperationID || launcher.version != "v1.3.0" {
		t.Fatalf("launcher=%+v", launcher)
	}
	operation, err := storage.Operation(t.Context(), accepted.OperationID)
	if err != nil || operation.Status != "running" || operation.Stage != "launching_updater" || operation.Target != "v1.3.0" || operation.Detail["expectedDisconnect"] != true {
		t.Fatalf("operation=%+v err=%v", operation, err)
	}
	duplicate := requestJSON(t, server, http.MethodPost, "/api/application/update", map[string]any{
		"version": "v1.3.0", "administratorPassword": "correct horse battery staple", "impactConfirmed": true,
	}, cookie)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate=%d %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestApplicationUpdateLauncherFailureIsPersistedWithoutRawError(t *testing.T) {
	launcher := &applicationUpdateLauncherStub{managed: true, err: errors.New("launch leaked-token-123")}
	server, storage := newApplicationUpdateTestServer(t, launcher)
	cookie := setupSession(t, server)
	response := requestJSON(t, server, http.MethodPost, "/api/application/update", map[string]any{
		"version": "v1.3.0", "administratorPassword": "correct horse battery staple", "impactConfirmed": true,
	}, cookie)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "leaked-token-123") {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	operation, err := storage.Operation(t.Context(), launcher.operation)
	if err != nil || operation.Status != "failed" || strings.Contains(operation.ErrorSummary, "leaked-token-123") {
		t.Fatalf("operation=%+v err=%v", operation, err)
	}
}

func TestApplicationUpdateRejectsDowngradeFromANewerInstalledVersion(t *testing.T) {
	launcher := &applicationUpdateLauncherStub{managed: true}
	server, _ := newApplicationUpdateTestServer(t, launcher)
	server.applicationVersion = "v2.0.0"
	cookie := setupSession(t, server)
	response := requestJSON(t, server, http.MethodPost, "/api/application/update", map[string]any{
		"version": "v1.3.0", "administratorPassword": "correct horse battery staple", "impactConfirmed": true,
	}, cookie)
	if response.Code != http.StatusConflict || launcher.operation != "" {
		t.Fatalf("downgrade=%d %s launcher=%+v", response.Code, response.Body.String(), launcher)
	}
}
