package compat

import (
	"context"
	"errors"
	"testing"

	"github.com/maboo-run/shadoc/internal/command"
)

type fakeExecutor struct {
	results map[string]command.Result
}

func (f fakeExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	result, ok := f.results[spec.Program]
	if !ok {
		return command.Result{ExitCode: -1}, errors.New("not found")
	}
	return result, nil
}

func TestProbeReportsToolCapabilitiesWithoutStoppingDiagnostics(t *testing.T) {
	probe := NewProbe(fakeExecutor{results: map[string]command.Result{
		"/tools/restic":     {ExitCode: 0, Stdout: "restic 0.18.0 compiled with go1.24"},
		"/tools/rsync":      {ExitCode: 0, Stdout: "rsync version 3.4.1 protocol version 32"},
		"/tools/mysqldump":  {ExitCode: 0, Stdout: "mysqldump  Ver 8.4.5 for macos14.7 on arm64"},
		"/tools/mysql":      {ExitCode: 0, Stdout: "mysql  Ver 8.4.5 for macos14.7 on arm64"},
		"/tools/pg_dump":    {ExitCode: 0, Stdout: "pg_dump (PostgreSQL) 17.4"},
		"/tools/pg_restore": {ExitCode: 0, Stdout: "pg_restore (PostgreSQL) 17.4"},
	}})

	report := probe.Tools(context.Background(), ToolPaths{
		Restic: "/tools/restic", Rsync: "/tools/rsync", MySQLDump: "/tools/mysqldump", MySQLRestore: "/tools/mysql",
		PostgresDump: "/tools/pg_dump", PostgresRestore: "/tools/pg_restore",
	})
	if report.Blocked {
		t.Fatalf("compatible tools must not block: %+v", report.Findings)
	}
	if len(report.Findings) != 6 {
		t.Fatalf("finding count = %d", len(report.Findings))
	}

	report = probe.Tools(context.Background(), ToolPaths{Restic: "/missing/restic"})
	if !report.Blocked {
		t.Fatalf("missing restic must block affected capability: %+v", report.Findings)
	}
	if len(report.Findings) != 6 {
		t.Fatalf("diagnostics must report every tool, got %d", len(report.Findings))
	}
}

func TestRsyncBeforeVersionThreeIsBlocked(t *testing.T) {
	probe := NewProbe(fakeExecutor{results: map[string]command.Result{
		"/tools/rsync": {ExitCode: 0, Stdout: "rsync version 2.6.9 protocol version 29"},
	}})
	report := probe.Tools(context.Background(), ToolPaths{Rsync: "/tools/rsync"})
	if !report.Blocked {
		t.Fatalf("old rsync was accepted: %+v", report.Findings)
	}
	found := false
	for _, finding := range report.Findings {
		found = found || finding.Tool == "rsync" && finding.Severity == Blocker
	}
	if !found {
		t.Fatalf("old rsync blocker missing: %+v", report.Findings)
	}
}

func TestSystemProbeReportsWritableDataDirectoryTimezoneAndSpace(t *testing.T) {
	report := System(t.TempDir())
	if report.Blocked {
		t.Fatalf("system unexpectedly blocked: %+v", report.Findings)
	}
	capabilities := map[string]bool{}
	for _, finding := range report.Findings {
		capabilities[finding.Capability] = true
	}
	for _, name := range []string{"system", "data-directory", "timezone", "temporary-space"} {
		if !capabilities[name] {
			t.Fatalf("missing %s finding: %+v", name, report.Findings)
		}
	}
}

func TestResticBeforeCommandBackupSupportIsBlocked(t *testing.T) {
	probe := NewProbe(fakeExecutor{results: map[string]command.Result{
		"/tools/restic": {ExitCode: 0, Stdout: "restic 0.16.5"},
	}})
	report := probe.Tools(context.Background(), ToolPaths{Restic: "/tools/restic"})
	if !report.Blocked || report.Findings[0].Severity != Blocker {
		t.Fatalf("old restic was accepted: %+v", report.Findings)
	}
}
