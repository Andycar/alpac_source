package transcodesvc

import (
	"fmt"
	"lampac-go/internal/transcode"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// ABR multi-output ffmpeg argv builder — P3.K2 (Phase 2 of K).
//
// buildMultiRungArgs emits a single ffmpeg invocation that produces N HLS
// renditions in parallel, one per ABRRung.  Each rendition lives in its
// own v<N>/ subdir of OutputDir (init.mp4 + seg_*.m4s + index.m3u8); the
// audio track is encoded once and mapped into all variants.
//
// Phase 2 scope (this commit):
//   - SW encoding only (libx264).  HW backends route through
//     buildMultiRungArgsHW (added in P3.N).
//   - VOD only — live streams stay on the single-rung path because the
//     master playlist's sliding window semantics differ.
//   - Subtitle extract path is preserved (one VTT per stream, shared
//     across variants).  Burn-in is impossible with multi-rung so the
//     planner falls back to single-rung when the user's profile picked
//     burn-in.
//
// The argv structure mirrors createProcess for the input/seek/subtitle
// extraction phases — same flags, same probe-tuning, same -copyts logic.
// Divergence starts at the per-rung video map block.
// ---------------------------------------------------------------------------

// buildMultiRungArgs returns the full ffmpeg argv tail (after binary name
// is set by exec.Command).  It is invoked from createProcess when
// ctx.MultiRung is true.
func (svc *TranscodingService) buildMultiRungArgs(ctx transcodingContext) []string {
	tc := svc.cfg.Transcoding

	args := []string{}

	// stats line cadence (cheap log noise reduction during long encodes).
	if cmds, ok := tc.Command["demuxer"]; ok {
		for _, c := range cmds {
			if !strings.Contains(c, "stats_period") {
				args = append(args, strings.Fields(c)...)
			}
		}
		args = append(args, "-stats_period", "5")
	}

	// Read-rate throttle: same policy as single-rung.  Skip on pipe:0.
	if ctx.Live {
		args = append(args, "-re")
	} else if tc.Playlist.ReadRate > 0 && svc.ffmpegMajorVersion >= 5 && ctx.StdinPipe == nil {
		args = append(args, "-readrate", strconv.FormatFloat(tc.Playlist.ReadRate, 'f', -1, 64))
		if tc.Playlist.Burst > 0 && svc.ffmpegMajorVersion >= 7 {
			args = append(args, "-readrate_initial_burst", strconv.Itoa(tc.Playlist.Burst))
		}
	}

	// Source-type aware probe-tuning (same as single-rung).
	isTorrentStream := ctx.StdinPipe != nil ||
		(strings.Contains(ctx.Source, "/stream") && strings.Contains(ctx.Source, "link="))
	if isTorrentStream {
		if ctx.StdinPipe == nil {
			args = append(args, "-seekable", "0")
		}
		args = append(args,
			"-fflags", "+ignidx+genpts+discardcorrupt",
			"-probesize", "20000000",
			"-analyzeduration", "10000000",
		)
	} else if !ctx.BestEffort && !tc.DisableFastStart {
		args = append(args,
			"-fflags", "+nobuffer+genpts+discardcorrupt",
			"-probesize", "2000000",
			"-analyzeduration", "2000000",
		)
	}

	args = append(args, "-i", ctx.Source)

	// Output-seek for pipe inputs — same as single-rung.
	if ctx.HLS.Seek > 0 && ctx.StdinPipe != nil {
		args = append(args, "-ss", strconv.Itoa(ctx.HLS.Seek), "-noaccurate_seek")
	}

	// Seek-aware copyts + post-seek hardening (P3.I).
	if ctx.HLS.Seek > 0 {
		args = append(args, "-copyts", "-start_at_zero")
		if svc.ffmpegMajorVersion > 0 && svc.ffmpegMajorVersion < 5 {
			args = append(args, "-avoid_negative_ts", "make_zero")
		}
		// Post-seek CFR — multi-rung is always SW transcode in Phase 2.
		if svc.ffmpegMajorVersion >= 5 {
			args = append(args, "-fps_mode", "cfr")
		} else {
			args = append(args, "-vsync", "cfr")
		}
	}

	args = appendCommandArgs(args, tc.Command, "input")

	// Subtitle extract — shared across variants.  Burn-in is rejected
	// upstream so SubPlan.Strategy is either Extract or None here.
	if ctx.SubPlan.Strategy == StrategyExtract && !ctx.Live {
		if streams, ok := ctx.FFProbe["streams"].([]any); ok {
			if ctx.HLS.Seek == 0 {
				args = append(args, "-copyts")
			}
			for _, s := range streams {
				sm, _ := s.(map[string]any)
				if sm == nil {
					continue
				}
				if fmt.Sprint(sm["codec_type"]) != "subtitle" {
					continue
				}
				codecName := fmt.Sprint(sm["codec_name"])
				if !stringSliceContains(tc.Subtitle.Codec, codecName) {
					continue
				}
				subIdx := toInt(sm["index"])
				if subIdx == 0 {
					continue
				}
				for _, c := range tc.Subtitle.Command {
					for a := range strings.FieldsSeq(c) {
						args = append(args, strings.ReplaceAll(a, "{subIndex}", strconv.Itoa(subIdx)))
					}
				}
			}
		}
	}

	// ----- Per-rung video maps + scale filters + encoder specs -----
	//
	// Layout: each rung gets its own video stream specifier (v:0, v:1, …) and
	// its own audio output (a:0, a:1, …); var_stream_map pairs v:i,a:i.
	//
	// P3.N: when an HW backend that supports multi-session is active and
	// not opted out for this job, route per-rung encoding through it.
	// NVENC and VideoToolbox accept CPU frames natively so the scale
	// filter is identical to SW; only the -c:v / rate-control flags
	// differ.  Other HW backends (VAAPI/QSV) need backend-specific
	// filter graphs and stay on the SW path until added separately.
	audioIndex := max(ctx.Audio.Index, 0)
	for range ctx.Ladder {
		args = append(args, "-map", "0:v:0")
	}
	// One audio output per rung. ffmpeg 7+/Lavf62's HLS muxer rejects a
	// var_stream_map that references the same audio elementary stream from two
	// variants ("Same elementary stream found more than once in two different
	// variant definitions" → "Could not write header" → 0 segments → 502).
	// Map the audio once per variant so each variant owns a distinct output
	// stream (v:i,a:i). `-c:a` below applies to every audio output stream.
	for range ctx.Ladder {
		args = append(args, "-map", fmt.Sprintf("0:a:%d", audioIndex))
	}

	useHW := !ctx.DisableHW && svc.hwAccel != nil && svc.hwAccel.supportsMultiRung()
	gop := ctx.HLS.SegDur * 30 // ~30fps assumption; ffmpeg copies if source is lower
	for i, rung := range ctx.Ladder {
		if useHW {
			args = svc.hwAccel.appendMultiRungVideoArgs(args, i, rung, gop)
			continue
		}
		// SW per-rung — libx264 with the same rate-control envelope
		// as buildHWEncoderArgsForTonemappedInput so HW and SW
		// outputs match in target quality.
		filter := fmt.Sprintf("scale=%d:-2", rung.Width)
		args = append(args,
			fmt.Sprintf("-filter:v:%d", i), filter,
			fmt.Sprintf("-c:v:%d", i), "libx264",
			fmt.Sprintf("-preset:v:%d", i), "veryfast",
			fmt.Sprintf("-pix_fmt:v:%d", i), "yuv420p",
			fmt.Sprintf("-b:v:%d", i), strconv.Itoa(rung.BitrateKbps)+"k",
			fmt.Sprintf("-maxrate:v:%d", i), strconv.Itoa(int(float64(rung.BitrateKbps)*1.4))+"k",
			fmt.Sprintf("-bufsize:v:%d", i), strconv.Itoa(rung.BitrateKbps*2)+"k",
			fmt.Sprintf("-g:v:%d", i), strconv.Itoa(gop),
		)
	}

	// Audio: encoded once.  Honour the smart-mode hint for codec-copy
	// vs aac re-encode — mirrors appendAudioCodec's logic in compact form.
	if shouldCopyAudio(ctx) {
		args = append(args, "-c:a", "copy")
	} else {
		stereo := 2
		if !ctx.Audio.Stereo {
			stereo = 0 // 0 = passthrough channel count
		}
		args = append(args, "-c:a", "aac")
		if stereo > 0 {
			args = append(args, "-ac", strconv.Itoa(stereo))
		}
		if ctx.Audio.BitrateKbps > 0 {
			args = append(args, "-b:a", strconv.Itoa(ctx.Audio.BitrateKbps)+"k")
		} else {
			args = append(args, "-b:a", "128k")
		}
	}

	// Var stream map: each variant gets its own video AND audio output stream
	// (v:i,a:i). Sharing a single a:0 across variants is rejected by ffmpeg 7+.
	mapParts := make([]string, 0, len(ctx.Ladder))
	for i := range ctx.Ladder {
		mapParts = append(mapParts, fmt.Sprintf("v:%d,a:%d", i, i))
	}
	args = append(args, "-var_stream_map", strings.Join(mapParts, " "))

	// Drop subtitle output streams from the muxer; sub VTTs are emitted
	// separately by the extract loop above.
	args = append(args, "-sn")

	// HLS muxer config.
	args = append(args, "-f", "hls")
	if cmds, ok := tc.HLS.Command["output"]; ok {
		for _, c := range cmds {
			args = append(args, strings.Fields(c)...)
		}
	}
	args = append(args, "-hls_segment_type")
	if ctx.HLS.FMP4 {
		args = append(args, "fmp4")
	} else {
		args = append(args, "mpegts")
		if cmds, ok := tc.HLS.Command["segment_mpegts"]; ok {
			for _, c := range cmds {
				args = append(args, strings.Fields(c)...)
			}
		}
	}

	args = append(args, "-hls_time", strconv.Itoa(ctx.HLS.SegDur))
	if !ctx.Live && !tc.DisableFastStart && svc.ffmpegMajorVersion >= 5 && ctx.HLS.SegDur > 1 {
		args = append(args, "-hls_init_time", "1")
	}
	if ctx.Live {
		args = append(args, "-hls_flags", "append_list+omit_endlist+delete_segments")
	}
	args = append(args, "-hls_list_size", strconv.Itoa(ctx.HLS.WinSize))

	// Start number — same logic as single-rung.
	startNum := ctx.StartNum
	if startNum == nil && ctx.HLS.Seek > 0 {
		segDur := max(ctx.HLS.SegDur, 1)
		sn := ctx.HLS.Seek / segDur
		startNum = &sn
	}
	if startNum != nil {
		args = append(args, "-start_number", strconv.Itoa(*startNum))
	}

	// Master playlist generated by ffmpeg goes to a different name so our
	// transcodingMasterHandler stays authoritative (it's the one that
	// injects the SUBTITLES rendition group).
	args = append(args, "-master_pl_name", "ffmpeg_master.m3u8")

	if ctx.HLS.FMP4 {
		args = append(args, "-hls_fmp4_init_filename", "init.mp4")
	} else if !ctx.Live {
		args = append(args, "-hls_playlist_type", "vod")
	}

	// Per-variant segment + playlist paths.  ffmpeg substitutes %v with
	// the rung index (0,1,2,...).
	if ctx.HLS.FMP4 {
		args = append(args, "-hls_segment_filename", "v%v/seg_%05d.m4s")
	} else {
		args = append(args, "-hls_segment_filename", "v%v/seg_%05d.ts")
	}
	args = append(args, "-y", "v%v/index.m3u8")

	return args
}

// ensureMultiRungDirs creates the v<N>/ subdirs that ffmpeg expects to
// exist before emitting segments.  ffmpeg's HLS muxer doesn't auto-mkdir
// subdirs in the segment_filename pattern; without these calls the run
// fails with "No such file or directory" on the first segment.
func ensureMultiRungDirs(outputDir string, ladder []transcode.ABRRung) error {
	for i := range ladder {
		dir := filepath.Join(outputDir, fmt.Sprintf("v%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("multi-rung mkdir %s: %w", dir, err)
		}
	}
	return nil
}

// shouldMultiRung decides whether a job is eligible for ABR Phase 2.
// Returns the chosen ladder when eligible, nil when not.
//
// Eligibility:
//
//   - probe succeeded (we need real dimensions and codec info)
//   - subtitle plan is NOT burn-in (the filter graph owns the video output;
//     can't fan it across multiple variants)
//   - mode is sw-transcode, or hw-transcode on a backend that supports
//     concurrent sessions (NVENC, VideoToolbox — see HWAccelInfo.supportsMultiRung)
//   - the source is large enough to benefit (planABRLadder returns >1 rung)
//   - not a live job (master playlist semantics differ for live streams)
//   - ABR is not explicitly disabled for this job
func (svc *TranscodingService) shouldMultiRung(ctx transcodingContext) []transcode.ABRRung {
	// P3.O: operator can disable ABR ladder globally.
	if svc.cfg.Transcoding.DisableABRLadder {
		return nil
	}
	// P3.R: ffmpeg < 5 falls into the multi-rung "fallback" bucket per
	// the compatibility table.  Operators who tested var_stream_map on
	// their 4.x build can flip ForceABROnLegacyFFmpeg to opt back in.
	if !svc.FeatureActive("abr_multirung") && !svc.cfg.Transcoding.ForceABROnLegacyFFmpeg {
		return nil
	}
	if ctx.BestEffort {
		return nil
	}
	if ctx.Live {
		return nil
	}
	if ctx.SubPlan.Strategy == StrategyBurnIn {
		return nil
	}
	switch ctx.Mode {
	case transcode.ModeSWTranscode:
		// Always eligible.
	case transcode.ModeHWTranscode:
		// Only when the backend can run several encode sessions cheaply
		// AND HW isn't disabled for this job.
		if ctx.DisableHW || svc.hwAccel == nil || !svc.hwAccel.supportsMultiRung() {
			return nil
		}
	default:
		return nil
	}
	w, h := extractVideoDimensions(ctx.FFProbe)
	ladder := transcode.PlanABRLadder(w, h, ctx.Mode)
	if len(ladder) <= 1 {
		return nil
	}
	// P3.O: cap ladder length when operator wants to limit concurrent
	// encode sessions (e.g. NVENC consumer cards with 2 simultaneous
	// session licenses).
	if cap := svc.cfg.Transcoding.MaxLadderRungs; cap > 0 && len(ladder) > cap {
		ladder = ladder[:cap]
	}
	return ladder
}

// shouldCopyAudio checks whether the chosen smart mode copies audio
// (Remux/Native/Direct) or re-encodes it (AudioOnly/SW/HW transcode).
// Multi-rung emits one audio stream — we mirror the single-rung decision.
func shouldCopyAudio(ctx transcodingContext) bool {
	switch ctx.Mode {
	case transcode.ModeRemux, transcode.ModeNative, transcode.ModeDirect:
		return true
	}
	// audio-only / sw-transcode / hw-transcode → re-encode to AAC for HLS.
	return false
}
