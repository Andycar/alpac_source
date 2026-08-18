package transcodesvc

import (
	"strings"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/transcode"
)

// TestCompatibilityReport_Ubuntu2004 covers the most common production
// case: Ubuntu 20.04 ships ffmpeg 4.2.7.  All ≥5 features must report
// inactive with helpful fallback descriptions.
func TestCompatibilityReport_Ubuntu2004(t *testing.T) {
	report := CompatibilityReport(4)
	if len(report) == 0 {
		t.Fatal("compatibility report must not be empty")
	}

	byName := make(map[string]CompatStatus, len(report))
	for _, s := range report {
		byName[s.Feature.Name] = s
	}

	// All known features that need ≥5 must be inactive on 4.x.
	mustBeInactive := []string{"abr_multirung", "fps_mode_cfr", "hls_init_time", "fmp4_hls", "readrate"}
	for _, name := range mustBeInactive {
		s, ok := byName[name]
		if !ok {
			t.Errorf("feature %q missing from report", name)
			continue
		}
		if s.Active {
			t.Errorf("feature %q must be inactive on ffmpeg 4.x, got Active=true", name)
		}
		if s.Fallback == "" {
			t.Errorf("feature %q must have a Fallback description when inactive", name)
		}
	}

	// readrate_initial_burst needs 7+ — also inactive on 4.x.
	if byName["readrate_initial_burst"].Active {
		t.Errorf("readrate_initial_burst must be inactive on 4.x")
	}
}

func TestCompatibilityReport_FFmpeg5(t *testing.T) {
	report := CompatibilityReport(5)
	for _, s := range report {
		if s.Feature.Name == "readrate_initial_burst" {
			if s.Active {
				t.Errorf("readrate_initial_burst needs ffmpeg 7+, must be inactive on 5")
			}
			continue
		}
		if !s.Active {
			t.Errorf("feature %q (min %d) must be active on ffmpeg 5", s.Feature.Name, s.Feature.MinMajor)
		}
	}
}

func TestCompatibilityReport_FFmpeg7_AllActive(t *testing.T) {
	report := CompatibilityReport(7)
	for _, s := range report {
		if !s.Active {
			t.Errorf("feature %q must be active on ffmpeg 7", s.Feature.Name)
		}
	}
}

func TestCompatibilityReport_UnknownVersion(t *testing.T) {
	report := CompatibilityReport(0)
	for _, s := range report {
		if s.Active {
			t.Errorf("unknown version (0) must report all features inactive, got %q active", s.Feature.Name)
		}
	}
}

// TestFeatureActive_PerFeature verifies the FeatureActive shortcut returns
// the same answer the report does.
func TestFeatureActive_PerFeature(t *testing.T) {
	cases := []struct {
		major   int
		feature string
		want    bool
	}{
		{4, "abr_multirung", false},
		{4, "fmp4_hls", false},
		{5, "abr_multirung", true},
		{5, "fmp4_hls", true},
		{5, "readrate_initial_burst", false},
		{7, "readrate_initial_burst", true},
		{7, "abr_multirung", true},
		{4, "unknown_feature_name", false}, // typo defends against accidental enable
		{99, "abr_multirung", true},
	}
	for _, c := range cases {
		svc := &TranscodingService{ffmpegMajorVersion: c.major}
		if got := svc.FeatureActive(c.feature); got != c.want {
			t.Errorf("FeatureActive(major=%d, %q) = %v, want %v", c.major, c.feature, got, c.want)
		}
	}
}

// TestShouldMultiRung_LegacyFFmpegBlockedByDefault verifies the user's
// concrete case: ffmpeg 4.2.7 on Ubuntu 20.04 must NOT trigger multi-rung
// unless the operator explicitly opts in via ForceABROnLegacyFFmpeg.
func TestShouldMultiRung_LegacyFFmpegBlockedByDefault(t *testing.T) {
	svc := &TranscodingService{
		ffmpegMajorVersion: 4,
		cfg: config.Config{
			Transcoding: config.TranscodingConf{},
		},
	}
	ctx := transcodingContext{
		Mode: transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	if got := svc.shouldMultiRung(ctx); got != nil {
		t.Errorf("ffmpeg 4.x must default-deny multi-rung, got %d rungs", len(got))
	}
}

// TestShouldMultiRung_LegacyFFmpegOptIn verifies the override flag works.
func TestShouldMultiRung_LegacyFFmpegOptIn(t *testing.T) {
	svc := &TranscodingService{
		ffmpegMajorVersion: 4,
		cfg: config.Config{
			Transcoding: config.TranscodingConf{
				ForceABROnLegacyFFmpeg: true,
			},
		},
	}
	ctx := transcodingContext{
		Mode: transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	got := svc.shouldMultiRung(ctx)
	if got == nil {
		t.Errorf("ForceABROnLegacyFFmpeg=true must allow multi-rung even on 4.x")
	}
}

// TestShouldMultiRung_FFmpeg5_NoFlagNeeded verifies 5+ doesn't need the
// override (the gate evaluates Active=true on the feature table).
func TestShouldMultiRung_FFmpeg5_NoFlagNeeded(t *testing.T) {
	svc := &TranscodingService{
		ffmpegMajorVersion: 5,
		cfg: config.Config{
			Transcoding: config.TranscodingConf{},
		},
	}
	ctx := transcodingContext{
		Mode: transcode.ModeSWTranscode,
		FFProbe: map[string]any{
			"streams": []any{map[string]any{"codec_type": "video", "width": float64(1920), "height": float64(1080)}},
		},
	}
	got := svc.shouldMultiRung(ctx)
	if got == nil {
		t.Errorf("ffmpeg 5 must enable multi-rung without any opt-in flag")
	}
}

// TestCompatibilityReport_OrderingActiveFirst sanity-checks the sort
// order so the log block is readable (active features first).
func TestCompatibilityReport_OrderingActiveFirst(t *testing.T) {
	report := CompatibilityReport(5)
	seenInactive := false
	for _, s := range report {
		if !s.Active {
			seenInactive = true
		} else if seenInactive {
			t.Errorf("active feature %q appeared after inactive ones — order broken", s.Feature.Name)
		}
	}
}

// TestLogCompatibilityReport_Smoke runs the log function for both
// branches (known + unknown version) to confirm it doesn't panic.
// Output is dropped because we don't assert on log content here.
func TestLogCompatibilityReport_Smoke(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("LogCompatibilityReport must not panic, got %v", r)
		}
	}()
	LogCompatibilityReport(0)
	LogCompatibilityReport(4)
	LogCompatibilityReport(5)
	LogCompatibilityReport(7)
}

// TestLogCompatibilityReport_Format spot-checks that the report still
// includes the expected feature names (regression guard if the table is
// ever accidentally cleared).
func TestLogCompatibilityReport_Format(t *testing.T) {
	report := CompatibilityReport(4)
	names := make([]string, 0, len(report))
	for _, s := range report {
		names = append(names, s.Feature.Name)
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"abr_multirung", "fmp4_hls", "fps_mode_cfr", "readrate", "hls_init_time"} {
		if !strings.Contains(joined, want) {
			t.Errorf("compatibility table missing feature %q (regression?)", want)
		}
	}
}
