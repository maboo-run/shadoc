package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestActivityEndpointRequiresSessionAndReturnsFilterBoundPage(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	storage := srv.store.(*store.Store)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	repository := domain.Repository{ID: "activity-repo", Name: "历史仓库", Engine: domain.RsyncEngine, Kind: domain.LocalRepository, Path: "/archive", Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := storage.CreateRepository(ctx, repository, ""); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "activity-task", Name: "历史任务", Engine: domain.RsyncEngine, Kind: domain.RsyncTask, RepositoryID: repository.ID, Rsync: &domain.RsyncSource{Path: "/source"}, CreatedAt: now, UpdatedAt: now}
	if err := storage.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"run-old", "run-new"} {
		started := now.Add(time.Duration(index) * time.Minute)
		if err := storage.StartRun(ctx, store.RunRecord{ID: id, TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: started}); err != nil {
			t.Fatal(err)
		}
		if err := storage.FinishRun(ctx, id, "failed", started.Add(time.Second), 1, "", map[string]any{"error": "bounded failure"}, "SENSITIVE RAW LOG"); err != nil {
			t.Fatal(err)
		}
	}

	if response := requestJSON(t, srv, http.MethodGet, "/api/activity", nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", response.Code, response.Body.String())
	}
	path := "/api/activity?recordType=run&objectId=" + url.QueryEscape(task.ID) + "&engine=rsync&status=failed&trigger=manual&kind=backup&limit=1"
	response := requestJSON(t, srv, http.MethodGet, path, nil, cookie)
	var page store.ActivityPage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(page.Items) != 1 || page.Items[0].ID != "run-new" || page.NextCursor == "" || !page.Truncated || page.Filter.ObjectID != task.ID {
		t.Fatalf("page=%+v", page)
	}
	if page.Total != 0 || page.PageSize != 1 {
		t.Fatalf("pagination=%+v", page)
	}
	if strings.Contains(response.Body.String(), "SENSITIVE RAW LOG") || strings.Contains(response.Body.String(), "rawLog") {
		t.Fatalf("activity leaked raw log: %s", response.Body.String())
	}
	next := requestJSON(t, srv, http.MethodGet, path+"&cursor="+url.QueryEscape(page.NextCursor), nil, cookie)
	var nextPage store.ActivityPage
	if next.Code != http.StatusOK || json.Unmarshal(next.Body.Bytes(), &nextPage) != nil || len(nextPage.Items) != 1 || nextPage.Items[0].ID != "run-old" || nextPage.NextCursor != "" {
		t.Fatalf("next status=%d body=%s", next.Code, next.Body.String())
	}
	numbered := requestJSON(t, srv, http.MethodGet, path+"&page=2", nil, cookie)
	var numberedPage store.ActivityPage
	if numbered.Code != http.StatusOK || json.Unmarshal(numbered.Body.Bytes(), &numberedPage) != nil || numberedPage.Page != 2 || numberedPage.Total != 2 || len(numberedPage.Items) != 1 || numberedPage.Items[0].ID != "run-old" {
		t.Fatalf("numbered status=%d body=%s", numbered.Code, numbered.Body.String())
	}
	mismatch := requestJSON(t, srv, http.MethodGet, "/api/activity?recordType=run&status=success&limit=1&cursor="+url.QueryEscape(page.NextCursor), nil, cookie)
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
}

func TestActivityEndpointRejectsInvalidBounds(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	for _, path := range []string{
		"/api/activity?limit=201",
		"/api/activity?recordType=unknown",
		"/api/activity?from=not-a-time",
		"/api/activity?page=0",
		"/api/activity?page=two",
		"/api/activity?page=2&cursor=invalid",
		"/api/activity?from=2026-07-16T00%3A00%3A00Z&to=2026-07-15T00%3A00%3A00Z",
		"/api/activity?cursor=invalid",
	} {
		response := requestJSON(t, srv, http.MethodGet, path, nil, cookie)
		if response.Code != http.StatusBadRequest {
			t.Errorf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestActivityExportStreamsEveryMatchingSummaryWithoutLogsOrDetails(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	storage := srv.store.(*store.Store)
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC)
	repository := domain.Repository{ID: "export-repo", Name: "导出仓库", Engine: domain.RsyncEngine, Kind: domain.LocalRepository, Path: "/archive", Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := storage.CreateRepository(ctx, repository, ""); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "export-task", Name: `=HYPERLINK("https://invalid")`, Engine: domain.RsyncEngine, Kind: domain.RsyncTask, RepositoryID: repository.ID, Rsync: &domain.RsyncSource{Path: "/source"}, CreatedAt: now, UpdatedAt: now}
	if err := storage.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		id := "export-run-" + string(rune('a'+index))
		started := now.Add(time.Duration(index) * time.Minute)
		if err := storage.StartRun(ctx, store.RunRecord{ID: id, TaskID: task.ID, Trigger: "schedule", Status: "running", StartedAt: started}); err != nil {
			t.Fatal(err)
		}
		if err := storage.FinishRun(ctx, id, "failed", started.Add(time.Second), index+1, "", map[string]any{"error": "bounded failure"}, "NEVER-EXPORT-RAW-LOG"); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.CreateOperation(ctx, store.OperationRecord{ID: "export-operation", Kind: "directory_restore", Actor: "admin", TaskID: task.ID, Status: "failed", Stage: "failed", CreatedAt: now, Detail: map[string]any{"token": "NEVER-EXPORT-DETAIL"}}); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, srv, http.MethodGet, "/api/activity/export?recordType=run&objectId=export-task&status=failed&limit=1", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(response.Header().Get("Content-Disposition"), "shadoc-activity.csv") {
		t.Fatalf("headers=%v", response.Header())
	}
	rows, err := csv.NewReader(strings.NewReader(response.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows=%d csv=%s", len(rows), response.Body.String())
	}
	for _, row := range rows[1:] {
		if row[0] != "run" || row[8] != `'=HYPERLINK("https://invalid")` {
			t.Fatalf("unsafe or unexpected row=%v", row)
		}
	}
	if strings.Contains(response.Body.String(), "NEVER-EXPORT-RAW-LOG") || strings.Contains(response.Body.String(), "NEVER-EXPORT-DETAIL") || strings.Contains(response.Body.String(), "export-operation") {
		t.Fatalf("export leaked excluded content: %s", response.Body.String())
	}
	audits, err := storage.ListAudits(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		if audit.Action == "activity.export" && audit.Detail["rowCount"] == float64(3) {
			found = true
		}
	}
	if !found {
		t.Fatalf("activity export audit missing: %+v", audits)
	}

	cursorResponse := requestJSON(t, srv, http.MethodGet, "/api/activity/export?cursor=forbidden", nil, cookie)
	if cursorResponse.Code != http.StatusBadRequest {
		t.Fatalf("cursor status=%d body=%s", cursorResponse.Code, cursorResponse.Body.String())
	}
	pageResponse := requestJSON(t, srv, http.MethodGet, "/api/activity/export?page=2", nil, cookie)
	if pageResponse.Code != http.StatusBadRequest {
		t.Fatalf("page status=%d body=%s", pageResponse.Code, pageResponse.Body.String())
	}
}

func TestTaskTrendsEndpointExplainsWindowDenominatorAndFreshness(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	storage := srv.store.(*store.Store)
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Hour)
	repository := domain.Repository{ID: "trend-repo", Name: "趋势仓库", Engine: domain.RsyncEngine, Kind: domain.LocalRepository, Path: "/trend", Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := storage.CreateRepository(ctx, repository, ""); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "trend-task", Name: "趋势任务", Engine: domain.RsyncEngine, Kind: domain.RsyncTask, RepositoryID: repository.ID, Rsync: &domain.RsyncSource{Path: "/source"}, CreatedAt: now, UpdatedAt: now}
	if err := storage.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ id, status string }{{"trend-success", "success"}, {"trend-cancelled", "cancelled"}} {
		if err := storage.StartRun(ctx, store.RunRecord{ID: fixture.id, TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := storage.FinishRun(ctx, fixture.id, fixture.status, now.Add(time.Second), 1, "", map[string]any{"bytesChanged": int64(128)}, ""); err != nil {
			t.Fatal(err)
		}

	}
	if response := requestJSON(t, srv, http.MethodGet, "/api/task-trends", nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.Code)
	}
	response := requestJSON(t, srv, http.MethodGet, "/api/task-trends", nil, cookie)
	var report store.TaskTrendReport
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &report) != nil {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if report.GeneratedAt.IsZero() || len(report.Tasks) != 1 || len(report.Tasks[0].Windows) != 3 {
		t.Fatalf("report=%+v", report)
	}
	week := report.Tasks[0].Windows[0]
	if week.WindowDays != 7 || week.EligibleCount != 1 || week.ExcludedCount != 1 || week.SuccessRate == nil || *week.SuccessRate != 100 || week.MetricCoverage.BytesChanged != 1 {
		t.Fatalf("week=%+v", week)
	}
}
