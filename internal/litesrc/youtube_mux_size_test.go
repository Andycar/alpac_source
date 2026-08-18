package litesrc

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMuxDirSize pins the size-guard helper that stops runaway livestream muxes
// (prod: a single /tmp/yt-mux-* dir reached 28GB). It sums regular files only,
// ignores subdirs, and returns 0 for a missing dir.
func TestMuxDirSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seg00001.ts"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seg00002.ts"), make([]byte, 2000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "playlist.m3u8"), []byte("#EXTM3U"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got, want := muxDirSize(dir), int64(1000+2000+7); got != want {
		t.Fatalf("muxDirSize = %d, want %d", got, want)
	}
	if got := muxDirSize(filepath.Join(dir, "does-not-exist")); got != 0 {
		t.Fatalf("muxDirSize(missing) = %d, want 0", got)
	}
}
