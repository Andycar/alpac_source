package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestFXAPIChecksearchNativeFromFilmix(t *testing.T) {
	filmixUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/search" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("user_dev_token") != "fx-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`[{"id":42,"title":"Inception","original_title":"Inception","year":2010}]`))
	}))
	defer filmixUpstream.Close()

	filmixTVUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer filmixTVUpstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Filmix:        config.FilmixSource{Host: filmixUpstream.URL},
			FilmixTV:      config.HostSource{Host: filmixTVUpstream.URL},
			FilmixPartner: config.TokenSource{Token: "fx-token"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/fxapi?checksearch=true&title=Inception&original_title=Inception&year=2010", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "data-json=") {
		t.Fatalf("expected data-json marker, got: %s", rec.Body.String())
	}
}

func TestFXAPIChecksearchFallbackToFilmixTV(t *testing.T) {
	filmixUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer filmixUpstream.Close()

	filmixTVUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api-fx/list" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":7,"title":"Interstellar","original_title":"Interstellar","year":2014}]}`))
	}))
	defer filmixTVUpstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Filmix:        config.FilmixSource{Host: filmixUpstream.URL},
			FilmixTV:      config.HostSource{Host: filmixTVUpstream.URL},
			FilmixPartner: config.TokenSource{Token: "fx-token"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/fxapi?checksearch=true&title=Interstellar&original_title=Interstellar&year=2014", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "data-json=") {
		t.Fatalf("expected data-json marker, got: %s", rec.Body.String())
	}
}
