package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestRunListOmitsRawLogAndSupportsStatusLimit(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	resources := srv.store.(*store.Store)
	now := time.Now().UTC()
	createReadyMaintenanceRepository(t, srv, "repo-1")
	if err := resources.CreateTask(context.Background(), domain.Task{ID: "task", Name: "task", Kind: domain.DirectoryTask, RepositoryID: "repo-1", Directory: &domain.DirectorySource{Path: "/srv"}, Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for index, status := range []string{"success", "failed"} {
		id := []string{"one", "two"}[index]
		if err := resources.StartRun(context.Background(), store.RunRecord{ID: id, TaskID: "task", Trigger: "manual", Status: "running", StartedAt: now.Add(time.Duration(index) * time.Second)}); err != nil {
			t.Fatal(err)
		}
		if err := resources.FinishRun(context.Background(), id, status, now, 1, "", map[string]any{"error": "summary"}, "very-sensitive-large-log"); err != nil {
			t.Fatal(err)
		}
	}
	response := requestJSON(t, srv, http.MethodGet, "/api/runs?status=failed&limit=1", nil, cookie)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "very-sensitive-large-log") || !strings.Contains(response.Body.String(), `"id":"two"`) || strings.Contains(response.Body.String(), `"id":"one"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
