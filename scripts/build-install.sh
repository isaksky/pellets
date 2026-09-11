#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'Usage: %s [INSTALL_DIRECTORY]\n' "${0##*/}"
  printf 'Build pl from this checkout and install it (default: ~/.local/bin).\n'
}

if [[ $# -eq 1 && ( "$1" == --help || "$1" == -h ) ]]; then
  usage
  exit 0
fi
if [[ $# -gt 1 || ( $# -eq 1 && ( -z "$1" || "$1" == -* ) ) ]]; then
  usage >&2
  exit 2
fi

if ! command -v go >/dev/null 2>&1; then
  printf 'Go 1.24 or newer is required; install Go and rerun this script.\n' >&2
  exit 1
fi

script_directory="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(CDPATH= cd -- "$script_directory/.." && pwd)"
install_directory="${1:-$HOME/.local/bin}"
mkdir -p -- "$install_directory"
install_directory="$(CDPATH= cd -- "$install_directory" && pwd)"

build_directory="$(mktemp -d "${TMPDIR:-/tmp}/pellets-install.XXXXXX")"
cleanup() {
  find "$build_directory" -depth -mindepth 1 -delete
  rmdir "$build_directory"
}
trap cleanup EXIT

printf 'Building pl...\n'
(
  cd "$repository_root"
  CGO_ENABLED=0 GOOS="$(go env GOHOSTOS)" GOARCH="$(go env GOHOSTARCH)" \
    go build -trimpath -o "$build_directory/pl" ./cmd/pl
)
"$build_directory/pl" --version
install -m 0755 "$build_directory/pl" "$install_directory/pl"
printf 'Installed %s/pl\n' "$install_directory"

case ":${PATH:-}:" in
  *":$install_directory:"*) ;;
  *)
    printf 'Add this directory to your shell configuration to run pl:\n'
    printf '  export PATH=%q:"$PATH"\n' "$install_directory"
    ;;
esac
