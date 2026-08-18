package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// Sample pleer_on.php?kp=6750360 response — minimal but realistic, with the
// two iframes kinobadi serves: femd Плеер 1 (movie), Mirage Плеер 2 (4K).
const kinobadiPleerOnHTML = `<!DOCTYPE html><html><head><title>Горничная</title></head><body>
<div class="tabs slider-items">
<input type="radio" name="inset" id="tab_1" checked><label for="tab_1">Плеер 1</label>
<input type="radio" name="inset" id="tab_3"><label for="tab_3">Плеер 2  (<b>4K</b>)</label>
<div id="txt_1"><iframe src="https://api.femd.ws/embed/movie/86126?sharing=false&host=kinotik.top"></iframe></div>
<div id="txt_3"><iframe src="https://wonder-as.stloadi.live/?token_movie=22b440ccb9ff50149d6622c0542f1f&translation=85&token=23872e4443722d6578fd6fd8600c11&hd_4K"></iframe></div>
</div>
</body></html>`

// femd embed sample matching kinobadi.go's parser.
const kinobadiFemdEmbedHTML = `<!DOCTYPE html><html><head><title>Горничная</title></head><body>
<script data-name="mk">
makePlayer({
    title: "Горничная",
    id: 830090,
    source: {
        hls: "https://cdnr.example/master.m3u8?ha=1&t=42",
        cc: [{"url":"https://hye.example/rus.vtt","name":"Рус. полные - 5"}]
    }
});
</script>
</body></html>`

// kinobadi stub when KP isn't in catalog (~2KB, no femd iframe).
var kinobadiStubHTML = `<!DOCTYPE html><html><head><title>Title</title></head><body>
<div class="tabs"></div>
` + strings.Repeat("x", 1800) + `</body></html>`

func TestKinobadiIndexPageRjson(t *testing.T) {
	var (
		pleerHost string
		femdHost  string
	)

	pleer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Rewrite the femd iframe to point at our local femdHost so the
		// resolver can fetch it without hitting the real internet.
		html := strings.ReplaceAll(kinobadiPleerOnHTML,
			"https://api.femd.ws", femdHost)
		_, _ = w.Write([]byte(html))
	}))
	defer pleer.Close()
	pleerHost = pleer.URL

	femd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/embed/movie/86126") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("host") != "kinotik.top" {
			t.Errorf("expected host=kinotik.top, got %q", r.URL.Query().Get("host"))
		}
		_, _ = w.Write([]byte(kinobadiFemdEmbedHTML))
	}))
	defer femd.Close()
	femdHost = femd.URL

	cfg := config.Config{
		Online: config.OnlineConfig{Kinobadi: config.KinobadiSource{
			PleerHost: pleerHost,
			FemdHost:  femdHost,
			Consumer:  "kinotik.top",
		}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"http://lampac.local/lite/kinobadi?rjson=true&kinopoisk_id=6750360&title=Горничная&original_title=The+Housemaid", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v, body=%s", err, rec.Body.String())
	}
	if payload["type"] != "movie" {
		t.Fatalf("type: %#v", payload["type"])
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("data: %#v", payload["data"])
	}
	row := data[0].(map[string]any)
	if row["method"] != "play" {
		t.Fatalf("method: %#v", row["method"])
	}
	if got, _ := row["stream"].(string); got != "https://cdnr.example/master.m3u8?ha=1&t=42" {
		t.Fatalf("stream: %v", row["stream"])
	}
	subs, ok := row["subtitles"].([]any)
	if !ok || len(subs) != 1 {
		t.Fatalf("subtitles: %#v", row["subtitles"])
	}
}

func TestKinobadiIndexPageEmptyMissing(t *testing.T) {
	pleer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(kinobadiStubHTML))
	}))
	defer pleer.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinobadi: config.KinobadiSource{PleerHost: pleer.URL}},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"http://lampac.local/lite/kinobadi?rjson=true&kinopoisk_id=4664994&title=Foo", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)
	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("expected {} for missing kp, got: %s", rec.Body.String())
	}
}

func TestKinobadiIndexPageEmptyNoKP(t *testing.T) {
	cfg := config.Config{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinobadi?rjson=true&title=NoKP", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)
	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("expected {} when no KP, got: %s", rec.Body.String())
	}
}
