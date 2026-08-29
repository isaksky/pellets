# Project Memory

Project memory is an optional keyword-searchable text store shared by every registered worktree workspace of one logical project. A worker may use it or ignore it. Memory does not change task selection and is not a second task system.

The authoritative schema is in [data-model.md](data-model.md); command-wide JSON and error rules are in [cli-spec.md](cli-spec.md).

## Purpose

A memory captures durable project knowledge that may be useful after an agent context boundary: a decision, discovery, convention, error and solution, or other reusable fact. It is deliberately free-form because the product does not decide what the agent must remember.

Examples:

- “Windows file replacement fails while an SQLite handle is open; close the pool before rename.”
- “The parser keeps `--` semantics compatible with Go’s flag package.”
- “foo-123 introduced schema version 3; older binaries must reject it.”

## Separation from pellets

Pellets and memories are independent authoritative records:

- A pellet represents executable work with status and priority.
- A memory represents searchable knowledge with provenance and approval.
- A memory has a logical `project_id`, but no workspace owner, pellet number, task foreign key, status, priority, external ID, or group field.
- References such as `foo-123` are ordinary text and may refer to a current, closed, or purged pellet.
- Closing or purging a pellet never creates, changes, or deletes memory.
- Memory search never affects `pl next`.

This loose relationship prevents memory from becoming an event log or dependency mechanism.

## Provenance and approval

Every memory records `created_by` as `agent` or `human`.

- Agent-created memory begins unapproved.
- Human-created memory is approved immediately.
- `pl memory approve` adds a human approval timestamp to agent-created memory.
- CLI memory commands do not edit text. The foreground `pl web` editor may replace text under full-row optimistic concurrency. Changing approved agent-created text clears approval; changing human-created text records a new human approval/update instant.
- The first release records approval state and time, not human identity or a full approval history.

Approval means “a human has reviewed this text,” not “this statement is guaranteed true.” Search results expose provenance and approval so an agent can weigh them appropriately.

## What to remember

Useful memories are self-contained facts likely to save future investigation:

- architectural or product decisions;
- repository-specific conventions;
- non-obvious environment or platform behavior;
- discoveries about unfamiliar code;
- failure modes and verified fixes;
- commands or procedures that are safe to repeat;
- concise outcomes from completed work when an agent chooses to preserve them.

Avoid:

- future work that belongs in a pellet;
- a running transcript or automatic event stream;
- secrets, credentials, private keys, or access tokens;
- large file copies that are cheaper to locate in the repository;
- speculative claims written as facts;
- dependency or blocking relationships between pellets.

## Record size and chunking

Each memory row is one independently retrievable idea. The CLI does not automatically chunk text.

Agents should split unrelated facts into separate memories and include enough local context for a search result to make sense alone. V1 accepts one non-empty, valid UTF-8 value up to 1 MiB (1,048,576 bytes); `pl memory --help` documents this conservative safety limit.

Because each row is independently retrieved and a web text replacement is one atomic authoritative/FTS transaction, approval semantics remain clear. Automatic chunking would create hidden child records and is intentionally absent.

## Keyword retrieval with FTS5

SQLite FTS5 indexes memory text with the `unicode61` tokenizer. Hyphen and underscore are token characters so references such as `foo-123` and common code identifiers remain searchable units.

Default search behavior:

1. Restrict results to the selected logical project; all of its worktrees see the same rows.
2. Escape ordinary input into safe FTS terms rather than treating it as raw FTS syntax.
3. Rank with FTS5 `bm25`.
4. Break equal ranks by newest memory first.
5. Return provenance and approval on every result.

`--approved-only` filters relationally after joining FTS rows to authoritative memories. An empty result is successful.

The FTS table is derived. Migrations or repair tooling may rebuild it from `memories` without loss of authoritative content.

## No semantic retrieval

Pellets v1 has no `sqlite-vec`, embedding model, inference runtime, model download, provider boundary, hybrid ranker, or re-embedding lifecycle. This is a deliberate complexity reduction.

Keyword search is the complete supported memory retrieval path, not a degraded fallback. Adding semantic retrieval requires evidence that FTS5 is insufficient and a new architecture decision covering binary size, offline operation, model licensing, platform support, performance, and migrations.

## CLI contract

### Create

```text
pl memory add (--text TEXT | --file PATH) [--created-by agent|human]
```

- Default `created_by` is `agent`, matching the primary caller.
- `--file -` reads text from stdin.
- `--created-by human` creates an immediately approved memory.
- Exactly one of `--text` or `--file` is required.

### List and show

```text
pl memory list [--approved-only] [--limit N]
pl memory show MEMORY_ID
```

Memory IDs are database-local positive integers used only by memory commands. They are not pellet references. Once an ID belongs to a committed memory row, it is never reused after removal; a later automatically allocated memory ID is greater than every previously committed memory ID. IDs may contain gaps, and an allocation that is rolled back before commit may be reused.

### Search

```text
pl memory search QUERY [--approved-only] [--limit N]
```

Search defaults to the current project and returns FTS rank, a bounded snippet, full provenance fields, and the memory ID. `show` returns the full text.

### Approve

```text
pl memory approve MEMORY_ID
```

Set `approved_at` and `updated_at` if approval is absent. Repeating approval is idempotent and retains the original approval and update times. The command represents an explicit human action; Pellets has no authentication layer.

### Web editing

`pl web` can create memory, replace memory text, and approve current text. Browser creation is always server-assigned `human` provenance and is immediately approved; the client cannot submit a different provenance. The web tool does not expose removal. Each edit or approval submits an opaque token for the complete authoritative memory row; storage validates it after acquiring the short writer transaction. A stale editor receives HTTP 409 with the current row and preserved draft, and no authoritative or FTS write occurs.

Text replacement sends the old text to the external-content FTS delete command, updates the authoritative row, and inserts the new FTS text in one immediate transaction. An approved agent-created memory becomes unapproved after its statement changes. A human-created memory remains human-approved and receives the edit timestamp as its new approval/update time because the web action is explicitly human-facing. Pellets records no editor identity or revision history.

### Remove

```text
pl memory remove MEMORY_ID --yes
```

Permanently delete one memory and its derived FTS row. Task purge never calls this command implicitly.

## JSON shape

Memory objects follow the global JSON v1 envelope:

```json
{"id":42,"project":"foo","text":"foo-123 established the released-migration immutability rule.","created_by":"agent","human_approved":true,"created_at":"2026-08-28T20:00:00Z","updated_at":"2026-08-28T20:05:00Z","approved_at":"2026-08-28T20:05:00Z"}
```

Search results may add `rank` and `snippet`. The authoritative full `text` remains available from `memory show`.

## Privacy and local behavior

- Memory text remains in the discovered local SQLite database.
- `pl` performs no external network request, telemetry, embedding, or external indexing. `pl web` serves only its local browser over loopback from embedded assets.
- The database is locally excluded from Git and must not be committed.
- Anyone who can read the database file can read memory; encryption at rest is not provided.
- Purge of closed pellets does not remove memory. Users must remove sensitive or obsolete memory explicitly.
- Backups made outside `pl` contain memory in plaintext.

## Deferred possibilities

Do not implement these in v1:

- memory categories or tags;
- memory revision history or CLI text editing;
- automatic extraction from pellets, diffs, or conversations;
- task foreign keys;
- structured task-group links;
- workspace/agent ownership, approval identities, or histories;
- expiration policies;
- vector or hybrid search;
- cloud or cross-machine synchronization.
