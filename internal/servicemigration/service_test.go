package servicemigration

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestMigrationStartsRootServiceThenDeletesTheUserInstance(t *testing.T) {
	source := &sourceFake{installation: testInstallation()}
	target := &targetFake{}
	mover := &moverFake{}
	markers := &markerStoreFake{}
	service := New(source, target, mover, markers, source.installation.Executable, "/var/lib/shadoc", "/var/lib/.shadoc-root-staging")

	result, err := service.Migrate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.SourceRemoved || result.TargetDataDir != "/var/lib/shadoc" {
		t.Fatalf("result=%+v", result)
	}
	if source.disableCalls != 1 || source.removeCalls != 1 || source.restoreCalls != 0 {
		t.Fatalf("source disable=%d remove=%d restore=%d", source.disableCalls, source.removeCalls, source.restoreCalls)
	}
	if target.installCalls != 1 || target.uninstallCalls != 0 || target.healthyCalls != 1 {
		t.Fatalf("target install=%d uninstall=%d healthy=%d", target.installCalls, target.uninstallCalls, target.healthyCalls)
	}
	if !reflect.DeepEqual(mover.removed, []string{"/home/backup/.config/shadoc"}) {
		t.Fatalf("removed paths=%v", mover.removed)
	}
	if markers.present {
		t.Fatalf("completed marker retained: %+v", markers.marker)
	}
}

func TestMigrationFailureBeforeHealthRestoresTheUserInstance(t *testing.T) {
	source := &sourceFake{installation: testInstallation()}
	target := &targetFake{installErr: errors.New("system service failed")}
	mover := &moverFake{}
	markers := &markerStoreFake{}
	service := New(source, target, mover, markers, source.installation.Executable, "/var/lib/shadoc", "/var/lib/.shadoc-root-staging")

	if _, err := service.Migrate(context.Background()); err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	if source.restoreCalls != 1 || source.removeCalls != 0 {
		t.Fatalf("source restore=%d remove=%d", source.restoreCalls, source.removeCalls)
	}
	if target.uninstallCalls != 1 {
		t.Fatalf("target uninstall=%d", target.uninstallCalls)
	}
	if !reflect.DeepEqual(mover.removed, []string{"/var/lib/shadoc", "/var/lib/.shadoc-root-staging"}) {
		t.Fatalf("rollback removed paths=%v", mover.removed)
	}
	if markers.present {
		t.Fatalf("rollback marker retained: %+v", markers.marker)
	}
}

func TestMigrationPreflightFailureDoesNotTouchTheUserInstance(t *testing.T) {
	source := &sourceFake{installation: testInstallation()}
	target := &targetFake{prepareErr: errors.New("system unit already exists")}
	markers := &markerStoreFake{}
	service := New(source, target, &moverFake{}, markers, source.installation.Executable, "/var/lib/shadoc", "/var/lib/.shadoc-root-staging")

	if _, err := service.Migrate(context.Background()); err == nil {
		t.Fatal("preflight failure was hidden")
	}
	if source.disableCalls != 0 || source.restoreCalls != 0 || source.removeCalls != 0 || markers.present {
		t.Fatalf("preflight mutated source: %+v marker=%t", source, markers.present)
	}
}

func TestMigrationRollbackRestoresTheUserInstanceWithAFreshContext(t *testing.T) {
	source := &sourceFake{installation: testInstallation()}
	target := &targetFake{installErr: errors.New("system service failed")}
	service := New(source, target, &moverFake{}, &markerStoreFake{}, source.installation.Executable, "/var/lib/shadoc", "/var/lib/.shadoc-root-staging")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.Migrate(ctx); err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	if source.restoreCalls != 1 || source.restoreSawCanceled {
		t.Fatalf("restore calls=%d saw canceled context=%t", source.restoreCalls, source.restoreSawCanceled)
	}
}

func TestCleanupFailureAfterHealthNeverRollsBackRootData(t *testing.T) {
	source := &sourceFake{installation: testInstallation()}
	target := &targetFake{}
	mover := &moverFake{removeErrors: map[string]error{"/home/backup/.config/shadoc": errors.New("source cleanup failed")}}
	markers := &markerStoreFake{}
	service := New(source, target, mover, markers, source.installation.Executable, "/var/lib/shadoc", "/var/lib/.shadoc-root-staging")

	if _, err := service.Migrate(context.Background()); err == nil {
		t.Fatal("cleanup failure was hidden")
	}
	if source.restoreCalls != 0 || target.uninstallCalls != 0 {
		t.Fatalf("committed migration rolled back: source restore=%d target uninstall=%d", source.restoreCalls, target.uninstallCalls)
	}
	if !markers.present || markers.marker.Stage != stageHealthVerified {
		t.Fatalf("cleanup retry marker=%+v present=%t", markers.marker, markers.present)
	}

	delete(mover.removeErrors, "/home/backup/.config/shadoc")
	result, err := service.Migrate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.SourceRemoved || markers.present {
		t.Fatalf("resumed result=%+v marker present=%t", result, markers.present)
	}
	if target.installCalls != 1 || source.disableCalls != 1 {
		t.Fatalf("migration restarted instead of resuming: target installs=%d source disables=%d", target.installCalls, source.disableCalls)
	}
}

func testInstallation() Installation {
	return Installation{
		Username:       "backup",
		UID:            1001,
		Home:           "/home/backup",
		Identity:       "shadoc",
		DataDir:        "/home/backup/.config/shadoc",
		Listen:         "127.0.0.1:8585",
		Executable:     "/home/backup/.config/shadoc/app/shadoc",
		UnitDefinition: "[Service]\nExecStart=\"/home/backup/.config/shadoc/app/shadoc\"\n",
		UnitMode:       0o600,
		WasEnabled:     true,
		WasActive:      true,
	}
}

type sourceFake struct {
	installation       Installation
	inspectErr         error
	disableErr         error
	restoreErr         error
	removeErr          error
	inspectCalls       int
	disableCalls       int
	restoreCalls       int
	removeCalls        int
	restoreSawCanceled bool
}

func (s *sourceFake) Inspect(context.Context) (Installation, error) {
	s.inspectCalls++
	return s.installation, s.inspectErr
}

func (s *sourceFake) Disable(context.Context, Installation) error {
	s.disableCalls++
	return s.disableErr
}

func (s *sourceFake) Restore(ctx context.Context, _ Installation) error {
	s.restoreCalls++
	s.restoreSawCanceled = ctx.Err() != nil
	return s.restoreErr
}

func (s *sourceFake) Remove(context.Context, Installation) error {
	s.removeCalls++
	return s.removeErr
}

type targetFake struct {
	prepareErr     error
	installErr     error
	healthyErr     error
	uninstallErr   error
	prepareCalls   int
	installCalls   int
	healthyCalls   int
	uninstallCalls int
}

func (t *targetFake) Prepare() error {
	t.prepareCalls++
	return t.prepareErr
}

func (t *targetFake) Install(string, []string) error {
	t.installCalls++
	return t.installErr
}

func (t *targetFake) Healthy(context.Context, string) error {
	t.healthyCalls++
	return t.healthyErr
}

func (t *targetFake) Uninstall() error {
	t.uninstallCalls++
	return t.uninstallErr
}

type moverFake struct {
	stageErr     error
	commitErr    error
	removeErrors map[string]error
	staged       bool
	committed    bool
	removed      []string
}

func (m *moverFake) Stage(string, string, string, int) error {
	m.staged = true
	return m.stageErr
}

func (m *moverFake) Commit(string, string) error {
	m.committed = true
	return m.commitErr
}

func (m *moverFake) Remove(path string, _ int) error {
	m.removed = append(m.removed, path)
	return m.removeErrors[path]
}

func (m *moverFake) Exists(string) (bool, error) { return false, nil }

type markerStoreFake struct {
	marker  Marker
	present bool
}

func (m *markerStoreFake) Load() (Marker, bool, error) {
	return m.marker, m.present, nil
}

func (m *markerStoreFake) Save(marker Marker) error {
	m.marker, m.present = marker, true
	return nil
}

func (m *markerStoreFake) Remove() error {
	m.marker, m.present = Marker{}, false
	return nil
}
