package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/browser"
	"lampac-go/internal/config"
)

func TestMirageChecksearch(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"category":1,"token_movie":"tm1"}}`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Mirage: config.MirageSource{APIHost: api.URL, LinkHost: api.URL, Token: "token123"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/mirage?checksearch=true&kinopoisk_id=123", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", rec.Body.String())
	}
}

func TestMirageMovieRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"category":1,"token_movie":"tm1"}}`))
	}))
	defer api.Close()

	link := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.URL.Query().Get("token_movie") == "tm1" {
			frame := `{"all":{"theatrical":{"v1":{"a":{"id":101,"translation":"Dub A","quality":"1080p"}},"v2":{"a":{"id":102,"translation":"Dub B","quality":"720p"}}}}}`
			_, _ = w.Write([]byte(mirageTestFrameHTML(frame)))
			return
		}
		http.NotFound(w, r)
	}))
	defer link.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Mirage: config.MirageSource{APIHost: api.URL, LinkHost: link.URL, Token: "token123"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/mirage?rjson=true&kinopoisk_id=123&title=Film&original_title=Film+Orig",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v body=%s", err, rec.Body.String())
	}
	if payload["type"] != "movie" {
		t.Fatalf("unexpected type: %#v body=%s", payload["type"], rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected movie rows: %d", len(data))
	}
	row0, _ := data[0].(map[string]any)
	if !strings.Contains(toString(row0["url"]), "/lite/mirage/video?id_file=") {
		t.Fatalf("unexpected first row url: %#v", row0["url"])
	}
}

func TestMirageSeasonEpisodeRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"token_movie":"ts1","name":"Show","year":2024,"category_id":2}]}`))
	}))
	defer api.Close()

	link := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.URL.Query().Get("token_movie") == "ts1" {
			frame := `{"all":{"1":{"1":{"a":{"id_translation":10,"translation":"Dub A","episode":1,"id":1001},"b":{"id_translation":20,"translation":"Dub B","episode":1,"id":2001}},"2":{"a":{"id_translation":10,"translation":"Dub A","episode":2,"id":1002}}},"2":{"1":{"a":{"id_translation":10,"translation":"Dub A","episode":1,"id":1101}}}}}`
			_, _ = w.Write([]byte(mirageTestFrameHTML(frame)))
			return
		}
		http.NotFound(w, r)
	}))
	defer link.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Mirage: config.MirageSource{APIHost: api.URL, LinkHost: link.URL, Token: "token123"},
		},
	}

	seasonRec := httptest.NewRecorder()
	seasonReq := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/mirage?rjson=true&title=Show&original_title=Show&year=2024&serial=1",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(seasonRec, seasonReq)

	if seasonRec.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d body=%s", seasonRec.Code, seasonRec.Body.String())
	}
	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(seasonRec.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("invalid season json: %v", err)
	}
	if seasonPayload["type"] != "season" {
		t.Fatalf("unexpected season type: %#v body=%s", seasonPayload["type"], seasonRec.Body.String())
	}
	seasons, _ := seasonPayload["data"].([]any)
	if len(seasons) != 2 {
		t.Fatalf("unexpected seasons count: %d", len(seasons))
	}

	episodeRec := httptest.NewRecorder()
	episodeReq := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/mirage?rjson=true&title=Show&original_title=Show&year=2024&serial=1&s=1",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(episodeRec, episodeReq)

	if episodeRec.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d body=%s", episodeRec.Code, episodeRec.Body.String())
	}
	var episodePayload map[string]any
	if err := stdjson.Unmarshal(episodeRec.Body.Bytes(), &episodePayload); err != nil {
		t.Fatalf("invalid episode json: %v", err)
	}
	if episodePayload["type"] != "episode" {
		t.Fatalf("unexpected episode type: %#v", episodePayload["type"])
	}
	voices, _ := episodePayload["voice"].([]any)
	if len(voices) != 2 {
		t.Fatalf("unexpected voices count: %d", len(voices))
	}
	rows, _ := episodePayload["data"].([]any)
	if len(rows) != 2 {
		t.Fatalf("unexpected episodes count: %d", len(rows))
	}
}

func TestMirageVideoEndpoints(t *testing.T) {
	// The video endpoint resolves stream qualities through a headless browser
	// (chromedp/facade) that replays the CDN's JS-generated auth headers — the
	// mock's /movies/{id} XHR is meant to be driven by that browser. Without a
	// chrome/chromium binary the resolve can't run (the plain-HTTP fallbacks
	// hit / and /bnsi/movies/{id}, which the mock 404s), so skip rather than
	// assert a guaranteed-empty response.
	if eng := browser.ForBalancer("mirage"); eng == nil || eng.Available() != nil {
		t.Skip("mirage video resolve requires a headless browser (chrome/chromium not available)")
	}

	hits := 0
	link := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/movies/777" {
			_, _ = w.Write([]byte(`{"hlsSource":[{"quality":{"1080":"https://cdn.example/m1080.m3u8","720":"https://cdn.example/m720.m3u8"},"default":true}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer link.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Mirage: config.MirageSource{APIHost: link.URL, LinkHost: link.URL, Token: "token123"},
		},
	}

	jsonRec := httptest.NewRecorder()
	jsonReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/mirage/video?id_file=777&token_movie=tm1", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(jsonRec, jsonReq)

	if jsonRec.Code != http.StatusOK {
		t.Fatalf("unexpected json status: %d body=%s", jsonRec.Code, jsonRec.Body.String())
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(jsonRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json payload: %v", err)
	}
	if payload["method"] != "play" {
		t.Fatalf("unexpected method: %#v body=%s hits=%d", payload["method"], jsonRec.Body.String(), hits)
	}
	if toString(payload["url"]) != "https://cdn.example/m1080.m3u8" {
		t.Fatalf("unexpected play url: %#v", payload["url"])
	}

	redirectRec := httptest.NewRecorder()
	redirectReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/mirage/video.m3u8?id_file=777&token_movie=tm1&play=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(redirectRec, redirectReq)

	if redirectRec.Code != http.StatusFound {
		t.Fatalf("unexpected redirect status: %d", redirectRec.Code)
	}
	if got := redirectRec.Header().Get("Location"); got != "https://cdn.example/m1080.m3u8" {
		t.Fatalf("unexpected redirect location: %s", got)
	}
	if hits == 0 {
		t.Fatalf("expected link host hits, got 0")
	}
}

func mirageTestFrameHTML(frame string) string {
	enc := strings.ReplaceAll(frame, `\`, `\\`)
	enc = strings.ReplaceAll(enc, `'`, `\'`)
	return `<script>fileList = JSON.parse('` + enc + `');</script>`
}
