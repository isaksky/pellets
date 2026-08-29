package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

var releaseLicenseFiles = map[string]string{
	"LICENSE":                 "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30",
	"THIRD_PARTY_NOTICES.txt": "10aea239a00997bcea8a46b8add4357754b4bfdeb693bd161ea341c8f143cbbb",
	"internal/webui/assets/htmx-2.0.4.min.js": "e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447",
	"internal/webui/assets/HTMX-LICENSE.txt":  "d3d2456f76414f2456104660ebd65aff1c04cd7966b942bdabd63f3cdb316a38",
	"internal/webui/assets/HTMX-NOTICE.txt":   "b667e742a2f2f354c4699725573e91c49efd57b3eff4a82c278d8a74b71e379e",
}

var auditedReleaseModules = []string{
	"github.com/dustin/go-humanize v1.0.1",
	"github.com/google/uuid v1.6.0",
	"github.com/mattn/go-isatty v0.0.20",
	"github.com/ncruces/go-strftime v1.0.0",
	"github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec",
	"golang.org/x/exp v0.0.0-20251023183803-a4bb9ffd2546",
	"golang.org/x/sys v0.37.0",
	"modernc.org/libc v1.67.6",
	"modernc.org/mathutil v1.7.1",
	"modernc.org/memory v1.11.0",
	"modernc.org/sqlite v1.45.0",
}

func TestReleaseLicensePayloadStaysAudited(t *testing.T) {
	repositoryRoot := licenseRepositoryRoot(t)
	contents := make(map[string][]byte, len(releaseLicenseFiles))

	for path, expectedDigest := range releaseLicenseFiles {
		content, err := os.ReadFile(filepath.Join(repositoryRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read required release license input %s: %v", path, err)
		}
		actualDigest := fmt.Sprintf("%x", sha256.Sum256(content))
		if actualDigest != expectedDigest {
			t.Errorf("%s SHA-256 = %s, want audited %s", path, actualDigest, expectedDigest)
		}
		contents[path] = content
	}

	notices := contents["THIRD_PARTY_NOTICES.txt"]
	for _, path := range []string{
		"internal/webui/assets/HTMX-LICENSE.txt",
		"internal/webui/assets/HTMX-NOTICE.txt",
	} {
		if !bytes.Contains(notices, bytes.TrimSpace(contents[path])) {
			t.Errorf("THIRD_PARTY_NOTICES.txt does not reproduce %s", path)
		}
	}

	for _, module := range auditedReleaseModules {
		if !bytes.Contains(notices, []byte("Module: "+module+"\n")) {
			t.Errorf("THIRD_PARTY_NOTICES.txt has no audited marker for %s", module)
		}
	}

	for _, target := range []struct {
		goos   string
		goarch string
	}{
		{goos: "darwin", goarch: "amd64"},
		{goos: "darwin", goarch: "arm64"},
		{goos: "windows", goarch: "amd64"},
	} {
		t.Run(target.goos+"/"+target.goarch, func(t *testing.T) {
			actual := releaseModulesForTarget(t, repositoryRoot, target.goos, target.goarch)
			expected := auditedReleaseModulesForTarget(target.goos)
			if !slices.Equal(actual, expected) {
				t.Fatalf("linked release modules =\n%s\nwant audited inventory =\n%s", strings.Join(actual, "\n"), strings.Join(expected, "\n"))
			}
		})
	}
}

func auditedReleaseModulesForTarget(goos string) []string {
	modules := slices.Clone(auditedReleaseModules)
	if goos == "windows" {
		modules = slices.Delete(modules, 1, 2)
	}
	return modules
}

func licenseRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate license contract test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
}

func releaseModulesForTarget(t *testing.T, repositoryRoot, goos, goarch string) []string {
	t.Helper()
	command := exec.Command(
		"go", "list", "-deps", "-f",
		`{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}{{end}}{{end}}`,
		"./cmd/pl",
	)
	command.Dir = repositoryRoot
	command.Env = append(command.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list %s/%s release dependencies: %v\n%s", goos, goarch, err, output)
	}

	unique := make(map[string]struct{})
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			unique[line] = struct{}{}
		}
	}
	modules := make([]string, 0, len(unique))
	for module := range unique {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	return modules
}
