package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadUsesSafeDefaultsAndEnvironmentOverride(t *testing.T) {
	env := map[string]string{
		"RESTIC_CONTROL_DATA_DIR": "/srv/restic-control",
		"RESTIC_CONTROL_LISTEN":   "127.0.0.1:9090",
	}
	cfg, err := Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DataDir != "/srv/restic-control" {
		t.Fatalf("data dir = %q", cfg.DataDir)
	}
	if cfg.Listen != "127.0.0.1:9090" {
		t.Fatalf("listen = %q", cfg.Listen)
	}
	if cfg.DatabasePath != "/srv/restic-control/shadoc.db" {
		t.Fatalf("database path = %q", cfg.DatabasePath)
	}
	if cfg.VaultKeyPath != "/srv/restic-control/vault.key" {
		t.Fatalf("vault key path = %q", cfg.VaultKeyPath)
	}
}

func TestLoadReusesLegacyDatabaseInsideSelectedShadocDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	legacy := filepath.Join(dataDir, "restic-control.db")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(func(key string) string {
		if key == "SHADOC_DATA_DIR" {
			return dataDir
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabasePath != legacy {
		t.Fatalf("database path=%q", cfg.DatabasePath)
	}
}

func TestDefaultDataDirectoryReusesExistingLegacyInstallation(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "restic-control")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := defaultDataDirectory(base); got != legacy {
		t.Fatalf("legacy default data dir=%q", got)
	}
	current := filepath.Join(base, "shadoc")
	if err := os.MkdirAll(current, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := defaultDataDirectory(base); got != current {
		t.Fatalf("current default data dir=%q", got)
	}
}

func TestLoadPrefersShadocEnvironmentAndDefaultDirectory(t *testing.T) {
	env := map[string]string{
		"SHADOC_DATA_DIR":         "/srv/shadoc",
		"RESTIC_CONTROL_DATA_DIR": "/srv/legacy",
		"SHADOC_LISTEN":           "127.0.0.1:9090",
		"RESTIC_CONTROL_LISTEN":   "127.0.0.1:7070",
	}
	cfg, err := Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/srv/shadoc" || cfg.Listen != "127.0.0.1:9090" {
		t.Fatalf("config=%+v", cfg)
	}

	defaultBase := t.TempDir()
	defaults, err := load(func(string) string { return "" }, func() (string, error) { return defaultBase, nil })
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(defaults.DataDir) != "shadoc" {
		t.Fatalf("default data dir=%q", defaults.DataDir)
	}
}

func TestLoadDoesNotUseRemovedAgentEnvironmentControls(t *testing.T) {
	withoutOverride, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	removed := map[string]string{
		"RESTIC_CONTROL_AGENT_LISTEN":       "0.0.0.0:9443",
		"RESTIC_CONTROL_AGENT_TLS_NAMES":    "control.example",
		"RESTIC_CONTROL_AGENT_ARTIFACT_DIR": "/tmp/should-not-control-agent-artifacts",
	}
	withRemovedOverride, err := Load(func(key string) string {
		return removed[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withRemovedOverride, withoutOverride) {
		t.Fatalf("removed Agent environment controls still change config: with=%+v without=%+v", withRemovedOverride, withoutOverride)
	}
}
