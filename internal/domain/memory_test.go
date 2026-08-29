package domain

import (
	"strings"
	"testing"
)

func TestMemoryCreatorAndTextValidation(t *testing.T) {
	t.Parallel()

	for _, creator := range []MemoryCreator{MemoryCreatedByAgent, MemoryCreatedByHuman} {
		if err := ValidateMemoryCreator(creator); err != nil {
			t.Errorf("ValidateMemoryCreator(%q) = %v", creator, err)
		}
	}
	if err := ValidateMemoryCreator("worker"); err == nil || PublicError(err).Code != "invalid_memory_creator" {
		t.Fatalf("invalid creator error = %v", err)
	}

	for _, text := range []string{"self-contained fact", "\t useful Unicode 界 \n"} {
		if err := ValidateMemoryText(text); err != nil {
			t.Errorf("ValidateMemoryText(%q) = %v", text, err)
		}
	}
	for _, text := range []string{"", " \t\n", string([]byte{0xff})} {
		if err := ValidateMemoryText(text); err == nil || PublicError(err).Code != "invalid_memory_text" {
			t.Errorf("ValidateMemoryText(%q) error = %v", text, err)
		}
	}
	tooLarge := strings.Repeat("x", MaxMemoryTextBytes+1)
	if err := ValidateMemoryText(tooLarge); err == nil || PublicError(err).Code != "memory_text_too_large" {
		t.Fatalf("oversize memory error = %v", err)
	}
}

func TestParseMemoryIDRequiresCanonicalPositiveDecimal(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		value string
		want  int64
	}{
		{value: "1", want: 1},
		{value: "42", want: 42},
		{value: "9223372036854775807", want: 9223372036854775807},
	} {
		got, err := ParseMemoryID(test.value)
		if err != nil || got != test.want {
			t.Errorf("ParseMemoryID(%q) = (%d, %v), want %d", test.value, got, err, test.want)
		}
	}
	for _, value := range []string{"", "0", "01", "-1", "+1", "1 ", "18446744073709551615"} {
		if _, err := ParseMemoryID(value); err == nil || PublicError(err).Code != "invalid_memory_id" {
			t.Errorf("ParseMemoryID(%q) error = %v", value, err)
		}
	}
}
