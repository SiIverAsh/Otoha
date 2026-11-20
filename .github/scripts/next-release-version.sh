#!/usr/bin/env bash
set -euo pipefail

# Read one Git tag per line and use only canonical stable release versions.
version_component='(0|[1-9][0-9]*)'
latest_tag=$(
  awk -v component="$version_component" \
    '$0 ~ "^v" component "[.]" component "[.]" component "$"' |
    LC_ALL=C sort -Vr |
    sed -n '1p'
)
base_version="${latest_tag:-v0.0.0}"
IFS=. read -r major minor patch <<< "${base_version#v}"

case "${1:-}" in
  major) printf '%s\n' "$((major + 1)).0.0" ;;
  minor) printf '%s\n' "$major.$((minor + 1)).0" ;;
  patch) printf '%s\n' "$major.$minor.$((patch + 1))" ;;
  *) printf '%s\n' 'Expected a version increment of major, minor, or patch.' >&2; exit 1 ;;
esac
