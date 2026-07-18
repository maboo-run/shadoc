package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
)

func TestResetAdministratorRevokesEverySession(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	manager := New(s, time.Now)
	ctx := context.Background()
	first, err := manager.Setup(ctx, "admin", "old-password-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Login(ctx, "admin", "old-password-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ResetPassword(ctx, "new-password-long-enough"); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first.Token, second.Token} {
		if _, err := manager.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("old session err=%v", err)
		}
	}
	if _, err := manager.Login(ctx, "admin", "old-password-long-enough"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password err=%v", err)
	}
	if _, err := manager.Login(ctx, "admin", "new-password-long-enough"); err != nil {
		t.Fatalf("new password err=%v", err)
	}
	audits, err := s.ListAudits(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		found = found || audit.Action == "administrator.password.reset" && audit.Actor == "local-admin-cli"
	}
	if !found {
		t.Fatalf("missing password reset audit: %+v", audits)
	}
}

func TestReauthenticateChecksCurrentAdministratorWithoutCreatingSession(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	manager := New(s, time.Now)
	ctx := context.Background()
	if _, err := manager.Setup(ctx, "admin", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reauthenticate(ctx, "admin", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password err=%v", err)
	}
	if err := manager.Reauthenticate(ctx, "admin", "correct-horse-battery-staple"); err != nil {
		t.Fatalf("reauthenticate err=%v", err)
	}
}
