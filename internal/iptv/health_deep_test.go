package iptv

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"lampac-go/internal/mediaprobe"
	"time"
)

func newHealthStore(t *testing.T, deep, stale bool) *Store {
	t.Helper()
	s := NewStore(t.TempDir(), StoreConfig{})
	s.SetHealthDepth(deep, stale)
	s.healthClient = &http.Client{Timeout: 5 * time.Second}
	return s
}

// The channel that stayed in the list for weeks: playlist serves fine, every
// segment 403s. The shallow probe calls it alive; the deep one does not.
func TestProbeAliveDeepCatchesDeadSegments(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:5\n#EXTINF:6.0,\nseg5.ts\n"))
	})
	mux.HandleFunc("/seg5.ts", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if alive := newHealthStore(t, false, false).probeAlive(context.Background(), srv.URL+"/live.m3u8", "", ""); !alive {
		t.Error("shallow probe should still pass — that is exactly the blind spot")
	}
	if alive := newHealthStore(t, true, false).probeAlive(context.Background(), srv.URL+"/live.m3u8", "", ""); alive {
		t.Error("deep probe must fail a channel whose segments 403")
	}
}

func TestProbeAliveDeepAcceptsWorkingChannel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:5\n#EXTINF:6.0,\nseg5.ts\n"))
	})
	mux.HandleFunc("/seg5.ts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ts-bytes"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if alive := newHealthStore(t, true, false).probeAlive(context.Background(), srv.URL+"/live.m3u8", "", ""); !alive {
		t.Error("a working channel was marked dead")
	}
}

// Non-HLS channels (raw ts/udp-over-http) keep the cheap check — there is no
// playlist to walk.
func TestProbeAliveDeepFallsBackForNonHLS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			t.Error("non-HLS probe should stay a ranged read")
		}
		_, _ = w.Write([]byte("\x47\x40\x00\x10")) // TS sync byte
	}))
	defer srv.Close()

	if alive := newHealthStore(t, true, false).probeAlive(context.Background(), srv.URL+"/stream.ts", "", ""); !alive {
		t.Error("raw TS channel marked dead")
	}
}

func TestProbeAlivePassesUserAgentAndReferer(t *testing.T) {
	var gotUA, gotRef string
	mux := http.NewServeMux()
	mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotRef = r.Header.Get("User-Agent"), r.Header.Get("Referer")
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:6.0,\nseg.ts\n"))
	})
	mux.HandleFunc("/seg.ts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("bytes"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Providers gate on these; a probe that drops them measures the wrong thing.
	newHealthStore(t, true, false).probeAlive(context.Background(), srv.URL+"/live.m3u8", "MyPlayer/1.0", "https://provider.example/")
	if gotUA != "MyPlayer/1.0" || gotRef != "https://provider.example/" {
		t.Errorf("ua=%q ref=%q", gotUA, gotRef)
	}
}

func TestLooksHLS(t *testing.T) {
	cases := map[string]bool{
		"https://x/live.m3u8":          true,
		"https://x/live.m3u8?token=1":  true,
		"https://x/playlist.m3u":       true,
		"https://x/stream.ts":          false,
		"https://x/udp/239.1.1.1:1234": false,
	}
	for u, want := range cases {
		if got := looksHLS(u); got != want {
			t.Errorf("looksHLS(%q) = %v, want %v", u, got, want)
		}
	}
}

// The deep probe already downloaded the segment; naming what is inside it costs
// nothing and answers the second-most-common IPTV complaint after a dead
// channel — picture plays, sound does not.
func TestDeepProbeRecordsStreamCodecs(t *testing.T) {
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
			trak("vide", box("hvc1", make([]byte, 78), box("hvcC", []byte{
				0x01, 0x01, 0x60, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 120,
			}))),
			trak("soun", box("ec-3", make([]byte, 28), box("dec3", []byte{0x00}))),
		)...)

	mux := http.NewServeMux()
	mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6.0,\nseg.m4s\n"))
	})
	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(initSegment)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := newHealthStore(t, true, false)
	if !s.probeAlive(context.Background(), srv.URL+"/live.m3u8", "", "") {
		t.Fatal("channel should be alive")
	}

	info, ok := s.StreamInfoFor(srv.URL + "/live.m3u8")
	if !ok {
		t.Fatal("no stream info recorded")
	}
	if info.Codecs != "hvc1.1.6.L120,ec-3" {
		t.Errorf("codecs = %q", info.Codecs)
	}
	if info.Container != "mp4" || len(info.Tracks) != 2 || info.ProbedAt.IsZero() {
		t.Errorf("info = %+v", info)
	}

	// A channel nobody probed must read as unknown, never as "fine".
	if _, ok := s.StreamInfoFor("https://other.example/live.m3u8"); ok {
		t.Error("unprobed channel reported stream info")
	}
}

func TestCodecCensusCountsChannelsNotTracks(t *testing.T) {
	s := newHealthStore(t, true, false)
	// Two channels, one of them with two audio tracks of the same codec: the
	// census answers "how many channels", so the duplicate must not double-count.
	s.recordStreamInfo("https://a.example/1.m3u8", mediaprobe.Tracks{Container: "ts", Tracks: []mediaprobe.Track{
		{Kind: mediaprobe.KindVideo, Name: "hevc"},
		{Kind: mediaprobe.KindAudio, Name: "eac3"},
		{Kind: mediaprobe.KindAudio, Name: "eac3"},
	}})
	s.recordStreamInfo("https://a.example/2.m3u8", mediaprobe.Tracks{Container: "ts", Tracks: []mediaprobe.Track{
		{Kind: mediaprobe.KindVideo, Name: "h264"},
		{Kind: mediaprobe.KindAudio, Name: "aac"},
	}})

	got := s.codecCensus()
	if got["hevc"] != 1 || got["eac3"] != 1 || got["h264"] != 1 || got["aac"] != 1 {
		t.Errorf("census = %v", got)
	}
}
