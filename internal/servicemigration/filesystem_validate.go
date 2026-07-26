package servicemigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/maboo-run/shadoc/internal/appinstall"
	"github.com/maboo-run/shadoc/internal/vault"
	_ "modernc.org/sqlite"
)

func validateStagedData(dataDir string, expectedUID int) error {
	if err := validateStagedRegularFile(filepath.Join(dataDir, "app", "shadoc"), expectedUID, 0o755); err != nil {
		return fmt.Errorf("validate staged Shadoc executable: %w", err)
	}
	if err := appinstall.VerifyInstalledBinary(filepath.Join(dataDir, "app", "shadoc")); err != nil {
		return fmt.Errorf("validate staged Shadoc integrity record: %w", err)
	}
	keyPath := filepath.Join(dataDir, "vault.key")
	if err := validateStagedRegularFile(keyPath, expectedUID, 0o600); err != nil {
		return fmt.Errorf("validate staged vault key: %w", err)
	}
	key, _, err := vault.NewKeyFile(keyPath).Load("")
	clear(key)
	if err != nil {
		return fmt.Errorf("validate staged vault key: %w", err)
	}
	databasePath := filepath.Join(dataDir, "shadoc.db")
	if _, err := os.Lstat(databasePath); errors.Is(err, os.ErrNotExist) {
		databasePath = filepath.Join(dataDir, "restic-control.db")
	}
	if err := validateStagedRegularFile(databasePath, expectedUID, 0o600); err != nil {
		return fmt.Errorf("validate staged database: %w", err)
	}
	if err := sqliteQuickCheck(databasePath); err != nil {
		return fmt.Errorf("validate staged database: %w", err)
	}
	return nil
}

func validateStagedRegularFile(path string, expectedUID int, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	uid, ownerOK := fileOwnerUID(info)
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode ||
		!ownerOK || uid != expectedUID {
		return fmt.Errorf("%s is not a root-owned regular file with mode %o", path, mode)
	}
	if err := validateMigratedMetadata(path, false); err != nil {
		return err
	}
	return nil
}

func sqliteQuickCheck(path string) error {
	dsn := (&url.URL{Scheme: "file", Path: filepath.Clean(path)}).String() + "?mode=ro"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var result string
	if err := database.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite quick_check returned %q", result)
	}
	return nil
}
