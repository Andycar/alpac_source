package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestIframeVideoChecksearch(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v2/search"):
			_, _ = w.Write([]byte(`{"results":[{"cid":101,"path":"` + upstream.URL + `/ru/token101/iframe","type":"movie"}]}`))
		case r.URL.Path == "/ru/token101/iframe":
			_, _ = w.Write([]byte(`<a href='/ru/token101/iframe'><span>Voice A</span></a>`))
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			IframeVideo: config.IframeVideoSource{APIHost: upstream.URL, CDNHost: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/iframevideo?checksearch=true&imdb_id=tt1375666", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", rec.Body.String())
	}
}

func TestIframeVideoMovieRjson(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v2/search"):
			_, _ = w.Write([]byte(`{"results":[{"cid":202,"path":"` + upstream.URL + `/ru/token202/iframe","type":"movie"}]}`))
		case r.URL.Path == "/ru/token202/iframe":
			_, _ = w.Write([]byte(`<a href='/ru/tokenA/iframe'><span>Dub A</span></a><a href='/ru/tokenB/iframe'><span>Dub B</span></a>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			IframeVideo: config.IframeVideoSource{APIHost: upstream.URL, CDNHost: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/iframevideo?rjson=true&imdb_id=tt1375666&title=Film&original_title=Film+Orig",
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
	if !ok || len(data) != 2 {
		t.Fatalf("unexpected data: %#v", payload["data"])
	}
	row, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected row: %#v", data[0])
	}
	if !strings.Contains(toString(row["url"]), "/lite/iframevideo/video?") {
		t.Fatalf("unexpected call url: %#v", row["url"])
	}
	if !strings.Contains(toString(row["stream"]), "/lite/iframevideo/video.m3u8?") {
		t.Fatalf("unexpected stream url: %#v", row["stream"])
	}
}

func TestIframeVideoVideoJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/loadvideo" {
			_, _ = w.Write([]byte(`{"src":"https://cdn.example/master.m3u8"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			IframeVideo: config.IframeVideoSource{APIHost: upstream.URL, CDNHost: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/iframevideo/video?type=movie&cid=303&token=tok303&title=Film&original_title=Film+Orig",
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
	if got := toString(payload["url"]); got != "https://cdn.example/master.m3u8" {
		t.Fatalf("unexpected play url: %s", got)
	}
}

func TestIframeVideoVideoRedirect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/loadvideo" {
			_, _ = w.Write([]byte(`{"src":"https://cdn.example/master.m3u8"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			IframeVideo: config.IframeVideoSource{APIHost: upstream.URL, CDNHost: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/iframevideo/video.m3u8?type=movie&cid=404&token=tok404&play=true",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "https://cdn.example/master.m3u8" {
		t.Fatalf("unexpected location: %s", got)
	}
}
