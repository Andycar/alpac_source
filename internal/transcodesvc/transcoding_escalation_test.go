package transcodesvc

import (
	"lampac-go/internal/transcode"
	"testing"
)

func TestClassifyFFmpegStderr_HW(t *testing.T) {
	cases := [][]string{
		{"[h264_nvenc @ 0x55] No such filter: 'tonemap_cuda'"},
		{"failed to initialize cuda"},
		{"VAAPI: vaapi_create_buffer failed (status 1)"},
		{"Cannot allocate memory"},
		{"Device or resource busy"},
		{"Could not open hardware device"},
	}
	for _, lines := range cases {
		if got := classifyFFmpegStderr(lines); got != ErrHWUnavailable {
			t.Errorf("classifyFFmpegStderr(%v) = %v, want ErrHWUnavailable", lines, got)
		}
	}
}

func TestClassifyFFmpegStderr_Color(t *testing.T) {
	cases := [][]string{
		{"Unsupported pixel format yuv420p10le"},
		{"Impossible to convert between formats"},
		{"main10 profile requested but not supported"},
	}
	for _, lines := range cases {
		if got := classifyFFmpegStderr(lines); got != ErrColorSpace {
			t.Errorf("classifyFFmpegStderr(%v) = %v, want ErrColorSpace", lines, got)
		}
	}
}

func TestClassifyFFmpegStderr_Codec(t *testing.T) {
	cases := [][]string{
		{"[hls @ 0x55] Could not find tag for codec eac3 in stream #0"},
		{"Output file does not contain any stream"},
		{"Codec not currently supported in container"},
		{"Could not write header"},
		{"Non-monotonic DTS in output stream"},
	}
	for _, lines := range cases {
		if got := classifyFFmpegStderr(lines); got != ErrCodecRefused {
			t.Errorf("classifyFFmpegStderr(%v) = %v, want ErrCodecRefused", lines, got)
		}
	}
}

func TestClassifyFFmpegStderr_OutputWrite(t *testing.T) {
	cases := [][]string{
		// The exact prod symptom (Mortal Kombat II / tv.alcopa.cc 502): the HLS
		// muxer can't create init.mp4 because the working dir vanished. The
		// "could not write header" substring must NOT win — this is a disk/dir
		// failure, not a codec one.
		{
			"[hls @ 0x55] Opening 'init.mp4' for writing",
			"[hls @ 0x55] Failed to open segment 'init.mp4'",
			"[out#8/hls @ 0x55] Could not write header (incorrect codec parameters ?): No such file or directory",
			"Conversion failed!",
		},
		{"av_interleaved_write_frame(): No space left on device"},
		{"Error opening output file index.m3u8."},
		{"[hls] Failed to open segment 'seg_00000.m4s'"},
	}
	for _, lines := range cases {
		if got := classifyFFmpegStderr(lines); got != ErrOutputWrite {
			t.Errorf("classifyFFmpegStderr(%v) = %v, want ErrOutputWrite", lines, got)
		}
	}
	// A bare "Could not write header" with no ENOENT/segment signal is still a
	// genuine codec refusal — make sure we didn't over-grab.
	if got := classifyFFmpegStderr([]string{"Could not write header"}); got != ErrCodecRefused {
		t.Errorf("bare header failure = %v, want ErrCodecRefused (no output-write over-grab)", got)
	}
}

func TestClassifyFFmpegStderr_IO(t *testing.T) {
	cases := [][]string{
		{"Server returned 403 Forbidden"},
		{"Server returned 502 Bad Gateway"},
		{"Connection reset by peer"},
		{"End of file"},
		{"I/O error"},
		{"HTTP error 404"},
	}
	for _, lines := range cases {
		if got := classifyFFmpegStderr(lines); got != ErrIO {
			t.Errorf("classifyFFmpegStderr(%v) = %v, want ErrIO", lines, got)
		}
	}
}

func TestClassifyFFmpegStderr_Empty(t *testing.T) {
	if got := classifyFFmpegStderr(nil); got != ErrUnknown {
		t.Errorf("nil → %v, want ErrUnknown", got)
	}
	if got := classifyFFmpegStderr([]string{}); got != ErrUnknown {
		t.Errorf("empty → %v, want ErrUnknown", got)
	}
	if got := classifyFFmpegStderr([]string{"frame=12345 fps=240 q=22"}); got != ErrUnknown {
		t.Errorf("benign progress line → %v, want ErrUnknown", got)
	}
}

func TestClassifyFFmpegStderr_HWBeatsIO(t *testing.T) {
	// When stderr contains both an HW failure and a benign IO blip, HW wins
	// because it's the actionable one.
	lines := []string{
		"Connection reset by peer",
		"[h264_nvenc] failed to initialize CUDA: out of memory",
	}
	if got := classifyFFmpegStderr(lines); got != ErrHWUnavailable {
		t.Errorf("got %v, want ErrHWUnavailable (HW must outrank IO)", got)
	}
}

// ---------------------------------------------------------------------------
// escalateMode
// ---------------------------------------------------------------------------

func TestEscalateMode_HWFailure_ToSW(t *testing.T) {
	d := escalateMode(transcode.ModeHWTranscode, ErrHWUnavailable)
	if d.NewMode != transcode.ModeSWTranscode {
		t.Errorf("NewMode = %q, want sw-transcode", d.NewMode)
	}
	if !d.DisableHWForJob {
		t.Errorf("DisableHWForJob should be true on HW failure")
	}
}

func TestEscalateMode_ColorSpace_HW_ToSW(t *testing.T) {
	d := escalateMode(transcode.ModeHWTranscode, ErrColorSpace)
	if d.NewMode != transcode.ModeSWTranscode {
		t.Errorf("HW + color → sw, got %q", d.NewMode)
	}
	if !d.DisableHWForJob {
		t.Errorf("DisableHWForJob should be true after HW tonemap fail")
	}
}

func TestEscalateMode_ColorSpace_SW_NoEscalation(t *testing.T) {
	d := escalateMode(transcode.ModeSWTranscode, ErrColorSpace)
	if d.NewMode != transcode.ModeSWTranscode {
		t.Errorf("SW + color → sw (no further escalation), got %q", d.NewMode)
	}
}

func TestEscalateMode_CodecLadder(t *testing.T) {
	// remux fails → audio-only
	d := escalateMode(transcode.ModeRemux, ErrCodecRefused)
	if d.NewMode != transcode.ModeAudioOnly {
		t.Errorf("remux+codec → audio-only, got %q", d.NewMode)
	}
	// audio-only fails → sw-transcode
	d = escalateMode(transcode.ModeAudioOnly, ErrCodecRefused)
	if d.NewMode != transcode.ModeSWTranscode {
		t.Errorf("audio-only+codec → sw, got %q", d.NewMode)
	}
	// sw fails → no further mode (just retry)
	d = escalateMode(transcode.ModeSWTranscode, ErrCodecRefused)
	if d.NewMode != transcode.ModeSWTranscode {
		t.Errorf("sw+codec → sw (terminal), got %q", d.NewMode)
	}
}

func TestEscalateMode_IO_NoChange(t *testing.T) {
	for _, mode := range []transcode.TranscodingMode{transcode.ModeRemux, transcode.ModeAudioOnly, transcode.ModeSWTranscode, transcode.ModeHWTranscode} {
		d := escalateMode(mode, ErrIO)
		if d.NewMode != mode {
			t.Errorf("mode %q + IO → %q, want unchanged", mode, d.NewMode)
		}
		if d.DisableHWForJob {
			t.Errorf("IO error should NOT disable HW for mode %q", mode)
		}
	}
}

func TestEscalateMode_Unknown_NoChange(t *testing.T) {
	d := escalateMode(transcode.ModeRemux, ErrUnknown)
	if d.NewMode != transcode.ModeRemux {
		t.Errorf("unknown error → mode unchanged, got %q", d.NewMode)
	}
}
