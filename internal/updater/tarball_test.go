package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTarGz produces a gzip-compressed tar with the given entries.
// Each entry's name is taken verbatim (e.g. "plugins/", "plugins/foo.js")
// so tests can craft path-escape vectors.
func buildTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		isDir := strings.HasSuffix(name, "/")
		h := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
		if isDir {
			h.Typeflag = tar.TypeDir
			h.Mode = 0o755
			h.Size = 0
		} else {
			h.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("write header %q: %v", name, err)
		}
		if !isDir {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatalf("write body %q: %v", name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractTarGz_RegularFiles(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"plugins/":            "",
		"plugins/foo.js":      "console.log('foo');\n",
		"plugins/sub/bar.txt": "hello\n",
	})

	dir := t.TempDir()
	src := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(src, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(src, dst); err != nil {
		t.Fatalf("extract: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dst, "plugins", "foo.js"))
	if err != nil {
		t.Fatalf("read foo.js: %v", err)
	}
	if string(data) != "console.log('foo');\n" {
		t.Errorf("foo.js content: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dst, "plugins", "sub", "bar.txt")); err != nil {
		t.Errorf("sub/bar.txt missing: %v", err)
	}
}

func TestExtractTarGz_RejectsPathEscape(t *testing.T) {
	cases := []string{
		"../escape.txt",
		"plugins/../../etc/passwd",
		"/abs/path.txt",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			archive := buildTarGz(t, map[string]string{name: "x"})
			dir := t.TempDir()
			src := filepath.Join(dir, "a.tar.gz")
			if err := os.WriteFile(src, archive, 0o644); err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(dir, "out")
			if err := os.MkdirAll(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			err := extractTarGz(src, dst)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", name)
			}
		})
	}
}

func TestApplyTarballAsset_SwapAndRollback(t *testing.T) {
	// Archive ships a single top-level "plugins/" dir (as release.sh does)
	// with one new file and an updated copy of a stock file.
	archive := buildTarGz(t, map[string]string{
		"plugins/":         "",
		"plugins/new.js":   "NEW\n",
		"plugins/stock.js": "STOCK_V2\n",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// old.js: custom (absent from archive) — must survive the update.
	if err := os.WriteFile(filepath.Join(target, "old.js"), []byte("OLD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// stock.js: shipped by the archive — must be overwritten with V2.
	if err := os.WriteFile(filepath.Join(target, "stock.js"), []byte("STOCK_V1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	backup, err := ApplyTarballAsset(srv.URL, "", int64(len(archive)), target, "")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if backup != target+".old" {
		t.Errorf("backup path = %q, want %q", backup, target+".old")
	}

	if data, err := os.ReadFile(filepath.Join(target, "new.js")); err != nil || string(data) != "NEW\n" {
		t.Errorf("new.js missing/wrong: %v %q", err, data)
	}
	// Stock file taken from the archive.
	if data, err := os.ReadFile(filepath.Join(target, "stock.js")); err != nil || string(data) != "STOCK_V2\n" {
		t.Errorf("stock.js should be updated to V2: %v %q", err, data)
	}
	// Custom file preserved through the merge.
	if data, err := os.ReadFile(filepath.Join(target, "old.js")); err != nil || string(data) != "OLD\n" {
		t.Errorf("custom old.js should survive update: %v %q", err, data)
	}
	// Backup retains the pre-update tree (both old.js and stock V1).
	if data, err := os.ReadFile(filepath.Join(target+".old", "old.js")); err != nil || string(data) != "OLD\n" {
		t.Errorf("old.js missing from backup: %v %q", err, data)
	}
	if data, err := os.ReadFile(filepath.Join(target+".old", "stock.js")); err != nil || string(data) != "STOCK_V1\n" {
		t.Errorf("stock V1 missing from backup: %v %q", err, data)
	}

	// Rollback restores the pre-update tree (stock back to V1, new.js gone).
	if err := RollbackTarballAsset(target); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "new.js")); !os.IsNotExist(err) {
		t.Errorf("new.js should be gone after rollback, stat err = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "stock.js")); err != nil || string(data) != "STOCK_V1\n" {
		t.Errorf("stock.js should be V1 after rollback: %v %q", err, data)
	}
	if data, err := os.ReadFile(filepath.Join(target, "old.js")); err != nil || string(data) != "OLD\n" {
		t.Errorf("old.js missing after rollback: %v %q", err, data)
	}

	// Second rollback — no backup left, expect ErrNoRollback.
	if err := RollbackTarballAsset(target); !errors.Is(err, ErrNoRollback) {
		t.Errorf("second rollback = %v, want ErrNoRollback", err)
	}
}

func TestApplyTarballAsset_PreservesCustomDir(t *testing.T) {
	// Archive ships stock plugins but NOT plugins/custom (release.sh excludes
	// it). The operator's custom/ tree — including nested files — must be
	// carried over verbatim.
	archive := buildTarGz(t, map[string]string{
		"plugins/":         "",
		"plugins/online.js": "stock online\n",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(filepath.Join(target, "custom"), 0o755); err != nil {
		t.Fatal(err)
	}
	// custom/_registry.json + a user plugin + a *.my.js override at root.
	if err := os.WriteFile(filepath.Join(target, "custom", "_registry.json"), []byte(`{"foo":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "custom", "myplugin.js"), []byte("USER\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "online.my.js"), []byte("OVERRIDE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyTarballAsset(srv.URL, "", int64(len(archive)), target, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Stock file updated.
	if data, err := os.ReadFile(filepath.Join(target, "online.js")); err != nil || string(data) != "stock online\n" {
		t.Errorf("online.js wrong: %v %q", err, data)
	}
	// Custom dir + nested files preserved.
	if data, err := os.ReadFile(filepath.Join(target, "custom", "_registry.json")); err != nil || string(data) != `{"foo":1}` {
		t.Errorf("custom/_registry.json lost: %v %q", err, data)
	}
	if data, err := os.ReadFile(filepath.Join(target, "custom", "myplugin.js")); err != nil || string(data) != "USER\n" {
		t.Errorf("custom/myplugin.js lost: %v %q", err, data)
	}
	// *.my.js override preserved.
	if data, err := os.ReadFile(filepath.Join(target, "online.my.js")); err != nil || string(data) != "OVERRIDE\n" {
		t.Errorf("online.my.js override lost: %v %q", err, data)
	}
}

func TestApplyTarballAsset_SHA256Mismatch(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"plugins/":    "",
		"plugins/x.js": "x\n",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	preMarker := filepath.Join(target, "marker.js")
	if err := os.WriteFile(preMarker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ApplyTarballAsset(srv.URL, "deadbeef", int64(len(archive)), target, "")
	if err == nil {
		t.Fatal("expected sha256 mismatch error, got nil")
	}
	// Pre-existing content must remain untouched on failure.
	if _, err := os.Stat(preMarker); err != nil {
		t.Errorf("pre-existing marker.js disappeared on mismatch: %v", err)
	}
	if _, err := os.Stat(target + ".old"); err == nil {
		t.Errorf("backup created despite failure: %s", target+".old")
	}
}

func TestResolvePluginsDir_RepoRoot(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Clear env to avoid host leakage.
	t.Setenv("LAMPAC_GO_PLUGINS_DIR", "")

	got := ResolvePluginsDir(dir)
	if got != pluginsDir {
		t.Errorf("ResolvePluginsDir(%q) = %q, want %q", dir, got, pluginsDir)
	}
}

func TestResolvePluginsDir_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	override := filepath.Join(dir, "custom-plugins")
	t.Setenv("LAMPAC_GO_PLUGINS_DIR", override)
	if got := ResolvePluginsDir(dir); got != override {
		t.Errorf("env override ignored: got %q want %q", got, override)
	}
}
