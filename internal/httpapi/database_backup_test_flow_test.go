package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

type databaseBackupPreflighterStub struct {
	taskID string
	err    error
}

func decodeAcceptedOperation(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || accepted.OperationID == "" {
		t.Fatalf("accepted operation=%+v err=%v", accepted, err)
	}
	return accepted.OperationID
}

func (s *databaseBackupPreflighterStub) PreflightDatabaseBackup(_ context.Context, taskID string) error {
	s.taskID = taskID
	return s.err
}

func databaseBackupHTTPFixture(t *testing.T) (*Server, *store.Store, *http.Cookie, domain.Task, *databaseBackupPreflighterStub) {
	t.Helper()
	srv := newResourceTestServer(t)
	state := srv.store.(*store.Store)
	now := time.Now().UTC()
	for id, purpose := range map[string]string{
		"repo-password": "repository-password",
		"db-password":   "database-backup-password",
	} {
		if err := state.SaveSecret(t.Context(), id, purpose, []byte("cipher"), now); err != nil {
			t.Fatal(err)
		}
	}
	repositoryPath := filepath.Join(t.TempDir(), "repository")
	if err := state.CreateRepository(t.Context(), domain.Repository{
		ID: "repo", Name: "database", Kind: domain.LocalRepository, Path: repositoryPath, Status: "ready", CreatedAt: now, UpdatedAt: now,
	}, "repo-password"); err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(t.TempDir(), "mysqldump")
	admin := filepath.Join(t.TempDir(), "mysql")
	for _, program := range []string{dump, admin} {
		if err := os.WriteFile(program, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.CreateDatabaseConnection(t.Context(), domain.DatabaseConnection{
		ID: "connection", Name: "mysql", Engine: domain.MySQL, Purpose: domain.BackupConnection,
		Network: domain.TCPNetwork, Host: "127.0.0.1", Port: 3306, Username: "backup",
		ToolPaths: map[string]string{"dump": dump, "admin": admin}, Status: "ready",
		Preflight: domain.DatabasePreflight{CheckedAt: now, ClientVersion: "8.4", ServerVersion: "8.4"}, CreatedAt: now, UpdatedAt: now,
	}, "db-password"); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{
		ID: "task", Name: "database", Kind: domain.DatabaseTask, RepositoryID: "repo",
		Database:  &domain.DatabaseSource{ConnectionID: "connection", Database: "app"},
		Resources: domain.ResourcePolicy{Compression: "auto"}, Enabled: false, CreatedAt: now, UpdatedAt: now,
	}
	if err := state.CreateTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	tester := &databaseBackupPreflighterStub{}
	srv.databaseBackupPreflighter = tester
	return srv, state, setupSession(t, srv), task, tester
}

func TestDatabaseBackupPreflightIsAQueuedOperationForTheSavedDraft(t *testing.T) {
	srv, _, cookie, task, tester := databaseBackupHTTPFixture(t)
	response := requestJSON(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/database-backup-preflight", map[string]any{}, cookie)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	accepted := decodeAcceptedOperation(t, response)
	operation := waitForOperation(t, srv, cookie, accepted, "success")
	if operation.Kind != "database_backup_preflight" || operation.TaskID != task.ID || tester.taskID != task.ID {
		t.Fatalf("operation=%+v testerTask=%q", operation, tester.taskID)
	}
	if fingerprint, _ := operation.Detail["fingerprint"].(string); fingerprint == "" {
		t.Fatalf("operation did not persist a configuration fingerprint: %+v", operation.Detail)
	}
}

func TestDatabaseTaskCannotBeEnabledWithoutMatchingBackupPreflight(t *testing.T) {
	srv, state, cookie, task, _ := databaseBackupHTTPFixture(t)
	payload := map[string]any{
		"name": "database", "engine": "restic", "kind": "database", "repositoryId": task.RepositoryID,
		"executionTarget": map[string]any{"kind": "local"},
		"database":        map[string]any{"connectionId": task.Database.ConnectionID, "database": task.Database.Database},
		"retention":       map[string]any{}, "resources": map[string]any{"compression": "auto"},
		"health": map[string]any{}, "enabled": true,
	}
	withoutTest := requestJSON(t, srv, http.MethodPut, "/api/tasks/"+task.ID, payload, cookie)
	if withoutTest.Code != http.StatusUnprocessableEntity || !strings.Contains(withoutTest.Body.String(), "数据库备份预检") {
		t.Fatalf("without preflight status=%d body=%s", withoutTest.Code, withoutTest.Body.String())
	}

	testResponse := requestJSON(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/database-backup-preflight", map[string]any{}, cookie)
	accepted := decodeAcceptedOperation(t, testResponse)
	operation := waitForOperation(t, srv, cookie, accepted, "success")
	payload["databaseBackupPreflightOperationId"] = operation.ID
	withTest := requestJSON(t, srv, http.MethodPut, "/api/tasks/"+task.ID, payload, cookie)
	if withTest.Code != http.StatusOK {
		t.Fatalf("with test status=%d body=%s", withTest.Code, withTest.Body.String())
	}
	stored, err := state.ListTasks(t.Context())
	if err != nil || len(stored) != 1 || !stored[0].Enabled {
		t.Fatalf("stored tasks=%+v err=%v", stored, err)
	}
}
