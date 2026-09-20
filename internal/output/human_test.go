package output

import (
	"bytes"
	"strings"
	"testing"
)

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
