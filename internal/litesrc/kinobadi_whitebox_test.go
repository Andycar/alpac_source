package litesrc

import (
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

func TestKinobadiPleerOnParsing(t *testing.T) {
	// id_file extracted from femd iframe
	m := kinobadiFemdEmbedRe.FindStringSubmatch(kinobadiPleerOnHTML)
	if len(m) < 2 || m[1] != "86126" {
		t.Fatalf("expected id_file 86126, got match=%v", m)
	}
	// stloadi 4K URL is captured for future use
	st := kinobadiStloadiRe.FindStringSubmatch(kinobadiPleerOnHTML)
	if len(st) < 2 || !strings.Contains(st[1], "hd_4K") {
		t.Fatalf("stloadi URL missing 4K marker: %v", st)
	}
}

func TestKinobadiEmbedHLSAndSubs(t *testing.T) {
	if hls := kinobadiParseHLS(kinobadiFemdEmbedHTML); hls != "https://cdnr.example/master.m3u8?ha=1&t=42" {
		t.Fatalf("parseHLS bad value: %q", hls)
	}
	subs := kinobadiParseSubtitles(kinobadiFemdEmbedHTML)
	if len(subs) != 1 || subs[0]["label"] != "Рус. полные - 5" {
		t.Fatalf("parseSubtitles bad: %+v", subs)
	}
}

func TestKinobadiCheckSearchHit(t *testing.T) {
	pleer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/hd_pars/pleer_on.php") {
			http.NotFound(w, r)
			return
		}
		kp := r.URL.Query().Get("kp")
		if kp == "" || kp == "99999999" {
			_, _ = w.Write([]byte(kinobadiStubHTML))
			return
		}
		_, _ = w.Write([]byte(kinobadiPleerOnHTML))
	}))
	defer pleer.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{Kinobadi: config.KinobadiSource{
			PleerHost: pleer.URL,
			FemdHost:  "https://api.femd.ws",
			Consumer:  "kinotik.top",
		}},
	}
	k := NewKinobadiChecker(cfg)

	reqHit := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinobadi?checksearch=true&kinopoisk_id=6750360", nil)
	if !k.checkSearch(reqHit) {
		t.Fatal("expected checksearch=true for known kp")
	}

	reqMiss := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinobadi?checksearch=true&kinopoisk_id=99999999", nil)
	if k.checkSearch(reqMiss) {
		t.Fatal("expected checksearch=false for stub kp")
	}
}
