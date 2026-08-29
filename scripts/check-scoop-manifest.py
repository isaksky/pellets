#!/usr/bin/env python3
"""Validate the minimal project-owned Scoop manifest contract."""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path
from typing import Any


VERSION_PATTERN = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+")
HASH_PATTERN = re.compile(r"[0-9a-f]{64}")


def fail(message: str) -> None:
    print(f"invalid Scoop manifest: {message}", file=sys.stderr)
    raise SystemExit(1)


def object_without_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate key {key!r}")
        result[key] = value
    return result


def require_exact_keys(value: Any, expected: set[str], location: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        fail(f"{location} must be an object")
    actual = set(value)
    if actual != expected:
        fail(
            f"{location} keys must be exactly {sorted(expected)!r}, "
            f"got {sorted(actual)!r}"
        )
    return value


def main() -> None:
    if len(sys.argv) != 2:
        print(f"usage: {Path(sys.argv[0]).name} MANIFEST", file=sys.stderr)
        raise SystemExit(2)

    manifest_path = Path(sys.argv[1])
    try:
        manifest = json.loads(
            manifest_path.read_text(encoding="utf-8"),
            object_pairs_hook=object_without_duplicates,
        )
    except (OSError, UnicodeError, json.JSONDecodeError, ValueError) as error:
        fail(f"cannot read {manifest_path}: {error}")

    root = require_exact_keys(manifest, {"architecture", "bin", "version"}, "root")
    version = root["version"]
    if not isinstance(version, str) or VERSION_PATTERN.fullmatch(version) is None:
        fail("version must be a stable SemVer core without a leading v")

    architecture = require_exact_keys(root["architecture"], {"64bit"}, "architecture")
    amd64 = require_exact_keys(architecture["64bit"], {"hash", "url"}, "architecture.64bit")

    archive_name = f"pellets_{version}_windows_amd64.zip"
    expected_url = (
        f"https://github.com/isaksky/pellets/releases/download/v{version}/{archive_name}"
    )
    if amd64["url"] != expected_url:
        fail(f"architecture.64bit.url must be {expected_url!r}")

    archive_hash = amd64["hash"]
    if not isinstance(archive_hash, str) or HASH_PATTERN.fullmatch(archive_hash) is None:
        fail("architecture.64bit.hash must be a lowercase SHA-256 value")

    if root["bin"] != "pl.exe":
        fail("bin must be 'pl.exe'")

    print(f"validated {manifest_path} for Pellets {version} Windows AMD64")


if __name__ == "__main__":
    main()
