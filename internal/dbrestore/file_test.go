package dbrestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/maboo-run/shadoc/internal/database"
	repositoryservice "github.com/maboo-run/shadoc/internal/repository"
)

type dumpFileRepositoryStub struct {
	snapshots []repositoryservice.Snapshot
	content   string
	limit     int
	filename  string
	target    string
	err       error
}

func (s *dumpFileRepositoryStub) Snapshots(context.Context, string) ([]repositoryservice.Snapshot, error) {
	return s.snapshots, nil
}

func (s *dumpFileRepositoryStub) RestoreDumpFile(_ context.Context, _, _, filename, target string, limit int) error {
	s.filename, s.target, s.limit = filename, target, limit
	if s.err != nil {
		return s.err
	}
	return os.WriteFile(target, []byte(s.content), 0o600)
}

func databaseSnapshot(t *testing.T) repositoryservice.Snapshot {
	t.Helper()
	metadata := database.SnapshotMetadata{
		Engine:        database.MySQL,
		Database:      "maboo_course",
		Format:        "sql",
		Filename:      "maboo_course.sql",
		ServerVersion: "8.4.0",
		ClientVersion: "8.4.0",
		Encoding:      "utf8mb4",
		Collation:     "utf8mb4_0900_ai_ci",
	}
	tags, err := database.EncodeMetadataTags(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return repositoryservice.Snapshot{ID: "snapshot", Tags: tags}
}

func TestDumpFileRestorePreflightValidatesOutputDirectoryWithoutWriting(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "maboo_course.sql")
	repository := &dumpFileRepositoryStub{snapshots: []repositoryservice.Snapshot{databaseSnapshot(t)}}
	service := NewDumpFileService(repository)

	result, err := service.Preflight(context.Background(), DumpFileRequest{
		RepositoryID:         "repo",
		SnapshotID:           "snapshot",
		TargetDirectory:      directory,
		DownloadKiBPerSecond: 384,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata.Filename != "maboo_course.sql" || result.Target != target || result.Behavior != "create_file" {
		t.Fatalf("preflight result=%+v", result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("preflight wrote target: %v", err)
	}
}

func TestDumpFileRestoreUsesAuthoritativeMetadataAndControlledLimit(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "maboo_course.sql")
	repository := &dumpFileRepositoryStub{snapshots: []repositoryservice.Snapshot{databaseSnapshot(t)}, content: "CREATE TABLE example(id INT);"}
	service := NewDumpFileService(repository)

	if err := service.Restore(context.Background(), DumpFileRequest{
		RepositoryID:         "repo",
		SnapshotID:           "snapshot",
		TargetDirectory:      directory,
		DownloadKiBPerSecond: 256,
	}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != repository.content {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if repository.filename != "maboo_course.sql" || repository.limit != 256 || repository.target != target {
		t.Fatalf("restore call filename=%q limit=%d target=%q", repository.filename, repository.limit, repository.target)
	}
}

func TestDumpFileRestoreRejectsExistingTargetAndCleansFailedOutput(t *testing.T) {
	directory := t.TempDir()
	existingTarget := filepath.Join(directory, "maboo_course.sql")
	if err := os.WriteFile(existingTarget, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &dumpFileRepositoryStub{snapshots: []repositoryservice.Snapshot{databaseSnapshot(t)}, err: errors.New("source dump failed")}
	service := NewDumpFileService(repository)

	if err := service.Restore(context.Background(), DumpFileRequest{RepositoryID: "repo", SnapshotID: "snapshot", TargetDirectory: directory}); err == nil {
		t.Fatal("existing output target was accepted")
	}
	if repository.filename != "" {
		t.Fatalf("repository was called for existing target: %+v", repository)
	}

	failedDirectory := filepath.Join(directory, "failed-output")
	if err := os.Mkdir(failedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	failedTarget := filepath.Join(failedDirectory, "maboo_course.sql")
	if err := service.Restore(context.Background(), DumpFileRequest{RepositoryID: "repo", SnapshotID: "snapshot", TargetDirectory: failedDirectory}); err == nil {
		t.Fatal("failed dump was reported as successful")
	}
	if _, err := os.Stat(failedTarget); !os.IsNotExist(err) {
		t.Fatalf("failed restore published target: %v", err)
	}
	entries, err := os.ReadDir(failedDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging files remain: %+v", entries)
	}
}
