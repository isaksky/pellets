package domain

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxProjectCodeLength = 12

const generatedProjectCodeHashLength = 8

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

// GenerateProjectCode derives one public code candidate from a logical
// repository name and its normalized database-local identity. The plain
// normalized name is used only when it fits the public grammar and forceHash
// is false. Hashed candidates are deterministic for an identity and attempt;
// storage increments attempt only when a candidate is already owned by a
// different repository in the same database.
func GenerateProjectCode(repositoryName, repositoryIdentity string, forceHash bool, attempt uint64) string {
	normalized := normalizeProjectCodeName(repositoryName)
	if normalized != "" && len(normalized) <= MaxProjectCodeLength && !forceHash {
		return normalized
	}

	seed := repositoryIdentity
	if attempt > 0 {
		seed += "\x00" + strconv.FormatUint(attempt, 10)
	}
	digest := sha256.Sum256([]byte(seed))
	suffix := fmt.Sprintf("%x", digest[:generatedProjectCodeHashLength/2])
	prefix := normalized
	if prefix == "" {
		prefix = "p"
	}
	maximumPrefix := MaxProjectCodeLength - 1 - len(suffix)
	if len(prefix) > maximumPrefix {
		prefix = prefix[:maximumPrefix]
	}
	return prefix + "-" + suffix
}

func normalizeProjectCodeName(name string) string {
	var builder strings.Builder
	pendingHyphen := false
	for _, character := range name {
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			if pendingHyphen && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			builder.WriteRune(character)
			pendingHyphen = false
			continue
		}
		pendingHyphen = builder.Len() > 0
	}
	return builder.String()
}
