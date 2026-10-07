# Project agent guidance

## Protect the user's running Pellets instance

Treat every pre-existing Pellets server, listener, database, and installed binary
as user-owned. Development, testing, and UI verification must run independently.

- Never stop, restart, signal, or replace a pre-existing process to test changes.
  Never free a port with `kill`, `pkill`, `killall`, `fuser -k`, or an
  `lsof`-to-`kill` pipeline. An occupied port is not permission to stop its owner.
- Start test and preview servers with `server --port 0 --no-open` and use the URL
  printed by that child. Never assume the user's port (including 7419) is a test
  endpoint. A test specifically exercising a fixed port must fail or skip if it
  cannot reserve it; it must leave the existing listener alone.
- Cleanup may signal only a child process started by the current test or agent
  session, tracked through its process handle or captured PID. Do not infer
  ownership from a process name, port, executable path, or an old PID file.
  Wait for that child to exit before removing its fixture.
- Build into a unique temporary directory. Do not run `go install`,
  `scripts/build-install.sh`, or overwrite an installed `pl` as part of testing.
- Use a fresh independent Git repository in an OS temporary directory. Run the
  temporary binary's `--json init-db` there before any project command or server.
  A nested directory, `--project`, or linked worktree does not isolate a database;
  worktrees share the Git database binding. Verify any reused fixture resolves
  to its own database before use. Never run mutation tests, migrations, execution,
  recovery, or cleanup against the user's database or `.pellets` files.
- Use the deterministic Codex test peer for execution tests. For a preview that
  does not need execution, set `PELLETS_CODEX_EXECUTABLE` to a nonexistent path
  inside its fixture to prevent background discovery of the user's runtime.

A request to implement or test a change does not authorize changing the running
instance. Only an explicit user request targeting that instance can do so.
Use the [isolated local preview recipe](README.md#isolated-local-previews) for
manual verification. These rules apply to ad hoc shell commands and browser
tools as well as checked-in test runners.

## Browser UI changes

Read [UI and design-system guidance](docs/ui-agent-guidance.md) before planning,
implementing, or reviewing changes that affect the browser interface or design
system. This includes:

- Templates, CSS, browser JavaScript, and web components in `internal/webui/`.
- Server-rendered markup, UI interactions, accessibility, and responsive layout.
- The development gallery and browser tests that define UI behavior or appearance.

Read it even when a change starts outside those paths if it affects the browser
UI. It is optional for work with no browser UI impact.
