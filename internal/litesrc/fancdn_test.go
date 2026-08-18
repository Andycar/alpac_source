package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lampac-go/internal/config"
)

// fancdnFixtureHTML returns a minimal /gt/{kp} page with one season + two
// episodes and three voices, matching the real markup shape produced by
// lomont.site (verified via curl 2026-05-25). The actual production page
// is ~550 KB, but the parser only cares about the inputData div and the
// data-film / data-content-type attrs — so this short fixture exercises
// the same code paths as the real page.
func fancdnFixtureHTML(kp int, content string) string {
	return fmt.Sprintf(`<!doctype html>
<html><body>
<div id="inputData" style="display: none;" data-playlist="%d" data-season="1" data-episode="1" data-voice="12#Дубляж" data-content-type="0" data-film='{"kp_id":%d,"imdb_id":388629}'>%s</div>
</body></html>`, kp, kp, content)
}

// fancdnMovieFixture builds an inputData JSON tree for a single-episode
// "movie" (one season, episode 1, two voice options).
func fancdnMovieFixture() string {
	return `{"1":{"1":[{"video_id":1009280,"season":1,"episode":1,"voice_name":"Дубляж","voice_id":12,"duration":1402,"video_sewnjunk":0,"skip":[[],[],[]]},{"video_id":1009281,"season":1,"episode":1,"voice_name":"AniMedia","voice_id":261,"duration":1402,"video_sewnjunk":0,"skip":[[],[],[]]}]}}`
}

// fancdnSerialFixture builds a 2-season serial with 3 episodes per season,
// two voices per episode, plus one negative "special" episode in season 1.
func fancdnSerialFixture() string {
	type rv struct {
		VideoID   int    `json:"video_id"`
		Season    int    `json:"season"`
		Episode   int    `json:"episode"`
		VoiceName string `json:"voice_name"`
		VoiceID   int    `json:"voice_id"`
		Duration  int    `json:"duration"`
	}
	tree := map[string]map[string][]rv{
		"1": {
			"-1": {{VideoID: 91000, Season: 1, Episode: -1, VoiceName: "AniDUB", VoiceID: 22, Duration: 600}},
			"1":  {{VideoID: 91001, Season: 1, Episode: 1, VoiceName: "Дубляж", VoiceID: 12, Duration: 1402}, {VideoID: 91002, Season: 1, Episode: 1, VoiceName: "AniDUB", VoiceID: 22, Duration: 1402}},
			"2":  {{VideoID: 91003, Season: 1, Episode: 2, VoiceName: "Дубляж", VoiceID: 12, Duration: 1402}, {VideoID: 91004, Season: 1, Episode: 2, VoiceName: "AniDUB", VoiceID: 22, Duration: 1402}},
			"3":  {{VideoID: 91005, Season: 1, Episode: 3, VoiceName: "Дубляж", VoiceID: 12, Duration: 1402}},
		},
		"2": {
			"1": {{VideoID: 92001, Season: 2, Episode: 1, VoiceName: "Дубляж", VoiceID: 12, Duration: 1402}},
			"2": {{VideoID: 92002, Season: 2, Episode: 2, VoiceName: "Дубляж", VoiceID: 12, Duration: 1402}},
			"3": {{VideoID: 92003, Season: 2, Episode: 3, VoiceName: "Дубляж", VoiceID: 12, Duration: 1402}},
		},
	}
	b, _ := stdjson.Marshal(tree)
	return string(b)
}

// fancdnFakeUpstream mimics lomont.site closely enough for the checker to
// exercise checksearch / index / responce.php paths without going to the
// real upstream.
type fancdnFakeUpstream struct {
	gtHits         int32
	responceHits   int32
	movieKP        int // KP that returns the movie fixture
	serialKP       int // KP that returns the serial fixture
	servedVideoIDs map[int]bool
}

func (u *fancdnFakeUpstream) handler(t *testing.T) http.Handler {
	t.Helper()
	if u.servedVideoIDs == nil {
		u.servedVideoIDs = map[int]bool{}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/gt/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&u.gtHits, 1)
		kpStr := strings.TrimPrefix(r.URL.Path, "/gt/")
		var kp int
		_, _ = fmt.Sscanf(kpStr, "%d", &kp)
		switch kp {
		case u.movieKP:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(fancdnFixtureHTML(kp, fancdnMovieFixture())))
		case u.serialKP:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(fancdnFixtureHTML(kp, fancdnSerialFixture())))
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/player/responce.php", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&u.responceHits, 1)
		var vid int
		_, _ = fmt.Sscanf(r.URL.Query().Get("video_id"), "%d", &vid)
		if vid <= 0 {
			_, _ = w.Write([]byte(`{"error":"video not found"}`))
			return
		}
		u.servedVideoIDs[vid] = true
		// Fake signed CDN URL — same shape as the real one (random-looking
		// sig + ts + id).
		src := fmt.Sprintf("https://s6.example/v/fakesig%d/1779712776/%d/index.m3u8", vid, vid)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"src":%q,"spare":"https://f6.example","subtitles":[],"thumbnail":{}}`, src)
	})
	return mux
}

func TestParseFancdnTreeMovie(t *testing.T) {
	html := fancdnFixtureHTML(382731, fancdnMovieFixture())
	tree := parseFancdnTree(382731, []byte(html))
	if tree == nil {
		t.Fatal("parseFancdnTree returned nil")
	}
	if !tree.isMovie {
		t.Fatalf("expected isMovie=true for 1-episode tree, got %#v", tree)
	}
	if got := len(tree.seasons); got != 1 {
		t.Fatalf("expected 1 season, got %d", got)
	}
	s, e, v := tree.pickFirstPlayable()
	if s != 1 || e != 1 || v == nil || v.VideoID != 1009280 {
		t.Fatalf("pickFirstPlayable mismatch: s=%d e=%d v=%+v", s, e, v)
	}
}

func TestParseFancdnTreeSerial(t *testing.T) {
	html := fancdnFixtureHTML(382731, fancdnSerialFixture())
	tree := parseFancdnTree(382731, []byte(html))
	if tree == nil {
		t.Fatal("parseFancdnTree returned nil")
	}
	if tree.isMovie {
		t.Fatalf("expected isMovie=false for multi-episode tree")
	}
	if got := len(tree.seasons); got != 2 {
		t.Fatalf("expected 2 seasons, got %d", got)
	}
	// Season 1 should have 4 episodes (-1, 1, 2, 3) — negatives sorted first.
	if got := tree.episodes[1]; len(got) != 4 || got[0] != -1 || got[3] != 3 {
		t.Fatalf("season 1 episode order wrong: %v", got)
	}
	// voiceLabels deduped across the whole tree — Дубляж (12) + AniDUB (22).
	if got := len(tree.voiceLabels); got != 2 {
		t.Fatalf("expected 2 unique voices, got %d (%+v)", got, tree.voiceLabels)
	}
	if tree.voiceLabels[0].ID != 12 || tree.voiceLabels[1].ID != 22 {
		t.Fatalf("voice ordering wrong: %+v", tree.voiceLabels)
	}
}

func TestParseFancdnInputDataShapes(t *testing.T) {
	// Each shape we've seen lomont serve. Each fixture must normalise into
	// a non-empty serial tree with a video_id we can verify.
	cases := []struct {
		name        string
		body        string
		wantSeasons int
		wantVidIn   int // a video_id we expect to appear somewhere in the tree
	}{
		{
			"shape1-full-serial",
			fancdnSerialFixture(),
			2, 91001,
		},
		{
			"shape2-flat-voice-array-movie",
			`[{"video_id":555,"voice_name":"Дубляж","voice_id":12,"duration":7200}]`,
			1, 555,
		},
		{
			"shape3-season-only-no-episode-level",
			`{"1":[{"video_id":777,"season":1,"episode":1,"voice_name":"Дубляж","voice_id":12,"duration":7200}]}`,
			1, 777,
		},
		{
			"shape4-inner-single-voice-object",
			`{"1":{"1":{"video_id":999,"season":1,"episode":1,"voice_name":"Дубляж","voice_id":12,"duration":7200}}}`,
			1, 999,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := parseFancdnInputData([]byte(c.body))
			if out == nil {
				t.Fatalf("parseFancdnInputData returned nil for %q", c.body)
			}
			if got := len(out); got != c.wantSeasons {
				t.Fatalf("seasons: got %d, want %d", got, c.wantSeasons)
			}
			found := false
			for _, eMap := range out {
				for _, voices := range eMap {
					for _, v := range voices {
						if v.VideoID == c.wantVidIn {
							found = true
						}
					}
				}
			}
			if !found {
				t.Fatalf("video_id %d not found in normalised tree %+v", c.wantVidIn, out)
			}
		})
	}
}

func TestParseFancdnTreeMovieFlatArray(t *testing.T) {
	// End-to-end: full HTML with the prod movie shape (kp 460586 hit).
	// parseFancdnTree must accept the flat-array inputData and produce a
	// tree with isMovie=true and one playable voice.
	html := fancdnFixtureHTML(460586, `[{"video_id":555,"voice_name":"Дубляж","voice_id":12,"duration":7200}]`)
	tree := parseFancdnTree(460586, []byte(html))
	if tree == nil {
		t.Fatal("parseFancdnTree returned nil for flat-array movie")
	}
	if !tree.isMovie {
		t.Fatalf("expected isMovie=true for single-voice movie, got %#v", tree)
	}
	s, e, v := tree.pickFirstPlayable()
	if v == nil || v.VideoID != 555 {
		t.Fatalf("pickFirstPlayable failed: s=%d e=%d v=%+v", s, e, v)
	}
}

func TestParseFancdnTreeBadHTML(t *testing.T) {
	cases := []struct {
		name, html string
		wantNil    bool
	}{
		{"no-inputdata", `<html>no inputdata div here</html>`, true},
		{"kp-mismatch", fancdnFixtureHTML(999, fancdnMovieFixture()), true},
		{"invalid-json", `<div id="inputData" data-film='{"kp_id":1}'>not json</div>`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tree := parseFancdnTree(1, []byte(c.html))
			if c.wantNil && tree != nil {
				t.Errorf("expected nil tree, got %#v", tree)
			}
		})
	}
}

func TestFancdnCheckSearch(t *testing.T) {
	u := &fancdnFakeUpstream{movieKP: 1009280, serialKP: 382731}
	srv := httptest.NewServer(u.handler(t))
	defer srv.Close()

	cfg := config.Config{}
	cfg.Online.FanCDN.Host = srv.URL
	c := NewFancdnChecker(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !c.checkSearch(ctx, "382731") {
		t.Errorf("serial kp 382731 should checksearch=true")
	}
	if !c.checkSearch(ctx, "1009280") {
		t.Errorf("movie kp 1009280 should checksearch=true")
	}
	if c.checkSearch(ctx, "999999999") {
		t.Errorf("unknown kp should checksearch=false")
	}
	if c.checkSearch(ctx, "") {
		t.Errorf("empty kp should checksearch=false")
	}
	// Repeat hit on the same kp — should be served from cache.
	hitsBefore := atomic.LoadInt32(&u.gtHits)
	c.checkSearch(ctx, "382731")
	if got := atomic.LoadInt32(&u.gtHits); got != hitsBefore {
		t.Errorf("expected cached lookup, but gt was hit again (%d → %d)", hitsBefore, got)
	}
}

func TestFancdnIndexMovie(t *testing.T) {
	u := &fancdnFakeUpstream{movieKP: 1009280, serialKP: 382731}
	srv := httptest.NewServer(u.handler(t))
	defer srv.Close()

	cfg := config.Config{}
	cfg.Online.FanCDN.Host = srv.URL
	c := NewFancdnChecker(cfg)
	handler := c.Handle(cfg, nil)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/fancdn?rjson=true&kinopoisk_id=1009280&title=Movie", nil)
	handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad JSON: %v body=%s", err, rec.Body.String())
	}
	if out["type"] != "movie" {
		t.Fatalf("expected type=movie, got %#v", out["type"])
	}
	data, _ := out["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("expected 2 voice rows, got %d: %+v", len(data), out)
	}
	for _, row := range data {
		m := row.(map[string]any)
		if m["method"] != "play" {
			t.Fatalf("expected method=play, got %#v", m["method"])
		}
		u, _ := m["url"].(string)
		if !strings.Contains(u, "/lite/fancdn/video.m3u8?vid=") {
			t.Fatalf("row url should be lazy-resolve link, got %q", u)
		}
	}
}

func TestFancdnIndexSerialSeasonsThenEpisodes(t *testing.T) {
	u := &fancdnFakeUpstream{movieKP: 1, serialKP: 382731}
	srv := httptest.NewServer(u.handler(t))
	defer srv.Close()

	cfg := config.Config{}
	cfg.Online.FanCDN.Host = srv.URL
	c := NewFancdnChecker(cfg)
	handler := c.Handle(cfg, nil)

	// 1) No s/t → season list.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/fancdn?rjson=true&kinopoisk_id=382731&title=Serial&year=1999", nil)
	handler.ServeHTTP(rec, r)
	var out map[string]any
	_ = stdjson.Unmarshal(rec.Body.Bytes(), &out)
	if out["type"] != "season" {
		t.Fatalf("stage 1: expected type=season, got %#v body=%s", out["type"], rec.Body.String())
	}
	data, _ := out["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("expected 2 seasons, got %d", len(data))
	}

	// 2) s=1 → episode list for season 1 with default voice (id=12).
	rec2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/fancdn?rjson=true&kinopoisk_id=382731&title=Serial&year=1999&s=1", nil)
	handler.ServeHTTP(rec2, r2)
	var out2 map[string]any
	_ = stdjson.Unmarshal(rec2.Body.Bytes(), &out2)
	if out2["type"] != "episode" {
		t.Fatalf("stage 2: expected type=episode, got %#v body=%s", out2["type"], rec2.Body.String())
	}
	d2, _ := out2["data"].([]any)
	if len(d2) != 4 {
		t.Fatalf("expected 4 episodes in season 1 (incl. special), got %d", len(d2))
	}
	voiceTabs, hasVoice := out2["voice"].([]any)
	if !hasVoice || len(voiceTabs) != 2 {
		t.Fatalf("expected 2 voice tabs (Дубляж + AniDUB), got %d/%v", len(voiceTabs), voiceTabs)
	}

	// 3) s=1&t=22 → episode list with AniDUB chosen, fallback to Дубляж
	// where AniDUB is missing (ep 3 only has Дубляж in the fixture).
	rec3 := httptest.NewRecorder()
	r3 := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/fancdn?rjson=true&kinopoisk_id=382731&s=1&t=22", nil)
	handler.ServeHTTP(rec3, r3)
	var out3 map[string]any
	_ = stdjson.Unmarshal(rec3.Body.Bytes(), &out3)
	d3, _ := out3["data"].([]any)
	if len(d3) != 4 {
		t.Fatalf("voice-2 listing should still show all 4 eps (with fallback), got %d", len(d3))
	}
}

func TestExtractFancdnSubtitles(t *testing.T) {
	// Polymorphic shapes observed on the wire: [] when empty, {lang:url}
	// when populated. Extractor must accept both and ignore anything
	// weird so a future schema change doesn't blow up playback.
	cases := []struct {
		name    string
		raw     any
		wantLen int
		wantRu  string // URL we expect for the "ru" label, when applicable
	}{
		{"nil-empty", nil, 0, ""},
		{"empty-array-fallback", []any{}, 0, ""}, // wrong shape — handled as miss
		{"prod-shape", map[string]any{
			"ru": "https://lomont.site/player/subtitle/ru_366667.vtt",
			"en": "https://lomont.site/player/subtitle/en_366667.vtt",
		}, 2, "https://lomont.site/player/subtitle/ru_366667.vtt"},
		{"empty-string-value", map[string]any{"ru": ""}, 0, ""},
		{"non-string-value", map[string]any{"ru": 42}, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := extractFancdnSubtitles(c.raw)
			if len(out) != c.wantLen {
				t.Fatalf("got %d subs, want %d (%+v)", len(out), c.wantLen, out)
			}
			if c.wantRu == "" {
				return
			}
			found := false
			for _, s := range out {
				if s.Label == "ru" && s.URL == c.wantRu {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected ru subtitle %q, not in %+v", c.wantRu, out)
			}
		})
	}
}

func TestFancdnResponcePolymorphicJSON(t *testing.T) {
	// Real responce.php payload from prod (kp 366667) that crashed the old
	// strict-array Subtitles type and the strict-object Thumbnail type.
	// resolveVideo must succeed and emit a usable src + extracted subs.
	mux := http.NewServeMux()
	mux.HandleFunc("/player/responce.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"src":"https://s1.example/v/sig/1779723463/366667/index.m3u8","spare":"https://f1.werberk.pro","subtitles":{"ru":"https://lomont.site/player/subtitle/ru_366667.vtt","en":"https://lomont.site/player/subtitle/en_366667.vtt"},"thumbnail":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := config.Config{}
	cfg.Online.FanCDN.Host = srv.URL
	c := NewFancdnChecker(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	src, hdr, subs, err := c.resolveVideo(ctx, 366667)
	if err != nil {
		t.Fatalf("resolveVideo failed: %v", err)
	}
	if !strings.HasSuffix(src, "/index.m3u8") {
		t.Fatalf("unexpected src: %s", src)
	}
	if hdr["Referer"] == "" {
		t.Fatalf("Referer not set in headers: %#v", hdr)
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 subtitles (ru+en), got %d: %+v", len(subs), subs)
	}
}

func TestFancdnVideoHandlerResolves(t *testing.T) {
	u := &fancdnFakeUpstream{movieKP: 1009280, serialKP: 382731}
	srv := httptest.NewServer(u.handler(t))
	defer srv.Close()

	cfg := config.Config{}
	cfg.Online.FanCDN.Host = srv.URL
	c := NewFancdnChecker(cfg)
	handler := FancdnVideoHandler(c, nil) // nil links → 302 to raw CDN URL

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/fancdn/video.m3u8?vid=1009280", nil)
	handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/v/fakesig1009280/") || !strings.HasSuffix(loc, "/index.m3u8") {
		t.Fatalf("Location header doesn't match fake CDN URL: %s", loc)
	}
	if got := atomic.LoadInt32(&u.responceHits); got != 1 {
		t.Fatalf("expected 1 responce.php hit, got %d", got)
	}
	if !u.servedVideoIDs[1009280] {
		t.Fatalf("responce.php was not called with vid=1009280")
	}

	// Invalid vid
	rec2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/fancdn/video?vid=0", nil)
	handler.ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for vid=0, got %d", rec2.Code)
	}
}

func TestFancdnEpisodeName(t *testing.T) {
	cases := []struct {
		e    int
		want string
	}{
		{1, "1 серия"},
		{42, "42 серия"},
		{-18, "Спецвыпуск 18"},
		{-1, "Спецвыпуск 1"},
	}
	for _, c := range cases {
		if got := fancdnEpisodeName(c.e); got != c.want {
			t.Errorf("fancdnEpisodeName(%d) = %q, want %q", c.e, got, c.want)
		}
	}
}

// fancdnGeoBlockBody is the real-world stub lomont.site serves to IPs
// outside the catalogue's allowed regions — captured from a PL-based
// AS215730 server during prod debugging on 2026-05-25.
const fancdnGeoBlockBody = `<html><head><title>404 Not Found</title></head><body bgcolor="white"><script>var isFramed=!1;try{isFramed=window!=window.top||document!=top.document||self.location!=top.location}catch(e){isFramed=!0}isFramed||(document.querySelectorAll("body")[0].remove(),document.querySelectorAll("html")[0].innerHTML="<html><head><title>404 Not Found</title></head><body bgcolor=\"white\"><center><h1>404 Not Found</h1></center><hr><center>NGINX</center></body></html>");</script><center><h1>404 Not Found</h1></center><hr><center>nginx</center></body></html>`

func TestFancdnGeoBlockDetect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/gt/", func(w http.ResponseWriter, r *http.Request) {
		// HTTP 200 with the geo-block stub body — the exact pattern lomont
		// returns to non-allowed regions.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fancdnGeoBlockBody))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := config.Config{}
	cfg.Online.FanCDN.Host = srv.URL
	c := NewFancdnChecker(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Tree resolve should return nil (correctly treating geo-block as miss)
	// and flip the internal blocked flag.
	if tree := c.resolve(ctx, "382731"); tree != nil {
		t.Fatalf("expected nil tree on geo-block, got %#v", tree)
	}
	c.geo.mu.Lock()
	blocked := c.geo.blocked
	c.geo.mu.Unlock()
	if !blocked {
		t.Fatal("expected geo.blocked=true after seeing 200-with-404 stub")
	}
	if c.checkSearch(ctx, "382731") {
		t.Fatal("checkSearch must return false when geo-blocked")
	}
}

func TestFancdnTransportErrorCached(t *testing.T) {
	// Server that ALWAYS closes the connection immediately to simulate
	// the "utls dial: EOF" pattern we see when lomont reset-bans our IP.
	// Use a TCP listener that accepts and closes — httptest.Server runs
	// HTTP, so a raw net.Listener is closer to the reality.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	cfg := config.Config{}
	cfg.Online.FanCDN.Host = "http://" + ln.Addr().String()
	c := NewFancdnChecker(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First call: hits the dead listener, errors out, MUST be cached as miss.
	if tree := c.resolve(ctx, "382731"); tree != nil {
		t.Fatalf("expected nil tree on transport error, got %#v", tree)
	}

	// Cache must contain a negative entry — second call within the miss
	// TTL must return nil WITHOUT actually dialing again.
	v, ok := c.treeCache.Load(382731)
	if !ok {
		t.Fatal("transport error was not cached — would cause Lampa probe storms")
	}
	entry := v.(*fancdnTreeEntry)
	if entry.tree != nil {
		t.Fatalf("expected negative cache entry, got %#v", entry.tree)
	}
}

func TestFancdnGeoBlockRecovers(t *testing.T) {
	// Server flips from geo-block to working catalogue between requests —
	// the second call should reset the blocked flag.
	var requestN int32
	mux := http.NewServeMux()
	mux.HandleFunc("/gt/", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requestN, 1)
		if n == 1 {
			_, _ = w.Write([]byte(fancdnGeoBlockBody))
			return
		}
		_, _ = w.Write([]byte(fancdnFixtureHTML(382731, fancdnMovieFixture())))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := config.Config{}
	cfg.Online.FanCDN.Host = srv.URL
	c := NewFancdnChecker(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First call: geo-blocked.
	c.resolve(ctx, "382731")
	c.geo.mu.Lock()
	blockedAfter1 := c.geo.blocked
	c.geo.mu.Unlock()
	if !blockedAfter1 {
		t.Fatal("expected blocked=true after first call")
	}
	// Wait past negative cache so second call re-fetches.
	// (Easier than reaching into the cache from a test — just bypass with a
	// new kp string; the cache key is the int kp so this still hits.)
	// We instead clear the cache directly.
	c.treeCache.Range(func(k, _ any) bool { c.treeCache.Delete(k); return true })

	// Second call: working catalogue → blocked flag must clear.
	tree := c.resolve(ctx, "382731")
	if tree == nil {
		t.Fatal("expected non-nil tree on second call")
	}
	c.geo.mu.Lock()
	blockedAfter2 := c.geo.blocked
	c.geo.mu.Unlock()
	if blockedAfter2 {
		t.Fatal("expected blocked=false after successful catalogue parse")
	}
}

func TestFancdnLegacyHostRedirect(t *testing.T) {
	// Old fanserial.me / r.xsmart.tv hosts in user TOML get silently
	// redirected to lomont.site so upgrades don't strand stale configs
	// pointing at dead backends.
	cases := []struct {
		host string
		want string
	}{
		{"", "https://lomont.site"},
		{"https://fanserial.me", "https://lomont.site"},
		{"https://r.xsmart.tv", "https://lomont.site"},
		{"https://1fanserials.com", "https://lomont.site"},
		{"https://lomont.site", "https://lomont.site"},
		{"https://example.org", "https://example.org"}, // unknown host left alone
	}
	for _, c := range cases {
		cfg := config.Config{}
		cfg.Online.FanCDN.Host = c.host
		ch := NewFancdnChecker(cfg)
		if ch.host != c.want {
			t.Errorf("host %q → %q, want %q", c.host, ch.host, c.want)
		}
	}
}
