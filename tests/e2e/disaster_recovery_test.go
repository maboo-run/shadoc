//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	disasterSourceAdministratorPassword = "source administrator password 2026"
	disasterTargetAdministratorPassword = "target administrator password 2026"
	disasterRepositoryPassword          = "repository recovery password 2026"
	disasterRecoveryPassphrase          = "independent recovery passphrase 2026"
	disasterPayload                     = "restic-control disaster recovery payload\n"
)

type disasterRecoveryFixture struct {
	Bundle             []byte
	RepositoryPath     string
	RepositoryID       string
	SnapshotID         string
	SourceOperationIDs []string
	SourceRunID        string
}

func TestProductionBinaryDisasterRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-recording Restic wrapper used by this production E2E currently requires a POSIX shell")
	}
	binary := productionBinary(t)
	realRestic, err := exec.LookPath("restic")
	if err != nil {
		t.Fatal("production disaster recovery E2E requires restic on PATH")
	}
	root := t.TempDir()
	fixture := createDisasterRecoveryFixture(t, binary, root)
	if bytes.Contains(fixture.Bundle, []byte(disasterRepositoryPassword)) || bytes.Contains(fixture.Bundle, []byte(disasterSourceAdministratorPassword)) {
		t.Fatal("recovery bundle exposed a plaintext secret")
	}

	wrapperDirectory, commandLog := writeResticRecordingWrapper(t, realRestic)
	path := wrapperDirectory + string(os.PathListSeparator) + os.Getenv("PATH")
	exerciseExistingRepositoryFallback(t, binary, root, fixture, commandLog, map[string]string{"PATH": path})
	exerciseControlPlaneImport(t, binary, root, fixture, commandLog, map[string]string{"PATH": path})
	recordCheck("production-binary-disaster-recovery", "passed", runtime.GOOS+"/"+runtime.GOARCH)
}

func createDisasterRecoveryFixture(t *testing.T, binary, root string) disasterRecoveryFixture {
	t.Helper()
	sourcePath := filepath.Join(root, "source-data", "album")
	if err := os.MkdirAll(sourcePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "payload.txt"), []byte(disasterPayload), 0o600); err != nil {
		t.Fatal(err)
	}
	repositoryPath := filepath.Join(root, "restic-repository")
	process, client, baseURL, csrf := startConfiguredProductionBinary(t, binary, filepath.Join(root, "source-control-plane"), "source-admin", disasterSourceAdministratorPassword, nil)
	defer process.Stop(t)

	repositoryBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/repositories", csrf, map[string]any{
		"name": "灾难恢复源仓库", "engine": "restic", "kind": "local", "path": repositoryPath,
		"password": disasterRepositoryPassword, "passwordConfirmed": true,
	})
	repositoryID := jsonID(t, repositoryBody)
	initializeBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/repositories/"+repositoryID+"/initialize", csrf, map[string]any{}, http.StatusAccepted)
	initializeOperationID := acceptedOperationID(t, initializeBody)
	waitApplicationOperation(t, client, baseURL, csrf, initializeOperationID)

	taskPayload := map[string]any{
		"name": "灾难恢复目录任务", "kind": "directory", "repositoryId": repositoryID,
		"directory": map[string]any{"path": filepath.Dir(sourcePath), "exclusions": []string{}, "skipIfUnchanged": false},
		"retention": map[string]any{"keepLast": 3}, "resources": map[string]any{"downloadKiBPerSecond": 128, "compression": "auto"}, "enabled": false,
	}
	taskBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/tasks", csrf, taskPayload)
	taskID := jsonID(t, taskBody)
	previewBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/tasks/"+taskID+"/preview", csrf, map[string]any{})
	var scopePreview struct {
		PreviewID string `json:"previewId"`
	}
	if err := json.Unmarshal(previewBody, &scopePreview); err != nil || scopePreview.PreviewID == "" {
		t.Fatalf("task scope preview=%s err=%v", previewBody, err)
	}
	taskPayload["enabled"] = true
	taskPayload["previewId"] = scopePreview.PreviewID
	requestApplicationStatus(t, client, http.MethodPut, baseURL+"/api/tasks/"+taskID, csrf, taskPayload, http.StatusOK)
	runBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/tasks/"+taskID+"/run", csrf, map[string]any{}, http.StatusAccepted)
	runOperationID := acceptedOperationID(t, runBody)
	waitApplicationOperation(t, client, baseURL, csrf, runOperationID)
	requestApplication(t, client, http.MethodPost, baseURL+"/api/plans", csrf, map[string]any{
		"name": "灾难恢复计划", "schedule": map[string]any{"kind": "daily", "timeOfDay": "23:59"},
		"timezone": "UTC", "maxParallel": 1, "taskIds": []string{taskID}, "enabled": true,
	})

	snapshotsBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/repositories/"+repositoryID+"/snapshots", csrf, nil)
	snapshotID := firstSnapshotID(t, snapshotsBody)
	runsBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/runs?limit=20", csrf, nil)
	var runs []struct {
		ID     string `json:"id"`
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(runsBody, &runs); err != nil {
		t.Fatal(err)
	}
	sourceRunID := ""
	for _, item := range runs {
		if item.TaskID == taskID {
			sourceRunID = item.ID
			break
		}
	}
	if sourceRunID == "" {
		t.Fatalf("source run missing: %s", runsBody)
	}

	bundle, headers := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/control-plane/export", csrf, map[string]any{
		"administratorPassword":          disasterSourceAdministratorPassword,
		"recoveryPassphrase":             disasterRecoveryPassphrase,
		"recoveryPassphraseConfirmation": disasterRecoveryPassphrase,
	}, http.StatusOK)
	if len(bundle) == 0 || !strings.Contains(headers.Get("Content-Disposition"), ".rcbundle") {
		t.Fatalf("invalid recovery bundle response: headers=%v bytes=%d", headers, len(bundle))
	}
	process.Stop(t)
	return disasterRecoveryFixture{
		Bundle: bundle, RepositoryPath: repositoryPath, RepositoryID: repositoryID, SnapshotID: snapshotID,
		SourceOperationIDs: []string{initializeOperationID, runOperationID}, SourceRunID: sourceRunID,
	}
}

func exerciseExistingRepositoryFallback(t *testing.T, binary, root string, fixture disasterRecoveryFixture, commandLog string, environment map[string]string) {
	t.Helper()
	process, client, baseURL, csrf := startConfiguredProductionBinary(t, binary, filepath.Join(root, "direct-connect-control-plane"), "direct-admin", disasterTargetAdministratorPassword, environment)
	defer process.Stop(t)
	baselineDigest := directoryTreeDigest(t, fixture.RepositoryPath)

	failedBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/repositories/connect", csrf, map[string]any{
		"name": "口令错误的接入", "engine": "restic", "kind": "local", "path": fixture.RepositoryPath,
		"password": "wrong repository password", "passwordConfirmed": true,
	}, http.StatusAccepted)
	waitApplicationOperationState(t, client, baseURL, csrf, acceptedOperationID(t, failedBody), "failed")
	assertReadOnlyVerificationCommands(t, commandLog)
	if got := directoryTreeDigest(t, fixture.RepositoryPath); got != baselineDigest {
		t.Fatalf("failed existing-repository verification mutated repository: before=%s after=%s", baselineDigest, got)
	}
	repositoriesBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/repositories", csrf, nil)
	assertJSONArrayLength(t, repositoriesBody, 0, "failed repository connection")

	clearCommandLog(t, commandLog)
	connectedBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/repositories/connect", csrf, map[string]any{
		"name": "全新实例已有仓库", "engine": "restic", "kind": "local", "path": fixture.RepositoryPath,
		"password": disasterRepositoryPassword, "passwordConfirmed": true,
	}, http.StatusAccepted)
	var connected struct {
		RepositoryID string `json:"repositoryId"`
		OperationID  string `json:"operationId"`
	}
	if err := json.Unmarshal(connectedBody, &connected); err != nil || connected.RepositoryID == "" || connected.OperationID == "" {
		t.Fatalf("connect response=%s err=%v", connectedBody, err)
	}
	waitApplicationOperation(t, client, baseURL, csrf, connected.OperationID)
	assertReadOnlyVerificationCommands(t, commandLog)
	if got := directoryTreeDigest(t, fixture.RepositoryPath); got != baselineDigest {
		t.Fatalf("successful existing-repository verification mutated repository: before=%s after=%s", baselineDigest, got)
	}
	tasksBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/tasks", csrf, nil)
	assertJSONArrayLength(t, tasksBody, 0, "direct-connect target tasks")

	snapshotsBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/repositories/"+connected.RepositoryID+"/snapshots", csrf, nil)
	snapshotID := firstSnapshotID(t, snapshotsBody)
	if snapshotID != fixture.SnapshotID {
		t.Fatalf("connected snapshot=%q source=%q", snapshotID, fixture.SnapshotID)
	}
	assertSnapshotContainsPayload(t, client, baseURL, csrf, connected.RepositoryID, snapshotID)
	restoreTarget := filepath.Join(root, "direct-connect-restored")
	restoreDirectoryThroughAPI(t, client, baseURL, csrf, connected.RepositoryID, snapshotID, restoreTarget, disasterTargetAdministratorPassword, 0)
	content, err := os.ReadFile(filepath.Join(restoreTarget, "album", "payload.txt"))
	if err != nil || string(content) != disasterPayload {
		t.Fatalf("direct-connect restore result=%q err=%v", content, err)
	}
}

func exerciseControlPlaneImport(t *testing.T, binary, root string, fixture disasterRecoveryFixture, commandLog string, environment map[string]string) {
	t.Helper()
	process, client, baseURL, csrf := startConfiguredProductionBinary(t, binary, filepath.Join(root, "import-control-plane"), "replacement-admin", disasterTargetAdministratorPassword, environment)
	defer process.Stop(t)

	requestApplicationMultipartStatus(t, client, baseURL+"/api/control-plane/import/preflight", csrf, fixture.Bundle, map[string]string{
		"recoveryPassphrase": "wrong recovery passphrase",
	}, http.StatusUnprocessableEntity)
	tampered := append([]byte(nil), fixture.Bundle...)
	tampered[len(tampered)-1] ^= 1
	requestApplicationMultipartStatus(t, client, baseURL+"/api/control-plane/import/preflight", csrf, tampered, map[string]string{
		"recoveryPassphrase": disasterRecoveryPassphrase,
	}, http.StatusUnprocessableEntity)
	repositoriesBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/repositories", csrf, nil)
	assertJSONArrayLength(t, repositoriesBody, 0, "rejected control-plane imports")

	previewBody, _ := requestApplicationMultipartStatus(t, client, baseURL+"/api/control-plane/import/preflight", csrf, fixture.Bundle, map[string]string{
		"recoveryPassphrase": disasterRecoveryPassphrase,
	}, http.StatusOK)
	var preview struct {
		PreviewID                string         `json:"previewId"`
		CanImport                bool           `json:"canImport"`
		ResourceCounts           map[string]int `json:"resourceCounts"`
		Conflicts                []any          `json:"conflicts"`
		MissingTools             []any          `json:"missingTools"`
		ExcludedTransientClasses []string       `json:"excludedTransientClasses"`
	}
	if err := json.Unmarshal(previewBody, &preview); err != nil {
		t.Fatalf("decode recovery preview=%s: %v", previewBody, err)
	}
	if !preview.CanImport || preview.PreviewID == "" || len(preview.Conflicts) != 0 || len(preview.MissingTools) != 0 || preview.ResourceCounts["repositories"] != 1 || preview.ResourceCounts["tasks"] != 1 || preview.ResourceCounts["plans"] != 1 {
		t.Fatalf("unexpected recovery preview: %s", previewBody)
	}
	for _, class := range []string{"sessions", "active_operations", "agent_enrollment_tokens", "run_records_and_logs"} {
		if !containsString(preview.ExcludedTransientClasses, class) {
			t.Fatalf("recovery preview did not declare %q exclusion: %s", class, previewBody)
		}
	}

	importBody, _ := requestApplicationMultipartStatus(t, client, baseURL+"/api/control-plane/import", csrf, fixture.Bundle, map[string]string{
		"recoveryPassphrase": disasterRecoveryPassphrase, "previewId": preview.PreviewID,
		"administratorPassword": disasterTargetAdministratorPassword, "impactConfirmed": "true",
	}, http.StatusAccepted)
	importOperationID := acceptedOperationID(t, importBody)
	waitApplicationOperation(t, client, baseURL, csrf, importOperationID)

	sessionBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/session", csrf, nil)
	if !bytes.Contains(sessionBody, []byte(`"username":"replacement-admin"`)) || bytes.Contains(sessionBody, []byte("source-admin")) {
		t.Fatalf("target administrator session was replaced: %s", sessionBody)
	}
	runsBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/runs?limit=20", csrf, nil)
	assertJSONArrayLength(t, runsBody, 0, "imported run history")
	if bytes.Contains(runsBody, []byte(fixture.SourceRunID)) {
		t.Fatalf("source run was imported: %s", runsBody)
	}
	operationsBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/operations?limit=100", csrf, nil)
	for _, operationID := range fixture.SourceOperationIDs {
		if bytes.Contains(operationsBody, []byte(operationID)) {
			t.Fatalf("source operation %q was imported: %s", operationID, operationsBody)
		}
	}

	repositoriesBody, _ = requestApplication(t, client, http.MethodGet, baseURL+"/api/repositories", csrf, nil)
	var repositories []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(repositoriesBody, &repositories); err != nil || len(repositories) != 1 || repositories[0].ID != fixture.RepositoryID || repositories[0].Status != "disconnected" {
		t.Fatalf("imported repositories=%s err=%v", repositoriesBody, err)
	}
	assertImportedResourcesDisabled(t, client, baseURL, csrf)

	clearCommandLog(t, commandLog)
	verifyBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/repositories/"+fixture.RepositoryID+"/verify-existing", csrf, map[string]any{}, http.StatusAccepted)
	waitApplicationOperation(t, client, baseURL, csrf, acceptedOperationID(t, verifyBody))
	assertReadOnlyVerificationCommands(t, commandLog)
	repositoriesBody, _ = requestApplication(t, client, http.MethodGet, baseURL+"/api/repositories", csrf, nil)
	if !bytes.Contains(repositoriesBody, []byte(`"status":"ready"`)) {
		t.Fatalf("imported repository was not revalidated: %s", repositoriesBody)
	}
	snapshotsBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/repositories/"+fixture.RepositoryID+"/snapshots", csrf, nil)
	if snapshotID := firstSnapshotID(t, snapshotsBody); snapshotID != fixture.SnapshotID {
		t.Fatalf("imported repository snapshot=%q source=%q", snapshotID, fixture.SnapshotID)
	}
	assertSnapshotContainsPayload(t, client, baseURL, csrf, fixture.RepositoryID, fixture.SnapshotID)
	clearCommandLog(t, commandLog)
	restoreTarget := filepath.Join(root, "import-restored")
	restoreDirectoryThroughAPI(t, client, baseURL, csrf, fixture.RepositoryID, fixture.SnapshotID, restoreTarget, disasterTargetAdministratorPassword, 128)
	content, err := os.ReadFile(filepath.Join(restoreTarget, "album", "payload.txt"))
	if err != nil || string(content) != disasterPayload {
		t.Fatalf("import restore result=%q err=%v", content, err)
	}
	assertRestoreDownloadLimitCommand(t, commandLog, 128)
}

func startConfiguredProductionBinary(t *testing.T, binary, dataDir, username, password string, environment map[string]string) (*productionProcess, *http.Client, string, string) {
	t.Helper()
	address := freeAddress(t)
	baseURL := "http://" + address
	process := startProductionBinaryWithEnvironment(t, binary, dataDir, address, environment)
	waitForHealth(t, baseURL)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	_, headers := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/setup", "", map[string]any{
		"username": username, "password": password,
	}, http.StatusCreated)
	csrf := headers.Get("X-CSRF-Token")
	if csrf == "" {
		process.Stop(t)
		t.Fatal("setup did not return a CSRF token")
	}
	return process, client, baseURL, csrf
}

func startProductionBinaryWithEnvironment(t *testing.T, binary, dataDir, address string, overrides map[string]string) *productionProcess {
	t.Helper()
	logFile, err := os.CreateTemp(t.TempDir(), "shadoc-recovery-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary)
	values := map[string]string{"SHADOC_DATA_DIR": dataDir, "SHADOC_LISTEN": address}
	for key, value := range overrides {
		values[key] = value
	}
	cmd.Env = environmentWithOverrides(os.Environ(), values)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	return &productionProcess{cmd: cmd, logFile: logFile}
}

func environmentWithOverrides(current []string, overrides map[string]string) []string {
	result := make([]string, 0, len(current)+len(overrides))
	for _, value := range current {
		key, _, ok := strings.Cut(value, "=")
		if ok {
			if _, replaced := overrides[key]; replaced {
				continue
			}
		}
		result = append(result, value)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+overrides[key])
	}
	return result
}

func writeResticRecordingWrapper(t *testing.T, realRestic string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	commandLog := filepath.Join(directory, "commands.log")
	if err := os.WriteFile(commandLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %s\nexec %s \"$@\"\n", shellQuote(commandLog), shellQuote(realRestic))
	if err := os.WriteFile(filepath.Join(directory, "restic"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return directory, commandLog
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func clearCommandLog(t *testing.T, path string) {
	t.Helper()
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
}

func assertReadOnlyVerificationCommands(t *testing.T, path string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	foundVerification := false
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		fields := strings.Fields(line)
		for _, field := range fields {
			if field == "init" {
				t.Fatalf("existing repository flow invoked restic init: %q", line)
			}
		}
		for index := range fields {
			if fields[index] == "snapshots" && containsString(fields[index+1:], "--json") && containsString(fields[index+1:], "--no-lock") {
				foundVerification = true
			}
		}
	}
	if !foundVerification {
		t.Fatalf("fixed read-only verification command missing from log: %q", content)
	}
}

func directoryTreeDigest(t *testing.T, root string) string {
	t.Helper()
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(relative)+"\x00"+entry.Type().String()+"\x00")
		if entry.Type().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, _ = hash.Write(content)
		} else if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(hash, target)
		}
		_, _ = io.WriteString(hash, "\x00")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func requestApplicationMultipartStatus(t *testing.T, client *http.Client, url, csrf string, bundle []byte, fields map[string]string, want int) ([]byte, http.Header) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("bundle", "recovery.rcbundle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(bundle); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := writer.WriteField(key, fields[key]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", csrf)
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
		t.Fatalf("POST %s status=%d want=%d body=%s", url, response.StatusCode, want, content)
	}
	return content, response.Header.Clone()
}

func acceptedOperationID(t *testing.T, content []byte) string {
	t.Helper()
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(content, &accepted); err != nil || accepted.OperationID == "" {
		t.Fatalf("decode accepted operation=%s err=%v", content, err)
	}
	return accepted.OperationID
}

func firstSnapshotID(t *testing.T, content []byte) string {
	t.Helper()
	var snapshots []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(content, &snapshots); err != nil || len(snapshots) == 0 || snapshots[0].ID == "" {
		t.Fatalf("decode snapshots=%s err=%v", content, err)
	}
	return snapshots[0].ID
}

func assertSnapshotContainsPayload(t *testing.T, client *http.Client, baseURL, csrf, repositoryID, snapshotID string) {
	t.Helper()
	content, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/repositories/"+repositoryID+"/snapshots/"+snapshotID+"/contents", csrf, nil)
	var page struct {
		Items []struct {
			Name string `json:"name"`
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"items"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(content, &page); err != nil || page.Truncated {
		t.Fatalf("snapshot contents=%s err=%v", content, err)
	}
	for _, node := range page.Items {
		if node.Name == "payload.txt" && node.Type == "file" && strings.HasSuffix(filepath.ToSlash(node.Path), "/album/payload.txt") {
			return
		}
	}
	t.Fatalf("snapshot payload missing: %s", content)
}

func restoreDirectoryThroughAPI(t *testing.T, client *http.Client, baseURL, csrf, repositoryID, snapshotID, target, administratorPassword string, expectedDownloadKiBPerSecond int) {
	t.Helper()
	request := map[string]any{"snapshotId": snapshotID, "target": target, "includes": []string{}}
	preflightBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/repositories/"+repositoryID+"/restore-directory/preflight", csrf, request, http.StatusOK)
	var preflight struct {
		ConfirmationID string `json:"confirmationId"`
		Summary        struct {
			DownloadKiBPerSecond int `json:"downloadKiBPerSecond"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(preflightBody, &preflight); err != nil || preflight.ConfirmationID == "" || preflight.Summary.DownloadKiBPerSecond != expectedDownloadKiBPerSecond {
		t.Fatalf("restore preflight=%s err=%v", preflightBody, err)
	}
	requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/restores/"+preflight.ConfirmationID+"/authorize", csrf, map[string]any{
		"password": administratorPassword,
	}, http.StatusNoContent)
	request["confirmationId"] = preflight.ConfirmationID
	restoreBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/repositories/"+repositoryID+"/restore-directory", csrf, request, http.StatusAccepted)
	waitApplicationOperation(t, client, baseURL, csrf, acceptedOperationID(t, restoreBody))
}

func assertRestoreDownloadLimitCommand(t *testing.T, path string, wanted int) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	flag, value := "--limit-download", fmt.Sprint(wanted)
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		fields := strings.Fields(line)
		if !containsString(fields, "restore") {
			continue
		}
		for index := 0; index+1 < len(fields); index++ {
			if fields[index] == flag && fields[index+1] == value {
				return
			}
		}
	}
	t.Fatalf("restore command did not contain %s %s: %q", flag, value, content)
}

func assertImportedResourcesDisabled(t *testing.T, client *http.Client, baseURL, csrf string) {
	t.Helper()
	for _, resource := range []string{"tasks", "plans"} {
		content, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/"+resource, csrf, nil)
		var items []struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(content, &items); err != nil || len(items) != 1 || items[0].Enabled {
			t.Fatalf("imported %s were not disabled: %s err=%v", resource, content, err)
		}
	}
}

func assertJSONArrayLength(t *testing.T, content []byte, wanted int, label string) {
	t.Helper()
	var items []json.RawMessage
	if err := json.Unmarshal(content, &items); err != nil || len(items) != wanted {
		t.Fatalf("%s length=%d want=%d body=%s err=%v", label, len(items), wanted, content, err)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
