# Pellets

Pellets (`pl`) is a local SQLite task queue for coding agents. It keeps a
deterministic project queue, worktree-scoped in-progress ownership, exact
external-ID and group filters, FTS5 keyword search, and independent project
memory in a shared local `.pellets/pellets.db`.

Pellets is one CGo-free executable with SQLite embedded. It has no account,
telemetry, cloud synchronization, plugin runtime, daemon, or required service.
Git must be available for repository and worktree discovery. Its optional
foreground server manages a verified, versioned Codex runtime, but
ordinary queue, memory, and inspection use never require Codex or an account.

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

To build and install from the checkout into `~/.local/bin`, run:

```bash
./scripts/build-install.sh
```

You can pass a different install directory, for example
`./scripts/build-install.sh "$HOME/bin"`. The script works from any working
directory and prints a `PATH` setup command if needed.

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
pl add "First pellet"
```

The first valid command that needs the current project creates the database at
the worktree root when no binding or discoverable database exists, registers the logical Git
repository and current worktree, and then performs the requested operation.
There is no separate project-initialization command.

Pellets derives the initial canonical public project code from the logical
repository name. It lowercases ASCII, converts each run of other characters to
one hyphen, trims edge hyphens, and uses the result directly when it is non-empty,
at most 12 characters, and unreserved. Empty, longer, or already-reserved names
use up to three normalized prefix characters (or `p`) plus `-` and the first
eight hexadecimal digits of SHA-256 over the normalized repository identity.
If that candidate is also claimed, Pellets deterministically rehashes the
identity with increasing canonical decimal attempts until it finds a free
code. Every result uses 1–12 lowercase ASCII letters, digits, or internal
hyphens. A registered logical repository always reuses its stored current
canonical code.

### Several repositories with one common-parent database

Create the database before first use in the sibling repositories:

```text
cd path/to/common-parent
pl init-db

cd service-a
pl add "First service-a pellet"

cd ../service-b
pl list

cd ..
pl project list
```

Each registered project keeps its own pellet numbers, queue order, filters,
and memories even though SQLite stores them in one file. `pl project show
svc-a` displays a project's registered workspace identities.

Linked Git worktrees automatically share the repository's database, even when
located outside the original checkout. The first current-project command records
a binding in Git's shared common directory. Each linked worktree uses that
binding, reuses the logical project's stored code, and attaches a distinct
workspace. A common-parent database remains useful for sharing across unrelated
repositories, but is no longer required for linked worktrees.

### Discovery and Git safety

Inside a Git repository, normal commands first consult
`pellets-database.json` in Git's common directory. This binding takes precedence
over any nearer database. Without a binding, discovery walks upward from the
current directory and also checks registered worktrees for an existing database.
Multiple different databases across those locations produce
`database_binding_conflict` instead of choosing a queue. If none exists, a
current-project command creates the database at its worktree root. Concurrent
first use is serialized so linked worktrees converge on one database.

Outside Git, commands use the nearest ancestor `.pellets/pellets.db`, preserving
the common-parent layout. There is no database-path flag; `--project CODE`
selects a registered project where permitted, not a different database.
`init-db` explicitly creates a database at the current directory; it does not
replace an existing repository binding.

Bindings store paths relative to Git's common directory when possible, so moving
the repository together with its database preserves discovery. An unavailable
bound database returns `database_binding_unavailable`; Pellets never falls back
to another queue. Restore the database at the reported path, or deliberately
repair the JSON binding to its new `.pellets/pellets.db` location after stopping
Pellets commands. Malformed bindings return `database_binding_failed`.
A setup interrupted before publishing the binding can leave a
`pellets-database.json.lock` directory; `database_binding_busy` reports its path.
Remove that directory only after confirming no Pellets setup is running, then retry.

The database is local plaintext and must never be committed. When `.pellets`
is inside a Git worktree, `init-db` or automatic bootstrap adds `.pellets/` to
that worktree's local Git exclude file, not its committed `.gitignore`, and
refuses to proceed if the database or an SQLite companion file is tracked. Do
not stage or commit `.pellets`, and protect and back it up like any other local
SQLite data.

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

Pellet references such as `demo-1` contain a canonical or former project code
and a monotonically allocated project-local number. Save the `data.id` returned
by `add` instead of assuming a number in automation.

### Retry an add safely

Supply a project-scoped request ID when an agent may need to retry after losing
command output:

```text
pl add "Implement parser" --request-id "parser-attempt-84" --description "Reject malformed input."
```

Reusing that ID with the same creation inputs returns the original creation
result without allocating another pellet. Different inputs return
`request_id_conflict`. Reuse the same ID across retries; use a new ID for each
new intended task. `external-id` remains an independent correspondence field.

Every successful add, including an add without a request ID, deletes retry
records older than two days across the database. This never deletes pellets or
memory. Age starts at the original creation; retries do not refresh it. Once a
record expires, that ID is treated as a new request. No background job runs.

Replayed results describe the original creation, even if the pellet was later
edited, closed, or purged; replay does not undo those actions. Use `pl show` for
current state. Project references in replayed output use the current canonical
project code.

### Rename a project without breaking old references

Rename the current logical project with:

```text
pl project rename new-code
pl project show new-code
```

The project keeps its stable database identity and pellet numbers. Its former
code becomes a direct redirect, so old references, scripts, filters, and deep
links still resolve while successful JSON and human output use `new-code` and
`new-code-N`. `pl project list` and `pl project show` display each project's
canonical code and redirects.

A canonical code owned by another project is always a hard conflict. If the
requested code is another project's redirect, default JSON and other
noninteractive invocations return `project_rename_confirmation_required` with
the exact conflicting rules and retry arguments; they never wait for input.
Review that payload before the explicit automation-safe retry:

```text
pl --project old-code project rename new-code \
  --delete-conflicting-redirects --yes
```

Only terminal `--human` mode offers the equivalent default-no prompt. Deleting
a conflicting redirect can break or reinterpret references held outside the
database. The rename revalidates the displayed rules inside one transaction,
and any changed conflict or failure leaves project and redirect state intact.

### JSON is the agent interface

Compact JSON is the default; there is no `--json` flag. Each successful
short-lived command writes one versioned object and a newline to stdout. For
example, the read-only `next` command above returns this shape:

```json
{"schema_version":1,"command":"next","data":{"selection_reason":"next_open","pellet":{"id":"demo-1","project":"demo","number":1,"title":"Implement parser","description":"Reject malformed input.","external_id":"github:acme/demo#84","group":"parser","status":"open","priority":1024,"workspace":null,"created_at":"2026-08-29T20:00:00Z","updated_at":"2026-08-29T20:00:00Z","completed_at":null}}}
```

`next`, list/search/show reads, and dry runs do not change operation state once
the current project/workspace is registered. On first use only, a valid
current-project command may create and register local Pellets metadata before
executing its otherwise read-only operation.

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

### Review selected implementations

```text
pl add "Review parser changes" --review-targets foo-12,foo-15 --request-id parser-review-1
pl show foo-16
```

A review checkpoint retains exactly the selected ordinary Pellets and their
scope, and is inserted after their last active queue position. Completed
targets are allowed. Readiness requires every selected target to be closed
with matching durable implementation-run and verified commit evidence, even
when implementation happened in another worktree. Queue closure alone is
insufficient. Waiting checkpoints are skipped for unrelated eligible work.

Checkpoint JSON adds `kind: "review_checkpoint"` and a versioned `checkpoint`
object containing readiness, target identities, waiting reasons, and exact
run/commit evidence. Ordinary command output is unchanged. The server's New
task form accepts optional Review targets and its inspector shows this scope.
Execution requires the separate checkpoint review policy; an absent policy is
reported before dispatch. See [the CLI contract](docs/cli-spec.md#pl-add).

The inspector reads durable review outcomes for that checkpoint's exact
implementation generation. It shows clean/findings/needs-attention status,
partial or complete triage counts, created follow-up links, and non-creation
dispositions. Reconnecting or running another Pellet in the same workspace
preserves these results. Purged follow-ups retain their reference with a
“no longer present” label; reopening the checkpoint starts a new generation.
Reviewer transcripts, commands, full diffs, and assessment prose are not shown.

Checkpoint execution reviews the exact commits, then independently triages
findings against current code, repository instructions, and existing active
Pellets. Each valid distinct issue becomes an ordinary follow-up with context
and acceptance criteria, immediately after the checkpoint in deterministic
order and with its exact group/external-ID. Invalid, already-fixed, stylistic,
duplicate, and already-queued issues retain explicit dispositions. Permanent
finding receipts prevent duplicate creation across crashes and expired add
request IDs. Resume preserves completed triage and finishes only the remaining
findings; closure waits for every follow-up to be durably reconciled. Reviews
and triage do not fix code, create commits, or create another checkpoint.

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

## Local foreground server

```text
pl server
pl server --no-open
pl server --port 8123 --no-open
pl --project demo server
```

`pl server` is an optional foreground operator tool over the same resolved
database and performs the same automatic first-use bootstrap. It listens only
on `127.0.0.1`, uses embedded offline assets, and stops when interrupted. It
owns the browser UI and any Codex execution it starts: closing a browser tab
does not stop that work, while stopping the server does. It can inspect every
registered project, workspace, pellet state, and memory; it supports routine
queue and memory edits with optimistic conflict detection. Purge, memory
removal, and other irreversible actions are intentionally absent.

The browser keeps the database location and project navigation visible, even
with one project. Each project has Queue, Workspaces, and Memory views. The
queue is shared across worktrees; workspace cards open dedicated activity and
run controls at `/projects/CODE/workspaces/ID`. Workspace attention counts stay
visible in the Workspaces tab while browsing the queue or memory. Search stays visible,
with optional filters in a disclosure. Task titles open the inspector; Queue
order restores priority sorting while retaining filters. Project details holds
the theme setting and repository metadata.

`pl web` remains a deprecated compatibility alias with the same options and
foreground behavior. New scripts and documentation must use `pl server`.

Codex execution automatically installs the reviewed 0.154.0 runtime for macOS
AMD64/ARM64 and Windows AMD64, independently of Homebrew, Node, PATH, or the
Codex desktop app. Runtime compatibility is checked before claiming eligible
work. Explicit executable overrides, cache/offline setup, authentication, updates,
legacy settings, and Resume are documented in [managed Codex runtime](docs/codex-runtime.md).

The internal [Codex stdio adapter](docs/codex-app-server.md), run preflight,
and workspace scheduler are wired to the foreground server lifetime. Internal
HTTP interfaces expose Run one, Drain, Watch, explicit Resume, and both stop
actions. The browser shows each registered workspace's durable latest activity,
phase, terminal result, and current foreground receipt; controls refresh from the
server after every action and never expose a command log or transcript. Preflight verifies the
managed or explicitly overridden runtime's version, protocol, local account, normal
workspace configuration, managed requirements, and model capabilities without
copying credentials or starting a model turn. Prepared runs use
`workspace-write` plus `on-request` automatic approval review, retain narrow
access to an out-of-worktree bound Pellets database, and preserve conversations
through thread read/resume operations. Credential-free settings are persisted
by stable workspace identity, and one-run overrides may select an executable,
open-ended model ID, supported reasoning effort, and
bounded transport limits; normal Codex defaults remain valid. This does not
introduce a second model runtime or make normal Pellets use depend on Codex.

New conversations receive a deterministic Pellets workflow prefix before the
selected task. Pellets reuses the captured conversation on Resume instead of
resending that prefix, and exposes cached-input-token telemetry only when Codex
reports it. This stable-prefix layout is intended to make provider caching
possible; it does not promise a cache hit or infer one when telemetry is absent.
Pellets remains the task-planning source of truth—runs and checkpoints retain
execution evidence, not a parallel plan or backlog.

The internal execution recorder also persists attempts, Codex thread/turn IDs,
captured settings and filters, interruption outcomes, and verified commit
evidence. These records survive reconnects, project renames, and pellet purge;
bounded activity retention preserves the original review target. They do not
add another queue. Credentials and full transcripts
remain outside Pellets. See [execution evidence](docs/data-model.md#durable-execution-evidence).

The supervisor permits one active execution per canonical Git worktree across
participating servers. Existing linked worktrees and independent projects may
run concurrently with their own cwd and settings. This lock is separate from
`pl start-next`; external agents that do not participate can still conflict and
must be coordinated separately. Server shutdown stops admission, requests an
active turn's interruption, then stops and reaps its owned Codex process family
before releasing the lock. It never selects processes by name or terminates
unrelated Codex sessions. Crashes or unconfirmed cleanup leave an explicit
recovery fence with the exact database/run receipt; restarting does not replay
work. Do not delete `pellets-execution.lock` from Git's worktree metadata.

Schedules require an explicitly chosen existing workspace and freeze exact
group/external-ID filters. Run one completes one pellet; Drain advances until
none are eligible; Watch waits for database changes with a 30-second recovery
check and makes zero model calls while idle. Both repeating modes stop at a
visible configurable limit (100 by default). Existing in-progress work needs
explicit Resume and must match the filters. Stop after pellet lets active work
finish; Stop now interrupts it. Only an exact successful turn, verified new
commit, and closed target pellet permit advancement. Failures stop for attention
without hidden retries. See [scheduler interfaces](docs/codex-app-server.md#foreground-scheduler).

Startup marks abandoned active attempts interrupted or needing attention and
waits for explicit Resume. Resume shows the saved pellet, phase, mode, remaining
limit, and exact filters. For ordinary implementation before finalization, Resume adopts the current HEAD
while preserving unfinished edits and the previous attempt, and asks Codex to
reassess the code. It checks the original worktree/branch, ownership,
commit evidence, and saved Codex history before continuing. Missing or ambiguous
evidence stays visible and preserved. Checkpoint recovery requires its own
idempotent policy. An uncertain Windows crash receipt remains fenced because
the current unnamed Job Object cannot be inspected after server death; see
[recovery limits](docs/codex-app-server.md#startup-and-explicit-recovery).

If preflight failed before a run was recorded, or `pl start-next` claimed the
pellet without running Codex, its workspace card still offers explicit Resume.
There is no saved conversation or durable schedule intent in this case. Choose
a new Run one, Drain, or Watch mode and confirm its limit and exact filters;
the form initially suggests the pellet's current metadata, independently of
the page filters. Blank filters mean any value. Resume revalidates the exact
owned pellet and repaired preflight before starting a new conversation.
A reopened pellet has a new implementation revision; a completed run from its
previous revision does not prevent this recovery or supply the new conversation.

Ordinary runs preserve existing edits. Codex implements and verifies the exact
pellet, then returns a structured result. The server checks the reported files,
stages only those paths, creates one pellet-ID commit, verifies it, and closes
the pellet. Codex's implementation phase cannot commit, close, release, defer,
select more work, or create follow-ups. Explicit finalization recovery reuses
the persisted tree and existing commit without repeating implementation or
tests. Verified already-satisfied work closes without a new commit. Unrelated
staged and unstaged edits remain intact and do not block completion.

Press Ctrl+C to stop (SIGTERM also requests orderly shutdown). Pellets immediately acknowledges the interrupt on stderr,
closes live-update streams, and allows ordinary requests up to five seconds to
finish. A second Ctrl+C forces exit if shutdown stalls. Stdout remains reserved
for the listener URL.

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
  account system, or general plugin framework. Optional Codex execution is
  local, foreground-server-owned supervision of the managed runtime, not a
  persistent worker or an alternative agent integration.
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

The optional browser regression suite uses Playwright and a temporary database:
`node scripts/test-web-browser.cjs`. Make `playwright` available on Node's module
path (or set `NODE_PATH`), and set `PLAYWRIGHT_CHANNEL=chrome` to use installed
Chrome instead of Playwright's Chromium. It covers the Datastar navigation,
forms, live refresh, conflict handling, and keyboard flows. Also run
`node scripts/test-web-recovery-browser.cjs` for authentication/config
failure before run capture, repaired explicit Resume, server restart, and
CLI-started ownership. This uses the compiled server and a deterministic Codex
protocol peer in disposable repositories, without a model service or account.
`node scripts/test-web-runtime-browser.cjs` verifies pre-claim version rejection
and sanitized, durable Codex errors after browser/server restart.
`node scripts/test-web-checkpoint-browser.cjs` covers durable clean/findings
outcomes, partial triage and recovery, follow-up navigation, server restart,
later workspace runs, narrow inspectors, and generation isolation.
Node and Playwright are development tools only; `pl server` serves embedded
assets and works offline.

The foreground execution integration is part of `go test ./...`. Its scripted
app-server runs only in disposable Git repositories and databases and makes no
account, network, or model call. It covers Run one, filtered Drain, idle Watch
wakeup, linked-worktree concurrency/evidence, concise activity, questions and
follow-ups, stop/shutdown, explicit Resume, checkpoint review, and idempotent
finding triage. To check an installed Codex version and the real ephemeral
thread request shape—including `workspace-write`, `on-request`, and
`approvals_reviewer=auto_review`—without starting a model turn, opt in explicitly:

```text
PELLETS_CODEX_LIVE=1 go test ./internal/codex -run '^TestInstalledRuntime$' -v
```

Normal tests skip this check. It uses the managed runtime (or
`PELLETS_CODEX_EXECUTABLE`) and the current OS user's account/configuration;
Codex continues to own those credentials.

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
