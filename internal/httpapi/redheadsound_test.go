package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestRedheadsoundChecksearchNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "":
			_, _ = w.Write([]byte(`<html><script>var dle_login_hash = 'abcdef1234';</script></html>`))
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_, _ = w.Write([]byte(`<html><script>var dle_login_hash = 'abcdef1234';</script></html>`))
		case r.Method == http.MethodPost && r.URL.Path == "/engine/ajax/controller.php":
			_, _ = w.Write([]byte(`
<div class="move-item">
  <h4 class="title"><a href="https://example/movie">Inception</a></h4>
  <span class="year"><a>2010</a></span>
</div>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Redheadsound: config.RedheadsoundSource{Host: upstream.URL, SocksProxy: "none"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/redheadsound?checksearch=true&title=Inception&year=2010", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestRedheadsoundMovieRjson(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_, _ = w.Write([]byte(`<html><script>var dle_login_hash = 'abcdef1234';</script></html>`))
		case r.Method == http.MethodPost && r.URL.Path == "/engine/ajax/controller.php":
			_, _ = w.Write([]byte(`
<div class="move-item">
  <a class="move-item__img" href="/movie/inception"></a>
  <h4 class="title"><a href="#">Inception</a></h4>
  <span class="year"><a>2010</a></span>
</div>`))
		case r.Method == http.MethodGet && r.URL.Path == "/movie/inception":
			_, _ = w.Write([]byte(`<script>var videoUrl = '` + upstream.URL + `/iframe/inception';</script>`))
		case r.Method == http.MethodGet && r.URL.Path == "/iframe/inception":
			_, _ = w.Write([]byte(`{"contentUrl":"https://cdn.example/film/master.m3u8"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Redheadsound: config.RedheadsoundSource{Host: upstream.URL, SocksProxy: "none"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/redheadsound?rjson=true&title=Inception&year=2010", nil)
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
	if toString(row["url"]) != "https://cdn.example/film/master.m3u8" {
		t.Fatalf("unexpected stream url: %#v", row["url"])
	}
}
