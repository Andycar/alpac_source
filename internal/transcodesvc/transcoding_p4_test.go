package transcodesvc

import (
	"lampac-go/internal/transcode"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"

	"lampac-go/internal/config"
)

// vfValue returns the value of the first -vf arg, or "".
func vfValue(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-vf" {
			return args[i+1]
		}
	}
	return ""
}

func probeWH(w, h int) map[string]any {
	return map[string]any{"streams": []any{
		map[string]any{"codec_type": "video", "codec_name": "h264", "width": float64(w), "height": float64(h)},
	}}
}

func TestApplyTranscodeDownscale(t *testing.T) {
	svc := &TranscodingService{cfg: config.Config{Transcoding: config.TranscodingConf{MaxTranscodeHeight: 1080}}}
	ctx4k := transcodingContext{FFProbe: probeWH(3840, 2160)}

	// HDR-tonemap path: existing -vf scale without dimensions → splice -2:1080.
	in := []string{"-vf", "scale=in_color_matrix=bt2020nc:out_color_matrix=bt709:in_range=pc:out_range=tv,format=yuv420p", "-c:v", "libx264"}
	got := vfValue(svc.applyTranscodeDownscale(in, ctx4k))
	want := "scale=-2:1080:in_color_matrix=bt2020nc:out_color_matrix=bt709:in_range=pc:out_range=tv,format=yuv420p"
	if got != want {
		t.Fatalf("tonemap downscale:\n got %q\nwant %q", got, want)
	}

	// Plain libx264, no -vf → add one.
	plain := []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "23"}
	if got := vfValue(svc.applyTranscodeDownscale(plain, ctx4k)); got != "scale=-2:1080" {
		t.Fatalf("plain downscale: got -vf %q, want scale=-2:1080", got)
	}

	// Stream-copy → no -vf injected.
	if got := vfValue(svc.applyTranscodeDownscale([]string{"-c:v", "copy"}, ctx4k)); got != "" {
		t.Fatalf("copy must not get -vf, got %q", got)
	}

	// Source already <= cap → no-op.
	if got := vfValue(svc.applyTranscodeDownscale(plain, transcodingContext{FFProbe: probeWH(1920, 1080)})); got != "" {
		t.Fatalf("1080p source must not be scaled, got %q", got)
	}

	// Cap disabled → no-op.
	svc0 := &TranscodingService{cfg: config.Config{Transcoding: config.TranscodingConf{MaxTranscodeHeight: 0}}}
	if got := vfValue(svc0.applyTranscodeDownscale(plain, ctx4k)); got != "" {
		t.Fatalf("cap=0 must not scale, got %q", got)
	}

	// Burn-in (-filter_complex) owns the graph → don't add -vf.
	fc := []string{"-filter_complex", "[0:v]subtitles=...[vout]", "-c:v", "libx264"}
	if got := vfValue(svc.applyTranscodeDownscale(fc, ctx4k)); got != "" {
		t.Fatalf("filter_complex present must not add -vf, got %q", got)
	}

	// Already-dimensioned scale → left alone.
	dim := []string{"-vf", "scale=1280:-2", "-c:v", "libx264"}
	if got := vfValue(svc.applyTranscodeDownscale(dim, ctx4k)); got != "scale=1280:-2" {
		t.Fatalf("dimensioned scale must be untouched, got %q", got)
	}
}

func TestOmnivorousPlayersGoNative(t *testing.T) {
	// 4K HDR HEVC + DTS source — the worst case for a CPU. A SW player must
	// still get it natively (zero ffmpeg), not a 4K transcode.
	probe := map[string]any{"streams": []any{
		map[string]any{"codec_type": "video", "codec_name": "hevc", "pix_fmt": "yuv420p10le", "width": float64(3840), "height": float64(2160)},
		map[string]any{"codec_type": "audio", "codec_name": "dts"},
	}}

	for _, ua := range []string{
		"VLC/3.0.22-rc1 LibVLC/3.0.22-rc1",
		"Kodi/20.2 (X11; Linux x86_64) App_Bitness/64",
		"libmpv 0.35.0",
		"Infuse/7.6 (com.firecore.infuse)",
	} {
		caps, label, _ := transcode.EnrichCapsFromUA(nil, ua)
		if !caps.IsOmnivorous() {
			t.Fatalf("%q: caps not omnivorous (label=%q)", ua, label)
		}
		if dec := transcode.SelectMode(probe, caps, true); dec.Mode != transcode.ModeNative {
			t.Fatalf("%q: mode=%s, want native", ua, dec.Mode)
		}
	}

	// Desktop Chrome must NOT be treated as omnivorous (it genuinely can't
	// play HEVC10/DTS in a <video> element).
	if c, _, _ := transcode.EnrichCapsFromUA(nil, "Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Chrome/120.0 Safari/537.36"); c.IsOmnivorous() {
		t.Fatalf("desktop Chrome should not be omnivorous")
	}
}

func TestInflightDedup(t *testing.T) {
	svc := &TranscodingService{
		jobs:     map[string]*TranscodingJob{},
		inflight: map[string]string{},
	}

	if svc.LookupInflightJob("") != nil || svc.LookupInflightJob("missing") != nil {
		t.Fatal("empty/missing keys must return nil")
	}

	// A real, still-running process so HasExited() is false.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	job := &TranscodingJob{ID: "job1", StreamID: "job1.sig", Cmd: cmd, exitCode: -1}
	atomic.StoreInt64(&job.lastSegIndex, -1) // warming up, no segment served
	svc.mu.Lock()
	svc.jobs["job1"] = job
	svc.mu.Unlock()

	svc.RegisterInflightJob("k1", job)

	// Warming up → reused (this is what collapses the retry storm).
	if got := svc.LookupInflightJob("k1"); got != job {
		t.Fatal("warming-up job should be reused")
	}

	// Once it has served a segment → NOT reused (independent viewers get
	// their own job and never collide on seek).
	job.UpdateLastSegmentIndex(3)
	if got := svc.LookupInflightJob("k1"); got != nil {
		t.Fatal("streaming job must not be reused")
	}

	// dropInflight removes the entry.
	svc.dropInflight(job)
	svc.inflightMu.Lock()
	_, present := svc.inflight["k1"]
	svc.inflightMu.Unlock()
	if present {
		t.Fatal("dropInflight should remove the coalescing entry")
	}

	// Stale entry (jobID no longer in jobs map) → returns nil AND self-heals.
	svc.inflightMu.Lock()
	svc.inflight["ghost"] = "no-such-job"
	svc.inflightMu.Unlock()
	if svc.LookupInflightJob("ghost") != nil {
		t.Fatal("stale entry must return nil")
	}
	svc.inflightMu.Lock()
	_, stillThere := svc.inflight["ghost"]
	svc.inflightMu.Unlock()
	if stillThere {
		t.Fatal("stale entry should be cleaned up on lookup")
	}
}

func TestWindowAction(t *testing.T) {
	const high, low = 22, 12
	cases := []struct {
		name          string
		lookahead     int
		paused        bool
		pause, resume bool
	}{
		{"running, far behind window", 5, false, false, false},
		{"running, at high → pause", 22, false, true, false},
		{"running, past high → pause", 30, false, true, false},
		{"paused, still buffered → stay", 18, true, false, false},
		{"paused, drained to low → resume", 12, true, false, true},
		{"paused, drained below low → resume", 8, true, false, true},
	}
	for _, c := range cases {
		p, r := windowAction(c.lookahead, high, low, c.paused)
		if p != c.pause || r != c.resume {
			t.Errorf("%s: got (pause=%v resume=%v), want (%v %v)", c.name, p, r, c.pause, c.resume)
		}
	}

	// high<=0 disables the pacer entirely.
	if p, r := windowAction(100, 0, 0, false); p || r {
		t.Fatalf("high=0 must disable; got pause=%v resume=%v", p, r)
	}
	if p, r := windowAction(0, 0, 0, true); p || r {
		t.Fatalf("high=0 must disable while paused; got pause=%v resume=%v", p, r)
	}
}

func TestHighestSegmentOnDisk(t *testing.T) {
	svc := &TranscodingService{segmentFileRe: regexp.MustCompile(`^seg_(\d+)\.(m4s|ts)$`)}
	dir := t.TempDir()
	job := &TranscodingJob{OutputDir: dir}

	// Empty dir → -1.
	if got := svc.highestSegmentOnDisk(job); got != -1 {
		t.Fatalf("empty dir: got %d, want -1", got)
	}

	for _, name := range []string{
		"init.mp4", "index.m3u8", "seg_00000.m4s", "seg_00005.m4s",
		"seg_00012.m4s", "subs_3.vtt", "notasegment.m4s",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := svc.highestSegmentOnDisk(job); got != 12 {
		t.Fatalf("got highest %d, want 12", got)
	}

	// Missing dir → -1 (e.g. multi-rung variants in subdirs).
	job2 := &TranscodingJob{OutputDir: filepath.Join(dir, "nope")}
	if got := svc.highestSegmentOnDisk(job2); got != -1 {
		t.Fatalf("missing dir: got %d, want -1", got)
	}
}
