package transcodesvc

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"lampac-go/internal/config"
)

// TestStartupSweepSparesFreshJobDirs: the NewTranscodingService constructor sweep must only
// reap STALE job dirs. A fresh dir may belong to a live job of ANOTHER lampac-go instance
// sharing the runtime dir (prod 2026-07-16: a second instance's boot wiped the main instance's
// live IPTV «Эконом» job mid-write → ffmpeg «failed to rename index.m3u8.tmp» / exit 254).
func TestStartupSweepSparesFreshJobDirs(t *testing.T) {
	root := t.TempDir()

	fresh := filepath.Join(root, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") // 32 hex — a job dir name
	stale := filepath.Join(root, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	foreign := filepath.Join(root, "yt-mux-something") // not a job dir name — never touched
	for _, d := range []string{fresh, stale, foreign} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Make `stale` look like an orphan of a long-dead run; `fresh` was written to seconds ago.
	old := time.Now().Add(-2 * constructorWipeMinAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{}
	cfg.Transcoding.Enable = true
	cfg.Transcoding.TempRoot = root
	svc := NewTranscodingService(cfg)
	defer svc.Stop()

	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh job dir was wiped by the startup sweep — another instance's live job would die: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale orphan dir survived the sweep (err=%v)", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("foreign (non-job) dir must never be touched: %v", err)
	}
}
