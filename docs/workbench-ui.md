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
click or Escape. Unsaved edits retain the discard guard. They support optimistic conflicts,
lifecycle operations, recovery, provenance, and explicit human memory approval.
An owned pellet and a running process remain distinct.

Both sidebars collapse independently using the bottom corner buttons. The theme
selector immediately precedes the execution toggle and offers Gruvbox Light,
Gruvbox Dark, Light, Dark, and Icy. Theme and panel preferences are localStorage
presentation values applied before first paint. Theme updates do not navigate,
rebuild execution, or discard input. Minor changes darken secondary light-theme
text from the prototype to retain readable contrast.

At phone widths, navigation becomes compact horizontal rows and execution sits
below the queue. Both stay independently collapsible. The status bar remains
pinned, and the breadcrumb continues to navigate when the left sidebar is hidden.

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

## Verification

`test-web-workbench-browser.cjs` runs an embedded production build in disposable
Git repositories with the existing deterministic Codex peer. It covers routing,
conflicts, dialogs, keyboard menus, checkpoint scope/placement, themes, panel
combinations, narrow layouts, activity, input preservation, stopping, and restart.
The separate runtime, recovery, and checkpoint browser suites preserve their
execution and evidence scenarios. Go storage/application tests cover atomic claims,
concurrency, project isolation, captured intent, and checkpoint generations.
