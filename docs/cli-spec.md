# CLI Specification

The product is Pellets; the executable is `pl`. The CLI is designed for coding agents first. Compact JSON is the default interface.

See [project-goals.md](project-goals.md) for product intent, [data-model.md](data-model.md) for invariants, and [memory.md](memory.md) for memory behavior.

## Conventions

```text
pl [global-options] <command> [command-options] [arguments]
```

- Commands and long flags use lowercase kebab-case.
- Mutating pellet commands accept one canonical reference such as `foo-123`.
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

After strict parsing and usage validation, commands explicitly classified as needing the current project/workspace walk upward and use the nearest `.pellets/pellets.db`. They ask Git for the common directory to find the logical project and for the worktree root plus worktree-specific Git directory to find the current workspace. Walking for the database continues past Git boundaries, which permits one database at a common parent of linked worktrees and unrelated sibling repositories. V1 has no path override: selecting a database is intentionally a property of the working directory.

If no ancestor database exists, the first valid current-project command creates `.pellets/pellets.db` at the Git worktree root, registers the logical repository and current worktree, and then completes the requested operation in that same invocation. With an existing ancestor database, it adds an unknown repository as a distinct project or attaches an unknown linked worktree to the already-known logical project. `add`, list/search/show/next and lifecycle commands, every `memory` operation, current `project show`, and `web` have this capability. There is no separate project-initialization command.

Bootstrap happens only after parsing and usage validation. Help/version, invalid invocations, `init-db`, `skill install`, `project list`, named or `--project`-selected `project show`, and explicit project-scoped `purge` do not bootstrap. Those database-level commands retain their existing nearest-database semantics and fail with `database_not_found` when none exists.

Project codes are generated without prompting. The logical repository name is the directory containing a `.git` common directory, or a bare common-directory basename with one terminal `.git` removed. Pellets lowercases ASCII letters, preserves ASCII digits, collapses every run of other characters into one hyphen, and trims edge hyphens. A non-empty normalized name of at most 12 bytes is the first candidate. Empty or longer names, and candidates already owned by another repository, use up to three normalized prefix bytes (or `p`), `-`, and the first eight lowercase hexadecimal SHA-256 digits over `true:<relative-common-dir>` or `false:<absolute-common-dir>`, using the same slash/case normalization stored in SQLite. If that candidate is occupied, attempts rehash the identity plus a NUL byte and the increasing canonical decimal attempt. Allocation and registration share one immediate transaction. An already-known common-directory identity ignores new checkout names and always reuses its stored immutable code.

Bootstrap writes are a one-time pre-command effect, not a change to operation semantics. Once the exact project/workspace is registered, `next`, `list`, `search`, `show`, memory reads, named/database-level project reads, and every dry run retain their write-free guarantees. On first use only, a valid current-project command can create the database, update Git's local exclude, and transactionally register the project/workspace before running an otherwise read-only operation. The requested read itself still performs no queue or memory mutation.

`--project CODE` does not silently let a caller mutate an unrelated repository. For pellet mutations, the code in the pellet reference must match the selected/current project. Database-level and read-only administrative commands may operate across registered projects when explicitly documented.

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

Show the current logical project, or a named project when `CODE` is supplied, including its Git common directory and registered workspace IDs, roots, Git directories, relative/absolute flags, and timestamps. Current `project show` bootstraps on first use; positional `CODE` or global `--project CODE` is a database-level read and never registers the current directory. Public project codes and pellet references do not change.

Project `list` and `show` use this data shape (timestamps omitted here only for brevity):

```json
{"code":"foo","git_common_dir":"main/.git","git_common_dir_relative":true,"workspaces":[{"id":1,"root_path":"main","root_path_relative":true,"git_dir":"main/.git","git_dir_relative":true},{"id":2,"root_path":"linked","root_path_relative":true,"git_dir":"main/.git/worktrees/linked","git_dir_relative":true}]}
```

### `pl add`

Add an open pellet at the end of the project’s active priority order by default.

```text
pl add TITLE [--description TEXT | --description-file PATH]
                  [--external-id ID]
                  [--group GROUP]
                  [--before PELLET | --after PELLET]
                  [--maybe-later]
```

- `--description-file -` reads the description from stdin.
- `--before` and `--after` are mutually exclusive and require an `open` or `in_progress` pellet in the same project.
- `--maybe-later` creates the pellet in `maybe_later` with `priority: null`; otherwise it is `open`. It is mutually exclusive with `--before` and `--after`.
- New project-local numbers are monotonically allocated and never reused.

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

### `pl web`

Run the optional local web inspector in the foreground.

```text
pl [--project CODE] web [--port PORT] [--no-open]
```

- Database discovery and first-use bootstrap are identical to other current-project commands: the nearest ancestor `.pellets/pellets.db` wins, or a project-local database is created when none exists. `--project` selects the initial project area when it exists; the interface can inspect every registered project in that database.
- The only listener address is IPv4 `127.0.0.1`. There is no bind-address flag. Omitted `--port`, or explicit canonical port `0`, requests an OS-selected available port; `--port` otherwise accepts 1 through 65535.
- Print `http://127.0.0.1:PORT` followed by one newline after the listener is ready. This foreground command is the sole exception to the normal JSON-success envelope.
- Unless `--no-open` is present, open the default browser only after readiness. A launcher failure writes a useful warning to stderr while leaving the printed URL and server usable.
- Remain in the foreground until interrupted. Interruption performs bounded graceful shutdown; `pl web` never installs, daemonizes, or registers a background service.
- `--human` and `--pretty` are rejected because the command owns its foreground output.

The browser uses only embedded, offline assets: pinned HTMX 2.0.4 and its license, repository-owned JavaScript/CSS, system fonts, and standard-library HTTP/templates. There is no runtime CDN, font/icon fetch, Node/npm build, WebSocket, service worker, or remote API. A first visit follows `prefers-color-scheme`; the light/dark/system selector persists locally and applies before first paint.

Project pages expose every registered workspace and current pellet, all pellet states, and all authoritative memories. Project/status/exact-group/exact-external-ID/search filters are encoded in the URL; search uses the same escaped safe FTS semantics as `pl search`. Stable project codes, pellet references, and memory IDs form deep links. With one project the header is compact; with several projects a wide sidebar and narrow-screen drawer keep project data separated. Tasks use priority/status ordering and a wide inspector or narrow modal sheet. Browser history, Escape, focus trapping/restoration, scroll preservation, dirty-form warnings, reduced motion, and recovery polling are presentation contracts.

The permitted mutations are pellet create/scalar edit/reorder/lifecycle, memory create/text edit, and memory approval. Purge, memory removal, project deletion, and any irreversible action are absent. Scalar and memory edits can address a selected project. Lifecycle actions require the server's current registered project workspace. A pellet owned by another workspace disables normal actions; an explicit recovery form names the pellet and stored workspace, explains that it does not authenticate an agent, and requires confirmation.

Every existing-row mutation submits a complete-row optimistic token. A mismatch returns HTTP 409, performs no write, and swaps an inspector fragment showing current authoritative data beside the preserved submitted draft. Invalid mutations return HTTP 422 and swap actionable validation feedback with the submitted draft. HTMX retains its safe no-swap handling for every other 4xx/5xx response. Requests must use the exact listener Host and same-loopback Origin, POST, URL-encoded form content, a process-random CSRF value in both strict cookie and form, and normal HTML escaping. Restrictive CSP and response headers permit only the embedded local application.

Live refresh is invalidation-only. One pinned read-only/query-only monitor connection compares `PRAGMA data_version` from that same connection at a bounded interval only while SSE clients exist. A changed value is coalesced into a small SSE event; native `EventSource` triggers authoritative HTMX list/detail GETs. Slower HTMX polling and every initial load recover missed events. SSE carries no row payload, database path, or capability and never owns a database connection. Every GET uses a separate read-only/query-only path and closes SQLite rows before response output.

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

- stdin is read only when an explicit option names `-`, such as `--description-file -` or `pl memory add --file -`, or by the documented interactive `--human skill install` wizard when both stdin and stdout are terminals.
- JSON commands never read stdin implicitly; this prevents an agent invocation from hanging.
- stdout contains the successful result only. For `pl web`, that result is the ready listener URL rather than JSON.
- stderr contains the structured error only, plus diagnostics only when an explicit future debug flag is used. A non-fatal `pl web` browser-launch warning is the documented exception.
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
- Noninteractive `skill install` writes require `--yes`; interactive cancellation is a successful write-free result. Differing skill files additionally require `--force` or the separate interactive replacement confirmation.
- `init-db` and automatic bootstrap never overwrite an existing database.
- `start`, `close`, `reopen`, and `defer` are idempotent only when the pellet is already in their target status.
- Repeating `add` is not idempotent and creates another pellet. A future request-id mechanism is out of scope until a real need appears.

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
