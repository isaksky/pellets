package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"pellets/internal/codex"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"pellets/internal/storage"
)

func TestExecutionGitDiagnosticIsBoundedSanitizedAndKeepsCause(t *testing.T) {
	cause := errors.New("owned command failure")
	err := newExecutionGitFailure(cause, "\x1b[31mSigning failed\x1b[0m\nAuthorization: Bearer opaque-token\ntoken=private-token\n{\"access_token\":\"quoted secret\"}\nhttps://user:pass@example.invalid/repo\x00"+strings.Repeat("界", 1000))
	summary := executionFailureDiagnostic(err)
	if !errors.Is(err, cause) || !strings.Contains(summary, "Signing failed") || len(summary) > storage.MaxRunSummaryBytes || !utf8.ValidString(summary) {
		t.Fatalf("diagnostic lost cause/content/bound: %q", summary)
	}
	for _, unsafe := range []string{"opaque-token", "private-token", "quoted secret", "user:pass", "\x1b", "\x00", "\n"} {
		if strings.Contains(summary, unsafe) {
			t.Fatalf("unsafe diagnostic content %q", unsafe)
		}
	}
}

func TestExecutionGitDiagnosticRedactsShortUnmatchedQuotedCredentials(t *testing.T) {
	for _, credential := range []string{
		`token="first-secret second-secret`,
		`password='first-secret second-secret`,
		`{"access_token":"first-secret second-secret`,
		`Authorization: Bearer "first-secret second-secret`,
		`Bearer 'first-secret second-secret`,
		`token="first-secret \" second-secret"`,
	} {
		for _, ending := range []string{"", "\nUseful next diagnostic", "\r\nUseful next diagnostic"} {
			summary := executionFailureDiagnostic(newExecutionGitFailure(errors.New("failed"), "Useful initial diagnostic; "+credential+ending))
			if strings.Contains(summary, "first-secret") || strings.Contains(summary, "second-secret") || !strings.Contains(summary, "[redacted]") || !strings.Contains(summary, "Useful initial diagnostic") {
				t.Fatalf("quoted credential escaped: %q", summary)
			}
			if ending != "" && !strings.Contains(summary, "Useful next diagnostic") {
				t.Fatalf("redaction consumed next diagnostic line: %q", summary)
			}
		}
	}
}

func TestOwnedGitDiagnosticHelper(t *testing.T) {
	mode := os.Getenv("PELLETS_LONG_GIT_DIAGNOSTIC")
	if mode == "" {
		return
	}
	credential := strings.Repeat("FAKE-CREDENTIAL-FRAGMENT", 1024)
	switch mode {
	case "token":
		_, _ = os.Stderr.WriteString("token=" + credential)
	case "authorization":
		_, _ = os.Stderr.WriteString("Authorization: Bearer " + credential + "\nUseful signing failure\n")
	case "malformed":
		_, _ = os.Stderr.Write([]byte("token=" + credential + "\xff\xfe\rFAKE-CREDENTIAL-FRAGMENT"))
	}
	os.Exit(9)
}

func TestExecutionGitDiagnosticDoesNotExposeTruncatedCredentials(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"token", "authorization", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.Command(executable, "-test.run=^TestOwnedGitDiagnosticHelper$")
			cmd.Env = append(os.Environ(), "PELLETS_LONG_GIT_DIAGNOSTIC="+mode, "GORACE=atexit_sleep_ms=0")
			_, stderr, err := codex.RunOwnedCommand(ctx, cmd)
			if err == nil {
				t.Fatal("failure helper succeeded")
			}
			failure := newExecutionGitFailure(err, stderr)
			summary := executionFailureDiagnostic(failure)
			if strings.Contains(summary, "CREDENTIAL") || strings.Contains(summary, "FRAGMENT") || strings.Contains(stderr, "CREDENTIAL") || len(summary) > storage.MaxRunSummaryBytes || !utf8.ValidString(summary) {
				t.Fatalf("unsafe failure receipt: %q", summary)
			}
			if mode == "authorization" && !strings.Contains(summary, "Useful signing failure") {
				t.Fatalf("complete diagnostic lost: %q", summary)
			}
		})
	}
}
