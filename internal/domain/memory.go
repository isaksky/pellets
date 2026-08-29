package domain

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxMemoryTextBytes is the v1 safety limit for one independently retrievable
// memory. It is measured in UTF-8 bytes, matching SQLite's stored text.
const MaxMemoryTextBytes = 1024 * 1024

// MemoryCreator records the deliberately small provenance vocabulary.
type MemoryCreator string

const (
	MemoryCreatedByAgent MemoryCreator = "agent"
	MemoryCreatedByHuman MemoryCreator = "human"
)

// ValidateMemoryCreator accepts exactly the two v1 provenance values.
func ValidateMemoryCreator(creator MemoryCreator) error {
	switch creator {
	case MemoryCreatedByAgent, MemoryCreatedByHuman:
		return nil
	default:
		return NewError(
			Usage,
			"invalid_memory_creator",
			fmt.Sprintf("memory creator %q must be agent or human", creator),
			map[string]any{"created_by": creator},
		)
	}
}

// ValidateMemoryText protects the JSON and SQLite text boundary while keeping
// each v1 memory a non-empty, immutable UTF-8 value.
func ValidateMemoryText(text string) error {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return NewError(
			Usage,
			"invalid_memory_text",
			"memory text must be non-empty valid UTF-8",
			nil,
		)
	}
	if len(text) > MaxMemoryTextBytes {
		return NewError(
			Usage,
			"memory_text_too_large",
			fmt.Sprintf("memory text must not exceed %d UTF-8 bytes", MaxMemoryTextBytes),
			map[string]any{"maximum_bytes": MaxMemoryTextBytes, "actual_bytes": len(text)},
		)
	}
	return nil
}

// ParseMemoryID accepts only positive canonical decimal database-local IDs.
func ParseMemoryID(value string) (int64, error) {
	if value == "" || value[0] == '0' {
		return 0, invalidMemoryID(value)
	}
	for index := range len(value) {
		if value[index] < '0' || value[index] > '9' {
			return 0, invalidMemoryID(value)
		}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, invalidMemoryID(value)
	}
	return id, nil
}

func invalidMemoryID(value string) error {
	return NewError(
		Usage,
		"invalid_memory_id",
		fmt.Sprintf("memory ID %q must be a positive canonical decimal integer", value),
		map[string]any{"memory_id": value},
	)
}
