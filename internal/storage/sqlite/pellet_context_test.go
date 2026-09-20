package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"pellets/internal/storage"
)

func TestPelletContextConcurrentMembershipRenameAndDocument(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, q, groups := groupFixture(t)
	g, err := groups.CreateGroupWithContext(ctx, f.main.Project, "group-0", "document-0")
	if err != nil {
		t.Fatal(err)
	}
	other := mustGroup(t, groups, f.main.Project, "spare")
	ids := []int64{g.ID, other.ID}
	p, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "0", Group: &g.Name})
	if err != nil {
		t.Fatal(err)
	}
	writer := f.open(t)
	defer writer.Close()
	begin := make(chan struct{})
	errs := make(chan error, 5)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-begin
		for i := 1; i <= 100; i++ {
			// Each committed generation changes membership, group name, revision
			// and text together. Title identifies the generation, so even a
			// self-consistent document from the wrong membership is detected.
			err := func() error {
				conn, err := writer.db.Conn(ctx)
				if err != nil {
					return err
				}
				defer conn.Close()
				if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
					return err
				}
				defer conn.ExecContext(ctx, "ROLLBACK")
				name := fmt.Sprintf("group-%d", i)
				if _, err := conn.ExecContext(ctx, `UPDATE groups SET name=?, context=?, revision=? WHERE group_id=?`, name, fmt.Sprintf("document-%d", i), i+1, ids[i%2]); err != nil {
					return err
				}
				if _, err := conn.ExecContext(ctx, `UPDATE pellets SET group_id=?, title=? WHERE project_id=? AND number=?`, name, strconv.Itoa(i), f.main.Project.ID, p.Reference.Number); err != nil {
					return err
				}
				_, err = conn.ExecContext(ctx, "COMMIT")
				return err
			}()
			if err != nil {
				errs <- err
				return
			}
		}
	}()
	for _, command := range []string{"show", "next", "start", "start-next"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			for range 100 {
				var got storage.Pellet
				var err error
				switch command {
				case "show":
					got, err = q.ReadPellet(ctx, f.main, p.Reference)
				case "start":
					var result storage.PelletLifecycleResult
					result, err = q.TransitionPellet(ctx, f.main, p.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart})
					got = result.Pellet
				default:
					var result storage.NextSelection
					if command == "next" {
						result, err = q.NextPellet(ctx, f.main, nil, nil)
					} else {
						result, err = q.StartNextPellet(ctx, f.main, nil, nil)
					}
					if err == nil && result.Pellet == nil {
						err = fmt.Errorf("%s lost the current pellet", command)
					}
					if result.Pellet != nil {
						got = *result.Pellet
					}
				}
				if err != nil {
					errs <- err
					return
				}
				i, err := strconv.Atoi(got.Title)
				context := got.GroupContext
				if err != nil || context == nil || got.GroupID == nil || got.Group == nil ||
					context.ID != ids[i%2] || context.ID != *got.GroupID || context.Name != *got.Group ||
					context.Name != fmt.Sprintf("group-%d", i) || context.Revision != int64(i+1) || context.Context != fmt.Sprintf("document-%d", i) {
					errs <- fmt.Errorf("%s mixed membership/context generations: pellet=%+v context=%+v", command, got, context)
					return
				}
			}
		}()
	}
	close(begin)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPelletContextOmittedFromCompactReadsAndExecutionSerialization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, q, groups := groupFixture(t)
	g, err := groups.CreateGroupWithContext(ctx, f.main.Project, "shared", "group document")
	if err != nil {
		t.Fatal(err)
	}
	p, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "member", Group: &g.Name})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := q.ListPellets(ctx, f.main, storage.PelletListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	searched, err := q.SearchPellets(ctx, f.main, storage.PelletSearchOptions{Query: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || len(searched) != 1 || listed[0].GroupContext != nil || searched[0].GroupContext != nil {
		t.Fatalf("compact reads loaded group bodies: %+v %+v", listed, searched)
	}
	selected, err := q.SelectScheduledPellet(ctx, f.main, storage.ScheduleSelection{})
	if err != nil || selected.Pellet == nil || selected.Pellet.GroupContext != nil {
		t.Fatalf("scheduler acquired live CLI enrichment: %+v %v", selected, err)
	}
	detail, err := q.ReadPellet(ctx, f.main, p.Reference)
	if err != nil || detail.GroupContext == nil {
		t.Fatalf("missing live detail: %+v %v", detail, err)
	}
	encoded, err := json.Marshal(detail)
	if err != nil || strings.Contains(string(encoded), g.Context) || strings.Contains(string(encoded), "GroupContext") {
		t.Fatalf("live context leaked into generic evidence serialization: %s %v", encoded, err)
	}
}
