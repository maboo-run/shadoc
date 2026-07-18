package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/store"
)

func TestRunDetailsLoadLogsOnDemandAndReportLifecycleExpiry(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	repository := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{"name": "local", "kind": "local", "path": t.TempDir(), "password": "repository-password-long", "passwordConfirmed": true}, cookie)
	if repository.Code != http.StatusCreated {
		t.Fatalf("repository status=%d body=%s", repository.Code, repository.Body.String())
	}
	var repo struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(repository.Body.Bytes(), &repo)
	resources := srv.store.(*store.Store)
	if err := resources.UpdateRepositoryStatus(context.Background(), repo.ID, "ready"); err != nil {
		t.Fatal(err)
	}
	taskResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks", map[string]any{
		"name": "task", "kind": "directory", "repositoryId": repo.ID, "directory": map[string]any{"path": t.TempDir(), "skipIfUnchanged": true},
		"retention": map[string]any{"keepWithinDays": 30}, "resources": map[string]any{"compression": "auto"}, "enabled": false,
	}, cookie)
	if taskResponse.Code != http.StatusCreated {
		t.Fatalf("task status=%d body=%s", taskResponse.Code, taskResponse.Body.String())
	}
	var task struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(taskResponse.Body.Bytes(), &task)
	started := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	if err := resources.StartRun(context.Background(), store.RunRecord{ID: "run-detail", TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	if err := resources.FinishRun(context.Background(), "run-detail", "failed", started.Add(time.Minute), 2, "", map[string]any{"error": "safe failure"}, "safe log line"); err != nil {
		t.Fatal(err)
	}

	detail := requestJSON(t, srv, http.MethodGet, "/api/runs/run-detail", nil, cookie)
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), "safe log line") || !strings.Contains(detail.Body.String(), `"logBytes":13`) {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	log := requestJSON(t, srv, http.MethodGet, "/api/runs/run-detail/log?download=1", nil, cookie)
	if log.Code != http.StatusOK || log.Body.String() != "safe log line" || !strings.Contains(log.Header().Get("Content-Disposition"), "run-detail.log") {
		t.Fatalf("log status=%d headers=%v body=%s", log.Code, log.Header(), log.Body.String())
	}
	if _, err := resources.CleanupExecutionData(context.Background(), store.LifecyclePolicy{RunDays: 365, RawLogDays: 0, AuditDays: 365, RawLogMaxBytes: 1 << 20}, started.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	expired := requestJSON(t, srv, http.MethodGet, "/api/runs/run-detail/log", nil, cookie)
	if expired.Code != http.StatusGone {
		t.Fatalf("expired status=%d body=%s", expired.Code, expired.Body.String())
	}
}

func TestAuditPageAndCSVUseTheSameActionAndTimeFilters(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := srv.store.(*store.Store)
	base := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	for index, action := range []string{"task.delete", "repository.delete", "task.delete"} {
		if err := resources.AppendAudit(context.Background(), store.AuditRecord{OccurredAt: base.Add(time.Duration(index) * time.Hour), Actor: "admin", Action: action, TargetType: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	query := "?action=task.delete&from=2026-07-12T10:30:00Z&to=2026-07-12T12:30:00Z&page=1&pageSize=1"
	page := requestJSON(t, srv, http.MethodGet, "/api/audits"+query, nil, cookie)
	if page.Code != http.StatusOK || strings.Count(page.Body.String(), `"action":"task.delete"`) != 1 || !strings.Contains(page.Body.String(), `"total":1`) || !strings.Contains(page.Body.String(), `"pageSize":1`) || strings.Contains(page.Body.String(), "repository.delete") {
		t.Fatalf("filtered page status=%d body=%s", page.Code, page.Body.String())
	}
	csv := requestJSON(t, srv, http.MethodGet, "/api/audits/export"+query, nil, cookie)
	if csv.Code != http.StatusOK || strings.Count(csv.Body.String(), "task.delete") != 1 || strings.Contains(csv.Body.String(), "repository.delete") {
		t.Fatalf("filtered csv status=%d body=%s", csv.Code, csv.Body.String())
	}
}
