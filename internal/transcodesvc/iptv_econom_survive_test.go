package transcodesvc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/transcode"

	"github.com/go-chi/chi/v5"
)

// TestEconomJobSurvivesStartup proves the fix for «Эконом job dies at ~11–20s»: a client's
// live.m3u8 request blocks up to 60s waiting for the first fmp4 segment (which lands ~11s in),
// and the idle-live watchdog (20s floor) used to reap the job — deleting its output dir out from
// under the live ffmpeg — because the handler only Touch'd once at entry. The handler now
// re-Touches every poll, so an actively-waiting viewer keeps the job warm.
//
// The test drives the REAL transcodingLiveHandler through a chi router against a live re-encode of
// a locally-generated 40s source, then keeps refreshing the live playlist (as a real HLS player
// does) and asserts the job is still alive well past the 20s idle floor.
func TestEconomJobSurvivesStartup(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "src.ts")
	gen := exec.Command(ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=1920x1080:rate=25:duration=40",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=40",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", srcFile)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("gen: %v\n%s", err, out)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(srcDir)))
	defer srv.Close()

	cfg := config.Config{}
	cfg.Transcoding.Enable = true
	cfg.Transcoding.FFmpeg = "ffmpeg"
	cfg.Transcoding.TempRoot = t.TempDir()
	cfg.Transcoding.MaxConcurrent = 10
	cfg.Transcoding.DisableHWAccel = true
	cfg.Transcoding.HLS.FMP4 = true
	cfg.Transcoding.Playlist.DeleteSegments = true
	cfg.Transcoding.IdleTimeoutLive = 20 // the default floor — the window that used to reap the job

	svc := NewTranscodingService(cfg)

	job, errMsg := svc.Start(&TranscodingStartRequest{
		Src:       srv.URL + "/src.ts",
		Live:      true,
		MaxHeight: 720,
		Client:    &transcode.ClientCaps{ForceTranscode: true, Platform: "browser"},
	})
	if job == nil {
		t.Fatalf("Start nil: %s", errMsg)
	}
	defer func() { svc.stopJob(job, true); _ = os.RemoveAll(job.OutputDir) }()
	t.Logf("started streamId=%s", job.StreamID)

	router := chi.NewRouter()
	router.Get("/transcoding/{streamId}/live.m3u8", transcodingLiveHandler(cfg, svc))
	ts := httptest.NewServer(router)
	defer ts.Close()

	fetchLive := func() (int, string) {
		resp, err := http.Get(ts.URL + "/transcoding/" + job.StreamID + "/live.m3u8")
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		return resp.StatusCode, string(buf[:n])
	}

	// First fetch blocks until the first segment (~11s). With the fix, the handler re-Touches
	// throughout, so the idle watchdog can't reap the job mid-wait.
	t0 := time.Now()
	code, body := fetchLive()
	t.Logf("first live.m3u8: HTTP %d after %.1fs (%d bytes)", code, time.Since(t0).Seconds(), len(body))
	if code != http.StatusOK {
		dumpDir(t, job.OutputDir)
		t.Fatalf("live.m3u8 returned HTTP %d (want 200) — job was reaped during startup wait (the bug)", code)
	}

	// Now behave like a live player: refresh the playlist every 2s. Run WELL past the 20s idle
	// floor to prove the job stays alive under a real viewer.
	deadline := time.Now().Add(24 * time.Second) // cross the 20s idle floor in absolute wall time under a live viewer
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if job.HasExited() {
			t.Fatalf("job exited at %.1fs — idle watchdog reaped a watched live job (fix regressed)", time.Since(t0).Seconds())
		}
		code, _ := fetchLive()
		if code != http.StatusOK {
			t.Fatalf("live.m3u8 HTTP %d at %.1fs — job dir gone under an active viewer", code, time.Since(t0).Seconds())
		}
	}
	t.Logf("SUCCESS: job alive at %.1fs (well past the 20s idle floor) under a live viewer", time.Since(t0).Seconds())
}
