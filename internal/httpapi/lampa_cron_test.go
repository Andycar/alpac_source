package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
)

// TestLampaCronBackupRestore verifies that user-sensitive files survive update.
func TestLampaCronBackupRestore(t *testing.T) {
	lampaDir := t.TempDir()

	// Create user files.
	_ = os.WriteFile(filepath.Join(lampaDir, "personal.lampa"), []byte("personal"), 0o644)
	_ = os.WriteFile(filepath.Join(lampaDir, "plugins_black_list.json"), []byte(`["bad.js"]`), 0o644)

	_ = os.MkdirAll(filepath.Join(lampaDir, "plugins"), 0o755)
	_ = os.WriteFile(filepath.Join(lampaDir, "plugins", "modification.js"), []byte("// custom"), 0o644)
	_ = os.WriteFile(filepath.Join(lampaDir, "plugins", "my_plugin.js"), []byte("// my plugin"), 0o644)

	lc := &LampaCron{}

	// Backup.
	backups := lc.backupUserFiles(lampaDir)
	if len(backups) == 0 {
		t.Fatal("expected backups")
	}

	// Simulate ZIP overwrite by deleting everything.
	_ = os.RemoveAll(filepath.Join(lampaDir, "plugins"))
	_ = os.Remove(filepath.Join(lampaDir, "personal.lampa"))
	_ = os.Remove(filepath.Join(lampaDir, "plugins_black_list.json"))

	// Restore.
	lc.restoreUserFiles(lampaDir, backups)

	// Verify all files restored.
	assertFileContent(t, filepath.Join(lampaDir, "personal.lampa"), "personal")
	assertFileContent(t, filepath.Join(lampaDir, "plugins_black_list.json"), `["bad.js"]`)
	assertFileContent(t, filepath.Join(lampaDir, "plugins", "modification.js"), "// custom")
	assertFileContent(t, filepath.Join(lampaDir, "plugins", "my_plugin.js"), "// my plugin")
}

// TestLampaCronPostProcess verifies lampainit injection and marker file creation.
func TestLampaCronPostProcess(t *testing.T) {
	lampaDir := t.TempDir()

	indexHTML := `<html><head></head><body><script src="app.min.js"></script></body></html>`
	_ = os.WriteFile(filepath.Join(lampaDir, "index.html"), []byte(indexHTML), 0o644)

	lampaPostProcess(lampaDir)

	// lampainit.js injected.
	data, err := os.ReadFile(filepath.Join(lampaDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"/lampainit.js"`) {
		t.Fatal("lampainit.js not injected into index.html")
	}

	// personal.lampa created.
	if _, err := os.Stat(filepath.Join(lampaDir, "personal.lampa")); os.IsNotExist(err) {
		t.Fatal("personal.lampa not created")
	}

	// plugins_black_list.json created with [].
	assertFileContent(t, filepath.Join(lampaDir, "plugins_black_list.json"), "[]")

	// plugins/modification.js created.
	if _, err := os.Stat(filepath.Join(lampaDir, "plugins", "modification.js")); os.IsNotExist(err) {
		t.Fatal("plugins/modification.js not created")
	}
}

// TestLampaCronPostProcessIdempotent verifies that double injection doesn't duplicate.
func TestLampaCronPostProcessIdempotent(t *testing.T) {
	lampaDir := t.TempDir()

	indexHTML := `<html><head></head><body><script src="/lampainit.js"></script></body></html>`
	_ = os.WriteFile(filepath.Join(lampaDir, "index.html"), []byte(indexHTML), 0o644)

	lampaPostProcess(lampaDir)

	data, _ := os.ReadFile(filepath.Join(lampaDir, "index.html"))
	if strings.Count(string(data), "lampainit.js") != 1 {
		t.Fatal("lampainit.js should not be duplicated")
	}
}

// TestCacheCronDeletesExpired verifies TTL-based file deletion.
func TestCacheCronDeletesExpired(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache")

	// Create html cache dir with old and new files.
	htmlDir := filepath.Join(cacheDir, "html")
	_ = os.MkdirAll(htmlDir, 0o755)

	// Old file: 10 minutes ago.
	oldFile := filepath.Join(htmlDir, "old.html")
	_ = os.WriteFile(oldFile, []byte("old"), 0o644)
	oldTime := time.Now().Add(-10 * time.Minute)
	_ = os.Chtimes(oldFile, oldTime, oldTime)

	// New file: just now.
	newFile := filepath.Join(htmlDir, "new.html")
	_ = os.WriteFile(newFile, []byte("new"), 0o644)

	cfg := config.Config{}
	cfg.Compat.RepoRoot = tmpDir
	cfg.FileCacheInactive = config.FileCacheConfig{
		HTML:    5, // 5 min TTL — old file should be deleted
		Torrent: 2880,
		HLS:     90,
	}
	cfg.ServerProxy.Image.CacheTime = 60

	// Override relToRuntime for test: point "cache" to our tmp dir.
	origRelToRuntime := relToRuntimeFunc
	relToRuntimeFunc = func(rel string) string {
		if rel == "cache" {
			return cacheDir
		}
		return rel
	}
	defer func() { relToRuntimeFunc = origRelToRuntime }()

	cc := NewCacheCron(cfg)
	cc.run()

	// Old file should be deleted.
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatal("old file should have been deleted")
	}

	// New file should still exist.
	if _, err := os.Stat(newFile); err != nil {
		t.Fatal("new file should still exist")
	}
}

// TestCacheCronSkipsMinus1 verifies that TTL=-1 skips the directory.
func TestCacheCronSkipsMinus1(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache")

	torrentDir := filepath.Join(cacheDir, "torrent")
	_ = os.MkdirAll(torrentDir, 0o755)

	f := filepath.Join(torrentDir, "old.torrent")
	_ = os.WriteFile(f, []byte("data"), 0o644)
	old := time.Now().Add(-24 * time.Hour)
	_ = os.Chtimes(f, old, old)

	cfg := config.Config{}
	cfg.FileCacheInactive = config.FileCacheConfig{
		HTML:    5,
		Torrent: -1, // disabled
		HLS:     90,
	}
	cfg.ServerProxy.Image.CacheTime = 60

	origRelToRuntime := relToRuntimeFunc
	relToRuntimeFunc = func(rel string) string {
		if rel == "cache" {
			return cacheDir
		}
		return rel
	}
	defer func() { relToRuntimeFunc = origRelToRuntime }()

	cc := NewCacheCron(cfg)
	cc.run()

	// File should still exist because TTL is -1.
	if _, err := os.Stat(f); err != nil {
		t.Fatal("torrent file should NOT be deleted when TTL is -1")
	}
}

// TestLoadLampaWebCronSettingsDefaults verifies default values.
func TestLoadLampaWebCronSettingsDefaults(t *testing.T) {
	settings := loadLampaWebCronSettings()
	if !settings.AutoUpdate {
		t.Fatal("autoupdate should default to true")
	}
	if settings.Git != "yumata/lampa" {
		t.Fatalf("git should default to yumata/lampa, got %s", settings.Git)
	}
	if settings.IntervalUpdate != 90 {
		t.Fatalf("intervalupdate should default to 90, got %d", settings.IntervalUpdate)
	}
}

// TestMD5Hex verifies MD5 hashing.
func TestMD5Hex(t *testing.T) {
	got := md5Hex([]byte("hello"))
	want := "5d41402abc4b2a76b9719d911017c592"
	if got != want {
		t.Fatalf("md5Hex(hello) = %s, want %s", got, want)
	}
}

// TestMD5FileHex verifies file MD5 hashing.
func TestMD5FileHex(t *testing.T) {
	f := filepath.Join(t.TempDir(), "test.txt")
	_ = os.WriteFile(f, []byte("hello"), 0o644)

	got := md5FileHex(f)
	want := "5d41402abc4b2a76b9719d911017c592"
	if got != want {
		t.Fatalf("md5FileHex = %s, want %s", got, want)
	}
}

// TestRemoveEmptyDirs verifies empty directory cleanup.
func TestRemoveEmptyDirs(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b", "c")
	_ = os.MkdirAll(sub, 0o755)

	// "c" is empty, "b" contains only "c", "a" contains only "b" → all should be removed.
	removeEmptyDirs(root)

	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries in root, got %d", len(entries))
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s content = %q, want %q", filepath.Base(path), string(data), want)
	}
}
