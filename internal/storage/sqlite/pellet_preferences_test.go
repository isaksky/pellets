package sqlite

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestPelletPreferencesRoundTripAndScope(t *testing.T) {
	f := newPelletRepositoryFixture(t)
	r := f.open(t)
	defer r.Close()
	ctx := context.Background()
	model, effort, id := "custom-model", "high", "preferences-request"
	p, err := r.CreatePellet(ctx, f.main, storage.NewPellet{Title: "preferences", Model: &model, ReasoningEffort: &effort, RequestID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.ExecutionPreferences(), &storage.PelletExecutionPreferences{Model: &model, ReasoningEffort: &effort}) {
		t.Fatal(p)
	}
	replay, err := r.CreatePellet(ctx, f.main, storage.NewPellet{Title: "preferences", Model: &model, ReasoningEffort: &effort, RequestID: &id})
	if err != nil || replay.Reference != p.Reference {
		t.Fatal(replay, err)
	}
	other := "other"
	if _, err = r.CreatePellet(ctx, f.main, storage.NewPellet{Title: "preferences", Model: &other, ReasoningEffort: &effort, RequestID: &id}); err == nil {
		t.Fatal("changed request reused receipt")
	}
	rows, err := r.SearchPellets(ctx, f.main, storage.PelletSearchOptions{Query: "preferences"})
	if err != nil || len(rows) != 1 || rows[0].Model == nil || *rows[0].Model != model {
		t.Fatal(rows, err)
	}
	version := storage.PelletVersion(p)
	updated, err := r.UpdateWebPellet(ctx, f.main, p.Reference, version, storage.PelletChanges{Model: storage.NullableTextChange{Set: true}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Model != nil || updated.ReasoningEffort == nil || *updated.ReasoningEffort != effort || updated.ImplementationRevision != p.ImplementationRevision || storage.PelletVersion(updated) == version {
		t.Fatal(updated)
	}
	if _, err = r.UpdateWebPellet(ctx, f.main, p.Reference, version, storage.PelletChanges{ReasoningEffort: storage.NullableTextChange{Set: true}}); err == nil {
		t.Fatal("stale edit accepted")
	}
	updated, err = r.UpdatePellet(ctx, f.main, p.Reference, storage.PelletChanges{ReasoningEffort: storage.NullableTextChange{Set: true}})
	if err != nil || updated.ReasoningEffort != nil {
		t.Fatal(updated, err)
	}
	for _, bad := range []string{"", " spaced ", "line\nbreak"} {
		if _, err = r.UpdatePellet(ctx, f.main, p.Reference, storage.PelletChanges{Model: storage.NullableTextChange{Set: true, Value: &bad}}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
func TestPelletPreferencesMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v24.db")
	db, err := openWithMigrations(ctx, path, migrations[:24])
	if err != nil {
		t.Fatal(err)
	}
	pd := ProjectDatabase{db: db}
	_, _, err = pd.RegisterProject(ctx, projectRegistration("old", "repository/.git", "repository", "repository/.git"))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO pellets(project_id,number,title,priority,created_at,updated_at) VALUES(1,1,'legacy',1024,2460000,2460000); INSERT INTO pellets_fts(rowid,title,description,external_id) SELECT rowid,title,description,external_id FROM pellets;`)
	db.Close()
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	p, err := loadPellet(ctx, upgraded, 1, 1)
	if err != nil || p.Model != nil || p.ReasoningEffort != nil {
		t.Fatal(p, err)
	}
	assertPragmaInt(t, upgraded, "user_version", LatestSchemaVersion)
}
func TestExecutionPreferenceSnapshotRejectsConcurrentEdit(t *testing.T) {
	f := newPelletRepositoryFixture(t)
	r := f.open(t)
	defer r.Close()
	ctx := context.Background()
	p, err := r.CreatePellet(ctx, f.main, storage.NewPellet{Title: "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.TransitionPellet(ctx, f.main, p.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
		t.Fatal(err)
	}
	capture := runCapture(f.main.Project.ID, f.main.Workspace.ID, p.Reference.Number)
	capture.ExpectedPreferences = p.ExecutionPreferences()
	effort := "medium"
	if _, err = r.UpdatePellet(ctx, f.main, p.Reference, storage.PelletChanges{ReasoningEffort: storage.NullableTextChange{Set: true, Value: &effort}}); err != nil {
		t.Fatal(err)
	}
	db := ProjectDatabase{db: r.db}
	if _, err = db.CreateExecutionRun(ctx, capture); err == nil {
		t.Fatal("preflight race accepted")
	}
	assertPelletQueryInt(t, r.db, "SELECT count(*) FROM execution_runs", 0)
	capture.ExpectedPreferences.ReasoningEffort = &effort
	if _, err = db.CreateExecutionRun(ctx, capture); err != nil {
		t.Fatal(err)
	}
	p, err = r.ReadPellet(ctx, f.main, domain.PelletReference{ProjectCode: f.main.Project.Code, Number: p.Reference.Number})
	if err != nil || p.ImplementationRevision != 1 {
		t.Fatal(p, err)
	}
}

func TestLegacyCreationReceiptInheritsPreferences(t *testing.T) {
	f := newPelletRepositoryFixture(t)
	r := f.open(t)
	defer r.Close()
	ctx := context.Background()
	// The exact pre-migration creation payload omits both new fields.
	legacy := `{"RequestID":null,"Title":"legacy","Description":"","ExternalID":null,"Group":null,"Status":"open","Placement":null}`
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(legacy)))
	receipt := fmt.Sprintf(`{"version":1,"pellet":{"ProjectID":%d,"Reference":{"ProjectCode":"code","Number":41},"Title":"legacy","Status":"open"}}`, f.main.Project.ID)
	if _, err := r.db.ExecContext(ctx, `INSERT INTO pellet_add_requests(project_id,request_id,fingerprint,result_json,created_at) VALUES(?,?,?,?,julianday('now'))`, f.main.Project.ID, "old-request", fingerprint, receipt); err != nil {
		t.Fatal(err)
	}
	requestID := "old-request"
	p, err := r.CreatePellet(ctx, f.main, storage.NewPellet{Title: "legacy", RequestID: &requestID})
	if err != nil || p.Reference.Number != 41 || p.Model != nil || p.ReasoningEffort != nil {
		t.Fatal(p, err)
	}
	assertPelletQueryInt(t, r.db, "SELECT count(*) FROM pellets", 0)
}
