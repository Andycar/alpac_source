package jacred

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const releaseBase = "https://github.com/jacred-fdb/jacred/releases/latest/download"

// versionStampFile records the installed release tag next to the binary.
const versionStampFile = ".jacred_version"

// releaseAssetName maps GOOS/GOARCH to the jacred-fdb release asset.
// Assets (3.0.0): jacred-linux-{amd64,arm,arm64}.zip, jacred-linux-musl-*.zip,
// jacred-osx-{amd64,arm64}.zip, jacred-win-{x64,x86,arm64}.zip.
func releaseAssetName(goos, goarch string) (string, error) {
	switch goos {
	case "linux":
		switch goarch {
		case "amd64", "arm64", "arm":
			return "jacred-linux-" + goarch + ".zip", nil
		}
	case "darwin":
		switch goarch {
		case "amd64", "arm64":
			return "jacred-osx-" + goarch + ".zip", nil
		}
	case "windows":
		switch goarch {
		case "amd64":
			return "jacred-win-x64.zip", nil
		case "arm64":
			return "jacred-win-arm64.zip", nil
		case "386":
			return "jacred-win-x86.zip", nil
		}
	}
	return "", fmt.Errorf("jacred: no release asset for %s/%s", goos, goarch)
}

// Install downloads the latest jacred-fdb release and unpacks it into homeDir.
// Safe to call for updates — existing Data/ and init.conf are never touched
// (the zip only carries the runtime payload; Data entries inside the zip are
// skipped when the target already exists).
func Install(ctx context.Context, homeDir string) error {
	asset, err := releaseAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return err
	}

	url := releaseBase + "/" + asset
	log.Info().Str("url", url).Msg("jacred: downloading release")

	tmp, err := downloadToTemp(ctx, url, homeDir, "jacred-release-*.zip")
	if err != nil {
		return fmt.Errorf("download release: %w", err)
	}
	defer os.Remove(tmp)

	if err := extractZip(tmp, homeDir); err != nil {
		return fmt.Errorf("extract release: %w", err)
	}

	bin, err := binaryPath(homeDir)
	if err != nil {
		return err
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return err
	}

	if tag := latestReleaseTag(ctx); tag != "" {
		_ = os.WriteFile(filepath.Join(homeDir, versionStampFile), []byte(tag), 0o644)
	}
	log.Info().Str("bin", bin).Msg("jacred: installed")
	return nil
}

func installedVersion(homeDir string) string {
	b, err := os.ReadFile(filepath.Join(homeDir, versionStampFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func latestReleaseTag(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/jacred-fdb/jacred/releases/latest", nil)
	if err != nil {
		return ""
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return ""
	}
	return payload.TagName
}

// downloadToTemp streams url into a temp file inside dir (same filesystem as
// the final destination so no cross-device surprises). Logs progress for
// large payloads (the FDB archive is multi-GB).
func downloadToTemp(ctx context.Context, url, dir, pattern string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 0} // size unbounded; ctx cancels
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}

	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()

	var written int64
	lastLog := time.Now()
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(name)
				return "", werr
			}
			written += int64(n)
			if time.Since(lastLog) > 10*time.Second {
				log.Info().Int64("mb", written/(1024*1024)).Int64("total_mb", resp.ContentLength/(1024*1024)).
					Msg("jacred: downloading…")
				lastLog = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(name)
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// extractZip unpacks src into dstDir. Existing Data/* and init.conf are left
// alone so updates don't clobber the database or user config. Paths are
// sanitized against zip-slip.
func extractZip(src, dstDir string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		rel := filepath.Clean(f.Name)
		if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			continue
		}
		target := filepath.Join(dstDir, rel)
		if !strings.HasPrefix(target, filepath.Clean(dstDir)+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		// Never overwrite live data/config on update.
		base := filepath.ToSlash(rel)
		if base == "init.conf" || base == "init.yaml" || strings.HasPrefix(base, "Data/") {
			if _, err := os.Stat(target); err == nil {
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		// Write via temp + rename: overwriting a *running* JacRed binary in
		// place fails with ETXTBSY on Linux; rename over it is always fine.
		tmpOut, err := os.CreateTemp(filepath.Dir(target), ".extract-*")
		if err != nil {
			rc.Close()
			return err
		}
		_, cerr := io.Copy(tmpOut, rc)
		rc.Close()
		if err := tmpOut.Close(); err != nil && cerr == nil {
			cerr = err
		}
		if cerr == nil {
			cerr = os.Chmod(tmpOut.Name(), f.Mode().Perm()|0o644)
		}
		if cerr == nil {
			cerr = os.Rename(tmpOut.Name(), target)
		}
		if cerr != nil {
			os.Remove(tmpOut.Name())
			return cerr
		}
	}
	return nil
}
