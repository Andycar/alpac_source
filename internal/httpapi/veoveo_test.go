package httpapi

import (
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestVeoVeoChecksearchNative(t *testing.T) {
	dataFile, err := os.CreateTemp(t.TempDir(), "veoveo-*.json.gz")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	defer dataFile.Close()

	zw := gzip.NewWriter(dataFile)
	_, _ = zw.Write([]byte(`[{"id":1,"year":2010,"kinopoiskId":447301,"imdbId":"tt1375666","originalTitle":"Inception","title":"Начало"}]`))
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close failed: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/balancer-api/proxy/playlists/catalog-api/episodes" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("content-id") != "1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[{"order":1}]`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			VeoVeo: config.VeoVeoSource{Host: upstream.URL, DataPath: dataFile.Name()},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/veoveo?checksearch=true&kinopoisk_id=447301&title=Начало&original_title=Inception", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

// TestVeoVeoResolvesMovieIDViaIframe covers the online fallback: with no local
// catalog DB, the balancer resolves a Kinopoisk ID to the internal movieID by
// reading window.MOVIE_ID from the iframe bootstrap, then fetches episodes.
func TestVeoVeoResolvesMovieIDViaIframe(t *testing.T) {
	const kp = "5698586"
	const movieID = "162545"
	const token = "test-affiliate-jwt"

	var iframeHits, episodeHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/balancer-api/iframe":
			iframeHits++
			// Resolution must carry the affiliate token, else the iframe is empty.
			if r.URL.Query().Get("token") != token || r.URL.Query().Get("kp") != kp {
				_, _ = w.Write([]byte(`<html><body>missing movie_id</body></html>`))
				return
			}
			_, _ = w.Write([]byte(`<script>window.MOVIE_ID=` + movieID + `;</script>`))
		case "/balancer-api/proxy/playlists/catalog-api/episodes":
			episodeHits++
			if r.URL.Query().Get("content-id") != movieID {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`[{"order":1,"season":{"order":0},"episodeVariants":[{"filepath":"https://global.temptcdn.com/content-router/r/movies/files/episodes/91270_91270/91270_91270__master.m3u8"}]}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			// No local DB: point DataPath at a non-existent file so kp resolution
			// must go through the iframe.
			VeoVeo: config.VeoVeoSource{Host: upstream.URL, Token: token, DataPath: filepath.Join(t.TempDir(), "missing.json")},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/veoveo?rjson=true&kinopoisk_id="+kp+"&title=Паук-Нуар", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `91270_91270__master.m3u8`) {
		t.Fatalf("expected resolved stream URL in body, got: %s", rec.Body.String())
	}
	if iframeHits == 0 {
		t.Fatalf("expected iframe to be queried for kp->movieID resolution")
	}
	if episodeHits == 0 {
		t.Fatalf("expected episodes endpoint to be queried")
	}
}

// TestVeoVeoSerialVoiceSelection verifies that a serial with multiple dub
// variants exposes a voice picker and binds the episode streams to the active
// dub selected via &t=.
func TestVeoVeoSerialVoiceSelection(t *testing.T) {
	const episodesJSON = `[
		{"order":1,"title":"E1","season":{"order":1},"episodeVariants":[
			{"title":"Dubbing One","filepath":"https://cdn.example/one/e1.m3u8"},
			{"title":"Dubbing Two","filepath":"https://cdn.example/two/e1.m3u8"}]},
		{"order":2,"title":"E2","season":{"order":1},"episodeVariants":[
			{"title":"Dubbing One","filepath":"https://cdn.example/one/e2.m3u8"},
			{"title":"Dubbing Two","filepath":"https://cdn.example/two/e2.m3u8"}]}
	]`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/balancer-api/proxy/playlists/catalog-api/episodes" && r.URL.Query().Get("content-id") == "999" {
			_, _ = w.Write([]byte(episodesJSON))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			VeoVeo: config.VeoVeoSource{Host: upstream.URL, DataPath: filepath.Join(t.TempDir(), "missing.json")},
		},
	}

	do := func(query string) string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/veoveo?"+query, nil)
		authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("query %q: status %d", query, rec.Code)
		}
		return rec.Body.String()
	}

	// Default season view (no &t): picker present, first dub active.
	body := do("rjson=true&movieid=999&s=1&title=Show")
	if !strings.Contains(body, `"voice"`) {
		t.Fatalf("expected voice picker, got: %s", body)
	}
	if !strings.Contains(body, "Dubbing One") || !strings.Contains(body, "Dubbing Two") {
		t.Fatalf("expected both dub names in picker, got: %s", body)
	}
	if !strings.Contains(body, "one/e1.m3u8") || !strings.Contains(body, "one/e2.m3u8") {
		t.Fatalf("expected default-dub (One) episode streams, got: %s", body)
	}
	if strings.Contains(body, "two/e1.m3u8") {
		t.Fatalf("default view must not serve the inactive dub stream, got: %s", body)
	}

	// Switch to the second dub via &t=1.
	body = do("rjson=true&movieid=999&s=1&t=1&title=Show")
	if !strings.Contains(body, "two/e1.m3u8") || !strings.Contains(body, "two/e2.m3u8") {
		t.Fatalf("expected second-dub (Two) episode streams for t=1, got: %s", body)
	}
	if strings.Contains(body, "one/e1.m3u8") {
		t.Fatalf("t=1 must not serve the first dub stream, got: %s", body)
	}
}

// TestVeoVeoChecksearchQualityBadge verifies the checksearch badge is the max
// resolution across the title's dub variants (720p + 1080p -> FHD), exercising
// the content-router 307 -> edge redirect follow for each variant.
func TestVeoVeoChecksearchQualityBadge(t *testing.T) {
	const master720 = "#EXTM3U\n#EXT-X-STREAM-INF:RESOLUTION=1280x720\nv.m3u8\n#EXT-X-STREAM-INF:RESOLUTION=854x480\nw.m3u8\n"
	const master1080 = "#EXTM3U\n#EXT-X-STREAM-INF:RESOLUTION=1920x1080\nv.m3u8\n"

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/balancer-api/proxy/playlists/catalog-api/episodes":
			if r.URL.Query().Get("content-id") != "777" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// Two dubs at different resolutions: 720p and 1080p.
			_, _ = fmt.Fprintf(w, `[{"order":0,"season":{"order":0},"episodeVariants":[`+
				`{"title":"Dub A","filepath":"%s/cr/a.m3u8"},`+
				`{"title":"Dub B","filepath":"%s/cr/b.m3u8"}]}]`, ts.URL, ts.URL)
		case "/cr/a.m3u8":
			http.Redirect(w, r, ts.URL+"/edge/a.m3u8", http.StatusTemporaryRedirect)
		case "/cr/b.m3u8":
			http.Redirect(w, r, ts.URL+"/edge/b.m3u8", http.StatusTemporaryRedirect)
		case "/edge/a.m3u8":
			_, _ = w.Write([]byte(master720))
		case "/edge/b.m3u8":
			_, _ = w.Write([]byte(master1080))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			VeoVeo: config.VeoVeoSource{Host: ts.URL, DataPath: filepath.Join(t.TempDir(), "missing.json")},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/veoveo?checksearch=true&movieid=777", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"quality":"FHD"`) {
		t.Fatalf("expected FHD (max across 720p+1080p dubs), got: %s", body)
	}
}

// TestVeoVeoLiveResolution exercises the real production client against the live
// temptcdn host. Guarded by VEOVEO_LIVE=1 so it never runs in normal CI.
//
//	VEOVEO_LIVE=1 go test ./internal/httpapi/ -run TestVeoVeoLiveResolution -v
