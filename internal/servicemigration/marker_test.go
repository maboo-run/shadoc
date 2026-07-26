package servicemigration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileMarkerStoreRoundTripsWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.json")
	store, err := NewFileMarkerStore(path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	want := Marker{
		Stage:             stageSourceDisabled,
		Source:            testInstallation(),
		CurrentExecutable: "/tmp/shadoc",
		TargetDataDir:     "/var/lib/shadoc",
		StagingDataDir:    "/var/lib/.shadoc-root-staging",
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("marker permissions=%o", info.Mode().Perm())
	}
	got, present, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !present || got.Stage != want.Stage || got.Source.Username != want.Source.Username || got.TargetDataDir != want.TargetDataDir {
		t.Fatalf("marker=%+v present=%t", got, present)
	}
	if err := store.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, present, err := store.Load(); err != nil || present {
		t.Fatalf("removed marker present=%t err=%v", present, err)
	}
}

func TestFileMarkerStoreRejectsMalformedOrOverexposedMarkers(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "unknown field", content: `{"stage":"prepared","unknown":true}`, mode: 0o600},
		{name: "trailing data", content: `{"stage":"prepared"} {}`, mode: 0o600},
		{name: "public marker", content: `{"stage":"prepared"}`, mode: 0o644},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "migration.json")
			if err := os.WriteFile(path, []byte(test.content), test.mode); err != nil {
				t.Fatal(err)
			}
			store, err := NewFileMarkerStore(path, os.Geteuid())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.Load(); err == nil {
				t.Fatal("unsafe marker accepted")
			}
		})
	}
}
