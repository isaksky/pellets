package cli

import (
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/discovery"
)

func TestCompiledCLIUsageValidationDoesNotDependOnDatabase(t *testing.T) {
	executable := buildTestExecutable(t)

	withoutDatabase := filepath.Join(t.TempDir(), "without database")
	withDatabase := filepath.Join(t.TempDir(), "with database")
	for _, directory := range []string{withoutDatabase, withDatabase} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if stdout, stderr, exit := runCompiledCLI(t, executable, withDatabase, "init-db"); exit != 0 || stdout != exactInitDBSuccess(discovery.DatabasePath(withDatabase)) || stderr != "" {
		t.Fatalf("compiled fixture init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	tests := []struct {
		name string
		args []string
		code string
	}{
		{name: "forbidden project override", args: []string{"--project", "foo", "project", "list"}, code: "project_not_allowed"},
		{name: "conflicting project selection", args: []string{"--project", "foo", "project", "show", "bar"}, code: "conflicting_project_selection"},
		{name: "malformed global project", args: []string{"--project", "bad_code", "project", "show"}, code: "invalid_project_code"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			withoutStdout, withoutStderr, withoutExit := runCompiledCLI(t, executable, withoutDatabase, test.args...)
			withStdout, withStderr, withExit := runCompiledCLI(t, executable, withDatabase, test.args...)
			if withoutExit != 2 || withoutStdout != "" {
				t.Fatalf("without database = exit %d stdout %q stderr %q", withoutExit, withoutStdout, withoutStderr)
			}
			if withExit != withoutExit || withStdout != withoutStdout || withStderr != withoutStderr {
				t.Fatalf(
					"result depends on database presence:\nwithout = exit %d stdout %q stderr %q\nwith = exit %d stdout %q stderr %q",
					withoutExit,
					withoutStdout,
					withoutStderr,
					withExit,
					withStdout,
					withStderr,
				)
			}
			assertCompactErrorCode(t, withoutStderr, test.code)
		})
	}

	stdout, stderr, exit := runCompiledCLI(t, executable, withoutDatabase, "--project", "foo", "project", "show")
	if exit != 3 || stdout != "" {
		t.Fatalf("valid project show = exit %d stdout %q stderr %q, want discovery failure", exit, stdout, stderr)
	}
	assertCompactErrorCode(t, stderr, "database_not_found")
}
