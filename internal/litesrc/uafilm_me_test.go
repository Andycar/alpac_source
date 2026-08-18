package litesrc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// uafilmTestServer serves the recorded UAFilm /api/v1 JSON shapes so the handler
// can be exercised end-to-end without touching the live site.
func uafilmTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// search → one movie (matches tmdb 986056) + one unrelated.
	mux.HandleFunc("/api/v1/search/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"id":507,"name":"Громовержці","original_title":"Thunderbolts*","year":2025,"tmdb_id":986056,"imdb_id":"tt20969586","poster":"https://img/p.jpg","type":"movie"},
			{"id":9554,"name":"The Wednesday Play","original_title":"The Wednesday Play","year":1964,"tmdb_id":11303,"poster":"https://img/q.jpg","type":"movie"}
		]}`))
	})
	// movie title 507 → ashdi stream + a tmdb trailer that must be skipped.
	mux.HandleFunc("/api/v1/titles/507", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":{"is_series":false,"seasons_count":0,"videos":[
			{"name":"Трейлер","origin":"tmdb","type":"embed","src":"https://youtube.com/embed/x"},
			{"name":"Український Дубляж","origin":"ashdi","type":"stream","src":"https://uafilm.me/video/movie/index.m3u8"}
		]}}`))
	})
	// series title 124 → is_series with 2 seasons.
	mux.HandleFunc("/api/v1/titles/124", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/seasons/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"episodes":{"data":[
				{"season_number":1,"episode_number":1,"primary_video":{"id":1421,"name":"Серія 1"}},
				{"season_number":1,"episode_number":2,"primary_video":{"id":1425,"name":"Серія 2"}},
				{"season_number":2,"episode_number":1,"primary_video":{"id":1500,"name":"S2"}}
			]}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":{"is_series":true,"seasons_count":2,"videos":[]}}`))
	})
	mux.HandleFunc("/api/v1/watch/1421", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"video":{"src":"https://uafilm.me/video/ep1/index.m3u8","origin":"ashdi","type":"stream"}}`))
	})

	// series title 124 seasons path (chi-free mux needs an explicit prefix match).
	mux.HandleFunc("/api/v1/titles/124/seasons/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"episodes":{"data":[
			{"season_number":1,"episode_number":1,"primary_video":{"id":1421,"name":"Серія 1"}},
			{"season_number":1,"episode_number":2,"primary_video":{"id":1425,"name":"Серія 2"}}
		]}}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newUAFilmTestChecker(srv *httptest.Server) *uafilmChecker {
	return &uafilmChecker{client: srv.Client(), host: srv.URL}
}

func uafilmDo(t *testing.T, u *uafilmChecker, rawQuery string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/lite/uafilm?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	u.Handle(config.Config{}, nil)(rec, req)
	return rec.Code, rec.Body.String()
}

func TestUAFilmMovieFlow(t *testing.T) {
	srv := uafilmTestServer(t)
	u := newUAFilmTestChecker(srv)

	// id (tmdb) + title/year → search matches 507 → movie videos.
	code, body := uafilmDo(t, u, "id=986056&title=Thunderbolts&original_title=Thunderbolts&year=2025&rjson=true")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "uafilm.me/video/movie/index.m3u8") {
		t.Errorf("movie HLS missing:\n%s", body)
	}
	if strings.Contains(body, "youtube.com/embed") {
		t.Errorf("trailer (tmdb origin) must be filtered:\n%s", body)
	}
	if !strings.Contains(body, "Український Дубляж") {
		t.Errorf("voice label missing:\n%s", body)
	}
}

func TestUAFilmSeriesSeasonsAndEpisodes(t *testing.T) {
	srv := uafilmTestServer(t)
	u := newUAFilmTestChecker(srv)

	// orid set, s=-1 → seasons list.
	code, body := uafilmDo(t, u, "orid=124&title=Wednesday&original_title=Wednesday&rjson=true")
	if code != http.StatusOK {
		t.Fatalf("seasons status %d", code)
	}
	if !strings.Contains(body, `"season"`) || !strings.Contains(body, "1 сезон") || !strings.Contains(body, "2 сезон") {
		t.Errorf("seasons wrong:\n%s", body)
	}

	// orid + s=1 → episodes with call-method video links.
	code, body = uafilmDo(t, u, "orid=124&title=Wednesday&original_title=Wednesday&s=1&rjson=true")
	if code != http.StatusOK {
		t.Fatalf("episodes status %d", code)
	}
	if !strings.Contains(body, `"episode"`) || !strings.Contains(body, "/lite/uafilm/video?vid=1421") {
		t.Errorf("episodes wrong:\n%s", body)
	}
	if !strings.Contains(body, `"call"`) {
		t.Errorf("episode method must be call:\n%s", body)
	}
}

func TestUAFilmVideoResolve(t *testing.T) {
	srv := uafilmTestServer(t)
	u := newUAFilmTestChecker(srv)

	req := httptest.NewRequest(http.MethodGet, "/lite/uafilm/video?vid=1421", nil)
	rec := httptest.NewRecorder()
	u.Handle(config.Config{}, nil)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "uafilm.me/video/ep1/index.m3u8") {
		t.Errorf("episode HLS missing:\n%s", rec.Body.String())
	}
}

func TestUAFilmSimilarWhenNoMatch(t *testing.T) {
	srv := uafilmTestServer(t)
	u := newUAFilmTestChecker(srv)

	// No matching tmdb/imdb/name+year → similar list.
	code, body := uafilmDo(t, u, "title=Zzz&original_title=Zzz&year=1900&rjson=true")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, `"similar"`) {
		t.Errorf("expected similar list:\n%s", body)
	}
}

func TestUAFilmNameEquals(t *testing.T) {
	if !uafilmNameEquals("Thunderbolts*", "Thunderbolts") {
		t.Error("punctuation should be ignored")
	}
	if uafilmNameEquals("", "") {
		t.Error("empty must not equal")
	}
	if uafilmNameEquals("Loki", "Wednesday") {
		t.Error("different titles must not equal")
	}
}

// TestUAFilmLive hits the real uafilm.me API. Guarded by LAMPAC_LIVE_UAFILM=1 so
// normal CI stays offline. Run: LAMPAC_LIVE_UAFILM=1 go test -run UAFilmLive.
func TestUAFilmLive(t *testing.T) {
	if os.Getenv("LAMPAC_LIVE_UAFILM") != "1" {
		t.Skip("set LAMPAC_LIVE_UAFILM=1 for the live network test")
	}
	u := NewUAFilmChecker(config.Config{})

	// Movie flow: tmdb id 986056 (Thunderbolts) → direct HLS.
	req := httptest.NewRequest(http.MethodGet, "/lite/uafilm?id=986056&title=Thunderbolts&original_title=Thunderbolts&year=2025&rjson=true", nil)
	rec := httptest.NewRecorder()
	u.index(rec, req, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ".m3u8") {
		t.Fatalf("movie: no HLS:\n%s", rec.Body.String())
	}

	// Series flow: Wednesday (tmdb 119051, uafilm orid 124) → seasons.
	req = httptest.NewRequest(http.MethodGet, "/lite/uafilm?orid=124&title=Wednesday&original_title=Wednesday&serial=1&rjson=true", nil)
	rec = httptest.NewRecorder()
	u.index(rec, req, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "season") {
		t.Fatalf("series: no seasons:\n%s", rec.Body.String())
	}

	// Episode listing → call-method video links.
	req = httptest.NewRequest(http.MethodGet, "/lite/uafilm?orid=124&title=Wednesday&original_title=Wednesday&s=1&rjson=true", nil)
	rec = httptest.NewRecorder()
	u.index(rec, req, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/lite/uafilm/video?vid=") {
		t.Fatalf("episodes: no video links:\n%s", rec.Body.String())
	}

	// Watch resolve: episode primary video id 1421 → HLS.
	req = httptest.NewRequest(http.MethodGet, "/lite/uafilm/video?vid=1421", nil)
	rec = httptest.NewRecorder()
	u.video(rec, req, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ".m3u8") {
		t.Fatalf("watch: no HLS:\n%s", rec.Body.String())
	}
}
