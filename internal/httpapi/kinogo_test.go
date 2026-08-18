package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

const kinogoSampleSearchHTML = `
<!doctype html><html><body>
<div class="fullsearch-msg">По Вашему запросу найдено <b>2 фильмов</b>:</div>
<div class="movie" id="26146">
  <div class="shortstorytitle">
    <h2 class="zagolovki"><a href="%[1]s/26146-movie-one.html">Movie One (2014)</a></h2>
  </div><!--shortstorytitle-->
  <div class="movie__card">
    <div class="movie__info-img">
      <img src="/templates/Kinogo/images/lazy-poster.png" data-src="/uploads/posts/2025/movie-one.webp" alt="">
    </div>
    <div class="movie__info-item"><b>Год выпуска:</b> <a href="/year/2014/">2014</a></div>
  </div>
</div>
<!--shortstory--><div class="movie" id="35185">
  <div class="shortstorytitle">
    <h2 class="zagolovki"><a href="%[1]s/35185-movie-two.html">Movie Two (2016)</a></h2>
  </div><!--shortstorytitle-->
  <div class="movie__card">
    <div class="movie__info-img">
      <img src="/templates/Kinogo/images/lazy-poster.png" data-src="/uploads/posts/2025/movie-two.webp" alt="">
    </div>
    <div class="movie__info-item"><b>Год выпуска:</b> <a href="/year/2016/">2016</a></div>
  </div>
</div>
<!--shortstory--></body></html>`

const kinogoSampleMovieHTML = `
<html><body>
<iframe data-src="%[1]s/embed/movie/180"></iframe>
<iframe data-src="//walking-as.allarknow.online/?token_movie=abc&amp;token=def"></iframe>
<iframe data-src="%[1]s/embed/trailer/180?number=1"></iframe>
</body></html>`

const kinogoSampleMovieEmbed = `
<html><body>
<script>
makePlayer({
  blocked: false,
  title: "Movie One",
  source: {
    hls: "https://cdn.example/movie/master.m3u8?t=1",
    dash: "https://cdn.example/movie/master.mpd?t=1",
    audio: {"names":["Дубляж","Eng.Original"],"order":[0,1]},
    cc: [{"url":"https://subs.example/ru.vtt","name":"Рус. полные - 1"}]
  }
});
</script>
</body></html>`

const kinogoSampleSerialEmbed = `
<html><body>
<script>
makePlayer({
  playlist: {
    seasons:[{"season":1,"episodes":[{"episode":"1","hls":"https://cdn.example/s1e1.m3u8","audio":{"names":["Дубляж"],"order":[0]}},{"episode":"2","hls":"https://cdn.example/s1e2.m3u8","audio":{"names":["Дубляж"],"order":[0]}}]},{"season":2,"episodes":[{"episode":"1","hls":"https://cdn.example/s2e1.m3u8","audio":{"names":["Дубляж"],"order":[0]}}]}],
  }
});
</script>
</body></html>`

func newKinogoTestUpstream(t *testing.T, movieEmbed, serialEmbed string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/"):
			_, _ = w.Write([]byte(strings.ReplaceAll(kinogoSampleSearchHTML, "%[1]s", srv.URL)))
		case strings.HasPrefix(r.URL.Path, "/26146-"):
			_, _ = w.Write([]byte(strings.ReplaceAll(kinogoSampleMovieHTML, "%[1]s", srv.URL)))
		case strings.HasPrefix(r.URL.Path, "/57363-"):
			_, _ = w.Write([]byte(strings.ReplaceAll(kinogoSampleMovieHTML, "%[1]s", srv.URL)))
		case r.URL.Path == "/embed/movie/180":
			_, _ = w.Write([]byte(movieEmbed))
		case r.URL.Path == "/embed/movie/255":
			_, _ = w.Write([]byte(serialEmbed))
		case r.URL.Path == "/":
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestKinogoChecksearch(t *testing.T) {
	upstream := newKinogoTestUpstream(t, kinogoSampleMovieEmbed, kinogoSampleSerialEmbed)
	cfg := config.Config{
		Online: config.OnlineConfig{Kinogo: config.KinogoSource{Host: upstream.URL}},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinogo?checksearch=true&title=Movie+One", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", rec.Body.String())
	}
}

func TestKinogoSimilarRjson(t *testing.T) {
	upstream := newKinogoTestUpstream(t, kinogoSampleMovieEmbed, kinogoSampleSerialEmbed)
	cfg := config.Config{
		Online: config.OnlineConfig{Kinogo: config.KinogoSource{Host: upstream.URL}},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/kinogo?rjson=true&title=Need+Another&year=2024&similar=true",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, rec.Body.String())
	}
	if payload["type"] != "similar" {
		t.Fatalf("unexpected type: %#v", payload["type"])
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) == 0 {
		t.Fatalf("unexpected data: %#v", payload["data"])
	}
}

func TestKinogoMovieRjson(t *testing.T) {
	upstream := newKinogoTestUpstream(t, kinogoSampleMovieEmbed, kinogoSampleSerialEmbed)
	cfg := config.Config{
		Online: config.OnlineConfig{Kinogo: config.KinogoSource{Host: upstream.URL}},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/kinogo?rjson=true&href="+url.QueryEscape("26146-movie-one.html")+"&title=Movie+One&original_title=Movie+One+Orig",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, rec.Body.String())
	}
	if payload["type"] != "movie" {
		t.Fatalf("unexpected type: %#v, body=%s", payload["type"], rec.Body.String())
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("unexpected data: %#v", payload["data"])
	}
	row := data[0].(map[string]any)
	if row["method"] != "play" {
		t.Fatalf("expected method=play, got %#v", row["method"])
	}
	stream, _ := row["stream"].(string)
	if stream == "" {
		t.Fatalf("expected non-empty stream URL, got %#v", row["stream"])
	}
}

func TestKinogoSerialRjson(t *testing.T) {
	upstream := newKinogoTestUpstream(t, kinogoSampleMovieEmbed, kinogoSampleSerialEmbed)
	// Switch the search "Movie One" page (26146) to point to the serial embed
	// (movie/255) by rerouting through a wrapped test server isn't needed —
	// instead we hit the upstream directly via a serial href.
	cfg := config.Config{
		Online: config.OnlineConfig{Kinogo: config.KinogoSource{Host: upstream.URL}},
	}

	// Use a movie-page route that resolves to /embed/movie/255 (the serial).
	// The shared movie HTML iframe points to /embed/movie/180 — patch the
	// upstream to swap the iframe target for href starting with 57363-.
	upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/57363-"):
			_, _ = w.Write([]byte(`<html><body><iframe data-src="` + upstream.URL + `/embed/movie/255"></iframe></body></html>`))
		case r.URL.Path == "/embed/movie/255":
			_, _ = w.Write([]byte(kinogoSampleSerialEmbed))
		default:
			http.NotFound(w, r)
		}
	})

	recSeason := httptest.NewRecorder()
	reqSeason := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/kinogo?rjson=true&href="+url.QueryEscape("57363-serial.html")+"&title=Serial&original_title=Serial+Orig",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(recSeason, reqSeason)
	if recSeason.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d, body=%s", recSeason.Code, recSeason.Body.String())
	}
	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(recSeason.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("season unmarshal: %v, body=%s", err, recSeason.Body.String())
	}
	if seasonPayload["type"] != "season" {
		t.Fatalf("unexpected season type: %#v", seasonPayload["type"])
	}
	seasonData, _ := seasonPayload["data"].([]any)
	if len(seasonData) != 2 {
		t.Fatalf("expected 2 seasons, got %d (body=%s)", len(seasonData), recSeason.Body.String())
	}

	recEpisode := httptest.NewRecorder()
	reqEpisode := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/kinogo?rjson=true&href="+url.QueryEscape("57363-serial.html")+"&title=Serial&original_title=Serial+Orig&s=1",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(recEpisode, reqEpisode)
	if recEpisode.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d", recEpisode.Code)
	}
	var episodePayload map[string]any
	if err := stdjson.Unmarshal(recEpisode.Body.Bytes(), &episodePayload); err != nil {
		t.Fatalf("episode unmarshal: %v, body=%s", err, recEpisode.Body.String())
	}
	if episodePayload["type"] != "episode" {
		t.Fatalf("unexpected episode type: %#v, body=%s", episodePayload["type"], recEpisode.Body.String())
	}
	episodeData, _ := episodePayload["data"].([]any)
	if len(episodeData) != 2 {
		t.Fatalf("expected 2 episodes in s1, got %d", len(episodeData))
	}
}

// TestKinogoParseRealFixtures verifies that the search HTML parser and the
// iframe parser work on real snapshots captured from kinogo.la / api.ortified.ws.
// These fixtures live in testdata/ and can be refreshed by re-fetching the
// pages whenever the templates change.
