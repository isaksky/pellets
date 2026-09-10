package storage

import "reflect"

// SameReviewScope compares immutable identities, selected scope and current
// evidence, ignoring only the display reference that a project rename changes.
func SameReviewScope(a, b *ReviewCheckpoint) bool {
	canonical := func(input *ReviewCheckpoint) *ReviewCheckpoint {
		if input == nil {
			return nil
		}
		copy := *input
		copy.Targets = append([]ReviewTarget(nil), input.Targets...)
		for i := range copy.Targets {
			copy.Targets[i].Reference = ""
		}
		return &copy
	}
	return reflect.DeepEqual(canonical(a), canonical(b))
}
