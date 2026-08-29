# Pellets

Pellets (`pl`) is a local SQLite task queue for coding agents. It keeps a
deterministic project queue, worktree-scoped in-progress ownership, exact
external-ID and group filters, FTS5 keyword search, and independent project
memory in the nearest `.pellets/pellets.db`.

Pellets is one CGo-free executable with SQLite embedded. It has no account,
telemetry, cloud synchronization, model, plugin runtime, daemon, or required
service. Git must be available for repository and worktree discovery.

## Install

### macOS with Homebrew

Pellets uses the project-owned `isaksky/pellets` tap in this same source
repository. Because the repository is named `pellets` rather than
`homebrew-pellets`, add it with its explicit URL:

```text
brew tap isaksky/pellets https://github.com/isaksky/pellets.git
brew install isaksky/pellets/pl
pl --version
```

Upgrade or uninstall with:

```text
brew update
brew upgrade isaksky/pellets/pl
brew uninstall isaksky/pellets/pl
brew untap isaksky/pellets
```

### Windows AMD64 with Scoop

Pellets uses a project-owned Scoop bucket in this same source repository. It
installs for the current user without administrator privileges:

```text
scoop bucket add pellets https://github.com/isaksky/pellets.git
scoop install pellets/pl
pl --version
```

Upgrade or uninstall with:

```text
scoop update
scoop update pl
scoop uninstall pl
scoop bucket rm pellets
```

### From source or a release archive

Versioned CGo-free archives are published on the project's
[GitHub Releases](https://github.com/isaksky/pellets/releases) page. Unpack the
archive for the supported target and place `pl` or `pl.exe` on `PATH`; no
SQLite library, configuration file, model, or installer is required.

To build from a checkout with Go 1.24 or newer:

```text
git clone https://github.com/isaksky/pellets.git
cd pellets
go build -o pl ./cmd/pl
./pl --version
```

### Platform support

| Target | Support and validation |
|---|---|
| macOS AMD64 | Supported; CGo-free build plus the native macOS CI suite. |
| macOS ARM64 | Supported; CGo-free build plus the native macOS CI suite. |
| Windows AMD64 | Supported; CGo-free build, the complete suite under native Windows PowerShell CI, and native archive/Scoop smoke tests. |
| Windows ARM64 | Not built, tested, or released; native ARM64 CI and hardware or VM validation are required before support. |

Maintainer hands-on testing is available only on macOS. Windows behavior is
therefore covered by native Windows AMD64 CI, including Git discovery, path and
locking behavior, queue operations, release-archive execution, and Scoop
installation. A successful cross-build alone is not treated as Windows
support.

## Choose a database layout

Every database lives at `.pellets/pellets.db`. The directory containing
`.pellets` is the database root.

### One repository with a project-local database

From an existing Git worktree:

```text
cd path/to/repository
pl init --code demo
```

If no ancestor database exists, `pl init` creates one at the current worktree
root and registers the logical Git repository and current worktree. Project
codes are immutable, unique within the database, and contain 1–12 lowercase
letters, digits, or internal hyphens.

### Several repositories with one common-parent database

Create the database before registering the sibling repositories:

```text
cd path/to/common-parent
pl init-db

cd service-a
pl init --code svc-a

cd ../service-b
pl init --code svc-b

cd ..
pl project list
```

Each registered project keeps its own pellet numbers, queue order, filters,
and memories even though SQLite stores them in one file. `pl project show
svc-a` displays a project's registered workspace identities.

For linked Git worktrees, put the database at a common ancestor of every
worktree and run `pl init --code CODE` with the same code in each worktree.
They then share one logical project, queue, and memory store while remaining
distinct workspaces.

### Discovery and Git safety

Normal commands walk upward from the current directory to the filesystem root
and use the nearest `.pellets/pellets.db`. Discovery continues across Git
boundaries, which is why a common-parent database works from nested directories
inside sibling repositories. There is no database-path flag; `--project CODE`
selects a registered project where permitted, not a different database.

The database is local plaintext and must never be committed. When `.pellets`
is inside a Git worktree, initialization adds `.pellets/` to that worktree's
local Git exclude file, not its committed `.gitignore`, and refuses to proceed
if the database or an SQLite companion file is tracked. Do not stage or commit
`.pellets`, and protect and back it up like any other local SQLite data.

## Operate the queue

A minimal project workflow is:

```text
pl add "Implement parser" --description "Reject malformed input." --external-id "github:acme/demo#84" --group parser
pl add "Test invalid input" --external-id "github:acme/demo#84" --group parser
pl list --group parser
pl next --group parser
pl start-next --group parser
pl close demo-1
pl next --group parser
```

Pellet references such as `demo-1` contain the project code and a
monotonically allocated project-local number. Save the `data.id` returned by
`add` instead of assuming a number in automation.

### JSON is the agent interface

Compact JSON is the default; there is no `--json` flag. Each successful
short-lived command writes one versioned object and a newline to stdout. For
example, the read-only `next` command above returns this shape:

```json
{"schema_version":1,"command":"next","data":{"selection_reason":"next_open","pellet":{"id":"demo-1","project":"demo","number":1,"title":"Implement parser","description":"Reject malformed input.","external_id":"github:acme/demo#84","group":"parser","status":"open","priority":1024,"workspace":null,"created_at":"2026-08-29T20:00:00Z","updated_at":"2026-08-29T20:00:00Z","completed_at":null}}}
```

An empty selection is also a successful typed result:

```json
{"schema_version":1,"command":"next","data":{"selection_reason":"none","pellet":null}}
```

Agent integrations should check the process exit code, require the supported
`schema_version`, branch on `command` and command-specific `data`, and handle
empty arrays or `null` explicitly. Errors write one JSON object to stderr and
nothing to stdout; branch on stable `error.code`, not the diagnostic
`error.message`. Use `--pretty` while inspecting JSON or `--human` for concise
human-readable output. The exact field and exit-code contract is in
[the CLI specification](docs/cli-spec.md#json-contract).

### Selection, ordering, and lifecycle

`open` and `in_progress` pellets share one project order. Lower integer
priority values come first. `add` appends by default; `--before` and `--after`
place newly discovered work, and `pl move PELLET (--before OTHER | --after
OTHER)` reorders active work. Priority values are implementation-managed; use
relative placement rather than treating them as high/medium/low categories.
Closed and `maybe_later` pellets have `priority: null` and do not occupy the
active order.

| Command | Lifecycle effect |
|---|---|
| `pl start PELLET` | Change one open pellet to `in_progress` in the current workspace. |
| `pl start-next [filters]` | Atomically resume the current pellet or select and start the first matching open pellet. |
| `pl release PELLET` | Return the current workspace's in-progress pellet to `open` without losing its priority. |
| `pl close PELLET` | Change open or current-workspace work to `closed`, clearing priority and ownership. |
| `pl defer PELLET` | Change open or current-workspace work to `maybe_later`, outside the executable queue. |
| `pl reopen PELLET` | Return closed or deferred work to `open` at the end of the active order. |

`pl next` is strictly read-only. It first resumes the current workspace's
in-progress pellet, even if that pellet does not match supplied filters; it
otherwise returns the lowest-priority matching open pellet or `pellet: null`.
Use `start-next` when work should begin immediately because composing `next`
and `start` is not atomic across concurrent worktrees.

Only one pellet may be in progress in each registered project workspace. For
the usual single-worktree project, this means the project can have only one
in-progress pellet. Linked worktrees are separate workspaces and may each own
one different in-progress pellet in the same logical project. Ownership is a
worktree coordination pointer, not an agent claim, lease, or authentication
mechanism. Recovery from a removed worktree requires the explicit stored
workspace ID and `--yes`; see the `release`, `close`, and `defer` forms in the
[CLI specification](docs/cli-spec.md#pl-release).

## Search tasks and operate memory

Task search covers title, description, and external-ID text across every
status by default, so closed pellets remain discoverable:

```text
pl search parser
pl search "invalid input" --external-id "github:acme/demo#84" --group parser
pl search parser --status closed --limit 20
```

Search treats ordinary input as safe FTS5 terms rather than raw FTS syntax.
External-ID and group filters are separate exact, case-sensitive filters. A
pellet has at most one opaque group; a group is not a tag or epic.

Memory is independent, project-scoped knowledge. It has no pellet foreign key,
status, priority, group, or automatic relationship to queue lifecycle. An
agent-oriented review workflow is:

```text
pl memory add --text "Parser identifiers preserve underscores." --created-by agent
pl memory search "parser identifiers"
pl memory show 1

# After a human reviews the text:
pl memory approve 1
pl memory list --approved-only
```

Use the `data.id` returned by `memory add`; memory IDs are database-local
integers and may contain gaps. Agent-created memory begins unapproved.
`memory approve` represents an explicit human review action, and an agent must
not use it to self-approve its own text. Text actually authored or supplied by
a human may be added with `--created-by human`, which approves it immediately.
Approval records review, not guaranteed truth.

`pl memory search` is keyword-only FTS5 retrieval. Memory remains plaintext in
the local database, is shared by all worktrees of the logical project, never
affects `next`, and is never created automatically when a pellet closes.

## Purge safely

Closed pellets remain stored until explicitly purged. Preview the exact
references first, then repeat with confirmation:

```text
pl purge --project demo --dry-run
pl purge --project demo --yes
```

To limit the selection, add a cutoff to both commands:

```text
pl purge --project demo --closed-before 2026-01-01 --dry-run
pl purge --project demo --closed-before 2026-01-01 --yes
```

Purge is permanent and database-level, so it always requires an explicit
project and exactly one of `--dry-run` or `--yes`. It can delete only `closed`
pellets—never open, in-progress, or deferred work—and never reuses their
numbers. Purge does not delete memory. Remove one memory separately and
irreversibly with `pl memory remove MEMORY_ID --yes`.

## Install the Pellets agent skill

Install the focused, instruction-only `pellets` skill for Codex, Claude, or
both:

```text
pl --human skill install
pl skill install --scope repo --agent both --yes
pl skill install --scope personal --agent codex --dry-run
```

Repository scope writes `.agents/skills/pellets/SKILL.md` for Codex and/or
`.claude/skills/pellets/SKILL.md` for Claude beneath the current Git worktree.
These are ordinary untracked files until the user chooses to commit them; `pl`
never changes the Git index or ignore files. Personal scope writes the same
instructions beneath the operating-system home directory.

The interactive wizard requires `--human` plus terminal stdin and stdout.
Default JSON mode never prompts and requires explicit `--scope`, `--agent`,
and `--yes` for writes. `--dry-run` returns every target and the full generated
content without writing. Differing files require separate replacement consent
or `--force`; a multi-agent install is preflighted and rolled back as one
operation.

The installed skill activates implicitly only when a prompt explicitly names
`pl` or Pellet/Pellets. Generic task, issue, backlog, project-management, or
memory requests are deliberately excluded.

## Local web inspector

```text
pl web
pl web --no-open
pl web --port 8123 --no-open
pl --project demo web
```

`pl web` is an optional foreground operator tool over the same nearest
database. It listens only on `127.0.0.1`, uses embedded offline assets, and
stops when interrupted. It can inspect every registered project, workspace,
pellet state, and memory; it supports routine queue and memory edits with
optimistic conflict detection. Purge, memory removal, and other irreversible
actions are intentionally absent.

There is no user login. The loopback listener, exact Host/Origin checks, and a
per-process CSRF capability protect against ordinary cross-site browser
mutation, but not against another process running as the same local user or
anyone who can read the database. Do not expose the port through a proxy or
tunnel.

## Scope and non-goals

Pellets is deliberately a local ordered queue, not a general project manager:

- Pellets has no task dependencies or blocking graph, epics or subtasks,
  claims or assignments, and no vector or semantic search.
- It has no tags, multiple groups, custom statuses, task notes, event history,
  agent identity, leases, heartbeats, or background orchestration.
- It has no cloud or Git synchronization, remote API, hosted service, daemon,
  account system, or general plugin framework.
- Memory is free-form keyword-searchable project knowledge, not task history,
  a dependency mechanism, or automatically generated content.
- Workspace ownership identifies a registered Git worktree only. Two workers
  using the same worktree are not isolated from each other.

The database is always local and never committed. Repository-scoped agent
skill files are a separate artifact that the user may choose to commit.

## Build, test, and release maintenance

```text
go build ./cmd/pl
go test ./...
./scripts/verify-cross-builds.sh
```

The cross-build script verifies `CGO_ENABLED=0` artifacts for macOS
AMD64/ARM64 and Windows AMD64. Stable release automation uses the Go 1.26.5
toolchain pinned by CI.

On macOS, build versioned release archives and render the package-manager
metadata from their verified SHA-256 values with a SemVer value without a
leading `v`:

```text
./scripts/build-release.sh 1.2.3
./scripts/update-homebrew-formula.sh --write 1.2.3
./scripts/update-scoop-manifest.sh --write 1.2.3
```

The fixed outputs are:

```text
pellets_1.2.3_darwin_amd64.tar.gz
pellets_1.2.3_darwin_arm64.tar.gz
pellets_1.2.3_windows_amd64.zip
pellets_1.2.3_checksums.txt
```

Each archive has a flat, sorted three-file layout: `LICENSE`,
`THIRD_PARTY_NOTICES.txt`, and `pl` or `pl.exe`. The builder verifies names,
entries, executable formats, target metadata, normalized archive metadata, and
checksums, then smoke-tests only the Mac's native architecture without Rosetta.
Windows CI runs the same macOS-produced Windows archive natively. Stable names
and layouts are promised; independently reproducible Go binaries or identical
output across toolchains, operating systems, and compression implementations
are not. Tagged release publication is gated on the full native test,
cross-build, Homebrew, Scoop, and archive checks. Signing, notarization,
bottles, installers, and Windows ARM64 artifacts are out of scope.

## Authoritative documentation

This README is the operator introduction. The design documents remain
authoritative for exact requirements and edge cases:

- [Project goals and product boundaries](docs/project-goals.md)
- [Architecture, discovery, storage, and platform support](docs/architecture.md)
- [CLI commands, JSON, errors, and lifecycle](docs/cli-spec.md)
- [Memory provenance, approval, retrieval, and privacy](docs/memory.md)
- [Relational data model and ordering invariants](docs/data-model.md)
- [Implementation plan and release checklist](docs/implementation-plan.md)

## License

Repository-owned Pellets source and binaries use the same
[Apache License 2.0](LICENSE) terms. Binary release archives also carry
[the consolidated third-party notices](THIRD_PARTY_NOTICES.txt). The license
and exact archive contract are recorded in
[ADR 0003](docs/decisions/0003-distribution-license-and-release-notices.md).
