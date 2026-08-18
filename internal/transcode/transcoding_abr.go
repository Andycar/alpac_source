package transcode

// ---------------------------------------------------------------------------
// ABR ladder planner — P3.K (Phase 1: pure planner).
//
// "Ставлю качество 720p вручную" — the народный фикс repeated across every
// 4PDA / Telegram thread when a 1080p stream chokes mid-playback.  The
// long-term answer is to deliver multiple bitrates simultaneously and let
// the player switch transparently as bandwidth changes (HLS adaptive bit
// rate).  This file is the *planner* part: a pure function that, given a
// source resolution and the chosen pipeline mode, returns the list of
// renditions that should be emitted.
//
// Phase 1: planner + master.m3u8 awareness — this file. A pure function
// that, given a source resolution and pipeline mode, returns the rungs.
//
// Phase 2: SHIPPED in transcoding_multirung.go (buildMultiRungArgs, P3.K2/N).
// createProcess routes through it when shouldMultiRung() returns a ladder and
// the ffmpeg build supports it (FeatureActive("abr_multirung"), see
// transcoding_compat.go — degrades to single-rung on legacy ffmpeg). It emits
// one ffmpeg invocation with -var_stream_map / -master_pl_name / per-variant
// v%v/ outputs; the planner below is unchanged, only the argv builder differs.
//
// Why a separate planner: keeping it pure makes it trivial to unit-test
// across resolutions / modes and keeps the ladder policy in one place
// (instead of scattered if-tree inside createProcess).
// ---------------------------------------------------------------------------

// ABRRung describes one rendition in the ladder.  The first entry is the
// "primary" — the one the existing single-output pipeline emits today.
// Subsequent entries are downscales that Phase 2 will materialise.
type ABRRung struct {
	// Label is a stable identifier used in segment paths and master
	// playlist NAME attributes ("1080p", "720p", "480p").
	Label string

	// Width/Height are the target dimensions.  When the source is wider
	// than the target the encoder downscales; equal-or-narrower sources
	// get the original dimensions (no upscale).
	Width  int
	Height int

	// BitrateKbps is the target encode bitrate.  Comes from
	// pickVideoBitrate so it stays consistent with the HW encoder block.
	BitrateKbps int

	// Primary is true for the rung that matches the chosen smart-mode
	// pipeline — i.e. what the single-rendition path produces today.
	// Master playlist marks this as DEFAULT.
	Primary bool
}

// planABRLadder picks rungs for the given source dimensions and pipeline
// mode.  Returns at least one rung — the primary, matching what we'd emit
// without ABR.
//
// Policy:
//
//   - Modes that stream-copy video (Native/Direct/Remux/AudioOnly): we
//     can't downscale a copied stream; the ladder is single-rung.  ABR
//     here would require a second encode pipeline running alongside the
//     copy, which doubles CPU for marginal gain — defer to Phase 2 to
//     decide.  For now, single rung.
//
//   - SW transcode: the encoder is already running.  Adding a 720p rung
//     when source is ≥1080p costs ~30% extra CPU; adding a 480p rung
//     adds another ~15%.  The user-facing benefit (player adapts to
//     bandwidth without reload) easily justifies it on any modern VPS.
//
//   - HW transcode: HW encoders (NVENC ~8 sessions, VAAPI unbounded,
//     VideoToolbox ~3) handle multiple parallel encodes very cheaply.
//     Same ladder as SW, but the cost factor is closer to 1.05x.
//
// 4K sources never include a "4K copy" rung — almost no client TV plays
// 4K reliably from HLS, and the bitrate alone would saturate any
// residential connection.  4K input → ladder caps at 1080p downscale.
func PlanABRLadder(width, height int, mode TranscodingMode) []ABRRung {
	// Fall through to safe single-rung for stream-copy modes.
	switch mode {
	case ModeNative, ModeDirect, ModeRemux, ModeAudioOnly:
		return []ABRRung{primaryRung(width, height)}
	}

	// 4K and above: cap the primary at 1080p downscale, add 720p.
	if height >= 2160 || width >= 3840 {
		return []ABRRung{
			{Label: "1080p", Width: 1920, Height: 1080, BitrateKbps: PickVideoBitrate(1920, 1080), Primary: true},
			{Label: "720p", Width: 1280, Height: 720, BitrateKbps: PickVideoBitrate(1280, 720)},
		}
	}

	// 1440p (QHD): downscale to 1080p as primary, add 720p.
	if height >= 1440 || width >= 2560 {
		return []ABRRung{
			{Label: "1080p", Width: 1920, Height: 1080, BitrateKbps: PickVideoBitrate(1920, 1080), Primary: true},
			{Label: "720p", Width: 1280, Height: 720, BitrateKbps: PickVideoBitrate(1280, 720)},
		}
	}

	// 1080p source: 1080p primary + 720p + 480p.
	if height >= 1080 || width >= 1920 {
		return []ABRRung{
			{Label: "1080p", Width: 1920, Height: 1080, BitrateKbps: PickVideoBitrate(1920, 1080), Primary: true},
			{Label: "720p", Width: 1280, Height: 720, BitrateKbps: PickVideoBitrate(1280, 720)},
			{Label: "480p", Width: 854, Height: 480, BitrateKbps: PickVideoBitrate(854, 480)},
		}
	}

	// 720p source: 720p primary + 480p.
	if height >= 720 || width >= 1280 {
		return []ABRRung{
			{Label: "720p", Width: 1280, Height: 720, BitrateKbps: PickVideoBitrate(1280, 720), Primary: true},
			{Label: "480p", Width: 854, Height: 480, BitrateKbps: PickVideoBitrate(854, 480)},
		}
	}

	// Anything smaller (480p / SD) — no downscale ladder, the bandwidth
	// benefit doesn't justify a second encode.
	return []ABRRung{primaryRung(width, height)}
}

// primaryRung builds a single rung labelled by the source resolution.
// Used for stream-copy modes where the ladder is just the source itself.
func primaryRung(width, height int) ABRRung {
	label := "source"
	switch {
	case height >= 2160:
		label = "4k"
	case height >= 1080:
		label = "1080p"
	case height >= 720:
		label = "720p"
	case height >= 480:
		label = "480p"
	}
	if width == 0 || height == 0 {
		// Fallback: assume 1080p when probe didn't yield dimensions.
		width, height = 1920, 1080
		label = "1080p"
	}
	return ABRRung{
		Label:       label,
		Width:       width,
		Height:      height,
		BitrateKbps: PickVideoBitrate(width, height),
		Primary:     true,
	}
}
