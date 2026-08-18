package litesrc

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// Each obrut host answers only for its own site id, and 30bf3790 (site AN) has
// been 404-ing for every id — a config that still names it as the primary must
// not cost every lookup a wasted hop.
func TestVideodbObrutEndpointsPairHostWithSiteAndDemoteDead(t *testing.T) {
	eps := videodbBuildObrutEndpoints("https://30bf3790.obrut.show", "https://d6dd387e.obrut.show")

	if len(eps) < 3 {
		t.Fatalf("want all known catalogues, got %+v", eps)
	}
	if eps[0].Host != "https://d6dd387e.obrut.show" || eps[0].Site != "cjM" {
		t.Fatalf("first endpoint = %+v, want the live d6dd387e/cjM", eps[0])
	}
	last := eps[len(eps)-1]
	if last.Host != "https://30bf3790.obrut.show" || last.Site != "AN" {
		t.Fatalf("last endpoint = %+v, want the dead 30bf3790/AN", last)
	}
	// The AO catalogue is always reachable as a candidate, even unconfigured.
	var hasAO bool
	for _, ep := range eps {
		if ep.Site == "AO" && ep.Host == zetflixDBDefaultHost {
			hasAO = true
		}
	}
	if !hasAO {
		t.Fatalf("AO catalogue missing from candidates: %+v", eps)
	}

	// Hosts must never be listed twice, whatever the config repeats.
	dupes := videodbBuildObrutEndpoints("d6dd387e.obrut.show", "https://d6dd387e.obrut.show/")
	seen := map[string]int{}
	for _, ep := range dupes {
		seen[videodbObrutHostname(ep.Host)]++
	}
	for host, n := range seen {
		if n != 1 {
			t.Fatalf("host %s listed %d times: %+v", host, n, dupes)
		}
	}
}

func TestVideodbObrutCandidatesPreferLastWorking(t *testing.T) {
	v := &videodbChecker{obrutEndpoints: videodbBuildObrutEndpoints("https://30bf3790.obrut.show")}
	v.lastObrutOK.Store(zetflixDBDefaultHost)

	got := v.obrutCandidates()
	if got[0].Host != zetflixDBDefaultHost || got[0].Site != "AO" {
		t.Fatalf("first candidate = %+v, want the last-working AO endpoint", got[0])
	}
	if len(got) != len(v.obrutEndpoints) {
		t.Fatalf("candidates dropped entries: %d vs %d", len(got), len(v.obrutEndpoints))
	}
}

// ZetflixDB is the videodb engine pinned to one catalogue: no videodb.cloud, no
// collaps fallback (those would make it a slower duplicate of videodb).
func TestNewZetflixDBCheckerIsAOOnly(t *testing.T) {
	z := NewZetflixDBChecker(config.Config{})

	if z.plugin != "zetflixdb" {
		t.Fatalf("plugin=%q, want zetflixdb", z.plugin)
	}
	if len(z.obrutEndpoints) != 1 {
		t.Fatalf("endpoints=%+v, want exactly the AO catalogue", z.obrutEndpoints)
	}
	if z.obrutEndpoints[0].Site != "AO" || z.obrutEndpoints[0].Host != zetflixDBDefaultHost {
		t.Fatalf("endpoint=%+v, want %s/AO", z.obrutEndpoints[0], zetflixDBDefaultHost)
	}
	if z.videodbCloud != "" || z.fallbackHost != "" {
		t.Fatalf("cloud=%q fallback=%q, want both disabled", z.videodbCloud, z.fallbackHost)
	}

	custom := NewZetflixDBChecker(config.Config{
		Online: config.OnlineConfig{ZetflixDB: config.ZetflixDBSource{Host: "mirror.example"}},
	})
	if custom.obrutEndpoints[0].Host != "https://mirror.example" {
		t.Fatalf("configured host ignored: %+v", custom.obrutEndpoints[0])
	}
}

// The "auto" link is the CDN's HLS master playlist: it answers with
// application/vnd.apple.mpegurl no matter what extension we advertise. Handing
// it to the player as .mp4 — and, worse, making it the default "url" — is what
// produced «видео не найдено или повреждено».
func TestVideodbManifestAutoStaysHLSAndNeverDefaultsInMP4Mode(t *testing.T) {
	const cdn = "https://cdn-54243ba5.obrut.show/stream/AO/abc/"
	links := map[string]string{
		"FHD":  cdn + "fhd",
		"HD":   cdn + "hd",
		"SD":   cdn + "sd",
		"auto": cdn + "auto",
	}
	linksJSON, err := stdjson.Marshal(links)
	if err != nil {
		t.Fatal(err)
	}

	call := func(useHLS bool) map[string]any {
		t.Helper()
		v := &videodbChecker{plugin: "zetflixdb", useHLS: useHLS}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			"http://x/lite/zetflixdb/manifest?links="+url.QueryEscape(string(linksJSON)), nil)
		v.manifest(rec, req)

		var out map[string]any
		if err := stdjson.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %v (%s)", err, rec.Body.String())
		}
		return out
	}

	t.Run("mp4 mode", func(t *testing.T) {
		out := call(false)
		q, _ := out["quality"].(map[string]any)
		auto, _ := q["auto"].(string)
		if !strings.Contains(auto, "/manifest.m3u8?") {
			t.Errorf("auto served as %q, want the .m3u8 endpoint", auto)
		}
		for _, label := range []string{"FHD", "HD", "SD"} {
			if got, _ := q[label].(string); !strings.Contains(got, "/manifest.mp4?") {
				t.Errorf("%s served as %q, want the .mp4 endpoint", label, got)
			}
		}
		// The default must be real MP4 — and the best one, not a random map entry.
		def, _ := out["url"].(string)
		if !strings.Contains(def, "/manifest.mp4?") {
			t.Errorf("default url = %q, want an .mp4 stream", def)
		}
		if def != q["FHD"] {
			t.Errorf("default url = %q, want the FHD entry %q", def, q["FHD"])
		}
	})

	t.Run("hls mode keeps auto as default", func(t *testing.T) {
		out := call(true)
		q, _ := out["quality"].(map[string]any)
		if def, _ := out["url"].(string); def != q["auto"] {
			t.Errorf("default url = %q, want the auto master playlist %q", def, q["auto"])
		}
	})
}

func TestVideodbLabelRank(t *testing.T) {
	cases := map[string]int{"4K": 2160, "FHD": 1080, "HD": 720, "SD": 480, "1080p": 1080, "720p": 720, "": 0}
	for label, want := range cases {
		if got := videodbLabelRank(label); got != want {
			t.Errorf("videodbLabelRank(%q) = %d, want %d", label, got, want)
		}
	}
}

// Routes are per-plugin: the ZetflixDB handler serves /lite/zetflixdb and must
// not answer for /lite/videodb (and vice versa).
func TestZetflixDBHandlerRoutesOnItsOwnPath(t *testing.T) {
	handler := NewZetflixDBChecker(config.Config{}).Handle(config.Config{}, nil)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "http://x/lite/videodb?kinopoisk_id=301", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("videodb path on zetflixdb handler: status=%d, want 501", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "http://x/lite/zetflixdb", nil))
	if rec.Code == http.StatusNotImplemented {
		t.Fatalf("own path rejected: %s", rec.Body.String())
	}

	// The self-links it emits must point back at its own route.
	if body := rec.Body.String(); strings.Contains(body, "/lite/videodb") {
		t.Fatalf("zetflixdb emitted a videodb self-link: %s", body)
	}
}
