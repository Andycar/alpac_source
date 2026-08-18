package dlna

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadTrackersRoundTrip verifies save + load of tracker list.
func TestLoadTrackersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trackers.txt")

	trackers := []string{
		"udp://tracker.opentrackr.org:1337/announce",
		"http://bt4.t-ru.org/ann?magnet",
		"udp://explodie.org:6969",
	}

	if err := SaveTrackers(path, trackers); err != nil {
		t.Fatalf("SaveTrackers: %v", err)
	}

	loaded := LoadTrackers(path)
	if len(loaded) != len(trackers) {
		t.Fatalf("loaded %d trackers, want %d", len(loaded), len(trackers))
	}
	for i, tr := range loaded {
		if tr != trackers[i] {
			t.Errorf("tracker[%d] = %q, want %q", i, tr, trackers[i])
		}
	}
}

// TestLoadTrackersDedup verifies that duplicate trackers are removed.
func TestLoadTrackersDedup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trackers.txt")

	content := "udp://a:1337\nudp://b:6969\nudp://a:1337\n"
	_ = os.WriteFile(path, []byte(content), 0o644)

	loaded := LoadTrackers(path)
	if len(loaded) != 2 {
		t.Fatalf("expected 2 unique trackers, got %d: %v", len(loaded), loaded)
	}
}

// TestLoadTrackersEmpty verifies that loading a missing file returns nil.
func TestLoadTrackersEmpty(t *testing.T) {
	loaded := LoadTrackers("/nonexistent/path/trackers.txt")
	if loaded != nil {
		t.Fatalf("expected nil for missing file, got %v", loaded)
	}
}

// TestSaveTrackersCreatesDir verifies that SaveTrackers creates parent directories.
func TestSaveTrackersCreatesDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "trackers.txt")

	if err := SaveTrackers(path, []string{"udp://test:1337"}); err != nil {
		t.Fatalf("SaveTrackers: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "udp://test:1337") {
		t.Fatal("expected tracker in file")
	}
}

// TestDefaultTrackers verifies that defaults are non-empty and well-formed.
func TestDefaultTrackers(t *testing.T) {
	if len(DefaultTrackers) == 0 {
		t.Fatal("DefaultTrackers should not be empty")
	}
	for _, tr := range DefaultTrackers {
		if !strings.HasPrefix(tr, "http") && !strings.HasPrefix(tr, "udp") {
			t.Errorf("unexpected tracker protocol: %s", tr)
		}
	}
}

// TestTorrentConfigDefaults verifies the TorrentConfig struct.
func TestTorrentConfigDefaults(t *testing.T) {
	cfg := TorrentConfig{
		DownloadDir:   t.TempDir(),
		DownloadSpeed: 0,
		UploadSpeed:   0,
	}

	// Should create without error.
	tm, err := NewTorrentManager(cfg)
	if err != nil {
		t.Fatalf("NewTorrentManager: %v", err)
	}
	defer tm.Close()

	// No active tasks.
	if tm.HasActiveTasks() {
		t.Fatal("should have no active tasks initially")
	}

	// Stats should be empty.
	stats := tm.Stats()
	if len(stats) != 0 {
		t.Fatalf("expected 0 stats, got %d", len(stats))
	}
}

// TestTorrentManagerGetNonexistent verifies Get returns nil for unknown hash.
func TestTorrentManagerGetNonexistent(t *testing.T) {
	cfg := TorrentConfig{DownloadDir: t.TempDir()}
	tm, err := NewTorrentManager(cfg)
	if err != nil {
		t.Fatalf("NewTorrentManager: %v", err)
	}
	defer tm.Close()

	mt := tm.Get("0000000000000000000000000000000000000000")
	if mt != nil {
		t.Fatal("Get should return nil for unknown hash")
	}

	// Invalid hex should also return nil.
	mt = tm.Get("not-a-hex-hash")
	if mt != nil {
		t.Fatal("Get should return nil for invalid hex")
	}
}

// TestTorrentManagerRemoveNonexistent verifies Remove doesn't panic.
func TestTorrentManagerRemoveNonexistent(t *testing.T) {
	cfg := TorrentConfig{DownloadDir: t.TempDir()}
	tm, err := NewTorrentManager(cfg)
	if err != nil {
		t.Fatalf("NewTorrentManager: %v", err)
	}
	defer tm.Close()

	// Should not panic.
	tm.Remove("0000000000000000000000000000000000000000")
	tm.Remove("invalid")
}

// TestTorrentManagerListFilesNoMetadata verifies ListFiles returns nil without metadata.
func TestTorrentManagerListFilesNoMetadata(t *testing.T) {
	cfg := TorrentConfig{DownloadDir: t.TempDir()}
	tm, err := NewTorrentManager(cfg)
	if err != nil {
		t.Fatalf("NewTorrentManager: %v", err)
	}
	defer tm.Close()

	files := tm.ListFiles("0000000000000000000000000000000000000000")
	if files != nil {
		t.Fatal("ListFiles should return nil for unknown hash")
	}
}
