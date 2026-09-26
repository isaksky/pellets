# Drag-and-drop queue ordering

Status: proposed implementation plan, 2026-09-26. No feature code changed.

## Product decisions

Confirmed with the user:

- Move one pellet at a time within the current queue.
- Persist the move by changing priority, affecting subsequent work selection.
- Support filtered queues. A drop before a visible pellet places the moved
  pellet directly before that pellet in the full project queue.
- Include active review checkpoints as draggable rows. Their explicit review
  membership stays unchanged by movement.

Other implementation defaults:

- Enable dragging when the display sort is priority ascending, including group,
  search, status, and workspace filters. For other sorts, show a quiet explanation
  beside the existing **Queue order** action; switching preserves filters.
- Open and in-progress records with a priority can move. Closed and maybe-later
  records cannot move or serve as drop anchors. Moving in-progress work retains
  its owner and does not interrupt execution. Subsequent selection still respects
  existing resume, routing, and checkpoint-readiness rules.
- This operates on the shared project priority order, including when browsing a
  workspace's results. Group membership and workspace assignments are separate
  operations.

## Interaction

Give each eligible row a compact, consistently placed drag handle. Reserve its
space so hover does not shift the title. Preserve ordinary row height, title
ellipsis, status, independent links, actions, and selection controls. Review rows
use the same handle beside their heading; their expanded contents
remain independently interactive.

Use a feature-owned Pointer Events controller, following the app's existing
pointer-capture approach in `assets/sidebar-resize.js`. No new drag library is
expected. Start only from the handle after a small movement threshold. Apply
touch gesture suppression only to the handle so the rest of the queue scrolls
normally. Support mouse, touch, and pen.

During dragging, show a lightweight title preview and insertion marker, with a
clear destination such as **Before project-42**. Scroll the queue pane near its
edges. Hit testing uses actual row geometry, including expanded reviews. Do not
transform production row ancestors: row menus use fixed positioning.

Define every insertion gap deterministically: insert immediately before the next
visible eligible row; the final gap inserts immediately after the last visible
eligible row. Hidden records keep their relative order to one another. For
example, with full order `A, hidden-X, B, C`, dragging C into the visible gap
before B produces `A, hidden-X, C, B`. The destination label makes this explicit.

Escape, pointer cancellation, loss of capture, leaving the page, or release
outside a valid queue target cancels an unfinished gesture. Canceled and untouched
gestures send no request. Dropping submits one move; further gestures wait for
its result. Show pending feedback and render the confirmed server order on
success, retaining scroll position and focus on the moved row. Dragging does not
open the record dialog or change the current route.

Provide keyboard operation on the handle: Space/Enter picks up, Up/Down changes
the insertion slot, Space/Enter commits, and Escape cancels. Announce pickup,
destination, completion, and errors in a small live region. Retain the existing
Move up/down menu actions and the dialog's explicit Before/After controls.
Queue-relative menu actions should share the same sort eligibility and move
submission behavior as dragging; the dialog's explicit target controls remain
usable independently of the display sort.

## Existing implementation to reuse

- [Queue and pellet markup](../internal/webui/templates/main.html) and
  [review markup](../internal/webui/templates/checkpoint_row.html) already expose
  row identity, row version, and priority.
- [Workbench row actions](../internal/webui/assets/workbench.js) already submit
  `version`, `target`, and `direction` to the pellet move endpoint.
- [Move handler](../internal/webui/mutations.go) and
  [application service](../internal/app/web.go) already connect browser moves to
  transactional storage.
- [SQLite movement](../internal/storage/sqlite/pellets.go) checks source versions
  under `BEGIN IMMEDIATE`, calculates a sparse integer priority, and rebalances
  atomically when necessary. A schema migration is not expected.
- [Datastar request handling](../internal/webui/assets/app.js) already manages
  foreground mutations, refreshes, feedback, and protection for active controls.

## Required integration work

1. **Make queue movement stay in the queue.** Add a queue-move submission mode
   to the existing request flow and endpoint. Today, the generic move response
   renders the moved pellet's inspector. Queue moves need a list-oriented success
   response and queue-local errors that preserve filters, workspace context,
   selected record, drafts, review selection, expanded details, scroll, and URL.
   Keep existing dialog and CLI callers working.
2. **Validate the destination snapshot.** Queue-mode moves submit the source
   version captured at pickup and the target version from the displayed queue.
   Extend the web move input to check both inside the same write transaction,
   before priority allocation. Existing code checks only the source version.
   Reject stale, missing, or inactive participants without a partial write.
   Unrelated changes elsewhere need not invalidate an anchored move.
3. **Add the gesture controller.** Put feature behavior in a small
   `internal/webui/assets/queue-reorder.js` module. Templates expose explicit
   eligibility and request context. Scope handle and marker styles to queue rows
   in `workbench.css`; reuse existing theme tokens.
4. **Protect live updates.** Extend the existing refresh protection so incoming
   queue patches, including already-started responses, cannot replace rows
   during a gesture. Continue independent execution updates. Release the guard
   on drop/cancel and request a fresh authoritative queue. Navigation or UI
   version changes cancel the gesture and clean up listeners, capture, preview,
   and scrolling. A pending move uses the existing foreground mutation lock.
5. **Handle conflicts and uncertain responses.** Show a concise queue-local
   message and refresh authoritative state. Do not silently rebase a stale drop
   or automatically repeat a mutation after a lost response. A failed response
   can follow a successful write, so reconcile before allowing another move.

## Verification and delivery

Before editing UI code, reproduce the production queue in an independent
temporary Git repository with its own explicitly initialized database. Capture
focused baseline evidence under
`artifacts/ui-review/queue-drag-and-drop/<attempt>/`, then retain matched after
captures and a reproduction index according to [UI guidance](ui-agent-guidance.md).

Add focused Go tests for target-version conflicts, atomic rejection, active-state
validation, unchanged ownership/review scope, and queue-mode responses. Reuse
existing priority allocation and rebalance tests; verify reordered open work is
selected according to existing scheduler rules.

Add a dedicated Chromium browser suite covering both directions, first/last
positions, filtered gaps with hidden records, workspace views, disallowed sorts
and statuses, no-op/cancel, keyboard and touch, autoscroll, expanded reviews,
persistence after reload, pending/error states, lost responses, and
concurrent changes during dragging. Assert that no dialog opens and drafts,
review selection, focus, and browsing context survive.

Inspect real-app screenshots across all five themes at desktop, intermediate,
and phone widths, including narrow panes with sidebars visible. Run the affected
Workbench, review-row, interaction, live-menu, and parity suites; use gallery
checks for the changed presentation and document only intended handle geometry
differences. Update [Workbench behavior](workbench-ui.md),
[component adoption notes](web-components.md), and relevant gallery examples.

Suggested implementation order: queue move response and concurrency contract;
drag/keyboard interaction and refresh integration; browser coverage, visual
verification, and reference updates.
