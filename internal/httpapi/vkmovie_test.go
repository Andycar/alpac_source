package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
)

func TestVKMovieChecksearchNative(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		expires := time.Now().UTC().Add(24 * time.Hour).Unix()
		_, _ = w.Write([]byte(`{"data":{"access_token":"vk-token","expires":` + strconv.FormatInt(expires, 10) + `}}`))
	})
	mux.HandleFunc("/method/catalog.getVideoSearchWeb2", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		bodyS := string(body)
		if !strings.Contains(bodyS, "access_token=vk-token") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{
			"response": {
				"catalog_videos": [{
					"video": {
						"title":"Inception 2010 Full Movie",
						"duration": 7200,
						"files": {"mp4_1080":"https://cdn.example/1080.mp4"}
					}
				}]
			}
		}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			VKMovie: config.VKMovieSource{Host: srv.URL, TokenURL: srv.URL + "/token"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/vkmovie?checksearch=true&title=Inception&year=2010&serial=0", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestVKMovieChecksearchSerialRejected(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{
			VKMovie: config.VKMovieSource{Host: "https://api.vkvideo.ru", TokenURL: "https://login.vk.com/?act=get_anonym_token"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/vkmovie?checksearch=true&title=Inception&year=2010&serial=1", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("did not expect movie marker, got: %s", rec.Body.String())
	}
}
