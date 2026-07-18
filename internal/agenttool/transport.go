package agenttool

import (
	"context"
	"fmt"
	"strings"

	"github.com/maboo-run/shadoc/internal/agentdeploy"
)

const (
	linuxResticStageCommand        = `set -eu; umask 077; if [ -f "$HOME/.config/systemd/user/shadoc-agent.service" ]; then root="$HOME/.local/share/shadoc-agent/tools"; else root="$HOME/.local/share/restic-control-agent/tools"; fi; mkdir -p "$root"; cat > "$root/restic.install"; chmod 0700 "$root/restic.install"`
	linuxResticActivateCommand     = `set -eu; if [ -f "$HOME/.config/systemd/user/shadoc-agent.service" ]; then root="$HOME/.local/share/shadoc-agent/tools"; service=shadoc-agent.service; else root="$HOME/.local/share/restic-control-agent/tools"; service=restic-control-agent.service; fi; active="$root/restic"; previous="$root/restic.previous"; staged="$root/restic.install"; failed="$root/restic.failed"; test -x "$staged"; rm -f "$previous" "$failed"; if [ -e "$active" ]; then mv "$active" "$previous"; fi; mv "$staged" "$active"; systemctl --user restart "$service"`
	linuxResticRollbackCommand     = `set -eu; if [ -f "$HOME/.config/systemd/user/shadoc-agent.service" ]; then root="$HOME/.local/share/shadoc-agent/tools"; service=shadoc-agent.service; else root="$HOME/.local/share/restic-control-agent/tools"; service=restic-control-agent.service; fi; active="$root/restic"; previous="$root/restic.previous"; failed="$root/restic.failed"; systemctl --user stop "$service" >/dev/null 2>&1 || true; if [ -e "$active" ]; then mv "$active" "$failed"; fi; if [ -e "$previous" ]; then mv "$previous" "$active"; fi; rm -f "$failed"; systemctl --user start "$service"`
	linuxResticCleanupStageCommand = `if [ -f "$HOME/.config/systemd/user/shadoc-agent.service" ]; then root="$HOME/.local/share/shadoc-agent/tools"; else root="$HOME/.local/share/restic-control-agent/tools"; fi; rm -f "$root/restic.install"`
	linuxResticFinalizeCommand     = `if [ -f "$HOME/.config/systemd/user/shadoc-agent.service" ]; then root="$HOME/.local/share/shadoc-agent/tools"; else root="$HOME/.local/share/restic-control-agent/tools"; fi; rm -f "$root/restic.previous" "$root/restic.install" "$root/restic.failed"`
)

type SSHDialer struct{}

func (SSHDialer) Dial(ctx context.Context, target agentdeploy.Target) (Remote, error) {
	connection, err := agentdeploy.DialPinned(ctx, target)
	if err != nil {
		return nil, err
	}
	return &sshRemote{probe: agentdeploy.NewRemote(connection), connection: connection}, nil
}

type sshCommandRunner interface {
	agentdeploy.CommandRunner
	Close() error
}

type sshRemote struct {
	probe      *agentdeploy.Remote
	connection sshCommandRunner
}

func (r *sshRemote) Probe(ctx context.Context) (agentdeploy.Platform, error) {
	return r.probe.Probe(ctx)
}

func (r *sshRemote) StageRestic(ctx context.Context, content []byte) error {
	if len(content) == 0 || len(content) > 256<<20 {
		return fmt.Errorf("verified Restic artifact is empty or too large")
	}
	output, err := r.connection.Run(ctx, linuxResticStageCommand, content)
	return remoteCommandError(output, err)
}

func (r *sshRemote) ActivateRestic(ctx context.Context) error {
	output, err := r.connection.Run(ctx, linuxResticActivateCommand, nil)
	return remoteCommandError(output, err)
}

func (r *sshRemote) RollbackRestic(ctx context.Context) error {
	output, err := r.connection.Run(ctx, linuxResticRollbackCommand, nil)
	return remoteCommandError(output, err)
}

func (r *sshRemote) CleanupStagedRestic(ctx context.Context) error {
	output, err := r.connection.Run(ctx, linuxResticCleanupStageCommand, nil)
	return remoteCommandError(output, err)
}

func (r *sshRemote) FinalizeRestic(ctx context.Context) error {
	output, err := r.connection.Run(ctx, linuxResticFinalizeCommand, nil)
	return remoteCommandError(output, err)
}

func (r *sshRemote) Close() error { return r.connection.Close() }

func remoteCommandError(output []byte, err error) error {
	if err == nil {
		return nil
	}
	const limit = 2048
	if len(output) > limit {
		output = output[:limit]
	}
	diagnostic := strings.ToValidUTF8(string(output), "�")
	diagnostic = strings.Map(func(character rune) rune {
		if character < 0x20 && character != '\n' && character != '\t' || character == 0x7f {
			return ' '
		}
		return character
	}, diagnostic)
	diagnostic = strings.Join(strings.Fields(diagnostic), " ")
	if diagnostic == "" {
		return err
	}
	return fmt.Errorf("%w; remote diagnostic: %s", err, diagnostic)
}
