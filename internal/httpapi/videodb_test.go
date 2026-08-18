package httpapi

import (
	"encoding/base64"
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestVideoDBChecksearch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(videodbTestHTML(`{"file":[{"title":"Dub","file":"[1080]https://cdn.example/m1080.m3u8"}]}`)))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			VideoDB: config.VideoDBSource{Host: upstream.URL, APIHost: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/videodb?checksearch=true&kinopoisk_id=123", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", rec.Body.String())
	}
}

func TestVideoDBMovieRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		raw := `{"file":[{"title":"Dub A","file":"[1080]https://cdn.example/m1080.m3u8,[720]https://cdn.example/m720.m3u8"}]}`
		_, _ = w.Write([]byte(videodbTestHTML(raw)))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			VideoDB: config.VideoDBSource{Host: upstream.URL, APIHost: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/videodb?rjson=true&kinopoisk_id=123&title=Film&original_title=Film+Orig",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, rec.Body.String())
	}
	if payload["type"] != "movie" {
		t.Fatalf("unexpected type: %#v", payload["type"])
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) == 0 {
		t.Fatalf("unexpected data: %#v", payload["data"])
	}
	row := data[0].(map[string]any)
	if _, ok := row["streamquality"].([]any); !ok {
		t.Fatalf("streamquality missing: %#v", row)
	}
}

func TestVideoDBSerialRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		raw := `{"file":[{"title":"1 сезон","folder":[{"title":"1 серия","folder":[{"title":"MVO | Voice A","file":"[1080]https://cdn.example/s1e1a.m3u8"},{"title":"MVO | Voice B","file":"[720]https://cdn.example/s1e1b.m3u8"}]},{"title":"2 серия","folder":[{"title":"MVO | Voice A","file":"[1080]https://cdn.example/s1e2a.m3u8"}]}]}]}`
		_, _ = w.Write([]byte(videodbTestHTML(raw)))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			VideoDB: config.VideoDBSource{Host: upstream.URL, APIHost: upstream.URL},
		},
	}

	recSeason := httptest.NewRecorder()
	reqSeason := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/videodb?rjson=true&kinopoisk_id=777&title=Serial&original_title=Serial+Orig",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(recSeason, reqSeason)

	if recSeason.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d", recSeason.Code)
	}
	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(recSeason.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("season unmarshal: %v, body=%s", err, recSeason.Body.String())
	}
	if seasonPayload["type"] != "season" {
		t.Fatalf("unexpected season type: %#v", seasonPayload["type"])
	}

	recEpisode := httptest.NewRecorder()
	reqEpisode := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/videodb?rjson=true&kinopoisk_id=777&title=Serial&original_title=Serial+Orig&s=1&sid=0&t=Voice+A",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(recEpisode, reqEpisode)

	if recEpisode.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d", recEpisode.Code)
	}
	var episodePayload map[string]any
	if err := stdjson.Unmarshal(recEpisode.Body.Bytes(), &episodePayload); err != nil {
		t.Fatalf("episode unmarshal: %v, body=%s", err, recEpisode.Body.String())
	}
	if episodePayload["type"] != "episode" {
		t.Fatalf("unexpected episode type: %#v", episodePayload["type"])
	}
	if _, ok := episodePayload["voice"].([]any); !ok {
		t.Fatalf("voice payload missing: %#v", episodePayload)
	}
	data, ok := episodePayload["data"].([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("unexpected episode data: %#v", episodePayload["data"])
	}
}

func TestVideoDBManifestJSON(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			w.Header().Set("Location", "/stream.m3u8")
			w.WriteHeader(http.StatusFound)
		case "/stream.m3u8":
			_, _ = w.Write([]byte("#EXTM3U"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			VideoDB: config.VideoDBSource{Host: target.URL, APIHost: target.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/videodb/manifest?link="+urlQueryEscape(target.URL+"/redirect"),
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, rec.Body.String())
	}
	if payload["method"] != "play" {
		t.Fatalf("unexpected method: %#v", payload["method"])
	}
	if !strings.Contains(toString(payload["url"]), "/stream.m3u8") {
		t.Fatalf("unexpected play url: %#v", payload["url"])
	}
}

func TestVideoDBManifestRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			w.Header().Set("Location", "/stream.m3u8")
			w.WriteHeader(http.StatusFound)
		case "/stream.m3u8":
			_, _ = w.Write([]byte("#EXTM3U"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			VideoDB: config.VideoDBSource{Host: target.URL, APIHost: target.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/videodb/manifest.m3u8?link="+urlQueryEscape(target.URL+"/redirect"),
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); !strings.Contains(got, "/stream.m3u8") {
		t.Fatalf("unexpected location: %s", got)
	}
}

func videodbTestHTML(decodedJSON string) string {
	prefix := strings.Repeat("A", 73)
	enc := base64.StdEncoding.EncodeToString([]byte(decodedJSON))
	return `<script>new Player("` + prefix + enc + `");</script>`
}

func urlQueryEscape(v string) string {
	return strings.ReplaceAll(url.QueryEscape(v), "+", "%20")
}
