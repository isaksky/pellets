package domain

import "testing"

func TestGenerateProjectCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		repository string
		identity   string
		forceHash  bool
		attempt    uint64
		want       string
	}{
		{name: "plain normalized", repository: "Demo Service", identity: "demo/.git", want: "demo-service"},
		{name: "punctuation collapsed", repository: "--API__Worker--", identity: "api/.git", want: "api-worker"},
		{name: "empty normalized", repository: "界", identity: "unicode/.git", want: "p-51791df8"},
		{name: "truncated uses identity hash", repository: "abcdefghijklmnop", identity: "long/.git", want: "abc-e1bd336d"},
		{name: "collision uses identity hash", repository: "demo", identity: "other/.git", forceHash: true, want: "dem-f970b2e7"},
		{name: "later collision changes deterministically", repository: "demo", identity: "other/.git", forceHash: true, attempt: 1, want: "dem-71ec38d4"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := GenerateProjectCode(test.repository, test.identity, test.forceHash, test.attempt)
			if got != test.want {
				t.Fatalf("GenerateProjectCode() = %q, want %q", got, test.want)
			}
			if err := ValidateProjectCode(got); err != nil {
				t.Fatalf("generated code %q is invalid: %v", got, err)
			}
		})
	}
}
