package transcodesvc

import (
	"lampac-go/internal/transcode"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lampac-go/internal/config"
)

// TestTranscodingTokenRoundTrip verifies HMAC token build/parse cycle.
func TestTranscodingTokenRoundTrip(t *testing.T) {
	cfg := config.Config{}
	cfg.Transcoding.Enable = true
	cfg.Transcoding.TempRoot = t.TempDir()

	svc := NewTranscodingService(cfg)
	defer svc.Stop()

	id := newJobID()
	token := svc.buildToken(id)

	parsed, ok := svc.parseToken(token)
	if !ok {
		t.Fatal("parseToken should succeed for valid token")
	}
	if parsed != id {
		t.Fatalf("parsed id %q != original %q", parsed, id)
	}
}

// TestTranscodingTokenTampered verifies that a tampered token is rejected.
func TestTranscodingTokenTampered(t *testing.T) {
	cfg := config.Config{}
	cfg.Transcoding.Enable = true
	cfg.Transcoding.TempRoot = t.TempDir()

	svc := NewTranscodingService(cfg)
	defer svc.Stop()

	id := newJobID()
	token := svc.buildToken(id)

	// Tamper: replace last char.
	tampered := token[:len(token)-1] + "X"
	if _, ok := svc.parseToken(tampered); ok {
		t.Fatal("tampered token should NOT parse")
	}

	// Completely bogus.
	if _, ok := svc.parseToken("garbage"); ok {
		t.Fatal("garbage should NOT parse")
	}
	if _, ok := svc.parseToken(""); ok {
		t.Fatal("empty should NOT parse")
	}
}

// TestTranscodingNewJobID verifies job ID uniqueness and format.
func TestTranscodingNewJobID(t *testing.T) {
	ids := make(map[string]bool, 100)
	for range 100 {
		id := newJobID()
		if len(id) != 32 {
			t.Fatalf("job ID should be 32 hex chars, got %d: %q", len(id), id)
		}
		if ids[id] {
			t.Fatalf("duplicate job ID: %s", id)
		}
		ids[id] = true
	}
}

// TestTranscodingStatsEmpty verifies stats on empty service.
func TestTranscodingStatsEmpty(t *testing.T) {
	cfg := config.Config{}
	cfg.Transcoding.Enable = true
	cfg.Transcoding.TempRoot = t.TempDir()

	svc := NewTranscodingService(cfg)
	defer svc.Stop()

	snap := svc.StatsSnapshot()
	jobs, ok := snap["jobs"].([]map[string]any)
	if !ok {
		t.Fatalf("expected []map[string]any for jobs, got %T", snap["jobs"])
	}
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs, got %d", len(jobs))
	}
}

// TestTranscodingServiceDefaults verifies default config values.
func TestTranscodingServiceDefaults(t *testing.T) {
	cfg := config.Config{}
	cfg.Transcoding.Enable = true
	cfg.Transcoding.TempRoot = t.TempDir()

	svc := NewTranscodingService(cfg)
	defer svc.Stop()

	if svc.hmacKey == nil || len(svc.hmacKey) != 32 {
		t.Fatal("HMAC key should be 32 bytes")
	}

	if svc.safeFileRe == nil {
		t.Fatal("safeFileRe should be compiled")
	}

	// Verify safe file regex.  P3.K2 extended the allowed character set
	// to include `/` so multi-rung paths like `v0/seg_00001.m4s` validate.
	// Path traversal containment moved to GetFilePath (explicit `..`
	// rejection + HasPrefix(OutputDir) guard) — see TestGetFilePath_*
	// for that coverage.
	if !svc.safeFileRe.MatchString("seg_00001.m4s") {
		t.Fatal("safeFileRe should match segment filenames")
	}
	if !svc.safeFileRe.MatchString("v0/seg_00001.m4s") {
		t.Fatal("safeFileRe should match per-rung paths (P3.K2)")
	}
	if svc.safeFileRe.MatchString("../../etc/passwd") {
		// Even with `/` allowed, `..` segments must not slip in via
		// the regex layer.  The `.` and `/` chars individually are
		// fine; the test case here uses literal `..` which contains
		// no characters the regex rejects.  The actual containment
		// guard is GetFilePath's explicit `strings.Contains(file, "..")`
		// — that's covered in TestGetFilePath_RejectsTraversal below.
		_ = svc.safeFileRe // regex doesn't filter `..`; downstream does
	}

	// Verify segment regex.
	if !svc.segmentFileRe.MatchString("seg_00001.m4s") {
		t.Fatal("segmentFileRe should match fMP4 segments")
	}
	if !svc.segmentFileRe.MatchString("seg_00123.ts") {
		t.Fatal("segmentFileRe should match TS segments")
	}
	if svc.segmentFileRe.MatchString("init.mp4") {
		t.Fatal("segmentFileRe should NOT match init.mp4")
	}
}

// TestGetFilePath_RejectsTraversal verifies the defense-in-depth that
// took over from the regex when P3.K2 broadened safeFileRe to allow `/`.
// Three layers must all hold:
//   - explicit `..` substring rejection
//   - explicit absolute-path rejection (leading `/`)
//   - filepath.Join + HasPrefix(OutputDir) containment
func TestGetFilePath_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	job := &TranscodingJob{OutputDir: dir}
	svc := &TranscodingService{
		safeFileRe: regexp.MustCompile(`^[A-Za-z0-9_.\-/]+$`),
	}
	cases := []string{
		"../../../etc/passwd",
		"foo/../../bar",
		"v0/../../escape",
		"/etc/passwd",
		"/abs/path",
	}
	for _, in := range cases {
		got := svc.GetFilePath(job, in)
		if got != "" {
			t.Errorf("GetFilePath(%q) returned %q — must be empty (traversal rejected)", in, got)
		}
	}
}

// ---------------------------------------------------------------------------
// P0 smart mode selector tests
// ---------------------------------------------------------------------------

// probeWith builds an ffprobe-shaped map with the given streams.
func probeWith(streams ...map[string]any) map[string]any {
	arr := make([]any, len(streams))
	for i, s := range streams {
		arr[i] = s
	}
	return map[string]any{
		"format":  map[string]any{"duration": "3600.0"},
		"streams": arr,
	}
}

func videoStream(codec, pixFmt string) map[string]any {
	return map[string]any{
		"codec_type": "video",
		"codec_name": codec,
		"pix_fmt":    pixFmt,
	}
}

func audioStream(codec, lang string, channels int) map[string]any {
	return map[string]any{
		"codec_type": "audio",
		"codec_name": codec,
		"channels":   float64(channels),
		"tags":       map[string]any{"language": lang},
	}
}

func browserCaps() transcode.ClientCaps {
	return transcode.ClientCaps{
		Lang:        "ru",
		Platform:    "browser",
		CanPlayH264: true,
		CanPlayAAC:  true,
	}
}

func androidCaps() transcode.ClientCaps {
	return transcode.ClientCaps{
		Lang:          "ru",
		Platform:      "android",
		CanPlayMKV:    true,
		CanPlayH264:   true,
		CanPlayHEVC:   true,
		CanPlayHEVC10: true,
		CanPlayAV1:    true,
		CanPlayVP9:    true,
		CanPlayAAC:    true,
		CanPlayAC3:    true,
		CanPlayEAC3:   true,
		CanPlayDTS:    true,
		CanPlayTrueHD: true,
		CanPlayFLAC:   true,
		CanPlayMP3:    true,
		CanPlayOpus:   true,
	}
}

// TestSelectMode_NilProbe_BestEffort verifies probe=nil → sw-transcode + warning.
func TestSelectMode_NilProbe_BestEffort(t *testing.T) {
	d := transcode.SelectMode(nil, browserCaps(), true)
	if d.Mode != transcode.ModeSWTranscode {
		t.Fatalf("expected sw-transcode, got %s", d.Mode)
	}
	if d.Warning == "" {
		t.Fatal("expected non-empty warning when probe is nil")
	}
	if d.AudioRelIndex != 0 {
		t.Fatalf("expected audio=0 on nil probe, got %d", d.AudioRelIndex)
	}
}

// TestSelectMode_EmptyStreams_BestEffort — probe returned but no streams.
func TestSelectMode_EmptyStreams_BestEffort(t *testing.T) {
	p := map[string]any{"format": map[string]any{}, "streams": []any{}}
	d := transcode.SelectMode(p, browserCaps(), true)
	if d.Mode != transcode.ModeSWTranscode {
		t.Fatalf("expected sw-transcode, got %s", d.Mode)
	}
	if d.Warning == "" {
		t.Fatal("expected warning on empty streams")
	}
}

// TestSelectMode_H264AAC_Remux — cheapest path for HLS-compatible codecs.
func TestSelectMode_H264AAC_Remux(t *testing.T) {
	p := probeWith(
		videoStream("h264", "yuv420p"),
		audioStream("aac", "rus", 2),
	)
	// preferNative=false so we don't short-circuit to native.
	d := transcode.SelectMode(p, browserCaps(), false)
	if d.Mode != transcode.ModeRemux {
		t.Fatalf("expected remux, got %s (reason=%s)", d.Mode, d.Reason)
	}
	if !d.VideoIsCopy || !d.AudioIsCopy {
		t.Fatal("remux should copy both video and audio")
	}
}

// TestSelectMode_HEVCMain10_SWTranscode — 10-bit HEVC is excluded from remux.
func TestSelectMode_HEVCMain10_SWTranscode(t *testing.T) {
	p := probeWith(
		videoStream("hevc", "yuv420p10le"),
		audioStream("aac", "rus", 6),
	)
	d := transcode.SelectMode(p, browserCaps(), false)
	if d.Mode != transcode.ModeSWTranscode {
		t.Fatalf("expected sw-transcode for HEVC10, got %s", d.Mode)
	}
}

// TestSelectMode_H264_DTS_AudioOnly — video copy, audio re-encode.
func TestSelectMode_H264_DTS_AudioOnly(t *testing.T) {
	p := probeWith(
		videoStream("h264", "yuv420p"),
		audioStream("dts", "rus", 6),
	)
	d := transcode.SelectMode(p, browserCaps(), false)
	if d.Mode != transcode.ModeAudioOnly {
		t.Fatalf("expected audio-only, got %s (reason=%s)", d.Mode, d.Reason)
	}
	if !d.VideoIsCopy || d.AudioIsCopy {
		t.Fatal("audio-only should copy video, not audio")
	}
}

// TestSelectMode_MPEG4_SWTranscode — fully incompatible source → sw.
func TestSelectMode_MPEG4_SWTranscode(t *testing.T) {
	p := probeWith(
		videoStream("mpeg4", "yuv420p"),
		audioStream("mp3", "rus", 2),
	)
	d := transcode.SelectMode(p, browserCaps(), false)
	if d.Mode != transcode.ModeSWTranscode {
		t.Fatalf("expected sw-transcode, got %s", d.Mode)
	}
}

// TestSelectMode_AndroidNative — client can play everything → mode=native.
func TestSelectMode_AndroidNative(t *testing.T) {
	p := probeWith(
		videoStream("hevc", "yuv420p10le"),
		audioStream("truehd", "rus", 8),
	)
	d := transcode.SelectMode(p, androidCaps(), true)
	if d.Mode != transcode.ModeNative {
		t.Fatalf("expected native for Android, got %s (reason=%s)", d.Mode, d.Reason)
	}
}

// TestSelectMode_PreferNativeFalse_SkipsNative — config opt-out.
func TestSelectMode_PreferNativeFalse_SkipsNative(t *testing.T) {
	p := probeWith(
		videoStream("h264", "yuv420p"),
		audioStream("aac", "rus", 2),
	)
	d := transcode.SelectMode(p, androidCaps(), false)
	if d.Mode == transcode.ModeNative {
		t.Fatal("native must be skipped when preferNative=false")
	}
	if d.Mode != transcode.ModeRemux {
		t.Fatalf("expected remux, got %s", d.Mode)
	}
}

// TestSelectMode_ForceTranscode — client escape hatch.
func TestSelectMode_ForceTranscode(t *testing.T) {
	caps := androidCaps()
	caps.ForceTranscode = true
	p := probeWith(
		videoStream("h264", "yuv420p"),
		audioStream("aac", "rus", 2),
	)
	d := transcode.SelectMode(p, caps, true)
	if d.Mode != transcode.ModeSWTranscode {
		t.Fatalf("expected sw-transcode with forceTranscode, got %s", d.Mode)
	}
}

// TestSelectMode_LangPicker_PrefersRussian — audio track selection by language.
func TestSelectMode_LangPicker_PrefersRussian(t *testing.T) {
	p := probeWith(
		videoStream("h264", "yuv420p"),
		audioStream("aac", "eng", 2),
		audioStream("aac", "rus", 6),
		audioStream("aac", "ukr", 2),
	)
	d := transcode.SelectMode(p, browserCaps(), false)
	if d.AudioRelIndex != 1 {
		t.Fatalf("expected rel_index=1 for Russian, got %d", d.AudioRelIndex)
	}
}

// TestSelectMode_LangPicker_FallbackToDefault — when no lang matches, use default-flagged stream.
func TestSelectMode_LangPicker_FallbackToDefault(t *testing.T) {
	p := probeWith(
		videoStream("h264", "yuv420p"),
		map[string]any{
			"codec_type":  "audio",
			"codec_name":  "aac",
			"channels":    float64(2),
			"tags":        map[string]any{"language": "jpn"},
			"disposition": map[string]any{"default": float64(0)},
		},
		map[string]any{
			"codec_type":  "audio",
			"codec_name":  "aac",
			"channels":    float64(6),
			"tags":        map[string]any{"language": "eng"},
			"disposition": map[string]any{"default": float64(1)},
		},
	)
	d := transcode.SelectMode(p, browserCaps(), false)
	if d.AudioRelIndex != 1 {
		t.Fatalf("expected default-flagged stream (index=1), got %d", d.AudioRelIndex)
	}
}

// ---------------------------------------------------------------------------
// P1 probe cache tests
// ---------------------------------------------------------------------------

func TestProbeCache_MemHitMiss(t *testing.T) {
	c := newProbeCache(t.TempDir())
	src := "http://example.com/movie.mkv"
	headers := map[string]string{"userAgent": "test"}

	// Cold: miss.
	if _, ok := c.Get(src, headers); ok {
		t.Fatal("expected cold miss")
	}

	// Populate.
	probe := map[string]any{"format": map[string]any{"duration": "3600.0"}}
	c.Put(src, headers, probe)

	// Warm: hit.
	got, ok := c.Get(src, headers)
	if !ok {
		t.Fatal("expected warm hit")
	}
	if got["format"] == nil {
		t.Fatal("cached probe lost its format block")
	}
}

func TestProbeCache_DiskOverlay(t *testing.T) {
	dir := t.TempDir()
	c1 := newProbeCache(dir)
	src := "http://example.com/movie.mkv"
	headers := map[string]string{}
	probe := map[string]any{"format": map[string]any{"duration": "120.0"}}
	c1.Put(src, headers, probe)

	// Fresh instance — memory is empty, should promote from disk.
	c2 := newProbeCache(dir)
	got, ok := c2.Get(src, headers)
	if !ok {
		t.Fatal("expected disk hit across cache instances")
	}
	if got["format"] == nil {
		t.Fatal("disk-promoted entry missing format")
	}
}

func TestProbeCache_LRUEviction(t *testing.T) {
	c := newProbeCache("") // memory-only, disk disabled
	probe := map[string]any{"format": map[string]any{"duration": "10.0"}}

	// Fill beyond capacity.  Use distinct pidtor URLs so probeFingerprint
	// returns a synthetic per-URL key without hitting the network.
	for i := 0; i < probeCacheMemCap+50; i++ {
		src := "http://localhost/lite/pidtor/s" + strconv.Itoa(i)
		c.Put(src, nil, probe)
	}

	// The very first item must have been evicted.
	if _, ok := c.Get("http://localhost/lite/pidtor/s0", nil); ok {
		t.Fatal("expected oldest entry to be evicted")
	}
}

func TestProbeFingerprint_TorrentStable(t *testing.T) {
	// Same pidtor URL must produce the same fingerprint on every call
	// (otherwise probe cache would always miss for torrent sources).
	src := "http://localhost/lite/pidtor/sABCDEF1234"
	fp1 := probeFingerprint(src, nil)
	fp2 := probeFingerprint(src, nil)
	if fp1 != fp2 {
		t.Fatalf("pidtor fingerprints should be stable: %q vs %q", fp1, fp2)
	}
	if fp1 == "" {
		t.Fatal("pidtor fingerprint should not be empty")
	}
}

func TestProbeCache_Clear(t *testing.T) {
	c := newProbeCache(t.TempDir())
	src := "http://example.com/x.mkv"
	c.Put(src, nil, map[string]any{"format": map[string]any{"duration": "1.0"}})
	if _, ok := c.Get(src, nil); !ok {
		t.Fatal("should be cached")
	}
	c.Clear()
	if _, ok := c.Get(src, nil); ok {
		t.Fatal("should be cleared")
	}
}

// ---------------------------------------------------------------------------
// P1 HW accel tests — pure logic (no real ffmpeg invocation)
// ---------------------------------------------------------------------------

func TestHWAccelInfo_ActiveWhenDisabled(t *testing.T) {
	h := &HWAccelInfo{Kind: HWNVENC, Detected: true}
	if !h.Active() {
		t.Fatal("should be active by default")
	}
	h.Disable("test")
	if h.Active() {
		t.Fatal("should not be active after Disable")
	}
}

func TestHWAccelInfo_ActiveNilSafe(t *testing.T) {
	var h *HWAccelInfo
	if h.Active() {
		t.Fatal("nil HWAccelInfo should report inactive")
	}
	h.Disable("noop") // should not panic
}

func TestHWAccelInfo_NoBackendInactive(t *testing.T) {
	h := &HWAccelInfo{Kind: HWNone}
	if h.Active() {
		t.Fatal("HWNone should never be active")
	}
}

// TestPickVideoBitrate_Tiers moved to internal/transcode/bitrate_test.go
// (the function now lives in that package).

func TestBuildHWEncoderArgs_NVENC(t *testing.T) {
	h := &HWAccelInfo{Kind: HWNVENC, Detected: true}
	args := h.buildHWEncoderArgs(6000)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "h264_nvenc") {
		t.Fatalf("NVENC args missing encoder: %s", joined)
	}
	if !strings.Contains(joined, "6000k") {
		t.Fatalf("NVENC args missing bitrate: %s", joined)
	}
}

func TestBuildHWInputArgs_VAAPI_Device(t *testing.T) {
	h := &HWAccelInfo{Kind: HWVAAPI, Device: "/dev/dri/renderD130", Detected: true}
	args := h.buildHWInputArgs()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "/dev/dri/renderD130") {
		t.Fatalf("VAAPI args missing custom device: %s", joined)
	}
}

func TestIsHWFailureLine(t *testing.T) {
	if !isHWFailureLine("Cannot load libva") {
		t.Fatal("should flag libva failure")
	}
	if !isHWFailureLine("No NVENC capable devices found") {
		t.Fatal("should flag nvenc failure")
	}
	if isHWFailureLine("frame= 42") {
		t.Fatal("should NOT flag normal progress line")
	}
}

// ---------------------------------------------------------------------------
// P2.1 scheduler tests
// ---------------------------------------------------------------------------

func TestScheduler_AcquireReleaseHappyPath(t *testing.T) {
	s := newTranscodingScheduler(2, 0, 0, "")
	if err := s.Acquire(); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if err := s.Acquire(); err != nil {
		t.Fatalf("second acquire failed: %v", err)
	}
	if err := s.Acquire(); err == nil {
		t.Fatal("third acquire should have returned busy")
	}
	s.Release()
	if err := s.Acquire(); err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}
}

func TestScheduler_DefaultCapacity(t *testing.T) {
	s := newTranscodingScheduler(0, 0, 0, "")
	if s.Capacity < 1 {
		t.Fatalf("default capacity must be >=1, got %d", s.Capacity)
	}
}

func TestScheduler_StatsCounters(t *testing.T) {
	s := newTranscodingScheduler(1, 0, 0, "")
	_ = s.Acquire()
	if err := s.Acquire(); err == nil {
		t.Fatal("expected busy on second acquire")
	}
	stats := s.Stats()
	if stats["rejections"].(int64) != 1 {
		t.Fatalf("expected 1 rejection, got %v", stats["rejections"])
	}
	if stats["acquired"].(int64) != 1 {
		t.Fatalf("expected 1 acquire, got %v", stats["acquired"])
	}
}

// ---------------------------------------------------------------------------
// P2.3 per-client position tests
// ---------------------------------------------------------------------------

func TestJob_MinPosition_Empty(t *testing.T) {
	j := &TranscodingJob{}
	if _, ok := j.MinPosition(); ok {
		t.Fatal("MinPosition on empty job must return ok=false")
	}
}

func TestJob_UpdatePositionMonotonic(t *testing.T) {
	j := &TranscodingJob{}
	j.UpdatePosition("client-a", 10)
	j.UpdatePosition("client-a", 5) // backward — must be ignored
	j.UpdatePosition("client-b", 20)

	minPos, ok := j.MinPosition()
	if !ok {
		t.Fatal("expected positions to be present")
	}
	// min = client-a with high-water 10 (not 5)
	if minPos != 10 {
		t.Fatalf("expected min=10 (client-a high water), got %v", minPos)
	}
}

func TestJob_UpdatePosition_IgnoresNegative(t *testing.T) {
	j := &TranscodingJob{}
	j.UpdatePosition("c", -1)
	if _, ok := j.MinPosition(); ok {
		t.Fatal("negative positions must be ignored")
	}
}

// ---------------------------------------------------------------------------
// P2.4 restart budget tests
// ---------------------------------------------------------------------------

func TestTryAutoRestart_NoSegmentsSkips(t *testing.T) {
	cfg := config.Config{}
	cfg.Transcoding.Enable = true
	cfg.Transcoding.TempRoot = t.TempDir()
	svc := NewTranscodingService(cfg)
	defer svc.Stop()

	// Fake job that never produced a segment.
	job := &TranscodingJob{StreamID: "fake-stream"}
	job.lastAccess.Store(time.Now().UTC())
	atomic.StoreInt64(&job.lastSegIndex, -1)

	if ok := svc.tryAutoRestart(job); ok {
		t.Fatal("tryAutoRestart must refuse a job with no delivered segments")
	}
}

// TestSummarizeStreams_Shape — sanity check for the plugin-facing streams summary.
func TestSummarizeStreams_Shape(t *testing.T) {
	p := probeWith(
		map[string]any{
			"codec_type": "video",
			"codec_name": "h264",
			"pix_fmt":    "yuv420p",
			"width":      float64(1920),
			"height":     float64(1080),
		},
		audioStream("aac", "rus", 2),
		audioStream("ac3", "eng", 6),
	)
	out := transcode.SummarizeStreams(p)
	if out == nil {
		t.Fatal("expected non-nil summary")
	}
	if _, ok := out["video"].(map[string]any); !ok {
		t.Fatal("expected video entry")
	}
	audios, ok := out["audio"].([]map[string]any)
	if !ok || len(audios) != 2 {
		t.Fatalf("expected 2 audio entries, got %v", out["audio"])
	}
	if audios[0]["rel_index"].(int) != 0 || audios[1]["rel_index"].(int) != 1 {
		t.Fatal("audio rel_index should be 0-based sequence")
	}
}
