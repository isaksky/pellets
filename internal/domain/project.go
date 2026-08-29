package domain

import (
	"fmt"
	"unicode/utf8"
)

const MaxProjectCodeLength = 12

// ValidateProjectCode enforces the public, ASCII-only project-code grammar.
// Hyphens may occur only inside the code; repeated internal hyphens are valid.
func ValidateProjectCode(code string) error {
	if !utf8.ValidString(code) || len(code) < 1 || len(code) > MaxProjectCodeLength {
		return invalidProjectCode(code)
	}
	for index := 0; index < len(code); index++ {
		character := code[index]
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			continue
		}
		if character == '-' && index > 0 && index < len(code)-1 {
			continue
		}
		return invalidProjectCode(code)
	}
	return nil
}

func invalidProjectCode(code string) error {
	return NewError(
		Usage,
		"invalid_project_code",
		fmt.Sprintf("project code %q must be 1-12 lowercase ASCII letters, digits, or internal hyphens", code),
		map[string]any{"code": code},
	)
}
