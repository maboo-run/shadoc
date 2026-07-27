//go:build linux

package servicemigration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileLockRejectsConcurrentRootMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.lock")
	first, err := AcquireFileLock(path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := AcquireFileLock(path, os.Geteuid()); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent migration lock error=%v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireFileLock(path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}
