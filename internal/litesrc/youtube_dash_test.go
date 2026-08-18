package litesrc

import (
	stdjson "encoding/json"
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestYtByteRangeFlexParse(t *testing.T) {
	// yt-dlp emits index_range/init_range as objects with start/end either numeric or string.
	for _, raw := range []string{
		`{"start":0,"end":740}`,
		`{"start":"0","end":"740"}`,
	} {
		var r ytByteRange
		if err := stdjson.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if got := r.rangeAttr(); got != "0-740" {
			t.Errorf("rangeAttr(%s) = %q, want 0-740", raw, got)
		}
	}
	// Missing / zero-end → no usable range.
	var empty ytByteRange
	if got := (&empty).rangeAttr(); got != "" {
		t.Errorf("empty rangeAttr() = %q, want \"\"", got)
	}
	if got := (*ytByteRange)(nil).rangeAttr(); got != "" {
		t.Errorf("nil rangeAttr() = %q, want \"\"", got)
	}
}

func TestIsByteRange(t *testing.T) {
	ok := []string{"0-740", "1234-56789"}
	bad := []string{"", "-5", "5-", "abc", "0-740\"><script>", "0--5", "0-7 40", "x-y"}
	for _, s := range ok {
		if !isByteRange(s) {
			t.Errorf("isByteRange(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if isByteRange(s) {
			t.Errorf("isByteRange(%q) = true, want false", s)
		}
	}
}

func TestYtBestLabelCapped(t *testing.T) {
	q := func(labels ...string) map[string]string {
		m := map[string]string{}
		for _, l := range labels {
			m[l] = "u"
		}
		return m
	}
	cases := []struct {
		name   string
		quals  map[string]string
		cap    int
		expect string
	}{
		{"cap1080 picks 1080 over 4k", q("2160p", "1440p", "1080p", "720p", "360p"), 1080, "1080p"},
		{"cap1080 best available below cap", q("720p", "480p", "360p"), 1080, "720p"},
		{"cap1080 single low", q("360p"), 1080, "360p"},
		{"no cap picks highest", q("2160p", "1080p", "720p"), 0, "2160p"},
		{"all above cap → fall back to best (never empty)", q("2160p", "1440p"), 1080, "2160p"},
		{"empty", q(), 1080, ""},
	}
	for _, c := range cases {
		if got := ytBestLabelCapped(c.quals, c.cap); got != c.expect {
			t.Errorf("%s: ytBestLabelCapped(cap=%d) = %q, want %q", c.name, c.cap, got, c.expect)
		}
	}
}

func TestParseContentRangeTotal(t *testing.T) {
	cases := map[string]int64{
		"bytes 0-10485759/52428800": 52428800,
		"bytes 0-100/200":           200,
		"bytes 0-100/*":             -1,
		"":                          -1,
		"garbage":                   -1,
		"bytes */1234":              1234,
	}
	for cr, want := range cases {
		if got := parseContentRangeTotal(cr); got != want {
			t.Errorf("parseContentRangeTotal(%q) = %d, want %d", cr, got, want)
		}
	}
}

func TestParseContentRangeStart(t *testing.T) {
	cases := map[string]int64{
		"bytes 0-10485759/52428800":        0,
		"bytes 10485760-20971519/52428800": 10485760,
		"bytes */1234":                     -1,
		"":                                 -1,
		"garbage":                          -1,
	}
	for cr, want := range cases {
		if got := parseContentRangeStart(cr); got != want {
			t.Errorf("parseContentRangeStart(%q) = %d, want %d", cr, got, want)
		}
	}
}

func TestDashSegmentBase(t *testing.T) {
	out := dashSegmentBase("100-200", "0-99")
	if !strings.Contains(out, `<SegmentBase indexRange="100-200">`) || !strings.Contains(out, `<Initialization range="0-99"/>`) {
		t.Errorf("dashSegmentBase missing expected tags: %q", out)
	}
	// Malformed/missing → empty (so we never emit a broken SegmentBase, and never inject XML).
	for _, p := range [][2]string{{"", ""}, {"0-740", ""}, {`0"><x`, "0-99"}, {"100-200", "junk"}} {
		if got := dashSegmentBase(p[0], p[1]); got != "" {
			t.Errorf("dashSegmentBase(%q,%q) = %q, want \"\"", p[0], p[1], got)
		}
	}
}

// The whole point: the generated MPD must be well-formed XML AND carry a SegmentBase so MSE players
// can index the fragmented MP4 (the bug was a bare <BaseURL> with no SegmentBase → DASH wouldn't play).
func TestHandleDashMPDHasSegmentBase(t *testing.T) {
	y := &YoutubeChecker{}
	req := httptest.NewRequest("GET", "/lite/youtube/dash.mpd?"+
		"video=https://tv.example.com/proxy/VTOK&audio=https://tv.example.com/proxy/ATOK"+
		"&height=720&vcodec=avc1.4d401f&acodec=mp4a.40.2&vbr=1707698&abr=128000"+
		"&vir=0-739&vix=740-1200&air=0-599&aix=600-900", nil)
	w := httptest.NewRecorder()
	y.HandleDashMPD(w, req)

	body := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, body)
	}
	if err := xml.Unmarshal([]byte(body), new(struct{})); err != nil {
		t.Fatalf("MPD is not well-formed XML: %v\n%s", err, body)
	}
	for _, want := range []string{
		`<SegmentBase indexRange="740-1200">`,
		`<Initialization range="0-739"/>`,
		`<SegmentBase indexRange="600-900">`,
		`<Initialization range="0-599"/>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("MPD missing %q\n%s", want, body)
		}
	}
}
