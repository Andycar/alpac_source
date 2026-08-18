package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestAshdiChecksearchByKinopoisk(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/product/read_api.php"):
			_, _ = w.Write([]byte(`<iframe src="` + upstream.URL + `/iframe/ok"></iframe>`))
		case r.URL.Path == "/iframe/ok":
			_, _ = w.Write([]byte(`new Playerjs({file:"https://cdn.example/movie/index.m3u8"})`))
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Ashdi: config.HostSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/ashdi?checksearch=true&kinopoisk_id=123", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"rch":true`) {
		t.Fatalf("expected rch:true in payload, got: %s", rec.Body.String())
	}
}

func TestAshdiMovieRjson(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/product/read_api.php"):
			_, _ = w.Write([]byte(`<iframe src="` + upstream.URL + `/iframe/movie"></iframe>`))
		case r.URL.Path == "/iframe/movie":
			_, _ = w.Write([]byte(`new Playerjs({file:"https://cdn.example/movie/index.m3u8", subtitle:"[RU]https://subs.example/ru.vtt"})`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Ashdi: config.HostSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/ashdi?rjson=true&kinopoisk_id=123&title=Film&original_title=Film+Orig", nil)
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
	if row["method"] != "play" {
		t.Fatalf("unexpected method: %#v", row["method"])
	}
	if _, ok := row["subtitles"].([]any); !ok {
		t.Fatalf("expected subtitles in payload: %#v", row)
	}
}

func TestAshdiSerialSeasonAndEpisodeRjson(t *testing.T) {
	serialJSON := `[{"title":"Voice A","folder":[{"title":"Сезон 1","folder":[{"title":"1 серия","file":"https://cdn.example/s1e1/index.m3u8","subtitle":"[RU]https://subs.example/e1.vtt"},{"title":"2 серия","file":"https://cdn.example/s1e2/index.m3u8"}]}]},{"title":"Voice B","folder":[{"title":"Сезон 1","folder":[{"title":"1 серия","file":"https://cdn.example/b1/index.m3u8"}]}]}]`

	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/product/read_api.php"):
			_, _ = w.Write([]byte(`<iframe src="` + upstream.URL + `/iframe/serial"></iframe>`))
		case r.URL.Path == "/iframe/serial":
			_, _ = w.Write([]byte(`new Playerjs({file:'` + serialJSON + `', poster:"x"})`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Ashdi: config.HostSource{Host: upstream.URL}},
	}

	recSeason := httptest.NewRecorder()
	reqSeason := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/ashdi?rjson=true&kinopoisk_id=555&title=Serial&original_title=Serial+Orig", nil)
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
	reqEpisode := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/ashdi?rjson=true&kinopoisk_id=555&s=1&t=0&title=Serial&original_title=Serial+Orig", nil)
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
		t.Fatalf("voice payload is missing: %#v", episodePayload)
	}
	data, ok := episodePayload["data"].([]any)
	if !ok || len(data) == 0 {
		t.Fatalf("episode data is missing: %#v", episodePayload["data"])
	}
	// Verify method is now "play" instead of "call"
	row := data[0].(map[string]any)
	if row["method"] != "play" {
		t.Fatalf("expected method play, got: %#v", row["method"])
	}
}

func TestAshdiRjsonEmptyWithoutKinopoisk(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{Ashdi: config.HostSource{Host: "https://base.ashdi.vip"}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/ashdi?rjson=true&title=Only+Title", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("expected empty object, got: %s", rec.Body.String())
	}
}
