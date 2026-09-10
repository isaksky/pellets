package testcodex

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

// This external protocol peer performs actual Git and queue transitions in
// disposable integration repositories using the exact target sent by Pellets.
func completeScheduled(mode string, params json.RawMessage) {
	var turn struct{ Input []struct{ Text string } }
	must(json.Unmarshal(params, &turn))
	text := turn.Input[0].Text
	var target struct{ Reference string }
	must(json.Unmarshal([]byte(text[strings.LastIndex(text, "\n")+1:]), &target))
	ref, err := domain.ParsePelletReference(target.Reference)
	must(err)
	if mode == "schedule_success" || mode == "schedule_gate" || mode == "schedule_commit_only" {
		must(exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "complete "+target.Reference).Run())
	}
	if mode == "schedule_success" || mode == "schedule_gate" || mode == "schedule_close_only" {
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
}
