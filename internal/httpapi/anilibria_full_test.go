package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestAnilibriaSimilarRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/searchTitles" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[
			{"names":{"ru":"Наруто","en":"Naruto"},"code":"naruto","season":{"year":2002},"posters":{"original":{"url":"/uploads/posters/naruto.jpg"}}},
			{"names":{"ru":"Наруто: Ураганные хроники","en":"Naruto Shippuuden"},"code":"naruto-shippuuden","season":{"year":2007},"posters":{"original":{"url":"/uploads/posters/naruto-s.jpg"}}}
		]`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Anilibria: config.HostSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/anilibria?rjson=true&title=Naruto&year=2020", nil)
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
	if !strings.Contains(toString(row0["url"]), "/lite/anilibria?title=Naruto&code=") {
		t.Fatalf("unexpected similar url: %#v", row0["url"])
	}
	if !strings.Contains(toString(row0["img"]), "https://anilibria.tv/") {
		t.Fatalf("unexpected image url: %#v", row0["img"])
	}
}

func TestAnilibriaEpisodeByCodeRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/searchTitles" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[
			{
				"names":{"ru":"Другое имя","en":"Other Name"},
				"code":"my-show-2nd",
				"season":{"year":2024},
				"player":{"host":"cdn.anilibria.tv","playlist":{
					"ep2":{"serie":2,"hls":{"fhd":"https://cdn.anilibria.tv/ep2-1080.m3u8","hd":"","sd":"/ep2-480.m3u8"}},
					"ep1":{"serie":1,"hls":{"fhd":"/ep1-1080.m3u8","hd":"/ep1-720.m3u8","sd":""}}
				}}
			}
		]`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Anilibria: config.HostSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/anilibria?rjson=true&title=My%20Show&code=my-show-2nd", nil)
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

func toIntFromAny(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	default:
		return 0
	}
}
