# Implementation Plan

Implement Pellets as small vertical slices. Each milestone must leave the executable usable and tested; do not build a generic framework in anticipation of later features.

The product boundary is in [project-goals.md](project-goals.md), architecture in [architecture.md](architecture.md), schema in [data-model.md](data-model.md), and public command contract in [cli-spec.md](cli-spec.md).

## Minimal first-release scope

The first release includes:

- CGo-free Go executable for macOS and Windows;
- upward database discovery plus Git common-directory project and worktree/Git-directory workspace resolution;
- one database containing one or more logical projects, each with one or more registered workspaces;
- project codes and project-local pellet numbers;
- add, list, next, show, edit, move, start, start-next, release, close, reopen, defer, and confirmed stale-worktree recovery;
- exact external-ID and group filtering;
- sparse project-scoped integer priority for the active queue, with transactional rebalancing;
- FTS5 pellet search;
- independent FTS5 project memory with provenance and human approval;
- optional foreground, loopback-only HTMX inspector/editor with optimistic concurrency and invalidation-only live refresh;
- explicit closed-pellet purge;
- compact versioned JSON by default and optional human output;
- embedded forward database migrations;
- a database-independent installer for one embedded portable Pellets Agent Skill shared by Codex and Claude.

Anything outside this list is deferred unless required to satisfy an invariant or acceptance test.

## Milestone 0: executable and contract harness

Create the Go module, `cmd/pl`, strict command parsing, typed errors, JSON v1 envelope, human renderer interface, and command-level test harness. Add no database behavior yet.

Acceptance criteria:

- `pl --version` and `pl --help` work without a database.
- Unknown commands/flags produce JSON errors on stderr and exit 2.
- Successful JSON contains `schema_version`, `command`, and `data`.
- stdout is empty on error and stderr is empty on success.
- Golden tests cover compact JSON, pretty JSON, human output, help, and errors.
- Builds succeed with `CGO_ENABLED=0` for macOS and Windows AMD64.

## Milestone 1: database and project initialization

Implement SQLite connection setup, embedded migrations, `init-db`, upward discovery, Git repository/worktree identity, `init --code`, `project list`, and `project show`.

Acceptance criteria:

- `init-db` creates exactly `.pellets/pellets.db`, rejects symlink escapes and pre-existing SQLite companions, and does not overwrite or delete any existing path.
- `init --code foo` registers one logical repository plus its current worktree workspace, using normalized relative paths where possible.
- With no ancestor database, `init` creates one at the Git root.
- With a database at a common parent, the main work tree and at least two linked worktrees register as three workspaces of one project/code; unrelated sibling repositories register as separate projects with different codes.
- Repeating initialization is idempotent. Repository/code reuse, duplicate live worktree identity, cross-project attachment, and inconsistent Git identities are write-free typed conflicts. Moved, removed, and stale paths have the documented behavior.
- The nearest ancestor database wins when databases are nested.
- Duplicate identities and invalid codes fail cleanly; Git locations outside the database root use explicit normalized absolute storage.
- When either initialization command places `.pellets` inside a Git work tree, it is added to the local Git exclude and not to `.gitignore`.
- Initialization detects and rejects an already tracked database.
- Foreign keys, trusted-schema hardening, WAL, synchronous mode, busy timeout, and FTS5 capability are verified.
- `PRAGMA user_version` is the only persisted schema version; a new version-0 database reaches version 1 through embedded migration 1 and contains no migration bookkeeping table.
- Integration tests pass for paths containing spaces and Unicode on macOS and Windows CI.
- Migration 3 upgrades the frozen v1 schema without editing it, creates an initial workspace per project, assigns legacy in-progress rows, preserves authoritative/FTS/application state, and rolls back schema and `user_version` on injected failure.
- Real-SQLite constraints reject cross-project owners, ownerless in-progress rows, owned non-in-progress rows, and two in-progress rows in one workspace while permitting distinct workspace owners in one project.

## Milestone 2: basic pellet queue

Implement project-local number allocation plus `add`, `list`, `show`, `edit`, and read-only `next`. Use a stride of 1024 from the start, but postpone relative movement.

Acceptance criteria:

- If the first pellet is added as open, it is `<code>-1` with priority 1024; a pellet added directly to `maybe_later` has `NULL` priority.
- Concurrent open additions in one project receive distinct, increasing numbers and distinct non-null priorities.
- Numbers are independent across projects in one database.
- Default list returns only open/in-progress records in priority order.
- `--all`, status, exact external-ID, and exact group filters behave as specified.
- A pellet has at most one group; groups are opaque project-scoped strings with no separate table.
- Editing cannot change project, number, status, or priority.
- `next` resumes only the current workspace's in-progress pellet, otherwise returns the lowest-priority matching open pellet, and does not mutate or register anything.
- Empty list and next results are successful typed empty values.
- Timestamps round-trip from Julian storage to UTC RFC 3339 JSON.
- JSON golden tests lock all object and list shapes.

## Milestone 3: lifecycle and resumption

Implement workspace-aware `start`, atomic `start-next`, `release`, `close`, `reopen`, `defer`, and explicit confirmed recovery.

Acceptance criteria:

- The documented transition table is enforced.
- A workspace can never commit two in-progress pellets, while different workspaces in one project may each own one.
- `start-next` atomically resumes or starts distinct eligible pellets across concurrent worktrees with deterministic bounded retry and typed exhaustion.
- `next` returns only the current workspace's in-progress pellet before open pellets, even when that pellet lies outside a filter.
- `workspace_already_in_progress` and `pellet_in_progress_elsewhere` are stable conflicts with no partial write.
- Closing sets `completed_at` and clears priority; reopening clears `completed_at` and appends to the active queue. Idempotent repeats preserve the completion timestamp on an already closed pellet and the queue position of an already open pellet.
- Owner release retains priority and clears workspace ownership. Cross-workspace release/close/defer rejects by default and requires the exact explicit confirmed recovery override.
- Deferring or closing in-progress work clears workspace ownership; reopening never restores an owner.
- Closed and deferred pellets have `NULL` priority and are absent from the active-priority index.
- No agent, PID, session, claim, lease, heartbeat, expiry, assignment-history, or event row is stored.

## Milestone 4: relative ordering and rebalance

Implement `add --before/--after`, `move`, midpoint priority allocation, and rare project-scoped active-queue rebalance using a window function in one materialized-CTE update. The update writes into a fresh non-overlapping band above the old maximum.

Acceptance criteria:

- Head, middle, and tail insertions produce the requested order.
- Moving upward and downward excludes the moving pellet when finding neighbors.
- Cross-project movement is rejected.
- Moving or positioning relative to a closed or deferred pellet is rejected.
- All committed non-null priorities are positive and unique within the project; closed and deferred priorities are `NULL`.
- No floating-point priority arithmetic exists.
- Gap exhaustion triggers one deterministic rebalance and retry.
- The rebalance is one SQL statement inside the move transaction and rolls back with that transaction on failure.
- Rebalance does not touch closed or deferred rows and does not change logical order, pellet numbers, or unrelated `updated_at` values.
- Property tests compare thousands of random add/move operations against an in-memory reference list.
- Overflow and fresh-band preflight failures occur before the update and leave priorities unchanged.

## Milestone 5: task search

Add external-content FTS5 indexing and `pl search` over title, description, and external ID.

Acceptance criteria:

- Add, edit, and purge keep FTS rows synchronized transactionally.
- Ordinary punctuation and malformed FTS operators are safely treated as text.
- Task references and code identifiers containing hyphens/underscores are searchable.
- Exact external-ID and group filtering do not depend on FTS tokenization.
- Search is project-scoped and respects status filters.
- Search includes closed and deferred pellets by default.
- Rebuilding FTS from authoritative rows yields the same tested result set.
- FTS unavailability fails with `fts_unavailable`; no silent linear-scan mode is introduced.

## Milestone 6: project memory

Implement memory add, list, show, search, approve, and remove with its independent FTS5 index.

Acceptance criteria:

- Agent-created memory begins unapproved.
- Human-created memory is immediately approved.
- Approval is idempotent and retains its original timestamp.
- Memory has no pellet number, task foreign key, external-ID column, group column, status, or priority.
- Searching a textual pellet reference such as `foo-123` works.
- `--approved-only` excludes unapproved results.
- Pellet lifecycle and purge never mutate memory.
- Removing memory requires `--yes` and removes its FTS row atomically.
- No network, model, vector, embedding, or automatic memory creation code exists.

## Milestone 7: purge, migrations, and recovery behavior

Implement dry-run and confirmed purge, finalize migration validation, and harden busy/corruption/schema error paths.

Acceptance criteria:

- Purge requires an explicit project and either `--dry-run` or `--yes`.
- With no cutoff it deletes all and only closed pellets in the project.
- With a cutoff it compares `completed_at` and deletes only older closed pellets.
- Purge never resets the project number counter or deletes memory.
- FTS rows are removed in the same transaction.
- Embedded migration versions begin at 1, are unique and consecutive, and released migration files are immutable.
- Negative `user_version` values, version 0 with persistent schema, and newer versions are rejected with stable typed errors before persistent writes; opening the latest version performs no schema-version write.
- An older `user_version` is re-read after `BEGIN IMMEDIATE`; two processes racing to migrate apply every missing migration exactly once.
- Migration SQL, assertions, consecutive `user_version` advances, `foreign_key_check`, external-content FTS verification, and final database integrity diagnostics share one transaction and roll back together on failure. FTS rebuild checks pass after every supported migration endpoint.
- A read-only integrity diagnostic rejects corrupt and incompatible files before journal-mode or migration writes and emits no partial success.
- Busy errors, including migration-lock contention, stop within the configured five-second bound and map to the stable `database_busy` response.

## Milestone 8: foreground local web inspector

Implement `pl web` with standard-library HTTP/templates/embedding, pinned vendored HTMX, repository CSS and small JavaScript enhancements, a separate read-only/query-only pool, one separate writer connection, and exactly one pinned read-only/query-only `PRAGMA data_version` monitor connection.

Acceptance criteria:

- Normal upward database discovery runs before startup. The listener is hard-coded to `127.0.0.1`, defaults to an OS-selected port, supports `--port`/`--no-open`, prints readiness URL, opens the browser after readiness, warns without exiting on launcher failure, and shuts down cleanly on interruption.
- Empty, one-project, and multi-project databases render without crossing project boundaries. Wide multi-project navigation, narrow drawer navigation, stable task/memory deep links, task table ordering, composable URL filters, exact ungrouped handling, and safe escaped FTS search are covered with deterministic handlers.
- The interface displays complete project/workspace ownership, pellet lifecycle/order/identity, and memory provenance/approval/timestamps. It supports pellet create/scalar edit/reorder/lifecycle, memory create/text edit/approve, and explicit named workspace recovery. It exposes no purge or removal.
- Every existing-row mutation validates a complete-row token under the short writer lock. Concurrent edits yield one commit and one write-free 409 containing current row plus preserved draft. Memory text/FTS changes are atomic and agent-memory approval resets when text changes.
- Every GET uses `mode=ro` plus `query_only=ON`, materializes and closes rows before output, and cannot retain a transaction across a slow response. Mutation parsing/validation finishes before `BEGIN IMMEDIATE`, and commit/rollback finishes before rendering.
- One pinned monitor connection compares its own `data_version` only while SSE clients exist. External CLI and separate web-writer commits generate coalesced invalidation; rollback/read activity is silent. SSE client queues are bounded and own no database handle. Native EventSource refresh, initial loads, and slower HTMX polling recover missed signals.
- Exact Host/Origin, per-process CSRF cookie/form capability, method/media-type checks, escaping, CSP, framing/MIME protections, and loopback-only binding protect mutation routes.
- Vendored HTMX and license work offline. No Node/npm, CDN, external font/icon, SPA/CSS framework, SSE extension, WebSocket, service worker, daemon, or background service is added.
- Automated markup/style tests cover system/light/dark pre-paint theming, narrow/wide layouts, visible focus, dialog/focus/dirty behavior, changed-row animation, reduced motion, and WCAG AA palette contrast. A hands-on macOS browser smoke check covers both themes, zoom/reflow, keyboard navigation, deep-link/back/forward behavior, and live external changes.

## Milestone 9: portable agent skill installer

Implement `pl skill install` with an embedded instruction-only `pellets/SKILL.md`, deterministic repository/personal and Codex/Claude target selection, an injected standard-library wizard, read-only dry-run planning, conflict refusal, atomic replacement, and multi-target rollback. This command bypasses Pellets database discovery.

Acceptance criteria:

- The generated frontmatter is valid portable Agent Skills metadata with `name: pellets` and a narrow description that requires explicit `pl` or Pellet/Pellets naming while rejecting generic task/project/memory triggers.
- The shared instructions use only implemented CLI commands and teach atomic `start-next`, current-workspace resumption, conflict/recovery behavior, lifecycle/order/project/group/external-ID semantics, focused follow-ups, and memory provenance/approval.
- Repository and platform-home roots produce the exact Codex, Claude, and Both path matrix; nested paths, linked worktrees, spaces, Unicode, macOS homes, and Windows path behavior are covered.
- JSON never prompts. Noninteractive missing choices and confirmation fail with stable typed errors; interactive choices, exact path preview, replacement confirmation, final confirmation, and cancellation use injected input/output/terminal detection.
- Dry-run creates no directories or temporary files and returns the complete plan/content. Identical targets are idempotent. Differing files require explicit replacement authority.
- Complete preflight rejects symlinks, non-regular paths, escapes, and unusable permissions. Atomic per-file writes and injected second-target failures prove Both restores replaced files and removes invocation-created files/directories.
- Golden frontmatter/body tests plus static positive/negative trigger fixtures preserve the activation boundary. A drift contract parses every documented command example and rejects referenced flags without implemented CLI coverage.
- Real-filesystem, CLI-harness, and compiled-executable tests prove no-database operation, scope/agent behavior, Git discovery, protected unrelated files, JSON/error shapes, and full content installation. `CGO_ENABLED=0` macOS and Windows builds add no runtime, prompt, network, or plugin dependency.

## Milestone 10: release hardening

Exercise complete workflows, establish build provenance, and publish release archives.

Acceptance criteria:

- CI runs unit and integration tests on current supported macOS and Windows runners.
- Release builds cover macOS AMD64/ARM64 and Windows AMD64 with `CGO_ENABLED=0`.
- Windows ARM64 is either tested and released or explicitly excluded from the support matrix.
- Archives contain the executable, licenses, and checksums only.
- A clean machine can run `pl --version`, initialize, and complete a workflow without a SQLite DLL or network access.
- The documentation contradiction checklist below passes.
- Manual macOS smoke tests pass; Windows smoke tests run in CI with retained logs/artifacts.

## Testing strategy

### Unit tests

- reference parsing and project-code validation;
- status transition matrix;
- FTS query escaping;
- JSON rendering and stable error mapping;
- portable skill frontmatter, trigger fixtures, command/flag drift, prompt state, and installation result rendering;
- relative/absolute platform path normalization and Git repository/workspace identity;
- midpoint and overflow arithmetic.

### SQLite integration tests

Run against temporary real SQLite files, not a mocked SQL interface:

- all migrations from an empty database;
- an injected two-step migration sequence, upgrade from an older released fixture, latest-version no-op open, and negative/unsupported/newer write-free rejection;
- SQL, assertion, `user_version`, and `foreign_key_check` failure rollback with the preceding schema and version intact;
- corrupt and incompatible file rejection before writes, deterministic SQLite-result-code mapping, and external-content FTS drift detection/rebuild verification;
- workspace ownership constraints and indexes, including direct invalid SQL;
- transaction rollback and busy behavior;
- project-local number allocation;
- sparse ordering and rebalance;
- transactional FTS maintenance, ranking, and rebuild;
- purge cascades into derived FTS only;
- multiple workspaces sharing one project and unrelated projects sharing one database.

### Command integration tests

Invoke the compiled executable in temporary Git repositories and assert stdout, stderr, exit code, database location, local Git exclude changes, and final rows. Cover nested directories, sibling repositories, a main work tree plus two linked worktrees, worktree move/removal/duplicate/stale registration, and filenames with spaces/Unicode.

Invoke the compiled skill installer in temporary home and repository fixtures without a Pellets database. Assert exact destinations and content, default JSON non-interactivity, dry-run write freedom, idempotence/conflicts, and unchanged Git index/ignore/config files.

### Property and concurrency tests

- Randomized ordering operations against a simple slice model.
- Combined project, external-ID, and group filters across list, next, and search.
- Concurrent `add` calls within one project.
- Concurrent `start-next` calls from distinct linked worktrees selecting distinct pellets; same-pellet and same-workspace races return stable conflict/exhaustion without partial writes.
- Reads during a write and bounded busy-timeout behavior.
- Query-only web reads, pinned `data_version` monitoring, multiple/slow SSE clients, burst coalescing, disconnected recovery, zero-client idling, and graceful shutdown.
- Concurrent web edits, current/foreign workspace lifecycle controls, memory text/FTS replacement, security headers/CSRF/origin rejection, and compiled foreground command startup/interruption.
- Two independent processes racing from the same old schema version, with the lock waiter applying nothing twice.
- Independent activity in different projects sharing the same SQLite file.

### Compatibility tests

- Golden JSON fixtures per command are treated as public API tests.
- Keep a frozen real-database fixture at every released schema version and test forward migration. Never regenerate an old fixture from mutable current migration input.
- Treat every shipped migration file as immutable; add a new consecutive migration for schema changes.
- Build and test with the pinned SQLite driver on every target matrix entry before upgrading it.
- Treat the embedded portable skill and positive/negative trigger fixtures as compatibility artifacts. Parse its frontmatter and every referenced `pl` command/flag so CLI and skill changes cannot drift independently.

## Cross-platform release strategy

Use ordinary Go cross-compilation with a pinned CGo-free SQLite driver. Prefer a small repository-owned release script plus CI over introducing a release framework until signing or packaging requirements justify one.

Required CI jobs:

- macOS ARM64 or AMD64 tests;
- Windows AMD64 tests under PowerShell and native filesystem semantics;
- cross-build macOS AMD64/ARM64 and Windows AMD64;
- archive-content and checksum verification;
- a no-network smoke test after dependencies are already vendored/cached for the build.

Because hands-on Windows testing is unavailable, Windows-specific integration tests are release-blocking. Do not claim Windows ARM64 support based only on successful compilation.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Agent output changes break automation. | Version JSON, use golden fixtures, and treat it as public API. |
| Immediate SQLite uniqueness checks break in-place normalization. | Materialize the old order, preflight overflow, and assign unique values in a fresh band above the old maximum with one CTE update. |
| Several projects contend for SQLite’s single writer. | Keep writes short, use WAL/busy timeout, and perform no Git/filesystem/browser/render/network work inside transactions. |
| A slow local browser holds a read lock or blocks notifications. | Materialize query rows before response writes, give SSE clients bounded non-blocking queues, and keep clients database-free. |
| Cross-process live refresh misses or misinterprets changes. | Compare `PRAGMA data_version` only on one pinned monitor connection, treat changes only as invalidation, and retain initial/fallback authoritative GETs. |
| A moved, copied, or removed worktree presents stale local paths. | Treat Git directory as workspace identity, update only after outside-transaction stale checks, reject live duplicates, retain removed registrations, and require explicit lifecycle recovery. |
| Windows-only path or locking failures go unnoticed. | Make native Windows CI integration tests release-blocking. |
| Database is accidentally committed. | Use local Git exclude, check tracking during init, document local-only storage. |
| FTS derived rows drift. | Explicit same-transaction maintenance plus a tested rebuild command/path. |
| Project code changes invalidate textual references. | Treat project codes as immutable in v1. |
| Group grows into an epic or tag subsystem. | Keep it a single nullable exact-filter string with no table, metadata, hierarchy, or behavior. |
| Julian floating-point timestamps surprise API users. | Keep storage internal and render stable UTC RFC 3339 timestamps. |
| Scope expands toward Beads. | Require a decision record for new core concepts and enforce explicit non-goals. |

## Features deliberately deferred

- dependencies, blocking, graphs, epics, subtasks, and milestones;
- agent accounts, PID/session ownership, claiming, leases, heartbeats, expiry, assignment history, and orchestration;
- tags, separate notes, and automatic task history;
- multiple groups per pellet or a group entity;
- archive state and arbitrary task deletion;
- semantic/vector memory and embedding providers;
- memory automation, categories, and task links;
- Git/database synchronization, import, and export;
- cloud service or remote API;
- custom statuses, custom workflows, and plugins;
- project-code rename and project deletion;
- automatic `VACUUM`;
- a terminal UI, hosted web service, remote bind address, or daemon (the foreground loopback web inspector is the sole graphical surface).

## Contradiction checklist

Before each release, verify:

- no schema, command, or prose introduces dependency concepts;
- no priority path uses floating-point arithmetic or a linked list;
- non-null priority remains unique per project, not per database or external ID; only open/in-progress pellets have it;
- closed and deferred pellets have `NULL` priority and are not reprocessed by active-queue rebalance;
- group is one optional exact-filter value per pellet, not a tag set, entity, hierarchy, or ordering input;
- `next` is read-only and resumes only the current workspace; `start-next` is the atomic begin-work path;
- the database supports several projects but pellet numbers are project-local;
- one logical repository has shared project state across worktrees, at most one worker is assumed per worktree, and each workspace owns at most one in-progress pellet;
- no schema or prose invents an agent/PID/session/lease/heartbeat/expiry ownership model;
- memory has no task foreign key and uses FTS5 only;
- no core behavior needs external network access or a vector capability; the optional web inspector uses loopback only;
- the database is never part of a Git synchronization workflow;
- JSON v1 fixtures and exit codes match [cli-spec.md](cli-spec.md).
