#!/usr/bin/env bash
set -euo pipefail

script_directory="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(CDPATH= cd -- "$script_directory/.." && pwd)"
cross_build_directory="$(mktemp -d "${TMPDIR:-/tmp}/pellets-cross-build.XXXXXX")"

cleanup() {
  find "$cross_build_directory" -depth -mindepth 1 -delete
  rmdir "$cross_build_directory"
}
trap cleanup EXIT

targets=(
  "darwin amd64 pl-darwin-amd64"
  "darwin arm64 pl-darwin-arm64"
  "windows amd64 pl-windows-amd64.exe"
)

for target in "${targets[@]}"; do
  read -r target_os target_arch artifact_name <<<"$target"
  artifact_path="$cross_build_directory/$artifact_name"

  (
    cd "$repository_root"
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
      go build -trimpath -o "$artifact_path" ./cmd/pl
  )

  metadata="$(go version -m "$artifact_path")"
  for expected in \
    $'\tbuild\tCGO_ENABLED=0' \
    $'\tbuild\tGOOS='"$target_os" \
    $'\tbuild\tGOARCH='"$target_arch"; do
    if ! grep -Fqx "$expected" <<<"$metadata"; then
      printf 'missing metadata %q in %s:\n%s\n' "$expected" "$artifact_name" "$metadata" >&2
      exit 1
    fi
  done

  printf 'verified %s/%s: %s\n' "$target_os" "$target_arch" "$artifact_name"
done
