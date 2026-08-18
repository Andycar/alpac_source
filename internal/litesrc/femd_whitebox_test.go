package litesrc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// Realistic excerpt from api.femd.ws/embed/kp/{kp} — keep mirroring the
// upstream layout: makePlayer({ source: { hls, cc } }) inside a script block.
func femdSampleEmbedHTML(hlsURL string) string {
	return `<!DOCTYPE html><html><head><title>Горничная</title></head><body>
<script data-name="mk">
makePlayer({
    blocked: false,
    title: "Горничная",
    id: 830090,
    poster: 'data:image/gif;base64,xx',
    source: {
        dash: "https://cdnr.interkh.com/A/1238040.mpd?t=123",
        hls: "` + hlsURL + `",
        audio: {"names":["Рус. Дублированный","Eng.Original"],"order":[0,1]},
        cc: [{"url":"https://hye1eaipby4w.interkh.com/cc/eng.vtt?t=123","name":"Eng. full - 1"},{"url":"https://hye1eaipby4w.interkh.com/cc/rus.vtt?t=123","name":"Рус. полные - 5"}]
    },
    sections: []
});
</script>
</body></html>`
}

func TestFemdParseHLSAndSubtitles(t *testing.T) {
	f := &femdChecker{}
	hls := "https://cdnr.interkh.com/X/master.m3u8?ha=1&t=2"
	html := femdSampleEmbedHTML(hls)

	got := f.parseHLS(html)
	if got != hls {
		t.Fatalf("parseHLS: want %q, got %q", hls, got)
	}

	subs := f.parseSubtitles(html)
	if len(subs) != 2 {
		t.Fatalf("parseSubtitles: want 2, got %d (%+v)", len(subs), subs)
	}
	if subs[0]["label"] != "Eng. full - 1" || !strings.HasSuffix(subs[0]["url"], "/eng.vtt?t=123") {
		t.Fatalf("parseSubtitles: bad first entry %+v", subs[0])
	}
	if subs[1]["label"] != "Рус. полные - 5" {
		t.Fatalf("parseSubtitles: bad second label %+v", subs[1])
	}

	if title := f.parseTitle(html); title != "Горничная" {
		t.Fatalf("parseTitle: want Горничная, got %q", title)
	}
}

func TestFemdEmptyStubPageRejected(t *testing.T) {
	stub := `<html><head><title>Title</title></head><body>missing</body></html>`
	if !femdEmptyTitleRe.MatchString(stub) {
		t.Fatal("stub page should match femdEmptyTitleRe")
	}
	real := `<html><head><title>Дюна</title></head><body></body></html>`
	if femdEmptyTitleRe.MatchString(real) {
		t.Fatal("real title should not match stub regex")
	}
}

func TestFemdCheckSearchHit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/embed/kp/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/99999999") {
			// Mimic real 404 stub: HTTP 200, generic <title>Title</title>, no m3u8.
			_, _ = w.Write([]byte(`<html><head><title>Title</title></head><body>nope</body></html>`))
			return
		}
		_, _ = w.Write([]byte(femdSampleEmbedHTML("https://cdnr.example/master.m3u8?t=1")))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Femd: config.FemdSource{Host: upstream.URL}},
	}
	f := NewFemdChecker(cfg)

	reqHit := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/femd?checksearch=true&kinopoisk_id=86126", nil)
	if !f.checkSearch(reqHit) {
		t.Fatal("expected checksearch=true for known kp")
	}

	reqMiss := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/femd?checksearch=true&kinopoisk_id=99999999", nil)
	if f.checkSearch(reqMiss) {
		t.Fatal("expected checksearch=false for missing kp")
	}

	// Second hit should be served from cache (no network call) — easiest way to
	// check is to take the server down and confirm result is the same.
	upstream.Close()
	if !f.checkSearch(reqHit) {
		t.Fatal("expected cached checksearch=true after upstream shutdown")
	}
}
