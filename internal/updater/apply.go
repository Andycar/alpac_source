package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/rs/zerolog/log"
)

// diskSafetyMargin is extra headroom required on top of the download size, so
// an update never fills the filesystem to the brim — the running process still
// needs scratch space for logs, caches and history.json.
const diskSafetyMargin uint64 = 32 << 20 // 32 MiB

// diskFree probes free bytes for the filesystem backing dir. It is a package
// var so tests can stub it; the real implementation lives in the
// platform-specific diskspace_*.go files.
var diskFree = freeDiskBytes

// ErrNoRollback is returned by Rollback when no previous binary is kept.
var ErrNoRollback = errors.New("updater: nothing to rollback to")

// DownloadResult describes a freshly staged binary ready to be activated.
type DownloadResult struct {
	StagedPath string // path to "<exe>.new" with the downloaded bytes
	Size       int64
	SHA256     string // hex digest of the staged file
}

// DownloadAsset fetches the asset URL into "<currentExe>.new", verifies its
// SHA256 (if wantSHA is non-empty), chmods it executable and returns info
// about the staged file. wantSize is the expected byte size from the release
// metadata; when > 0 it drives a pre-flight free-space check so a full disk
// fails fast with a clear message instead of a raw ENOSPC mid-download.
// token authorizes downloads from password-protected channels. The caller
// is expected to call Activate next.
func DownloadAsset(url, wantSHA string, wantSize int64, token string) (*DownloadResult, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("executable: %w", err)
	}
	// Resolve symlinks so we replace the real file, not a launcher symlink.
	if rp, err := filepath.EvalSymlinks(exe); err == nil {
		exe = rp
	}

	staged := exe + ".new"
	_ = os.Remove(staged) // leftover from a previous failed attempt

	if wantSize > 0 {
		if err := ensureDiskSpace(exe, wantSize); err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lampac-go-updater")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return nil, fmt.Errorf("create staged: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(staged)
		return nil, fmt.Errorf("write staged: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if wantSHA != "" && got != wantSHA {
		_ = os.Remove(staged)
		return nil, fmt.Errorf("sha256 mismatch: got %s, want %s", got, wantSHA)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(staged, 0o755); err != nil {
			_ = os.Remove(staged)
			return nil, fmt.Errorf("chmod staged: %w", err)
		}
	}

	return &DownloadResult{StagedPath: staged, Size: n, SHA256: got}, nil
}

// ensureDiskSpace verifies the filesystem holding the staged binary has room
// for wantSize bytes plus a safety margin. If it falls short and a stale
// rollback backup ("<exe>.old") is present, that backup is removed to reclaim
// space: Activate recreates a fresh, correct ".old" from the running binary
// right after the download, so the only rollback target sacrificed is the
// stale pre-update one. Returns a clear error (path + figures) when even that
// is not enough.
func ensureDiskSpace(exe string, wantSize int64) error {
	dir := filepath.Dir(exe)
	free, ok := diskFree(dir)
	if !ok {
		return nil // platform can't report free space — skip the check
	}
	needed := uint64(wantSize) + diskSafetyMargin
	if free >= needed {
		return nil
	}

	backup := exe + ".old"
	if fi, statErr := os.Stat(backup); statErr == nil {
		if rmErr := os.Remove(backup); rmErr == nil {
			free += uint64(fi.Size())
			log.Warn().Str("backup", backup).Int64("freed_bytes", fi.Size()).
				Msg("updater: removed stale rollback backup to make room for the new binary")
		}
	}
	if free >= needed {
		return nil
	}
	return fmt.Errorf("not enough disk space in %s: need %s for the new binary, only %s free",
		dir, humanBytes(needed), humanBytes(free))
}

// humanBytes renders a byte count as an approximate MiB/KiB string for messages.
func humanBytes(n uint64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// Activate atomically replaces the running binary with the staged one and
// keeps the old binary as "<exe>.old" for rollback. Call this BEFORE Restart.
func Activate(staged string) (backup string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("executable: %w", err)
	}
	if rp, err := filepath.EvalSymlinks(exe); err == nil {
		exe = rp
	}

	backup = exe + ".old"
	_ = os.Remove(backup)

	// Rename current → backup. If the renamed file is the one currently
	// being executed, that's fine on Linux/macOS — the kernel holds the
	// inode open until the process exits.
	if err := os.Rename(exe, backup); err != nil {
		return "", fmt.Errorf("rename current→backup: %w", err)
	}
	// Move staged → current.
	if err := os.Rename(staged, exe); err != nil {
		// Try to restore backup before giving up.
		_ = os.Rename(backup, exe)
		return "", fmt.Errorf("rename staged→current: %w", err)
	}
	return backup, nil
}

// Rollback restores "<exe>.old" as the running binary. Useful if the new
// build failed to start on the next boot; the supervisor can flip an
// environment variable and invoke this helper.
func Rollback() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if rp, err := filepath.EvalSymlinks(exe); err == nil {
		exe = rp
	}
	backup := exe + ".old"
	if _, err := os.Stat(backup); err != nil {
		return ErrNoRollback
	}
	tmp := exe + ".rollback"
	if err := os.Rename(exe, tmp); err != nil {
		return err
	}
	if err := os.Rename(backup, exe); err != nil {
		_ = os.Rename(tmp, exe)
		return err
	}
	_ = os.Remove(tmp)
	return nil
}

// Restart re-executes the current binary. On Unix it uses execve (the
// implementation lives in restart_unix.go), which preserves the PID and
// keeps systemd happy. On Windows it spawns a detached child and calls
// os.Exit(0) — see restart_windows.go.
//
// This function does NOT return on success — the process either replaces
// itself or exits.
func Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if rp, err := filepath.EvalSymlinks(exe); err == nil {
		exe = rp
	}
	args := append([]string{exe}, os.Args[1:]...)
	return platformRestart(exe, args)
}
