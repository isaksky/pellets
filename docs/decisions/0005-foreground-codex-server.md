# ADR 0005: Foreground server and optional Codex supervision

- Status: Accepted
- Date: 2026-09-09

> This record supersedes ADR 0001 only where it rejected a local server or
> model-runtime boundary. ADR 0001's rejection of daemons, dependency graphs,
> plugins, generic event history, remote services, and orchestration remains
> accepted.

## Context

The loopback inspector needs a canonical product boundary that can eventually
supervise local coding work without turning Pellets into a hosted agent system.
The old `pl web` name describes only the browser surface and obscures that the
long-lived foreground process, not a tab, owns the work it starts.

## Decision

- `pl server` is the canonical foreground command. It preserves the inspector's
  `127.0.0.1` listener, port and `--no-open` options, ready URL on stdout,
  offline assets, strict Host/Origin/CSRF controls, and interrupt behavior.
  `pl web` remains a deprecated compatibility alias with canonical server help.
- The foreground server owns both the UI and every Codex execution it starts.
  Closing a browser tab does not stop a run. Stopping the server stops its
  runs. There is no daemon, remote listener, background start/stop service,
  separate persistent worker, or automatic worktree creation.
- Codex execution is optional. Normal queue/memory commands and the inspector
  stay available without Codex installed or authenticated. When execution is
  used, Pellets reuses the installed Codex runtime, credentials, configuration,
  instructions, and tools. It does not add a second runtime integration.
- A compact durable run record may preserve run/worktree identity, conversation
  linkage, concise activity, state, and stop/interruption outcome. It is not a
  general task-event history. At most one run may be active per worktree;
  restart never resumes a run automatically. Questions, steering, stopping,
  and resuming are explicit.
- Automatic approval uses `REVIEW` by default. Pellets does not grant blanket
  approval or disable the sandbox.
- Normal work remains test → commit → close. Review checkpoints have explicit
  scope and a separate Codex review context. Any resulting focused follow-up
  pellets are deduplicated; review does not create dependencies, an epic, or a
  general workflow engine.

## Consequences

The server is the one intentional long-lived local process, but only while its
foreground command is alive. The narrow execution record and review boundary
provide enough continuity for interrupted work without persistent autonomous
operation. Future execution implementation must keep queue semantics simple
and must not add push, pull-request, full Git UI, Claude execution, plugin, or
dependency-graph features under this decision.
