package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/auth"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
)

func TestVaultCanRequireUnlockAfterRestartWhileLoginRemainsAvailable(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	master := bytes.Repeat([]byte{5}, 32)
	keyFile := vault.NewKeyFile(filepath.Join(dir, "vault.key"))
	if err := keyFile.SaveAutomatic(master); err != nil {
		t.Fatal(err)
	}
	gate := secret.NewGate(s, time.Now)
	if err := gate.Unlock(master); err != nil {
		t.Fatal(err)
	}
	controller := secret.NewVaultController(keyFile, gate, vault.Automatic)
	manager := auth.New(s, time.Now)
	srv := NewWithRuntime(s, manager, gate, Runtime{Vault: controller})

	setup := requestJSON(t, srv, http.MethodPost, "/api/setup", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup status=%d body=%s", setup.Code, setup.Body.String())
	}
	cookie := sessionCookie(t, setup)
	cookie.Raw = setup.Header().Get("X-CSRF-Token")

	lock := requestJSON(t, srv, http.MethodPost, "/api/vault/lock-on-restart", map[string]string{
		"passphrase": "long vault passphrase",
	}, cookie)
	if lock.Code != http.StatusNoContent {
		t.Fatalf("lock status=%d body=%s", lock.Code, lock.Body.String())
	}
	gate.Lock()

	status := requestJSON(t, srv, http.MethodGet, "/api/vault/status", nil, cookie)
	var got secret.VaultStatus
	if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &got) != nil || !got.Locked || got.Mode != "lock-on-restart" {
		t.Fatalf("status=%d body=%s decoded=%+v", status.Code, status.Body.String(), got)
	}
	if blocked := requestJSON(t, srv, http.MethodGet, "/api/dashboard", nil, cookie); blocked.Code != http.StatusLocked {
		t.Fatalf("dashboard while locked status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	if compatibility := requestJSON(t, srv, http.MethodGet, "/api/compatibility", nil, cookie); compatibility.Code != http.StatusOK {
		t.Fatalf("compatibility while locked status=%d body=%s", compatibility.Code, compatibility.Body.String())
	}
	if diagnostics := requestJSON(t, srv, http.MethodGet, "/api/diagnostics/export", nil, cookie); diagnostics.Code != http.StatusOK {
		t.Fatalf("diagnostic export while locked status=%d body=%s", diagnostics.Code, diagnostics.Body.String())
	}
	login := requestJSON(t, srv, http.MethodPost, "/api/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("login while locked status=%d body=%s", login.Code, login.Body.String())
	}
	wrongUnlock := requestJSON(t, srv, http.MethodPost, "/api/vault/unlock", map[string]string{"passphrase": "wrong vault passphrase"}, cookie)
	if wrongUnlock.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong unlock status=%d body=%s", wrongUnlock.Code, wrongUnlock.Body.String())
	}
	time.Sleep(1100 * time.Millisecond)
	unlock := requestJSON(t, srv, http.MethodPost, "/api/vault/unlock", map[string]string{
		"passphrase": "long vault passphrase",
	}, cookie)
	if unlock.Code != http.StatusNoContent {
		t.Fatalf("unlock status=%d body=%s", unlock.Code, unlock.Body.String())
	}
	if dashboard := requestJSON(t, srv, http.MethodGet, "/api/dashboard", nil, cookie); dashboard.Code != http.StatusOK {
		t.Fatalf("dashboard after unlock status=%d body=%s", dashboard.Code, dashboard.Body.String())
	}
	deniedAutomatic := requestJSON(t, srv, http.MethodPost, "/api/vault/automatic", map[string]any{"password": "wrong", "confirmed": true}, cookie)
	if deniedAutomatic.Code != http.StatusUnauthorized {
		t.Fatalf("denied automatic=%d %s", deniedAutomatic.Code, deniedAutomatic.Body.String())
	}
	automatic := requestJSON(t, srv, http.MethodPost, "/api/vault/automatic", map[string]any{"password": "correct horse battery staple", "confirmed": true}, cookie)
	if automatic.Code != http.StatusNoContent {
		t.Fatalf("automatic status=%d body=%s", automatic.Code, automatic.Body.String())
	}
	audits, err := s.ListAudits(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"vault.protection.enable": false, "vault.unlock.failure": false, "vault.unlock.success": false, "vault.protection.disable": false}
	for _, audit := range audits {
		if _, ok := wanted[audit.Action]; ok && audit.Actor == "admin" {
			wanted[audit.Action] = true
		}
	}
	for action, found := range wanted {
		if !found {
			t.Fatalf("missing %s audit: %+v", action, audits)
		}
	}
}
