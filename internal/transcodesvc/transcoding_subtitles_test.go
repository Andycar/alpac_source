package transcodesvc

import (
	"strings"
	"testing"
)

// makeProbeWithSubs builds a synthetic ffprobe-shaped map with one video,
// one audio, and N subtitle streams.  The subs slice declares each subtitle
// stream's codec / language / disposition flags in order.
func makeProbeWithSubs(subs []map[string]any) map[string]any {
	streams := []any{
		map[string]any{
			"codec_type": "video",
			"codec_name": "h264",
			"index":      float64(0),
		},
		map[string]any{
			"codec_type": "audio",
			"codec_name": "aac",
			"index":      float64(1),
		},
	}
	for i, s := range subs {
		entry := map[string]any{
			"codec_type": "subtitle",
			"codec_name": s["codec_name"],
			"index":      float64(2 + i),
		}
		if lang, ok := s["language"]; ok {
			entry["tags"] = map[string]any{"language": lang}
		}
		if def, ok := s["default"]; ok && def == true {
			entry["disposition"] = map[string]any{"default": float64(1)}
		}
		streams = append(streams, entry)
	}
	return map[string]any{"streams": streams}
}

func TestPlanSubtitles_NotRequested(t *testing.T) {
	probe := makeProbeWithSubs([]map[string]any{
		{"codec_name": "subrip", "language": "rus"},
	})
	plan := planSubtitles(probe, "ru", "lampa-native", false, false)
	if plan.Strategy != StrategyNone {
		t.Errorf("strategy = %q, want %q", plan.Strategy, StrategyNone)
	}
}

func TestPlanSubtitles_NoStreams(t *testing.T) {
	probe := map[string]any{"streams": []any{}}
	plan := planSubtitles(probe, "ru", "lampa-native", true, false)
	if plan.Strategy != StrategyNone {
		t.Errorf("strategy = %q, want %q", plan.Strategy, StrategyNone)
	}
}

func TestPlanSubtitles_PlainSRT_Extract(t *testing.T) {
	probe := makeProbeWithSubs([]map[string]any{
		{"codec_name": "subrip", "language": "rus", "default": true},
	})
	plan := planSubtitles(probe, "ru", "lampa-native", true, false)
	if plan.Strategy != StrategyExtract {
		t.Errorf("strategy = %q, want %q (reason=%s)", plan.Strategy, StrategyExtract, plan.Reason)
	}
	if len(plan.Streams) != 1 {
		t.Errorf("got %d streams, want 1", len(plan.Streams))
	}
}

func TestPlanSubtitles_PGS_BurnIn(t *testing.T) {
	// Blu-Ray MKV with PGS subs only — most painful case from research.
	probe := makeProbeWithSubs([]map[string]any{
		{"codec_name": "hdmv_pgs_subtitle", "language": "rus", "default": true},
	})
	plan := planSubtitles(probe, "ru", "lampa-native", true, false)
	if plan.Strategy != StrategyBurnIn {
		t.Errorf("strategy = %q, want %q (reason=%s)", plan.Strategy, StrategyBurnIn, plan.Reason)
	}
	if plan.BurnIn == nil {
		t.Fatalf("BurnIn target should be populated")
	}
	if plan.BurnIn.Codec != "hdmv_pgs_subtitle" {
		t.Errorf("BurnIn.Codec = %q, want hdmv_pgs_subtitle", plan.BurnIn.Codec)
	}
	if plan.BurnIn.RelIndex != 0 {
		t.Errorf("BurnIn.RelIndex = %d, want 0", plan.BurnIn.RelIndex)
	}
}

func TestPlanSubtitles_PGS_PipeSource_Skipped(t *testing.T) {
	// Torrent / pipe source — burn-in not possible (libavformat can't
	// re-read pipe:0).  Should degrade to "none" rather than fail.
	probe := makeProbeWithSubs([]map[string]any{
		{"codec_name": "hdmv_pgs_subtitle", "language": "rus", "default": true},
	})
	plan := planSubtitles(probe, "ru", "lampa-native", true, true /* sourceIsPipe */)
	if plan.Strategy != StrategyNone {
		t.Errorf("strategy = %q, want %q (reason=%s)", plan.Strategy, StrategyNone, plan.Reason)
	}
	if !strings.Contains(plan.Reason, "pipe") {
		t.Errorf("reason should mention pipe, got %q", plan.Reason)
	}
}

func TestPlanSubtitles_DVB_BurnIn(t *testing.T) {
	probe := makeProbeWithSubs([]map[string]any{
		{"codec_name": "dvb_subtitle", "language": "rus", "default": true},
	})
	plan := planSubtitles(probe, "ru", "android-tv", true, false)
	if plan.Strategy != StrategyBurnIn {
		t.Errorf("DVB sub should burn-in, got %q (%s)", plan.Strategy, plan.Reason)
	}
}

func TestPlanSubtitles_ASS_OnTizenLegacy_BurnIn(t *testing.T) {
	// Anime release with ASS subs on a Tizen 4 TV — yumata/lampa #212.
	probe := makeProbeWithSubs([]map[string]any{
		{"codec_name": "ass", "language": "rus", "default": true},
	})
	plan := planSubtitles(probe, "ru", "tizen-4", true, false)
	if plan.Strategy != StrategyBurnIn {
		t.Errorf("ASS on tizen-4 should burn-in, got %q (%s)", plan.Strategy, plan.Reason)
	}
	if !strings.Contains(plan.Reason, "tizen") {
		t.Errorf("reason should reference profile, got %q", plan.Reason)
	}
}

func TestPlanSubtitles_ASS_OnAppleTV_Extract(t *testing.T) {
	// Apple TV / lampa-native both render WebVTT correctly enough that
	// burn-in's CPU cost isn't justified — extract pipeline wins.
	probe := makeProbeWithSubs([]map[string]any{
		{"codec_name": "ass", "language": "rus", "default": true},
	})
	plan := planSubtitles(probe, "ru", "apple-tv-17", true, false)
	if plan.Strategy != StrategyExtract {
		t.Errorf("ASS on apple-tv-17 should extract, got %q (%s)", plan.Strategy, plan.Reason)
	}
}

func TestPlanSubtitles_LangPreference(t *testing.T) {
	// Multi-track source; we want pickPreferredSubtitle to honour caps.Lang
	// over the disposition default.  English is "default" but Russian is
	// requested → Russian wins.
	probe := makeProbeWithSubs([]map[string]any{
		{"codec_name": "subrip", "language": "eng", "default": true},
		{"codec_name": "subrip", "language": "rus"},
		{"codec_name": "subrip", "language": "ukr"},
	})
	plan := planSubtitles(probe, "ru", "lampa-native", true, false)
	if plan.Strategy != StrategyExtract {
		t.Fatalf("strategy = %q, want extract", plan.Strategy)
	}
	if len(plan.Streams) != 3 {
		t.Errorf("got %d streams, want 3", len(plan.Streams))
	}
	// pickPreferredSubtitle returned the russian stream — verify by
	// running the pick directly.
	streams := extractSubtitleStreams(probe)
	idx := pickPreferredSubtitle(streams, "ru")
	if idx != 1 {
		t.Errorf("preferred index = %d, want 1 (russian)", idx)
	}
}

func TestPlanSubtitles_MixedTracks_PicksRussianBitmap(t *testing.T) {
	// English SRT (default disposition) + Russian PGS — caps.Lang=ru should
	// pick the PGS Russian and trigger burn-in (the WHOLE point of this
	// feature: PGS Russian on a TV that can't render bitmap subs).
	probe := makeProbeWithSubs([]map[string]any{
		{"codec_name": "subrip", "language": "eng", "default": true},
		{"codec_name": "hdmv_pgs_subtitle", "language": "rus"},
	})
	plan := planSubtitles(probe, "ru", "lampa-native", true, false)
	if plan.Strategy != StrategyBurnIn {
		t.Errorf("strategy = %q, want burn-in (reason=%s)", plan.Strategy, plan.Reason)
	}
	if plan.BurnIn == nil || plan.BurnIn.Codec != "hdmv_pgs_subtitle" {
		t.Errorf("BurnIn = %+v, want hdmv_pgs_subtitle", plan.BurnIn)
	}
}

// ---------------------------------------------------------------------------
// buildBurnInVideoFilter
// ---------------------------------------------------------------------------

func TestBuildBurnInVideoFilter_Bitmap(t *testing.T) {
	plan := SubtitlePlan{
		Strategy: StrategyBurnIn,
		BurnIn: &SubtitleStream{
			RelIndex: 0,
			Codec:    "hdmv_pgs_subtitle",
		},
	}
	fc, mp := buildBurnInVideoFilter(plan, "https://cdn.example.com/video.mkv")
	if mp != "[vout]" {
		t.Errorf("map = %q, want [vout]", mp)
	}
	// PGS / DVD / DVB → overlay filter.
	if !strings.Contains(fc, "overlay") {
		t.Errorf("filter_complex should use overlay for bitmap, got %q", fc)
	}
	if !strings.Contains(fc, "[0:s:0]") {
		t.Errorf("filter_complex should reference subtitle stream, got %q", fc)
	}
}

func TestBuildBurnInVideoFilter_ASS(t *testing.T) {
	plan := SubtitlePlan{
		Strategy: StrategyBurnIn,
		BurnIn: &SubtitleStream{
			RelIndex: 2,
			Codec:    "ass",
		},
	}
	fc, mp := buildBurnInVideoFilter(plan, "https://cdn.example.com/anime.mkv")
	if mp != "[vout]" {
		t.Errorf("map = %q, want [vout]", mp)
	}
	if !strings.Contains(fc, "subtitles=") {
		t.Errorf("filter_complex should use subtitles= for ASS, got %q", fc)
	}
	if !strings.Contains(fc, "si=2") {
		t.Errorf("filter_complex should reference si=2, got %q", fc)
	}
}

func TestBuildBurnInVideoFilter_NoOpForExtract(t *testing.T) {
	plan := SubtitlePlan{Strategy: StrategyExtract}
	fc, mp := buildBurnInVideoFilter(plan, "x")
	if fc != "" || mp != "" {
		t.Errorf("non-burn plan must return empty, got %q / %q", fc, mp)
	}
}

func TestEscapeFilterPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"a.mkv", "a.mkv"},
		{"http://x:80/a.mkv", `http\://x\:80/a.mkv`},
		{`a'b.mkv`, `a\'b.mkv`},
		{`C:\dir\file.mkv`, `C\:\\dir\\file.mkv`},
	}
	for _, c := range cases {
		got := escapeFilterPath(c.in)
		if got != c.want {
			t.Errorf("escapeFilterPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSubtitleCodecClassification guards the text/bitmap split that drives the
// extract-to-WebVTT vs burn-in/OCR decision. Misclassifying a bitmap codec as
// non-bitmap sends it down the WebVTT-extract path, which ffmpeg's text muxer
// can't do → a broken/empty subtitle track. vobsub/pgssub were the recent gaps.
func TestSubtitleCodecClassification(t *testing.T) {
	bitmap := []string{"hdmv_pgs_subtitle", "pgs", "pgssub", "dvd_subtitle", "dvdsub", "vobsub", "dvb_subtitle", "dvbsub", "dvb_teletext", "xsub"}
	for _, c := range bitmap {
		if !isBitmapSubtitleCodec(c) {
			t.Errorf("%q should classify as bitmap (→ burn-in/OCR)", c)
		}
		if isPlainTextSubtitle(c) || isAdvancedTextSubtitle(c) {
			t.Errorf("%q is bitmap, must not also be text", c)
		}
	}
	text := []string{"subrip", "srt", "webvtt", "vtt", "mov_text", "tx3g", "ttml", "sami", "smi"}
	for _, c := range text {
		if isBitmapSubtitleCodec(c) {
			t.Errorf("%q is plain text, must not be bitmap", c)
		}
		if !isPlainTextSubtitle(c) {
			t.Errorf("%q should classify as plain text (→ WebVTT extract)", c)
		}
	}
	for _, c := range []string{"ass", "ssa"} {
		if !isAdvancedTextSubtitle(c) || isBitmapSubtitleCodec(c) {
			t.Errorf("%q should be advanced text, not bitmap", c)
		}
	}
}
