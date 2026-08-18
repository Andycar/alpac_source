package proxyapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/hlsvalidate"
)

// Structural invariants of the rewriter, checked on realistic playlists.
//
// The existing rewriteM3U tests assert that specific URLs come out right. This
// one asserts what they cannot: that nothing was LOST. Both playlist incidents
// we shipped — the hashless EXT-X-MAP and the dropped EXT-X-START — were
// losses, invisible to a test that only greps for the URL it expects.
func TestRewriteM3UPreservesPlaylistStructure(t *testing.T) {
	const vodFMP4 = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:10
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-START:TIME-OFFSET=0
#EXT-X-MAP:URI="init-v1-a1.mp4"
#EXTINF:10.0,
seg-1-v1-a1.m4s
#EXTINF:10.0,
seg-2-v1-a1.m4s
#EXT-X-ENDLIST
`

	const vodAES = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:6
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-KEY:METHOD=AES-128,URI="key.key",IV=0x00000000000000000000000000000001
#EXTINF:6.0,
https://cdn.example/1.ts
#EXTINF:6.0,
https://cdn.example/2.ts
#EXT-X-ENDLIST
`

	const master = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="ru",DEFAULT=YES,URI="audio/ru.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,CODECS="avc1.42c01e,mp4a.40.2",AUDIO="aud"
360/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6000000,CODECS="avc1.640028,mp4a.40.2",AUDIO="aud"
1080/index.m3u8
`

	const liveDiscontinuity = `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:1200
#EXTINF:6.0,
https://cdn.example/1200.ts
#EXT-X-DISCONTINUITY
#EXTINF:6.0,
https://cdn.example/1201.ts
`

	cases := []struct {
		name   string
		src    string
		plugin string
	}{
		{"vod-fmp4", vodFMP4, "kodik"},
		{"vod-fmp4-filmix", vodFMP4, "filmix"}, // hash inheritance path
		{"vod-aes-key", vodAES, "kodik"},
		{"master-with-audio-group", master, "kodik"},
		{"live-with-discontinuity", liveDiscontinuity, "kodik"},
	}

	h := New(config.Config{}, nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/x.m3u8", nil)
			meta := linkMeta{
				reqIP:    "127.0.0.1",
				verifyIP: true,
				plugin:   tc.plugin,
				baseURI:  "https://cdn.example/path/index.m3u8?hash=FHASH",
			}

			got := h.rewriteM3U(tc.src, req, meta)

			issues := hlsvalidate.CompareRewrite(tc.src, got, hlsvalidate.CompareOpts{
				ProxyPrefix: "http://lampac.local/proxy/",
			})
			for _, is := range issues {
				t.Errorf("%s\n--- upstream ---\n%s--- rewritten ---\n%s", is, tc.src, got)
			}
		})
	}
}

// Manifest-only mode is a deliberate departure: segments are handed to the
// client as direct CDN links. Everything else must still survive — especially
// the init segment, which is repaired in place rather than proxied.
func TestRewriteM3UManifestOnlyPreservesStructure(t *testing.T) {
	const src = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:10
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-MAP:URI="init-v1-a1.mp4"
#EXTINF:10.0,
seg-1-v1-a1.m4s?hash=FHASH
#EXTINF:10.0,
seg-2-v1-a1.m4s?hash=FHASH
#EXT-X-ENDLIST
`
	h := New(config.Config{}, nil)
	h.manifestOnlyPlugins = map[string]bool{"filmix": true}

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/x.m3u8", nil)
	meta := linkMeta{
		reqIP: "127.0.0.1", verifyIP: true, plugin: "filmix",
		baseURI: "https://nl104.cdnsqu.com/hls/hdr/film.mp4/index.m3u8?hash=FHASH",
	}

	got := h.rewriteM3U(src, req, meta)

	issues := hlsvalidate.CompareRewrite(src, got, hlsvalidate.CompareOpts{
		ProxyPrefix:         "http://lampac.local/proxy/",
		AllowDirectSegments: true,
	})
	for _, is := range issues {
		t.Errorf("%s\n--- rewritten ---\n%s", is, got)
	}

	// And the repair itself: a hashless init must not reach the CDN.
	if strings.Contains(got, `URI="https://nl104.cdnsqu.com/hls/hdr/film.mp4/init-v1-a1.mp4"`) {
		t.Errorf("init segment went out without the hash:\n%s", got)
	}
}

// The IPTV path trims a long DVR window on purpose; the invariant checker must
// be told, and everything else must still hold.
func TestRewriteM3UIPTVWindowTrimStaysValid(t *testing.T) {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:1\n")
	for i := 1; i <= iptvLiveKeepSegments+40; i++ {
		b.WriteString("#EXTINF:6.0,\nhttps://cdn.example/live/" + strconv.Itoa(i) + ".ts\n")
	}
	src := b.String()

	h := New(config.Config{}, nil)
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/proxy/x.m3u8", nil)
	meta := linkMeta{
		reqIP: "127.0.0.1", verifyIP: true, plugin: "iptv",
		baseURI: "https://cdn.example/live/index.m3u8",
	}

	got := h.rewriteM3U(src, req, meta)

	issues := hlsvalidate.CompareRewrite(src, got, hlsvalidate.CompareOpts{
		ProxyPrefix:     "http://lampac.local/proxy/",
		AllowWindowTrim: true,
	})
	for _, is := range issues {
		t.Errorf("%s", is)
	}
}
