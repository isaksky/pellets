package testcodex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

// This external protocol peer performs actual Git and queue transitions in
// disposable integration repositories using the exact target sent by Pellets.
func completeScheduled(mode string, params json.RawMessage) string {
	var turn struct{ Input []struct{ Text string } }
	must(json.Unmarshal(params, &turn))
	text := turn.Input[0].Text
	var target struct{ Reference, StartingHead string }
	must(json.Unmarshal([]byte(text[strings.LastIndex(text, "\n")+1:]), &target))
	ref, err := domain.ParsePelletReference(target.Reference)
	must(err)
	files := []string{}
	proposal := map[string]any{"commit_subject": "Record the verified implementation result", "commit_body": "Keep the implementation result in a repository file so it remains visible\nwithout access to the local task queue.\n\nVerified the exact file content in the disposable integration repository."}
	var delivery map[string]any
	if data, err := os.ReadFile("fake-commit-message.json"); err == nil {
		must(json.Unmarshal(data, &delivery))
		proposal = delivery
	}
	if mode == "schedule_activity" || mode == "schedule_activity_gate" || mode == "schedule_success" || mode == "schedule_gate" || mode == "schedule_reimplementation" || mode == "schedule_wrong_report" || mode == "schedule_unreported_file" || mode == "schedule_staged" || mode == "schedule_input_live" || mode == "schedule_approval_live" || mode == "schedule_followup_live" {
		file := target.Reference + ".txt"
		content := "Implemented " + target.Reference + "\n"
		if path, ok := delivery["path"].(string); ok {
			file, content = path, delivery["content"].(string)
		}
		if mode == "schedule_reimplementation" {
			content = "Reimplemented " + target.Reference + " from " + target.StartingHead + "\n"
		}
		must(os.WriteFile(file, []byte(content), 0600))
		files = append(files, file)
		if mode == "schedule_staged" {
			must(exec.Command("git", "add", "--", file).Run())
		}
		if mode == "schedule_unreported_file" {
			must(os.WriteFile("unrelated.txt", []byte("preserve"), 0600))
		}
	}
	if mode == "schedule_commit_only" {
		must(exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "complete "+target.Reference).Run())
	}
	if mode == "schedule_close_only" {
		ctx := context.Background()
		projects, err := sqlite.OpenProjectDatabase(ctx, "pellets.db")
		must(err)
		project, err := projects.FindProjectByCode(ctx, ref.ProjectCode)
		must(err)
		must(projects.Close())
		queue, err := sqlite.OpenPelletRepository(ctx, "pellets.db")
		must(err)
		_, err = queue.TransitionPellet(ctx, storage.ResolvedProject{Project: project, Workspace: project.Workspaces[0]}, ref, storage.PelletLifecycleRequest{Operation: storage.PelletClose})
		must(err)
		must(queue.Close())
	}
	if mode == "schedule_wrong_report" {
		target.Reference = "wrong-999"
	}
	outcome := "ready"
	if mode == "schedule_noop" || mode == "schedule_empty_history" {
		outcome = "already_satisfied"
	}
	verification := "Disposable integration peer: verified exact file content."
	if mode == "schedule_unfinished" {
		outcome = "needs_attention"
		verification = "Outside-click checks passed; browser test failed on a hidden table cell.\nToken=private-attention-token\n<script>window.codexErrorInjected=true</script>"
	}
	report := map[string]any{"reference": target.Reference, "starting_head": target.StartingHead, "outcome": outcome, "files": files, "verification": verification}
	if outcome == "ready" {
		for _, key := range []string{"commit_subject", "commit_body"} {
			if value, ok := proposal[key]; ok {
				report[key] = value
			}
		}
	}
	result, err := json.Marshal(report)
	must(err)
	return string(result)
}
