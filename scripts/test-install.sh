#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/shadoc-install-test.XXXXXX")
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM

release_directory="$temporary_directory/release"
fake_bin="$temporary_directory/bin"
record="$temporary_directory/install-record"
download_log="$temporary_directory/download-log"
mkdir -p "$release_directory" "$fake_bin"

printf '#!/bin/sh\nprintf "%%s\\n" "$*" > "$SHADOC_INSTALL_TEST_RECORD"\n' > "$temporary_directory/control"
chmod 0755 "$temporary_directory/control"

for asset in \
  shadoc_linux_amd64 shadoc_linux_arm64 \
  shadoc_darwin_amd64 shadoc_darwin_arm64; do
  cp "$temporary_directory/control" "$release_directory/$asset"
done

for asset in \
  shadoc-agent-linux-amd64 shadoc-agent-linux-arm64 \
  shadoc-agent-darwin-amd64 shadoc-agent-darwin-arm64 \
  shadoc-agent-windows-amd64.exe shadoc-agent-windows-arm64.exe; do
  printf 'fixture:%s\n' "$asset" > "$release_directory/$asset"
done

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$release_directory" && sha256sum shadoc_* shadoc-agent-* > SHA256SUMS)
else
  (cd "$release_directory" && shasum -a 256 shadoc_* shadoc-agent-* > SHA256SUMS)
fi

printf '%s\n' '#!/bin/sh' \
  'set -eu' \
  'url=' \
  'output=' \
  'while [ "$#" -gt 0 ]; do' \
  '  case "$1" in' \
  '    -o) output=$2; shift 2 ;;' \
  '    https://*) url=$1; shift ;;' \
  '    *) shift ;;' \
  '  esac' \
  'done' \
  '[ -n "$url" ] && [ -n "$output" ]' \
  'asset=${url##*/}' \
  'printf "%s\n" "$asset" >> "$SHADOC_INSTALL_TEST_DOWNLOAD_LOG"' \
  'cp "$SHADOC_INSTALL_TEST_RELEASE/$asset" "$output"' \
  > "$fake_bin/curl"
chmod 0755 "$fake_bin/curl"

PATH="$fake_bin:$PATH" \
SHADOC_INSTALL_TEST_RELEASE="$release_directory" \
SHADOC_INSTALL_TEST_RECORD="$record" \
SHADOC_INSTALL_TEST_DOWNLOAD_LOG="$download_log" \
SHADOC_DATA_DIR="$temporary_directory/data" \
SHADOC_INSTALL_AGENTS=1 \
SHADOC_ALLOW_ROOT=1 \
  "$script_dir/install.sh" >/dev/null

[ "$(cat "$record")" = "install-app" ]
for asset in SHA256SUMS \
  shadoc-agent-linux-amd64 shadoc-agent-linux-arm64 \
  shadoc-agent-darwin-amd64 shadoc-agent-darwin-arm64 \
  shadoc-agent-windows-amd64.exe shadoc-agent-windows-arm64.exe; do
  grep -Fx "$asset" "$download_log" >/dev/null
done

printf 'installer verification passed\n'
