package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/discovery"
	"pellets/internal/storage/sqlite"
)

type compiledProjectRename struct {
	Status           string            `json:"status"`
	PreviousCode     string            `json:"previous_code"`
	Project          foundationProject `json:"project"`
	RemovedRedirects []struct {
		Code            string `json:"code"`
		CanonicalTarget string `json:"canonical_target"`
	} `json:"removed_redirects"`
}

func TestCompiledProjectRenameCanonicalizesOldReferencesAndNeverPromptsJSON(t *testing.T) {
	if _, err := foundationGitCommand(".", "--version"); err != nil {
		t.Fatalf("compiled rename integration requires native Git: %v", err)
	}
	executable := buildFoundationExecutable(t)
	common := filepath.Join(t.TempDir(), "compiled rename common root")
	foo := filepath.Join(common, "foo")
	other := filepath.Join(common, "other")
	for _, root := range []string{foo, other} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if output, err := foundationGitCommand(root, "init", "--quiet"); err != nil {
			t.Fatalf("git init %s: %v\n%s", root, err, output)
		}
	}
	decodeFoundationSuccess[foundationInitDB](t, runFoundationCLI(t, executable, common, "init-db"), "init-db")
	if project := decodeFoundationSuccess[foundationProject](t, runFoundationCLI(t, executable, foo, "project", "show"), "project show"); project.Code != "foo" {
		t.Fatalf("foo bootstrap = %#v", project)
	}
	if pellet := decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, foo, "add", "compiled stable pellet"), "add"); pellet.ID != "foo-1" {
		t.Fatalf("pre-rename pellet = %#v", pellet)
	}
	renamed := decodeFoundationSuccess[compiledProjectRename](t, runFoundationCLI(t, executable, foo, "project", "rename", "bar"), "project rename")
	if renamed.Status != "renamed" || renamed.PreviousCode != "foo" || renamed.Project.Code != "bar" || len(renamed.Project.Redirects) != 1 || renamed.Project.Redirects[0].Code != "foo" {
		t.Fatalf("compiled rename result = %#v", renamed)
	}
	if pellet := decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, foo, "show", "foo-1"), "show"); pellet.ID != "bar-1" || pellet.Project != "bar" || pellet.Number != 1 {
		t.Fatalf("compiled old reference = %#v", pellet)
	}

	if project := decodeFoundationSuccess[foundationProject](t, runFoundationCLI(t, executable, other, "project", "show"), "project show"); project.Code != "other" {
		t.Fatalf("other bootstrap = %#v", project)
	}
	decodeFoundationSuccess[compiledProjectRename](t, runFoundationCLI(t, executable, other, "project", "rename", "baz"), "project rename")
	conflict := runFoundationCLIWithBlockedStdin(t, executable, common, "--project", "foo", "project", "rename", "other")
	errorResult := decodeFoundationError(t, conflict, 6, "project_rename_confirmation_required", "renaming requires explicit confirmation before conflicting redirect rules can be deleted")
	if got := errorResult.Error.Details["new_code"]; got != "other" {
		t.Fatalf("compiled confirmation details = %#v", errorResult.Error.Details)
	}
	confirmed := decodeFoundationSuccess[compiledProjectRename](t, runFoundationCLI(
		t, executable, common, "--project", "foo", "project", "rename", "other", "--delete-conflicting-redirects", "--yes",
	), "project rename")
	if confirmed.Project.Code != "other" || len(confirmed.RemovedRedirects) != 1 || confirmed.RemovedRedirects[0].Code != "other" || confirmed.RemovedRedirects[0].CanonicalTarget != "baz" {
		t.Fatalf("compiled confirmed rename = %#v", confirmed)
	}
	if pellet := decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, foo, "show", "bar-1"), "show"); pellet.ID != "other-1" || pellet.Project != "other" {
		t.Fatalf("compiled repeatedly renamed reference = %#v", pellet)
	}

	database, err := sqlite.Open(context.Background(), discovery.DatabasePath(common))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	assertFoundationQueryInt(t, database, `SELECT COUNT(*) FROM projects WHERE code = 'other'`, 1)
	assertFoundationQueryInt(t, database, `SELECT COUNT(*) FROM project_code_redirects WHERE code IN ('foo', 'bar') AND project_id = (SELECT project_id FROM projects WHERE code = 'other')`, 2)
	assertFoundationQueryInt(t, database, `SELECT COUNT(*) FROM project_code_redirects WHERE code = 'other'`, 0)
	assertFoundationQueryInt(t, database, `SELECT COUNT(*) FROM pellets WHERE number = 1 AND project_id = (SELECT project_id FROM projects WHERE code = 'other')`, 1)
}
