package transcodesvc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/transcode"
)

// TestEconomLocalTranscode drives the REAL econom transcode path against a locally-generated
// video served over httptest — no network/geo/probe flakiness. It asserts the session produces
// a playable playlist + segments and that its streamID resolves (the two things whose absence is
// the user's live.m3u8 404). Skips without ffmpeg. Inspects the output dir to catch layout bugs
// (multi-rung subdirs / master-vs-media playlist collisions) that a URL-only check would miss.
func TestEconomLocalTranscode(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	// 1) Generate a ~12s H.264 test clip locally (1080p so the 480p downscale is exercised).
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "src.ts")
	gen := exec.Command(ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=1920x1080:rate=25:duration=12",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=12",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", srcFile)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("could not generate test clip: %v\n%s", err, out)
	}

	// 2) Serve it over HTTP so the transcoder fetches it exactly like a real (proxied) channel URL.
	srv := httptest.NewServer(http.FileServer(http.Dir(srcDir)))
	defer srv.Close()
	srcURL := srv.URL + "/src.ts"

	cfg := config.Config{}
	cfg.Transcoding.Enable = true
	cfg.Transcoding.FFmpeg = "ffmpeg"
	cfg.Transcoding.TempRoot = t.TempDir()
	cfg.Transcoding.MaxConcurrent = 2
	cfg.Transcoding.DisableHWAccel = true // SW libx264 — matches a typical Linux server

	svc := NewTranscodingService(cfg)

	job, errMsg := svc.Start(&TranscodingStartRequest{
		Src:       srcURL,
		Live:      true,
		MaxHeight: 480,
		Client:    &transcode.ClientCaps{ForceTranscode: true, Platform: "browser"},
	})
	if job == nil {
		t.Fatalf("econom Start returned nil job: %q", errMsg)
	}
	t.Logf("job: streamID=%s mode=%s dir=%s", job.StreamID, job.Mode, job.OutputDir)
	if job.Mode == transcode.ModeNative || job.Mode == transcode.ModeDirect {
		t.Fatalf("econom must re-encode, but Mode=%s", job.Mode)
	}

	// 3) The source is finite (12s), so ffmpeg exits and the job cleans up — SNAPSHOT index.m3u8's
	// content the moment it first appears (poll fast) so the cleanup race can't hide the result.
	index := filepath.Join(job.OutputDir, "index.m3u8")
	deadline := time.Now().Add(30 * time.Second)
	var snapshot string
	var segs int
	for time.Now().Before(deadline) {
		if b, e := os.ReadFile(index); e == nil && len(b) > 0 {
			snapshot = string(b)
			segs = countSegments(job.OutputDir)
			if segs >= 1 {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	if snapshot == "" {
		t.Errorf("econom produced no index.m3u8 — this is the user's live.m3u8 404")
		dumpDir(t, job.OutputDir)
		svc.stopJob(job, true)
		return
	}
	t.Logf("index.m3u8:\n%s", snapshot)
	// The playlist must be a MEDIA playlist referencing real segments — NOT a self-referential
	// master (the -master_pl_name collision bug: #EXT-X-STREAM-INF pointing back at index.m3u8).
	if strings.Contains(snapshot, "#EXT-X-STREAM-INF") {
		t.Errorf("index.m3u8 is a MASTER playlist (self-referential master bug not fixed)")
	}
	if !strings.Contains(snapshot, "#EXTINF") || !strings.Contains(snapshot, ".ts") {
		t.Errorf("index.m3u8 is not a playable media playlist (no #EXTINF/.ts)")
	}
	// The econom job must RE-ENCODE (libx264), not -c:v copy — else the bitrate isn't reduced.
	if !sliceContainsPair(job.Cmd.Args, "-c:v", "libx264") {
		t.Errorf("econom did NOT re-encode video (args lack -c:v libx264) — bitrate wouldn't drop; args=%v", job.Cmd.Args)
	} else {
		t.Logf("SUCCESS: valid media playlist, %d segments, video re-encoded (libx264)", segs)
	}

	svc.stopJob(job, true)
}

func countSegments(dir string) int {
	n := 0
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if ext := filepath.Ext(p); ext == ".ts" || ext == ".m4s" {
			n++
		}
		return nil
	})
	return n
}

func dumpDir(t *testing.T, dir string) {
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			t.Logf("  dir  %s/", rel)
		} else {
			info, _ := d.Info()
			t.Logf("  file %s (%d B)", rel, info.Size())
		}
		return nil
	})
}
