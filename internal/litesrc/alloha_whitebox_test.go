package litesrc

import (
	"strings"
	"testing"
)

func TestAllohaIframeURLBuilders(t *testing.T) {
	a := &allohaChecker{linkHost: "https://play.alcopa.cc", token: "TOK"}

	// Movie: token_movie + token, no season params.
	got := a.allohaIframeURL("TM", "", 0, 0)
	want := "https://play.alcopa.cc/?token_movie=TM&token=TOK"
	if got != want {
		t.Errorf("movie iframe url:\n got  %q\n want %q", got, want)
	}

	// Serial: translation + season/episode appended.
	got = a.allohaIframeURL("TM", "56", 2, 3)
	if !strings.Contains(got, "translation=56") || !strings.Contains(got, "season=2") || !strings.Contains(got, "episode=3") {
		t.Errorf("serial iframe url missing params: %q", got)
	}

	// capi variant wraps the SAME url in the iframe:// scheme (drops https://) so
	// the marker survives the capi ByQuality merge and capiSameOrigin passthrough.
	cap := a.allohaCapiIframeURL("TM", "", 0, 0)
	if !strings.HasPrefix(cap, allohaIframeScheme) {
		t.Errorf("capi iframe url missing scheme: %q", cap)
	}
	if strings.Contains(cap, "https://") || strings.Contains(cap, "http://") {
		t.Errorf("capi iframe url must not keep http(s) scheme: %q", cap)
	}
	if want := allohaIframeScheme + "play.alcopa.cc/?token_movie=TM&token=TOK"; cap != want {
		t.Errorf("capi iframe url:\n got  %q\n want %q", cap, want)
	}
}

func TestAllohaNonVideoContentType(t *testing.T) {
	garbage := []string{
		"application/json",
		"application/json; charset=utf-8",
		"text/html",
		"text/plain",
		"TEXT/HTML; charset=windows-1251",
	}
	for _, ct := range garbage {
		if !allohaNonVideoContentType(ct) {
			t.Errorf("expected %q to be flagged as non-video garbage", ct)
		}
	}
	video := []string{
		"",                         // unknown → treat as video, don't drop
		"video/mp2t",               // HLS TS segment
		"application/octet-stream", // common for segments
		"video/mp4",
		"binary/octet-stream",
	}
	for _, ct := range video {
		if allohaNonVideoContentType(ct) {
			t.Errorf("expected %q to be treated as a valid segment", ct)
		}
	}
}
