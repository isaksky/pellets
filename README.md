# Pellets

Pellets (`pl`) is a local SQLite task queue for coding agents. It keeps project-local task ordering, worktree-scoped in-progress ownership, exact external-ID/group filters, FTS5 search, and independent project memory in the nearest `.pellets/pellets.db`.

The executable is CGo-free and targets macOS and Windows. Pellets has no account, telemetry, cloud synchronization, model, plugin runtime, or required service.

## Build and test

```text
go build ./cmd/pl
go test ./...
./scripts/verify-cross-builds.sh
```

The cross-build script verifies `CGO_ENABLED=0` artifacts for macOS AMD64/ARM64 and Windows AMD64. Runtime dependencies are Go's standard library plus the pinned in-process SQLite driver.

## Start a project

```text
pl init --code foo
pl add "Implement parser" --group parser --external-id github:acme/foo#84
pl add "Test invalid input" --group parser
pl start-next --group parser
pl close foo-1
pl memory add --text "Parser identifiers preserve underscores." --created-by agent
```

Commands discover the nearest database by walking from the current directory to the filesystem root. A database can hold several unrelated projects, and linked Git worktrees register as separate workspaces of one logical project.

Compact versioned JSON is the default command output. See [the CLI specification](docs/cli-spec.md) for exact commands, fields, errors, and lifecycle behavior.

## Install the Pellets agent skill

Install the focused, instruction-only `pellets` skill for Codex, Claude, or both:

```text
pl --human skill install
pl skill install --scope repo --agent both --yes
pl skill install --scope personal --agent codex --dry-run
```

Choose **Repository** when the skill should travel with one Git worktree/repository and be available to collaborators after they choose to commit it. Codex reads `<git-root>/.agents/skills/pellets/SKILL.md`; Claude reads `<git-root>/.claude/skills/pellets/SKILL.md`. The files remain ordinary untracked files until the user stages them—`pl` never changes the Git index or ignore files.

Choose **Personal** when the same Pellets guidance should be available across local projects without adding repository files. Codex reads `<home>/.agents/skills/pellets/SKILL.md`; Claude reads `<home>/.claude/skills/pellets/SKILL.md`. The home directory is resolved with the operating system API, and personal installation stays outside the selected repository.

The interactive wizard requires `--human` plus terminal stdin/stdout. Default JSON mode never prompts and requires explicit `--scope`, `--agent`, and `--yes` for writes. `--dry-run` returns every target and the complete generated content without creating directories or temporary files. Differing existing files are refused unless the user separately confirms replacement or supplies `--force`; Both is preflighted and rolled back as one installation.

The generated instructions activate implicitly only when a prompt explicitly names `pl` or Pellet/Pellets; generic task, issue, backlog, project-management, or memory requests are deliberately excluded. The format and locations follow the current official [OpenAI Codex skill guidance](https://developers.openai.com/codex/skills) and [Claude Code skill guidance](https://code.claude.com/docs/en/skills).

## Local web inspector

```text
pl web
pl web --no-open
pl web --port 8123 --no-open
pl --project foo web
```

`pl web` is an optional foreground local tool, not a daemon. It:

- uses the same nearest-database discovery as other commands;
- listens only on `127.0.0.1` and selects a free port by default;
- prints its URL after listener readiness and optionally opens the default browser;
- remains in the foreground until interrupted;
- displays every registered project/workspace, every pellet state, and every authoritative memory;
- supports composable URL filters, safe task search, stable deep links, task inspection/editing/reordering/lifecycle, memory creation/editing/approval, and explicitly confirmed worktree recovery;
- never exposes purge, memory removal, or another irreversible action.

All browser assets are embedded and work with external networking disabled. The repository vendors HTMX 2.0.4 at `internal/webui/assets/htmx-2.0.4.min.js` with its Zero-Clause BSD license. Styling, theme behavior, EventSource glue, focus/dirty-form protection, and state-change treatment are repository-owned; there is no Node/npm build, CDN, web font, icon service, SPA framework, CSS framework, WebSocket, or service worker.

Live refresh uses invalidation rather than mutable event payloads. Exactly one query-only monitor connection compares its own SQLite `PRAGMA data_version` values while SSE clients are connected. External CLI and web-writer commits prompt authoritative HTMX GETs; initial loads and slower polling recover missed events. GETs use separate read-only/query-only connections and release SQLite rows before rendering.

Existing-row edits use opaque complete-row optimistic tokens. If a CLI or another browser edits the row first, the stale request writes nothing and receives HTTP 409 showing current state beside its preserved draft. Memory text and its FTS entry change atomically.

### Local security boundary

There is no user login. The server instead enforces a loopback-only listener, exact Host and Origin, per-process CSRF cookie/form capability, POST/form media types, HTML escaping, and restrictive CSP/security headers. These controls prevent ordinary cross-site browser mutation, but they do not protect against another process running as the same local user or anyone who can read/write the database file. Do not expose the port through a proxy or tunnel.

## Scope and limitations

- A pellet has one optional opaque group string, not tags or an epic hierarchy.
- Priority is the unique sparse integer order of active pellets; closed/deferred pellets have no priority.
- Workspace ownership identifies a registered Git worktree, not an agent, process, lease, or presence signal.
- Memory is independent free-form project knowledge with provenance and approval; it is not a task history or foreign-key attachment.
- Pellets has no dependency graph, custom workflow, remote API, background server, synchronization, vector search, or automatic memory generation.
- The SQLite file is local plaintext and is excluded from Git; keep normal filesystem backups and access controls.

Product boundaries and implementation details live in [project goals](docs/project-goals.md), [architecture](docs/architecture.md), [data model](docs/data-model.md), [memory](docs/memory.md), and the [implementation plan](docs/implementation-plan.md).
