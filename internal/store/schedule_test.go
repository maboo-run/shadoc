package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
)

func TestOpenMigratesLegacySchedulePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-schedule.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE plans (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    schedule_json TEXT NOT NULL,
    timezone TEXT NOT NULL,
    max_parallel INTEGER NOT NULL DEFAULT 1,
    enabled INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE repository_maintenance (
    repository_id TEXT PRIMARY KEY,
    schedule_json TEXT NOT NULL,
    timezone TEXT NOT NULL,
    retention_json TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    updated_at TEXT NOT NULL
);
INSERT INTO plans VALUES ('legacy-plan','Legacy','{"kind":"daily","timeOfDay":"02:30"}','UTC',1,1,'2026-07-11T00:00:00Z','2026-07-11T00:00:00Z');
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var catchUp int
	var anchor string
	if err := s.db.QueryRow(`SELECT catch_up_window_minutes,schedule_anchor_at FROM plans WHERE id='legacy-plan'`).Scan(&catchUp, &anchor); err != nil {
		t.Fatal(err)
	}
	if catchUp != 60 || anchor == "" {
		t.Fatalf("catchUp=%d anchor=%q", catchUp, anchor)
	}
	for _, table := range []string{"plans", "repository_maintenance"} {
		for _, column := range []string{"catch_up_window_minutes", "schedule_anchor_at"} {
			if !tableHasColumn(t, s.db, table, column) {
				t.Fatalf("%s.%s was not migrated", table, column)
			}
		}
	}
}

func TestOpenMigratesScheduleOccurrenceOwnerKindsForRestoreVerification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-occurrences.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE schedule_occurrences (
    id TEXT PRIMARY KEY,
    owner_kind TEXT NOT NULL CHECK(owner_kind IN ('plan','maintenance')),
    owner_id TEXT NOT NULL,
    scheduled_at TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    mode TEXT NOT NULL CHECK(mode IN ('on_time','catch_up','missed')),
    status TEXT NOT NULL,
    target_ids_json TEXT NOT NULL DEFAULT '[]',
    run_ids_json TEXT NOT NULL DEFAULT '[]',
    started_at TEXT,
    finished_at TEXT,
    UNIQUE(owner_kind, owner_id, scheduled_at)
);
INSERT INTO schedule_occurrences VALUES ('legacy','plan','plan-a','2026-07-15T01:00:00Z','2026-07-15T01:00:00Z','on_time','success','[]','[]',NULL,'2026-07-15T01:01:00Z');
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC)
	created, err := s.CreateScheduleOccurrence(context.Background(), ScheduleOccurrence{ID: "verification", OwnerKind: "restore_verification", OwnerID: "task-a", ScheduledAt: now, ObservedAt: now, Mode: "on_time", Status: "pending", TargetIDs: []string{"task-a"}})
	if err != nil || !created {
		t.Fatalf("restore verification occurrence created=%v err=%v", created, err)
	}
	legacy, err := s.LatestScheduleOccurrence(context.Background(), "plan", "plan-a", time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	if err != nil || legacy.ID != "legacy" {
		t.Fatalf("legacy=%+v err=%v", legacy, err)
	}
}

func TestScheduleOccurrenceIsUniqueClaimedOnceAndSummarized(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	scheduled := time.Date(2026, 7, 15, 2, 30, 0, 0, time.UTC)
	occurrence := ScheduleOccurrence{
		ID:          "occurrence-a",
		OwnerKind:   "plan",
		OwnerID:     "plan-a",
		ScheduledAt: scheduled,
		ObservedAt:  scheduled.Add(30 * time.Minute),
		Mode:        "catch_up",
		Status:      "pending",
		TargetIDs:   []string{"task-a", "task-b"},
	}
	created, err := s.CreateScheduleOccurrence(ctx, occurrence)
	if err != nil || !created {
		t.Fatalf("first create=%v err=%v", created, err)
	}
	created, err = s.CreateScheduleOccurrence(ctx, occurrence)
	if err != nil || created {
		t.Fatalf("duplicate create=%v err=%v", created, err)
	}

	start := make(chan struct{})
	results := make(chan bool, 2)
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, claimErr := s.ClaimScheduleOccurrence(ctx, occurrence.ID, scheduled.Add(31*time.Minute))
			results <- claimed
			errors <- claimErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)
	claimedCount := 0
	for claimed := range results {
		if claimed {
			claimedCount++
		}
	}
	for claimErr := range errors {
		if claimErr != nil {
			t.Fatal(claimErr)
		}
	}
	if claimedCount != 1 {
		t.Fatalf("claimed count=%d, want 1", claimedCount)
	}

	finished := scheduled.Add(40 * time.Minute)
	if err := s.FinishScheduleOccurrence(ctx, occurrence.ID, "success", []string{"run-a", "run-b"}, finished); err != nil {
		t.Fatal(err)
	}
	missedAt := scheduled.Add(24 * time.Hour)
	missed := ScheduleOccurrence{ID: "occurrence-b", OwnerKind: "plan", OwnerID: "plan-a", ScheduledAt: missedAt, ObservedAt: missedAt.Add(2 * time.Hour), Mode: "missed", Status: "missed", TargetIDs: []string{"task-a", "task-b"}, FinishedAt: timePointer(missedAt.Add(2 * time.Hour))}
	if created, err := s.CreateScheduleOccurrence(ctx, missed); err != nil || !created {
		t.Fatalf("create missed=%v err=%v", created, err)
	}
	other := ScheduleOccurrence{ID: "occurrence-other", OwnerKind: "plan", OwnerID: "plan-other", ScheduledAt: missedAt.Add(time.Hour), ObservedAt: missedAt.Add(time.Hour), Mode: "on_time", Status: "success", TargetIDs: []string{"task-other"}, FinishedAt: timePointer(missedAt.Add(time.Hour))}
	if created, err := s.CreateScheduleOccurrence(ctx, other); err != nil || !created {
		t.Fatalf("create other=%v err=%v", created, err)
	}

	latest, err := s.LatestScheduleOccurrence(ctx, "plan", "plan-a", scheduled.Add(-time.Minute))
	if err != nil || latest.ID != missed.ID || latest.Status != "missed" || len(latest.TargetIDs) != 2 {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	latestByOwner, err := s.LatestScheduleOccurrences(ctx, "plan")
	if err != nil || latestByOwner["plan-a"].ID != missed.ID {
		t.Fatalf("latest by owner=%+v err=%v", latestByOwner, err)
	}
	recent, err := s.ListScheduleOccurrences(ctx, "plan", "plan-a", 2)
	if err != nil || len(recent) != 2 || recent[0].ID != missed.ID || recent[1].ID != occurrence.ID {
		t.Fatalf("recent=%+v err=%v", recent, err)
	}
	stats, err := s.ScheduleOccurrenceStats(ctx, "plan", "plan-a", scheduled.Add(-time.Minute))
	if err != nil || stats.Total != 2 || stats.Success != 1 || stats.Missed != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestRecoverInterruptedScheduleOccurrences(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	items := []ScheduleOccurrence{
		{ID: "pending", OwnerKind: "plan", OwnerID: "pending-plan", ScheduledAt: now, ObservedAt: now, Mode: "on_time", Status: "pending", TargetIDs: []string{"task"}},
		{ID: "running", OwnerKind: "plan", OwnerID: "running-plan", ScheduledAt: now, ObservedAt: now, Mode: "on_time", Status: "pending", TargetIDs: []string{"task"}},
		{ID: "success", OwnerKind: "plan", OwnerID: "successful-plan", ScheduledAt: now, ObservedAt: now, Mode: "on_time", Status: "success", TargetIDs: []string{"task"}, FinishedAt: timePointer(now)},
	}
	for _, item := range items {
		if created, err := s.CreateScheduleOccurrence(ctx, item); err != nil || !created {
			t.Fatalf("create %s=%v err=%v", item.ID, created, err)
		}
	}
	if claimed, err := s.ClaimScheduleOccurrence(ctx, "running", now); err != nil || !claimed {
		t.Fatalf("claim running=%v err=%v", claimed, err)
	}

	recovered, err := s.RecoverInterruptedScheduleOccurrences(ctx, now.Add(time.Minute))
	if err != nil || recovered != 2 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	for id, want := range map[string]string{"pending": "interrupted", "running": "interrupted", "success": "success"} {
		var got string
		if err := s.db.QueryRowContext(ctx, `SELECT status FROM schedule_occurrences WHERE id=?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s status=%q want=%q", id, got, want)
		}
	}
}

func TestPlanAndMaintenanceScheduleAnchorsResetOnlyForCadenceChanges(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	taskID, repositoryID := createScheduleFixture(t, s, now)
	plan := domain.Plan{ID: "plan", Name: "nightly", Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:30"}, Timezone: "UTC", MaxParallel: 1, TaskIDs: []string{taskID}, Enabled: true, CatchUpWindowMinutes: 45, CreatedAt: now, UpdatedAt: now}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	loaded := loadOnlyPlan(t, s)
	if !loaded.ScheduleAnchorAt.Equal(now) || loaded.CatchUpWindowMinutes != 45 {
		t.Fatalf("created plan=%+v", loaded)
	}

	loaded.Name = "renamed"
	loaded.UpdatedAt = now.Add(time.Hour)
	if err := s.UpdatePlan(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	if got := loadOnlyPlan(t, s).ScheduleAnchorAt; !got.Equal(now) {
		t.Fatalf("name change moved anchor to %s", got)
	}

	loaded = loadOnlyPlan(t, s)
	loaded.Schedule.TimeOfDay = "03:30"
	loaded.UpdatedAt = now.Add(2 * time.Hour)
	if err := s.UpdatePlan(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	if got := loadOnlyPlan(t, s).ScheduleAnchorAt; !got.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("schedule change anchor=%s", got)
	}

	loaded = loadOnlyPlan(t, s)
	loaded.Enabled = false
	loaded.UpdatedAt = now.Add(3 * time.Hour)
	if err := s.UpdatePlan(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	disabledAnchor := loadOnlyPlan(t, s).ScheduleAnchorAt
	loaded = loadOnlyPlan(t, s)
	loaded.Enabled = true
	loaded.UpdatedAt = now.Add(4 * time.Hour)
	if err := s.UpdatePlan(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	if got := loadOnlyPlan(t, s).ScheduleAnchorAt; got.Equal(disabledAnchor) || !got.Equal(now.Add(4*time.Hour)) {
		t.Fatalf("re-enabled anchor=%s disabled anchor=%s", got, disabledAnchor)
	}

	policy := domain.MaintenancePolicy{RepositoryID: repositoryID, Schedule: domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Monday, TimeOfDay: "03:00"}, Timezone: "UTC", Retention: domain.RetentionPolicy{KeepLast: 3}, Enabled: true, CatchUpWindowMinutes: 30, UpdatedAt: now}
	if err := s.SaveMaintenancePolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	maintenance, err := s.MaintenancePolicy(ctx, repositoryID)
	if err != nil || !maintenance.ScheduleAnchorAt.Equal(now) || maintenance.CatchUpWindowMinutes != 30 {
		t.Fatalf("maintenance=%+v err=%v", maintenance, err)
	}
	maintenance.Retention.KeepLast = 4
	maintenance.UpdatedAt = now.Add(time.Hour)
	if err := s.SaveMaintenancePolicy(ctx, maintenance); err != nil {
		t.Fatal(err)
	}
	maintenance, _ = s.MaintenancePolicy(ctx, repositoryID)
	if !maintenance.ScheduleAnchorAt.Equal(now) {
		t.Fatalf("retention change moved maintenance anchor=%s", maintenance.ScheduleAnchorAt)
	}
	maintenance.Schedule.TimeOfDay = "04:00"
	maintenance.UpdatedAt = now.Add(2 * time.Hour)
	if err := s.SaveMaintenancePolicy(ctx, maintenance); err != nil {
		t.Fatal(err)
	}
	maintenance, _ = s.MaintenancePolicy(ctx, repositoryID)
	if !maintenance.ScheduleAnchorAt.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("maintenance schedule anchor=%s", maintenance.ScheduleAnchorAt)
	}
	maintenance.Enabled = false
	maintenance.UpdatedAt = now.Add(3 * time.Hour)
	if err := s.SaveMaintenancePolicy(ctx, maintenance); err != nil {
		t.Fatal(err)
	}
	disabledMaintenance, _ := s.MaintenancePolicy(ctx, repositoryID)
	maintenanceAnchor := disabledMaintenance.ScheduleAnchorAt
	disabledMaintenance.Enabled = true
	disabledMaintenance.UpdatedAt = now.Add(4 * time.Hour)
	if err := s.SaveMaintenancePolicy(ctx, disabledMaintenance); err != nil {
		t.Fatal(err)
	}
	maintenance, _ = s.MaintenancePolicy(ctx, repositoryID)
	if maintenance.ScheduleAnchorAt.Equal(maintenanceAnchor) || !maintenance.ScheduleAnchorAt.Equal(now.Add(4*time.Hour)) {
		t.Fatalf("re-enabled maintenance anchor=%s disabled anchor=%s", maintenance.ScheduleAnchorAt, maintenanceAnchor)
	}
}

func createScheduleFixture(t *testing.T, s *Store, now time.Time) (string, string) {
	t.Helper()
	ctx := context.Background()
	if err := s.SaveSecret(ctx, "password", "repository-password", []byte("ciphertext"), now); err != nil {
		t.Fatal(err)
	}
	repositoryID := "schedule-repository"
	if err := s.CreateRepository(ctx, domain.Repository{ID: repositoryID, Name: "schedule repository", Kind: domain.LocalRepository, Path: "/backup/schedule", Status: "ready", CreatedAt: now, UpdatedAt: now}, "password"); err != nil {
		t.Fatal(err)
	}
	taskID := "schedule-task"
	if err := s.CreateTask(ctx, domain.Task{ID: taskID, Name: "schedule task", Kind: domain.DirectoryTask, RepositoryID: repositoryID, Directory: &domain.DirectorySource{Path: "/source"}, Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return taskID, repositoryID
}

func loadOnlyPlan(t *testing.T, s *Store) domain.Plan {
	t.Helper()
	plans, err := s.ListPlans(context.Background())
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%+v err=%v", plans, err)
	}
	return plans[0]
}

func tableHasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	return false
}

func timePointer(value time.Time) *time.Time { return &value }
