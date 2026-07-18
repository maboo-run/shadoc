package dbrestore

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/database"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/repository"
	"github.com/maboo-run/shadoc/internal/store"
)

type restoreStore struct {
	record store.DatabaseConnectionExecution
}

func (s restoreStore) LoadDatabaseConnectionExecution(context.Context, string) (store.DatabaseConnectionExecution, error) {
	return s.record, nil
}

type restoreSecrets struct{ calls int }

func (s *restoreSecrets) Get(context.Context, string, string) ([]byte, error) {
	s.calls++
	return nil, errors.New("must not read secrets for rejected metadata")
}

type workingRestoreSecrets struct{}

func (workingRestoreSecrets) Get(context.Context, string, string) ([]byte, error) {
	return []byte("secret"), nil
}

type restoreRepository struct {
	snapshots []repository.Snapshot
	dump      string
	limit     *int
}

func (r restoreRepository) Snapshots(context.Context, string) ([]repository.Snapshot, error) {
	return r.snapshots, nil
}
func (r restoreRepository) Dump(_ context.Context, _, _, _ string, downloadKiBPerSecond int, output io.Writer) error {
	if r.limit != nil {
		*r.limit = downloadKiBPerSecond
	}
	if r.dump == "" {
		return errors.New("must not dump rejected snapshot")
	}
	_, err := io.WriteString(output, r.dump)
	return err
}

type metadataRestoreExecutor struct {
	createArgs []string
	imported   string
}

func (e *metadataRestoreExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	joined := strings.Join(spec.Args, " ")
	if joined == "--version" {
		return command.Result{ExitCode: 0, Stdout: "mysql  Ver 8.4.5"}, nil
	}
	if strings.Contains(joined, "information_schema.schemata") {
		return command.Result{ExitCode: 0, Stdout: "0\n"}, nil
	}
	if strings.Contains(joined, "CREATE DATABASE") {
		e.createArgs = append([]string{}, spec.Args...)
		return command.Result{ExitCode: 0}, nil
	}
	if spec.Stdin != nil {
		value, _ := io.ReadAll(spec.Stdin)
		e.imported = string(value)
		return command.Result{ExitCode: 0}, nil
	}
	return command.Result{ExitCode: 0}, nil
}

type unusedExecutor struct{}

func (unusedExecutor) Run(context.Context, command.Spec) (command.Result, error) {
	return command.Result{}, errors.New("must not execute rejected restore")
}

func TestRestoreRejectsSnapshotWithoutTrustedDatabaseMetadata(t *testing.T) {
	connection := domain.DatabaseConnection{ID: "restore", Engine: domain.MySQL, Purpose: domain.RestoreConnection}
	for _, test := range []struct {
		name     string
		tags     []string
		request  Request
		contains string
	}{
		{name: "missing metadata", tags: []string{"unrelated"}, request: Request{SnapshotID: "snap"}, contains: "metadata"},
		{name: "legacy incomplete metadata", tags: []string{"rc:source=database", "rc:engine=postgresql", "rc:format=postgres-custom", "rc:filename=db.dump"}, request: Request{SnapshotID: "snap"}, contains: "metadata"},
	} {
		t.Run(test.name, func(t *testing.T) {
			secrets := &restoreSecrets{}
			test.request.RepositoryID = "repo"
			test.request.ConnectionID = "restore"
			service := New(restoreStore{record: store.DatabaseConnectionExecution{Connection: connection, PasswordSecretID: "password"}}, secrets, restoreRepository{snapshots: []repository.Snapshot{{ID: "snap", Tags: test.tags}}}, unusedExecutor{}, t.TempDir())
			err := service.Restore(context.Background(), test.request)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error=%v", err)
			}
			if secrets.calls != 0 {
				t.Fatalf("read secrets before metadata rejection: %d", secrets.calls)
			}
		})
	}
}

func TestRestoreRejectsRequestThatConflictsWithTrustedMetadata(t *testing.T) {
	metadata := database.SnapshotMetadata{Engine: database.MySQL, Database: "db", Format: "sql", Filename: "db.sql", ServerVersion: "8.4.5", ClientVersion: "8.4.5", Encoding: "utf8mb4", Collation: "utf8mb4_bin"}
	tags, _ := database.EncodeMetadataTags(metadata)
	secrets := &restoreSecrets{}
	service := New(restoreStore{record: store.DatabaseConnectionExecution{Connection: domain.DatabaseConnection{Engine: domain.MySQL, Purpose: domain.RestoreConnection}}}, secrets, restoreRepository{snapshots: []repository.Snapshot{{ID: "snap", Tags: tags}}}, unusedExecutor{}, t.TempDir())
	err := service.Restore(context.Background(), Request{RepositoryID: "repo", SnapshotID: "snap", ConnectionID: "restore", Filename: "other.sql"})
	if err == nil || !strings.Contains(err.Error(), "filename") || secrets.calls != 0 {
		t.Fatalf("error=%v secretCalls=%d", err, secrets.calls)
	}
}

func TestRestoreRejectsUnknownSnapshotID(t *testing.T) {
	service := New(restoreStore{record: store.DatabaseConnectionExecution{Connection: domain.DatabaseConnection{Purpose: domain.RestoreConnection}}}, &restoreSecrets{}, restoreRepository{}, unusedExecutor{}, t.TempDir())
	err := service.Restore(context.Background(), Request{RepositoryID: "repo", SnapshotID: "missing", ConnectionID: "restore"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error=%v", err)
	}
}

func TestRestoreUsesAuthoritativeSnapshotEncodingWhenCreatingDatabase(t *testing.T) {
	metadata := database.SnapshotMetadata{Engine: database.MySQL, Database: "source", Format: "sql", Filename: "source.sql", ServerVersion: "8.4.5", ClientVersion: "8.4.5", Encoding: "utf8mb4", Collation: "utf8mb4_0900_ai_ci"}
	tags, err := database.EncodeMetadataTags(metadata)
	if err != nil {
		t.Fatal(err)
	}
	executor := &metadataRestoreExecutor{}
	downloadLimit := 0
	connection := domain.DatabaseConnection{ID: "restore", Engine: domain.MySQL, Purpose: domain.RestoreConnection, Network: domain.TCPNetwork, Host: "db", Port: 3306, Username: "restore", ToolPaths: map[string]string{"restore": "/tools/mysql"}}
	service := New(restoreStore{record: store.DatabaseConnectionExecution{Connection: connection, PasswordSecretID: "password"}}, workingRestoreSecrets{}, restoreRepository{snapshots: []repository.Snapshot{{ID: "snap", Tags: tags}}, dump: "CREATE TABLE example(id INT);", limit: &downloadLimit}, executor, t.TempDir())
	if err := service.Restore(context.Background(), Request{RepositoryID: "repo", SnapshotID: "snap", ConnectionID: "restore", Database: "restored", DownloadKiBPerSecond: 512}); err != nil {
		t.Fatal(err)
	}
	create := strings.Join(executor.createArgs, " ")
	if !strings.Contains(create, "CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci") || executor.imported == "" || downloadLimit != 512 {
		t.Fatalf("create=%s imported=%q downloadLimit=%d", create, executor.imported, downloadLimit)
	}
}
