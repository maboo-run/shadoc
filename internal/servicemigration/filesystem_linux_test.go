//go:build linux

package servicemigration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemMoverStagesCommitsAndSafelyRemovesData(t *testing.T) {
	parent := t.TempDir()
	source := createValidStagedData(t)
	current := filepath.Join(source, "app", "shadoc")
	if err := os.WriteFile(current, []byte("current-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestIntegrityRecord(t, current)
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("{}"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin", "restic"), []byte("restic"), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceUID := testSourceUID(t, source)
	target := filepath.Join(parent, "shadoc")
	staging := filepath.Join(parent, ".shadoc-staging")
	mover, err := NewFilesystemMover(target, staging)
	if err != nil {
		t.Fatal(err)
	}
	if err := mover.Stage(source, staging, current, sourceUID); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(staging, "app", "shadoc"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "current-binary" {
		t.Fatalf("staged executable=%q", got)
	}
	info, err := os.Stat(filepath.Join(staging, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("staged settings mode=%o", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(staging, "bin", "restic")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("user-owned managed Restic was migrated into the root instance: %v", err)
	}
	if err := mover.Commit(staging, target); err != nil {
		t.Fatal(err)
	}
	if exists, err := mover.Exists(target); err != nil || !exists {
		t.Fatalf("target exists=%t err=%v", exists, err)
	}
	if err := mover.Remove(source, sourceUID); err != nil {
		t.Fatal(err)
	}
	if err := mover.Remove(target, os.Geteuid()); err != nil {
		t.Fatal(err)
	}
}

func TestFilesystemMoverRejectsSymlinksWithoutLeavingStagingData(t *testing.T) {
	parent := t.TempDir()
	source := createValidStagedData(t)
	if err := os.Symlink("/etc/passwd", filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(source, "app", "shadoc")
	if err := os.WriteFile(current, []byte("current-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestIntegrityRecord(t, current)
	sourceUID := testSourceUID(t, source)
	target := filepath.Join(parent, "shadoc")
	staging := filepath.Join(parent, ".shadoc-staging")
	mover, err := NewFilesystemMover(target, staging)
	if err != nil {
		t.Fatal(err)
	}
	if err := mover.Stage(source, staging, current, sourceUID); err == nil {
		t.Fatal("symbolic link accepted")
	}
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed staging data remains: %v", err)
	}
}

func TestFilesystemMoverRejectsExecutableThatNoLongerMatchesItsLocalChecksum(t *testing.T) {
	parent := t.TempDir()
	source := createValidStagedData(t)
	current := filepath.Join(source, "app", "shadoc")
	if err := os.WriteFile(current, []byte("official"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestIntegrityRecord(t, current)
	if err := os.WriteFile(current, []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceUID := testSourceUID(t, source)
	mover, err := NewFilesystemMover(filepath.Join(parent, "target"), filepath.Join(parent, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	if err := mover.Stage(source, filepath.Join(parent, "staging"), current, sourceUID); err == nil {
		t.Fatal("changed migration executable accepted")
	}
}

func TestFilesystemMoverRenamesTheVerifiedLegacyProgramAndIntegrityRecord(t *testing.T) {
	parent := t.TempDir()
	source := createValidStagedData(t)
	current := filepath.Join(source, "app", "restic-control")
	if err := os.Rename(filepath.Join(source, "app", "shadoc"), current); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(source, "app", "shadoc.sha256")); err != nil {
		t.Fatal(err)
	}
	writeTestIntegrityRecord(t, current)
	sourceUID := testSourceUID(t, source)
	staging := filepath.Join(parent, "staging")
	mover, err := NewFilesystemMover(filepath.Join(parent, "target"), staging)
	if err != nil {
		t.Fatal(err)
	}
	if err := mover.Stage(source, staging, current, sourceUID); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedData(staging, os.Geteuid()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(staging, "app", "restic-control")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy executable retained in root target: %v", err)
	}
}

func writeTestIntegrityRecord(t *testing.T, binary string) {
	t.Helper()
	content, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(content)
	if err := os.WriteFile(binary+".sha256", []byte(hex.EncodeToString(checksum[:])+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
