package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/compat"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

func TestCompatibilityReprobeDiscoversNewlyInstalledLocalTool(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	directory := t.TempDir()
	dumpPath := executableTool(t, directory, "mysqldump", "mysqldump  Ver 10.19 Distrib 10.11.18")
	t.Setenv("PATH", directory)
	srv.paths.MySQLDump = ""
	srv.compatibilityCache = compat.Report{
		Blocked: true,
		Findings: []compat.Finding{{
			Capability: "mysql-backup", Tool: "mysqldump",
			Severity: compat.Blocker, Message: "未配置 mysqldump 的绝对路径",
		}},
	}
	srv.compatibilityCached = true

	response := requestJSON(t, srv, "POST", "/api/compatibility/reprobe", map[string]any{}, cookie)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Findings []compat.Finding `json:"findings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, finding := range result.Findings {
		if finding.Capability == "mysql-backup" {
			if finding.Path != dumpPath || finding.Severity != compat.Info {
				t.Fatalf("mysql finding=%+v", finding)
			}
			return
		}
	}
	t.Fatalf("mysql-backup finding missing: %+v", result.Findings)
}

func TestCompatibilityToolPathsPersistValidatedManualOverrides(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	directory := t.TempDir()
	dumpPath := executableTool(t, directory, "mysqldump", "mysqldump  Ver 10.19 Distrib 10.11.18")
	mysqlPath := executableTool(t, directory, "mysql", "mysql  Ver 15.1 Distrib 10.11.18")

	response := requestJSON(t, srv, "POST", "/api/compatibility/tool-paths", map[string]any{
		"mysqlDump": dumpPath, "mysqlRestore": mysqlPath,
	}, cookie)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"configuredPaths"`) ||
		!strings.Contains(response.Body.String(), dumpPath) ||
		!strings.Contains(response.Body.String(), mysqlPath) {
		t.Fatalf("body=%s", response.Body.String())
	}
	value, err := srv.store.(*store.Store).Metadata(t.Context(), "compatibility.tool-path-overrides")
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		MySQLDump    string `json:"mysqlDump"`
		MySQLRestore string `json:"mysqlRestore"`
	}
	if json.Unmarshal([]byte(value), &saved) != nil || saved.MySQLDump != dumpPath || saved.MySQLRestore != mysqlPath {
		t.Fatalf("saved=%+v raw=%s", saved, value)
	}
}

func TestCompatibilityToolPathsRejectUnexpectedExecutableName(t *testing.T) {
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	path := executableTool(t, t.TempDir(), "not-mysqldump", "mysqldump  Ver 10.19")

	response := requestJSON(t, srv, "POST", "/api/compatibility/tool-paths", map[string]any{
		"mysqlDump": path,
	}, cookie)
	if response.Code != 400 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCompatibilityToolPathsRejectNonExecutableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX execute bits are not used on Windows")
	}
	srv := newResourceTestServer(t)
	cookie := setupSession(t, srv)
	path := filepath.Join(t.TempDir(), "mysqldump")
	if err := os.WriteFile(path, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, srv, "POST", "/api/compatibility/tool-paths", map[string]any{
		"mysqlDump": path,
	}, cookie)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "没有执行权限") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCompatibilityMySQLPathsBecomeAutomaticDatabaseDefaults(t *testing.T) {
	srv := newResourceTestServer(t)
	srv.toolPathOverrides = compat.ToolPathOverrides{
		MySQLDump:    "/opt/mysql/bin/mysqldump",
		MySQLRestore: "/opt/mysql/bin/mysql",
	}

	backup := srv.resolveDatabaseToolPaths(domain.DatabaseConnection{
		Engine: domain.MySQL, Purpose: domain.BackupConnection,
	})
	if backup["dump"] != "/opt/mysql/bin/mysqldump" || backup["admin"] != "/opt/mysql/bin/mysql" {
		t.Fatalf("backup paths=%+v", backup)
	}
	restore := srv.resolveDatabaseToolPaths(domain.DatabaseConnection{
		Engine: domain.MySQL, Purpose: domain.RestoreConnection,
	})
	if restore["restore"] != "/opt/mysql/bin/mysql" || restore["admin"] != "/opt/mysql/bin/mysql" {
		t.Fatalf("restore paths=%+v", restore)
	}
}

func executableTool(t *testing.T, directory, name, version string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test helper requires a POSIX executable")
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho '"+version+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
