//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type releaseCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

var reportState struct {
	sync.Mutex
	Checks []releaseCheck `json:"checks"`
}

func recordCheck(name, status, detail string) {
	reportState.Lock()
	reportState.Checks = append(reportState.Checks, releaseCheck{Name: name, Status: status, Detail: detail})
	reportState.Unlock()
}

func requireReleaseConfiguration(t *testing.T, name string, values ...string) bool {
	t.Helper()
	for _, value := range values {
		if value == "" {
			detail := "required environment is not configured"
			recordCheck(name, "missing", detail)
			if os.Getenv("SHADOC_RELEASE_VERIFY") == "1" || os.Getenv("RESTIC_CONTROL_RELEASE_VERIFY") == "1" {
				t.Fatalf("%s: %s", name, detail)
			}
			t.Skipf("%s: %s", name, detail)
			return false
		}
	}
	return true
}

func TestMain(m *testing.M) {
	code := m.Run()
	path := os.Getenv("SHADOC_E2E_REPORT")
	if path == "" {
		path = os.Getenv("RESTIC_CONTROL_E2E_REPORT")
	}
	if path != "" {
		if err := writeReport(path); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "write E2E report: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func writeReport(path string) error {
	reportState.Lock()
	content, err := json.MarshalIndent(reportState, "", "  ")
	reportState.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o600)
}

func TestReportWriteFailureIsReturned(t *testing.T) {
	if err := writeReport(filepath.Join(t.TempDir(), "missing", "report.json")); err != nil {
		t.Fatalf("valid report path failed: %v", err)
	}
	if err := writeReport(filepath.Join("/dev/null", "report.json")); err == nil {
		t.Fatal("invalid report path succeeded")
	}
}
