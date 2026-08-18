package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestPlvideoChecksearchNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/videos" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{
			"items": [{
				"id":"1",
				"title":"Inception 2010",
				"visible":"public",
				"uploadFile":{"videoDuration": 2000000}
			}]
		}`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Plvideo: config.HostSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/plvideo?checksearch=true&title=Inception&year=2010&serial=0", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestPlvideoChecksearchSerialRejected(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{
			Plvideo: config.HostSource{Host: "https://api.g1.plvideo.ru"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/plvideo?checksearch=true&title=Inception&year=2010&serial=1", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("did not expect movie marker, got: %s", rec.Body.String())
	}
}
