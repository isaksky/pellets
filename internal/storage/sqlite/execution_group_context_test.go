package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestExecutionCapturesActualProjectGroupAndExplicitAbsence(t *testing.T) {
	for _, name := range []string{"ungrouped", "empty", "document", "filtered", "wrong filter"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, q, groups := groupFixture(t)
			foreign, err := groups.CreateGroupWithContext(ctx, f.other.Project, "shared", "foreign document")
			if err != nil {
				t.Fatal(err)
			}
			g := mustGroup(t, groups, f.main.Project, "shared")
			if name != "empty" {
				g, err = groups.EditGroupContext(ctx, f.main.Project, g.ID, g.Revision, "# Raw Ω\r\n```mermaid\nflowchart LR\n A --> B\n```\n")
				if err != nil {
					t.Fatal(err)
				}
			}
			var membership *string
			if name != "ungrouped" {
				membership = &g.Name
			}
			p, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "Exact target", Group: membership})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = q.TransitionPellet(ctx, f.main, p.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
				t.Fatal(err)
			}
			db, err := OpenExecutionRunDatabase(ctx, f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			capture := runCapture(f.main.Project.ID, f.main.Workspace.ID, p.Reference.Number)
			// Workspace routing is not membership evidence.
			capture.WorkspaceSelection = &storage.WorkspaceSelection{Enabled: true, Mode: "explicit", Groups: []string{"routing-only"}}
			if name == "filtered" {
				capture.Group = &g.Name
			}
			if name == "wrong filter" {
				filter := "other"
				capture.Group = &filter
			}
			run, err := db.CreateExecutionRun(ctx, capture)
			if name == "wrong filter" {
				if domain.PublicError(err).Code != "invalid_execution_run" {
					t.Fatalf("wrong filter admitted: %v", err)
				}
				assertQueryInt(t, db.db, `SELECT COUNT(*) FROM execution_runs`, 0)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := storage.GroupContextSnapshot{Version: 1, State: "ungrouped"}
			if membership != nil {
				want.State, want.Group = "captured", &storage.GroupContext{ID: g.ID, Name: g.Name, Revision: g.Revision, Context: g.Context}
				if run.GroupContext.Group.ID == foreign.ID {
					t.Fatal("cross-project capture")
				}
			}
			if !reflect.DeepEqual(run.GroupContext, want) {
				t.Fatalf("snapshot = %+v, want %+v", run.GroupContext, want)
			}
		})
	}
}

func TestExecutionGroupContextRetainedThroughRecoveryAndLiveChanges(t *testing.T) {
	for _, phase := range []string{"preflight", "implementation", "fresh conversation", "finalization"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			f, q, groups := groupFixture(t)
			g, err := groups.CreateGroupWithContext(ctx, f.main.Project, "original", "original Markdown\r\n")
			if err != nil {
				t.Fatal(err)
			}
			p, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "Exact target", Group: &g.Name})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = q.TransitionPellet(ctx, f.main, p.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
				t.Fatal(err)
			}
			db, err := OpenExecutionRunDatabase(ctx, f.path)
			if err != nil {
				t.Fatal(err)
			}
			run, err := db.CreateExecutionRun(ctx, runCapture(f.main.Project.ID, f.main.Workspace.ID, p.Reference.Number))
			if err != nil {
				t.Fatal(err)
			}
			original := run.GroupContext
			progress := storage.RunProgress{Phase: "implementation", State: "interrupted", Outcome: "unknown", ThreadID: "thread", TurnID: "turn"}
			if phase == "preflight" {
				progress.Phase, progress.ThreadID, progress.TurnID = "preflight", "", ""
			}
			if phase == "finalization" {
				progress.Phase = "verification"
				progress.Finalization = &storage.FinalizationEvidence{NoChanges: true, Tree: strings.Repeat("b", 40), Subject: "Already satisfied"}
			}
			run = updateRun(t, db, run, progress, "")
			if _, err = groups.EditGroupContext(ctx, f.main.Project, g.ID, g.Revision, ""); err != nil {
				t.Fatal(err)
			}
			db.Close()
			db, err = OpenExecutionRunDatabase(ctx, f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			run, err = db.ReadExecutionRun(ctx, run.ID)
			if err != nil || !reflect.DeepEqual(run.GroupContext, original) {
				t.Fatalf("restart snapshot changed: %+v %v", run.GroupContext, err)
			}
			capture := run.RunCapture
			capture.ResumeFrom, capture.FreshConversation = &run.ID, phase == "fresh conversation"
			resumed, err := db.CreateExecutionRun(ctx, capture)
			if err != nil || !reflect.DeepEqual(resumed.GroupContext, original) || !reflect.DeepEqual(resumed.Finalization, run.Finalization) {
				t.Fatalf("recovery snapshot changed: %+v %v", resumed, err)
			}
			if phase == "fresh conversation" && resumed.ThreadID != "" {
				t.Fatal("fresh recovery kept the old thread")
			}
			// Renames still obey the existing membership/generation checks.
			g, err = groups.ReadGroup(ctx, f.main.Project, g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = groups.RenameGroup(ctx, f.main.Project, g.ID, g.Revision, "renamed"); err != nil {
				t.Fatal(err)
			}
			// Live membership changes cannot rewrite even a stopped snapshot.
			resumed = updateRun(t, db, resumed, progress, "")
			if _, err := q.UpdatePellet(ctx, f.main, p.Reference, storage.PelletChanges{Group: storage.NullableTextChange{Set: true}}); err != nil {
				t.Fatal(err)
			}
			after, err := db.ReadExecutionRun(ctx, resumed.ID)
			if err != nil || !reflect.DeepEqual(after.GroupContext, original) {
				t.Fatalf("membership mutated snapshot: %+v %v", after.GroupContext, err)
			}
			capture.ResumeFrom = &resumed.ID
			if _, err := db.CreateExecutionRun(ctx, capture); err == nil {
				t.Fatal("membership generation change bypassed Resume checks")
			}
		})
	}
}

func TestExecutionGroupCaptureAtomicWithMembershipAndContextEdits(t *testing.T) {
	ctx := context.Background()
	f, q, groups := groupFixture(t)
	g, err := groups.CreateGroupWithContext(ctx, f.main.Project, "group-0", "document-0")
	if err != nil {
		t.Fatal(err)
	}
	spare := mustGroup(t, groups, f.main.Project, "spare")
	ids := []int64{g.ID, spare.ID}
	p, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "0", Group: &g.Name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.TransitionPellet(ctx, f.main, p.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
		t.Fatal(err)
	}
	db, err := OpenExecutionRunDatabase(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writer := f.open(t)
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		for i := 1; i <= 40; i++ {
			err := func() error {
				conn, err := writer.db.Conn(ctx)
				if err != nil {
					return err
				}
				defer conn.Close()
				if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
					return err
				}
				defer conn.ExecContext(ctx, "ROLLBACK")
				name := fmt.Sprintf("group-%d", i)
				if _, err := conn.ExecContext(ctx, `UPDATE groups SET name=?,context=?,revision=? WHERE group_id=?`, name, fmt.Sprintf("document-%d", i), i+1, ids[i%2]); err != nil {
					return err
				}
				if _, err := conn.ExecContext(ctx, `UPDATE pellets SET group_id=?,title=? WHERE project_id=? AND number=?`, name, strconv.Itoa(i), f.main.Project.ID, p.Reference.Number); err != nil {
					return err
				}
				_, err = conn.ExecContext(ctx, "COMMIT")
				return err
			}()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for range 40 {
		run, err := db.CreateExecutionRun(ctx, runCapture(f.main.Project.ID, f.main.Workspace.ID, p.Reference.Number))
		if err != nil {
			t.Fatal(err)
		}
		i, err := strconv.Atoi(run.PelletTitle)
		if err != nil {
			t.Fatal(err)
		}
		g := run.GroupContext.Group
		if g == nil || g.ID != ids[i%2] || g.Name != fmt.Sprintf("group-%d", i) || g.Context != fmt.Sprintf("document-%d", i) || g.Revision != int64(i+1) {
			t.Fatalf("torn admission: title=%s group=%+v", run.PelletTitle, g)
		}
		updateRun(t, db, run, storage.RunProgress{Phase: "preflight", State: "interrupted", Outcome: "unknown"}, "")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestExecutionGroupContextBoundsAndValidation(t *testing.T) {
	f, q, groups := groupFixture(t)
	ctx := context.Background()
	// JSON escaping must not truncate valid source at the full raw bound.
	g, err := groups.CreateGroupWithContext(ctx, f.main.Project, "large", strings.Repeat("\x01", storage.MaxGroupContextBytes))
	if err != nil {
		t.Fatal(err)
	}
	p, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "large", Group: &g.Name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.TransitionPellet(ctx, f.main, p.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
		t.Fatal(err)
	}
	db, err := OpenExecutionRunDatabase(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run, err := db.CreateExecutionRun(ctx, runCapture(f.main.Project.ID, f.main.Workspace.ID, p.Reference.Number))
	if err != nil || run.GroupContext.Group.Context != g.Context {
		t.Fatalf("maximum escaped context: %v", err)
	}
	for _, snapshot := range []storage.GroupContextSnapshot{
		{}, {Version: 2, State: "ungrouped"}, {Version: 1, State: "captured"},
		{Version: 1, State: "legacy", Group: run.GroupContext.Group},
		{Version: 1, State: "captured", Group: &storage.GroupContext{ID: 1, Name: "x", Revision: 1, Context: strings.Repeat("x", storage.MaxGroupContextBytes+1)}},
		{Version: 1, State: "captured", Group: &storage.GroupContext{ID: 1, Name: "x", Revision: 1, Context: string([]byte{255})}},
	} {
		if err := storage.ValidateGroupContextSnapshot(snapshot); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
}
