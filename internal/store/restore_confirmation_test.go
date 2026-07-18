package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoreConfirmationRequiresAuthorizationAndExactFingerprint(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 4, 0, 0, 0, time.UTC)
	record := RestoreConfirmation{ID: "restore-1", Actor: "admin", Kind: "directory", Fingerprint: "hash-a", Summary: map[string]any{"target": "…/restore"}, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	if err := s.CreateRestoreConfirmation(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeRestoreConfirmation(ctx, record.ID, record.Actor, record.Fingerprint, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("unauthorized err=%v", err)
	}
	if err := s.AuthorizeRestoreConfirmation(ctx, record.ID, "someone-else", now); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong actor err=%v", err)
	}
	if err := s.AuthorizeRestoreConfirmation(ctx, record.ID, record.Actor, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeRestoreConfirmation(ctx, record.ID, record.Actor, "hash-b", now); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request err=%v", err)
	}
	consumed, err := s.ConsumeRestoreConfirmation(ctx, record.ID, record.Actor, record.Fingerprint, now)
	if err != nil || consumed.ConsumedAt == nil {
		t.Fatalf("consumed=%+v err=%v", consumed, err)
	}
	if _, err := s.ConsumeRestoreConfirmation(ctx, record.ID, record.Actor, record.Fingerprint, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused err=%v", err)
	}
}

func TestRestoreConfirmationAuthorizationExpiresWithFlow(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 7, 12, 4, 0, 0, 0, time.UTC)
	record := RestoreConfirmation{ID: "restore-1", Actor: "admin", Kind: "database", Fingerprint: "hash", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := s.CreateRestoreConfirmation(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeRestoreConfirmation(context.Background(), record.ID, record.Actor, now.Add(2*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired authorize err=%v", err)
	}
}
