package httpapi

import (
	"encoding/base64"
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// kinoukrReverseString mirrors the litesrc original (test fixture builder).
func kinoukrReverseString(v string) string {
	if v == "" {
		return ""
	}
	runes := []rune(v)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func TestKinoukrMovieRjsonFromTortuga(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/player/movie" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`var x={file:"https://cdn.example/movie/index.m3u8","subtitle":"[UA]https://subs.example/ua.vtt"};`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinoukr: config.KinoukrSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	href := url.QueryEscape(upstream.URL + "/player/movie")
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinoukr?rjson=true&href="+href+"&title=Film&original_title=Film+Orig&year=2024", nil)
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
	rows, ok := payload["data"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("unexpected data: %#v", payload["data"])
	}
	row := rows[0].(map[string]any)
	if row["url"] != "https://cdn.example/movie/index.m3u8" {
		t.Fatalf("unexpected movie url: %#v", row["url"])
	}
	if _, ok := row["subtitles"].([]any); !ok {
		t.Fatalf("expected subtitles payload, got: %#v", row)
	}
}

func TestKinoukrMovieRjsonFromBase64(t *testing.T) {
	target := "https://cdn.example/base64/index.m3u8"
	encoded := base64.StdEncoding.EncodeToString([]byte(kinoukrReverseString(target)))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/player/base64" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`var x={file:"` + encoded + `"};`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinoukr: config.KinoukrSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	href := url.QueryEscape(upstream.URL + "/player/base64")
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinoukr?rjson=true&href="+href+"&title=Film&original_title=Film+Orig&year=2024", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, target) {
		t.Fatalf("expected decoded base64 stream url, got: %s", body)
	}
}

func TestKinoukrSerialRjsonFromTortuga(t *testing.T) {
	serialJSON := `[{"title":"1 сезон","season":"1","folder":[{"title":"1 серия","number":"1","folder":[{"title":"Voice A","file":"https://cdn.example/s1e1a.m3u8","subtitle":"[RU]https://subs.example/e1.vtt"},{"title":"Voice B","file":"https://cdn.example/s1e1b.m3u8"}]},{"title":"2 серия","number":"2","folder":[{"title":"Voice A","file":"https://cdn.example/s1e2a.m3u8"}]}]}]`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/player/serial" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`new Playerjs({file: '` + serialJSON + `', poster:"x"});`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinoukr: config.KinoukrSource{Host: upstream.URL}},
	}

	href := url.QueryEscape(upstream.URL + "/player/serial")

	recSeason := httptest.NewRecorder()
	reqSeason := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinoukr?rjson=true&href="+href+"&title=Serial&original_title=Serial+Orig&year=2024", nil)
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
	reqEpisode := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinoukr?rjson=true&href="+href+"&title=Serial&original_title=Serial+Orig&year=2024&s=1", nil)
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
	if !ok || len(data) != 2 {
		t.Fatalf("unexpected episode data: %#v", episodePayload["data"])
	}
}

func TestKinoukrSimilarFromXFSearchRjson(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/xfsearch/"):
			_, _ = w.Write([]byte(`<div id="dle-content">
				<div class="short"><a class="short-title" href="` + upstream.URL + `/100-movie-one.html">Фільм 1</a></div>
				<div class="short"><a class="short-title" href="` + upstream.URL + `/200-movie-two.html">Фільм 2</a></div>
			</div>`))
		case r.URL.Path == "/100-movie-one.html":
			_, _ = w.Write([]byte(`<div class="foriginal">Movie One</div>
				<a href="/xfsearch/year/2023/">2023</a>
				<iframe src="https://tortuga.tw/vod/1" frameborder="0"></iframe>`))
		case r.URL.Path == "/200-movie-two.html":
			_, _ = w.Write([]byte(`<div class="foriginal">Movie Two</div>
				<a href="/xfsearch/year/2025/">2025</a>
				<iframe src="https://ashdi.vip/vod/2" frameborder="0"></iframe>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Kinoukr: config.KinoukrSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinoukr?rjson=true&title=Фільм&original_title=Movie&year=2024", nil)
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
	if !ok || len(data) != 2 {
		t.Fatalf("unexpected similar data: %#v", payload["data"])
	}
}

func TestKinoukrSourceIDResolvesIframe(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie/10":
			_, _ = w.Write([]byte(`<iframe src="` + upstream.URL + `/player/10"></iframe>`))
		case "/player/10":
			_, _ = w.Write([]byte(`var x={file:"https://cdn.example/source/index.m3u8"};`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinoukr: config.KinoukrSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinoukr?rjson=true&source=kinoukr&id=movie/10&title=Film&original_title=Film&year=2024", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie payload, got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "https://cdn.example/source/index.m3u8") {
		t.Fatalf("expected source stream url, got: %s", rec.Body.String())
	}
}
