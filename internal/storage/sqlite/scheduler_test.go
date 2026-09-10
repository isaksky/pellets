package sqlite

import (
	"context"
	"sync"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestScheduledSelectionExactResumeFiltersAndReadiness(t *testing.T) {
	f := newPelletRepositoryFixture(t)
	q := f.open(t)
	defer q.Close()
	ctx := context.Background()
	group, external := " Group ", " external "
	wrong := "Group"
	p, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "target", Group: &group, ExternalID: &external})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := q.SelectScheduledPellet(ctx, f.main, storage.ScheduleSelection{Group: &wrong})
	if err != nil || selection.Reason != storage.NextNone {
		t.Fatalf("exact filter = %+v %v", selection, err)
	}
	selection, err = q.SelectScheduledPellet(ctx, f.main, storage.ScheduleSelection{Ready: func(context.Context, storage.Pellet) (bool, error) { return false, nil }})
	if err != nil || selection.Reason != storage.NextNotReady {
		t.Fatalf("readiness = %+v %v", selection, err)
	}
	read, _ := q.ReadPellet(ctx, f.main, p.Reference)
	if read.Status != domain.PelletOpen {
		t.Fatal("readiness mutated candidate")
	}
	selection, err = q.SelectScheduledPellet(ctx, f.main, storage.ScheduleSelection{Group: &group, ExternalID: &external})
	if err != nil || selection.Pellet.Reference != p.Reference {
		t.Fatalf("select = %+v %v", selection, err)
	}
	for _, test := range []struct {
		input storage.ScheduleSelection
		code  string
	}{
		{storage.ScheduleSelection{}, "schedule_resume_required"},
		{storage.ScheduleSelection{ResumePellet: &p.Reference.Number, Group: &wrong}, "schedule_filter_mismatch"},
	} {
		_, err := q.SelectScheduledPellet(ctx, f.main, test.input)
		if err == nil || domain.PublicError(err).Code != test.code {
			t.Fatalf("resume = %v, want %s", err, test.code)
		}
	}
	selection, err = q.SelectScheduledPellet(ctx, f.main, storage.ScheduleSelection{ResumePellet: &p.Reference.Number, Group: &group, ExternalID: &external})
	if err != nil || selection.Reason != storage.NextResumeInProgress {
		t.Fatalf("resume = %+v %v", selection, err)
	}
	_, err = q.TransitionPellet(ctx, f.main, p.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletRelease})
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.SelectScheduledPellet(ctx, f.main, storage.ScheduleSelection{ResumePellet: &p.Reference.Number})
	if err == nil || domain.PublicError(err).Code != "schedule_resume_changed" {
		t.Fatalf("changed resume = %v", err)
	}
}

func TestScheduledSelectionConcurrentWorkspacesNeverSharePellet(t *testing.T) {
	f := newPelletRepositoryFixture(t)
	q := f.open(t)
	defer q.Close()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "work"}); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan storage.NextSelection, 2)
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	for _, workspace := range []storage.ResolvedProject{f.main, f.linked} {
		other := f.open(t)
		wg.Add(1)
		go func(selected storage.ResolvedProject) {
			defer wg.Done()
			defer other.Close()
			<-start
			result, err := other.SelectScheduledPellet(ctx, selected, storage.ScheduleSelection{})
			results <- result
			errors <- err
		}(workspace)
	}
	close(start)
	wg.Wait()
	for i := 0; i < 2; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	first, second := <-results, <-results
	if first.Pellet == nil || second.Pellet == nil || first.Pellet.Reference == second.Pellet.Reference {
		t.Fatalf("duplicate selection: %+v %+v", first, second)
	}
}
