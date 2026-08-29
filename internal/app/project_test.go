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

func TestProjectManagerInitResolvesAllGitIdentityBeforeRegistration(t *testing.T) {
	t.Parallel()

	var calls []string
	wantProject := storage.Project{
		ID: 1, Code: "demo", GitCommonDir: relative("repos/demo/.git"),
		Workspaces: []storage.Workspace{{ID: 1, ProjectID: 1, RootPath: relative("repos/demo"), GitDir: relative("repos/demo/.git")}},
		CreatedAt:  time.Unix(1, 0), UpdatedAt: time.Unix(1, 0),
	}
	database := &fakeProjectDatabase{registerProject: wantProject, registerCreated: true, calls: &calls}
	manager := ProjectManager{
		Discover: ProjectDiscovery{
			FindGitIdentity: func(_ context.Context, workingDirectory string) (domain.GitIdentity, error) {
				calls = append(calls, "find Git identity "+workingDirectory)
				return domain.GitIdentity{WorkTreeRoot: "/workspace/repos/demo", GitCommonDir: "/workspace/repos/demo/.git", GitDir: "/workspace/repos/demo/.git"}, nil
			},
			FindDatabase: func(workingDirectory string) (Database, error) {
				calls = append(calls, "find database "+workingDirectory)
				return Database{}, domain.NewError(domain.NotFound, "database_not_found", "not found", nil)
			},
			NormalizePath: func(databaseRoot, localPath string) (domain.LocalPath, error) {
				calls = append(calls, "normalize "+databaseRoot+" "+localPath)
				switch localPath {
				case "/workspace/repos/demo":
					return relative("repos/demo"), nil
				case "/workspace/repos/demo/.git":
					return relative("repos/demo/.git"), nil
				default:
					return domain.LocalPath{}, errors.New("unexpected path")
				}
			},
			ResolvePath: func(string, domain.LocalPath) (string, error) { return "", errors.New("unexpected resolve") },
			PathExists:  func(string) (bool, error) { return false, errors.New("unexpected exists") },
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
	if !reflect.DeepEqual(project, wantProject) {
		t.Fatalf("Init() project = %#v, want %#v", project, wantProject)
	}
	wantCalls := []string{
		"find Git identity /workspace/repos/demo/nested",
		"find database /workspace/repos/demo/nested",
		"initialize /workspace/repos/demo",
		"normalize /workspace /workspace/repos/demo/.git",
		"normalize /workspace /workspace/repos/demo",
		"normalize /workspace /workspace/repos/demo/.git",
		"reject tracked /workspace /workspace/.pellets/pellets.db",
		"ensure excluded /workspace /workspace/.pellets/pellets.db",
		"open /workspace/.pellets/pellets.db",
		"find workspace repos/demo/.git",
		"register demo repos/demo/.git repos/demo repos/demo/.git move=false",
		"close",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("Init() calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestProjectManagerAllowsMovedWorkspaceOnlyWhenOldPathIsGone(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		oldExists bool
		allowMove bool
		wantCode  string
	}{
		{name: "stale old path permits move", allowMove: true},
		{name: "duplicate live path conflicts", oldExists: true, wantCode: "workspace_identity_conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := &fakeProjectDatabase{
				resolved:        storage.ResolvedProject{Workspace: storage.Workspace{ID: 9, RootPath: relative("old")}},
				registerProject: storage.Project{ID: 1, Code: "demo"},
			}
			if test.wantCode != "" {
				database.registerErr = domain.NewError(domain.Conflict, test.wantCode, "conflict", nil)
			}
			manager := successfulProjectManager(database)
			manager.Discover.ResolvePath = func(_ string, got domain.LocalPath) (string, error) {
				if got != relative("old") {
					t.Fatalf("old path = %#v", got)
				}
				return "/database/old", nil
			}
			manager.Discover.PathExists = func(path string) (bool, error) {
				if path != "/database/old" {
					t.Fatalf("resolved path = %q", path)
				}
				return test.oldExists, nil
			}

			_, err := manager.Init(context.Background(), "/working", "demo")
			if test.wantCode == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantCode != "" && (err == nil || domain.PublicError(err).Code != test.wantCode) {
				t.Fatalf("Init() error = %v, want %s", err, test.wantCode)
			}
			if database.lastRegistration.AllowWorkspaceMove != test.allowMove {
				t.Fatalf("AllowWorkspaceMove = %v, want %v", database.lastRegistration.AllowWorkspaceMove, test.allowMove)
			}
		})
	}
}

func TestResolveCurrentIsReadOnlyAndReturnsProjectAndWorkspace(t *testing.T) {
	t.Parallel()

	want := storage.ResolvedProject{
		Project:   storage.Project{ID: 2, Code: "demo"},
		Workspace: storage.Workspace{ID: 7, ProjectID: 2, RootPath: relative("repository")},
	}
	database := &fakeProjectDatabase{resolved: want}
	manager := successfulProjectManager(database)
	got, err := manager.ResolveCurrent(context.Background(), Database{Root: "/database", Path: "/database/.pellets/pellets.db"}, "/working/nested")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveCurrent() = %#v, want %#v", got, want)
	}
	if database.registerCalls != 0 || database.resolveCalls != 1 {
		t.Fatalf("register calls = %d, resolve calls = %d", database.registerCalls, database.resolveCalls)
	}
}

func TestResolvePelletProjectValidatesSelectionAndReferences(t *testing.T) {
	t.Parallel()

	resolved := storage.ResolvedProject{
		Project:   storage.Project{ID: 2, Code: "foo-bar"},
		Workspace: storage.Workspace{ID: 8, ProjectID: 2, RootPath: relative("linked")},
	}
	for _, test := range []struct {
		name       string
		selected   string
		references []domain.PelletReference
		wantCode   string
	}{
		{name: "current project"},
		{name: "matching selection and reference", selected: "foo-bar", references: []domain.PelletReference{{ProjectCode: "foo-bar", Number: 9}}},
		{name: "different selection", selected: "other", wantCode: "project_selection_mismatch"},
		{name: "different reference", references: []domain.PelletReference{{ProjectCode: "other", Number: 9}}, wantCode: "reference_project_mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := &fakeProjectDatabase{resolved: resolved}
			manager := successfulProjectManager(database)
			got, err := manager.ResolvePelletProject(
				context.Background(),
				Database{Root: "/database", Path: "/database/.pellets/pellets.db"},
				"/working",
				test.selected,
				test.references...,
			)
			if test.wantCode == "" {
				if err != nil || !reflect.DeepEqual(got, resolved) {
					t.Fatalf("ResolvePelletProject() = (%#v, %v), want %#v", got, err, resolved)
				}
			} else if err == nil || domain.PublicError(err).Code != test.wantCode || domain.PublicError(err).Kind != domain.Usage {
				t.Fatalf("ResolvePelletProject() error = %v, want %s", err, test.wantCode)
			}
			if database.registerCalls != 0 || database.resolveCalls != 1 {
				t.Fatalf("register calls = %d, resolve calls = %d", database.registerCalls, database.resolveCalls)
			}
		})
	}
}

func TestProjectManagerInitStopsBeforeSideEffectsOnIdentityFailure(t *testing.T) {
	t.Parallel()
	crossed := false
	manager := successfulProjectManager(&fakeProjectDatabase{})
	manager.Discover.FindGitIdentity = func(context.Context, string) (domain.GitIdentity, error) {
		return domain.GitIdentity{}, domain.NewError(domain.NotFound, "git_repository_not_found", "not found", nil)
	}
	manager.Initialize = func(context.Context, string) (InitializedDatabase, error) {
		crossed = true
		return InitializedDatabase{}, nil
	}
	manager.Open = func(context.Context, string) (storage.ProjectDatabase, error) {
		crossed = true
		return nil, nil
	}
	manager.GitSafety = failingIfCalledProjectGitSafety{called: &crossed}
	_, err := manager.Init(context.Background(), "/working", "demo")
	if err == nil || domain.PublicError(err).Code != "git_repository_not_found" || crossed {
		t.Fatalf("Init() error = %v, crossed=%v", err, crossed)
	}
}

func successfulProjectManager(database storage.ProjectDatabase) ProjectManager {
	return ProjectManager{
		Discover: ProjectDiscovery{
			FindGitIdentity: func(context.Context, string) (domain.GitIdentity, error) {
				return domain.GitIdentity{WorkTreeRoot: "/database/repository", GitCommonDir: "/database/repository/.git", GitDir: "/database/repository/.git/worktrees/current"}, nil
			},
			FindDatabase: func(string) (Database, error) {
				return Database{Root: "/database", Path: "/database/.pellets/pellets.db"}, nil
			},
			NormalizePath: func(_ string, value string) (domain.LocalPath, error) {
				switch value {
				case "/database/repository":
					return relative("repository"), nil
				case "/database/repository/.git":
					return relative("repository/.git"), nil
				case "/database/repository/.git/worktrees/current":
					return relative("repository/.git/worktrees/current"), nil
				default:
					return domain.LocalPath{}, errors.New("unexpected path")
				}
			},
			ResolvePath: func(string, domain.LocalPath) (string, error) { return "", nil },
			PathExists:  func(string) (bool, error) { return false, nil },
		},
		Initialize: func(context.Context, string) (InitializedDatabase, error) {
			return InitializedDatabase{}, errors.New("unexpected initialization")
		},
		Open:      func(context.Context, string) (storage.ProjectDatabase, error) { return database, nil },
		GitSafety: recordingProjectGitSafety{},
	}
}

func relative(value string) domain.LocalPath {
	return domain.LocalPath{Value: value, Relative: true}
}

type recordingProjectGitSafety struct{ calls *[]string }

func (s recordingProjectGitSafety) RejectTracked(_ context.Context, root, path string) error {
	if s.calls != nil {
		*s.calls = append(*s.calls, "reject tracked "+root+" "+path)
	}
	return nil
}
func (s recordingProjectGitSafety) EnsureExcluded(_ context.Context, root, path string) error {
	if s.calls != nil {
		*s.calls = append(*s.calls, "ensure excluded "+root+" "+path)
	}
	return nil
}

type failingIfCalledProjectGitSafety struct{ called *bool }

func (s failingIfCalledProjectGitSafety) RejectTracked(context.Context, string, string) error {
	*s.called = true
	return errors.New("unexpected")
}
func (s failingIfCalledProjectGitSafety) EnsureExcluded(context.Context, string, string) error {
	*s.called = true
	return errors.New("unexpected")
}

type fakeProjectDatabase struct {
	registerProject  storage.Project
	registerCreated  bool
	registerErr      error
	resolved         storage.ResolvedProject
	lookupErr        error
	closeErr         error
	registerCalls    int
	resolveCalls     int
	closeCalls       int
	lastRegistration storage.ProjectRegistration
	calls            *[]string
}

func (d *fakeProjectDatabase) RegisterProject(_ context.Context, registration storage.ProjectRegistration) (storage.Project, bool, error) {
	d.registerCalls++
	d.lastRegistration = registration
	if d.calls != nil {
		*d.calls = append(*d.calls, "register "+registration.Code+" "+registration.GitCommonDir.Value+" "+registration.WorkspaceRoot.Value+" "+registration.GitDir.Value+" move="+map[bool]string{true: "true", false: "false"}[registration.AllowWorkspaceMove])
	}
	return d.registerProject, d.registerCreated, d.registerErr
}
func (*fakeProjectDatabase) ListProjects(context.Context) ([]storage.Project, error) {
	return nil, errors.New("unexpected")
}
func (*fakeProjectDatabase) FindProjectByCode(context.Context, string) (storage.Project, error) {
	return storage.Project{}, errors.New("unexpected")
}
func (d *fakeProjectDatabase) FindWorkspaceByGitDir(_ context.Context, gitDir domain.LocalPath) (storage.ResolvedProject, error) {
	if d.calls != nil {
		*d.calls = append(*d.calls, "find workspace "+gitDir.Value)
	}
	if d.lookupErr != nil {
		return storage.ResolvedProject{}, d.lookupErr
	}
	if d.resolved.Workspace.ID == 0 {
		return storage.ResolvedProject{}, domain.NewError(domain.NotFound, "workspace_not_registered", "missing", nil)
	}
	return d.resolved, nil
}
func (d *fakeProjectDatabase) ResolveProjectWorkspace(context.Context, domain.LocalPath, domain.LocalPath, domain.LocalPath) (storage.ResolvedProject, error) {
	d.resolveCalls++
	return d.resolved, d.lookupErr
}
func (d *fakeProjectDatabase) Close() error {
	d.closeCalls++
	if d.calls != nil {
		*d.calls = append(*d.calls, "close")
	}
	return d.closeErr
}
