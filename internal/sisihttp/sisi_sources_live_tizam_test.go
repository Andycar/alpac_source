package sisihttp

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestSisiTizamListParsesItems(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<div class="video-item">
				<div data-name="name">Premium hidden</div>
				<div class="pin--premium"></div>
			</div>
			<div class="video-item">
				<div data-name="name">Tizam Title</div>
				<a href="/movie/abc-1" itemprop="url"></a>
				<img class="item__img" data-srcset="//img.cdn/abc.jpg 1x, //img.cdn/abc@2x.jpg 2x" />
				<meta itemprop="duration" content="12:34" />
			</div>
			<div id="pagination"></div>
			<div class="video-item">
				<div data-name="name">Must be ignored after pagination</div>
			</div>
		`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_TIZAM_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiTizamSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/tizam?pg=2", nil)
	rec := httptest.NewRecorder()
	source.listHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	list, _ := payload["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 item, got %d, body: %s", len(list), rec.Body.String())
	}
	item, _ := list[0].(map[string]any)
	if !strings.Contains(toString(item["video"]), "/tizam/vidosik?uri=movie%2Fabc-1") {
		t.Fatalf("unexpected video link: %v", item["video"])
	}
	if toString(item["picture"]) != "http://lampac.local/proxy/https%3A%2F%2Fimg.cdn%2Fabc.jpg" {
		t.Fatalf("unexpected picture: %v", item["picture"])
	}
	if toString(item["time"]) != "12:34" {
		t.Fatalf("unexpected time: %v", item["time"])
	}
}

func TestSisiTizamViewParsesAutoQuality(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<video><source src="https://cdn.example/tizam.mp4" type="video/mp4"></video>`))
	}))
	defer upstream.Close()

	t.Setenv("LAMPAC_GO_SISI_TIZAM_HOST", upstream.URL)

	cfg := config.Config{}
	source := newSisiTizamSource(cfg)

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/tizam/vidosik?uri=movie/abc-1", nil)
	rec := httptest.NewRecorder()
	source.viewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	qualitys, _ := payload["qualitys"].(map[string]any)
	if toString(qualitys["auto"]) != "https://cdn.example/tizam.mp4" {
		t.Fatalf("unexpected auto quality: %v", qualitys["auto"])
	}
}

func TestNormalizeTizamPicture(t *testing.T) {
	if got := normalizeTizamPicture("https://in.tizam.info", "/img/a.jpg"); got != "https://in.tizam.info/img/a.jpg" {
		t.Fatalf("relative path: got %q", got)
	}
	if got := normalizeTizamPicture("https://in.tizam.info", "//cdn.tizam.info/a.jpg"); got != "https://cdn.tizam.info/a.jpg" {
		t.Fatalf("protocol-relative path: got %q", got)
	}
}
