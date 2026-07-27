package servicemigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/maboo-run/shadoc/internal/vault"
	_ "modernc.org/sqlite"
)

func TestStagedDataValidationChecksExecutableVaultAndSQLite(t *testing.T) {
	dataDir := createValidStagedData(t)
	if err := validateStagedData(dataDir, os.Geteuid()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "shadoc.db"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedData(dataDir, os.Geteuid()); err == nil {
		t.Fatal("corrupt SQLite database accepted")
	}
}

func TestStagedDataValidationRejectsExecutableSymlink(t *testing.T) {
	dataDir := createValidStagedData(t)
	executable := filepath.Join(dataDir, "app", "shadoc")
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", executable); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedData(dataDir, os.Geteuid()); err == nil {
		t.Fatal("symbolic-link executable accepted")
	}
}

func createValidStagedData(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "app", "shadoc"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256([]byte("binary"))
	if err := os.WriteFile(
		filepath.Join(dataDir, "app", "shadoc.sha256"),
		[]byte(hex.EncodeToString(checksum[:])+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := vault.NewKeyFile(filepath.Join(dataDir, "vault.key")).SaveAutomatic(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "shadoc.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(context.Background(), "CREATE TABLE migration_test (id INTEGER PRIMARY KEY)"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dataDir, "shadoc.db"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dataDir
}
