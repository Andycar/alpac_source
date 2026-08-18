package transcodesvc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// FFmpeg version compatibility audit — P3.R.
//
// Lampac runs in the wild on whatever ffmpeg the host distro ships:
//
//   - Ubuntu 20.04 LTS:  ffmpeg 4.2.x   (very common — 5+ years lifecycle)
//   - Ubuntu 22.04 LTS:  ffmpeg 4.4.x
//   - Debian 11:         ffmpeg 4.3.x
//   - Debian 12:         ffmpeg 5.1.x
//   - Ubuntu 24.04:      ffmpeg 6.1.x
//   - Custom builds:     anywhere from 4.0 to 7.x
//
// Several features we shipped this round depend on ffmpeg ≥ 5 — the
// `-fps_mode cfr` flag, ABR multi-rung's per-variant init.mp4 handling,
// `-hls_init_time` (4.4+ but easier to gate on 5).  Up to now those
// gates were scattered ad-hoc through the argv builders.  This file
// consolidates them into one table, exposes a startup advisory log so
// operators see exactly what's degraded on their box, and adds an
// opt-in escape hatch (LegacyFFmpegABR) for operators who tested
// multi-rung on their 4.x build and want to use it.
// ---------------------------------------------------------------------------

// FFmpegFeature identifies one of the version-gated capabilities we ship.
type FFmpegFeature struct {
	Name        string // stable id, e.g. "abr_multirung"
	MinMajor    int    // minimum ffmpeg major version that supports it cleanly
	Description string // one-line user-facing explanation
}

// ffmpegFeatureTable lists the version-gated features in the transcoding
// pipeline, ordered by descending importance.  When you add a new
// version-conditional code path elsewhere, add it here too — the
// compatibility report uses this table to tell operators what's gated.
var ffmpegFeatureTable = []FFmpegFeature{
	{
		Name:        "abr_multirung",
		MinMajor:    5,
		Description: "ABR multi-rendition output (var_stream_map + per-variant init.mp4)",
	},
	{
		Name:        "fps_mode_cfr",
		MinMajor:    5,
		Description: "post-seek constant-frame-rate (-fps_mode cfr; 4.x falls back to -vsync cfr)",
	},
	{
		Name:        "hls_init_time",
		MinMajor:    5,
		Description: "fast-start short first segment (-hls_init_time 1)",
	},
	{
		Name:        "fmp4_hls",
		MinMajor:    5,
		Description: "fmp4 HLS segments (4.x downgrades automatically to mpegts)",
	},
	{
		Name:        "readrate",
		MinMajor:    5,
		Description: "playback-rate throttle (-readrate)",
	},
	{
		Name:        "readrate_initial_burst",
		MinMajor:    7,
		Description: "initial burst before -readrate kicks in",
	},
}

// CompatStatus is the result of evaluating one feature against a
// concrete ffmpeg major version.
type CompatStatus struct {
	Feature  FFmpegFeature
	Active   bool   // true when host version meets MinMajor
	Fallback string // human-readable description of what runs instead when Active is false
}

// fallbackDescriptions explains the per-feature graceful-degradation
// path in human terms — surfaced in logs and the /transcoding/stats
// JSON so operators can tell at a glance what behaviour they're
// actually getting.
var fallbackDescriptions = map[string]string{
	"abr_multirung":          "single-rendition only (player gets one bitrate)",
	"fps_mode_cfr":           "uses -vsync cfr instead (same effect, deprecated flag name)",
	"hls_init_time":          "first segment uses full hls_time duration",
	"fmp4_hls":               "auto-downgraded to mpegts segments",
	"readrate":               "no playback-rate throttling — ffmpeg writes as fast as it can",
	"readrate_initial_burst": "no burst window — first segment may take longer to materialise",
}

// CompatibilityReport returns the per-feature status for a given
// ffmpeg major version.  Pure function — no I/O, no side effects.
func CompatibilityReport(major int) []CompatStatus {
	out := make([]CompatStatus, 0, len(ffmpegFeatureTable))
	for _, f := range ffmpegFeatureTable {
		s := CompatStatus{Feature: f, Active: major >= f.MinMajor}
		if !s.Active {
			s.Fallback = fallbackDescriptions[f.Name]
		}
		out = append(out, s)
	}
	// Sort active-first then by name so the log block is readable.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return out[i].Feature.Name < out[j].Feature.Name
	})
	return out
}

// LogCompatibilityReport prints a structured advisory log block listing
// which features run normally and which fall back due to the detected
// ffmpeg version.  Called once at service startup after detection.
//
// The output looks like:
//
//	transcoding: ffmpeg compatibility report
//	  active: fmp4_hls fps_mode_cfr ...
//	  degraded: abr_multirung (single-rendition only) ...
//	  upgrade: ffmpeg ≥ 5 unlocks all features
func LogCompatibilityReport(major int) {
	if major <= 0 {
		log.Warn().Msg("transcoding: ffmpeg version unknown — assuming legacy 4.x compatibility profile")
		major = 4
	}
	report := CompatibilityReport(major)

	var active, degraded []string
	for _, s := range report {
		if s.Active {
			active = append(active, s.Feature.Name)
		} else {
			degraded = append(degraded, fmt.Sprintf("%s (%s)", s.Feature.Name, s.Fallback))
		}
	}

	ev := log.Info().Int("ffmpeg_major", major)
	if len(active) > 0 {
		ev = ev.Str("active", strings.Join(active, ", "))
	}
	if len(degraded) > 0 {
		ev = ev.Str("degraded", strings.Join(degraded, "; "))
	}
	ev.Msg("transcoding: ffmpeg compatibility report")

	// Only nag operators when their version is below the floor where the
	// majority of features land (5).  Hosts on 5/6 still see one or two
	// "future-looking" features as degraded (readrate_initial_burst
	// needs 7+) but that's not severe enough to warrant a warning.
	if major < 5 {
		log.Warn().
			Int("ffmpeg_major", major).
			Int("recommended", 5).
			Msg("transcoding: ffmpeg < 5 detected — some features run in fallback mode (see compatibility report). " +
				"Upgrading to ffmpeg ≥ 5 (Debian 12, Ubuntu 22.04+, or apt PPA) unlocks ABR multi-rendition, " +
				"-fps_mode cfr, and -hls_init_time fast-start.")
	}
}

// FeatureActive returns true when a named feature is supported by the
// service's detected ffmpeg version.  Used by code paths that want a
// single readable check instead of inlining `>= 5` comparisons.
func (svc *TranscodingService) FeatureActive(name string) bool {
	for _, f := range ffmpegFeatureTable {
		if f.Name == name {
			return svc.ffmpegMajorVersion >= f.MinMajor
		}
	}
	// Unknown feature names default to false rather than true, so a typo
	// in a caller doesn't accidentally enable an unsafe code path.
	return false
}
