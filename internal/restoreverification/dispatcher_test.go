package restoreverification

import (
	"context"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestDispatcherRunsDueRestoreVerificationAndLinksEvidence(t *testing.T) {
	database, task, now := createDispatcherFixture(t)
	runner := &dispatcherRunnerFake{result: store.RestoreVerificationRecord{ID: "verification-1", Status: "success"}}
	dispatcher := NewDispatcher(database, runner)

	launched, err := dispatcher.Tick(context.Background(), now)
	if err != nil || launched != 1 {
		t.Fatalf("launched=%d err=%v", launched, err)
	}
	dispatcher.wg.Wait()
	occurrences, err := database.ListScheduleOccurrences(context.Background(), "restore_verification", task.ID, 10)
	if err != nil || len(occurrences) != 1 {
		t.Fatalf("occurrences=%+v err=%v", occurrences, err)
	}
	if occurrences[0].Status != "success" || len(occurrences[0].RunIDs) != 1 || occurrences[0].RunIDs[0] != "verification-1" {
		t.Fatalf("occurrence=%+v", occurrences[0])
	}
	if runner.trigger != "scheduled" || runner.taskID != task.ID {
		t.Fatalf("runner=%+v", runner)
	}
}

func TestDispatcherDoesNotOverlapRestoreVerificationForOneTask(t *testing.T) {
	database, task, now := createDispatcherFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &dispatcherRunnerFake{started: started, release: release, result: store.RestoreVerificationRecord{ID: "verification-1", Status: "success"}}
	dispatcher := NewDispatcher(database, runner)

	if launched, err := dispatcher.Tick(context.Background(), now); err != nil || launched != 1 {
		t.Fatalf("first tick launched=%d err=%v", launched, err)
	}
	<-started
	if launched, err := dispatcher.Tick(context.Background(), now.Add(time.Hour)); err != nil || launched != 0 {
		t.Fatalf("overlapping tick launched=%d err=%v", launched, err)
	}
	close(release)
	dispatcher.wg.Wait()
	occurrences, err := database.ListScheduleOccurrences(context.Background(), "restore_verification", task.ID, 10)
	if err != nil || len(occurrences) != 1 {
		t.Fatalf("occurrences=%+v err=%v", occurrences, err)
	}
}

type dispatcherRunnerFake struct {
	taskID  string
	trigger string
	started chan struct{}
	release chan struct{}
	result  store.RestoreVerificationRecord
	err     error
}

func (r *dispatcherRunnerFake) Run(_ context.Context, taskID, trigger string) (store.RestoreVerificationRecord, error) {
	r.taskID, r.trigger = taskID, trigger
	if r.started != nil {
		close(r.started)
	}
	if r.release != nil {
		<-r.release
	}
	return r.result, r.err
}

func createDispatcherFixture(t *testing.T) (*store.Store, domain.Task, time.Time) {
	t.Helper()
	database, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	created := now.Add(-time.Hour)
	if err := database.SaveSecret(ctx, "repository-password", "repository-password", []byte("ciphertext"), created); err != nil {
		t.Fatal(err)
	}
	repository := domain.Repository{ID: "repo", Name: "验证仓库", Kind: domain.LocalRepository, Path: "/repo", Status: "ready", CreatedAt: created, UpdatedAt: created}
	if err := database.CreateRepository(ctx, repository, "repository-password"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", Name: "照片", Kind: domain.DirectoryTask, RepositoryID: repository.ID, Directory: &domain.DirectorySource{Path: "/srv/photos"}, Enabled: true, CreatedAt: created, UpdatedAt: created}
	if err := database.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	policy := domain.RestoreVerificationPolicy{
		TaskID: task.ID, Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 1}, Timezone: "UTC",
		SelectionPath: "album/sample.jpg", MaximumBytes: 1 << 20, MaximumSuccessAgeHours: 24,
		Enabled: true, CatchUpWindowMinutes: 60, UpdatedAt: created,
	}
	if err := database.SaveRestoreVerificationPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	return database, task, now
}
