package store

import (
	"context"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestRestoreVerificationPolicyAndEvidenceAreDurable(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	policy := domain.RestoreVerificationPolicy{
		TaskID: task.ID, SelectionPath: "album/sample.jpg", MaximumBytes: 64 << 20, MaximumSuccessAgeHours: 24 * 8,
		Schedule: domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Sunday, TimeOfDay: "04:00"}, Timezone: "UTC",
		Enabled: true, CatchUpWindowMinutes: 60, UpdatedAt: now,
	}
	if err := s.SaveRestoreVerificationPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	policies, err := s.ListRestoreVerificationPolicies(ctx)
	if err != nil || len(policies) != 1 {
		t.Fatalf("policies=%+v err=%v", policies, err)
	}
	gotPolicy := policies[0]
	if gotPolicy.TaskID != task.ID || gotPolicy.SelectionPath != "album/sample.jpg" || gotPolicy.MaximumBytes != 64<<20 || !gotPolicy.Enabled || !gotPolicy.ScheduleAnchorAt.Equal(now) {
		t.Fatalf("policy=%+v", gotPolicy)
	}

	attempt := RestoreVerificationRecord{
		ID: "verification-1", TaskID: task.ID, RepositoryID: task.RepositoryID, SnapshotID: "snapshot-a", SelectionPath: policy.SelectionPath,
		Trigger: "manual", Status: "running", StartedAt: now, CleanupStatus: "pending",
	}
	if err := s.CreateRestoreVerification(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(2 * time.Second)
	if err := s.FinishRestoreVerification(ctx, attempt.ID, RestoreVerificationFinish{Status: "success", FinishedAt: finished, FileCount: 1, ByteCount: 7, ManifestSHA256: "sha256:abc", CleanupStatus: "removed"}); err != nil {
		t.Fatal(err)
	}
	records, err := s.ListRestoreVerifications(ctx, task.ID, 20)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	got := records[0]
	if got.Status != "success" || got.FileCount != 1 || got.ByteCount != 7 || got.ManifestSHA256 != "sha256:abc" || got.FinishedAt == nil || !got.FinishedAt.Equal(finished) || got.CleanupStatus != "removed" {
		t.Fatalf("record=%+v", got)
	}
	latest, err := s.LatestSuccessfulRestoreVerifications(ctx)
	if err != nil || latest[task.ID].ID != attempt.ID {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}

func TestRestoreVerificationRecoveryMarksEvidenceInterruptedAndCleanupRequired(t *testing.T) {
	s, task := createIdentityFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	if err := s.CreateRestoreVerification(ctx, RestoreVerificationRecord{ID: "verification-running", TaskID: task.ID, RepositoryID: task.RepositoryID, SnapshotID: "snapshot", SelectionPath: "sample", Trigger: "scheduled", Status: "running", StartedAt: now, CleanupStatus: "pending"}); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.RecoverInterruptedRestoreVerifications(ctx, now.Add(time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	record, err := s.RestoreVerification(ctx, "verification-running")
	if err != nil || record.Status != "interrupted" || record.CleanupStatus != "required" || record.FinishedAt == nil {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestEnabledRestoreVerificationPolicyRejectsUnsupportedTask(t *testing.T) {
	s, task := createIdentityFixture(t)
	task.Enabled = false
	task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	if err := s.UpdateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	policy := domain.RestoreVerificationPolicy{TaskID: task.ID, SelectionPath: "sample", MaximumBytes: 1024, MaximumSuccessAgeHours: 48, Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 24}, Timezone: "UTC", Enabled: true, UpdatedAt: time.Now().UTC()}
	if err := s.SaveRestoreVerificationPolicy(context.Background(), policy); err != ErrConflict {
		t.Fatalf("disabled task policy error=%v", err)
	}
}

func TestEnabledRestoreVerificationPolicyPreventsTaskDisable(t *testing.T) {
	s, task := createIdentityFixture(t)
	policy := domain.RestoreVerificationPolicy{TaskID: task.ID, SelectionPath: "sample", MaximumBytes: 1024, MaximumSuccessAgeHours: 48, Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 24}, Timezone: "UTC", Enabled: true, UpdatedAt: time.Now().UTC()}
	if err := s.SaveRestoreVerificationPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	task.Enabled = false
	task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	if err := s.UpdateTask(context.Background(), task); err != ErrConflict {
		t.Fatalf("disable task error=%v", err)
	}
}
