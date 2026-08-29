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

Normal commands walk upward from the current directory and use the nearest `.pellets/pellets.db`. They ask Git for the common directory to find the logical project and for the worktree root plus worktree-specific Git directory to find the current workspace. Walking for the database continues past Git boundaries, which permits one database at a common parent of linked worktrees and unrelated sibling repositories. V1 has no path override: selecting a database is intentionally a property of the working directory.

`--project CODE` does not silently let a caller mutate an unrelated repository. For pellet mutations, the code in the pellet reference must match the selected/current project. Database-level and read-only administrative commands may operate across registered projects when explicitly documented.

## Commands

### `pl init-db`

Create `.pellets/pellets.db` beneath the current directory, without registering a project.

```text
pl init-db
```

Use this at a common parent before registering sibling repositories. Fail if the database, its WAL/SHM/journal companions, or a symlinked `.pellets` metadata directory already exists; never overwrite or remove any of them.

If the new database is inside a Git work tree, add `.pellets/` to Git’s local exclude file and fail if the database or any SQLite companion path is already tracked. Index-only entries and case-equivalent paths on case-insensitive filesystems count as tracked.

### `pl init`

Register the current Git repository and worktree.

```text
pl init --code CODE
```

If an ancestor database exists, use the nearest one. Otherwise create `.pellets/pellets.db` at the current worktree root. Git's common directory identifies one logical project; the worktree root and worktree-specific Git directory identify one workspace. Paths are stored relative to the database root when possible and otherwise as normalized absolute local paths.

When the database lies inside the Git work tree, ensure `.pellets/` is in Git’s local exclude file. Do not edit committed `.gitignore`. Fail if the database is already tracked.

The first invocation creates the project and initial workspace. Running the same command and code in another linked worktree attaches that workspace to the same project; repeating either is idempotent. A different code for the same repository, the same code for an unrelated repository, a live duplicate worktree, one worktree attached to two projects, or inconsistent common/root/Git-directory identity is a typed write-free conflict. `init` may update a moved workspace root when its Git directory is unchanged and the old root is absent. Removed worktrees otherwise remain listed; there is no automatic cleanup. Project codes are immutable in v1.

### `pl project list`

List registered logical projects and every workspace identity in the selected database. This is a database-level read command and does not require the current directory to be inside a registered project. It never registers or repairs a workspace.

### `pl project show [CODE]`

Show the current logical project, or a named project when `CODE` is supplied, including its Git common directory and registered workspace IDs, roots, Git directories, relative/absolute flags, and timestamps. Public project codes and pellet references do not change.

Project `list`, `show`, and `init` use this data shape (timestamps omitted here only for brevity):

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

The current workspace's in-progress pellet wins even when it does not match `--external-id` or `--group`; another workspace's pellet is never resumed. The JSON field `selection_reason` is `resume_in_progress`, `next_open`, or `none`. `next` never registers a workspace or writes. Workers that intend to begin work immediately should use atomic `start-next` rather than composing `next` and `start`.

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

Search includes every status by default so closed pellets remain discoverable in FTS without participating in queue maintenance. Ordinary query text is escaped into safe FTS terms. Exact external-ID and group filtering are relational and independent of tokenization. Results sort by FTS relevance, actionable records before non-actionable records on a relevance tie, active priority, and update time.

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

- stdin is read only when an explicit option names `-`, such as `--description-file -` or `pl memory add --file -`.
- Commands never read stdin implicitly; this prevents an agent invocation from hanging.
- stdout contains the successful result only.
- stderr contains the structured error only, plus diagnostics only when an explicit future debug flag is used.
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
- Initialization never overwrites an existing database.
- `start`, `close`, `reopen`, and `defer` are idempotent only when the pellet is already in their target status.
- Repeating `add` is not idempotent and creates another pellet. A future request-id mechanism is out of scope until a real need appears.

## Common workflows

### One repository, one database

```text
pl init --code foo
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
pl init --code svc-a
cd ../service-b
pl init --code svc-b
```

### Several worktrees, one logical project

```text
cd common-parent
pl init-db
cd main-work-tree
pl init --code foo
git worktree add ../review-work-tree review-branch
cd ../review-work-tree
pl init --code foo
pl start-next --group parser-rollout
```

Both worktrees use `foo-N` references, one queue, and one memory store. Each resumes only its own in-progress pellet. If a worktree is later removed while it owns work, another workspace uses the explicit recovery form rather than silently taking it.

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
