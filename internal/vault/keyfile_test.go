package vault

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWrappedKeyRequiresPassphraseAfterReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.key")
	file := NewKeyFile(path)
	master := bytes.Repeat([]byte{7}, 32)
	if err := file.SaveLocked(master, "vault passphrase"); err != nil {
		t.Fatal(err)
	}
	if got, state, err := file.Load(""); err != nil || state != Locked || got != nil {
		t.Fatalf("got=%x state=%s err=%v", got, state, err)
	}
	got, state, err := file.Load("vault passphrase")
	if err != nil || state != Unlocked || !bytes.Equal(got, master) {
		t.Fatalf("got=%x state=%s err=%v", got, state, err)
	}
	if _, _, err := file.Load("wrong passphrase"); !errors.Is(err, ErrInvalidPassphrase) {
		t.Fatalf("wrong passphrase err=%v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestAutomaticKeyRemainsBackwardCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.key")
	master := bytes.Repeat([]byte{9}, 32)
	if err := os.WriteFile(path, master, 0o600); err != nil {
		t.Fatal(err)
	}
	got, state, err := NewKeyFile(path).Load("")
	if err != nil || state != Automatic || !bytes.Equal(got, master) {
		t.Fatalf("got=%x state=%s err=%v", got, state, err)
	}
}

func TestSaveAutomaticReplacesWrappedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.key")
	file := NewKeyFile(path)
	master := bytes.Repeat([]byte{3}, 32)
	if err := file.SaveLocked(master, "vault passphrase"); err != nil {
		t.Fatal(err)
	}
	if err := file.SaveAutomatic(master); err != nil {
		t.Fatal(err)
	}
	got, state, err := file.Load("")
	if err != nil || state != Automatic || !bytes.Equal(got, master) {
		t.Fatalf("got=%x state=%s err=%v", got, state, err)
	}
}
