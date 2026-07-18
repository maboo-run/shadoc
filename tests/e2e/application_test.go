//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProductionBinaryCompleteAdministrationFlow(t *testing.T) {
	_, currentFile, _, _ := runtime.Caller(0)
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	binary := os.Getenv("RESTIC_CONTROL_E2E_BINARY")
	if binary == "" {
		binary = filepath.Join(repositoryRoot, "dist", "shadoc")
	}
	if info, err := os.Stat(binary); err != nil || info.IsDir() {
		t.Fatalf("production E2E binary is missing at %s; run make build first", binary)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	baseURL := "http://" + address
	dataDir := filepath.Join(t.TempDir(), "application-data")
	process := startProductionBinary(t, binary, dataDir, address)
	defer func() { process.Stop(t) }()
	waitForHealth(t, baseURL)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	index, _ := requestApplication(t, client, http.MethodGet, baseURL+"/", "", nil)
	if !bytes.Contains(index, []byte("<title>影刻 · Shadoc</title>")) {
		t.Fatal("production binary did not serve the embedded frontend")
	}
	_, setupHeaders := requestApplication(t, client, http.MethodPost, baseURL+"/api/setup", "", map[string]any{
		"username": "admin", "password": "correct horse battery staple",
	})
	csrf := setupHeaders.Get("X-CSRF-Token")
	if csrf == "" {
		t.Fatal("setup did not return a CSRF token")
	}

	hostBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/remote-hosts", csrf, map[string]any{
		"name": "NAS", "host": "nas.example.test", "port": 2222, "username": "backup",
		"privateKey":      "-----BEGIN OPENSSH PRIVATE KEY-----\nfixture\n-----END OPENSSH PRIVATE KEY-----",
		"hostFingerprint": "[nas.example.test]:2222 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2Efixture",
	})
	_ = jsonID(t, hostBody)
	repositoryPath := filepath.Join(t.TempDir(), "repository")
	repoBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/repositories", csrf, map[string]any{
		"name": "目录 A", "kind": "local", "path": repositoryPath, "password": "repository-password-long", "passwordConfirmed": true,
	})
	repositoryID := jsonID(t, repoBody)
	initializeBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/repositories/"+repositoryID+"/initialize", csrf, map[string]any{}, http.StatusAccepted)
	var initialize struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(initializeBody, &initialize); err != nil || initialize.OperationID == "" {
		t.Fatalf("initialize=%s err=%v", initializeBody, err)
	}
	waitApplicationOperation(t, client, baseURL, csrf, initialize.OperationID)
	requestApplication(t, client, http.MethodPost, baseURL+"/api/database-connections", csrf, map[string]any{
		"name": "MySQL 草稿", "engine": "mysql", "purpose": "backup", "network": "tcp",
		"host": "127.0.0.1", "port": 3306, "username": "backup", "password": "database-password-long",
		"tls":       map[string]any{"mode": "preferred"},
		"toolPaths": map[string]string{"dump": "/missing/mysqldump", "admin": "/missing/mysql"},
	})
	taskBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/tasks", csrf, map[string]any{
		"name": "目录 A", "kind": "directory", "repositoryId": repositoryID,
		"directory": map[string]any{"path": "/path/that/does/not/exist", "exclusions": []string{"**/.cache"}, "skipIfUnchanged": true},
		"retention": map[string]any{"keepWithinDays": 30}, "resources": map[string]any{"compression": "auto"}, "enabled": false,
	})
	taskID := jsonID(t, taskBody)
	for _, schedule := range []map[string]any{
		{"kind": "daily", "timeOfDay": "02:30"},
		{"kind": "weekly", "dayOfWeek": 1, "timeOfDay": "03:15"},
		{"kind": "interval", "intervalHours": 6},
	} {
		requestApplication(t, client, http.MethodPost, baseURL+"/api/plans", csrf, map[string]any{
			"name": "计划 " + fmt.Sprint(schedule["kind"]), "schedule": schedule, "timezone": "Asia/Shanghai",
			"maxParallel": 1, "taskIds": []string{taskID}, "enabled": false,
		})
	}
	requestApplication(t, client, http.MethodPut, baseURL+"/api/lifecycle-policy", csrf, map[string]any{
		"runDays": 90, "rawLogDays": 7, "auditDays": 180, "rawLogMaxBytes": 64 << 20,
	})
	requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/lifecycle/cleanup", csrf, map[string]any{"password": "correct horse battery staple"}, http.StatusOK)
	audit, headers := requestApplication(t, client, http.MethodGet, baseURL+"/api/audits/export", csrf, nil)
	if !strings.Contains(headers.Get("Content-Type"), "text/csv") || !bytes.Contains(audit, []byte("action")) {
		t.Fatalf("audit export content-type=%s body=%q", headers.Get("Content-Type"), audit)
	}
	requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/vault/lock-on-restart", csrf, map[string]any{
		"passphrase": "independent vault passphrase",
	}, http.StatusNoContent)
	process.Stop(t)

	process = startProductionBinary(t, binary, dataDir, address)
	waitForHealth(t, baseURL)
	lockedBody, _ := requestApplicationStatus(t, client, http.MethodGet, baseURL+"/api/dashboard", csrf, nil, http.StatusLocked)
	if !bytes.Contains(lockedBody, []byte("秘密库已锁定")) {
		t.Fatalf("locked response=%s", lockedBody)
	}
	_, loginHeaders := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/login", "", map[string]any{
		"username": "admin", "password": "correct horse battery staple",
	}, http.StatusOK)
	csrf = loginHeaders.Get("X-CSRF-Token")
	requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/vault/unlock", csrf, map[string]any{
		"passphrase": "independent vault passphrase",
	}, http.StatusNoContent)
	requestApplication(t, client, http.MethodGet, baseURL+"/api/dashboard", csrf, nil)
	requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/vault/automatic", csrf, map[string]any{"password": "correct horse battery staple", "confirmed": true}, http.StatusNoContent)
	recordCheck("production-binary-administration", "passed", runtime.GOOS+"/"+runtime.GOARCH)
}

func waitApplicationOperation(t *testing.T, client *http.Client, baseURL, csrf, id string) {
	t.Helper()
	waitApplicationOperationState(t, client, baseURL, csrf, id, "success")
}

func waitApplicationOperationState(t *testing.T, client *http.Client, baseURL, csrf, id, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		body, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/operations/"+id, csrf, nil)
		var operation struct{ Status, ErrorSummary string }
		if json.Unmarshal(body, &operation) == nil {
			if operation.Status == want {
				return
			}
			if operation.Status == "failed" || operation.Status == "cancelled" || operation.Status == "cleanup_required" {
				t.Fatalf("operation status=%s want=%s: %s", operation.Status, want, operation.ErrorSummary)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("operation %s did not reach %s", id, want)
}

type productionProcess struct {
	cmd     *exec.Cmd
	logFile *os.File
}

func startProductionBinary(t *testing.T, binary, dataDir, address string) *productionProcess {
	t.Helper()
	logFile, err := os.CreateTemp(t.TempDir(), "shadoc-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "SHADOC_DATA_DIR="+dataDir, "SHADOC_LISTEN="+address)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return &productionProcess{cmd: cmd, logFile: logFile}
}

func (p *productionProcess) Stop(t *testing.T) {
	t.Helper()
	if p == nil || p.cmd == nil || p.cmd.ProcessState != nil {
		return
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
	}
	_ = p.logFile.Close()
}

func waitForHealth(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/api/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("production binary did not become healthy")
}

func requestApplication(t *testing.T, client *http.Client, method, url, csrf string, body any) ([]byte, http.Header) {
	t.Helper()
	return requestApplicationStatus(t, client, method, url, csrf, body, expectedStatus(method))
}

func requestApplicationStatus(t *testing.T, client *http.Client, method, url, csrf string, body any, want int) ([]byte, http.Header) {
	t.Helper()
	var input io.Reader
	if body != nil {
		content, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(content)
	}
	request, err := http.NewRequest(method, url, input)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, url, response.StatusCode, want, content)
	}
	return content, response.Header.Clone()
}

func expectedStatus(method string) int {
	switch method {
	case http.MethodPost:
		return http.StatusCreated
	case http.MethodPut, http.MethodDelete:
		return http.StatusNoContent
	default:
		return http.StatusOK
	}
}

func jsonID(t *testing.T, content []byte) string {
	t.Helper()
	var value struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(content, &value); err != nil || value.ID == "" {
		t.Fatalf("decode resource ID body=%s err=%v", content, err)
	}
	return value.ID
}
