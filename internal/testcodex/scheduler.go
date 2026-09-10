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
	if mode == "schedule_success" || mode == "schedule_gate" || mode == "schedule_wrong_report" || mode == "schedule_unreported_file" || mode == "schedule_staged" || mode == "schedule_input_live" || mode == "schedule_approval_live" || mode == "schedule_followup_live" {
		file := target.Reference + ".txt"
		must(os.WriteFile(file, []byte("Implemented "+target.Reference+"\n"), 0600))
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
	if mode == "schedule_unfinished" {
		outcome = "needs_attention"
	}
	result, err := json.Marshal(map[string]any{"reference": target.Reference, "starting_head": target.StartingHead, "outcome": outcome, "files": files, "verification": "Disposable integration peer: verified exact file content."})
	must(err)
	return string(result)
}
