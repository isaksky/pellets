#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

usage() {
  printf 'usage: %s (--write|--check) VERSION [RELEASE_DIRECTORY]\n' "${0##*/}" >&2
  exit 2
}

if [[ $# -lt 2 || $# -gt 3 ]]; then
  usage
fi

mode="$1"
version="$2"
version_pattern='^[0-9]+\.[0-9]+\.[0-9]+$'
if [[ "$mode" != "--write" && "$mode" != "--check" ]]; then
  usage
fi
if [[ ! "$version" =~ $version_pattern ]]; then
  printf 'Scoop stable VERSION must be a SemVer core without a leading v: %s\n' "$version" >&2
  exit 2
fi

for command_name in awk cmp cp diff mkdir mktemp python3 shasum; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf 'required command not found: %s\n' "$command_name" >&2
    exit 1
  fi
done

script_directory="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(CDPATH= cd -- "$script_directory/.." && pwd)"
release_directory="${3:-$repository_root/dist}"
manifest_path="$repository_root/bucket/pl.json"
checksum_name="pellets_${version}_checksums.txt"
checksum_path="$release_directory/$checksum_name"
archive_name="pellets_${version}_windows_amd64.zip"
archive_path="$release_directory/$archive_name"

if [[ ! -f "$checksum_path" ]]; then
  printf 'release checksum manifest not found: %s\n' "$checksum_path" >&2
  exit 1
fi
if [[ ! -f "$archive_path" ]]; then
  printf 'release archive not found: %s\n' "$archive_path" >&2
  exit 1
fi

manifest_hash="$(
  awk -v archive_name="$archive_name" '
    $2 == archive_name && $1 ~ /^[0-9a-f]{64}$/ { print $1 }
  ' "$checksum_path"
)"
if [[ $(printf '%s\n' "$manifest_hash" | awk 'NF { count++ } END { print count + 0 }') -ne 1 ]]; then
  printf 'checksum manifest must contain exactly one SHA-256 for %s\n' "$archive_name" >&2
  exit 1
fi
actual_hash="$(shasum -a 256 "$archive_path" | awk '{ print $1 }')"
if [[ "$actual_hash" != "$manifest_hash" ]]; then
  printf 'release archive checksum mismatch for %s: got %s, want %s\n' \
    "$archive_name" "$actual_hash" "$manifest_hash" >&2
  exit 1
fi

work_directory="$(mktemp -d "${TMPDIR:-/tmp}/pellets-scoop-manifest.XXXXXX")"
cleanup() {
  find "$work_directory" -depth -mindepth 1 -delete
  rmdir "$work_directory"
}
trap cleanup EXIT
rendered_manifest="$work_directory/pl.json"

{
  printf '%s\n' \
    '{' \
    "    \"version\": \"$version\"," \
    '    "homepage": "https://github.com/isaksky/pellets",' \
    '    "license": "Apache-2.0",' \
    '    "architecture": {' \
    '        "64bit": {' \
    "            \"url\": \"https://github.com/isaksky/pellets/releases/download/v$version/$archive_name\"," \
    "            \"hash\": \"$manifest_hash\"" \
    '        }' \
    '    },' \
    '    "bin": "pl.exe"' \
    '}'
} >"$rendered_manifest"

"$script_directory/check-scoop-manifest.py" "$rendered_manifest"

if [[ "$mode" == "--write" ]]; then
  mkdir -p "$(dirname -- "$manifest_path")"
  cp "$rendered_manifest" "$manifest_path"
  printf 'updated %s for release %s\n' "$manifest_path" "$version"
  exit 0
fi

if [[ ! -f "$manifest_path" ]]; then
  printf 'Scoop manifest not found: %s\n' "$manifest_path" >&2
  exit 1
fi
"$script_directory/check-scoop-manifest.py" "$manifest_path"
if ! cmp -s "$rendered_manifest" "$manifest_path"; then
  printf 'Scoop manifest does not match release %s inputs:\n' "$version" >&2
  diff -u "$manifest_path" "$rendered_manifest" >&2 || true
  exit 1
fi
printf 'verified %s against release %s inputs\n' "$manifest_path" "$version"
