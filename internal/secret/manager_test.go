package secret

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
)

func TestManagerPersistsEncryptedPurposeBoundSecrets(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "restic-control.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	v, err := vault.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	m := New(s, v, func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC) })

	id, err := m.Put(context.Background(), "ssh-private-key", []byte("PRIVATE KEY"))
	if err != nil {
		t.Fatalf("put secret: %v", err)
	}
	plaintext, err := m.Get(context.Background(), id, "ssh-private-key")
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(plaintext) != "PRIVATE KEY" {
		t.Fatalf("plaintext = %q", plaintext)
	}
	if _, err := m.Get(context.Background(), id, "repository-password"); err == nil {
		t.Fatal("wrong purpose must not decrypt secret")
	}
}
