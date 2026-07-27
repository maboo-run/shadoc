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

printf '%s\n' '#!/bin/sh' \
  'if [ "${1:-}" = "-u" ]; then printf "%s\n" "${SHADOC_INSTALL_TEST_UID:-1000}"; exit 0; fi' \
  'exit 1' \
  > "$fake_bin/id"
chmod 0755 "$fake_bin/id"

printf '%s\n' '#!/bin/sh' \
  'case "${1:-}" in' \
  '  -s) printf "Linux\n" ;;' \
  '  -m) printf "x86_64\n" ;;' \
  '  *) exit 1 ;;' \
  'esac' \
  > "$fake_bin/uname"
chmod 0755 "$fake_bin/uname"

PATH="$fake_bin:$PATH" \
SHADOC_INSTALL_TEST_RELEASE="$release_directory" \
SHADOC_INSTALL_TEST_RECORD="$record" \
SHADOC_INSTALL_TEST_DOWNLOAD_LOG="$download_log" \
SHADOC_INSTALL_TEST_UID=1000 \
SHADOC_DATA_DIR="$temporary_directory/data" \
SHADOC_INSTALL_AGENTS=1 \
  "$script_dir/install.sh" >/dev/null

[ "$(cat "$record")" = "install-app" ]
for asset in SHA256SUMS \
  shadoc-agent-linux-amd64 shadoc-agent-linux-arm64 \
  shadoc-agent-darwin-amd64 shadoc-agent-darwin-arm64 \
  shadoc-agent-windows-amd64.exe shadoc-agent-windows-arm64.exe; do
  grep -Fx "$asset" "$download_log" >/dev/null
done

root_record="$temporary_directory/root-install-record"
root_output="$temporary_directory/root-install-output"
if PATH="$fake_bin:$PATH" \
  SHADOC_INSTALL_TEST_RELEASE="$release_directory" \
  SHADOC_INSTALL_TEST_RECORD="$root_record" \
  SHADOC_INSTALL_TEST_DOWNLOAD_LOG="$download_log" \
  SHADOC_INSTALL_TEST_UID=0 \
  SHADOC_INSTALL_AGENTS=0 \
  "$script_dir/install.sh" >/dev/null 2>&1; then
  printf 'root installation unexpectedly succeeded without explicit confirmation\n' >&2
  exit 1
fi

PATH="$fake_bin:$PATH" \
SHADOC_INSTALL_TEST_RELEASE="$release_directory" \
SHADOC_INSTALL_TEST_RECORD="$root_record" \
SHADOC_INSTALL_TEST_DOWNLOAD_LOG="$download_log" \
SHADOC_INSTALL_TEST_UID=0 \
SHADOC_INSTALL_AGENTS=0 \
SHADOC_ALLOW_ROOT=1 \
  "$script_dir/install.sh" >"$root_output"

[ "$(cat "$root_record")" = "install-app --system" ]
grep -F 'status --system' "$root_output" >/dev/null

printf 'installer verification passed\n'
