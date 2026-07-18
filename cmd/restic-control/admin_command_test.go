package main

import (
	"context"
	"errors"
	"testing"
)

type fakePasswordReader struct {
	values [][]byte
	calls  int
}

func (f *fakePasswordReader) ReadPassword(string) ([]byte, error) {
	if f.calls >= len(f.values) {
		return nil, errors.New("unexpected password read")
	}
	value := append([]byte(nil), f.values[f.calls]...)
	f.calls++
	return value, nil
}

type fakePasswordResetter struct{ password string }

func (f *fakePasswordResetter) ResetPassword(_ context.Context, password string) error {
	f.password = password
	return nil
}

func TestResetAdminPasswordReadsTwiceWithoutAcceptingPasswordArgument(t *testing.T) {
	reader := &fakePasswordReader{values: [][]byte{[]byte("new-password-long-enough"), []byte("new-password-long-enough")}}
	resetter := &fakePasswordResetter{}
	handled, err := handleAdminCommand(context.Background(), []string{"reset-admin-password"}, reader, resetter)
	if err != nil || !handled || reader.calls != 2 || resetter.password != "new-password-long-enough" {
		t.Fatalf("handled=%v err=%v calls=%d password=%q", handled, err, reader.calls, resetter.password)
	}
	if _, err := handleAdminCommand(context.Background(), []string{"reset-admin-password", "secret-in-argv"}, reader, resetter); err == nil {
		t.Fatal("password argument accepted")
	}
}

func TestResetAdminPasswordRejectsMismatch(t *testing.T) {
	reader := &fakePasswordReader{values: [][]byte{[]byte("new-password-long-enough"), []byte("different-password-long")}}
	resetter := &fakePasswordResetter{}
	handled, err := handleAdminCommand(context.Background(), []string{"reset-admin-password"}, reader, resetter)
	if !handled || err == nil || resetter.password != "" {
		t.Fatalf("handled=%v err=%v password=%q", handled, err, resetter.password)
	}
}
