package hlsvalidate

import (
	"strings"
	"testing"
)

func rules(issues []Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Rule)
	}
	return out
}

func has(issues []Issue, rule string) bool {
	for _, i := range issues {
		if i.Rule == rule {
			return true
		}
	}
	return false
}

const upstreamVOD = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:10
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-START:TIME-OFFSET=0
#EXT-X-MAP:URI="https://cdn.example/init.mp4?hash=abc"
#EXTINF:10.0,
https://cdn.example/seg1.m4s?hash=abc
#EXTINF:10.0,
https://cdn.example/seg2.m4s?hash=abc
#EXT-X-ENDLIST
`

func TestCompareRewriteAcceptsAFaithfulRewrite(t *testing.T) {
	out := strings.ReplaceAll(upstreamVOD, "https://cdn.example/", "https://our.host/proxy/enc-")
	issues := CompareRewrite(upstreamVOD, out, CompareOpts{ProxyPrefix: "https://our.host/proxy/"})
	if len(issues) != 0 {
		t.Fatalf("faithful rewrite flagged: %v", rules(issues))
	}
}

// The Filmix incident, as a test: the init segment goes out unproxied (and,
// in the real bug, without the hash) while the segments are rewritten.
func TestCompareRewriteCatchesUnproxiedInitSegment(t *testing.T) {
	out := strings.ReplaceAll(upstreamVOD, "https://cdn.example/seg", "https://our.host/proxy/enc-seg")
	issues := CompareRewrite(upstreamVOD, out, CompareOpts{ProxyPrefix: "https://our.host/proxy/"})
	if !has(issues, "map-not-proxied") {
		t.Errorf("expected map-not-proxied, got %v", rules(issues))
	}
}

// The YouTube incident: the tag that decides where playback starts is dropped.
func TestCompareRewriteCatchesLostStartTag(t *testing.T) {
	out := strings.ReplaceAll(
		strings.ReplaceAll(upstreamVOD, "#EXT-X-START:TIME-OFFSET=0\n", ""),
		"https://cdn.example/", "https://our.host/proxy/enc-")
	issues := CompareRewrite(upstreamVOD, out, CompareOpts{ProxyPrefix: "https://our.host/proxy/"})
	if !has(issues, "tag-lost") && !has(issues, "attr-changed") {
		t.Errorf("expected the lost EXT-X-START to be reported, got %v", rules(issues))
	}
}

func TestCompareRewriteCatchesDroppedSegment(t *testing.T) {
	out := strings.ReplaceAll(upstreamVOD, "https://cdn.example/", "https://our.host/proxy/enc-")
	out = strings.ReplaceAll(out, "#EXTINF:10.0,\nhttps://our.host/proxy/enc-seg2.m4s?hash=abc\n", "")
	issues := CompareRewrite(upstreamVOD, out, CompareOpts{ProxyPrefix: "https://our.host/proxy/"})
	if !has(issues, "uri-lost") {
		t.Errorf("expected uri-lost, got %v", rules(issues))
	}
}

func TestCompareRewriteAllowsManifestOnlyDirectSegments(t *testing.T) {
	// Manifest-only mode: segments stay on the CDN by design, the init segment
	// too (it is repaired in place, not proxied).
	out := upstreamVOD
	issues := CompareRewrite(upstreamVOD, out, CompareOpts{
		ProxyPrefix:         "https://our.host/proxy/",
		AllowDirectSegments: true,
	})
	if len(issues) != 0 {
		t.Fatalf("manifest-only rewrite flagged: %v", rules(issues))
	}
}

func TestCompareRewriteAllowsLiveWindowTrim(t *testing.T) {
	live := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:100
#EXTINF:6.0,
https://cdn.example/100.ts
#EXTINF:6.0,
https://cdn.example/101.ts
#EXTINF:6.0,
https://cdn.example/102.ts
`
	trimmed := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:102
#EXTINF:6.0,
https://our.host/proxy/enc-102.ts
`
	issues := CompareRewrite(live, trimmed, CompareOpts{
		ProxyPrefix:     "https://our.host/proxy/",
		AllowWindowTrim: true,
	})
	if len(issues) != 0 {
		t.Fatalf("legitimate DVR trim flagged: %v", rules(issues))
	}

	// Without the opt-in it must be reported — a rewrite that silently drops
	// segments from a VOD playlist is a bug.
	if issues := CompareRewrite(live, trimmed, CompareOpts{ProxyPrefix: "https://our.host/proxy/"}); !has(issues, "uri-lost") {
		t.Errorf("expected uri-lost without AllowWindowTrim, got %v", rules(issues))
	}
}

func TestCompareRewriteAllowsDeclaredKeyRemoval(t *testing.T) {
	// redheadsound: the server decrypts CBCS itself and strips the KEY tag.
	src := "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"https://lic.example/k\"\n#EXTINF:6.0,\nhttps://cdn.example/1.ts\n#EXT-X-ENDLIST\n"
	out := "#EXTM3U\n#EXTINF:6.0,\nhttps://our.host/proxy/enc-1.ts\n#EXT-X-ENDLIST\n"

	if issues := CompareRewrite(src, out, CompareOpts{ProxyPrefix: "https://our.host/proxy/"}); !has(issues, "tag-lost") {
		t.Errorf("an undeclared KEY removal must be reported, got %v", rules(issues))
	}
	issues := CompareRewrite(src, out, CompareOpts{
		ProxyPrefix:    "https://our.host/proxy/",
		AllowedTagLoss: []string{"EXT-X-KEY"},
	})
	if len(issues) != 0 {
		t.Fatalf("declared KEY removal flagged: %v", rules(issues))
	}
}

func TestCompareRewriteCatchesUnproxiedKey(t *testing.T) {
	src := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://cdn.example/key\"\n#EXTINF:6.0,\nhttps://cdn.example/1.ts\n#EXT-X-ENDLIST\n"
	out := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://cdn.example/key\"\n#EXTINF:6.0,\nhttps://cdn.example/1.ts\n#EXT-X-ENDLIST\n"
	// Even in direct-segment mode the key must come through us: it is the one
	// fetch that carries auth.
	issues := CompareRewrite(src, out, CompareOpts{
		ProxyPrefix:         "https://our.host/proxy/",
		AllowDirectSegments: true,
	})
	if !has(issues, "key-not-proxied") {
		t.Errorf("expected key-not-proxied, got %v", rules(issues))
	}
}

func TestCompareRewriteCatchesRelativeLeftovers(t *testing.T) {
	src := "#EXTM3U\n#EXTINF:6.0,\nseg1.ts\n#EXT-X-ENDLIST\n"
	out := src // rewriter did nothing → relative URI now resolves against our host
	issues := CompareRewrite(src, out, CompareOpts{ProxyPrefix: "https://our.host/proxy/"})
	if !has(issues, "relative-uri") {
		t.Errorf("expected relative-uri, got %v", rules(issues))
	}
}

func TestLintStructuralProblems(t *testing.T) {
	cases := map[string]string{
		"no-extm3u":           "#EXT-X-VERSION:3\n#EXTINF:6.0,\na.ts\n",
		"dangling-tag":        "#EXTM3U\n#EXTINF:6.0,\n#EXT-X-ENDLIST\n",
		"mixed-playlist":      "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n#EXTINF:6.0,\na.ts\n",
		"vod-without-endlist": "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:6.0,\na.ts\n",
		"empty-uri":           "#EXTM3U\n#EXT-X-MAP:URI=\"\"\n#EXTINF:6.0,\na.ts\n#EXT-X-ENDLIST\n",
	}
	for rule, playlist := range cases {
		if !has(Lint(playlist), rule) {
			t.Errorf("%s not reported for:\n%s\ngot %v", rule, playlist, rules(Lint(playlist)))
		}
	}

	if issues := Lint(upstreamVOD); len(issues) != 0 {
		t.Errorf("a valid playlist was flagged: %v", rules(issues))
	}
}

func TestParseMasterAndAttrs(t *testing.T) {
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\n360/index.m3u8\n"
	p := Parse(master)
	if !p.Master || len(p.URIs) != 1 {
		t.Errorf("p = %+v", p)
	}

	p2 := Parse(upstreamVOD)
	if p2.Attr["EXT-X-START"] != "0" || p2.Attr["EXT-X-PLAYLIST-TYPE"] != "VOD" {
		t.Errorf("attrs = %+v", p2.Attr)
	}
	if len(p2.MapURIs) != 1 || !strings.Contains(p2.MapURIs[0], "init.mp4") {
		t.Errorf("map uris = %v", p2.MapURIs)
	}
}
