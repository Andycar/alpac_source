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

func TestAnimegoSimilarRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/anime" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`
<div class="p-poster__stack" data-ajax-url="/anime/naruto-101" data-original="/img/naruto.jpg">
  <div class="card-title text-truncate"><a href="/anime/naruto-101">Naruto</a></div>
  <div class="anime-year"><a href="#">2002</a></div>
</div>
<div class="p-poster__stack" data-ajax-url="/anime/naruto-shippuuden-102" data-original="img/naruto-s.jpg">
  <div class="card-title text-truncate"><a href="/anime/naruto-shippuuden-102">Naruto Shippuuden</a></div>
  <div class="anime-year"><a href="#">2007</a></div>
</div>`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			AnimeGo: config.HostSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/animego?rjson=true&title=Naruto", nil)
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
	if !strings.Contains(toString(row0["url"]), "/lite/animego?rjson=true&title=Naruto&pid=101&s=0") {
		t.Fatalf("unexpected similar url: %#v", row0["url"])
	}
	if !strings.Contains(toString(row0["img"]), upstream.URL) {
		t.Fatalf("unexpected image url: %#v", row0["img"])
	}
}

func TestAnimegoEpisodeByPidRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/anime/101/player" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{
				"content":"<div data-player=\"//aniboom.site/embed/tok123?episode=1&amp;translation=11\" data-provider=\"1\" data-provide-dubbing=\"100\"></div><div data-player=\"//aniboom.site/embed/tok123?episode=1&amp;translation=22\" data-provider=\"1\" data-provide-dubbing=\"200\"></div><div data-dubbing=\"100\"><span class=\"name\">AniDub</span></div><div data-dubbing=\"200\"><span class=\"name\">AniLibria</span></div><div data-episode=\"1\"></div><div data-episode=\"2\"></div>"
			}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			AnimeGo: config.HostSource{Host: upstream.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/animego?rjson=true&title=Naruto&pid=101&s=1", nil)
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
	if toIntFromAny(row0["s"]) != 1 {
		t.Fatalf("unexpected season: %#v", row0["s"])
	}
	if toIntFromAny(row0["e"]) != 1 {
		t.Fatalf("unexpected episode: %#v", row0["e"])
	}
	if !strings.Contains(toString(row0["url"]), "/lite/animego/video.m3u8?host=aniboom.site&token=tok123&e=1&t=11&play=true") {
		t.Fatalf("unexpected stream url: %#v", row0["url"])
	}
	voiceRows, _ := payload["voice"].([]any)
	if len(voiceRows) == 0 {
		t.Fatalf("voice rows missing: %#v", payload["voice"])
	}
}

func TestAnimegoVideoRedirectAndJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed/tok123" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"hls":"{\"src\":\"//cdn.aniboom.site/hls/stream.m3u8\"}"}`))
	}))
	defer upstream.Close()

	host := upstream.URL

	cfg := config.Config{
		Online: config.OnlineConfig{
			AnimeGo: config.HostSource{Host: upstream.URL},
		},
	}

	redirectRec := httptest.NewRecorder()
	redirectReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/animego/video.m3u8?host="+url.QueryEscape(host)+"&token=tok123&e=2&t=11&play=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(redirectRec, redirectReq)

	if redirectRec.Code != http.StatusFound {
		t.Fatalf("unexpected redirect status: %d body=%s", redirectRec.Code, redirectRec.Body.String())
	}
	if got := redirectRec.Header().Get("Location"); got != "https://cdn.aniboom.site/hls/stream.m3u8" {
		t.Fatalf("unexpected redirect location: %s", got)
	}

	jsonRec := httptest.NewRecorder()
	jsonReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/animego/video?host="+url.QueryEscape(host)+"&token=tok123&e=2&t=11", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(jsonRec, jsonReq)

	if jsonRec.Code != http.StatusOK {
		t.Fatalf("unexpected json status: %d body=%s", jsonRec.Code, jsonRec.Body.String())
	}
	if !strings.Contains(jsonRec.Body.String(), `"method":"play"`) {
		t.Fatalf("unexpected json body: %s", jsonRec.Body.String())
	}
	if !strings.Contains(jsonRec.Body.String(), `"https://cdn.aniboom.site/hls/stream.m3u8"`) {
		t.Fatalf("unexpected json body: %s", jsonRec.Body.String())
	}
}
