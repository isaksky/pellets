package storage

import "testing"

func TestSidebarWidthSettings(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		valid      bool
	}{
		{"navigation_width", "140", true}, {"navigation_width", "360", true},
		{"execution_width", "280", true}, {"execution_width", "720", true},
		{"navigation_width", "139", false}, {"navigation_width", "361", false},
		{"execution_width", "279", false}, {"execution_width", "721", false},
		{"execution_width", "300px", false}, {"navigation_width", "180.5", false},
	} {
		if err := ValidateSetting(tc.key, tc.value); (err == nil) != tc.valid {
			t.Errorf("%s=%s: %v", tc.key, tc.value, err)
		}
	}
}
