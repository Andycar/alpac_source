package transcodesvc

import (
	"fmt"
	"lampac-go/internal/transcode"
	"strings"
)

// ---------------------------------------------------------------------------
// Subtitle strategy planner — P3.C.
//
// Two unsolved subtitle problems show up across yumata/lampa #85, #212 and
// every long-running 4PDA / Telegram thread:
//
//   1. PGS / DVD / DVB bitmap subtitles are silently dropped.  The current
//      pipeline only whitelists text codecs in tc.Subtitle.Codec, so the
//      whole bitmap family disappears with no error to the player.
//
//   2. ASS / SSA on Tizen and old WebOS lose formatting (positioning,
//      font, color, karaoke).  Our WebVTT muxer keeps the dialogue text
//      but strips advanced styling, then the TV renders the result with
//      its own minimal vtt engine — visually broken even when "working".
//
// planSubtitles() looks at the probed streams + the device profile and
// picks one of three strategies per session:
//
//   - StrategyExtract  — text codec, client renders WebVTT properly
//                         (Apple TV, Lampa native, modern browsers).
//                         Existing pipeline owns this; we just whitelist.
//   - StrategyBurnIn   — bitmap subs OR ASS on a styling-broken client.
//                         The chosen subtitle stream is rendered into
//                         the video frame via ffmpeg's `subtitles=` /
//                         `overlay` filter.  Forces video re-encode.
//   - StrategyNone     — subtitles=false, no usable streams, or burn-in
//                         impossible (pipe:0 source — we don't re-read).
//
// Burn-in is heavyweight (forces video re-encode) but it's the *only*
// reliable way to deliver PGS or correctly-styled ASS to clients that
// can't render them — exactly the year-old GitHub issues we're closing.
// ---------------------------------------------------------------------------

// SubtitleStrategy describes how a single subtitle stream is handled.
type SubtitleStrategy string

const (
	StrategyNone    SubtitleStrategy = "none"
	StrategyExtract SubtitleStrategy = "extract"
	StrategyBurnIn  SubtitleStrategy = "burn-in"
)

// SubtitleStream is a probe-derived view of one subtitle track.
type SubtitleStream struct {
	// AbsIndex is the absolute ffmpeg stream index (0:N notation).
	AbsIndex int
	// RelIndex is the 0-based subtitle-only index — what the
	// `subtitles=...:si=N` and `[0:s:N]` filters expect.
	RelIndex int
	Codec    string
	Lang     string
	Title    string
	Default  bool
	Forced   bool
}

// SubtitlePlan is the per-session subtitle decision.
type SubtitlePlan struct {
	Strategy SubtitleStrategy

	// Streams is the full subtitle list (for the audio-track-style menu
	// the plugin renders).  Populated for both Extract and BurnIn so the
	// admin / diagnostics panel sees what was available.
	Streams []SubtitleStream

	// BurnIn is set only when Strategy == StrategyBurnIn.  It identifies
	// the single stream to render into the video frame.
	BurnIn *SubtitleStream

	// Reason is a one-line human-readable explanation surfaced in /stats
	// and the start-response, e.g.
	//   "burn-in PGS (no client renders bitmap subs)"
	//   "burn-in ASS (tizen-4 strips ASS styling)"
	//   "extract subrip → WebVTT"
	Reason string
}

// ---------------------------------------------------------------------------
// Codec classification
// ---------------------------------------------------------------------------

// isBitmapSubtitleCodec returns true for picture-based subtitle codecs that
// can't be converted to WebVTT and must be either OCR-ed or rendered into
// the video frame (burn-in).
func isBitmapSubtitleCodec(codec string) bool {
	switch strings.ToLower(codec) {
	case "hdmv_pgs_subtitle", "pgs", "pgssub",
		"dvd_subtitle", "dvdsub", "vobsub", // VobSub (S_VOBSUB / .idx+.sub) — ffprobe usually says dvd_subtitle, some muxers/containers report vobsub
		"dvb_subtitle", "dvbsub",
		"dvb_teletext", "xsub":
		return true
	}
	return false
}

// isAdvancedTextSubtitle returns true for SSA/ASS — text subs whose styling
// is lost when converted to WebVTT.  Profiles known to butcher VTT styling
// will trigger burn-in for these.
func isAdvancedTextSubtitle(codec string) bool {
	switch strings.ToLower(codec) {
	case "ass", "ssa":
		return true
	}
	return false
}

// isPlainTextSubtitle returns true for codecs that survive the ffmpeg WebVTT
// muxer cleanly (subtitle text + minimal cue timing).
func isPlainTextSubtitle(codec string) bool {
	switch strings.ToLower(codec) {
	case "subrip", "srt", "webvtt", "vtt", "mov_text", "tx3g", "ttml", "sami", "smi":
		return true
	}
	return false
}

// profileNeedsAssBurnIn returns true for device families known to render
// ASS/SSA → WebVTT badly (no styling, broken positioning).  The mapping
// comes from yumata/lampa #212 (Samsung Q9FNA → no formatting), 4PDA
// reports across Tizen 3-5, and webos-forums coverage of WebOS 3-4.
func profileNeedsAssBurnIn(profileLabel string) bool {
	switch {
	case strings.HasPrefix(profileLabel, "tizen-legacy"),
		strings.HasPrefix(profileLabel, "tizen-3"),
		strings.HasPrefix(profileLabel, "tizen-4"),
		strings.HasPrefix(profileLabel, "tizen-5"),
		strings.HasPrefix(profileLabel, "webos-legacy"),
		profileLabel == "unknown":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Stream extraction
// ---------------------------------------------------------------------------

// extractSubtitleStreams returns a SubtitleStream slice from raw ffprobe
// output, preserving stream order.  Both abs and rel index are tracked so
// callers can build either "-map 0:N" or "subtitles=...:si=N" arguments.
func extractSubtitleStreams(probe map[string]any) []SubtitleStream {
	if probe == nil {
		return nil
	}
	streams, _ := probe["streams"].([]any)
	if len(streams) == 0 {
		return nil
	}

	var out []SubtitleStream
	rel := 0
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil || fmt.Sprint(sm["codec_type"]) != "subtitle" {
			continue
		}
		st := SubtitleStream{
			AbsIndex: toIntOrZero(sm["index"]),
			RelIndex: rel,
			Codec:    strings.ToLower(fmt.Sprint(sm["codec_name"])),
		}
		if tags, ok := sm["tags"].(map[string]any); ok {
			if l, ok := tags["language"].(string); ok {
				st.Lang = strings.ToLower(l)
			}
			if t, ok := tags["title"].(string); ok {
				st.Title = t
			}
		}
		if disp, ok := sm["disposition"].(map[string]any); ok {
			if d, ok := disp["default"].(float64); ok && d > 0 {
				st.Default = true
			}
			if f, ok := disp["forced"].(float64); ok && f > 0 {
				st.Forced = true
			}
		}
		out = append(out, st)
		rel++
	}
	return out
}

// pickPreferredSubtitle returns the index into `streams` that best matches
// the requested language, falling back to the first default-disposition
// stream, then the first stream overall.  Returns -1 when streams is empty.
func pickPreferredSubtitle(streams []SubtitleStream, lang string) int {
	if len(streams) == 0 {
		return -1
	}

	// 1. Exact language match (with ISO 639-1 → 639-2/3 normalisation).
	wanted := transcode.NormalizeLangCodes(lang)
	if len(wanted) > 0 {
		for i, s := range streams {
			if s.Lang == "" {
				continue
			}
			for _, w := range wanted {
				if s.Lang == w {
					return i
				}
			}
		}
	}

	// 2. Default-disposition stream.
	for i, s := range streams {
		if s.Default {
			return i
		}
	}

	// 3. Fall through.
	return 0
}

// ---------------------------------------------------------------------------
// Planner
// ---------------------------------------------------------------------------

// planSubtitles is the entry point used by Start().  It returns a SubtitlePlan
// that the ffmpeg argv builder consumes.  The function is pure (no side
// effects, no I/O) so it can be tested with synthetic probe data.
//
// Arguments:
//   - probe         : ffprobe JSON (may be nil → returns StrategyNone)
//   - lang          : preferred language (caps.Lang)
//   - profileLabel  : UA-DB device label ("tizen-4", …) — drives ASS burn-in
//   - requested     : whether the request asked for subtitles at all
//   - sourceIsPipe  : true when ffmpeg input is pipe:0 — burn-in unsupported
//     because libavformat can't re-read a stdin stream for
//     the subtitles filter.
func planSubtitles(probe map[string]any, lang, profileLabel string, requested, sourceIsPipe bool) SubtitlePlan {
	if !requested {
		return SubtitlePlan{Strategy: StrategyNone, Reason: "subtitles disabled by request"}
	}

	streams := extractSubtitleStreams(probe)
	if len(streams) == 0 {
		return SubtitlePlan{Strategy: StrategyNone, Reason: "no subtitle streams in source"}
	}

	picked := pickPreferredSubtitle(streams, lang)
	if picked < 0 {
		return SubtitlePlan{Strategy: StrategyNone, Streams: streams, Reason: "no suitable subtitle pick"}
	}
	chosen := streams[picked]

	// Bitmap subs — burn-in is the only viable path.
	if isBitmapSubtitleCodec(chosen.Codec) {
		if sourceIsPipe {
			// We can't re-read pipe:0 for the subtitles filter; degrade
			// gracefully — better to show no subs than to fail the session.
			return SubtitlePlan{
				Strategy: StrategyNone,
				Streams:  streams,
				Reason:   fmt.Sprintf("bitmap %s subs require burn-in but source is pipe:0 — skipping", chosen.Codec),
			}
		}
		picked := chosen
		return SubtitlePlan{
			Strategy: StrategyBurnIn,
			Streams:  streams,
			BurnIn:   &picked,
			Reason:   fmt.Sprintf("burn-in %s (bitmap subtitle, no native client support)", chosen.Codec),
		}
	}

	// ASS / SSA on profiles that strip styling — burn-in.
	if isAdvancedTextSubtitle(chosen.Codec) && profileNeedsAssBurnIn(profileLabel) {
		if sourceIsPipe {
			// Same constraint as bitmap; prefer extract-vtt over silent fail.
			return SubtitlePlan{
				Strategy: StrategyExtract,
				Streams:  streams,
				Reason:   fmt.Sprintf("ASS burn-in disabled (pipe:0 source) — falling back to WebVTT extract for %s", profileLabel),
			}
		}
		picked := chosen
		return SubtitlePlan{
			Strategy: StrategyBurnIn,
			Streams:  streams,
			BurnIn:   &picked,
			Reason:   fmt.Sprintf("burn-in %s (%s strips ASS styling — yumata/lampa#212)", chosen.Codec, profileLabel),
		}
	}

	// Plain text or modern client — extract as WebVTT siblings.
	if isPlainTextSubtitle(chosen.Codec) || isAdvancedTextSubtitle(chosen.Codec) {
		return SubtitlePlan{
			Strategy: StrategyExtract,
			Streams:  streams,
			Reason:   fmt.Sprintf("extract %s → WebVTT", chosen.Codec),
		}
	}

	// Unknown codec — be cautious, don't break playback.
	return SubtitlePlan{
		Strategy: StrategyNone,
		Streams:  streams,
		Reason:   fmt.Sprintf("unsupported subtitle codec %q — skipping", chosen.Codec),
	}
}

// ---------------------------------------------------------------------------
// ffmpeg argv builders
// ---------------------------------------------------------------------------

// buildBurnInVideoFilter returns the `-filter_complex` argument pair for
// burning a single subtitle stream into the main video.  Uses different
// filters for bitmap vs text:
//
//   - bitmap: overlay filter chain — the subtitle stream is decoded as a
//     pre-rendered RGBA picture and composited on top of the video.
//   - text:   subtitles= filter, which calls libass to rasterise dialogue.
//
// The output label is always "[vout]"; the caller maps that as 0:v.
//
// Returns ("", "") when plan does not require burn-in.
func buildBurnInVideoFilter(plan SubtitlePlan, sourceFile string) (filterComplex string, mapVideo string) {
	if plan.Strategy != StrategyBurnIn || plan.BurnIn == nil {
		return "", ""
	}
	si := plan.BurnIn.RelIndex

	if isBitmapSubtitleCodec(plan.BurnIn.Codec) {
		// Bitmap subs (PGS/DVD/DVB) — overlay the rasterised subtitle
		// frames onto the main video.  format=auto picks the smallest
		// pix_fmt that preserves alpha.
		fc := fmt.Sprintf("[0:v:0][0:s:%d]overlay=format=auto[vout]", si)
		return fc, "[vout]"
	}

	// Text subs (ASS / SSA) — rasterise via libass.  Source must be a
	// libavformat-readable URL; pipe:0 is rejected upstream in planSubtitles.
	//
	// Escaping rules for the subtitles filter on the ffmpeg command line
	// are notoriously fragile.  Wrap the filename in single quotes and
	// percent-escape any embedded ':' / '\' / "'".
	escaped := escapeFilterPath(sourceFile)
	fc := fmt.Sprintf("[0:v:0]subtitles=filename='%s':si=%d[vout]", escaped, si)
	return fc, "[vout]"
}

// escapeFilterPath escapes a path/URL for safe embedding inside an ffmpeg
// filter argument wrapped in single quotes.  See ffmpeg-filters(1) section
// "Notes on filtergraph escaping".
func escapeFilterPath(p string) string {
	// First-level (filter argument): replace ':' with '\:' and '\' with '\\'.
	r := strings.NewReplacer(
		`\`, `\\`,
		`:`, `\:`,
		`'`, `\'`,
	)
	return r.Replace(p)
}

// summarizeSubtitlesForResponse builds the JSON-friendly subtitle list that
// the plugin uses to render its track menu.  It includes the strategy and
// chosen burn-in target so the admin panel and diagnostics see exactly
// which stream is being rendered into the video.
func summarizeSubtitlesForResponse(plan SubtitlePlan) map[string]any {
	out := map[string]any{
		"strategy": string(plan.Strategy),
		"reason":   plan.Reason,
	}
	if len(plan.Streams) > 0 {
		streams := make([]map[string]any, 0, len(plan.Streams))
		for _, s := range plan.Streams {
			streams = append(streams, map[string]any{
				"index":   s.AbsIndex,
				"si":      s.RelIndex,
				"codec":   s.Codec,
				"lang":    s.Lang,
				"title":   s.Title,
				"default": s.Default,
				"forced":  s.Forced,
			})
		}
		out["streams"] = streams
	}
	if plan.BurnIn != nil {
		out["burn_in_si"] = plan.BurnIn.RelIndex
		out["burn_in_codec"] = plan.BurnIn.Codec
	}
	return out
}
