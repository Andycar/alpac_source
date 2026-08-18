package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

func TestZetflixChecksearch(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/iplayer/videodb.php" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("kp") != "900" {
			t.Fatalf("unexpected kp: %s", r.URL.Query().Get("kp"))
		}
		_, _ = w.Write([]byte(`new Playerjs({file:[{title:"Dub",file:"[1080]https://cdn.example/m.mp4"}]});`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Zetflix: config.ZetflixSource{Host: api.URL, HLS: true},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/zetflix?checksearch=true&kinopoisk_id=900", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", rec.Body.String())
	}
}

func TestZetflixMovieRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/iplayer/videodb.php" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`new Playerjs({file:[{title:"Dub A",file:"[1080]https://cdn.example/m1.mp4,[720]https://cdn.example/m1-720.m3u8"}]});`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Zetflix: config.ZetflixSource{Host: api.URL, HLS: true},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/zetflix?rjson=true&kinopoisk_id=901&title=Film&original_title=Film",
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
	if toString(payload["type"]) != "movie" {
		t.Fatalf("unexpected type: %#v body=%s", payload["type"], rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("unexpected rows count: %d", len(data))
	}
	row, _ := data[0].(map[string]any)
	if !strings.Contains(toString(row["url"]), ":hls:manifest.m3u8") {
		t.Fatalf("unexpected movie url (hls conversion expected): %#v", row["url"])
	}
	if _, ok := row["streamquality"].([]any); !ok {
		t.Fatalf("streamquality missing: %#v", row)
	}
}

func TestZetflixSerialRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/iplayer/videodb.php" {
			_, _ = w.Write([]byte(`new Playerjs({file:[{title:"Dub A",folder:[{comment:"1 серия",file:"[1080]https://cdn.example/a-s1e1.mp4"},{comment:"2 серия",file:"[720]https://cdn.example/a-s1e2.mp4"}]},{title:"Dub B",folder:[{comment:"1 серия",file:"[1080]https://cdn.example/b-s1e1.mp4"}]}],quality:"1080p"});`))
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Zetflix: config.ZetflixSource{Host: api.URL, HLS: true},
		},
	}

	seasonRec := httptest.NewRecorder()
	seasonReq := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/zetflix?rjson=true&kinopoisk_id=902&title=Show&original_title=Show&serial=1",
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
	if toString(seasonPayload["type"]) != "season" {
		t.Fatalf("unexpected season type: %#v body=%s", seasonPayload["type"], seasonRec.Body.String())
	}
	seasons, _ := seasonPayload["data"].([]any)
	if len(seasons) != 1 {
		t.Fatalf("unexpected season rows count: %d", len(seasons))
	}

	episodeRec := httptest.NewRecorder()
	episodeReq := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/zetflix?rjson=true&kinopoisk_id=902&title=Show&original_title=Show&serial=1&s=1&t=Dub%20B",
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
	if toString(episodePayload["type"]) != "episode" {
		t.Fatalf("unexpected episode type: %#v body=%s", episodePayload["type"], episodeRec.Body.String())
	}
	voices, _ := episodePayload["voice"].([]any)
	if len(voices) != 2 {
		t.Fatalf("unexpected voice rows: %d", len(voices))
	}
	data, _ := episodePayload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("unexpected episode rows: %d", len(data))
	}
	row, _ := data[0].(map[string]any)
	if !strings.Contains(toString(row["url"]), "b-s1e1.mp4:hls:manifest.m3u8") {
		t.Fatalf("unexpected episode url: %#v", row["url"])
	}
}

func TestZetflixCDNReplace(t *testing.T) {
	// Simulates zetflix returning URLs with dead prosto.hdvideobox.me CDN
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`new Playerjs({file:[{title:"Dub",file:"[1080]https://prosto.hdvideobox.me/hash:2026/movies/abc/1080.mp4,[720]https://prosto.hdvideobox.me/hash:2026/movies/abc/720.mp4"}]});`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Zetflix: config.ZetflixSource{Host: api.URL, HLS: false, CDN: "https://newcdn.example.com"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/zetflix?rjson=true&kinopoisk_id=100&title=Test&original_title=Test",
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
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("unexpected rows: %d", len(data))
	}
	row, _ := data[0].(map[string]any)
	u := toString(row["url"])
	if strings.Contains(u, "prosto.hdvideobox.me") {
		t.Fatalf("dead CDN domain not replaced: %s", u)
	}
	if !strings.Contains(u, "newcdn.example.com") {
		t.Fatalf("CDN replacement not applied, url=%s", u)
	}
}

func TestZetflixStreamProxy(t *testing.T) {
	// Test that streamproxy wraps URLs through /proxy/
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`new Playerjs({file:[{title:"Dub",file:"[1080]https://cdn.example/m.mp4"}]});`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Zetflix: config.ZetflixSource{Host: api.URL, HLS: false, StreamProxy: true},
		},
	}

	links, err := proxylink.New(proxylink.Options{
		CacheDir:   t.TempDir(),
		EncryptAES: true,
	})
	if err != nil {
		t.Fatalf("failed to create proxylink: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/zetflix?rjson=true&kinopoisk_id=200&title=Test&original_title=Test",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, links, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("unexpected rows: %d", len(data))
	}
	row, _ := data[0].(map[string]any)
	u := toString(row["url"])
	if !strings.Contains(u, "/proxy/") {
		t.Fatalf("stream proxy not applied, url=%s", u)
	}
	if strings.Contains(u, "cdn.example") {
		t.Fatalf("raw CDN URL should be encrypted, url=%s", u)
	}
}
