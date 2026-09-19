package storage

import (
	"reflect"
	"testing"
)

func TestEffectiveRoutingDefaultsIndependentlyToMainCheckout(t *testing.T) {
	r := ProjectRouting{Enabled: true, MainWorkspaceID: 9, Assignments: []WorkspaceAssignment{
		DefaultWorkspaceAssignment(2), DefaultWorkspaceAssignment(9),
	}}
	original := append([]WorkspaceAssignment{}, r.Assignments...)
	group := "new-group"
	if !r.Selection(9).AcceptsGroup(&group) || !r.Selection(9).AcceptsGroup(nil) || r.Selection(2).AcceptsGroup(&group) || r.Selection(2).AcceptsGroup(nil) {
		t.Fatal("defaults must go only to main, not the first registered workspace")
	}
	a := r.EffectiveAssignment(9)
	if !a.AutomaticRemaining || !a.AutomaticUngrouped {
		t.Fatalf("missing default provenance: %+v", a)
	}
	if !reflect.DeepEqual(original, r.Assignments) {
		t.Fatal("resolution mutated preferences")
	}
	r.Assignments[0].IncludeUngrouped = true
	if r.Selection(9).AcceptsGroup(nil) || !r.Selection(9).AcceptsGroup(&group) || !r.Selection(2).AcceptsGroup(nil) {
		t.Fatal("ungrouped preference must not change catch-all")
	}
	r.Assignments[0].IncludeUngrouped = false
	r.Assignments[0].Mode = "remaining"
	if r.Selection(9).AcceptsGroup(&group) || !r.Selection(9).AcceptsGroup(nil) || !r.Selection(2).AcceptsGroup(&group) {
		t.Fatal("catch-all preference must not change ungrouped")
	}
	r.UnavailableWorkspaces = []int64{2}
	if !r.Selection(9).AcceptsGroup(&group) || r.Selection(2).AcceptsGroup(&group) {
		t.Fatal("unavailable recipient must yield to main")
	}
}

func TestAutomaticCatchAllPreservesExplicitSharedGroups(t *testing.T) {
	shared, other, unseen := "shared", "other", "unseen"
	r := ProjectRouting{Enabled: true, MainWorkspaceID: 9, Assignments: []WorkspaceAssignment{
		{WorkspaceID: 2, Mode: "explicit", Groups: []string{shared, other}},
		{WorkspaceID: 9, Mode: "explicit", Groups: []string{shared}},
	}}
	if !r.Selection(9).AcceptsGroup(&shared) || !r.Selection(2).AcceptsGroup(&shared) || r.Selection(9).AcceptsGroup(&other) || !r.Selection(9).AcceptsGroup(&unseen) {
		t.Fatal("fallback must preserve explicit overlap and exclude other assignments")
	}
	r.Assignments[1].Mode = "remaining"
	if r.Selection(9).AcceptsGroup(&shared) {
		t.Fatal("saved groups in remaining mode must not become explicit inclusions")
	}
	r.Assignments[0].Mode = "remaining"
	if !r.Selection(9).AcceptsGroup(&other) || !r.Selection(2).AcceptsGroup(&other) {
		t.Fatal("multiple explicit catch-all recipients must remain supported")
	}
	r.Enabled = false
	if !r.Selection(9).AcceptsGroup(nil) || !r.Selection(2).AcceptsGroup(&other) {
		t.Fatal("disabled routing must preserve whole-queue behavior")
	}
}
