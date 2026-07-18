package agenttool

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
)

func TestSSHRemoteUsesOnlyFixedManagedResticCommands(t *testing.T) {
	runner := &recordingRunner{probeOutput: []byte("Linux\nx86_64\nsystemd\n/home/backup\n")}
	remote := &sshRemote{probe: agentdeploy.NewRemote(runner), connection: runner}
	platform, err := remote.Probe(t.Context())
	if err != nil || platform.OS != "linux" || platform.Arch != "amd64" {
		t.Fatalf("platform=%+v err=%v", platform, err)
	}
	artifact := []byte("verified-restic")
	for _, action := range []func(context.Context) error{
		func(ctx context.Context) error { return remote.StageRestic(ctx, artifact) },
		remote.ActivateRestic,
		remote.RollbackRestic,
		remote.CleanupStagedRestic,
		remote.FinalizeRestic,
	} {
		if err := action(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		linuxResticStageCommand,
		linuxResticActivateCommand,
		linuxResticRollbackCommand,
		linuxResticCleanupStageCommand,
		linuxResticFinalizeCommand,
	}
	if !reflect.DeepEqual(runner.commands[1:], want) {
		t.Fatalf("commands=%q want=%q", runner.commands[1:], want)
	}
	if !bytes.Equal(runner.stdin[1], artifact) {
		t.Fatalf("stage stdin=%q", runner.stdin[1])
	}
}

func TestManagedResticCommandsSupportCurrentAndLegacyAgentIdentities(t *testing.T) {
	for name, command := range map[string]string{
		"stage":    linuxResticStageCommand,
		"activate": linuxResticActivateCommand,
		"rollback": linuxResticRollbackCommand,
		"cleanup":  linuxResticCleanupStageCommand,
		"finalize": linuxResticFinalizeCommand,
	} {
		if !strings.Contains(command, "shadoc-agent") || !strings.Contains(command, "restic-control-agent") {
			t.Fatalf("%s command does not support both Agent identities: %s", name, command)
		}
	}
}

func TestSSHRemoteRejectsEmptyResticArtifact(t *testing.T) {
	runner := &recordingRunner{}
	remote := &sshRemote{probe: agentdeploy.NewRemote(runner), connection: runner}
	if err := remote.StageRestic(t.Context(), nil); err == nil {
		t.Fatal("empty artifact accepted")
	}
	if len(runner.commands) != 0 {
		t.Fatalf("remote command executed for invalid artifact: %q", runner.commands)
	}
}

func TestSSHRemoteIncludesBoundedDiagnosticFromFixedInstallCommand(t *testing.T) {
	runner := &recordingRunner{
		failCommand: linuxResticActivateCommand,
		failOutput:  []byte("Failed to connect to user service manager\n"),
		failErr:     errors.New("exit status 1"),
	}
	remote := &sshRemote{probe: agentdeploy.NewRemote(runner), connection: runner}
	err := remote.ActivateRestic(t.Context())
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to user service manager") {
		t.Fatalf("activation diagnostic=%v", err)
	}
}

type recordingRunner struct {
	probeOutput []byte
	commands    []string
	stdin       [][]byte
	failCommand string
	failOutput  []byte
	failErr     error
}

func (r *recordingRunner) Run(_ context.Context, command string, stdin []byte) ([]byte, error) {
	r.commands = append(r.commands, command)
	r.stdin = append(r.stdin, append([]byte(nil), stdin...))
	if len(r.commands) == 1 && r.probeOutput != nil {
		return r.probeOutput, nil
	}
	if command == r.failCommand {
		return append([]byte(nil), r.failOutput...), r.failErr
	}
	return nil, nil
}

func (*recordingRunner) Close() error { return nil }
