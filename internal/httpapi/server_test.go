package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/maboo-run/shadoc/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "restic-control.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return New(s)
}

func getJSON(t *testing.T, handler http.Handler, path string, target any) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec.Code
}

func TestHealthAndSetupStatus(t *testing.T) {
	srv := newTestServer(t)

	var health struct {
		Status string `json:"status"`
	}
	if status := getJSON(t, srv, "/api/health", &health); status != http.StatusOK {
		t.Fatalf("health status = %d", status)
	}
	if health.Status != "ok" {
		t.Fatalf("health status payload = %q", health.Status)
	}

	var setup struct {
		Initialized bool `json:"initialized"`
	}
	if status := getJSON(t, srv, "/api/setup/status", &setup); status != http.StatusOK {
		t.Fatalf("setup status = %d", status)
	}
	if setup.Initialized {
		t.Fatal("new service must require setup")
	}
}
