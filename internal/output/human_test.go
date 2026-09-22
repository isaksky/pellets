package output

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"pellets/internal/domain"
)

func TestWriteHuman(t *testing.T) {
	text := "record 界\x1b[2J\x1b[H\rchanged\b\x00\x7f\u009b2J\nnext\tcolumn\n"
	want := "record 界\\u001b[2J\\u001b[H\\u000dchanged\\u0008\\u0000\\u007f\\u009b2J\nnext\tcolumn\n"
	var b bytes.Buffer
	if err := WriteHuman(&b, text); err != nil {
		t.Fatal(err)
	}
	if b.String() != want {
		t.Fatalf("WriteHuman() = %q, want %q", b.String(), want)
	}
	if err := WriteHuman(failingWriter{}, text); !IsWriteFailure(err) || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("WriteHuman() error = %v, want a closed-pipe write failure", err)
	}
}

func TestHumanErrorsKeepUsefulDetailsWithoutInternalCodes(t *testing.T) {
	var b bytes.Buffer
	err := domain.NewError(domain.Usage, "unknown_command", `unknown command "wat"`, map[string]any{"command": "wat"})
	if writeErr := WriteHumanError(&b, err, 0); writeErr != nil || b.String() != "Error: unknown command \"wat\"\n" {
		t.Fatalf("usage error: %v, %q", writeErr, b.String())
	}
	b.Reset()
	err = domain.NewError(domain.Storage, "database_binding_unavailable", "the bound database is unavailable", map[string]any{"database_path": "/project/data/pellets.db"})
	if writeErr := WriteHumanError(&b, err, 0); writeErr != nil || !strings.Contains(b.String(), "database path: /project/data/pellets.db") || strings.Contains(b.String(), "database_binding_unavailable") {
		t.Fatalf("operational error: %v, %q", writeErr, b.String())
	}
}

func TestHumanWrappingAndControlSafety(t *testing.T) {
	text := "abcdefghijklmnopqrstuvwxyz 界界界\n" + "tail\tend\x1b[31m\r\x00\n"
	got := wrapHuman(text, 12)
	if strings.ContainsAny(got, "\x1b\r\x00") || !strings.Contains(strings.ReplaceAll(got, "\n", ""), "\\u001b") || !strings.Contains(got, "tail") {
		t.Fatal(got)
	}
	if unwrapped := wrapHuman(text, 0); !strings.Contains(unwrapped, "abcdefghijklmnopqrstuvwxyz 界界界") {
		t.Fatal(unwrapped)
	}
	// An arbitrarily long redirected result retains all bytes except unsafe controls.
	long := strings.Repeat("complete text 界\n", 1000)
	if wrapHuman(long, 0) != long {
		t.Fatal("redirected content lost")
	}
	var b bytes.Buffer
	if err := RenderDetails(&b, map[string]any{"description": long}); err != nil || !strings.Contains(b.String(), long) {
		t.Fatalf("%v %s", err, b.String())
	}
}
