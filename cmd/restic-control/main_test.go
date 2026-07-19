package main

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
	"github.com/maboo-run/shadoc/internal/command"
)

func TestRecoverInterruptedStateRunsBeforeBackgroundServices(t *testing.T) {
	at := time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC)
	recovery := &recoveryStoreFake{}
	if err := recoverInterruptedState(context.Background(), recovery, at); err != nil {
		t.Fatal(err)
	}
	if len(recovery.calls) != 5 || recovery.calls[0] != "schedule" || recovery.calls[1] != "runs" || recovery.calls[2] != "restore-verifications" || recovery.calls[3] != "protection-drafts" || recovery.calls[4] != "agent-drains" || !recovery.at.Equal(at) {
		t.Fatalf("calls=%v at=%s", recovery.calls, recovery.at)
	}

	recovery = &recoveryStoreFake{scheduleErr: errors.New("schedule recovery failed")}
	if err := recoverInterruptedState(context.Background(), recovery, at); err == nil || len(recovery.calls) != 1 {
		t.Fatalf("err=%v calls=%v", err, recovery.calls)
	}
}

func TestAgentArtifactDirectoryUsesControlServiceDirectory(t *testing.T) {
	dir, err := agentArtifactDirectory(func() (string, error) {
		return "/srv/shadoc/dist/shadoc", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/srv/shadoc/dist" {
		t.Fatalf("artifact directory = %q", dir)
	}
}

func TestSelectLocalRsyncProgramSkipsIncompatibleSystemVersion(t *testing.T) {
	executor := &localRsyncVersionExecutor{versions: map[string]string{
		"/usr/bin/rsync":          "rsync version 2.6.9 protocol version 29",
		"/opt/homebrew/bin/rsync": "rsync version 3.4.1 protocol version 32",
	}}
	program, err := selectLocalRsyncProgram(context.Background(), executor, "darwin", func(string) (string, error) {
		return "/usr/bin/rsync", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if program != "/opt/homebrew/bin/rsync" {
		t.Fatalf("program=%q probes=%v", program, executor.programs)
	}
}

func TestSelectLocalResticProgramUsesKnownHomebrewPathWhenServicePATHMissesIt(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "restic")
	if err := os.WriteFile(executable, []byte("restic"), 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	stat := func(path string) (os.FileInfo, error) {
		if path == "/opt/homebrew/bin/restic" {
			return info, nil
		}
		return os.Stat(path)
	}
	program := selectLocalResticProgram(filepath.Join(t.TempDir(), "bin", "restic"), "darwin", func(string) (string, error) {
		return "", errors.New("restic is not on the service PATH")
	}, stat)
	if program != "/opt/homebrew/bin/restic" {
		t.Fatalf("program=%q", program)
	}
}

func TestRunningFromManagedPathUsesFileIdentity(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "restic-control")
	alias := filepath.Join(dir, "restic-control-alias")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(executable, alias); err != nil {
		t.Fatal(err)
	}
	if !runningFromManagedPath(executable, alias) {
		t.Fatal("same installed binary was not recognized")
	}
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if runningFromManagedPath(executable, other) || runningFromManagedPath("relative", executable) {
		t.Fatal("unmanaged binary was recognized as managed")
	}
}

func TestManagedApplicationBinaryPrefersShadocAndFallsBackToLegacyInstall(t *testing.T) {
	dataDir := t.TempDir()
	legacy := filepath.Join(dataDir, "app", "restic-control")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := existingManagedApplicationBinary(dataDir); got != legacy {
		t.Fatalf("legacy managed binary=%q", got)
	}
	current := filepath.Join(dataDir, "app", "shadoc")
	if err := os.WriteFile(current, []byte("current"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := existingManagedApplicationBinary(dataDir); got != current {
		t.Fatalf("current managed binary=%q", got)
	}
	if got := newManagedApplicationBinary(dataDir); got != current {
		t.Fatalf("new managed binary=%q", got)
	}
}

func TestAgentDeploymentResolverFindsArtifactBesideControlService(t *testing.T) {
	dir := t.TempDir()
	control := filepath.Join(dir, "shadoc")
	artifactPath := filepath.Join(dir, "shadoc-agent-linux-amd64")
	elf := make([]byte, 64)
	copy(elf, []byte{0x7f, 'E', 'L', 'F'})
	elf[5] = 1
	binary.LittleEndian.PutUint16(elf[18:20], 62)
	if err := os.WriteFile(artifactPath, elf, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactDir, err := agentArtifactDirectory(func() (string, error) { return control, nil })
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := (agentdeploy.ArtifactResolver{Dir: artifactDir}).Resolve(agentdeploy.Platform{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Path != artifactPath {
		t.Fatalf("artifact path = %q", artifact.Path)
	}
}

func TestCapacityMonitorIsProcessOwnedAndWaitedDuringShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	monitor := &capacityMonitorFake{started: make(chan struct{}), stopped: make(chan struct{})}
	var wg sync.WaitGroup
	startCapacityMonitor(&wg, ctx, monitor, 30*time.Second)
	select {
	case <-monitor.started:
	case <-time.After(time.Second):
		t.Fatal("capacity monitor was not started")
	}
	cancel()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not wait for capacity monitor")
	}
	if monitor.interval != 30*time.Second {
		t.Fatalf("interval=%s", monitor.interval)
	}
}

type recoveryStoreFake struct {
	calls       []string
	at          time.Time
	scheduleErr error
	runErr      error
	restoreErr  error
}

type localRsyncVersionExecutor struct {
	versions map[string]string
	programs []string
}

func (e *localRsyncVersionExecutor) Run(_ context.Context, spec command.Spec) (command.Result, error) {
	e.programs = append(e.programs, spec.Program)
	version, ok := e.versions[spec.Program]
	if !ok {
		return command.Result{ExitCode: -1}, errors.New("program unavailable")
	}
	return command.Result{Stdout: version}, nil
}

type capacityMonitorFake struct {
	started  chan struct{}
	stopped  chan struct{}
	interval time.Duration
}

func (m *capacityMonitorFake) Run(ctx context.Context, interval time.Duration) {
	m.interval = interval
	close(m.started)
	<-ctx.Done()
	close(m.stopped)
}

func (s *recoveryStoreFake) RecoverInterruptedScheduleOccurrences(_ context.Context, at time.Time) (int, error) {
	s.calls = append(s.calls, "schedule")
	s.at = at
	return 0, s.scheduleErr
}

func (s *recoveryStoreFake) RecoverInterruptedRuns(_ context.Context, at time.Time) (int, error) {
	s.calls = append(s.calls, "runs")
	s.at = at
	return 0, s.runErr
}

func (s *recoveryStoreFake) RecoverInterruptedRestoreVerifications(_ context.Context, at time.Time) (int, error) {
	s.calls = append(s.calls, "restore-verifications")
	s.at = at
	return 0, s.restoreErr
}

func (s *recoveryStoreFake) RecoverInterruptedProtectionDrafts(_ context.Context, at time.Time) (int, error) {
	s.calls = append(s.calls, "protection-drafts")
	s.at = at
	return 0, nil
}

func (s *recoveryStoreFake) RecoverInterruptedAgentDrains(context.Context) (int, error) {
	s.calls = append(s.calls, "agent-drains")
	return 0, nil
}
