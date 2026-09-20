package sqlite

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func groupFixture(t *testing.T) (pelletRepositoryFixture, *PelletRepository, *GroupRepository) {
	t.Helper()
	f := newPelletRepositoryFixture(t)
	q := f.open(t)
	t.Cleanup(func() { q.Close() })
	g, err := OpenGroupRepository(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return f, q, g
}
func mustGroup(t *testing.T, g *GroupRepository, p storage.Project, name string) storage.Group {
	t.Helper()
	result, err := g.CreateGroup(context.Background(), p, name)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestGroupsIdentityContextValidationAndStaleEdits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, _, r := groupFixture(t)
	a := mustGroup(t, r, f.main.Project, " Exact\nΩ ")
	b := mustGroup(t, r, f.main.Project, " exact\nΩ ")
	c := mustGroup(t, r, f.other.Project, a.Name)
	if a.ID == b.ID || a.ID == c.ID || a.Context != "" || a.Revision != 1 {
		t.Fatalf("invalid groups: %+v %+v %+v", a, b, c)
	}
	markdown := "# Shared context\n\n**Raw** <script>text</script>\r\nΩ\x00"
	edited, err := r.EditGroupContext(ctx, f.main.Project, a.ID, a.Revision, markdown)
	if err != nil || edited.Context != markdown || edited.Revision != 2 || edited.CreatedAt != a.CreatedAt || edited.UpdatedAt.Before(a.UpdatedAt) {
		t.Fatalf("edit: %+v %v", edited, err)
	}
	replay := mustGroup(t, r, f.main.Project, a.Name)
	if !reflect.DeepEqual(replay, edited) {
		t.Fatal("create replaced existing context")
	}
	_, err = r.EditGroupContext(ctx, f.main.Project, a.ID, a.Revision, "lost update")
	assertDomainErrorCode(t, err, "group_revision_conflict")
	_, err = r.RenameGroup(ctx, f.main.Project, a.ID, a.Revision, "renamed")
	assertDomainErrorCode(t, err, "group_revision_conflict")
	_, err = r.ReadGroup(ctx, f.other.Project, a.ID)
	assertDomainErrorCode(t, err, "group_not_found")
	for _, text := range []string{string([]byte{0xff}), strings.Repeat("x", storage.MaxGroupContextBytes+1)} {
		_, err = r.EditGroupContext(ctx, f.main.Project, a.ID, edited.Revision, text)
		assertDomainErrorCode(t, err, "invalid_group_context")
	}
	limit := strings.Repeat("Ω", storage.MaxGroupContextBytes/2)
	edited, err = r.EditGroupContext(ctx, f.main.Project, a.ID, edited.Revision, limit)
	if err != nil || edited.Context != limit {
		t.Fatalf("limit: %v", err)
	}
	edited, err = r.EditGroupContext(ctx, f.main.Project, a.ID, edited.Revision, "")
	if err != nil || edited.Context != "" {
		t.Fatalf("empty: %v", err)
	}
	for _, name := range []string{"", string([]byte{0xff})} {
		_, err = r.CreateGroup(ctx, f.main.Project, name)
		assertDomainErrorCode(t, err, "invalid_group_name")
	}
	groups, err := r.ListGroups(ctx, f.main.Project)
	if err != nil || len(groups) != 2 || groups[0].Name != a.Name {
		t.Fatalf("groups: %+v %v", groups, err)
	}
}

func TestGroupConcurrentCreationAndOptimisticWriters(t *testing.T) {
	t.Parallel()
	f, q, r := groupFixture(t)
	ctx := context.Background()
	const count = 8
	repos := make([]*GroupRepository, count)
	for i := range repos {
		var err error
		repos[i], err = OpenGroupRepository(ctx, f.path)
		if err != nil {
			t.Fatal(err)
		}
		defer repos[i].Close()
	}
	var wg sync.WaitGroup
	results := make(chan storage.Group, count)
	errs := make(chan error, count)
	for _, repo := range repos {
		wg.Add(1)
		go func(repo *GroupRepository) {
			defer wg.Done()
			g, err := repo.CreateGroup(ctx, f.main.Project, "concurrent")
			results <- g
			errs <- err
		}(repo)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	g := mustGroup(t, r, f.main.Project, "concurrent")
	for got := range results {
		if got.ID != g.ID {
			t.Fatalf("duplicate identity: %+v", got)
		}
	}
	// Implicit creation uses the same unique record and keeps its context.
	g, err := r.EditGroupContext(ctx, f.main.Project, g.ID, g.Revision, "preserved")
	if err != nil {
		t.Fatal(err)
	}
	p, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "member", Group: &g.Name})
	if err != nil || p.GroupID == nil || *p.GroupID != g.ID {
		t.Fatalf("member: %+v %v", p, err)
	}
	errs = make(chan error, count)
	for _, repo := range repos {
		wg.Add(1)
		go func(repo *GroupRepository) {
			defer wg.Done()
			_, err := repo.EditGroupContext(ctx, f.main.Project, g.ID, g.Revision, "one writer")
			errs <- err
		}(repo)
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else {
			assertDomainErrorCode(t, err, "group_revision_conflict")
		}
	}
	if successes != 1 {
		t.Fatalf("successful writers: %d", successes)
	}
}

func TestGroupRenamePreservesMembershipRoutingAndEmptyGroups(t *testing.T) {
	t.Parallel()
	f, q, r := groupFixture(t)
	ctx := context.Background()
	reader, err := OpenWebReader(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := OpenWebWriter(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	name := " old Ω "
	g := mustGroup(t, r, f.main.Project, name)
	g, err = r.EditGroupContext(ctx, f.main.Project, g.ID, g.Revision, "# Durable context")
	if err != nil {
		t.Fatal(err)
	}
	foreign := mustGroup(t, r, f.other.Project, name)
	var members []storage.Pellet
	for i, status := range []domain.PelletStatus{domain.PelletOpen, domain.PelletInProgress, domain.PelletClosed, domain.PelletMaybeLater} {
		p, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "member", Group: &name})
		if err != nil {
			t.Fatal(err)
		}
		switch status {
		case domain.PelletInProgress:
			p = transitionReviewTest(t, q, f.linked, p, storage.PelletStart)
		case domain.PelletClosed:
			p = transitionReviewTest(t, q, f.main, p, storage.PelletClose)
		case domain.PelletMaybeLater:
			p = transitionReviewTest(t, q, f.main, p, storage.PelletDefer)
		}
		if p.GroupID == nil || *p.GroupID != g.ID {
			t.Fatalf("member %d has no stable relation", i)
		}
		members = append(members, p)
	}
	routing, err := reader.ReadProjectRouting(ctx, f.main.Project)
	if err != nil {
		t.Fatal(err)
	}
	routing, err = writer.SaveWorkspaceAssignment(ctx, f.main.Project, f.linked.Workspace.ID, routing.Version, storage.WorkspaceAssignment{Mode: "explicit", Groups: []string{name, "routing-only"}})
	if err != nil {
		t.Fatal(err)
	}
	routing, err = writer.SetGroupAssignments(ctx, f.main.Project, routing.Version, false)
	if err != nil {
		t.Fatal(err)
	}
	oldRouting := routing
	renamed, err := r.RenameGroup(ctx, f.main.Project, g.ID, g.Revision, "new Ω")
	if err != nil || renamed.ID != g.ID || renamed.Context != g.Context || renamed.Revision != g.Revision+1 || renamed.CreatedAt != g.CreatedAt {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	for _, before := range members {
		after, err := q.ReadPellet(ctx, f.main, before.Reference)
		if err != nil || after.GroupID == nil || *after.GroupID != g.ID || *after.Group != renamed.Name || after.Status != before.Status || !reflect.DeepEqual(after.Workspace, before.Workspace) || !reflect.DeepEqual(after.Priority, before.Priority) || after.ImplementationRevision != before.ImplementationRevision+1 {
			t.Fatalf("membership changed: %+v %v", after, err)
		}
	}
	unchanged, err := r.ReadGroup(ctx, f.other.Project, foreign.ID)
	if err != nil || unchanged.Name != name {
		t.Fatalf("foreign: %+v %v", unchanged, err)
	}
	routing, err = reader.ReadProjectRouting(ctx, f.main.Project)
	if err != nil {
		t.Fatal(err)
	}
	if routing.Enabled || routing.Version == oldRouting.Version || !reflect.DeepEqual(routing.Assignment(f.linked.Workspace.ID).Groups, []string{"new Ω", "routing-only"}) {
		t.Fatalf("routing: %+v", routing)
	}
	_, err = writer.SaveWorkspaceAssignment(ctx, f.main.Project, f.linked.Workspace.ID, oldRouting.Version, oldRouting.Assignment(f.linked.Workspace.ID))
	assertDomainErrorCode(t, err, "workspace_assignment_conflict")
	collision := mustGroup(t, r, f.main.Project, "collision")
	_, err = r.RenameGroup(ctx, f.main.Project, g.ID, renamed.Revision, collision.Name)
	assertDomainErrorCode(t, err, "group_name_conflict")
	_, err = r.RenameGroup(ctx, f.main.Project, g.ID, renamed.Revision, strings.Repeat("x", 4097))
	assertDomainErrorCode(t, err, "invalid_workspace_assignment")
	got, err := r.ReadGroup(ctx, f.main.Project, g.ID)
	if err != nil || !reflect.DeepEqual(got, renamed) {
		t.Fatalf("failed rename wrote state: %+v %v", got, err)
	}
	oldFilter, err := q.ListPellets(ctx, f.main, storage.PelletListOptions{Group: &name, All: true})
	if err != nil || len(oldFilter) != 0 {
		t.Fatalf("old filter: %+v %v", oldFilter, err)
	}
	newFilter, err := q.ListPellets(ctx, f.main, storage.PelletListOptions{Group: &renamed.Name, All: true})
	if err != nil || len(newFilter) != 4 {
		t.Fatalf("new filter: %+v %v", newFilter, err)
	}
	for _, p := range members {
		owner := f.main
		if p.Status == domain.PelletInProgress {
			owner = f.linked
		}
		_, err = q.UpdatePellet(ctx, owner, p.Reference, storage.PelletChanges{Group: storage.NullableTextChange{Set: true}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = q.PurgeClosedPellets(ctx, f.main.Project, storage.PelletPurgeOptions{}); err != nil {
		t.Fatal(err)
	}
	empty, err := r.ReadGroup(ctx, f.main.Project, g.ID)
	if err != nil || empty.Context != g.Context {
		t.Fatalf("empty group lost: %+v %v", empty, err)
	}
	groups, err := reader.ListWebGroups(ctx, f.main.Project)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, n := range groups {
		if n != nil {
			names = append(names, *n)
		}
	}
	if !reflect.DeepEqual(names, []string{"collision", "new Ω", "routing-only"}) {
		t.Fatalf("web groups: %q", names)
	}
	assertQueryInt(t, q.db, "SELECT COUNT(*) FROM pragma_foreign_key_check", 0)
}

func TestGroupRenamePreservesDraftAndAddReplayNames(t *testing.T) {
	t.Parallel()
	f, reader, writer := planningFixture(t)
	ctx := context.Background()
	groups := &GroupRepository{db: writer.db}
	g := mustGroup(t, groups, f.main.Project, " exact\ngroup ")
	chat := createPlanningTestChat(t, writer, f.main.Project, "legacy-draft", samplePlanningState())
	request := "group-request"
	original, err := writer.CreateWebPellet(ctx, f.main.Project, storage.NewPellet{Title: "replayed", Group: &g.Name, RequestID: &request})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := groups.RenameGroup(ctx, f.main.Project, g.ID, g.Revision, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := writer.CreateWebPellet(ctx, f.main.Project, storage.NewPellet{Title: "replayed", Group: &g.Name, RequestID: &request})
	if err != nil || !reflect.DeepEqual(original, replay) {
		t.Fatalf("replay was rewritten: %+v %v", replay, err)
	}
	assertQueryInt(t, writer.db, "SELECT count(*) FROM groups", 1)
	saved, err := reader.ReadPlanningChat(ctx, f.main.Project, chat.ID)
	if err != nil || !reflect.DeepEqual(saved, chat) {
		t.Fatalf("draft changed: %+v %v", saved, err)
	}
	created, err := writer.CreatePlanningPellets(ctx, f.main.Project, chat.ID, chat.Version, []string{"first"})
	if err != nil || len(created.Pellets) != 1 || *created.Pellets[0].Group != g.Name || created.Pellets[0].GroupID == nil || *created.Pellets[0].GroupID == renamed.ID {
		t.Fatalf("draft lost its exact intended name: %+v %v", created, err)
	}
	current, err := reader.ReadWebPellet(ctx, f.main.Project, original.Reference)
	if err != nil || *current.Group != renamed.Name || *current.GroupID != g.ID {
		t.Fatalf("live member: %+v %v", current, err)
	}
}

func TestGroupRenameRetainsExecutionAndCheckpointEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, q, groups := groupFixture(t)
	db, err := OpenProjectDatabase(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g := mustGroup(t, groups, f.main.Project, "captured")
	target, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "target", Group: &g.Name})
	if err != nil {
		t.Fatal(err)
	}
	completeReviewTarget(t, q, db, f.main, target)
	checkpoint := addReview(t, q, f.main, target)
	if !checkpoint.Checkpoint.Ready {
		t.Fatal("test checkpoint not ready")
	}
	member, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "active", Group: &g.Name})
	if err != nil {
		t.Fatal(err)
	}
	member = transitionReviewTest(t, q, f.main, member, storage.PelletStart)
	capture := runCapture(f.main.Project.ID, f.main.Workspace.ID, member.Reference.Number)
	capture.Group = &g.Name
	capture.WorkspaceSelection = &storage.WorkspaceSelection{Enabled: true, Mode: "explicit", Groups: []string{g.Name}}
	run, err := db.CreateExecutionRun(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	historical := groupMigrationRows(t, q.db, "SELECT * FROM execution_runs ORDER BY run_id")
	checkpointHistory := groupMigrationRows(t, q.db, "SELECT * FROM review_checkpoint_targets")
	renamed, err := groups.RenameGroup(ctx, f.main.Project, g.ID, g.Revision, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	if groupMigrationRows(t, q.db, "SELECT * FROM execution_runs ORDER BY run_id") != historical || groupMigrationRows(t, q.db, "SELECT * FROM review_checkpoint_targets") != checkpointHistory {
		t.Fatal("rename rewrote immutable evidence")
	}
	current, err := q.ReadPellet(ctx, f.main, checkpoint.Reference)
	if err != nil || current.Checkpoint.Ready || current.Checkpoint.Targets[0].Reason != "scope_changed" || *current.Checkpoint.Targets[0].Group != g.Name {
		t.Fatalf("checkpoint silently retargeted: %+v %v", current, err)
	}
	assertQueryInt(t, q.db, `SELECT COUNT(*) FROM execution_changes WHERE json_extract(old_json,'$.group')='captured' AND json_extract(new_json,'$.group')='renamed'`, 1)
	_, err = q.TransitionPellet(ctx, f.main, member.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletClose, ExpectedImplementationRevision: &member.ImplementationRevision})
	if err == nil {
		t.Fatal("stale active implementation finalized after rename")
	}
	run = updateRun(t, db, run, storage.RunProgress{Phase: "implementation", State: "needs_attention", Outcome: "failed"}, "")
	capture.ResumeFrom = &run.ID
	_, err = db.CreateExecutionRun(ctx, capture)
	assertDomainErrorCode(t, err, "invalid_execution_run")
	// Editing shared context is independent of a pellet's original task scope.
	before, err := q.ReadPellet(ctx, f.main, member.Reference)
	if err != nil {
		t.Fatal(err)
	}
	_, err = groups.EditGroupContext(ctx, f.main.Project, g.ID, renamed.Revision, "later context")
	if err != nil {
		t.Fatal(err)
	}
	after, err := q.ReadPellet(ctx, f.main, member.Reference)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("context edit rewrote pellet: %+v %v", after, err)
	}
}

func TestGroupCheckpointFollowupAndLastMemberPurge(t *testing.T) {
	finding := triageFinding(1)
	f, q, db, run := triageFixture(t, []storage.ReviewFinding{finding})
	ctx := context.Background()
	groups := &GroupRepository{db: q.db}
	g := mustGroup(t, groups, f.main.Project, "Group/Exact")
	g, err := groups.EditGroupContext(ctx, f.main.Project, g.ID, g.Revision, "context for followups")
	if err != nil {
		t.Fatal(err)
	}
	receipt := reconcileFinding(t, db, run, validAssessment(finding))
	p, err := q.ReadPellet(ctx, f.main, domain.PelletReference{ProjectCode: f.main.Project.Code, Number: receipt.PelletNumber})
	if err != nil || p.GroupID == nil || *p.GroupID != g.ID {
		t.Fatalf("followup lost group relation: %+v %v", p, err)
	}
	loneName := "last member"
	last, err := q.CreatePellet(ctx, f.linked, storage.NewPellet{Title: "last", Group: &loneName})
	if err != nil {
		t.Fatal(err)
	}
	lone := mustGroup(t, groups, f.main.Project, loneName)
	lone, err = groups.EditGroupContext(ctx, f.main.Project, lone.ID, lone.Revision, "survives purge")
	if err != nil {
		t.Fatal(err)
	}
	transitionReviewTest(t, q, f.linked, last, storage.PelletClose)
	if _, err = q.PurgeClosedPellets(ctx, f.main.Project, storage.PelletPurgeOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := groups.ReadGroup(ctx, f.main.Project, lone.ID)
	if err != nil || !reflect.DeepEqual(got, lone) {
		t.Fatalf("purge lost context: %+v %v", got, err)
	}
}

func TestGroupImplicitCreatorsConvergeAndRejectForeignMembership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, q, groups := groupFixture(t)
	const count = 6
	name := "implicit"
	writers := make([]*PelletRepository, count)
	members := make([]storage.Pellet, count)
	for i := range writers {
		writers[i] = f.open(t)
		defer writers[i].Close()
		var err error
		members[i], err = q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "editable"})
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i, w := range writers {
		wg.Add(1)
		go func(i int, w *PelletRepository) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = w.CreateWebPellet(ctx, f.main, storage.NewPellet{Title: "created", Group: &name})
			} else {
				_, err = w.UpdatePellet(ctx, f.main, members[i].Reference, storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &name}})
			}
			errs <- err
		}(i, w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	all, err := groups.ListGroups(ctx, f.main.Project)
	if err != nil || len(all) != 1 || all[0].Context != "" {
		t.Fatalf("implicit groups: %+v %v", all, err)
	}
	selected, err := q.ListPellets(ctx, f.main, storage.PelletListOptions{Group: &name})
	if err != nil || len(selected) != count {
		t.Fatalf("selected: %+v %v", selected, err)
	}
	for _, p := range selected {
		if p.GroupID == nil || *p.GroupID != all[0].ID {
			t.Fatalf("different identity: %+v", p)
		}
	}
	foreign := mustGroup(t, groups, f.other.Project, name)
	if _, err = q.db.Exec(`UPDATE pellets SET group_record_id=? WHERE project_id=? AND number=?`, foreign.ID, f.main.Project.ID, selected[0].Reference.Number); err == nil {
		t.Fatal("foreign group ID admitted")
	}
	if _, err = q.db.Exec(`UPDATE pellets SET group_record_id=NULL WHERE project_id=? AND number=?`, f.main.Project.ID, selected[0].Reference.Number); err == nil {
		t.Fatal("lost group relation admitted")
	}
	invalid := string([]byte{0xff})
	_, err = q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "invalid group", Group: &invalid})
	assertDomainErrorCode(t, err, "invalid_group_name")
	_, err = q.UpdatePellet(ctx, f.main, selected[0].Reference, storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &invalid}})
	assertDomainErrorCode(t, err, "invalid_group_name")
}
