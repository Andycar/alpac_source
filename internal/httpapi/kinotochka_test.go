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

func TestKinotochkaChecksearchByKinopoisk(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/find-by-kinopoisk.php" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("kinopoisk") != "447301" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`[{"url":"https://kinovibe.vip/example.html"}]`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Kinotochka: config.KinotochkaSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?checksearch=true&kinopoisk_id=447301", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestKinotochkaChecksearchByTitle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.php" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte(`<html><body>Поиск по сайту<div class="sres-wrap clearfix"><a href="https://kinovibe.vip/some-title-2024.html">item</a></div></body></html>`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Kinotochka: config.KinotochkaSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?checksearch=true&title=Inception", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestKinotochkaFullModeMovieLocalCore(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed/kinopoisk/447301" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`<script>id:"playerjshd", file:"https://cdn.example/low.mp4,https://cdn.example/high.mp4"</script>`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinotochka: config.KinotochkaSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?title=Inception&original_title=Inception&serial=0&kinopoisk_id=447301&rjson=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("expected movie payload, got: %s", body)
	}
	if !strings.Contains(body, "https://cdn.example/high.mp4") {
		t.Fatalf("expected last quality file in payload, got: %s", body)
	}
	if strings.Contains(body, "https://cdn.example/low.mp4") {
		t.Fatalf("unexpected low quality file leak, got: %s", body)
	}
}

func TestKinotochkaFullModeMovieFallbackByTitleWithoutKinopoisk(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embed/kinopoisk/999":
			w.WriteHeader(http.StatusNotFound)
		case "/index.php":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			_, _ = w.Write([]byte(`<html><body><div class="sres-wrap clearfix"><a href="` + upstream.URL + `/the-green-mile-1999.html">item</a></div></body></html>`))
		case "/the-green-mile-1999.html":
			_, _ = w.Write([]byte(`<script>id:'playerjshd', file:'https://cdn.example/low.mp4, https://cdn.example/high.mp4'</script>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinotochka: config.KinotochkaSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?title=The%20Green%20Mile&serial=0&kinopoisk_id=999&rjson=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("expected movie payload, got: %s", body)
	}
	if !strings.Contains(body, "https://cdn.example/high.mp4") {
		t.Fatalf("expected high quality movie file, got: %s", body)
	}
	if strings.Contains(body, "https://cdn.example/low.mp4") {
		t.Fatalf("unexpected low quality movie file in payload: %s", body)
	}
}

func TestKinotochkaFullModeMovieByKinopoiskGenericFileFallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed/kinopoisk/447301" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`<script>const alt={id:"player", file:"https://cdn.example/movie_480.mp4,https://cdn.example/movie_720.mp4"};</script>`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinotochka: config.KinotochkaSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?serial=0&kinopoisk_id=447301&rjson=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("expected movie payload, got: %s", body)
	}
	if !strings.Contains(body, "https://cdn.example/movie_720.mp4") {
		t.Fatalf("expected highest quality stream, got: %s", body)
	}
}

func TestKinotochkaFullModeSeasonsByKinopoisk(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/find-by-kinopoisk.php" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("kinopoisk") != "100" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`[
			{"url":"https://kinovibe.vip/title-2-sezon.html"},
			{"url":"https://kinovibe.vip/title-1-sezon.html"}
		]`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinotochka: config.KinotochkaSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?title=Demo&serial=1&kinopoisk_id=100&rjson=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var root struct {
		Type string `json:"type"`
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if root.Type != "season" {
		t.Fatalf("unexpected type: %s", root.Type)
	}
	if len(root.Data) != 2 {
		t.Fatalf("expected 2 seasons, got: %d", len(root.Data))
	}

	for _, row := range root.Data {
		u, err := url.Parse(row.URL)
		if err != nil {
			t.Fatalf("invalid season url %q: %v", row.URL, err)
		}
		q := u.Query()
		if q.Get("serial") != "1" {
			t.Fatalf("serial query must be 1, got: %s", q.Get("serial"))
		}
		if strings.TrimSpace(q.Get("newsuri")) == "" {
			t.Fatalf("newsuri must be present in season url: %s", row.URL)
		}
	}
}

func TestKinotochkaFullModeEpisodes(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/season-1.html":
			_, _ = w.Write([]byte(`id:"playerjshd", file:"` + upstream.URL + `/playlist.txt"`))
		case "/playlist.txt":
			_, _ = w.Write([]byte(`{"playlist":[
				{"comment":"1 серия <br>","file":"https://cdn.example/file[voice,1].mp4"},
				{"comment":"2 серия","file":"https://cdn.example/file2.mp4"}
			]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinotochka: config.KinotochkaSource{Host: upstream.URL}},
	}

	newsURI := url.QueryEscape(upstream.URL + "/season-1.html")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?title=Demo&serial=1&s=1&newsuri="+newsURI+"&rjson=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var root struct {
		Type string `json:"type"`
		Data []struct {
			URL string `json:"url"`
			S   int    `json:"s"`
			E   int    `json:"e"`
		} `json:"data"`
	}
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if root.Type != "episode" {
		t.Fatalf("unexpected type: %s", root.Type)
	}
	if len(root.Data) != 2 {
		t.Fatalf("expected 2 episodes, got: %d", len(root.Data))
	}
	if root.Data[0].URL != "https://cdn.example/file1.mp4" {
		t.Fatalf("unexpected normalized first file: %s", root.Data[0].URL)
	}
	if root.Data[0].S != 1 || root.Data[0].E != 1 {
		t.Fatalf("unexpected S/E in first episode: s=%d e=%d", root.Data[0].S, root.Data[0].E)
	}
}

func TestKinotochkaFullModeEpisodesWithBOM(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/season-1.html":
			_, _ = w.Write([]byte(`id:"playerjshd", file:"` + upstream.URL + `/playlist.txt"`))
		case "/playlist.txt":
			// UTF-8 BOM prefix — some kinotochka endpoints return this.
			_, _ = w.Write([]byte("\xEF\xBB\xBF" + `{"playlist":[
				{"comment":"1 серия","file":"https://cdn.example/ep1.mp4"},
				{"comment":"2 серия","file":"https://cdn.example/ep2.mp4"}
			]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinotochka: config.KinotochkaSource{Host: upstream.URL}},
	}

	newsURI := url.QueryEscape(upstream.URL + "/season-1.html")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?title=Demo&serial=1&s=1&newsuri="+newsURI+"&rjson=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var root struct {
		Type string `json:"type"`
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if root.Type != "episode" {
		t.Fatalf("expected episode type, got: %s", root.Type)
	}
	if len(root.Data) != 2 {
		t.Fatalf("expected 2 episodes, got: %d", len(root.Data))
	}
}
