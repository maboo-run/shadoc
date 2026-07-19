package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/auth"
	"github.com/maboo-run/shadoc/internal/compat"
	databaseverify "github.com/maboo-run/shadoc/internal/database"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
)

func newResourceTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "restic-control.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC) }
	v, err := vault.New(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	srv := NewWithDependencies(s, auth.New(s, now), secret.New(s, v, now))
	srv.databaseVerifier = successfulDatabaseVerifier{now: now}
	return srv
}

type successfulDatabaseVerifier struct{ now func() time.Time }

func (v successfulDatabaseVerifier) Verify(context.Context, domain.DatabaseConnection, string) databaseverify.Verification {
	return databaseverify.Verification{CheckedAt: v.now(), ClientVersion: "8.0.36", ServerVersion: "8.0.36"}
}

type failedDatabaseVerifier struct{}

func (failedDatabaseVerifier) Verify(context.Context, domain.DatabaseConnection, string) databaseverify.Verification {
	return databaseverify.Verification{CheckedAt: time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC), ClientVersion: "8.0.36", Error: "数据库认证失败"}
}

type connectionTesterStub struct {
	result     databaseverify.Verification
	password   string
	connection domain.DatabaseConnection
}

func (t *connectionTesterStub) Test(_ context.Context, connection domain.DatabaseConnection, password string) databaseverify.Verification {
	t.connection = connection
	t.password = password
	return t.result
}

type databaseEnumeratorStub struct {
	connection domain.DatabaseConnection
	password   string
	items      []string
	err        error
}

func (e *databaseEnumeratorStub) List(_ context.Context, connection domain.DatabaseConnection, password string) ([]string, error) {
	e.connection = connection
	e.password = password
	return e.items, e.err
}

func TestEnumeratesLogicalDatabasesThroughStoredVerifiedConnection(t *testing.T) {
	srv := newResourceTestServer(t)
	enumerator := &databaseEnumeratorStub{items: []string{"accounts", "orders"}}
	srv.databaseEnumerator = enumerator
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/database-connections", map[string]any{
		"name": "production", "engine": "mysql", "purpose": "backup", "network": "tcp", "host": "db.internal", "port": 3306,
		"username": "backup", "password": "database-secret-password", "tls": map[string]any{"mode": "preferred"},
		"toolPaths": map[string]string{"dump": "/usr/bin/mysqldump", "admin": "/usr/bin/mysql"},
	}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var connection domain.DatabaseConnection
	if err := json.Unmarshal(created.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, srv, http.MethodPost, "/api/database-connections/"+connection.ID+"/databases", map[string]any{}, cookie)
	if response.Code != http.StatusOK || response.Body.String() != "{\"items\":[\"accounts\",\"orders\"]}\n" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if enumerator.connection.ID != connection.ID || enumerator.password != "database-secret-password" {
		t.Fatalf("enumerator connection=%q password=%q", enumerator.connection.ID, enumerator.password)
	}
	if strings.Contains(response.Body.String(), "database-secret-password") {
		t.Fatalf("response leaked password: %s", response.Body.String())
	}
}

func TestDatabaseEnumerationRequiresMutationSessionAndReturnsBoundedDiagnostic(t *testing.T) {
	srv := newResourceTestServer(t)
	if response := requestJSON(t, srv, http.MethodPost, "/api/database-connections/missing/databases", map[string]any{}, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", response.Code)
	}
	cookie := setupSession(t, srv)
	srv.databaseEnumerator = &databaseEnumeratorStub{err: errors.New("password=must-not-leak and an internal command failed")}
	response := requestJSON(t, srv, http.MethodPost, "/api/database-connections/missing/databases", map[string]any{}, cookie)
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "must-not-leak") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFailedDatabasePreflightPersistsOnlyADraftAndDoesNotLeakPassword(t *testing.T) {
	srv := newResourceTestServer(t)
	srv.databaseVerifier = failedDatabaseVerifier{}
	cookie := setupSession(t, srv)
	rec := requestJSON(t, srv, http.MethodPost, "/api/database-connections", map[string]any{
		"name": "source", "engine": "mysql", "purpose": "backup", "network": "tcp", "host": "db.internal", "port": 3306,
		"username": "backup", "password": "database-secret-password", "tls": map[string]any{"mode": "preferred"},
		"toolPaths": map[string]string{"dump": "/usr/bin/mysqldump", "admin": "/usr/bin/mysql"},
	}, cookie)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"status":"draft"`) || !strings.Contains(rec.Body.String(), "数据库认证失败") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "database-secret-password") {
		t.Fatalf("preflight response leaked password: %s", rec.Body.String())
	}
	var saved domain.DatabaseConnection
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	err := validateTaskActivation(t.Context(), srv.store.(*store.Store), domain.Task{Kind: domain.DatabaseTask, Enabled: true, Database: &domain.DatabaseSource{ConnectionID: saved.ID, Database: "app"}})
	if err == nil || !strings.Contains(err.Error(), "尚未通过有效预检") {
		t.Fatalf("draft connection activation error=%v", err)
	}
}

func TestDatabaseConnectionTestUsesNativeTesterWithoutPersistingConnection(t *testing.T) {
	srv := newResourceTestServer(t)
	tester := &connectionTesterStub{result: databaseverify.Verification{
		CheckedAt:     time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC),
		ServerVersion: "8.0.36",
	}}
	srv.databaseTester = tester
	cookie := setupSession(t, srv)
	rec := requestJSON(t, srv, http.MethodPost, "/api/database-connections/test", map[string]any{
		"name": "source", "engine": "mysql", "purpose": "backup", "network": "tcp", "host": "db.internal", "port": 3306,
		"username": "backup", "password": "database-test-secret", "tls": map[string]any{"mode": "preferred"},
	}, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), `"serverVersion":"8.0.36"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if tester.password != "database-test-secret" || strings.Contains(rec.Body.String(), "database-test-secret") {
		t.Fatalf("tester password=%q response=%s", tester.password, rec.Body.String())
	}
	list := requestJSON(t, srv, http.MethodGet, "/api/database-connections", nil, cookie)
	if list.Code != http.StatusOK || list.Body.String() != "[]\n" {
		t.Fatalf("saved connections status=%d body=%s", list.Code, list.Body.String())
	}
}

func TestDatabaseConnectionTestReturnsFailureWithoutSavingDraft(t *testing.T) {
	srv := newResourceTestServer(t)
	srv.databaseTester = &connectionTesterStub{result: databaseverify.Verification{
		CheckedAt: time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC),
		Error:     "数据库账号缺少当前用途所需权限",
	}}
	cookie := setupSession(t, srv)
	rec := requestJSON(t, srv, http.MethodPost, "/api/database-connections/test", map[string]any{
		"name": "source", "engine": "mysql", "purpose": "backup", "network": "tcp", "host": "db.internal", "port": 3306,
		"username": "backup", "password": "database-test-secret", "tls": map[string]any{"mode": "preferred"},
	}, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "数据库账号缺少当前用途所需权限") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	list := requestJSON(t, srv, http.MethodGet, "/api/database-connections", nil, cookie)
	if list.Code != http.StatusOK || list.Body.String() != "[]\n" {
		t.Fatalf("saved connections status=%d body=%s", list.Code, list.Body.String())
	}
}

func TestDatabaseConnectionTestDoesNotReuseOtherEngineClientPaths(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/database-connections", map[string]any{
		"name": "mysql", "engine": "mysql", "purpose": "backup", "network": "tcp", "host": "db.internal", "port": 3306,
		"username": "backup", "password": "mysql-secret", "tls": map[string]any{"mode": "preferred"},
		"toolPaths": map[string]string{"dump": "/custom/mysqldump", "admin": "/custom/mysql"},
	}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var connection domain.DatabaseConnection
	if err := json.Unmarshal(created.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	tester := &connectionTesterStub{result: databaseverify.Verification{ServerVersion: "16.3"}}
	srv.databaseTester = tester
	rec := requestJSON(t, srv, http.MethodPost, "/api/database-connections/test", map[string]any{
		"id": connection.ID, "name": "postgres", "engine": "postgresql", "purpose": "backup", "network": "tcp", "host": "pg.internal", "port": 5432,
		"username": "backup", "password": "postgres-secret", "tls": map[string]any{"mode": "preferred"},
	}, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if tester.connection.ToolPaths["admin"] == "/custom/mysql" || tester.connection.ToolPaths["dump"] == "/custom/mysqldump" {
		t.Fatalf("reused incompatible tool paths: %v", tester.connection.ToolPaths)
	}
}

func TestDatabaseConnectionUpdateClearsExplicitToolPathForRediscovery(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/database-connections", map[string]any{
		"name": "mysql", "engine": "mysql", "purpose": "backup", "network": "tcp", "host": "db.internal", "port": 3306,
		"username": "backup", "password": "mysql-secret", "tls": map[string]any{"mode": "preferred"},
		"toolPaths": map[string]string{"dump": "/custom/mysqldump", "admin": "/custom/mysql"},
	}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var connection domain.DatabaseConnection
	if err := json.Unmarshal(created.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	connectionChange := requestJSON(t, srv, http.MethodPut, "/api/database-connections/"+connection.ID, map[string]any{
		"name": "mysql", "engine": "mysql", "purpose": "backup", "network": "tcp", "host": "db.internal", "port": 3306,
		"username": "backup", "password": "mysql-secret", "tls": map[string]any{"mode": "preferred"},
		"toolPaths": map[string]string{"dump": "", "admin": ""},
	}, cookie)
	if connectionChange.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", connectionChange.Code, connectionChange.Body.String())
	}
	var updated domain.DatabaseConnection
	if err := json.Unmarshal(connectionChange.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ToolPaths["dump"] == "/custom/mysqldump" {
		t.Fatalf("explicit dump path was not cleared: %v", updated.ToolPaths)
	}
	if updated.ToolPaths["admin"] == "/custom/mysql" {
		t.Fatalf("explicit admin path was not cleared: %v", updated.ToolPaths)
	}
}

func TestCreatesDraftAgentLocalToLocalRsyncTaskWithoutRemoteHost(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if err := srv.store.(*store.Store).SaveAgent(t.Context(), store.AgentRecord{
		ID: "agent-local", CertificateSerial: "serial", Capabilities: []string{"rsync"}, Status: "online", LastHeartbeatAt: &now, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	repositoryResponse := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
		"name": "second disk", "engine": "rsync", "kind": "local", "path": "/mnt/disk-b/photos",
	}, cookie)
	var repository domain.Repository
	if repositoryResponse.Code != http.StatusCreated || json.Unmarshal(repositoryResponse.Body.Bytes(), &repository) != nil {
		t.Fatalf("repository status=%d body=%s", repositoryResponse.Code, repositoryResponse.Body.String())
	}

	created := requestJSON(t, srv, http.MethodPost, "/api/tasks", map[string]any{
		"name": "disk mirror", "engine": "rsync", "kind": "rsync", "repositoryId": repository.ID, "enabled": false,
		"executionTarget": map[string]any{"kind": "agent", "agentId": "agent-local"},
		"rsync":           map[string]any{"path": "/mnt/disk-a/photos", "delete": true},
		"retention":       map[string]any{}, "resources": map[string]any{},
	}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", created.Code, created.Body.String())
	}

	var task domain.Task
	if err := json.Unmarshal(created.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if task.Rsync == nil || task.RepositoryID != repository.ID || task.Rsync.DestinationHostID != "" {
		t.Fatalf("task=%+v", task)
	}
	aggregate, err := srv.store.(*store.Store).LoadRsyncExecution(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Repository.ID != repository.ID || aggregate.Host.ID != "" || aggregate.PrivateKeySecretID != "" {
		t.Fatalf("local task unexpectedly loaded SSH data: %+v", aggregate)
	}
}

func TestCreatesReadyRsyncRepositoryWithoutResticPassword(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
		"name": "disk mirror", "engine": "rsync", "kind": "local", "path": "/mnt/disk-b/photos",
	}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", created.Code, created.Body.String())
	}
	var repository domain.Repository
	if err := json.Unmarshal(created.Body.Bytes(), &repository); err != nil {
		t.Fatal(err)
	}
	if repository.EffectiveEngine() != domain.RsyncEngine || repository.Status != "ready" {
		t.Fatalf("repository=%+v", repository)
	}
	items, err := srv.store.(*store.Store).ListRepositories(t.Context())
	if err != nil || len(items) != 1 || items[0].EffectiveEngine() != domain.RsyncEngine {
		t.Fatalf("repositories=%+v err=%v", items, err)
	}
}

func TestRepositoryBackedRsyncActivationUsesRepositoryRemoteHost(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	createdHost := requestJSON(t, srv, http.MethodPost, "/api/remote-hosts", map[string]any{
		"name": "sync target", "host": "backup.example", "port": 22, "username": "backup",
		"privateKey": "private-key", "hostFingerprint": "backup.example ssh-ed25519 AAAA-pinned",
	}, cookie)
	if createdHost.Code != http.StatusCreated {
		t.Fatalf("host status=%d body=%s", createdHost.Code, createdHost.Body.String())
	}
	var host domain.RemoteHost
	if err := json.Unmarshal(createdHost.Body.Bytes(), &host); err != nil {
		t.Fatal(err)
	}
	createdRepository := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
		"name": "sync repository", "engine": "rsync", "kind": "sftp", "remoteHostId": host.ID, "path": "/srv/sync",
	}, cookie)
	if createdRepository.Code != http.StatusCreated {
		t.Fatalf("repository status=%d body=%s", createdRepository.Code, createdRepository.Body.String())
	}
	var repository domain.Repository
	if err := json.Unmarshal(createdRepository.Body.Bytes(), &repository); err != nil {
		t.Fatal(err)
	}
	repositories, err := srv.store.(*store.Store).ListRepositories(t.Context())
	if err != nil || len(repositories) != 1 || repositories[0].RemoteHostID != host.ID {
		t.Fatalf("persisted repositories=%+v err=%v", repositories, err)
	}
	hosts, err := srv.store.(*store.Store).ListRemoteHosts(t.Context())
	if err != nil || len(hosts) != 1 || hosts[0].HostFingerprint != "backup.example ssh-ed25519 AAAA-pinned" {
		t.Fatalf("persisted hosts=%+v err=%v", hosts, err)
	}

	err = validateTaskActivation(t.Context(), srv.store.(*store.Store), domain.Task{
		Engine: domain.RsyncEngine, Kind: domain.RsyncTask, RepositoryID: repository.ID, Enabled: true,
		Rsync: &domain.RsyncSource{Path: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("repository-backed rsync activation rejected pinned repository host: %v", err)
	}
}

func TestRepositoryBackedRsyncActivationRequiresPinnedRepositoryHost(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	createdHost := requestJSON(t, srv, http.MethodPost, "/api/remote-hosts", map[string]any{
		"name": "unpinned sync target", "host": "backup.example", "port": 22, "username": "backup", "privateKey": "private-key",
	}, cookie)
	if createdHost.Code != http.StatusCreated {
		t.Fatalf("host status=%d body=%s", createdHost.Code, createdHost.Body.String())
	}
	var host domain.RemoteHost
	if err := json.Unmarshal(createdHost.Body.Bytes(), &host); err != nil {
		t.Fatal(err)
	}
	createdRepository := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
		"name": "unpinned sync repository", "engine": "rsync", "kind": "sftp", "remoteHostId": host.ID, "path": "/srv/sync",
	}, cookie)
	if createdRepository.Code != http.StatusCreated {
		t.Fatalf("repository status=%d body=%s", createdRepository.Code, createdRepository.Body.String())
	}
	var repository domain.Repository
	if err := json.Unmarshal(createdRepository.Body.Bytes(), &repository); err != nil {
		t.Fatal(err)
	}

	err := validateTaskActivation(t.Context(), srv.store.(*store.Store), domain.Task{
		Engine: domain.RsyncEngine, Kind: domain.RsyncTask, RepositoryID: repository.ID, Enabled: true,
		Rsync: &domain.RsyncSource{Path: t.TempDir()},
	})
	if err == nil || !strings.Contains(err.Error(), "未固定 SSH 主机密钥") {
		t.Fatalf("unpinned repository host activation error=%v", err)
	}
}

func TestTemporaryRestoreConnectionIsVerifiedButHiddenFromSavedConnections(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/database-connections/temporary", map[string]any{
		"name": "本次恢复", "engine": "mysql", "network": "tcp", "host": "db.internal", "port": 3306,
		"username": "restore", "password": "one-time-secret", "tls": map[string]any{"mode": "preferred"},
		"toolPaths": map[string]string{"restore": "/usr/bin/mysql", "admin": "/usr/bin/mysql", "create": "/usr/bin/mysql"},
	}, cookie)
	if created.Code != http.StatusCreated || strings.Contains(created.Body.String(), "one-time-secret") || !strings.Contains(created.Body.String(), `"temporary":true`) {
		t.Fatalf("created=%d %s", created.Code, created.Body.String())
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || !strings.HasPrefix(result.ID, "temporary-dbconn_") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := srv.store.(*store.Store).LoadDatabaseConnectionExecution(context.Background(), result.ID); err != nil {
		t.Fatalf("temporary execution record missing: %v", err)
	}
	listed := requestJSON(t, srv, http.MethodGet, "/api/database-connections", nil, cookie)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), result.ID) || strings.Contains(listed.Body.String(), "本次恢复") {
		t.Fatalf("saved list=%d %s", listed.Code, listed.Body.String())
	}
}

func setupSession(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()
	rec := requestJSON(t, srv, http.MethodPost, "/api/setup", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d body=%s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookie(t, rec)
	cookie.Raw = rec.Header().Get("X-CSRF-Token")
	return cookie
}

func TestRemoteHostCreateAndListNeverReturnsPrivateKey(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)

	created := requestJSON(t, srv, http.MethodPost, "/api/remote-hosts", map[string]any{
		"name":       "绿联 NAS",
		"host":       "192.168.1.20",
		"port":       22,
		"username":   "backup",
		"privateKey": "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----",
	}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "PRIVATE KEY") || strings.Contains(created.Body.String(), "privateKey") {
		t.Fatalf("create response leaked private key: %s", created.Body.String())
	}

	listed := requestJSON(t, srv, http.MethodGet, "/api/remote-hosts", nil, cookie)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listed.Code, listed.Body.String())
	}
	var hosts []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Host     string `json:"host"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &hosts); err != nil {
		t.Fatalf("decode hosts: %v", err)
	}
	if len(hosts) != 1 || hosts[0].Name != "绿联 NAS" || hosts[0].Host != "192.168.1.20" || hosts[0].Username != "backup" {
		t.Fatalf("unexpected hosts: %+v", hosts)
	}
	if hosts[0].ID == "" {
		t.Fatal("created host must have an id")
	}
	if strings.Contains(listed.Body.String(), "PRIVATE KEY") || strings.Contains(listed.Body.String(), "privateKey") {
		t.Fatalf("list response leaked private key: %s", listed.Body.String())
	}
}

func TestRemoteHostCanGenerateAndExposeOnlyPublicKey(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)

	created := requestJSON(t, srv, http.MethodPost, "/api/remote-hosts", map[string]any{
		"name": "应用生成密钥的 NAS", "host": "192.168.1.20", "port": 22, "username": "backup", "keyMode": "generated",
	}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", created.Code, created.Body.String())
	}
	var result struct {
		ID        string `json:"id"`
		PublicKey string `json:"publicKey"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID == "" || !strings.HasPrefix(result.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("generated remote host=%+v", result)
	}
	if strings.Contains(created.Body.String(), "PRIVATE KEY") || strings.Contains(created.Body.String(), "privateKey") {
		t.Fatalf("create response leaked private key: %s", created.Body.String())
	}

	publicKey := requestJSON(t, srv, http.MethodGet, "/api/remote-hosts/"+result.ID+"/ssh-public-key", nil, cookie)
	if publicKey.Code != http.StatusOK || !strings.Contains(publicKey.Body.String(), result.PublicKey) {
		t.Fatalf("public key status = %d body=%s", publicKey.Code, publicKey.Body.String())
	}
}

func TestRemoteHostConnectionTestRequiresPinnedHostKey(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/remote-hosts", map[string]any{
		"name": "待核对主机密钥的服务器", "host": "192.168.1.20", "port": 22, "username": "backup", "keyMode": "generated",
	}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var host domain.RemoteHost
	if err := json.Unmarshal(created.Body.Bytes(), &host); err != nil {
		t.Fatal(err)
	}
	tested := requestJSON(t, srv, http.MethodPost, "/api/remote-hosts/"+host.ID+"/connection-test", map[string]any{}, cookie)
	if tested.Code != http.StatusUnprocessableEntity || !strings.Contains(tested.Body.String(), "先获取并核对主机密钥") {
		t.Fatalf("connection test status=%d body=%s", tested.Code, tested.Body.String())
	}
}

func TestRemoteHostFingerprintChangeHasDedicatedSemanticAudit(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/remote-hosts", map[string]any{"name": "NAS", "host": "nas", "port": 22, "username": "backup", "privateKey": "private-key", "hostFingerprint": "nas ssh-ed25519 OLD"}, cookie)
	var host domain.RemoteHost
	_ = json.Unmarshal(created.Body.Bytes(), &host)
	updated := requestJSON(t, srv, http.MethodPut, "/api/remote-hosts/"+host.ID, map[string]any{"name": "NAS", "host": "nas", "port": 22, "username": "backup", "hostFingerprint": "nas ssh-ed25519 NEW"}, cookie)
	if updated.Code != http.StatusOK {
		t.Fatalf("updated=%d %s", updated.Code, updated.Body.String())
	}
	audits, _ := srv.store.(*store.Store).ListAudits(context.Background(), 30)
	found := false
	for _, audit := range audits {
		found = found || audit.Action == "remote_host.fingerprint.change" && audit.Actor == "admin" && audit.TargetID == host.ID
	}
	if !found {
		t.Fatalf("missing fingerprint audit: %+v", audits)
	}
}

func TestLocalRepositoryCanBeCreatedWithoutRemoteHost(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)

	created := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
		"name": "本地照片仓库", "kind": "local", "path": "/Volumes/Backup/photos", "password": "repository-password", "passwordConfirmed": true,
		"maintenance": map[string]any{"schedule": map[string]any{"kind": "weekly", "dayOfWeek": 0, "timeOfDay": "03:00"}, "timezone": "Asia/Shanghai", "retention": map[string]any{"keepLast": 3}, "enabled": true},
	}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create local repository: status=%d body=%s", created.Code, created.Body.String())
	}
	var repository struct {
		ID           string `json:"id"`
		Kind         string `json:"kind"`
		RemoteHostID string `json:"remoteHostId"`
		Path         string `json:"path"`
		Status       string `json:"status"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &repository); err != nil {
		t.Fatal(err)
	}
	if repository.Kind != "local" || repository.RemoteHostID != "" || repository.Path != "/Volumes/Backup/photos" || repository.Status != "uninitialized" {
		t.Fatalf("repository=%+v", repository)
	}
	policy := requestJSON(t, srv, http.MethodGet, "/api/repositories/"+repository.ID+"/maintenance-policy", nil, cookie)
	if policy.Code != http.StatusOK || !strings.Contains(policy.Body.String(), `"enabled":true`) {
		t.Fatalf("new repository maintenance policy status=%d body=%s", policy.Code, policy.Body.String())
	}
}

func TestS3RepositoryUsesStructuredCredentialsWithoutExposingThem(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	accessKey, secretKey := "access-key-private", "secret-key-private"
	created := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
		"name": "对象存储仓库", "engine": "restic", "kind": "s3", "password": "repository-password", "passwordConfirmed": true,
		"s3": map[string]any{"endpoint": "https://objects.example.com", "bucket": "backup-prod", "region": "us-east-1", "prefix": "photos/main", "pathStyle": true, "accessKey": accessKey, "secretKey": secretKey, "credentialsConfirmed": true},
	}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create S3 repository: status=%d body=%s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), accessKey) || strings.Contains(created.Body.String(), secretKey) || strings.Contains(created.Body.String(), "backendSecret") {
		t.Fatalf("S3 response exposed protected material: %s", created.Body.String())
	}
	var repository domain.Repository
	if err := json.Unmarshal(created.Body.Bytes(), &repository); err != nil || repository.ID == "" || repository.S3 == nil || repository.Path != "photos/main" || !repository.S3.CredentialsConfigured {
		t.Fatalf("repository=%+v err=%v", repository, err)
	}
	execution, err := srv.store.(*store.Store).LoadRepositoryExecution(t.Context(), repository.ID)
	if err != nil || execution.Repository.BackendSecretID == "" {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
	firstSecretID := execution.Repository.BackendSecretID
	encoded, err := srv.secrets.Get(t.Context(), firstSecretID, s3backend.CredentialPurpose)
	credentials, decodeErr := s3backend.DecodeCredentials(encoded)
	if err != nil || decodeErr != nil || credentials.AccessKey != accessKey || credentials.SecretKey != secretKey {
		t.Fatalf("credentials=%+v getErr=%v decodeErr=%v", credentials, err, decodeErr)
	}

	updated := requestJSON(t, srv, http.MethodPut, "/api/repositories/"+repository.ID, map[string]any{
		"name": "对象存储仓库", "engine": "restic", "kind": "s3",
		"s3": map[string]any{"endpoint": "https://objects.example.com", "bucket": "backup-prod", "region": "us-east-1", "prefix": "photos/main", "pathStyle": true, "accessKey": "rotated-access", "secretKey": "rotated-secret", "credentialsConfirmed": true},
	}, cookie)
	if updated.Code != http.StatusOK || strings.Contains(updated.Body.String(), "rotated-access") || strings.Contains(updated.Body.String(), "rotated-secret") {
		t.Fatalf("update S3 repository: status=%d body=%s", updated.Code, updated.Body.String())
	}
	if _, err := srv.secrets.Get(t.Context(), firstSecretID, s3backend.CredentialPurpose); err == nil {
		t.Fatal("rotated S3 credential secret was retained")
	}
	execution, err = srv.store.(*store.Store).LoadRepositoryExecution(t.Context(), repository.ID)
	if err != nil || execution.Repository.BackendSecretID == firstSecretID {
		t.Fatalf("rotated execution=%+v err=%v", execution, err)
	}

	listed := requestJSON(t, srv, http.MethodGet, "/api/repositories", nil, cookie)
	audits, _ := srv.store.(*store.Store).ListAudits(t.Context(), 100)
	auditJSON, _ := json.Marshal(audits)
	combined := listed.Body.String() + string(auditJSON)
	for _, protected := range []string{accessKey, secretKey, "rotated-access", "rotated-secret"} {
		if strings.Contains(combined, protected) {
			t.Fatalf("protected S3 credential leaked through API or audit: %q", protected)
		}
	}
}

func TestS3RepositoryRejectsUnsafeEndpointAndUnconfirmedCredentials(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	for _, s3 := range []map[string]any{
		{"endpoint": "http://objects.example.com", "bucket": "backup-prod", "region": "us-east-1", "accessKey": "access", "secretKey": "secret", "credentialsConfirmed": true},
		{"endpoint": "https://user:pass@objects.example.com", "bucket": "backup-prod", "region": "us-east-1", "accessKey": "access", "secretKey": "secret", "credentialsConfirmed": true},
		{"endpoint": "https://objects.example.com?option=unsafe", "bucket": "backup-prod", "region": "us-east-1", "accessKey": "access", "secretKey": "secret", "credentialsConfirmed": true},
		{"endpoint": "https://objects.example.com", "bucket": "backup-prod", "region": "us-east-1", "accessKey": "access", "secretKey": "secret", "credentialsConfirmed": false},
	} {
		response := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{"name": "invalid", "engine": "restic", "kind": "s3", "password": "repository-password", "passwordConfirmed": true, "s3": s3}, cookie)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unsafe S3 settings accepted: status=%d body=%s input=%+v", response.Code, response.Body.String(), s3)
		}
	}
}

func TestDeletePreviewNamesDependenciesAndRejectsStaleConfirmation(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{"name": "待删除仓库", "kind": "local", "path": "/backup/delete", "password": "repository-password", "passwordConfirmed": true}, cookie)
	var repository domain.Repository
	_ = json.Unmarshal(created.Body.Bytes(), &repository)
	preview := requestJSON(t, srv, http.MethodGet, "/api/delete-previews/repositories/"+repository.ID, nil, cookie)
	var first store.ResourceDeletePreview
	_ = json.Unmarshal(preview.Body.Bytes(), &first)
	if preview.Code != http.StatusOK || first.Name != "待删除仓库" || first.UpdatedAt == "" {
		t.Fatalf("preview=%d %s", preview.Code, preview.Body.String())
	}
	updated := requestJSON(t, srv, http.MethodPut, "/api/repositories/"+repository.ID, map[string]any{"name": "已变化仓库", "kind": "local", "path": "/backup/delete"}, cookie)
	if updated.Code != http.StatusOK {
		t.Fatalf("updated=%d %s", updated.Code, updated.Body.String())
	}
	stale := requestJSON(t, srv, http.MethodPost, "/api/delete-previews/repositories/"+repository.ID+"/confirm", map[string]any{"expectedUpdatedAt": first.UpdatedAt}, cookie)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale=%d %s", stale.Code, stale.Body.String())
	}
	freshResponse := requestJSON(t, srv, http.MethodGet, "/api/delete-previews/repositories/"+repository.ID, nil, cookie)
	var fresh store.ResourceDeletePreview
	_ = json.Unmarshal(freshResponse.Body.Bytes(), &fresh)
	deleted := requestJSON(t, srv, http.MethodPost, "/api/delete-previews/repositories/"+repository.ID+"/confirm", map[string]any{"expectedUpdatedAt": fresh.UpdatedAt}, cookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("deleted=%d %s", deleted.Code, deleted.Body.String())
	}
}

func TestTaskCannotUseUninitializedRepository(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
		"name": "待初始化仓库", "kind": "local", "path": "/Volumes/Backup/pending", "password": "repository-password", "passwordConfirmed": true,
	}, cookie)
	var repository struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &repository); err != nil || repository.ID == "" {
		t.Fatalf("repository status=%d body=%s err=%v", created.Code, created.Body.String(), err)
	}

	task := requestJSON(t, srv, http.MethodPost, "/api/tasks", map[string]any{
		"name": "不能创建的任务", "kind": "directory", "repositoryId": repository.ID,
		"directory": map[string]any{"path": "/srv/photos"},
	}, cookie)
	if task.Code != http.StatusConflict {
		t.Fatalf("task status=%d body=%s", task.Code, task.Body.String())
	}
}

func TestChangingRepositoryLocationRequiresReinitialization(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
		"name": "照片仓库", "kind": "local", "path": "/backup/photos", "password": "repository-password", "passwordConfirmed": true,
	}, cookie)
	var repository struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &repository); err != nil || repository.ID == "" {
		t.Fatalf("create repository status=%d body=%s err=%v", created.Code, created.Body.String(), err)
	}
	if err := srv.store.(*store.Store).UpdateRepositoryStatus(context.Background(), repository.ID, "ready"); err != nil {
		t.Fatal(err)
	}

	updated := requestJSON(t, srv, http.MethodPut, "/api/repositories/"+repository.ID, map[string]any{
		"name": "照片仓库", "kind": "local", "path": "/backup/photos-new", "password": "",
	}, cookie)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"status":"uninitialized"`) {
		t.Fatalf("update repository status=%d body=%s", updated.Code, updated.Body.String())
	}
}

func TestEditingLocalRepositoryWithoutKindPreservesExistingKind(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
		"name": "本地仓库", "kind": "local", "path": "/backup/local", "password": "repository-password", "passwordConfirmed": true,
	}, cookie)
	var repository struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &repository); err != nil || repository.ID == "" {
		t.Fatalf("create repository status=%d body=%s err=%v", created.Code, created.Body.String(), err)
	}

	updated := requestJSON(t, srv, http.MethodPut, "/api/repositories/"+repository.ID, map[string]any{
		"name": "本地仓库", "path": "/backup/local", "password": "",
	}, cookie)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"kind":"local"`) {
		t.Fatalf("update repository status=%d body=%s", updated.Code, updated.Body.String())
	}
}

func TestEditingLocalRepositoryAcceptsLegacyEditorConnectionMode(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	created := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
		"name": "本地仓库", "kind": "local", "path": "/backup/local", "password": "repository-password", "passwordConfirmed": true,
	}, cookie)
	var repository struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &repository); err != nil || repository.ID == "" {
		t.Fatalf("create repository status=%d body=%s err=%v", created.Code, created.Body.String(), err)
	}

	updated := requestJSON(t, srv, http.MethodPut, "/api/repositories/"+repository.ID, map[string]any{
		"name": "本地仓库", "engine": "restic", "kind": "local", "remoteHostId": "", "path": "/backup/local",
		"password": "", "passwordConfirmed": true, "connectionMode": "create",
	}, cookie)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"kind":"local"`) {
		t.Fatalf("legacy editor update status=%d body=%s", updated.Code, updated.Body.String())
	}
}

func TestCompatibilityEndpointUsesCachedInitialReport(t *testing.T) {
	srv := newResourceTestServer(t)
	srv.compatibilityCache = compat.Report{Findings: []compat.Finding{{Capability: "restic", Tool: "restic", Path: "/cached/restic", Severity: compat.Info, Version: "0.19.0", Message: "缓存的 Restic 可用"}}}
	srv.compatibilityCached = true
	cookie := setupSession(t, srv)
	response := requestJSON(t, srv, http.MethodGet, "/api/compatibility", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("cached compatibility status=%d body=%s", response.Code, response.Body.String())
	}
	var report compat.Report
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 1 || report.Findings[0].Path != "/cached/restic" || report.Findings[0].Version != "0.19.0" {
		t.Fatalf("cached compatibility report=%+v", report)
	}
}

func TestNtfyBlankTokenKeepsExistingSecretAndReplacementDeletesOldSecret(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	for _, payload := range []map[string]any{
		{"baseUrl": "https://ntfy.example", "topic": "backup", "token": "first-token"},
		{"baseUrl": "https://ntfy.example", "topic": "backup-updated", "token": ""},
	} {
		rec := requestJSON(t, srv, http.MethodPost, "/api/ntfy", payload, cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("save ntfy status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	state := srv.store.(*store.Store)
	value, err := state.Metadata(context.Background(), "ntfy.config")
	if err != nil {
		t.Fatal(err)
	}
	var retained ntfyStored
	if err := json.Unmarshal([]byte(value), &retained); err != nil {
		t.Fatal(err)
	}
	if token, err := srv.secrets.Get(context.Background(), retained.TokenSecretID, "ntfy-token"); err != nil || string(token) != "first-token" {
		t.Fatalf("blank token did not retain secret: token=%q err=%v", token, err)
	}
	oldID := retained.TokenSecretID
	replaced := requestJSON(t, srv, http.MethodPost, "/api/ntfy", map[string]any{"baseUrl": "https://ntfy.example", "topic": "backup", "token": "second-token"}, cookie)
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace ntfy status=%d body=%s", replaced.Code, replaced.Body.String())
	}
	if _, err := srv.secrets.Get(context.Background(), oldID, "ntfy-token"); err == nil {
		t.Fatal("replaced ntfy token secret was not deleted")
	}
}

func TestNtfySupportsExplicitTokenClearAndDisable(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	if rec := requestJSON(t, srv, http.MethodPost, "/api/ntfy", map[string]any{"baseUrl": "https://ntfy.example", "topic": "backup", "token": "secret", "enabled": true}, cookie); rec.Code != http.StatusOK {
		t.Fatalf("save=%d %s", rec.Code, rec.Body.String())
	}
	var stored ntfyStored
	value, _ := srv.store.(*store.Store).Metadata(context.Background(), "ntfy.config")
	_ = json.Unmarshal([]byte(value), &stored)
	oldSecret := stored.TokenSecretID
	cleared := requestJSON(t, srv, http.MethodPost, "/api/ntfy", map[string]any{"baseUrl": "https://ntfy.example", "topic": "backup", "token": "", "clearToken": true, "enabled": true}, cookie)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear=%d %s", cleared.Code, cleared.Body.String())
	}
	if _, err := srv.secrets.Get(context.Background(), oldSecret, "ntfy-token"); err == nil {
		t.Fatal("cleared token secret still exists")
	}
	disabled := requestJSON(t, srv, http.MethodPost, "/api/ntfy", map[string]any{"baseUrl": "https://ntfy.example", "topic": "backup", "enabled": false}, cookie)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable=%d %s", disabled.Code, disabled.Body.String())
	}
	state := requestJSON(t, srv, http.MethodGet, "/api/ntfy", nil, cookie)
	if !strings.Contains(state.Body.String(), `"enabled":false`) || !strings.Contains(state.Body.String(), `"hasToken":false`) {
		t.Fatalf("state=%s", state.Body.String())
	}
}

func TestNtfyTestReturnsUnavailableWithoutRuntimeClient(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	rec := requestJSON(t, srv, http.MethodPost, "/api/ntfy/test", map[string]any{}, cookie)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ntfy test status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdministratorCanConfigureMixedBackupPlan(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)

	hostRec := requestJSON(t, srv, http.MethodPost, "/api/remote-hosts", map[string]any{
		"name": "nas", "host": "192.168.1.20", "port": 22, "username": "backup", "privateKey": "private-key",
	}, cookie)
	var host struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(hostRec.Body.Bytes(), &host); err != nil || hostRec.Code != http.StatusCreated {
		t.Fatalf("create host: status=%d body=%s err=%v", hostRec.Code, hostRec.Body.String(), err)
	}

	createRepo := func(name, path string) string {
		t.Helper()
		rec := requestJSON(t, srv, http.MethodPost, "/api/repositories", map[string]any{
			"name": name, "remoteHostId": host.ID, "path": path, "password": "repository-password", "passwordConfirmed": true,
		}, cookie)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create repository: status=%d body=%s", rec.Code, rec.Body.String())
		}
		var repository struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &repository); err != nil {
			t.Fatalf("decode repository: %v", err)
		}
		if strings.Contains(rec.Body.String(), "repository-password") || strings.Contains(rec.Body.String(), "passwordSecret") {
			t.Fatalf("repository response leaked secret: %s", rec.Body.String())
		}
		return repository.ID
	}
	directoryRepoID := createRepo("photos repo", "/volume1/restic/photos")
	databaseRepoID := createRepo("gitea repo", "/volume1/restic/gitea")
	state := srv.store.(*store.Store)
	if err := state.UpdateRepositoryStatus(context.Background(), directoryRepoID, "ready"); err != nil {
		t.Fatal(err)
	}
	if err := state.UpdateRepositoryStatus(context.Background(), databaseRepoID, "ready"); err != nil {
		t.Fatal(err)
	}

	connectionRec := requestJSON(t, srv, http.MethodPost, "/api/database-connections", map[string]any{
		"name": "mysql backup", "engine": "mysql", "purpose": "backup", "network": "tcp",
		"host": "127.0.0.1", "port": 3306, "username": "backup", "password": "database-password",
		"tls": map[string]any{"mode": "preferred"}, "toolPaths": map[string]string{"dump": "/usr/bin/mysqldump", "restore": "/usr/bin/mysql"},
	}, cookie)
	if connectionRec.Code != http.StatusCreated {
		t.Fatalf("create connection: status=%d body=%s", connectionRec.Code, connectionRec.Body.String())
	}
	var connection struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(connectionRec.Body.Bytes(), &connection); err != nil {
		t.Fatalf("decode connection: %v", err)
	}
	if strings.Contains(connectionRec.Body.String(), "database-password") {
		t.Fatalf("database connection response leaked secret: %s", connectionRec.Body.String())
	}

	createTask := func(payload map[string]any) string {
		t.Helper()
		rec := requestJSON(t, srv, http.MethodPost, "/api/tasks", payload, cookie)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create task: status=%d body=%s", rec.Code, rec.Body.String())
		}
		var task struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		return task.ID
	}
	directoryTaskID := createTask(map[string]any{
		"name": "photos", "kind": "directory", "repositoryId": directoryRepoID,
		"directory": map[string]any{"path": "/srv/photos", "skipIfUnchanged": true},
		"retention": map[string]any{"keepWithinDays": 30}, "resources": map[string]any{"compression": "auto"},
	})
	databaseTaskID := createTask(map[string]any{
		"name": "gitea database", "kind": "database", "repositoryId": databaseRepoID,
		"database":  map[string]any{"connectionId": connection.ID, "database": "gitea"},
		"retention": map[string]any{"keepLast": 14}, "resources": map[string]any{"compression": "auto"},
	})
	connectionChange := requestJSON(t, srv, http.MethodPut, "/api/database-connections/"+connection.ID, map[string]any{
		"name": "mysql restore", "engine": "mysql", "purpose": "restore", "network": "tcp",
		"host": "127.0.0.1", "port": 3306, "username": "restore", "password": "new-purpose-password",
		"tls": map[string]any{"mode": "preferred"}, "toolPaths": map[string]string{"restore": "/usr/bin/mysql"},
	}, cookie)
	if connectionChange.Code != http.StatusConflict {
		t.Fatalf("referenced connection purpose change status=%d body=%s", connectionChange.Code, connectionChange.Body.String())
	}

	conflict := requestJSON(t, srv, http.MethodPost, "/api/tasks", map[string]any{
		"name": "duplicate photos", "kind": "directory", "repositoryId": directoryRepoID,
		"directory": map[string]any{"path": "/srv/photos-copy"},
	}, cookie)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("repository reuse status = %d body=%s", conflict.Code, conflict.Body.String())
	}

	planRec := requestJSON(t, srv, http.MethodPost, "/api/plans", map[string]any{
		"name": "nightly", "timezone": "Asia/Shanghai", "maxParallel": 1,
		"schedule": map[string]any{"kind": "daily", "timeOfDay": "02:30"},
		"taskIds":  []string{directoryTaskID, databaseTaskID},
	}, cookie)
	if planRec.Code != http.StatusCreated {
		t.Fatalf("create plan: status=%d body=%s", planRec.Code, planRec.Body.String())
	}

	listed := requestJSON(t, srv, http.MethodGet, "/api/tasks", nil, cookie)
	if listed.Code != http.StatusOK {
		t.Fatalf("list tasks status = %d body=%s", listed.Code, listed.Body.String())
	}
	var tasks []map[string]any
	if err := json.Unmarshal(listed.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("task count = %d", len(tasks))
	}

	listedPlans := requestJSON(t, srv, http.MethodGet, "/api/plans", nil, cookie)
	if listedPlans.Code != http.StatusOK {
		t.Fatalf("list plans status = %d body=%s", listedPlans.Code, listedPlans.Body.String())
	}
	var plans []struct {
		Name    string   `json:"name"`
		TaskIDs []string `json:"taskIds"`
	}
	if err := json.Unmarshal(listedPlans.Body.Bytes(), &plans); err != nil {
		t.Fatalf("decode plans: %v", err)
	}
	if len(plans) != 1 || plans[0].Name != "nightly" || len(plans[0].TaskIDs) != 2 {
		t.Fatalf("unexpected plans: %+v", plans)
	}

	updatedHost := requestJSON(t, srv, http.MethodPut, "/api/remote-hosts/"+host.ID, map[string]any{
		"name": "nas updated", "host": "nas.example.test", "port": 2222, "username": "backup", "hostFingerprint": "nas.example.test ssh-ed25519 AAAA",
	}, cookie)
	if updatedHost.Code != http.StatusOK {
		t.Fatalf("update host status=%d body=%s", updatedHost.Code, updatedHost.Body.String())
	}
	gotHost := requestJSON(t, srv, http.MethodGet, "/api/remote-hosts/"+host.ID, nil, cookie)
	if gotHost.Code != http.StatusOK || !strings.Contains(gotHost.Body.String(), "nas.example.test") {
		t.Fatalf("get updated host status=%d body=%s", gotHost.Code, gotHost.Body.String())
	}
	unsafePasswordEdit := requestJSON(t, srv, http.MethodPut, "/api/repositories/"+directoryRepoID, map[string]any{"name": "photos repo", "remoteHostId": host.ID, "path": "/volume1/restic/photos", "password": "bypass-rotation"}, cookie)
	if unsafePasswordEdit.Code != http.StatusUnprocessableEntity {
		t.Fatalf("repository password bypass status=%d body=%s", unsafePasswordEdit.Code, unsafePasswordEdit.Body.String())
	}
	if rec := requestJSON(t, srv, http.MethodDelete, "/api/remote-hosts/"+host.ID, nil, cookie); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("delete referenced host status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, target := range []string{"/api/plans/" + func() string {
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(planRec.Body.Bytes(), &p)
		return p.ID
	}(), "/api/tasks/" + directoryTaskID, "/api/tasks/" + databaseTaskID, "/api/repositories/" + directoryRepoID, "/api/repositories/" + databaseRepoID, "/api/database-connections/" + connection.ID, "/api/remote-hosts/" + host.ID} {
		parts := strings.Split(strings.TrimPrefix(target, "/api/"), "/")
		previewResponse := requestJSON(t, srv, http.MethodGet, "/api/delete-previews/"+parts[0]+"/"+parts[1], nil, cookie)
		var preview store.ResourceDeletePreview
		_ = json.Unmarshal(previewResponse.Body.Bytes(), &preview)
		rec := requestJSON(t, srv, http.MethodPost, "/api/delete-previews/"+parts[0]+"/"+parts[1]+"/confirm", map[string]any{"expectedUpdatedAt": preview.UpdatedAt}, cookie)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("delete %s status=%d body=%s", target, rec.Code, rec.Body.String())
		}
	}
	audits, err := srv.store.(*store.Store).ListAudits(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	foundDomainDelete := false
	for _, audit := range audits {
		if audit.Action == "plan.delete" && audit.Actor == "admin" {
			foundDomainDelete = true
		}
	}
	if !foundDomainDelete {
		t.Fatalf("missing domain deletion audit: %+v", audits)
	}
}
