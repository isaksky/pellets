package cli

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"testing"

	"pellets/internal/domain"
)

func TestParseWebOptionsStrictly(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		want WebOptions
		code string
	}{
		{name: "defaults", want: WebOptions{}},
		{name: "free port explicit", args: []string{"--port", "0", "--no-open"}, want: WebOptions{NoOpen: true}},
		{name: "fixed port equals", args: []string{"--port=8123"}, want: WebOptions{Port: 8123}},
		{name: "duplicate", args: []string{"--no-open", "--no-open"}, code: "duplicate_flag"},
		{name: "unknown", args: []string{"--listen", "0.0.0.0"}, code: "unknown_flag"},
		{name: "noncanonical", args: []string{"--port", "080"}, code: "invalid_port"},
		{name: "overflow", args: []string{"--port", "65536"}, code: "invalid_port"},
		{name: "positional", args: []string{"extra"}, code: "unexpected_argument"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parseWebOptions(test.args)
			if test.code != "" {
				if err == nil || publicCode(err) != test.code {
					t.Fatalf("parseWebOptions() error = %v, want %s", err, test.code)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(parsed, test.want) {
				t.Fatalf("parseWebOptions() = (%#v, %v), want %#v", parsed, err, test.want)
			}
		})
	}
}

func TestWebCommandForegroundOwnsOutputAndRejectsFormatGlobals(t *testing.T) {
	t.Parallel()
	var got WebOptions
	command := WebCommand(func(_ context.Context, invocation Invocation, options WebOptions, stdout, stderr io.Writer) error {
		got = options
		_, _ = io.WriteString(stdout, "http://127.0.0.1:8123\n")
		_, _ = io.WriteString(stderr, "warning: test\n")
		if invocation.Globals.Project != "demo" {
			t.Fatalf("project = %q", invocation.Globals.Project)
		}
		return nil
	})
	if command.Run != nil || command.RunForeground == nil {
		t.Fatal("web command did not use the foreground boundary")
	}
	if err := command.Validate(GlobalOptions{Pretty: true}, WebOptions{}); err == nil || publicCode(err) != "format_not_supported" {
		t.Fatalf("pretty validation error = %v", err)
	}
	var stdout, stderr bytes.Buffer
	err := command.RunForeground(context.Background(), Invocation{
		Globals: GlobalOptions{Project: "demo"}, Input: WebOptions{Port: 8123, NoOpen: true},
	}, &stdout, &stderr)
	if err != nil || got != (WebOptions{Port: 8123, NoOpen: true}) {
		t.Fatalf("RunForeground() = (%#v, %v)", got, err)
	}
	if stdout.String() != "http://127.0.0.1:8123\n" || stderr.String() != "warning: test\n" {
		t.Fatalf("foreground output = stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func publicCode(err error) string {
	if err == nil {
		return ""
	}
	return domain.PublicError(err).Code
}
