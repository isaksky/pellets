package webui

import (
	"encoding/json"
	"fmt"

	"pellets/internal/storage"
)

type reviewGroupContextView struct {
	ID           string
	GroupContext storage.GroupContextSnapshot
	Targets      []reviewContextTargetView
}

type reviewContextTargetView struct {
	Reference string
	RunID     int64
}

func makeReviewGroupContextViews(run storage.ExecutionRun) (int, []reviewGroupContextView) {
	s := run.ReviewSnapshot
	if s == nil {
		return 0, nil
	}
	var views []reviewGroupContextView
	seen := map[string]int{}
	for i, context := range s.GroupContexts {
		// Share source only for identical snapshots, never just a group name.
		encoded, _ := json.Marshal(context.Snapshot)
		key := string(encoded)
		index, exists := seen[key]
		if !exists {
			index = len(views)
			seen[key] = index
			views = append(views, reviewGroupContextView{ID: fmt.Sprintf("review-%d-%d", run.ID, index), GroupContext: context.Snapshot})
		}
		views[index].Targets = append(views[index].Targets, reviewContextTargetView{Reference: s.Targets[i].Reference, RunID: context.RunID})
	}
	return s.Version, views
}
