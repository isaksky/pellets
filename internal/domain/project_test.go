package domain

import "testing"

func TestValidateProjectCode(t *testing.T) {
	t.Parallel()

	valid := []string{"a", "0", "foo", "foo-bar", "a--b", "a1-b2", "abcdefghijkl"}
	for _, code := range valid {
		if err := ValidateProjectCode(code); err != nil {
			t.Errorf("ValidateProjectCode(%q) = %v, want nil", code, err)
		}
	}

	invalid := []string{"", "-a", "a-", "-", "ABCDEFGHIJKL", "abcdefghijklm", "Foo", "foo_bar", "foo bar", "café", "界"}
	for _, code := range invalid {
		err := ValidateProjectCode(code)
		if err == nil {
			t.Errorf("ValidateProjectCode(%q) = nil, want error", code)
			continue
		}
		if PublicError(err).Code != "invalid_project_code" {
			t.Errorf("ValidateProjectCode(%q) code = %q", code, PublicError(err).Code)
		}
	}
}
