#!/bin/sh
set -eu

version=${1:-}

if ! printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
  printf 'invalid release version %s; expected x.y.z without leading zeroes\n' "${version:-<empty>}" >&2
  exit 1
fi
