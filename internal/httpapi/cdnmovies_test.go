package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestCDNmoviesChecksearch(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/serial/kinopoisk/700" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`<script>makePlayer({file:'[{"title":"Voice A","folder":[]}]'})</script>`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			CDNmovies: config.HostSource{Host: api.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/cdnmovies?checksearch=true&kinopoisk_id=700", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", rec.Body.String())
	}
}

func TestCDNmoviesSeasonAndEpisodeRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/serial/kinopoisk/701" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`<script>makePlayer({file:'[
			{"title":"Voice A","folder":[
				{"title":"Season 1","folder":[
					{"title":"Episode 1","file":"[360]https://cdn.example/a-s1e1.m3u8,[240]https://cdn.example/a-s1e1.mp4"},
					{"title":"Episode 2","file":"[360p]https://cdn.example/a-s1e2.m3u8"}
				]},
				{"title":"Season 2","folder":[
					{"title":"Episode 1","file":"[360]https://cdn.example/a-s2e1.m3u8"}
				]}
			]},
			{"title":"Voice B","folder":[
				{"title":"Season 1","folder":[
					{"title":"Episode 1","file":"[360]https://cdn.example/b-s1e1.m3u8"}
				]}
			]}
		]'})</script>`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			CDNmovies: config.HostSource{Host: api.URL},
		},
	}

	seasonRec := httptest.NewRecorder()
	seasonReq := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/cdnmovies?rjson=true&kinopoisk_id=701&title=Show&original_title=Show&t=0",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(seasonRec, seasonReq)

	if seasonRec.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d body=%s", seasonRec.Code, seasonRec.Body.String())
	}
	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(seasonRec.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("invalid season json: %v", err)
	}
	if toString(seasonPayload["type"]) != "season" {
		t.Fatalf("unexpected season type: %#v body=%s", seasonPayload["type"], seasonRec.Body.String())
	}
	seasons, _ := seasonPayload["data"].([]any)
	if len(seasons) != 2 {
		t.Fatalf("unexpected seasons count: %d", len(seasons))
	}

	episodeRec := httptest.NewRecorder()
	episodeReq := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/cdnmovies?rjson=true&kinopoisk_id=701&title=Show&original_title=Show&t=0&s=1&sid=0",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(episodeRec, episodeReq)

	if episodeRec.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d body=%s", episodeRec.Code, episodeRec.Body.String())
	}
	var episodePayload map[string]any
	if err := stdjson.Unmarshal(episodeRec.Body.Bytes(), &episodePayload); err != nil {
		t.Fatalf("invalid episode json: %v", err)
	}
	if toString(episodePayload["type"]) != "episode" {
		t.Fatalf("unexpected episode type: %#v body=%s", episodePayload["type"], episodeRec.Body.String())
	}
	voices, _ := episodePayload["voice"].([]any)
	if len(voices) != 2 {
		t.Fatalf("unexpected voice count: %d", len(voices))
	}
	data, _ := episodePayload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected episode rows count: %d", len(data))
	}
	first, _ := data[0].(map[string]any)
	if !strings.Contains(toString(first["url"]), "a-s1e1.m3u8") {
		t.Fatalf("unexpected first episode url: %#v", first["url"])
	}
	if _, ok := first["streamquality"].([]any); !ok {
		t.Fatalf("streamquality missing: %#v", first)
	}
}
