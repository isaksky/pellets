#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

usage() {
  printf 'usage: %s VERSION [RELEASE_DIRECTORY]\n' "${0##*/}" >&2
  exit 2
}

if [[ $# -lt 1 || $# -gt 2 ]]; then
  usage
fi

version="$1"
version_pattern='^[0-9]+\.[0-9]+\.[0-9]+$'
if [[ ! "$version" =~ $version_pattern ]]; then
  printf 'Homebrew stable VERSION must be a SemVer core without a leading v: %s\n' "$version" >&2
  exit 2
fi
if [[ "$(uname -s)" != "Darwin" ]]; then
  printf 'Homebrew formula installation must be tested on macOS\n' >&2
  exit 1
fi

for command_name in brew cp grep mkdir mktemp; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf 'required command not found: %s\n' "$command_name" >&2
    exit 1
  fi
done

script_directory="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(CDPATH= cd -- "$script_directory/.." && pwd)"
release_directory="${2:-$repository_root/dist}"
tap_name="isaksky/pellets"

"$script_directory/update-homebrew-formula.sh" --check "$version" "$release_directory"

case "$(uname -m)" in
  arm64) native_archive="pellets_${version}_darwin_arm64.tar.gz" ;;
  x86_64) native_archive="pellets_${version}_darwin_amd64.tar.gz" ;;
  *)
    printf 'unsupported native macOS architecture: %s\n' "$(uname -m)" >&2
    exit 1
    ;;
esac

if brew list --versions pl >/dev/null 2>&1; then
  printf 'refusing to replace an existing Homebrew pl installation\n' >&2
  exit 1
fi
if brew tap | grep -Fqx "$tap_name"; then
  printf 'refusing to replace an existing Homebrew tap: %s\n' "$tap_name" >&2
  exit 1
fi
developer_mode_was_enabled=false
if brew developer state | grep -Fq 'Developer mode is enabled'; then
  developer_mode_was_enabled=true
fi

work_directory="$(mktemp -d "${TMPDIR:-/tmp}/pellets-homebrew-test.XXXXXX")"
homebrew_cache="$work_directory/cache"
cleanup() {
  local original_status=$?
  local cleanup_status=0
  set +e
  if brew list --versions pl >/dev/null 2>&1; then
    HOMEBREW_NO_AUTO_UPDATE=1 brew uninstall "$tap_name/pl" >/dev/null
    [[ $? -eq 0 ]] || cleanup_status=1
  fi
  if brew tap | grep -Fqx "$tap_name"; then
    HOMEBREW_NO_AUTO_UPDATE=1 brew untap "$tap_name" >/dev/null
    [[ $? -eq 0 ]] || cleanup_status=1
  fi
  if [[ "$developer_mode_was_enabled" == false ]]; then
    brew developer off >/dev/null
    [[ $? -eq 0 ]] || cleanup_status=1
  fi
  find "$work_directory" -depth -mindepth 1 -delete
  [[ $? -eq 0 ]] || cleanup_status=1
  rmdir "$work_directory"
  [[ $? -eq 0 ]] || cleanup_status=1
  trap - EXIT
  if [[ $original_status -ne 0 ]]; then
    exit "$original_status"
  fi
  exit "$cleanup_status"
}
trap cleanup EXIT

mkdir -p "$homebrew_cache"
HOMEBREW_NO_AUTO_UPDATE=1 brew tap "$tap_name" "$repository_root"
tap_repository="$(brew --repo "$tap_name")"
mkdir -p "$tap_repository/Formula"
cp "$repository_root/Formula/pl.rb" "$tap_repository/Formula/pl.rb"
cached_archive="$(HOMEBREW_CACHE="$homebrew_cache" HOMEBREW_NO_AUTO_UPDATE=1 \
  brew --cache --formula "$tap_name/pl")"
mkdir -p "$(dirname -- "$cached_archive")"
cp "$release_directory/$native_archive" "$cached_archive"

HOMEBREW_CACHE="$homebrew_cache" \
HOMEBREW_NO_AUTO_UPDATE=1 \
HOMEBREW_FORMULA_BUILD_NETWORK=deny \
HOMEBREW_FORMULA_POSTINSTALL_NETWORK=deny \
HTTP_PROXY=http://127.0.0.1:1 \
HTTPS_PROXY=http://127.0.0.1:1 \
ALL_PROXY=http://127.0.0.1:1 \
NO_PROXY= \
  brew install "$tap_name/pl"

HOMEBREW_NO_AUTO_UPDATE=1 \
HOMEBREW_FORMULA_TEST_NETWORK=deny \
  brew test "$tap_name/pl"
version_output="$("$(brew --prefix "$tap_name/pl")/bin/pl" --version)"
if [[ "$version_output" != "pl $version (JSON schema 1)" ]]; then
  printf 'unexpected Homebrew-installed version output: %s\n' "$version_output" >&2
  exit 1
fi

printf 'verified native Homebrew installation for Pellets %s (%s)\n' "$version" "$(uname -m)"
