package secret

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
)

func TestGateRejectsSecretOperationsUntilUnlocked(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "restic-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gate := NewGate(s, time.Now)
	ctx := context.Background()
	if _, err := gate.Put(ctx, "repository-password", []byte("secret")); !errors.Is(err, ErrLocked) {
		t.Fatalf("put err=%v", err)
	}
	if _, err := gate.Get(ctx, "unknown", "repository-password"); !errors.Is(err, ErrLocked) {
		t.Fatalf("get err=%v", err)
	}
	if err := gate.Delete(ctx, "unknown"); !errors.Is(err, ErrLocked) {
		t.Fatalf("delete err=%v", err)
	}

	master := bytes.Repeat([]byte{4}, 32)
	if err := gate.Unlock(master); err != nil {
		t.Fatal(err)
	}
	id, err := gate.Put(ctx, "repository-password", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := gate.Get(ctx, id, "repository-password"); err != nil || string(got) != "secret" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	gate.Lock()
	if _, err := gate.Get(ctx, id, "repository-password"); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked get err=%v", err)
	}
}

func TestGateConcurrentUnlockAndReadIsSafe(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "restic-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gate := NewGate(s, time.Now)
	master := bytes.Repeat([]byte{6}, 32)
	if err := gate.Unlock(master); err != nil {
		t.Fatal(err)
	}
	id, err := gate.Put(context.Background(), "token", []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				gate.Lock()
				_ = gate.Unlock(master)
				_, err := gate.Get(context.Background(), id, "token")
				if err != nil && !errors.Is(err, ErrLocked) {
					t.Errorf("read err=%v", err)
				}
			}
		}()
	}
	wg.Wait()
}
