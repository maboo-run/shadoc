package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestVaultEncryptsAndRejectsTampering(t *testing.T) {
	v, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}

	ciphertext, err := v.Seal("repository-password", []byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(ciphertext, []byte("correct horse battery staple")) {
		t.Fatal("ciphertext contains plaintext")
	}

	plaintext, err := v.Open("repository-password", ciphertext)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(plaintext) != "correct horse battery staple" {
		t.Fatalf("plaintext = %q", plaintext)
	}

	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 1
	if _, err := v.Open("repository-password", tampered); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
	if _, err := v.Open("ssh-private-key", ciphertext); err == nil {
		t.Fatal("ciphertext must be bound to its purpose")
	}
}

func TestLoadOrCreateKeyIsStableAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.key")
	first, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	second, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("reload key: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("reloaded key differs")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o", info.Mode().Perm())
	}
}
