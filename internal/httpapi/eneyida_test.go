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

func TestEneyidaChecksearch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Eneyida: config.HostSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/eneyida?checksearch=true&title=Test", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", body)
	}
}

func TestEneyidaMovieRjson(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie.html":
			_, _ = w.Write([]byte(`<div class="full_content fx_row"><div> 720p</div></div><iframe width="100%" height="400" src="` + upstream.URL + `/player/101"></iframe>`))
		case "/player/101":
			_, _ = w.Write([]byte(`var x={file:"https://cdn.example/movie/index.m3u8", subtitle:"[RU]https://subs.example/ru.vtt"};`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Eneyida: config.HostSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	href := url.QueryEscape(upstream.URL + "/movie.html")
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/eneyida?rjson=true&href="+href+"&title=Film&original_title=Film+Orig&year=2024", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, rec.Body.String())
	}
	if payload["type"] != "movie" {
		t.Fatalf("unexpected type: %#v", payload["type"])
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("unexpected data: %#v", payload["data"])
	}
	row := data[0].(map[string]any)
	if row["name"] != "720p" {
		t.Fatalf("unexpected movie label: %#v", row["name"])
	}
	if _, ok := row["subtitles"].([]any); !ok {
		t.Fatalf("subtitles missing: %#v", row)
	}
}

func TestEneyidaSerialRjson(t *testing.T) {
	serialJSON := `[{"title":"Voice A","folder":[{"title":"1 season","folder":[{"title":"1 episode","file":"https://cdn.example/s1e1/index.m3u8","subtitle":"[RU]https://subs.example/e1.vtt"},{"title":"2 episode","file":"https://cdn.example/s1e2/index.m3u8"}]}]},{"title":"Voice B","folder":[{"title":"1 season","folder":[{"title":"1 episode","file":"https://cdn.example/s1e1b/index.m3u8"}]}]}]`

	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/serial.html":
			_, _ = w.Write([]byte(`<iframe width="100%" height="400" src="` + upstream.URL + `/player/202"></iframe>`))
		case "/player/202":
			_, _ = w.Write([]byte(`new Playerjs({file: '` + serialJSON + `', poster:"x"});`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Eneyida: config.HostSource{Host: upstream.URL}},
	}

	href := url.QueryEscape(upstream.URL + "/serial.html")

	recSeason := httptest.NewRecorder()
	reqSeason := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/eneyida?rjson=true&href="+href+"&title=Serial&original_title=Serial+Orig&year=2024", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(recSeason, reqSeason)

	if recSeason.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d", recSeason.Code)
	}
	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(recSeason.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("season unmarshal: %v, body=%s", err, recSeason.Body.String())
	}
	if seasonPayload["type"] != "season" {
		t.Fatalf("unexpected season type: %#v", seasonPayload["type"])
	}

	recEpisode := httptest.NewRecorder()
	reqEpisode := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/eneyida?rjson=true&href="+href+"&title=Serial&original_title=Serial+Orig&year=2024&s=1&t=0", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(recEpisode, reqEpisode)

	if recEpisode.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d", recEpisode.Code)
	}
	var episodePayload map[string]any
	if err := stdjson.Unmarshal(recEpisode.Body.Bytes(), &episodePayload); err != nil {
		t.Fatalf("episode unmarshal: %v, body=%s", err, recEpisode.Body.String())
	}
	if episodePayload["type"] != "episode" {
		t.Fatalf("unexpected episode type: %#v", episodePayload["type"])
	}
	if _, ok := episodePayload["voice"].([]any); !ok {
		t.Fatalf("voice payload missing: %#v", episodePayload)
	}
	data, ok := episodePayload["data"].([]any)
	if !ok || len(data) == 0 {
		t.Fatalf("episode data missing: %#v", episodePayload["data"])
	}
}

func TestEneyidaSimilarRjson(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/index.php") {
			_, _ = w.Write([]byte(`
				<html>
					<article class="x">
						<a href="` + upstream.URL + `/news/thunderbolts-2024.html">news</a>
						<div id="short_title">Громовержцы</div>
						<div class="short_subtitle"><a href="` + upstream.URL + `/xfsearch/year/2024/">2024</a> &bull; Thunderbolts</div>
						<img data-src="/uploads/poster.jpg">
					</article>
				</html>`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Eneyida: config.HostSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/eneyida?rjson=true&similar=true&title=Громовержцы&original_title=Thunderbolts&year=2024&clarification=1", nil)
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
		t.Fatalf("similar data missing: %#v", payload["data"])
	}
}
