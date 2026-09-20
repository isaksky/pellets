package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"pellets/internal/codex"
)

// Activity is a disposable browser projection, never execution or review
// evidence. Limits include idle and completed runs and disconnected browsers.
const (
	activityMaxRuns        = 32
	activityMaxItems       = 128
	activityMaxRunBytes    = 512 << 10
	activityMaxTotalBytes  = 4 << 20
	activityMaxItemBytes   = 24 << 10
	activityMaxFieldBytes  = 12 << 10
	activityMaxEventBytes  = 1 << 20
	activityMaxSubscribers = 128
)

type ActivityItem struct {
	ID        string    `json:"id"`
	Sequence  uint64    `json:"sequence"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Title     string    `json:"title"`
	Text      string    `json:"text,omitempty"`
	Path      string    `json:"path,omitempty"`
	Source    string    `json:"source,omitempty"`
	Diff      string    `json:"diff,omitempty"`
	Command   string    `json:"command,omitempty"`
	Output    string    `json:"output,omitempty"`
	Error     string    `json:"error,omitempty"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Truncated bool      `json:"truncated,omitempty"`
}

type ActivitySnapshot struct {
	RunID     int64          `json:"run_id"`
	Cursor    uint64         `json:"cursor"`
	Available bool           `json:"available"`
	Truncated bool           `json:"truncated"`
	Reset     bool           `json:"reset"`
	Items     []ActivityItem `json:"items"`
	Message   string         `json:"message"`
}

type activityRun struct {
	items                        []ActivityItem
	cursor, lostThrough, touched uint64
	bytes                        int
	truncated                    bool
}

type activityProjection struct {
	mu             sync.Mutex
	runs           map[activeExecutionKey]*activityRun
	subscribers    map[activeExecutionKey]map[chan struct{}]struct{}
	bytes, clients int
	clock          uint64
}

func newActivityProjection() *activityProjection {
	return &activityProjection{runs: make(map[activeExecutionKey]*activityRun), subscribers: make(map[activeExecutionKey]map[chan struct{}]struct{})}
}

func (p *activityProjection) begin(key activeExecutionKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runs[key] == nil {
		p.clock++
		p.runs[key] = &activityRun{touched: p.clock}
	}
	p.evict(key)
}

func activityItemBytes(item ActivityItem) int {
	return 256 + len(item.ID) + len(item.Kind) + len(item.Status) + len(item.Title) + len(item.Text) + len(item.Path) + len(item.Source) + len(item.Diff) + len(item.Command) + len(item.Output) + len(item.Error)
}

func (p *activityProjection) publish(key activeExecutionKey, item ActivityItem) {
	p.mu.Lock()
	defer p.mu.Unlock()
	run := p.runs[key]
	if run == nil {
		return
	} // Eviction must not resurrect a partial history.
	p.clock++
	run.touched = p.clock
	run.cursor++
	item.Sequence = run.cursor
	item.Timestamp = time.Now().UTC()
	size := activityItemBytes(item)
	if size > activityMaxItemBytes {
		return
	} // Projectors budget fields before publishing.
	index := -1
	for i := range run.items {
		if run.items[i].ID == item.ID {
			index = i
			break
		}
	}
	if index >= 0 {
		old := activityItemBytes(run.items[index])
		run.bytes -= old
		p.bytes -= old
		run.items[index] = item
	} else {
		run.items = append(run.items, item)
	}
	run.bytes += size
	p.bytes += size
	for len(run.items) > activityMaxItems || run.bytes > activityMaxRunBytes {
		removed := run.items[0]
		run.items = run.items[1:]
		size := activityItemBytes(removed)
		run.bytes -= size
		p.bytes -= size
		run.lostThrough = run.cursor
		run.truncated = true
	}
	p.evict(key)
	p.signal(key)
}

func (p *activityProjection) evict(prefer activeExecutionKey) {
	for len(p.runs) > activityMaxRuns || p.bytes > activityMaxTotalBytes {
		var oldest activeExecutionKey
		var tick uint64 = ^uint64(0)
		for key, run := range p.runs {
			if key != prefer && run.touched < tick {
				oldest = key
				tick = run.touched
			}
		}
		if tick == ^uint64(0) {
			oldest = prefer
		}
		p.bytes -= p.runs[oldest].bytes
		delete(p.runs, oldest)
		p.signal(oldest)
	}
}

func (p *activityProjection) signal(key activeExecutionKey) {
	for ch := range p.subscribers[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (p *activityProjection) snapshot(key activeExecutionKey, after uint64) ActivitySnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := ActivitySnapshot{RunID: key.runID, Items: []ActivityItem{}, Reset: after == 0, Message: "Only reported activity is shown. Text and command output appear when complete; sensitive values and oversized details are omitted."}
	run := p.runs[key]
	if run == nil {
		result.Reset = true
		result.Message = "Detailed activity is unavailable for this attempt. It is retained only in bounded memory in the foreground server; durable execution and review evidence remain available."
		return result
	}
	result.Available = true
	result.Cursor = run.cursor
	result.Truncated = run.truncated
	result.Reset = result.Reset || after < run.lostThrough || after > run.cursor
	for _, item := range run.items {
		if result.Reset || item.Sequence > after {
			result.Items = append(result.Items, item)
		}
	}
	return result
}

func (p *activityProjection) subscribe(key activeExecutionKey) (<-chan struct{}, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients >= activityMaxSubscribers {
		return nil, nil, scheduleError("activity_subscribers_full", "too many browser activity streams are open")
	}
	ch := make(chan struct{}, 1)
	if p.subscribers[key] == nil {
		p.subscribers[key] = make(map[chan struct{}]struct{})
	}
	p.subscribers[key][ch] = struct{}{}
	p.clients++
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			delete(p.subscribers[key], ch)
			p.clients--
			if len(p.subscribers[key]) == 0 {
				delete(p.subscribers, key)
			}
		})
	}, nil
}

// SnapshotActivity is read only. Its caller must authorize the run with durable
// project evidence before returning this process-local projection to a browser.
func (s *ExecutionSupervisor) SnapshotActivity(database Database, runID int64, after uint64) ActivitySnapshot {
	return s.activity.snapshot(activeExecutionKey{databasePath: database.Path, runID: runID}, after)
}
func (s *ExecutionSupervisor) SubscribeActivity(database Database, runID int64) (<-chan struct{}, func(), error) {
	return s.activity.subscribe(activeExecutionKey{databasePath: database.Path, runID: runID})
}

// ProjectActivity is called by the existing single driver consumer after it has
// the exact recorded turn identity. It never consumes Events or writes storage.
// Browsers receive capacity-one wakeups; their reads and network writes happen
// outside this bounded projection lock and cannot backpressure the driver.
func (e *WorkspaceExecution) ProjectActivity(event codex.Event, threadID, turnID string) {
	if e.activity == nil || threadID == "" || turnID == "" || len(event.Params) > activityMaxEventBytes {
		return
	}
	for _, item := range projectCodexActivity(event, threadID, turnID) {
		e.activity.publish(activeExecutionKey{databasePath: e.database.Path, runID: e.id}, item)
	}
}

type activityEvent struct {
	ThreadID    string                          `json:"threadId"`
	TurnID      string                          `json:"turnId"`
	ItemID      string                          `json:"itemId"`
	Reason      string                          `json:"reason"`
	Explanation string                          `json:"explanation"`
	Diff        string                          `json:"diff"`
	WillRetry   *bool                           `json:"willRetry"`
	Error       *struct{ Message string }       `json:"error"`
	Plan        []struct{ Step, Status string } `json:"plan"`
	Turn        struct {
		ID, Status string
		Error      *struct{ Message string }
	} `json:"turn"`
	Item struct {
		ID, Type, Status, Text, Phase, Command, AggregatedOutput, Path, Content, Source, Tool string
		ExitCode                                                                              *int `json:"exitCode"`
		Error                                                                                 *struct{ Message string }
		Changes                                                                               []struct {
			Path, Diff string
			Kind       json.RawMessage
		} `json:"changes"`
		CommandActions []struct{ Type, Path, Name string } `json:"commandActions"`
	} `json:"item"`
}

func projectCodexActivity(event codex.Event, threadID, turnID string) []ActivityItem {
	var payload activityEvent
	if json.Unmarshal(event.Params, &payload) != nil || payload.ThreadID != threadID {
		return nil
	}
	scopedTurn := payload.TurnID
	if event.Method == "turn/completed" {
		scopedTurn = payload.Turn.ID
	}
	if scopedTurn != turnID {
		return nil
	}
	item := ActivityItem{Status: "reported"}
	rawID := payload.Item.ID
	if rawID == "" {
		rawID = payload.ItemID
	}
	if rawID == "" {
		rawID = event.Method + ":" + payload.Item.Type
	}
	key := threadID + "\x00" + turnID + "\x00" + rawID
	item.ID = activityID(key)
	switch event.Method {
	case "item/started", "item/completed":
		item.Status = "running"
		if event.Method == "item/completed" {
			item.Status = "completed"
		}
		switch payload.Item.Type {
		case "agentMessage":
			if event.Method != "item/completed" || payload.Item.Text == "" {
				return nil
			}
			item.Kind = "message"
			item.Title = "Agent update"
			if payload.Item.Phase == "final_answer" {
				item.Title = "Agent response"
			}
			item.Text = payload.Item.Text
		case "commandExecution":
			item.Kind = "command"
			item.Title = "Command"
			item.Command = payload.Item.Command
			if payload.Item.Status == "failed" || payload.Item.Status == "declined" {
				item.Status = payload.Item.Status
			}
			if event.Method == "item/completed" {
				item.Output = payload.Item.AggregatedOutput
				item.ExitCode = payload.Item.ExitCode
			}
			if payload.Item.ExitCode != nil && *payload.Item.ExitCode != 0 {
				item.Status = "failed"
			}
			if len(payload.Item.CommandActions) == 1 && payload.Item.CommandActions[0].Type == "read" {
				item.Kind = "file_read"
				item.Title = "Read file"
				item.Path = payload.Item.CommandActions[0].Path
				if item.Path == "" {
					item.Path = payload.Item.CommandActions[0].Name
				}
			}
		case "fileChange":
			var files []ActivityItem
			for i, change := range payload.Item.Changes {
				if i >= 32 {
					break
				}
				file := item
				file.ID = activityID(key + "\x00" + change.Path)
				file.Kind = "file_change"
				file.Title = "File change"
				file.Path = change.Path
				file.Diff = change.Diff
				if payload.Item.Status == "failed" || payload.Item.Status == "declined" {
					file.Status = payload.Item.Status
				}
				files = append(files, sanitizeActivityItem(file))
			}
			return files
		case "fileRead":
			item.Kind = "file_read"
			item.Title = "Read file"
			item.Path = payload.Item.Path
			if payload.Item.Status == "failed" {
				item.Status = "failed"
			}
			if event.Method == "item/completed" {
				item.Source = payload.Item.Source
				if item.Source == "" {
					item.Source = payload.Item.Content
				}
			}
		case "enteredReviewMode":
			item.Kind = "review"
			item.Title = "Review started"
		case "exitedReviewMode":
			item.Kind = "review"
			item.Title = "Review finished"
		case "mcpToolCall":
			item.Kind = "tool"
			item.Title = "Tool: " + payload.Item.Tool
			if payload.Item.Status == "failed" {
				item.Status = "failed"
			}
			if event.Method == "item/completed" && payload.Item.Error != nil {
				item.Error = payload.Item.Error.Message
			}
		default:
			return nil
		}
	case "turn/plan/updated":
		item.Kind = "progress"
		item.Title = "Plan"
		item.Text = payload.Explanation
		for i, step := range payload.Plan {
			if i >= 24 {
				break
			}
			item.Text += "\n" + step.Status + ": " + step.Step
		}
	case "error":
		// Runtime errors have exact turn scope and an explicit retry flag, but
		// no command identity. Never attach them to the nearest tool operation.
		item.Kind, item.Title, item.Status = "error", "Runtime error", "failed"
		if payload.Error != nil {
			item.Error = payload.Error.Message
		}
		if payload.WillRetry != nil {
			item.Status = "not_retrying"
			if *payload.WillRetry {
				item.Status = "retrying"
			}
		}
	case "turn/diff/updated":
		item.Kind = "diff"
		item.Title = "Reported turn diff"
		item.Diff = payload.Diff
	case "turn/completed":
		item.Kind = "turn"
		item.Title = "Turn " + payload.Turn.Status
		item.Status = payload.Turn.Status
		if payload.Turn.Error != nil {
			item.Text = payload.Turn.Error.Message
		}
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval":
		item.Kind = "approval"
		item.Status = "awaiting_input"
		item.Title = "Human approval requested"
		item.Text = payload.Reason
	case "item/tool/requestUserInput", "mcpServer/elicitation/request":
		item.Kind = "question"
		item.Status = "awaiting_input"
		item.Title = "Input requested"
	case "item/autoApprovalReview/started", "item/autoApprovalReview/completed", "autoApprovalReview/strictReviewRequired":
		item.Kind = "approval"
		item.Title = "Automatic approval review"
		item.Text = conciseCodexEvent(event, threadID, turnID)
	default:
		// Never expose partial deltas: a credential prefix/value can straddle protocol
		// messages. Complete item snapshots are independently sanitized instead.
		return nil
	}
	return []ActivityItem{sanitizeActivityItem(item)}
}

func activityID(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:12])
}

func sanitizeActivityItem(item ActivityItem) ActivityItem {
	if sensitiveActivityPath(item.Path) {
		item.Source, item.Diff, item.Output = "", "", ""
		item.Text = "Contents withheld for a credential file."
	}
	remaining := activityMaxItemBytes - 512
	for _, field := range []*string{&item.Kind, &item.Status, &item.Title, &item.Path, &item.Command, &item.Error, &item.Text, &item.Source, &item.Diff, &item.Output} {
		limit := min(activityMaxFieldBytes, remaining)
		value, truncated := sanitizeActivityText(*field, limit)
		*field = value
		remaining -= len(value)
		item.Truncated = item.Truncated || truncated
	}
	return item
}

// Preserve source/diff newlines, but apply credential redaction before any
// truncation. No suffix or partial delta is ever retained after losing its
// credential prefix. ANSI, bidi overrides and other control bytes are removed.
func sanitizeActivityText(value string, limit int) (string, bool) {
	text := diagnosticANSI.ReplaceAllString(strings.ToValidUTF8(value, "�"), "")
	text = activityLabeledCredential.ReplaceAllString(text, "[redacted]")
	text = diagnosticSecret.ReplaceAllString(text, "[redacted]")
	text = activityPrivateKey.ReplaceAllString(text, "[redacted private key]")
	text = activityCredentialFlag.ReplaceAllString(text, "[redacted]")
	text = activityKnownToken.ReplaceAllString(text, "[redacted]")
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
	if len(text) <= limit {
		return text, false
	}
	if limit < 32 {
		return "", true
	}
	text = text[:limit-24]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text + "\n[details truncated]", true
}

// Keep these additional recognizers separate from the short durable diagnostic
// sanitizer: activity contains source and command arguments as well as prose.
var (
	activityLabeledCredential = regexp.MustCompile(`(?i)(?:[A-Za-z0-9_-]*(?:password|passwd|token|secret|api[_-]?key|access[_-]?key(?:[_-]?id)?|private[_-]?key)|cookie|set-cookie)["']?\s*(?::=|=>|[:=])\s*(?:["'][^\r\n]*|[^\s,;]+)`)
	activityPrivateKey        = regexp.MustCompile(`(?s)-----BEGIN [^-\r\n]*PRIVATE KEY-----.*?(?:-----END [^-\r\n]*PRIVATE KEY-----|$)`)
	activityCredentialFlag    = regexp.MustCompile(`(?i)--(?:password|passwd|token|secret|api[_-]?key|access[_-]?token|client[_-]?secret)(?:=|\s+)(?:["'][^\r\n]*|[^\s,;]+)`)
	activityKnownToken        = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{12,}|github_pat_[A-Za-z0-9_]{12,}|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[A-Z0-9]{16}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+)\b`)
)

func sensitiveActivityPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == ".env" || strings.HasPrefix(base, ".env.") || base == "auth.json" || base == "credentials" || base == "credentials.json" || base == ".netrc" || base == ".npmrc" || strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_ed25519") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key")
}

// recordActivityAction records only application-confirmed operational outcomes.
// It deliberately excludes human answers and follow-up text, including secrets.
func (e *WorkspaceExecution) recordActivityAction(revision int64, kind, title, status string) {
	if e.activity == nil {
		return
	}
	if status == "resolved" && (kind == "question" || kind == "approval") {
		e.activity.resolveInteractions(activeExecutionKey{databasePath: e.database.Path, runID: e.id})
	}
	e.activity.publish(activeExecutionKey{databasePath: e.database.Path, runID: e.id}, ActivityItem{ID: activityID("action:" + strconv.FormatInt(revision, 10) + ":" + kind + ":" + title), Kind: kind, Title: title, Status: status})
}

func (p *activityProjection) resolveInteractions(key activeExecutionKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	run := p.runs[key]
	if run == nil {
		return
	}
	changed := false
	for i := range run.items {
		item := &run.items[i]
		if item.Status != "awaiting_input" {
			continue
		}
		before := activityItemBytes(*item)
		run.cursor++
		item.Sequence = run.cursor
		item.Status = "resolved"
		item.Timestamp = time.Now().UTC()
		delta := activityItemBytes(*item) - before
		run.bytes += delta
		p.bytes += delta
		changed = true
	}
	if changed {
		p.signal(key)
	}
}
