package app

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"pellets/internal/domain"
)

//go:embed skill_template/SKILL.md
var pelletsSkillTemplate string

// SkillScope selects whether an installed agent skill belongs to one Git
// worktree or to the current operating-system user.
type SkillScope string

const (
	SkillScopeRepository SkillScope = "repo"
	SkillScopePersonal   SkillScope = "personal"
)

// SkillAgent selects one or both supported portable skill locations.
type SkillAgent string

const (
	SkillAgentCodex  SkillAgent = "codex"
	SkillAgentClaude SkillAgent = "claude"
	SkillAgentBoth   SkillAgent = "both"
)

// SkillEnvironment contains the platform- and Git-discovered roots used by
// the interactive command and deterministic planner.
type SkillEnvironment struct {
	HomeRoot       string
	RepositoryRoot string
}

// SkillTargetPlan is one exact destination and its read-only preflight state.
type SkillTargetPlan struct {
	Agent SkillAgent `json:"agent"`
	Path  string     `json:"path"`
	State string     `json:"state"`
}

// SkillPlan is a complete, write-free installation plan.
type SkillPlan struct {
	Scope          SkillScope
	Agent          SkillAgent
	Root           string
	RepositoryRoot string
	Content        string
	Targets        []SkillTargetPlan
}

// SkillTargetResult reports the result for one planned destination.
type SkillTargetResult struct {
	Agent  SkillAgent `json:"agent"`
	Path   string     `json:"path"`
	Result string     `json:"result"`
}

// SkillApplyResult is returned only after all target writes have succeeded.
type SkillApplyResult struct {
	Status  string
	Targets []SkillTargetResult
}

type appliedSkillTarget struct {
	path     string
	existed  bool
	original []byte
	mode     fs.FileMode
}

// SkillInstaller owns database-independent skill discovery, preflight, and
// filesystem transactions. Its function fields are injected in tests and are
// standard-library implementations in the executable.
type SkillInstaller struct {
	FindGitRoot func(context.Context, string) (string, error)
	UserHomeDir func() (string, error)
	AtomicWrite func(path string, content []byte, mode fs.FileMode) error
}

// PelletsSkillContent returns the exact embedded portable skill artifact.
func PelletsSkillContent() string {
	// Git normally checks the template out with LF through .gitattributes, but
	// normalize defensively so a build from another source archive cannot emit
	// a platform-dependent Agent Skill artifact.
	return strings.ReplaceAll(pelletsSkillTemplate, "\r\n", "\n")
}

// Environment resolves the home directory and optional current Git worktree
// without looking for or opening a Pellets database.
func (installer SkillInstaller) Environment(ctx context.Context, workingDirectory string) (SkillEnvironment, error) {
	homeResolver := installer.UserHomeDir
	if homeResolver == nil {
		homeResolver = os.UserHomeDir
	}
	home, err := homeResolver()
	if err != nil || strings.TrimSpace(home) == "" {
		return SkillEnvironment{}, domain.WrapError(
			domain.Unexpected,
			"home_directory_unavailable",
			"could not determine the current user's home directory",
			nil,
			err,
		)
	}
	home, err = filepath.Abs(filepath.Clean(home))
	if err != nil {
		return SkillEnvironment{}, domain.WrapError(
			domain.Unexpected,
			"home_directory_unavailable",
			"could not resolve the current user's home directory",
			nil,
			err,
		)
	}
	if err := requireDirectory(home, "home directory"); err != nil {
		return SkillEnvironment{}, err
	}

	environment := SkillEnvironment{HomeRoot: home}
	if installer.FindGitRoot == nil {
		return environment, nil
	}
	repositoryRoot, err := installer.FindGitRoot(ctx, workingDirectory)
	if err == nil {
		environment.RepositoryRoot = filepath.Clean(repositoryRoot)
		return environment, nil
	}
	if domain.PublicError(err).Code == "git_repository_not_found" {
		return environment, nil
	}
	return SkillEnvironment{}, err
}

// Plan validates the selected matrix and preflights every destination without
// creating directories, temporary files, or targets.
func (installer SkillInstaller) Plan(environment SkillEnvironment, scope SkillScope, agent SkillAgent) (SkillPlan, error) {
	if scope != SkillScopeRepository && scope != SkillScopePersonal {
		return SkillPlan{}, invalidSkillScope(string(scope))
	}
	if agent != SkillAgentCodex && agent != SkillAgentClaude && agent != SkillAgentBoth {
		return SkillPlan{}, invalidSkillAgent(string(agent))
	}

	root := environment.HomeRoot
	repositoryRoot := ""
	if scope == SkillScopeRepository {
		if environment.RepositoryRoot == "" {
			return SkillPlan{}, domain.NewError(
				domain.Usage,
				"repository_scope_unavailable",
				"repository scope requires the current directory to be inside a Git work tree",
				nil,
			)
		}
		root = environment.RepositoryRoot
		repositoryRoot = environment.RepositoryRoot
	}
	if err := requireDirectory(root, "skill installation root"); err != nil {
		return SkillPlan{}, err
	}

	plan := SkillPlan{
		Scope: scope, Agent: agent, Root: root, RepositoryRoot: repositoryRoot,
		Content: PelletsSkillContent(),
	}
	if agent == SkillAgentCodex || agent == SkillAgentBoth {
		plan.Targets = append(plan.Targets, SkillTargetPlan{
			Agent: SkillAgentCodex,
			Path:  filepath.Join(root, ".agents", "skills", "pellets", "SKILL.md"),
		})
	}
	if agent == SkillAgentClaude || agent == SkillAgentBoth {
		plan.Targets = append(plan.Targets, SkillTargetPlan{
			Agent: SkillAgentClaude,
			Path:  filepath.Join(root, ".claude", "skills", "pellets", "SKILL.md"),
		})
	}

	for index := range plan.Targets {
		state, _, _, err := inspectSkillTarget(root, plan.Targets[index].Path, []byte(plan.Content))
		if err != nil {
			return SkillPlan{}, err
		}
		plan.Targets[index].State = state
	}
	return plan, nil
}

// ConflictError identifies every differing regular target in one stable,
// write-free conflict.
func (installer SkillInstaller) ConflictError(plan SkillPlan) error {
	conflicts := skillConflictDetails(plan)
	if len(conflicts) == 0 {
		return nil
	}
	return domain.NewError(
		domain.Conflict,
		"skill_content_conflict",
		"one or more skill targets contain different content; use --force or confirm replacement interactively",
		map[string]any{"scope": plan.Scope, "targets": conflicts},
	)
}

// Apply re-preflights the complete matrix, then writes each file atomically.
// Any later failure restores every file and directory changed by this call.
func (installer SkillInstaller) Apply(plan SkillPlan, allowReplacement bool) (SkillApplyResult, error) {
	refreshed, err := installer.Plan(
		SkillEnvironment{HomeRoot: plan.Root, RepositoryRoot: plan.RepositoryRoot},
		plan.Scope,
		plan.Agent,
	)
	if err != nil {
		return SkillApplyResult{}, err
	}
	if !allowReplacement {
		if err := installer.ConflictError(refreshed); err != nil {
			return SkillApplyResult{}, err
		}
	}

	createdDirectories := make([]string, 0, len(refreshed.Targets)*3)
	for _, target := range refreshed.Targets {
		if err := createSkillParents(refreshed.Root, filepath.Dir(target.Path), &createdDirectories); err != nil {
			cleanupCreatedDirectories(createdDirectories)
			return SkillApplyResult{}, skillInstallFailure(target.Path, err, nil)
		}
	}

	applied := make([]appliedSkillTarget, 0, len(refreshed.Targets))
	results := make([]SkillTargetResult, 0, len(refreshed.Targets))
	writer := installer.AtomicWrite
	if writer == nil {
		writer = atomicWriteSkillFile
	}
	for _, target := range refreshed.Targets {
		result := SkillTargetResult{Agent: target.Agent, Path: target.Path}
		currentState, original, mode, inspectErr := inspectSkillTarget(refreshed.Root, target.Path, []byte(refreshed.Content))
		if inspectErr != nil {
			return SkillApplyResult{}, rollbackSkillInstallation(applied, createdDirectories, writer, target.Path, inspectErr)
		}
		if currentState == "identical" {
			result.Result = "idempotent"
			results = append(results, result)
			continue
		}
		if currentState == "different" && !allowReplacement {
			conflictPlan := refreshed
			for index := range conflictPlan.Targets {
				if conflictPlan.Targets[index].Path == target.Path {
					conflictPlan.Targets[index].State = "different"
				}
			}
			return SkillApplyResult{}, rollbackSkillInstallation(
				applied, createdDirectories, writer, target.Path, installer.ConflictError(conflictPlan),
			)
		}
		existed := currentState == "different"
		fileMode := fs.FileMode(0o644)
		if existed {
			fileMode = mode.Perm()
		}
		if err := writer(target.Path, []byte(refreshed.Content), fileMode); err != nil {
			return SkillApplyResult{}, rollbackSkillInstallation(applied, createdDirectories, writer, target.Path, err)
		}
		written, err := os.ReadFile(target.Path)
		if err != nil || !bytes.Equal(written, []byte(refreshed.Content)) {
			if err == nil {
				err = errors.New("written skill content did not match the embedded template")
			}
			current := appliedSkillTarget{path: target.Path, existed: existed, original: original, mode: fileMode}
			return SkillApplyResult{}, rollbackSkillInstallation(append(applied, current), createdDirectories, writer, target.Path, err)
		}
		applied = append(applied, appliedSkillTarget{path: target.Path, existed: existed, original: original, mode: fileMode})
		if existed {
			result.Result = "replaced"
		} else {
			result.Result = "installed"
		}
		results = append(results, result)
	}

	status := "idempotent"
	if len(applied) > 0 {
		status = "installed"
	}
	return SkillApplyResult{Status: status, Targets: results}, nil
}

func inspectSkillTarget(root, target string, content []byte) (string, []byte, fs.FileMode, error) {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return "", nil, 0, unsafeSkillPath(target, "could not resolve installation root", err)
	}
	targetAbsolute, err := filepath.Abs(target)
	if err != nil {
		return "", nil, 0, unsafeSkillPath(target, "could not resolve target path", err)
	}
	relative, err := filepath.Rel(rootAbsolute, targetAbsolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", nil, 0, unsafeSkillPath(target, "target escapes the selected skill root", err)
	}

	nearest, err := inspectSkillParents(rootAbsolute, filepath.Dir(targetAbsolute))
	if err != nil {
		return "", nil, 0, err
	}
	info, err := os.Lstat(targetAbsolute)
	if errors.Is(err, os.ErrNotExist) {
		if err := requireWritableDirectory(nearest, targetAbsolute); err != nil {
			return "", nil, 0, err
		}
		return "missing", nil, 0o644, nil
	}
	if err != nil {
		return "", nil, 0, unsafeSkillPath(targetAbsolute, "could not inspect target", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, 0, unsafeSkillPath(targetAbsolute, "target is a symbolic link", nil)
	}
	if !info.Mode().IsRegular() {
		return "", nil, 0, unsafeSkillPath(targetAbsolute, "target is not a regular file", nil)
	}
	original, err := os.ReadFile(targetAbsolute)
	if err != nil {
		return "", nil, 0, unsafeSkillPath(targetAbsolute, "could not read existing target", err)
	}
	if bytes.Equal(original, content) {
		return "identical", original, info.Mode(), nil
	}
	if err := requireWritableDirectory(filepath.Dir(targetAbsolute), targetAbsolute); err != nil {
		return "", nil, 0, err
	}
	return "different", original, info.Mode(), nil
}

func inspectSkillParents(root, parent string) (string, error) {
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return "", unsafeSkillPath(root, "selected skill root is not an accessible directory", err)
	}
	relative, err := filepath.Rel(root, parent)
	if err != nil {
		return "", unsafeSkillPath(parent, "could not resolve target parent", err)
	}
	current := root
	if relative == "." {
		return current, nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return filepath.Dir(current), nil
		}
		if err != nil {
			return "", unsafeSkillPath(current, "could not inspect target parent", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", unsafeSkillPath(current, "target parent is a symbolic link", nil)
		}
		if !info.IsDir() {
			return "", unsafeSkillPath(current, "target parent is not a directory", nil)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			return "", unsafeSkillPath(current, "target parent is not searchable", nil)
		}
	}
	return current, nil
}

func createSkillParents(root, parent string, created *[]string) error {
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return unsafeSkillPath(parent, "target parent escapes the selected skill root", err)
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			*created = append(*created, current)
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return unsafeSkillPath(current, "target parent changed during installation", nil)
		}
	}
	return nil
}

func atomicWriteSkillFile(path string, content []byte, mode fs.FileMode) (returnedError error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".pellets-skill-*")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	closed := false
	defer func() {
		if !closed {
			closeErr := file.Close()
			if returnedError == nil && closeErr != nil {
				returnedError = closeErr
			}
		}
		if returnedError != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := file.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func rollbackSkillInstallation(applied []appliedSkillTarget, createdDirectories []string, writer func(string, []byte, fs.FileMode) error, failedPath string, cause error) error {
	rollbackFailures := make([]string, 0)
	for index := len(applied) - 1; index >= 0; index-- {
		value := applied[index]
		if value.existed {
			if err := writer(value.path, value.original, value.mode); err != nil {
				rollbackFailures = append(rollbackFailures, value.path)
			}
		} else if err := os.Remove(value.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackFailures = append(rollbackFailures, value.path)
		}
	}
	cleanupCreatedDirectories(createdDirectories)
	if len(rollbackFailures) == 0 && domain.PublicError(cause).Kind == domain.Conflict {
		return cause
	}
	return skillInstallFailure(failedPath, cause, rollbackFailures)
}

func cleanupCreatedDirectories(created []string) {
	for index := len(created) - 1; index >= 0; index-- {
		_ = os.Remove(created[index])
	}
}

func requireWritableDirectory(directory, target string) error {
	info, err := os.Stat(directory)
	if err != nil {
		return unsafeSkillPath(target, "could not inspect writable target parent", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o222 == 0 {
		return unsafeSkillPath(target, "target parent is not writable", nil)
	}
	return nil
}

func requireDirectory(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return domain.WrapError(
			domain.Unexpected,
			"skill_root_unavailable",
			fmt.Sprintf("the %s is unavailable", label),
			map[string]any{"path": path},
			err,
		)
	}
	if !info.IsDir() {
		return domain.NewError(
			domain.Conflict,
			"skill_root_unsafe",
			fmt.Sprintf("the %s is not a directory", label),
			map[string]any{"path": path},
		)
	}
	return nil
}

func skillConflictDetails(plan SkillPlan) []map[string]any {
	conflicts := make([]map[string]any, 0)
	for _, target := range plan.Targets {
		if target.State == "different" {
			conflicts = append(conflicts, map[string]any{
				"agent":  target.Agent,
				"path":   target.Path,
				"reason": "content_differs",
			})
		}
	}
	return conflicts
}

func skillInstallFailure(path string, cause error, rollbackFailures []string) error {
	details := map[string]any{"path": path, "rolled_back": len(rollbackFailures) == 0}
	message := "could not install the Pellets skill; prior changes were rolled back"
	if len(rollbackFailures) > 0 {
		details["rollback_failures"] = rollbackFailures
		message = "could not install the Pellets skill and rollback did not complete"
	}
	return domain.WrapError(
		domain.Unexpected,
		"skill_install_failed",
		message,
		details,
		cause,
	)
}

func unsafeSkillPath(path, reason string, cause error) error {
	details := map[string]any{"path": path, "reason": reason}
	if cause != nil {
		return domain.WrapError(domain.Conflict, "skill_target_unsafe", "skill target is unsafe", details, cause)
	}
	return domain.NewError(domain.Conflict, "skill_target_unsafe", "skill target is unsafe", details)
}

func invalidSkillScope(value string) error {
	return domain.NewError(
		domain.Usage,
		"invalid_skill_scope",
		fmt.Sprintf("invalid skill scope %q; expected repo or personal", value),
		map[string]any{"scope": value, "allowed": []string{"repo", "personal"}},
	)
}

func invalidSkillAgent(value string) error {
	return domain.NewError(
		domain.Usage,
		"invalid_skill_agent",
		fmt.Sprintf("invalid skill agent %q; expected codex, claude, or both", value),
		map[string]any{"agent": value, "allowed": []string{"codex", "claude", "both"}},
	)
}
