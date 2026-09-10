package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestReviewCheckpointCLIContractAndOrdinaryCompatibility(t *testing.T) {
	current := filepath.Join(t.TempDir(), "demo")
	if err := os.Mkdir(current, 0755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, current, "init", "--quiet")
	application := projectTestApp(&current)
	call := func(args ...string) json.RawMessage {
		t.Helper()
		out, stderr, exit := runTestApp(application, args...)
		if exit != 0 || stderr != "" {
			t.Fatalf("%v: %d %s %s", args, exit, out, stderr)
		}
		var envelope struct {
			SchemaVersion int             `json:"schema_version"`
			Data          json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &envelope); err != nil || envelope.SchemaVersion != 1 {
			t.Fatalf("invalid envelope: %s %v", out, err)
		}
		return envelope.Data
	}
	ordinary := call("add", "ordinary")
	if strings.Contains(string(ordinary), `"kind"`) || strings.Contains(string(ordinary), `"checkpoint"`) {
		t.Fatalf("ordinary JSON changed: %s", ordinary)
	}
	call("add", "unselected")
	call("add", "last selected")
	created := call("add", "Review exact scope", "--review-targets", "demo-3,demo-1", "--request-id", "review-creation")
	var checkpoint struct {
		ID         string                   `json:"id"`
		Kind       domain.PelletKind        `json:"kind"`
		Checkpoint storage.ReviewCheckpoint `json:"checkpoint"`
	}
	if err := json.Unmarshal(created, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.ID != "demo-4" || checkpoint.Kind != domain.PelletReviewCheckpoint || checkpoint.Checkpoint.Version != 1 || checkpoint.Checkpoint.Ready || len(checkpoint.Checkpoint.Targets) != 2 || checkpoint.Checkpoint.Targets[1].Number != 3 {
		t.Fatalf("checkpoint contract: %s", created)
	}
	call("project", "rename", "renamed")
	replayed := call("add", "Review exact scope", "--review-targets", "demo-1,renamed-3", "--request-id", "review-creation")
	if !strings.Contains(string(replayed), `"reference":"renamed-1"`) || !strings.Contains(string(replayed), `"selected_reference":"demo-1"`) {
		t.Fatalf("rename replay retargeted scope: %s", replayed)
	}
	for _, args := range [][]string{{"show", "demo-4"}, {"list"}, {"search", "Review"}} {
		out := call(args...)
		if !strings.Contains(string(out), `"version":1`) || !strings.Contains(string(out), `"target_incomplete"`) {
			t.Fatalf("read omitted scope/readiness: %s", out)
		}
	}
	for _, args := range [][]string{{"add", "bad", "--review-targets", ""}, {"add", "bad", "--review-targets", "renamed-1", "--before", "renamed-2"}, {"add", "bad", "--review-targets", "renamed-1", "--maybe-later"}} {
		_, _, exit := runTestApp(application, args...)
		if exit == 0 {
			t.Fatalf("accepted invalid checkpoint %v", args)
		}
	}
}
