package codex

import (
	"strconv"
	"strings"
	"testing"
)

func TestOwnedCommandStderrOmitsTruncatedCredentialFragments(t *testing.T) {
	for _, test := range []struct{ name, text, want string }{
		{"token_no_newline", "token=" + strings.Repeat("SECRET-FRAGMENT", 30), ""},
		{"unterminated_quoted_no_newline", "{\"token\":\"" + strings.Repeat("SECRET-FRAGMENT ", 30), ""},
		{"authorization", "Authorization: Bearer " + strings.Repeat("SECRET-FRAGMENT", 30) + "\nUseful signing failure\n", "Useful signing failure"},
		{"malformed_no_newline", "token=" + strings.Repeat("SECRET-FRAGMENT", 30) + "\xff\xfe\rSECRET-FRAGMENT", ""},
		{"malformed_then_diagnostic", "token=" + strings.Repeat("SECRET-FRAGMENT", 30) + "\xff\xfe\rSECRET-FRAGMENT\nUseful error without final newline", "Useful error without final newline"},
	} {
		for _, chunk := range []int{1, 17, len(test.text)} {
			t.Run(test.name+"/"+strconv.Itoa(chunk), func(t *testing.T) {
				buffer := &commandStderr{limit: 64}
				for i := 0; i < len(test.text); i += chunk {
					end := min(i+chunk, len(test.text))
					if n, err := buffer.Write([]byte(test.text[i:end])); err != nil || n != end-i {
						t.Fatalf("write = %d %v", n, err)
					}
				}
				output := buffer.String()
				if strings.Contains(output, "SECRET") || strings.Contains(output, "FRAGMENT") || !strings.Contains(output, "leading line omitted") || test.want != "" && !strings.Contains(output, test.want) {
					t.Fatalf("truncated credential escaped or diagnostic lost: %q", output)
				}
			})
		}
	}
	buffer := &commandStderr{limit: 64}
	_, _ = buffer.Write([]byte("short diagnostic without newline"))
	if got := buffer.String(); got != "short diagnostic without newline" {
		t.Fatalf("untruncated diagnostic changed: %q", got)
	}
}
