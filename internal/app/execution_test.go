package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"pellets/internal/codex"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func executionFixture(t *testing.T) (ExecutionRecorder, Database, storage.ResolvedProject, storage.RunCapture) {
	t.Helper()
	root := t.TempDir()
	gitForExecutionTest(t, root, "init")
	gitForExecutionTest(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "start")
	database := Database{Root: root, Path: filepath.Join(root, "pellets.db")}
	identity, err := discovery.FindGitIdentity(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	common, err := discovery.NormalizeLocalPath(root, identity.GitCommonDir)
	if err != nil {
		t.Fatal(err)
	}
	gitdir, err := discovery.NormalizeLocalPath(root, identity.GitDir)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := discovery.NormalizeLocalPath(root, identity.WorkTreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := sqlite.OpenProjectDatabase(context.Background(), database.Path)
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := projects.RegisterProject(context.Background(), storage.ProjectRegistration{Code: "demo", GitCommonDir: common, GitDir: gitdir, WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	projects.Close()
	selected := storage.ResolvedProject{Project: project, Workspace: project.Workspaces[0]}
	pellets, err := sqlite.OpenPelletRepository(context.Background(), database.Path)
	if err != nil {
		t.Fatal(err)
	}
	pellet, err := pellets.CreatePellet(context.Background(), selected, storage.NewPellet{Title: "Snapshot", Description: "Original scope"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pellets.TransitionPellet(context.Background(), selected, pellet.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
		t.Fatal(err)
	}
	pellets.Close()
	recorder := ExecutionRecorder{Open: func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
		return sqlite.OpenExecutionRunDatabase(ctx, path)
	}}
	capture := storage.RunCapture{ProjectID: project.ID, WorkspaceID: selected.Workspace.ID, PelletNumber: pellet.Reference.Number, Mode: "run_one", Settings: storage.EffectiveRunSettings{Codex: storage.CodexRunSettings{Executable: "codex", Model: "test-model", Limits: storage.CodexRunLimits{MaxMessageBytes: 4096, EventBuffer: 8, MaxPending: 8, StderrBytes: 1024}}, ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review", SandboxMode: "workspace-write"}}
	return recorder, database, selected, capture
}

func gitForExecutionTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

type evidenceSession struct {
	call func(context.Context, codex.Operation, any) (json.RawMessage, error)
}

func (s evidenceSession) Call(c context.Context, o codex.Operation, p any) (json.RawMessage, error) {
	return s.call(c, o, p)
}
func (evidenceSession) Respond(context.Context, json.RawMessage, any, *codex.RPCError) error {
	return nil
}
func (evidenceSession) Events() <-chan codex.Event     { return nil }
func (evidenceSession) LatestCompletion() *codex.Event { return nil }
func (evidenceSession) Wait(context.Context) error     { return nil }
func (evidenceSession) Close() error                   { return nil }

func TestExecutionRecorderPersistsBeforeCodexCallsAndVerifiesRealGit(t *testing.T) {
	t.Parallel()
	recorder, database, selected, capture := executionFixture(t)
	run, err := recorder.Begin(context.Background(), database, selected, capture)
	if err != nil {
		t.Fatal(err)
	}
	starting := gitForExecutionTest(t, database.Root, "rev-parse", "HEAD")
	if run.StartingHead != starting {
		t.Fatalf("starting head %s", run.StartingHead)
	}
	calls := 0
	session := evidenceSession{call: func(ctx context.Context, op codex.Operation, params any) (json.RawMessage, error) {
		calls++
		saved, err := recorder.Read(ctx, database, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch op {
		case codex.ThreadStart:
			if saved.Phase != "thread_start" {
				t.Fatalf("call preceded durable intent: %#v", saved)
			}
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case codex.TurnStart:
			if saved.Phase != "turn_start" || saved.ThreadID != "thread-1" {
				t.Fatalf("turn preceded conversation save: %#v", saved)
			}
			return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
		case codex.ThreadRead:
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		default:
			t.Fatalf("unexpected call %s", op)
			return nil, nil
		}
	}}
	run, _, err = recorder.CallCodex(context.Background(), database, run.ID, run.Revision, session, codex.ThreadStart, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	prior := run.Revision
	run, _, err = recorder.CallCodex(context.Background(), database, run.ID, run.Revision, session, codex.TurnStart, map[string]any{"threadId": "thread-1", "input": []any{"secret transcript content must remain outside SQLite"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := recorder.CallCodex(context.Background(), database, run.ID, prior, session, codex.TurnStart, map[string]any{"threadId": "thread-1"}); err == nil || calls != 2 {
		t.Fatalf("stale intent called Codex: %d %v", calls, err)
	}
	gitForExecutionTest(t, database.Root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "result")
	commit := gitForExecutionTest(t, database.Root, "rev-parse", "HEAD")
	if _, err := recorder.VerifyCommit(context.Background(), database, run.ID, run.Revision, strings.Repeat("f", 40)); domain.PublicError(err).Code != "commit_unavailable" {
		t.Fatalf("missing commit %v", err)
	}
	run, err = recorder.VerifyCommit(context.Background(), database, run.ID, run.Revision, commit)
	if err != nil {
		t.Fatal(err)
	}
	progress := run.RunProgress
	progress.State = "completed"
	progress.Outcome = "succeeded"
	run, err = recorder.Save(context.Background(), database, storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: progress})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := recorder.InspectEvidence(context.Background(), database, run.ID, session)
	if err != nil || evidence.ResultCommit != "available" || evidence.StartingCommit != "available" || evidence.Conversation != "available" {
		t.Fatalf("evidence %#v %v", evidence, err)
	}
	// Moving HEAD later must not make review silently target a different commit.
	gitForExecutionTest(t, database.Root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "later")
	evidence, err = recorder.InspectEvidence(context.Background(), database, run.ID, nil)
	if err != nil || evidence.Run.ResultCommit != commit || evidence.Conversation != "unchecked" {
		t.Fatalf("retargeted evidence %#v %v", evidence, err)
	}
	missingSession := evidenceSession{call: func(context.Context, codex.Operation, any) (json.RawMessage, error) {
		return nil, errors.New("transcript is unavailable")
	}}
	evidence, err = recorder.InspectEvidence(context.Background(), database, run.ID, missingSession)
	if err != nil || evidence.Conversation != "unavailable" || evidence.Run.Outcome != "succeeded" {
		t.Fatalf("missing transcript %#v %v", evidence, err)
	}
	// A missing exact Git object is reported even though historical success is retained.
	objectPath := filepath.Join(database.Root, ".git", "objects", commit[:2], commit[2:])
	if err := os.Rename(objectPath, objectPath+".unavailable"); err != nil {
		t.Fatal(err)
	}
	evidence, err = recorder.InspectEvidence(context.Background(), database, run.ID, nil)
	if err != nil || evidence.ResultCommit != "unavailable" || evidence.Run.ResultCommit != commit {
		t.Fatalf("missing Git evidence %#v %v", evidence, err)
	}
}

func TestExecutionRecorderUncertainCallsKeepPhaseAndExcludeRawErrors(t *testing.T) {
	t.Parallel()
	recorder, database, selected, capture := executionFixture(t)
	run, err := recorder.Begin(context.Background(), database, selected, capture)
	if err != nil {
		t.Fatal(err)
	}
	session := evidenceSession{call: func(context.Context, codex.Operation, any) (json.RawMessage, error) {
		return nil, errors.New("credential=do-not-store full transcript text")
	}}
	run, _, err = recorder.CallCodex(context.Background(), database, run.ID, run.Revision, session, codex.ThreadStart, map[string]any{})
	if err == nil || run.State != "needs_attention" || run.Outcome != "unknown" || run.Phase != "thread_start" || run.ErrorCode != "codex_call_unconfirmed" {
		t.Fatalf("uncertain call %#v %v", run, err)
	}
	data, _ := json.Marshal(run)
	if strings.Contains(string(data), "credential=") || strings.Contains(string(data), "transcript text") {
		t.Fatal("raw error persisted")
	}
	if _, err := recorder.Read(context.Background(), database, run.ID+999); domain.PublicError(err).Code != "execution_run_not_found" {
		t.Fatalf("missing run %v", err)
	}
}

func TestExecutionRecorderPendingCallsSerializeAndMergeConcurrentProgress(t *testing.T) {
	t.Parallel()
	for _, operation := range []codex.Operation{codex.ThreadStart, codex.TurnStart, codex.TurnInterrupt} {
		t.Run(string(operation), func(t *testing.T) {
			recorder, database, selected, capture := executionFixture(t)
			run, err := recorder.Begin(context.Background(), database, selected, capture)
			if err != nil {
				t.Fatal(err)
			}
			params := map[string]any{}
			if operation != codex.ThreadStart {
				progress := run.RunProgress
				progress.ThreadID, progress.TurnID = "thread", "prior-turn"
				run, err = recorder.Save(context.Background(), database, storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: progress})
				if err != nil {
					t.Fatal(err)
				}
				params["threadId"], params["turnId"] = "thread", "prior-turn"
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			session := evidenceSession{call: func(context.Context, codex.Operation, any) (json.RawMessage, error) {
				if calls.Add(1) != 1 {
					return nil, errors.New("overlapping call dispatched")
				}
				close(entered)
				<-release
				switch operation {
				case codex.ThreadStart:
					return json.RawMessage(`{"thread":{"id":"new-thread"}}`), nil
				case codex.TurnStart:
					return json.RawMessage(`{"turn":{"id":"new-turn"}}`), nil
				default:
					return json.RawMessage(`{}`), nil
				}
			}}
			type callResult struct {
				run storage.ExecutionRun
				err error
			}
			finished := make(chan callResult, 1)
			go func() {
				updated, _, err := recorder.CallCodex(context.Background(), database, run.ID, run.Revision, session, operation, params)
				finished <- callResult{updated, err}
			}()
			<-entered
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			pending, err := recorder.Read(context.Background(), database, run.ID)
			if err != nil || pending.PendingOperation != string(operation) || pending.PendingRevision != pending.Revision {
				t.Fatalf("durable intent %#v %v", pending, err)
			}
			if !storage.RunActive(pending.State) || pending.FinishedAt != nil {
				t.Fatalf("intent claimed external completion: %#v", pending)
			}
			progress := pending.RunProgress
			progress.Phase, progress.State, progress.Summary = "implementation", "awaiting_input", "Concurrent event needs an answer"
			concurrent, err := recorder.Save(context.Background(), database, storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: pending.Revision, Progress: progress})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := recorder.CallCodex(context.Background(), database, run.ID, concurrent.Revision, session, operation, params); domain.PublicError(err).Code != "execution_run_conflict" || calls.Load() != 1 {
				t.Fatalf("overlapping dispatch: %d %v", calls.Load(), err)
			}
			close(release)
			result := <-finished
			if result.err != nil {
				t.Fatal(result.err)
			}
			merged := result.run
			if merged.State != progress.State || merged.Phase != progress.Phase || merged.Summary != progress.Summary || merged.PendingOperation != "" || merged.PendingRevision != 0 || merged.FinishedAt != nil {
				t.Fatalf("response lost concurrent progress: %#v", merged)
			}
			if operation == codex.ThreadStart && merged.ThreadID != "new-thread" {
				t.Fatalf("thread response lost: %#v", merged)
			}
			if operation == codex.TurnStart && merged.TurnID != "new-turn" {
				t.Fatalf("turn response lost: %#v", merged)
			}
			if operation == codex.TurnInterrupt {
				found := false
				for _, activity := range merged.Activity {
					if activity.Revision == pending.PendingRevision && activity.Operation == "turn/interrupt" {
						found = true
					}
				}
				if !found {
					t.Fatal("interrupt request has no durable operation evidence")
				}
			}
		})
	}
}
