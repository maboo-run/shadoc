package restoreverification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
	"github.com/maboo-run/shadoc/internal/store"
)

type verificationStore struct {
	policy    domain.RestoreVerificationPolicy
	execution store.TaskExecution
	latest    store.RunRecord
	records   map[string]store.RestoreVerificationRecord
}

func (s *verificationStore) RestoreVerificationPolicy(context.Context, string) (domain.RestoreVerificationPolicy, error) {
	return s.policy, nil
}
func (s *verificationStore) LoadTaskExecution(context.Context, string) (store.TaskExecution, error) {
	return s.execution, nil
}
func (s *verificationStore) LatestSuccessfulRun(context.Context, string) (store.RunRecord, error) {
	return s.latest, nil
}
func (s *verificationStore) CreateRestoreVerification(_ context.Context, record store.RestoreVerificationRecord) error {
	if s.records == nil {
		s.records = map[string]store.RestoreVerificationRecord{}
	}
	s.records[record.ID] = record
	return nil
}
func (s *verificationStore) FinishRestoreVerification(_ context.Context, id string, finish store.RestoreVerificationFinish) error {
	record, ok := s.records[id]
	if !ok || record.Status != "running" {
		return errors.New("record is not running")
	}
	record.Status, record.FinishedAt = finish.Status, &finish.FinishedAt
	record.FileCount, record.ByteCount, record.ManifestSHA256 = finish.FileCount, finish.ByteCount, finish.ManifestSHA256
	record.CleanupStatus, record.ErrorSummary = finish.CleanupStatus, finish.ErrorSummary
	s.records[id] = record
	return nil
}
func (s *verificationStore) RestoreVerification(_ context.Context, id string) (store.RestoreVerificationRecord, error) {
	record, ok := s.records[id]
	if !ok {
		return store.RestoreVerificationRecord{}, errors.New("not found")
	}
	return record, nil
}
func (s *verificationStore) ResolveRestoreVerificationCleanup(_ context.Context, id string) error {
	record := s.records[id]
	record.CleanupStatus = "removed"
	s.records[id] = record
	return nil
}

type verificationRepository struct {
	repositoryID string
	snapshotID   string
	target       string
	includes     []string
	download     int
	payload      []byte
	makeSymlink  bool
}

func (r *verificationRepository) RestoreDirectory(_ context.Context, repositoryID, snapshotID, target string, includes []string, download int) error {
	r.repositoryID, r.snapshotID, r.target, r.includes, r.download = repositoryID, snapshotID, target, append([]string(nil), includes...), download
	if err := os.MkdirAll(filepath.Join(target, "album"), 0o700); err != nil {
		return err
	}
	if r.makeSymlink {
		return os.Symlink("/etc/passwd", filepath.Join(target, "album", "sample.jpg"))
	}
	return os.WriteFile(filepath.Join(target, "album", "sample.jpg"), r.payload, 0o600)
}

func TestRunRestoresHashesAndRemovesControlledTarget(t *testing.T) {
	now := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	storage := verificationFixture(now)
	repository := &verificationRepository{payload: []byte("payload")}
	root := filepath.Join(t.TempDir(), "verification-root")
	service := New(storage, repository, root, func() time.Time { return now })
	service.newID = func() string { return "verification-1" }

	record, err := service.Run(context.Background(), "task", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "success" || record.FileCount != 1 || record.ByteCount != 7 || record.ManifestSHA256 == "" || record.CleanupStatus != "removed" || record.FinishedAt == nil {
		t.Fatalf("record=%+v", record)
	}
	if repository.repositoryID != "repo" || repository.snapshotID != "snapshot" || repository.download != 128 || len(repository.includes) != 1 || repository.includes[0] != "album/sample.jpg" {
		t.Fatalf("restore repository=%+v", repository)
	}
	if _, err := os.Stat(filepath.Join(root, "verification-1")); !os.IsNotExist(err) {
		t.Fatalf("verification target was not removed: %v", err)
	}
}

func TestRunFailsClosedWhenRestoredBytesExceedPolicy(t *testing.T) {
	now := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	storage := verificationFixture(now)
	storage.policy.MaximumBytes = 4
	repository := &verificationRepository{payload: []byte("payload")}
	root := filepath.Join(t.TempDir(), "verification-root")
	service := New(storage, repository, root, func() time.Time { return now })
	service.newID = func() string { return "verification-large" }

	record, err := service.Run(context.Background(), "task", "scheduled")
	if err == nil || record.Status != "failed" || record.CleanupStatus != "removed" || record.ByteCount > storage.policy.MaximumBytes {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "verification-large")); !os.IsNotExist(statErr) {
		t.Fatalf("failed verification target was not removed: %v", statErr)
	}
}

func TestRunRejectsSymlinksAndPersistsCleanupResidueUntilSafeRetry(t *testing.T) {
	now := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	storage := verificationFixture(now)
	repository := &verificationRepository{makeSymlink: true}
	root := filepath.Join(t.TempDir(), "verification-root")
	service := New(storage, repository, root, func() time.Time { return now })
	service.newID = func() string { return "verification-residual" }
	service.removeAll = func(string) error { return errors.New("injected cleanup failure") }

	record, err := service.Run(context.Background(), "task", "manual")
	if err == nil || record.Status != "cleanup_required" || record.CleanupStatus != "required" {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	service.removeAll = os.RemoveAll
	cleaned, err := service.Cleanup(context.Background(), record.ID)
	if err != nil || cleaned.CleanupStatus != "removed" {
		t.Fatalf("cleaned=%+v err=%v", cleaned, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "verification-residual")); !os.IsNotExist(statErr) {
		t.Fatalf("cleanup retry left target: %v", statErr)
	}
}

func TestRunRejectsConcurrentVerificationForSameTask(t *testing.T) {
	now := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	storage := verificationFixture(now)
	started, release := make(chan struct{}), make(chan struct{})
	repository := &blockingVerificationRepository{verificationRepository: verificationRepository{payload: []byte("payload")}, started: started, release: release}
	service := New(storage, repository, filepath.Join(t.TempDir(), "verification-root"), func() time.Time { return now })
	service.newID = func() string { return "verification-concurrent" }
	done := make(chan error, 1)
	go func() {
		_, err := service.Run(context.Background(), "task", "scheduled")
		done <- err
	}()
	<-started

	if _, err := service.Run(context.Background(), "task", "manual"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent run error=%v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type blockingVerificationRepository struct {
	verificationRepository
	started chan struct{}
	release chan struct{}
}

func (r *blockingVerificationRepository) RestoreDirectory(ctx context.Context, repositoryID, snapshotID, target string, includes []string, download int) error {
	close(r.started)
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.verificationRepository.RestoreDirectory(ctx, repositoryID, snapshotID, target, includes, download)
}

func verificationFixture(now time.Time) *verificationStore {
	task := domain.Task{
		ID: "task", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/srv/photos"}, Enabled: true,
		ExecutionTarget: execution.Target{Kind: execution.Local}, Resources: domain.ResourcePolicy{DownloadKiBPerSecond: 128}, CreatedAt: now, UpdatedAt: now,
	}
	return &verificationStore{
		policy: domain.RestoreVerificationPolicy{
			TaskID: task.ID, SelectionPath: "album/sample.jpg", MaximumBytes: 64 << 20, MaximumSuccessAgeHours: 192,
			Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 24}, Timezone: "UTC", Enabled: true, CatchUpWindowMinutes: 60, UpdatedAt: now,
		},
		execution: store.TaskExecution{Task: task, Repository: domain.Repository{ID: "repo", Kind: domain.LocalRepository, Path: "/backup", Status: "ready"}},
		latest:    store.RunRecord{ID: "run", TaskID: task.ID, Status: "success", SnapshotID: "snapshot", StartedAt: now.Add(-time.Hour)},
	}
}
