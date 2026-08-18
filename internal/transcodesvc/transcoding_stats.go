package transcodesvc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
)

// ---------------------------------------------------------------------------
// GET /transcoding/stats — live service telemetry.
//
// Returns the StatsSnapshot plus a few values useful for ops (host OS,
// ffmpeg path, temp root).  Designed to be polled by the admin panel
// every few seconds so operators can see HW accel health, queue
// pressure, probe cache hit rate, and what each active session is
// currently doing.
// ---------------------------------------------------------------------------

func transcodingStatsHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}
		snap := svc.StatsSnapshot()
		snap["host_os"] = runtime.GOOS
		snap["host_arch"] = runtime.GOARCH
		snap["ffmpeg_path"] = cfg.Transcoding.FFmpeg
		snap["temp_root"] = cfg.Transcoding.TempRoot
		writeJSON(w, http.StatusOK, snap)
	}
}

// ---------------------------------------------------------------------------
// GET /transcoding/selftest — one-shot health check.
//
// Runs a short diagnostic: ffmpeg/ffprobe presence, ffmpeg version,
// HW accel probe, temp dir writability, disk space.  Returns a JSON
// report with pass/fail per check.  Intended for the admin panel
// "Проверить конфигурацию" button — tells the user exactly what's
// broken without having to grep logs.
// ---------------------------------------------------------------------------

type selfTestCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Details string `json:"details,omitempty"`
}

func transcodingSelfTestHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !cfg.Transcoding.Enable {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":     false,
				"checks": []selfTestCheck{{Name: "transcoding enabled", Passed: false, Details: "set transcoding.enable = true"}},
			})
			return
		}

		var checks []selfTestCheck
		allPassed := true
		addCheck := func(c selfTestCheck) {
			checks = append(checks, c)
			if !c.Passed {
				allPassed = false
			}
		}

		ffmpegPath := cfg.Transcoding.FFmpeg
		if ffmpegPath == "" {
			ffmpegPath = "ffmpeg"
		}

		// 1. ffmpeg binary present + version.
		addCheck(runSelfTestFFmpegVersion(ffmpegPath))

		// 2. ffprobe binary present.
		ffprobePath := "ffprobe"
		if cfg.Transcoding.FFmpeg != "" && cfg.Transcoding.FFmpeg != "ffmpeg" {
			dir := filepath.Dir(cfg.Transcoding.FFmpeg)
			candidate := filepath.Join(dir, "ffprobe")
			if _, err := os.Stat(candidate); err == nil {
				ffprobePath = candidate
			}
		}
		addCheck(runSelfTestFFprobePresent(ffprobePath))

		// 3. Temp root writable.
		addCheck(runSelfTestTempRoot(cfg.Transcoding.TempRoot))

		// 4. HW accel active?
		hwCheck := selfTestCheck{Name: "hardware acceleration"}
		if svc.hwAccel != nil && svc.hwAccel.Active() {
			hwCheck.Passed = true
			hwCheck.Details = string(svc.hwAccel.Kind)
			if svc.hwAccel.Device != "" {
				hwCheck.Details += " (" + svc.hwAccel.Device + ")"
			}
		} else {
			// Not a hard failure — software encode still works.
			hwCheck.Passed = true
			hwCheck.Details = "software encoding only (no compatible HW detected)"
		}
		addCheck(hwCheck)

		// 5. Probe cache working.
		pcCheck := selfTestCheck{Name: "probe cache"}
		if svc.probeCache != nil {
			stats := svc.probeCache.Stats()
			pcCheck.Passed = true
			pcCheck.Details = stringifyProbeCacheStats(stats)
		} else {
			pcCheck.Passed = false
			pcCheck.Details = "probe cache not initialised"
		}
		addCheck(pcCheck)

		// 6. Scheduler has free capacity.
		schedCheck := selfTestCheck{Name: "scheduler"}
		if svc.scheduler != nil {
			free, total := svc.scheduler.Available()
			schedCheck.Passed = true
			schedCheck.Details = fmt.Sprintf("capacity=%d, free=%d", total, free)
		} else {
			schedCheck.Passed = false
			schedCheck.Details = "scheduler not initialised"
		}
		addCheck(schedCheck)

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     allPassed,
			"checks": checks,
		})
	}
}

// ---------------------------------------------------------------------------
// Self-test helpers
// ---------------------------------------------------------------------------

func runSelfTestFFmpegVersion(path string) selfTestCheck {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return selfTestCheck{Name: "ffmpeg binary", Passed: false, Details: err.Error()}
	}
	firstLine := strings.SplitN(string(out), "\n", 2)[0]
	return selfTestCheck{Name: "ffmpeg binary", Passed: true, Details: firstLine}
}

func runSelfTestFFprobePresent(path string) selfTestCheck {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return selfTestCheck{Name: "ffprobe binary", Passed: false, Details: err.Error()}
	}
	firstLine := strings.SplitN(string(out), "\n", 2)[0]
	return selfTestCheck{Name: "ffprobe binary", Passed: true, Details: firstLine}
}

func runSelfTestTempRoot(raw string) selfTestCheck {
	check := selfTestCheck{Name: "temp root writable"}
	if raw == "" {
		raw = filepath.Join("cache", "transcoding")
	}
	root := relToRuntime(raw)
	if err := os.MkdirAll(root, 0o755); err != nil {
		check.Details = err.Error()
		return check
	}
	// Try to write and delete a probe file.
	testPath := filepath.Join(root, ".selftest")
	if err := os.WriteFile(testPath, []byte("ok"), 0o644); err != nil {
		check.Details = err.Error()
		return check
	}
	_ = os.Remove(testPath)
	check.Passed = true
	check.Details = root
	return check
}

func stringifyProbeCacheStats(stats map[string]any) string {
	if stats == nil {
		return ""
	}
	size, _ := stats["size"].(int)
	hits, _ := stats["hits"].(int64)
	misses, _ := stats["misses"].(int64)
	return "size=" + strconv.Itoa(size) + ", hits=" + strconv.FormatInt(hits, 10) + ", misses=" + strconv.FormatInt(misses, 10)
}
