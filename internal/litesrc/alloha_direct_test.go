package litesrc

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func urlEscape(s string) string { return url.QueryEscape(s) }

const allohaTestPlaylist = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=2692000,RESOLUTION=1280x720
index-f1-v1-a1.m3u8
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",URI="audio-eng.m3u8"
https://ec-39-54-r200.vkvideo.cloud/0/SIGNED/seg-1.ts
`

func allohaTestChecker(direct bool) *allohaChecker {
	return &allohaChecker{
		linkHost:     "https://rerun-as.stravers.live",
		directStream: direct,
	}
}

// Direct mode must hand the player the CDN's own URLs: that's the whole point —
// the video bytes stop flowing through us. Relative entries have to be resolved
// against the upstream playlist, since the client can't know the CDN base.
func TestAllohaRewriteM3UDirectKeepsCDNURLs(t *testing.T) {
	a := allohaTestChecker(true)
	r := httptest.NewRequest("GET", "http://lampac.local/lite/alloha/stream.m3u8?token_movie=tm&t=77&direct=1", nil)
	upstream := "https://ec-39-54-r200.vkvideo.cloud/0/SIGNED/master.m3u8"

	out := a.allohaRewriteM3U(allohaTestPlaylist, r, "tm", "auto", upstream)

	// Media goes straight to the CDN...
	if !strings.Contains(out, "https://ec-39-54-r200.vkvideo.cloud/0/SIGNED/seg-1.ts") {
		t.Errorf("segment was not handed out directly:\n%s", out)
	}
	// ...but manifests keep flowing through us: the CDN wants a per-session Borth
	// on .m3u8 requests, which a client cannot mint (measured: variant playlist
	// fetched directly with the full header set still answers 403).
	if !strings.Contains(out, "&sub="+urlEscape("index-f1-v1-a1.m3u8")) {
		t.Errorf("variant playlist must stay proxied:\n%s", out)
	}
	if !strings.Contains(out, `URI="http://lampac.local/lite/alloha/stream.m3u8`) {
		t.Errorf("rendition URI must stay proxied:\n%s", out)
	}
}

// Without the client opt-in (or with the admin switch off) nothing may change —
// a browser that can't set Origin/Referer would get nothing but 403s.
func TestAllohaRewriteM3UProxyByDefault(t *testing.T) {
	upstream := "https://ec-39-54-r200.vkvideo.cloud/0/SIGNED/master.m3u8"

	cases := []struct {
		name   string
		direct bool
		query  string
	}{
		{"no client opt-in", true, "token_movie=tm&t=77"},
		{"admin switch off", false, "token_movie=tm&t=77&direct=1"},
	}
	for _, tc := range cases {
		a := allohaTestChecker(tc.direct)
		r := httptest.NewRequest("GET", "http://lampac.local/lite/alloha/stream.m3u8?"+tc.query, nil)
		out := a.allohaRewriteM3U(allohaTestPlaylist, r, "tm", "auto", upstream)
		if !strings.Contains(out, "/lite/alloha/stream.m3u8") || !strings.Contains(out, "&sub=") {
			t.Errorf("%s: expected proxied playlist, got:\n%s", tc.name, out)
		}
		if strings.Contains(out, "\nhttps://ec-39-54-r200.vkvideo.cloud") {
			t.Errorf("%s: leaked a raw CDN url:\n%s", tc.name, out)
		}
	}
}

// A malformed upstream URL must not silently produce half-rewritten garbage —
// it falls back to the proxy path.
func TestAllohaRewriteM3UDirectFallsBackWithoutBase(t *testing.T) {
	a := allohaTestChecker(true)
	r := httptest.NewRequest("GET", "http://lampac.local/lite/alloha/stream.m3u8?token_movie=tm&direct=1", nil)

	out := a.allohaRewriteM3U(allohaTestPlaylist, r, "tm", "auto", "::not-a-url::")
	if !strings.Contains(out, "&sub=") {
		t.Fatalf("expected proxy fallback when upstream base is unusable:\n%s", out)
	}
}

func TestAllohaDirectHeaders(t *testing.T) {
	a := allohaTestChecker(true)

	off := httptest.NewRequest("GET", "http://lampac.local/lite/alloha/video?token_movie=tm", nil)
	if h := a.directHeaders(off, "tm", "77", 0, 0); h != nil {
		t.Fatalf("headers handed out without client opt-in: %v", h)
	}

	// With no edge hash and no guard token there is nothing to authorise with;
	// staying on the proxy beats shipping the client a guaranteed 403.
	on := httptest.NewRequest("GET", "http://lampac.local/lite/alloha/video?token_movie=tm&direct=1", nil)
	if h := a.directHeaders(on, "tm", "77", 0, 0); h != nil {
		for _, k := range []string{"Accepts-Controls", "Authorizations", "Referer", "Origin", "User-Agent"} {
			if strings.TrimSpace(h[k]) == "" {
				t.Errorf("header %q missing/empty: %v", k, h)
			}
		}
		if h["Referer"] != "https://rerun-as.stravers.live/" || h["Origin"] != "https://rerun-as.stravers.live" {
			t.Errorf("Referer/Origin must name the player host, got %q / %q", h["Referer"], h["Origin"])
		}
	}
}
