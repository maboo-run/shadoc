package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/maboo-run/shadoc/internal/command"
)

func TestWindowsServiceCompatibilityIncludesCurrentAndLegacyIdentity(t *testing.T) {
	for path, want := range map[string]string{
		`C:\ProgramData\shadoc-agent\shadoc-agent.exe`:                 "shadoc-agent",
		`C:\ProgramData\restic-control-agent\restic-control-agent.exe`: "restic-control-agent",
		`C:\ProgramData\restic-control-agent\shadoc-agent.upgrade.exe`: "restic-control-agent",
		`/ProgramData/restic-control-agent/restic-control-agent.exe`:   "restic-control-agent",
		`/ProgramData/shadoc-agent/shadoc-agent.exe`:                   "shadoc-agent",
	} {
		if got := windowsServiceNameForExecutable(path); got != want {
			t.Fatalf("path=%q service=%q want=%q", path, got, want)
		}
	}
}

func TestShadocAgentEnvironmentTakesPrecedenceOverLegacyName(t *testing.T) {
	t.Setenv("SHADOC_AGENT_SERVICE", "https://shadoc.example:9443")
	t.Setenv("RESTIC_CONTROL_AGENT_SERVICE", "https://legacy.example:9443")
	if got := compatibleEnv("SHADOC_AGENT_SERVICE", "RESTIC_CONTROL_AGENT_SERVICE", "fallback"); got != "https://shadoc.example:9443" {
		t.Fatalf("service=%q", got)
	}
	t.Setenv("SHADOC_AGENT_SERVICE", "")
	if got := compatibleEnv("SHADOC_AGENT_SERVICE", "RESTIC_CONTROL_AGENT_SERVICE", "fallback"); got != "https://legacy.example:9443" {
		t.Fatalf("legacy service=%q", got)
	}
}

func TestFilesystemCapabilitiesAdvertiseScopePreview(t *testing.T) {
	capabilities := filesystemCapabilities("windows")
	for _, expected := range []string{"filesystem-browse", "filesystem-create-directory", "filesystem-scope-preview", "filesystem-restore-target", "path-style:windows"} {
		if !slices.Contains(capabilities, expected) {
			t.Fatalf("capabilities=%v missing %q", capabilities, expected)
		}
	}
}

func TestAgentCapabilitiesAdvertiseManagedResticInstallation(t *testing.T) {
	if capabilities := agentCapabilities("posix", "linux"); !slices.Contains(capabilities, "managed-restic-install-v1") {
		t.Fatalf("capabilities=%v missing managed Restic installation", capabilities)
	}
	for _, goos := range []string{"darwin", "windows"} {
		if capabilities := agentCapabilities("posix", goos); slices.Contains(capabilities, "managed-restic-install-v1") {
			t.Fatalf("capabilities=%v unexpectedly advertise managed Restic installation on %s", capabilities, goos)
		}
	}
}

func TestProbeAgentToolVersionUsesOnlyFixedVersionArguments(t *testing.T) {
	executor := &versionRecordingExecutor{result: command.Result{Stdout: "restic 0.18.0 compiled with go1.24"}}
	version := probeAgentToolVersion(t.Context(), executor, "/opt/restic", []string{"version"})
	if version != "0.18.0" || executor.spec.Program != "/opt/restic" || !slices.Equal(executor.spec.Args, []string{"version"}) {
		t.Fatalf("version=%q spec=%+v", version, executor.spec)
	}
	executor.result = command.Result{Stdout: "rsync  version 3.4.1  protocol version 32"}
	version = probeAgentToolVersion(t.Context(), executor, "/opt/rsync", []string{"--version"})
	if version != "3.4.1" || !slices.Equal(executor.spec.Args, []string{"--version"}) {
		t.Fatalf("version=%q spec=%+v", version, executor.spec)
	}
}

func TestResolveResticProgramPrefersManagedToolWithoutDependingOnServicePATH(t *testing.T) {
	dataDir := t.TempDir()
	managed := filepath.Join(dataDir, "tools", "restic")
	if err := os.MkdirAll(filepath.Dir(managed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("managed-restic"), 0o700); err != nil {
		t.Fatal(err)
	}
	program, found := resolveResticProgram("restic", dataDir, "linux", func(string) (string, error) {
		return "/usr/bin/restic", nil
	}, os.Stat)
	if !found || program != managed {
		t.Fatalf("program=%q found=%v", program, found)
	}

	program, found = resolveResticProgram("/opt/restic", dataDir, "linux", func(value string) (string, error) {
		return value, nil
	}, os.Stat)
	if !found || program != "/opt/restic" {
		t.Fatalf("explicit program=%q found=%v", program, found)
	}
}

func TestResolveResticProgramFallsBackToLegacyManagedToolDuringAgentMigration(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "shadoc-agent")
	legacy := filepath.Join(root, "restic-control-agent", "tools", "restic")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("managed-restic"), 0o700); err != nil {
		t.Fatal(err)
	}
	program, found := resolveResticProgram("restic", dataDir, "linux", func(string) (string, error) {
		return "", errors.New("not found")
	}, os.Stat)
	if !found || program != legacy {
		t.Fatalf("program=%q found=%v", program, found)
	}
}

type versionRecordingExecutor struct {
	result command.Result
	spec   command.Spec
}

func (e *versionRecordingExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	e.spec = spec
	return e.result, nil
}

func TestResolveRsyncProgramFindsCwRsyncOnWindows(t *testing.T) {
	seen := []string{}
	lookPath := func(value string) (string, error) {
		seen = append(seen, value)
		if value == `C:\Program Files\cwRsync\bin\rsync.exe` {
			return value, nil
		}
		return "", errors.New("not found")
	}
	program, runtimeName, found := resolveRsyncProgram("rsync", "windows", func(key string) string {
		if key == "ProgramFiles" {
			return `C:\Program Files`
		}
		return ""
	}, lookPath)
	if !found || program != `C:\Program Files\cwRsync\bin\rsync.exe` || runtimeName != "cwrsync" {
		t.Fatalf("program=%q runtime=%q found=%v candidates=%v", program, runtimeName, found, seen)
	}
}

func TestEnrollmentTokenFileIsRemovedOnlyAfterSuccessfulEnrollment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(path, []byte("one-time-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, cleanup, err := enrollmentToken("", path)
	if err != nil || token != "one-time-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	cleanup(false)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("token removed after failure: %v", err)
	}
	cleanup(true)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("token file still exists: %v", err)
	}
}

func TestEnrollmentTokenFileRejectsOversizedSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(path, make([]byte, 4097), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := enrollmentToken("", path); err == nil {
		t.Fatal("oversized token accepted")
	}
}
