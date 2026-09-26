package transcodesvc

import (
	"context"
	"fmt"
	"lampac-go/internal/transcode"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Hardware acceleration auto-detect.
//
// On service start we probe ffmpeg for available hwaccels, try each one
// against a tiny test encode, and pick the best option for the platform:
//
//	linux/amd64  → nvenc > qsv > vaapi > sw
//	linux/arm64  → rkmpp > v4l2m2m > sw
//	darwin       → videotoolbox > sw
//	windows      → qsv > nvenc > d3d11va > sw
//
// When the smart-mode selector lands on ModeHWTranscode or ModeSWTranscode,
// appendVideoCodec consults HWAccelInfo.Active() to decide whether to emit
// HW args or fall back to libx264.
//
// A cascade-failure guard flips HWAccelInfo.disabled when the first real job
// crashes with a HW-related error, so subsequent jobs don't re-trigger the
// same failure until the server is restarted.
// ---------------------------------------------------------------------------

// HWKind enumerates supported hardware acceleration backends.
type HWKind string

const (
	HWNone         HWKind = ""
	HWNVENC        HWKind = "nvenc"
	HWQSV          HWKind = "qsv"
	HWVAAPI        HWKind = "vaapi"
	HWVideoToolbox HWKind = "videotoolbox"
	HWRKMPP        HWKind = "rkmpp"
	HWV4L2M2M      HWKind = "v4l2m2m"
	HWD3D11VA      HWKind = "d3d11va"
)

// HWAccelInfo is the runtime state of the HW detection subsystem.
type HWAccelInfo struct {
	Kind     HWKind // selected backend (HWNone = software only)
	Device   string // e.g. /dev/dri/renderD128 for VAAPI; "" for others
	Detected bool   // true if any HW backend passed the tiny encode probe

	// Cascade fallback: a single HW job failure trips disabled=1 via atomic
	// so every subsequent appendVideoCodec call goes straight to libx264.
	disabled int32
}

// Active reports whether HW encoding should be attempted right now.
func (h *HWAccelInfo) Active() bool {
	if h == nil || !h.Detected || h.Kind == HWNone {
		return false
	}
	return atomic.LoadInt32(&h.disabled) == 0
}

// Disable permanently flips the HW path off (used by the cascade guard
// when a HW job dies with a HW-specific error).
func (h *HWAccelInfo) Disable(reason string) {
	if h == nil {
		return
	}
	if atomic.CompareAndSwapInt32(&h.disabled, 0, 1) {
		log.Warn().Str("kind", string(h.Kind)).Str("reason", reason).Msg("transcoding: HW accel disabled for remainder of session")
	}
}

// detectHWAccel runs ffmpeg -hwaccels + tiny probe encodes and returns the
// best available backend.  Never errors — on any failure returns an empty
// HWAccelInfo so callers fall through to software.
func detectHWAccel(ffmpegPath string) *HWAccelInfo {
	info := &HWAccelInfo{Kind: HWNone}
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}

	// 1. List hwaccels ffmpeg was compiled with.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-hwaccels").Output()
	if err != nil {
		log.Warn().Err(err).Msg("transcoding: hw detect — ffmpeg -hwaccels failed")
		return info
	}
	available := parseHWAccelList(string(out))
	if len(available) == 0 {
		log.Info().Msg("transcoding: no HW accel backends compiled in ffmpeg")
		return info
	}

	// 2. Platform-ordered candidate list.
	candidates := platformHWPriority()

	// 3. Walk candidates; for each one that ffmpeg advertises, run a tiny
	//    test encode to verify the encoder actually works on this machine
	//    (kernel driver / GPU / codec support).
	for _, cand := range candidates {
		if !hasString(available, hwKindToFFmpegName(cand.kind)) && cand.kind != HWVideoToolbox && cand.kind != HWV4L2M2M {
			// videotoolbox shows up as "videotoolbox", v4l2m2m not in -hwaccels
			continue
		}
		if probeHWEncoder(ffmpegPath, cand.kind, cand.device) {
			info.Kind = cand.kind
			info.Device = cand.device
			info.Detected = true
			log.Info().
				Str("kind", string(cand.kind)).
				Str("device", cand.device).
				Msg("transcoding: HW accel detected")
			return info
		}
		log.Debug().Str("kind", string(cand.kind)).Msg("transcoding: HW probe failed, trying next")
	}

	log.Info().Msg("transcoding: no usable HW accel, falling back to software")
	return info
}

// parseHWAccelList parses the output of `ffmpeg -hwaccels`.  Format:
//
//	Hardware acceleration methods:
//	vaapi
//	qsv
//	cuda
func parseHWAccelList(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Hardware") {
			continue
		}
		// Ignore fluff like "ffmpeg version ..." that some wrappers emit first.
		if strings.Contains(line, "version") || strings.Contains(line, "built") {
			continue
		}
		out = append(out, strings.ToLower(line))
	}
	return out
}

// hwCandidate pairs a kind with an optional device path.
type hwCandidate struct {
	kind   HWKind
	device string
}

// platformHWPriority returns the preferred backend order for the current GOOS/GOARCH.
func platformHWPriority() []hwCandidate {
	switch runtime.GOOS {
	case "linux":
		if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
			return []hwCandidate{
				{HWRKMPP, ""},
				{HWV4L2M2M, ""},
			}
		}
		// linux amd64 / x86_64 desktops and servers.
		return []hwCandidate{
			{HWNVENC, ""},
			{HWQSV, ""},
			{HWVAAPI, pickVAAPIDevice()},
		}
	case "darwin":
		return []hwCandidate{
			{HWVideoToolbox, ""},
		}
	case "windows":
		return []hwCandidate{
			{HWQSV, ""},
			{HWNVENC, ""},
			{HWD3D11VA, ""},
		}
	}
	return nil
}

// pickVAAPIDevice returns the first /dev/dri/renderD1NN that exists.
func pickVAAPIDevice() string {
	for i := 128; i < 136; i++ {
		p := "/dev/dri/renderD" + strconv.Itoa(i)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/dev/dri/renderD128"
}

// hwKindToFFmpegName maps our kind to the string used in ffmpeg -hwaccels.
func hwKindToFFmpegName(k HWKind) string {
	switch k {
	case HWNVENC:
		return "cuda"
	case HWQSV:
		return "qsv"
	case HWVAAPI:
		return "vaapi"
	case HWVideoToolbox:
		return "videotoolbox"
	case HWRKMPP:
		return "rkmpp"
	case HWV4L2M2M:
		return "v4l2m2m"
	case HWD3D11VA:
		return "d3d11va"
	}
	return ""
}

// probeHWEncoder runs a 1-frame null encode to verify the backend actually
// works on this machine.  Returns true on exit code 0.
func probeHWEncoder(ffmpegPath string, kind HWKind, device string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Build a minimal test command tailored to each backend.  We encode a
	// tiny black frame to /dev/null — if the codec path loads libs and
	// accepts params, we're good.
	var args []string
	switch kind {
	case HWNVENC:
		args = []string{
			"-hide_banner", "-v", "error",
			"-f", "lavfi", "-i", "color=black:s=64x64:d=0.1",
			"-frames:v", "1", "-c:v", "h264_nvenc",
			"-f", "null", "-",
		}
	case HWQSV:
		args = []string{
			"-hide_banner", "-v", "error",
			"-f", "lavfi", "-i", "color=black:s=64x64:d=0.1",
			"-frames:v", "1", "-c:v", "h264_qsv",
			"-f", "null", "-",
		}
	case HWVAAPI:
		dev := device
		if dev == "" {
			dev = "/dev/dri/renderD128"
		}
		if _, err := os.Stat(dev); err != nil {
			return false
		}
		args = []string{
			"-hide_banner", "-v", "error",
			"-vaapi_device", dev,
			"-f", "lavfi", "-i", "color=black:s=64x64:d=0.1",
			"-vf", "format=nv12,hwupload",
			"-frames:v", "1", "-c:v", "h264_vaapi",
			"-f", "null", "-",
		}
	case HWVideoToolbox:
		args = []string{
			"-hide_banner", "-v", "error",
			"-f", "lavfi", "-i", "color=black:s=64x64:d=0.1",
			"-frames:v", "1", "-c:v", "h264_videotoolbox", "-allow_sw", "1",
			"-f", "null", "-",
		}
	case HWRKMPP:
		args = []string{
			"-hide_banner", "-v", "error",
			"-f", "lavfi", "-i", "color=black:s=64x64:d=0.1",
			"-frames:v", "1", "-c:v", "h264_rkmpp",
			"-f", "null", "-",
		}
	case HWV4L2M2M:
		args = []string{
			"-hide_banner", "-v", "error",
			"-f", "lavfi", "-i", "color=black:s=64x64:d=0.1",
			"-frames:v", "1", "-c:v", "h264_v4l2m2m", "-b:v", "1M",
			"-f", "null", "-",
		}
	case HWD3D11VA:
		// Windows D3D11VA — use qsv/nvenc probes, skip here.
		return false
	default:
		return false
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Debug().
			Str("kind", string(kind)).
			Str("stderr", strings.TrimSpace(stderr.String())).
			Msg("transcoding: HW probe encode failed")
		return false
	}
	return true
}

func hasString(slice []string, needle string) bool {
	for _, s := range slice {
		if s == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Argument builders
// ---------------------------------------------------------------------------

// buildHWInputArgs returns ffmpeg flags that need to be placed BEFORE `-i`.
// For HW decoding paths (VAAPI/QSV/NVENC) we want `-hwaccel <kind>` up front
// so the decoder frames land directly in GPU memory.
func (h *HWAccelInfo) buildHWInputArgs() []string {
	if !h.Active() {
		return nil
	}
	switch h.Kind {
	case HWNVENC:
		return []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
	case HWQSV:
		return []string{"-hwaccel", "qsv", "-hwaccel_output_format", "qsv"}
	case HWVAAPI:
		dev := h.Device
		if dev == "" {
			dev = "/dev/dri/renderD128"
		}
		return []string{"-hwaccel", "vaapi", "-vaapi_device", dev, "-hwaccel_output_format", "vaapi"}
	case HWVideoToolbox:
		return []string{"-hwaccel", "videotoolbox"}
	case HWRKMPP:
		return []string{"-hwaccel", "rkmpp", "-hwaccel_output_format", "drm_prime"}
	}
	return nil
}

// buildHWEncoderArgs returns the `-c:v <hw_encoder> …` block for the output.
// bitrateKbps is the target bitrate (per transcode.PickVideoBitrate for the source).
func (h *HWAccelInfo) buildHWEncoderArgs(bitrateKbps int) []string {
	if !h.Active() {
		return nil
	}
	br := strconv.Itoa(bitrateKbps) + "k"
	maxrate := strconv.Itoa(int(float64(bitrateKbps)*1.4)) + "k"
	bufsize := strconv.Itoa(bitrateKbps*2) + "k"

	switch h.Kind {
	case HWNVENC:
		return []string{
			"-c:v", "h264_nvenc",
			"-preset", "p4",
			"-tune", "hq",
			"-rc", "vbr",
			"-cq", "23",
			"-b:v", br, "-maxrate", maxrate, "-bufsize", bufsize,
		}
	case HWQSV:
		return []string{
			"-c:v", "h264_qsv",
			"-preset", "veryfast",
			"-global_quality", "23",
			"-look_ahead", "0",
			"-b:v", br, "-maxrate", maxrate,
		}
	case HWVAAPI:
		// vaapi needs an explicit format filter to upload CPU frames when
		// decoded via software.  When the whole pipeline is on GPU
		// (decoder gave us vaapi frames), the filter is a no-op.
		return []string{
			"-vf", "format=nv12|vaapi,hwupload",
			"-c:v", "h264_vaapi",
			"-qp", "23",
			"-b:v", br, "-maxrate", maxrate,
		}
	case HWVideoToolbox:
		return []string{
			"-c:v", "h264_videotoolbox",
			"-allow_sw", "1",
			"-b:v", br, "-maxrate", maxrate,
		}
	case HWRKMPP:
		return []string{
			"-c:v", "h264_rkmpp",
			"-b:v", br,
		}
	case HWV4L2M2M:
		return []string{
			"-c:v", "h264_v4l2m2m",
			"-b:v", br,
		}
	}
	return nil
}

// is10BitPixFmt returns true for 10-/12-bit video pixel formats commonly
// produced by HEVC HDR sources (yuv420p10le, yuv422p10le, yuv444p10le,
// p010le, p016le, etc.).  These input formats need a colour-space and
// bit-depth conversion before they can be fed to an h264 encoder, since
// h264 is 8-bit-only on every consumer device that ships in 2026.
func is10BitPixFmt(pixFmt string) bool {
	if pixFmt == "" {
		return false
	}
	pf := strings.ToLower(pixFmt)
	return strings.Contains(pf, "p10") || strings.Contains(pf, "p012") ||
		strings.Contains(pf, "p016") || strings.Contains(pf, "10le") ||
		strings.Contains(pf, "12le") || strings.Contains(pf, "16le")
}

// tonemapFilters records which HDR-relevant ffmpeg filters this build ships.
// Probed once per process from `ffmpeg -filters` (static builds vary: BtbN
// bundles zimg, distro ffmpeg may not).
type tonemapFilters struct {
	// TonemapX — SIMD-тонмаппер jellyfin-ffmpeg: один фильтр вместо цепочки zscale→tonemap→zscale
	// (в 2–4 раза дешевле по CPU при том же результате; умеет bt2390, HLG, DoVi-метаданные).
	TonemapX bool
	ZScale   bool // zscale (libzimg) — precise transfer/matrix conversions
	Tonemap  bool // tonemap — the linear-light HDR→SDR operator
}

var (
	tonemapFiltersOnce sync.Once
	tonemapFiltersHave tonemapFilters
)

// detectTonemapFilters probes the ffmpeg binary's filter list once and caches
// the result. Called from NewTranscodingService; safe (and a no-op) elsewhere.
func detectTonemapFilters(ffmpegPath string) {
	tonemapFiltersOnce.Do(func() {
		if ffmpegPath == "" {
			ffmpegPath = "ffmpeg"
		}
		out, err := exec.Command(ffmpegPath, "-hide_banner", "-filters").Output()
		if err != nil {
			return // leave zero value → legacy chain
		}
		s := string(out)
		tonemapFiltersHave = tonemapFilters{
			ZScale:   strings.Contains(s, " zscale "),
			Tonemap:  strings.Contains(s, " tonemap "),
			TonemapX: strings.Contains(s, " tonemapx "),
		}
		log.Info().Bool("zscale", tonemapFiltersHave.ZScale).Bool("tonemap", tonemapFiltersHave.Tonemap).
			Bool("tonemapx", tonemapFiltersHave.TonemapX).Msg("transcoding: HDR tonemap filter support")
	})
}

// buildTonemapPrefilter returns the `-vf <chain>` arguments that downconvert
// a 10-/12-bit HDR source to 8-bit BT.709 SDR before reaching the encoder.
//
// When the ffmpeg build ships zscale+tonemap, this is a REAL tonemap: linearize
// the PQ/HLG transfer (zscale reads the input trc from frame props) → hable
// operator in linear light → re-encode to BT.709. Without those filters we fall
// back to the legacy matrix-only `scale` conversion — it fixes the bt2020
// matrix but NOT the transfer curve, so PQ sources come out dim/washed-out
// («блеклый HDR»); better than nothing, worse than zscale. The legacy chain
// also had `in_range=pc`, which is wrong for virtually all video (limited/tv
// range) and additionally crushed levels — removed, range now comes from frame
// props.
//
// The chain is intentionally CPU-side: HW tonemap exists only on a subset of
// backends (vaapi with iHD, libplacebo with vulkan, tonemap_cuda) and breaks
// differently on each; CPU runs everywhere ffmpeg does. The alternative is
// "video doesn't play at all on every TV from 2013–2017" — see webos-forums
// topic5073 and the recurring «чёрный экран» reports for HDR10 on Tizen ≤ 5.
//
// kind hints which downstream HW encoder will consume the output: vaapi
// requires the format chain to terminate with `hwupload` so the encoder
// receives GPU-mapped frames.
func buildTonemapPrefilter(kind HWKind) []string {
	// Target pixel format per consumer.
	target := "yuv420p" // SW libx264
	hwupload := ""
	switch kind {
	case HWVAAPI:
		target = "nv12"
		hwupload = ",hwupload"
	case HWNVENC, HWQSV, HWVideoToolbox, HWRKMPP, HWV4L2M2M:
		target = "nv12"
	}

	if tonemapFiltersHave.TonemapX {
		// jellyfin-ffmpeg: tonemapx делает всю цепочку (линеаризация PQ/HLG, оператор bt2390,
		// обратно в BT.709 tv-range) одним SIMD-проходом в yuv, без float-RGB через zscale —
		// та же картинка, что у Jellyfin, и заметно меньше CPU на 4K HDR.
		chain := "tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:r=tv:format=" + target + hwupload
		return []string{"-vf", chain}
	}
	if tonemapFiltersHave.ZScale && tonemapFiltersHave.Tonemap {
		// Canonical ffmpeg HDR→SDR chain: linear light in float RGB, hable
		// tonemap (npl=100 nits nominal SDR peak), back to BT.709 tv-range.
		chain := "zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709," +
			"tonemap=tonemap=hable:desat=0," +
			"zscale=t=bt709:m=bt709:r=tv,format=" + target + hwupload
		return []string{"-vf", chain}
	}

	// Legacy fallback (no zimg in this ffmpeg): matrix-only conversion.
	return []string{
		"-vf",
		"scale=in_color_matrix=bt2020nc:out_color_matrix=bt709:out_range=tv,format=" + target + hwupload,
	}
}

// buildHWEncoderArgsForTonemappedInput returns `-c:v <hw>` plus rate-control
// for a HW backend that's about to receive an already-tonemapped 8-bit
// frame stream (output of buildTonemapPrefilter).
//
// Differs from buildHWEncoderArgs in two places:
//   - VAAPI doesn't re-emit format=nv12|vaapi,hwupload (already done by the
//     prefilter chain), so the -vf clash is avoided.
//   - VideoToolbox emits explicit BT.709 metadata flags so the encoded
//     stream carries the correct colourspace tags.
func (h *HWAccelInfo) buildHWEncoderArgsForTonemappedInput(bitrateKbps int) []string {
	if !h.Active() {
		return nil
	}
	br := strconv.Itoa(bitrateKbps) + "k"
	maxrate := strconv.Itoa(int(float64(bitrateKbps)*1.4)) + "k"
	bufsize := strconv.Itoa(bitrateKbps*2) + "k"

	colourFlags := []string{
		"-color_primaries", "bt709",
		"-color_trc", "bt709",
		"-colorspace", "bt709",
		"-color_range", "tv",
	}

	switch h.Kind {
	case HWNVENC:
		args := []string{
			"-c:v", "h264_nvenc",
			"-preset", "p4",
			"-tune", "hq",
			"-rc", "vbr",
			"-cq", "23",
			"-pix_fmt", "yuv420p",
			"-b:v", br, "-maxrate", maxrate, "-bufsize", bufsize,
		}
		return append(args, colourFlags...)
	case HWQSV:
		args := []string{
			"-c:v", "h264_qsv",
			"-preset", "veryfast",
			"-global_quality", "23",
			"-look_ahead", "0",
			"-b:v", br, "-maxrate", maxrate,
		}
		return append(args, colourFlags...)
	case HWVAAPI:
		// No -vf here — the prefilter already terminated with hwupload.
		args := []string{
			"-c:v", "h264_vaapi",
			"-qp", "23",
			"-b:v", br, "-maxrate", maxrate,
		}
		return append(args, colourFlags...)
	case HWVideoToolbox:
		args := []string{
			"-c:v", "h264_videotoolbox",
			"-allow_sw", "1",
			"-pix_fmt", "yuv420p",
			"-b:v", br, "-maxrate", maxrate,
		}
		return append(args, colourFlags...)
	case HWRKMPP:
		args := []string{
			"-c:v", "h264_rkmpp",
			"-pix_fmt", "yuv420p",
			"-b:v", br,
		}
		return append(args, colourFlags...)
	case HWV4L2M2M:
		args := []string{
			"-c:v", "h264_v4l2m2m",
			"-pix_fmt", "yuv420p",
			"-b:v", br,
		}
		return append(args, colourFlags...)
	}
	return nil
}

// supportsMultiRung returns true when this HW backend can run several
// concurrent encode sessions cheaply enough to back an ABR ladder
// (P3.K2 + P3.N).
//
//   - NVENC: ~5-8 concurrent sessions on consumer cards, more on Quadro/A.
//     Frame transfer between CPU and GPU is fast enough that we don't
//     need backend-specific filter chains.
//   - VideoToolbox (macOS / Apple Silicon): 2-3 hardware encode contexts;
//     accepts CPU frames natively, same simple filter graph as SW.
//   - VAAPI / QSV: technically support concurrent sessions, but the
//     filter graph needs backend-specific format conversions (scale_vaapi,
//     vpp_qsv) which aren't trivial to mix with -filter:v:N per-stream
//     specifiers.  Defer those backends to a later iteration.
//   - RKMPP / V4L2M2M: typically single-session capable hardware (TV-box
//     SoCs); multi-rung doesn't pay off there.
func (h *HWAccelInfo) supportsMultiRung() bool {
	if !h.Active() {
		return false
	}
	switch h.Kind {
	case HWNVENC, HWVideoToolbox:
		return true
	}
	return false
}

// appendMultiRungVideoArgs appends `-filter:v:N`, `-c:v:N`, and rate-
// control flags for one ABR rung using the active HW backend.  Mirrors
// the structure of buildHWEncoderArgs but uses the per-stream specifier
// suffix that var_stream_map needs.
//
// Source frames arrive as CPU frames in this build (we don't pass
// -hwaccel for the decoder when multi-rung is on, to keep the filter
// graph simple).  The encoders we enable here all accept CPU input.
func (h *HWAccelInfo) appendMultiRungVideoArgs(args []string, rungIdx int, rung transcode.ABRRung, gop int) []string {
	suffix := strconv.Itoa(rungIdx)
	br := strconv.Itoa(rung.BitrateKbps) + "k"
	maxrate := strconv.Itoa(int(float64(rung.BitrateKbps)*1.4)) + "k"
	bufsize := strconv.Itoa(rung.BitrateKbps*2) + "k"

	// All backends do the same scale step on CPU.
	args = append(args, "-filter:v:"+suffix, fmt.Sprintf("scale=%d:-2", rung.Width))

	switch h.Kind {
	case HWNVENC:
		args = append(args,
			"-c:v:"+suffix, "h264_nvenc",
			"-preset:v:"+suffix, "p4",
			"-tune:v:"+suffix, "hq",
			"-rc:v:"+suffix, "vbr",
			"-cq:v:"+suffix, "23",
			"-pix_fmt:v:"+suffix, "yuv420p",
			"-b:v:"+suffix, br,
			"-maxrate:v:"+suffix, maxrate,
			"-bufsize:v:"+suffix, bufsize,
			"-g:v:"+suffix, strconv.Itoa(gop),
		)
	case HWVideoToolbox:
		args = append(args,
			"-c:v:"+suffix, "h264_videotoolbox",
			"-allow_sw:v:"+suffix, "1",
			"-pix_fmt:v:"+suffix, "yuv420p",
			"-b:v:"+suffix, br,
			"-maxrate:v:"+suffix, maxrate,
			"-g:v:"+suffix, strconv.Itoa(gop),
		)
	}
	return args
}

// extractVideoDimensions reads width/height from the first video stream.
// Returns 1920x1080 when probe is unavailable (best-effort default).
func extractVideoDimensions(probe map[string]any) (int, int) {
	if probe == nil {
		return 1920, 1080
	}
	streams, _ := probe["streams"].([]any)
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil || toStr(sm["codec_type"]) != "video" {
			continue
		}
		w := toIntOrZero(sm["width"])
		h := toIntOrZero(sm["height"])
		if w > 0 && h > 0 {
			return w, h
		}
	}
	return 1920, 1080
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toIntOrZero(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	}
	return 0
}

// ---------------------------------------------------------------------------
// hwDetectOnce runs detection lazily so tests (and callers that don't care)
// never pay the probe cost.
// ---------------------------------------------------------------------------

var (
	hwDetectMu    sync.Mutex
	hwDetectedFor string
	hwDetectedVal *HWAccelInfo
)

func getOrDetectHWAccel(ffmpegPath string) *HWAccelInfo {
	hwDetectMu.Lock()
	defer hwDetectMu.Unlock()
	if hwDetectedFor == ffmpegPath && hwDetectedVal != nil {
		return hwDetectedVal
	}
	info := detectHWAccel(ffmpegPath)
	hwDetectedFor = ffmpegPath
	hwDetectedVal = info
	return info
}
