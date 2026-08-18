package hlsprobe

import (
	"context"
	"encoding/binary"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const master = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"
1080/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.42c01e,mp4a.40.2"
360/index.m3u8
`

const mediaVOD = `#EXTM3U
#EXT-X-TARGETDURATION:10
#EXT-X-MAP:URI="init.mp4"
#EXTINF:10.0,
seg1.m4s
#EXTINF:10.0,
seg2.m4s
#EXT-X-ENDLIST
`

const mediaLiveNoMap = `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:42
#EXTINF:6.0,
https://cdn.example/live/42.ts
#EXTINF:6.0,
https://cdn.example/live/43.ts
`

// fakeFetch serves canned bodies and records the order of the URLs asked for.
type fakeFetch struct {
	bodies map[string]string
	status map[string]int
	asked  []string
	err    error
}

func (f *fakeFetch) fetch(_ context.Context, rawURL, rangeHdr string) (int, []byte, string, error) {
	f.asked = append(f.asked, rawURL)
	if f.err != nil {
		return 0, nil, "", f.err
	}
	st := f.status[rawURL]
	if st == 0 {
		st = 200
	}
	body, ok := f.bodies[rawURL]
	if !ok && st == 200 {
		body = "media-bytes"
	}
	return st, []byte(body), rawURL, nil
}

func TestProbeWalksMasterVariantSegment(t *testing.T) {
	f := &fakeFetch{bodies: map[string]string{
		"https://cdn.example/master.m3u8":    master,
		"https://cdn.example/360/index.m3u8": mediaVOD,
	}}

	res := Probe(context.Background(), "https://cdn.example/master.m3u8", f.fetch)

	if !res.OK {
		t.Fatalf("expected OK, got %+v", res)
	}
	if res.Variants != 2 || res.Bandwidth != 800000 {
		t.Errorf("variants=%d bandwidth=%d — the cheapest rung should be probed", res.Variants, res.Bandwidth)
	}
	// The init segment is what a player fetches first, so it is what we check.
	want := "https://cdn.example/360/init.mp4"
	if res.SegmentURL != want {
		t.Errorf("segment = %q, want %q", res.SegmentURL, want)
	}
	if res.Live {
		t.Error("VOD playlist reported as live")
	}
	if res.Segments != 2 {
		t.Errorf("segments = %d", res.Segments)
	}
}

// The failure this whole package exists for: manifest fine, segment 403.
func TestProbeCatchesSegment403BehindHealthyManifest(t *testing.T) {
	f := &fakeFetch{
		bodies: map[string]string{
			"https://cdn.example/master.m3u8":    master,
			"https://cdn.example/360/index.m3u8": mediaVOD,
		},
		status: map[string]int{"https://cdn.example/360/init.mp4": 403},
	}

	res := Probe(context.Background(), "https://cdn.example/master.m3u8", f.fetch)

	if res.OK {
		t.Fatal("a 403 on the first segment must not pass")
	}
	if res.Stage != StageSegment || res.SegmentStatus != 403 {
		t.Errorf("stage=%q status=%d", res.Stage, res.SegmentStatus)
	}
	if res.ManifestStatus != 200 || res.VariantStatus != 200 {
		t.Errorf("upper stages should still report 200: %+v", res)
	}
}

func TestProbeCatchesEmptyPlaylist(t *testing.T) {
	f := &fakeFetch{bodies: map[string]string{
		"https://cdn.example/index.m3u8": "#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXT-X-ENDLIST\n",
	}}

	res := Probe(context.Background(), "https://cdn.example/index.m3u8", f.fetch)

	if res.OK || !strings.Contains(res.Err, "no segments") {
		t.Errorf("expected an empty-playlist failure, got %+v", res)
	}
}

func TestProbeLiveWithAbsoluteSegments(t *testing.T) {
	f := &fakeFetch{bodies: map[string]string{
		"https://cdn.example/live.m3u8": mediaLiveNoMap,
	}}

	res := Probe(context.Background(), "https://cdn.example/live.m3u8", f.fetch)

	if !res.OK || !res.Live {
		t.Fatalf("expected a live OK, got %+v", res)
	}
	if res.SegmentURL != "https://cdn.example/live/42.ts" {
		t.Errorf("segment = %q", res.SegmentURL)
	}
}

func TestProbeRetriesWithoutRangeOn416(t *testing.T) {
	f := &fakeFetch{
		bodies: map[string]string{"https://cdn.example/live.m3u8": mediaLiveNoMap},
		status: map[string]int{"https://cdn.example/live/42.ts": 416},
	}
	// Range-hostile origin: 416 for a ranged ask, 200 for the whole body.
	res := Probe(context.Background(), "https://cdn.example/live.m3u8", func(ctx context.Context, u, rng string) (int, []byte, string, error) {
		if u == "https://cdn.example/live/42.ts" {
			if rng != "" {
				return 416, nil, u, nil
			}
			return 200, []byte("bytes"), u, nil
		}
		return f.fetch(ctx, u, rng)
	})

	if !res.OK {
		t.Fatalf("a Range-hostile origin must not be reported broken: %+v", res)
	}
}

func TestProbeNonPlaylistBody(t *testing.T) {
	// A progressive mp4 URL: no playlist, but real bytes → playable.
	f := &fakeFetch{bodies: map[string]string{"https://cdn.example/movie.mp4": "\x00\x00\x00\x18ftypmp42"}}
	res := Probe(context.Background(), "https://cdn.example/movie.mp4", f.fetch)
	if !res.OK || res.Stage != StageDirect {
		t.Errorf("expected a direct OK, got %+v", res)
	}

	// An HTML error page served with 200 is the classic "source is up" lie.
	f2 := &fakeFetch{bodies: map[string]string{"https://cdn.example/movie.mp4": "<!DOCTYPE html><html>403</html>"}}
	res2 := Probe(context.Background(), "https://cdn.example/movie.mp4", f2.fetch)
	if res2.OK {
		t.Errorf("HTML behind a 200 must not count as playable: %+v", res2)
	}
}

func TestProbeTransportError(t *testing.T) {
	f := &fakeFetch{err: errors.New("dial tcp: timeout")}
	res := Probe(context.Background(), "https://cdn.example/master.m3u8", f.fetch)
	if res.OK || res.Stage != StageManifest || !strings.Contains(res.Err, "timeout") {
		t.Errorf("res = %+v", res)
	}
}

func TestParseMediaPrefersInitSegment(t *testing.T) {
	base, _ := url.Parse("https://cdn.example/hls/index.m3u8")
	m := ParseMedia(mediaVOD, base)
	if m.First != "https://cdn.example/hls/init.mp4" {
		t.Errorf("first = %q", m.First)
	}
	if m.Count != 2 || m.Live {
		t.Errorf("count=%d live=%v", m.Count, m.Live)
	}
}

func TestParseMasterAttributes(t *testing.T) {
	base, _ := url.Parse("https://cdn.example/master.m3u8")
	vs := ParseMaster(master, base)
	if len(vs) != 2 {
		t.Fatalf("variants = %d", len(vs))
	}
	if vs[0].Codecs != "avc1.640028,mp4a.40.2" {
		t.Errorf("codecs = %q — quoted attribute with a comma must survive", vs[0].Codecs)
	}
	if vs[0].Resolution != "1920x1080" || vs[0].Bandwidth != 6000000 {
		t.Errorf("v0 = %+v", vs[0])
	}
	if vs[1].URL != "https://cdn.example/360/index.m3u8" {
		t.Errorf("relative URI not resolved: %q", vs[1].URL)
	}
}

// A frozen channel: every fetch succeeds, the window never moves. This is the
// failure that a status-code check can never see.
func TestProbeLiveDetectsFrozenWindow(t *testing.T) {
	frozen := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:100\n#EXTINF:6.0,\nhttps://cdn.example/live/100.ts\n"
	fetch := func(_ context.Context, u, _ string) (int, []byte, string, error) {
		if strings.HasSuffix(u, ".m3u8") {
			return 200, []byte(frozen), u, nil
		}
		return 200, []byte("ts-bytes"), u, nil
	}

	res := ProbeLive(context.Background(), "https://cdn.example/live.m3u8", fetch, time.Millisecond)
	if res.OK || !res.Stale {
		t.Fatalf("frozen window not detected: %+v", res)
	}
	if !strings.Contains(res.Err, "did not advance") {
		t.Errorf("err = %q", res.Err)
	}
}

func TestProbeLiveAcceptsAdvancingWindow(t *testing.T) {
	var reads int
	fetch := func(_ context.Context, u, _ string) (int, []byte, string, error) {
		if strings.HasSuffix(u, ".m3u8") {
			reads++
			seq := 100 + reads
			body := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:" + strconv.Itoa(seq) +
				"\n#EXTINF:6.0,\nhttps://cdn.example/live/" + strconv.Itoa(seq) + ".ts\n"
			return 200, []byte(body), u, nil
		}
		return 200, []byte("ts-bytes"), u, nil
	}

	res := ProbeLive(context.Background(), "https://cdn.example/live.m3u8", fetch, time.Millisecond)
	if !res.OK || res.Stale {
		t.Fatalf("a live channel was called stale: %+v", res)
	}
}

// VOD never advances by definition — ProbeLive must not call it frozen.
func TestProbeLiveSkipsVOD(t *testing.T) {
	fetch := func(_ context.Context, u, _ string) (int, []byte, string, error) {
		if strings.HasSuffix(u, ".m3u8") {
			return 200, []byte(mediaVOD), u, nil
		}
		return 200, []byte("bytes"), u, nil
	}
	res := ProbeLive(context.Background(), "https://cdn.example/index.m3u8", fetch, time.Millisecond)
	if !res.OK || res.Stale {
		t.Fatalf("VOD flagged as stale: %+v", res)
	}
}

func TestStaleNeedsBothSignals(t *testing.T) {
	// Sequence moved, last segment repeated (a discontinuity re-using a name):
	// not stale.
	a := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:6,\nseg.ts\n"
	b := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:11\n#EXTINF:6,\nseg.ts\n"
	if Stale(a, b) {
		t.Error("advancing media sequence must not read as stale")
	}
	// No MEDIA-SEQUENCE at all (some origins omit it): fall back to the last
	// segment name.
	c := "#EXTM3U\n#EXTINF:6,\nseg9.ts\n"
	d := "#EXTM3U\n#EXTINF:6,\nseg10.ts\n"
	if Stale(c, d) {
		t.Error("changed last segment must not read as stale")
	}
	if !Stale(c, c) {
		t.Error("identical windows must read as stale")
	}
}

// The probe already holds the first bytes of the segment; naming what is inside
// them is free and answers "why is this silent on that TV" before anyone plays it.
func TestProbeReportsRealCodecs(t *testing.T) {
	box := func(typ string, payload ...[]byte) []byte {
		var body []byte
		for _, p := range payload {
			body = append(body, p...)
		}
		out := make([]byte, 8, 8+len(body))
		binary.BigEndian.PutUint32(out[0:4], uint32(8+len(body)))
		copy(out[4:8], typ)
		return append(out, body...)
	}
	hdlr := func(kind string) []byte {
		b := make([]byte, 8)
		b = append(b, []byte(kind)...)
		return box("hdlr", b, make([]byte, 12))
	}
	trak := func(handler string, entry []byte) []byte {
		return box("trak", box("mdia", hdlr(handler), box("minf", box("stbl",
			box("stsd", []byte{0, 0, 0, 0, 0, 0, 0, 1}, entry)))))
	}
	initSegment := append(box("ftyp", []byte("isom")),
		box("moov",
			trak("vide", box("avc1", make([]byte, 78), box("avcC", []byte{0x01, 0x64, 0x00, 0x28, 0xFF}))),
			trak("soun", box("ec-3", make([]byte, 28), box("dec3", []byte{0x00}))),
		)...)

	fetch := func(_ context.Context, u, _ string) (int, []byte, string, error) {
		if strings.HasSuffix(u, ".m3u8") {
			return 200, []byte("#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6,\nseg1.m4s\n#EXT-X-ENDLIST\n"), u, nil
		}
		return 200, initSegment, u, nil
	}

	res := Probe(context.Background(), "https://cdn.example/index.m3u8", fetch)
	if !res.OK {
		t.Fatalf("res = %+v", res)
	}
	if res.Media.Container != "mp4" {
		t.Fatalf("container = %q", res.Media.Container)
	}
	if got := res.Media.CodecString(); got != "avc1.640028,ec-3" {
		t.Errorf("codecs = %q", got)
	}
	// The point of the exercise: the audio codec is named even though the
	// playlist said nothing about codecs at all.
	if !res.Media.Has("eac3") {
		t.Errorf("E-AC-3 not detected: %+v", res.Media.Tracks)
	}
}
