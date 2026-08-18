package litesrc

import (
	"os"
	"testing"
)

func TestFlixcdnParseMetaMovie(t *testing.T) {
	html, err := os.ReadFile("testdata/flixcdn_movie.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	f := &flixcdnChecker{}
	meta, ok := f.parseMeta(string(html))
	if !ok {
		t.Fatal("parseMeta returned false on a real movie payload")
	}
	if meta.ID == 0 {
		t.Errorf("ID = 0, want non-zero")
	}
	if meta.ContentType != "movie" {
		t.Errorf("ContentType = %q, want movie", meta.ContentType)
	}
	if len(meta.Translators) == 0 {
		t.Errorf("Translators empty, want non-empty")
	}
	if meta.CaptchaSitekey == "" {
		t.Errorf("CaptchaSitekey empty, want sitekey")
	}
	if len(meta.SeasonsEpisodes) != 0 {
		t.Errorf("SeasonsEpisodes = %v, want empty for movie", meta.SeasonsEpisodes)
	}
}

func TestFlixcdnParseMetaSerial(t *testing.T) {
	html, err := os.ReadFile("testdata/flixcdn_serial.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	f := &flixcdnChecker{}
	meta, ok := f.parseMeta(string(html))
	if !ok {
		t.Fatal("parseMeta returned false on a real serial payload")
	}
	if meta.ID == 0 {
		t.Errorf("ID = 0, want non-zero")
	}
	if meta.ContentType != "serial" {
		t.Errorf("ContentType = %q, want serial", meta.ContentType)
	}
	if len(meta.Translators) == 0 {
		t.Errorf("Translators empty, want non-empty")
	}
	if len(meta.SeasonsEpisodes) == 0 {
		t.Errorf("SeasonsEpisodes empty, want at least one season")
	}
	// GoT fixture: 8 seasons, season 1 has 10 episodes
	eps, ok := meta.SeasonsEpisodes["1"]
	if !ok || len(eps) == 0 {
		t.Errorf("season 1 episodes missing, got %v", meta.SeasonsEpisodes)
	}
}

func TestFlixcdnParseMetaRejectsNonPlayer(t *testing.T) {
	f := &flixcdnChecker{}
	if _, ok := f.parseMeta("<html><body>not a player</body></html>"); ok {
		t.Error("parseMeta accepted non-player HTML")
	}
}
