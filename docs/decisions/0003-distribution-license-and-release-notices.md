# ADR 0003: Distribution License and Release Notice Payload

- Status: Accepted
- Date: 2026-08-29

## Context

Pellets previously had no project distribution-license decision or top-level
license file. That left repository-owned source without explicit reuse terms
and left release packaging unable to define a legally complete, stable notice
payload.

The `pl` executable is a statically linked Go program. Its supported CGo-free
macOS and Windows builds link the Go standard library and third-party Go
modules, including the translated SQLite implementation. The executable also
embeds the vendored HTMX 2.0.4 browser asset. Binary distribution therefore
has to carry both the Pellets license and the notices required by incorporated
third-party work.

## Decision

### Repository-owned work

Pellets repository-owned source code, documentation, tests, scripts, and
assets are distributed under the Apache License, Version 2.0
(`Apache-2.0`). Source and object forms, including the `pl` executable, use the
same terms; there is no binary-only license or end-user license agreement.
Copyright remains with the applicable contributors.

The top-level `LICENSE` file is the unmodified canonical Apache-2.0 text. The
project does not create a top-level Apache `NOTICE` file because Pellets has no
project attribution notice to impose under section 4(d). Third-party notices
are kept separately and do not modify the Apache-2.0 terms.

Apache-2.0 is permissive, permits commercial and non-commercial source and
binary redistribution, and supplies an explicit contributor patent grant and
patent-litigation termination rule. Those properties suit a reusable local
developer tool better than leaving patent rights implicit. Its notice
conditions are compatible with every permissively licensed component in the
current binary.

### Binary release archive contract

Every binary release archive must place these two repository-root files next
to the executable, preserving these exact names and contents:

1. `LICENSE`
2. `THIRD_PARTY_NOTICES.txt`

`LICENSE` supplies the terms for repository-owned work.
`THIRD_PARTY_NOTICES.txt` is the stable, consolidated binary-distribution
payload for linked Go code and the embedded browser asset. It reproduces the
applicable upstream license and attribution files so an archive does not
depend on a module cache, network access, or repository-relative links.

The source-only audit inputs
`internal/webui/assets/HTMX-LICENSE.txt` and
`internal/webui/assets/HTMX-NOTICE.txt` remain in the repository and embedded
binary. They need not be duplicated as separate archive entries because the
HTMX license and provenance are represented in the consolidated notice.

This decision defines archive inputs only. It does not publish an archive or
add signing, provenance, installers, package-manager metadata, or publishing
infrastructure.

### Shipped-code audit

The Go inventory comes from the distinct module paths selected by
`go list -deps ./cmd/pl`, checked with `CGO_ENABLED=0` for every supported
release target (`darwin/amd64`, `darwin/arm64`, and `windows/amd64`). The three
targets select the same module/version set except that Modernc's Unix layer
links `github.com/google/uuid` on both macOS targets and the Windows build does
not. The consolidated payload intentionally covers the union so the same two
legal files can ship with every archive. Go standard-library code is also
linked and is included even though it has no `go.mod` module entry. The binary
records its exact toolchain version in Go build metadata.

| Shipped component | Version | Terms | Material reproduced in `THIRD_PARTY_NOTICES.txt` |
| --- | --- | --- | --- |
| Go standard library | build toolchain version | BSD-3-Clause | Go `LICENSE` |
| `github.com/dustin/go-humanize` | v1.0.1 | MIT | `LICENSE` |
| `github.com/google/uuid` (macOS binaries only) | v1.6.0 | BSD-3-Clause | `LICENSE` |
| `github.com/mattn/go-isatty` | v0.0.20 | MIT | `LICENSE` |
| `github.com/ncruces/go-strftime` | v1.0.0 | MIT | `LICENSE` |
| `github.com/remyoudompheng/bigfft` | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause | `LICENSE` |
| `golang.org/x/exp` | v0.0.0-20251023183803-a4bb9ffd2546 | BSD-3-Clause | `LICENSE` |
| `golang.org/x/sys` | v0.37.0 | BSD-3-Clause | `LICENSE` |
| `modernc.org/libc` | v1.67.6 | BSD-3-Clause plus nested permissive components | `LICENSE` and the complete `LICENSE-3RD-PARTY.md` for Go-derived code, musl libc, go-netdb, and NixOS/nixpkgs material |
| `modernc.org/mathutil` | v1.7.1 | BSD-3-Clause | `LICENSE` |
| `modernc.org/memory` | v1.11.0 | BSD-3-Clause | `LICENSE` and `LICENSE-MMAP-GO` for the Unix mmap implementation linked into macOS binaries |
| `modernc.org/sqlite` | v1.45.0 | BSD-3-Clause; translated SQLite core is public domain | `LICENSE`; public-domain SQLite adds no required notice |
| HTMX | v2.0.4 | Zero-Clause BSD | `internal/webui/assets/HTMX-LICENSE.txt`; version, upstream location, embedded path, and pinned digest are recorded by the accompanying HTMX notice |

Module `AUTHORS` files do not state additional redistribution conditions and
are not separate archive inputs. Go and `golang.org/x` `PATENTS` files state
additional patent grants but impose no notice-retention condition, so they are
not required archive inputs. Test-only code, module-download metadata, source
fixtures, build tools, and GitHub Actions are not linked into `pl` and are
outside the binary notice payload.

### Drift protection

`cmd/pl/license_contract_test.go` protects the decision in three ways:

- fixed SHA-256 values require intentional review when either stable archive
  file or either HTMX audit input changes;
- the pinned HTMX asset digest is checked against its provenance notice; and
- `go list -deps` is compared with each target's exact audited module/version set, and
  every audited module marker must remain present in the consolidated notice.

Adding, removing, replacing, or upgrading a linked module therefore fails the
normal test suite until this audit, the notice payload, and the expected
inventory are updated together.

## Consequences

- Users and redistributors receive explicit, identical terms for Pellets
  source and binaries.
- A binary archive has a small, deterministic two-file legal payload.
- Consolidation duplicates upstream notice text in the repository, but makes
  offline binary redistribution complete and reviewable.
- Dependency and browser-asset changes now require an explicit license audit
  rather than silently inheriting the preceding payload.

Related documents:

- [Initial architecture](0001-initial-architecture.md)
- [Architecture](../architecture.md)
- [Implementation plan](../implementation-plan.md)
