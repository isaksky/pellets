package storage

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"pellets/internal/domain"
)

// ValidatePelletPreferences validates stored choices without runtime discovery.
func ValidatePelletPreferences(model, effort *string) error {
	for _, field := range []struct {
		name  string
		value *string
		limit int
	}{{"model", model, 512}, {"reasoning_effort", effort, 128}} {
		if field.value != nil && (*field.value == "" || strings.TrimSpace(*field.value) != *field.value || len(*field.value) > field.limit || !utf8.ValidString(*field.value) || strings.ContainsFunc(*field.value, unicode.IsControl)) {
			return domain.NewError(domain.Usage, "invalid_pellet_field", "execution preference must be a nonempty, trimmed identifier within its length limit", map[string]any{"field": field.name})
		}
	}
	return nil
}

// PelletExecutionPreferences is an admission snapshot, not implementation scope.
type PelletExecutionPreferences struct {
	Model           *string `json:"model,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
}

func (p Pellet) ExecutionPreferences() *PelletExecutionPreferences {
	return &PelletExecutionPreferences{Model: p.Model, ReasoningEffort: p.ReasoningEffort}
}
