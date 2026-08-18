package dlnahttp

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"lampac-go/internal/config"
)

// ---------------------------------------------------------------------------
//  Helper: compile media pattern
// ---------------------------------------------------------------------------

func TestCompiledMediaPatternDefault(t *testing.T) {
	re := compiledMediaPattern("")
	for _, ext := range []string{".mp4", ".mkv", ".avi", ".mov", ".ts", ".webm"} {
		if !re.MatchString(ext) {
			t.Errorf("default pattern should match %s", ext)
		}
	}
	for _, ext := range []string{".txt", ".jpg", ".go", ".exe"} {
		if re.MatchString(ext) {
			t.Errorf("default pattern should NOT match %s", ext)
		}
	}
}

func TestCompiledMediaPatternCustom(t *testing.T) {
	re := compiledMediaPattern(`^\.(mp4|mkv)$`)
	if !re.MatchString(".mp4") {
		t.Fatal("should match .mp4")
	}
	if re.MatchString(".avi") {
		t.Fatal("should NOT match .avi with custom pattern")
	}
}

func TestCompiledMediaPatternInvalid(t *testing.T) {
	// Invalid regex falls back to simple default.
	re := compiledMediaPattern(`[invalid`)
	if !re.MatchString(".mp4") {
		t.Fatal("fallback pattern should match .mp4")
	}
}

// ---------------------------------------------------------------------------
//  dlnaMD5
// ---------------------------------------------------------------------------

func TestDlnaMD5(t *testing.T) {
	h := dlnaMD5("test.mp4")
	if len(h) != 32 {
		t.Fatalf("expected 32 hex chars, got %d", len(h))
	}
	// Same input = same output.
	if dlnaMD5("test.mp4") != h {
		t.Fatal("MD5 should be deterministic")
	}
	// Different input = different output.
	if dlnaMD5("other.mp4") == h {
		t.Fatal("different inputs should produce different hashes")
	}
}

// ---------------------------------------------------------------------------
//  extractFirstNumber
// ---------------------------------------------------------------------------

func TestExtractFirstNumber(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"Episode 5 - Title", 5},
		{"S01E03.mkv", 1},
		{"nonum.mp4", 4}, // finds "4" in "mp4" extension
		{"nonum", 0},
		{"123abc", 123},
		{"abc456def789", 456},
	}
	for _, tc := range tests {
		got := extractFirstNumber(tc.input)
		if got != tc.want {
			t.Errorf("extractFirstNumber(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
//  schemeHost
// ---------------------------------------------------------------------------

func TestSchemeHost(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.com/foo", nil)
	got := schemeHost(r)
	if got != "http://example.com" {
		t.Fatalf("schemeHost = %q, want http://example.com", got)
	}

	// With X-Forwarded-Proto.
	r.Header.Set("X-Forwarded-Proto", "https")
	got = schemeHost(r)
	if got != "https://example.com" {
		t.Fatalf("schemeHost = %q with forwarded proto", got)
	}
}

// ---------------------------------------------------------------------------
//  splitFFmpegArgs
// ---------------------------------------------------------------------------

func TestSplitFFmpegArgs(t *testing.T) {
	args := splitFFmpegArgs(`-n -ss 3:00 -i "/path/to my file.mp4" -vf "scale=400:-2" out.jpg`)
	expected := []string{"-n", "-ss", "3:00", "-i", "/path/to my file.mp4", "-vf", "scale=400:-2", "out.jpg"}
	if len(args) != len(expected) {
		t.Fatalf("got %d args, want %d: %v", len(args), len(expected), args)
	}
	for i, a := range args {
		if a != expected[i] {
			t.Errorf("arg[%d] = %q, want %q", i, a, expected[i])
		}
	}
}

func TestSplitFFmpegArgsSingleQuotes(t *testing.T) {
	args := splitFFmpegArgs(`-i 'my file.mp4' out.jpg`)
	if len(args) != 3 {
		t.Fatalf("got %d args, want 3: %v", len(args), args)
	}
	if args[1] != "my file.mp4" {
		t.Errorf("arg[1] = %q, want 'my file.mp4'", args[1])
	}
}

// ---------------------------------------------------------------------------
//  dlnaFindSubtitles
// ---------------------------------------------------------------------------

func TestDlnaFindSubtitles(t *testing.T) {
	dir := t.TempDir()

	// Create media and subtitle files.
	_ = os.WriteFile(filepath.Join(dir, "movie.mp4"), []byte("video"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "movie.srt"), []byte("subs"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "movie.en.srt"), []byte("en subs"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "movie.ua.vtt"), []byte("ua subs"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "other.srt"), []byte("other"), 0o644)

	subs := dlnaFindSubtitles("http://localhost", dir, "movie", "")
	if len(subs) != 3 {
		t.Fatalf("expected 3 subtitles, got %d: %+v", len(subs), subs)
	}

	// "movie.srt" has no language suffix → "Sub #1"
	// "movie.en.srt" → label "en"
	// "movie.ua.vtt" → label "ua"
	labels := map[string]bool{}
	for _, s := range subs {
		labels[s.Label] = true
	}
	if !labels["en"] {
		t.Error("expected subtitle with label 'en'")
	}
	if !labels["ua"] {
		t.Error("expected subtitle with label 'ua'")
	}
}

func TestDlnaFindSubtitlesNone(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "movie.mp4"), []byte("video"), 0o644)

	subs := dlnaFindSubtitles("http://localhost", dir, "movie", "")
	if len(subs) != 0 {
		t.Fatalf("expected 0 subtitles, got %d", len(subs))
	}
}

// ---------------------------------------------------------------------------
//  dlnaDirHasMedia
// ---------------------------------------------------------------------------

func TestDlnaDirHasMedia(t *testing.T) {
	re := compiledMediaPattern("")

	// Directory with media.
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "movie.mp4"), []byte("video"), 0o644)
	if !dlnaDirHasMedia(dir, re) {
		t.Fatal("directory with .mp4 should have media")
	}

	// Directory with only non-media.
	dir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir2, "readme.txt"), []byte("text"), 0o644)
	if dlnaDirHasMedia(dir2, re) {
		t.Fatal("directory with only .txt should NOT have media")
	}

	// Directory with subfolder counts as having media.
	dir3 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir3, "sub"), 0o755)
	if !dlnaDirHasMedia(dir3, re) {
		t.Fatal("directory with subdirectory should count as having media")
	}
}

// ---------------------------------------------------------------------------
//  dlnaIndexHandler — integration test with temp dir
// ---------------------------------------------------------------------------

func TestDlnaIndexHandlerBasic(t *testing.T) {
	dlnaDir := t.TempDir()

	// Create test files.
	_ = os.WriteFile(filepath.Join(dlnaDir, "movie.mp4"), []byte("video"), 0o644)
	_ = os.WriteFile(filepath.Join(dlnaDir, "readme.txt"), []byte("text"), 0o644)
	_ = os.MkdirAll(filepath.Join(dlnaDir, "Series"), 0o755)
	_ = os.WriteFile(filepath.Join(dlnaDir, "Series", "S01E01.mkv"), []byte("ep"), 0o644)

	cfg := config.Config{}
	cfg.DLNA.Enable = true
	cfg.DLNA.Path = dlnaDir
	cfg.Compat.RepoRoot = "" // Path is absolute.

	// Override resolveDLNARoot behavior: since Path is absolute, RepoRoot is unused.
	handler := dlnaIndexHandler(cfg)

	req := httptest.NewRequest("GET", "/dlna?path=", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var items []dlnaItem
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	// Should have 1 folder (Series) + 1 file (movie.mp4). readme.txt filtered out.
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d: %+v", len(items), items)
	}

	// First item should be the folder.
	if items[0].Type != "folder" || items[0].Name != "Series" {
		t.Errorf("items[0] = %+v, want folder 'Series'", items[0])
	}
	// Second should be the file.
	if items[1].Type != "file" || items[1].Name != "movie.mp4" {
		t.Errorf("items[1] = %+v, want file 'movie.mp4'", items[1])
	}
}

func TestDlnaIndexHandlerPathTraversal(t *testing.T) {
	dlnaDir := t.TempDir()

	cfg := config.Config{}
	cfg.DLNA.Enable = true
	cfg.DLNA.Path = dlnaDir

	handler := dlnaIndexHandler(cfg)

	req := httptest.NewRequest("GET", "/dlna?path=../../etc", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("path traversal should return 400, got %d", rec.Code)
	}
}

func TestDlnaIndexHandlerSeasonEpisode(t *testing.T) {
	dlnaDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dlnaDir, "Show.S02E05.720p.mkv"), []byte("ep"), 0o644)

	cfg := config.Config{}
	cfg.DLNA.Enable = true
	cfg.DLNA.Path = dlnaDir

	handler := dlnaIndexHandler(cfg)

	req := httptest.NewRequest("GET", "/dlna?path=", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler(rec, req)

	var items []dlnaItem
	_ = stdjson.Unmarshal(rec.Body.Bytes(), &items)

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].S != 2 {
		t.Errorf("season = %d, want 2", items[0].S)
	}
	if items[0].E != 5 {
		t.Errorf("episode = %d, want 5", items[0].E)
	}
}

// ---------------------------------------------------------------------------
//  dlnaStreamHandler
// ---------------------------------------------------------------------------

func TestDlnaStreamHandler(t *testing.T) {
	dlnaDir := t.TempDir()
	content := "video content here"
	_ = os.WriteFile(filepath.Join(dlnaDir, "movie.mp4"), []byte(content), 0o644)

	cfg := config.Config{}
	cfg.DLNA.Path = dlnaDir

	handler := dlnaStreamHandler(cfg)

	req := httptest.NewRequest("GET", "/dlna/stream?path=movie.mp4", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.String() != content {
		t.Fatalf("body = %q, want %q", rec.Body.String(), content)
	}
}

func TestDlnaStreamHandlerNotFound(t *testing.T) {
	dlnaDir := t.TempDir()

	cfg := config.Config{}
	cfg.DLNA.Path = dlnaDir

	handler := dlnaStreamHandler(cfg)

	req := httptest.NewRequest("GET", "/dlna/stream?path=nonexistent.mp4", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDlnaStreamHandlerPathTraversal(t *testing.T) {
	dlnaDir := t.TempDir()

	cfg := config.Config{}
	cfg.DLNA.Path = dlnaDir

	handler := dlnaStreamHandler(cfg)

	req := httptest.NewRequest("GET", "/dlna/stream?path=../../etc/passwd", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("path traversal should return 400, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
//  dlnaDeleteHandler
// ---------------------------------------------------------------------------

func TestDlnaDeleteHandler(t *testing.T) {
	dlnaDir := t.TempDir()
	filePath := filepath.Join(dlnaDir, "delete_me.mp4")
	_ = os.WriteFile(filePath, []byte("video"), 0o644)

	cfg := config.Config{}
	cfg.DLNA.Path = dlnaDir

	handler := dlnaDeleteHandler(cfg)

	req := httptest.NewRequest("GET", "/dlna/delete?path=delete_me.mp4", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	// File should be deleted.
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatal("file should be deleted")
	}
}

func TestDlnaDeleteHandlerPathTraversal(t *testing.T) {
	dlnaDir := t.TempDir()

	cfg := config.Config{}
	cfg.DLNA.Path = dlnaDir

	handler := dlnaDeleteHandler(cfg)

	req := httptest.NewRequest("GET", "/dlna/delete?path=../../etc/important", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	// Should be rejected.
	if rec.Code == http.StatusOK {
		var resp map[string]any
		_ = stdjson.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["error"] == nil {
			t.Fatal("path traversal should be rejected")
		}
	}
}

// ---------------------------------------------------------------------------
//  dlnaCoverURL
// ---------------------------------------------------------------------------

func TestDlnaCoverURL(t *testing.T) {
	dlnaRoot := t.TempDir()
	thumbsDir := filepath.Join(dlnaRoot, "thumbs")
	_ = os.MkdirAll(thumbsDir, 0o755)

	// No thumbnail → empty string.
	url := dlnaCoverURL("http://localhost", dlnaRoot, "movie.mp4")
	if url != "" {
		t.Fatalf("expected empty URL when no thumbnail, got %q", url)
	}

	// Create thumbnail.
	hash := dlnaMD5("movie.mp4")
	_ = os.WriteFile(filepath.Join(thumbsDir, hash+".jpg"), []byte("thumb"), 0o644)

	url = dlnaCoverURL("http://localhost", dlnaRoot, "movie.mp4")
	if url == "" {
		t.Fatal("expected non-empty URL when thumbnail exists")
	}
	if url != "http://localhost/dlna/stream?path="+filepath.Join("thumbs", hash+".jpg") {
		t.Fatalf("unexpected URL: %s", url)
	}
}

// ---------------------------------------------------------------------------
//  Torrent API handlers — nil manager safety
// ---------------------------------------------------------------------------

func TestDlnaTorrentManagersNilSafe(t *testing.T) {
	cfg := config.Config{}
	handler := dlnaTorrentManagersHandler(cfg, nil)

	req := httptest.NewRequest("GET", "/dlna/tracker/managers", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestDlnaTorrentShowNilSafe(t *testing.T) {
	cfg := config.Config{}
	handler := dlnaTorrentShowHandler(cfg, nil)

	req := httptest.NewRequest("GET", "/dlna/tracker/show?path=magnet:test", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestDlnaTorrentDeleteNilSafe(t *testing.T) {
	cfg := config.Config{}
	handler := dlnaTorrentDeleteHandler(cfg, nil)

	req := httptest.NewRequest("GET", "/dlna/tracker/delete?infohash=abc123", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}
