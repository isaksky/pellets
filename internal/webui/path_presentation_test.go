package webui

import (
	"testing"

	"pellets/internal/domain"
)

func TestRepositoryLocationPresentation(t *testing.T) {
	for _, tc := range []struct {
		name, root, value, label, full string
		relative                       bool
	}{
		{"database checkout", "/src/tool", ".git", "Database folder", "/src/tool", true},
		{"nested checkout", "/src", "tools/tool/.git", "tools/tool", "/src/tools/tool", true},
		{"root database", "/", "tool/.git", "tool", "/tool", true},
		{"outside database", "/src", "/other/tool/.git", "/other/tool", "/other/tool", false},
		{"windows database checkout", "C:/src/tool", ".git", "Database folder", "C:/src/tool", true},
		{"windows nested", "C:/src", "tool/.git", "tool", "C:/src/tool", true},
		{"drive root", "D:/src", "C:/.git", "C:/", "C:/", false},
		{"filesystem root", "/src", "/.git", "/", "/", false},
		{"other drive", "C:/src", "D:/tool/.git", "D:/tool", "D:/tool", false},
		{"UNC", "//server/share", "tool/.git", "tool", "//server/share/tool", true},
		{"absolute UNC", "C:/src", "//server/share/tool/.git", "//server/share/tool", "//server/share/tool", false},
		{"separate git directory", "/src", "git-data", "git-data", "/src/git-data", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := domain.LocalPath{Value: tc.value, Relative: tc.relative}
			if got := repositoryPath(value); got != tc.label {
				t.Errorf("label = %q; want %q", got, tc.label)
			}
			if got := repositoryFullPath(tc.root, value); got != tc.full {
				t.Errorf("full path = %q; want %q", got, tc.full)
			}
		})
	}
}
