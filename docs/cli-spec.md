# CLI Specification

The product is Pellets; the executable is `pl`. The CLI is designed for coding agents first. Compact JSON is the default interface.

See [project-goals.md](project-goals.md) for product intent, [data-model.md](data-model.md) for invariants, and [memory.md](memory.md) for memory behavior.

## Conventions

```text
pl [global-options] <command> [command-options] [arguments]
```

- Commands and long flags use lowercase kebab-case.
- Mutating pellet commands accept a canonical or redirected reference such as `foo-123`; successful output canonicalizes it.
- Parse a reference at its final hyphen: `foo-bar-123` means project `foo-bar`, pellet number `123`.
- Pellet numbers are canonical unsigned decimal without leading zeros.
- Bare numbers are rejected because one database may contain several projects.
- Project codes are 1–12 lowercase letters, digits, or internal hyphens.
- External IDs are optional, opaque, case-sensitive strings matched exactly by filters.
- Groups are optional, opaque, case-sensitive strings. A pellet has at most one group, and group filters are exact and project-scoped.
- Dates accepted from the CLI use RFC 3339 or `YYYY-MM-DD`; stored dates use SQLite Julian `REAL` values.
- Results are compact JSON followed by one newline unless `--human` is set.
- Unknown flags and positional arguments are errors; the parser never silently guesses.

## Global options

| Option | Meaning |
|---|---|
| `--human` | Render concise human-readable text instead of JSON. |
| `--pretty` | Pretty-print JSON. Mutually exclusive with `--human`. |
| `--project CODE` | Select a registered project explicitly where the command permits it. |
| `--help` | Print help to stdout and exit successfully. |
| `--version` | Print executable and JSON schema versions. |

There is no `--json` flag because JSON is already the default. There is no color in JSON. Human output uses color only on a terminal and honors `NO_COLOR`.

## Database and project selection

After strict parsing and usage validation, commands inside Git first use the
repository binding, `pellets-database.json` in Git's shared common directory.
A binding takes precedence over nearer databases. Without one, inspect ancestor
`.pellets/pellets.db` paths from the current directory and existing registered Git
worktrees; multiple distinct databases return `database_binding_conflict`.
Outside Git, use the nearest ancestor database across repository boundaries.
There is no database-path override; `--project` selects within the resolved database.

If discovery finds no database, the first valid current-project command creates
`.pellets/pellets.db` at the current Git worktree root. It registers the repository
and workspace and publishes the binding before executing the requested operation.
First use is serialized through Git's common directory, so concurrent commands in
separate worktrees share one queue. Existing installations acquire a binding on
their first successful current-project bootstrap after upgrading. `add`,
list/search/show/next and lifecycle commands, every `memory` operation, current
`project show`, and `server` have this capability.

Help/version, invalid invocations, `init-db`, and `skill install` do not use this
bootstrap. `init-db` explicitly creates at the current directory without replacing
a repository binding. Database-level reads and purge honor existing bindings and
discovery but do not create bindings or databases; absent databases return
`database_not_found`.

Bindings contain version-1 JSON with `version` and `path`, relative to Git's
common directory when possible. An unavailable target returns
`database_binding_unavailable` (exit 5) with `binding_path` and `database_path`;
restore the target or repair the binding after stopping Pellets commands. Invalid
bindings return `database_binding_failed` (exit 5). Neither permits fallback or
creation. Conflicting discovered databases return `database_binding_conflict`
(exit 4). Setup lock contention returns `database_binding_busy` (exit 4) after
five seconds with `lock_path`; retry, or remove a stale lock directory only after
confirming no setup remains active.

Project codes are generated without prompting. The logical repository name is the directory containing a `.git` common directory, or a bare common-directory basename with one terminal `.git` removed. Pellets lowercases ASCII letters, preserves ASCII digits, collapses every run of other characters into one hyphen, and trims edge hyphens. A non-empty normalized name of at most 12 bytes is the first candidate. Empty or longer names, and candidates already reserved as either a canonical code or redirect, use up to three normalized prefix bytes (or `p`), `-`, and the first eight lowercase hexadecimal SHA-256 digits over `true:<relative-common-dir>` or `false:<absolute-common-dir>`, using the same slash/case normalization stored in SQLite. If that candidate is occupied, attempts rehash the identity plus a NUL byte and the increasing canonical decimal attempt. Allocation and registration share one immediate transaction. An already-known common-directory identity ignores new checkout names and always reuses its stored current canonical code.

Bootstrap writes are a one-time pre-command effect, not a change to operation semantics. Once the repository is bound and the exact project/workspace is registered, `next`, `list`, `search`, `show`, memory reads, named/database-level project reads, and every dry run retain their write-free guarantees. On first use only, a valid current-project command can create the database, update Git's local exclude, publish its shared binding, and transactionally register the project/workspace before running an otherwise read-only operation. The requested read itself still performs no queue or memory mutation.

`--project CODE` and every command input that accepts a project code resolve a canonical code or one direct redirect to the stable project row. Redirects are never followed recursively. `--project CODE` does not silently let a caller mutate an unrelated repository: for pellet mutations, the resolved stable project ID in the pellet reference must match the selected/current project. Database-level and read-only administrative commands may operate across registered projects when explicitly documented. Successful JSON and human output always emits the current canonical project code and pellet references.

## Commands

### `pl init-db`

Create `.pellets/pellets.db` beneath the current directory, without registering a project.

```text
pl init-db
```

Use this at a common parent before first use in sibling repositories or linked worktrees. Fail if the database, its WAL/SHM/journal companions, or a symlinked `.pellets` metadata directory already exists; never overwrite or remove any of them.

If the new database is inside a Git work tree, add `.pellets/` to Git’s local exclude file and fail if the database or any SQLite companion path is already tracked. Index-only entries and case-equivalent paths on case-insensitive filesystems count as tracked.

### `pl project list`

List registered logical projects and every workspace identity in the selected database. This is a database-level read command and does not require the current directory to be inside a registered project. It never registers or repairs a workspace.

### `pl project show [CODE]`

Show the current logical project, or a named project when `CODE` is supplied, including its current canonical code, direct redirects, Git common directory, registered workspace IDs, roots, Git directories, relative/absolute flags, and timestamps. Current `project show` bootstraps on first use; positional `CODE` or global `--project CODE` is a database-level read and never registers the current directory. A redirected `CODE` resolves the project but the result emits its current canonical code.

Project `list` and `show` use this data shape (timestamps omitted here only for brevity):

```json
{"code":"bar","git_common_dir":"main/.git","git_common_dir_relative":true,"workspaces":[{"id":1,"root_path":"main","root_path_relative":true,"git_dir":"main/.git","git_dir_relative":true},{"id":2,"root_path":"linked","root_path_relative":true,"git_dir":"main/.git/worktrees/linked","git_dir_relative":true}],"redirects":[{"code":"foo","created_at":"2026-08-31T12:00:00Z","updated_at":"2026-08-31T12:00:00Z"}]}
```

### `pl project rename NEW_CODE`

Change a logical project's canonical public code while preserving its former code as a direct redirect.

```text
pl [--project CODE] project rename NEW_CODE
    [--delete-conflicting-redirects --yes]
```

Without `--project`, rename selects the current logical project and may bootstrap it on first use. `--project CODE` may itself be canonical or redirected and performs a database-level selection. Renaming `foo` to `bar` keeps the stable project ID and all project-local pellet numbers, makes `bar` canonical, and adds the direct redirect `foo -> bar`. Existing `foo-N` inputs continue to resolve, while all successful results emit `bar` and `bar-N`.

Renaming to the current canonical code is an idempotent success. Renaming to a redirect already owned by the same project promotes that code without confirmation and preserves the former canonical code as a redirect. The canonical code of another project is `project_code_already_registered`, a hard conflict that is never eligible for deletion.

If `NEW_CODE` is a redirect owned by another project, JSON and every noninteractive invocation return `project_rename_confirmation_required` without reading stdin. Its details contain every conflicting `code` and `canonical_target`, the warning that deletion can break or reinterpret old pellet references, and the exact `retry_argv`. Automation may retry only with both `--delete-conflicting-redirects --yes`. Those flags must be supplied together. The rename transaction revalidates the complete displayed conflict set and deletes only those rules; a changed set returns `project_redirect_conflicts_changed` without writes.

Only terminal `--human` mode prompts. It lists every conflicting rule and target, repeats the warning, and asks `Delete only these redirect rules and rename OLD_CODE to NEW_CODE? [y/N]:`. Answering no, EOF, or interruption cancels without a write. Answering yes performs the same atomic conflict revalidation and rename. A failed rename leaves project and redirect state unchanged.

### `pl add`

Add an open pellet at the end of the project’s active priority order by default.

```text
pl add TITLE [--request-id ID] [--description TEXT | --description-file PATH]
                  [--external-id ID]
                  [--group GROUP]
                  [--before PELLET | --after PELLET]
                  [--maybe-later]
                  [--review-targets PELLET,PELLET]
```

- `--description-file -` reads the description from stdin.
- `--before` and `--after` are mutually exclusive and require an `open` or `in_progress` pellet in the same project.
- `--maybe-later` creates the pellet in `maybe_later` with `priority: null`; otherwise it is `open`. It is mutually exclusive with `--before` and `--after`.
- New project-local numbers are monotonically allocated and never reused.

`--request-id` is an optional non-empty, opaque, case-sensitive key scoped to the
logical project, shared across its worktrees. Within its retention window, the
same ID and normalized creation inputs return the stored creation snapshot with
no new allocation. A different title, description content, external ID, group,
initial status, or relative placement returns `request_id_conflict` (exit 4),
including `project` and `request_id` in error details. Description filenames,
flag order, workspace identity, and canonical versus redirect spelling of the
placement project's code do not change the creation intent.

Every successful add (keyed, unkeyed, or replayed) deletes request records whose
original creation timestamp is strictly earlier than the transaction's current
time minus two days (48 hours), across all projects in that database. Cleanup,
receipt lookup, number allocation, pellet/FTS insertion, and receipt insertion
are in one immediate transaction; errors roll everything back. Retries never
extend retention. Expired keys may be reused and create new pellets. Cleanup
never deletes pellets or memories and requires no scheduled job.

A receipt preserves the original creation response after subsequent edits,
lifecycle transitions, or purge. Replaying never resurrects purged work. Output
uses the current canonical project code; `show` retrieves current pellet state.

`--review-targets` creates the narrow `review_checkpoint` kind. Supply 1–1000
distinct, explicit ordinary Pellet references in one project; a single target
is valid. Empty, missing, cross-project, or checkpoint targets are rejected.
This flag cannot combine with `--before`, `--after`, or `--maybe-later`.
Target order and project-code aliases do not affect retry identity. A different
target set conflicts with the same `--request-id`.

Creation validates and snapshots the exact selected targets, then inserts the
checkpoint immediately after the last selected active target in authoritative
priority/number order, all under one writer transaction. If no selected target
is active, it appends to the active queue. Closed and deferred targets are
allowed. Unselected rows between selected targets never enter the scope.
Reordering the checkpoint or its targets does not change the selection.

Checkpoint records add `kind: "review_checkpoint"` and a `checkpoint` object
to JSON v1 on add/show/list/search/next/start-next/lifecycle responses. Ordinary
records retain their existing shape (absent kind means ordinary). The nested
contract is explicitly versioned: `checkpoint.version` is 1, `ready` is a
boolean, and `targets` is ordered by stable target number. Each target includes
`project_id`, `number`, current canonical `reference`, immutable
`selected_reference`, selected `title`/`description`/nullable `external_id` and
`group`, current nullable `status` and `implementation_revision`, `reason`, and
nullable `evidence`. Evidence contains `run_id`, `workspace_id`, `starting_head`,
and `result_commit`; it identifies exact implementations, never a broad Git
range inferred from queue positions. Purged targets retain their selected
identity and scope and report `target_missing`.

Readiness requires every selected target to retain its selected scope and be
closed with a successful completed implementation attempt, verified commit,
finalization and Codex conversation evidence for its current implementation
revision. The other waiting reasons are `scope_changed`, `target_incomplete`,
and `evidence_missing`; a usable target reports `ready`. Closing queue rows
alone never supplies evidence. Reopen, release, defer, or scope edits invalidate
earlier implementation evidence. A continuation retains its original revision;
it cannot turn an old attempt into evidence for a new lifecycle generation.
Legacy attempts with an unknown revision cannot establish checkpoint readiness.

`next` and `start-next` skip waiting open checkpoints while preserving exact
filters and workspace ownership rules. Checkpoints use the existing four
lifecycle statuses. Direct start/close checks readiness transactionally and
returns `review_checkpoint_not_ready` (exit 4) if needed. Once a review attempt
captures scope, close and run completion also reject changed scope or evidence
with `execution_run_conflict`. A read or add receipt is an observation, not
permission to reuse stale evidence. Use `show` to refresh current readiness.

### `pl list`

List pellets in a deterministic status-appropriate order.

```text
pl list [--status STATUS] [--external-id ID] [--group GROUP]
        [--all] [--limit N]
```

By default, show `in_progress` and `open` pellets in priority order. `--all` then shows `maybe_later` newest-updated first and `closed` newest-completed first. A status-filtered non-actionable list uses the corresponding date order. `--status` and `--all` are mutually exclusive.

### `pl next`

Return work without changing it.

```text
pl next [--external-id ID] [--group GROUP]
```

Selection is deterministic:

1. Return the current workspace's `in_progress` pellet, if present.
2. Otherwise return the lowest-priority `open` pellet matching the optional exact external ID and group filters.
3. Otherwise return a successful empty result.

The current workspace's in-progress pellet wins even when it does not match `--external-id` or `--group`; another workspace's pellet is never resumed. The JSON field `selection_reason` is `resume_in_progress`, `next_open`, or `none`. After bootstrap, `next` never registers a workspace or changes operation state. On first use it may create/register Pellets metadata before performing this read-only selection. Workers that intend to begin work immediately should use atomic `start-next` rather than composing `next` and `start`.

### `pl show`

```text
pl show PELLET
```

Return the complete pellet record. A pellet reference’s project code must identify the current/selected project.

### `pl edit`

```text
pl edit PELLET [--title TEXT]
                     [--description TEXT | --description-file PATH]
                     [--external-id ID | --clear-external-id]
                     [--group GROUP | --clear-group]
```

At least one edit option is required. Editing status or priority through this command is forbidden; use the lifecycle and move commands.

### `pl move`

```text
pl move PELLET (--before OTHER | --after OTHER)
```

Both pellets must belong to the same project and both must be `open` or `in_progress`. The operation uses sparse integer priority and performs a transactional active-queue rebalance only when no integer gap is available. Closed and deferred pellets cannot be moved because they have no priority.

Raw numeric priority assignment is not part of v1 because relative placement is safer for agents and preserves implementation freedom over gap size.

### `pl start`

```text
pl start PELLET
```

Move an `open` pellet to `in_progress` and assign the current workspace. Repeating `start` is idempotent only when that same workspace owns that pellet. Return `workspace_already_in_progress` if the workspace owns another pellet and `pellet_in_progress_elsewhere` if another workspace owns this pellet.

### `pl start-next`

```text
pl start-next [--external-id ID] [--group GROUP]
```

In one immediate transaction, resume the current workspace's pellet or select and start the lowest-priority matching open pellet. Selection uses the same filters as `next`. Concurrent worktrees must receive distinct pellets or a stable conflict. Exhaustion is a successful typed empty result with `selection_reason: "none"` and `pellet: null`. Bounded deterministic retry never exposes a partial write.

### `pl release`

```text
pl release PELLET
pl release PELLET --recover-workspace WORKSPACE_ID --yes
```

The owning workspace returns its `in_progress` pellet to `open`, clearing ownership while retaining active priority. Another workspace is rejected by default. The second form is an explicit confirmed recovery for a removed or unavailable worktree; the supplied ID must match the stored owner and the response names that workspace. It is not authentication or silent stealing.

### `pl close`

```text
pl close PELLET
pl close PELLET --recover-workspace WORKSPACE_ID --yes
```

Move an `open` or current-workspace `in_progress` pellet to `closed`, set `completed_at`, and clear priority and workspace ownership. Repeating `close` on a closed pellet is idempotent and does not replace the original completion time. Closing another workspace's in-progress pellet requires `--recover-workspace WORKSPACE_ID --yes` with the same recovery semantics as `release`.

### `pl reopen`

```text
pl reopen PELLET
```

Move a `closed` or `maybe_later` pellet to `open`, clear `completed_at` and any workspace ownership, and append it at the end of the active priority order. Repeating it on an open pellet is idempotent. It never starts or carries a stale owner.

### `pl defer`

```text
pl defer PELLET
pl defer PELLET --recover-workspace WORKSPACE_ID --yes
```

Move an `open` or current-workspace `in_progress` pellet to `maybe_later` and clear priority and workspace ownership. Repeating it on a deferred pellet is idempotent. Deferring another workspace's in-progress pellet requires `--recover-workspace WORKSPACE_ID --yes`. Deferred pellets are excluded from `next` and the active priority index until reopened.

### `pl search`

Search title, description, and external-ID text with FTS5.

```text
pl search QUERY [--external-id ID] [--group GROUP]
                [--status STATUS] [--limit N]
```

Search includes every status by default so closed pellets remain discoverable in FTS without participating in queue maintenance. Ordinary query text is escaped into safe FTS terms. Exact external-ID and group filtering are relational and independent of tokenization. Results sort by FTS relevance, actionable records before non-actionable records on a relevance tie, active priority, update time newest first, and finally pellet number for deterministic ties.

### `pl purge`

Permanently delete closed pellets from a project.

```text
pl purge --project CODE [--closed-before DATE] --yes
pl purge --project CODE [--closed-before DATE] --dry-run
```

- With no date filter, select every closed pellet in the project.
- With `--closed-before`, select only pellets completed before the cutoff.
- Never select open, in-progress, or `maybe_later` pellets.
- `--yes` is required for deletion; there is no interactive prompt in default JSON mode.
- `--dry-run` returns the count and references without deleting.
- Purge does not delete memories or reuse pellet numbers.

### `pl memory`

Memory commands are specified in [memory.md](memory.md#cli-contract):

```text
pl memory add
pl memory list
pl memory show
pl memory search
pl memory approve
pl memory remove
```

### `pl skill install`

Install the embedded portable Pellets Agent Skill without discovering or opening a Pellets database.

```text
pl skill install [--scope repo|personal] [--agent codex|claude|both]
                 [--yes] [--dry-run] [--force]
```

| Option | Meaning |
|---|---|
| `--scope repo|personal` | Select repository or personal installation. |
| `--agent codex|claude|both` | Select one or both agent destinations. |
| `--yes` | Suppress only the final write confirmation. It does not choose missing values or approve replacement of differing files. |
| `--dry-run` | Return the complete plan and embedded content without any directory, temporary-file, or target write. No final confirmation is required. |
| `--force` | Permit replacement of differing regular target files. It never permits a symlink, non-regular target, unsafe parent, or path escape. |

The exact destination matrix is:

| Scope | Codex | Claude |
|---|---|---|
| Repository | `<git-root>/.agents/skills/pellets/SKILL.md` | `<git-root>/.claude/skills/pellets/SKILL.md` |
| Personal | `<home>/.agents/skills/pellets/SKILL.md` | `<home>/.claude/skills/pellets/SKILL.md` |

`<git-root>` is Git's resolved current worktree root, including linked-worktree semantics. `<home>` comes from the operating system's user-home API. A personal selection never substitutes a repository-relative location. Repository installation creates ordinary untracked files the user may choose to commit. The command never edits `.gitignore`, Git local exclude, the index, commits, `AGENTS.md`, `CLAUDE.md`, settings, or `.pellets` data.

Compact JSON is the default, so JSON invocations never prompt or read stdin. They require both `--scope` and `--agent`; a write also requires `--yes`. Missing choices return `missing_skill_choices` with exit 2. A write without an available final confirmation returns `confirmation_required` with exit 6. Unknown choice values are `invalid_skill_scope` or `invalid_skill_agent`, and unavailable repository scope is `repository_scope_unavailable`; all fail before target creation.

The wizard runs only with `--human` when both stdin and stdout are interactive terminals. Supplied choices are retained and only missing choices are asked. When Git is available, the scope prompt is exactly:

```text
Git repository root: <git-root>
Choose installation scope:
  1) Repository
  2) Personal (<home>)
  0) Cancel
Scope:
```

Outside a Git worktree, Repository is omitted and Personal is selected after this explanation:

```text
Repository scope is unavailable because the current directory is not inside a Git work tree.
Using Personal scope rooted at <home>.
```

The agent prompt is:

```text
Choose agent target:
  1) Codex
  2) Claude
  3) Both
  0) Cancel
Agent:
```

After read-only preflight, human mode prints the selected scope, the repository root when applicable, and every exact destination. A differing regular file is labeled `(different existing file)` and an identical file `(already current)`. A differing file is never silently overwritten: without `--force`, interactive mode asks `Replace every differing existing skill file? [y/N]:`; noninteractive mode returns `skill_content_conflict` with every conflicting agent/path and exit 4. `--yes` does not suppress this replacement question. The final prompt is `Install the Pellets skill at every displayed destination? [y/N]:` unless `--yes` is present.

Empty input, `0`, `cancel`, `c`, `q`, `quit`, `n`, or `no` cancels at the applicable prompt. Cancellation exits 0, writes no files, and reports `status: "cancelled"` with per-target `result: "cancelled"` for targets already planned. Invalid interactive answers are explained and retried without writing.

Normal JSON results use `command: "skill install"`:

```json
{"schema_version":1,"command":"skill install","data":{"status":"installed","scope":"repo","agent":"both","repository_root":"/work/repo","targets":[{"agent":"codex","path":"/work/repo/.agents/skills/pellets/SKILL.md","result":"installed"},{"agent":"claude","path":"/work/repo/.claude/skills/pellets/SKILL.md","result":"idempotent"}]}}
```

Overall `status` is `installed` when any target was installed or replaced and `idempotent` when every target already matched. Per-target results are `installed`, `replaced`, or `idempotent`. Dry-run uses overall `dry_run`, includes `content`, and reports `would_install`, `would_replace` when `--force` is present, `would_conflict` for an unforced differing file, or `idempotent` per target.

Before any multi-target write, every destination, existing parent, file type, permission, and content state is preflighted. A target or descendant parent symlink, non-regular target, non-directory parent, path escape, or unusable parent returns `skill_target_unsafe` with exit 4 even under `--force`. Files are replaced atomically. If a later target write fails, `skill_install_failed` reports whether rollback completed; the invocation restores replaced bytes/modes and removes only files and empty directories it created, so `both` never intentionally leaves one agent updated and the other stale.

The embedded artifact contains only portable instructions and narrow `name: pellets`/`description` frontmatter. Its implicit trigger applies only when a prompt explicitly names the `pl` command or Pellet/Pellets. It explicitly rejects generic task, issue, ticket, queue, backlog, project, project-management, and memory requests that do not name `pl`/Pellets. Explicit skill invocation remains available. See the current official [OpenAI Codex skill guidance](https://developers.openai.com/codex/skills) and [Claude Code skill guidance](https://code.claude.com/docs/en/skills).

### `pl server`

Run the optional local server and inspector in the foreground.

```text
pl [--project CODE] server [--port PORT] [--no-open]
```

- Inside Git, database discovery and first-use bootstrap are identical to other current-project commands: the repository binding wins, otherwise existing worktree/ancestor databases are discovered, or a project-local database is created when none exists. Outside Git, the server opens the nearest database in the current directory or its ancestors, including an empty database, with no current project workspace. It never scans child repositories. If no database exists, `database_not_found` explains how to run inside a Git worktree or create a common-parent database with `pl init-db`. `--project` selects the initial project area when it exists; the interface can inspect every registered project in that database.
- The only listener address is IPv4 `127.0.0.1`. There is no bind-address flag. Omitted `--port`, or explicit canonical port `0`, requests an OS-selected available port; `--port` otherwise accepts 1 through 65535.
- Print `http://127.0.0.1:PORT` followed by one newline after the listener is ready. This foreground command is the sole exception to the normal JSON-success envelope.
- Unless `--no-open` is present, open the default browser only after readiness. A launcher failure writes a useful warning to stderr while leaving the printed URL and server usable.
- Remain in the foreground until interrupted. Interruption performs bounded graceful shutdown; `pl server` never installs, daemonizes, or registers a background service. It owns any Codex execution it starts, so closing a browser tab does not stop work and stopping the server does.
- `--human` and `--pretty` are rejected because the command owns its foreground output.

`pl web [--port PORT] [--no-open]` remains a deprecated compatibility alias.
It has identical parsing and foreground behavior, but its `--help` output uses
the canonical `pl server` usage. New automation must invoke `pl server`.

The browser uses only embedded, offline assets: pinned Datastar 1.0.3 and its license, repository-owned JavaScript/CSS, system fonts, and standard-library HTTP/templates. There is no runtime CDN, font/icon fetch, Node/npm build, WebSocket, service worker, or remote API. A first visit follows `prefers-color-scheme`; the light/dark/system selector persists locally and applies before first paint.

Project pages expose every registered workspace and current pellet, all pellet states, all direct project redirects, and all authoritative memories. Project/status/exact-group/exact-external-ID/search filters are encoded in the URL; search uses the same escaped safe FTS semantics as `pl search`. Canonical project codes, pellet references, and memory IDs form current deep links. Requests using a direct old-code project or pellet path receive a temporary redirect to the equivalent current canonical path with the query string preserved. With one project the header is compact; with several projects a wide sidebar and narrow-screen drawer keep project data separated. Tasks use priority/status ordering and a wide inspector or narrow modal sheet. The task title column consumes available table width before truncating title or owner text, while genuinely constrained tables retain clean ellipsis, responsive column hiding, and horizontal scrolling. The task inspector description editor has a bounded viewport-responsive initial height, remains vertically resizable, and stays within the inspector's scroll region; create-task and memory editors keep their independent sizing. Each task row forwards clicks in non-interactive cells to its single native inspector link, without relying on positioned table-row overlays, so the pointer and keyboard affordances share one URL and preserve modified-link actions without adding duplicate interactive controls. Browser history, Escape, focus trapping/restoration, scroll preservation, dirty-form warnings, reduced motion, and recovery polling are presentation contracts.

Every task-table heading is a native link and sortable with a pointer or keyboard. The active heading alone carries `aria-sort`; its visible arrow and accessible action name expose the current direction, and activating it reverses that direction. Activating another heading starts ascending. `sort=reference|title|group|status|priority|external_id|updated` and `direction=asc|desc` are URL state. Missing or unknown components independently normalize to `priority` and `asc`, respectively, without failing the page. Header links retain the selected-task path and all current filters, filter submissions retain normalized sort state, and live fragment GETs use that same URL, so reload and history navigation reproduce the ordering.

Task-table comparison is deterministic. Reference compares the positive project-local task number. Title, group, and external ID compare by ASCII-folded byte order and then exact UTF-8 byte order; optional group and external-ID values place `NULL` after populated values in either direction. Status ascending is `in_progress`, `open`, `maybe_later`, then `closed`, and descending reverses it. Priority compares populated positive integers in the requested direction while keeping `NULL` last; the default empty-priority tail keeps `maybe_later` before `closed` and each group newest-updated first. Updated time compares the authoritative timestamp chronologically. Reference, title, status, and updated time cannot be empty under the data model. Task number ascending is the final tie-breaker regardless of selected direction.

The permitted mutations are pellet create/scalar edit/reorder/lifecycle, memory create/text edit, and memory approval. Purge, memory removal, project deletion, and any irreversible action are absent. Scalar and memory edits can address a selected project. Lifecycle actions require the server's current registered project workspace. A pellet owned by another workspace disables normal actions; an explicit recovery form names the pellet and stored workspace, explains that it does not authenticate an agent, and requires confirmation.

Every existing-row mutation submits a complete-row optimistic token. A mismatch performs no write and patches an inspector fragment showing current authoritative data beside the preserved submitted draft. Invalid mutations patch actionable validation feedback with the submitted draft. Datastar requests receive HTTP 200 SSE containing element patches and an `_webResult` signal with the application status (409 for conflicts, 422 for validation, and 200/201 for success); ordinary HTTP requests retain their original status codes. Other errors retain their HTTP status and do not patch the UI. Requests must use the exact listener Host and same-loopback Origin, POST, URL-encoded form content, a process-random CSRF value in both strict cookie and form, and normal HTML escaping. Restrictive CSP and response headers permit only the embedded local application.

Live refresh is invalidation-only. One pinned read-only/query-only monitor connection compares `PRAGMA data_version` from that same connection at a bounded interval only while SSE clients exist. A changed value is coalesced into a small SSE event; native `EventSource` triggers authoritative Datastar list/detail GETs. Slower Datastar polling and every initial load recover missed events. SSE carries no row payload, database path, or capability and never owns a database connection. Every GET uses a separate read-only/query-only path and closes SQLite rows before response output.

#### Foreground execution controls

Codex execution is an optional browser/server capability, not a new public CLI
runner command. The user chooses one already registered workspace, optional
exact group and external-ID filters, a bounded limit (100 by default), and one
of Run one, Drain, or Watch. Run one completes at most one eligible Pellet;
Drain repeats until the queue is empty, the limit is reached, or attention is
required; Watch additionally waits on database invalidation and a bounded
recovery check. Idle Watch starts no Codex process and makes no model call.
Filters and the remaining limit are copied into every attempt and cannot be
broadened by a later browser request.

An existing in-progress Pellet requires the explicit Resume control and must
match both saved filters. Resume restores the exact prior attempt, worktree,
branch, phase, Codex thread/turn, schedule mode, and remaining limit after
validating current ownership and Git/queue evidence. Server startup only marks
abandoned attempts interrupted or needing attention; it never resumes them.
When a Unix preflight crash leaves a validated zero-run receipt, Resume instead
requires confirmation of its saved mode, remaining limit, and exact filters for
the same owned pellet generation. After custodian cleanup and repaired preflight,
it creates one fresh run and conversation. Legacy or mismatched receipts and
Windows cleanup without post-crash proof remain fenced. With no receipt and no
current-generation run, the form asks for a new mode and filters explicitly.
A missing conversation, ambiguous pending call, changed scope, missing commit,
or unconfirmed process cleanup remains visible for recovery rather than being
silently retried. Stop after Pellet permits the active unit to finish; Stop now
interrupts it. Stopping `pl server` stops admission and every Codex process it
owns before releasing the workspace execution lock.

Workspace settings contain only an executable selector, optional open-ended
model ID, optional runtime-supported reasoning effort, and bounded transport
limits. Empty model/effort preserve Codex defaults. One-run overrides do not
rewrite the saved row. Preflight uses the installed runtime's local account,
configuration, managed requirements, and model catalog; Codex owns credential
storage and refresh, and Pellets stores neither credentials nor account email.
New prepared conversations use `workspace-write`, `on-request`, and
`approvals_reviewer=auto_review`. Automatic approval REVIEW evaluates eligible
requests; it is not blanket approval and does not disable managed restrictions,
questions, denials, timeouts, or the sandbox.

The implementation turn may edit and test only the exact selected ordinary
Pellet. On an exact successful result, the server verifies unchanged ownership
and starting HEAD, stages only the reported changed paths, creates and verifies
one Pellet-ID commit, then closes that Pellet. A commit alone, queue closure
alone, or a no-op result cannot advance the schedule. Finalization recovery
reuses durable tree/commit/close evidence and never repeats a completed
implementation, commit, or close.

Ready review checkpoints use a fresh detached read-only Codex review context
and independently present every selected result commit, including
noncontiguous commits and evidence produced in different registered worktrees.
A selected Pellet still in progress in another workspace leaves the checkpoint
waiting. A successful clean review or fully reconciled finding triage closes
the checkpoint without creating a Git commit. Each valid distinct finding
becomes one ordinary follow-up immediately after the checkpoint; permanent
finding receipts and stable add request IDs prevent duplicates across lost
responses, receipt expiry, closure failures, and explicit Resume. Review and
triage never fix code, push, create a PR, or automatically create another
checkpoint.

New conversations receive a deterministic, versioned Pellets prefix before the
variable task. Resume retains the captured conversation and does not resend the
full prefix. Reported cached-input-token counts are telemetry only: the stable
layout is intended to permit caching but never promises or infers a cache hit.
Runs and checkpoints are execution evidence; planning remains in Pellets and no
parallel Markdown plan or server backlog is created.

## JSON contract

### Envelope

Every successful command emits exactly one JSON object:

```json
{"schema_version":1,"command":"next","data":{"selection_reason":"next_open","pellet":{"id":"foo-12","project":"foo","number":12,"title":"Add parser","description":"Implement strict command parsing.","external_id":"github:acme/tool#84","group":"parser-rollout","status":"open","priority":2048,"workspace":null,"created_at":"2026-08-28T20:00:00Z","updated_at":"2026-08-28T20:00:00Z","completed_at":null}}}
```

Rules:

- `schema_version` is an integer and changes only for breaking JSON changes.
- `command` is the canonical command name; nested commands use a space, for example `memory search`.
- `data` is always present and command-specific.
- Lists use arrays, including empty arrays; absent optional scalar values use JSON `null`.
- Pellet `priority` is an integer for `open` and `in_progress` records and JSON `null` for `closed` and `maybe_later` records.
- Pellet `workspace` is JSON `null` except for `in_progress`; there it contains the owning workspace ID, root path, Git-directory path, and relative/absolute flags.
- Timestamps are UTC RFC 3339 strings even though SQLite stores Julian values.
- Object key order is not contractual.
- No logs, progress messages, ANSI escapes, or prose appear on stdout in JSON mode.

Adding an optional field is backward-compatible within a schema version. Removing or changing the type/meaning of a field requires a new schema version. Golden tests cover every command’s success and error shape.

### Empty `next`

No available pellet is not an error:

```json
{"schema_version":1,"command":"next","data":{"selection_reason":"none","pellet":null}}
```

### Errors

Errors emit one compact object to stderr and nothing to stdout:

```json
{"schema_version":1,"error":{"code":"workspace_already_in_progress","message":"workspace 7 already owns foo-9","details":{"workspace_id":7,"pellet_id":"foo-9"}}}
```

`code` and the types of documented `details` fields are stable. `message` is diagnostic text and may improve without a schema-version change.

SQLite lock contention is `database_busy` with exit code 4 and a stable string `details.operation`; this includes migration writer-lock acquisition and ordinary mutations. A malformed SQLite image is `database_corrupt`, while a non-SQLite or unsupported file format is `database_incompatible`; both use exit code 5, emit no success object, perform no recovery write, and expose only a stable operation name rather than raw SQL or SQLite diagnostics. Schema-version failures remain distinct as `schema_version_invalid`, `schema_version_unsupported`, and `schema_too_new`.

## Human-readable output

`--human` is intended for inspection, not scripting. It may use tables for lists and labeled fields for `show`, but must remain concise. Human formatting is not stable across releases.

Examples:

```text
foo-12  open  p=2048  Add parser
```

```text
No open pellets.
```

Never truncate titles or descriptions when stdout is not a terminal. Terminal truncation must be visibly marked.

## stdin, stdout, and stderr

- stdin is read only when an explicit option names `-`, such as `--description-file -` or `pl memory add --file -`, or by a documented `--human` wizard or project-rename confirmation when both stdin and stdout are terminals.
- JSON commands never read stdin implicitly; this prevents an agent invocation from hanging.
- stdout contains the successful result only. For `pl server`, that result is the ready listener URL rather than JSON.
- stderr contains the structured error only, plus diagnostics only when an explicit future debug flag is used. A non-fatal `pl server` browser-launch warning is the documented exception.
- Help and version text go to stdout with exit code 0.
- Broken-pipe errors terminate quietly with a nonzero operational exit.

## Exit codes

| Code | Meaning |
|---:|---|
| 0 | Success, including an empty `next` or search. |
| 2 | CLI usage or validation error. |
| 3 | Database, project, pellet, or memory not found. |
| 4 | State conflict, uniqueness conflict, or database busy. |
| 5 | Database/schema/FTS failure. |
| 6 | Confirmation required for a destructive command. |
| 1 | Unexpected operational failure not covered above. |

Specific machine error codes disambiguate cases that share an exit code.

## Confirmation and idempotency rules

- `purge` requires `--yes`; `--human` does not weaken this rule.
- `memory remove` requires `--yes`.
- Cross-workspace recovery requires both the exact stored `--recover-workspace WORKSPACE_ID` and `--yes`; human output does not weaken this rule.
- Project rename deletes foreign redirect conflicts only after terminal human confirmation or an exact noninteractive retry with both `--delete-conflicting-redirects` and `--yes`; the transaction revalidates the displayed set.
- Noninteractive `skill install` writes require `--yes`; interactive cancellation is a successful write-free result. Differing skill files additionally require `--force` or the separate interactive replacement confirmation.
- `init-db` and automatic bootstrap never overwrite an existing database.
- `start`, `close`, `reopen`, and `defer` are idempotent only when the pellet is already in their target status.
- Repeating `add` without `--request-id` creates another pellet. With a request ID, identical creation inputs replay the original result for two days; conflicting inputs fail. Every successful add prunes older request records without deleting pellets.

## Common workflows

### One repository, one database

```text
pl add "Implement parser" --description-file parser.md --external-id "github:acme/foo#84" --group "parser-rollout"
pl add "Add parser tests" --external-id "github:acme/foo#84" --group "parser-rollout"
pl add "Migrate existing configs" --external-id "github:acme/foo#85" --group "parser-rollout"
pl next --external-id "github:acme/foo#84" --group "parser-rollout"
pl start-next --external-id "github:acme/foo#84" --group "parser-rollout"
pl close foo-1
pl next --group "parser-rollout"
```

The example uses a file only as input to `add`; Pellets becomes the authoritative task state afterward.

### Several repositories, one database

```text
cd common-parent
pl init-db
cd service-a
pl add "First service-a pellet"
cd ../service-b
pl list
```

### Several worktrees, one logical project

```text
cd common-parent
pl init-db
cd main-work-tree
pl list
git worktree add ../review-work-tree review-branch
cd ../review-work-tree
pl start-next --group parser-rollout
```

Both worktrees use the same generated `<project-code>-N` references, one queue, and one memory store. Each resumes only its own in-progress pellet. If a worktree is later removed while it owns work, another workspace uses the explicit recovery form rather than silently taking it.

### Insert discovered work before an existing pellet

```text
pl add "Handle invalid UTF-8" --before foo-12
```

### Defer for human review

```text
pl defer foo-18
pl list --status maybe_later
pl reopen foo-18
```

### Purge old closed work

```text
pl purge --project foo --closed-before 2026-01-01 --dry-run
pl purge --project foo --closed-before 2026-01-01 --yes
```

## Deliberately absent commands

There are no `block`, `unblock`, `dependency`, `graph`, `ready`, `epic`, `tag`, `note`, `claim`, `assign`, `sync`, or vector/embedding commands. `next` is ordering-based, not graph-based; `start-next` is a worktree-scoped atomic lifecycle command, not an agent claim or lease.

`--group` is intentionally singular. It is not a tag system: there is no repeated flag, many-to-many table, group entity, hierarchy, or group-specific behavior.
