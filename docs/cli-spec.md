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

Normal commands walk upward from the current directory and use the nearest `.pellets/pellets.db`. They then use the nearest Git root to find the registered project. Walking for the database continues past Git boundaries, which permits one database at a common parent of several repositories. V1 has no path override: selecting a database is intentionally a property of the working directory.

`--project CODE` does not silently let a caller mutate an unrelated repository. For pellet mutations, the code in the pellet reference must match the selected/current project. Database-level and read-only administrative commands may operate across registered projects when explicitly documented.

## Commands

### `pl init-db`

Create `.pellets/pellets.db` beneath the current directory, without registering a project.

```text
pl init-db
```

Use this at a common parent before registering sibling repositories. Fail if a database already exists; never overwrite it.

If the new database is inside a Git work tree, add `.pellets/` to Git’s local exclude file and fail if that path is already tracked.

### `pl init`

Register the current Git work tree as a project.

```text
pl init --code CODE
```

If an ancestor database exists, register the Git root in the nearest one. Otherwise create `.pellets/pellets.db` at the Git root, then register the project. The project path is stored relative to the database root.

When the database lies inside the Git work tree, ensure `.pellets/` is in Git’s local exclude file. Do not edit committed `.gitignore`. Fail if the database is already tracked.

Registering an already registered Git root with the same code is idempotent. A different code is a conflict. Project codes are immutable in v1.

### `pl project list`

List registered projects in the selected database. This is a database-level read command and does not require the current directory to be inside a registered project.

### `pl project show [CODE]`

Show the current project, or a named project when `CODE` is supplied.

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

1. Return the project’s one `in_progress` pellet, if present.
2. Otherwise return the lowest-priority `open` pellet matching the optional exact external ID and group filters.
3. Otherwise return a successful empty result.

An in-progress pellet wins even when it does not match `--external-id` or `--group`. The JSON field `selection_reason` is `resume_in_progress`, `next_open`, or `none`. This avoids directing the sole project agent to new work while different work is already in progress.

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

Move an `open` pellet to `in_progress`. Fail with a conflict if the project already has a different in-progress pellet. Repeating `start` on the same in-progress pellet is idempotent and returns it unchanged.

### `pl close`

```text
pl close PELLET
```

Move an `open` or `in_progress` pellet to `closed`, set `completed_at`, and set priority to `null`. Repeating `close` on a closed pellet is idempotent and does not replace the original completion time.

### `pl reopen`

```text
pl reopen PELLET
```

Move a `closed` or `maybe_later` pellet to `open`, clear `completed_at`, and append it at the end of the active priority order. Repeating it on an open pellet is idempotent. It never starts the pellet automatically.

### `pl defer`

```text
pl defer PELLET
```

Move an `open` or `in_progress` pellet to `maybe_later` and set priority to `null`. Repeating it on a deferred pellet is idempotent. Deferred pellets are excluded from `next` and the active priority index until reopened.

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
{"schema_version":1,"command":"next","data":{"selection_reason":"next_open","pellet":{"id":"foo-12","project":"foo","number":12,"title":"Add parser","description":"Implement strict command parsing.","external_id":"github:acme/tool#84","group":"parser-rollout","status":"open","priority":2048,"created_at":"2026-08-28T20:00:00Z","updated_at":"2026-08-28T20:00:00Z","completed_at":null}}}
```

Rules:

- `schema_version` is an integer and changes only for breaking JSON changes.
- `command` is the canonical command name; nested commands use a space, for example `memory search`.
- `data` is always present and command-specific.
- Lists use arrays, including empty arrays; absent optional scalar values use JSON `null`.
- Pellet `priority` is an integer for `open` and `in_progress` records and JSON `null` for `closed` and `maybe_later` records.
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
{"schema_version":1,"error":{"code":"project_already_has_in_progress","message":"project foo already has foo-9 in progress","details":{"pellet_id":"foo-9"}}}
```

`code` and the types of documented `details` fields are stable. `message` is diagnostic text and may improve without a schema-version change.

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
pl start foo-1
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

There are no `block`, `unblock`, `dependency`, `graph`, `ready`, `epic`, `tag`, `note`, `claim`, `claim-next`, `assign`, `sync`, or vector/embedding commands. `next` is ordering-based, not graph-based.

`--group` is intentionally singular. It is not a tag system: there is no repeated flag, many-to-many table, group entity, hierarchy, or group-specific behavior.
