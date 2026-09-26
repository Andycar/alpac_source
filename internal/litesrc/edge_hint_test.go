package litesrc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func withEdgeURL(t *testing.T, edge string) {
	t.Helper()
	old := deps
	d := deps
	d.LiveConfig = func(fallback config.Config) config.Config {
		fallback.Cluster.EdgeURL = edge
		return fallback
	}
	deps = d
	t.Cleanup(func() { deps = old })
}

// Подсказка есть только у сервера с клиентским адресом; без него ссылки как прежде.
func TestLiteEdgeHint(t *testing.T) {
	withEdgeURL(t, "")
	if h := liteEdgeHint(); h != "" {
		t.Fatalf("без edge_url ждали пусто, получили %q", h)
	}
	withEdgeURL(t, " https://torr.example.com ")
	if h := liteEdgeHint(); h != "&edge=https%3A%2F%2Ftorr.example.com" {
		t.Fatalf("подсказка: %q", h)
	}
}

// Ссылки на манифест obrut у zetflix и videodb несут edge= добывшей ноды: без него
// манифест уходил на любую edge-ноду, резолв там давал 404 и минтился мёртвый токен.
func TestObrutManifestURLsCarryEdgeHint(t *testing.T) {
	withEdgeURL(t, "https://torr.example.com")
	links := map[string]string{"1080p": "https://cdn-54243ba5.obrut.show/stream/abc?e=1", "720p": "https://cdn-54243ba5.obrut.show/stream/def?e=1"}

	req := httptest.NewRequest(http.MethodGet, "http://tv.example.com/lite/zetflix/manifest?links=x", nil)
	rec := httptest.NewRecorder()
	(&zetflixChecker{}).manifestMulti(rec, req, nil, links, false)
	body := rec.Body.String()
	if strings.Count(body, "edge=https%3A%2F%2Ftorr.example.com") < 2 || !strings.Contains(body, "/lite/zetflix/manifest.mp4?link=") {
		t.Fatalf("zetflix: ссылки без подсказки: %s", body)
	}

	req = httptest.NewRequest(http.MethodGet, "http://tv.example.com/lite/videodb/manifest?links=x", nil)
	rec = httptest.NewRecorder()
	(&videodbChecker{plugin: "videodb"}).manifestMulti(rec, req, links, false)
	body = rec.Body.String()
	if strings.Count(body, "edge=https%3A%2F%2Ftorr.example.com") < 2 || !strings.Contains(body, "/lite/videodb/manifest.mp4?link=") {
		t.Fatalf("videodb: ссылки без подсказки: %s", body)
	}

	// Без клиентского адреса (main) — ссылки прежние, без хвоста.
	withEdgeURL(t, "")
	rec = httptest.NewRecorder()
	(&zetflixChecker{}).manifestMulti(rec, req, nil, links, false)
	if strings.Contains(rec.Body.String(), "edge=") {
		t.Fatal("без edge_url подсказки быть не должно")
	}
}
