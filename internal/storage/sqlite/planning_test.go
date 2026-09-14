package sqlite

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func planningFixture(t *testing.T) (pelletRepositoryFixture, *WebReader, *WebWriter) {
	t.Helper()
	f := newPelletRepositoryFixture(t)
	reader, err := OpenWebReader(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	writer, err := OpenWebWriter(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	return f, reader, writer
}

func samplePlanningState() storage.PlanningState {
	return storage.PlanningState{
		Model: "model-one", Effort: "high", Composer: "Unfinished next question\nwith a second line",
		Messages: []storage.PlanningMessage{{ID: "message-1", Role: "user", Text: "Plan the work"}, {ID: "message-2", Role: "assistant", Text: "Here are proposals", Model: "model-one", Effort: "high"}},
		Drafts: []storage.PlanningDraft{
			{ID: "first", Title: "first draft", Description: "first description", Acceptance: "one check\nsecond check", Group: " exact\ngroup ", Selected: true},
			{ID: "second", Title: "second draft", Description: "second description", Group: "other", Selected: true},
			{ID: "third", Title: "third draft", Description: "third description"},
		},
	}
}

func createPlanningTestChat(t *testing.T, w *WebWriter, p storage.Project, request string, state storage.PlanningState) storage.PlanningChat {
	t.Helper()
	chat, err := w.CreatePlanningChat(context.Background(), p, request, state)
	if err != nil {
		t.Fatal(err)
	}
	return chat
}

func planningError(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || domain.PublicError(err).Code != code {
		t.Fatalf("error = %v; want %s", err, code)
	}
}

func TestPlanningProjectPersistenceAndIdempotentChatSave(t *testing.T) {
	f, reader, writer := planningFixture(t)
	ctx := context.Background()
	latest, err := reader.LatestPlanningChat(ctx, f.main.Project)
	if err != nil || latest != nil {
		t.Fatalf("initial latest = %#v, %v", latest, err)
	}
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM planning_chats", 0)
	state := samplePlanningState()
	chat := createPlanningTestChat(t, writer, f.main.Project, "new-chat", state)
	if chat.ID < 1 || chat.ProjectID != f.main.Project.ID || chat.Version == "" || chat.CreatedAt.IsZero() || !reflect.DeepEqual(chat.State, state) {
		t.Fatalf("created chat = %#v", chat)
	}
	state.Composer, state.Model, state.Effort, state.RefiningDraftID = "Still unfinished", "model-two", "max", "second"
	state.Drafts[1].Description = "edited description"
	saved, err := writer.SavePlanningChat(ctx, f.main.Project, chat.ID, chat.Version, state)
	if err != nil || saved.Version == chat.Version || !reflect.DeepEqual(saved.State, state) {
		t.Fatalf("save = %#v, %v", saved, err)
	}
	// An exact response-lost retry is a no-op; a different stale draft conflicts.
	replayed, err := writer.SavePlanningChat(ctx, f.main.Project, chat.ID, chat.Version, state)
	if err != nil || !reflect.DeepEqual(replayed, saved) {
		t.Fatalf("save replay = %#v, %v", replayed, err)
	}
	changed := state
	changed.Composer = "would overwrite another editor"
	_, err = writer.SavePlanningChat(ctx, f.main.Project, chat.ID, chat.Version, changed)
	planningError(t, err, "planning_chat_conflict")
	for _, mutation := range []func(*storage.PlanningState){
		func(s *storage.PlanningState) { s.Messages = s.Messages[:1] },
		func(s *storage.PlanningState) { s.Messages[0].Text = "rewritten history" },
		func(s *storage.PlanningState) { s.Messages[1].ID = "rewritten-retry-identity" },
	} {
		invalid, _ := freezePlanningState(saved.State)
		mutation(&invalid)
		_, err = writer.SavePlanningChat(ctx, f.main.Project, chat.ID, saved.Version, invalid)
		planningError(t, err, "invalid_planning_state")
		_, err = writer.SavePlanningChat(ctx, f.main.Project, chat.ID, chat.Version, invalid)
		planningError(t, err, "planning_chat_conflict")
	}
	// Retrying chat creation returns its current state without duplicating it.
	replayed = createPlanningTestChat(t, writer, f.main.Project, "new-chat", samplePlanningState())
	if !reflect.DeepEqual(replayed, saved) {
		t.Fatalf("chat creation replay lost current state: %#v", replayed)
	}
	_, err = writer.CreatePlanningChat(ctx, f.main.Project, "new-chat", changed)
	planningError(t, err, "planning_request_conflict")
	_, err = reader.ReadPlanningChat(ctx, f.other.Project, chat.ID)
	planningError(t, err, "planning_chat_not_found")
	_, err = writer.SavePlanningChat(ctx, f.other.Project, chat.ID, saved.Version, state)
	planningError(t, err, "planning_chat_not_found")
	_, err = writer.CreatePlanningPellets(ctx, f.other.Project, chat.ID, saved.Version, []string{"first"})
	planningError(t, err, "planning_chat_not_found")
	foreign := createPlanningTestChat(t, writer, f.other.Project, "new-chat", state)
	second := createPlanningTestChat(t, writer, f.main.Project, "second-chat", storage.PlanningState{})
	if foreign.ID == chat.ID || second.ID == chat.ID {
		t.Fatal("new chat did not receive a separate stable identity")
	}
	latest, err = reader.LatestPlanningChat(ctx, f.main.Project)
	if err != nil || latest == nil || latest.ID != second.ID || latest.State.Messages == nil || latest.State.Drafts == nil {
		t.Fatalf("latest = %#v, %v", latest, err)
	}
	restored, err := reader.ReadPlanningChat(ctx, f.main.Project, saved.ID)
	if err != nil || !reflect.DeepEqual(restored, saved) {
		t.Fatalf("old chat changed = %#v, %v", restored, err)
	}
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM pellets", 0)
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM execution_runs", 0)
}

func TestPlanningSelectiveCreationAtomicityAndImmutableReceipts(t *testing.T) {
	f, reader, writer := planningFixture(t)
	ctx := context.Background()
	chat := createPlanningTestChat(t, writer, f.main.Project, "chat", samplePlanningState())
	// Exact explicit IDs govern creation, regardless of the stored checkbox
	// state. Numbering and order follow the proposal order, not request order.
	created, err := writer.CreatePlanningPellets(ctx, f.main.Project, chat.ID, chat.Version, []string{"third", "first"})
	if err != nil || len(created.Pellets) != 2 {
		t.Fatalf("create = %#v, %v", created, err)
	}
	for i, pellet := range created.Pellets {
		if pellet.Reference.Number != int64(i+1) || pellet.Priority == nil || pellet.ProjectID != f.main.Project.ID || pellet.Kind != domain.PelletOrdinary || pellet.Workspace != nil || pellet.Status != domain.PelletOpen || pellet.Checkpoint != nil || pellet.ExternalID != nil {
			t.Fatalf("created ownership/order/kind = %#v", pellet)
		}
	}
	if *created.Pellets[0].Priority >= *created.Pellets[1].Priority {
		t.Fatal("created queue order differs from proposal order")
	}
	if created.Pellets[0].Group == nil || *created.Pellets[0].Group != " exact\ngroup " || created.Pellets[1].Group != nil || created.Pellets[0].Description != "first description\n\nAcceptance criteria\none check\nsecond check" {
		t.Fatalf("draft content lost: %#v", created.Pellets)
	}
	if created.Chat.State.Drafts[1].CreatedReference != "" || created.Chat.State.Drafts[0].CreatedReference != "code-1" || created.Chat.State.Drafts[2].CreatedReference != "code-2" || created.Chat.State.Drafts[0].Selected {
		t.Fatalf("draft receipts = %#v", created.Chat.State.Drafts)
	}
	listed, err := reader.ListWebPellets(ctx, f.main.Project, storage.WebPelletFilters{Query: "criteria"})
	if err != nil || len(listed) != 1 || listed[0].Reference.Number != 1 {
		t.Fatalf("ordinary search index = %#v, %v", listed, err)
	}
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM execution_runs", 0)
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM pellets WHERE workspace_id IS NOT NULL", 0)
	assertPelletQueryInt(t, writer.db, "SELECT next_pellet_number FROM projects WHERE project_id=?", 3, f.main.Project.ID)
	state := created.Chat.State
	state.Drafts[0].CreatedNumber, state.Drafts[0].CreatedReference = 0, ""
	state.Composer = "another unfinished question"
	saved, err := writer.SavePlanningChat(ctx, f.main.Project, chat.ID, created.Chat.Version, state)
	if err != nil || saved.State.Drafts[0].CreatedNumber != 1 || saved.State.Drafts[0].CreatedReference != "code-1" {
		t.Fatalf("save erased receipt: %#v, %v", saved, err)
	}
	for name, mutate := range map[string]func(*storage.PlanningState){
		"created title":      func(s *storage.PlanningState) { s.Drafts[0].Title = "edit actual pellet" },
		"created deletion":   func(s *storage.PlanningState) { s.Drafts = s.Drafts[1:] },
		"created number":     func(s *storage.PlanningState) { s.Drafts[0].CreatedNumber = 999 },
		"forged draft":       func(s *storage.PlanningState) { s.Drafts[1].CreatedReference = "code-50" },
		"created refinement": func(s *storage.PlanningState) { s.RefiningDraftID = "first" },
	} {
		t.Run(name, func(t *testing.T) {
			proposed, _ := freezePlanningState(saved.State)
			mutate(&proposed)
			_, err := writer.SavePlanningChat(ctx, f.main.Project, chat.ID, saved.Version, proposed)
			planningError(t, err, "invalid_planning_state")
		})
	}
	replayed, err := writer.CreatePlanningPellets(ctx, f.main.Project, chat.ID, chat.Version, []string{"first", "third"})
	if err != nil || !reflect.DeepEqual(replayed.Pellets, created.Pellets) || replayed.Chat.Version != saved.Version {
		t.Fatalf("stale completed replay = %#v, %v", replayed, err)
	}
	_, err = writer.CreatePlanningPellets(ctx, f.main.Project, chat.ID, chat.Version, []string{"first", "second"})
	planningError(t, err, "planning_chat_conflict")
	mixed, err := writer.CreatePlanningPellets(ctx, f.main.Project, chat.ID, saved.Version, []string{"first", "second"})
	if err != nil || len(mixed.Pellets) != 2 || mixed.Pellets[0].Reference.Number != 1 || mixed.Pellets[1].Reference.Number != 3 {
		t.Fatalf("mixed creation = %#v, %v", mixed, err)
	}
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM planning_draft_creations", 3)
}

func TestPlanningInvalidBatchRollsBackAllNumbersAndQueueWork(t *testing.T) {
	f, reader, writer := planningFixture(t)
	ctx := context.Background()
	state := samplePlanningState()
	state.Drafts[1].Title = " "
	chat := createPlanningTestChat(t, writer, f.main.Project, "invalid-batch", state)
	result, err := writer.CreatePlanningPellets(ctx, f.main.Project, chat.ID, chat.Version, []string{"first", "second"})
	if err == nil || len(result.Pellets) != 0 || result.Chat.ID != 0 {
		t.Fatalf("invalid batch returned success records: %#v, %v", result, err)
	}
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM pellets", 0)
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM planning_draft_creations", 0)
	assertPelletQueryInt(t, writer.db, "SELECT next_pellet_number FROM projects WHERE project_id=?", 1, f.main.Project.ID)
	current, err := reader.ReadPlanningChat(ctx, f.main.Project, chat.ID)
	if err != nil || !reflect.DeepEqual(current, chat) {
		t.Fatalf("invalid batch modified chat = %#v, %v", current, err)
	}
	for _, ids := range [][]string{nil, {"first", "first"}, {"missing"}, {"invalid ID"}} {
		_, err = writer.CreatePlanningPellets(ctx, f.main.Project, chat.ID, chat.Version, ids)
		planningError(t, err, "invalid_planning_state")
	}
	// Storage failure after the first allocation must also roll back the batch.
	state.Drafts[1].Title = "valid second"
	chat, err = writer.SavePlanningChat(ctx, f.main.Project, chat.ID, chat.Version, state)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, writer.db, `CREATE TEMP TRIGGER fail_second_planning_creation BEFORE INSERT ON planning_draft_creations WHEN NEW.draft_id='second' BEGIN SELECT RAISE(ABORT,'injected second receipt failure'); END;`)
	result, err = writer.CreatePlanningPellets(ctx, f.main.Project, chat.ID, chat.Version, []string{"first", "second"})
	if err == nil || len(result.Pellets) != 0 {
		t.Fatalf("injected storage failure = %#v, %v", result, err)
	}
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM pellets", 0)
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM planning_draft_creations", 0)
	assertPelletQueryInt(t, writer.db, "SELECT next_pellet_number FROM projects WHERE project_id=?", 1, f.main.Project.ID)
	current, err = reader.ReadPlanningChat(ctx, f.main.Project, chat.ID)
	if err != nil || current.Version != chat.Version {
		t.Fatalf("storage failure modified version: %#v, %v", current, err)
	}
}

func TestPlanningConcurrentSaveAndCreation(t *testing.T) {
	f, reader, writer := planningFixture(t)
	ctx := context.Background()
	other, err := OpenWebWriter(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	chat := createPlanningTestChat(t, writer, f.main.Project, "concurrent-chat", samplePlanningState())
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, w := range []*WebWriter{writer, other} {
		wg.Add(1)
		go func(i int, w *WebWriter) {
			defer wg.Done()
			<-start
			state, _ := freezePlanningState(chat.State)
			state.Composer = []string{"first editor", "second editor"}[i]
			_, err := w.SavePlanningChat(ctx, f.main.Project, chat.ID, chat.Version, state)
			results <- err
		}(i, w)
	}
	close(start)
	wg.Wait()
	succeeded, conflicted := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else if domain.PublicError(err).Code == "planning_chat_conflict" {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent saves successes=%d conflicts=%d", succeeded, conflicted)
	}
	chat, err = reader.ReadPlanningChat(ctx, f.main.Project, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	creations := make(chan storage.PlanningCreation, 2)
	for _, w := range []*WebWriter{writer, other} {
		wg.Add(1)
		go func(w *WebWriter) {
			defer wg.Done()
			<-start
			result, err := w.CreatePlanningPellets(ctx, f.main.Project, chat.ID, chat.Version, []string{"first", "second"})
			results <- err
			creations <- result
		}(w)
	}
	close(start)
	wg.Wait()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	a, b := <-creations, <-creations
	if !reflect.DeepEqual(a, b) || len(a.Pellets) != 2 {
		t.Fatalf("concurrent replay mismatch: %#v / %#v", a, b)
	}
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM pellets", 2)
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM planning_draft_creations", 2)
	assertPelletQueryInt(t, writer.db, "SELECT next_pellet_number FROM projects WHERE project_id=?", 3, f.main.Project.ID)
}

func TestPlanningReceiptsSurvivePurgeAndProjectRename(t *testing.T) {
	f, reader, writer := planningFixture(t)
	ctx := context.Background()
	chat := createPlanningTestChat(t, writer, f.main.Project, "history", samplePlanningState())
	creation, err := writer.CreatePlanningPellets(ctx, f.main.Project, chat.ID, chat.Version, []string{"first"})
	if err != nil {
		t.Fatal(err)
	}
	repo := writer.pelletRepository()
	if _, err := repo.TransitionPellet(ctx, f.main, creation.Pellets[0].Reference, storage.PelletLifecycleRequest{Operation: storage.PelletClose}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PurgeClosedPellets(ctx, f.main.Project, storage.PelletPurgeOptions{}); err != nil {
		t.Fatal(err)
	}
	database := ProjectDatabase{db: writer.db}
	renamed, err := database.RenameProject(ctx, storage.ProjectRenameRequest{ProjectID: f.main.Project.ID, NewCode: "renamed"})
	if err != nil || renamed.Project.Code != "renamed" {
		t.Fatalf("project rename = %#v, %v", renamed, err)
	}
	// A previously resolved project code remains a valid redirect, but all
	// successful responses must project the current canonical code.
	replayed, err := writer.CreatePlanningPellets(ctx, f.main.Project, chat.ID, chat.Version, []string{"first"})
	if err != nil || len(replayed.Pellets) != 1 || replayed.Pellets[0].Reference.String() != "renamed-1" || replayed.Chat.State.Drafts[0].CreatedReference != "renamed-1" || replayed.Chat.Version != creation.Chat.Version {
		t.Fatalf("purged renamed replay = %#v, %v", replayed, err)
	}
	current, err := reader.ReadPlanningChat(ctx, f.main.Project, chat.ID)
	if err != nil || !reflect.DeepEqual(current, replayed.Chat) {
		t.Fatalf("canonical read = %#v, %v", current, err)
	}
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM pellets", 0)
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM planning_draft_creations", 1)
	assertPelletQueryInt(t, writer.db, "SELECT next_pellet_number FROM projects WHERE project_id=?", 2, f.main.Project.ID)
	if _, err := writer.db.Exec("UPDATE planning_draft_creations SET pellet_number=50 WHERE chat_id=?", chat.ID); err == nil {
		t.Fatal("immutable creation evidence was overwritten")
	}
}

func TestPlanningBoundedValidatedState(t *testing.T) {
	f, _, writer := planningFixture(t)
	for name, mutate := range map[string]func(*storage.PlanningState){
		"composer":     func(s *storage.PlanningState) { s.Composer = strings.Repeat("a", storage.MaxPlanningComposerBytes+1) },
		"model":        func(s *storage.PlanningState) { s.Model = "model\nsecond" },
		"invalid UTF8": func(s *storage.PlanningState) { s.Composer = string([]byte{0xff}) },
		"NUL":          func(s *storage.PlanningState) { s.Drafts[0].Group = "a\x00b" },
		"message count": func(s *storage.PlanningState) {
			s.Messages = make([]storage.PlanningMessage, storage.MaxPlanningMessages+1)
		},
		"message size": func(s *storage.PlanningState) {
			s.Messages[0].Text = strings.Repeat("a", storage.MaxPlanningTextBytes+1)
		},
		"empty message":            func(s *storage.PlanningState) { s.Messages[0].Text = " \n" },
		"message role":             func(s *storage.PlanningState) { s.Messages[0].Role = "system" },
		"duplicate message":        func(s *storage.PlanningState) { s.Messages[1].ID = s.Messages[0].ID },
		"draft count":              func(s *storage.PlanningState) { s.Drafts = make([]storage.PlanningDraft, storage.MaxPlanningDrafts+1) },
		"duplicate draft":          func(s *storage.PlanningState) { s.Drafts[1].ID = s.Drafts[0].ID },
		"draft ID":                 func(s *storage.PlanningState) { s.Drafts[0].ID = "spaces invalid" },
		"missing refinement":       func(s *storage.PlanningState) { s.RefiningDraftID = "unknown" },
		"forged created reference": func(s *storage.PlanningState) { s.Drafts[0].CreatedReference = "code-1" },
		"forged created number":    func(s *storage.PlanningState) { s.Drafts[0].CreatedNumber = 1 },
		"encoded state": func(s *storage.PlanningState) {
			for i := range s.Drafts {
				s.Drafts[i].Description = strings.Repeat("\t", storage.MaxPlanningTextBytes)
				s.Drafts[i].Acceptance = strings.Repeat("\t", storage.MaxPlanningTextBytes)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			state := samplePlanningState()
			mutate(&state)
			_, err := writer.CreatePlanningChat(context.Background(), f.main.Project, "bad-state", state)
			planningError(t, err, "invalid_planning_state")
		})
	}
	for _, request := range []string{"", "with space", strings.Repeat("a", 129)} {
		_, err := writer.CreatePlanningChat(context.Background(), f.main.Project, request, samplePlanningState())
		planningError(t, err, "invalid_planning_state")
	}
	assertPelletQueryInt(t, writer.db, "SELECT count(*) FROM planning_chats", 0)
}

func TestPlanningMigrationPreservesExistingQueue(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v18.db")
	legacy, err := openWithMigrations(ctx, path, migrations[:18])
	if err != nil {
		t.Fatal(err)
	}
	database := ProjectDatabase{db: legacy}
	project, _, err := database.RegisterProject(ctx, projectRegistration("old", "repository/.git", "repository", "repository/.git"))
	if err != nil {
		t.Fatal(err)
	}
	repo := PelletRepository{db: legacy}
	pellet, err := repo.CreatePellet(ctx, storage.ResolvedProject{Project: project, Workspace: project.Workspaces[0]}, storage.NewPellet{Title: "existing work", Description: "migration preserves this"})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWebWriter(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := OpenWebReader(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	current, err := reader.ReadWebPellet(ctx, project, pellet.Reference)
	if err != nil || !reflect.DeepEqual(current, pellet) {
		t.Fatalf("migration changed queue: %#v, %v", current, err)
	}
	latest, err := reader.LatestPlanningChat(ctx, project)
	if err != nil || latest != nil {
		t.Fatalf("migration created planning state = %#v, %v", latest, err)
	}
	assertPelletQueryInt(t, writer.db, "SELECT next_pellet_number FROM projects WHERE project_id=?", 2, project.ID)
	createPlanningTestChat(t, writer, project, "after-upgrade", samplePlanningState())
}
