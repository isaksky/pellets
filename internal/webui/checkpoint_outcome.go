package webui

import (
	"net/url"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

type checkpointOutcomeView struct {
	storage.CheckpointOutcome
	Label        string
	Followups    []checkpointDispositionView
	Dispositions []checkpointDispositionView
}

type checkpointDispositionView struct {
	FindingNumber int
	Label         string
	Reference     string
	URL           string
}

func makeCheckpointOutcomeView(outcome storage.CheckpointOutcome, code string, query url.Values, sort storage.WebPelletSort) *checkpointOutcomeView {
	v := &checkpointOutcomeView{CheckpointOutcome: outcome}
	v.Label = map[string]string{"pending": "Pending separate review", "running": "Review in progress", "needs_attention": "Needs attention", "clean": "Clean", "findings": "Findings"}[outcome.Status]
	for _, disposition := range outcome.Dispositions {
		d := checkpointDispositionView{FindingNumber: disposition.FindingNumber}
		d.Label = map[string]string{"valid": "Created follow-up", "existing": "Covered by existing Pellet", "duplicate": "Duplicate finding", "invalid": "Invalid finding", "already_fixed": "Already fixed", "stylistic": "Stylistic; no follow-up"}[disposition.Decision]
		if disposition.PelletNumber > 0 {
			d.Reference = (domain.PelletReference{ProjectCode: code, Number: disposition.PelletNumber}).String()
			if disposition.PelletPresent {
				d.URL = taskURL(code, query, d.Reference, sort)
			}
		}
		if disposition.Decision == "valid" {
			v.Followups = append(v.Followups, d)
		} else {
			v.Dispositions = append(v.Dispositions, d)
		}
	}
	return v
}
