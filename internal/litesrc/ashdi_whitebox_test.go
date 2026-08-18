package litesrc

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestAshdiChecksearchByWormhole(t *testing.T) {
	wormhole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("imdb_id") == "tt1234567" {
			_, _ = w.Write([]byte(`{"play":"https://ashdi.vip/vod/12345"}`))
		} else {
			http.NotFound(w, r)
		}
	}))
	defer wormhole.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Ashdi: config.HostSource{Host: ""}},
	}

	checker := NewAshdiChecker(cfg)
	checker.wormholeHost = wormhole.URL

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/ashdi?checksearch=true&imdb_id=tt1234567", nil)

	show := checker.checkSearch(req)
	if !show {
		t.Fatalf("expected checksearch to return true for wormhole hit")
	}
}

func TestAshdiMovieMultiVoiceRjson(t *testing.T) {
	multiJSON := `[{"title":" дубляж | Le Doyen", "file":"https://cdn.example/voice1/index.m3u8", "subtitle":"[UA]https://subs.example/ua.vtt", "id":"100"},{"title":" оригінал", "file":"https://cdn.example/voice2/index.m3u8", "subtitle":"", "id":"101"}]`

	ashdiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "multivoice") {
			_, _ = w.Write([]byte(`new Playerjs({file:'` + multiJSON + `', default_quality:"480p"})`))
		} else {
			_, _ = w.Write([]byte(`new Playerjs({file:"https://cdn.example/voice1/index.m3u8"})`))
		}
	}))
	defer ashdiServer.Close()

	wormhole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"play":"` + ashdiServer.URL + `/vod/100"}`))
	}))
	defer wormhole.Close()

	cfg := config.Config{}
	checker := NewAshdiChecker(cfg)
	checker.wormholeHost = wormhole.URL

	handler := checker.Handle(cfg, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/ashdi?rjson=true&imdb_id=tt9999&title=Test&original_title=Test+Orig", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v, body=%s", err, rec.Body.String())
	}
	if payload["type"] != "movie" {
		t.Fatalf("unexpected type: %#v", payload["type"])
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("expected 2 voices, got: %#v", payload["data"])
	}
	row := data[0].(map[string]any)
	if row["method"] != "play" {
		t.Fatalf("unexpected method: %#v", row["method"])
	}
	if !strings.Contains(row["name"].(string), "Le Doyen") {
		t.Fatalf("expected voice name, got: %#v", row["name"])
	}
}

func TestAshdiMovieSingleVoiceWormholeRjson(t *testing.T) {
	// When ?multivoice returns a plain URL (not JSON array), should still work.
	ashdiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`new Playerjs({file:'https://cdn.example/single/index.m3u8', subtitle:"[UA]https://subs.example/ua.vtt", default_quality:"480p"})`))
	}))
	defer ashdiServer.Close()

	wormhole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"play":"` + ashdiServer.URL + `/vod/3205"}`))
	}))
	defer wormhole.Close()

	cfg := config.Config{}
	checker := NewAshdiChecker(cfg)
	checker.wormholeHost = wormhole.URL

	handler := checker.Handle(cfg, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/ashdi?rjson=true&imdb_id=tt0816692&title=Interstellar&original_title=Interstellar", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v, body=%s", err, rec.Body.String())
	}
	if payload["type"] != "movie" {
		t.Fatalf("unexpected type: %#v", payload["type"])
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("expected 1 voice, got: %#v", payload["data"])
	}
	row := data[0].(map[string]any)
	if row["method"] != "play" {
		t.Fatalf("unexpected method: %#v", row["method"])
	}
	if _, ok := row["subtitles"].([]any); !ok {
		t.Fatalf("expected subtitles: %#v", row)
	}
}
