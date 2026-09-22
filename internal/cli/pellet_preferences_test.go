package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestPelletPreferenceCommands(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	runGitTest(t, filepath.Dir(root), "init", "--quiet", root)
	app := projectTestApp(&root)
	read := func(args ...string) pelletData {
		raw := runPelletCommand(t, app, args...)
		var envelope struct {
			Data pelletData `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	p := read("add", "preference", "--model", "custom-model", "--reasoning-effort", "high")
	if p.Model == nil || *p.Model != "custom-model" || p.ReasoningEffort == nil || *p.ReasoningEffort != "high" {
		t.Fatal(p)
	}
	stdout, stderr, exit := runTestApp(app, "--human", "show", p.ID)
	if exit != 0 || stderr != "" || !strings.Contains(stdout, "custom-model") || !strings.Contains(stdout, "high") {
		t.Fatalf("human preferences: %s %s %d", stdout, stderr, exit)
	}
	p = read("edit", p.ID, "--clear-model", "--reasoning-effort", "medium")
	if p.Model != nil || p.ReasoningEffort == nil || *p.ReasoningEffort != "medium" {
		t.Fatal(p)
	}
	p = read("edit", p.ID, "--clear-reasoning-effort")
	if p.ReasoningEffort != nil {
		t.Fatal(p)
	}
	runPelletError(t, app, 2, "edit", p.ID, "--model", "x", "--clear-model")
	runPelletError(t, app, 2, "edit", p.ID, "--reasoning-effort", "high", "--clear-reasoning-effort")
}
