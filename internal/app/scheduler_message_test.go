package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func writeMessageFixture(t *testing.T, root string, fields map[string]any) {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake-commit-message.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func assertRawCommitMessage(t *testing.T, root, want string) {
	t.Helper()
	object, err := executionGitRaw(context.Background(), root, "cat-file", "commit", "HEAD")
	_, got, ok := strings.Cut(object, "\n\n")
	if err != nil || !ok || got != want {
		t.Fatalf("commit message = %q, want %q: %v", got, want, err)
	}
}

func TestSchedulerStandaloneCommitMessages(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, test := range []struct{ subject, body, path, content, canonicalBody string }{
		{"Document literal shell syntax for café paths", "\r\nShell examples previously omitted Unicode paths and literal metacharacters.\r\nKeep \"quotes\", `backticks`, $HOME, and $(touch fake-expanded) visible.  \r\n\r\n# Example\r\n\tUse the documented literal syntax.\r\n", "shell-examples.md", "Literal café path: \"$HOME\" `backticks`\n", "Shell examples previously omitted Unicode paths and literal metacharacters.\nKeep \"quotes\", `backticks`, $HOME, and $(touch fake-expanded) visible.  \n\n# Example\n\tUse the documented literal syntax."},
		{"fix(config): disable caching for local previews", "Preview content could remain stale after edits.\nDisable the preview cache so changes appear on reload.", "preview.json", "{\"cache\": false}\n", "Preview content could remain stale after edits.\nDisable the preview cache so changes appear on reload."},
		{"Correct the preview command example", "", "preview.md", "Run preview with --no-open.\n", ""},
	} {
		t.Run(test.subject, func(t *testing.T) {
			s, request, _ := schedulerFixture(t, executable, "schedule_success")
			root := s.options.Database.Root
			// Repository cleanup/encoding preferences cannot silently rewrite
			// literal proposed bytes; conventional subjects are accepted as-is.
			gitForExecutionTest(t, root, "config", "commit.cleanup", "strip")
			gitForExecutionTest(t, root, "config", "i18n.logOutputEncoding", "ISO-8859-1")
			writeMessageFixture(t, root, map[string]any{"commit_subject": test.subject, "commit_body": test.body, "path": test.path, "content": test.content})
			status := awaitSchedule(t, startSchedule(t, s, request))
			if status.State != "completed" {
				t.Fatalf("%+v", status)
			}
			run, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, status.RunID)
			if err != nil {
				t.Fatal(err)
			}
			want := test.subject + "\n\n"
			if test.canonicalBody != "" {
				want += test.canonicalBody + "\n\n"
			}
			want += "Pellet: demo-1\n"
			if run.Finalization.MessageVersion != 1 || run.Finalization.Message != want || run.Finalization.Subject != test.subject || run.PelletTitle == test.subject {
				t.Fatalf("delivered result replaced by task title or lost: %+v", run.Finalization)
			}
			assertRawCommitMessage(t, root, want)
			if content, err := os.ReadFile(filepath.Join(root, test.path)); err != nil || string(content) != test.content {
				t.Fatalf("delivered file: %q %v", content, err)
			}
			if _, err := os.Stat(filepath.Join(root, "fake-expanded")); !os.IsNotExist(err) {
				t.Fatal("message was shell-expanded")
			}
			for _, event := range readPeerEvents(t, root) {
				if event.Method != "turn/start" {
					continue
				}
				var params struct {
					Input        []struct{ Text string }
					OutputSchema map[string]any
				}
				if err := json.Unmarshal(event.Params, &params); err != nil {
					t.Fatal(err)
				}
				wantSchema, _ := json.Marshal(implementationSchema())
				gotSchema, _ := json.Marshal(params.OutputSchema)
				if string(gotSchema) != string(wantSchema) {
					t.Fatal("turn omitted commit result schema")
				}
				for _, required := range []string{"commit_subject", "commit_body", "repository commit conventions", "scope adjustments", "Do not supply a Pellet trailer", "Do not paste descriptions", "already_satisfied has no message requirement"} {
					if !strings.Contains(params.Input[0].Text, required) {
						t.Fatalf("missing prompt contract: %s", required)
					}
				}
				if strings.Contains(params.Input[0].Text, "concise commit containing the pellet ID") {
					t.Fatal("obsolete commit contract")
				}
			}
		})
	}
}

func TestSchedulerRejectsInvalidMessagesBeforeGitMutation(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, test := range []struct {
		name   string
		fields map[string]any
	}{
		{"missing fields", map[string]any{}},
		{"missing body", map[string]any{"commit_subject": "Record the verified result"}},
		{"missing subject", map[string]any{"commit_body": "Reason"}},
		{"null subject", map[string]any{"commit_subject": nil, "commit_body": ""}},
		{"empty subject", map[string]any{"commit_subject": "", "commit_body": ""}},
		{"multiline", map[string]any{"commit_subject": "Fix parser\nbody", "commit_body": ""}},
		{"controls", map[string]any{"commit_subject": "Fix parser\x1b", "commit_body": ""}},
		{"body controls", map[string]any{"commit_subject": "Fix parser", "commit_body": "invalid\x00"}},
		{"placeholder", map[string]any{"commit_subject": "implement pellet", "commit_body": ""}},
		{"id prefix", map[string]any{"commit_subject": "demo-1: Fix parser", "commit_body": ""}},
		{"large subject", map[string]any{"commit_subject": strings.Repeat("s", 241), "commit_body": ""}},
		{"large body", map[string]any{"commit_subject": "Fix parser", "commit_body": strings.Repeat("b", storage.MaxCommitBodyBytes+1)}},
		{"forged trailer", map[string]any{"commit_subject": "Fix parser", "commit_body": "Pellet: other-3"}},
		{"spaced forged trailer", map[string]any{"commit_subject": "Fix parser", "commit_body": "Pellet : other-3"}},
		{"wrong type", map[string]any{"commit_subject": 123, "commit_body": ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, request, q := schedulerFixture(t, executable, "schedule_success")
			root := s.options.Database.Root
			head := gitForExecutionTest(t, root, "rev-parse", "HEAD")
			writeMessageFixture(t, root, test.fields)
			status := awaitSchedule(t, startSchedule(t, s, request))
			if status.State != "needs_attention" || (status.Reason != "implementation_message_invalid" && status.Reason != "implementation_report_invalid") {
				t.Fatalf("%+v", status)
			}
			run, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, status.RunID)
			if err != nil || run.Finalization != nil || run.ResultCommit != "" {
				t.Fatalf("invalid message persisted: %+v %v", run, err)
			}
			if got := gitForExecutionTest(t, root, "rev-parse", "HEAD"); got != head {
				t.Fatal("invalid proposal committed")
			}
			if staged := gitForExecutionTest(t, root, "diff", "--cached", "--name-only"); staged != "" {
				t.Fatalf("invalid proposal staged: %s", staged)
			}
			pellet, err := q.ReadPellet(context.Background(), request.Selected, domain.PelletReference{ProjectCode: "demo", Number: 1})
			if err != nil || pellet.Status != domain.PelletInProgress {
				t.Fatalf("invalid proposal closed: %+v %v", pellet, err)
			}
			if _, err := os.Stat(filepath.Join(root, "demo-1.txt")); err != nil {
				t.Fatal("unfinished edits lost", err)
			}
			if test.name == "missing fields" {
				writeMessageFixture(t, root, map[string]any{"commit_subject": "Record the verified implementation result", "commit_body": "Retain the result for readers of the repository."})
				request.ResumeFrom, request.ResumePellet = &run.ID, &run.PelletNumber
				corrected := awaitSchedule(t, startSchedule(t, s, request))
				if corrected.State != "completed" {
					t.Fatalf("correction failed: %+v", corrected)
				}
				assertRawCommitMessage(t, root, "Record the verified implementation result\n\nRetain the result for readers of the repository.\n\nPellet: demo-1\n")
			}
		})
	}
}

func TestFinalizationRejectsHookChangedMessageAndResumeDoesNotRecommit(t *testing.T) {
	for _, change := range []string{"subject", "body", "trailer"} {
		t.Run(change, func(t *testing.T) {
			s, request, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_success")
			root := s.options.Database.Root
			hook := filepath.Join(root, ".git", "hooks", "commit-msg")
			script := "#!/bin/sh\nprintf '\\nHook-added body\\n' >> \"$1\"\n"
			if change == "subject" {
				script = "#!/bin/sh\nprintf 'Replaced subject\\n' > \"$1\"\n"
			}
			if change == "trailer" {
				script = "#!/bin/sh\nprintf '\\nPellet: other-7\\n' >> \"$1\"\n"
			}
			if err := os.WriteFile(hook, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			first := awaitSchedule(t, startSchedule(t, s, request))
			if first.State != "needs_attention" || first.Reason != "finalization_message_changed" {
				t.Fatalf("%+v", first)
			}
			previous, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, first.RunID)
			if err != nil || previous.ResultCommit != "" || previous.Finalization == nil {
				t.Fatalf("%+v %v", previous, err)
			}
			if err := os.Remove(hook); err != nil {
				t.Fatal(err)
			}
			request.ResumeFrom, request.ResumePellet = &previous.ID, &previous.PelletNumber
			second := awaitSchedule(t, startSchedule(t, s, request))
			if second.State != "needs_attention" || second.Reason != "finalization_message_changed" {
				t.Fatalf("mismatch accepted: %+v", second)
			}
			if count := gitForExecutionTest(t, root, "rev-list", "--count", previous.StartingHead+"..HEAD"); count != "1" {
				t.Fatal("recommitted", count)
			}
			pellet, err := q.ReadPellet(context.Background(), request.Selected, domain.PelletReference{ProjectCode: "demo", Number: 1})
			if err != nil || pellet.Status != domain.PelletInProgress {
				t.Fatalf("wrong message closed pellet: %+v %v", pellet, err)
			}
		})
	}
}

type legacyFinalizationDatabase struct{ storage.ExecutionRunDatabase }

func (db legacyFinalizationDatabase) UpdateExecutionRun(ctx context.Context, update storage.UpdateExecutionRun) (storage.ExecutionRun, error) {
	if update.Progress.Phase == "verification" && update.Progress.Finalization != nil {
		f := *update.Progress.Finalization
		f.MessageVersion, f.Message, f.Subject = 0, "", "demo-1: implement pellet"
		update.Progress.Finalization = &f
	}
	return db.ExecutionRunDatabase.UpdateExecutionRun(ctx, update)
}

func TestLegacySubjectOnlyFinalizationRecovery(t *testing.T) {
	for _, boundary := range []string{"before_staging", "after_commit"} {
		t.Run(boundary, func(t *testing.T) {
			s, request, _ := schedulerFixture(t, installSupervisorPeer(t), "schedule_success")
			open := s.options.Supervisor.options.Recorder.Open
			var failed atomic.Bool
			s.options.Supervisor.options.Recorder.Open = func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
				db, err := open(ctx, path)
				if err != nil {
					return nil, err
				}
				return legacyFinalizationDatabase{finalizationFailureDatabase{ExecutionRunDatabase: db, fail: func(update storage.UpdateExecutionRun) bool {
					atBoundary := boundary == "before_staging" && update.Progress.Phase == "commit" || boundary == "after_commit" && update.VerifiedCommit != ""
					return atBoundary && failed.CompareAndSwap(false, true)
				}}}, nil
			}
			first := awaitSchedule(t, startSchedule(t, s, request))
			previous, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, first.RunID)
			if err != nil || first.State != "needs_attention" || !failed.Load() || previous.Finalization == nil {
				t.Fatalf("%+v %v", first, err)
			}
			if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_failed"), 0600); err != nil {
				t.Fatal(err)
			}
			request.ResumeFrom, request.ResumePellet = &previous.ID, &previous.PelletNumber
			second := awaitSchedule(t, startSchedule(t, s, request))
			if second.State != "completed" {
				t.Fatalf("legacy recovery: %+v", second)
			}
			resumed, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, second.RunID)
			if err != nil || !reflect.DeepEqual(resumed.Finalization, previous.Finalization) {
				t.Fatal("legacy evidence replaced", err)
			}
			assertRawCommitMessage(t, s.options.Database.Root, "demo-1: implement pellet\n")
			if count := gitForExecutionTest(t, s.options.Database.Root, "rev-list", "--count", previous.StartingHead+"..HEAD"); count != "1" {
				t.Fatal("legacy recommitted", count)
			}
		})
	}
}
