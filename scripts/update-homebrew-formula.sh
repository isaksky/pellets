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
  printf 'Homebrew stable VERSION must be a SemVer core without a leading v: %s\n' "$version" >&2
  exit 2
fi

for command_name in awk cmp diff mkdir mktemp shasum; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf 'required command not found: %s\n' "$command_name" >&2
    exit 1
  fi
done

script_directory="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(CDPATH= cd -- "$script_directory/.." && pwd)"
release_directory="${3:-$repository_root/dist}"
formula_path="$repository_root/Formula/pl.rb"
checksum_name="pellets_${version}_checksums.txt"
checksum_path="$release_directory/$checksum_name"

if [[ ! -f "$checksum_path" ]]; then
  printf 'release checksum manifest not found: %s\n' "$checksum_path" >&2
  exit 1
fi

archive_hash() {
  local archive_name="$1"
  local archive_path="$release_directory/$archive_name"
  local manifest_hash
  local actual_hash

  if [[ ! -f "$archive_path" ]]; then
    printf 'release archive not found: %s\n' "$archive_path" >&2
    return 1
  fi
  manifest_hash="$(
    awk -v archive_name="$archive_name" '
      $2 == archive_name && $1 ~ /^[0-9a-f]{64}$/ { print $1 }
    ' "$checksum_path"
  )"
  if [[ $(printf '%s\n' "$manifest_hash" | awk 'NF { count++ } END { print count + 0 }') -ne 1 ]]; then
    printf 'checksum manifest must contain exactly one SHA-256 for %s\n' "$archive_name" >&2
    return 1
  fi
  actual_hash="$(shasum -a 256 "$archive_path" | awk '{ print $1 }')"
  if [[ "$actual_hash" != "$manifest_hash" ]]; then
    printf 'release archive checksum mismatch for %s: got %s, want %s\n' \
      "$archive_name" "$actual_hash" "$manifest_hash" >&2
    return 1
  fi
  printf '%s\n' "$manifest_hash"
}

amd64_archive="pellets_${version}_darwin_amd64.tar.gz"
arm64_archive="pellets_${version}_darwin_arm64.tar.gz"
amd64_hash="$(archive_hash "$amd64_archive")"
arm64_hash="$(archive_hash "$arm64_archive")"

work_directory="$(mktemp -d "${TMPDIR:-/tmp}/pellets-homebrew-formula.XXXXXX")"
cleanup() {
  find "$work_directory" -depth -mindepth 1 -delete
  rmdir "$work_directory"
}
trap cleanup EXIT
rendered_formula="$work_directory/pl.rb"

{
  printf '%s\n' \
    'class Pl < Formula' \
    '  desc "Local SQLite task queue for coding agents"' \
    '  homepage "https://github.com/isaksky/pellets"' \
    "  version \"$version\"" \
    '  license "Apache-2.0"' \
    '' \
    '  depends_on :macos' \
    '' \
    '  on_arm do' \
    "    url \"https://github.com/isaksky/pellets/releases/download/v$version/$arm64_archive\"" \
    "    sha256 \"$arm64_hash\"" \
    '  end' \
    '' \
    '  on_intel do' \
    "    url \"https://github.com/isaksky/pellets/releases/download/v$version/$amd64_archive\"" \
    "    sha256 \"$amd64_hash\"" \
    '  end' \
    '' \
    '  def install' \
    '    bin.install "pl"' \
    '  end' \
    '' \
    '  test do' \
    '    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip' \
    '  end' \
    'end'
} >"$rendered_formula"

if [[ "$mode" == "--write" ]]; then
  mkdir -p "$(dirname -- "$formula_path")"
  cp "$rendered_formula" "$formula_path"
  printf 'updated %s for release %s\n' "$formula_path" "$version"
  exit 0
fi

if [[ ! -f "$formula_path" ]]; then
  printf 'Homebrew formula not found: %s\n' "$formula_path" >&2
  exit 1
fi
if ! cmp -s "$rendered_formula" "$formula_path"; then
  printf 'Homebrew formula does not match release %s inputs:\n' "$version" >&2
  diff -u "$formula_path" "$rendered_formula" >&2 || true
  exit 1
fi
printf 'verified %s against release %s inputs\n' "$formula_path" "$version"
