package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/auth"
	"github.com/maboo-run/shadoc/internal/lifecycle"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestLifecyclePolicyCanBeSavedAndCleanupRunManually(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	manager := auth.New(s, time.Now)
	lifecycleService := lifecycle.New(s)
	srv := NewWithRuntime(s, manager, nil, Runtime{Lifecycle: lifecycleService})
	setup := requestJSON(t, srv, http.MethodPost, "/api/setup", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	cookie := sessionCookie(t, setup)
	cookie.Raw = setup.Header().Get("X-CSRF-Token")

	get := requestJSON(t, srv, http.MethodGet, "/api/lifecycle-policy", nil, cookie)
	var initial lifecycle.Policy
	if get.Code != http.StatusOK || json.Unmarshal(get.Body.Bytes(), &initial) != nil || initial.RunDays != 365 || initial.RawLogDays != 30 {
		t.Fatalf("get status=%d body=%s policy=%+v", get.Code, get.Body.String(), initial)
	}
	invalid := requestJSON(t, srv, http.MethodPut, "/api/lifecycle-policy", lifecycle.Policy{RunDays: -1}, cookie)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	wanted := lifecycle.Policy{RunDays: 90, RawLogDays: 7, AuditDays: 180, RawLogMaxBytes: 64 << 20}
	saved := requestJSON(t, srv, http.MethodPut, "/api/lifecycle-policy", wanted, cookie)
	if saved.Code != http.StatusNoContent {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	preview := requestJSON(t, srv, http.MethodPost, "/api/lifecycle/cleanup/preview", map[string]any{}, cookie)
	var previewReport lifecycle.Report
	if preview.Code != http.StatusOK || json.Unmarshal(preview.Body.Bytes(), &previewReport) != nil {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	denied := requestJSON(t, srv, http.MethodPost, "/api/lifecycle/cleanup", map[string]any{"password": "wrong-password"}, cookie)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("denied status=%d body=%s", denied.Code, denied.Body.String())
	}
	cleanup := requestJSON(t, srv, http.MethodPost, "/api/lifecycle/cleanup", map[string]any{"password": "correct horse battery staple"}, cookie)
	var report lifecycle.Report
	if cleanup.Code != http.StatusOK || json.Unmarshal(cleanup.Body.Bytes(), &report) != nil || report.CompletedAt.IsZero() {
		t.Fatalf("cleanup status=%d body=%s report=%+v", cleanup.Code, cleanup.Body.String(), report)
	}
}
