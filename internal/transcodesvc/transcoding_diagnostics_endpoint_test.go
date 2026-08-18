package transcodesvc

import (
	"lampac-go/internal/transcode"
	"strings"
	"testing"
	"time"
)

// TestBuildDiagnosticsSnapshot_NilJob verifies the helper doesn't panic
// on a nil job (which can't happen in practice but defends against
// future refactors).
func TestBuildDiagnosticsSnapshot_NilJob(t *testing.T) {
	got := buildDiagnosticsSnapshot(nil)
	if got["error"] == nil {
		t.Errorf("nil job should return an error field")
	}
}

// TestBuildDiagnosticsSnapshot_FullPicture covers a realistic job with
// most fields populated — verifies every observability surface ends up
// in the response.
func TestBuildDiagnosticsSnapshot_FullPicture(t *testing.T) {
	job := &TranscodingJob{
		ID:           "abc123",
		StreamID:     "tok_abc123",
		Mode:         transcode.ModeSWTranscode,
		ProfileLabel: "tizen-5",
		KnownIssues:  []string{"Tizen 5: 10-bit HEVC unreliable"},
		Warning:      "probe was best-effort",
		StartedUtc:   time.Now().UTC(),
		Context: transcodingContext{
			Mode:      transcode.ModeSWTranscode,
			Live:      false,
			Subtitles: true,
			HLS:       transcodingHLSCtx{SegDur: 6, WinSize: 10, FMP4: true, Seek: 0},
			SubPlan: SubtitlePlan{
				Strategy: StrategyExtract,
				Streams:  []SubtitleStream{{AbsIndex: 2, Codec: "subrip", Lang: "rus"}},
				Reason:   "extract subrip → WebVTT",
			},
			MultiRung: true,
			Ladder: []transcode.ABRRung{
				{Label: "1080p", Width: 1920, Height: 1080, BitrateKbps: 6000, Primary: true},
				{Label: "720p", Width: 1280, Height: 720, BitrateKbps: 3000},
			},
			FFProbe: map[string]any{
				"streams": []any{
					map[string]any{"codec_type": "video", "codec_name": "hevc", "pix_fmt": "yuv420p10le", "width": float64(1920), "height": float64(1080)},
					map[string]any{"codec_type": "audio", "codec_name": "eac3"},
				},
			},
		},
	}
	job.lastAccess.Store(time.Now())

	snap := buildDiagnosticsSnapshot(job)

	// Identifiers
	if snap["id"] != "abc123" {
		t.Errorf("id = %v, want abc123", snap["id"])
	}
	if snap["mode"] != "sw-transcode" {
		t.Errorf("mode = %v, want sw-transcode", snap["mode"])
	}
	if snap["profile"] != "tizen-5" {
		t.Errorf("profile = %v, want tizen-5", snap["profile"])
	}

	// Subtitle plan should be summarized in.
	subPlan, ok := snap["subtitlePlan"].(map[string]any)
	if !ok {
		t.Fatalf("subtitlePlan should be a map, got %T", snap["subtitlePlan"])
	}
	if subPlan["strategy"] != "extract" {
		t.Errorf("subtitlePlan.strategy = %v, want extract", subPlan["strategy"])
	}

	// ABR ladder must be present with both rungs.
	abr, ok := snap["abrLadder"].(map[string]any)
	if !ok {
		t.Fatalf("abrLadder should be a map, got %T", snap["abrLadder"])
	}
	rungs, ok := abr["rungs"].([]map[string]any)
	if !ok || len(rungs) != 2 {
		t.Errorf("abrLadder.rungs should have 2 entries, got %v", abr["rungs"])
	}
	if abr["multiRung"] != true {
		t.Errorf("abrLadder.multiRung should be true (job actually emits multi-rung), got %v", abr["multiRung"])
	}

	// 10-bit detection from probe must surface.
	if snap["sourcePixFmt"] != "yuv420p10le" {
		t.Errorf("sourcePixFmt = %v, want yuv420p10le", snap["sourcePixFmt"])
	}
	if snap["sourceIs10Bit"] != true {
		t.Errorf("sourceIs10Bit should be true for yuv420p10le")
	}

	// Known issues from UA-DB profile must surface.
	issues, ok := snap["knownIssues"].([]string)
	if !ok || len(issues) == 0 {
		t.Errorf("knownIssues should be populated, got %v", snap["knownIssues"])
	}

	// HLS state must round-trip.
	hls, ok := snap["hls"].(map[string]any)
	if !ok {
		t.Fatalf("hls should be a map")
	}
	if hls["segDur"] != 6 {
		t.Errorf("hls.segDur = %v, want 6", hls["segDur"])
	}

	// hasSubtitles flag must reflect ctx.
	if snap["hasSubtitles"] != true {
		t.Errorf("hasSubtitles should be true when ctx.Subtitles=true")
	}
}

// TestBuildDiagnosticsSnapshot_RecentLogIncludesClassification verifies
// that when stderr lines look like a known failure pattern, the
// classifier output is surfaced as lastErrorClass.
func TestBuildDiagnosticsSnapshot_RecentLogIncludesClassification(t *testing.T) {
	job := &TranscodingJob{
		ID:       "x",
		StreamID: "y",
		Mode:     transcode.ModeHWTranscode,
		Context:  transcodingContext{Mode: transcode.ModeHWTranscode, FFProbe: map[string]any{}},
		maxLog:   100,
	}
	job.lastAccess.Store(time.Now())
	// Inject HW failure stderr into the rolling log.
	job.AppendLog("[h264_nvenc] failed to initialize CUDA")

	snap := buildDiagnosticsSnapshot(job)
	cls, ok := snap["lastErrorClass"].(string)
	if !ok {
		t.Fatalf("lastErrorClass should be set when log matches a pattern, got %v", snap["lastErrorClass"])
	}
	if cls != string(ErrHWUnavailable) {
		t.Errorf("lastErrorClass = %q, want %q", cls, ErrHWUnavailable)
	}
}

// TestBuildDiagnosticsSnapshot_NoSubsKeyOmittedWhenNonePlan verifies
// that subtitlePlan is NOT in the output when the plan strategy is empty
// (legacy job without the new planner).
func TestBuildDiagnosticsSnapshot_NoSubsKeyOmittedWhenNonePlan(t *testing.T) {
	job := &TranscodingJob{
		ID:       "x",
		StreamID: "y",
		Mode:     transcode.ModeRemux,
		Context:  transcodingContext{Mode: transcode.ModeRemux, FFProbe: map[string]any{}},
	}
	job.lastAccess.Store(time.Now())
	snap := buildDiagnosticsSnapshot(job)
	if _, ok := snap["subtitlePlan"]; ok {
		t.Errorf("subtitlePlan should be omitted when SubPlan.Strategy is empty, got %v", snap["subtitlePlan"])
	}
}

// TestExtractPixFmt covers the helper directly.
func TestExtractPixFmt(t *testing.T) {
	cases := []struct {
		name  string
		probe map[string]any
		want  string
	}{
		{"nil probe", nil, ""},
		{"no streams", map[string]any{"streams": []any{}}, ""},
		{
			"video stream with pix_fmt",
			map[string]any{
				"streams": []any{
					map[string]any{"codec_type": "video", "pix_fmt": "yuv420p"},
				},
			},
			"yuv420p",
		},
		{
			"audio first then video — picks video",
			map[string]any{
				"streams": []any{
					map[string]any{"codec_type": "audio"},
					map[string]any{"codec_type": "video", "pix_fmt": "yuv420p10le"},
				},
			},
			"yuv420p10le",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPixFmt(tc.probe)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildDiagnosticsSnapshot_StableKeyOrdering is a smoke-test that we
// don't crash on a minimal job; format errors are JSON-marshal issues
// that we don't unit-test here directly but rely on this exercising the
// happy path enough to surface them.
func TestBuildDiagnosticsSnapshot_MinimalJob_NoCrash(t *testing.T) {
	job := &TranscodingJob{
		StreamID: "min",
	}
	job.lastAccess.Store(time.Now())
	snap := buildDiagnosticsSnapshot(job)
	if snap == nil {
		t.Fatal("snap should not be nil even for minimal job")
	}
	// Must at least include the streamId.
	if snap["streamId"] != "min" {
		t.Errorf("streamId missing from minimal snapshot")
	}
	// Mode is "" (zero), but the field should still be present so consumers
	// can rely on key existence.
	if _, ok := snap["mode"]; !ok {
		t.Errorf("mode field must be present even when zero-valued")
	}
}

// TestBuildDiagnosticsSnapshot_BurnInExposesTarget verifies the burn-in
// path exposes the chosen subtitle stream so admins can confirm exactly
// which track is rendered into video.
func TestBuildDiagnosticsSnapshot_BurnInExposesTarget(t *testing.T) {
	job := &TranscodingJob{
		StreamID: "burn",
		Mode:     transcode.ModeSWTranscode,
		Context: transcodingContext{
			Mode: transcode.ModeSWTranscode,
			SubPlan: SubtitlePlan{
				Strategy: StrategyBurnIn,
				BurnIn:   &SubtitleStream{AbsIndex: 3, RelIndex: 0, Codec: "hdmv_pgs_subtitle", Lang: "rus"},
				Streams:  []SubtitleStream{{AbsIndex: 3, RelIndex: 0, Codec: "hdmv_pgs_subtitle", Lang: "rus"}},
				Reason:   "burn-in PGS",
			},
			FFProbe: map[string]any{},
		},
	}
	job.lastAccess.Store(time.Now())
	snap := buildDiagnosticsSnapshot(job)
	subPlan, ok := snap["subtitlePlan"].(map[string]any)
	if !ok {
		t.Fatalf("subtitlePlan should be set for burn-in job")
	}
	if subPlan["burn_in_codec"] != "hdmv_pgs_subtitle" {
		t.Errorf("burn_in_codec = %v, want hdmv_pgs_subtitle", subPlan["burn_in_codec"])
	}
	if !strings.Contains(subPlan["reason"].(string), "burn-in") {
		t.Errorf("reason should mention burn-in, got %v", subPlan["reason"])
	}
}
