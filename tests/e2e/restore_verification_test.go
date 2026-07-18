//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const largeSnapshotFileCount = 100_001

func TestProductionBinaryLargeSnapshotPaging(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Fatal("large snapshot restore-proof E2E requires restic on PATH")
	}
	binary := productionBinary(t)
	root := t.TempDir()
	source, verificationPath := createLargeSnapshotSource(t, root)
	dataDir := filepath.Join(root, "control-plane")
	process, client, baseURL, csrf := startConfiguredProductionBinary(t, binary, dataDir, "restore-proof-admin", "restore proof administrator password 2026", nil)
	defer process.Stop(t)
	client.Timeout = 3 * time.Minute

	repositoryBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/repositories", csrf, map[string]any{
		"name": "十万文件恢复证据仓库", "kind": "local", "path": filepath.Join(root, "repository"),
		"password": "large snapshot repository password 2026", "passwordConfirmed": true,
	})
	repositoryID := jsonID(t, repositoryBody)
	initializeBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/repositories/"+repositoryID+"/initialize", csrf, map[string]any{}, http.StatusAccepted)
	waitLongApplicationOperation(t, client, baseURL, csrf, acceptedOperationID(t, initializeBody), 2*time.Minute)

	taskPayload := map[string]any{
		"name": "十万文件目录任务", "kind": "directory", "repositoryId": repositoryID,
		"directory": map[string]any{"path": source, "exclusions": []string{}, "skipIfUnchanged": false},
		"retention": map[string]any{"keepLast": 2}, "resources": map[string]any{"compression": "auto"}, "enabled": false,
	}
	taskBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/tasks", csrf, taskPayload)
	taskID := jsonID(t, taskBody)
	previewBody, _ := requestApplication(t, client, http.MethodPost, baseURL+"/api/tasks/"+taskID+"/preview", csrf, map[string]any{})
	var preview struct {
		PreviewID string         `json:"previewId"`
		Summary   map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(previewBody, &preview); err != nil || preview.PreviewID == "" || preview.Summary["truncated"] != true {
		t.Fatalf("large task scope preview must explicitly report truncation: body=%s err=%v", previewBody, err)
	}
	taskPayload["enabled"] = true
	taskPayload["previewId"] = preview.PreviewID
	requestApplicationStatus(t, client, http.MethodPut, baseURL+"/api/tasks/"+taskID, csrf, taskPayload, http.StatusOK)

	backupBody, _ := requestApplicationStatus(t, client, http.MethodPost, baseURL+"/api/tasks/"+taskID+"/run", csrf, map[string]any{}, http.StatusAccepted)
	waitLongApplicationOperation(t, client, baseURL, csrf, acceptedOperationID(t, backupBody), 5*time.Minute)
	snapshotsBody, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/repositories/"+repositoryID+"/snapshots", csrf, nil)
	snapshotID := firstSnapshotID(t, snapshotsBody)

	first := readSnapshotPage(t, client, baseURL, csrf, repositoryID, snapshotID, "zzzz-proof", "")
	if len(first.Items) != 1 || !first.Truncated || first.NextCursor == "" || !strings.HasSuffix(first.Items[0].Path, "/zzzz-proof") {
		t.Fatalf("first page does not prove explicit truncation beyond %d files: %+v", largeSnapshotFileCount, first)
	}
	badQuery := url.Values{"recursive": {"true"}, "search": {"different-query"}, "limit": {"1"}, "cursor": {first.NextCursor}}
	requestApplicationStatus(t, client, http.MethodGet, fmt.Sprintf("%s/api/repositories/%s/snapshots/%s/contents?%s", baseURL, repositoryID, snapshotID, badQuery.Encode()), csrf, nil, http.StatusUnprocessableEntity)
	second := readSnapshotPage(t, client, baseURL, csrf, repositoryID, snapshotID, "zzzz-proof", first.NextCursor)
	if len(second.Items) != 1 || second.Truncated || second.NextCursor != "" || !strings.HasSuffix(second.Items[0].Path, "/"+verificationPath) {
		t.Fatalf("second page did not locate content after the former fixed boundary: %+v", second)
	}

	recordCheck("large-snapshot-paging", "passed", fmt.Sprintf("files=%d snapshot=%s", largeSnapshotFileCount, snapshotID))
}

type snapshotPage struct {
	Items []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"items"`
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"nextCursor"`
}

func readSnapshotPage(t *testing.T, client *http.Client, baseURL, csrf, repositoryID, snapshotID, search, cursor string) snapshotPage {
	t.Helper()
	query := url.Values{"recursive": {"true"}, "search": {search}, "limit": {"1"}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	body, _ := requestApplication(t, client, http.MethodGet, fmt.Sprintf("%s/api/repositories/%s/snapshots/%s/contents?%s", baseURL, repositoryID, snapshotID, query.Encode()), csrf, nil)
	var page snapshotPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode snapshot page=%s err=%v", body, err)
	}
	return page
}

func createLargeSnapshotSource(t *testing.T, root string) (source, verificationPath string) {
	t.Helper()
	source = filepath.Join(root, "source")
	for directory := 0; directory < 100; directory++ {
		batch := filepath.Join(source, fmt.Sprintf("batch-%03d", directory))
		if err := os.MkdirAll(batch, 0o700); err != nil {
			t.Fatal(err)
		}
		for file := 0; file < 1_000; file++ {
			path := filepath.Join(batch, fmt.Sprintf("entry-%04d", file))
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	verificationPath = "zzzz-proof/payload.bin"
	payload := []byte("restic-control snapshot paging proof\x00\x01\n")
	absolute := filepath.Join(source, filepath.FromSlash(verificationPath))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return source, verificationPath
}

func waitLongApplicationOperation(t *testing.T, client *http.Client, baseURL, csrf, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body, _ := requestApplication(t, client, http.MethodGet, baseURL+"/api/operations/"+id, csrf, nil)
		var operation struct {
			Status       string `json:"status"`
			ErrorSummary string `json:"errorSummary"`
		}
		if json.Unmarshal(body, &operation) == nil {
			switch operation.Status {
			case "success":
				return
			case "failed", "cancelled", "cleanup_required":
				t.Fatalf("operation %s ended as %s: %s", id, operation.Status, operation.ErrorSummary)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("operation %s did not finish within %s", id, timeout)
}
