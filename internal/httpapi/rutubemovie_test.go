package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestRutubeMovieChecksearchNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search/video/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{
			"results": [{
				"title":"Inception (2010) full movie",
				"duration": 7200,
				"is_hidden": false,
				"is_deleted": false,
				"is_adult": false,
				"is_locked": false,
				"is_audio": false,
				"is_paid": false,
				"is_livestream": false,
				"category": {"id": 4}
			}]
		}`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			RutubeMovie: config.HostSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/rutubemovie?checksearch=true&title=Inception&year=2010&serial=0", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestRutubeMovieChecksearchSerialRejected(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{
			RutubeMovie: config.HostSource{Host: "https://rutube.ru"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/rutubemovie?checksearch=true&title=Inception&year=2010&serial=1", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("did not expect movie marker, got: %s", rec.Body.String())
	}
}
