package updater

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Asset names that release.sh publishes alongside the binary.
const (
	PluginsAssetName = "plugins.tar.gz"
	WwwrootAssetName = "wwwroot.tar.gz"
)

// ResolvePluginsDir returns the directory where ubuntu/plugins/* live at
// runtime. Mirrors pluginCandidates() in httpapi/plugins.go: env override,
// then repoRoot/plugins, "plugins" relative to CWD, then /home/plugins
// (Docker). Returns the first existing dir; otherwise the first candidate
// as a creation target.
func ResolvePluginsDir(repoRoot string) string {
	if v := strings.TrimSpace(os.Getenv("LAMPAC_GO_PLUGINS_DIR")); v != "" {
		return v
	}
	return firstExistingDir([]string{
		filepath.Join(repoRoot, "plugins"),
		"plugins",
		"/home/plugins",
	})
}

// ResolveWwwrootDir — same idea for wwwroot/.
func ResolveWwwrootDir(repoRoot string) string {
	return firstExistingDir([]string{
		filepath.Join(repoRoot, "wwwroot"),
		"wwwroot",
		"/home/wwwroot",
	})
}

func firstExistingDir(candidates []string) string {
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

// ApplyTarballAsset downloads the tarball at `url`, verifies SHA256
// against `wantSHA` (when set), extracts it next to `target` and atomically
// swaps the new content in. The old directory is preserved as
// `target+".old"` for rollback.
//
// The archive is expected to be gzip-compressed tar with a single top-level
// directory matching `basename(target)` — release.sh creates it via
// `tar -C parent -czf plugins.tar.gz plugins`. If the archive has no
// top-level wrapper, the staging directory itself becomes the new target.
func ApplyTarballAsset(url, wantSHA string, wantSize int64, target, token string) (string, error) {
	if target == "" {
		return "", errors.New("updater: empty target dir")
	}
	parent := filepath.Dir(target)
	base := filepath.Base(target)

	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("mkdir parent: %w", err)
	}

	suffix := fmt.Sprintf(".%d", time.Now().UnixNano())
	archive := filepath.Join(parent, base+".tar.gz.tmp"+suffix)
	defer os.Remove(archive)

	if err := downloadAndVerify(url, wantSHA, wantSize, archive, token); err != nil {
		return "", err
	}
	return ApplyTarballFile(archive, target)
}

// ApplyTarballFile extracts an ALREADY-DOWNLOADED gzip tar at `archive` and
// atomically swaps its contents into `target`, preserving operator custom
// files (mergeCustomIntoStage) and keeping the previous tree at target+".old"
// for rollback. Returns the backup path. The caller owns `archive` (it is not
// removed here). Shared by the self-updater (ApplyTarballAsset) and the mirror
// plugin/wwwroot syncer, which downloads with its own authenticated client.
func ApplyTarballFile(archive, target string) (string, error) {
	if target == "" {
		return "", errors.New("updater: empty target dir")
	}
	parent := filepath.Dir(target)
	base := filepath.Base(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("mkdir parent: %w", err)
	}

	suffix := fmt.Sprintf(".%d", time.Now().UnixNano())
	stageDir := filepath.Join(parent, "."+base+".new"+suffix)
	defer func() {
		if _, err := os.Stat(stageDir); err == nil {
			_ = os.RemoveAll(stageDir)
		}
	}()

	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir stage: %w", err)
	}
	if err := extractTarGz(archive, stageDir); err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}

	// Locate the new content. release.sh wraps everything in a single
	// top-level dir named after the target (plugins/wwwroot). Tolerate
	// archives without a wrapper too.
	extractedRoot := stageDir
	if entries, err := os.ReadDir(stageDir); err == nil && len(entries) == 1 && entries[0].IsDir() {
		extractedRoot = filepath.Join(stageDir, entries[0].Name())
	}

	// Preserve operator customisations: anything present in the live target
	// but absent from the archive (custom plugins, *.my.js overrides,
	// plugins/custom/*, hand-added files) is copied into the staged tree so
	// it survives the swap. Files that exist in BOTH are taken from the
	// archive — the source is authoritative for the stock set.
	if err := mergeCustomIntoStage(target, extractedRoot); err != nil {
		return "", fmt.Errorf("preserve custom files: %w", err)
	}

	backup := target + ".old"
	_ = os.RemoveAll(backup)

	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return "", fmt.Errorf("rename target→backup: %w", err)
		}
	}
	if err := os.Rename(extractedRoot, target); err != nil {
		// Restore the previous tree before bailing out.
		_ = os.Rename(backup, target)
		return "", fmt.Errorf("rename staged→target: %w", err)
	}
	return backup, nil
}

// mergeCustomIntoStage copies every file/dir under `target` that is NOT
// present in `stage` into `stage`, preserving operator customisations across
// an update. Files present in both are left as the archive shipped them (the
// release owns the stock set). A missing target is a no-op (fresh install).
//
// Symlinks and other non-regular entries in target are skipped — the plugins
// and wwwroot trees only ever contain regular files and directories, and
// blindly recreating a symlink could escape the tree.
func mergeCustomIntoStage(target, stage string) error {
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		return nil // nothing to merge (fresh install or target is a file)
	}

	// Set of relative paths the archive already provides.
	stagePaths := make(map[string]struct{})
	err := filepath.Walk(stage, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(stage, p)
		if rerr != nil {
			return rerr
		}
		if rel != "." {
			stagePaths[rel] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return err
	}

	return filepath.Walk(target, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(target, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if _, exists := stagePaths[rel]; exists {
			// The archive ships this path — keep the archive's version.
			// If it's a directory, descend so we can still pick up custom
			// files nested inside an otherwise-stock directory.
			return nil
		}
		dst := filepath.Join(stage, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm()|0o700)
		}
		if !info.Mode().IsRegular() {
			return nil // skip symlinks/devices/etc.
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return copyFilePreserve(p, dst, info.Mode().Perm())
	})
}

// copyFilePreserve copies src→dst with the given file mode.
func copyFilePreserve(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// RollbackTarballAsset swaps `target+".old"` back into `target` if such a
// backup exists. Returns ErrNoRollback when there is nothing to revert.
func RollbackTarballAsset(target string) error {
	backup := target + ".old"
	if _, err := os.Stat(backup); err != nil {
		return ErrNoRollback
	}
	tmp := target + ".rollback"
	_ = os.RemoveAll(tmp)
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, tmp); err != nil {
			return err
		}
	}
	if err := os.Rename(backup, target); err != nil {
		_ = os.Rename(tmp, target)
		return err
	}
	_ = os.RemoveAll(tmp)
	return nil
}

// downloadAndVerify streams `url` into `dst`, verifying SHA256 when
// `wantSHA` is non-empty. `wantSize` > 0 triggers a pre-flight free-space
// check. token authorizes password-protected channels.
func downloadAndVerify(url, wantSHA string, wantSize int64, dst, token string) error {
	if wantSize > 0 {
		if err := ensureDiskSpace(dst, wantSize); err != nil {
			return err
		}
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "lampac-go-updater")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("write archive: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if wantSHA != "" && got != wantSHA {
		_ = os.Remove(dst)
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, wantSHA)
	}
	return nil
}

// extractTarGz unpacks `src` into `dst`. Rejects absolute paths and any
// entry that would escape `dst`. Non-regular non-directory entries
// (symlinks, devices, fifos) are skipped — release.sh tarballs only
// contain regular files and directories.
func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
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
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if name == "" || name == "." {
			continue
		}
		if strings.HasPrefix(name, "..") || strings.Contains(name, "/../") || filepath.IsAbs(name) {
			return fmt.Errorf("unsafe path in archive: %q", h.Name)
		}
		out := filepath.Join(dst, name)
		if rel, err := filepath.Rel(dst, out); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("path escape: %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			of, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(of, tr); err != nil {
				_ = of.Close()
				return err
			}
			if err := of.Close(); err != nil {
				return err
			}
		default:
			log.Debug().Str("name", h.Name).Int("typeflag", int(h.Typeflag)).Msg("updater: skipping non-regular tar entry")
		}
	}
}
