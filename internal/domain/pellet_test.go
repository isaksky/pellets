package domain

import "testing"

func TestParsePelletReferenceUsesFinalHyphen(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		value string
		code  string
		num   int64
	}{
		{value: "foo-1", code: "foo", num: 1},
		{value: "foo-bar-123", code: "foo-bar", num: 123},
		{value: "a--b-9223372036854775807", code: "a--b", num: 9223372036854775807},
	} {
		t.Run(test.value, func(t *testing.T) {
			reference, err := ParsePelletReference(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if reference.ProjectCode != test.code || reference.Number != test.num || reference.String() != test.value {
				t.Fatalf("ParsePelletReference(%q) = %#v, string %q", test.value, reference, reference.String())
			}
		})
	}
}

func TestParsePelletReferenceRejectsNonCanonicalAndInvalidValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"", "1", "-1", "foo-", "foo-0", "foo-01", "foo-+1", "foo--1",
		"Upper-1", "foo_bar-1", "foo-1 ", "foo-18446744073709551616",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := ParsePelletReference(value)
			if err == nil || PublicError(err).Code != "invalid_reference" || PublicError(err).Kind != Usage {
				t.Fatalf("ParsePelletReference(%q) error = %v", value, err)
			}
		})
	}
}
