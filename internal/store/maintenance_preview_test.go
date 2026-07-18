package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestMaintenancePreviewBindsRepositoryPolicyAndCanOnlyBeConsumedOnce(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 2, 0, 0, 0, time.UTC)
	if err := s.SaveSecret(ctx, "secret-1", "repository-password", []byte("ciphertext"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(ctx, domain.Repository{ID: "repo-1", Name: "repo", Kind: domain.LocalRepository, Path: "/backup/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "secret-1"); err != nil {
		t.Fatal(err)
	}
	preview := MaintenancePreview{
		ID: "preview-1", RepositoryID: "repo-1", Retention: domain.RetentionPolicy{KeepLast: 3},
		KeepCount: 3, RemoveCount: 2, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := s.CreateMaintenancePreview(ctx, preview); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.MaintenancePreview(ctx, preview.ID)
	if err != nil || loaded.RemoveCount != 2 || loaded.Retention.KeepLast != 3 || loaded.PolicyFingerprint != preview.Retention.Fingerprint() {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if _, err := s.ConsumeMaintenancePreview(ctx, preview.ID, "repo-2", preview.Retention, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-repository preview err=%v", err)
	}
	if _, err := s.ConsumeMaintenancePreview(ctx, preview.ID, "repo-1", domain.RetentionPolicy{KeepLast: 4}, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed-policy preview err=%v", err)
	}
	consumed, err := s.ConsumeMaintenancePreview(ctx, preview.ID, "repo-1", preview.Retention, now)
	if err != nil || consumed.ConsumedAt == nil {
		t.Fatalf("consumed=%+v err=%v", consumed, err)
	}
	if _, err := s.ConsumeMaintenancePreview(ctx, preview.ID, "repo-1", preview.Retention, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused preview err=%v", err)
	}
}

func TestMaintenancePreviewExpires(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 7, 12, 2, 0, 0, 0, time.UTC)
	if err := s.SaveSecret(context.Background(), "secret-1", "repository-password", []byte("ciphertext"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(context.Background(), domain.Repository{ID: "repo-1", Name: "repo", Kind: domain.LocalRepository, Path: "/backup/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "secret-1"); err != nil {
		t.Fatal(err)
	}
	preview := MaintenancePreview{ID: "preview-1", RepositoryID: "repo-1", Retention: domain.RetentionPolicy{KeepLast: 1}, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := s.CreateMaintenancePreview(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeMaintenancePreview(context.Background(), preview.ID, preview.RepositoryID, preview.Retention, now.Add(2*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired preview err=%v", err)
	}
}
