---
name: pellets
description: Use only when a prompt explicitly names the pl command or Pellet/Pellets and asks to use, inspect, or manage Pellets. Do not activate for generic task, issue, ticket, queue, backlog, project, project-management, or memory requests that do not name pl or Pellet/Pellets. Explicit skill invocation remains available.
---

# Pellets

Use `pl` as the authoritative local queue and memory interface when the user explicitly asks for Pellets work.

## Start safely

- Run `pl --help` and the relevant command's `--help` before relying on remembered syntax. The installed executable is authoritative.
- Prefer the default compact JSON output for machine parsing; use `--pretty` only when readable JSON helps. Do not scrape `--human` output.
- Let `pl` use the database binding in Git’s common directory, falling back to ancestor/worktree discovery only when unbound. Linked worktrees share that binding even outside the checkout. On `database_binding_unavailable`, restore or coordinate repair of the reported database; never create a replacement queue.
- On the first valid current-project command, let `pl` create or discover the database, derive the initial canonical project code, and register the logical repository and current worktree automatically. No separate project initialization command is required. That one-time bootstrap may create `.pellets/`, add it to Git's local exclude, and record the shared database binding before the requested operation runs.
- Treat former project codes as direct redirects. Inputs may use them, but successful output is authoritative and always uses the current canonical project code and pellet reference.
- Resolve the current Git worktree before changing work. One logical project is shared across its linked worktrees, but each registered worktree is a distinct workspace with at most one in-progress pellet.
- Before beginning new work, use atomic selection:

```text
pl start-next
pl start-next --external-id "github:acme/tool#84" --group "parser-rollout"
```

`pl next` is read-only. It resumes only the current workspace's in-progress pellet before considering open work. Resume that pellet; never resume work owned by another workspace.

## Handle coordination deliberately

- On `workspace_already_in_progress`, run `pl next`, inspect the current workspace pellet, and resume it instead of starting another.
- On `pellet_in_progress_elsewhere`, do not steal or duplicate the work. Refresh state and select other eligible work with `pl start-next`, or ask the user to coordinate.
- Keep retries bounded. Re-read the current state between retries and stop when the same conflict persists.
- Release current-workspace work normally with `pl release`. Use recovery only when the recorded worktree is unavailable and the user has explicitly approved the exact workspace recovery:

```text
pl release foo-12 --recover-workspace 7 --yes
```

Recovery coordinates worktrees; it does not authenticate an agent or transfer a lease.

## Maintain the queue

- For retryable creation, supply `pl add --request-id ID` with a fresh ID per intended pellet and reuse it on retries. Matching inputs replay the creation result for two days; changed inputs return `request_id_conflict`. Every successful add expires older retry records without deleting pellets. Use `pl show` for current state after a replay.
- Use `pl add` for a focused, independently actionable follow-up. Do not encode epics or dependencies in pellets.
- Use lifecycle commands for status and ordering commands for priority:

```text
pl add "Handle invalid UTF-8" --before foo-12 --external-id "github:acme/tool#84" --group "parser-rollout"
pl move foo-13 --after foo-12
pl close foo-12
pl defer foo-13
pl reopen foo-13
```

- Preserve project semantics. A project is one logical Git repository shared by its registered worktrees. Put global selection before the command when an explicit project is appropriate: `pl --project foo project show`.
- Rename a project only when the user requests it: `pl [--project OLD_CODE] project rename NEW_CODE`. A foreign canonical code is a hard conflict. If JSON returns `project_rename_confirmation_required`, show the user every conflicting redirect and canonical target plus the warning; do not infer permission. Retry only after explicit approval with the exact documented `--delete-conflicting-redirects --yes` contract. Human confirmation is terminal-only and defaults to no.
- Preserve one optional opaque `external-id` for correspondence with an outside system and one optional opaque `group` for exact filtering. A group is not an epic, dependency, hierarchy, or tag set.
- Lower priority order means earlier work; do not invent or edit raw priorities.

## Use project memory correctly

- Record only durable, self-contained knowledge. Future work belongs in a focused pellet.
- Agent-authored memory must retain agent provenance and begins unapproved:

```text
pl memory add --text "Parser identifiers preserve underscores." --created-by agent
pl memory search "parser identifiers" --approved-only
```

- Use `--created-by human` only for text actually supplied or authored by a human. `pl memory approve` represents explicit human review; never use it to mark agent-created content as human-approved.
- Treat memory as project knowledge, not task state, history, or a dependency edge.

## Preserve the product boundary

- Never edit the SQLite database directly or commit `.pellets` data.
- Never invent dependencies, blocking graphs, epics, agent/PID ownership, leases, heartbeats, or assignment history.
- Never maintain a parallel Markdown task queue. Pellets is authoritative once the work is recorded there.
- Do not manually change Git state, ignore files, agent configuration, or repository policy unless the user separately requests that work. Pellets' own first-use local-exclude safeguard is expected.

## Optional foreground server

- Use `pl server [--port PORT] [--no-open]` for the local foreground inspector. `pl web` is a deprecated compatibility alias; use `server` in new commands and examples.
- The server is loopback-only and foreground-bound. It owns the browser UI and any Codex execution it starts: closing a browser tab does not stop work, while stopping the server does. Do not treat it as a daemon, persistent worker, remote service, or worktree manager.
- Queue and memory commands remain usable without Codex installed or authenticated. If the server supervises Codex, it reuses the installed runtime's credentials, configuration, instructions, and tools; never assume blanket approval or a disabled sandbox.
- The internal Codex adapter checks the installed runtime's stable app-server schema and uses local stdio. Run preflight requires `pl` on the inherited child `PATH` and reads account status through app-server; Codex owns credential storage and refresh, so never copy credentials into Pellets, prompts, browser state, or logs. It is not yet a public runner command. Treat a missing tool, login, or unsupported capability as an actionable error; do not substitute a separate model/tool loop or invent command flags.
- Saved workspace run settings and one-run overrides may select a Codex executable, open-ended model ID, runtime-supported reasoning effort, and bounded transport limits. Empty model and effort retain normal Codex defaults; `gpt-5.6-sol` with `high` effort is an offered preset, not a forced default.
- Prepared work uses `workspace-write` with `on-request` and `approvals_reviewer=auto_review`. This routes eligible requests to automatic approval review; it never blanket-accepts actions or disables the sandbox. Preserve managed restrictions and surface questions, denials, timeouts, and errors. If the bound database is outside the worktree, grant only its immediate directory rather than broad filesystem access.
- A canceled client request does not prove Codex work stopped. Supervised work must explicitly interrupt the turn and retain its terminal outcome and conversation identity for read/resume; process exit alone is not evidence that the work succeeded.
- Durable execution attempts are orchestration evidence, not another queue. Keep run states and phases separate from pellet lifecycle; reconnect by the exact saved run/thread/turn identity and resume only on explicit user action. A missing conversation or commit must remain visible as unavailable evidence, never success or permission to substitute newer work.
- A persisted pending Codex operation, including `turn/interrupt`, must be reconciled before another consequential call or attempt. Do not replay it on reconnect. An interrupt request or acknowledgement is not evidence that the turn stopped; retain concurrent progress and the returned conversation IDs when reconciling the operation.
- The internal recorder preserves stable project/workspace/pellet identity, original title/description, starting HEAD, verified result commit, effective settings and exact mode/filters. Project renames and pellet purge do not retarget these records; retention trims bounded activity only. Do not copy credentials, raw errors/configuration, prompts, or full transcripts into run summaries. This is an internal server boundary, not a new `pl` command.
