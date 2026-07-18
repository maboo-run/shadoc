package command

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOSExecutorPassesLiteralArgumentsWithoutShell(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	literal := "$(touch " + marker + ")"

	result, err := (OSExecutor{}).Run(context.Background(), Spec{
		Program: "/bin/echo",
		Args:    []string{literal},
	})
	if err != nil {
		t.Fatalf("run echo: %v", err)
	}
	if result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != literal {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("literal argument was executed by a shell: %v", err)
	}
}

func TestOSExecutorStreamsInputAndOutputWithoutLosingBoundedDiagnostics(t *testing.T) {
	var streamed bytes.Buffer
	result, err := (OSExecutor{}).Run(context.Background(), Spec{
		Program: "/bin/cat", Stdin: strings.NewReader("database-stream"), Stdout: &streamed,
	})
	if err != nil {
		t.Fatalf("run cat: %v", err)
	}
	if streamed.String() != "database-stream" || result.Stdout != "database-stream" {
		t.Fatalf("streamed=%q diagnostics=%q", streamed.String(), result.Stdout)
	}
}

func TestOSExecutorCancelsLongRunningProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := (OSExecutor{InterruptTimeout: 100 * time.Millisecond}).Run(ctx, Spec{
		Program: "/bin/sleep",
		Args:    []string{"5"},
	})
	if err == nil {
		t.Fatal("cancelled process must return an error")
	}
	if result.ExitCode == 0 {
		t.Fatalf("cancelled process exit code = %d", result.ExitCode)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("cancel took too long: %s", time.Since(started))
	}
}
func TestOSExecutorUsesEnvironmentAllowlist(t *testing.T) {
	t.Setenv("RESTIC_CONTROL_TEST_SECRET", "must-not-leak")
	result, err := (OSExecutor{}).Run(context.Background(), Spec{Program: "/usr/bin/env", Env: map[string]string{"EXPLICIT_VALUE": "allowed"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Stdout, "RESTIC_CONTROL_TEST_SECRET") || !strings.Contains(result.Stdout, "EXPLICIT_VALUE=allowed") {
		t.Fatalf("unsafe child environment: %s", result.Stdout)
	}
}
