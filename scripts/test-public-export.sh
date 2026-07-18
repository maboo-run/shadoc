#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
archive_file=$(mktemp "${TMPDIR:-/tmp}/shadoc-public-export.XXXXXX")
trap 'rm -f "$archive_file"' EXIT HUP INT TERM

git -C "$repository_root" archive --worktree-attributes --format=tar HEAD > "$archive_file"

if tar -tf "$archive_file" | grep -Eq '(^|/)AGENTS\.md$|^docs(/|$)|^learning-records(/|$)'; then
  printf 'private contributor files leaked into the public export\n' >&2
  exit 1
fi

for required_file in README.md LICENSE .goreleaser.yml .github/workflows/ci.yml .github/workflows/package.yml .github/workflows/release.yml; do
  if ! git -C "$repository_root" ls-files --error-unmatch "$required_file" >/dev/null 2>&1; then
    continue
  fi
  if ! tar -tf "$archive_file" | grep -Fx "$required_file" >/dev/null; then
    printf 'public export is missing required file: %s\n' "$required_file" >&2
    exit 1
  fi
done

printf 'public source export verification passed\n'
