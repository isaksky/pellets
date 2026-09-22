package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/codex"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestPelletPreferencePrecedence(t *testing.T) {
	model, effort, override, empty := "pellet-model", "medium", "run-model", ""
	for _, tc := range []struct {
		name          string
		p             *storage.PelletExecutionPreferences
		model, effort string
	}{
		{"inherit", nil, "run-model", "high"}, {"model only", &storage.PelletExecutionPreferences{Model: &model}, "pellet-model", "high"}, {"effort only", &storage.PelletExecutionPreferences{ReasoningEffort: &effort}, "run-model", "medium"}, {"both", &storage.PelletExecutionPreferences{Model: &model, ReasoningEffort: &effort}, "pellet-model", "medium"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := codex.ResolveRunSettings(storage.CodexRunSettings{Model: "workspace-model", ReasoningEffort: "high"}, pelletRunOverrides(codex.RunOverrides{Model: &override}, tc.p))
			if err != nil || got.Model != tc.model || got.ReasoningEffort != tc.effort {
				t.Fatal(got, err)
			}
		})
	}
	got := pelletRunOverrides(codex.RunOverrides{Model: &empty}, &storage.PelletExecutionPreferences{Model: &model})
	if *got.Model != model {
		t.Fatal("empty run reset overrode pellet")
	}
}
func TestSchedulerResolvesEachPelletPreferences(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, mode := range []string{"drain", "watch"} {
		t.Run(mode, func(t *testing.T) {
			s, r, q := schedulerFixture(t, executable, "schedule_success")
			r.Mode = mode
			r.Limit = 2
			ctx := context.Background()
			medium := "medium"
			model := "test-model"
			_, err := q.UpdatePellet(ctx, r.Selected, domain.PelletReference{ProjectCode: r.Selected.Project.Code, Number: 1}, storage.PelletChanges{Model: storage.NullableTextChange{Set: true, Value: &model}, ReasoningEffort: storage.NullableTextChange{Set: true, Value: &medium}})
			if err != nil {
				t.Fatal(err)
			}
			second, err := q.CreatePellet(ctx, r.Selected, storage.NewPellet{Title: "inherits high"})
			if err != nil {
				t.Fatal(err)
			}
			status := awaitSchedule(t, startSchedule(t, s, r))
			if status.State != "stopped" || status.Reason != "limit_reached" || status.Completed != 2 {
				t.Fatal(status)
			}
			runs, err := s.options.Supervisor.ListWorkspaceRuns(ctx, s.options.Database, r.Selected.Workspace.ID, 10)
			if err != nil || len(runs) != 2 {
				t.Fatal(runs, err)
			}
			for _, run := range runs {
				want := "medium"
				if run.PelletNumber == second.Reference.Number {
					want = "high"
				}
				if run.Settings.Codex.Model != model || run.Settings.Codex.ReasoningEffort != want {
					t.Fatalf("pellet %d: %+v", run.PelletNumber, run.Settings.Codex)
				}
			}
		})
	}
}
func TestAdmissionValidatesPelletPreferences(t *testing.T) {
	s, r, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_success")
	invalid := "unsupported-effort"
	_, err := q.UpdatePellet(context.Background(), r.Selected, domain.PelletReference{ProjectCode: r.Selected.Project.Code, Number: 1}, storage.PelletChanges{ReasoningEffort: storage.NullableTextChange{Set: true, Value: &invalid}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CheckAdmission(context.Background(), r); err == nil {
		t.Fatal("unsupported pellet effort passed admission")
	}
	p, err := q.ReadPellet(context.Background(), r.Selected, domain.PelletReference{ProjectCode: r.Selected.Project.Code, Number: 1})
	if err != nil || p.Status != domain.PelletOpen {
		t.Fatal(p, err)
	}
}

func TestActiveAttemptKeepsPreferencesAndResumeUsesLatest(t *testing.T) {
	s, r, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_gate")
	ctx := context.Background()
	h := startSchedule(t, s, r)
	awaitScheduledTurn(t, s)
	first := awaitActiveRun(t, s, false)
	medium := "medium"
	_, err := q.UpdatePellet(ctx, r.Selected, domain.PelletReference{ProjectCode: r.Selected.Project.Code, Number: first.PelletNumber}, storage.PelletChanges{ReasoningEffort: storage.NullableTextChange{Set: true, Value: &medium}})
	if err != nil {
		t.Fatal(err)
	}
	active, err := s.options.Supervisor.ReadRun(ctx, s.options.Database, first.ID)
	if err != nil || active.Settings.Codex.ReasoningEffort != "high" {
		t.Fatal(active.Settings, err)
	}
	h.StopNow()
	awaitSchedule(t, h)
	if err = os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
		t.Fatal(err)
	}
	r.ResumeFrom, r.ResumePellet = &first.ID, &first.PelletNumber
	status := awaitSchedule(t, startSchedule(t, s, r))
	if status.State != "completed" {
		t.Fatal(status)
	}
	resumed, err := s.options.Supervisor.ReadRun(ctx, s.options.Database, status.RunID)
	if err != nil || resumed.Settings.Codex.ReasoningEffort != "medium" {
		t.Fatal(resumed.Settings, err)
	}
	original, err := s.options.Supervisor.ReadRun(ctx, s.options.Database, first.ID)
	if err != nil || original.Settings.Codex.ReasoningEffort != "high" {
		t.Fatal("historical settings changed", err)
	}
}
