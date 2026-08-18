package litesrc

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Unit tests for pure helpers ----------------------------------------

func TestZonaDecodeNextSSR(t *testing.T) {
	html := `<script>self.__next_f.push([1,"a:1\nb:\"c\""])</script>` +
		`<script>self.__next_f.push([1,"d:2"])</script>`
	got := zonaDecodeNextSSR(html)
	want := "a:1\nb:\"c\"" + "d:2"
	if got != want {
		t.Errorf("decode: got %q want %q", got, want)
	}
}

func TestZonaExtractJSONArray(t *testing.T) {
	blob := `before "seasons":[{"slug":"1","part":1,"episode_count":9},` +
		`{"slug":"2","episode_count":7}] after`
	got := zonaExtractJSONArray(blob, `"seasons":`)
	want := `[{"slug":"1","part":1,"episode_count":9},{"slug":"2","episode_count":7}]`
	if got != want {
		t.Errorf("extract: got %q want %q", got, want)
	}
	// With nested arrays.
	nested := `"foo":[[1,2],[3,4]] tail`
	if got := zonaExtractJSONArray(nested, `"foo":`); got != `[[1,2],[3,4]]` {
		t.Errorf("nested: %q", got)
	}
	// Missing key returns empty.
	if got := zonaExtractJSONArray(blob, `"missing":`); got != "" {
		t.Errorf("missing should be empty, got %q", got)
	}
}

func TestZonaExtractorFromPath(t *testing.T) {
	cases := map[string]string{
		"/lite/zona":              "",
		"/lite/zona-mobilink":     "MOBILINK",
		"/lite/zona-hdvb":         "HDVB",
		"/lite/zona-filmix/video": "FILMIX",
		"/lite/zona-takedwn":      "TAKEDWN",
		"/lite/unknown":           "",
	}
	for path, want := range cases {
		if got := zonaExtractorFromPath(path); got != want {
			t.Errorf("%s → got %q want %q", path, got, want)
		}
	}
}

func TestZonaQualityRank(t *testing.T) {
	if zonaQualityRank("1080p") <= zonaQualityRank("480p") {
		t.Error("1080p should rank above 480p")
	}
	if zonaQualityRank("HLS") <= zonaQualityRank("360p") {
		t.Error("HLS should rank above 360p")
	}
	if zonaQualityRank("2160p") <= zonaQualityRank("1080p") {
		t.Error("2160p should rank above 1080p")
	}
}

func TestZonaVoiceName(t *testing.T) {
	// Plain translation is prefixed with the extractor name so users can
	// tell different sources apart when they appear side-by-side.
	if got := zonaVoiceName("MOBILINK", "Русский язык"); got != "MOBILINK · Русский язык" {
		t.Errorf("plain: %q", got)
	}
	// JSON array of voices flattens to the first non-empty value.
	arr := `[{"0":"Пифагор"},{"1":"Так Треба"}]`
	if got := zonaVoiceName("TAKEDWN", arr); got != "TAKEDWN · Пифагор" {
		t.Errorf("array: %q", got)
	}
	// Empty translation falls back to the extractor name alone.
	if got := zonaVoiceName("HDVB", ""); got != "HDVB" {
		t.Errorf("empty: %q", got)
	}
	// Empty extractor falls back to "Zona" prefix.
	if got := zonaVoiceName("", "Русский язык"); got != "Zona · Русский язык" {
		t.Errorf("empty extractor: %q", got)
	}
}

func TestZonaFilterExtractor(t *testing.T) {
	streams := []zonaStream{
		{Extractor: "MOBILINK", URL: "a"},
		{Extractor: "HDVB", URL: "b"},
		{Extractor: "HDVB", URL: "c"},
		{Extractor: "FILMIX", URL: "d"},  // unsupported — dropped in aggregator
		{Extractor: "TAKEDWN", URL: "e"}, // supported via Go cipher port — kept
	}
	// Aggregator mode drops only unsupported extractors (FILMIX).
	// MOBILINK + 2×HDVB + TAKEDWN = 4 remain.
	if got := zonaFilterExtractor(streams, ""); len(got) != 4 {
		t.Errorf("aggregator: expected 4, got %d", len(got))
	}
	// Explicit filter bypasses the supported check — useful if someone
	// wires up an unsupported extractor later.
	if got := zonaFilterExtractor(streams, "HDVB"); len(got) != 2 {
		t.Errorf("HDVB: %d", len(got))
	}
	// Case-insensitive.
	if got := zonaFilterExtractor(streams, "mobilink"); len(got) != 1 {
		t.Errorf("mobilink ci: %d", len(got))
	}
	// Unknown filter empty.
	if got := zonaFilterExtractor(streams, "UNKNOWN"); len(got) != 0 {
		t.Errorf("unknown: %d", len(got))
	}
	// Explicit FILMIX filter returns the unsupported stream anyway — the
	// aggregator's `supported` check is only in place when no explicit
	// extractor is requested.
	if got := zonaFilterExtractor(streams, "FILMIX"); len(got) != 1 {
		t.Errorf("explicit FILMIX: %d", len(got))
	}
}

// TestZonaParseStreams parses a representative getStreams output — one
// chunk per extractor — and verifies we keep MOBILINK+HDVB while dropping
// FILMIX (error) and TAKEDWN (urlTransform present).
func TestZonaParseStreams(t *testing.T) {
	chunks := []string{
		// FILMIX failure — must be skipped.
		`{"videoStreams":[],"extractorType":{"id":3,"name":"FILMIX"},"error":"Fail to fetch"}`,
		// MOBILINK — kept.
		`{"videoStreams":[{"source":{"url":"https://dlcache4.vibio.tv/abc/output.lq.mp4/index-v1-a1.m3u8"},"translation":"Русский язык","resolution":"LQ"}],"extractorType":{"id":1,"name":"MOBILINK"}}`,
		// TAKEDWN with urlTransform — must be skipped.
		`{"videoStreams":[{"source":{"url":"https://interkh.com/foo/master.m3u8"},"translation":"[{\"0\":\"Пифагор\"}]","resolution":"HLS","parameters":{"urlTransform":"return function(){};"}}],"extractorType":{"id":32,"name":"TAKEDWN"}}`,
		// HDVB — 3 qualities kept.
		`{"videoStreams":[` +
			`{"source":{"url":"https://cdn.fotpro.com/360/index.m3u8"},"translation":"LostFilm","resolution":"360p"},` +
			`{"source":{"url":"https://cdn.fotpro.com/720/index.m3u8"},"translation":"LostFilm","resolution":"720p"},` +
			`{"source":{"url":"https://cdn.fotpro.com/1080/index.m3u8"},"translation":"LostFilm","resolution":"1080p"}` +
			`],"extractorType":{"id":34,"name":"HDVB"}}`,
	}
	got := zonaParseStreams(chunks)
	if len(got) != 4 {
		t.Fatalf("expected 4 streams (1 MOBILINK + 3 HDVB), got %d", len(got))
	}
	extractors := map[string]int{}
	for _, s := range got {
		extractors[s.Extractor]++
	}
	if extractors["MOBILINK"] != 1 || extractors["HDVB"] != 3 {
		t.Errorf("extractor mix: %v", extractors)
	}
	if extractors["FILMIX"] != 0 || extractors["TAKEDWN"] != 0 {
		t.Errorf("unwanted extractors kept: %v", extractors)
	}
}

// TestZonaGroupStreamsSortsByQuality verifies that zonaGroupStreams keeps
// voices in first-seen order but sorts streams within a voice by quality.
func TestZonaGroupStreamsSortsByQuality(t *testing.T) {
	streams := []zonaStream{
		{Extractor: "HDVB", Translation: "LostFilm", Resolution: "360p", URL: "a"},
		{Extractor: "HDVB", Translation: "LostFilm", Resolution: "1080p", URL: "b"},
		{Extractor: "HDVB", Translation: "LostFilm", Resolution: "720p", URL: "c"},
		{Extractor: "HDVB", Translation: "BaibaKo", Resolution: "480p", URL: "d"},
	}
	voices := zonaGroupStreams(streams)
	if len(voices) != 2 {
		t.Fatalf("voices: %d", len(voices))
	}
	if voices[0].name != "HDVB · LostFilm" || voices[1].name != "HDVB · BaibaKo" {
		t.Errorf("voice order: %v", []string{voices[0].name, voices[1].name})
	}
	if voices[0].streams[0].Resolution != "1080p" {
		t.Errorf("first voice best: %q", voices[0].streams[0].Resolution)
	}
}

// --- Integration-ish test: meta lookup via stub HTTP server --------------

// TestZonaLookupSlugViaStub runs lookupSlug against a local HTTP server that
// returns a minimal Next.js SSR blob — the same shape Zona emits for its
// `/movies?q=` listing pages.
func TestZonaLookupSlugViaStub(t *testing.T) {
	ssr := `{"slug":"other","kp_id":111},{"slug":"krik-7","title":"X","kp_id":5364826},{"slug":"third","kp_id":222}`
	html := `<!DOCTYPE html><html><body>` +
		`<script>self.__next_f.push([1,"a:1\n` + stringEscape(ssr) + `"])</script>` +
		`</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/movies") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	z := &zonaChecker{
		client:     srv.Client(),
		metaClient: srv.Client(),
		zonaHost:   srv.URL,
	}
	got := z.lookupSlug(5364826, "Scream 7", "movies")
	if got != "krik-7" {
		t.Errorf("slug: got %q want krik-7", got)
	}
	// Unknown kp ID → empty.
	if got := z.lookupSlug(999999, "Scream 7", "movies"); got != "" {
		t.Errorf("unknown kp should be empty, got %q", got)
	}
}

func TestZonaFetchMetaViaStub(t *testing.T) {
	// A single SSR blob with top-level movie data + seasons array.
	ssrRaw := `{"slug":"test","title":"Test Series","title_original":"Test Series",` +
		`"kp_id":123,"seasons":[{"slug":"1","part":1,"episode_count":3},{"slug":"2","part":2,"episode_count":4}]}`
	html := `<script>self.__next_f.push([1,"` + stringEscape(ssrRaw) + `"])</script>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	z := &zonaChecker{client: srv.Client(), metaClient: srv.Client(), zonaHost: srv.URL}
	meta, err := z.fetchMeta("test", "tvseries")
	if err != nil {
		t.Fatal(err)
	}
	if meta.KpID != 123 {
		t.Errorf("kp: %d", meta.KpID)
	}
	if !meta.IsSerial {
		t.Error("should be serial")
	}
	if len(meta.Seasons) != 2 {
		t.Fatalf("seasons: %d", len(meta.Seasons))
	}
	if meta.Seasons[0].EpisodeCount != 3 || meta.Seasons[1].EpisodeCount != 4 {
		t.Errorf("ep counts: %+v", meta.Seasons)
	}
}

// stringEscape turns a plain string into the JS-literal form that
// self.__next_f.push uses (double quotes escaped).
func stringEscape(s string) string {
	b, _ := stdjson.Marshal(s)
	inner := string(b[1 : len(b)-1]) // strip outer quotes
	return inner
}
