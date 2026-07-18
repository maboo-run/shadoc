package backup

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/database"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/restic"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/store"
)

type secretReader map[string][]byte

func (s secretReader) Get(_ context.Context, id, purpose string) ([]byte, error) { return s[id], nil }

type fakeRestic struct {
	operations []restic.Operation
	stdout     string
	err        error
	failKind   restic.OperationKind
	outcome    restic.Outcome
	summary    map[string]any
}

type s3LeakRestic struct{ operation restic.Operation }

func (r *s3LeakRestic) Execute(_ context.Context, operation restic.Operation) (restic.Result, error) {
	r.operation = operation
	return restic.Result{Outcome: restic.Success, SnapshotID: "snapshot-s3", Stdout: strings.Join([]string{operation.Repository.Password, operation.Repository.S3AccessKey, operation.Repository.S3SecretKey}, " ")}, nil
}

type metadataExecutor struct{}

func (metadataExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	if strings.HasSuffix(spec.Program, "mysqldump") {
		return command.Result{ExitCode: 0, Stdout: "mysqldump  Ver 8.4.5"}, nil
	}
	return command.Result{ExitCode: 0, Stdout: "8.4.5\tutf8mb4\tutf8mb4_0900_ai_ci\n"}, nil
}

func (f *fakeRestic) Execute(_ context.Context, operation restic.Operation) (restic.Result, error) {
	f.operations = append(f.operations, operation)
	value := f.stdout
	if value == "" {
		value = `{"message_type":"summary"}`
	} else {
		value = "diagnostic " + operation.Repository.Password
	}
	if f.failKind == operation.Kind {
		return restic.Result{Outcome: restic.Failure, SnapshotID: "snapshot-a", Stdout: value, Summary: f.summary}, errors.New("tag connection failed")
	}
	outcome := f.outcome
	if outcome == "" {
		outcome = restic.Success
	}
	return restic.Result{Outcome: outcome, SnapshotID: "snapshot-a", Stdout: value, Summary: f.summary}, f.err
}

func TestServiceExecutesDirectoryAndDatabaseTasksAndPersistsRuns(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	for id, purpose := range map[string]string{"key": "ssh-private-key", "repo1": "repository-password", "repo2": "repository-password", "dbpass": "database-backup-password"} {
		if err := s.SaveSecret(context.Background(), id, purpose, []byte("cipher"), now); err != nil {
			t.Fatal(err)
		}
	}
	host := domain.RemoteHost{ID: "host", Name: "nas", Host: "nas.local", Port: 22, Username: "backup", HostFingerprint: "nas.local ssh-ed25519 AAAA", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRemoteHost(context.Background(), host, "key"); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []domain.Repository{{ID: "r1", Name: "dir", RemoteHostID: "host", Path: "/dir", Status: "ready", CreatedAt: now, UpdatedAt: now}, {ID: "r2", Name: "db", RemoteHostID: "host", Path: "/db", Status: "ready", CreatedAt: now, UpdatedAt: now}} {
		pass := "repo1"
		if repo.ID == "r2" {
			pass = "repo2"
		}
		if err := s.CreateRepository(context.Background(), repo, pass); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateDatabaseConnection(context.Background(), domain.DatabaseConnection{ID: "conn", Name: "mysql", Engine: domain.MySQL, Purpose: domain.BackupConnection, Network: domain.TCPNetwork, Host: "127.0.0.1", Port: 3306, Username: "backup", ToolPaths: map[string]string{"dump": "/tools/mysqldump", "admin": "/tools/mysql"}, Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now, ClientVersion: "8.0", ServerVersion: "8.0"}, CreatedAt: now, UpdatedAt: now}, "dbpass"); err != nil {
		t.Fatal(err)
	}
	for _, task := range []domain.Task{{ID: "t1", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "r1", Directory: &domain.DirectorySource{Path: "/srv/photos"}, Resources: domain.ResourcePolicy{UploadKiBPerSecond: 128, ReadConcurrency: 4, Compression: "max"}, ScopeConfirmation: domain.TaskScopeConfirmation{PreviewID: "preview-1", Fingerprint: "fingerprint-1", ConfirmedBy: "admin", ConfirmedAt: now, Summary: map[string]any{"includedFiles": 4}}, Enabled: true, CreatedAt: now, UpdatedAt: now}, {ID: "t2", Name: "gitea", Kind: domain.DatabaseTask, RepositoryID: "r2", Database: &domain.DatabaseSource{ConnectionID: "conn", Database: "gitea"}, Resources: domain.ResourcePolicy{UploadKiBPerSecond: 64, ReadConcurrency: 2, Compression: "off"}, Enabled: true, CreatedAt: now, UpdatedAt: now}} {
		if err := s.CreateTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeRestic{stdout: "diagnostic pass1 pass2 db-secret", summary: map[string]any{"filesProcessed": int64(8), "bytesChanged": int64(2048)}}
	service := New(s, secretReader{"key": []byte("key"), "repo1": []byte("pass1"), "repo2": []byte("pass2"), "dbpass": []byte("db-secret")}, runner, database.NewMySQL(t.TempDir()), database.NewPostgres(t.TempDir()), nil)
	service.SetMetadataExecutor(metadataExecutor{})
	for _, id := range []string{"t1", "t2"} {
		record, err := service.Run(context.Background(), id, "", "manual")
		if err != nil || record.Status != "success" || record.SnapshotID != "snapshot-a" {
			t.Fatalf("run %s record=%+v err=%v", id, record, err)
		}
	}
	if len(runner.operations) != 3 || runner.operations[0].Kind != restic.BackupDirectory || runner.operations[1].Kind != restic.TagSnapshot || runner.operations[2].Kind != restic.BackupCommand {
		t.Fatalf("operations=%+v", runner.operations)
	}
	if runner.operations[2].Command == nil || runner.operations[2].Filename != "gitea.sql" {
		t.Fatalf("database operation=%+v", runner.operations[2])
	}
	directoryArgs := strings.Join(runner.operations[0].Arguments, " ")
	if directoryArgs != "--limit-upload 128 --read-concurrency 4" || runner.operations[0].Directory.Compression != "max" {
		t.Fatalf("directory resources args=%q directory=%+v", directoryArgs, runner.operations[0].Directory)
	}
	databaseArgs := strings.Join(runner.operations[2].Arguments, " ")
	for _, expected := range []string{"--limit-upload 64", "--read-concurrency 2", "--compression off"} {
		if !strings.Contains(databaseArgs, expected) {
			t.Fatalf("database resources missing %q from %q", expected, databaseArgs)
		}
	}
	var metadataTags []string
	for index := 0; index+1 < len(runner.operations[2].Arguments); index += 2 {
		if runner.operations[2].Arguments[index] == "--tag" {
			metadataTags = append(metadataTags, runner.operations[2].Arguments[index+1])
		}
	}
	decoded, err := database.DecodeMetadataTags(metadataTags)
	if err != nil || decoded.ServerVersion != "8.4.5" || decoded.Encoding != "utf8mb4" {
		t.Fatalf("metadata tags=%v decoded=%+v err=%v", metadataTags, decoded, err)
	}
	indexed, err := s.SnapshotMetadata(context.Background(), "r2", "snapshot-a")
	if err != nil || indexed.Metadata != decoded {
		t.Fatalf("snapshot index=%+v err=%v", indexed, err)
	}
	if bytes.Contains([]byte(runner.operations[2].Command.Program), []byte("db-secret")) {
		t.Fatal("secret leaked")
	}
	runs, err := s.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	for _, run := range runs {
		if strings.Contains(run.RawLog, "pass1") || strings.Contains(run.RawLog, "pass2") {
			t.Fatalf("run log leaked secret: %q", run.RawLog)
		}
		if run.TaskID == "t1" {
			confirmation, ok := run.Summary["scopeConfirmation"].(map[string]any)
			if !ok || confirmation["fingerprint"] != "fingerprint-1" {
				t.Fatalf("directory run summary=%+v", run.Summary)
			}
			if run.Summary["filesProcessed"] != float64(8) || run.Summary["bytesChanged"] != float64(2048) || run.Metrics == nil || run.Metrics.BytesChanged == nil || *run.Metrics.BytesChanged != 2048 {
				t.Fatalf("directory metrics summary=%+v metrics=%+v", run.Summary, run.Metrics)
			}
		}
	}

	runner.outcome = restic.Partial
	runner.failKind = restic.TagSnapshot
	failed, err := service.Run(context.Background(), "t1", "", "manual")
	if err == nil || failed.Status != "failed" {
		t.Fatalf("unprotected partial result=%+v err=%v", failed, err)
	}
	repositories, _ := s.ListRepositories(context.Background())
	status := ""
	for _, repository := range repositories {
		if repository.ID == "r1" {
			status = repository.Status
		}
	}
	if status != "unprotected-partial:snapshot-a" {
		t.Fatalf("unprotected partial status=%q", status)
	}
	runner.failKind = ""
	runner.outcome = restic.Success
	recovered, err := service.Run(context.Background(), "t1", "", "manual")
	if err != nil || recovered.Status != "success" {
		t.Fatalf("partial protection recovery=%+v err=%v", recovered, err)
	}
	repositories, _ = s.ListRepositories(context.Background())
	status = ""
	for _, repository := range repositories {
		if repository.ID == "r1" {
			status = repository.Status
		}
	}
	if status != "ready" {
		t.Fatalf("recovered repository status=%q", status)
	}
}

func TestServiceBuildsStructuredS3MaterialAndRedactsEveryCredential(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	if err := s.SaveSecret(t.Context(), "repo-pass", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(t.Context(), "s3-secret", s3backend.CredentialPurpose, []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	repository := domain.Repository{ID: "repo-s3", Name: "object archive", Kind: domain.S3Repository, Path: "photos", BackendSecretID: "s3-secret", S3: &domain.S3RepositoryConfig{Endpoint: "https://objects.example.com", Bucket: "backup-prod", Region: "us-east-1", PathStyle: true}, Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRepository(t.Context(), repository, "repo-pass"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-s3", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: repository.ID, Directory: &domain.DirectorySource{Path: "/srv/photos"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	encoded, _ := s3backend.EncodeCredentials(s3backend.Credentials{AccessKey: "access-private", SecretKey: "secret-private"})
	runner := &s3LeakRestic{}
	service := New(s, secretReader{"repo-pass": []byte("repository-private"), "s3-secret": encoded}, runner, nil, nil, nil)
	record, err := service.Run(t.Context(), task.ID, "", "manual")
	if err != nil || record.Status != "success" {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	material := runner.operation.Repository
	if material.Location != "s3:https://objects.example.com/backup-prod/photos" || material.S3BucketLookup != "path" || material.S3AccessKey != "access-private" || material.S3SecretKey != "secret-private" {
		t.Fatalf("material=%+v", material)
	}
	for _, protected := range []string{"repository-private", "access-private", "secret-private"} {
		if strings.Contains(record.RawLog, protected) {
			t.Fatalf("run log exposed %q: %q", protected, record.RawLog)
		}
	}
}

func TestServiceRefusesDisabledTaskBeforeCreatingRun(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	for id, purpose := range map[string]string{"key": "ssh-private-key", "repo": "repository-password"} {
		if err := s.SaveSecret(context.Background(), id, purpose, []byte("cipher"), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateRemoteHost(context.Background(), domain.RemoteHost{ID: "host", Name: "nas", Host: "nas.local", Port: 22, Username: "backup", CreatedAt: now, UpdatedAt: now}, "key"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(context.Background(), domain.Repository{ID: "repo", Name: "repo", RemoteHostID: "host", Path: "/repo", Status: "ready", CreatedAt: now, UpdatedAt: now}, "repo"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(context.Background(), domain.Task{ID: "task", Name: "disabled", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/srv/data"}, Enabled: false, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRestic{}
	_, err = New(s, secretReader{}, runner, database.NewMySQL(t.TempDir()), database.NewPostgres(t.TempDir()), nil).Run(context.Background(), "task", "", "manual")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled run error = %v", err)
	}
	if len(runner.operations) != 0 {
		t.Fatalf("disabled task executed operations: %+v", runner.operations)
	}
	runs, listErr := s.ListRuns(context.Background(), 10)
	if listErr != nil || len(runs) != 0 {
		t.Fatalf("disabled task persisted runs=%+v err=%v", runs, listErr)
	}
}

func TestSafeErrorRedactsSecrets(t *testing.T) {
	got := safeError(errors.New("connection failed using super-secret"), "super-secret")
	if strings.Contains(got, "super-secret") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("unsafe error summary: %q", got)
	}
}

func TestSFTPLocationLeavesPortToPinnedSSHCommand(t *testing.T) {
	location := sftpLocation(domain.RemoteHost{Host: "nas.example", Port: 2222, Username: "backup"}, "/volume/restic")
	if location != "sftp:backup@nas.example:/volume/restic" {
		t.Fatalf("location = %q", location)
	}
}
