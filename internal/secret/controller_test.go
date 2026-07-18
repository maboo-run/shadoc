package secret

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
)

func TestVaultControllerSwitchesRestartModeWithoutChangingMasterKey(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	file := vault.NewKeyFile(filepath.Join(dir, "vault.key"))
	master := bytes.Repeat([]byte{8}, 32)
	if err := file.SaveAutomatic(master); err != nil {
		t.Fatal(err)
	}
	gate := NewGate(s, time.Now)
	if err := gate.Unlock(master); err != nil {
		t.Fatal(err)
	}
	controller := NewVaultController(file, gate, vault.Automatic)
	if err := controller.LockOnRestart("long vault passphrase"); err != nil {
		t.Fatal(err)
	}
	if status := controller.Status(); status.Mode != "lock-on-restart" || status.Locked {
		t.Fatalf("status=%+v", status)
	}

	reloadedGate := NewGate(s, time.Now)
	reloaded := NewVaultController(file, reloadedGate, vault.Locked)
	if status := reloaded.Status(); !status.Locked || status.Mode != "lock-on-restart" {
		t.Fatalf("reloaded status=%+v", status)
	}
	if err := reloaded.Unlock("long vault passphrase"); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Automatic(); err != nil {
		t.Fatal(err)
	}
	got, state, err := file.Load("")
	if err != nil || state != vault.Automatic || !bytes.Equal(got, master) {
		t.Fatalf("got=%x state=%s err=%v", got, state, err)
	}
}

func TestVaultControllerRateLimitsConsecutiveUnlockFailures(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	file := vault.NewKeyFile(filepath.Join(dir, "vault.key"))
	if err := file.SaveLocked(bytes.Repeat([]byte{2}, 32), "correct vault passphrase"); err != nil {
		t.Fatal(err)
	}
	controller := NewVaultController(file, NewGate(s, time.Now), vault.Locked)
	if err := controller.Unlock("wrong passphrase"); !errors.Is(err, vault.ErrInvalidPassphrase) {
		t.Fatalf("first err=%v", err)
	}
	if err := controller.Unlock("another wrong passphrase"); !errors.Is(err, ErrUnlockRateLimited) {
		t.Fatalf("second err=%v", err)
	}
}
