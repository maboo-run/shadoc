package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAgentServiceSettingsAreOptionalAndPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restic-control.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, exists, err := s.LoadAgentServiceSettings(t.Context()); err != nil || exists {
		t.Fatalf("initial settings: exists=%v err=%v", exists, err)
	}
	want := AgentServiceSettings{
		Enabled: true, ListenHost: "0.0.0.0", Port: 10443,
		AdvertisedHost: "control.lan", TLSNames: []string{"control.lan", "192.168.1.20"},
	}
	if err := s.SaveAgentServiceSettings(t.Context(), want, time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, exists, err := s.LoadAgentServiceSettings(t.Context())
	if err != nil || !exists {
		t.Fatalf("saved settings: exists=%v err=%v", exists, err)
	}
	if !got.Enabled || got.ListenHost != want.ListenHost || got.Port != want.Port || got.AdvertisedHost != want.AdvertisedHost || len(got.TLSNames) != 2 {
		t.Fatalf("settings=%+v", got)
	}
}
