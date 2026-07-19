package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/restic"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/store"
)

type repoStore struct {
	execution        store.RepositoryExecution
	remote           store.RemoteHostExecution
	status           string
	pending          *store.RepositoryKeyRevocation
	created          domain.Repository
	secretID         string
	createErr        error
	statusContextErr error
}

func (s *repoStore) LoadRepositoryExecution(context.Context, string) (store.RepositoryExecution, error) {
	return s.execution, nil
}
func (s *repoStore) UpdateRepositoryStatus(ctx context.Context, _ string, status string) error {
	s.statusContextErr = ctx.Err()
	if s.statusContextErr != nil {
		return s.statusContextErr
	}
	s.status = status
	s.execution.Repository.Status = status
	return nil
}
func (s *repoStore) LoadRemoteHostExecution(context.Context, string) (store.RemoteHostExecution, error) {
	return s.remote, nil
}
func (s *repoStore) CreateRepository(_ context.Context, repository domain.Repository, secretID string) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.created, s.secretID = repository, secretID
	return nil
}
func (s *repoStore) CommitRepositoryPasswordRotation(_ context.Context, repositoryID, newSecretID, oldKeyID, oldSecretID string, at time.Time) error {
	if s.pending != nil {
		return store.ErrConflict
	}
	s.execution.RepositoryPasswordSecretID = newSecretID
	s.pending = &store.RepositoryKeyRevocation{RepositoryID: repositoryID, KeyID: oldKeyID, SecretID: oldSecretID, CreatedAt: at}
	return nil
}
func (s *repoStore) PendingRepositoryKeyRevocation(_ context.Context, _ string) (store.RepositoryKeyRevocation, bool, error) {
	if s.pending == nil {
		return store.RepositoryKeyRevocation{}, false, nil
	}
	return *s.pending, true, nil
}
func (s *repoStore) CompleteRepositoryKeyRevocation(_ context.Context, _, keyID string) error {
	if s.pending == nil || s.pending.KeyID != keyID {
		return errors.New("pending key revocation not found")
	}
	s.pending = nil
	return nil
}

type repoSecrets map[string][]byte

func (s repoSecrets) Get(_ context.Context, id, purpose string) ([]byte, error) { return s[id], nil }
func (s repoSecrets) Put(_ context.Context, purpose string, value []byte) (string, error) {
	id := "new-secret"
	if purpose == s3backend.CredentialPurpose {
		id = "new-s3-secret"
	}
	s[id] = append([]byte(nil), value...)
	return id, nil
}
func (s repoSecrets) Delete(_ context.Context, id string) error { delete(s, id); return nil }

type repoRunner struct {
	operations     []restic.Operation
	failCheck      bool
	failVerify     bool
	failDump       bool
	dumpContent    string
	verifyOutput   string
	forgetOutput   string
	contentsOutput string
	contentsByID   map[string]string
	afterExecute   func()
}

type directoryRestoreRunner struct {
	fail             bool
	restoreArguments []string
	stagingTarget    string
}

type recordingLocker struct{ calls int }

func (l *recordingLocker) With(_ context.Context, _ string, work func() error) error {
	l.calls++
	return work()
}

func (r *directoryRestoreRunner) Execute(_ context.Context, op restic.Operation) (restic.Result, error) {
	switch op.Kind {
	case restic.ListSnapshots:
		return restic.Result{Outcome: restic.Success, Stdout: `[{"id":"snap","time":"2026-07-11T00:00:00Z","paths":["/A/B/C"]}]`}, nil
	case restic.ListSnapshotContents:
		return restic.Result{Outcome: restic.Success, Stdout: `{"struct_type":"node","name":"nested","type":"dir","path":"/A/B/C/nested"}` + "\n"}, nil
	case restic.RestoreDirectory:
		r.restoreArguments = append([]string(nil), op.Arguments...)
		for index, argument := range op.Arguments {
			if argument == "--target" && index+1 < len(op.Arguments) {
				r.stagingTarget = op.Arguments[index+1]
				break
			}
		}
		if r.stagingTarget == "" {
			return restic.Result{}, errors.New("restore target argument is missing")
		}
		if err := os.MkdirAll(filepath.Join(r.stagingTarget, "nested"), 0o700); err != nil {
			return restic.Result{}, err
		}
		if err := os.WriteFile(filepath.Join(r.stagingTarget, "nested", "payload.txt"), []byte("payload"), 0o600); err != nil {
			return restic.Result{}, err
		}
		if r.fail {
			return restic.Result{Outcome: restic.Failure}, errors.New("injected restore failure")
		}
		return restic.Result{Outcome: restic.Success}, nil
	default:
		return restic.Result{}, errors.New("unexpected operation")
	}
}

type directoryRestoreFailure interface {
	error
	RestoreStage() string
	RestoreResidualPath() string
}

func (r *repoRunner) Execute(_ context.Context, op restic.Operation) (restic.Result, error) {
	r.operations = append(r.operations, op)
	if op.Kind == restic.VerifyRepository {
		if r.failVerify {
			return restic.Result{}, errors.New("repository is not readable")
		}
		output := r.verifyOutput
		if output == "" {
			output = `[]`
		}
		return restic.Result{Outcome: restic.Success, Stdout: output}, nil
	}
	if op.Kind == restic.CheckRepository && r.failCheck {
		return restic.Result{}, errors.New("damaged")
	}
	if op.Kind == restic.DumpSnapshot {
		if op.Output != nil {
			_, _ = io.WriteString(op.Output, r.dumpContent)
		}
		if r.failDump {
			return restic.Result{}, errors.New("dump failed")
		}
		return restic.Result{Outcome: restic.Success}, nil
	}
	output := `[{"id":"abc","time":"2026-07-11T00:00:00Z","paths":["/srv"]}]`
	if op.Kind == restic.ListSnapshotContents {
		output = r.contentsOutput
		if len(op.Arguments) != 0 && r.contentsByID[op.Arguments[0]] != "" {
			output = r.contentsByID[op.Arguments[0]]
		}
		if op.Output != nil {
			_, _ = io.WriteString(op.Output, output)
			// The executor diagnostic buffer is intentionally not the source of
			// truth for large listings. Simulate a truncated diagnostic copy.
			output = strings.SplitN(output, "\n", 2)[0]
		}
	}
	if op.Kind == restic.ForgetSnapshots && r.forgetOutput != "" {
		output = r.forgetOutput
	}
	if op.Kind == restic.ListKeys {
		output = `[{"id":"old-key","current":true}]`
	}
	if r.afterExecute != nil {
		r.afterExecute()
	}
	return restic.Result{Outcome: restic.Success, Stdout: output}, nil
}

func TestConnectExistingRepositoryVerifiesReadOnlyBeforePersisting(t *testing.T) {
	storage := &repoStore{remote: store.RemoteHostExecution{
		Host:               domain.RemoteHost{ID: "host", Name: "NAS", Host: "nas.example", Port: 2222, Username: "backup", HostFingerprint: "nas.example ssh-ed25519 AAAA"},
		PrivateKeySecretID: "ssh-key",
	}}
	secrets := repoSecrets{"ssh-key": []byte("PRIVATE KEY")}
	runner := &repoRunner{verifyOutput: `[{"id":"snapshot-a","time":"2026-07-11T00:00:00Z","paths":["/srv/photos"]}]`}
	service := New(storage, secrets, runner)
	candidate := domain.Repository{ID: "repo-existing", Name: "已有照片仓库", Engine: domain.ResticEngine, Kind: domain.SFTPRepository, RemoteHostID: "host", Path: "/backup/photos", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}

	snapshots, err := service.ConnectExisting(context.Background(), candidate, "existing-password", nil)
	if err != nil || len(snapshots) != 1 || snapshots[0].ID != "snapshot-a" {
		t.Fatalf("snapshots=%+v err=%v", snapshots, err)
	}
	if storage.created.ID != candidate.ID || storage.created.Status != "ready" || storage.secretID != "new-secret" || string(secrets["new-secret"]) != "existing-password" {
		t.Fatalf("created=%+v secretID=%q secrets=%+v", storage.created, storage.secretID, secrets)
	}
	if len(runner.operations) != 1 || runner.operations[0].Kind != restic.VerifyRepository || runner.operations[0].Repository.Location != "sftp:backup@nas.example:/backup/photos" || string(runner.operations[0].Repository.SSHPrivateKey) != "PRIVATE KEY" || string(runner.operations[0].Repository.KnownHosts) != storage.remote.Host.HostFingerprint {
		t.Fatalf("operations=%+v", runner.operations)
	}
}

func TestConnectExistingRepositoryFailureLeavesNoLocalResourceOrSecret(t *testing.T) {
	storage := &repoStore{}
	secrets := repoSecrets{}
	service := New(storage, secrets, &repoRunner{failVerify: true})
	candidate := domain.Repository{ID: "repo-existing", Name: "已有仓库", Engine: domain.ResticEngine, Kind: domain.LocalRepository, Path: "/backup/photos", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}

	if _, err := service.ConnectExisting(context.Background(), candidate, "existing-password", nil); err == nil {
		t.Fatal("expected read-only verification failure")
	}
	if storage.created.ID != "" || secrets["new-secret"] != nil {
		t.Fatalf("failed connection persisted resource=%+v secrets=%+v", storage.created, secrets)
	}
}

func TestConnectExistingS3RepositoryVerifiesThenPersistsPurposeBoundCredentials(t *testing.T) {
	storage := &repoStore{}
	secrets := repoSecrets{}
	runner := &repoRunner{}
	service := New(storage, secrets, runner)
	candidate := domain.Repository{
		ID: "repo-s3", Name: "object archive", Engine: domain.ResticEngine, Kind: domain.S3Repository, Path: "photos",
		S3: &domain.S3RepositoryConfig{Endpoint: "https://objects.example.com", Bucket: "backup-prod", Region: "us-east-1", PathStyle: true}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	credentials := &s3backend.Credentials{AccessKey: "access-private", SecretKey: "secret-private"}
	if _, err := service.ConnectExisting(context.Background(), candidate, "existing-password", credentials); err != nil {
		t.Fatal(err)
	}
	if len(runner.operations) != 1 {
		t.Fatalf("operations=%+v", runner.operations)
	}
	material := runner.operations[0].Repository
	if material.Location != "s3:https://objects.example.com/backup-prod/photos" || material.S3BucketLookup != "path" || material.S3AccessKey != credentials.AccessKey || material.S3SecretKey != credentials.SecretKey {
		t.Fatalf("material=%+v", material)
	}
	if storage.created.BackendSecretID != "new-s3-secret" || storage.secretID != "new-secret" {
		t.Fatalf("created=%+v passwordSecret=%q", storage.created, storage.secretID)
	}
	stored, err := s3backend.DecodeCredentials(secrets["new-s3-secret"])
	if err != nil || stored != *credentials {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestConnectExistingRepositoryDeletesSecretWhenPersistenceFails(t *testing.T) {
	storage := &repoStore{createErr: store.ErrConflict}
	secrets := repoSecrets{}
	service := New(storage, secrets, &repoRunner{})
	candidate := domain.Repository{ID: "repo-existing", Name: "已有仓库", Engine: domain.ResticEngine, Kind: domain.LocalRepository, Path: "/backup/photos", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}

	if _, err := service.ConnectExisting(context.Background(), candidate, "existing-password", nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err=%v", err)
	}
	if secrets["new-secret"] != nil {
		t.Fatalf("orphaned secret=%q", secrets["new-secret"])
	}
}

func TestVerifyImportedExistingRepositoryOnlyMarksReadyAfterReadOnlySuccess(t *testing.T) {
	storage := &repoStore{execution: store.RepositoryExecution{
		Repository:                 domain.Repository{ID: "repo", Name: "导入仓库", Engine: domain.ResticEngine, Kind: domain.LocalRepository, Path: "/backup", Status: "disconnected"},
		RepositoryPasswordSecretID: "pass",
	}}
	runner := &repoRunner{}
	service := New(storage, repoSecrets{"pass": []byte("password")}, runner)
	if _, err := service.VerifyExisting(context.Background(), "repo"); err != nil {
		t.Fatal(err)
	}
	if storage.status != "ready" || len(runner.operations) != 1 || runner.operations[0].Kind != restic.VerifyRepository {
		t.Fatalf("status=%q operations=%+v", storage.status, runner.operations)
	}

	storage.execution.Repository.Status = "ready"
	if _, err := service.VerifyExisting(context.Background(), "repo"); err == nil || len(runner.operations) != 1 {
		t.Fatalf("ready repository was re-verified: err=%v operations=%+v", err, runner.operations)
	}
}

func TestSnapshotContentsPagesStreamSafeBrowsableNodes(t *testing.T) {
	runner := &repoRunner{contentsOutput: strings.Join([]string{
		`{"struct_type":"snapshot","id":"abc"}`,
		`{"struct_type":"node","name":"photos","type":"dir","path":"/srv/photos"}`,
		`{"struct_type":"node","name":"one.jpg","type":"file","path":"/srv/photos/one.jpg","size":42}`,
		`{"struct_type":"node","name":"two.jpg","type":"file","path":"/srv/photos/two.jpg","size":84}`,
		`{"struct_type":"node","name":"escape","type":"file","path":"../escape"}`,
	}, "\n")}
	service := New(&repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}, RepositoryPasswordSecretID: "password"}}, repoSecrets{"password": []byte("secret")}, runner)
	first, err := service.BrowseSnapshotContents(context.Background(), "repo", "abc", SnapshotContentsQuery{Path: "/srv/photos", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].Path != "/srv/photos/one.jpg" || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first page=%+v", first)
	}
	second, err := service.BrowseSnapshotContents(context.Background(), "repo", "abc", SnapshotContentsQuery{Path: "/srv/photos", Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Path != "/srv/photos/two.jpg" || second.Truncated || second.NextCursor != "" {
		t.Fatalf("second page=%+v", second)
	}
	if got := runner.operations[len(runner.operations)-1]; got.Kind != restic.ListSnapshotContents || len(got.Arguments) != 2 || got.Arguments[0] != "abc" || got.Arguments[1] != "/srv/photos" || got.Output == nil {
		t.Fatalf("operation=%+v", got)
	}
	if _, err := service.BrowseSnapshotContents(context.Background(), "repo", "abc", SnapshotContentsQuery{Path: "/srv", Search: "jpg", Limit: 1, Cursor: first.NextCursor}); err == nil {
		t.Fatal("cursor from another query was accepted")
	}
}

func TestSnapshotContentsSearchReturnsExplicitContinuationPastFormerLimit(t *testing.T) {
	var output strings.Builder
	output.WriteString(`{"struct_type":"snapshot","id":"abc"}` + "\n")
	for index := 0; index < 10005; index++ {
		_, _ = io.WriteString(&output, fmt.Sprintf("{\"struct_type\":\"node\",\"name\":\"file-%05d\",\"type\":\"file\",\"path\":\"/srv/archive/file-%05d\",\"size\":1}\n", index, index))
	}
	runner := &repoRunner{contentsOutput: output.String()}
	service := New(&repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}, RepositoryPasswordSecretID: "password"}}, repoSecrets{"password": []byte("secret")}, runner)
	page, err := service.BrowseSnapshotContents(context.Background(), "repo", "abc", SnapshotContentsQuery{Path: "/srv", Search: "file-10004", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Path != "/srv/archive/file-10004" || page.Truncated {
		t.Fatalf("search page=%+v", page)
	}
}

func TestDirectoryRestorePreflightFindsSelectionPastFormerListingLimit(t *testing.T) {
	var output strings.Builder
	output.WriteString(`{"struct_type":"snapshot","id":"abc"}` + "\n")
	for index := 0; index < 10005; index++ {
		_, _ = io.WriteString(&output, fmt.Sprintf("{\"struct_type\":\"node\",\"name\":\"file-%05d\",\"type\":\"file\",\"path\":\"/srv/file-%05d\",\"size\":1}\n", index, index))
	}
	output.WriteString(`{"struct_type":"node","name":"late.txt","type":"file","path":"/srv/late.txt","size":7}` + "\n")
	runner := &repoRunner{contentsOutput: output.String()}
	service := New(&repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}, RepositoryPasswordSecretID: "password"}}, repoSecrets{"password": []byte("secret")}, runner)
	target := filepath.Join(t.TempDir(), "restored")
	result, err := service.PreflightDirectoryRestore(context.Background(), "repo", "abc", target, []string{"late.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Includes) != 1 || result.Includes[0] != "late.txt" {
		t.Fatalf("preflight=%+v", result)
	}
}

func TestCompareSnapshotsReturnsCompleteBoundedChangeSummary(t *testing.T) {
	runner := &repoRunner{contentsByID: map[string]string{
		"old": strings.Join([]string{
			`{"struct_type":"node","name":"same.txt","type":"file","path":"/srv/same.txt","size":1,"mtime":"2026-01-01T00:00:00Z"}`,
			`{"struct_type":"node","name":"changed.txt","type":"file","path":"/srv/changed.txt","size":2,"mtime":"2026-01-01T00:00:00Z"}`,
			`{"struct_type":"node","name":"removed.txt","type":"file","path":"/srv/removed.txt","size":3}`,
		}, "\n"),
		"new": strings.Join([]string{
			`{"struct_type":"node","name":"same.txt","type":"file","path":"/srv/same.txt","size":1,"mtime":"2026-01-01T00:00:00Z"}`,
			`{"struct_type":"node","name":"changed.txt","type":"file","path":"/srv/changed.txt","size":4,"mtime":"2026-01-02T00:00:00Z"}`,
			`{"struct_type":"node","name":"added.txt","type":"file","path":"/srv/added.txt","size":5}`,
		}, "\n"),
	}}
	service := New(&repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}, RepositoryPasswordSecretID: "password"}}, repoSecrets{"password": []byte("secret")}, runner)
	diff, err := service.CompareSnapshots(context.Background(), "repo", "old", "new", SnapshotDiffQuery{Path: "/srv", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if diff.Added != 1 || diff.Modified != 1 || diff.Removed != 1 || diff.Incomplete || len(diff.Items) != 2 || !diff.ExamplesTruncated {
		t.Fatalf("diff=%+v", diff)
	}
	if diff.Items[0].Path != "/srv/added.txt" || diff.Items[0].Change != "added" {
		t.Fatalf("items=%+v", diff.Items)
	}
}

func TestPasswordRotationAddsVerifiesSwitchesAndRetainsOldKey(t *testing.T) {
	storeFake := &repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Path: "/backup"}, Host: domain.RemoteHost{Host: "nas", Port: 22, Username: "backup", HostFingerprint: "known"}, PrivateKeySecretID: "key", RepositoryPasswordSecretID: "old-secret"}}
	secrets := repoSecrets{"key": []byte("key"), "old-secret": []byte("old-password")}
	runner := &repoRunner{}
	service := New(storeFake, secrets, runner)
	if err := service.RotatePassword(context.Background(), "repo", "new-password-strong"); err != nil {
		t.Fatal(err)
	}
	want := []restic.OperationKind{restic.ListKeys, restic.AddKey, restic.ListSnapshots}
	if len(runner.operations) != len(want) {
		t.Fatalf("operations=%+v", runner.operations)
	}
	for i := range want {
		if runner.operations[i].Kind != want[i] {
			t.Fatalf("op %d=%s", i, runner.operations[i].Kind)
		}
	}
	if storeFake.execution.RepositoryPasswordSecretID != "new-secret" || string(secrets["new-secret"]) != "new-password-strong" {
		t.Fatal("new password was not committed")
	}
	if _, ok := secrets["old-secret"]; !ok {
		t.Fatal("old secret was removed before explicit key revocation")
	}
}

func TestExplicitOldPasswordRevocationIsIdempotent(t *testing.T) {
	storeFake := &repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Path: "/backup"}, Host: domain.RemoteHost{Host: "nas", Port: 22, Username: "backup", HostFingerprint: "known"}, PrivateKeySecretID: "key", RepositoryPasswordSecretID: "old-secret"}}
	secrets := repoSecrets{"key": []byte("key"), "old-secret": []byte("old-password")}
	runner := &repoRunner{}
	service := New(storeFake, secrets, runner)
	if err := service.RotatePassword(context.Background(), "repo", "new-password-strong"); err != nil {
		t.Fatal(err)
	}
	status, err := service.PasswordRotationStatus(context.Background(), "repo")
	if err != nil || !status.Pending || status.OldKeyID != "old-key" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if err := service.RevokeOldPassword(context.Background(), "repo"); err != nil {
		t.Fatal(err)
	}
	if storeFake.pending != nil {
		t.Fatalf("old key revocation remained pending: %+v", storeFake.pending)
	}
	wantTail := []restic.OperationKind{restic.ListKeys, restic.RemoveKey}
	operations := runner.operations[len(runner.operations)-len(wantTail):]
	for index, kind := range wantTail {
		if operations[index].Kind != kind {
			t.Fatalf("revocation operations=%+v", operations)
		}
	}
	before := len(runner.operations)
	if err := service.RevokeOldPassword(context.Background(), "repo"); err != nil || len(runner.operations) != before {
		t.Fatalf("idempotent revoke err=%v operations=%+v", err, runner.operations)
	}
}

func TestPasswordRotationRejectsAnotherRotationWhileOldKeyIsPending(t *testing.T) {
	storeFake := &repoStore{
		execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Path: "/backup"}, Host: domain.RemoteHost{Host: "nas", Port: 22, Username: "backup", HostFingerprint: "known"}, PrivateKeySecretID: "key", RepositoryPasswordSecretID: "current-secret"},
		pending:   &store.RepositoryKeyRevocation{RepositoryID: "repo", KeyID: "older-key", SecretID: "older-secret"},
	}
	runner := &repoRunner{}
	service := New(storeFake, repoSecrets{"key": []byte("key"), "current-secret": []byte("current-password")}, runner)
	if err := service.RotatePassword(context.Background(), "repo", "another-password-strong"); err == nil || len(runner.operations) != 0 {
		t.Fatalf("pending rotation was not blocked: err=%v operations=%+v", err, runner.operations)
	}
}

func TestServiceInitializesListsAndMaintainsRepository(t *testing.T) {
	storeFake := &repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.SFTPRepository, Path: "/backup", Status: "uninitialized"}, Host: domain.RemoteHost{Host: "nas", Port: 22, Username: "backup", HostFingerprint: "nas ssh-ed25519 AAAA"}, PrivateKeySecretID: "key", RepositoryPasswordSecretID: "pass"}}
	runner := &repoRunner{}
	service := New(storeFake, repoSecrets{"key": []byte("key"), "pass": []byte("password")}, runner)
	if err := service.Initialize(context.Background(), "repo"); err != nil {
		t.Fatal(err)
	}
	storeFake.execution.Repository.Status = "ready"
	snapshots, err := service.Snapshots(context.Background(), "repo")
	if err != nil || len(snapshots) != 1 || snapshots[0].ID != "abc" {
		t.Fatalf("snapshots=%+v err=%v", snapshots, err)
	}
	if err := service.Maintain(context.Background(), "repo", domain.RetentionPolicy{KeepWithinDays: 30, KeepHourly: 24, KeepWeekly: 8}, false); err != nil {
		t.Fatal(err)
	}
	want := []restic.OperationKind{restic.InitializeRepo, restic.ListSnapshots, restic.ForgetSnapshots, restic.PruneRepository, restic.CheckRepository}
	if len(runner.operations) != len(want) {
		t.Fatalf("operations=%+v", runner.operations)
	}
	for i := range want {
		if runner.operations[i].Kind != want[i] {
			t.Fatalf("operation %d=%s", i, runner.operations[i].Kind)
		}
	}
	forgetArgs := runner.operations[2].Arguments
	joined := strings.Join(forgetArgs, " ")
	if !strings.Contains(joined, "--keep-within 30d") || !strings.Contains(joined, "--keep-hourly 24") || !strings.Contains(joined, "--keep-weekly 8") || !strings.Contains(joined, "--keep-tag rc:protected-partial") {
		t.Fatalf("unsafe retention arguments: %v", forgetArgs)
	}
}

func TestInitializePersistsReadyAfterCommandContextIsCancelled(t *testing.T) {
	storeFake := &repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "uninitialized"}, RepositoryPasswordSecretID: "pass"}}
	ctx, cancel := context.WithCancel(t.Context())
	runner := &repoRunner{afterExecute: cancel}
	service := New(storeFake, repoSecrets{"pass": []byte("password")}, runner)

	if err := service.Initialize(ctx, "repo"); err != nil {
		t.Fatalf("initialize after successful command: %v", err)
	}
	if storeFake.status != "ready" || storeFake.statusContextErr != nil {
		t.Fatalf("status=%q update context err=%v", storeFake.status, storeFake.statusContextErr)
	}
}

func TestServiceRejectsRepeatedRepositoryInitialization(t *testing.T) {
	storeFake := &repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}, RepositoryPasswordSecretID: "pass"}}
	runner := &repoRunner{}
	service := New(storeFake, repoSecrets{"pass": []byte("password")}, runner)
	if err := service.Initialize(context.Background(), "repo"); err == nil || len(runner.operations) != 0 {
		t.Fatalf("repeated initialization was not blocked: err=%v operations=%+v", err, runner.operations)
	}
}

func TestServiceMaintenanceDryRunOnlyForgets(t *testing.T) {
	storeFake := &repoStore{execution: store.RepositoryExecution{
		Repository:                 domain.Repository{ID: "repo", Kind: domain.SFTPRepository, Path: "/backup", Status: "ready"},
		Host:                       domain.RemoteHost{Host: "nas", Port: 22, Username: "backup", HostFingerprint: "known"},
		PrivateKeySecretID:         "key",
		RepositoryPasswordSecretID: "pass",
	}}
	runner := &repoRunner{}
	service := New(storeFake, repoSecrets{"key": []byte("key"), "pass": []byte("password")}, runner)

	if err := service.Maintain(context.Background(), "repo", domain.RetentionPolicy{KeepLast: 3}, true); err != nil {
		t.Fatal(err)
	}
	if len(runner.operations) != 1 || runner.operations[0].Kind != restic.ForgetSnapshots {
		t.Fatalf("dry-run operations=%+v", runner.operations)
	}
	joined := strings.Join(runner.operations[0].Arguments, " ")
	if !strings.Contains(joined, "--keep-last 3") || !strings.Contains(joined, "--dry-run") {
		t.Fatalf("dry-run arguments=%v", runner.operations[0].Arguments)
	}
}

func TestServiceMaintenancePreviewCountsKeptAndRemovedSnapshots(t *testing.T) {
	storeFake := &repoStore{execution: store.RepositoryExecution{
		Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}, RepositoryPasswordSecretID: "pass",
	}}
	runner := &repoRunner{forgetOutput: `[{"keep":[{"id":"a"},{"id":"b"}],"remove":[{"id":"c"}]}]`}
	service := New(storeFake, repoSecrets{"pass": []byte("password")}, runner)
	summary, err := service.PreviewMaintenance(context.Background(), "repo", domain.RetentionPolicy{KeepLast: 2})
	if err != nil || summary.KeepCount != 2 || summary.RemoveCount != 1 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
}

func TestRestoreDirectoryUsesSnapshotPathAndCommitsStaging(t *testing.T) {
	runner := &directoryRestoreRunner{}
	service := New(&repoStore{execution: store.RepositoryExecution{
		Repository:                 domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"},
		RepositoryPasswordSecretID: "pass",
	}}, repoSecrets{"pass": []byte("password")}, runner)
	locker := &recordingLocker{}
	service.SetLocker(locker)
	target := filepath.Join(t.TempDir(), "restored")

	if err := service.RestoreDirectory(context.Background(), "repo", "snap", target, nil, 128); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(runner.restoreArguments, " "); !strings.HasPrefix(got, "--limit-download 128 snap:/A/B/C --target ") {
		t.Fatalf("restore arguments=%v", runner.restoreArguments)
	}
	if _, err := os.Stat(filepath.Join(target, "nested", "payload.txt")); err != nil {
		t.Fatalf("committed payload: %v", err)
	}
	if runner.stagingTarget == target || filepath.Dir(runner.stagingTarget) != filepath.Dir(target) {
		t.Fatalf("unsafe staging target=%q final=%q", runner.stagingTarget, target)
	}
	if locker.calls != 1 {
		t.Fatalf("restore lock calls=%d", locker.calls)
	}
}

func TestDirectoryRestorePreflightDoesNotCreateTarget(t *testing.T) {
	runner := &directoryRestoreRunner{}
	service := New(&repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}, RepositoryPasswordSecretID: "pass"}}, repoSecrets{"pass": []byte("password")}, runner)
	target := filepath.Join(t.TempDir(), "restored")
	result, err := service.PreflightDirectoryRestore(context.Background(), "repo", "snap", target, []string{"nested"})
	if err != nil || result.SourcePath != "/A/B/C" || result.Target != target || len(result.Includes) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("preflight created target: %v", err)
	}
}

func TestRestoreDirectoryFailureLeavesTargetRetryable(t *testing.T) {
	runner := &directoryRestoreRunner{fail: true}
	service := New(&repoStore{execution: store.RepositoryExecution{
		Repository:                 domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"},
		RepositoryPasswordSecretID: "pass",
	}}, repoSecrets{"pass": []byte("password")}, runner)
	target := filepath.Join(t.TempDir(), "restored")

	err := service.RestoreDirectory(context.Background(), "repo", "snap", target, nil, 0)
	var restoreErr directoryRestoreFailure
	if !errors.As(err, &restoreErr) {
		t.Fatalf("restore error does not expose residual state: %v", err)
	}
	if restoreErr.RestoreStage() != "restore" || restoreErr.RestoreResidualPath() == "" {
		t.Fatalf("restore failure stage=%q residual=%q", restoreErr.RestoreStage(), restoreErr.RestoreResidualPath())
	}
	if _, err := os.Stat(restoreErr.RestoreResidualPath()); err != nil {
		t.Fatalf("isolated residual: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("failed restore published final target: %v", err)
	}

	runner.fail = false
	if err := service.RestoreDirectory(context.Background(), "repo", "snap", target, nil, 0); err != nil {
		t.Fatalf("retry restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "nested", "payload.txt")); err != nil {
		t.Fatalf("retried payload: %v", err)
	}
}

func TestRestoreDirectoryRejectsIncludesOutsideSnapshotSource(t *testing.T) {
	for _, include := range []string{"/etc/passwd", "../sibling", "nested/../../sibling"} {
		t.Run(include, func(t *testing.T) {
			runner := &directoryRestoreRunner{}
			service := New(&repoStore{execution: store.RepositoryExecution{
				Repository:                 domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"},
				RepositoryPasswordSecretID: "pass",
			}}, repoSecrets{"pass": []byte("password")}, runner)
			target := filepath.Join(t.TempDir(), "restored")

			if err := service.RestoreDirectory(context.Background(), "repo", "snap", target, []string{include}, 0); err == nil {
				t.Fatalf("unsafe include %q was accepted", include)
			}
			if len(runner.restoreArguments) != 0 {
				t.Fatalf("unsafe include reached Restic: %v", runner.restoreArguments)
			}
		})
	}
}

func TestDumpAppliesControlledDownloadLimit(t *testing.T) {
	runner := &repoRunner{}
	service := New(&repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}, RepositoryPasswordSecretID: "pass"}}, repoSecrets{"pass": []byte("password")}, runner)
	if err := service.Dump(context.Background(), "repo", "snap", "database.sql", 256, io.Discard); err != nil {
		t.Fatal(err)
	}
	operation := runner.operations[len(runner.operations)-1]
	if operation.Kind != restic.DumpSnapshot || strings.Join(operation.Arguments, " ") != "--limit-download 256 snap database.sql" {
		t.Fatalf("operation=%+v", operation)
	}
	if err := service.Dump(context.Background(), "repo", "snap", "database.sql", -1, io.Discard); err == nil {
		t.Fatal("negative download limit was accepted")
	}
}

func TestRestoreDumpFileStreamsToSecureNewFileAndUsesRepositoryLock(t *testing.T) {
	runner := &repoRunner{dumpContent: "CREATE TABLE example(id INT);"}
	service := New(&repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}, RepositoryPasswordSecretID: "pass"}}, repoSecrets{"pass": []byte("password")}, runner)
	locker := &recordingLocker{}
	service.SetLocker(locker)
	target := filepath.Join(t.TempDir(), "database.sql")

	if err := service.RestoreDumpFile(context.Background(), "repo", "snap", "database.sql", target, 128); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != runner.dumpContent {
		t.Fatalf("content=%q err=%v", content, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("output permissions=%o", info.Mode().Perm())
	}
	if locker.calls != 1 {
		t.Fatalf("restore lock calls=%d", locker.calls)
	}
	operation := runner.operations[len(runner.operations)-1]
	if operation.Kind != restic.DumpSnapshot || strings.Join(operation.Arguments, " ") != "--limit-download 128 snap database.sql" {
		t.Fatalf("operation=%+v", operation)
	}
}

func TestRestoreDumpFileDoesNotPublishFailedOrUnsafeOutput(t *testing.T) {
	for _, test := range []struct {
		name     string
		filename string
	}{
		{name: "existing target", filename: "database.sql"},
		{name: "path traversal source", filename: "../database.sql"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			target := filepath.Join(directory, "database.sql")
			if test.name == "existing target" {
				if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runner := &repoRunner{failDump: true, dumpContent: "partial"}
			service := New(&repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}, RepositoryPasswordSecretID: "pass"}}, repoSecrets{"pass": []byte("password")}, runner)

			err := service.RestoreDumpFile(context.Background(), "repo", "snap", test.filename, target, 0)
			if test.name == "existing target" {
				if err == nil {
					t.Fatalf("existing target reached dump: err=%v operations=%+v", err, runner.operations)
				}
				for _, operation := range runner.operations {
					if operation.Kind == restic.DumpSnapshot {
						t.Fatalf("existing target reached dump: operations=%+v", runner.operations)
					}
				}
				content, readErr := os.ReadFile(target)
				if readErr != nil || string(content) != "keep" {
					t.Fatalf("existing target changed content=%q err=%v", content, readErr)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe source filename was accepted")
			}
			if len(runner.operations) != 0 {
				t.Fatalf("unsafe source reached Restic: %+v", runner.operations)
			}
		})
	}
}

func TestDirectoryRestorePreflightRejectsIncludeMissingFromSnapshot(t *testing.T) {
	runner := &directoryRestoreRunner{}
	service := New(&repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}, RepositoryPasswordSecretID: "pass"}}, repoSecrets{"pass": []byte("password")}, runner)
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := service.PreflightDirectoryRestore(context.Background(), "repo", "snap", target, []string{"not-in-snapshot"}); err == nil {
		t.Fatal("expected missing include to be rejected")
	}
}

func TestServiceUsesLocalRepositoryWithoutSSHMaterial(t *testing.T) {
	storeFake := &repoStore{execution: store.RepositoryExecution{
		Repository:                 domain.Repository{ID: "local", Kind: domain.LocalRepository, Path: "/Volumes/Backup/photos", Status: "uninitialized"},
		RepositoryPasswordSecretID: "pass",
	}}
	runner := &repoRunner{}
	service := New(storeFake, repoSecrets{"pass": []byte("password")}, runner)

	if err := service.Initialize(context.Background(), "local"); err != nil {
		t.Fatal(err)
	}
	if len(runner.operations) != 1 {
		t.Fatalf("operations=%+v", runner.operations)
	}
	repository := runner.operations[0].Repository
	if repository.Location != "/Volumes/Backup/photos" || len(repository.SSHPrivateKey) != 0 || repository.SSHPort != 0 || len(repository.KnownHosts) != 0 {
		t.Fatalf("local repository material=%+v", repository)
	}
	if storeFake.status != "ready" {
		t.Fatalf("status=%q", storeFake.status)
	}
}

func TestServiceBlocksMaintenanceForAbnormalRepository(t *testing.T) {
	storeFake := &repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Path: "/backup", Status: "abnormal"}, Host: domain.RemoteHost{Host: "nas", Port: 22, Username: "backup", HostFingerprint: "known"}, PrivateKeySecretID: "key", RepositoryPasswordSecretID: "pass"}}
	runner := &repoRunner{}
	service := New(storeFake, repoSecrets{"key": []byte("key"), "pass": []byte("password")}, runner)
	if err := service.Maintain(context.Background(), "repo", domain.RetentionPolicy{KeepLast: 3}, false); err == nil {
		t.Fatal("abnormal repository maintenance was accepted")
	}
	if len(runner.operations) != 0 {
		t.Fatalf("abnormal repository executed operations: %+v", runner.operations)
	}
}

func TestServiceMarksRepositoryAbnormalWhenCheckFails(t *testing.T) {
	storeFake := &repoStore{execution: store.RepositoryExecution{Repository: domain.Repository{ID: "repo", Path: "/backup", Status: "ready"}, Host: domain.RemoteHost{Host: "nas", Port: 22, Username: "backup", HostFingerprint: "known"}, PrivateKeySecretID: "key", RepositoryPasswordSecretID: "pass"}}
	service := New(storeFake, repoSecrets{"key": []byte("key"), "pass": []byte("password")}, &repoRunner{failCheck: true})
	if err := service.Maintain(context.Background(), "repo", domain.RetentionPolicy{KeepLast: 3}, false); err == nil || storeFake.status != "abnormal" {
		t.Fatalf("err=%v status=%q", err, storeFake.status)
	}
}
