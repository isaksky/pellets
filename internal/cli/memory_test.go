package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"pellets/internal/domain"
)

func TestMemoryCommandsJSONV1AcrossLinkedWorktrees(t *testing.T) {
	t.Parallel()

	common := filepath.Join(t.TempDir(), "memory command database 界")
	mainWorkTree := filepath.Join(common, "main")
	linkedWorkTree := filepath.Join(common, "linked")
	if err := os.MkdirAll(mainWorkTree, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainWorkTree, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(mainWorkTree, "README"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainWorkTree, "add", "README")
	runGitTest(
		t, mainWorkTree,
		"-c", "user.name=Pellets Test", "-c", "user.email=pellets@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture",
	)
	runGitTest(t, mainWorkTree, "worktree", "add", "--quiet", "-b", "memory-command-linked", linkedWorkTree)
	if stdout, stderr, exit := runTestApp(initDBTestApp(common), "init-db"); exit != 0 || stderr != "" {
		t.Fatalf("init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	current := mainWorkTree
	application := projectTestApp(&current)
	runProjectInit(t, application, "shared")
	current = linkedWorkTree
	runProjectInit(t, application, "shared")

	var golden strings.Builder
	agentRaw := runPelletCommand(
		t, application, "memory", "add", "--text", "shared-123 is ordinary memory text.",
	)
	agent := decodeMemoryCommand(t, agentRaw, "memory add")
	if agent.ID != 1 || agent.Project != "shared" || agent.CreatedBy != domain.MemoryCreatedByAgent || agent.HumanApproved || agent.ApprovedAt != nil || agent.CreatedAt != agent.UpdatedAt {
		t.Fatalf("agent memory JSON = %#v", agent)
	}
	assertMemoryJSONKeys(t, agentRaw)
	appendMemoryGolden(t, &golden, "add-agent", agentRaw)

	application.stdin = strings.NewReader("reviewed from stdin\n")
	humanRaw := runPelletCommand(
		t, application, "--project", "shared", "memory", "add", "--file", "-", "--created-by", "human",
	)
	human := decodeMemoryCommand(t, humanRaw, "memory add")
	if human.ID != 2 || human.CreatedBy != domain.MemoryCreatedByHuman || !human.HumanApproved || human.ApprovedAt == nil || *human.ApprovedAt != human.CreatedAt || human.UpdatedAt != human.CreatedAt || human.Text != "reviewed from stdin\n" {
		t.Fatalf("human memory JSON = %#v", human)
	}
	appendMemoryGolden(t, &golden, "add-human", humanRaw)

	current = mainWorkTree
	listRaw := runPelletCommand(t, application, "memory", "list")
	listed := decodeMemoryListCommand(t, listRaw, "memory list")
	if len(listed) != 2 || listed[0].ID != human.ID || listed[1].ID != agent.ID {
		t.Fatalf("memory list = %#v", listed)
	}
	appendMemoryGolden(t, &golden, "list", listRaw)

	approvedRaw := runPelletCommand(t, application, "memory", "list", "--approved-only", "--limit", "1")
	approved := decodeMemoryListCommand(t, approvedRaw, "memory list")
	if len(approved) != 1 || approved[0].ID != human.ID {
		t.Fatalf("approved list = %#v", approved)
	}
	appendMemoryGolden(t, &golden, "list-approved-limit", approvedRaw)

	searchRaw := runPelletCommand(t, application, "memory", "search", "shared-123")
	searched := decodeMemoryListCommand(t, searchRaw, "memory search")
	if len(searched) != 1 || searched[0].ID != agent.ID || searched[0].Rank == nil || searched[0].Snippet == nil || *searched[0].Snippet != agent.Text || searched[0].HumanApproved {
		t.Fatalf("memory search = %#v", searched)
	}
	assertMemorySearchJSONKeys(t, searchRaw)
	appendMemoryGolden(t, &golden, "search-reference", searchRaw)

	emptyApprovedRaw := runPelletCommand(t, application, "memory", "search", "shared-123", "--approved-only")
	emptyApproved := decodeMemoryListCommand(t, emptyApprovedRaw, "memory search")
	if emptyApproved == nil || len(emptyApproved) != 0 {
		t.Fatalf("unapproved filtered search = %#v, want typed empty list", emptyApproved)
	}
	appendMemoryGolden(t, &golden, "search-approved-empty", emptyApprovedRaw)

	malformedRaw := runPelletCommand(t, application, "memory", "search", `shared-123 OR (`)
	malformed := decodeMemoryListCommand(t, malformedRaw, "memory search")
	if malformed == nil || len(malformed) != 0 {
		t.Fatalf("malformed FTS text search = %#v, want typed empty list", malformed)
	}
	appendMemoryGolden(t, &golden, "search-malformed-as-text", malformedRaw)

	showRaw := runPelletCommand(t, application, "memory", "show", "1")
	shown := decodeMemoryCommand(t, showRaw, "memory show")
	if !reflect.DeepEqual(shown, agent) {
		t.Fatalf("shown memory = %#v, want %#v", shown, agent)
	}
	appendMemoryGolden(t, &golden, "show", showRaw)

	approveRaw := runPelletCommand(t, application, "memory", "approve", "1")
	approvedAgent := decodeMemoryCommand(t, approveRaw, "memory approve")
	if approvedAgent.ApprovedAt == nil || !approvedAgent.HumanApproved || approvedAgent.CreatedBy != domain.MemoryCreatedByAgent || approvedAgent.CreatedAt != agent.CreatedAt || approvedAgent.Text != agent.Text || approvedAgent.UpdatedAt != *approvedAgent.ApprovedAt {
		t.Fatalf("approved agent memory = %#v", approvedAgent)
	}
	appendMemoryGolden(t, &golden, "approve", approveRaw)
	repeatRaw := runPelletCommand(t, application, "memory", "approve", "1")
	if repeatRaw != approveRaw {
		t.Fatalf("repeated approval changed JSON:\nfirst=%s\nrepeat=%s", approveRaw, repeatRaw)
	}
	appendMemoryGolden(t, &golden, "approve-repeat", repeatRaw)

	approvedSearchRaw := runPelletCommand(t, application, "memory", "search", "shared-123", "--approved-only", "--limit", "1")
	approvedSearch := decodeMemoryListCommand(t, approvedSearchRaw, "memory search")
	if len(approvedSearch) != 1 || approvedSearch[0].ID != agent.ID || !approvedSearch[0].HumanApproved || approvedSearch[0].Rank == nil || approvedSearch[0].Snippet == nil {
		t.Fatalf("approved memory search = %#v", approvedSearch)
	}
	appendMemoryGolden(t, &golden, "search-approved-limit", approvedSearchRaw)

	stdout, stderr, exit := runTestApp(application, "memory", "remove", "2")
	if exit != 6 || stdout != "" || !strings.Contains(stderr, `"code":"confirmation_required"`) {
		t.Fatalf("unconfirmed memory removal = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if stillPresent := decodeMemoryCommand(t, runPelletCommand(t, application, "memory", "show", "2"), "memory show"); !reflect.DeepEqual(stillPresent, human) {
		t.Fatalf("unconfirmed removal changed memory: %#v", stillPresent)
	}
	removedRaw := runPelletCommand(t, application, "memory", "remove", "2", "--yes")
	removed := decodeMemoryCommand(t, removedRaw, "memory remove")
	if !reflect.DeepEqual(removed, human) {
		t.Fatalf("removed memory = %#v, want %#v", removed, human)
	}
	appendMemoryGolden(t, &golden, "remove", removedRaw)
	assertGolden(t, "memory.golden", golden.String())

	stdout, stderr, exit = runTestApp(application, "--human", "memory", "search", "shared-123", "--approved-only")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, "shared-123") {
		t.Fatalf("human memory list = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "--project", "other", "memory", "list")
	if exit != 2 || stdout != "" || !strings.Contains(stderr, `"code":"project_selection_mismatch"`) {
		t.Fatalf("mismatched project memory list = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "memory", "show", "999")
	if exit != 3 || stdout != "" || !strings.Contains(stderr, `"code":"memory_not_found"`) {
		t.Fatalf("missing memory show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "memory", "show", "2")
	if exit != 3 || stdout != "" || !strings.Contains(stderr, `"code":"memory_not_found"`) {
		t.Fatalf("removed memory show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
}

func TestMemoryCommandStrictParsing(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		code string
	}{
		{name: "missing subcommand", code: "missing_subcommand"},
		{name: "search missing query", args: []string{"search"}, code: "missing_query"},
		{name: "search blank query", args: []string{"search", " \t"}, code: "missing_query"},
		{name: "search extra query", args: []string{"search", "fact", "extra"}, code: "unexpected_argument"},
		{name: "search zero limit", args: []string{"search", "fact", "--limit", "0"}, code: "invalid_limit"},
		{name: "search noncanonical limit", args: []string{"search", "fact", "--limit", "01"}, code: "invalid_limit"},
		{name: "search approved value", args: []string{"search", "fact", "--approved-only=true"}, code: "unexpected_flag_value"},
		{name: "remove missing ID", args: []string{"remove", "--yes"}, code: "missing_memory_id"},
		{name: "remove confirmation required", args: []string{"remove", "1"}, code: "confirmation_required"},
		{name: "remove confirmation value", args: []string{"remove", "1", "--yes=true"}, code: "unexpected_flag_value"},
		{name: "remove noncanonical ID", args: []string{"remove", "01", "--yes"}, code: "invalid_memory_id"},
		{name: "add missing source", args: []string{"add"}, code: "missing_memory_text"},
		{name: "add both sources", args: []string{"add", "--text", "fact", "--file", "fact.txt"}, code: "conflicting_flags"},
		{name: "invalid creator", args: []string{"add", "--text", "fact", "--created-by", "worker"}, code: "invalid_memory_creator"},
		{name: "blank text", args: []string{"add", "--text", " \t"}, code: "invalid_memory_text"},
		{name: "list positional", args: []string{"list", "extra"}, code: "unexpected_argument"},
		{name: "list zero limit", args: []string{"list", "--limit", "0"}, code: "invalid_limit"},
		{name: "list noncanonical limit", args: []string{"list", "--limit", "01"}, code: "invalid_limit"},
		{name: "show missing ID", args: []string{"show"}, code: "missing_memory_id"},
		{name: "show pellet reference", args: []string{"show", "shared-1"}, code: "invalid_memory_id"},
		{name: "approve noncanonical ID", args: []string{"approve", "01"}, code: "invalid_memory_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseMemory(test.args)
			if err == nil || domain.PublicError(err).Code != test.code {
				t.Fatalf("parseMemory(%v) error = %v, want %s", test.args, err, test.code)
			}
		})
	}
}

func decodeMemoryCommand(t *testing.T, raw, command string) memoryData {
	t.Helper()
	var envelope struct {
		SchemaVersion int        `json:"schema_version"`
		Command       string     `json:"command"`
		Data          memoryData `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("decode %s output %q: %v", command, raw, err)
	}
	if envelope.SchemaVersion != 1 || envelope.Command != command {
		t.Fatalf("%s envelope = %#v", command, envelope)
	}
	assertMemoryTimestamp(t, envelope.Data.CreatedAt)
	assertMemoryTimestamp(t, envelope.Data.UpdatedAt)
	if envelope.Data.ApprovedAt != nil {
		assertMemoryTimestamp(t, *envelope.Data.ApprovedAt)
	}
	return envelope.Data
}

func decodeMemoryListCommand(t *testing.T, raw, command string) memoryListData {
	t.Helper()
	var envelope struct {
		SchemaVersion int            `json:"schema_version"`
		Command       string         `json:"command"`
		Data          memoryListData `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("decode %s output %q: %v", command, raw, err)
	}
	if envelope.SchemaVersion != 1 || envelope.Command != command || envelope.Data == nil {
		t.Fatalf("%s envelope = %#v", command, envelope)
	}
	return envelope.Data
}

func assertMemoryJSONKeys(t *testing.T, raw string) {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	want := []string{"approved_at", "created_at", "created_by", "human_approved", "id", "project", "text", "updated_at"}
	got := make([]string, 0, len(envelope.Data))
	for key := range envelope.Data {
		got = append(got, key)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("memory JSON keys = %v, want %v", got, want)
	}
}

func assertMemorySearchJSONKeys(t *testing.T, raw string) {
	t.Helper()
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("memory search JSON data = %#v", envelope.Data)
	}
	want := []string{"approved_at", "created_at", "created_by", "human_approved", "id", "project", "rank", "snippet", "text", "updated_at"}
	got := make([]string, 0, len(envelope.Data[0]))
	for key := range envelope.Data[0] {
		got = append(got, key)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("memory search JSON keys = %v, want %v", got, want)
	}
}

func appendMemoryGolden(t *testing.T, builder *strings.Builder, label, raw string) {
	t.Helper()
	if strings.Count(raw, "\n") != 1 || !strings.HasSuffix(raw, "\n") {
		t.Fatalf("%s output is not one compact JSON line: %q", label, raw)
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	normalizeMemoryTimestamps(value)
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(builder, "%s %s", label, encoded.String())
}

func normalizeMemoryTimestamps(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if (key == "created_at" || key == "updated_at" || key == "approved_at") && child != nil {
				typed[key] = "<timestamp>"
				continue
			}
			normalizeMemoryTimestamps(child)
		}
	case []any:
		for _, child := range typed {
			normalizeMemoryTimestamps(child)
		}
	}
}

func assertMemoryTimestamp(t *testing.T, value string) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC {
		t.Fatalf("memory timestamp %q is not UTC RFC 3339: %v", value, err)
	}
}
