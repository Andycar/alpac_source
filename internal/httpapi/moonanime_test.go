package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestMoonAnimeChecksearch(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/2.0/titles" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"anime_list":[{"id":101,"title":"Naruto","year":2002,"poster":"https://img.example/naruto.jpg"}]}`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			MoonAnime: config.HostTokenSource{Host: api.URL, Token: "token123"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/moonanime?checksearch=true&title=Naruto&original_title=Naruto", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", rec.Body.String())
	}
}

func TestMoonAnimeSimilarRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/2.0/titles" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"anime_list":[{"id":201,"title":"Naruto","year":2002,"poster":"https://img.example/n1.jpg"},{"id":202,"title":"Naruto Shippuden","year":2007,"poster":"https://img.example/n2.jpg"}]}`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			MoonAnime: config.HostTokenSource{Host: api.URL, Token: "token123"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/moonanime?rjson=true&title=Naruto&original_title=Naruto", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload["type"] != "similar" {
		t.Fatalf("unexpected type: %#v", payload["type"])
	}
	data, _ := payload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected similar count: %d", len(data))
	}
}

func TestMoonAnimeSeasonAndEpisodeRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/2.0/titles":
			_, _ = w.Write([]byte(`{"anime_list":[{"id":301,"title":"One Piece","year":1999,"poster":"https://img.example/op.jpg"}]}`))
		case r.URL.Path == "/api/2.0/title/301/videos":
			_, _ = w.Write([]byte(`[{"AniDub":{"1":[{"episode":1,"vod":"https://vod.example/ad1"},{"episode":2,"vod":"https://vod.example/ad2"}],"2":[{"episode":1,"vod":"https://vod.example/ad-s2e1"}]}},{"AniLibria":{"1":[{"episode":1,"vod":"https://vod.example/al1"}]}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			MoonAnime: config.HostTokenSource{Host: api.URL, Token: "token123"},
		},
	}

	seasonRec := httptest.NewRecorder()
	seasonReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/moonanime?rjson=true&title=One+Piece&original_title=One+Piece", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(seasonRec, seasonReq)

	if seasonRec.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d body=%s", seasonRec.Code, seasonRec.Body.String())
	}
	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(seasonRec.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("invalid season json: %v", err)
	}
	if seasonPayload["type"] != "season" {
		t.Fatalf("unexpected season type: %#v", seasonPayload["type"])
	}
	seasons, _ := seasonPayload["data"].([]any)
	if len(seasons) != 2 {
		t.Fatalf("unexpected seasons count: %d", len(seasons))
	}

	episodeRec := httptest.NewRecorder()
	episodeReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/moonanime?rjson=true&title=One+Piece&original_title=One+Piece&s=1", nil)
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
		t.Fatalf("unexpected voice count: %d", len(voices))
	}
	rows, _ := episodePayload["data"].([]any)
	if len(rows) != 2 {
		t.Fatalf("unexpected episodes count: %d", len(rows))
	}
	row0, _ := rows[0].(map[string]any)
	if !strings.Contains(toString(row0["url"]), "/lite/moonanime/video?") {
		t.Fatalf("unexpected episode url: %#v", row0["url"])
	}
}

func TestMoonAnimeVideoEndpoints(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/player/1":
			_, _ = w.Write([]byte(`<script>var p={file:"https://cdn.example/moon/master.m3u8", subtitle:"https://cdn.example/moon/subs.vtt"};</script>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			MoonAnime: config.HostTokenSource{Host: api.URL, Token: "token123"},
		},
	}

	vod := url.QueryEscape(api.URL + "/player/1")

	jsonRec := httptest.NewRecorder()
	jsonReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/moonanime/video?vod="+vod+"&title=One+Piece&original_title=One+Piece", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(jsonRec, jsonReq)

	if jsonRec.Code != http.StatusOK {
		t.Fatalf("unexpected json status: %d body=%s", jsonRec.Code, jsonRec.Body.String())
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(jsonRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json payload: %v", err)
	}
	if payload["method"] != "play" {
		t.Fatalf("unexpected method: %#v", payload["method"])
	}
	if toString(payload["url"]) != "https://cdn.example/moon/master.m3u8" {
		t.Fatalf("unexpected url: %#v", payload["url"])
	}

	redirectRec := httptest.NewRecorder()
	redirectReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/moonanime/video.m3u8?vod="+vod+"&play=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(redirectRec, redirectReq)

	if redirectRec.Code != http.StatusFound {
		t.Fatalf("unexpected redirect status: %d", redirectRec.Code)
	}
	if got := redirectRec.Header().Get("Location"); got != "https://cdn.example/moon/master.m3u8" {
		t.Fatalf("unexpected redirect location: %s", got)
	}
}
