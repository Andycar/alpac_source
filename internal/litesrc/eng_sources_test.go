package litesrc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestEngHost(t *testing.T) {
	if got := engHost("", "https://embed.su"); got != "https://embed.su" {
		t.Errorf("fallback: %q", got)
	}
	if got := engHost("vidlink.pro", "https://x"); got != "https://vidlink.pro" {
		t.Errorf("scheme-add: %q", got)
	}
	if got := engHost("https://player.videasy.net/", "https://x"); got != "https://player.videasy.net" {
		t.Errorf("trim-slash: %q", got)
	}
}

func TestEngFilterHeaders(t *testing.T) {
	h := http.Header{
		"Referer":         {"https://vidlink.pro/"},
		"Origin":          {"https://vidlink.pro"},
		"Host":            {"cdn.example.com"},
		"Accept-Encoding": {"gzip"},
		"Range":           {"bytes=0-"},
	}
	out := engFilterHeaders(h)
	if out["Referer"] != "https://vidlink.pro/" || out["Origin"] != "https://vidlink.pro" {
		t.Errorf("kept headers wrong: %v", out)
	}
	for _, drop := range []string{"Host", "Accept-Encoding", "Range"} {
		if _, ok := out[drop]; ok {
			t.Errorf("should have dropped %s: %v", drop, out)
		}
	}
}

func TestEngMovieIndexEmitsCallEntry(t *testing.T) {
	e := NewEngSourceChecker("twoembed")
	if e == nil {
		t.Fatal("nil checker")
	}
	req := httptest.NewRequest(http.MethodGet, "http://h/lite/twoembed?id=550&title=Fight+Club&serial=0&rjson=true", nil)
	rec := httptest.NewRecorder()
	e.Handle(config.Config{}, nil)(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"movie"`) || !strings.Contains(body, `"call"`) {
		t.Errorf("movie call-entry wrong:\n%s", body)
	}
	if !strings.Contains(body, "/lite/twoembed/video?id=550") {
		t.Errorf("video link missing:\n%s", body)
	}
	if !strings.Contains(body, "play=true") {
		t.Errorf("stream play link missing:\n%s", body)
	}
}

func TestEngWriteSeasonsAndEpisodes(t *testing.T) {
	e := NewEngSourceChecker("vidlink")
	seasons := []engTmdbSeason{{Number: 1, EpisodeCount: 2}, {Number: 2, EpisodeCount: 3}}

	// Seasons.
	req := httptest.NewRequest(http.MethodGet, "http://h/lite/vidlink", nil)
	rec := httptest.NewRecorder()
	e.writeSeasons(rec, req, true, seasons, 1399, "tt0944947", "Игра престолов", "Game of Thrones")
	sbody := rec.Body.String()
	if !strings.Contains(sbody, `"season"`) || !strings.Contains(sbody, "1 сезон") || !strings.Contains(sbody, "2 сезон") {
		t.Errorf("seasons wrong:\n%s", sbody)
	}
	if !strings.Contains(sbody, "serial=1") || !strings.Contains(sbody, "s=2") {
		t.Errorf("season link params wrong:\n%s", sbody)
	}

	// Episodes of season 2 → 3 episodes, each a call into /video?s=2&e=N.
	rec = httptest.NewRecorder()
	e.writeEpisodes(rec, req, true, seasons, 1399, 2, "Game of Thrones")
	ebody := rec.Body.String()
	if !strings.Contains(ebody, `"episode"`) {
		t.Errorf("not episode type:\n%s", ebody)
	}
	// rjson path is json.Marshal'd, so "&" is emitted as & — assert on
	// the ampersand-free fragments.
	for _, want := range []string{"/lite/vidlink/video?id=1399", "s=2", "e=1", "e=3", `"call"`} {
		if !strings.Contains(ebody, want) {
			t.Errorf("episodes missing %q:\n%s", want, ebody)
		}
	}
	if strings.Contains(ebody, "e=4") {
		t.Errorf("season 2 has only 3 episodes, got e=4:\n%s", ebody)
	}
}

// TestEngVideoUsesCacheAndProxies exercises the non-browser half of video():
// a pre-populated cache entry is proxied and returned as method:play, so no
// headless Chrome is needed.
func TestEngVideoUsesCacheAndProxies(t *testing.T) {
	e := NewEngSourceChecker("twoembed")
	// video() computes the embed URL from (host, id); pre-seed the cache for it.
	embedURL := e.def.movieURL(e.def.host(config.Config{}), 550)
	e.cache.put(embedURL, engSniffResult{URL: "https://cdn.example.com/master.m3u8"})

	req := httptest.NewRequest(http.MethodGet, "http://h/lite/twoembed/video?id=550", nil)
	rec := httptest.NewRecorder()
	e.video(rec, req, config.Config{}, nil) // links=nil → raw URL passthrough
	body := rec.Body.String()
	if !strings.Contains(body, `"play"`) || !strings.Contains(body, "cdn.example.com/master.m3u8") {
		t.Errorf("cached resolve not returned:\n%s", body)
	}

	// play=true → redirect.
	req = httptest.NewRequest(http.MethodGet, "http://h/lite/twoembed/video?id=550&play=true", nil)
	rec = httptest.NewRecorder()
	e.video(rec, req, config.Config{}, nil)
	if rec.Code != http.StatusFound {
		t.Errorf("play=true should 302, got %d", rec.Code)
	}
}

func TestEngChecksearchDataJSON(t *testing.T) {
	e := NewEngSourceChecker("hydraflix")
	req := httptest.NewRequest(http.MethodGet, "http://h/lite/hydraflix?checksearch=true&title=X", nil)
	rec := httptest.NewRecorder()
	e.Handle(config.Config{}, nil)(rec, req)
	if !strings.Contains(rec.Body.String(), "data-json=") {
		t.Errorf("checksearch should return data-json=, got %q", rec.Body.String())
	}
}

func TestEngEmbedURLShapes(t *testing.T) {
	cfg := config.Config{}
	cases := []struct {
		plugin, movie, tv string
	}{
		{"vidlink", "https://vidlink.pro/movie/550", "https://vidlink.pro/tv/1399/2/5"},
		{"videasy", "https://player.videasy.net/movie/550", "https://player.videasy.net/tv/1399/2/5"},
		{"hydraflix", "https://vidfast.pro/movie/550?autoPlay=true&theme=e1216d", "https://vidfast.pro/tv/1399/2/5?autoPlay=true&theme=e1216d"},
		{"twoembed", "https://embed.su/embed/movie/550", "https://embed.su/embed/tv/1399/2/5"},
	}
	for _, c := range cases {
		d := engSourceDefs[c.plugin]
		if d == nil {
			t.Fatalf("no def for %s", c.plugin)
		}
		h := d.host(cfg)
		if got := d.movieURL(h, 550); got != c.movie {
			t.Errorf("%s movie: got %q want %q", c.plugin, got, c.movie)
		}
		if got := d.tvURL(h, 1399, 2, 5); got != c.tv {
			t.Errorf("%s tv: got %q want %q", c.plugin, got, c.tv)
		}
	}
}
