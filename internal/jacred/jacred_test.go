package jacred

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestHealthyFromProbe(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{200, true},  // working jacred API
		{204, true},  // any 2xx
		{401, true},  // auth-gated but present → still a live jacred
		{403, true},  // ditto
		{404, false}, // endpoint absent → not a functioning jacred (wrong version / foreign process)
		{500, false}, // server error
		{502, false}, // bad gateway
		{0, false},   // no response
	}
	for _, c := range cases {
		if got := healthyFromProbe(c.code); got != c.want {
			t.Errorf("healthyFromProbe(%d) = %v, want %v", c.code, got, c.want)
		}
	}
}

func TestSnapshotReflectsPortConflict(t *testing.T) {
	m := New(Config{Enable: true, HomeDir: t.TempDir(), Port: 9500})
	if m.Snapshot().PortConflict || m.PortConflict() {
		t.Fatal("fresh manager should not report a port conflict")
	}
	m.portConflict.Store(true)
	if !m.Snapshot().PortConflict {
		t.Fatal("Snapshot should reflect portConflict=true")
	}
	if !m.PortConflict() {
		t.Fatal("PortConflict() accessor should return true")
	}
}

func TestReleaseAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
		wantErr            bool
	}{
		{"linux", "amd64", "jacred-linux-amd64.zip", false},
		{"linux", "arm64", "jacred-linux-arm64.zip", false},
		{"linux", "arm", "jacred-linux-arm.zip", false},
		{"darwin", "arm64", "jacred-osx-arm64.zip", false},
		{"darwin", "amd64", "jacred-osx-amd64.zip", false},
		{"windows", "amd64", "jacred-win-x64.zip", false},
		{"windows", "arm64", "jacred-win-arm64.zip", false},
		{"linux", "mips", "", true},
		{"freebsd", "amd64", "", true},
	}
	for _, c := range cases {
		got, err := releaseAssetName(c.goos, c.goarch)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s/%s: expected error, got %q", c.goos, c.goarch, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s/%s: %v", c.goos, c.goarch, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s/%s: got %q want %q", c.goos, c.goarch, got, c.want)
		}
	}
}

func TestEnsureInitConfCreatesAndEnforces(t *testing.T) {
	home := t.TempDir()
	cfg := Config{Enable: true, HomeDir: home, Port: 9200, APIKey: "kk", SyncAPI: "https://up.example"}
	cfg.applyDefaults()

	if err := ensureInitConf(cfg); err != nil {
		t.Fatal(err)
	}
	got := readConf(t, filepath.Join(home, "init.conf"))
	if got["listenip"] != "127.0.0.1" || got["listenport"] != float64(9200) ||
		got["apikey"] != "kk" || got["syncapi"] != "https://up.example" {
		t.Fatalf("unexpected conf: %v", got)
	}
}

func TestEnsureInitConfPreservesUserKeysAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	user := map[string]any{
		"listenip":   "any",
		"listenport": 9117,
		"web":        true,
		"tsuri":      []any{"http://127.0.0.1:8090"},
	}
	b, _ := json.Marshal(user)
	if err := os.WriteFile(filepath.Join(home, "init.conf"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Enable: true, HomeDir: home, Port: 9300}
	cfg.applyDefaults()
	if err := ensureInitConf(cfg); err != nil {
		t.Fatal(err)
	}
	got := readConf(t, filepath.Join(home, "init.conf"))
	if got["listenip"] != "127.0.0.1" || got["listenport"] != float64(9300) {
		t.Fatalf("lampac-owned keys not enforced: %v", got)
	}
	if got["web"] != true {
		t.Fatalf("user key clobbered: %v", got)
	}

	// Second run with same config must not rewrite the file.
	st1, _ := os.Stat(filepath.Join(home, "init.conf"))
	if err := ensureInitConf(cfg); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(filepath.Join(home, "init.conf"))
	if st1.ModTime() != st2.ModTime() || st1.Size() != st2.Size() {
		t.Fatal("idempotent run rewrote init.conf")
	}
}

func TestEnsureInitConfUsesDataTemplate(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Data"), 0o755); err != nil {
		t.Fatal(err)
	}
	tpl := map[string]any{"mergeduplicates": true, "listenport": 9117}
	b, _ := json.Marshal(tpl)
	if err := os.WriteFile(filepath.Join(home, "Data", "init.conf"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Enable: true, HomeDir: home, Port: 9400}
	cfg.applyDefaults()
	if err := ensureInitConf(cfg); err != nil {
		t.Fatal(err)
	}
	got := readConf(t, filepath.Join(home, "init.conf"))
	if got["mergeduplicates"] != true || got["listenport"] != float64(9400) {
		t.Fatalf("template not merged: %v", got)
	}
}

func readConf(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// makeTarZst builds an in-memory tar.zst with the given files.
func makeTarZst(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	var zBuf bytes.Buffer
	zw, err := zstd.NewWriter(&zBuf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(tarBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return zBuf.Bytes()
}

func TestUnpackFDBArchiveRawTarZst(t *testing.T) {
	payload := makeTarZst(t, map[string]string{
		"torrents/aa/bb.json": `{"x":1}`,
		"init.conf":           `{}`,
	})
	src := filepath.Join(t.TempDir(), "db.archive")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	if err := unpackFDBArchive(src, dataDir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "torrents", "aa", "bb.json"))
	if err != nil || string(b) != `{"x":1}` {
		t.Fatalf("payload not extracted: %v %q", err, b)
	}
}

func TestUnpackFDBArchiveZipWrapped(t *testing.T) {
	payload := makeTarZst(t, map[string]string{"torrents/x.json": "1"})

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	f, err := zw.Create("latest.tar.zst")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(t.TempDir(), "db.archive")
	if err := os.WriteFile(src, zipBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	if err := unpackFDBArchive(src, dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "torrents", "x.json")); err != nil {
		t.Fatal("zip-wrapped payload not extracted")
	}
}

func TestUnpackFDBArchiveRejectsUnknownMagic(t *testing.T) {
	src := filepath.Join(t.TempDir(), "db.archive")
	if err := os.WriteFile(src, []byte("garbage-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unpackFDBArchive(src, t.TempDir()); err == nil {
		t.Fatal("expected error on unknown magic")
	}
}

func TestExtractTarZstSkipsPathTraversal(t *testing.T) {
	payload := makeTarZst(t, map[string]string{
		"../evil.txt": "boom",
		"ok.txt":      "fine",
	})
	dataDir := t.TempDir()
	if err := extractTarZst(bytes.NewReader(payload), dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dataDir), "evil.txt")); err == nil {
		t.Fatal("zip-slip: file escaped dataDir")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "ok.txt")); err != nil {
		t.Fatal("legit file not extracted")
	}
}

func TestExtractZipNoClobberDataAndConf(t *testing.T) {
	dst := t.TempDir()
	// Pre-existing live files.
	if err := os.MkdirAll(filepath.Join(dst, "Data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "init.conf"), []byte("user"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "Data", "keep.json"), []byte("user"), 0o644); err != nil {
		t.Fatal(err)
	}

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for name, content := range map[string]string{
		"JacRed":         "binary-v2",
		"init.conf":      "shipped",
		"Data/keep.json": "shipped",
		"Data/new.json":  "shipped",
	} {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "release.zip")
	if err := os.WriteFile(src, zipBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := extractZip(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "init.conf")); string(b) != "user" {
		t.Fatal("init.conf clobbered by update")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "Data", "keep.json")); string(b) != "user" {
		t.Fatal("Data file clobbered by update")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "Data", "new.json")); string(b) != "shipped" {
		t.Fatal("new Data file not extracted")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "JacRed")); string(b) != "binary-v2" {
		t.Fatal("binary not updated")
	}
}

func TestBinaryPathDirectAndNested(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("binary name differs on windows")
	}
	home := t.TempDir()
	if _, err := binaryPath(home); err == nil {
		t.Fatal("expected error on empty home")
	}
	// Nested (osx zip layout).
	nested := filepath.Join(home, "jacred-osx-arm64")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "JacRed"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p, err := binaryPath(home); err != nil || p != filepath.Join(nested, "JacRed") {
		t.Fatalf("nested lookup: %v %q", err, p)
	}
	// Direct wins.
	if err := os.WriteFile(filepath.Join(home, "JacRed"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p, err := binaryPath(home); err != nil || p != filepath.Join(home, "JacRed") {
		t.Fatalf("direct lookup: %v %q", err, p)
	}
}

func TestBootstrapDBSkipsWhenStamped(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, bootstrapStampFile), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	// URL is bogus — must not be contacted because the stamp short-circuits.
	if err := BootstrapDB(context.Background(), home, "http://127.0.0.1:1/nope"); err != nil {
		t.Fatal(err)
	}
}
