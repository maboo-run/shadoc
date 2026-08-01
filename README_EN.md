# Shadoc

<p align="center">
  <a href="README.md">简体中文</a> · <strong>English</strong>
</p>

<p align="center">
  <img src="web/public/shadoc-icon.png" alt="Shadoc icon" width="120" height="120">
</p>

<p align="center">A self-hosted backup control service for individuals and small teams.</p>

<p align="center">
  <a href="https://github.com/maboo-run/shadoc/actions/workflows/ci.yml"><img src="https://github.com/maboo-run/shadoc/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/maboo-run/shadoc/releases"><img src="https://img.shields.io/github/v/release/maboo-run/shadoc" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT License"></a>
</p>

Shadoc runs a persistent Go control service on a backup node and provides a browser-based management UI. Tasks, schedules, and long-running operations continue after the page is closed.

## Features

- Encrypted, deduplicated, incremental snapshots with Restic, including retention, maintenance, and restore.
- Explicit one-way incremental synchronization with rsync; rsync does not provide snapshots, retention, or restore semantics.
- Directory protection on the local node or a remote Agent, plus logical MySQL and PostgreSQL backups.
- Local repositories, SFTP with verified SSH host keys, and structured S3-compatible repositories.
- Remote Agent management over TLS 1.3 mTLS with one-time registration tokens and short-lived leases.
- Run history, capacity status, alerts, notifications, and audit records.

## Supported Platforms

| Component | Linux amd64/arm64 | macOS Intel/Apple Silicon | Windows amd64/arm64 |
| --- | --- | --- | --- |
| Control service | Supported | Supported | Not supported |
| Remote Agent | Supported | Supported | Supported |

## Installation

### Linux and macOS user service

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh | sh
```

The installer downloads the current stable control service, Agent artifacts, and `SHA256SUMS`, verifies them, and invokes Shadoc's built-in installer. Open the management address printed by the installer.

### Linux root system service

Install a Linux root systemd service when the control service must read system-wide or other users' source data:

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh \
  | sudo env SHADOC_ALLOW_ROOT=1 sh
```

Root mode uses fixed paths:

- Program: `/var/lib/shadoc/app/shadoc`
- Data directory: `/var/lib/shadoc`
- systemd unit: `/etc/systemd/system/shadoc.service`

Manage the root service:

```bash
sudo /var/lib/shadoc/app/shadoc status --system
sudo /var/lib/shadoc/app/shadoc start --system
sudo /var/lib/shadoc/app/shadoc restart --system
sudo /var/lib/shadoc/app/shadoc stop --system
```

Root mode has broader file access and increases the impact of unsafe task configuration or host compromise. The management UI remains HTTP; for access from another device, use a trusted network or an authenticated HTTPS reverse proxy.

### Migrate an existing user service to root

Save a control-plane recovery bundle before migrating, then run:

```bash
SHADOC_BIN="${XDG_CONFIG_HOME:-$HOME/.config}/shadoc/app/shadoc"
sudo "$SHADOC_BIN" migrate-to-root
```

Migration verifies the managed program offline, copies the data, installs the root systemd service, and removes the old user service only after the health check succeeds. A failed migration preserves the user instance.

## Quick Start

1. Open the management address printed during installation.
2. Create the single administrator account.
3. Check Restic, rsync, and database clients in Compatibility Center.
4. Create and verify a repository.
5. Create a backup task, review its protection scope, and enable it.
6. Configure schedules and repository retention.
7. Run the task once and perform a restore drill to a new target.

### Choose an engine

| Engine | Use it when |
| --- | --- |
| Restic | You need snapshots, encryption, retention, maintenance, and restore |
| rsync | You need one-way incremental synchronization without snapshots or restore |

Each Restic task owns one repository. Do not put unrelated data sources in the same task repository.

## Service Management

The installer prints the path to the managed user-service program. Common commands:

```bash
# Replace this with the path printed by the installer
SHADOC_BIN="/path/to/shadoc"
"$SHADOC_BIN" status
"$SHADOC_BIN" start
"$SHADOC_BIN" restart
"$SHADOC_BIN" stop
```

`stop` stops only the control service; it does not delete tasks, secrets, run history, or backup repositories. Use the `--system` commands above for a root system service.

## Uninstall

Remove the service and managed programs while retaining tasks, secrets, run history, and other application data:

```bash
# User service: replace with the path printed by the installer
"$SHADOC_BIN" uninstall-app

# Root system service
sudo /var/lib/shadoc/app/shadoc uninstall-app --system
```

To permanently remove application data, add `--remove-data`. The command requires typing `REMOVE`:

```bash
# User service
"$SHADOC_BIN" uninstall-app --remove-data

# Root system service
sudo /var/lib/shadoc/app/shadoc uninstall-app --system --remove-data
```

Uninstalling Shadoc does not delete existing backup contents in local, SFTP, or S3 repositories.

## Security Boundaries

- No arbitrary shell, scripts, command arguments, or environment-variable execution.
- Repository passwords, SSH private keys, database passwords, and notification tokens stay in the local encrypted secret vault.
- SSH host keys must be verified; unknown or changed host keys are not silently accepted.
- Restore, deletion, and long-running administrative actions use preflight checks, confirmation, reauthentication, or durable polling.
- Do not expose the management UI directly to the public internet. Report vulnerabilities through the private channel described in [SECURITY.md](SECURITY.md).

## License

Shadoc is available under the [MIT License](LICENSE).
