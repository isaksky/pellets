package sqlite

import (
	"context"
	"reflect"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

type lifecycleMatrixSource string

const (
	matrixOpen       lifecycleMatrixSource = "open"
	matrixInProgress lifecycleMatrixSource = "in_progress"
	matrixClosed     lifecycleMatrixSource = "closed"
	matrixMaybeLater lifecycleMatrixSource = "maybe_later"
)

type lifecycleMatrixCaller string

const (
	matrixCurrentOwner lifecycleMatrixCaller = "current_owner"
	matrixForeignOwner lifecycleMatrixCaller = "foreign_owner"
)

type lifecycleMatrixRecovery string

const (
	matrixNoRecovery           lifecycleMatrixRecovery = ""
	matrixExactOwnerRecovery   lifecycleMatrixRecovery = "exact_owner"
	matrixWrongOwnerRecovery   lifecycleMatrixRecovery = "wrong_owner"
	matrixMissingOwnerRecovery lifecycleMatrixRecovery = "missing_owner"
)

type lifecycleMatrixPriority string

const (
	matrixPriorityPreserved lifecycleMatrixPriority = "preserved"
	matrixPriorityCleared   lifecycleMatrixPriority = "cleared"
	matrixPriorityAppended  lifecycleMatrixPriority = "appended"
)

type lifecycleMatrixCompletion string

const (
	matrixCompletionCleared   lifecycleMatrixCompletion = "cleared"
	matrixCompletionSet       lifecycleMatrixCompletion = "set"
	matrixCompletionPreserved lifecycleMatrixCompletion = "preserved"
)

type lifecycleMatrixCase struct {
	name           string
	operation      storage.PelletLifecycleOperation
	source         lifecycleMatrixSource
	caller         lifecycleMatrixCaller
	recovery       lifecycleMatrixRecovery
	busyCaller     bool
	wantCode       string
	wantStatus     domain.PelletStatus
	wantOwner      bool
	wantPriority   lifecycleMatrixPriority
	wantCompletion lifecycleMatrixCompletion
	wantIdempotent bool
	wantRecovered  bool
}

// TestPelletRepositoryLifecycleTransitionMatrix is the executable form of the
// normative command/source-status table in docs/data-model.md. Each row uses a
// real database so rejected transitions can also prove that they attempted no
// SQLite write.
func TestPelletRepositoryLifecycleTransitionMatrix(t *testing.T) {
	t.Parallel()

	tests := []lifecycleMatrixCase{
		{name: "start/open", operation: storage.PelletStart, source: matrixOpen, wantStatus: domain.PelletInProgress, wantOwner: true, wantPriority: matrixPriorityPreserved, wantCompletion: matrixCompletionCleared},
		{name: "start/open/current-workspace-busy", operation: storage.PelletStart, source: matrixOpen, busyCaller: true, wantCode: "workspace_already_in_progress"},
		{name: "start/in_progress/current-owner", operation: storage.PelletStart, source: matrixInProgress, caller: matrixCurrentOwner, wantStatus: domain.PelletInProgress, wantOwner: true, wantPriority: matrixPriorityPreserved, wantCompletion: matrixCompletionCleared, wantIdempotent: true},
		{name: "start/in_progress/foreign-owner", operation: storage.PelletStart, source: matrixInProgress, caller: matrixForeignOwner, wantCode: "pellet_in_progress_elsewhere"},
		{name: "start/closed", operation: storage.PelletStart, source: matrixClosed, wantCode: "invalid_pellet_transition"},
		{name: "start/maybe_later", operation: storage.PelletStart, source: matrixMaybeLater, wantCode: "invalid_pellet_transition"},

		{name: "release/open", operation: storage.PelletRelease, source: matrixOpen, wantCode: "invalid_pellet_transition"},
		{name: "release/in_progress/current-owner", operation: storage.PelletRelease, source: matrixInProgress, caller: matrixCurrentOwner, wantStatus: domain.PelletOpen, wantPriority: matrixPriorityPreserved, wantCompletion: matrixCompletionCleared},
		{name: "release/in_progress/current-owner/wrong-recovery", operation: storage.PelletRelease, source: matrixInProgress, caller: matrixCurrentOwner, recovery: matrixWrongOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "release/in_progress/foreign-owner", operation: storage.PelletRelease, source: matrixInProgress, caller: matrixForeignOwner, wantCode: "pellet_in_progress_elsewhere"},
		{name: "release/in_progress/foreign-owner/exact-recovery", operation: storage.PelletRelease, source: matrixInProgress, caller: matrixForeignOwner, recovery: matrixExactOwnerRecovery, wantStatus: domain.PelletOpen, wantPriority: matrixPriorityPreserved, wantCompletion: matrixCompletionCleared, wantRecovered: true},
		{name: "release/in_progress/foreign-owner/wrong-recovery", operation: storage.PelletRelease, source: matrixInProgress, caller: matrixForeignOwner, recovery: matrixWrongOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "release/in_progress/foreign-owner/missing-recovery", operation: storage.PelletRelease, source: matrixInProgress, caller: matrixForeignOwner, recovery: matrixMissingOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "release/closed", operation: storage.PelletRelease, source: matrixClosed, wantCode: "invalid_pellet_transition"},
		{name: "release/maybe_later", operation: storage.PelletRelease, source: matrixMaybeLater, wantCode: "invalid_pellet_transition"},

		{name: "close/open", operation: storage.PelletClose, source: matrixOpen, caller: matrixForeignOwner, wantStatus: domain.PelletClosed, wantPriority: matrixPriorityCleared, wantCompletion: matrixCompletionSet},
		{name: "close/open/registered-recovery", operation: storage.PelletClose, source: matrixOpen, recovery: matrixWrongOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "close/open/missing-recovery", operation: storage.PelletClose, source: matrixOpen, recovery: matrixMissingOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "close/in_progress/current-owner", operation: storage.PelletClose, source: matrixInProgress, caller: matrixCurrentOwner, wantStatus: domain.PelletClosed, wantPriority: matrixPriorityCleared, wantCompletion: matrixCompletionSet},
		{name: "close/in_progress/current-owner/wrong-recovery", operation: storage.PelletClose, source: matrixInProgress, caller: matrixCurrentOwner, recovery: matrixWrongOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "close/in_progress/foreign-owner", operation: storage.PelletClose, source: matrixInProgress, caller: matrixForeignOwner, wantCode: "pellet_in_progress_elsewhere"},
		{name: "close/in_progress/foreign-owner/exact-recovery", operation: storage.PelletClose, source: matrixInProgress, caller: matrixForeignOwner, recovery: matrixExactOwnerRecovery, wantStatus: domain.PelletClosed, wantPriority: matrixPriorityCleared, wantCompletion: matrixCompletionSet, wantRecovered: true},
		{name: "close/in_progress/foreign-owner/wrong-recovery", operation: storage.PelletClose, source: matrixInProgress, caller: matrixForeignOwner, recovery: matrixWrongOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "close/in_progress/foreign-owner/missing-recovery", operation: storage.PelletClose, source: matrixInProgress, caller: matrixForeignOwner, recovery: matrixMissingOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "close/closed", operation: storage.PelletClose, source: matrixClosed, caller: matrixForeignOwner, wantStatus: domain.PelletClosed, wantPriority: matrixPriorityCleared, wantCompletion: matrixCompletionPreserved, wantIdempotent: true},
		{name: "close/maybe_later", operation: storage.PelletClose, source: matrixMaybeLater, wantCode: "invalid_pellet_transition"},

		{name: "reopen/open", operation: storage.PelletReopen, source: matrixOpen, caller: matrixForeignOwner, wantStatus: domain.PelletOpen, wantPriority: matrixPriorityPreserved, wantCompletion: matrixCompletionCleared, wantIdempotent: true},
		{name: "reopen/in_progress/current-owner", operation: storage.PelletReopen, source: matrixInProgress, caller: matrixCurrentOwner, wantCode: "invalid_pellet_transition"},
		{name: "reopen/in_progress/foreign-owner", operation: storage.PelletReopen, source: matrixInProgress, caller: matrixForeignOwner, wantCode: "invalid_pellet_transition"},
		{name: "reopen/closed", operation: storage.PelletReopen, source: matrixClosed, caller: matrixForeignOwner, wantStatus: domain.PelletOpen, wantPriority: matrixPriorityAppended, wantCompletion: matrixCompletionCleared},
		{name: "reopen/maybe_later", operation: storage.PelletReopen, source: matrixMaybeLater, caller: matrixForeignOwner, wantStatus: domain.PelletOpen, wantPriority: matrixPriorityAppended, wantCompletion: matrixCompletionCleared},

		{name: "defer/open", operation: storage.PelletDefer, source: matrixOpen, caller: matrixForeignOwner, wantStatus: domain.PelletMaybeLater, wantPriority: matrixPriorityCleared, wantCompletion: matrixCompletionCleared},
		{name: "defer/open/registered-recovery", operation: storage.PelletDefer, source: matrixOpen, recovery: matrixWrongOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "defer/open/missing-recovery", operation: storage.PelletDefer, source: matrixOpen, recovery: matrixMissingOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "defer/in_progress/current-owner", operation: storage.PelletDefer, source: matrixInProgress, caller: matrixCurrentOwner, wantStatus: domain.PelletMaybeLater, wantPriority: matrixPriorityCleared, wantCompletion: matrixCompletionCleared},
		{name: "defer/in_progress/current-owner/wrong-recovery", operation: storage.PelletDefer, source: matrixInProgress, caller: matrixCurrentOwner, recovery: matrixWrongOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "defer/in_progress/foreign-owner", operation: storage.PelletDefer, source: matrixInProgress, caller: matrixForeignOwner, wantCode: "pellet_in_progress_elsewhere"},
		{name: "defer/in_progress/foreign-owner/exact-recovery", operation: storage.PelletDefer, source: matrixInProgress, caller: matrixForeignOwner, recovery: matrixExactOwnerRecovery, wantStatus: domain.PelletMaybeLater, wantPriority: matrixPriorityCleared, wantCompletion: matrixCompletionCleared, wantRecovered: true},
		{name: "defer/in_progress/foreign-owner/wrong-recovery", operation: storage.PelletDefer, source: matrixInProgress, caller: matrixForeignOwner, recovery: matrixWrongOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "defer/in_progress/foreign-owner/missing-recovery", operation: storage.PelletDefer, source: matrixInProgress, caller: matrixForeignOwner, recovery: matrixMissingOwnerRecovery, wantCode: "recovery_workspace_mismatch"},
		{name: "defer/closed", operation: storage.PelletDefer, source: matrixClosed, wantCode: "invalid_pellet_transition"},
		{name: "defer/maybe_later", operation: storage.PelletDefer, source: matrixMaybeLater, caller: matrixForeignOwner, wantStatus: domain.PelletMaybeLater, wantPriority: matrixPriorityCleared, wantCompletion: matrixCompletionCleared, wantIdempotent: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runLifecycleMatrixCase(t, test)
		})
	}
}

func runLifecycleMatrixCase(t *testing.T, test lifecycleMatrixCase) {
	t.Helper()
	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	target, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "matrix target"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "matrix queue tail"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreatePellet(context.Background(), fixture.other, storage.NewPellet{Title: "unrelated project sentinel"}); err != nil {
		t.Fatal(err)
	}

	switch test.source {
	case matrixOpen:
	case matrixInProgress:
		target = transitionPellet(t, repository, fixture.main, target.Reference, storage.PelletStart, nil).Pellet
	case matrixClosed:
		target = transitionPellet(t, repository, fixture.main, target.Reference, storage.PelletClose, nil).Pellet
	case matrixMaybeLater:
		target = transitionPellet(t, repository, fixture.main, target.Reference, storage.PelletDefer, nil).Pellet
	default:
		t.Fatalf("unknown matrix source %q", test.source)
	}

	caller := fixture.main
	if test.caller == matrixForeignOwner {
		caller = fixture.linked
	}
	if test.busyCaller {
		blocker, err := repository.CreatePellet(context.Background(), caller, storage.NewPellet{Title: "current workspace blocker"})
		if err != nil {
			t.Fatal(err)
		}
		transitionPellet(t, repository, caller, blocker.Reference, storage.PelletStart, nil)
	}

	assertPelletQueryInt(t, repository.db, "SELECT COUNT(*) FROM project_workspaces WHERE project_id = ?", 2, fixture.main.Project.ID)
	before, err := repository.ReadPellet(context.Background(), fixture.main, target.Reference)
	if err != nil {
		t.Fatal(err)
	}
	assertLifecycleMatrixWorkspaceRelation(t, test, fixture, caller, before)
	mainBefore := captureRepositoryLifecycleWriteState(t, repository.db, fixture.main.Project.ID)
	unrelatedBefore := captureRepositoryLifecycleWriteState(t, repository.db, fixture.other.Project.ID)
	changesBefore := queryTotalChanges(t, repository.db)
	tailPriorityBefore := queryPelletInt64(t, repository.db, `
		SELECT COALESCE(MAX(priority), 0) FROM pellets WHERE project_id = ?`, fixture.main.Project.ID)

	recoveryWorkspaceID := lifecycleMatrixRecoveryWorkspaceID(test.recovery, fixture)
	result, transitionErr := repository.TransitionPellet(context.Background(), caller, target.Reference, storage.PelletLifecycleRequest{
		Operation: test.operation, RecoveryWorkspaceID: recoveryWorkspaceID,
	})

	if test.wantCode != "" {
		assertPelletErrorCode(t, transitionErr, test.wantCode)
		if test.wantCode == "recovery_workspace_mismatch" {
			public := domain.PublicError(transitionErr)
			var ownerWorkspaceID any
			if before.Workspace != nil {
				ownerWorkspaceID = before.Workspace.ID
			}
			if public.Details["pellet_id"] != target.Reference.String() ||
				public.Details["owner_workspace_id"] != ownerWorkspaceID ||
				public.Details["provided_workspace_id"] != *recoveryWorkspaceID {
				t.Fatalf("recovery mismatch details = %#v", public.Details)
			}
		}
		if changesAfter := queryTotalChanges(t, repository.db); changesAfter != changesBefore {
			t.Fatalf("rejected transition attempted a SQLite write: total_changes %d -> %d", changesBefore, changesAfter)
		}
		if mainAfter := captureRepositoryLifecycleWriteState(t, repository.db, fixture.main.Project.ID); !reflect.DeepEqual(mainAfter, mainBefore) {
			t.Fatalf("rejected transition changed logical project state:\nbefore=%#v\nafter=%#v", mainBefore, mainAfter)
		}
	} else {
		if transitionErr != nil {
			t.Fatal(transitionErr)
		}
		assertLifecycleMatrixResult(t, test, fixture, caller, before, result, tailPriorityBefore)
		persisted, err := repository.ReadPellet(context.Background(), fixture.main, target.Reference)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(persisted, result.Pellet) {
			t.Fatalf("persisted pellet = %#v, want returned pellet %#v", persisted, result.Pellet)
		}
	}

	if unrelatedAfter := captureRepositoryLifecycleWriteState(t, repository.db, fixture.other.Project.ID); !reflect.DeepEqual(unrelatedAfter, unrelatedBefore) {
		t.Fatalf("transition changed unrelated project:\nbefore=%#v\nafter=%#v", unrelatedBefore, unrelatedAfter)
	}
}

func assertLifecycleMatrixWorkspaceRelation(
	t *testing.T,
	test lifecycleMatrixCase,
	fixture pelletRepositoryFixture,
	caller storage.ResolvedProject,
	before storage.Pellet,
) {
	t.Helper()
	if fixture.main.Project.ID != fixture.linked.Project.ID ||
		fixture.main.Workspace.ID == fixture.linked.Workspace.ID {
		t.Fatalf("matrix requires two distinct workspaces in one logical project: main=%#v linked=%#v", fixture.main, fixture.linked)
	}
	if before.Status != domain.PelletStatus(test.source) {
		t.Fatalf("source status = %q, want %q", before.Status, test.source)
	}
	if test.source != matrixInProgress {
		if before.Workspace != nil {
			t.Fatalf("ownerless source has workspace %#v", before.Workspace)
		}
		return
	}
	if before.Workspace == nil || !reflect.DeepEqual(*before.Workspace, fixture.main.Workspace) {
		t.Fatalf("in-progress source owner = %#v, want %#v", before.Workspace, fixture.main.Workspace)
	}
	if test.caller == matrixForeignOwner && caller.Workspace.ID == before.Workspace.ID {
		t.Fatalf("foreign caller %d unexpectedly owns the source pellet", caller.Workspace.ID)
	}
	if test.caller != matrixForeignOwner && caller.Workspace.ID != before.Workspace.ID {
		t.Fatalf("current caller %d does not own source pellet in workspace %d", caller.Workspace.ID, before.Workspace.ID)
	}
}

func lifecycleMatrixRecoveryWorkspaceID(recovery lifecycleMatrixRecovery, fixture pelletRepositoryFixture) *int64 {
	var workspaceID int64
	switch recovery {
	case matrixNoRecovery:
		return nil
	case matrixExactOwnerRecovery:
		workspaceID = fixture.main.Workspace.ID
	case matrixWrongOwnerRecovery:
		workspaceID = fixture.linked.Workspace.ID
	case matrixMissingOwnerRecovery:
		workspaceID = fixture.linked.Workspace.ID + 1000
	default:
		panic("unknown lifecycle matrix recovery")
	}
	return &workspaceID
}

func assertLifecycleMatrixResult(
	t *testing.T,
	test lifecycleMatrixCase,
	fixture pelletRepositoryFixture,
	caller storage.ResolvedProject,
	before storage.Pellet,
	result storage.PelletLifecycleResult,
	tailPriorityBefore int64,
) {
	t.Helper()
	got := result.Pellet
	if got.Status != test.wantStatus {
		t.Errorf("status = %q, want %q", got.Status, test.wantStatus)
	}
	if test.wantOwner {
		if got.Workspace == nil || !reflect.DeepEqual(*got.Workspace, caller.Workspace) {
			t.Errorf("workspace = %#v, want exact caller workspace %#v", got.Workspace, caller.Workspace)
		}
	} else if got.Workspace != nil {
		t.Errorf("workspace = %#v, want nil", got.Workspace)
	}

	switch test.wantPriority {
	case matrixPriorityPreserved:
		if !reflect.DeepEqual(got.Priority, before.Priority) {
			t.Errorf("priority = %v, want preserved %v", got.Priority, before.Priority)
		}
	case matrixPriorityCleared:
		if got.Priority != nil {
			t.Errorf("priority = %v, want nil", got.Priority)
		}
	case matrixPriorityAppended:
		want := tailPriorityBefore + domain.PelletPriorityStride
		if got.Priority == nil || *got.Priority != want {
			t.Errorf("priority = %v, want appended tail priority %d", got.Priority, want)
		}
	default:
		t.Errorf("missing priority expectation")
	}

	switch test.wantCompletion {
	case matrixCompletionCleared:
		if got.CompletedAt != nil {
			t.Errorf("completed_at = %v, want nil", got.CompletedAt)
		}
	case matrixCompletionSet:
		if got.CompletedAt == nil || !got.CompletedAt.Equal(got.UpdatedAt) {
			t.Errorf("completed_at = %v, updated_at = %v; want equal non-nil timestamps", got.CompletedAt, got.UpdatedAt)
		}
	case matrixCompletionPreserved:
		if !reflect.DeepEqual(got.CompletedAt, before.CompletedAt) {
			t.Errorf("completed_at = %v, want preserved %v", got.CompletedAt, before.CompletedAt)
		}
	default:
		t.Errorf("missing completion expectation")
	}

	if got.Reference != before.Reference || got.ProjectID != before.ProjectID || got.Title != before.Title ||
		got.Description != before.Description || !reflect.DeepEqual(got.ExternalID, before.ExternalID) ||
		!reflect.DeepEqual(got.Group, before.Group) || !got.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("transition changed non-lifecycle fields:\nbefore=%#v\nafter=%#v", before, got)
	}
	if got.UpdatedAt.Before(before.UpdatedAt) {
		t.Errorf("updated_at moved backward: before=%s after=%s", before.UpdatedAt, got.UpdatedAt)
	}
	if test.wantIdempotent && !reflect.DeepEqual(got, before) {
		t.Errorf("idempotent transition changed timestamp or position:\nbefore=%#v\nafter=%#v", before, got)
	}

	if test.wantRecovered {
		if result.RecoveredWorkspace == nil || !reflect.DeepEqual(*result.RecoveredWorkspace, fixture.main.Workspace) {
			t.Errorf("recovered workspace = %#v, want exact owner %#v", result.RecoveredWorkspace, fixture.main.Workspace)
		}
	} else if result.RecoveredWorkspace != nil {
		t.Errorf("recovered workspace = %#v, want nil", result.RecoveredWorkspace)
	}
}
