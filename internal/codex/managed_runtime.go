package codex

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ManagedVersion and hashes are reviewed together when updating the runtime.
// Source: openai/codex release rust-v0.154.0, GitHub asset SHA-256 digests.
const ManagedVersion = "0.154.0"
const maxRuntimeArchive = 512 << 20
const maxRuntimeExpanded = 2 << 30

type runtimeAsset struct{ Target, SHA256 string }

var runtimeAssets = map[string]runtimeAsset{
	"darwin/arm64":  {"aarch64-apple-darwin", "427ca74c027049e0cd1a330d611e7f8d1fe0f1eb6a6d85ac16f61bcf2cb4a485"},
	"darwin/amd64":  {"x86_64-apple-darwin", "8052c6accbe0361bfbd424a10aa5f2226636ed8afb6dcbd5e6437993e57b16d8"},
	"windows/amd64": {"x86_64-pc-windows-msvc", "94cc5b3632769504c809f6c0364b693c0dfddc5c30c8361095d2263a07ac45a4"},
}

// ResolveManagedRuntime installs only a reviewed release, without invoking a
// package manager or reading workspace configuration. No credentials are moved.
func ResolveManagedRuntime(ctx context.Context, version string) (string, error) {
	if version == "" {
		version = ManagedVersion
	}
	if version != ManagedVersion {
		return "", fmt.Errorf("unsupported managed Codex version %q; this Pellets build manages %s; update Pellets or use an explicit executable override", version, ManagedVersion)
	}
	asset, ok := runtimeAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("managed Codex is unavailable for %s/%s; configure an explicit compatible executable", runtime.GOOS, runtime.GOARCH)
	}
	cache := os.Getenv("PELLETS_CODEX_CACHE")
	if cache == "" {
		var err error
		cache, err = os.UserCacheDir()
		if err != nil {
			return "", err
		}
		cache = filepath.Join(cache, "pellets", "codex")
	}
	if !filepath.IsAbs(cache) {
		return "", errors.New("PELLETS_CODEX_CACHE must be an absolute directory path")
	}
	cache, err := filepath.Abs(cache)
	if err != nil {
		return "", err
	}
	url := "https://github.com/openai/codex/releases/download/rust-v" + version + "/codex-package-" + asset.Target + ".tar.gz"
	executable, err := installRuntime(ctx, cache, version, asset, url, &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 10 || req.URL.Scheme != "https" {
			return errors.New("unsafe runtime download redirect")
		}
		return nil
	}}, os.Getenv("PELLETS_CODEX_OFFLINE") == "1")
	if err != nil {
		return "", fmt.Errorf("install Codex %s (%s) in %s: %w; check network/disk permissions, remove this version's cache if corrupt and retry online, or set PELLETS_CODEX_EXECUTABLE to a compatible Codex CLI", version, asset.Target, cache, err)
	}
	return executable, nil
}

func installRuntime(ctx context.Context, cache, version string, asset runtimeAsset, url string, client *http.Client, offline bool) (string, error) {
	parent := filepath.Join(cache, version)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	archive := filepath.Join(parent, asset.Target+".tar.gz")
	if err := verifyRuntimeArchive(archive, asset.SHA256); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if offline {
			return "", errors.New("runtime is not cached and PELLETS_CODEX_OFFLINE=1")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
		}
		f, err := os.CreateTemp(parent, ".download-")
		if err != nil {
			return "", err
		}
		defer os.Remove(f.Name())
		n, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxRuntimeArchive+1))
		closeErr := f.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if n > maxRuntimeArchive {
			return "", errors.New("runtime archive exceeds size limit")
		}
		if err := verifyRuntimeArchive(f.Name(), asset.SHA256); err != nil {
			return "", err
		}
		if err := os.Rename(f.Name(), archive); err != nil {
			if existing := verifyRuntimeArchive(archive, asset.SHA256); existing != nil {
				return "", err
			}
		}
	}
	destination := filepath.Join(parent, asset.Target)
	if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
		staging, err := os.MkdirTemp(parent, ".install-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(staging)
		if err := unpackRuntime(ctx, archive, staging, false); err != nil {
			return "", err
		}
		if err := os.Rename(staging, destination); err != nil {
			if _, statErr := os.Stat(destination); statErr != nil {
				return "", err
			}
		}
	} else if err != nil {
		return "", err
	}
	// Recheck installed bytes against the authenticated archive, including sidecars.
	// Never trust a locally generated checksum manifest or a partial installation.
	if err := unpackRuntime(ctx, archive, destination, true); err != nil {
		return "", err
	}
	exe := "codex"
	if strings.Contains(asset.Target, "windows") {
		exe += ".exe"
	}
	executable := filepath.Join(destination, "bin", exe)
	if info, err := os.Lstat(executable); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("runtime package lacks its Codex entrypoint")
	}
	return executable, nil
}

func verifyRuntimeArchive(file, digest string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxRuntimeArchive+1))
	if err != nil {
		return err
	}
	if n > maxRuntimeArchive || hex.EncodeToString(h.Sum(nil)) != digest {
		return errors.New("runtime archive SHA-256 mismatch (refusing to execute)")
	}
	return nil
}

func unpackRuntime(ctx context.Context, archive, root string, verify bool) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("runtime directory is not a real directory")
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" || name != path.Clean(name) || strings.ContainsAny(name, "\\:") || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || seen[name] || !filepath.IsLocal(filepath.FromSlash(name)) || len(seen) >= 4096 {
			return fmt.Errorf("unsafe runtime archive path %q", name)
		}
		seen[name] = true
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsupported runtime archive entry %q", name)
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		// Check every existing parent: no symlink traversal even in a cached tree.
		for parent := filepath.Dir(target); parent != root; parent = filepath.Dir(parent) {
			if info, err := os.Lstat(parent); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
				return errors.New("unsafe runtime parent directory")
			}
		}
		if header.Typeflag == tar.TypeDir {
			if verify {
				info, err := os.Lstat(target)
				if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("runtime directory damaged: %s", name)
				}
			} else if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		total += header.Size
		if header.Size < 0 || total > maxRuntimeExpanded {
			return errors.New("expanded runtime exceeds size limit")
		}
		if verify {
			info, err := os.Lstat(target)
			if err != nil || !info.Mode().IsRegular() || info.Size() != header.Size || (runtime.GOOS != "windows" && header.Mode&0111 != 0 && info.Mode()&0100 == 0) {
				return fmt.Errorf("runtime file damaged: %s", name)
			}
			installed, err := os.Open(target)
			if err != nil {
				return err
			}
			a, b := sha256.New(), sha256.New()
			_, err = io.Copy(a, installed)
			installed.Close()
			if err != nil {
				return err
			}
			if _, err := io.Copy(b, tr); err != nil {
				return err
			}
			if !strings.EqualFold(hex.EncodeToString(a.Sum(nil)), hex.EncodeToString(b.Sum(nil))) {
				return fmt.Errorf("runtime file checksum mismatch: %s", name)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			mode := os.FileMode(0600)
			if header.Mode&0111 != 0 {
				mode = 0700
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	// Consume the gzip trailer so truncated or corrupt streams cannot be installed.
	n, err := io.Copy(io.Discard, io.LimitReader(gz, maxRuntimeExpanded+1))
	if err != nil {
		return err
	}
	if n > maxRuntimeExpanded {
		return errors.New("runtime trailer exceeds size limit")
	}
	if verify {
		return filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if file == root {
				return nil
			}
			relative, err := filepath.Rel(root, file)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(relative)
			if !seen[name] && !entry.IsDir() {
				return fmt.Errorf("unexpected file in runtime cache: %s", name)
			}
			return nil
		})
	}
	return nil
}
