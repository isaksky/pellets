# Checkpoints in the Workbench queue

Reviews are full queue rows with an independent title/editor link, native
**Review · N pellets** scope disclosure, explicit activity/result label and the
standard actions menu. Titles retain the saved text verbatim. The default is
**Review selected changes**; follow-up creation belongs in supporting copy and
outcomes, not a required title suffix.

The disclosure lists every explicit selected member by title, reference and
current lifecycle status. It explains readiness failures and marks members not
shown by the current filters, including workspace routing. A missing record
retains its selected title/reference and has no replacement link. Scope is an
explicit set, including overlaps and noncontiguous members. Queue order and
filters never change it. There are no connecting brackets, scope lanes or layout
indentation. Optional focus/hover highlighting identifies only exact visible
members. Large scopes scroll in a keyboard-focusable panel bounded to 420px or
55vh; expanding does not duplicate, nest or move actual queue records.

Row labels separate **Ready**, **Evidence missing**,
**Target unavailable**, **Waiting for N pellets** (unfinished targets), **Not running** (claimed
without execution), **Preparing review**, **Reviewing**, **Checking findings**,
**Finishing review**, and **Needs attention**. A persisted running attempt alone
is insufficient: the foreground supervisor must own the exact attempt and the
checkpoint must still be in progress. Interrupted/failed attempts and unfinished
triage cannot appear successful. **Reviewed with no issues** requires a completed
clean result; **Reviewed with N follow-ups** counts only permanent `valid`
creation receipts, not existing coverage, duplicates, invalid, stylistic or
already-fixed findings. A closed record without retained successful evidence is
**Closed without review**. Deferred and removed reviews remain visible in
**All states** and **Maybe later**, with their distinct lifecycle labels.

The row disclosure reuses the inspector’s durable outcome, follow-up links and
other finding dispositions. Each result names its generation and latest attempt;
earlier generations remain available through the review details/history link.
Reopening never inherits a prior generation’s success. Outcomes are loaded in
two queries per batch of up to 100 exact identities in one read transaction,
independent of the workspace’s recent-run limit. No review/triage write or model
call is performed while browsing.

The navigation count is the complete project’s active queue: ordinary pellets
**and** reviews in open or in-progress state. The footer repeats that count as
**N active in project**. **Queue results** (or **Workspace results**) reports the
filtered composition as **N pellets · M reviews**, with the state filter visible
beside it. Those results can include closed/deferred records when requested;
project totals ignore display filters. All three are server-rendered in the same
refresh bundle, never inferred from the currently visible DOM.

Native disclosures, focus, ordinary review selection, bounded-panel scroll and
editor drafts survive refreshes and display changes. Successful menu removal
closes the menu, focuses Undo, and refreshes the row and counts together. Scope
editing, reversible removal/restoration, historical evidence and the separate
review/triage execution policy keep their existing domain contracts.

## Historical implementation requirements

A review selects explicit pellet identities and reviews each target’s latest
completed successful implementation. Its title, description, revision, and
commits come from that execution. Later description or metadata edits do not
require **Update scope** or **Save scope**. Targets must still exist, be closed,
and retain successful implementation evidence. Selected membership and saved
review generations are not rewritten by readiness checks.

New review snapshots use version 2. Each `group_contexts` entry pairs a stable
project ID, target number, and exact successful implementation run ID with that
run's captured group context. The reviewer assesses the target's description and
that historical Markdown alongside its exact commit, paths, and committed
repository instructions. Different groups and revisions remain distinct, even
when they share a name. Current group documents, checkpoint membership,
scheduling filters, and queue adjacency never supply historical requirements or
expand the selected target set.

The dedicated reviewer and each independent finding assessor receive this same
snapshot. Assessors distinguish the original requirements from current code,
current repository instructions, and the active queue. The checkpoint's exact
group and external ID still determine follow-up inheritance; implementation
group context does not change it.

A captured empty document, explicit ungrouped admission, and a legacy execution
that predates context capture remain separate states. Version 1 review snapshots
also predate this association and resume unchanged without backfilling context.
Missing or corrupt required version 2 evidence requires attention. Group edits
cannot change retained source. Renames and later task metadata changes do not
block review of the completed implementation.

The clean-result digest includes the captured context. Migration 23 also binds
new triage receipts to a SHA-256 digest of the complete review snapshot. Explicit
Resume preserves that snapshot, completed assessments, and permanent finding
receipts, and retries only unfinished assessments. It never substitutes newer
requirements or creates duplicate follow-ups. Existing snapshot and transport
size limits remain enforced: oversized evidence requires attention and is never
silently truncated.

The workspace's Run details shows reviewed implementation context with each
target and implementation run. Captured Markdown is escaped, read-only source in
a disclosure. Identical snapshots share one displayed document with multiple
target/run labels; different revisions retain separate documents.

## Inserting a checkpoint

The browser can insert before or after an active queue record. It submits that
record's exact row version and the exact versions of every selected ordinary
target. Creation verifies project isolation, target kind, selected versions and
the insertion anchor while holding `BEGIN IMMEDIATE`, then allocates the number,
captures scope, inserts the record, and rebalances queue priorities if needed.
A changed anchor or target conflicts without creating a checkpoint. Stable add
request IDs retain the existing replay guarantees for a lost response.

Omitting placement keeps the existing automatic placement after the last active
selected target, or the queue tail when every target is inactive. The CLI keeps
its existing automatic checkpoint placement and exact-group semantics.

## Editing scope and retaining history

Only an open, unowned checkpoint can change scope. The editor shows all ordinary
project records, including closed work, regardless of current browsing filters.
Saving requires the checkpoint's exact full-row version and the current version
of every chosen target. An empty scope, a foreign project, a duplicate target,
or another checkpoint is rejected atomically.

A successful save starts a new checkpoint implementation generation. It first
stores the superseded materialized scope and evidence in the immutable
`review_checkpoint_scope_history` table, then captures the selected current
target metadata in `review_checkpoint_targets`. This deliberate recapture also
allows the user to accept revised implementation scope. Changing the scope
does not move the checkpoint or alter other checkpoints that overlap it.

Existing execution `checkpoint_scope_json`, review snapshots, outcomes, findings,
and triage receipts are never rewritten. A previous attempt cannot resume or
complete against the new generation. A fresh run must explicitly capture the
new scope and satisfy normal readiness checks. The dialog lists earlier scope
generations and their separate review outcomes; a reopened completed checkpoint
retains its prior execution snapshot and result in this history.

## Removing and restoring

Remove is a reversible defer for an open, unowned checkpoint. It records the
neighboring active identities and original priority, preserves the old scope,
then changes the status to `maybe_later`. It clears active priority and never
sets a completion time or records a successful review. Owned checkpoints must
be released first. Completed reviews must be deliberately reopened before
removal; removal does not alter completed review history.

The checkpoint stays inspectable and appears in deferred work. Its dialog
offers Undo removal / Restore checkpoint. Restore requires its current exact
row version, reopens it, and allocates priority before the surviving original
next neighbor, otherwise after the surviving previous neighbor. If both
neighbors have left the active queue, it uses the first remaining position at
or after its old priority, otherwise the queue tail. Restoration and any
rebalance share one writer transaction. Concurrent edits make stale Undo fail
without overwriting the edit.

These operations advance normal lifecycle generations, so they cannot silently
reuse a captured execution intent. A deliberate CLI `reopen` clears the removal
marker and preserves the CLI's existing queue-tail behavior. Removal is never
mapped to destructive purge. The prototype deletes an array entry; production
retains the domain record and evidence instead.

## Storage and HTTP boundaries

Migration 17 adds `review_checkpoint_scope_history` and
`review_checkpoint_removals`; existing target and execution tables retain their
contract. The optional `storage.CheckpointWriter` capability exposes versioned
scope/remove/restore operations independently of generic lifecycle operations.

The browser posts to `/projects/CODE/checkpoints/REF/scope`, `/remove`, and
`/restore`, through the shared method, origin, bounded-body, and CSRF checks.
Scope targets are explicit `REF:ROW_VERSION` values. All authoritative mutation
checks run again inside the database writer transaction; UI availability is
only presentation.

## Presentation verification

`scripts/test-web-review-rows-browser.cjs` exercises the production queue with
five adjacent overlapping reviews, noncontiguous/hidden/deferred/purged members,
large scopes, native keyboard controls, sorting, live drafts/selection/focus/scroll,
and shared lifecycle counts. It captures all five themes at 1280, 1092, 800 and
390px with both sidebars available. Run it in Chromium and again with
`PLAYWRIGHT_BROWSER=webkit`. The checkpoint browser suite uses the deterministic
protocol peer to verify ready/idle/running, interrupted triage, Resume, clean and
finding outcomes, and reopened generations. Neither suite uses a live model.
The gallery reuses the production row with local-only fixture actions. The parity
suite checks the exact intentional removal of the bracket gutter and displacement
from taller review rows while continuing to compare all original controls.
