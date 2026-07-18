//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/backup"
	"github.com/maboo-run/shadoc/internal/command"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/repolock"
	"github.com/maboo-run/shadoc/internal/repository"
	"github.com/maboo-run/shadoc/internal/restic"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
)

func TestRealResticS3(t *testing.T) {
	endpoint := os.Getenv("RESTIC_CONTROL_E2E_S3_ENDPOINT")
	bucket := os.Getenv("RESTIC_CONTROL_E2E_S3_BUCKET")
	region := os.Getenv("RESTIC_CONTROL_E2E_S3_REGION")
	accessKey := os.Getenv("RESTIC_CONTROL_E2E_S3_ACCESS_KEY")
	secretKey := os.Getenv("RESTIC_CONTROL_E2E_S3_SECRET_KEY")
	pathStyleText := os.Getenv("RESTIC_CONTROL_E2E_S3_PATH_STYLE")
	requireReleaseConfiguration(t, "real-restic-s3", endpoint, bucket, region, accessKey, secretKey, pathStyleText)
	pathStyle, err := strconv.ParseBool(pathStyleText)
	if err != nil {
		t.Fatalf("RESTIC_CONTROL_E2E_S3_PATH_STYLE must be true or false: %v", err)
	}
	resticPath := os.Getenv("RESTIC_CONTROL_E2E_RESTIC")
	if resticPath == "" {
		resticPath, err = exec.LookPath("restic")
		if err != nil {
			t.Fatal("real S3 release E2E requires RESTIC_CONTROL_E2E_RESTIC or restic in PATH")
		}
	}

	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	storage, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	cipher, err := vault.New(bytes.Repeat([]byte{11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	secrets := secret.New(storage, cipher, time.Now)
	const repositoryPassword = "real-s3-repository-password-long"
	passwordSecretID, err := secrets.Put(ctx, "repository-password", []byte(repositoryPassword))
	if err != nil {
		t.Fatal(err)
	}
	encodedCredentials, err := s3backend.EncodeCredentials(s3backend.Credentials{AccessKey: accessKey, SecretKey: secretKey})
	if err != nil {
		t.Fatal(err)
	}
	backendSecretID, err := secrets.Put(ctx, s3backend.CredentialPurpose, encodedCredentials)
	clear(encodedCredentials)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	prefix := "restic-control-e2e/" + strconv.FormatInt(now.UnixNano(), 36)
	repositoryDefinition := domain.Repository{
		ID: "repo-s3", Name: "real-s3", Kind: domain.S3Repository, Path: prefix, BackendSecretID: backendSecretID,
		S3:     &domain.S3RepositoryConfig{Endpoint: endpoint, Bucket: bucket, Region: region, PathStyle: pathStyle},
		Status: "uninitialized", CreatedAt: now, UpdatedAt: now,
	}
	if err := repositoryDefinition.Validate(); err != nil {
		t.Fatalf("invalid real S3 configuration: %v", err)
	}
	if err := storage.CreateRepository(ctx, repositoryDefinition, passwordSecretID); err != nil {
		t.Fatal(err)
	}
	engine := restic.New(resticPath, command.OSExecutor{}, filepath.Join(root, "runtime"))
	defer func() {
		credentials := s3backend.Credentials{AccessKey: accessKey, SecretKey: secretKey}
		material, materialErr := s3backend.Material(repositoryDefinition, repositoryPassword, credentials)
		if materialErr != nil {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if _, forgetErr := engine.Execute(cleanupCtx, restic.Operation{Kind: restic.ForgetSnapshots, Repository: material, Arguments: []string{"--keep-last", "0"}}); forgetErr == nil {
			_, _ = engine.Execute(cleanupCtx, restic.Operation{Kind: restic.PruneRepository, Repository: material})
		}
	}()
	locks := repolock.New()
	repositories := repository.New(storage, secrets, engine)
	repositories.SetLocker(locks)
	if err := repositories.Initialize(ctx, repositoryDefinition.ID); err != nil {
		t.Fatalf("initialize real S3 repository: %v", err)
	}

	source := filepath.Join(root, "source")
	want := []byte("restic-control real S3 payload\x00\x01\n")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "payload.bin"), want, 0o600); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-s3", Name: "real-s3-directory", Kind: domain.DirectoryTask, RepositoryID: repositoryDefinition.ID, Directory: &domain.DirectorySource{Path: source}, Resources: domain.ResourcePolicy{Compression: "auto"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := storage.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	backups := backup.New(storage, secrets, engine, nil, nil, time.Now)
	backups.SetRepositoryLocker(locks)
	run, err := backups.Run(ctx, task.ID, "", "e2e")
	if err != nil || run.Status != "success" || run.SnapshotID == "" {
		t.Fatalf("real S3 backup run=%+v err=%v", run, err)
	}
	for _, protected := range []string{repositoryPassword, accessKey, secretKey} {
		if strings.Contains(run.RawLog, protected) {
			t.Fatalf("real S3 run log exposed credential %q", protected)
		}
	}
	restoreTarget := filepath.Join(root, "restored")
	if err := repositories.RestoreDirectory(ctx, repositoryDefinition.ID, run.SnapshotID, restoreTarget, nil, 0); err != nil {
		t.Fatalf("restore real S3 snapshot: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(restoreTarget, "nested", "payload.bin"))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("restored real S3 payload=%q err=%v", got, err)
	}
	recordCheck("real-restic-s3", "passed", "structured S3 init, backup, snapshot, and restore")
}
