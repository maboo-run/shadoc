package servicemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLinuxUserSourceInspectsOnlyGeneratedServiceDefinition(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".config", "shadoc")
	executable := filepath.Join(dataDir, "app", "shadoc")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	definition := generatedUserUnit(executable, dataDir, "0.0.0.0:9090")
	unitPath := filepath.Join(home, ".config", "systemd", "user", "shadoc.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceUID := testSourceUID(t, dataDir, executable, unitPath)
	runner := &nativeRunnerFake{responses: map[string]nativeResponse{
		"is-enabled": {output: "enabled\n"},
		"is-active":  {output: "active\n"},
	}}
	source, err := NewLinuxUserSource("backup", sourceUID, home, runner)
	if err != nil {
		t.Fatal(err)
	}
	source.health = func(context.Context, string) error { return nil }
	got, err := source.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Identity != "shadoc" || got.DataDir != dataDir || got.Executable != executable ||
		got.Listen != "0.0.0.0:9090" || !got.WasEnabled || !got.WasActive ||
		got.UnitDefinition != definition || got.UnitMode != 0o600 {
		t.Fatalf("installation=%+v", got)
	}
	for _, call := range runner.calls {
		if call[0] != "systemctl" || !strings.Contains(strings.Join(call, " "), "--machine=backup@.host") {
			t.Fatalf("unsafe inspection command=%v", call)
		}
	}
}

func TestGeneratedExecStartParserRejectsShellsAndUnsupportedArguments(t *testing.T) {
	for _, definition := range []string{
		"[Service]\nExecStart=/bin/sh -c backup\n",
		generatedUserUnitWithExec(`"/opt/shadoc" "serve" "--data-dir" "/safe/data" "--unknown" "value"`),
		generatedUserUnitWithExec("\"/opt/shadoc\"\nExecStart=\"/opt/other\""),
		generatedUserUnitWithExec(`"/opt/%%n/shadoc" "serve" "--data-dir" "/safe/data" "--service-scope" "system"`),
		generatedUserUnitWithExec(`"/opt/shadoc"`) + "Environment=ATTACK=1\n",
	} {
		if _, err := parseGeneratedUserUnit(definition, "/home/backup"); err == nil {
			t.Fatalf("unsafe unit accepted:\n%s", definition)
		}
	}
}

func TestLinuxUserSourceRejectsSystemdDropIns(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".config", "shadoc")
	executable := filepath.Join(dataDir, "app", "shadoc")
	unitPath := filepath.Join(home, ".config", "systemd", "user", "shadoc.service")
	dropIn := unitPath + ".d"
	for _, directory := range []string{filepath.Dir(executable), filepath.Dir(unitPath), dropIn} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte(generatedUserUnit(executable, dataDir, "127.0.0.1:8585")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropIn, "override.conf"), []byte("[Service]\nEnvironment=ATTACK=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceUID := testSourceUID(t, dataDir, executable, unitPath)
	source, err := NewLinuxUserSource("backup", sourceUID, home, &nativeRunnerFake{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Inspect(context.Background()); err == nil || !strings.Contains(err.Error(), "drop-in") {
		t.Fatalf("drop-in error=%v", err)
	}
}

func TestLegacyServiceWithoutArgumentsKeepsItsLegacyDefaultDataDirectory(t *testing.T) {
	home := t.TempDir()
	for _, directory := range []string{"shadoc", "restic-control"} {
		if err := os.MkdirAll(filepath.Join(home, ".config", directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(home, ".config", "restic-control", "app", "restic-control")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(home, ".config", "systemd", "user", "restic-control.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	definition := generatedLegacyUserUnit(executable)
	if err := os.WriteFile(unitPath, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceUID := testSourceUID(t, filepath.Join(home, ".config", "restic-control"), executable, unitPath)
	source, err := NewLinuxUserSource("backup", sourceUID, home, &nativeRunnerFake{responses: map[string]nativeResponse{
		"is-enabled": {output: "enabled\n"}, "is-active": {output: "active\n"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	source.health = func(context.Context, string) error { return nil }
	installation, err := source.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if installation.DataDir != filepath.Join(home, ".config", "restic-control") {
		t.Fatalf("legacy data directory=%q", installation.DataDir)
	}
}

func TestLinuxUserSourceDisablesRestoresAndRemovesTheExactUnit(t *testing.T) {
	home := t.TempDir()
	unitPath := filepath.Join(home, ".config", "systemd", "user", "shadoc.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	definition := generatedUserUnit("/opt/shadoc", filepath.Join(home, ".config", "shadoc"), "127.0.0.1:8585")
	if err := os.WriteFile(unitPath, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceUID := testSourceUID(t, unitPath)
	runner := &nativeRunnerFake{sequences: map[string][]nativeResponse{
		"is-active": {
			{output: "inactive\n", err: errors.New("exit 3")},
			{output: "active\n"},
		},
	}}
	source, err := NewLinuxUserSource("backup", sourceUID, home, runner)
	if err != nil {
		t.Fatal(err)
	}
	source.health = func(context.Context, string) error { return nil }
	installation := Installation{
		Username: "backup", UID: sourceUID, Home: home, Identity: "shadoc",
		UnitDefinition: definition, UnitMode: 0o600, WasEnabled: true, WasActive: true,
	}
	if err := source.Disable(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	if err := source.Restore(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	if err := source.Remove(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still exists: %v", err)
	}
	wantActions := []string{"disable", "is-active", "daemon-reload", "enable", "start", "is-active", "disable", "daemon-reload"}
	var actions []string
	for _, call := range runner.calls {
		for _, candidate := range wantActions {
			if containsNativeArgument(call, candidate) {
				actions = append(actions, candidate)
				break
			}
		}
	}
	if !reflect.DeepEqual(actions, wantActions) {
		t.Fatalf("actions=%v calls=%v", actions, runner.calls)
	}
}

func generatedUserUnit(executable, dataDir, listen string) string {
	return generatedUserUnitWithExec(
		quotedSystemdToken(executable) + " \"serve\" \"--listen\" " + quotedSystemdToken(listen) +
			" \"--data-dir\" " + quotedSystemdToken(dataDir),
	)
}

func generatedUserUnitWithExec(execStart string) string {
	return "[Unit]\nDescription=Shadoc backup service\nAfter=network-online.target\n\n[Service]\nExecStart=" +
		execStart +
		"\nRestart=on-failure\nRestartSec=5\nNoNewPrivileges=true\nPrivateTmp=true\n\n[Install]\nWantedBy=default.target\n"
}

func generatedLegacyUserUnit(executable string) string {
	return "[Unit]\nDescription=restic-control backup service\nAfter=network-online.target\n\n[Service]\nExecStart=" +
		quotedSystemdToken(executable) +
		"\nRestart=on-failure\nRestartSec=5\nNoNewPrivileges=true\nPrivateTmp=true\n\n[Install]\nWantedBy=default.target\n"
}

func quotedSystemdToken(value string) string {
	value = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(value)
	return `"` + value + `"`
}

type nativeResponse struct {
	output string
	err    error
}

type nativeRunnerFake struct {
	responses map[string]nativeResponse
	sequences map[string][]nativeResponse
	calls     [][]string
}

func (r *nativeRunnerFake) CombinedOutput(_ context.Context, program string, arguments ...string) ([]byte, error) {
	call := append([]string{program}, arguments...)
	r.calls = append(r.calls, call)
	for action, sequence := range r.sequences {
		if !containsNativeArgument(call, action) || len(sequence) == 0 {
			continue
		}
		response := sequence[0]
		r.sequences[action] = sequence[1:]
		return []byte(response.output), response.err
	}
	for action, response := range r.responses {
		if containsNativeArgument(call, action) {
			return []byte(response.output), response.err
		}
	}
	return nil, nil
}

func containsNativeArgument(arguments []string, value string) bool {
	for _, argument := range arguments {
		if argument == value {
			return true
		}
	}
	return false
}

func testSourceUID(t *testing.T, paths ...string) int {
	t.Helper()
	uid := os.Geteuid()
	if uid != 0 {
		return uid
	}
	uid = 1001
	for _, path := range paths {
		if err := os.Chown(path, uid, -1); err != nil {
			t.Fatal(err)
		}
	}
	return uid
}
