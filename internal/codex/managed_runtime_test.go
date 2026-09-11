package codex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func testRuntimeArchive(t *testing.T, name string, kind byte) ([]byte, runtimeAsset) {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	data := []byte("test executable")
	h := &tar.Header{Name: name, Typeflag: kind, Mode: 0755, Size: int64(len(data))}
	if kind != tar.TypeReg {
		h.Size = 0
		h.Linkname = "outside"
	}
	if err := tw.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if kind == tar.TypeReg {
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), runtimeAsset{Target: "test", SHA256: fmt.Sprintf("%x", sha256.Sum256(b.Bytes()))}
}
func TestManagedRuntimeVerifiedCacheAndOffline(t *testing.T) {
	data, asset := testRuntimeArchive(t, "bin/codex", tar.TypeReg)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); w.Write(data) }))
	defer server.Close()
	cache := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := installRuntime(context.Background(), cache, ManagedVersion, asset, server.URL, server.Client(), false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	count := downloads.Load()
	exe, err := installRuntime(context.Background(), cache, ManagedVersion, asset, server.URL, server.Client(), true)
	if err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != count {
		t.Fatal("cached runtime used network")
	}
	if err := os.WriteFile(exe, []byte("tampered bytes!"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = installRuntime(context.Background(), cache, ManagedVersion, asset, server.URL, server.Client(), true); err == nil {
		t.Fatal("accepted corrupted installed runtime")
	}
}
func TestManagedRuntimeRejectsUnverifiedAndUnsafeDownloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    byte
		badHash bool
	}{{"../escape", tar.TypeReg, false}, {"/absolute", tar.TypeReg, false}, {"bin/link", tar.TypeSymlink, false}, {"bin/codex", tar.TypeReg, true}, {`C:\escape`, tar.TypeReg, false}} {
		t.Run(tc.name, func(t *testing.T) {
			data, asset := testRuntimeArchive(t, tc.name, tc.kind)
			if tc.badHash {
				asset.SHA256 = strings.Repeat("0", 64)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
			defer server.Close()
			cache := t.TempDir()
			if _, err := installRuntime(context.Background(), cache, ManagedVersion, asset, server.URL, server.Client(), false); err == nil {
				t.Fatal("accepted unsafe package")
			}
			if _, err := os.Stat(filepath.Join(cache, ManagedVersion, asset.Target)); !os.IsNotExist(err) {
				t.Fatal("published failed install")
			}
		})
	}
}
func TestManagedRuntimeFailuresAndCancellation(t *testing.T) {
	_, asset := testRuntimeArchive(t, "bin/codex", tar.TypeReg)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer server.Close()
	for _, offline := range []bool{true, false} {
		if _, err := installRuntime(context.Background(), t.TempDir(), ManagedVersion, asset, server.URL, server.Client(), offline); err == nil {
			t.Fatal("expected actionable installation failure")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := installRuntime(ctx, t.TempDir(), ManagedVersion, asset, server.URL, server.Client(), false); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := ResolveManagedRuntime(ctx, "../../version"); err == nil {
		t.Fatal("accepted arbitrary version")
	}
}
func TestRuntimeVersionFloor(t *testing.T) {
	for _, v := range []string{"codex-cli 0.151.0", "codex-cli 0.153.0", "codex-cli 0.154.0-alpha.1", "garbage"} {
		if checkRuntimeVersion(v) == nil {
			t.Fatalf("accepted %s", v)
		}
	}
	for _, v := range []string{"codex-cli 0.154.0", "codex-cli 0.155.1", "codex-cli 1.0.0"} {
		if err := checkRuntimeVersion(v); err != nil {
			t.Fatal(err)
		}
	}
}

// Optional native package smoke test uses only an already downloaded archive
// and an isolated CODEX_HOME. It neither reads an account nor starts a turn.
func TestManagedRuntimeNativePackage(t *testing.T) {
	archive := os.Getenv("PELLETS_CODEX_TEST_PACKAGE")
	if archive == "" {
		t.Skip("set PELLETS_CODEX_TEST_PACKAGE to the official native package archive")
	}
	asset, ok := runtimeAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		t.Fatal("missing native asset")
	}
	cache := t.TempDir()
	parent := filepath.Join(cache, ManagedVersion)
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(parent, asset.Target+".tar.gz"), data, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := installRuntime(context.Background(), cache, ManagedVersion, asset, "", http.DefaultClient, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PELLETS_CODEX_CACHE", cache)
	t.Setenv("PELLETS_CODEX_OFFLINE", "1")
	t.Setenv("PATH", "")
	c, err := Start(context.Background(), Config{Dir: t.TempDir(), Env: []string{"CODEX_HOME=" + t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.Runtime().Executable != exe || c.Runtime().Version != "codex-cli "+ManagedVersion {
		t.Fatal(c.Runtime())
	}
}
