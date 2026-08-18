package transcodesvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/transcode"
)

// testTranscodingCfg returns a minimal config for exercising argv builders.
func testTranscodingCfg() config.Config {
	return config.Config{
		Transcoding: config.TranscodingConf{
			Enable:        true,
			FFmpeg:        "ffmpeg",
			MaxConcurrent: 5,
			HLS: config.TranscodingHLSOptions{
				SegDur:  6,
				WinSize: 10,
				FMP4:    true,
				Command: map[string][]string{
					"output": {"-max_delay 5000000"},
				},
			},
			Audio: config.TranscodingAudioOptions{
				BitrateKbps: 192,
				Stereo:      true,
				CodecCopy:   []string{"aac"},
			},
			Subtitle: config.TranscodingSubtitleOptions{
				Codec: []string{"subrip", "ass", "ssa"},
				Command: []string{
					"-map 0:{subIndex}", "-an -vn", "-c:s webvtt",
					"-f webvtt", "subs_{subIndex}.vtt",
				},
			},
			Command: map[string][]string{
				"demuxer": {"-threads 0", "-fflags +genpts"},
				"input":   {"-avoid_negative_ts disabled"},
			},
		},
	}
}

func TestShouldMultiRung_BestEffortDisqualified(t *testing.T) {
	ctx := transcodingContext{
		BestEffort: true,
		Mode:       transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	if got := (&TranscodingService{}).shouldMultiRung(ctx); got != nil {
		t.Errorf("best-effort jobs must opt out of multi-rung, got %d rungs", len(got))
	}
}

func TestShouldMultiRung_LiveDisqualified(t *testing.T) {
	ctx := transcodingContext{
		Live: true,
		Mode: transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	if got := (&TranscodingService{}).shouldMultiRung(ctx); got != nil {
		t.Errorf("live jobs must opt out, got %d rungs", len(got))
	}
}

// TestShouldMultiRung_HWModeWithoutBackend_Disqualified covers the case
// where the smart-mode picked hw-transcode but no HW backend was detected
// at startup (svc.hwAccel == nil).  Multi-rung must opt out so the
// fallback single-rung path can run libx264 instead.
func TestShouldMultiRung_HWModeWithoutBackend_Disqualified(t *testing.T) {
	ctx := transcodingContext{
		Mode: transcode.ModeHWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	if got := (&TranscodingService{}).shouldMultiRung(ctx); got != nil {
		t.Errorf("HW mode without backend must opt out of multi-rung, got %d rungs", len(got))
	}
}

func TestShouldMultiRung_RemuxDisqualified(t *testing.T) {
	ctx := transcodingContext{
		Mode: transcode.ModeRemux,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	if got := (&TranscodingService{}).shouldMultiRung(ctx); got != nil {
		t.Errorf("remux mode shouldn't multi-rung — would need a parallel encode, got %d rungs", len(got))
	}
}

func TestShouldMultiRung_BurnInDisqualified(t *testing.T) {
	ctx := transcodingContext{
		Mode: transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
		SubPlan: SubtitlePlan{
			Strategy: StrategyBurnIn,
			BurnIn:   &SubtitleStream{Codec: "hdmv_pgs_subtitle"},
		},
	}
	if got := (&TranscodingService{}).shouldMultiRung(ctx); got != nil {
		t.Errorf("burn-in subs require single video output, got %d rungs", len(got))
	}
}

func TestShouldMultiRung_LowResDisqualified(t *testing.T) {
	ctx := transcodingContext{
		Mode: transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(854), "height": float64(480)}},
		},
	}
	if got := (&TranscodingService{}).shouldMultiRung(ctx); got != nil {
		t.Errorf("480p source has 1-rung ladder, got %d rungs", len(got))
	}
}

func TestShouldMultiRung_1080p_SWTranscode_Eligible(t *testing.T) {
	ctx := transcodingContext{
		Mode: transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	// P3.R: shouldMultiRung now gates on ffmpeg ≥ 5; supply that explicitly.
	svc := &TranscodingService{ffmpegMajorVersion: 5}
	got := svc.shouldMultiRung(ctx)
	if got == nil {
		t.Fatalf("1080p SW transcode should be eligible")
	}
	if len(got) != 3 {
		t.Errorf("1080p ladder should be 3 rungs (1080+720+480), got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// HW multi-rung — P3.N
// ---------------------------------------------------------------------------

func TestShouldMultiRung_HWTranscode_NVENC_Eligible(t *testing.T) {
	svc := &TranscodingService{
		ffmpegMajorVersion: 5,
		hwAccel:            &HWAccelInfo{Kind: HWNVENC, Detected: true},
	}
	ctx := transcodingContext{
		Mode: transcode.ModeHWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	got := svc.shouldMultiRung(ctx)
	if got == nil {
		t.Fatalf("HW transcode on NVENC should be eligible for multi-rung")
	}
}

func TestShouldMultiRung_HWTranscode_VAAPI_NotEligible(t *testing.T) {
	// VAAPI needs backend-specific filter graphs we haven't wired yet.
	// Multi-rung must opt out cleanly so the job stays single-rung HW.
	svc := &TranscodingService{
		hwAccel: &HWAccelInfo{Kind: HWVAAPI, Detected: true},
	}
	ctx := transcodingContext{
		Mode: transcode.ModeHWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	if got := svc.shouldMultiRung(ctx); got != nil {
		t.Errorf("VAAPI multi-rung not yet supported, got %d rungs", len(got))
	}
}

func TestShouldMultiRung_HWTranscode_DisableHWFallsBack(t *testing.T) {
	svc := &TranscodingService{
		hwAccel: &HWAccelInfo{Kind: HWNVENC, Detected: true},
	}
	ctx := transcodingContext{
		Mode:      transcode.ModeHWTranscode,
		DisableHW: true, // escalation cascade earlier disabled HW for this job
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	if got := svc.shouldMultiRung(ctx); got != nil {
		t.Errorf("DisableHW should rule out HW multi-rung, got %d rungs", len(got))
	}
}

// ---------------------------------------------------------------------------
// HWAccelInfo per-rung argv builder
// ---------------------------------------------------------------------------

func TestAppendMultiRungVideoArgs_NVENC(t *testing.T) {
	h := &HWAccelInfo{Kind: HWNVENC, Detected: true}
	rung := transcode.ABRRung{Label: "1080p", Width: 1920, Height: 1080, BitrateKbps: 6000, Primary: true}
	args := h.appendMultiRungVideoArgs(nil, 0, rung, 180)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-filter:v:0 scale=1920:-2",
		"-c:v:0 h264_nvenc",
		"-b:v:0 6000k",
		"-g:v:0 180",
		"-rc:v:0 vbr",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("NVENC per-rung args missing %q in: %s", want, joined)
		}
	}
}

func TestAppendMultiRungVideoArgs_VideoToolbox(t *testing.T) {
	h := &HWAccelInfo{Kind: HWVideoToolbox, Detected: true}
	rung := transcode.ABRRung{Label: "720p", Width: 1280, Height: 720, BitrateKbps: 3000}
	args := h.appendMultiRungVideoArgs(nil, 1, rung, 180)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-filter:v:1 scale=1280:-2",
		"-c:v:1 h264_videotoolbox",
		"-allow_sw:v:1 1",
		"-b:v:1 3000k",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("VideoToolbox per-rung args missing %q in: %s", want, joined)
		}
	}
}

func TestSupportsMultiRung_PerBackend(t *testing.T) {
	cases := []struct {
		kind HWKind
		want bool
	}{
		{HWNVENC, true},
		{HWVideoToolbox, true},
		{HWVAAPI, false},
		{HWQSV, false},
		{HWRKMPP, false},
		{HWV4L2M2M, false},
		{HWNone, false},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			h := &HWAccelInfo{Kind: c.kind, Detected: c.kind != HWNone}
			if got := h.supportsMultiRung(); got != c.want {
				t.Errorf("kind %q: supportsMultiRung = %v, want %v", c.kind, got, c.want)
			}
		})
	}
}

// TestBuildMultiRungArgs_HWNVENC verifies that when an NVENC HW backend is
// active, the multi-rung builder emits h264_nvenc per rung instead of
// libx264.
func TestBuildMultiRungArgs_HWNVENC(t *testing.T) {
	svc := &TranscodingService{
		ffmpegMajorVersion: 5,
		cfg:                testTranscodingCfg(),
		hwAccel:            &HWAccelInfo{Kind: HWNVENC, Detected: true},
	}
	ctx := transcodingContext{
		Source: "https://cdn.example.com/movie.mkv",
		HLS:    transcodingHLSCtx{SegDur: 6, FMP4: true},
		Audio:  transcodingAudioCtx{Index: 0, BitrateKbps: 128, Stereo: true},
		Mode:   transcode.ModeHWTranscode,
		FFProbe: map[string]any{
			"streams": []any{
				map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)},
				map[string]any{"codec_type": "audio"},
			},
		},
		MultiRung: true,
		Ladder: []transcode.ABRRung{
			{Label: "1080p", Width: 1920, Height: 1080, BitrateKbps: 6000, Primary: true},
			{Label: "720p", Width: 1280, Height: 720, BitrateKbps: 3000},
		},
	}
	args := svc.buildMultiRungArgs(ctx)
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "libx264") {
		t.Errorf("HW path must not emit libx264, got: %s", joined)
	}
	if !strings.Contains(joined, "h264_nvenc") {
		t.Errorf("expected h264_nvenc in HW path, got: %s", joined)
	}
}

// ---------------------------------------------------------------------------
// ensureMultiRungDirs
// ---------------------------------------------------------------------------

func TestEnsureMultiRungDirs(t *testing.T) {
	dir := t.TempDir()
	ladder := []transcode.ABRRung{
		{Label: "1080p"},
		{Label: "720p"},
		{Label: "480p"},
	}
	if err := ensureMultiRungDirs(dir, ladder); err != nil {
		t.Fatalf("ensureMultiRungDirs returned error: %v", err)
	}
	for i := range ladder {
		path := filepath.Join(dir, "v"+intToStr(i))
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected v%d subdir to exist, stat err: %v", i, err)
		}
		if !info.IsDir() {
			t.Errorf("v%d should be a directory", i)
		}
	}
}

// ---------------------------------------------------------------------------
// buildMultiRungArgs argv shape
// ---------------------------------------------------------------------------

func TestBuildMultiRungArgs_BasicShape(t *testing.T) {
	svc := &TranscodingService{
		ffmpegMajorVersion: 5,
		cfg:                testTranscodingCfg(),
	}
	ctx := transcodingContext{
		Source:    "https://cdn.example.com/movie.mkv",
		UserAgent: "Mozilla/5.0",
		HLS:       transcodingHLSCtx{SegDur: 6, WinSize: 10, FMP4: true},
		Audio:     transcodingAudioCtx{Index: 0, BitrateKbps: 128, Stereo: true},
		Mode:      transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{
				map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)},
				map[string]any{"codec_type": "audio"},
			},
		},
		MultiRung: true,
		Ladder: []transcode.ABRRung{
			{Label: "1080p", Width: 1920, Height: 1080, BitrateKbps: 6000, Primary: true},
			{Label: "720p", Width: 1280, Height: 720, BitrateKbps: 3000},
			{Label: "480p", Width: 854, Height: 480, BitrateKbps: 1500},
		},
	}
	args := svc.buildMultiRungArgs(ctx)
	joined := strings.Join(args, " ")

	// One -map 0:v:0 AND one -map 0:a:0 per rung (each variant owns a distinct
	// audio output — ffmpeg 7+ rejects sharing one a:0 across variants).
	if got := strings.Count(joined, "-map 0:v:0"); got != 3 {
		t.Errorf("expected 3 -map 0:v:0 entries (one per rung), got %d in: %s", got, joined)
	}
	if got := strings.Count(joined, "-map 0:a:0"); got != 3 {
		t.Errorf("expected 3 -map 0:a:0 entries (one per rung), got %d in: %s", got, joined)
	}

	// Per-rung filter+codec specs.
	for _, want := range []string{
		"-filter:v:0 scale=1920:-2",
		"-filter:v:1 scale=1280:-2",
		"-filter:v:2 scale=854:-2",
		"-c:v:0 libx264",
		"-c:v:1 libx264",
		"-c:v:2 libx264",
		"-b:v:0 6000k",
		"-b:v:1 3000k",
		"-b:v:2 1500k",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in argv:\n%s", want, joined)
		}
	}

	// var_stream_map must pair every rung with its OWN audio output (v:i,a:i).
	if !strings.Contains(joined, "-var_stream_map v:0,a:0 v:1,a:1 v:2,a:2") {
		t.Errorf("missing or malformed var_stream_map: %s", joined)
	}

	// Per-variant output paths must use %v substitution.
	for _, want := range []string{
		"v%v/seg_%05d.m4s",
		"v%v/index.m3u8",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected output path %q in argv: %s", want, joined)
		}
	}

	// Master playlist gets a separate name so our Go handler stays
	// authoritative.
	if !strings.Contains(joined, "-master_pl_name ffmpeg_master.m3u8") {
		t.Errorf("ffmpeg's master should NOT clash with our handler's master.m3u8: %s", joined)
	}

	// Subs not requested → -sn must appear.
	if !strings.Contains(joined, "-sn") {
		t.Errorf("multi-rung without subs should emit -sn: %s", joined)
	}
}

func TestBuildMultiRungArgs_WithSubtitleExtract(t *testing.T) {
	svc := &TranscodingService{
		ffmpegMajorVersion: 5,
		cfg:                testTranscodingCfg(),
	}
	ctx := transcodingContext{
		Source: "https://cdn.example.com/movie.mkv",
		HLS:    transcodingHLSCtx{SegDur: 6, WinSize: 10, FMP4: true},
		Audio:  transcodingAudioCtx{Index: 0, BitrateKbps: 128},
		Mode:   transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{
				map[string]any{"codec_type": "video", "codec_name": "h264", "width": float64(1920), "height": float64(1080), "index": float64(0)},
				map[string]any{"codec_type": "audio", "codec_name": "aac", "index": float64(1)},
				map[string]any{"codec_type": "subtitle", "codec_name": "subrip", "index": float64(2)},
			},
		},
		MultiRung: true,
		Ladder: []transcode.ABRRung{
			{Label: "1080p", Width: 1920, Height: 1080, BitrateKbps: 6000, Primary: true},
			{Label: "720p", Width: 1280, Height: 720, BitrateKbps: 3000},
		},
		SubPlan: SubtitlePlan{
			Strategy: StrategyExtract,
			Streams:  []SubtitleStream{{AbsIndex: 2, RelIndex: 0, Codec: "subrip"}},
		},
	}
	args := svc.buildMultiRungArgs(ctx)
	joined := strings.Join(args, " ")

	// Subtitle extract appends the configured subtitle command for index 2.
	if !strings.Contains(joined, "subs_2.vtt") {
		t.Errorf("expected subtitle extract output for stream 2: %s", joined)
	}
}
