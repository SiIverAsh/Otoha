#!/usr/bin/env bash
set -euo pipefail

version_script="${BASH_SOURCE[0]%/*}/next-release-version.sh"

check_version() {
  local bump="$1"
  local expected="$2"
  local tags="$3"
  local actual
  actual=$(bash "$version_script" "$bump" <<< "$tags")
  if [[ "$actual" != "$expected" ]]; then
    printf 'Expected %s increment to yield %s, got %s\n' "$bump" "$expected" "$actual" >&2
    exit 1
  fi
}

check_version major 1.0.0 ''
check_version minor 0.1.0 'nightly'
check_version patch 0.0.1 $'nightly\nv1.0.0-rc.1'

tags=$'v1.9.9\nv1.10.3\nv1.10.2\nv2.0.0-rc.1\nv01.20.30\nnightly'
check_version major 2.0.0 "$tags"
check_version minor 1.11.0 "$tags"
check_version patch 1.10.4 "$tags"

if bash "$version_script" invalid <<< "$tags" 2>/dev/null; then
  printf '%s\n' 'Expected an invalid version increment to fail.' >&2
  exit 1
fi
