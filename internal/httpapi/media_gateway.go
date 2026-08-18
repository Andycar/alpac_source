package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/transcode"
	"lampac-go/internal/transcodesvc"
)

// ---------------------------------------------------------------------------
// Media Gateway — admin-side glue for the existing transcodesvc.TranscodingService.
//
// The transcoding service itself is mature: it spawns ffmpeg, manages HLS
// segments, picks codecs, runs probe-cache lookups. What it lacks is a
// unified admin view: list active jobs, surface probe results on demand,
// and a "wrap any URL through HLS" entry point for clients that can't play
// raw MP4 / fMP4 / SAMPLE-AES.
//
// This file adds three orthogonal capabilities, each behind a separate
// admin endpoint:
//
//   GET /admin/api/media/probe?url=...   — transcodesvc.RunFFProbeStandalone wrapper
//   GET /admin/api/media/jobs            — snapshot of running transcoding jobs
//   POST /admin/api/media/jobs/kill      — terminate a job by ID
//
// All endpoints reuse existing infrastructure (no new ffmpeg processes for
// probe — transcoding's own probe cache is hit on duplicates).
// ---------------------------------------------------------------------------

// tgAdminMediaProbeHandler runs ffprobe against a user-supplied URL and
// returns the parsed JSON. Only admins can call this — output may include
// upstream auth headers etc, and the cost is non-trivial (one ffprobe spawn
// per call, capped to 10s by the underlying helper).
func tgAdminMediaProbeHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		raw := strings.TrimSpace(r.URL.Query().Get("url"))
		if raw == "" {
			http.Error(w, "missing 'url' parameter", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			http.Error(w, "url must start with http:// or https://", http.StatusBadRequest)
			return
		}
		if !serverReady() {
			http.Error(w, "server not ready", http.StatusServiceUnavailable)
			return
		}
		cfg := liveConfig(config.Config{})
		host := r.Host
		started := time.Now()
		probeData, errMsg := transcodesvc.RunFFProbeStandalone(cfg, raw, host)
		took := time.Since(started)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := stdjson.NewEncoder(w)
		_ = enc.Encode(map[string]any{
			"ok":         errMsg == "",
			"error":      errMsg,
			"probe":      probeData,
			"summary":    transcode.SummarizeStreams(probeData),
			"took_ms":    took.Milliseconds(),
			"target_url": raw,
		})
	}
}

// tgAdminMediaJobsHandler returns a snapshot of all currently tracked
// transcoding jobs.
func tgAdminMediaJobsHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		ts := liveTransSvc()
		if ts == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"enabled": false,
				"jobs":    []any{},
			})
			return
		}
		summaries := transcodesvc.SnapshotTranscodingJobs(ts)
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": true,
			"jobs":    summaries,
			"now":     time.Now(),
		})
	}
}

// tgAdminMediaKillHandler terminates a single job by ID.
func tgAdminMediaKillHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		jobID := strings.TrimSpace(r.URL.Query().Get("id"))
		if jobID == "" {
			http.Error(w, "missing 'id' parameter", http.StatusBadRequest)
			return
		}
		ts := liveTransSvc()
		if ts == nil {
			http.Error(w, "transcoding service unavailable", http.StatusServiceUnavailable)
			return
		}
		killed := transcodesvc.KillTranscodingJobByID(ts, jobID)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     killed,
			"job_id": jobID,
		})
	}
}
