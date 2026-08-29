package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestProjectManagerInitUsesInjectedDatabaseNotFoundAndInitialization(t *testing.T) {
	t.Parallel()

	var calls []string
	wantProject := storage.Project{
		Code:      "demo",
		RootPath:  "repos/demo",
		CreatedAt: time.Unix(1, 0),
		UpdatedAt: time.Unix(1, 0),
	}
	database := &fakeProjectDatabase{registerProject: wantProject, registerCreated: true, calls: &calls}
	manager := ProjectManager{
		Discover: ProjectDiscovery{
			FindGitRoot: func(_ context.Context, workingDirectory string) (string, error) {
				calls = append(calls, "find Git root "+workingDirectory)
				return "/workspace/repos/demo", nil
			},
			FindDatabase: func(workingDirectory string) (Database, error) {
				calls = append(calls, "find database "+workingDirectory)
				return Database{}, domain.NewError(domain.NotFound, "database_not_found", "not found", nil)
			},
			RelativeProjectPath: func(databaseRoot, projectRoot string) (string, error) {
				calls = append(calls, "relative path "+databaseRoot+" "+projectRoot)
				return "repos/demo", nil
			},
		},
		Initialize: func(_ context.Context, root string) (InitializedDatabase, error) {
			calls = append(calls, "initialize "+root)
			return InitializedDatabase{Root: "/workspace", Path: "/workspace/.pellets/pellets.db"}, nil
		},
		Open: func(_ context.Context, path string) (storage.ProjectDatabase, error) {
			calls = append(calls, "open "+path)
			return database, nil
		},
		GitSafety: recordingProjectGitSafety{calls: &calls},
	}

	project, err := manager.Init(context.Background(), "/workspace/repos/demo/nested", "demo")
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if project != wantProject {
		t.Fatalf("Init() project = %#v, want %#v", project, wantProject)
	}
	wantCalls := []string{
		"find Git root /workspace/repos/demo/nested",
		"find database /workspace/repos/demo/nested",
		"initialize /workspace/repos/demo",
		"relative path /workspace /workspace/repos/demo",
		"reject tracked /workspace /workspace/.pellets/pellets.db",
		"ensure excluded /workspace /workspace/.pellets/pellets.db",
		"open /workspace/.pellets/pellets.db",
		"register demo repos/demo",
		"close",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("Init() calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestProjectManagerInitStopsAtInjectedDiscoveryFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*ProjectManager)
		wantCode  string
	}{
		{
			name: "Git not found",
			configure: func(manager *ProjectManager) {
				manager.Discover.FindGitRoot = func(context.Context, string) (string, error) {
					return "", domain.NewError(domain.NotFound, "git_repository_not_found", "not found", nil)
				}
			},
			wantCode: "git_repository_not_found",
		},
		{
			name: "project outside database root",
			configure: func(manager *ProjectManager) {
				manager.Discover.RelativeProjectPath = func(string, string) (string, error) {
					return "", domain.NewError(domain.Conflict, "project_outside_database_root", "outside", nil)
				}
			},
			wantCode: "project_outside_database_root",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			crossedSideEffectBoundary := false
			manager := successfulProjectManager(&fakeProjectDatabase{})
			manager.Initialize = func(context.Context, string) (InitializedDatabase, error) {
				crossedSideEffectBoundary = true
				return InitializedDatabase{}, errors.New("unexpected initialization")
			}
			manager.Open = func(context.Context, string) (storage.ProjectDatabase, error) {
				crossedSideEffectBoundary = true
				return nil, errors.New("unexpected open")
			}
			manager.GitSafety = failingIfCalledProjectGitSafety{called: &crossedSideEffectBoundary}
			test.configure(&manager)

			_, err := manager.Init(context.Background(), "/working", "demo")
			if err == nil || domain.PublicError(err).Code != test.wantCode {
				t.Fatalf("Init() error = %v, want %s", err, test.wantCode)
			}
			if crossedSideEffectBoundary {
				t.Fatal("discovery failure crossed an initialization, Git-safety, or storage boundary")
			}
		})
	}
}

func TestProjectManagerInitPropagatesInjectedInitializationFailure(t *testing.T) {
	t.Parallel()

	initializationCalls := 0
	crossedLaterBoundary := false
	manager := successfulProjectManager(&fakeProjectDatabase{})
	manager.Discover.FindDatabase = func(string) (Database, error) {
		return Database{}, domain.NewError(domain.NotFound, "database_not_found", "not found", nil)
	}
	manager.Initialize = func(_ context.Context, root string) (InitializedDatabase, error) {
		initializationCalls++
		if root != "/database/repository" {
			t.Fatalf("initialization root = %q", root)
		}
		return InitializedDatabase{}, domain.NewError(domain.Storage, "database_creation_failed", "creation failed", nil)
	}
	manager.Open = func(context.Context, string) (storage.ProjectDatabase, error) {
		crossedLaterBoundary = true
		return nil, errors.New("unexpected open")
	}
	manager.GitSafety = failingIfCalledProjectGitSafety{called: &crossedLaterBoundary}

	_, err := manager.Init(context.Background(), "/working", "demo")
	if err == nil || domain.PublicError(err).Code != "database_creation_failed" {
		t.Fatalf("Init() error = %v, want database_creation_failed", err)
	}
	if initializationCalls != 1 {
		t.Fatalf("initialization calls = %d, want 1", initializationCalls)
	}
	if crossedLaterBoundary {
		t.Fatal("initialization failure crossed a Git-safety or storage boundary")
	}
}

func TestProjectManagerInitPropagatesRegistrationAndCloseOutcomes(t *testing.T) {
	t.Parallel()

	wantProject := storage.Project{Code: "demo", RootPath: "."}
	tests := []struct {
		name        string
		database    fakeProjectDatabase
		wantProject storage.Project
		wantCode    string
	}{
		{
			name:        "idempotent registration",
			database:    fakeProjectDatabase{registerProject: wantProject, registerCreated: false},
			wantProject: wantProject,
		},
		{
			name: "duplicate code",
			database: fakeProjectDatabase{registerErr: domain.NewError(
				domain.Conflict,
				"project_code_already_registered",
				"duplicate code",
				nil,
			)},
			wantCode: "project_code_already_registered",
		},
		{
			name: "duplicate path",
			database: fakeProjectDatabase{registerErr: domain.NewError(
				domain.Conflict,
				"project_path_already_registered",
				"duplicate path",
				nil,
			)},
			wantCode: "project_path_already_registered",
		},
		{
			name:        "close failure",
			database:    fakeProjectDatabase{registerProject: wantProject, registerCreated: true, closeErr: errors.New("close failed")},
			wantProject: wantProject,
			wantCode:    "database_close_failed",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			database := test.database
			manager := successfulProjectManager(&database)
			project, err := manager.Init(context.Background(), "/working", "demo")
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("Init() error = %v", err)
				}
			} else if err == nil || domain.PublicError(err).Code != test.wantCode {
				t.Fatalf("Init() error = %v, want %s", err, test.wantCode)
			}
			if project != test.wantProject {
				t.Fatalf("Init() project = %#v, want %#v", project, test.wantProject)
			}
			if database.registerCalls != 1 || database.closeCalls != 1 {
				t.Fatalf("registration calls = %d, close calls = %d, want 1 each", database.registerCalls, database.closeCalls)
			}
		})
	}
}

func successfulProjectManager(database storage.ProjectDatabase) ProjectManager {
	return ProjectManager{
		Discover: ProjectDiscovery{
			FindGitRoot: func(context.Context, string) (string, error) {
				return "/database/repository", nil
			},
			FindDatabase: func(string) (Database, error) {
				return Database{Root: "/database", Path: "/database/.pellets/pellets.db"}, nil
			},
			RelativeProjectPath: func(string, string) (string, error) {
				return ".", nil
			},
		},
		Initialize: func(context.Context, string) (InitializedDatabase, error) {
			return InitializedDatabase{}, errors.New("unexpected initialization")
		},
		Open: func(context.Context, string) (storage.ProjectDatabase, error) {
			return database, nil
		},
		GitSafety: recordingProjectGitSafety{},
	}
}

type recordingProjectGitSafety struct {
	calls *[]string
}

func (safety recordingProjectGitSafety) RejectTracked(_ context.Context, root, path string) error {
	if safety.calls != nil {
		*safety.calls = append(*safety.calls, "reject tracked "+root+" "+path)
	}
	return nil
}

func (safety recordingProjectGitSafety) EnsureExcluded(_ context.Context, root, path string) error {
	if safety.calls != nil {
		*safety.calls = append(*safety.calls, "ensure excluded "+root+" "+path)
	}
	return nil
}

type failingIfCalledProjectGitSafety struct {
	called *bool
}

func (safety failingIfCalledProjectGitSafety) RejectTracked(context.Context, string, string) error {
	*safety.called = true
	return errors.New("unexpected Git safety call")
}

func (safety failingIfCalledProjectGitSafety) EnsureExcluded(context.Context, string, string) error {
	*safety.called = true
	return errors.New("unexpected Git safety call")
}

type fakeProjectDatabase struct {
	registerProject storage.Project
	registerCreated bool
	registerErr     error
	closeErr        error
	registerCalls   int
	closeCalls      int
	calls           *[]string
}

func (database *fakeProjectDatabase) RegisterProject(_ context.Context, code, rootPath string) (storage.Project, bool, error) {
	database.registerCalls++
	if database.calls != nil {
		*database.calls = append(*database.calls, "register "+code+" "+rootPath)
	}
	return database.registerProject, database.registerCreated, database.registerErr
}

func (*fakeProjectDatabase) ListProjects(context.Context) ([]storage.Project, error) {
	return nil, errors.New("unexpected ListProjects call")
}

func (*fakeProjectDatabase) FindProjectByCode(context.Context, string) (storage.Project, error) {
	return storage.Project{}, errors.New("unexpected FindProjectByCode call")
}

func (*fakeProjectDatabase) FindProjectByRootPath(context.Context, string) (storage.Project, error) {
	return storage.Project{}, errors.New("unexpected FindProjectByRootPath call")
}

func (database *fakeProjectDatabase) Close() error {
	database.closeCalls++
	if database.calls != nil {
		*database.calls = append(*database.calls, "close")
	}
	return database.closeErr
}
