# Checkpoints in the Workbench queue

Review dividers are queue records with explicit target identities. Their visual
brackets come from those identities, including overlapping and noncontiguous
scopes. Neither queue position nor browsing filters change review scope.
Ordinary implementation readiness, lifecycle generations, verified evidence,
and the separate checkpoint review/triage execution policy remain authoritative.
The queue's Closed label describes lifecycle status; the dialog's review outcome
separately records whether a review succeeded and its findings.

## Historical implementation requirements

New review snapshots use version 2. Each `group_contexts` entry pairs a stable
project ID, target number, and exact successful implementation run ID with that
run's captured group context. The reviewer assesses the target's description and
that historical Markdown alongside its exact commit, paths, and committed
repository instructions. Different groups and revisions remain distinct, even
when they share a name. Current group documents, checkpoint membership,
scheduling filters, and queue adjacency never supply historical requirements or
expand the selected target set.

The detached reviewer and each independent finding assessor receive this same
snapshot. Assessors distinguish the original requirements from current code,
current repository instructions, and the active queue. The checkpoint's exact
group and external ID still determine follow-up inheritance; implementation
group context does not change it.

A captured empty document, explicit ungrouped admission, and a legacy execution
that predates context capture remain separate states. Version 1 review snapshots
also predate this association and resume unchanged without backfilling context.
Missing or corrupt required version 2 evidence requires attention. Group edits
cannot change retained source; renames and other membership changes still obey
the existing scope, generation, and readiness checks.

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
