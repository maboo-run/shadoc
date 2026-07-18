package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/auth"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestApplicationVersionIsAvailableToAdministrator(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := NewWithRuntime(s, auth.New(s, time.Now), nil, Runtime{ApplicationVersion: "1.2.3"})
	setup := requestJSON(t, srv, http.MethodPost, "/api/setup", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	cookie := sessionCookie(t, setup)
	response := requestJSON(t, srv, http.MethodGet, "/api/application/version", nil, cookie)
	var value struct {
		Version string `json:"version"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &value) != nil || value.Version != "1.2.3" {
		t.Fatalf("status=%d body=%s version=%+v", response.Code, response.Body.String(), value)
	}
}
