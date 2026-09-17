# Workbench browser interface

The production browser uses the Workbench v2 presentation in
`internal/webui/templates/` and embedded offline assets. The authoritative design
reference was `pellets-exp/iterations/workbench-v2`; production does not import or
write prototype state. Palette tokens and the presentation-only dropdown component
are adapted from that reference. Go, SQLite, and Datastar remain the application
boundaries. JavaScript holds presentation state, never ownership or execution
selection.

## Navigation and presentation

The first breadcrumb selects the project. The second selects the shared Queue,
Memories, or a registered workspace. Workspace links appear in the left sidebar.
A workspace queue uses `/projects/CODE/tasks?workspace=ID`; existing
`/projects/CODE/workspaces/ID` links still open that workspace queue. The execution
sidebar always has an explicit project-local workspace, including while browsing
Memories or the shared Queue. The existing shared Queue entry point is retained;
workspace URLs preserve a selected workspace. Browsing status, group, external ID, search, and sort
remain independent from scheduler selection.

Queue rows are 37 pixels high with reference, lifecycle status, title, group, and
owner. Narrow layouts prioritize reference, status, and title; full metadata stays
in the dialog. Pellet and memory dialogs open directly in edit mode and close on a backdrop
click or Escape. Unsaved edits retain the discard guard. A fixed dialog footer places Cancel and
the primary save action on the right. More actions on the left contains lifecycle
operations and memory approval; queue and scope controls stay beside their fields. They support optimistic conflicts,
lifecycle operations, recovery, provenance, and explicit human memory approval.
An owned pellet and a running process remain distinct.

The Filters control shows the selected status and opens a panel for queue filters
and sorting. It stays anchored beside search without moving the toolbar. The panel
fits the viewport above the scrolling panes and dismisses on Escape, outside
click, or keyboard focus leaving it. A nested selector consumes the first Escape.
Changes apply immediately; Clear filters preserves the browsing workspace,
execution sidebar selection, and sort order.

Both sidebars collapse independently using the bottom corner buttons. The theme
selector immediately precedes the execution toggle and offers Gruvbox Light,
Gruvbox Dark, Light, Dark, and Icy. Theme, panel visibility, and the Plan/Execution tab are database-wide SQLite settings, initialized from existing browser preferences only when unset. Saved database values take precedence on reload. Changes apply immediately without rebuilding dialogs or execution; failed saves offer Retry. Browser localStorage values are fallback
presentation values applied before first paint. Theme updates do not navigate,
rebuild execution, or discard input. Minor changes darken secondary light-theme
text from the prototype to retain readable contrast.

At phone widths, navigation becomes compact horizontal rows and the shared right
panel opens over the available content area. Both stay independently collapsible. The status bar remains
pinned, and the breadcrumb continues to navigate when the left sidebar is hidden.

## Planning chat

The right panel has Plan and Execution tabs. Its footer toggle hides the whole
panel and remembers the selected tab. Switching tabs, projects, themes, or queue
filters preserves planning and execution drafts independently. Execution status
remains visible on the tab and on the collapsed panel toggle.

Planning conversations and editable draft pellets are persisted in the project
database, with optimistic versions. A conversation stays bound to its original
project and saved workspace when the main view changes. New chats use the
workspace selected in the workbench; the panel displays its full path. A saved
workspace binding cannot be changed: start a new chat to use another worktree.
Legacy chats without a binding automatically use the project’s sole checkout.
The workspace selector is hidden when there is no choice to make. Projects with
multiple checkouts still require a one-time selection for unbound chats. Missing or changed checkouts stop planning rather than selecting another
workspace. Model discovery uses the same selected workspace and its settings.
New chat requires confirmation when replacing
visible content; existing stored conversations are not destructively deleted.
Compact draft rows expand for title, description, acceptance criteria, and group
editing. Add, remove, split, combine, select, and refinement controls operate on
drafts. Creation is an explicit action: selected drafts become ordinary open
pellets atomically, with stable references preventing duplicate creation on retry.
Creation never claims work or starts execution. Group routing uses the current
project assignments; groups retain their existing opaque exact-value semantics.

Sending a message uses the configured Codex runtime in a separate ephemeral
session with bounded conversation and draft context. Model choices come from
that runtime; there are no local template replies or simulated model events.
The planner can inspect repository files and Git state using shell commands.
The **Access** dropdown offers **Automatic approval** (the default) and **Full access**. Automatic approval uses a
workspace-write sandbox with `on-request` approvals and the runtime's
`auto_review` reviewer. Network access and additional writable roots are not
pre-granted in automatic mode. Apps, plugins, MCP tools, browsing, and delegation remain disabled.
Full access uses `danger-full-access`, `never` approvals, and the `user` reviewer.
The choice is saved in the chat and captured for each request and retry. Managed
policy and runtime capability checks must permit the selected mode; there is no
automatic fallback to another mode. An unresolved request for human
input or approval stops the call with an actionable error.

The planner is instructed to investigate and propose work without implementing
changes or mutating pellets through the shell. This is a behavioral restriction,
not a read-only filesystem guarantee: workspace writes are technically permitted,
and reviewer-approved commands can exceed the sandbox. Shell side effects are
not rolled back if a response fails or the chat version conflicts. Planning has
no execution ownership or schedule. Failed, cancelled, or interrupted model calls
leave the saved chat unchanged; retry is explicit. A successful exchange and its
draft proposals are saved together using the original chat version. A response
may append drafts or refine the explicitly selected uncreated draft, never
silently replace unrelated work.

Each chat is bounded to 200 messages, 200 draft rows, and 512 KiB of encoded state.
The browser retains unsaved input on conflicts and request failures. A bounded
one-use reload handoff retains draft edits and exact retry identities. Long chats
must start a new conversation rather than silently evicting prior messages.
Planning is independent of execution evidence, review checkpoints, memory
provenance, and checkpoint approval. Its acceptance text is included in the created pellet's
description; it does not claim tests passed or mark a review approved.

The optional live smoke test starts a real model turn using the configured account,
asks for an automatically reviewed shell read of a temporary file, and verifies
its unique contents in the structured reply. Normal tests skip it:

```sh
PELLETS_CODEX_PLANNING_LIVE=1 go test ./internal/codex -run '^TestInstalledPlanningShellAutomaticReview$' -count=1 -v
```

## Workspace assignments

Assignments persist in SQLite with project-scoped compare-and-swap versions.
Workspaces can select exact groups, use All other groups, and optionally include
ungrouped work. Explicit assignments may overlap. The project can disable routing
while keeping saved assignments. Existing and newly registered workspaces default
to All other groups plus ungrouped work, preserving prior eligibility without
inventing role names or demo assignments.

Assignment changes apply to the next atomic claim. Owned work remains visible and
is never transferred or silently retargeted. Exact Resume restores the policy
captured by its prior attempt or preflight receipt. A continuing schedule reads
current assignments again for subsequent new claims. CLI exact-group behavior is
unchanged. See [the data model](data-model.md) for persistence and captured intent.

## Review dividers

Each divider represents an actual checkpoint. Brackets derive from explicit target
identities, with separate lanes for overlaps and dashed segments across unrelated
rows. Hover and keyboard focus highlight exact visible members. Counts show hidden
members, while the dialog includes the entire scope and evidence.

A row action menu or context menu inserts a checkpoint before or after an active
pellet. The initial scope comes from the authoritative queue since the preceding
checkpoint, independently of the current display filter or sort. It remains
editable before submission. Insertion atomically checks the anchor and target row
versions. A checkpoint needs explicit scope; adjacency never becomes evidence.

Open, unowned checkpoints can change scope or be removed. Removal defers the
checkpoint and retains a restore operation; it does not purge data or record a
successful review. Deferred checkpoints remain discoverable using Maybe later.
Scope changes create a new generation and preserve earlier captured evidence and
review outcomes. See [checkpoint management](checkpoint-management.md).

## Live execution

Editing an ordinary pellet during implementation updates its running
conversation. A fresh, separate `gpt-5.6-terra` agent at `max` reasoning compares
the exact old and new title, description, group, and external ID. It is read-only
and instructed to compare only, without tools or implementation work. Cosmetic
or organizational edits need no additional implementation turn. Substantive
changes are sent to the executing agent as a follow-up with the current task.
The implementation model and workspace remain unchanged.

The server waits for assessment before finalizing. If the original turn finishes
while the assessment is pending, it continues the same conversation with the
updated requirements. After steering a running turn, it also obtains a fresh
verification result in a continuation turn, so a final answer already in flight
cannot close changed work. Multiple edits are assessed and delivered in order.
Pending human questions remain answerable; edit delivery waits for their resolution.

The execution activity shows assessment and delivery. Exact edit snapshots and
assessment receipts survive restart; detailed activity remains temporary.
Release/reclaim, reopening, checkpoint scope changes, and changes after durable
finalization begins retain the existing recovery checks. Assessment failure,
unavailable Terra/max, or unconfirmed delivery stops visibly for attention and
preserves the issue and work. Restart never resumes or replays a delivery
automatically. This handling also covers CLI edits to the same running pellet.

The execution sidebar uses durable run and schedule state for controls, with a
separate bounded activity stream for reported events. File operations expand to
reported source/read output or diffs; source and diffs receive local syntax
highlighting. Commands include output and exit codes when reported. Progress,
questions, approvals, steering, stopping, and explicit Resume retain the existing
application checks.

Datastar patches authoritative regions while protecting edited records. Activity
uses stable event IDs and cursor updates without rebuilding forms. Workspace input
drafts, focus, caret, disclosures, and scroll position survive live refreshes and
presentation changes. Browser caches and server projections are bounded. Password
answers are never saved to presentation storage.

Detailed activity is an in-memory projection, not a durable transcript. Text and
command output appear when complete notification snapshots arrive; partial deltas
are withheld to avoid leaking split credentials. Source is shown only when
reported, and command-based reads are labeled reported read output. Restart or
retention eviction explicitly makes these details unavailable. Durable state,
review evidence, and recovery controls survive. No server restart resumes work.
See [execution activity](execution-activity.md) for protocol and resource bounds.

## UI build compatibility

Each embedded UI build has a deterministic revision derived from its templates,
assets, and protocol version. Full pages declare that revision and load assets
under `/assets/REVISION/`; relative JavaScript imports stay within the same build.
The server never serves current asset content under an older revision path.

Datastar requests must carry the matching `Pellets-UI-Revision` header. Missing or
stale revisions receive `412 ui_revision_changed` before application reads or
mutations, with no HTML fragments. Explicitly stale revisions on other browser
requests are also rejected; ordinary non-Datastar API clients without this header
retain their existing behavior. `/ui-version` exposes the current revision, and
invalidation and activity streams announce it before sending updates.

A loaded browser pauses updates when the server revision changes and offers an
explicit reload. Reloading does not save restored record or assignment edits,
claim pellets, or resume execution. Tabs loaded before this revision protocol was introduced need
one ordinary manual reload: the new server prevents their old Datastar client
from consuming incompatible markup, but cannot replace JavaScript already running
in that document.

The reload button temporarily stores at most 256 KiB of unfinished form values
in this tab's session storage and consumes that handoff once on reload. Record
and assignment versions remain the original optimistic tokens; CSRF comes from
the new page. Passwords and file inputs block the handoff until cleared and are
never included. Forms are matched to their original action and execution/request
identity. Input whose controls no longer exist is shown for copying, rather than
submitted to another action. Drafts, caret/focus, and open disclosures are restored;
no mutation is submitted by this handoff. A pending browsing filter is applied
through its read-only queue request. Oversized drafts stay in the current page.

## Verification

`test-web-workbench-browser.cjs` runs an embedded production build in disposable
Git repositories with the existing deterministic Codex peer. It covers routing,
conflicts, dialogs, keyboard menus, checkpoint scope/placement, themes, panel
combinations, narrow layouts, activity, input preservation, stopping, and restart.
The separate runtime, recovery, and checkpoint browser suites preserve their
execution and evidence scenarios. Go storage/application tests cover atomic claims,
concurrency, project isolation, captured intent, and checkpoint generations.
`test-web-upgrade-browser.cjs` builds two distinct embedded UIs and restarts them
on the same origin and disposable database, testing existing tabs, asset graphs,
draft reloads, stale conflicts, and the absence of automatic execution. It also
checks planning drafts, exact retry identities, fresh CSRF, and focus/scroll
restoration after the planning panel loads. The core
and Workbench browser runners also support `PLAYWRIGHT_BROWSER=webkit`; filter
checks cover intrinsic panel height, nested controls, and short-screen scrolling.

`test-web-planning-browser.cjs` uses the real production planning endpoint and
SQLite with the deterministic Codex peer. It verifies actual catalog discovery,
model responses, draft editing and selective creation, split/combine/refinement,
project pinning, lost-response retries, panel/tab state, and mobile keyboard
access. It runs with Chrome or WebKit. Planning has its own bounded reload
handoff; ordinary draft autosave can continue after restoration, but interrupted
Send, Create, and other ambiguous requests require explicit retry with their
original request identity. No restored planning conversation starts execution.

The composer shows Working folder alongside Access, with the selected full path
beneath. A missing folder selection has an inline error and error border; a project
without an available checkout shows an explanation instead of an empty selector.
Planning progress appears below the messages with an animated busy indicator.
Submission errors remain immediately above the composer with retry/recovery controls
and preserve the message. Reduced-motion
preferences disable the animation. Execution start and resume forms offer the same
Access dropdown; the browser remembers it per project/workspace. A schedule captures
the selected mode for its runs, and Run details displays the policy used. Existing
runs keep their captured policy. Internal checkpoint reviewers remain read-only.

The panel is a continuous scrolling conversation with a proposal tray docked above
the composer. Progress appears inside the conversation only while a request runs;
there is no permanent idle status box. The tray shows only uncreated proposals,
with wrapping titles, expandable details, selection, and a direct × Dismiss action.
Its header reports the proposal count and can collapse the tray; new proposals
expand it again. Dismiss all and Create selected controls stay with the tray.
Dismissal autosaves without an Undo notice. Created pellets leave the tray and
appear as linked confirmations in the conversation; empty trays disappear.
