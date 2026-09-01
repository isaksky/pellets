#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
export TZ=UTC

usage() {
  printf 'usage: %s VERSION [OUTPUT_DIRECTORY]\n' "${0##*/}" >&2
  exit 2
}

if [[ $# -lt 1 || $# -gt 2 ]]; then
  usage
fi

version="$1"
version_pattern='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
if [[ ! "$version" =~ $version_pattern ]]; then
  printf 'VERSION must be a SemVer value without a leading v: %s\n' "$version" >&2
  exit 2
fi

if [[ "$(uname -s)" != "Darwin" ]]; then
  printf 'release archives must be built and verified on macOS\n' >&2
  exit 1
fi

for command_name in cmp file git go gzip otool sandbox-exec shasum tar unzip zip; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf 'required command not found: %s\n' "$command_name" >&2
    exit 1
  fi
done

script_directory="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(CDPATH= cd -- "$script_directory/.." && pwd)"
output_directory="${2:-$repository_root/dist}"
mkdir -p "$output_directory"
output_directory="$(CDPATH= cd -- "$output_directory" && pwd)"

release_work_directory="$(mktemp -d "${TMPDIR:-/tmp}/pellets-release.XXXXXX")"
cleanup() {
  find "$release_work_directory" -depth -mindepth 1 -delete
  rmdir "$release_work_directory"
}
trap cleanup EXIT

release_directory="$release_work_directory/release"
mkdir "$release_directory"

archive_names=(
  "pellets_${version}_darwin_amd64.tar.gz"
  "pellets_${version}_darwin_arm64.tar.gz"
  "pellets_${version}_windows_amd64.zip"
)
checksum_name="pellets_${version}_checksums.txt"

build_archive() {
  local target_os="$1"
  local target_arch="$2"
  local archive_name="$3"
  local executable_name="$4"
  local staging_directory="$release_work_directory/stage-${target_os}-${target_arch}"
  local archive_path="$release_directory/$archive_name"

  mkdir "$staging_directory"
  cp "$repository_root/LICENSE" "$repository_root/THIRD_PARTY_NOTICES.txt" "$staging_directory/"
  (
    cd "$repository_root"
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
      go build -trimpath -buildvcs=false \
        -ldflags="-X main.version=$version" \
        -o "$staging_directory/$executable_name" ./cmd/pl
  )

  chmod 0644 "$staging_directory/LICENSE" "$staging_directory/THIRD_PARTY_NOTICES.txt"
  chmod 0755 "$staging_directory/$executable_name"
  touch -t 200001010000 \
    "$staging_directory/LICENSE" \
    "$staging_directory/THIRD_PARTY_NOTICES.txt" \
    "$staging_directory/$executable_name"

  if [[ "$archive_name" == *.tar.gz ]]; then
    (
      cd "$staging_directory"
      COPYFILE_DISABLE=1 tar -cf - \
        --format ustar --uid 0 --gid 0 --uname root --gname root \
        --no-acls --no-fflags --no-mac-metadata --no-xattrs \
        LICENSE THIRD_PARTY_NOTICES.txt "$executable_name" \
        | gzip -n >"$archive_path"
    )
  else
    (
      cd "$staging_directory"
      zip -X -q "$archive_path" LICENSE THIRD_PARTY_NOTICES.txt "$executable_name"
    )
  fi
}

build_archive darwin amd64 "${archive_names[0]}" pl
build_archive darwin arm64 "${archive_names[1]}" pl
build_archive windows amd64 "${archive_names[2]}" pl.exe

(
  cd "$release_directory"
  for archive_name in "${archive_names[@]}"; do
    shasum -a 256 "$archive_name"
  done >"$checksum_name"
)

expected_output_names="$(printf '%s\n' "$checksum_name" "${archive_names[@]}")"
actual_output_names="$(cd "$release_directory" && printf '%s\n' *)"
if [[ "$actual_output_names" != "$expected_output_names" ]]; then
  printf 'unexpected release output names:\n%s\n' "$actual_output_names" >&2
  exit 1
fi

verify_archive() {
  local target_os="$1"
  local target_arch="$2"
  local archive_name="$3"
  local executable_name="$4"
  local archive_path="$release_directory/$archive_name"
  local extraction_directory="$release_work_directory/extract-${target_os}-${target_arch}"
  local expected_entries
  local actual_entries
  local extracted_names
  local metadata

  expected_entries="$(printf '%s\n' LICENSE THIRD_PARTY_NOTICES.txt "$executable_name")"
  if [[ "$archive_name" == *.tar.gz ]]; then
    actual_entries="$(tar -tzf "$archive_path")"
    mkdir "$extraction_directory"
    tar -xzf "$archive_path" -C "$extraction_directory"
  else
    unzip -tqq "$archive_path"
    actual_entries="$(unzip -Z1 "$archive_path")"
    mkdir "$extraction_directory"
    unzip -q "$archive_path" -d "$extraction_directory"
  fi
  if [[ "$actual_entries" != "$expected_entries" ]]; then
    printf 'unexpected entries in %s:\n%s\n' "$archive_name" "$actual_entries" >&2
    exit 1
  fi

  extracted_names="$(cd "$extraction_directory" && printf '%s\n' *)"
  if [[ "$extracted_names" != "$expected_entries" ]]; then
    printf 'unexpected extracted files in %s:\n%s\n' "$archive_name" "$extracted_names" >&2
    exit 1
  fi
  if [[ ! -f "$extraction_directory/$executable_name" || ! -x "$extraction_directory/$executable_name" ]]; then
    printf '%s does not contain an executable regular file named %s\n' "$archive_name" "$executable_name" >&2
    exit 1
  fi
  cmp "$repository_root/LICENSE" "$extraction_directory/LICENSE"
  cmp "$repository_root/THIRD_PARTY_NOTICES.txt" "$extraction_directory/THIRD_PARTY_NOTICES.txt"

  metadata="$(go version -m "$extraction_directory/$executable_name")"
  for expected_metadata in \
    $'\tbuild\tCGO_ENABLED=0' \
    $'\tbuild\tGOOS='"$target_os" \
    $'\tbuild\tGOARCH='"$target_arch"; do
    if ! grep -Fqx "$expected_metadata" <<<"$metadata"; then
      printf 'missing metadata %q in %s:\n%s\n' "$expected_metadata" "$archive_name" "$metadata" >&2
      exit 1
    fi
  done

  case "$target_os/$target_arch" in
    darwin/amd64) expected_file_description='Mach-O 64-bit executable x86_64' ;;
    darwin/arm64) expected_file_description='Mach-O 64-bit executable arm64' ;;
    windows/amd64) expected_file_description='PE32+ executable' ;;
    *) return 1 ;;
  esac
  if ! file -b "$extraction_directory/$executable_name" | grep -Fq "$expected_file_description"; then
    printf 'unexpected executable format in %s: %s\n' \
      "$archive_name" "$(file -b "$extraction_directory/$executable_name")" >&2
    exit 1
  fi
}

verify_archive darwin amd64 "${archive_names[0]}" pl
verify_archive darwin arm64 "${archive_names[1]}" pl
verify_archive windows amd64 "${archive_names[2]}" pl.exe

(
  cd "$release_directory"
  shasum -a 256 -c "$checksum_name"
)

case "$(uname -m)" in
  arm64) native_archive="${archive_names[1]}" ;;
  x86_64) native_archive="${archive_names[0]}" ;;
  *)
    printf 'unsupported native macOS architecture: %s\n' "$(uname -m)" >&2
    exit 1
    ;;
esac

native_directory="$release_work_directory/native"
smoke_directory="$release_work_directory/smoke"
mkdir "$native_directory" "$smoke_directory"
tar -xzf "$release_directory/$native_archive" -C "$native_directory"

if otool -L "$native_directory/pl" | grep -Fqi sqlite; then
  printf 'native release binary links a SQLite dynamic library\n' >&2
  exit 1
fi

offline_profile='(version 1) (allow default) (deny network*)'
run_offline() {
  HTTP_PROXY=http://127.0.0.1:1 \
  HTTPS_PROXY=http://127.0.0.1:1 \
  ALL_PROXY=http://127.0.0.1:1 \
  NO_PROXY= \
  NO_COLOR=1 \
    sandbox-exec -p "$offline_profile" "$@"
}

version_output="$(run_offline "$native_directory/pl" --version)"
if [[ "$version_output" != "pl $version (JSON schema 1)" ]]; then
  printf 'unexpected native version output: %s\n' "$version_output" >&2
  exit 1
fi

git -C "$smoke_directory" init -q
smoke_add="$(cd "$smoke_directory" && run_offline "$native_directory/pl" add 'release archive smoke')"
smoke_start="$(cd "$smoke_directory" && run_offline "$native_directory/pl" start-next)"
smoke_close="$(cd "$smoke_directory" && run_offline "$native_directory/pl" close smoke-1)"

for expected_result in \
  "$smoke_add|\"command\":\"add\"|\"id\":\"smoke-1\"" \
  "$smoke_start|\"command\":\"start-next\"|\"status\":\"in_progress\"" \
  "$smoke_close|\"command\":\"close\"|\"status\":\"closed\""; do
  IFS='|' read -r result required_one required_two <<<"$expected_result"
  if [[ "$result" != *"$required_one"* || "$result" != *"$required_two"* ]]; then
    printf 'unexpected native smoke result: %s\n' "$result" >&2
    exit 1
  fi
done
if [[ ! -f "$smoke_directory/.pellets/pellets.db" ]]; then
  printf 'native smoke test did not bootstrap its isolated database\n' >&2
  exit 1
fi

for release_file in "${archive_names[@]}" "$checksum_name"; do
  cp "$release_directory/$release_file" "$output_directory/$release_file"
done

printf 'verified release %s in %s\n' "$version" "$output_directory"
