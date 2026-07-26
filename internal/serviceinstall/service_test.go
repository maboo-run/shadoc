package serviceinstall

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefinitionsRunBinaryDirectlyWithoutShell(t *testing.T) {
	for _, value := range []string{systemdUnit("/opt/shadoc", []string{"serve", "--port", "9090"}), launchdPlist("/Applications/shadoc", []string{"serve", "--port", "9090"})} {
		if !strings.Contains(value, "shadoc") || !strings.Contains(value, "serve") || !strings.Contains(value, "9090") || strings.Contains(value, "/bin/sh") {
			t.Fatalf("unsafe service definition: %s", value)
		}
		if strings.Contains(value, "restic-control") {
			t.Fatalf("legacy product identity remains in service definition: %s", value)
		}
	}
}

func TestStatusMapsNativeInactiveAndMissingStates(t *testing.T) {
	exitErr := &exec.ExitError{}
	for _, test := range []struct {
		goos   string
		output string
		want   string
	}{
		{goos: "linux", output: "inactive\n", want: "stopped"},
		{goos: "linux", output: "Unit shadoc.service could not be found.\n", want: "not installed"},
		{goos: "darwin", output: "Could not find service io.shadoc in domain for user\n", want: "stopped"},
	} {
		got, err := statusFromCommand(test.goos, []byte(test.output), exitErr)
		if err != nil || got != test.want {
			t.Fatalf("goos=%s output=%q status=%q err=%v", test.goos, test.output, got, err)
		}
	}
	if got, err := statusFromCommand("linux", []byte("active\n"), nil); err != nil || got != "running" {
		t.Fatalf("active status=%q err=%v", got, err)
	}
}

func TestServiceHealthURLUsesLoopbackForUnspecifiedListenAddress(t *testing.T) {
	for listen, want := range map[string]string{
		"0.0.0.0:9090": "http://127.0.0.1:9090/api/health",
		"[::]:9090":    "http://127.0.0.1:9090/api/health",
		"10.0.0.5:80":  "http://10.0.0.5:80/api/health",
	} {
		got, ok := serviceHealthURL([]string{"serve", "--listen", listen, "--data-dir", "/srv/shadoc"})
		if !ok || got != want {
			t.Fatalf("listen=%q url=%q ok=%t", listen, got, ok)
		}
	}
}

func TestUpdaterRunsInSeparateNativeServiceWithoutShellOrArbitraryIdentity(t *testing.T) {
	arguments := []string{"managed-update", "--operation-id", "op_0123456789abcdef01234567", "--version", "v1.2.3"}
	for _, test := range []struct {
		goos, program string
		scope         Scope
		wantUserFlag  bool
	}{
		{goos: "linux", program: "systemd-run", scope: UserScope, wantUserFlag: true},
		{goos: "linux", program: "systemd-run", scope: SystemScope},
		{goos: "darwin", program: "launchctl", scope: UserScope},
	} {
		program, args, err := updaterCommand(test.goos, test.scope, 501, "op_0123456789abcdef01234567", "/opt/restic-control", arguments)
		if err != nil || program != test.program || strings.Contains(strings.Join(args, " "), "/bin/sh") || !strings.Contains(strings.Join(args, " "), "/opt/restic-control managed-update") {
			t.Fatalf("goos=%s program=%q args=%v err=%v", test.goos, program, args, err)
		}
		hasUserFlag := false
		for _, argument := range args {
			hasUserFlag = hasUserFlag || argument == "--user"
		}
		if hasUserFlag != test.wantUserFlag {
			t.Fatalf("goos=%s scope=%s args=%v user flag=%t", test.goos, test.scope, args, hasUserFlag)
		}
	}
	if _, _, err := updaterCommand("linux", UserScope, 501, "../../unsafe", "/opt/restic-control", arguments); err == nil {
		t.Fatal("unsafe updater identity accepted")
	}
	if _, _, err := updaterCommand("darwin", SystemScope, 0, "op_0123456789abcdef01234567", "/opt/restic-control", arguments); err == nil {
		t.Fatal("unsupported macOS system updater accepted")
	}
}

func TestInstallExecutableRequiresStableAbsolutePath(t *testing.T) {
	if err := InstallExecutable("restic-control"); err == nil {
		t.Fatal("relative service executable accepted")
	}
}

func TestSystemdDefinitionEscapesDirectiveAndSpecifierCharacters(t *testing.T) {
	unit := systemdUnit("/opt/with space/%n/evil\nDirective/shadoc", nil)
	if strings.Contains(unit, "\nDirective/") || !strings.Contains(unit, "%%n") || !strings.Contains(unit, `evil\nDirective`) {
		t.Fatalf("unsafe systemd definition: %s", unit)
	}
}

func TestLinuxSystemServiceUsesRootOwnedDefinitionAndSystemManager(t *testing.T) {
	if got := definitionPathForScope("linux", SystemScope, "/home/example", "shadoc"); got != "/etc/systemd/system/shadoc.service" {
		t.Fatalf("system definition path=%q", got)
	}
	for _, test := range []struct {
		action string
		want   string
	}{
		{action: "stop", want: "systemctl stop shadoc.service"},
		{action: "restart", want: "systemctl restart shadoc.service"},
		{action: "status", want: "systemctl is-active shadoc.service"},
	} {
		program, arguments, err := serviceActionCommandForScope("linux", SystemScope, 0, "/root", test.action, "shadoc")
		if err != nil {
			t.Fatalf("action=%s err=%v", test.action, err)
		}
		if got := strings.Join(append([]string{program}, arguments...), " "); got != test.want {
			t.Fatalf("action=%s command=%q", test.action, got)
		}
	}
}

func TestLinuxSystemUnitRunsOnlyTheFixedRootService(t *testing.T) {
	unit := systemdUnitForScope(SystemScope, "/var/lib/shadoc/app/shadoc", []string{
		"serve", "--service-scope", "system", "--listen", "127.0.0.1:8585", "--data-dir", "/var/lib/shadoc",
	})
	for _, expected := range []string{
		"User=root",
		"Group=root",
		"UMask=0077",
		"WantedBy=multi-user.target",
		`ExecStart="/var/lib/shadoc/app/shadoc" "serve" "--service-scope" "system"`,
		"NoNewPrivileges=true",
		"PrivateTmp=true",
	} {
		if !strings.Contains(unit, expected) {
			t.Fatalf("system unit missing %q:\n%s", expected, unit)
		}
	}
	if strings.Contains(unit, "/bin/sh") || strings.Contains(unit, "WantedBy=default.target") {
		t.Fatalf("unsafe system unit:\n%s", unit)
	}
}

func TestScopeValidationRejectsRootUserServicesAndNonLinuxSystemServices(t *testing.T) {
	if err := validateScopeRuntime("linux", UserScope, 0); err == nil || !strings.Contains(err.Error(), "--system") {
		t.Fatalf("root user service error=%v", err)
	}
	if err := validateScopeRuntime("darwin", SystemScope, 0); err == nil {
		t.Fatal("macOS system scope accepted")
	}
	if err := validateScopeRuntime("linux", SystemScope, 1000); err == nil {
		t.Fatal("unprivileged system scope accepted")
	}
	if err := validateScopeRuntime("linux", SystemScope, 0); err != nil {
		t.Fatalf("root Linux system scope rejected: %v", err)
	}
}

func TestOwnedExecutableValidationRejectsWritableOrSymlinkedPaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(root, "data", "app")
	executable := filepath.Join(app, "shadoc")
	if err := os.MkdirAll(app, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateOwnedExecutablePath(executable, os.Geteuid()); err != nil {
		t.Fatalf("safe executable rejected: %v", err)
	}
	if err := os.Chmod(app, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := validateOwnedExecutablePath(executable, os.Geteuid()); err == nil {
		t.Fatal("world-writable executable parent accepted")
	}
	if err := os.Chmod(app, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-app")
	if err := os.Symlink(app, link); err != nil {
		t.Fatal(err)
	}
	if err := validateOwnedExecutablePath(filepath.Join(link, "shadoc"), os.Geteuid()); err == nil {
		t.Fatal("symlinked executable path accepted")
	}
}

func TestSystemServiceArgumentsAreLimitedToFixedRootServeCommand(t *testing.T) {
	valid := []string{"serve", "--service-scope", "system", "--listen", "127.0.0.1:8585", "--data-dir", "/var/lib/shadoc"}
	if err := validateSystemServiceArguments(valid); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{
		nil,
		{"serve", "--listen", "127.0.0.1:8585", "--data-dir", "/tmp/shadoc"},
		{"serve", "--service-scope", "system", "--listen", "bad", "--data-dir", "/var/lib/shadoc"},
		{"managed-update", "--data-dir", "/var/lib/shadoc"},
	} {
		if err := validateSystemServiceArguments(arguments); err == nil {
			t.Fatalf("unsafe system service arguments accepted: %v", arguments)
		}
	}
}

func TestRemovingServiceDefinitionPreservesApplicationData(t *testing.T) {
	home := t.TempDir()
	path := definitionPath("linux", home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(home, ".config", "restic-control", "state.db")
	if err := os.MkdirAll(filepath.Dir(data), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeDefinition("linux", home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatalf("application data removed: %v", err)
	}
}

func TestNativeServiceIdentityUsesShadocOnSupportedPlatforms(t *testing.T) {
	home := "/Users/example"
	if got := definitionPath("linux", home); got != "/Users/example/.config/systemd/user/shadoc.service" {
		t.Fatalf("linux definition path=%q", got)
	}
	if got := definitionPath("darwin", home); got != "/Users/example/Library/LaunchAgents/io.shadoc.plist" {
		t.Fatalf("darwin definition path=%q", got)
	}
	for _, test := range []struct {
		goos   string
		action string
		want   string
	}{
		{goos: "linux", action: "stop", want: "systemctl --user stop shadoc.service"},
		{goos: "linux", action: "restart", want: "systemctl --user restart shadoc.service"},
		{goos: "linux", action: "status", want: "systemctl --user is-active shadoc.service"},
		{goos: "darwin", action: "stop", want: "launchctl bootout gui/501 /Users/example/Library/LaunchAgents/io.shadoc.plist"},
		{goos: "darwin", action: "start", want: "launchctl bootstrap gui/501 /Users/example/Library/LaunchAgents/io.shadoc.plist"},
		{goos: "darwin", action: "status", want: "launchctl print gui/501/io.shadoc"},
	} {
		program, arguments, err := serviceActionCommand(test.goos, 501, home, test.action)
		if err != nil {
			t.Fatalf("goos=%s action=%s err=%v", test.goos, test.action, err)
		}
		if got := strings.Join(append([]string{program}, arguments...), " "); got != test.want {
			t.Fatalf("goos=%s action=%s command=%q", test.goos, test.action, got)
		}
	}
	darwinRestart, err := restartActionCommands("darwin", 501, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(darwinRestart) != 2 || !darwinRestart[0].ignoreError {
		t.Fatalf("darwin restart commands=%+v", darwinRestart)
	}
	if first := strings.Join(append([]string{darwinRestart[0].program}, darwinRestart[0].arguments...), " "); first != "launchctl bootout gui/501 /Users/example/Library/LaunchAgents/io.shadoc.plist" {
		t.Fatalf("darwin restart stop=%q", first)
	}
	if second := strings.Join(append([]string{darwinRestart[1].program}, darwinRestart[1].arguments...), " "); second != "launchctl bootstrap gui/501 /Users/example/Library/LaunchAgents/io.shadoc.plist" {
		t.Fatalf("darwin restart start=%q", second)
	}
}

func TestInstalledServiceIdentityPrefersShadocAndFallsBackToLegacyDefinition(t *testing.T) {
	home := t.TempDir()
	legacy := definitionPathFor("linux", home, "restic-control")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := installedServiceIdentity("linux", home); got != "restic-control" {
		t.Fatalf("legacy identity=%q", got)
	}
	current := definitionPathFor("linux", home, "shadoc")
	if err := os.WriteFile(current, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := installedServiceIdentity("linux", home); got != "shadoc" {
		t.Fatalf("current identity=%q", got)
	}
	program, arguments, err := serviceActionCommandFor("linux", 501, home, "restart", "restic-control")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(append([]string{program}, arguments...), " "); got != "systemctl --user restart restic-control.service" {
		t.Fatalf("legacy restart=%q", got)
	}
}

func TestLegacyServiceTransitionCommandsStopOldIdentityAndCanRestoreIt(t *testing.T) {
	home := "/Users/example"
	for _, test := range []struct {
		goos        string
		wantStop    string
		wantRestore string
	}{
		{
			goos:        "linux",
			wantStop:    "systemctl --user disable --now restic-control.service",
			wantRestore: "systemctl --user enable --now restic-control.service",
		},
		{
			goos:        "darwin",
			wantStop:    "launchctl bootout gui/501 /Users/example/Library/LaunchAgents/io.restic-control.plist",
			wantRestore: "launchctl bootstrap gui/501 /Users/example/Library/LaunchAgents/io.restic-control.plist",
		},
	} {
		stop, restore, err := legacyServiceTransitionCommands(test.goos, 501, home)
		if err != nil {
			t.Fatalf("goos=%s err=%v", test.goos, err)
		}
		if got := strings.Join(append([]string{stop.program}, stop.arguments...), " "); got != test.wantStop {
			t.Fatalf("goos=%s stop=%q", test.goos, got)
		}
		if got := strings.Join(append([]string{restore.program}, restore.arguments...), " "); got != test.wantRestore {
			t.Fatalf("goos=%s restore=%q", test.goos, got)
		}
	}
}

func TestFinalizeLegacyMigrationRemovesOnlyLegacyDefinition(t *testing.T) {
	home := t.TempDir()
	for _, identity := range []string{"shadoc", "restic-control"} {
		path := definitionPathFor("darwin", home, identity)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(identity), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := finalizeLegacyMigration("darwin", home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(definitionPathFor("darwin", home, "restic-control")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy definition still exists: %v", err)
	}
	if _, err := os.Stat(definitionPathFor("darwin", home, "shadoc")); err != nil {
		t.Fatalf("current definition removed: %v", err)
	}
}
