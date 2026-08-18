package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestGetsTVChecksearchNative(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/movies" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"_id":"m1","contentType":"movie","poster":"p1","title":{"ru":"Начало","en":"Inception"},"released":"2010-07-16T00:00:00Z"}
		]`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			GetsTV: config.HostTokenSource{Host: api.URL, Token: "tok"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/getstv?checksearch=true&title=Inception&original_title=Inception&year=2010", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestGetsTVIndexMovieHTML(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		switch r.URL.Path {
		case "/api/movies":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"_id":"m1","contentType":"movie","poster":"p1","title":{"ru":"Начало","en":"Inception"},"released":"2010-07-16T00:00:00Z"}
			]`))
		case "/api/movies/m1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"type":"movie","media":[{"_id":"med-1","trName":"English","sourceType":"dub"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			GetsTV: config.HostTokenSource{Host: api.URL, Token: "tok"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/getstv?title=Inception&original_title=Inception&year=2010", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "videos__item videos__movie") {
		t.Fatalf("unexpected html body: %s", body)
	}
	if !strings.Contains(body, "/lite/getstv/video.m3u8?id=med-1") {
		t.Fatalf("missing video link in html: %s", body)
	}
	if !strings.Contains(body, "English") {
		t.Fatalf("missing translation label in html: %s", body)
	}
}

func TestGetsTVIndexSeasonAndEpisodeRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		if r.URL.Path != "/api/movies/s1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"type":"serial",
			"seasons":[
				{"seasonNum":1,"episodes":[
					{"episodeNum":1,"trs":[{"_id":"ep-1-v1","trId":1,"trName":"Voice A"},{"_id":"ep-1-v2","trId":2,"trName":"Voice B"}]},
					{"episodeNum":2,"trs":[{"_id":"ep-2-v2","trId":2,"trName":"Voice B"}]}
				]},
				{"seasonNum":2,"episodes":[]}
			]
		}`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			GetsTV: config.HostTokenSource{Host: api.URL, Token: "tok"},
		},
	}

	seasonRec := httptest.NewRecorder()
	seasonReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/getstv?orid=s1&rjson=true&title=Serial&original_title=Serial&year=2024", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(seasonRec, seasonReq)

	if seasonRec.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d", seasonRec.Code)
	}
	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(seasonRec.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("invalid season json: %v", err)
	}
	if toString(seasonPayload["type"]) != "season" {
		t.Fatalf("unexpected season type: %v", seasonPayload["type"])
	}
	seasonData, _ := seasonPayload["data"].([]any)
	if len(seasonData) != 2 {
		t.Fatalf("unexpected season data length: %d", len(seasonData))
	}

	episodeRec := httptest.NewRecorder()
	episodeReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/getstv?orid=s1&s=1&t=2&rjson=true&title=Serial&original_title=Serial&year=2024", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(episodeRec, episodeReq)

	if episodeRec.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d", episodeRec.Code)
	}
	var episodePayload map[string]any
	if err := stdjson.Unmarshal(episodeRec.Body.Bytes(), &episodePayload); err != nil {
		t.Fatalf("invalid episode json: %v", err)
	}
	if toString(episodePayload["type"]) != "episode" {
		t.Fatalf("unexpected episode type: %v", episodePayload["type"])
	}
	voices, _ := episodePayload["voice"].([]any)
	if len(voices) != 2 {
		t.Fatalf("unexpected voice length: %d", len(voices))
	}
	data, _ := episodePayload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected episode data length: %d", len(data))
	}
	first, _ := data[0].(map[string]any)
	if toString(first["url"]) == "" || !strings.Contains(toString(first["url"]), "ep-1-v2") {
		t.Fatalf("unexpected first episode url: %v", first["url"])
	}
}

func TestGetsTVVideoRoute(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		if r.URL.Path != "/api/media/med-1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("format") != "m3u8" || r.URL.Query().Get("protocol") != "https" {
			t.Fatalf("unexpected media query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"resolutions":[
				{"url":"https://cdn.example/1080.m3u8","type":1080},
				{"url":"https://cdn.example/720.m3u8","type":720}
			],
			"subtitles":[{"lang":"ru","url":"https://cdn.example/sub-ru.vtt"}],
			"media":{"movie":{"title":{"ru":"Тест","en":"Test"}}}
		}`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			GetsTV: config.HostTokenSource{Host: api.URL, Token: "tok"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/getstv/video.m3u8?id=med-1", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if toString(payload["method"]) != "play" {
		t.Fatalf("unexpected method: %v", payload["method"])
	}
	if toString(payload["url"]) != "https://cdn.example/1080.m3u8" {
		t.Fatalf("unexpected play url: %v", payload["url"])
	}
	quality, _ := payload["quality"].(map[string]any)
	if toString(quality["1080p"]) != "https://cdn.example/1080.m3u8" {
		t.Fatalf("unexpected 1080 quality: %v", quality["1080p"])
	}

	redirectRec := httptest.NewRecorder()
	redirectReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/getstv/video.m3u8?id=med-1&play=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(redirectRec, redirectReq)

	if redirectRec.Code != http.StatusFound {
		t.Fatalf("unexpected redirect status: %d", redirectRec.Code)
	}
	if redirectRec.Header().Get("Location") != "https://cdn.example/1080.m3u8" {
		t.Fatalf("unexpected redirect location: %s", redirectRec.Header().Get("Location"))
	}
}

func TestGetsTVSearchRouteRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		if r.URL.Path != "/api/movies" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"_id":"m1","contentType":"movie","poster":"p1","title":{"ru":"Начало","en":"Inception"},"released":"2010-07-16T00:00:00Z"}
		]`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			GetsTV: config.HostTokenSource{Host: api.URL, Token: "tok"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/getstv-search?title=Inception&rjson=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if toString(payload["type"]) != "similar" {
		t.Fatalf("unexpected type: %v", payload["type"])
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("unexpected similar length: %d", len(data))
	}
	first, _ := data[0].(map[string]any)
	if !strings.Contains(toString(first["url"]), "/lite/getstv?orid=m1") {
		t.Fatalf("unexpected similar url: %v", first["url"])
	}
}
