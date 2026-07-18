package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestRetiredRestoreVerificationKeepsHistoricalReadAndCleanupFlow(t *testing.T) {
	srv := newResourceTestServer(t)
	resources := createReadyMaintenanceRepository(t, srv, "repo-verify")
	now := time.Now().UTC()
	task := domain.Task{ID: "task-verify", Name: "照片", Kind: domain.DirectoryTask, RepositoryID: "repo-verify", Directory: &domain.DirectorySource{Path: "/srv/photos"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := resources.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	manager := &restoreVerificationManagerFake{storage: resources}
	srv.restoreVerification = manager

	if response := requestJSON(t, srv, http.MethodGet, "/api/restore-verifications", nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status=%d body=%s", response.Code, response.Body.String())
	}
	cookie := setupSession(t, srv)
	if response := requestJSON(t, srv, http.MethodPut, "/api/tasks/task-verify/restore-verification-policy", restoreVerificationPolicyPayload(), cookie); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("retired policy mutation status=%d body=%s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, srv, http.MethodPost, "/api/tasks/task-verify/restore-verification/run", map[string]any{}, cookie); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("retired verification execution status=%d body=%s", response.Code, response.Body.String())
	}
	seedPolicy := domain.RestoreVerificationPolicy{
		TaskID: task.ID, Schedule: domain.Schedule{Kind: domain.IntervalSchedule, IntervalHours: 24}, Timezone: "UTC",
		SelectionPath: "album/sample.jpg", MaximumBytes: 1048576, MaximumSuccessAgeHours: 168,
		CatchUpWindowMinutes: 60, Enabled: false, UpdatedAt: now,
	}
	if err := resources.SaveRestoreVerificationPolicy(context.Background(), seedPolicy); err != nil {
		t.Fatal(err)
	}
	policy, err := resources.RestoreVerificationPolicy(context.Background(), task.ID)
	if err != nil || policy.TaskID != task.ID || policy.SelectionPath != "album/sample.jpg" || policy.Enabled || policy.CatchUpWindowMinutes != 60 {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}

	listed := requestJSON(t, srv, http.MethodGet, "/api/restore-verifications?taskId=task-verify", nil, cookie)
	if listed.Code != http.StatusOK || !containsAny(listed.Body.String(), `"taskId":"task-verify"`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	gotPolicy := requestJSON(t, srv, http.MethodGet, "/api/tasks/task-verify/restore-verification-policy", nil, cookie)
	if gotPolicy.Code != http.StatusOK {
		t.Fatalf("get policy status=%d body=%s", gotPolicy.Code, gotPolicy.Body.String())
	}

	if err := resources.StartRun(context.Background(), store.RunRecord{ID: "run-complete", TaskID: task.ID, Trigger: "manual", Status: "running", StartedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := resources.FinishRun(context.Background(), "run-complete", "success", now, 1, "snapshot-latest", map[string]any{}, ""); err != nil {
		t.Fatal(err)
	}
	dashboard := requestJSON(t, srv, http.MethodGet, "/api/dashboard", nil, cookie)
	var dashboardBody struct {
		Tasks []struct {
			LastCompleteBackup struct {
				SnapshotID string `json:"snapshotId"`
			} `json:"lastCompleteBackup"`
			LatestVerifiedRestore json.RawMessage `json:"latestVerifiedRestore"`
		} `json:"tasks"`
	}
	if dashboard.Code != http.StatusOK || json.Unmarshal(dashboard.Body.Bytes(), &dashboardBody) != nil || len(dashboardBody.Tasks) != 1 || dashboardBody.Tasks[0].LastCompleteBackup.SnapshotID != "snapshot-latest" || dashboardBody.Tasks[0].LatestVerifiedRestore != nil {
		t.Fatalf("dashboard should expose the latest complete backup without restore-verification evidence: status=%d body=%s", dashboard.Code, dashboard.Body.String())
	}

	cleanupRecord := store.RestoreVerificationRecord{ID: "verification-cleanup", TaskID: task.ID, RepositoryID: task.RepositoryID, SnapshotID: "snapshot-old", SelectionPath: policy.SelectionPath, Trigger: "scheduled", Status: "running", StartedAt: now.Add(-time.Hour), CleanupStatus: "pending"}
	if err := resources.CreateRestoreVerification(context.Background(), cleanupRecord); err != nil {
		t.Fatal(err)
	}
	if err := resources.FinishRestoreVerification(context.Background(), cleanupRecord.ID, store.RestoreVerificationFinish{Status: "cleanup_required", FinishedAt: now.Add(-time.Hour + time.Minute), CleanupStatus: "required", ErrorSummary: "cleanup required"}); err != nil {
		t.Fatal(err)
	}
	blockedDelete := requestJSON(t, srv, http.MethodDelete, "/api/tasks/task-verify/restore-verification-policy", map[string]any{}, cookie)
	if blockedDelete.Code != http.StatusConflict {
		t.Fatalf("policy deletion hid cleanup residue: status=%d body=%s", blockedDelete.Code, blockedDelete.Body.String())
	}
	cleanup := requestJSON(t, srv, http.MethodPost, "/api/restore-verifications/verification-cleanup/cleanup", map[string]any{}, cookie)
	accepted := struct {
		OperationID string `json:"operationId"`
	}{}
	if cleanup.Code != http.StatusAccepted || json.Unmarshal(cleanup.Body.Bytes(), &accepted) != nil || accepted.OperationID == "" {
		t.Fatalf("cleanup status=%d body=%s", cleanup.Code, cleanup.Body.String())
	}
	waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	cleaned, err := resources.RestoreVerification(context.Background(), cleanupRecord.ID)
	if err != nil || cleaned.CleanupStatus != "removed" {
		t.Fatalf("cleaned=%+v err=%v", cleaned, err)
	}
	deleted := requestJSON(t, srv, http.MethodDelete, "/api/tasks/task-verify/restore-verification-policy", map[string]any{}, cookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}

	audits, err := resources.ListAudits(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, audit := range audits {
		actions[audit.Action] = true
	}
	for _, action := range []string{"restore-verification.cleanup.start", "restore-verification.cleanup.complete", "restore-verification.policy.delete"} {
		if !actions[action] {
			t.Fatalf("missing semantic audit %q in %+v", action, actions)
		}
	}
}

func TestRetiredRestoreVerificationRejectsNewPolicyAndExecution(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	response := requestJSON(t, srv, http.MethodPut, "/api/tasks/task-disabled/restore-verification-policy", restoreVerificationPolicyPayload(), cookie)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("retired policy status=%d body=%s", response.Code, response.Body.String())
	}
	response = requestJSON(t, srv, http.MethodPost, "/api/tasks/task-disabled/restore-verification/run", map[string]any{}, cookie)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("retired execution status=%d body=%s", response.Code, response.Body.String())
	}
}

func restoreVerificationPolicyPayload() map[string]any {
	return map[string]any{
		"schedule": map[string]any{"kind": "interval", "intervalHours": 24}, "timezone": "UTC",
		"selectionPath": "album/sample.jpg", "maximumBytes": 1048576, "maximumSuccessAgeHours": 168, "enabled": true,
	}
}

type restoreVerificationManagerFake struct {
	storage *store.Store
}

func (m *restoreVerificationManagerFake) Run(context.Context, string, string) (store.RestoreVerificationRecord, error) {
	return store.RestoreVerificationRecord{}, errors.New("restore verification is retired")
}

func (m *restoreVerificationManagerFake) Cleanup(ctx context.Context, id string) (store.RestoreVerificationRecord, error) {
	if err := m.storage.ResolveRestoreVerificationCleanup(ctx, id); err != nil {
		return store.RestoreVerificationRecord{}, err
	}
	return m.storage.RestoreVerification(ctx, id)
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if candidate != "" && len(value) >= len(candidate) {
			for index := 0; index+len(candidate) <= len(value); index++ {
				if value[index:index+len(candidate)] == candidate {
					return true
				}
			}
		}
	}
	return false
}
