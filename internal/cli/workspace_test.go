package cli

import (
	"encoding/json"
	"path/filepath"
	"pellets/internal/app"
	"strings"
	"testing"
)

func TestWorkspacePruneCLI(t *testing.T) {
	root := t.TempDir()
	current := root
	runGitTest(t, root, "init", "-q")
	runGitTest(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "Initial")
	application := projectTestApp(&current)
	if out, err, exit := runTestApp(application, "init-db"); exit != 0 {
		t.Fatalf("init: %s %s", out, err)
	}
	p := runCurrentProject(t, application)
	linked := filepath.Join(t.TempDir(), "linked")
	runGitTest(t, root, "worktree", "add", "--detach", linked)
	current = linked
	runCurrentProject(t, application)
	current = root
	runGitTest(t, root, "worktree", "remove", linked)
	for _, dry := range []bool{true, false} {
		args := []string{"--project", p.Code, "workspace", "prune"}
		if dry {
			args = append(args, "--dry-run")
		}
		out, err, exit := runTestApp(application, args...)
		if exit != 0 {
			t.Fatalf("prune: %s %s", out, err)
		}
		var result struct{ Data app.WorkspacePruneResult }
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		if result.Data.DryRun != dry || len(result.Data.Pruned) != 1 {
			t.Fatalf("result: %s", out)
		}
	}
	if p := runCurrentProject(t, application); len(p.Workspaces) != 1 {
		t.Fatalf("workspaces: %+v", p)
	}
	if out, stderr, exit := runTestApp(application, "workspace", "prune", "--wat"); exit != 2 || !strings.Contains(out+stderr, "unknown_flag") {
		t.Fatalf("invalid: %d %s", exit, out)
	}
}
