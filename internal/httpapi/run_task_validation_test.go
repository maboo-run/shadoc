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

type blockingTaskRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingTaskRunner) Run(ctx context.Context, taskID, _, _ string) (store.RunRecord, error) {
	close(r.started)
	select {
	case <-r.release:
		return store.RunRecord{ID: "run-blocking", TaskID: taskID, Status: "success"}, nil
	case <-ctx.Done():
		return store.RunRecord{}, ctx.Err()
	}
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

func TestTaskListReportsTheActiveManualRunUntilWorkFinishes(t *testing.T) {
	srv := newResourceTestServer(t)
	runner := &blockingTaskRunner{started: make(chan struct{}), release: make(chan struct{})}
	srv.runner = runner
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-active-run")
	now := time.Now().UTC()
	task := domain.Task{ID: "task-active-run", Name: "active task", Kind: domain.DirectoryTask, RepositoryID: "repo-active-run", Directory: &domain.DirectorySource{Path: "/srv/source"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := resources.CreateTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}

	started := requestJSON(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/run", map[string]any{}, cookie)
	if started.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", started.Code, started.Body.String())
	}
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &accepted); err != nil || accepted.OperationID == "" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	select {
	case <-runner.started:
	case <-time.After(10 * time.Second):
		close(runner.release)
		t.Fatal("manual task run did not start")
	}

	listed := requestJSON(t, srv, http.MethodGet, "/api/tasks", nil, cookie)
	var tasks []struct {
		ID              string                 `json:"id"`
		ActiveOperation *store.OperationRecord `json:"activeOperation"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &tasks); err != nil {
		close(runner.release)
		t.Fatal(err)
	}
	if listed.Code != http.StatusOK || len(tasks) != 1 || tasks[0].ID != task.ID || tasks[0].ActiveOperation == nil || tasks[0].ActiveOperation.ID != accepted.OperationID || tasks[0].ActiveOperation.Status != "running" {
		close(runner.release)
		t.Fatalf("tasks=%+v status=%d body=%s", tasks, listed.Code, listed.Body.String())
	}

	close(runner.release)
	waitForOperation(t, srv, cookie, accepted.OperationID, "success")
}

func TestTaskListOwnsRunHealthAndNextSchedule(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-task-health")
	anchor := time.Now().UTC().Truncate(time.Second)
	task := domain.Task{ID: "task-health-view", Name: "health view", Kind: domain.DirectoryTask, RepositoryID: "repo-task-health", Directory: &domain.DirectorySource{Path: "/srv/source"}, Enabled: true, CreatedAt: anchor, UpdatedAt: anchor}
	if err := resources.CreateTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	plan := domain.Plan{ID: "plan-health-view", Name: "hourly", Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 2}, Timezone: "UTC", TaskIDs: []string{task.ID}, Enabled: true, ScheduleAnchorAt: anchor, CreatedAt: anchor, UpdatedAt: anchor}
	if err := resources.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	runStarted := anchor.Add(time.Minute)
	if err := resources.StartRun(t.Context(), store.RunRecord{ID: "run-health-view", TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: runStarted}); err != nil {
		t.Fatal(err)
	}
	if err := resources.FinishRun(t.Context(), "run-health-view", "failed", runStarted.Add(time.Minute), 1, "", map[string]any{"error": "safe"}, "safe"); err != nil {
		t.Fatal(err)
	}

	taskResponse := requestJSON(t, srv, http.MethodGet, "/api/tasks", nil, cookie)
	var tasks []struct {
		ID      string `json:"id"`
		LastRun *struct {
			Status    string    `json:"status"`
			StartedAt time.Time `json:"startedAt"`
		} `json:"lastRun"`
		NextRun string `json:"nextRun"`
	}
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &tasks); err != nil {
		t.Fatal(err)
	}
	if taskResponse.Code != http.StatusOK || len(tasks) != 1 || tasks[0].ID != task.ID || tasks[0].LastRun == nil || tasks[0].LastRun.Status != "failed" || !tasks[0].LastRun.StartedAt.Equal(runStarted) || tasks[0].NextRun != anchor.Add(2*time.Hour).Format(time.RFC3339) {
		t.Fatalf("tasks=%+v status=%d body=%s", tasks, taskResponse.Code, taskResponse.Body.String())
	}

	repositoryResponse := requestJSON(t, srv, http.MethodGet, "/api/repositories", nil, cookie)
	var repositories []map[string]any
	if err := json.Unmarshal(repositoryResponse.Body.Bytes(), &repositories); err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 {
		t.Fatalf("repositories=%v", repositories)
	}
	if _, exists := repositories[0]["lastRun"]; exists {
		t.Fatalf("repository still exposes task run health: %v", repositories[0])
	}
	if _, exists := repositories[0]["nextRun"]; exists {
		t.Fatalf("repository still exposes task schedule: %v", repositories[0])
	}
}

func TestActiveTaskOperationIncludesRenewedAgentProgressAndStallState(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := createReadyMaintenanceRepository(t, srv, "repo-progress")
	now := time.Now().UTC().Truncate(time.Second)
	task := domain.Task{ID: "task-progress", Name: "progress task", Kind: domain.DirectoryTask, RepositoryID: "repo-progress", Directory: &domain.DirectorySource{Path: "/srv/source"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := resources.CreateTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if err := resources.SaveAgent(t.Context(), store.AgentRecord{ID: "agent-progress", CertificateSerial: "serial", Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	operation := store.OperationRecord{ID: "operation-progress", Kind: "sync", Actor: "admin", TaskID: task.ID, Status: "queued", Stage: "queued", CreatedAt: now}
	if err := resources.CreateOperation(t.Context(), operation); err != nil {
		t.Fatal(err)
	}
	if err := resources.StartOperation(t.Context(), operation.ID, "syncing", now); err != nil {
		t.Fatal(err)
	}
	if err := resources.CreateAgentLease(t.Context(), store.AgentLease{ID: "lease-progress", AgentID: "agent-progress", TaskID: task.ID, Engine: "rsync", Definition: json.RawMessage(`{}`), ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := resources.ClaimAgentLease(t.Context(), "agent-progress", now); err != nil {
		t.Fatal(err)
	}
	progress := json.RawMessage(`{"version":1,"assignmentId":"lease-progress","agentId":"agent-progress","sequence":2,"phase":"transferring","bytesTransferred":4096,"totalBytes":8192,"filesTransferred":4,"filesTotal":8,"rateBytesPerSecond":1024,"etaSeconds":4}`)
	if err := resources.UpdateAgentLeaseProgress(t.Context(), "lease-progress", "agent-progress", progress, now.Add(10*time.Second), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, srv, http.MethodGet, "/api/operations/"+operation.ID, nil, cookie)
	var got store.OperationRecord
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	progressDetail, _ := got.Detail["progress"].(map[string]any)
	if response.Code != http.StatusOK || progressDetail["phase"] != "transferring" || progressDetail["bytesTransferred"] != float64(4096) || progressDetail["totalBytes"] != float64(8192) || got.Detail["progressState"] != "active" {
		t.Fatalf("operation=%+v status=%d body=%s", got, response.Code, response.Body.String())
	}

	stalledAt := time.Now().UTC().Add(-45 * time.Second)
	if err := resources.UpdateAgentLeaseProgress(t.Context(), "lease-progress", "agent-progress", progress, stalledAt, time.Now().UTC().Add(75*time.Second)); err != nil {
		t.Fatal(err)
	}
	response = requestJSON(t, srv, http.MethodGet, "/api/operations/"+operation.ID, nil, cookie)
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Detail["progressState"] != "stalled" {
		t.Fatalf("stale Agent progress was still presented as active: %+v", got.Detail)
	}
}
