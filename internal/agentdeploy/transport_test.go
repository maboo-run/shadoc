package agentdeploy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSSHTransportUsesFixedCommandsAndStreamsSecretsOnlyOnStdin(t *testing.T) {
	runner := &recordingRunner{probe: "Linux\nx86_64\nsystemd\n/home/backup\n"}
	remote := NewRemote(runner)
	platform, err := remote.Probe(context.Background())
	if err != nil || platform.OS != "linux" || platform.Arch != "amd64" {
		t.Fatalf("platform=%+v err=%v", platform, err)
	}
	secret := []byte("one-time-enrollment-secret")
	if err := remote.Upload(context.Background(), TokenFile, secret); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls=%v", runner.calls)
	}
	call := runner.calls[1]
	if strings.Contains(call.command, string(secret)) || !bytes.Equal(call.stdin, secret) {
		t.Fatalf("command=%q stdin=%q", call.command, call.stdin)
	}
	if call.command != tokenUploadCommand {
		t.Fatalf("command=%q", call.command)
	}
}

func TestSSHTransportRollsBackFilesWhenActivationFails(t *testing.T) {
	runner := &recordingRunner{probe: "Linux\naarch64\nsystemd\n/home/backup\n", activateErr: errors.New("systemctl failed")}
	remote := NewRemote(runner)
	if err := remote.Activate(context.Background(), Platform{OS: "linux", Arch: "arm64"}); err == nil {
		t.Fatal("activation failure was ignored")
	}
	if len(runner.calls) != 2 || runner.calls[1].command != linuxCleanupCommand {
		t.Fatalf("calls=%+v", runner.calls)
	}
}

func TestSSHTransportStopsAgentBeforeRemovingItsFiles(t *testing.T) {
	runner := &recordingRunner{}
	remote := NewRemote(runner)
	platform := Platform{OS: "linux", Arch: "amd64", Service: "systemd", Home: "/home/example"}
	if err := remote.Stop(context.Background(), platform); err != nil {
		t.Fatal(err)
	}
	if err := remote.Remove(context.Background(), platform); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 || runner.calls[0].command != linuxStopCommand || runner.calls[1].command != linuxRemoveCommand {
		t.Fatalf("calls=%+v", runner.calls)
	}
	if strings.Contains(runner.calls[0].command, "rm -") || !strings.Contains(runner.calls[1].command, ".local/share/shadoc-agent") {
		t.Fatalf("stop/remove commands are not safely separated: %+v", runner.calls)
	}
}

func TestFixedAgentLifecycleCommandsMigrateOrSupportLegacyServiceIdentity(t *testing.T) {
	for name, command := range map[string]string{
		"linux activate":   linuxActivateCommand,
		"darwin activate":  darwinActivateCommand,
		"windows activate": windowsActivateCommand,
		"linux restart":    linuxRestartCommand,
		"darwin restart":   darwinRestartCommand,
		"windows restart":  windowsRestartCommand,
		"linux stop":       linuxStopCommand,
		"darwin stop":      darwinStopCommand,
		"windows stop":     windowsStopCommand,
		"linux remove":     linuxRemoveCommand,
		"darwin remove":    darwinRemoveCommand,
		"windows remove":   windowsRemoveCommand,
		"linux upgrade":    linuxUpgradeActivateCommand,
		"darwin upgrade":   darwinUpgradeActivateCommand,
		"windows upgrade":  windowsUpgradeActivateCommand,
	} {
		if !strings.Contains(command, "shadoc-agent") || !strings.Contains(command, "restic-control-agent") {
			t.Errorf("%s does not support both Shadoc and legacy Agent identities: %s", name, command)
		}
	}
}

func TestFinalizeCommandsRetireOnlyLegacyAgentIdentity(t *testing.T) {
	for name, command := range map[string]string{
		"linux":   linuxFinalizeCommand,
		"darwin":  darwinFinalizeCommand,
		"windows": windowsFinalizeCommand,
	} {
		if !strings.Contains(command, "restic-control-agent") {
			t.Errorf("%s finalize does not retire the legacy Agent: %s", name, command)
		}
		if strings.Contains(command, `rm -f "$HOME/.config/systemd/user/shadoc-agent.service"`) ||
			strings.Contains(command, `rm -f "$HOME/Library/LaunchAgents/io.shadoc-agent.plist"`) ||
			strings.Contains(command, "delete shadoc-agent") {
			t.Errorf("%s finalize removes the current Agent: %s", name, command)
		}
	}
}

func TestActivationMigratesCredentialsAndCleanupCanRemoveOnlyTheStagedCopy(t *testing.T) {
	for name, pair := range map[string][2]string{
		"linux":   {linuxActivateCommand, linuxCleanupCommand},
		"darwin":  {darwinActivateCommand, darwinCleanupCommand},
		"windows": {windowsActivateCommand, windowsCleanupCommand},
	} {
		for _, required := range []string{"agent.crt", "agent.key", "ca.crt", ".credential-migration"} {
			if !strings.Contains(pair[0], required) || !strings.Contains(pair[1], required) {
				t.Errorf("%s migration/cleanup missing %q", name, required)
			}
		}
	}
}

func TestMigrationProtectsWindowsCredentialsAndFinalizationDeletesUnusedEnrollmentToken(t *testing.T) {
	if !strings.Contains(windowsActivateCommand, "icacls.exe") || !strings.Contains(windowsActivateCommand, "/inheritance:r") {
		t.Fatalf("Windows credential migration lacks fixed ACL hardening: %s", windowsActivateCommand)
	}
	for name, command := range map[string]string{"linux": linuxFinalizeCommand, "darwin": darwinFinalizeCommand, "windows": windowsFinalizeCommand} {
		if !strings.Contains(command, "enrollment.token") {
			t.Errorf("%s finalization leaves the unused enrollment token: %s", name, command)
		}
	}
}

func TestSSHTransportUsesFixedCommandsForTransactionalUpgrade(t *testing.T) {
	runner := &recordingRunner{probe: "Linux\nx86_64\nsystemd\n/home/backup\n"}
	remote := NewRemote(runner)
	platform, err := remote.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	binary := []byte("replacement Agent binary")
	if err := remote.StageUpgrade(t.Context(), binary); err != nil {
		t.Fatal(err)
	}
	if err := remote.ActivateUpgrade(t.Context(), platform); err != nil {
		t.Fatal(err)
	}
	if err := remote.RollbackUpgrade(t.Context(), platform); err != nil {
		t.Fatal(err)
	}
	if err := remote.FinalizeUpgrade(t.Context(), platform); err != nil {
		t.Fatal(err)
	}
	want := []string{probeCommand, linuxUpgradeUploadCommand, linuxUpgradeActivateCommand, linuxUpgradeRollbackCommand, linuxUpgradeFinalizeCommand}
	if len(runner.calls) != len(want) {
		t.Fatalf("calls=%+v", runner.calls)
	}
	for index, command := range want {
		if runner.calls[index].command != command {
			t.Fatalf("call %d command=%q want=%q", index, runner.calls[index].command, command)
		}
		if index == 1 && !bytes.Equal(runner.calls[index].stdin, binary) || index != 1 && len(runner.calls[index].stdin) != 0 {
			t.Fatalf("call %d stdin=%q", index, runner.calls[index].stdin)
		}
	}
}

func TestSSHTransportRestartsAgentWithFixedPlatformCommand(t *testing.T) {
	for name, test := range map[string]struct {
		platform Platform
		command  string
	}{
		"linux":   {platform: Platform{OS: "linux"}, command: linuxRestartCommand},
		"darwin":  {platform: Platform{OS: "darwin"}, command: darwinRestartCommand},
		"windows": {platform: Platform{OS: "windows"}, command: windowsRestartCommand},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &recordingRunner{}
			if err := NewRemote(runner).Restart(t.Context(), test.platform); err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != 1 || runner.calls[0].command != test.command || len(runner.calls[0].stdin) != 0 {
				t.Fatalf("calls=%+v", runner.calls)
			}
		})
	}
}

func TestUpgradeRollbackCommandsAreSafeAfterActivationAlreadyRestoredThePreviousBinary(t *testing.T) {
	for name, command := range map[string]string{
		"linux":   linuxUpgradeRollbackCommand,
		"darwin":  darwinUpgradeRollbackCommand,
		"windows": windowsUpgradeRollbackCommand,
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(command, "previous Agent binary is unavailable") || strings.Contains(command, `test -x "$previous"`) {
				t.Fatalf("rollback still requires a previous binary on every invocation: %s", command)
			}
			if !strings.Contains(command, "Start-Service") && !strings.Contains(command, "systemctl --user start") && !strings.Contains(command, "launchctl bootstrap") {
				t.Fatalf("rollback does not restore service availability: %s", command)
			}
		})
	}
}

func TestSSHTransportFallsBackToWindowsAndUsesServiceCommands(t *testing.T) {
	runner := &recordingRunner{probeErr: errors.New("uname unavailable"), windowsProbe: "Windows\nAMD64\nwindows-service\nC:\\Users\\backup\n"}
	remote := NewRemote(runner)
	platform, err := remote.Probe(context.Background())
	if err != nil || platform.OS != "windows" || platform.Arch != "amd64" {
		t.Fatalf("platform=%+v err=%v", platform, err)
	}
	if err := remote.Upload(context.Background(), BinaryFile, []byte("PE binary")); err != nil {
		t.Fatal(err)
	}
	if err := remote.Upload(context.Background(), WindowsServiceFile, []byte("service script")); err != nil {
		t.Fatal(err)
	}
	if err := remote.Activate(context.Background(), platform); err != nil {
		t.Fatal(err)
	}
	if err := remote.Finalize(context.Background(), platform); err != nil {
		t.Fatal(err)
	}
	if runner.calls[len(runner.calls)-2].command != windowsActivateCommand || runner.calls[len(runner.calls)-1].command != windowsFinalizeCommand {
		t.Fatalf("calls=%+v", runner.calls)
	}
}

type commandCall struct {
	command string
	stdin   []byte
}

type recordingRunner struct {
	probe        string
	probeErr     error
	windowsProbe string
	activateErr  error
	calls        []commandCall
}

func (r *recordingRunner) Run(_ context.Context, command string, stdin []byte) ([]byte, error) {
	r.calls = append(r.calls, commandCall{command: command, stdin: append([]byte(nil), stdin...)})
	if command == probeCommand {
		return []byte(r.probe), r.probeErr
	}
	if command == windowsProbeCommand {
		return []byte(r.windowsProbe), nil
	}
	if command == linuxActivateCommand && r.activateErr != nil {
		return nil, r.activateErr
	}
	return nil, nil
}
