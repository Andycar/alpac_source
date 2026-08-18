package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestAnilibertySimilarRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/app/search/releases" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[
			{"name":{"main":"Наруто","english":"Naruto"},"id":101,"year":2002,"poster":{"src":"/images/naruto.jpg"}},
			{"name":{"main":"Наруто: Ураганные хроники","english":"Naruto Shippuuden"},"id":102,"year":2007,"poster":{"src":"images/naruto-s.jpg"}}
		]`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			AniLiberty: config.HostSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/aniliberty?rjson=true&title=Naruto", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v body=%s", err, rec.Body.String())
	}
	if toString(payload["type"]) != "similar" {
		t.Fatalf("unexpected type: %#v body=%s", payload["type"], rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected similar rows count: %d", len(data))
	}
	row0, _ := data[0].(map[string]any)
	if !strings.Contains(toString(row0["url"]), "/lite/aniliberty?rjson=true&title=Naruto&releases=") {
		t.Fatalf("unexpected similar url: %#v", row0["url"])
	}
	if !strings.Contains(toString(row0["img"]), upstream.URL) {
		t.Fatalf("unexpected image url: %#v", row0["img"])
	}
}

func TestAnilibertyEpisodeByReleasesRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/anime/releases/42" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{
			"alias":"my-show-2nd",
			"episodes":[
				{"ordinal":"1","name":"Серия 1","hls_1080":"https://cdn.example/ep1-1080.m3u8","hls_720":"https://cdn.example/ep1-720.m3u8","hls_480":""},
				{"ordinal":"2","name":"Серия 2","hls_1080":"","hls_720":"//cdn.example/ep2-720.m3u8","hls_480":"https://cdn.example/ep2-480.m3u8"}
			]
		}`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			AniLiberty: config.HostSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/aniliberty?rjson=true&title=My%20Show&releases=42", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v body=%s", err, rec.Body.String())
	}
	if toString(payload["type"]) != "episode" {
		t.Fatalf("unexpected type: %#v body=%s", payload["type"], rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected episode rows count: %d", len(data))
	}
	row0, _ := data[0].(map[string]any)
	if toIntFromAny(row0["s"]) != 2 {
		t.Fatalf("unexpected season number: %#v", row0["s"])
	}
	if !strings.Contains(toString(row0["url"]), "ep1-1080.m3u8") {
		t.Fatalf("unexpected first episode url: %#v", row0["url"])
	}
	streams, _ := row0["streamquality"].([]any)
	if len(streams) == 0 {
		t.Fatalf("streamquality missing: %#v", row0)
	}
}
