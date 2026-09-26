package proxyapi

import (
	"fmt"
	"strings"
	"testing"
)

// buildLivePlaylist makes a sliding-window live playlist with n segments starting at mediaSeq.
func buildLivePlaylist(n, mediaSeq int) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n")
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", mediaSeq)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "#EXTINF:6.000,\nseg%d.ts\n", mediaSeq+i)
	}
	return b.String()
}

func TestTrimLiveDVRBasic(t *testing.T) {
	src := buildLivePlaylist(1000, 5000)
	out := trimLiveDVR(src, 20)

	if got := strings.Count(out, "#EXTINF"); got != 20 {
		t.Fatalf("kept %d segments, want 20", got)
	}
	// media sequence advanced by the 980 dropped segments
	if !strings.Contains(out, "#EXT-X-MEDIA-SEQUENCE:5980") {
		t.Fatalf("media sequence not advanced:\n%s", out[:200])
	}
	// the newest segment survived, the oldest kept is seg5980
	if !strings.Contains(out, "seg5999.ts") || !strings.Contains(out, "seg5980.ts") {
		t.Fatalf("wrong window kept")
	}
	if strings.Contains(out, "seg5979.ts") {
		t.Fatalf("segment before the window leaked through")
	}
	// header intact
	if !strings.HasPrefix(out, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n") {
		t.Fatalf("header mangled:\n%s", out[:120])
	}
}

func TestTrimLiveDVRInsertsMediaSequence(t *testing.T) {
	// no EXT-X-MEDIA-SEQUENCE → implicit 0; trimming must INSERT one so refreshes stay continuous
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "#EXTINF:4,\nchunk%d.ts\n", i)
	}
	out := trimLiveDVR(b.String(), 10)
	if !strings.Contains(out, "#EXT-X-MEDIA-SEQUENCE:40") {
		t.Fatalf("media sequence not inserted:\n%s", out[:160])
	}
	if got := strings.Count(out, "#EXTINF"); got != 10 {
		t.Fatalf("kept %d segments, want 10", got)
	}
}

func TestTrimLiveDVRPassThrough(t *testing.T) {
	cases := map[string]string{
		"vod":                         "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:6,\na.ts\n#EXTINF:6,\nb.ts\n#EXT-X-ENDLIST\n",
		"endlist (flussonic catchup)": "#EXTM3U\n#EXTINF:6,\na.ts\n#EXTINF:6,\nb.ts\n#EXT-X-ENDLIST\n",
		"master":                      "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720\nhd.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nsd.m3u8\n",
		"short live":                  buildLivePlaylist(5, 100),
	}
	for name, src := range cases {
		if out := trimLiveDVR(src, 20); out != src {
			t.Fatalf("%s: playlist must pass through untouched\nwant:\n%s\ngot:\n%s", name, src, out)
		}
	}
}

func TestTrimLiveDVRKeyMapDiscontinuity(t *testing.T) {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-DISCONTINUITY-SEQUENCE:2\n")
	b.WriteString("#EXT-X-MAP:URI=\"init1.mp4\"\n")
	b.WriteString("#EXT-X-KEY:METHOD=AES-128,URI=\"key1\"\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "#EXTINF:6,\nold%d.m4s\n", i)
	}
	// a discontinuity + new key/init in the span that will be dropped
	b.WriteString("#EXT-X-DISCONTINUITY\n")
	b.WriteString("#EXT-X-MAP:URI=\"init2.mp4\"\n")
	b.WriteString("#EXT-X-KEY:METHOD=AES-128,URI=\"key2\"\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "#EXTINF:6,\nnew%d.m4s\n", i)
	}
	out := trimLiveDVR(b.String(), 10)

	if got := strings.Count(out, "#EXTINF"); got != 10 {
		t.Fatalf("kept %d segments, want 10", got)
	}
	// 60 segments total, 50 dropped → media sequence 10+50
	if !strings.Contains(out, "#EXT-X-MEDIA-SEQUENCE:60") {
		t.Fatalf("media sequence wrong:\n%s", out)
	}
	// the dropped span contained one DISCONTINUITY → 2+1
	if !strings.Contains(out, "#EXT-X-DISCONTINUITY-SEQUENCE:3") {
		t.Fatalf("discontinuity sequence wrong:\n%s", out)
	}
	// the key/init in effect at the cut (the SECOND pair) must be re-emitted
	if !strings.Contains(out, `URI="key2"`) || !strings.Contains(out, `URI="init2.mp4"`) {
		t.Fatalf("active KEY/MAP not re-emitted:\n%s", out)
	}
	// the re-emitted KEY/MAP must precede the first kept segment
	if strings.Index(out, `URI="key2"`) > strings.Index(out, "new20.m4s") {
		t.Fatalf("KEY re-emitted after the first kept segment")
	}
	if strings.Contains(out, "old0.m4s") || strings.Contains(out, "new19.m4s") {
		t.Fatalf("dropped segments leaked through")
	}
}

func TestTrimLiveDVRCRLF(t *testing.T) {
	src := strings.ReplaceAll(buildLivePlaylist(100, 0), "\n", "\r\n")
	out := trimLiveDVR(src, 20)
	if got := strings.Count(out, "#EXTINF"); got != 20 {
		t.Fatalf("CRLF playlist: kept %d segments, want 20", got)
	}
	if !strings.Contains(out, "seg99.ts") {
		t.Fatalf("CRLF playlist: newest segment missing")
	}
}
