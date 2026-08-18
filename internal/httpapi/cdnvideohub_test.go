package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestCDNvideohubChecksearch(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/player/sv/playlist" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("id") != "321" {
			t.Fatalf("unexpected id: %s", r.URL.Query().Get("id"))
		}
		_, _ = w.Write([]byte(`{"isSerial":false,"items":[{"vkId":"mv1"}]}`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			CDNvideohub: config.HostSource{Host: api.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/cdnvideohub?checksearch=true&kinopoisk_id=321", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", rec.Body.String())
	}
}

func TestCDNvideohubSerialRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/player/sv/playlist" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"isSerial": true,
			"items": [
				{"season":1,"voiceStudio":"Dub A","episode":1,"vkId":"vk-1a"},
				{"season":1,"voiceStudio":"Dub B","episode":1,"vkId":"vk-1b"},
				{"season":1,"voiceStudio":"Dub A","episode":2,"vkId":"vk-2a"},
				{"season":2,"voiceStudio":"Dub A","episode":1,"vkId":"vk-21a"}
			]
		}`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			CDNvideohub: config.HostSource{Host: api.URL},
		},
	}

	seasonRec := httptest.NewRecorder()
	seasonReq := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/cdnvideohub?rjson=true&kinopoisk_id=100&title=Show&original_title=Show",
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
		"http://lampac.local/lite/cdnvideohub?rjson=true&kinopoisk_id=100&title=Show&original_title=Show&s=1&t=Dub%20A",
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
		t.Fatalf("unexpected voices count: %d", len(voices))
	}
	rows, _ := episodePayload["data"].([]any)
	if len(rows) != 2 {
		t.Fatalf("unexpected episodes count: %d", len(rows))
	}
	row0, _ := rows[0].(map[string]any)
	if !strings.Contains(toString(row0["url"]), "/lite/cdnvideohub/video.m3u8?vkId=vk-1a") {
		t.Fatalf("unexpected first row url: %#v", row0["url"])
	}
}

func TestCDNvideohubMovieRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/player/sv/playlist" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"isSerial": false,
			"items": [
				{"voiceStudio":"Dub A","vkId":"mv-1"},
				{"voiceType":"Original","vkId":"mv-2"}
			]
		}`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			CDNvideohub: config.HostSource{Host: api.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/cdnvideohub?rjson=true&kinopoisk_id=500&title=Film&original_title=Film",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if toString(payload["type"]) != "movie" {
		t.Fatalf("unexpected type: %#v body=%s", payload["type"], rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected movie rows count: %d", len(data))
	}
	first, _ := data[0].(map[string]any)
	if !strings.Contains(toString(first["stream"]), "&play=true") {
		t.Fatalf("unexpected movie stream: %#v", first["stream"])
	}
}

func TestCDNvideohubVideoRoute(t *testing.T) {
	hits := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/player/sv/video/vk-1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		hits++
		_, _ = w.Write([]byte(`{"hlsUrl":"https:\\/\\/cdn.example\\/master.m3u8?x=1u0026y=2"}`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			CDNvideohub: config.HostSource{Host: api.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/cdnvideohub/video.m3u8?vkId=vk-1&title=Film", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected json status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid payload: %v", err)
	}
	if toString(payload["method"]) != "play" {
		t.Fatalf("unexpected method: %#v body=%s", payload["method"], rec.Body.String())
	}
	if toString(payload["url"]) != "https://cdn.example/master.m3u8?x=1&y=2" {
		t.Fatalf("unexpected play url: %#v body=%s", payload["url"], rec.Body.String())
	}

	redirectRec := httptest.NewRecorder()
	redirectReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/cdnvideohub/video.m3u8?vkId=vk-1&play=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(redirectRec, redirectReq)
	if redirectRec.Code != http.StatusFound {
		t.Fatalf("unexpected redirect status: %d", redirectRec.Code)
	}
	if got := redirectRec.Header().Get("Location"); got != "https://cdn.example/master.m3u8?x=1&y=2" {
		t.Fatalf("unexpected redirect location: %s", got)
	}
	if hits == 0 {
		t.Fatalf("expected upstream hits, got 0")
	}
}
