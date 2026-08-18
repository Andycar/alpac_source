package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestAllohaChecksearchByKinopoiskNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// v2 API: Bearer auth + /v2/movies/{kp|imdb|token}/{id}.
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v2/movies/kp/123" {
			_, _ = w.Write([]byte(`{"data":{"category":{"slug":"movie","name":"Фильм"},"token":"tm123"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			// LinkHost also points at the mock so the guard client doesn't reach
			// out to the real CDN during the test.
			Alloha: config.AllohaSource{APIHost: upstream.URL, LinkHost: upstream.URL, Token: "tok"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/alloha?checksearch=true&kinopoisk_id=123", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestAllohaChecksearchByTMDBNative(t *testing.T) {
	// The /capi card is TMDB-keyed and often has NO kinopoisk_id (the TMDB→KP
	// resolver misses). Alloha must still resolve via /v2/movies/tmdb/{id}.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v2/movies/tmdb/459151" {
			_, _ = w.Write([]byte(`{"data":{"category":{"slug":"movie","name":"Фильм"},"token":"tm459151"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Alloha: config.AllohaSource{APIHost: upstream.URL, LinkHost: upstream.URL, Token: "tok"},
		},
	}

	rec := httptest.NewRecorder()
	// Note: only "id" (TMDB) is present — no kinopoisk_id, no imdb_id.
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/alloha?checksearch=true&id=459151", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker from TMDB lookup, got: %s", rec.Body.String())
	}
}

func TestAllohaCapiDeferredMovie(t *testing.T) {
	// Under /capi resolve, a movie must return DEFERRED play-nodes: voice +
	// quality object pointing at /lite/alloha/stream.m3u8 — no browser resolve
	// during aggregation. The stream resolves on first playback (streamHLS).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v2/movies/tmdb/459151" {
			_, _ = w.Write([]byte(`{"data":{"category":{"slug":"movie","name":"Фильм"},"token":"tm459151","translations":[{"id":66,"name":"Дублированный","quality":"BDRip","resolutions":[2160,1080,720]}]}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Alloha: config.AllohaSource{APIHost: upstream.URL, LinkHost: upstream.URL, Token: "tok"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/alloha?rjson=true&id=459151", nil)
	req = req.WithContext(capiWithResolve(req.Context()))
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"Дублированный"`) {
		t.Fatalf("expected voice name, got: %s", body)
	}
	if !strings.Contains(body, `/lite/alloha/stream.m3u8`) {
		t.Fatalf("expected deferred stream URL, got: %s", body)
	}
	if !strings.Contains(body, `"2160p"`) {
		t.Fatalf("expected 2160p quality advertised, got: %s", body)
	}
	// It must NOT emit the browser-resolve call-node (that would block aggregation).
	if strings.Contains(body, `/lite/alloha/video`) {
		t.Fatalf("capi movie should be deferred, not a video() call-node: %s", body)
	}
}

func TestAllohaSearchByTitleNoYear(t *testing.T) {
	// /capi frequently passes a title with NO year (kp/tmdb resolution missed).
	// Alloha must still find the card by title — requiring year>0 made it bail
	// instantly (0 voices) while filmix found the same card. Uses checksearch,
	// which reaches lookupByTitle.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// /v2/movies/name/list?name=… — no year param expected.
		if strings.HasPrefix(r.URL.Path, "/v2/movies/name") {
			if r.URL.Query().Get("year") != "" {
				t.Errorf("year must NOT be sent when absent, got %q", r.URL.Query().Get("year"))
			}
			_, _ = w.Write([]byte(`{"data":[{"name":"Матрица","year":1999,"category":{"slug":"movie"},"token":"tmX","translations":[{"id":66,"name":"Дубляж","quality":"BDRip"}]}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Alloha: config.AllohaSource{APIHost: upstream.URL, LinkHost: upstream.URL, Token: "tok"},
		},
	}

	rec := httptest.NewRecorder()
	// title but NO year and NO ids — the /capi-without-year case.
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/alloha?checksearch=true&title=%D0%9C%D0%B0%D1%82%D1%80%D0%B8%D1%86%D0%B0", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"show":true`) && !strings.Contains(rec.Body.String(), `"rch"`) {
		t.Fatalf("expected a positive checksearch from title-only lookup, got: %s", rec.Body.String())
	}
}

func TestAllohaCapiDeferredSerial(t *testing.T) {
	// Under /capi with a concrete season+episode, a serial must return DEFERRED
	// play-nodes: one per voice available for THAT episode, each with a
	// /lite/alloha/stream.m3u8 quality object — no Voice array (so capi reads them
	// directly, not via per-dub re-drill) and no browser resolve during aggregation.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v2/movies/tmdb/1399" {
			_, _ = w.Write([]byte(`{"data":{"category":{"slug":"serial","name":"Сериал"},"token":"tmS","seasons":[{"season":1,"episodes":[{"episode":1,"translations":[{"id":66,"name":"Дублированный","quality":"WEB-DL","resolutions":[1080,720]},{"id":93,"name":"Оригинальный","quality":"WEB-DL"}]}]}]}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Alloha: config.AllohaSource{APIHost: upstream.URL, LinkHost: upstream.URL, Token: "tok"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/alloha?rjson=true&id=1399&serial=1&s=1&e=1", nil)
	req = req.WithContext(capiWithResolve(req.Context()))
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"Дублированный"`) || !strings.Contains(body, `"Оригинальный"`) {
		t.Fatalf("expected both voices for the episode, got: %s", body)
	}
	if !strings.Contains(body, `/lite/alloha/stream.m3u8`) || !strings.Contains(body, `s=1`) || !strings.Contains(body, `e=1`) {
		t.Fatalf("expected deferred per-episode stream URLs, got: %s", body)
	}
	if strings.Contains(body, `"voice"`) || strings.Contains(body, `/lite/alloha/video`) {
		t.Fatalf("capi serial must be deferred play-nodes, not Voice-array/video() nodes: %s", body)
	}
}

// TestAllohaResolveLive drives the real headless-Chrome resolve against the live
// Phantom CDN (Matrix, kp=301). Guarded by ALLOHA_LIVE=1 — needs Chrome + network.
//
//	ALLOHA_LIVE=1 go test ./internal/httpapi/ -run TestAllohaResolveLive -v
