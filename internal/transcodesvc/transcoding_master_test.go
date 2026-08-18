package transcodesvc

import (
	"lampac-go/internal/transcode"
	"strings"
	"testing"
)

func TestBuildSubtitleRenditionPlaylist(t *testing.T) {
	pl := buildSubtitleRenditionPlaylist("subs_3.vtt", 3600.0)
	if !strings.HasPrefix(pl, "#EXTM3U") {
		t.Errorf("must start with #EXTM3U, got %q", pl[:20])
	}
	if !strings.Contains(pl, "#EXT-X-PLAYLIST-TYPE:VOD") {
		t.Errorf("must declare VOD playlist type, got %q", pl)
	}
	if !strings.Contains(pl, "#EXT-X-ENDLIST") {
		t.Errorf("must end the playlist, got %q", pl)
	}
	if !strings.Contains(pl, "subs_3.vtt") {
		t.Errorf("must reference the VTT file, got %q", pl)
	}
	if !strings.Contains(pl, "#EXTINF:3600.000,") {
		t.Errorf("must declare actual duration, got %q", pl)
	}
}

func TestBuildSubtitleRenditionPlaylist_UnknownDuration(t *testing.T) {
	pl := buildSubtitleRenditionPlaylist("subs_5.vtt", 0)
	if !strings.Contains(pl, "#EXTINF:9999") {
		t.Errorf("unknown duration → 9999 fallback, got %q", pl)
	}
}

func TestBuildMasterPlaylist_NoSubs_SingleStream(t *testing.T) {
	job := &TranscodingJob{
		Mode: transcode.ModeRemux,
		Context: transcodingContext{
			Mode: transcode.ModeRemux,
			FFProbe: map[string]any{
				"streams": []any{
					map[string]any{
						"codec_type": "video",
						"codec_name": "h264",
						"width":      float64(1920),
						"height":     float64(1080),
					},
					map[string]any{
						"codec_type": "audio",
						"codec_name": "aac",
					},
				},
				"format": map[string]any{
					"bit_rate": "5000000",
				},
			},
		},
	}
	pl := buildMasterPlaylist(job, "https://example.com/transcoding/abc")

	if !strings.Contains(pl, "#EXT-X-STREAM-INF:") {
		t.Errorf("expected STREAM-INF, got %q", pl)
	}
	if strings.Contains(pl, "#EXT-X-MEDIA:TYPE=SUBTITLES") {
		t.Errorf("no subs in plan → no SUBTITLES MEDIA tags, got %q", pl)
	}
	if !strings.Contains(pl, "main.m3u8") {
		t.Errorf("expected main.m3u8 reference, got %q", pl)
	}
	if !strings.Contains(pl, "RESOLUTION=1920x1080") {
		t.Errorf("expected 1920x1080 resolution, got %q", pl)
	}
	if !strings.Contains(pl, "avc1") {
		t.Errorf("expected avc1 codec, got %q", pl)
	}
}

func TestBuildMasterPlaylist_WithSubExtract(t *testing.T) {
	job := &TranscodingJob{
		Mode: transcode.ModeRemux,
		Context: transcodingContext{
			Mode: transcode.ModeRemux,
			FFProbe: map[string]any{
				"streams": []any{
					map[string]any{"codec_type": "video", "codec_name": "h264", "width": float64(1280), "height": float64(720)},
					map[string]any{"codec_type": "audio", "codec_name": "aac"},
				},
			},
			SubPlan: SubtitlePlan{
				Strategy: StrategyExtract,
				Streams: []SubtitleStream{
					{AbsIndex: 3, RelIndex: 0, Codec: "subrip", Lang: "rus", Title: "Russian", Default: true},
					{AbsIndex: 4, RelIndex: 1, Codec: "subrip", Lang: "eng", Title: "English"},
				},
			},
		},
	}
	pl := buildMasterPlaylist(job, "https://example.com/transcoding/abc")

	if strings.Count(pl, "#EXT-X-MEDIA:TYPE=SUBTITLES") != 2 {
		t.Errorf("expected exactly 2 SUBTITLES MEDIA tags, got %q", pl)
	}
	if !strings.Contains(pl, `LANGUAGE="rus"`) {
		t.Errorf("expected LANGUAGE rus, got %q", pl)
	}
	if !strings.Contains(pl, `LANGUAGE="eng"`) {
		t.Errorf("expected LANGUAGE eng, got %q", pl)
	}
	// First MEDIA entry (russian) must declare DEFAULT=YES; second (english)
	// must NOT.
	var russianLine, englishLine string
	for _, line := range strings.Split(pl, "\n") {
		if strings.HasPrefix(line, "#EXT-X-MEDIA:") && strings.Contains(line, `LANGUAGE="rus"`) {
			russianLine = line
		}
		if strings.HasPrefix(line, "#EXT-X-MEDIA:") && strings.Contains(line, `LANGUAGE="eng"`) {
			englishLine = line
		}
	}
	if russianLine == "" || englishLine == "" {
		t.Fatalf("both russian and english MEDIA lines must exist, got %q", pl)
	}
	if !strings.Contains(russianLine, "DEFAULT=YES") {
		t.Errorf("first MEDIA entry must declare DEFAULT=YES: %q", russianLine)
	}
	if !strings.Contains(englishLine, "DEFAULT=NO") {
		t.Errorf("subsequent MEDIA entries must declare DEFAULT=NO: %q", englishLine)
	}
	// STREAM-INF must reference SUBTITLES group.
	if !strings.Contains(pl, `SUBTITLES="subs"`) {
		t.Errorf("STREAM-INF must declare SUBTITLES group, got %q", pl)
	}
	// Each MEDIA must point at subs_N.m3u8 (not .vtt).
	if !strings.Contains(pl, `URI="subs_3.m3u8"`) || !strings.Contains(pl, `URI="subs_4.m3u8"`) {
		t.Errorf("MEDIA URIs must point at subs_N.m3u8, got %q", pl)
	}
}

func TestBuildMasterPlaylist_BurnInDoesntEmitSubsGroup(t *testing.T) {
	job := &TranscodingJob{
		Mode: transcode.ModeSWTranscode,
		Context: transcodingContext{
			Mode: transcode.ModeSWTranscode,
			SubPlan: SubtitlePlan{
				Strategy: StrategyBurnIn,
				Streams:  []SubtitleStream{{AbsIndex: 2, Codec: "hdmv_pgs_subtitle"}},
				BurnIn:   &SubtitleStream{AbsIndex: 2, Codec: "hdmv_pgs_subtitle"},
			},
		},
	}
	pl := buildMasterPlaylist(job, "")
	if strings.Contains(pl, "#EXT-X-MEDIA:TYPE=SUBTITLES") {
		t.Errorf("burn-in subs are inside the video stream — must not appear as MEDIA, got %q", pl)
	}
	if strings.Contains(pl, `SUBTITLES=`) {
		t.Errorf("STREAM-INF must NOT reference a non-existent SUBTITLES group, got %q", pl)
	}
}

// TestBuildMasterPlaylist_ABRLadder verifies that when smart-mode picked
// SW/HW transcode AND source resolution warrants multi-rung, master.m3u8
// emits one STREAM-INF per rung with descending bandwidths.
func TestBuildMasterPlaylist_ABRLadder(t *testing.T) {
	job := &TranscodingJob{
		Mode: transcode.ModeSWTranscode,
		Context: transcodingContext{
			Mode: transcode.ModeSWTranscode,
			FFProbe: map[string]any{
				"streams": []any{
					map[string]any{"codec_type": "video", "codec_name": "h264", "width": float64(1920), "height": float64(1080)},
					map[string]any{"codec_type": "audio", "codec_name": "aac"},
				},
			},
		},
	}
	pl := buildMasterPlaylist(job, "")

	// 1080p SW transcode → 3 rungs (1080p / 720p / 480p).
	if got := strings.Count(pl, "#EXT-X-STREAM-INF:"); got != 3 {
		t.Errorf("expected 3 STREAM-INF entries (1080p+720p+480p), got %d in:\n%s", got, pl)
	}
	for _, want := range []string{"RESOLUTION=1920x1080", "RESOLUTION=1280x720", "RESOLUTION=854x480"} {
		if !strings.Contains(pl, want) {
			t.Errorf("missing %q in master playlist:\n%s", want, pl)
		}
	}
	// Bandwidths must descend across the ladder.
	idx1080 := strings.Index(pl, "RESOLUTION=1920x1080")
	idx720 := strings.Index(pl, "RESOLUTION=1280x720")
	idx480 := strings.Index(pl, "RESOLUTION=854x480")
	if !(idx1080 < idx720 && idx720 < idx480) {
		t.Errorf("rungs must be emitted in descending order, got positions 1080=%d 720=%d 480=%d", idx1080, idx720, idx480)
	}
}

// TestBuildMasterPlaylist_MultiRungActive_PointsAtVariantPlaylists verifies
// that when ffmpeg actually produces multi-rung output (Phase 2), each
// STREAM-INF URI points at v<N>/index.m3u8 instead of the shared
// main.m3u8.
func TestBuildMasterPlaylist_MultiRungActive_PointsAtVariantPlaylists(t *testing.T) {
	job := &TranscodingJob{
		Mode: transcode.ModeSWTranscode,
		Context: transcodingContext{
			Mode:      transcode.ModeSWTranscode,
			MultiRung: true,
			Ladder: []transcode.ABRRung{
				{Label: "1080p", Width: 1920, Height: 1080, BitrateKbps: 6000, Primary: true},
				{Label: "720p", Width: 1280, Height: 720, BitrateKbps: 3000},
				{Label: "480p", Width: 854, Height: 480, BitrateKbps: 1500},
			},
			FFProbe: map[string]any{
				"streams": []any{
					map[string]any{"codec_type": "video", "codec_name": "h264", "width": float64(1920), "height": float64(1080)},
					map[string]any{"codec_type": "audio", "codec_name": "aac"},
				},
			},
		},
	}
	pl := buildMasterPlaylist(job, "")
	for _, want := range []string{"v0/index.m3u8", "v1/index.m3u8", "v2/index.m3u8"} {
		if !strings.Contains(pl, want) {
			t.Errorf("expected per-variant URI %q in master:\n%s", want, pl)
		}
	}
	if strings.Contains(pl, "main.m3u8") {
		t.Errorf("multi-rung master must NOT reference main.m3u8 — variants own their own playlists\n%s", pl)
	}
}

// TestBuildMasterPlaylist_MultiRungInactive_FallsBackToMainM3u8 verifies
// the Phase 1 fallback: when the ladder planner returns >1 rung but the
// ffmpeg path didn't actually produce per-rung subdirs, we still emit
// multiple STREAM-INF (for codec/bandwidth metadata) but they all point
// at the single main.m3u8.
func TestBuildMasterPlaylist_MultiRungInactive_FallsBackToMainM3u8(t *testing.T) {
	job := &TranscodingJob{
		Mode: transcode.ModeSWTranscode,
		Context: transcodingContext{
			Mode:      transcode.ModeSWTranscode,
			MultiRung: false, // Phase 1 fallback
			FFProbe: map[string]any{
				"streams": []any{
					map[string]any{"codec_type": "video", "codec_name": "h264", "width": float64(1920), "height": float64(1080)},
					map[string]any{"codec_type": "audio", "codec_name": "aac"},
				},
			},
		},
	}
	pl := buildMasterPlaylist(job, "")
	if !strings.Contains(pl, "main.m3u8") {
		t.Errorf("Phase 1 fallback must reference main.m3u8\n%s", pl)
	}
	if strings.Contains(pl, "v0/index.m3u8") {
		t.Errorf("Phase 1 fallback must NOT reference per-variant URIs (they don't exist on disk)\n%s", pl)
	}
}

// TestBuildMasterPlaylist_RemuxNoLadder verifies stream-copy modes don't
// trigger ABR (we can't downscale a copied stream without a parallel
// encode, which Phase 1 doesn't run).
func TestBuildMasterPlaylist_RemuxNoLadder(t *testing.T) {
	job := &TranscodingJob{
		Mode: transcode.ModeRemux,
		Context: transcodingContext{
			Mode: transcode.ModeRemux,
			FFProbe: map[string]any{
				"streams": []any{
					map[string]any{"codec_type": "video", "codec_name": "h264", "width": float64(1920), "height": float64(1080)},
					map[string]any{"codec_type": "audio", "codec_name": "aac"},
				},
			},
		},
	}
	pl := buildMasterPlaylist(job, "")
	if got := strings.Count(pl, "#EXT-X-STREAM-INF:"); got != 1 {
		t.Errorf("Remux mode should keep single rendition (no parallel encode), got %d STREAM-INF\n%s", got, pl)
	}
}

func TestBuildMasterPlaylist_TranscodeMode_HardcodedCodecs(t *testing.T) {
	job := &TranscodingJob{
		Mode: transcode.ModeSWTranscode,
		Context: transcodingContext{
			Mode: transcode.ModeSWTranscode,
			FFProbe: map[string]any{
				"streams": []any{
					map[string]any{"codec_type": "video", "codec_name": "hevc", "width": float64(1920), "height": float64(1080)},
					map[string]any{"codec_type": "audio", "codec_name": "eac3"},
				},
			},
		},
	}
	pl := buildMasterPlaylist(job, "")
	if !strings.Contains(pl, `CODECS="avc1.640029,mp4a.40.2"`) {
		t.Errorf("transcode mode should advertise h264+aac (the actual output), got %q", pl)
	}
}

// ---------------------------------------------------------------------------
// Helper coverage
// ---------------------------------------------------------------------------

func TestEstimateBandwidth_FormatLevel(t *testing.T) {
	probe := map[string]any{
		"format": map[string]any{"bit_rate": "8000000"},
	}
	got := estimateBandwidth(probe)
	if got < 8_000_000 || got > 10_000_000 {
		t.Errorf("expected ~8.8M (8M + 10%%), got %d", got)
	}
}

func TestEstimateBandwidth_Fallback(t *testing.T) {
	got := estimateBandwidth(nil)
	if got != 5_000_000 {
		t.Errorf("nil probe → 5M default, got %d", got)
	}
}

func TestEstimateCodecs_RemuxSourceHEVC(t *testing.T) {
	probe := map[string]any{
		"streams": []any{
			map[string]any{"codec_type": "video", "codec_name": "hevc"},
			map[string]any{"codec_type": "audio", "codec_name": "ac3"},
		},
	}
	got := estimateCodecs(probe, transcode.ModeRemux)
	if !strings.Contains(got, "hvc1") {
		t.Errorf("HEVC source remux should advertise hvc1, got %q", got)
	}
	if !strings.Contains(got, "ac-3") {
		t.Errorf("AC3 source remux should advertise ac-3, got %q", got)
	}
}

func TestEscapeM3U8Attr(t *testing.T) {
	cases := map[string]string{
		`Russian`:               `Russian`,
		`"Russian" track`:       `'Russian' track`,
		`title with "embedded"`: `title with 'embedded'`,
	}
	for in, want := range cases {
		if got := escapeM3U8Attr(in); got != want {
			t.Errorf("escapeM3U8Attr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJobUsesMasterPlaylist(t *testing.T) {
	mk := func(multi bool, rungs int) *TranscodingJob {
		lad := make([]transcode.ABRRung, rungs)
		return &TranscodingJob{Context: transcodingContext{MultiRung: multi, Ladder: lad}}
	}
	// Multi-rung with >1 rung → master (segments in v0/v1, main.m3u8 hangs).
	if !jobUsesMasterPlaylist(mk(true, 2)) {
		t.Error("multi-rung 2 rungs should use master")
	}
	// MultiRung flag off → single ffmpeg → root segments → main works.
	if jobUsesMasterPlaylist(mk(false, 2)) {
		t.Error("non-multirung must use main even with a planned ladder")
	}
	// Degenerate 1-rung ladder → main (no v<N> subdirs emitted).
	if jobUsesMasterPlaylist(mk(true, 1)) {
		t.Error("single-rung ladder must use main")
	}
}
