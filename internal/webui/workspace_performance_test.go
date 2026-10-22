package webui

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"pellets/internal/app"
	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

type countingRunReader struct {
	*sqlite.WebReader
	reads int
}

func (reader *countingRunReader) LatestWebProjectRuns(ctx context.Context, projectID int64) (map[int64]storage.ExecutionRun, error) {
	reader.reads++
	return reader.WebReader.LatestWebProjectRuns(ctx, projectID)
}

func TestWorkspacePagesReadRunsOnceWithoutOpeningExecutionDatabase(t *testing.T) {
	f := newHandlerFixture(t, 1)
	db, err := sqlite.OpenProjectDatabase(context.Background(), f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := range 20 {
		_, _, err := db.RegisterProject(context.Background(), storage.ProjectRegistration{
			Code: f.projects[0].Code, GitCommonDir: f.projects[0].GitCommonDir,
			WorkspaceRoot: domain.LocalPath{Value: fmt.Sprintf("linked-%d", i), Relative: true},
			GitDir:        domain.LocalPath{Value: fmt.Sprintf("project1/.git/worktrees/linked-%d", i), Relative: true},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	reader := &countingRunReader{WebReader: f.application.Reader.(*sqlite.WebReader)}
	f.application.Reader = reader
	supervisor := app.NewExecutionSupervisor(context.Background(), app.SupervisorOptions{
		Recorder: app.ExecutionRecorder{Open: func(context.Context, string) (storage.ExecutionRunDatabase, error) {
			t.Error("page read opened the execution writer and database validation path")
			return nil, fmt.Errorf("execution database must not open for a page read")
		}},
	})
	defer supervisor.Close()
	f.application.Executions = supervisor
	for _, area := range []string{"tasks", "memories", "groups", "workspaces/1"} {
		for _, headers := range []http.Header{nil, {"Datastar-Request": {"true"}, "Pellets-Target": {"live"}}} {
			before := reader.reads
			response := performRequest(f.handler, http.MethodGet, "/projects/project1/"+area, "", headers)
			if response.Code != http.StatusOK || reader.reads-before != 1 {
				t.Fatalf("%s: status=%d batch reads=%d", area, response.Code, reader.reads-before)
			}
		}
	}
}
