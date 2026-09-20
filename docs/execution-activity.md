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
`output`, `exit_code` and `truncated`. Items are complete sanitized replacements;
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
