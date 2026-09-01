# ADR 0004: Renameable Project Codes with Direct Redirects

- Status: Accepted
- Date: 2026-08-31
- Supersedes: the immutable-project-code decisions in [ADR 0001](0001-initial-architecture.md) and [ADR 0002](0002-worktree-scoped-workspaces.md)

## Context

A logical project's public code appears in committed text, prompts, scripts,
memory text, browser links, and external systems. Permanently forbidding a
rename avoids broken references but also binds the canonical public name to
the repository name observed at first registration. Changing the code without
compatibility would silently invalidate or reinterpret existing `<code>-N`
references.

The logical project already has a stable database `project_id`, and pellet
numbers are allocated within that row. Compatibility therefore does not
require mutable pellet identity or redirect chains; it requires reserving each
former code as a direct alias of that stable row.

## Decision

- `projects.code` is the current canonical public code. A new
  `project_code_redirects` row maps a former code directly to `project_id`.
- Canonical codes and redirects share one database-wide namespace. Unique
  keys plus cross-table insert/update triggers prevent ambiguity even for
  direct SQL and concurrent writes.
- Resolution checks canonical codes and direct redirects once. Redirects never
  target codes, so chains and recursive lookup do not exist.
- Every successful CLI, application, storage, memory, lifecycle, filter, web,
  and deep-link result emits the current canonical code or pellet reference.
  Old web paths temporarily redirect to their canonical equivalent.
- Rename uses one immediate transaction and preserves project IDs, pellet
  numbers, workspaces, queue state, memories, and FTS content. It updates the
  canonical code and creates a direct redirect for the former code atomically.
- Renaming to the current code is idempotent. Renaming to the project's own
  redirect promotes it without confirmation and keeps the previous canonical
  code as a redirect.
- Another project's canonical code is a typed hard conflict and is never
  deletable through rename.
- Another project's redirect remains reserved by default. Terminal human mode
  lists every conflicting rule and canonical target, warns that deletion can
  break or reinterpret old references, and asks an explicit default-no
  question. No, EOF, and interruption make no write.
- JSON and other noninteractive invocations never prompt. They return a typed
  confirmation-required result with the complete conflict set and exact retry
  arguments. Both `--delete-conflicting-redirects` and `--yes` are required.
- The rename transaction revalidates the exact displayed conflict set. A
  changed set fails write-free; authorization applies only to the originally
  displayed rules.
- Redirects use `ON DELETE CASCADE` so a future valid project deletion cannot
  leave dangling aliases. Closed-pellet purge does not affect projects or
  redirects.

## Consequences

### Positive

- Existing pellet references remain resolvable after a project rename.
- Current output converges on one canonical code without rewriting historical
  commits, scripts, prompts, memories, or external records.
- Direct stable-ID targets make resolution bounded and prevent redirect-chain
  corruption.
- Transactional conflict confirmation is safe against concurrent namespace
  changes.

### Negative

- Former codes remain reserved and reduce the pool of short generated names.
- Explicitly deleting another project's redirect may break or reinterpret
  references outside Pellets; the CLI must keep that risk visible.
- Every code-accepting path must resolve stable project identity instead of
  comparing raw code strings.

## Rejected alternatives

### Keep project codes immutable

Rejected because first-registration naming should not permanently determine a
logical project's public name when compatibility can be preserved by its
existing stable project identity.

### Rewrite every stored or external reference

Rejected because Pellets cannot enumerate commits, prompts, scripts, memory
prose, browser history, or external systems, and rewriting free text would be
unsafe.

### Redirect one code to another code

Rejected because this creates chains, cycles, recursive resolution, and
additional transactional repair requirements. Redirects target stable project
rows directly.

### Silently reuse or delete redirects

Rejected because an old reference could begin resolving to a different
logical project. Conflicts require exact visible authorization and
transactional revalidation.

Related documents:

- [Project goals](../project-goals.md)
- [Architecture](../architecture.md)
- [Data model](../data-model.md)
- [CLI specification](../cli-spec.md)
- [Memory](../memory.md)
- [Implementation plan](../implementation-plan.md)
