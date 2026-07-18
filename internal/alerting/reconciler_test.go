package alerting

import (
	"context"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestReconcileRaisesActionableHealthAlertsAndRetainsResolvedHistory(t *testing.T) {
	database, now, task, repository, plan := createHealthFixture(t)
	capacityPolicy, err := database.RepositoryCapacityPolicy(context.Background(), repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	capacityPolicy.MinimumAvailablePercent = 10
	if err := database.SaveRepositoryCapacityPolicy(context.Background(), capacityPolicy, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	service := New(database, func() time.Time { return now })
	ctx := context.Background()

	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	active, err := service.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"task:" + task.ID + ":stale",
		"repository:" + repository.ID + ":integrity",
		"repository:" + repository.ID + ":capacity-low",
		"agent:agent-a:offline",
		"plan:" + plan.ID + ":schedule",
		"maintenance:" + repository.ID + ":schedule",
	} {
		if stateByKey(active, key).StateKey == "" {
			t.Fatalf("missing alert %q in %+v", key, active)
		}
	}
	if len(active) != 6 {
		t.Fatalf("active=%+v", active)
	}
	if stateByKey(active, "plan:"+plan.ID+":schedule").Severity != store.AlertCritical || stateByKey(active, "agent:agent-a:offline").TargetPage != "Agent 节点" || stateByKey(active, "repository:"+repository.ID+":capacity-low").RecoveryCondition == "" {
		t.Fatalf("alerts are not actionable: %+v", active)
	}
	history, err := service.History(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	historyAgain, err := service.History(ctx, 100)
	if err != nil || len(historyAgain) != len(history) {
		t.Fatalf("unchanged reconciliation created event noise: before=%d after=%d err=%v", len(history), len(historyAgain), err)
	}

	recoveredAt := now.Add(time.Minute)
	if err := database.StartRun(ctx, store.RunRecord{ID: "run-success", TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: recoveredAt}); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishRun(ctx, "run-success", "success", recoveredAt.Add(time.Minute), 1, "snapshot", map[string]any{}, ""); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRepositoryStatus(ctx, repository.ID, "ready"); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveRepositoryCapacity(ctx, repository.ID, domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: 500, CheckedAt: recoveredAt}); err != nil {
		t.Fatal(err)
	}
	if err := database.HeartbeatAgent(ctx, "agent-a", []string{"restic"}, recoveredAt); err != nil {
		t.Fatal(err)
	}
	createFinishedOccurrence(t, database, "plan-success", "plan", plan.ID, recoveredAt, "success")
	createFinishedOccurrence(t, database, "maintenance-success", "maintenance", repository.ID, recoveredAt, "success")
	now = recoveredAt.Add(time.Minute)
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	active, err = service.Active(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active after recovery=%+v err=%v", active, err)
	}
	history, err = service.History(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	resolved := map[string]bool{}
	for _, event := range history {
		if event.Transition == store.AlertResolvedTransition {
			resolved[event.StateKey] = true
		}
	}
	if len(resolved) != 6 {
		t.Fatalf("resolved history=%+v events=%+v", resolved, history)
	}
}

func TestReconcileRaisesAgentCertificateThresholdAndProtocolAlerts(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		id       string
		days     int
		protocol int
	}{
		{id: "agent-30", days: 29, protocol: 1},
		{id: "agent-14", days: 13, protocol: 1},
		{id: "agent-7", days: 6, protocol: 1},
		{id: "agent-incompatible", days: 90, protocol: 2},
		{id: "agent-renew-failed", days: 20, protocol: 1},
	} {
		expires := now.Add(time.Duration(item.days) * 24 * time.Hour)
		heartbeat := now.Add(-time.Second)
		renewalStatus := ""
		if item.id == "agent-renew-failed" {
			renewalStatus = "failed"
		}
		if err := database.SaveAgent(t.Context(), store.AgentRecord{
			ID: item.id, CertificateSerial: "serial-" + item.id, CertificateNotAfter: &expires,
			Status: "online", Capabilities: []string{"restic"}, BuildVersion: "v1.4.0",
			ProtocolMin: item.protocol, ProtocolMax: item.protocol, OS: "linux", Arch: "amd64",
			RenewalStatus:   renewalStatus,
			LastHeartbeatAt: &heartbeat, CreatedAt: now.Add(-time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	service := New(database, func() time.Time { return now })
	if err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	active, err := service.Active(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stateByKey(active, "agent:agent-30:certificate-expiry").Severity != store.AlertInfo {
		t.Fatalf("30-day alert=%+v", active)
	}
	if stateByKey(active, "agent:agent-14:certificate-expiry").Severity != store.AlertWarning {
		t.Fatalf("14-day alert=%+v", active)
	}
	if stateByKey(active, "agent:agent-7:certificate-expiry").Severity != store.AlertCritical {
		t.Fatalf("7-day alert=%+v", active)
	}
	if stateByKey(active, "agent:agent-incompatible:protocol").StateKey == "" {
		t.Fatalf("missing incompatible protocol alert: %+v", active)
	}
	if stateByKey(active, "agent:agent-renew-failed:certificate-renewal").StateKey == "" {
		t.Fatalf("missing certificate renewal failure alert: %+v", active)
	}
}

func TestReconcileSeparatesCapacityThresholdForecastStaleAndProbeFailureAlerts(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	created := now.Add(-96 * time.Hour)
	if err := database.SaveSecret(ctx, "password", "repository-password", []byte("cipher"), created); err != nil {
		t.Fatal(err)
	}
	repository := domain.Repository{ID: "repo", Name: "容量仓库", Kind: domain.LocalRepository, Path: "/repo", Status: "ready", CreatedAt: created, UpdatedAt: created}
	if err := database.CreateRepository(ctx, repository, "password"); err != nil {
		t.Fatal(err)
	}
	for index, available := range []uint64{300, 200, 100} {
		checkedAt := now.Add(time.Duration(index-3) * 24 * time.Hour)
		if err := database.SaveRepositoryCapacity(ctx, repository.ID, domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: available, CheckedAt: checkedAt}); err != nil {
			t.Fatal(err)
		}
	}
	policy := domain.RepositoryCapacityPolicy{
		RepositoryID: repository.ID, Enabled: true, ProbeIntervalMinutes: 60,
		MinimumAvailableBytes: 150, MinimumAvailablePercent: 20, ExhaustionWarningDays: 30,
	}
	if err := database.SaveRepositoryCapacityPolicy(ctx, policy, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordRepositoryCapacityFailure(ctx, repository.ID, now.Add(-30*time.Minute), "agent unavailable"); err != nil {
		t.Fatal(err)
	}
	service := New(database, func() time.Time { return now })
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	active, err := service.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"capacity-low", "capacity-forecast", "capacity-stale", "capacity-probe"} {
		state := stateByKey(active, "repository:"+repository.ID+":"+suffix)
		if state.StateKey == "" || state.TargetPage != "备份仓库" || state.RecoveryCondition == "" {
			t.Fatalf("missing actionable capacity alert %q in %+v", suffix, active)
		}
	}

	recoveredAt := now.Add(time.Minute)
	if err := database.SaveRepositoryCapacity(ctx, repository.ID, domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: 900, CheckedAt: recoveredAt}); err != nil {
		t.Fatal(err)
	}
	now = recoveredAt.Add(time.Minute)
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	active, err = service.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"capacity-low", "capacity-forecast", "capacity-stale", "capacity-probe"} {
		if stateByKey(active, "repository:"+repository.ID+":"+suffix).StateKey != "" {
			t.Fatalf("capacity alert %q did not resolve: %+v", suffix, active)
		}
	}
}

func TestReconcileDoesNotInventExpectationsForUnconfiguredOrRetiredObjects(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	if err := database.SaveSecret(ctx, "password", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateRepository(ctx, domain.Repository{ID: "repo", Name: "未初始化", Kind: domain.LocalRepository, Path: "/repo", Status: "uninitialized", CreatedAt: now.Add(-30 * 24 * time.Hour), UpdatedAt: now}, "password"); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveAgent(ctx, store.AgentRecord{ID: "retired", CertificateSerial: "serial-retired", Status: "revoked", CreatedAt: now.Add(-time.Hour), RevokedAt: timePointerForAlert(now.Add(-time.Minute))}); err != nil {
		t.Fatal(err)
	}
	service := New(database, func() time.Time { return now })
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	active, err := service.Active(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active=%+v err=%v", active, err)
	}
}

func TestReconcileDistinguishesRestoreVerificationFailuresStalenessAndCleanup(t *testing.T) {
	database, now, task, _, _ := createHealthFixture(t)
	ctx := context.Background()
	policy := domain.RestoreVerificationPolicy{
		TaskID: task.ID, Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 24}, Timezone: "UTC",
		SelectionPath: "album/sample.jpg", MaximumBytes: 1 << 20, MaximumSuccessAgeHours: 24,
		Enabled: true, CatchUpWindowMinutes: 60, UpdatedAt: task.CreatedAt,
	}
	if err := database.SaveRestoreVerificationPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	createFinishedVerification(t, database, store.RestoreVerificationRecord{
		ID: "verification-failed", TaskID: task.ID, RepositoryID: task.RepositoryID, SnapshotID: "snapshot-a",
		SelectionPath: policy.SelectionPath, Trigger: "scheduled", Status: "running", StartedAt: now.Add(-2 * time.Hour), CleanupStatus: "pending",
	}, store.RestoreVerificationFinish{
		Status: "cleanup_required", FinishedAt: now.Add(-2*time.Hour + time.Minute), CleanupStatus: "required", ErrorSummary: "temporary content requires cleanup",
	})
	createFinishedOccurrence(t, database, "restore-verification-failed", "restore_verification", task.ID, now.Add(-2*time.Hour), "failed")
	service := New(database, func() time.Time { return now })

	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	active, err := service.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"schedule", "result", "stale", "cleanup"} {
		key := "restore-verification:" + task.ID + ":" + suffix
		state := stateByKey(active, key)
		if state.StateKey == "" || state.TargetPage != "快照与恢复" {
			t.Fatalf("missing actionable restore verification alert %q in %+v", key, active)
		}
	}
	if stateByKey(active, "restore-verification:"+task.ID+":result").Reason == stateByKey(active, "repository:"+task.RepositoryID+":integrity").Reason {
		t.Fatalf("restore verification and repository integrity must remain distinct: %+v", active)
	}

	fresh := now.Add(time.Minute)
	createFinishedVerification(t, database, store.RestoreVerificationRecord{
		ID: "verification-success", TaskID: task.ID, RepositoryID: task.RepositoryID, SnapshotID: "snapshot-b",
		SelectionPath: policy.SelectionPath, Trigger: "manual", Status: "running", StartedAt: fresh, CleanupStatus: "pending",
	}, store.RestoreVerificationFinish{
		Status: "success", FinishedAt: fresh.Add(time.Minute), FileCount: 1, ByteCount: 7, ManifestSHA256: "sha256:abc", CleanupStatus: "removed",
	})
	createFinishedOccurrence(t, database, "restore-verification-success", "restore_verification", task.ID, fresh, "success")
	now = fresh.Add(2 * time.Minute)
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	active, err = service.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stateByKey(active, "restore-verification:"+task.ID+":cleanup").StateKey == "" {
		t.Fatalf("older cleanup residue was hidden by a newer success: %+v", active)
	}
	if err := database.ResolveRestoreVerificationCleanup(ctx, "verification-failed"); err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	active, err = service.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"schedule", "result", "stale", "cleanup"} {
		if stateByKey(active, "restore-verification:"+task.ID+":"+suffix).StateKey != "" {
			t.Fatalf("restore verification alert %q did not resolve: %+v", suffix, active)
		}
	}
}

func createHealthFixture(t *testing.T) (*store.Store, time.Time, domain.Task, domain.Repository, domain.Plan) {
	t.Helper()
	database, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	created := now.Add(-48 * time.Hour)
	if err := database.SaveSecret(ctx, "password", "repository-password", []byte("cipher"), created); err != nil {
		t.Fatal(err)
	}
	repository := domain.Repository{ID: "repo", Name: "异地仓库", Kind: domain.LocalRepository, Path: "/repo", Status: "ready", CreatedAt: created, UpdatedAt: created}
	if err := database.CreateRepository(ctx, repository, "password"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", Name: "照片", Kind: domain.DirectoryTask, RepositoryID: repository.ID, Directory: &domain.DirectorySource{Path: "/srv/photos"}, Health: domain.TaskHealthPolicy{MaxSuccessAgeHours: 24}, Enabled: true, CreatedAt: created, UpdatedAt: created}
	if err := database.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	plan := domain.Plan{ID: "plan", Name: "每日备份", Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:00"}, Timezone: "UTC", MaxParallel: 1, TaskIDs: []string{task.ID}, Enabled: true, CatchUpWindowMinutes: 60, CreatedAt: created, UpdatedAt: created}
	if err := database.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveMaintenancePolicy(ctx, domain.MaintenancePolicy{RepositoryID: repository.ID, Schedule: domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Sunday, TimeOfDay: "03:00"}, Timezone: "UTC", Enabled: true, CatchUpWindowMinutes: 60, UpdatedAt: created}); err != nil {
		t.Fatal(err)
	}
	certificateExpiry := now.Add(180 * 24 * time.Hour)
	if err := database.SaveAgent(ctx, store.AgentRecord{ID: "agent-a", CertificateSerial: "serial-a", CertificateNotAfter: &certificateExpiry, Status: "online", Capabilities: []string{"restic"}, BuildVersion: "v1.4.0", ProtocolMin: 1, ProtocolMax: 1, OS: "linux", Arch: "amd64", LastHeartbeatAt: timePointerForAlert(now.Add(-10 * time.Minute)), CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveRepositoryCapacity(ctx, repository.ID, domain.RepositoryCapacity{TotalBytes: 1000, AvailableBytes: 50, CheckedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRepositoryStatus(ctx, repository.ID, "abnormal"); err != nil {
		t.Fatal(err)
	}
	createFinishedOccurrence(t, database, "plan-missed", "plan", plan.ID, now.Add(-time.Hour), "missed")
	createFinishedOccurrence(t, database, "maintenance-failed", "maintenance", repository.ID, now.Add(-time.Hour), "failed")
	return database, now, task, repository, plan
}

func createFinishedOccurrence(t *testing.T, database *store.Store, id, ownerKind, ownerID string, at time.Time, status string) {
	t.Helper()
	mode := "on_time"
	if status == "missed" {
		mode = "missed"
	}
	finished := at.Add(time.Minute)
	created, err := database.CreateScheduleOccurrence(context.Background(), store.ScheduleOccurrence{ID: id, OwnerKind: ownerKind, OwnerID: ownerID, ScheduledAt: at, ObservedAt: at, Mode: mode, Status: status, TargetIDs: []string{ownerID}, FinishedAt: &finished})
	if err != nil || !created {
		t.Fatalf("create occurrence %s=%v err=%v", id, created, err)
	}
}

func createFinishedVerification(t *testing.T, database *store.Store, record store.RestoreVerificationRecord, finish store.RestoreVerificationFinish) {
	t.Helper()
	if err := database.CreateRestoreVerification(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishRestoreVerification(context.Background(), record.ID, finish); err != nil {
		t.Fatal(err)
	}
}

func timePointerForAlert(value time.Time) *time.Time { return &value }
