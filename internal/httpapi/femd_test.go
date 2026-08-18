package httpapi

import (
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// Realistic excerpt from api.femd.ws/embed/kp/{kp} — keep mirroring the
// upstream layout: makePlayer({ source: { hls, cc } }) inside a script block.
func femdSampleEmbedHTML(hlsURL string) string {
	return `<!DOCTYPE html><html><head><title>Горничная</title></head><body>
<script data-name="mk">
makePlayer({
    blocked: false,
    title: "Горничная",
    id: 830090,
    poster: 'data:image/gif;base64,xx',
    source: {
        dash: "https://cdnr.interkh.com/A/1238040.mpd?t=123",
        hls: "` + hlsURL + `",
        audio: {"names":["Рус. Дублированный","Eng.Original"],"order":[0,1]},
        cc: [{"url":"https://hye1eaipby4w.interkh.com/cc/eng.vtt?t=123","name":"Eng. full - 1"},{"url":"https://hye1eaipby4w.interkh.com/cc/rus.vtt?t=123","name":"Рус. полные - 5"}]
    },
    sections: []
});
</script>
</body></html>`
}

func TestFemdIndexPageMovieRjson(t *testing.T) {
	hlsURL := "https://cdnr.example/test/master.m3u8?ha=1&t=999"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/embed/kp/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(femdSampleEmbedHTML(hlsURL)))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Femd: config.FemdSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	url := fmt.Sprintf("http://lampac.local/lite/femd?rjson=true&kinopoisk_id=86126&title=%s&original_title=The+Housemaid",
		"%D0%93%D0%BE%D1%80%D0%BD%D0%B8%D1%87%D0%BD%D0%B0%D1%8F") // Горничная url-encoded
	req := httptest.NewRequest(http.MethodGet, url, nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d, body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v, body=%s", err, rec.Body.String())
	}
	if payload["type"] != "movie" {
		t.Fatalf("type: want movie, got %#v", payload["type"])
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("data: %#v", payload["data"])
	}
	row := data[0].(map[string]any)
	if row["method"] != "play" {
		t.Fatalf("method: want play, got %#v", row["method"])
	}
	// Without proxylink Manager, streamProxyURL returns the raw HLS URL.
	if got, _ := row["stream"].(string); got != hlsURL {
		t.Fatalf("stream: want %s, got %v", hlsURL, row["stream"])
	}
	subs, ok := row["subtitles"].([]any)
	if !ok || len(subs) != 2 {
		t.Fatalf("subtitles: want 2, got %#v", row["subtitles"])
	}
}

func TestFemdIndexPageEmptyForMissing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stub: HTTP 200, no hls, generic title — femd-style 404.
		_, _ = w.Write([]byte(`<html><head><title>Title</title></head><body>nope</body></html>`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Femd: config.FemdSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/femd?rjson=true&kinopoisk_id=613844", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body=%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("expected empty {}, got: %s", rec.Body.String())
	}
}

func TestFemdIndexPageEmptyWithoutKP(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{Femd: config.FemdSource{Host: "https://api.femd.ws"}},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/femd?rjson=true&title=Foo", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("expected empty {}, got: %s", rec.Body.String())
	}
}
