# Shadoc

<p align="center">
  <a href="README.md">简体中文</a> · <strong>English</strong>
</p>

<p align="center">
  <img src="web/public/shadoc-icon.png" alt="Shadoc icon" width="120" height="120">
</p>

<p align="center">
  A self-hosted backup control service for individuals and small teams.
</p>

<p align="center">
  <a href="https://github.com/maboo-run/shadoc/actions/workflows/ci.yml"><img src="https://github.com/maboo-run/shadoc/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/maboo-run/shadoc/releases"><img src="https://img.shields.io/github/v/release/maboo-run/shadoc" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT License"></a>
</p>

Shadoc runs on a backup node that owns or can access the source data. A persistent Go control service manages configuration, scheduling, security gates, and state, while the embedded management UI handles day-to-day operations. Closing the browser does not stop running tasks. Restic, rsync, and official database clients perform the actual data processing.

The current `0.x` series is a public preview. Before upgrading, read the Release Notes for the target version and retain your control-plane recovery bundle and repository credentials.

## Core Features

- **Versioned backups**: Use Restic to create encrypted, deduplicated, incremental snapshots with retention, checking, maintenance, and restore support.
- **Directories and databases**: Protect local or Agent-accessible directories, as well as logical backups of individual MySQL and PostgreSQL databases.
- **One-way incremental synchronization**: An explicitly selected rsync engine is available; it does not provide snapshot, retention, or restore semantics.
- **Multiple repository types**: Use local directories, SFTP with pinned SSH host keys, or structured S3-compatible object storage.
- **Safe restores**: Run read-only preflight checks and administrator reauthentication before a restore. Directories are restored only to a new destination, and databases never overwrite a non-empty target.
- **Remote Agents**: Use a dedicated TLS 1.3 mTLS channel, one-time enrollment tokens, capability detection, and short-lived task leases.
- **Persistent execution**: The background service manages tasks, schedules, and long-running operations. Refreshing or closing the management UI does not interrupt them.
- **Secure defaults**: No arbitrary shell, script, command-argument, or environment-variable execution interface. Secrets are encrypted at rest, and logs are redacted before storage.
- **Operational visibility**: Review run history, capacity trends, alerts, notification delivery, and audit records that cannot be modified individually.
- **Bilingual interface**: Switch the management UI between Simplified Chinese and English.

## Platform Support

| Component | Linux amd64 | Linux arm64 | macOS Intel | macOS Apple Silicon | Windows amd64/arm64 |
| --- | --- | --- | --- | --- | --- |
| Control service | Supported | Supported | Supported | Supported | Not supported |
| Remote Agent | Supported | Supported | Supported | Supported | Supported |

The Linux control service runs as a systemd user service, while macOS uses a LaunchAgent. Access the management UI with a modern browser; automated acceptance testing uses Chrome/Chromium.

## Installation

### Install the Latest Stable Release

Run the installer as a regular service account. Do not use `root` by default:

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh | sh
```

The installer detects the operating system and architecture, downloads the control service, Agent artifacts for all supported platforms, and `SHA256SUMS` from the same GitHub Release, verifies every SHA-256 digest, and then invokes the built-in installation command. It does not install system packages or expose the management UI to the public internet.

To inspect the script before running it:

```bash
curl -fsSLO https://github.com/maboo-run/shadoc/releases/latest/download/install.sh
less install.sh
sh install.sh
```

### Install a Specific Version

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh \
  | SHADOC_VERSION=0.1.0 sh
```

To install only the control service without downloading Agent artifacts used for remote deployment:

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh \
  | SHADOC_INSTALL_AGENTS=0 sh
```

The installed service listens on `127.0.0.1:8585` by default. To customize the data directory or listen address, pass environment variables to the `sh` process that runs the installer:

```bash
curl -fsSL https://github.com/maboo-run/shadoc/releases/latest/download/install.sh \
  | SHADOC_DATA_DIR=/srv/shadoc SHADOC_LISTEN=127.0.0.1:9090 sh
```

## Quick Start

1. Open [http://127.0.0.1:8585](http://127.0.0.1:8585).
2. Create the single administrator account with a password of at least 12 characters.
3. Open the Compatibility Center and confirm that Restic and the tools you plan to use are ready.
4. Create a local, SFTP, or S3 repository, then initialize it or complete read-only connection verification.
5. Create a backup task, review its protection scope, and then enable it.
6. Configure the task schedule and repository retention policy.
7. Run the task manually once, then review the result and redacted logs in Run History.
8. Perform a restore drill into a new destination to confirm that the backup is recoverable.

Each Restic task exclusively owns one repository. Do not combine unrelated data sources in the same task repository.

## Dependencies

| Tool | When it is needed | Requirements and installation |
| --- | --- | --- |
| Restic | Versioned directory or database backups, restores, and maintenance | `0.17.0` or later. Install an application-managed version from the Compatibility Center, or reuse a detected system installation. |
| rsync | One-way incremental synchronization tasks | `3.x`; installed by the operating system or administrator. |
| `mysqldump`, `mysql` | Logical MySQL backup and restore | Install official clients compatible with the target database. Shadoc does not install database clients automatically. |
| `pg_dump`, `pg_restore` | Logical PostgreSQL backup and restore | Install official clients compatible with the target database. Shadoc does not install database clients automatically. |
| SSH/SFTP service | SFTP repositories, SSH-based rsync, and remote Agent deployment | Obtain and verify the actual host key. Shadoc never silently accepts unknown or changed host keys. |

If `restic` works in a terminal but initialization says it is missing, the macOS LaunchAgent or another background service usually did not inherit the terminal `PATH`. The control Service prefers the application-managed Restic, then checks the service `PATH` and common system locations; if none is available, install Restic from the Compatibility Center and restart the control Service. Initialization errors now distinguish a missing Restic executable from a repository-directory problem.

During Service startup, Shadoc resolves and probes the local Restic/rsync executable paths and caches the resulting compatibility report. The Compatibility Center, diagnostics export, and actual tasks reuse that result instead of probing again for each page; restart the control Service after installing or replacing a tool. Remote Agent tool versions and each database connection's official clients are still verified independently.

The common database connection form only requires a name, database type/purpose, address, account, password, and TLS mode. “Test connection” uses the built-in Go driver for network, TLS, authentication, and purpose-permission checks; actual backup and restore still invoke `mysqldump`/`mysql` or `pg_dump`/`pg_restore`. The control Service automatically discovers the required official clients on the system `PATH`; enter absolute paths in Advanced settings only when discovery fails. MySQL “preferred TLS” uses the client’s portable default so MariaDB and older MySQL clients do not fail on the unsupported `--ssl-mode` option. Shadoc does not install database clients automatically.

When a database task is saved with “Preflight and enable”, Shadoc first saves a disabled draft and starts a durable lightweight preflight: it verifies read-only access to the Restic repository and runs the official dump client in `--no-data`/`--schema-only` mode without creating a database backup snapshot. Only after it succeeds does the task become enabled; a failure leaves the task disabled and exposes the diagnostic in the operation details. Enabled schedules are not stopped because a preflight is older than 24 hours; actual runs still record real export or repository failures.

Database backups stream logical exports directly into Restic. Shadoc does not copy live database files or write an unencrypted intermediate export to disk.

## Service Management

After a one-line installation, the installer prints the absolute path of the managed command. The default paths are:

- Linux: `$XDG_CONFIG_HOME/shadoc/app/shadoc`, or `$HOME/.config/shadoc/app/shadoc` when `XDG_CONFIG_HOME` is unset.
- macOS: `$HOME/Library/Application Support/shadoc/app/shadoc`.
- Custom installation: `$SHADOC_DATA_DIR/app/shadoc`.

The examples below first save the actual path in `SHADOC_BIN`:

```bash
# Default Linux installation
SHADOC_BIN="${XDG_CONFIG_HOME:-$HOME/.config}/shadoc/app/shadoc"

# For the default macOS installation, use:
# SHADOC_BIN="$HOME/Library/Application Support/shadoc/app/shadoc"
```

Common commands:

```bash
"$SHADOC_BIN" status
"$SHADOC_BIN" start
"$SHADOC_BIN" start --port 9090
"$SHADOC_BIN" restart
"$SHADOC_BIN" stop
"$SHADOC_BIN" help
```

`stop` stops only the control service. It does not delete tasks, secrets, run history, or backup repositories.

## Upgrading and Uninstalling

Upgrade to the latest stable release:

```bash
"$SHADOC_BIN" update-app
```

Upgrade to a specific stable release:

```bash
"$SHADOC_BIN" update-app --version 0.2.0
```

The upgrade process downloads the official platform artifact, verifies its SHA-256 digest against the Release, saves the previous binary, atomically replaces it, and restarts the service. If the new version fails its health check, Shadoc attempts to restore the previous binary. Database migrations cannot be undone by replacing the binary, so export a control-plane recovery bundle and read the Release Notes before upgrading.

Uninstall the control service and managed programs while retaining application data:

```bash
"$SHADOC_BIN" uninstall-app
```

Permanently deleting application data requires an explicit option and interactive confirmation:

```bash
"$SHADOC_BIN" uninstall-app --remove-data
```

Uninstalling Shadoc does not delete existing Restic repositories on local storage, SFTP, or S3.

## Configuration

| Environment variable | Description | Default |
| --- | --- | --- |
| `SHADOC_DATA_DIR` | Directory for SQLite, the secret vault, managed tools, and runtime data | The platform user configuration directory under `shadoc` |
| `SHADOC_LISTEN` | Management UI listen address | `127.0.0.1:8585` |
| `SHADOC_AGENT_SERVICE` | HTTPS address of the control service used by an Agent | None |
| `SHADOC_AGENT_DATA_DIR` | Directory for Agent certificates and runtime data | `./agent-data` |
| `SHADOC_AGENT_ALLOWED_ROOTS` | Comma-separated absolute root directories that an Agent may access | Platform root directories |

Legacy `RESTIC_CONTROL_*` variables are retained only for migration compatibility. When both old and new variables configure the same setting, `SHADOC_*` takes precedence.

## Data Privacy and Security

- Shadoc is self-hosted. It does not provide a hosted cloud service and contains no analytics or telemetry reporting.
- Configuration, SQLite data, encrypted secrets, and run history remain in the selected data directory. Backup contents are written to repositories configured by the administrator.
- Repository passwords, SSH private keys, database passwords, and notification tokens are stored in the local encrypted secret vault, never as plaintext in SQLite, audit records, or task leases.
- The management endpoint uses local HTTP by default and listens only on `127.0.0.1`. Do not expose it directly to the public internet. For access from another device, use a trusted VPN or an authenticated HTTPS reverse proxy.
- Shadoc accesses external networks only when an explicit feature requires it, including GitHub Releases, administrator-configured repositories, Agents, ntfy, or Webhook endpoints.
- High-impact operations such as restore and deletion require preflight checks, impact confirmation, or administrator reauthentication. Administrators should still perform independent restore drills regularly.
- Review diagnostics and Issue content before submission. Never publish passwords, tokens, private keys, raw logs, or identifiable internal paths.

Report security vulnerabilities through GitHub private vulnerability reporting as described in [SECURITY.md](SECURITY.md). Do not open a public Issue.

## License

Shadoc is available under the [MIT License](LICENSE).
