package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/servicemigration"
)

func TestValidateMigrationExecutableUsesOnlyTheLocalIntegrityRecord(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "shadoc")
	content := []byte("installed-release")
	if err := os.WriteFile(binary, content, 0o755); err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(content)
	record := []byte(hex.EncodeToString(checksum[:]) + "\n")
	if err := os.WriteFile(binary+".sha256", record, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationExecutable(binary); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationExecutable(binary); err == nil {
		t.Fatal("changed installed program passed local integrity validation")
	}
}

func TestMigrateToRootCommandReportsDestructiveSourceCleanup(t *testing.T) {
	migrator := &rootMigratorFake{result: servicemigration.Result{TargetDataDir: "/var/lib/shadoc", SourceRemoved: true}}
	var stdout bytes.Buffer
	handled, err := handleMigrateToRootCommand(context.Background(), []string{"migrate-to-root"}, &stdout, migrator)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if !strings.Contains(stdout.String(), "original user service and data were deleted") {
		t.Fatalf("output=%q", stdout.String())
	}
	if migrator.calls != 1 {
		t.Fatalf("migration calls=%d", migrator.calls)
	}
}

func TestResolveSudoSourceUserRequiresOneMatchingNonRootAccount(t *testing.T) {
	getenv := func(key string) string {
		return map[string]string{"SUDO_USER": "backup", "SUDO_UID": "1001"}[key]
	}
	lookup := func(uid string) (*user.User, error) {
		return &user.User{Uid: uid, Username: "backup", HomeDir: "/home/backup"}, nil
	}
	got, err := resolveSudoSourceUser(getenv, lookup)
	if err != nil || got.username != "backup" || got.uid != 1001 || got.home != "/home/backup" {
		t.Fatalf("source=%+v err=%v", got, err)
	}
	for _, values := range []map[string]string{
		{"SUDO_USER": "root", "SUDO_UID": "0"},
		{"SUDO_USER": "other", "SUDO_UID": "1001"},
		{"SUDO_USER": "bad@machine", "SUDO_UID": "1001"},
	} {
		_, err := resolveSudoSourceUser(func(key string) string { return values[key] }, lookup)
		if err == nil {
			t.Fatalf("unsafe sudo identity accepted: %v", values)
		}
	}
}

func TestMigrationRuntimeRequiresRootLinux(t *testing.T) {
	if err := validateMigrationRuntime("linux", 0); err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationRuntime("linux", 1000); err == nil {
		t.Fatal("unprivileged migration accepted")
	}
	if err := validateMigrationRuntime("darwin", 0); err == nil {
		t.Fatal("macOS migration accepted")
	}
}

type rootMigratorFake struct {
	result servicemigration.Result
	err    error
	calls  int
}

func (m *rootMigratorFake) Migrate(context.Context) (servicemigration.Result, error) {
	m.calls++
	return m.result, m.err
}

type migrationServiceManagerFake struct {
	status string
	err    error
}

func (m migrationServiceManagerFake) Start(string, []string) error { return m.err }
func (m migrationServiceManagerFake) Status() (string, error)      { return m.status, m.err }
func (m migrationServiceManagerFake) Uninstall() error             { return m.err }

type migrationHealthFake struct{ err error }

func (h migrationHealthFake) Wait(context.Context, string) error { return h.err }

func TestRootMigrationTargetRejectsAnExistingSystemService(t *testing.T) {
	target := &rootMigrationTarget{
		services:   migrationServiceManagerFake{status: "stopped"},
		health:     migrationHealthFake{},
		loadState:  func() (string, error) { return "loaded", nil },
		unitExists: func(string) (bool, error) { return false, nil },
	}
	if err := target.Prepare(); err == nil {
		t.Fatal("existing system service accepted")
	}
	target.services = migrationServiceManagerFake{status: "not installed"}
	target.loadState = func() (string, error) { return "not-found", nil }
	if err := target.Prepare(); err != nil {
		t.Fatal(err)
	}
	target.services = migrationServiceManagerFake{status: "running"}
	target.health = migrationHealthFake{err: errors.New("unhealthy")}
	if err := target.Healthy(context.Background(), "127.0.0.1:8585"); err == nil {
		t.Fatal("health failure hidden")
	}
}
