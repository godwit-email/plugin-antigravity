package main

// The official ACP server, fetched from Google at sign-in and pinned by hash.

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

const (
	serverVersion = "1.4.5"
	serverName    = "agy_acp_server.par"
	harnessName   = "localharness_external"
)

type serverRelease struct {
	url                       string
	size                      int64
	sha256                    string
	serverBytes, harnessBytes int64
}

// macOS only (ADR 0033): elsewhere the server keeps its token in a plain file.
var releases = map[string]serverRelease{
	"darwin/arm64": {"https://dl.google.com/agy-extensions/releases/macos/agy-acp-server-1.4.5-darwin-arm64.zip", 97_923_855,
		"886fff7767d08994ca39eb02c3d01a7532c7325a94aa33aaf52d52068733284f", 178_915_920, 122_957_936},
	"darwin/amd64": {"https://dl.google.com/agy-extensions/releases/macos/agy-acp-server-1.4.5-darwin-x86_64.zip", 103_577_295,
		"b39f3573c03b9acb9df028853f647527f1f3133f32bd77e78b796e48a205114a", 183_127_600, 127_612_208},
}

// runtimeDir is where this version's server lives under dataDir.
func runtimeDir(dataDir string) string { return filepath.Join(dataDir, "runtime", serverVersion) }

func regular(path string, size int64) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Size() == size
}

// runtimeInstalled reports whether dataDir holds the whole server. The rename
// in installRuntime makes it all or nothing.
func runtimeInstalled(dataDir string) bool {
	r, ok := releases[runtime.GOOS+"/"+runtime.GOARCH]
	dir := runtimeDir(dataDir)
	return ok && regular(filepath.Join(dir, serverName), r.serverBytes) && regular(filepath.Join(dir, harnessName), r.harnessBytes)
}

// installRuntime downloads and checks the pinned archive, then moves the two
// programs into place.
func installRuntime(ctx context.Context, dataDir string) error {
	if runtimeInstalled(dataDir) {
		return nil
	}
	r, ok := releases[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return errors.New("this plugin does not support this computer")
	}
	parent := filepath.Dir(runtimeDir(dataDir))
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "server.zip")
	if err := download(ctx, r, archive); err != nil {
		return err
	}
	dest := filepath.Join(tmp, "out")
	if err := os.Mkdir(dest, 0o700); err != nil {
		return err
	}
	if err := extract(archive, dest, map[string]int64{serverName: r.serverBytes, harnessName: r.harnessBytes}); err != nil {
		return err
	}
	if err := os.RemoveAll(runtimeDir(dataDir)); err != nil {
		return err
	}
	return os.Rename(dest, runtimeDir(dataDir))
}

func download(ctx context.Context, r serverRelease, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading the Antigravity server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading the Antigravity server: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, r.size+1))
	if err != nil {
		return fmt.Errorf("downloading the Antigravity server: %w", err)
	}
	if n != r.size || hex.EncodeToString(h.Sum(nil)) != r.sha256 {
		return errors.New("the downloaded Antigravity server is not the expected file")
	}
	return f.Close()
}

// extract writes exactly the expected entries, each of its expected size.
func extract(archive, dest string, want map[string]int64) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	seen := 0
	for _, e := range zr.File {
		if e.FileInfo().IsDir() {
			continue
		}
		size, ok := want[e.Name]
		if !ok || int64(e.UncompressedSize64) != size {
			return fmt.Errorf("unexpected entry %q in the Antigravity archive", e.Name)
		}
		if err := extractOne(e, filepath.Join(dest, e.Name), size); err != nil {
			return err
		}
		seen++
	}
	if seen != len(want) {
		return errors.New("the Antigravity archive is missing a program")
	}
	return nil
}

func extractOne(e *zip.File, path string, size int64) error {
	in, err := e.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	defer out.Close()
	if n, err := io.Copy(out, io.LimitReader(in, size+1)); err != nil || n != size {
		return errors.Join(errors.New("the Antigravity archive is damaged"), err)
	}
	return out.Close()
}
