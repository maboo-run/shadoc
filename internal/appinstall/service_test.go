package appinstall

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type fakeRelease struct {
	binary   []byte
	checksum [32]byte
	err      error
}

func (f fakeRelease) Fetch(context.Context, string) (Artifact, error) {
	return Artifact{Binary: f.binary, SHA256: f.checksum}, f.err
}

type fakeServices struct {
	installs             []string
	restarts, uninstalls int
	installErr           error
}

func (f *fakeServices) Install(path string) error {
	f.installs = append(f.installs, path)
	return f.installErr
}
func (f *fakeServices) Restart() error   { f.restarts++; return nil }
func (f *fakeServices) Uninstall() error { f.uninstalls++; return nil }

type fakeHealth struct {
	errors []error
	calls  int
}

type stageReporter struct{ stages []string }

func (r *stageReporter) Stage(stage string) error {
	r.stages = append(r.stages, stage)
	return nil
}

type contextHealth struct {
	contexts []error
}

func (f *contextHealth) Wait(ctx context.Context, _ string) error {
	f.contexts = append(f.contexts, ctx.Err())
	if len(f.contexts) == 1 {
		return context.DeadlineExceeded
	}
	return ctx.Err()
}

func (f *fakeHealth) Wait(context.Context, string) error {
	f.calls++
	if len(f.errors) >= f.calls {
		return f.errors[f.calls-1]
	}
	return nil
}

func TestUpdateRollsBackWhenNewBinaryFailsHealthCheck(t *testing.T) {
	dir := t.TempDir()
	paths := Paths{Binary: filepath.Join(dir, "bin", "restic-control"), Previous: filepath.Join(dir, "bin", "restic-control.previous"), DataDir: filepath.Join(dir, "data"), HealthURL: "http://127.0.0.1:8585/api/health"}
	if err := os.MkdirAll(filepath.Dir(paths.Binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Binary, []byte("known-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBinary := []byte("broken-new")
	services := &fakeServices{}
	health := &fakeHealth{errors: []error{errors.New("unhealthy"), nil}}
	service := New(fakeRelease{binary: newBinary, checksum: sha256.Sum256(newBinary)}, services, health, paths)
	reporter := &stageReporter{}
	if err := service.UpdateWithReporter(context.Background(), "1.1.0", reporter); err == nil {
		t.Fatal("unhealthy update accepted")
	}
	got, _ := os.ReadFile(paths.Binary)
	if string(got) != "known-good" || services.restarts != 2 || health.calls != 2 {
		t.Fatalf("binary=%q restarts=%d health=%d", got, services.restarts, health.calls)
	}
	want := []string{"downloading_release", "release_verified", "saving_rollback", "replacing_binary", "restarting_service", "verifying_health", "rolling_back", "verifying_rollback", "rollback_verified"}
	if fmt.Sprint(reporter.stages) != fmt.Sprint(want) {
		t.Fatalf("stages=%v", reporter.stages)
	}
}

func TestUpdateRollbackHealthCheckGetsFreshContext(t *testing.T) {
	dir := t.TempDir()
	paths := Paths{Binary: filepath.Join(dir, "restic-control"), Previous: filepath.Join(dir, "restic-control.previous"), HealthURL: "http://health"}
	if err := os.WriteFile(paths.Binary, []byte("known-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBinary := []byte("broken")
	health := &contextHealth{}
	service := New(fakeRelease{binary: newBinary, checksum: sha256.Sum256(newBinary)}, &fakeServices{}, health, paths)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.Update(ctx, "1.1.0"); err == nil {
		t.Fatal("update unexpectedly succeeded")
	}
	if len(health.contexts) != 2 || health.contexts[1] != nil {
		t.Fatalf("health contexts = %v, rollback context must be fresh", health.contexts)
	}
}

func TestUpdateRejectsChecksumWithoutTouchingKnownGoodBinary(t *testing.T) {
	dir := t.TempDir()
	paths := Paths{Binary: filepath.Join(dir, "restic-control"), Previous: filepath.Join(dir, "restic-control.previous")}
	if err := os.WriteFile(paths.Binary, []byte("known-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	service := New(fakeRelease{binary: []byte("tampered"), checksum: sha256.Sum256([]byte("different"))}, &fakeServices{}, &fakeHealth{}, paths)
	if err := service.Update(context.Background(), "1.1.0"); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	got, _ := os.ReadFile(paths.Binary)
	if string(got) != "known-good" {
		t.Fatalf("binary=%q", got)
	}
}

func TestInstallCopiesCurrentBinaryAndRegistersStablePath(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "download", "restic-control")
	if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths := Paths{Binary: filepath.Join(dir, "installed", "restic-control"), DataDir: filepath.Join(dir, "data")}
	services := &fakeServices{}
	service := New(nil, services, &fakeHealth{}, paths)
	if err := service.InstallCurrent(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(paths.Binary)
	if string(got) != "current" || len(services.installs) != 1 || services.installs[0] != paths.Binary {
		t.Fatalf("binary=%q installs=%v", got, services.installs)
	}
}

func TestInstallCopiesSiblingAgentArtifactsBesideStableBinary(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "download")
	current := filepath.Join(sourceDir, "restic-control")
	agentName := "shadoc-agent-linux-amd64"
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte("control"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, agentName), []byte("linux-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths := Paths{Binary: filepath.Join(dir, "installed", "restic-control"), Companions: []string{agentName}}
	service := New(nil, &fakeServices{}, nil, paths)
	if err := service.InstallCurrent(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(filepath.Dir(paths.Binary), agentName))
	if err != nil || string(got) != "linux-agent" {
		t.Fatalf("installed Agent artifact=%q err=%v", got, err)
	}
}

func TestInstallRestoresSiblingAgentArtifactWhenRegistrationFails(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "download")
	targetDir := filepath.Join(dir, "installed")
	agentName := "shadoc-agent-linux-amd64"
	for _, path := range []string{sourceDir, targetDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	current := filepath.Join(sourceDir, "restic-control")
	target := filepath.Join(targetDir, "restic-control")
	if err := os.WriteFile(current, []byte("new-control"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old-control"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, agentName), []byte("new-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	installedAgent := filepath.Join(targetDir, agentName)
	if err := os.WriteFile(installedAgent, []byte("old-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	service := New(nil, &fakeServices{installErr: errors.New("registration failed")}, nil, Paths{Binary: target, Companions: []string{agentName}})
	if err := service.InstallCurrent(context.Background(), current); err == nil {
		t.Fatal("registration failure accepted")
	}
	control, controlErr := os.ReadFile(target)
	agent, agentErr := os.ReadFile(installedAgent)
	if controlErr != nil || agentErr != nil || string(control) != "old-control" || string(agent) != "old-agent" {
		t.Fatalf("control=%q agent=%q controlErr=%v agentErr=%v", control, agent, controlErr, agentErr)
	}
}

func TestInstallRestoresExistingBinaryWhenRegistrationFails(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "download")
	target := filepath.Join(dir, "installed")
	if err := os.WriteFile(current, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("known-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	services := &fakeServices{installErr: errors.New("registration failed")}
	service := New(nil, services, nil, Paths{Binary: target})
	if err := service.InstallCurrent(context.Background(), current); err == nil {
		t.Fatal("registration failure accepted")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "known-good" || services.restarts != 1 {
		t.Fatalf("binary=%q restarts=%d err=%v", got, services.restarts, err)
	}
}

func TestInstallRemovesNewBinaryWhenRegistrationFails(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "download")
	target := filepath.Join(dir, "installed")
	if err := os.WriteFile(current, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	services := &fakeServices{installErr: errors.New("registration failed")}
	service := New(nil, services, nil, Paths{Binary: target})
	if err := service.InstallCurrent(context.Background(), current); err == nil {
		t.Fatal("registration failure accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("new binary left behind: %v", err)
	}
	if services.uninstalls != 1 {
		t.Fatalf("partial service registration was not cleaned up: uninstalls=%d", services.uninstalls)
	}
}

func TestInstallRestoresExistingBinaryWhenHealthCheckFails(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "download")
	target := filepath.Join(dir, "installed")
	if err := os.WriteFile(current, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("known-good"), 0o755); err != nil {
		t.Fatal(err)
	}
	services := &fakeServices{}
	service := New(nil, services, &fakeHealth{errors: []error{errors.New("unhealthy")}}, Paths{Binary: target, HealthURL: "http://health"})
	if err := service.InstallCurrent(context.Background(), current); err == nil {
		t.Fatal("health failure accepted")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "known-good" || services.restarts != 1 {
		t.Fatalf("binary=%q restarts=%d err=%v", got, services.restarts, err)
	}
}

func TestUninstallRemovesProgramButPreservesDataByDefault(t *testing.T) {
	dir := t.TempDir()
	agentName := "shadoc-agent-linux-amd64"
	paths := Paths{
		Binary:     filepath.Join(dir, "bin", "restic-control"),
		Previous:   filepath.Join(dir, "bin", "restic-control.previous"),
		DataDir:    filepath.Join(dir, "data"),
		Companions: []string{agentName},
	}
	installedAgent := filepath.Join(filepath.Dir(paths.Binary), agentName)
	for _, path := range []string{paths.Binary, paths.Previous, installedAgent, filepath.Join(paths.DataDir, "restic-control.db")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	services := &fakeServices{}
	service := New(nil, services, nil, paths)
	if err := service.Uninstall(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.Binary); !os.IsNotExist(err) {
		t.Fatalf("binary still present: %v", err)
	}
	if _, err := os.Stat(installedAgent); !os.IsNotExist(err) {
		t.Fatalf("Agent artifact still present: %v", err)
	}
	if _, err := os.Stat(paths.DataDir); err != nil {
		t.Fatalf("data removed by default: %v", err)
	}
	if services.uninstalls != 1 {
		t.Fatalf("uninstalls=%d", services.uninstalls)
	}
}

func TestUninstallCanExplicitlyRemoveData(t *testing.T) {
	dir := t.TempDir()
	paths := Paths{Binary: filepath.Join(dir, "bin", "restic-control"), DataDir: filepath.Join(dir, "data")}
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	service := New(nil, &fakeServices{}, nil, paths)
	if err := service.Uninstall(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.DataDir); !os.IsNotExist(err) {
		t.Fatalf("data still present: %v", err)
	}
}

func TestUninstallRejectsMissingStableBinaryPathBeforeRemovingCompanions(t *testing.T) {
	services := &fakeServices{}
	service := New(nil, services, nil, Paths{Companions: []string{"shadoc-agent-linux-amd64"}})
	if err := service.Uninstall(false); err == nil {
		t.Fatal("missing stable binary path accepted")
	}
	if services.uninstalls != 0 {
		t.Fatalf("service uninstalled before validating paths: %d", services.uninstalls)
	}
}
