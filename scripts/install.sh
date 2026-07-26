#!/bin/sh
set -eu

repository="maboo-run/shadoc"
requested_version=${SHADOC_VERSION:-latest}
install_agents=${SHADOC_INSTALL_AGENTS:-1}
install_as_root=0

fail() {
  printf 'Shadoc installation failed: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

require_command curl
require_command grep
require_command awk
require_command uname
require_command mktemp

if [ "$(id -u)" -eq 0 ]; then
  [ "${SHADOC_ALLOW_ROOT:-0}" = "1" ] ||
    fail "refusing to install as root; set SHADOC_ALLOW_ROOT=1 after reviewing the risk"
  install_as_root=1
fi

case "$install_agents" in
  0|1) ;;
  *) fail "SHADOC_INSTALL_AGENTS must be 0 or 1" ;;
esac

case "$(uname -s)" in
  Linux) platform=linux ;;
  Darwin) platform=darwin ;;
  *) fail "the control service supports only Linux and macOS" ;;
esac

if [ "$install_as_root" = "1" ]; then
  [ "$platform" = "linux" ] || fail "root system service installation is currently supported only on Linux"
  if [ -n "${SHADOC_DATA_DIR:-}" ] && [ "$SHADOC_DATA_DIR" != "/var/lib/shadoc" ]; then
    fail "root system service data directory must be /var/lib/shadoc"
  fi
  SHADOC_DATA_DIR=/var/lib/shadoc
  export SHADOC_DATA_DIR
fi

case "$(uname -m)" in
  x86_64|amd64) architecture=amd64 ;;
  arm64|aarch64) architecture=arm64 ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ "$requested_version" = "latest" ]; then
  release_base="https://github.com/$repository/releases/latest/download"
else
  version=${requested_version#v}
  if ! printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
    fail "SHADOC_VERSION must be latest or x.y.z"
  fi
  release_base="https://github.com/$repository/releases/download/v$version"
fi

temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/shadoc-install.XXXXXX")
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM

download() {
  asset=$1
  printf 'Downloading %s\n' "$asset"
  curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
    --retry 3 --connect-timeout 15 \
    "$release_base/$asset" -o "$temporary_directory/$asset"
}

checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    fail "sha256sum or shasum is required"
  fi
}

verify() {
  asset=$1
  expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1 }' "$temporary_directory/SHA256SUMS")
  [ -n "$expected" ] || fail "SHA256SUMS does not contain $asset"
  [ "${#expected}" -eq 64 ] || fail "invalid SHA-256 entry for $asset"
  actual=$(checksum "$temporary_directory/$asset")
  [ "$actual" = "$expected" ] || fail "SHA-256 mismatch for $asset"
}

download SHA256SUMS

control_asset="shadoc_${platform}_${architecture}"
assets=$control_asset
if [ "$install_agents" = "1" ]; then
  assets="$assets
shadoc-agent-linux-amd64
shadoc-agent-linux-arm64
shadoc-agent-darwin-amd64
shadoc-agent-darwin-arm64
shadoc-agent-windows-amd64.exe
shadoc-agent-windows-arm64.exe"
fi

for asset in $assets; do
  download "$asset"
  verify "$asset"
done

for asset in $assets; do
  case "$asset" in
    *.exe) ;;
    *) chmod 0755 "$temporary_directory/$asset" ;;
  esac
done

if [ "$install_as_root" = "1" ]; then
  "$temporary_directory/$control_asset" install-app --system
else
  "$temporary_directory/$control_asset" install-app
fi

if [ -n "${SHADOC_DATA_DIR:-}" ]; then
  managed_binary="$SHADOC_DATA_DIR/app/shadoc"
elif [ "$platform" = "darwin" ]; then
  managed_binary="$HOME/Library/Application Support/shadoc/app/shadoc"
else
  managed_binary="${XDG_CONFIG_HOME:-$HOME/.config}/shadoc/app/shadoc"
fi

printf '\nShadoc is installed and running.\n'
printf 'Configured management listener: %s\n' "${SHADOC_LISTEN:-127.0.0.1:8585}"
printf 'Managed command: %s\n' "$managed_binary"
if [ "$install_as_root" = "1" ]; then
  printf 'Check status with: sudo "%s" status --system\n' "$managed_binary"
else
  printf 'Check status with: "%s" status\n' "$managed_binary"
fi
