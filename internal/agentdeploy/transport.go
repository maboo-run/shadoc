package agentdeploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const probeCommand = `uname -s; uname -m; if command -v systemctl >/dev/null 2>&1; then printf 'systemd\n'; elif command -v launchctl >/dev/null 2>&1; then printf 'launchd\n'; else printf 'none\n'; fi; printf '%s\n' "$HOME"`
const windowsProbeCommand = `powershell.exe -NoProfile -NonInteractive -Command "Write-Output 'Windows'; Write-Output $env:PROCESSOR_ARCHITECTURE; Write-Output 'windows-service'; Write-Output $env:USERPROFILE"`

const (
	binaryUploadCommand           = `umask 077; mkdir -p "$HOME/.local/bin"; cat > "$HOME/.local/bin/shadoc-agent"; chmod 0700 "$HOME/.local/bin/shadoc-agent"`
	caUploadCommand               = `umask 077; mkdir -p "$HOME/.config/shadoc-agent"; cat > "$HOME/.config/shadoc-agent/ca.crt"; chmod 0600 "$HOME/.config/shadoc-agent/ca.crt"`
	tokenUploadCommand            = `umask 077; mkdir -p "$HOME/.config/shadoc-agent"; cat > "$HOME/.config/shadoc-agent/enrollment.token"; chmod 0600 "$HOME/.config/shadoc-agent/enrollment.token"`
	configUploadCommand           = `umask 077; mkdir -p "$HOME/.config/shadoc-agent"; cat > "$HOME/.config/shadoc-agent/agent.env"; chmod 0600 "$HOME/.config/shadoc-agent/agent.env"`
	linuxUnitUploadCommand        = `umask 077; mkdir -p "$HOME/.config/systemd/user"; cat > "$HOME/.config/systemd/user/shadoc-agent.service"; chmod 0600 "$HOME/.config/systemd/user/shadoc-agent.service"`
	darwinPlistUploadCommand      = `umask 077; mkdir -p "$HOME/Library/LaunchAgents"; cat > "$HOME/Library/LaunchAgents/io.shadoc-agent.plist"; chmod 0600 "$HOME/Library/LaunchAgents/io.shadoc-agent.plist"`
	linuxActivateCommand          = `set -eu; olddata="$HOME/.local/share/restic-control-agent"; newdata="$HOME/.local/share/shadoc-agent"; marker="$newdata/.credential-migration"; if [ ! -f "$newdata/agent.crt" ]; then mkdir -p "$newdata"; chmod 0700 "$newdata"; : > "$marker"; chmod 0600 "$marker"; if [ -f "$olddata/agent.crt" ] && [ -f "$olddata/agent.key" ] && [ -f "$olddata/ca.crt" ]; then cp "$olddata/agent.crt" "$olddata/agent.key" "$olddata/ca.crt" "$newdata/"; chmod 0600 "$newdata/agent.crt" "$newdata/agent.key" "$newdata/ca.crt"; fi; fi; legacy="$HOME/.config/systemd/user/restic-control-agent.service"; parked="$legacy.shadoc-migration"; had_legacy=0; if [ -f "$legacy" ]; then systemctl --user disable --now restic-control-agent.service; mv "$legacy" "$parked"; had_legacy=1; fi; if ! systemctl --user daemon-reload || ! systemctl --user enable shadoc-agent.service || ! systemctl --user restart shadoc-agent.service; then systemctl --user disable --now shadoc-agent.service >/dev/null 2>&1 || true; if [ "$had_legacy" = 1 ]; then mv "$parked" "$legacy"; systemctl --user daemon-reload; systemctl --user enable --now restic-control-agent.service; fi; exit 1; fi`
	darwinActivateCommand         = `set -eu; olddata="$HOME/.local/share/restic-control-agent"; newdata="$HOME/.local/share/shadoc-agent"; marker="$newdata/.credential-migration"; if [ ! -f "$newdata/agent.crt" ]; then mkdir -p "$newdata"; chmod 0700 "$newdata"; : > "$marker"; chmod 0600 "$marker"; if [ -f "$olddata/agent.crt" ] && [ -f "$olddata/agent.key" ] && [ -f "$olddata/ca.crt" ]; then cp "$olddata/agent.crt" "$olddata/agent.key" "$olddata/ca.crt" "$newdata/"; chmod 0600 "$newdata/agent.crt" "$newdata/agent.key" "$newdata/ca.crt"; fi; fi; domain="gui/$(id -u)"; current="$HOME/Library/LaunchAgents/io.shadoc-agent.plist"; legacy="$HOME/Library/LaunchAgents/io.restic-control-agent.plist"; parked="$legacy.shadoc-migration"; had_legacy=0; if [ -f "$legacy" ]; then launchctl bootout "$domain" "$legacy" >/dev/null 2>&1 || ! launchctl print "$domain/io.restic-control-agent" >/dev/null 2>&1; mv "$legacy" "$parked"; had_legacy=1; fi; launchctl bootout "$domain" "$current" >/dev/null 2>&1 || true; if ! launchctl bootstrap "$domain" "$current"; then if [ "$had_legacy" = 1 ]; then mv "$parked" "$legacy"; launchctl bootstrap "$domain" "$legacy"; fi; exit 1; fi`
	linuxRestartCommand           = `if [ -f "$HOME/.config/systemd/user/shadoc-agent.service" ]; then systemctl --user restart shadoc-agent.service; else systemctl --user restart restic-control-agent.service; fi`
	darwinRestartCommand          = `set -eu; domain="gui/$(id -u)"; if [ -f "$HOME/Library/LaunchAgents/io.shadoc-agent.plist" ]; then plist="$HOME/Library/LaunchAgents/io.shadoc-agent.plist"; label=io.shadoc-agent; else plist="$HOME/Library/LaunchAgents/io.restic-control-agent.plist"; label=io.restic-control-agent; fi; launchctl bootout "$domain" "$plist" >/dev/null 2>&1 || ! launchctl print "$domain/$label" >/dev/null 2>&1; launchctl bootstrap "$domain" "$plist"`
	windowsRestartCommand         = `powershell.exe -NoProfile -NonInteractive -Command "$ErrorActionPreference='Stop';if(Get-Service shadoc-agent -ErrorAction SilentlyContinue){Restart-Service shadoc-agent -Force}else{Restart-Service restic-control-agent -Force}"`
	linuxCleanupCommand           = `legacy="$HOME/.config/systemd/user/restic-control-agent.service"; parked="$legacy.shadoc-migration"; newdata="$HOME/.local/share/shadoc-agent"; systemctl --user disable --now shadoc-agent.service >/dev/null 2>&1 || true; rm -f "$HOME/.config/systemd/user/shadoc-agent.service" "$HOME/.config/shadoc-agent/enrollment.token" "$HOME/.config/shadoc-agent/agent.env" "$HOME/.config/shadoc-agent/ca.crt" "$HOME/.local/bin/shadoc-agent"; if [ -f "$newdata/.credential-migration" ]; then rm -f "$newdata/agent.crt" "$newdata/agent.key" "$newdata/ca.crt" "$newdata/.credential-migration"; fi; if [ -f "$parked" ]; then mv "$parked" "$legacy"; fi; systemctl --user daemon-reload >/dev/null 2>&1 || true; if [ -f "$legacy" ]; then systemctl --user enable --now restic-control-agent.service; fi`
	darwinCleanupCommand          = `domain="gui/$(id -u)"; legacy="$HOME/Library/LaunchAgents/io.restic-control-agent.plist"; parked="$legacy.shadoc-migration"; newdata="$HOME/.local/share/shadoc-agent"; launchctl bootout "$domain" "$HOME/Library/LaunchAgents/io.shadoc-agent.plist" >/dev/null 2>&1 || true; rm -f "$HOME/Library/LaunchAgents/io.shadoc-agent.plist" "$HOME/.config/shadoc-agent/enrollment.token" "$HOME/.config/shadoc-agent/agent.env" "$HOME/.config/shadoc-agent/ca.crt" "$HOME/.local/bin/shadoc-agent"; if [ -f "$newdata/.credential-migration" ]; then rm -f "$newdata/agent.crt" "$newdata/agent.key" "$newdata/ca.crt" "$newdata/.credential-migration"; fi; if [ -f "$parked" ]; then mv "$parked" "$legacy"; fi; if [ -f "$legacy" ]; then launchctl bootstrap "$domain" "$legacy"; fi`
	windowsBinaryUploadCommand    = `powershell.exe -NoProfile -NonInteractive -Command "$r=Join-Path $env:ProgramData 'shadoc-agent';New-Item -ItemType Directory -Force $r|Out-Null;$f=[IO.File]::Open((Join-Path $r 'shadoc-agent.exe'),[IO.FileMode]::Create,[IO.FileAccess]::Write);[Console]::OpenStandardInput().CopyTo($f);$f.Close()"`
	windowsCAUploadCommand        = `powershell.exe -NoProfile -NonInteractive -Command "$r=Join-Path $env:ProgramData 'shadoc-agent';New-Item -ItemType Directory -Force $r|Out-Null;$f=[IO.File]::Open((Join-Path $r 'ca.crt'),[IO.FileMode]::Create,[IO.FileAccess]::Write);[Console]::OpenStandardInput().CopyTo($f);$f.Close()"`
	windowsTokenUploadCommand     = `powershell.exe -NoProfile -NonInteractive -Command "$r=Join-Path $env:ProgramData 'shadoc-agent';New-Item -ItemType Directory -Force $r|Out-Null;$f=[IO.File]::Open((Join-Path $r 'enrollment.token'),[IO.FileMode]::Create,[IO.FileAccess]::Write);[Console]::OpenStandardInput().CopyTo($f);$f.Close()"`
	windowsServiceUploadCommand   = `powershell.exe -NoProfile -NonInteractive -Command "$r=Join-Path $env:ProgramData 'shadoc-agent';New-Item -ItemType Directory -Force $r|Out-Null;$f=[IO.File]::Open((Join-Path $r 'install-service.ps1'),[IO.FileMode]::Create,[IO.FileAccess]::Write);[Console]::OpenStandardInput().CopyTo($f);$f.Close()"`
	windowsActivateCommand        = `powershell.exe -NoProfile -NonInteractive -Command "$ErrorActionPreference='Stop';$old=Join-Path $env:ProgramData 'restic-control-agent\data';$new=Join-Path $env:ProgramData 'shadoc-agent\data';$marker=Join-Path $new '.credential-migration';if(!(Test-Path (Join-Path $new 'agent.crt'))){New-Item -ItemType Directory -Force $new|Out-Null;& icacls.exe $new /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F'|Out-Null;if($LASTEXITCODE -ne 0){throw 'unable to protect migrated Agent credentials'};New-Item -ItemType File -Force $marker|Out-Null;if((Test-Path (Join-Path $old 'agent.crt')) -and (Test-Path (Join-Path $old 'agent.key')) -and (Test-Path (Join-Path $old 'ca.crt'))){Copy-Item (Join-Path $old 'agent.crt'),(Join-Path $old 'agent.key'),(Join-Path $old 'ca.crt') $new}};$legacy=Get-Service restic-control-agent -ErrorAction SilentlyContinue;if($legacy){Set-Service restic-control-agent -StartupType Disabled;Stop-Service restic-control-agent -Force -ErrorAction Stop};try{& powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $env:ProgramData 'shadoc-agent\install-service.ps1');if($LASTEXITCODE -ne 0){throw 'unable to activate Shadoc Agent'}}catch{if($legacy){Set-Service restic-control-agent -StartupType Automatic;Start-Service restic-control-agent};throw}"`
	windowsCleanupCommand         = `powershell.exe -NoProfile -NonInteractive -Command "$new=Join-Path $env:ProgramData 'shadoc-agent';$data=Join-Path $new 'data';Stop-Service shadoc-agent -ErrorAction SilentlyContinue;sc.exe delete shadoc-agent|Out-Null;if(Test-Path (Join-Path $data '.credential-migration')){Remove-Item (Join-Path $data 'agent.crt'),(Join-Path $data 'agent.key'),(Join-Path $data 'ca.crt'),(Join-Path $data '.credential-migration') -Force -ErrorAction SilentlyContinue};Remove-Item (Join-Path $new 'shadoc-agent.exe'),(Join-Path $new 'install-service.ps1'),(Join-Path $new 'enrollment.token'),(Join-Path $new 'ca.crt') -Force -ErrorAction SilentlyContinue;if(Get-Service restic-control-agent -ErrorAction SilentlyContinue){Set-Service restic-control-agent -StartupType Automatic;Start-Service restic-control-agent}"`
	linuxFinalizeCommand          = `legacy="$HOME/.config/systemd/user/restic-control-agent.service"; rm -f "$legacy.shadoc-migration" "$HOME/.local/share/shadoc-agent/.credential-migration" "$HOME/.config/shadoc-agent/enrollment.token"`
	darwinFinalizeCommand         = `domain="gui/$(id -u)"; legacy="$HOME/Library/LaunchAgents/io.restic-control-agent.plist"; launchctl bootout "$domain" "$legacy" >/dev/null 2>&1 || true; rm -f "$legacy" "$legacy.shadoc-migration" "$HOME/.local/share/shadoc-agent/.credential-migration" "$HOME/.config/shadoc-agent/enrollment.token"`
	windowsFinalizeCommand        = `powershell.exe -NoProfile -NonInteractive -Command "$legacy=Get-Service restic-control-agent -ErrorAction SilentlyContinue;if($legacy){Stop-Service restic-control-agent -Force -ErrorAction SilentlyContinue;sc.exe delete restic-control-agent|Out-Null;if($LASTEXITCODE -ne 0){throw 'unable to retire legacy Agent service'}};Remove-Item (Join-Path $env:ProgramData 'shadoc-agent\data\.credential-migration'),(Join-Path $env:ProgramData 'shadoc-agent\enrollment.token') -Force -ErrorAction SilentlyContinue"`
	linuxStopCommand              = `for unit in shadoc-agent.service restic-control-agent.service; do if [ -f "$HOME/.config/systemd/user/$unit" ]; then systemctl --user disable --now "$unit"; fi; done`
	linuxRemoveCommand            = `rm -f "$HOME/.config/systemd/user/shadoc-agent.service" "$HOME/.config/systemd/user/restic-control-agent.service" "$HOME/.config/shadoc-agent/enrollment.token" "$HOME/.config/shadoc-agent/agent.env" "$HOME/.config/shadoc-agent/ca.crt" "$HOME/.config/restic-control-agent/enrollment.token" "$HOME/.config/restic-control-agent/agent.env" "$HOME/.config/restic-control-agent/ca.crt" "$HOME/.local/bin/shadoc-agent" "$HOME/.local/bin/restic-control-agent"; rm -rf "$HOME/.local/share/shadoc-agent" "$HOME/.local/share/restic-control-agent"; systemctl --user daemon-reload`
	darwinStopCommand             = `domain="gui/$(id -u)"; for name in shadoc-agent restic-control-agent; do plist="$HOME/Library/LaunchAgents/io.$name.plist"; if [ -f "$plist" ]; then launchctl bootout "$domain" "$plist" >/dev/null 2>&1 || ! launchctl print "$domain/io.$name" >/dev/null 2>&1; fi; done`
	darwinRemoveCommand           = `rm -f "$HOME/Library/LaunchAgents/io.shadoc-agent.plist" "$HOME/Library/LaunchAgents/io.restic-control-agent.plist" "$HOME/.config/shadoc-agent/enrollment.token" "$HOME/.config/shadoc-agent/agent.env" "$HOME/.config/shadoc-agent/ca.crt" "$HOME/.config/restic-control-agent/enrollment.token" "$HOME/.config/restic-control-agent/agent.env" "$HOME/.config/restic-control-agent/ca.crt" "$HOME/.local/bin/shadoc-agent" "$HOME/.local/bin/restic-control-agent"; rm -rf "$HOME/.local/share/shadoc-agent" "$HOME/.local/share/restic-control-agent"`
	windowsStopCommand            = `powershell.exe -NoProfile -NonInteractive -Command "$ErrorActionPreference='Stop';foreach($name in 'shadoc-agent','restic-control-agent'){$s=Get-Service $name -ErrorAction SilentlyContinue;if($s -and $s.Status -ne 'Stopped'){Stop-Service $name -Force -ErrorAction Stop;$s.WaitForStatus('Stopped',[TimeSpan]::FromSeconds(30))}}"`
	windowsRemoveCommand          = `powershell.exe -NoProfile -NonInteractive -Command "$ErrorActionPreference='Stop';foreach($name in 'shadoc-agent','restic-control-agent'){if(Get-Service $name -ErrorAction SilentlyContinue){sc.exe delete $name|Out-Null;if($LASTEXITCODE -ne 0){throw 'unable to delete Agent service'}}};Remove-Item -Recurse -Force (Join-Path $env:ProgramData 'shadoc-agent'),(Join-Path $env:ProgramData 'restic-control-agent') -ErrorAction SilentlyContinue"`
	linuxUpgradeUploadCommand     = `umask 077; mkdir -p "$HOME/.local/bin"; cat > "$HOME/.local/bin/shadoc-agent.upgrade"; chmod 0700 "$HOME/.local/bin/shadoc-agent.upgrade"`
	darwinUpgradeUploadCommand    = linuxUpgradeUploadCommand
	windowsUpgradeUploadCommand   = `powershell.exe -NoProfile -NonInteractive -Command "$r=Join-Path $env:ProgramData 'shadoc-agent';New-Item -ItemType Directory -Force $r|Out-Null;$f=[IO.File]::Open((Join-Path $r 'shadoc-agent.upgrade.exe'),[IO.FileMode]::Create,[IO.FileAccess]::Write);[Console]::OpenStandardInput().CopyTo($f);$f.Close()"`
	linuxUpgradeActivateCommand   = `set -eu; upgrade="$HOME/.local/bin/shadoc-agent.upgrade"; if [ -x "$HOME/.local/bin/shadoc-agent" ]; then active="$HOME/.local/bin/shadoc-agent"; service=shadoc-agent.service; else active="$HOME/.local/bin/restic-control-agent"; service=restic-control-agent.service; fi; previous="$active.previous"; test -x "$active"; test -x "$upgrade"; rm -f "$previous" "$active.failed"; mv "$active" "$previous"; mv "$upgrade" "$active"; if ! systemctl --user restart "$service"; then mv "$active" "$active.failed"; mv "$previous" "$active"; systemctl --user restart "$service"; exit 1; fi`
	darwinUpgradeActivateCommand  = `set -eu; domain="gui/$(id -u)"; upgrade="$HOME/.local/bin/shadoc-agent.upgrade"; if [ -x "$HOME/.local/bin/shadoc-agent" ]; then active="$HOME/.local/bin/shadoc-agent"; plist="$HOME/Library/LaunchAgents/io.shadoc-agent.plist"; else active="$HOME/.local/bin/restic-control-agent"; plist="$HOME/Library/LaunchAgents/io.restic-control-agent.plist"; fi; previous="$active.previous"; test -x "$active"; test -x "$upgrade"; rm -f "$previous" "$active.failed"; mv "$active" "$previous"; mv "$upgrade" "$active"; launchctl bootout "$domain" "$plist" >/dev/null 2>&1 || true; if ! launchctl bootstrap "$domain" "$plist"; then mv "$active" "$active.failed"; mv "$previous" "$active"; launchctl bootout "$domain" "$plist" >/dev/null 2>&1 || true; launchctl bootstrap "$domain" "$plist"; exit 1; fi`
	windowsUpgradeActivateCommand = `powershell.exe -NoProfile -NonInteractive -Command "$ErrorActionPreference='Stop';$newRoot=Join-Path $env:ProgramData 'shadoc-agent';if(Test-Path (Join-Path $newRoot 'shadoc-agent.exe')){$r=$newRoot;$name='shadoc-agent';$file='shadoc-agent.exe'}else{$r=Join-Path $env:ProgramData 'restic-control-agent';$name='restic-control-agent';$file='restic-control-agent.exe'};$a=Join-Path $r $file;$p=$a+'.previous';$u=Join-Path $newRoot 'shadoc-agent.upgrade.exe';Stop-Service $name -Force;Remove-Item $p -Force -ErrorAction SilentlyContinue;Move-Item $a $p;Move-Item $u $a;try{Start-Service $name}catch{Move-Item $a ($a+'.failed') -Force;Move-Item $p $a;Start-Service $name;throw}`
	linuxUpgradeRollbackCommand   = `set -eu; if [ -x "$HOME/.local/bin/shadoc-agent.previous" ]; then active="$HOME/.local/bin/shadoc-agent"; service=shadoc-agent.service; else active="$HOME/.local/bin/restic-control-agent"; service=restic-control-agent.service; fi; previous="$active.previous"; systemctl --user stop "$service" >/dev/null 2>&1 || true; if [ -x "$previous" ]; then rm -f "$active.failed"; if [ -e "$active" ]; then mv "$active" "$active.failed"; fi; mv "$previous" "$active"; fi; systemctl --user start "$service"`
	darwinUpgradeRollbackCommand  = `set -eu; domain="gui/$(id -u)"; if [ -x "$HOME/.local/bin/shadoc-agent.previous" ]; then active="$HOME/.local/bin/shadoc-agent"; plist="$HOME/Library/LaunchAgents/io.shadoc-agent.plist"; else active="$HOME/.local/bin/restic-control-agent"; plist="$HOME/Library/LaunchAgents/io.restic-control-agent.plist"; fi; previous="$active.previous"; launchctl bootout "$domain" "$plist" >/dev/null 2>&1 || true; if [ -x "$previous" ]; then rm -f "$active.failed"; if [ -e "$active" ]; then mv "$active" "$active.failed"; fi; mv "$previous" "$active"; fi; launchctl bootstrap "$domain" "$plist"`
	windowsUpgradeRollbackCommand = `powershell.exe -NoProfile -NonInteractive -Command "$ErrorActionPreference='Stop';$newRoot=Join-Path $env:ProgramData 'shadoc-agent';$newActive=Join-Path $newRoot 'shadoc-agent.exe';if(Test-Path ($newActive+'.previous')){$a=$newActive;$name='shadoc-agent'}else{$a=Join-Path (Join-Path $env:ProgramData 'restic-control-agent') 'restic-control-agent.exe';$name='restic-control-agent'};$p=$a+'.previous';Stop-Service $name -Force -ErrorAction SilentlyContinue;if(Test-Path $p){Remove-Item ($a+'.failed') -Force -ErrorAction SilentlyContinue;if(Test-Path $a){Move-Item $a ($a+'.failed') -Force};Move-Item $p $a};Start-Service $name"`
	linuxUpgradeFinalizeCommand   = `rm -f "$HOME/.local/bin/shadoc-agent.previous" "$HOME/.local/bin/shadoc-agent.upgrade" "$HOME/.local/bin/shadoc-agent.failed" "$HOME/.local/bin/restic-control-agent.previous" "$HOME/.local/bin/restic-control-agent.failed"`
	darwinUpgradeFinalizeCommand  = linuxUpgradeFinalizeCommand
	windowsUpgradeFinalizeCommand = `powershell.exe -NoProfile -NonInteractive -Command "$new=Join-Path $env:ProgramData 'shadoc-agent';$legacy=Join-Path $env:ProgramData 'restic-control-agent';Remove-Item (Join-Path $new 'shadoc-agent.exe.previous'),(Join-Path $new 'shadoc-agent.upgrade.exe'),(Join-Path $new 'shadoc-agent.exe.failed'),(Join-Path $legacy 'restic-control-agent.exe.previous'),(Join-Path $legacy 'restic-control-agent.exe.failed') -Force -ErrorAction SilentlyContinue"`
)

type Platform struct {
	OS      string
	Arch    string
	Service string
	Home    string
}

type RemoteFile string

const (
	BinaryFile         RemoteFile = "binary"
	CAFile             RemoteFile = "ca"
	TokenFile          RemoteFile = "token"
	ConfigFile         RemoteFile = "config"
	LinuxUnitFile      RemoteFile = "linux-unit"
	DarwinPlistFile    RemoteFile = "darwin-plist"
	WindowsServiceFile RemoteFile = "windows-service"
	UpgradeBinaryFile  RemoteFile = "upgrade-binary"
)

type CommandRunner interface {
	Run(context.Context, string, []byte) ([]byte, error)
}

type Remote struct {
	runner   CommandRunner
	platform Platform
}

func NewRemote(runner CommandRunner) *Remote { return &Remote{runner: runner} }

func (r *Remote) Probe(ctx context.Context) (Platform, error) {
	if r == nil || r.runner == nil {
		return Platform{}, errors.New("SSH command runner is required")
	}
	output, err := r.runner.Run(ctx, probeCommand, nil)
	if err != nil {
		output, err = r.runner.Run(ctx, windowsProbeCommand, nil)
		if err != nil {
			return Platform{}, fmt.Errorf("probe remote platform: %w", err)
		}
	}
	lines := splitOutputLines(string(output))
	if len(lines) < 4 {
		return Platform{}, errors.New("remote platform probe returned incomplete output")
	}
	platform := Platform{OS: normalizeOS(lines[0]), Arch: normalizeArch(lines[1]), Service: lines[2], Home: lines[3]}
	if (platform.OS != "windows" && !strings.HasPrefix(platform.Home, "/")) || (platform.OS == "windows" && (len(platform.Home) < 3 || platform.Home[1] != ':')) || strings.ContainsAny(platform.Home, "\x00\r\n") {
		return Platform{}, errors.New("remote home directory is invalid")
	}
	if platform.OS == "" || platform.Arch == "" {
		return Platform{}, fmt.Errorf("unsupported remote platform %s/%s", lines[0], lines[1])
	}
	if (platform.OS == "linux" && platform.Service != "systemd") || (platform.OS == "darwin" && platform.Service != "launchd") {
		return Platform{}, fmt.Errorf("remote %s user service manager is unavailable", platform.OS)
	}
	if platform.OS == "windows" && platform.Service != "windows-service" {
		return Platform{}, errors.New("remote Windows service manager is unavailable")
	}
	r.platform = platform
	return platform, nil
}

func splitOutputLines(output string) []string {
	lines := make([]string, 0, 4)
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func (r *Remote) Upload(ctx context.Context, file RemoteFile, content []byte) error {
	commands := map[RemoteFile]string{BinaryFile: binaryUploadCommand, CAFile: caUploadCommand, TokenFile: tokenUploadCommand, ConfigFile: configUploadCommand, LinuxUnitFile: linuxUnitUploadCommand, DarwinPlistFile: darwinPlistUploadCommand, UpgradeBinaryFile: linuxUpgradeUploadCommand}
	if r.platform.OS == "windows" {
		commands = map[RemoteFile]string{BinaryFile: windowsBinaryUploadCommand, CAFile: windowsCAUploadCommand, TokenFile: windowsTokenUploadCommand, WindowsServiceFile: windowsServiceUploadCommand, UpgradeBinaryFile: windowsUpgradeUploadCommand}
	}
	command, ok := commands[file]
	if !ok {
		return fmt.Errorf("unsupported Agent deployment file %q", file)
	}
	if len(content) == 0 {
		return fmt.Errorf("Agent deployment file %q is empty", file)
	}
	if _, err := r.runner.Run(ctx, command, content); err != nil {
		return fmt.Errorf("upload Agent %s: %w", file, err)
	}
	return nil
}

func (r *Remote) StageUpgrade(ctx context.Context, content []byte) error {
	return r.Upload(ctx, UpgradeBinaryFile, content)
}

func (r *Remote) ActivateUpgrade(ctx context.Context, platform Platform) error {
	command, err := upgradeCommand(platform.OS, linuxUpgradeActivateCommand, darwinUpgradeActivateCommand, windowsUpgradeActivateCommand)
	if err != nil {
		return err
	}
	if _, err := r.runner.Run(ctx, command, nil); err != nil {
		return fmt.Errorf("activate Agent upgrade: %w", err)
	}
	return nil
}

func (r *Remote) RollbackUpgrade(ctx context.Context, platform Platform) error {
	command, err := upgradeCommand(platform.OS, linuxUpgradeRollbackCommand, darwinUpgradeRollbackCommand, windowsUpgradeRollbackCommand)
	if err != nil {
		return err
	}
	if _, err := r.runner.Run(ctx, command, nil); err != nil {
		return fmt.Errorf("roll back Agent upgrade: %w", err)
	}
	return nil
}

func (r *Remote) FinalizeUpgrade(ctx context.Context, platform Platform) error {
	command, err := upgradeCommand(platform.OS, linuxUpgradeFinalizeCommand, darwinUpgradeFinalizeCommand, windowsUpgradeFinalizeCommand)
	if err != nil {
		return err
	}
	if _, err := r.runner.Run(ctx, command, nil); err != nil {
		return fmt.Errorf("finalize Agent upgrade: %w", err)
	}
	return nil
}

func (r *Remote) Restart(ctx context.Context, platform Platform) error {
	command, err := upgradeCommand(platform.OS, linuxRestartCommand, darwinRestartCommand, windowsRestartCommand)
	if err != nil {
		return err
	}
	if _, err := r.runner.Run(ctx, command, nil); err != nil {
		return fmt.Errorf("restart Agent: %w", err)
	}
	return nil
}

func upgradeCommand(osName, linux, darwin, windows string) (string, error) {
	switch osName {
	case "linux":
		return linux, nil
	case "darwin":
		return darwin, nil
	case "windows":
		return windows, nil
	default:
		return "", fmt.Errorf("unsupported Agent platform %q", osName)
	}
}

func (r *Remote) Activate(ctx context.Context, platform Platform) error {
	command, cleanup := "", ""
	switch platform.OS {
	case "linux":
		command, cleanup = linuxActivateCommand, linuxCleanupCommand
	case "darwin":
		command, cleanup = darwinActivateCommand, darwinCleanupCommand
	case "windows":
		command, cleanup = windowsActivateCommand, windowsCleanupCommand
	default:
		return fmt.Errorf("unsupported Agent platform %q", platform.OS)
	}
	if _, err := r.runner.Run(ctx, command, nil); err != nil {
		_, _ = r.runner.Run(context.WithoutCancel(ctx), cleanup, nil)
		return fmt.Errorf("activate Agent service: %w", err)
	}
	return nil
}

func (r *Remote) Finalize(ctx context.Context, platform Platform) error {
	command, err := upgradeCommand(platform.OS, linuxFinalizeCommand, darwinFinalizeCommand, windowsFinalizeCommand)
	if err != nil {
		return err
	}
	if _, err := r.runner.Run(ctx, command, nil); err != nil {
		return fmt.Errorf("finalize Agent service migration: %w", err)
	}
	return nil
}

func (r *Remote) Cleanup(ctx context.Context, platform Platform) error {
	command := linuxCleanupCommand
	if platform.OS == "darwin" {
		command = darwinCleanupCommand
	} else if platform.OS == "windows" {
		command = windowsCleanupCommand
	}
	_, err := r.runner.Run(ctx, command, nil)
	return err
}

func (r *Remote) Stop(ctx context.Context, platform Platform) error {
	command := linuxStopCommand
	if platform.OS == "darwin" {
		command = darwinStopCommand
	} else if platform.OS == "windows" {
		command = windowsStopCommand
	} else if platform.OS != "linux" {
		return fmt.Errorf("unsupported Agent platform %q", platform.OS)
	}
	if _, err := r.runner.Run(ctx, command, nil); err != nil {
		return fmt.Errorf("stop Agent: %w", err)
	}
	return nil
}

func (r *Remote) Remove(ctx context.Context, platform Platform) error {
	command := linuxRemoveCommand
	if platform.OS == "darwin" {
		command = darwinRemoveCommand
	} else if platform.OS == "windows" {
		command = windowsRemoveCommand
	} else if platform.OS != "linux" {
		return fmt.Errorf("unsupported Agent platform %q", platform.OS)
	}
	if _, err := r.runner.Run(ctx, command, nil); err != nil {
		return fmt.Errorf("remove Agent: %w", err)
	}
	return nil
}

func normalizeOS(value string) string {
	switch strings.ToLower(value) {
	case "linux":
		return "linux"
	case "darwin":
		return "darwin"
	case "windows":
		return "windows"
	default:
		return ""
	}
}

func normalizeArch(value string) string {
	switch strings.ToLower(value) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return ""
	}
}

type Target struct {
	Host       string
	Port       int
	Username   string
	PrivateKey []byte
	KnownHosts string
}

type SSHRemote struct {
	client *ssh.Client
}

func DialPinned(ctx context.Context, target Target) (*SSHRemote, error) {
	if target.Host == "" || target.Port < 1 || target.Port > 65535 || target.Username == "" || len(target.PrivateKey) == 0 || strings.TrimSpace(target.KnownHosts) == "" {
		return nil, errors.New("complete pinned SSH target is required")
	}
	signer, err := ssh.ParsePrivateKey(target.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("parse deployment SSH key: %w", err)
	}
	knownHostsFile, err := os.CreateTemp("", "shadoc-agent-known-hosts-*")
	if err != nil {
		return nil, err
	}
	path := knownHostsFile.Name()
	defer os.Remove(path)
	if err := knownHostsFile.Chmod(0o600); err != nil {
		_ = knownHostsFile.Close()
		return nil, err
	}
	if _, err := knownHostsFile.WriteString(strings.TrimSpace(target.KnownHosts) + "\n"); err != nil {
		_ = knownHostsFile.Close()
		return nil, err
	}
	if err := knownHostsFile.Close(); err != nil {
		return nil, err
	}
	hostKeyCallback, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))
	connection, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, &ssh.ClientConfig{User: target.Username, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: hostKeyCallback, Timeout: 10 * time.Second})
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	return &SSHRemote{client: ssh.NewClient(clientConnection, channels, requests)}, nil
}

func (r *SSHRemote) Run(ctx context.Context, command string, stdin []byte) ([]byte, error) {
	if r == nil || r.client == nil {
		return nil, errors.New("SSH client is closed")
	}
	session, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	if len(stdin) != 0 {
		session.Stdin = bytes.NewReader(stdin)
	}
	type completed struct {
		output []byte
		err    error
	}
	done := make(chan completed, 1)
	go func() {
		output, runErr := session.CombinedOutput(command)
		done <- completed{output: output, err: runErr}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			return result.output, fmt.Errorf("remote command failed: %w", result.err)
		}
		return result.output, nil
	case <-ctx.Done():
		_ = session.Close()
		return nil, ctx.Err()
	}
}

func (r *SSHRemote) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}
