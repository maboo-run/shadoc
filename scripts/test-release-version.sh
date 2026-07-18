#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
validator="$script_dir/validate-release-version.sh"

for version in 0.1.0 1.0.0 10.24.300; do
  "$validator" "$version"
done

for version in '' v1.2.3 1.2 1.2.3-rc.1 01.2.3 1.02.3 1.2.03 latest; do
  if "$validator" "$version" >/dev/null 2>&1; then
    printf 'invalid version was accepted: %s\n' "$version" >&2
    exit 1
  fi
done

printf 'release version validation passed\n'
