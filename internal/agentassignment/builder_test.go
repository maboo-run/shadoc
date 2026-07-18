package agentassignment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/agentrestore"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/repositorycapacity"
	"github.com/maboo-run/shadoc/internal/resticagent"
	"github.com/maboo-run/shadoc/internal/rsync"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestBuilderPreservesControlledResticResourceArguments(t *testing.T) {
	storage := capacityStorage{execution: store.TaskExecution{
		Task: domain.Task{
			ID: "task", Kind: domain.DirectoryTask,
			Directory: &domain.DirectorySource{Path: "/srv/photos"},
			Resources: domain.ResourcePolicy{UploadKiBPerSecond: 256, ReadConcurrency: 3, Compression: "max"},
		},
		Repository:                 domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup/repo"},
		RepositoryPasswordSecretID: "repo-password",
	}}
	raw, err := New(storage, capacitySecrets{"repo-password": []byte("secret")}).Build(context.Background(), store.AgentLease{TaskID: "task", Engine: string(domain.ResticEngine)})
	if err != nil {
		t.Fatal(err)
	}
	var definition resticagent.Definition
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(definition.Arguments, " "); got != "--limit-upload 256 --read-concurrency 3" || definition.Directory.Compression != "max" {
		t.Fatalf("definition=%+v", definition)
	}
}

func TestBuilderHydratesStructuredS3AssignmentAfterClaim(t *testing.T) {
	credentials, err := s3backend.EncodeCredentials(s3backend.Credentials{AccessKey: "access-private", SecretKey: "secret-private"})
	if err != nil {
		t.Fatal(err)
	}
	storage := capacityStorage{execution: store.TaskExecution{
		Task:                       domain.Task{ID: "task", Kind: domain.DirectoryTask, Directory: &domain.DirectorySource{Path: "/srv/photos"}},
		Repository:                 domain.Repository{ID: "repo", Name: "object archive", Kind: domain.S3Repository, Path: "photos", BackendSecretID: "s3-secret", S3: &domain.S3RepositoryConfig{Endpoint: "https://objects.example.com", Bucket: "backup-prod", Region: "us-east-1"}},
		RepositoryPasswordSecretID: "repo-password",
	}}
	raw, err := New(storage, capacitySecrets{"repo-password": []byte("password"), "s3-secret": credentials}).Build(context.Background(), store.AgentLease{TaskID: "task", Engine: string(domain.ResticEngine)})
	if err != nil {
		t.Fatal(err)
	}
	var definition resticagent.Definition
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Repository.Location != "s3:https://objects.example.com/backup-prod/photos" || definition.Repository.S3AccessKey != "access-private" || definition.Repository.S3SecretKey != "secret-private" || definition.Repository.S3BucketLookup != "dns" {
		t.Fatalf("definition=%+v", definition)
	}
}

type capacityStorage struct{ execution store.TaskExecution }

func (s capacityStorage) LoadTaskExecution(context.Context, string) (store.TaskExecution, error) {
	return s.execution, nil
}
func (s capacityStorage) LoadRepositoryExecution(context.Context, string) (store.RepositoryExecution, error) {
	return store.RepositoryExecution{Repository: s.execution.Repository, Host: s.execution.Host, PrivateKeySecretID: s.execution.PrivateKeySecretID, RepositoryPasswordSecretID: s.execution.RepositoryPasswordSecretID}, nil
}

type localRsyncStorage struct{ execution store.RsyncExecution }

func (s localRsyncStorage) LoadTaskExecution(context.Context, string) (store.TaskExecution, error) {
	return store.TaskExecution{}, nil
}
func (s localRsyncStorage) LoadRsyncExecution(context.Context, string) (store.RsyncExecution, error) {
	return s.execution, nil
}
func (s localRsyncStorage) LoadRepositoryExecution(context.Context, string) (store.RepositoryExecution, error) {
	return store.RepositoryExecution{}, nil
}

type rejectingSecrets struct{ called bool }

func (s *rejectingSecrets) Get(context.Context, string, string) ([]byte, error) {
	s.called = true
	return nil, errors.New("SSH secret must not be requested")
}
func (capacityStorage) LoadRsyncExecution(context.Context, string) (store.RsyncExecution, error) {
	return store.RsyncExecution{}, nil
}

func TestBuilderHydratesAgentRestoreOnlyAfterClaim(t *testing.T) {
	storage := capacityStorage{execution: store.TaskExecution{
		Repository:                 domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup/repo"},
		RepositoryPasswordSecretID: "repo-password",
	}}
	control, _ := json.Marshal(agentrestore.Definition{RepositoryID: "repo", SnapshotID: "snapshot", SourcePath: "/srv/photos", Target: "/restore/photos", Includes: []string{"one.jpg"}})
	raw, err := New(storage, capacitySecrets{"repo-password": []byte("secret")}).Build(context.Background(), store.AgentLease{TaskID: "restore-operation", Engine: string(agentrestore.Kind), Definition: control})
	if err != nil {
		t.Fatal(err)
	}
	var definition agentrestore.Definition
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Repository.Location != "/backup/repo" || definition.Repository.Password != "secret" || definition.Target != "/restore/photos" || definition.RepositoryID != "repo" {
		t.Fatalf("definition=%+v", definition)
	}
}

type capacitySecrets map[string][]byte

func (s capacitySecrets) Get(_ context.Context, id, _ string) ([]byte, error) { return s[id], nil }

func TestBuilderHydratesPinnedSFTPCapacityProbeWithoutRepositoryPassword(t *testing.T) {
	storage := capacityStorage{execution: store.TaskExecution{
		Repository:         domain.Repository{ID: "repo", Kind: domain.SFTPRepository, RemoteHostID: "host", Path: "/backup/repo"},
		Host:               domain.RemoteHost{ID: "host", Host: "backup.example", Port: 2222, Username: "backup", HostFingerprint: "backup.example ssh-ed25519 AAAA"},
		PrivateKeySecretID: "ssh-key",
	}}
	raw, err := New(storage, capacitySecrets{"ssh-key": []byte("private-key")}).Build(context.Background(), store.AgentLease{TaskID: "task", Engine: string(repositorycapacity.Kind)})
	if err != nil {
		t.Fatal(err)
	}
	var definition repositorycapacity.Definition
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Kind != "sftp" || definition.Path != "/backup/repo" || definition.Host != "backup.example" || definition.Port != 2222 || definition.Username != "backup" || definition.PrivateKey != "private-key" || definition.KnownHosts == "" {
		t.Fatalf("definition=%+v", definition)
	}
}

func TestBuilderCreatesLocalRsyncAssignmentWithoutSSHSecret(t *testing.T) {
	storage := localRsyncStorage{execution: store.RsyncExecution{Task: domain.Task{
		Rsync: &domain.RsyncSource{Path: "/mnt/disk-a/photos", DestinationKind: domain.RsyncDestinationLocal, DestinationPath: "/mnt/disk-b/photos"},
	}}}
	secrets := &rejectingSecrets{}
	raw, err := New(storage, secrets).Build(context.Background(), store.AgentLease{TaskID: "task", Engine: string(domain.RsyncEngine)})
	if err != nil {
		t.Fatal(err)
	}
	if secrets.called {
		t.Fatal("SSH secret was requested")
	}
	var definition rsync.Definition
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Destination.Kind != rsync.DestinationLocal || definition.Destination.Path != "/mnt/disk-b/photos" || definition.Destination.Host != "" {
		t.Fatalf("definition=%+v", definition)
	}
}

func TestBuilderUsesLocalRsyncRepositoryAsDestination(t *testing.T) {
	storage := localRsyncStorage{execution: store.RsyncExecution{
		Task:       domain.Task{RepositoryID: "repo", Rsync: &domain.RsyncSource{Path: "/mnt/disk-a/photos"}},
		Repository: domain.Repository{ID: "repo", Engine: domain.RsyncEngine, Kind: domain.LocalRepository, Path: "/mnt/disk-b/photos"},
	}}
	secrets := &rejectingSecrets{}
	raw, err := New(storage, secrets).Build(context.Background(), store.AgentLease{TaskID: "task", Engine: string(domain.RsyncEngine)})
	if err != nil {
		t.Fatal(err)
	}
	var definition rsync.Definition
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Destination.Kind != rsync.DestinationLocal || definition.Destination.Path != "/mnt/disk-b/photos" || secrets.called {
		t.Fatalf("definition=%+v secretCalled=%v", definition, secrets.called)
	}
}

func TestBuilderHydratesRsyncDryRunOnlyAfterAgentClaimsPreviewLease(t *testing.T) {
	storage := localRsyncStorage{execution: store.RsyncExecution{
		Task:       domain.Task{RepositoryID: "repo", Rsync: &domain.RsyncSource{Path: "/mnt/disk-a/photos", Delete: true}},
		Repository: domain.Repository{ID: "repo", Engine: domain.RsyncEngine, Kind: domain.LocalRepository, Path: "/mnt/disk-b/photos"},
	}}
	marker := json.RawMessage(`{"dryRun":true}`)
	raw, err := New(storage, &rejectingSecrets{}).Build(context.Background(), store.AgentLease{TaskID: "task", Engine: string(domain.RsyncEngine), Definition: marker})
	if err != nil {
		t.Fatal(err)
	}
	var definition rsync.Definition
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	if !definition.DryRun || !definition.Delete || definition.Destination.PrivateKey != "" {
		t.Fatalf("definition=%+v", definition)
	}
}
