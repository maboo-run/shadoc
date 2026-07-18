package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/dbrestore"
	"github.com/maboo-run/shadoc/internal/domain"
	repositoryservice "github.com/maboo-run/shadoc/internal/repository"
	"github.com/maboo-run/shadoc/internal/store"
)

type asyncRepositoryManager struct {
	repositoryManager
	initialize func(context.Context) error
	restore    func(context.Context, int) error
	contents   repositoryservice.SnapshotContentsPage
	query      repositoryservice.SnapshotContentsQuery
	diff       repositoryservice.SnapshotDiff
	diffQuery  repositoryservice.SnapshotDiffQuery
}

func (m *asyncRepositoryManager) CompareSnapshots(_ context.Context, _, _, _ string, query repositoryservice.SnapshotDiffQuery) (repositoryservice.SnapshotDiff, error) {
	m.diffQuery = query
	return m.diff, nil
}

func (m *asyncRepositoryManager) BrowseSnapshotContents(_ context.Context, _, _ string, query repositoryservice.SnapshotContentsQuery) (repositoryservice.SnapshotContentsPage, error) {
	m.query = query
	return m.contents, nil
}

func (m *asyncRepositoryManager) Initialize(ctx context.Context, _ string) error {
	return m.initialize(ctx)
}

func (m *asyncRepositoryManager) RestoreDirectory(ctx context.Context, _, _, _ string, _ []string, downloadKiBPerSecond int) error {
	return m.restore(ctx, downloadKiBPerSecond)
}

func (m *asyncRepositoryManager) PreflightDirectoryRestore(_ context.Context, _, _, target string, includes []string) (repositoryservice.DirectoryRestorePreflight, error) {
	return repositoryservice.DirectoryRestorePreflight{SourcePath: "/source", Target: target, Includes: includes}, nil
}

type restoreResidualError struct{ path string }

func (e restoreResidualError) Error() string               { return "injected partial restore" }
func (e restoreResidualError) RestoreResidualPath() string { return e.path }

func TestInitializeReturnsOperationBeforeWorkCompletes(t *testing.T) {
	srv := newResourceTestServer(t)
	started := make(chan struct{})
	release := make(chan struct{})
	srv.repositories = &asyncRepositoryManager{initialize: func(context.Context) error {
		close(started)
		<-release
		return nil
	}}
	cookie := setupSession(t, srv)

	responseChannel := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseChannel <- requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/initialize", map[string]any{}, cookie)
	}()
	var queued *httptest.ResponseRecorder
	select {
	case queued = <-responseChannel:
	case <-time.After(100 * time.Millisecond):
		close(release)
		synchronous := <-responseChannel
		t.Fatalf("handler waited for long operation: status=%d body=%s", synchronous.Code, synchronous.Body.String())
	}
	if queued.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", queued.Code, queued.Body.String())
	}
	var response struct {
		OperationID string `json:"operationId"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(queued.Body.Bytes(), &response); err != nil || response.OperationID == "" || response.Status != "queued" {
		t.Fatalf("queued response=%+v err=%v", response, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background operation did not start")
	}
	close(release)
	operation := waitForOperation(t, srv, cookie, response.OperationID, "success")
	if operation.Kind != "repository_initialize" || operation.RepositoryID != "repo" || operation.Actor != "admin" {
		t.Fatalf("operation=%+v", operation)
	}
}

func TestSnapshotContentsEndpointReturnsBrowsableNodes(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &asyncRepositoryManager{contents: repositoryservice.SnapshotContentsPage{Items: []repositoryservice.SnapshotNode{{Name: "one.jpg", Type: "file", Path: "/srv/photos/one.jpg", Size: 42}}, Path: "/srv/photos", Truncated: true, NextCursor: "opaque-next"}}
	srv.repositories = manager
	cookie := setupSession(t, srv)
	response := requestJSON(t, srv, http.MethodGet, "/api/repositories/repo/snapshots/abc/contents?path=%2Fsrv%2Fphotos&search=jpg&limit=25&cursor=opaque", nil, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"path":"/srv/photos/one.jpg"`) || !strings.Contains(response.Body.String(), `"truncated":true`) || !strings.Contains(response.Body.String(), `"nextCursor":"opaque-next"`) {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	if manager.query.Path != "/srv/photos" || manager.query.Search != "jpg" || manager.query.Limit != 25 || manager.query.Cursor != "opaque" {
		t.Fatalf("query=%+v", manager.query)
	}
}

func TestSnapshotDiffEndpointReturnsExplicitSummary(t *testing.T) {
	srv := newResourceTestServer(t)
	manager := &asyncRepositoryManager{diff: repositoryservice.SnapshotDiff{FromSnapshotID: "old", ToSnapshotID: "new", Added: 2, Modified: 1, Removed: 3, Items: []repositoryservice.SnapshotChange{{Path: "/srv/new.txt", Change: "added"}}, ExamplesTruncated: true}}
	srv.repositories = manager
	cookie := setupSession(t, srv)
	response := requestJSON(t, srv, http.MethodGet, "/api/repositories/repo/snapshot-diff?from=old&to=new&path=%2Fsrv&limit=25", nil, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"added":2`) || !strings.Contains(response.Body.String(), `"examplesTruncated":true`) {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	if manager.diffQuery.Path != "/srv" || manager.diffQuery.Limit != 25 {
		t.Fatalf("query=%+v", manager.diffQuery)
	}
}

type asyncResticInstaller struct{ installed string }

func (i *asyncResticInstaller) Versions(context.Context) ([]string, error) {
	return []string{"0.19.1"}, nil
}
func (i *asyncResticInstaller) Install(_ context.Context, version string) (string, error) {
	i.installed = version
	return "/managed/restic-" + version, nil
}

func TestResticInstallReturnsTrackedOperation(t *testing.T) {
	srv := newResourceTestServer(t)
	installer := &asyncResticInstaller{}
	srv.installer = installer
	selected := ""
	srv.selectRestic = func(path string) { selected = path }
	cookie := setupSession(t, srv)
	response := requestJSON(t, srv, http.MethodPost, "/api/restic/install", map[string]any{"version": "0.19.1"}, cookie)
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &accepted)
	if response.Code != http.StatusAccepted || accepted.OperationID == "" {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	operation := waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	if operation.Kind != "restic_install" || installer.installed != "0.19.1" || selected != "/managed/restic-0.19.1" || srv.paths.Restic != selected {
		t.Fatalf("operation=%+v installed=%q selected=%q path=%q", operation, installer.installed, selected, srv.paths.Restic)
	}
}

func TestOperationCancellationAndRestoreCleanupState(t *testing.T) {
	srv := newResourceTestServer(t)
	started := make(chan struct{})
	srv.repositories = &asyncRepositoryManager{
		initialize: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
		restore: func(context.Context, int) error { return restoreResidualError{path: "/tmp/restore-residual"} },
	}
	cookie := setupSession(t, srv)

	queued := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/initialize", map[string]any{}, cookie)
	var initResponse struct {
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(queued.Body.Bytes(), &initResponse)
	<-started
	cancelled := requestJSON(t, srv, http.MethodPost, "/api/operations/"+initResponse.OperationID+"/cancel", map[string]any{}, cookie)
	if cancelled.Code != http.StatusAccepted {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	audits, err := srv.store.(*store.Store).ListAudits(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	foundCancellation := false
	for _, audit := range audits {
		if audit.Action == "operation.cancel" && audit.Actor == "admin" && audit.TargetID == initResponse.OperationID {
			foundCancellation = true
		}
	}
	if !foundCancellation {
		t.Fatalf("missing operation.cancel audit: %+v", audits)
	}
	waitForOperation(t, srv, cookie, initResponse.OperationID, "cancelled")

	restoreRequest := map[string]any{
		"snapshotId": "snap", "target": "/restore/target", "includes": []string{},
	}
	preflight := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-directory/preflight", restoreRequest, cookie)
	var confirmation struct {
		ID string `json:"confirmationId"`
	}
	_ = json.Unmarshal(preflight.Body.Bytes(), &confirmation)
	authorized := requestJSON(t, srv, http.MethodPost, "/api/restores/"+confirmation.ID+"/authorize", map[string]any{"password": "correct horse battery staple"}, cookie)
	if preflight.Code != http.StatusOK || authorized.Code != http.StatusNoContent || confirmation.ID == "" {
		t.Fatalf("preflight=%d %s authorize=%d %s", preflight.Code, preflight.Body.String(), authorized.Code, authorized.Body.String())
	}
	restoreRequest["confirmationId"] = confirmation.ID
	restored := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-directory", restoreRequest, cookie)
	if restored.Code != http.StatusAccepted {
		t.Fatalf("restore status=%d body=%s", restored.Code, restored.Body.String())
	}
	var restoreResponse struct {
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(restored.Body.Bytes(), &restoreResponse)
	operation := waitForOperation(t, srv, cookie, restoreResponse.OperationID, "cleanup_required")
	if operation.SnapshotID != "snap" || operation.Detail["residualPath"] != "/tmp/restore-residual" {
		t.Fatalf("cleanup operation=%+v", operation)
	}
}

func TestRestoreConfirmationIsInvalidatedWhenTargetChanges(t *testing.T) {
	srv := newResourceTestServer(t)
	srv.repositories = &asyncRepositoryManager{initialize: func(context.Context) error { return nil }, restore: func(context.Context, int) error { return nil }}
	cookie := setupSession(t, srv)
	request := map[string]any{"snapshotId": "snap", "target": "/restore/original", "includes": []string{}}
	preflight := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-directory/preflight", request, cookie)
	var confirmation struct {
		ID string `json:"confirmationId"`
	}
	_ = json.Unmarshal(preflight.Body.Bytes(), &confirmation)
	if response := requestJSON(t, srv, http.MethodPost, "/api/restores/"+confirmation.ID+"/authorize", map[string]any{"password": "correct horse battery staple"}, cookie); response.Code != http.StatusNoContent {
		t.Fatalf("authorize=%d %s", response.Code, response.Body.String())
	}
	request["target"] = "/restore/changed"
	request["confirmationId"] = confirmation.ID
	response := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-directory", request, cookie)
	if response.Code != http.StatusConflict {
		t.Fatalf("changed target status=%d body=%s", response.Code, response.Body.String())
	}
	audits := requestJSON(t, srv, http.MethodGet, "/api/audits", nil, cookie)
	var page struct {
		Items []store.AuditRecord `json:"items"`
	}
	_ = json.Unmarshal(audits.Body.Bytes(), &page)
	records := page.Items
	foundPreflight, foundAuthorization := false, false
	for _, record := range records {
		if record.Actor != "admin" {
			continue
		}
		foundPreflight = foundPreflight || record.Action == "restore.preflight"
		foundAuthorization = foundAuthorization || record.Action == "restore.authorize"
	}
	if !foundPreflight || !foundAuthorization {
		t.Fatalf("semantic restore audits missing: %+v", records)
	}
}

func TestDirectoryRestoreConfirmationBindsServerDerivedDownloadLimit(t *testing.T) {
	srv := newResourceTestServer(t)
	resources := createReadyMaintenanceRepository(t, srv, "repo")
	now := time.Now().UTC()
	task := domain.Task{ID: "task", Name: "photos", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/srv"}, Resources: domain.ResourcePolicy{DownloadKiBPerSecond: 128}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := resources.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	usedLimit := make(chan int, 1)
	srv.repositories = &asyncRepositoryManager{restore: func(_ context.Context, limit int) error { usedLimit <- limit; return nil }}
	cookie := setupSession(t, srv)
	request := map[string]any{"snapshotId": "snap", "target": "/restore/target", "includes": []string{}}
	preflight := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-directory/preflight", request, cookie)
	var confirmation store.RestoreConfirmation
	if err := json.Unmarshal(preflight.Body.Bytes(), &confirmation); err != nil || confirmation.Summary["downloadKiBPerSecond"] != float64(128) {
		t.Fatalf("preflight=%d confirmation=%+v err=%v body=%s", preflight.Code, confirmation, err, preflight.Body.String())
	}
	task.Resources.DownloadKiBPerSecond = 256
	task.UpdatedAt = now.Add(time.Second)
	if err := resources.UpdateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if response := requestJSON(t, srv, http.MethodPost, "/api/restores/"+confirmation.ID+"/authorize", map[string]any{"password": "correct horse battery staple"}, cookie); response.Code != http.StatusNoContent {
		t.Fatalf("authorize=%d %s", response.Code, response.Body.String())
	}
	request["confirmationId"] = confirmation.ID
	stale := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-directory", request, cookie)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale confirmation=%d %s", stale.Code, stale.Body.String())
	}
	delete(request, "confirmationId")
	preflight = requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-directory/preflight", request, cookie)
	if err := json.Unmarshal(preflight.Body.Bytes(), &confirmation); err != nil || confirmation.Summary["downloadKiBPerSecond"] != float64(256) {
		t.Fatalf("second preflight=%d confirmation=%+v err=%v", preflight.Code, confirmation, err)
	}
	_ = requestJSON(t, srv, http.MethodPost, "/api/restores/"+confirmation.ID+"/authorize", map[string]any{"password": "correct horse battery staple"}, cookie)
	request["confirmationId"] = confirmation.ID
	started := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-directory", request, cookie)
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(started.Body.Bytes(), &accepted)
	operation := waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	if operation.Detail["downloadKiBPerSecond"] != float64(256) || <-usedLimit != 256 {
		t.Fatalf("operation=%+v", operation)
	}
}

func waitForOperation(t *testing.T, srv http.Handler, cookie *http.Cookie, id, status string) store.OperationRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response := requestJSON(t, srv, http.MethodGet, "/api/operations/"+id, nil, cookie)
		if response.Code == http.StatusOK {
			var operation store.OperationRecord
			if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
				t.Fatal(err)
			}
			if operation.Status == status {
				return operation
			}
			if operation.Status == "failed" && status != "failed" {
				t.Fatalf("operation failed unexpectedly: %+v", operation)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("operation %s did not reach %s", id, status)
	return store.OperationRecord{}
}

var _ error = restoreResidualError{}

type cleanupDatabaseRestore struct {
	request  dbrestore.Request
	restored chan dbrestore.Request
}

func (m *cleanupDatabaseRestore) Restore(_ context.Context, request dbrestore.Request) error {
	if m.restored != nil {
		m.restored <- request
	}
	return nil
}
func (m *cleanupDatabaseRestore) Preflight(_ context.Context, request dbrestore.Request) (dbrestore.PreflightResult, error) {
	m.request = request
	return dbrestore.PreflightResult{}, nil
}

func TestDirectoryCleanupRequiresPreflightReauthenticationAndAuditsRemoval(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	parent := t.TempDir()
	residual := filepath.Join(parent, ".target.restic-control-restore-owned")
	if err := os.Mkdir(residual, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(residual, "partial"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	operations := srv.store.(*store.Store)
	created := time.Now().UTC()
	record := store.OperationRecord{ID: "cleanup-op", Kind: "directory_restore", Actor: "admin", Status: "queued", Stage: "queued", CreatedAt: created, Detail: map[string]any{"residualPath": residual}}
	if err := operations.CreateOperation(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := operations.StartOperation(context.Background(), record.ID, "restoring", created); err != nil {
		t.Fatal(err)
	}
	if err := operations.FinishOperation(context.Background(), record.ID, "cleanup_required", "cleanup", created, "restore failed", nil); err != nil {
		t.Fatal(err)
	}

	preflight := requestJSON(t, srv, http.MethodPost, "/api/operations/cleanup-op/cleanup/preflight", map[string]any{}, cookie)
	if preflight.Code != http.StatusOK || !strings.Contains(preflight.Body.String(), `"safe":true`) {
		t.Fatalf("preflight=%d %s", preflight.Code, preflight.Body.String())
	}
	denied := requestJSON(t, srv, http.MethodPost, "/api/operations/cleanup-op/cleanup", map[string]any{"password": "wrong"}, cookie)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("denied=%d %s", denied.Code, denied.Body.String())
	}
	cleaned := requestJSON(t, srv, http.MethodPost, "/api/operations/cleanup-op/cleanup", map[string]any{"password": "correct horse battery staple"}, cookie)
	if cleaned.Code != http.StatusOK {
		t.Fatalf("cleaned=%d %s", cleaned.Code, cleaned.Body.String())
	}
	if _, err := os.Stat(residual); !os.IsNotExist(err) {
		t.Fatalf("residual still exists: %v", err)
	}
	resolved, err := operations.Operation(context.Background(), record.ID)
	if err != nil || resolved.Status != "failed" || resolved.Stage != "cleanup_resolved" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	audits, err := operations.ListAudits(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		found = found || audit.Action == "operation.cleanup" && audit.TargetID == record.ID && audit.Actor == "admin"
	}
	if !found {
		t.Fatalf("missing cleanup audit: %+v", audits)
	}
}

func TestDatabaseCleanupIsResolvedOnlyAfterTargetPassesPreflightAndReauthentication(t *testing.T) {
	srv := newResourceTestServer(t)
	databaseRestore := &cleanupDatabaseRestore{}
	srv.databaseRestore = databaseRestore
	cookie := setupSession(t, srv)
	operations := srv.store.(*store.Store)
	created := time.Now().UTC()
	record := store.OperationRecord{ID: "database-cleanup", Kind: "database_restore", Actor: "admin", RepositoryID: "repo", SnapshotID: "snapshot", Status: "queued", Stage: "queued", CreatedAt: created, Detail: map[string]any{"connectionId": "connection", "database": "restored_db"}}
	if err := operations.CreateOperation(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := operations.StartOperation(context.Background(), record.ID, "restoring", created); err != nil {
		t.Fatal(err)
	}
	if err := operations.FinishOperation(context.Background(), record.ID, "cleanup_required", "cleanup", created, "target requires cleanup", nil); err != nil {
		t.Fatal(err)
	}

	preflight := requestJSON(t, srv, http.MethodPost, "/api/operations/database-cleanup/cleanup/preflight", map[string]any{}, cookie)
	if preflight.Code != http.StatusOK || !strings.Contains(preflight.Body.String(), `"kind":"database_restore"`) {
		t.Fatalf("preflight=%d %s", preflight.Code, preflight.Body.String())
	}
	if databaseRestore.request.RepositoryID != "repo" || databaseRestore.request.SnapshotID != "snapshot" || databaseRestore.request.ConnectionID != "connection" || databaseRestore.request.Database != "restored_db" {
		t.Fatalf("preflight request=%+v", databaseRestore.request)
	}
	cleaned := requestJSON(t, srv, http.MethodPost, "/api/operations/database-cleanup/cleanup", map[string]any{"password": "correct horse battery staple"}, cookie)
	if cleaned.Code != http.StatusOK {
		t.Fatalf("cleaned=%d %s", cleaned.Code, cleaned.Body.String())
	}
	resolved, err := operations.Operation(context.Background(), record.ID)
	if err != nil || resolved.Stage != "cleanup_resolved" || resolved.Detail["cleanupResolution"] != "external_cleanup_verified" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
}

func TestSuccessfulRestoreDeletesOneTimeDatabaseConnection(t *testing.T) {
	srv := newResourceTestServer(t)
	srv.databaseRestore = &cleanupDatabaseRestore{}
	cookie := setupSession(t, srv)
	temporary := requestJSON(t, srv, http.MethodPost, "/api/database-connections/temporary", map[string]any{
		"name": "本次恢复", "engine": "mysql", "network": "tcp", "host": "db.internal", "port": 3306,
		"username": "restore", "password": "one-time-secret", "tls": map[string]any{"mode": "preferred"},
		"toolPaths": map[string]string{"restore": "/usr/bin/mysql", "admin": "/usr/bin/mysql", "create": "/usr/bin/mysql"},
	}, cookie)
	var temporaryConnection struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(temporary.Body.Bytes(), &temporaryConnection)
	request := map[string]any{"snapshotId": "snapshot", "connectionId": temporaryConnection.ID, "database": "restored_db"}
	preflight := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-database/preflight", request, cookie)
	var confirmation struct {
		ID string `json:"confirmationId"`
	}
	_ = json.Unmarshal(preflight.Body.Bytes(), &confirmation)
	if response := requestJSON(t, srv, http.MethodPost, "/api/restores/"+confirmation.ID+"/authorize", map[string]any{"password": "correct horse battery staple"}, cookie); response.Code != http.StatusNoContent {
		t.Fatalf("authorize=%d %s", response.Code, response.Body.String())
	}
	request["confirmationId"] = confirmation.ID
	started := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-database", request, cookie)
	var operation struct {
		ID string `json:"operationId"`
	}
	_ = json.Unmarshal(started.Body.Bytes(), &operation)
	if started.Code != http.StatusAccepted || operation.ID == "" {
		t.Fatalf("started=%d %s", started.Code, started.Body.String())
	}
	waitForOperation(t, srv, cookie, operation.ID, "success")
	if _, err := srv.store.(*store.Store).LoadDatabaseConnectionExecution(context.Background(), temporaryConnection.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("one-time connection remained after restore: %v", err)
	}
}

func TestDatabaseRestoreConfirmationBindsServerDerivedDownloadLimit(t *testing.T) {
	srv := newResourceTestServer(t)
	resources := createReadyMaintenanceRepository(t, srv, "repo")
	now := time.Now().UTC()
	task := domain.Task{ID: "task", Name: "database", Kind: domain.DirectoryTask, RepositoryID: "repo", Directory: &domain.DirectorySource{Path: "/srv"}, Resources: domain.ResourcePolicy{DownloadKiBPerSecond: 384}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := resources.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	restored := make(chan dbrestore.Request, 1)
	manager := &cleanupDatabaseRestore{restored: restored}
	srv.databaseRestore = manager
	cookie := setupSession(t, srv)
	request := map[string]any{"snapshotId": "snapshot", "connectionId": "connection", "database": "restored_db"}
	preflight := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-database/preflight", request, cookie)
	var confirmation store.RestoreConfirmation
	if err := json.Unmarshal(preflight.Body.Bytes(), &confirmation); err != nil || manager.request.DownloadKiBPerSecond != 384 || confirmation.Summary["downloadKiBPerSecond"] != float64(384) {
		t.Fatalf("preflight=%d request=%+v confirmation=%+v err=%v body=%s", preflight.Code, manager.request, confirmation, err, preflight.Body.String())
	}
	if response := requestJSON(t, srv, http.MethodPost, "/api/restores/"+confirmation.ID+"/authorize", map[string]any{"password": "correct horse battery staple"}, cookie); response.Code != http.StatusNoContent {
		t.Fatalf("authorize=%d %s", response.Code, response.Body.String())
	}
	request["confirmationId"] = confirmation.ID
	started := requestJSON(t, srv, http.MethodPost, "/api/repositories/repo/restore-database", request, cookie)
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	_ = json.Unmarshal(started.Body.Bytes(), &accepted)
	operation := waitForOperation(t, srv, cookie, accepted.OperationID, "success")
	if operation.Detail["downloadKiBPerSecond"] != float64(384) {
		t.Fatalf("operation=%+v", operation)
	}
	if executed := <-restored; executed.DownloadKiBPerSecond != 384 {
		t.Fatalf("restore request=%+v", executed)
	}
}
