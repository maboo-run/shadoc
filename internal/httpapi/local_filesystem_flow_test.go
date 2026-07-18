package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/localfilesystem"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestLocalFilesystemSettingsBrowseAndDirectoryCreation(t *testing.T) {
	srv := newResourceTestServer(t)
	service, err := localfilesystem.New(t.Context(), srv.store.(*store.Store), "posix")
	if err != nil {
		t.Fatal(err)
	}
	srv.localFilesystem = service
	cookie := setupSession(t, srv)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "photos"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := requestJSON(t, srv, http.MethodPut, "/api/local-filesystem/settings", map[string]any{"roots": []string{root}}, cookie)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), root) {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	read := requestJSON(t, srv, http.MethodGet, "/api/local-filesystem/settings", nil, cookie)
	if read.Code != http.StatusOK || read.Body.String() != saved.Body.String() {
		t.Fatalf("read status=%d body=%s saved=%s", read.Code, read.Body.String(), saved.Body.String())
	}
	browse := requestJSON(t, srv, http.MethodPost, "/api/local-filesystem/browse", map[string]any{"path": root}, cookie)
	if browse.Code != http.StatusOK || !strings.Contains(browse.Body.String(), `"name":"photos"`) || strings.Contains(browse.Body.String(), "ignored.txt") {
		t.Fatalf("browse status=%d body=%s", browse.Code, browse.Body.String())
	}
	createdPath := filepath.Join(root, "new", "archive")
	created := requestJSON(t, srv, http.MethodPost, "/api/local-filesystem/directories", map[string]any{"path": createdPath}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if info, err := os.Stat(createdPath); err != nil || !info.IsDir() {
		t.Fatalf("created info=%v err=%v", info, err)
	}
	audits := requestJSON(t, srv, http.MethodGet, "/api/audits", nil, cookie)
	if !strings.Contains(audits.Body.String(), "local_filesystem.settings.update") || !strings.Contains(audits.Body.String(), "local_filesystem.create_directory") {
		t.Fatalf("audits=%s", audits.Body.String())
	}
}

func TestLocalFilesystemEndpointsEnforceSessionAndAllowedRoots(t *testing.T) {
	srv := newResourceTestServer(t)
	service, err := localfilesystem.New(t.Context(), srv.store.(*store.Store), "posix")
	if err != nil {
		t.Fatal(err)
	}
	srv.localFilesystem = service
	if response := requestJSON(t, srv, http.MethodGet, "/api/local-filesystem/settings", nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", response.Code)
	}
	cookie := setupSession(t, srv)
	root := t.TempDir()
	outside := t.TempDir()
	if response := requestJSON(t, srv, http.MethodPut, "/api/local-filesystem/settings", map[string]any{"roots": []string{root}}, cookie); response.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
	}
	denied := requestJSON(t, srv, http.MethodPost, "/api/local-filesystem/browse", map[string]any{"path": outside}, cookie)
	if denied.Code != http.StatusUnprocessableEntity || strings.Contains(denied.Body.String(), outside) {
		t.Fatalf("denied status=%d body=%s", denied.Code, denied.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(denied.Body.Bytes(), &response); err != nil || response["error"] == "" {
		t.Fatalf("error response=%v err=%v", response, err)
	}
}
