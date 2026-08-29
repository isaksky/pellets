package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

const (
	defaultOrderingPropertySeed int64 = 0x5eed_c0de
	orderingPropertyOperations        = 2048
)

// TestPelletOrderingMatchesReferenceList exercises relative insertion and
// movement against the deliberately simpler queue model below. The seed is
// part of the subtest name, and PELLETS_ORDERING_SEED can replay another seed
// reported by a failure without changing the test.
func TestPelletOrderingMatchesReferenceList(t *testing.T) {
	t.Parallel()

	seed := defaultOrderingPropertySeed
	if configured := os.Getenv("PELLETS_ORDERING_SEED"); configured != "" {
		parsed, err := strconv.ParseInt(configured, 0, 64)
		if err != nil {
			t.Fatalf("parse PELLETS_ORDERING_SEED %q: %v", configured, err)
		}
		seed = parsed
	}

	t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
		fixture := newPelletRepositoryFixture(t)
		repository := fixture.open(t)
		defer repository.Close()

		closed, err := repository.CreatePellet(
			context.Background(), fixture.main, storage.NewPellet{Title: "inactive closed sentinel"},
		)
		if err != nil {
			t.Fatalf("seed %d: create closed sentinel: %v", seed, err)
		}
		transitionPellet(t, repository, fixture.main, closed.Reference, storage.PelletClose, nil)
		deferred, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
			Title: "inactive deferred sentinel", Status: domain.PelletMaybeLater,
		})
		if err != nil {
			t.Fatalf("seed %d: create deferred sentinel: %v", seed, err)
		}
		inactiveBefore := captureInactiveOrderingSentinels(
			t, repository.db, fixture.main.Project.ID, closed.Reference.Number, deferred.Reference.Number,
		)

		model := make(pelletReferenceList, 0, 400)
		for index := 0; index < 8; index++ {
			pellet, createErr := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
				Title: fmt.Sprintf("seed pellet %d", index),
			})
			if createErr != nil {
				t.Fatalf("seed %d: create initial pellet %d: %v", seed, index, createErr)
			}
			model = append(model, pellet.Reference)
		}

		random := rand.New(rand.NewSource(seed)) // #nosec G404 -- deterministic property-test input.
		forcedRebalances := 0
		for operation := 0; operation < orderingPropertyOperations; operation++ {
			if operation%199 == 0 && len(model) >= 3 {
				forceConsecutiveModelPriorities(t, repository, fixture.main.Project.ID, model)
				moving := len(model) - 1
				target := 1
				placement := storage.PelletPlacement{Target: model[target], Before: true}
				if _, moveErr := repository.MovePellet(
					context.Background(), fixture.main, model[moving], placement,
				); moveErr != nil {
					t.Fatalf("seed %d operation %d: forced-gap move: %v", seed, operation, moveErr)
				}
				model.move(moving, target, true)
				forcedRebalances++
				minimum := queryPelletInt64(
					t, repository.db,
					"SELECT MIN(priority) FROM pellets WHERE project_id = ? AND priority IS NOT NULL",
					fixture.main.Project.ID,
				)
				if minimum <= int64(len(model)) {
					t.Fatalf(
						"seed %d operation %d: forced gap did not move the active queue into a fresh band: min=%d old-max=%d",
						seed, operation, minimum, len(model),
					)
				}
			} else if len(model) < 2 || random.Intn(8) == 0 {
				input := storage.NewPellet{Title: fmt.Sprintf("random add %d", operation)}
				insertAt := len(model)
				if len(model) > 0 && random.Intn(3) != 0 {
					target := random.Intn(len(model))
					before := random.Intn(2) == 0
					input.Placement = &storage.PelletPlacement{Target: model[target], Before: before}
					insertAt = target
					if !before {
						insertAt++
					}
				}
				pellet, createErr := repository.CreatePellet(context.Background(), fixture.main, input)
				if createErr != nil {
					t.Fatalf("seed %d operation %d: random add: %v", seed, operation, createErr)
				}
				model.insert(insertAt, pellet.Reference)
			} else {
				moving := random.Intn(len(model))
				target := random.Intn(len(model) - 1)
				if target >= moving {
					target++
				}
				before := random.Intn(2) == 0
				placement := storage.PelletPlacement{Target: model[target], Before: before}
				if _, moveErr := repository.MovePellet(
					context.Background(), fixture.main, model[moving], placement,
				); moveErr != nil {
					t.Fatalf("seed %d operation %d: random move: %v", seed, operation, moveErr)
				}
				model.move(moving, target, before)
			}

			got, listErr := repository.ListPellets(context.Background(), fixture.main, storage.PelletListOptions{})
			if listErr != nil {
				t.Fatalf("seed %d operation %d: list active queue: %v", seed, operation, listErr)
			}
			gotReferences := make([]domain.PelletReference, len(got))
			for index := range got {
				gotReferences[index] = got[index].Reference
			}
			if !reflect.DeepEqual(gotReferences, []domain.PelletReference(model)) {
				t.Fatalf(
					"seed %d operation %d: active order = %v, want reference list %v",
					seed, operation, gotReferences, model,
				)
			}
			if operation%64 == 0 || operation == orderingPropertyOperations-1 {
				assertActivePriorityInvariants(t, repository.db, fixture.main.Project.ID, len(model))
			}
		}

		if forcedRebalances < 10 {
			t.Fatalf("seed %d: forced only %d gap-exhaustion rebalances", seed, forcedRebalances)
		}
		inactiveAfter := captureInactiveOrderingSentinels(
			t, repository.db, fixture.main.Project.ID, closed.Reference.Number, deferred.Reference.Number,
		)
		if !reflect.DeepEqual(inactiveAfter, inactiveBefore) {
			t.Fatalf(
				"seed %d: randomized rebalances touched inactive rows:\nbefore=%v\nafter=%v",
				seed, inactiveBefore, inactiveAfter,
			)
		}
	})
}

type pelletReferenceList []domain.PelletReference

func (list *pelletReferenceList) insert(index int, reference domain.PelletReference) {
	*list = append(*list, domain.PelletReference{})
	copy((*list)[index+1:], (*list)[index:])
	(*list)[index] = reference
}

func (list *pelletReferenceList) move(moving, target int, before bool) {
	reference := (*list)[moving]
	*list = append((*list)[:moving], (*list)[moving+1:]...)
	if moving < target {
		target--
	}
	if !before {
		target++
	}
	list.insert(target, reference)
}

func forceConsecutiveModelPriorities(
	t *testing.T,
	repository *PelletRepository,
	projectID int64,
	model pelletReferenceList,
) {
	t.Helper()
	const temporaryBand int64 = 1 << 50
	if _, err := repository.db.Exec(
		"UPDATE pellets SET priority = priority + ? WHERE project_id = ? AND priority IS NOT NULL",
		temporaryBand, projectID,
	); err != nil {
		t.Fatalf("move active priorities to temporary band: %v", err)
	}
	for index, reference := range model {
		result, err := repository.db.Exec(
			"UPDATE pellets SET priority = ? WHERE project_id = ? AND number = ? AND priority IS NOT NULL",
			index+1, projectID, reference.Number,
		)
		if err != nil {
			t.Fatalf("set consecutive priority for %s: %v", reference, err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			t.Fatalf("set consecutive priority for %s changed %d rows: %v", reference, changed, err)
		}
	}
}

func captureInactiveOrderingSentinels(
	t *testing.T,
	repository *sql.DB,
	projectID, closedNumber, deferredNumber int64,
) []string {
	t.Helper()
	rows, err := repository.QueryContext(context.Background(), `
		SELECT quote(number) || '|' || quote(status) || '|' || quote(priority) || '|' || quote(updated_at)
		FROM pellets
		WHERE project_id = ? AND number IN (?, ?)
		ORDER BY number`, projectID, closedNumber, deferredNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := make([]string, 0, 2)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
