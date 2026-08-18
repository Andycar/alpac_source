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

// videoseedEncode mirrors the litesrc original (test fixture builder).
func videoseedEncode(v string) string {
	return url.QueryEscape(v)
}

func TestVideoseedChecksearch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/apiv2.php") {
			_, _ = w.Write([]byte(`{"status":"ok","data":[{"iframe":"https://player.videoseed.net/embed/abc"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Videoseed: config.HostTokenSource{Host: upstream.URL, Token: "token123"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/videoseed?checksearch=true&title=Inception", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", rec.Body.String())
	}
}

func TestVideoseedChecksearchNoToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`ok`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Videoseed: config.HostTokenSource{Host: upstream.URL, Token: ""},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/videoseed?checksearch=true&title=Inception", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected show=false payload without token, got: %s", rec.Body.String())
	}
}

func TestVideoseedMovieRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/apiv2.php") {
			_, _ = w.Write([]byte(`{"status":"ok","data":[{"iframe":"https://player.videoseed.net/embed/abc"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Videoseed: config.HostTokenSource{Host: upstream.URL, Token: "token123"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/videoseed?rjson=true&kinopoisk_id=123&title=Film&original_title=Film+Orig",
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
	if !ok || len(data) != 1 {
		t.Fatalf("unexpected data: %#v", payload["data"])
	}
}

func TestVideoseedSerialRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/apiv2.php") {
			_, _ = w.Write([]byte(`{"status":"ok","data":[{"seasons":{"1":{"videos":{"1":{"iframe":"https://player.videoseed.net/embed/s1e1"},"2":{"iframe":"https://player.videoseed.net/embed/s1e2"}}}}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Videoseed: config.HostTokenSource{Host: upstream.URL, Token: "token123"},
		},
	}

	recSeason := httptest.NewRecorder()
	reqSeason := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/videoseed?rjson=true&serial=1&kinopoisk_id=555&title=Serial&original_title=Serial+Orig",
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
		"http://lampac.local/lite/videoseed?rjson=true&serial=1&kinopoisk_id=555&title=Serial&original_title=Serial+Orig&s=1",
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
}

func TestVideoseedVideoEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/embed/abc" {
			_, _ = w.Write([]byte(`<html><body>var x="https://cdn.example/video/master.m3u8";</body></html>`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Videoseed: config.HostTokenSource{Host: upstream.URL, Token: "token123"},
		},
	}

	enc := url.QueryEscape(videoseedEncode(upstream.URL + "/embed/abc"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/videoseed/video/"+enc, nil)
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
	if !strings.Contains(toString(payload["url"]), ".m3u8") {
		t.Fatalf("unexpected play url: %#v", payload["url"])
	}
}

func TestVideoseedVideoEndpointEscapedURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/embed/js" {
			_, _ = w.Write([]byte(`<script>var src="https:\\/\\/cdn.example\\/video\\/master.m3u8\\u0026token=abc";</script>`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Videoseed: config.HostTokenSource{Host: upstream.URL, Token: "token123"},
		},
	}

	enc := url.QueryEscape(videoseedEncode(upstream.URL + "/embed/js"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/videoseed/video/"+enc, nil)
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
	got := toString(payload["url"])
	if !strings.Contains(got, "https://cdn.example/video/master.m3u8&token=abc") {
		t.Fatalf("unexpected normalized play url: %#v", payload["url"])
	}
}
