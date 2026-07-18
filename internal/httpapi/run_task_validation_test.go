package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

type countingTaskRunner struct{ calls int }

func (r *countingTaskRunner) Run(context.Context, string, string, string) (store.RunRecord, error) {
	r.calls++
	return store.RunRecord{}, nil
}

func TestDashboardReportsAbnormalRepositoryInsteadOfHealthySummary(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := srv.store.(*store.Store)
	now := time.Now().UTC()
	if err := resources.SaveSecret(context.Background(), "password", "repository-password", []byte("cipher"), now); err != nil {
		t.Fatal(err)
	}
	if err := resources.CreateRepository(context.Background(), domain.Repository{ID: "repo-bad", Name: "damaged", Kind: domain.LocalRepository, Path: "/bad", Status: "abnormal", CreatedAt: now, UpdatedAt: now}, "password"); err != nil {
		t.Fatal(err)
	}
	if err := srv.alerts.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, srv, http.MethodGet, "/api/dashboard", nil, cookie)
	var dashboard struct {
		RepositoryStatus string           `json:"repositoryStatus"`
		Alerts           []map[string]any `json:"alerts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.RepositoryStatus != "abnormal" || len(dashboard.Alerts) != 1 {
		t.Fatalf("dashboard=%+v body=%s", dashboard, response.Body.String())
	}
}

func TestDashboardReportsPersistedScheduleHealthAndCoverage(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-schedule")
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	anchor := base.Add(-150 * time.Minute)
	task := domain.Task{ID: "task-schedule", Name: "scheduled task", Kind: domain.DirectoryTask, RepositoryID: "repo-schedule", Directory: &domain.DirectorySource{Path: "/srv/scheduled"}, Health: domain.TaskHealthPolicy{MaxSuccessAgeHours: 1}, Enabled: true, CreatedAt: anchor, UpdatedAt: anchor}
	if err := resources.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	plan := domain.Plan{ID: "plan-schedule", Name: "hourly", Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 1}, Timezone: "UTC", MaxParallel: 1, TaskIDs: []string{task.ID}, Enabled: true, CatchUpWindowMinutes: 60, ScheduleAnchorAt: anchor, CreatedAt: anchor, UpdatedAt: anchor}
	if err := resources.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	firstScheduled, lastScheduled := anchor.Add(time.Hour), anchor.Add(2*time.Hour)
	firstFinished, lastFinished := firstScheduled.Add(time.Minute), lastScheduled.Add(time.Minute)
	for _, occurrence := range []store.ScheduleOccurrence{
		{ID: "occurrence-failed", OwnerKind: "plan", OwnerID: plan.ID, ScheduledAt: firstScheduled, ObservedAt: firstScheduled, Mode: "on_time", Status: "failed", TargetIDs: []string{task.ID}, FinishedAt: &firstFinished},
		{ID: "occurrence-missed", OwnerKind: "plan", OwnerID: plan.ID, ScheduledAt: lastScheduled, ObservedAt: lastFinished, Mode: "missed", Status: "missed", TargetIDs: []string{task.ID}, FinishedAt: &lastFinished},
	} {
		if created, err := resources.CreateScheduleOccurrence(ctx, occurrence); err != nil || !created {
			t.Fatalf("create occurrence=%v err=%v", created, err)
		}
	}
	runStarted := base.Add(-80 * time.Minute)
	if err := resources.StartRun(ctx, store.RunRecord{ID: "scheduled-run", TaskID: task.ID, PlanID: plan.ID, Trigger: "schedule", Status: "running", StartedAt: runStarted}); err != nil {
		t.Fatal(err)
	}
	if err := resources.FinishRun(ctx, "scheduled-run", "failed", runStarted.Add(time.Minute), 1, "", map[string]any{"error": "safe failure"}, "safe log"); err != nil {
		t.Fatal(err)
	}

	if err := srv.alerts.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, srv, http.MethodGet, "/api/dashboard", nil, cookie)
	var dashboard struct {
		Tasks []struct {
			ID              string `json:"id"`
			LastScheduledAt string `json:"lastScheduledAt"`
			LastRun         string `json:"lastRun"`
			NextRun         string `json:"nextRun"`
		} `json:"tasks"`
		ScheduleCoverage []struct {
			PlanID          string `json:"planId"`
			Total           int    `json:"total"`
			Success         int    `json:"success"`
			Missed          int    `json:"missed"`
			Failed          int    `json:"failed"`
			CoveragePercent int    `json:"coveragePercent"`
		} `json:"scheduleCoverage"`
		Alerts []struct {
			Reason string `json:"reason"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(dashboard.Tasks) != 1 || dashboard.Tasks[0].LastScheduledAt != lastScheduled.Format(time.RFC3339) || dashboard.Tasks[0].LastRun != runStarted.Format(time.RFC3339) || dashboard.Tasks[0].NextRun != anchor.Add(3*time.Hour).Format(time.RFC3339) {
		t.Fatalf("dashboard=%+v body=%s", dashboard, response.Body.String())
	}
	if len(dashboard.ScheduleCoverage) != 1 || dashboard.ScheduleCoverage[0].PlanID != plan.ID || dashboard.ScheduleCoverage[0].Total != 2 || dashboard.ScheduleCoverage[0].Success != 0 || dashboard.ScheduleCoverage[0].Missed != 1 || dashboard.ScheduleCoverage[0].Failed != 1 || dashboard.ScheduleCoverage[0].CoveragePercent != 0 {
		t.Fatalf("coverage=%+v", dashboard.ScheduleCoverage)
	}
	reasons := map[string]bool{}
	for _, alert := range dashboard.Alerts {
		reasons[alert.Reason] = true
	}
	if !reasons["计划错过"] || !reasons["长期无完整成功"] {
		t.Fatalf("alerts=%+v", dashboard.Alerts)
	}
}

func TestManualRunRejectsMissingOrDisabledTaskBeforeQueuing(t *testing.T) {
	srv := newResourceTestServer(t)
	runner := &countingTaskRunner{}
	srv.runner = runner
	cookie := setupSession(t, srv)

	missing := requestJSON(t, srv, http.MethodPost, "/api/tasks/missing/run", map[string]any{}, cookie)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
	if runner.calls != 0 {
		t.Fatalf("missing task reached runner: %d", runner.calls)
	}
	resources := createReadyMaintenanceRepository(t, srv, "repo-1")
	now := time.Now().UTC()
	if err := resources.CreateTask(context.Background(), domain.Task{ID: "disabled", Name: "disabled", Kind: domain.DirectoryTask, RepositoryID: "repo-1", Directory: &domain.DirectorySource{Path: "/srv"}, Enabled: false, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	disabled := requestJSON(t, srv, http.MethodPost, "/api/tasks/disabled/run", map[string]any{}, cookie)
	if disabled.Code != http.StatusConflict || runner.calls != 0 {
		t.Fatalf("disabled status=%d calls=%d body=%s", disabled.Code, runner.calls, disabled.Body.String())
	}
}
