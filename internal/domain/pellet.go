package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// PelletStatus is one of the four persisted lifecycle states.
type PelletStatus string

const (
	PelletOpen       PelletStatus = "open"
	PelletInProgress PelletStatus = "in_progress"
	PelletClosed     PelletStatus = "closed"
	PelletMaybeLater PelletStatus = "maybe_later"

	// PelletPriorityStride is the sparse ordering interval for new active work.
	PelletPriorityStride int64 = 1024
)

// PelletReference is the stable project-local public identity of a pellet.
type PelletReference struct {
	ProjectCode string
	Number      int64
}

// ValidatePelletStatus accepts exactly the four persisted lifecycle states.
func ValidatePelletStatus(status PelletStatus) error {
	switch status {
	case PelletOpen, PelletInProgress, PelletClosed, PelletMaybeLater:
		return nil
	default:
		return NewError(
			Usage,
			"invalid_status",
			fmt.Sprintf("unknown pellet status %q", status),
			map[string]any{"status": status},
		)
	}
}

func (reference PelletReference) String() string {
	return fmt.Sprintf("%s-%d", reference.ProjectCode, reference.Number)
}

// ParsePelletReference parses at the final hyphen. The number must be a
// positive canonical decimal integer, so references have one stable spelling.
func ParsePelletReference(value string) (PelletReference, error) {
	separator := strings.LastIndexByte(value, '-')
	if separator < 1 || separator == len(value)-1 {
		return PelletReference{}, invalidPelletReference(value)
	}

	code := value[:separator]
	numberText := value[separator+1:]
	if err := ValidateProjectCode(code); err != nil || numberText[0] == '0' {
		return PelletReference{}, invalidPelletReference(value)
	}
	for index := range len(numberText) {
		if numberText[index] < '0' || numberText[index] > '9' {
			return PelletReference{}, invalidPelletReference(value)
		}
	}
	number, err := strconv.ParseInt(numberText, 10, 64)
	if err != nil || number <= 0 {
		return PelletReference{}, invalidPelletReference(value)
	}
	return PelletReference{ProjectCode: code, Number: number}, nil
}

func invalidPelletReference(value string) error {
	return NewError(
		Usage,
		"invalid_reference",
		fmt.Sprintf("pellet reference %q must contain a valid project code and a positive canonical decimal number", value),
		map[string]any{"reference": value},
	)
}
