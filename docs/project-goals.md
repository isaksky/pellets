# Pellets: Project Goals

Pellets is a local task queue for coding agents. The product is named **pellets** and its command is `pl`.

This document defines the product boundary. The engineering design is in [architecture.md](architecture.md), the command contract is in [cli-spec.md](cli-spec.md), and the relational model is in [data-model.md](data-model.md).

## Vision

Long-running coding work is easier to complete when it is split into small, explicitly ordered units. Today an agent often stores that plan in Markdown, then repeatedly spends tokens locating, parsing, and rewriting the file. `pl` replaces that loop with small, deterministic database operations.

Pellets should feel like a durable local queue, not a project-management system. An agent can add work, ask what to do next, record progress, and recover after a context reset without reconstructing state from prose.

## Target users

The primary user is a coding agent operating in a local Git worktree. A human may inspect the queue, defer work for later approval, approve memories, recover work owned by a removed worktree, or purge old closed pellets, but interactive human project management is not the design center.

The operating assumption is **at most one active worker in one Git worktree**. A logical project represents one Git repository and may register its main work tree plus any number of linked worktrees as workspaces. Those workspaces share the project code, pellet numbers, priority order, groups, external IDs, search, and memories, while each may own one in-progress pellet. Two workers using the same worktree are not isolated. This is coordination, not agent identity or authentication.

## Core use cases

1. Split one long-running issue into a priority-ordered list of pellets.
2. Give all pellets for that issue the same opaque external ID, then filter work to that ID.
3. Give pellets from several external issues one optional group identifier, then focus work to that group.
4. Resume the current workspace's in-progress pellet after an agent turn or context boundary.
5. Atomically select and start the highest-priority eligible open pellet when the current workspace has no work in progress.
6. Add newly discovered work without rewriting the surrounding plan.
7. Defer a pellet to `maybe_later` for human review without making it executable work.
8. Store optional project memories, distinguish agent-created memories from human-approved memories, and retrieve them with keyword search.
9. Use one local database for one repository with several worktrees or for several unrelated sibling repositories.
10. Let a human inspect and safely edit that same authoritative database through an optional foreground, loopback-only web interface.
11. Install a narrow, portable Pellets agent skill at repository or personal scope so Codex and Claude can follow the current `pl` contract before first-use bootstrap.

## Goals

- Make persistent task operations cheaper in tokens than searching and editing Markdown.
- Provide a strict, stable JSON interface suitable for agents.
- Keep one sparse, explicit priority order for each project's actionable pellets; lower integers are higher priority.
- Make `pl next` deterministic and read-only: resume the current workspace's in-progress pellet first, otherwise return the highest-priority matching open pellet.
- Make `pl start-next` atomically resume or claim eligible work so concurrent worktrees cannot both act on one read-only selection.
- Preserve closed pellets by default and make destructive cleanup explicit.
- Support exact filtering by project, optional external ID, and optional group.
- Discover the nearest database by walking upward from the current directory, similarly to Git.
- Let an ordinary current-project command automatically create/register local Pellets metadata on first use, without a prerequisite project-initialization command or interactive code prompt.
- Support one database containing several logical Git projects and several worktree workspaces per project, with short project codes and project-local pellet numbers.
- Let a logical project rename its public code without changing its stable project identity or invalidating references that use former codes.
- Remain local and usable without an account, hosted server, daemon, or external network connection. The optional `pl web` process is a foreground loopback tool, not a runtime dependency.
- Provide keyword search over pellets and memories through SQLite FTS5.
- Produce self-contained macOS and Windows executables.
- Install an instruction-only `pellets` Agent Skill for Codex, Claude, or both without opening a database, changing Git state, or requiring a network connection.

## Non-goals

- Dependencies, blocking relationships, graphs, DAG traversal, or cycle detection.
- Epics, subtasks, milestones, parent-child work items, or disguised dependency structures.
- Agent accounts, assignment history, PID/process ownership, sessions, leases, heartbeats, expiry, orchestration, or background cleanup.
- More than one in-progress pellet per workspace. Different workspaces of one project may progress different pellets.
- Cloud synchronization, automatic Git synchronization, or committing the database to Git.
- A hosted or remotely reachable server, daemon, account system, or network API. `pl web` is intentionally loopback-only and foreground-bound.
- Tags, separate task notes, or an automatic task event/history log.
- Multiple groups per pellet, a group table, or behavior attached to a group.
- Custom workflows or custom statuses in the first release.
- Semantic/vector retrieval, embedding models, or embedding providers.
- Plugins or a general extension framework.
- Agent configuration generation beyond the focused portable `pellets/SKILL.md` artifact.
- Automatic conversion of closed pellets into memories.
- Becoming a general-purpose issue tracker or project-management platform.

## Product principles

### Simplicity is a feature

Every new concept must justify its schema, commands, state transitions, error cases, migrations, and token cost. Features that recreate project-management machinery should be rejected.

### Structured state beats editable prose

Pellets are authoritative relational records. Agents interact through commands and stable JSON instead of editing database files or generated Markdown.

### Priority is the order

Priority is not a small category such as “high” or “low.” For `open` and `in_progress` pellets, it is a unique sparse integer rank within a project. Lower values come first. `closed` and `maybe_later` pellets have no priority and do not occupy or churn the active queue. There is no second position field.

### Current work is worktree-scoped

An `in_progress` pellet names exactly one registered workspace from its project. A workspace has at most one such pellet. `pl next` resumes only the current workspace before considering open work, while `pl start-next` performs selection and ownership assignment atomically. Ownership is a worktree coordination pointer, not an agent, PID, lease, presence, or security model.

### Local means local

The database is never committed to Git. `pl` does not send task or memory contents over an external network. The web inspector serves only the local browser over `127.0.0.1`, performs no runtime network fetch, and stops with its foreground process. Pellets performs no telemetry.

The optional skill installer uses one embedded instruction template and local filesystem operations. Repository-scoped skills are ordinary files the user may choose to commit; `pl` never stages or commits them. Personal-scoped skills stay under the platform-resolved home directory. Installation does not inspect or open `.pellets` data.

### Agent output is an API

Compact JSON is the default output. Its versioned shape, exit codes, and stdout/stderr behavior are public compatibility contracts. Human-readable output is optional presentation.

### Destructive behavior is visible

Closed pellets remain present. `purge` is the only bulk deletion path, operates only on closed pellets, and requires explicit confirmation.

## Definition of lightweight

For Pellets, “lightweight” means:

- one `pl` executable;
- one SQLite file for one or more nearby projects;
- no required or background long-running process; `pl web` runs only while its foreground command remains active;
- no required configuration file;
- no hosted runtime, daemon, container, account, or external-network dependency;
- no model downloads or native vector extensions;
- a small Go package graph and explicit SQL instead of an ORM;
- bounded, predictable JSON responses;
- schema and migrations embedded in the executable;
- no Git writes other than a local exclude entry when needed to keep the database untracked.

The SQLite driver may be a pinned CGo-free Go dependency. “Favor the standard library” does not mean reimplementing SQLite or Git repository discovery.

## Success criteria

The first release is successful when all of the following are true:

- An agent can begin directly with `pl add`, automatically bootstrap the project/workspace, reorder pellets, start one, close it, and obtain the next pellet without opening a planning file.
- `pl next` returns the current workspace's in-progress pellet before open work and otherwise returns the lowest-priority eligible open pellet without writing.
- Concurrent linked worktrees can use `pl start-next` to start distinct pellets while each workspace remains limited to one in-progress pellet.
- Project, exact external-ID, and exact group filters behave consistently across `list`, `next`, and `search`.
- Pellet references remain concise and unambiguous, for example `foo-123`.
- A database at a common ancestor can serve multiple Git repositories while actionable priority and pellet numbers remain project-scoped.
- Active-queue insertions and moves use integer arithmetic, survive gap exhaustion through transactional rebalancing, and never expose duplicate non-null priorities.
- Concurrent CLI processes cannot allocate duplicate pellet numbers, assign one pellet twice, or violate the one-in-progress-per-workspace invariant.
- Agent-created memories can be searched, reviewed, and marked human-approved without being attached to task rows.
- A human can inspect every project, workspace, pellet state, and memory in the nearest database and perform routine non-destructive edits through `pl web` without weakening queue, ownership, FTS, or concurrency invariants.
- Core workflows pass automated tests on macOS and Windows.
- Codex and Claude can discover logically identical repository- or personal-scoped `pellets` skills, and repeated installation is safe and idempotent.
- No documented or implemented workflow requires dependency concepts, vector search, Git commits, a daemon, or an external network connection.

## Constraints

- Implementation language: Go.
- Authoritative storage: SQLite with FTS5.
- Database path: `.pellets/pellets.db` beneath a selected database root.
- Logical-project identity: Git's common directory for the repository, normalized relative to the database root when possible.
- Workspace identity: Git's worktree root together with its worktree-specific Git directory, normalized relative to the database root when possible.
- Project code: automatically derived initially, renameable, unique within a database-wide canonical-and-redirect namespace, and 1–12 lowercase ASCII letters, digits, or internal hyphens. Former canonical codes remain direct redirects to the stable project row.
- Pellet identity: a positive project-local integer; external reference is `<project-code>-<number>`.
- Group: at most one optional, opaque, case-sensitive string per pellet, scoped to its project and used only as an exact filter.
- Statuses: `open`, `in_progress`, `closed`, and `maybe_later`.
- Timestamps: Julian day values stored as SQLite `REAL` values.
- Priority: a positive, project-unique integer for `open` and `in_progress` pellets; `closed` and `maybe_later` pellets store `NULL`.
- Distribution targets: macOS and Windows. Hands-on testing is available only on macOS, so Windows requires CI coverage.

## Open questions

These do not block the initial design:

- Which Go version becomes the minimum supported version?
- Is Windows on ARM64 a first-class first-release target or a later target? Windows AMD64 is required.
- Should release binaries be code-signed in the first release?
- Is a database backup/export command needed after the first release, despite there being no synchronization feature?
- What database-size and response-time thresholds should become release gates after realistic agent workloads are measured?
