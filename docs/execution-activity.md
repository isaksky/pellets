# Browser execution activity

The execution sidebar presents activity actually reported by the selected run's
Codex process. It does not read workspace files to reconstruct an earlier read,
infer checks from a successful turn, or treat activity as execution evidence.
Durable run state, optimistic interaction revisions, review snapshots, validated
Git evidence and explicit recovery remain authoritative. Server restart never
resumes a run, and it clears detailed activity.

The sticky **Current execution** summary sits beside the scrolling feed. Its
text and working indicator come from authoritative run/workspace records, never
from an activity item's completion. Working includes startup, gaps between
operations and the wait for complete sanitized snapshots. Only a working
implementation/review can display a reported active operation. A completed
operation or turn clears that operation description without ending the run.
Turn boundaries use activity kind `turn`, separate from plan `progress`.

Input/approval waits, stopping, interruption, attention and finished states have
no animated working indicator. The foreground schedule's stop-now receipt
remains visible through interruption and cleanup; a pending durable
`turn/interrupt` also presents Stopping. Stop-after is a separate instruction:
the current pellet can still be working or waiting for the user. An unowned
active record presents recovery attention, not live work.

Known execution phases have plain-language descriptions, such as checking the
workspace, working on the pellet, checking the implementation result and saving
verified changes. Detailed diagnostics remain under Run details. Commentary is
**Agent update** and a final message is **Agent response**; neither carries a
run-completion label. Both appear expanded as readable prose in chronological
position, while commands and file operations remain compact native disclosures.
Messages use the shared local Markdown renderer for paragraphs, inline code,
lists, emphasis, safe links and highlighted code fences. Raw HTML stays literal,
unsafe link schemes stay inert, and images become alt text without fetching
remote assets. Long paths wrap; code blocks scroll horizontally with keyboard
access. A pending message explicitly waits for its complete snapshot, and an
empty completed message says that no text was reported. Individual commands and
file operations keep their own reported status. Disconnection clears the active-operation description and
shows a reconnect notice while retaining the last authoritative execution state.
Unavailable/reset snapshots clear stale history; truncated history keeps its
visible notice. The routine projection explanation is available under **About
reported activity**; connection and history warnings remain outside that
disclosure. Neither condition means the execution finished.

Adjacent commands, file reads, and file changes of the same kind share a native
expandable summary once there are two events. Counts are **reported operations**
(stable event IDs), not successful commands or unique files. File summaries also
count distinct, nonempty **reported path strings** within that group: three edits
to the same path are `3 file changes · 1 reported path`. Paths are not resolved,
case-folded, or inferred from commands; missing paths add no path count. A file
change operation here is one projected file event, not an entire multi-file patch.
Counts cover retained members only; the feed header still counts individual
events, including those beneath closed groups.

Collapsed groups show in-progress, failed, declined and reported-only counts,
plus the last active operation in first-observed order or the latest operation
when none is active. These are reported item outcomes, never authoritative run
state. Full commands, paths and existing detail views remain with every child.
Messages, questions, approvals, turn boundaries, other kinds and action-required
or unknown tool statuses break groups and stay independent. Separate activity
URLs (project/run attempts) never share groups. Same-ID completions replace a
member in place without adding counts or moving it past intervening commentary.

Both group and child disclosures retain their choices on append and snapshot
replay. Promoting an open/focused single event keeps it visible. Groups keep
their identity through prefix eviction while any compatible member survives;
unavailable history removes stale groups. Native summary controls support Tab,
Enter and Space; the existing focus treatment is retained. Reconciliation keeps
retained DOM nodes, selection, focus and visible child scroll anchors. Truncation
and reconnect notices remain outside all groups. Grouping does not change the
server projection, sanitization or retention bounds, and the browser still caps
individual events at 128 rather than counting group wrappers toward that limit.

Only the state label is a polite live region. Operation changes and feed updates
do not repeatedly announce historical content. Reduced motion disables the
working animation, retaining the indicator and explicit text.

The implementation, checkpoint review and checkpoint triage drivers retain their
single ordered event consumers. Each also projects recognized notifications after
it knows the exact thread and turn identities. The projection never reads from
the protocol or driver channel. Unknown methods, unrelated thread/turn events,
reasoning text, arbitrary tool arguments/results and authentication events are
omitted. Supported activity includes agent messages, command execution and exit
codes, command-reported reads, explicit source if reported, individual file
changes and their diffs, turn diffs, plan progress, turn completion, questions and
approval review status. Existing revision-checked controls handle answers,
approvals, steering, stopping and explicit Resume.

Item started/completed events replace the same item snapshot. Complete messages,
source, diffs and command output are sanitized before truncation. Partial text
and output deltas are intentionally withheld: a credential prefix and value can
arrive in separate protocol messages, making independent delta redaction unsafe.
Read command output is labeled as output, not reconstructed source. Missing
source, diffs, command output or exit codes stay missing. The browser explains
this limitation; it does not invent a transcript to match the prototype.

Individual tool rows and group previews use the same deterministic summary of
the sanitized fields. Commands show a whitespace-normalized command excerpt;
read/change rows show the reported path relative to the selected workspace root
when an exact lexical prefix establishes that relationship. No files, symlinks
or historical source are read to construct a summary. Prefix lookalikes,
`..` segments, redacted paths and unavailable roots are not made relative.
Remaining absolute paths are explicitly labeled **Absolute path**. Directory
segments are retained to distinguish duplicate basenames; long duplicates prefer
a distinguishing suffix marked `…/`. Very long paths keep their beginning and
end. Previews wrap to two lines, and expanded evidence retains the complete safe
path, original command whitespace, reported output, source and diff. **Turn
changes** labels a reported diff (or excerpt), without implying verified changes.

Failed/declined operations show their operation, reported exit code or its
absence, and **Impact unknown**. An explicit tool error message is summarized
and retained in full; arbitrary output is never interpreted as an error or
recovery signal. Completed operations with a nonzero exit are still failures.
Groups keep failure counts and a separate last-failure summary even when a newer
operation runs or succeeds. Failure impact and counts are not line-clamped.
Nothing correlates separate successes with earlier failures or changes the
authoritative run state from this reported activity.

Exact-turn runtime `error` notifications expose only the sanitized message and
explicit `willRetry` flag: **Retry reported**, **No retry planned (reported)**,
or unknown impact if the flag is missing. These historical reports stay separate
from commands, since they have no command identity. Each received notification,
including an identical repeated message, retains a distinct ID and its
chronological position. Snapshot replay preserves those IDs, so errors continue
to separate command groups across reconnects. The protocol provides no
operation-level recovery relationship; agent statements about recovery remain
clearly labeled, expanded **Agent update** prose and do not resolve failed rows.
A failed turn says that the turn ended with an error and directs the reader to
Current execution. Input/approval requests stay outside groups and point to the
run's existing revision-checked controls. Actual waiting, attention and recovery
actions continue to come from authoritative records; stale reported requests
cannot create an answer control or authorize a retry.

Only selected presentation fields can enter the projection. Credential patterns,
Authorization headers, known token formats, command credential arguments,
private keys, ANSI escapes, control bytes and bidi controls are removed. File
contents for known credential paths (including `.env` variants, `auth.json`,
private-key files and credential stores) are withheld. Raw requests, environment
maps, server response payloads and human answers never enter this store. This is
bounded local operational display, not a facility for publishing arbitrary
process output or guaranteeing that every possible secret format can be detected.

Retention is in foreground-process memory only: at most 32 runs, 128 items and
512 KiB per run, 24 KiB per item (12 KiB per field), and 4 MiB total. Raw events
larger than 1 MiB are not projected. Retention evicts the least recently updated
run or oldest items and explicitly marks unavailable or truncated history.
There is no transcript table, SQLite trigger, filesystem watcher or additional
retention/purge operation. Browser closure does not affect execution lifetime.

`GET /projects/:code/runs/:id/activity?after=:cursor` returns JSON containing
`run_id`, `cursor`, `available`, `truncated`, `reset`, `items` and an explanatory
`message`. The handler resolves the project and checks the run against durable
evidence before exposing activity, including for old attempts. Database path and
run ID are combined internally because IDs are local to a database. Item IDs are
opaque stable hashes, not protocol IDs or filesystem paths.

Each item has `id`, `sequence`, `kind`, `status`, `title`, `timestamp` (the local
observation time) and optional `text`, `path`, `source`, `diff`, `command`,
`output`, `error`, `exit_code` and `truncated`. `error` contains only an explicit
reported tool/runtime error message, subject to the same field, item and memory
bounds as other text. Items are complete sanitized replacements;
an omitted optional field is unavailable. A zero cursor requests the retained
snapshot. Otherwise only changed items are returned. `reset` instructs the
browser to discard stale items after truncation, unavailable history or an
out-of-range cursor; stable retained IDs allow expansion state to survive.

Adding `stream=1` opens an independent SSE stream with `pellets-activity` events,
the same JSON payload and an SSE ID equal to the cursor. `Last-Event-ID` supports
reconnection. Subscription precedes the initial snapshot to avoid a missed update.
At most 128 streams can subscribe per foreground process. Each has a capacity-one
coalesced wakeup channel; no network I/O occurs under the projection lock. Slow
clients cannot block or steal driver events. Every network write has a two-second
deadline, keepalives run every 15 seconds, and server shutdown closes streams.

Activity updates target only the activity container. They are independent of the
Datastar database-invalidation stream and must preserve dialog drafts, pending
answers, active focus, expansions and scroll position. The ordinary database
monitor continues to refresh authoritative records and execution controls.
Unchanged rendered content retains its DOM nodes, including on snapshot replay
and status-only changes, preserving text selection, focused links and code-block
scroll. Changed content is replaced only in its own event. Event order follows
first observation, not the latest revision cursor. Readers away from the bottom
keep a retained visible event as their scroll anchor, including when earlier
content changes height or is evicted. Selection or focus inside the feed also
suppresses automatic following; readers at the end otherwise follow new events.

`web-activity-summary-contract.cjs`, included in the execution-state browser
runner, verifies command outcomes, missing evidence, relative/long/duplicate and
outside-workspace paths, redacted values, explicit retry and recovery reports,
unknown failure impact, failed turns and independent input requests. It checks
collapsed summaries and expanded evidence in Chromium/WebKit across all five
themes at desktop, intermediate and phone widths. The containing runner retains
its short-window, keyboard, live-update, wait/stop and explicit-recovery checks.

The `schedule_activity_errors_gate` case in `test-web-runtime-browser.cjs` uses
gated protocol notifications to check runtime-error chronology, identical
repeated errors and command completion through live updates, native SSE
reconnection and full snapshot replay in Chromium/WebKit. Select it with
`PELLETS_RUNTIME_BROWSER_CASE=schedule_activity_errors_gate`.
