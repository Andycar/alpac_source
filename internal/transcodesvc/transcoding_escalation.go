package transcodesvc

import (
	"lampac-go/internal/transcode"
	"strings"
)

// ---------------------------------------------------------------------------
// FFmpeg failure classification + mode escalation — P3.E.
//
// Pre-existing tryAutoRestart resumes a crashed job at the last segment,
// but always re-spawns with the SAME pipeline mode.  When the mode itself
// is the cause (e.g. "remux" picked AAC stream-copy on a Tizen with EAC3,
// HW encoder failed because the device is busy, libplacebo not available
// for the chosen tonemap), every restart fails identically until the
// 3-per-60s budget runs out.  The user sees the player give up after a
// few seconds of stalls.
//
// classifyFFmpegStderr() inspects the last few KB of ffmpeg output and
// labels the failure mode.  escalateMode() then picks the *cheapest*
// next-step pipeline that side-steps the cause — usually one tier more
// expensive than the failed one (remux → audio-only → sw-transcode).
// HW failures additionally flip a "disable HW for this job" flag so the
// retry skips the HW path entirely.
//
// Together these turn the year-old "обрыв на 6-12 минуте" / "крутится
// 30 секунд → финиш" reports into recoverable micro-stalls.
// ---------------------------------------------------------------------------

// FFmpegErrorClass labels what went wrong with an ffmpeg run.  The zero
// value is ErrUnknown — restart-only, no mode change.
type FFmpegErrorClass string

const (
	ErrUnknown        FFmpegErrorClass = "unknown"
	ErrHWUnavailable  FFmpegErrorClass = "hw-unavailable"
	ErrCodecRefused   FFmpegErrorClass = "codec-refused"
	ErrColorSpace     FFmpegErrorClass = "color-space"
	ErrIO             FFmpegErrorClass = "io"
	ErrSegmentMissing FFmpegErrorClass = "segment-missing"
	// ErrOutputWrite — the muxer couldn't create its OUTPUT (segment/init) file:
	// the working dir vanished, disk full, or read-only mount. Distinct from
	// ErrCodecRefused because ffmpeg reports it as "Could not write header
	// (incorrect codec parameters ?): No such file or directory" which reads
	// like a codec problem but is really a filesystem one.
	ErrOutputWrite FFmpegErrorClass = "output-write"
)

// classifyFFmpegStderr scans the captured ffmpeg stderr lines (most recent
// first or in chronological order — order doesn't matter, we look for any
// matching pattern) and returns the most specific class.  When multiple
// patterns hit, codec/HW errors win over IO errors because they're more
// actionable for escalation.
func classifyFFmpegStderr(lines []string) FFmpegErrorClass {
	if len(lines) == 0 {
		return ErrUnknown
	}

	// Concatenate lowercase for sub-string matching.  ffmpeg stderr is
	// English-only, so case folding is enough — we don't deal with Cyrillic
	// or non-ASCII here.
	var sb strings.Builder
	sb.Grow(len(lines) * 80)
	for _, l := range lines {
		sb.WriteString(strings.ToLower(l))
		sb.WriteByte('\n')
	}
	blob := sb.String()

	// HW failures are diagnostic gold — match them first, they ALWAYS
	// warrant mode escalation regardless of any IO noise around them.
	hwPatterns := []string{
		"no such filter",
		"unknown encoder",                   // h264_nvenc / h264_vaapi missing
		"cannot allocate memory",            // common VAAPI / NVENC OOM signal
		"device or resource busy",           // HW backend tied up by another job
		"failed to initialize cuda",         // NVENC startup failure
		"vaapi: vaapi_create_buffer failed", // VAAPI surface alloc failure
		"vaapi: failed to upload",           // hwupload surface failure
		"qsv: failed to initialize",         // QSV backend init
		"vt encoder: invalid",               // VideoToolbox path
		"could not open hardware device",
	}
	for _, p := range hwPatterns {
		if strings.Contains(blob, p) {
			return ErrHWUnavailable
		}
	}

	// Color-space / pixfmt failures — usually 10-bit input + 8-bit-only
	// encoder.  These also escalate (force tonemap path).
	colorPatterns := []string{
		"pixel format",
		"unsupported pixel format",
		"impossible to convert between",
		"format not supported",
		"yuv420p10le",
		"main10",
	}
	for _, p := range colorPatterns {
		if strings.Contains(blob, p) {
			return ErrColorSpace
		}
	}

	// Output write failure — the HLS muxer couldn't create its segment/init
	// file. MUST be checked before the codec block: the prod symptom is
	// "[hls] Failed to open segment 'init.mp4'" + "Could not write header
	// (incorrect codec parameters ?): No such file or directory", and the
	// "could not write header" substring would otherwise be misread as a codec
	// refusal. The real cause is the working dir gone / disk full / RO mount.
	writePatterns := []string{
		"failed to open segment",
		"error opening output",
		"no space left on device",
		"read-only file system",
		"could not write header (incorrect codec parameters ?): no such file or directory",
	}
	for _, p := range writePatterns {
		if strings.Contains(blob, p) {
			return ErrOutputWrite
		}
	}

	// Codec mismatch — output container doesn't accept the stream-copied
	// codec.  Most common is "remux EAC3 into HLS-MP2T" failing.
	codecPatterns := []string{
		"could not find tag",
		"codec not currently supported in container",
		"codec not supported in this container",
		"output file does not contain any stream",
		"non-monotonic dts",      // intermittent but worth re-encoding
		"non-monotonous dts",     // ffmpeg older spelling
		"could not write header", // muxer rejected the stream
		"unsupported codec id",
	}
	for _, p := range codecPatterns {
		if strings.Contains(blob, p) {
			return ErrCodecRefused
		}
	}

	// I/O — source went away, network blip, torrent pipe stalled.  These
	// don't warrant escalation; tryAutoRestart will resume from the last
	// segment with the same mode.
	ioPatterns := []string{
		"connection reset",
		"connection refused",
		"connection timed out",
		"end of file",
		"i/o error",
		"server returned 4",
		"server returned 5",
		"http error 4",
		"http error 5",
		"403 forbidden",
		"404 not found",
		"unable to read",
	}
	for _, p := range ioPatterns {
		if strings.Contains(blob, p) {
			return ErrIO
		}
	}

	return ErrUnknown
}

// EscalationDecision describes how to retry the job.  When NewMode equals
// the current mode and DisableHWForJob is false, the caller falls through
// to the normal "resume from last segment, same mode" path.
type EscalationDecision struct {
	NewMode         transcode.TranscodingMode
	DisableHWForJob bool
	Reason          string
}

// escalateMode picks the cheapest pipeline that should side-step the
// failure class observed in the previous run.  Returns a decision whose
// NewMode equals `current` when no escalation makes sense for this class.
func escalateMode(current transcode.TranscodingMode, class FFmpegErrorClass) EscalationDecision {
	switch class {
	case ErrHWUnavailable:
		// Drop to SW for this job, leave the global HW flag alone — the
		// device might just be momentarily busy with another stream.
		return EscalationDecision{
			NewMode:         transcode.ModeSWTranscode,
			DisableHWForJob: true,
			Reason:          "ffmpeg crashed in HW path → falling back to libx264 for this job",
		}

	case ErrColorSpace:
		// The HEVC10 → 8-bit tonemap chain (P3.D) failed.  Force SW so we
		// hit the config-driven `h264_yuv420p10le` filter graph instead.
		if current == transcode.ModeHWTranscode {
			return EscalationDecision{
				NewMode:         transcode.ModeSWTranscode,
				DisableHWForJob: true,
				Reason:          "HW tonemap chain failed → switching to SW tonemap+libx264",
			}
		}
		return EscalationDecision{
			NewMode: current,
			Reason:  "color-space error in SW path — restart-only",
		}

	case ErrCodecRefused:
		// Mode escalation ladder: remux → audio-only → sw-transcode.
		// AudioOnly handles "audio codec doesn't fit container" (the EAC3
		// in HLS-MP2T case); SW handles the rest.
		switch current {
		case transcode.ModeRemux:
			return EscalationDecision{
				NewMode: transcode.ModeAudioOnly,
				Reason:  "remux rejected by muxer → bumping to audio-only re-encode",
			}
		case transcode.ModeAudioOnly:
			return EscalationDecision{
				NewMode: transcode.ModeSWTranscode,
				Reason:  "audio-only failed → escalating to full SW transcode",
			}
		default:
			return EscalationDecision{NewMode: current, Reason: "codec refused but no further escalation available"}
		}

	case ErrIO, ErrSegmentMissing:
		// Source-side problem.  Same mode, just resume.
		return EscalationDecision{NewMode: current, Reason: "I/O blip — resume same mode"}
	}

	return EscalationDecision{NewMode: current, Reason: "no escalation rule"}
}
