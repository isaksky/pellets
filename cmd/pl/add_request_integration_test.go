package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestAddRequestCompiledContract(t *testing.T) {
	executable := buildFoundationExecutable(t)
	root := filepath.Join(foundationShortTempDir(t), "requests")
	createFoundationRepository(t, root)
	args := []string{"add", "task", "--request-id", "opaque-key", "--description", "content"}
	first := decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, root, args...), "add")
	retry := decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, root, args...), "add")
	if !reflect.DeepEqual(first, retry) {
		t.Fatalf("first=%#v retry=%#v", first, retry)
	}
	decodeFoundationError(t, runFoundationCLI(t, executable, root, "add", "changed", "--request-id", "opaque-key"), 4, "request_id_conflict", "the request ID was already used with different add inputs")
	next := decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, root, "add", "unkeyed"), "add")
	if next.Number != 2 {
		t.Fatalf("retry allocated number: %#v", next)
	}
	// Receipts survive purge: replay acknowledges the original creation and
	// never recreates a pellet deliberately removed within the retry window.
	decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, root, "close", first.ID), "close")
	decodeFoundationSuccess[foundationPurge](t, runFoundationCLI(t, executable, root, "purge", "--project", first.Project, "--yes"), "purge")
	retry = decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, root, args...), "add")
	if !reflect.DeepEqual(first, retry) {
		t.Fatalf("purge changed receipt: %#v", retry)
	}
	pellets := decodeFoundationSuccess[[]foundationPellet](t, runFoundationCLI(t, executable, root, "list", "--all"), "list")
	if len(pellets) != 1 || pellets[0].ID != next.ID {
		t.Fatalf("replay resurrected pellet: %#v", pellets)
	}
	decodeFoundationSuccess[compiledProjectRename](t, runFoundationCLI(t, executable, root, "project", "rename", "renamed"), "project rename")
	renamed := decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, root, args...), "add")
	if renamed.Project != "renamed" || renamed.ID != "renamed-1" || renamed.Number != first.Number {
		t.Fatalf("renamed receipt=%#v", renamed)
	}

}
