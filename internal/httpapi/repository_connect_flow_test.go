package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	repositoryservice "github.com/maboo-run/shadoc/internal/repository"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/store"
)

type existingRepositoryManager struct {
	repositoryManager
	candidate   domain.Repository
	password    string
	credentials *s3backend.Credentials
	verifyID    string
	started     chan struct{}
	release     chan struct{}
	err         error
}

func (m *existingRepositoryManager) ConnectExisting(ctx context.Context, candidate domain.Repository, password string, credentials *s3backend.Credentials) ([]repositoryservice.Snapshot, error) {
	m.candidate, m.password, m.credentials = candidate, password, credentials
	if m.started != nil {
		close(m.started)
	}
	if m.release != nil {
		select {
		case <-m.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []repositoryservice.Snapshot{{ID: "snapshot-a"}}, m.err
}

func (m *existingRepositoryManager) VerifyExisting(_ context.Context, id string) ([]repositoryservice.Snapshot, error) {
	m.verifyID = id
	return []repositoryservice.Snapshot{{ID: "snapshot-a"}, {ID: "snapshot-b"}}, m.err
}

func TestConnectExistingRepositoryQueuesVerificationBeforePersistence(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &existingRepositoryManager{started: make(chan struct{}), release: make(chan struct{})}
	srv.repositories = manager
	cookie := setupSession(t, srv)

	responseChannel := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseChannel <- requestJSON(t, srv, http.MethodPost, "/api/repositories/connect", map[string]any{
			"name": "已有仓库", "engine": "restic", "kind": "local", "path": "/backup/existing",
			"password": "existing-password", "passwordConfirmed": true,
		}, cookie)
	}()
	var response *httptest.ResponseRecorder
	select {
	case response = <-responseChannel:
	case <-time.After(100 * time.Millisecond):
		close(manager.release)
		t.Fatal("connect handler waited for repository verification")
	}
	var accepted struct {
		RepositoryID string `json:"repositoryId"`
		OperationID  string `json:"operationId"`
		Status       string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || response.Code != http.StatusAccepted || accepted.RepositoryID == "" || accepted.OperationID == "" || accepted.Status != "queued" {
		t.Fatalf("response=%d %s err=%v", response.Code, response.Body.String(), err)
	}
	select {
	case <-manager.started:
	case <-time.After(time.Second):
		t.Fatal("repository verification did not start")
	}
	close(manager.release)
	operation := waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	if operation.Kind != "repository_connect" || operation.RepositoryID != accepted.RepositoryID || operation.Detail["snapshotCount"] != float64(1) {
		t.Fatalf("operation=%+v", operation)
	}
	if manager.candidate.ID != accepted.RepositoryID || manager.candidate.Status != "" || manager.password != "existing-password" {
		t.Fatalf("candidate=%+v password=%q", manager.candidate, manager.password)
	}
	encoded, _ := json.Marshal(operation)
	if strings.Contains(string(encoded), "existing-password") || strings.Contains(string(encoded), "/backup/existing") {
		t.Fatalf("operation persisted sensitive connection input: %s", encoded)
	}
	audits, err := srv.store.(*store.Store).ListAudits(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		found = found || audit.Action == "repository.connect" && audit.Actor == "admin" && audit.TargetID == accepted.RepositoryID
	}
	if !found {
		t.Fatalf("missing repository.connect audit: %+v", audits)
	}
}

func TestConnectExistingS3RepositoryPassesCredentialsOnlyToVerifier(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &existingRepositoryManager{}
	srv.repositories = manager
	cookie := setupSession(t, srv)
	response := requestJSON(t, srv, http.MethodPost, "/api/repositories/connect", map[string]any{
		"name": "已有对象仓库", "engine": "restic", "kind": "s3", "password": "existing-password", "passwordConfirmed": true,
		"s3": map[string]any{"endpoint": "https://objects.example.com", "bucket": "backup-prod", "region": "us-east-1", "prefix": "existing", "pathStyle": true, "accessKey": "connect-access-private", "secretKey": "connect-secret-private", "credentialsConfirmed": true},
	}, cookie)
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || response.Code != http.StatusAccepted || accepted.OperationID == "" {
		t.Fatalf("response=%d %s err=%v", response.Code, response.Body.String(), err)
	}
	operation := waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	if manager.candidate.S3 == nil || manager.candidate.Path != "existing" || manager.credentials == nil || manager.credentials.AccessKey != "connect-access-private" || manager.credentials.SecretKey != "connect-secret-private" {
		t.Fatalf("candidate=%+v credentials=%+v", manager.candidate, manager.credentials)
	}
	encoded, _ := json.Marshal(operation)
	if strings.Contains(string(encoded), "connect-access-private") || strings.Contains(string(encoded), "connect-secret-private") {
		t.Fatalf("operation persisted S3 credentials: %s", encoded)
	}
}

func TestConnectExistingRepositoryRejectsUnsafeOrUnconfirmedInputBeforeQueueing(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &existingRepositoryManager{}
	srv.repositories = manager
	cookie := setupSession(t, srv)
	for _, payload := range []map[string]any{
		{"name": "未确认", "engine": "restic", "kind": "local", "path": "/backup", "password": "password", "passwordConfirmed": false},
		{"name": "错误引擎", "engine": "rsync", "kind": "local", "path": "/backup", "password": "password", "passwordConfirmed": true},
		{"name": "相对路径", "engine": "restic", "kind": "local", "path": "relative", "password": "password", "passwordConfirmed": true},
	} {
		response := requestJSON(t, srv, http.MethodPost, "/api/repositories/connect", payload, cookie)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("payload=%+v status=%d body=%s", payload, response.Code, response.Body.String())
		}
	}
	if manager.candidate.ID != "" {
		t.Fatalf("invalid candidate reached service: %+v", manager.candidate)
	}
}

func TestVerifyImportedExistingRepositoryIsPersistentOperation(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &existingRepositoryManager{}
	srv.repositories = manager
	cookie := setupSession(t, srv)
	response := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo-imported/verify-existing", map[string]any{}, cookie)
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || response.Code != http.StatusAccepted || accepted.OperationID == "" {
		t.Fatalf("response=%d %s err=%v", response.Code, response.Body.String(), err)
	}
	operation := waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	if manager.verifyID != "repo-imported" || operation.Kind != "repository_verify_existing" || operation.RepositoryID != "repo-imported" || operation.Detail["snapshotCount"] != float64(2) {
		t.Fatalf("verifyID=%q operation=%+v", manager.verifyID, operation)
	}
}
