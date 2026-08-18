package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestHDVBChecksearchNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/videos.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("token") != "hdvb-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Query().Get("id_kp") != "447301" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			HDVB: config.APIHostTokenSource{APIHost: upstream.URL, Token: "hdvb-token"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/hdvb?checksearch=true&kinopoisk_id=447301", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}
