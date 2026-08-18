package transcodesvc

import (
	"fmt"
	"lampac-go/internal/transcode"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Master playlist + sub-rendition groups — P3.J.
//
// The /transcoding/{id}/main.m3u8 endpoint serves a *media* playlist (a list
// of seg_*.m4s/.ts entries).  HLS clients that want adaptive sub tracks or
// alternate audio renditions need a *master* playlist that declares those
// groups via #EXT-X-MEDIA before pointing at the media playlist via
// #EXT-X-STREAM-INF.
//
// Until now the only exposed URL was the media playlist, so even when the
// SubPlan extracted WebVTT siblings the player UI never showed a track
// switcher — Lampa, hls.js, native Tizen all silently ignored the .vtt
// files because nothing in HLS told them they existed.
//
// This module fills the gap by generating the master playlist on demand
// from job context.  It does NOT replace main.m3u8 — both are served, and
// the start response surfaces masterUrl alongside playlistUrl so the
// plugin can decide which to feed the player.
//
// Out of scope (handled in P3.K when it lands): multi-rendition video
// ladders.  This file only emits a single STREAM-INF.
// ---------------------------------------------------------------------------

// buildSubtitleRenditionPlaylist returns the contents of an HLS sub-only
// playlist that wraps a single WebVTT file as one giant segment.
//
// For VOD this is enough — the player loads the whole VTT once at session
// start and uses it for the entire stream.  We trade fine-grained sub
// seeking for a one-line implementation that works in every player.
//
// `vttFilename` is the file name relative to the streamId base URL (e.g.
// `subs_3.vtt`).  `durationSec` should match the source duration so the
// EXTINF line is plausible; if unknown, pass 0 and we'll use 9999.
func buildSubtitleRenditionPlaylist(vttFilename string, durationSec float64) string {
	if durationSec <= 0 {
		durationSec = 9999
	}
	target := int(durationSec) + 1
	return fmt.Sprintf(
		"#EXTM3U\n"+
			"#EXT-X-VERSION:6\n"+
			"#EXT-X-TARGETDURATION:%d\n"+
			"#EXT-X-PLAYLIST-TYPE:VOD\n"+
			"#EXTINF:%.3f,\n"+
			"%s\n"+
			"#EXT-X-ENDLIST\n",
		target, durationSec, vttFilename,
	)
}

// buildMasterPlaylist generates a master playlist for a single VOD session.
// It references the existing main.m3u8 as the only video rendition and
// declares each WebVTT-extracted subtitle stream as a TYPE=SUBTITLES
// rendition group.
//
// `prefBaseURL` is the URL prefix shared with the segment serving
// endpoint, e.g. `https://example.com/transcoding/abcd1234`.  All inner
// URIs are emitted relative to that prefix so the master playlist stays
// portable across hosts / proxies.
func buildMasterPlaylist(job *TranscodingJob, baseURL string) string {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:6\n")
	sb.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")

	// Subtitle renditions — one #EXT-X-MEDIA per extracted WebVTT track.
	// Only emit when the planner chose StrategyExtract; burn-in and None
	// don't produce VTT siblings.
	subsGroup := ""
	if job.Context.SubPlan.Strategy == StrategyExtract && len(job.Context.SubPlan.Streams) > 0 {
		subsGroup = "subs"
		preferred := job.Context.Audio.Index // proxy for "user's language"
		_ = preferred                        // currently unused — we mark first as default
		first := true
		for _, s := range job.Context.SubPlan.Streams {
			// Only the codecs that actually got muxed to .vtt earn an entry.
			if !isPlainTextSubtitle(s.Codec) && !isAdvancedTextSubtitle(s.Codec) {
				continue
			}
			lang := strings.ToLower(strings.TrimSpace(s.Lang))
			if lang == "" {
				lang = "und"
			}
			name := s.Title
			if name == "" {
				name = lang
			}
			defaultFlag := "NO"
			if first {
				defaultFlag = "YES"
				first = false
			}
			fmt.Fprintf(&sb,
				`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="%s",LANGUAGE="%s",DEFAULT=%s,AUTOSELECT=YES,FORCED=NO,URI="subs_%d.m3u8"`+"\n",
				escapeM3U8Attr(name), lang, defaultFlag, s.AbsIndex,
			)
		}
	}

	// Multi-audio renditions (hls4) — one EXT-X-MEDIA AUDIO per shelvable
	// track. The audio muxed into the video variant is ALWAYS the active track,
	// so the group carries exactly one URI-less DEFAULT member (HLS "audio is
	// in the variant") plus a lazy AAC rendition for every OTHER shelvable
	// track, letting the player hot-switch without a session restart.
	//
	// The muxed member is emitted from whichever ShelfTrack matches the active
	// abs index (the common case). When the active track isn't shelvable — a
	// lossless default (TrueHD/FLAC/PCM) re-encoded to AAC in the variant — no
	// shelf track matches, so we synthesize the muxed member from the probe;
	// otherwise the group would have no DEFAULT and players would pick the
	// wrong track (or none).
	audioGroup := ""
	if len(job.Context.ShelfTracks) >= 2 {
		audioGroup = "aud"
		muxedEmitted := false
		for _, t := range job.Context.ShelfTracks {
			if t.AbsIndex == job.Context.ActiveAudioAbs {
				emitMuxedAudioMedia(&sb, t.displayName(), t.Lang)
				muxedEmitted = true
				continue
			}
			emitRenditionAudioMedia(&sb, t.displayName(), t.Lang, t.AbsIndex)
		}
		if !muxedEmitted {
			name, lang := activeMuxedAudioMeta(job.Context.FFProbe, job.Context.Audio.Index)
			emitMuxedAudioMedia(&sb, name, lang)
		}
	}

	// P3.K: video renditions — one STREAM-INF per ABR rung.
	//
	// Two emission modes:
	//   - Multi-rung active (Phase 2): the job actually produces v0/, v1/
	//     subdirs via var_stream_map ffmpeg.  Each rung gets its own URI,
	//     and the player switches between them based on real bandwidth.
	//   - Single-rung (Phase 1 fallback): the planner suggested rungs but
	//     ffmpeg only writes the primary.  All STREAM-INF lines point at
	//     main.m3u8; clients see codec/bandwidth metadata but can't actually
	//     adapt.  Still useful: lets the player pick "the right" stream
	//     once and report the correct bandwidth in stats.
	multiRung := job.Context.MultiRung && len(job.Context.Ladder) > 1
	var ladder []transcode.ABRRung
	if multiRung {
		ladder = job.Context.Ladder
	} else {
		w, h := extractVideoDimensions(job.Context.FFProbe)
		ladder = transcode.PlanABRLadder(w, h, job.Mode)
	}
	codecs := estimateCodecs(job.Context.FFProbe, job.Mode)
	// VIDEO-RANGE (HDR signalling — required so TVs engage HDR on a stream-copied
	// PQ/HLG source) and FRAME-RATE, ported from Jellyfin's DynamicHlsHelper.
	videoRange := videoRangeFromProbe(job.Context.FFProbe, job.Mode)
	frameRate := frameRateFromProbe(job.Context.FFProbe)
	// SUPPLEMENTAL-CODECS advertises a stream-copied Dolby Vision layer so
	// DoVi-capable players engage it while others fall back to the base CODECS.
	supplementalCodecs := hlsSupplementalCodecs(job.Context.FFProbe, job.Mode)

	for i, rung := range ladder {
		// Bandwidth: per-rung target bitrate (kbps → bps), padded 10%
		// to mirror estimateBandwidth's headroom.  Audio overhead is a
		// flat ~128 kbps until we know the actual audio bitrate.
		bwBps := rung.BitrateKbps*1000 + 128_000
		bwBps += bwBps / 10

		// AVERAGE-BANDWIDTH mirrors BANDWIDTH (Apple/Safari prefer it present).
		streamLine := fmt.Sprintf(`#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d`, bwBps, bwBps)
		if videoRange != "" {
			streamLine += `,VIDEO-RANGE=` + videoRange
		}
		if codecs != "" {
			streamLine += fmt.Sprintf(`,CODECS="%s"`, codecs)
		}
		if supplementalCodecs != "" {
			streamLine += fmt.Sprintf(`,SUPPLEMENTAL-CODECS="%s"`, supplementalCodecs)
		}
		streamLine += fmt.Sprintf(`,RESOLUTION=%dx%d`, rung.Width, rung.Height)
		if frameRate != "" {
			streamLine += `,FRAME-RATE=` + frameRate
		}
		if audioGroup != "" {
			streamLine += fmt.Sprintf(`,AUDIO="%s"`, audioGroup)
		}
		if subsGroup != "" {
			streamLine += fmt.Sprintf(`,SUBTITLES="%s"`, subsGroup)
		}
		streamLine += `,NAME="` + rung.Label + `"`
		streamLine += "\n"
		sb.WriteString(streamLine)

		if multiRung {
			fmt.Fprintf(&sb, "v%d/index.m3u8\n", i)
		} else {
			sb.WriteString("main.m3u8\n")
		}
	}

	_ = baseURL
	return sb.String()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// escapeM3U8Attr replaces double quotes with single quotes so the value is
// safe to embed in `NAME="…"` HLS tags.  HLS doesn't support escaped
// quotes inside attributes, so substitution is the only safe option.
func escapeM3U8Attr(s string) string {
	return strings.ReplaceAll(s, `"`, `'`)
}

// normalizeMediaLang lowercases + trims a language tag, defaulting to "und"
// (ISO 639-2 "undetermined") when empty — HLS wants a syntactically valid
// LANGUAGE value even when the source omitted the tag.
func normalizeMediaLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == "" {
		return "und"
	}
	return l
}

// emitMuxedAudioMedia writes the URI-less DEFAULT member of the hls4 AUDIO
// group — the track already muxed into the video variant.
func emitMuxedAudioMedia(sb *strings.Builder, name, lang string) {
	fmt.Fprintf(sb,
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="%s",LANGUAGE="%s",DEFAULT=YES,AUTOSELECT=YES`+"\n",
		escapeM3U8Attr(name), normalizeMediaLang(lang),
	)
}

// emitRenditionAudioMedia writes a lazy AAC rendition member of the hls4 AUDIO
// group — a non-active track the player fetches only if the viewer switches.
func emitRenditionAudioMedia(sb *strings.Builder, name, lang string, absIndex int) {
	fmt.Fprintf(sb,
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="%s",LANGUAGE="%s",DEFAULT=NO,AUTOSELECT=NO,URI="aud_%d/index.m3u8"`+"\n",
		escapeM3U8Attr(name), normalizeMediaLang(lang), absIndex,
	)
}

// activeMuxedAudioMeta resolves the display name + language of the audio track
// muxed into the video variant, identified by its RELATIVE audio index (the
// same semantics as ctx.Audio.Index). Used to label the URI-less member when
// the active track isn't shelvable and so is absent from ShelfTracks.
func activeMuxedAudioMeta(probe map[string]any, relIndex int) (name, lang string) {
	if relIndex < 0 {
		relIndex = 0
	}
	streams, _ := probe["streams"].([]any)
	rel := 0
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil || fmt.Sprint(sm["codec_type"]) != "audio" {
			continue
		}
		if rel == relIndex {
			if tags, ok := sm["tags"].(map[string]any); ok {
				if ti, ok := tags["title"].(string); ok {
					name = strings.TrimSpace(ti)
				}
				if l, ok := tags["language"].(string); ok {
					lang = strings.TrimSpace(l)
				}
			}
			if name == "" {
				if lang != "" {
					name = lang
				} else {
					name = fmt.Sprintf("Audio %d", rel+1)
				}
			}
			return name, lang
		}
		rel++
	}
	return "Audio", "und"
}

// estimateBandwidth picks a plausible BANDWIDTH value for the master's
// STREAM-INF entry.  Falls back to a 5 Mbps best-effort default when the
// probe doesn't carry bit_rate metadata.
func estimateBandwidth(probe map[string]any) int {
	if probe == nil {
		return 5_000_000
	}
	if format, ok := probe["format"].(map[string]any); ok {
		if br, ok := format["bit_rate"].(string); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(br)); err == nil && n > 0 {
				// Pad 10% to cover bursty CDN delivery — the BANDWIDTH
				// in HLS is intended to be the *peak* sustained rate.
				return n + n/10
			}
		}
	}
	// Fall back to per-stream bit_rate if format-level missing.
	if streams, ok := probe["streams"].([]any); ok {
		total := 0
		for _, s := range streams {
			sm, _ := s.(map[string]any)
			if sm == nil {
				continue
			}
			if br, ok := sm["bit_rate"].(string); ok {
				if n, err := strconv.Atoi(strings.TrimSpace(br)); err == nil && n > 0 {
					total += n
				}
			}
		}
		if total > 0 {
			return total + total/10
		}
	}
	return 5_000_000
}

// estimateResolution returns "WxH" string from the first video stream, or
// empty when probe is unavailable.
func estimateResolution(probe map[string]any) string {
	w, h := extractVideoDimensions(probe)
	if w == 1920 && h == 1080 && probe == nil {
		// extractVideoDimensions returns default 1920x1080 on nil probe;
		// we don't want to lie in the master playlist about resolution
		// when we don't know it.  Drop the field instead.
		return ""
	}
	if w <= 0 || h <= 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", w, h)
}

// estimateCodecs returns an RFC 6381 codec string suitable for STREAM-INF.
// Best-effort — when probe lacks data or the chosen mode forces re-encode
// to libx264 baseline, we emit a conservative `avc1.4d401f,mp4a.40.2`.
//
// Mode-aware: when smart-mode picked SW/HW transcode the output is
// always h264 + aac per our pipeline, so we can hardcode that.  For
// remux/audio-only we pick from the source codec.
func estimateCodecs(probe map[string]any, mode transcode.TranscodingMode) string {
	switch mode {
	case transcode.ModeSWTranscode, transcode.ModeHWTranscode:
		// We always re-encode to h264 high-ish + aac in our pipeline.
		return "avc1.640029,mp4a.40.2"
	}

	// Stream-copy path — derive from probe.
	vc := ""
	ac := ""
	if probe != nil {
		if streams, ok := probe["streams"].([]any); ok {
			for _, s := range streams {
				sm, _ := s.(map[string]any)
				if sm == nil {
					continue
				}
				switch fmt.Sprint(sm["codec_type"]) {
				case "video":
					if vc == "" {
						vc = videoCodecRFC6381(fmt.Sprint(sm["codec_name"]), fmt.Sprint(sm["profile"]), probeIntField(sm, "level"))
					}
				case "audio":
					if ac == "" {
						ac = audioCodecRFC6381(fmt.Sprint(sm["codec_name"]), fmt.Sprint(sm["profile"]))
					}
				}
			}
		}
	}
	parts := []string{}
	if vc != "" {
		parts = append(parts, vc)
	}
	if ac != "" {
		parts = append(parts, ac)
	}
	if len(parts) == 0 {
		return "avc1.4d401f,mp4a.40.2"
	}
	return strings.Join(parts, ",")
}

// Video/audio RFC 6381 descriptors are built by videoCodecRFC6381 /
// audioCodecRFC6381 in transcoding_hls_codec.go (ported from Jellyfin), which
// derive the exact string from the ffprobe profile + level.
