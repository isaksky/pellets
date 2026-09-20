package storage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildFinalizationMessageValidation(t *testing.T) {
	for _, test := range []struct {
		name, subject, body string
	}{
		{"empty", "", ""}, {"space", "   ", ""},
		{"newline", "Fix parser\nmore", ""}, {"cr", "Fix parser\r", ""},
		{"tab", "Fix\tparser", ""}, {"control", "Fix parser\x1b", ""},
		{"unicode control", "Fix parser\u0085", ""}, {"line separator", "Fix parser\u2028next", ""},
		{"utf8 subject", "Fix \xff", ""}, {"large subject", strings.Repeat("界", 81), ""},
		{"trailing space", "Fix parser ", ""}, {"placeholder", "Implement pellet", ""},
		{"conventional placeholder", "chore: implement pellet", ""}, {"id prefix", "demo-1: Fix parser", ""},
		{"generic", "Update files", ""}, {"todo", "TODO", ""},
		{"punctuation", "...", ""}, {"numeric", "123", ""}, {"spaced placeholder", "Implement   pellet", ""},
		{"large body", "Fix parser", strings.Repeat("x", MaxCommitBodyBytes+1)},
		{"utf8 body", "Fix parser", "bad \xff"}, {"body control", "Fix parser", "bad\x00"},
		{"body cr", "Fix parser", "bad\rline"}, {"body trailer", "Fix parser", "Reason\n\nPeLLeT: other-1"},
		{"spaced trailer", "Fix parser", "Pellet : other-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if message, err := BuildFinalizationMessage(test.subject, test.body, "demo-1"); err == nil || message != "" {
				t.Fatalf("invalid proposal accepted: %q %v", message, err)
			}
		})
	}
	for _, test := range []struct{ subject, body, want string }{
		{"Preserve café and 界", "\r\nKeep `literal` \"quotes\" and $HOME.  \r\n\r\n\t# Detail\r\n", "Preserve café and 界\n\nKeep `literal` \"quotes\" and $HOME.  \n\n\t# Detail\n\nPellet: demo-1\n"},
		{"docs: correct parser usage", "", "docs: correct parser usage\n\nPellet: demo-1\n"},
		{"Document parser usage", " \n\t", "Document parser usage\n\nPellet: demo-1\n"},
	} {
		got, err := BuildFinalizationMessage(test.subject, test.body, "demo-1")
		if err != nil || got != test.want {
			t.Fatalf("canonical message = %q %v, want %q", got, err, test.want)
		}
	}
	if _, err := BuildFinalizationMessage(strings.Repeat("界", 80), strings.Repeat("b", MaxCommitBodyBytes), "demo-1"); err != nil {
		t.Fatal("boundary bytes rejected:", err)
	}
}

func TestFinalizationMessageStorageValidation(t *testing.T) {
	message, err := BuildFinalizationMessage("Preserve parser whitespace", "Keep intentional indentation.\n\nVerified parser cases.", "demo-1")
	if err != nil {
		t.Fatal(err)
	}
	base := FinalizationEvidence{Files: []string{"parser.go"}, Tree: strings.Repeat("a", 40), Subject: "Preserve parser whitespace", MessageVersion: 1, Message: message}
	for _, test := range []struct {
		name   string
		change func(*FinalizationEvidence)
		valid  bool
	}{
		{"canonical", func(*FinalizationEvidence) {}, true},
		{"version", func(f *FinalizationEvidence) { f.MessageVersion = 2 }, false},
		{"downgrade", func(f *FinalizationEvidence) { f.MessageVersion = 0 }, false},
		{"missing", func(f *FinalizationEvidence) { f.Message = "" }, false},
		{"subject mismatch", func(f *FinalizationEvidence) { f.Subject = "Preserve parser comments" }, false},
		{"trailing newline", func(f *FinalizationEvidence) { f.Message += "\n" }, false},
		{"body controls", func(f *FinalizationEvidence) { f.Message = strings.ReplaceAll(f.Message, "indentation", "\x00") }, false},
		{"no trailer", func(f *FinalizationEvidence) { f.Message = f.Subject + "\n\nBody\n" }, false},
		{"overlapping separator", func(f *FinalizationEvidence) { f.Message = f.Subject + "\n\n\nPellet: demo-1\n" }, false},
		{"large", func(f *FinalizationEvidence) { f.Message = strings.Repeat("x", MaxCommitMessageBytes+1) }, false},
		{"legacy", func(f *FinalizationEvidence) {
			f.MessageVersion, f.Message, f.Subject = 0, "", "demo-1: implement pellet"
		}, true},
		{"satisfied", func(f *FinalizationEvidence) { f.NoChanges, f.Files, f.Subject, f.Message = true, nil, "", "" }, true},
		{"satisfied message", func(f *FinalizationEvidence) { f.NoChanges, f.Files = true, nil }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := base
			test.change(&f)
			data, _ := json.Marshal(f)
			var restored FinalizationEvidence
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			err := ValidateRunProgress(RunProgress{Phase: "verification", State: "running", Finalization: &restored})
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v: %v", test.valid, err)
			}
		})
	}
}
