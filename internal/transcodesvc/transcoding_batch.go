package transcodesvc

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Batch Transcoding — pre-transcode multiple episodes at once.
//
// Flow:
// 1. Client POSTs /transcoding/batch with list of episode URLs
// 2. Server starts transcoding first N episodes concurrently
// 3. Returns batchId + master playlist URL
// 4. Master playlist is a multi-episode VOD with #EXT-X-DISCONTINUITY
// 5. As user watches, server progressively pre-transcodes next episodes
// 6. Single heartbeat keeps the whole batch alive
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// BatchEpisode describes one episode in a batch request.
type BatchEpisode struct {
	URL        string            `json:"url"`
	Title      string            `json:"title"`
	AudioIndex int               `json:"audioIndex"`
	Headers    map[string]string `json:"headers"`
}

// BatchStartRequest is the POST body for /transcoding/batch.
type BatchStartRequest struct {
	Episodes  []BatchEpisode `json:"episodes"`
	Subtitles bool           `json:"subtitles"`
	Live      bool           `json:"live"`
}

// batchEpisodeState tracks one episode within a batch.
type batchEpisodeState struct {
	Index     int    // 0-based index in the batch
	URL       string // original source URL
	Title     string
	StreamID  string // transcoding stream ID (after Start)
	State     string // "pending", "starting", "running", "ready", "error"
	Error     string
	Duration  int // seconds, from ffprobe
	SegCount  int // number of HLS segments
	AudioIdx  int
	Headers   map[string]string
	Subtitles bool
}

// BatchJob manages a group of episode transcoding jobs.
type BatchJob struct {
	ID        string
	Episodes  []batchEpisodeState
	CreatedAt time.Time
	mu        sync.RWMutex

	lastAccess time.Time
	svc        *TranscodingService
	cfg        config.Config
	stopCh     chan struct{}
	stopped    bool

	// How many episodes to pre-transcode ahead of current playback.
	preloadAhead int
	// Index of the episode currently being watched (0-based).
	currentIndex int
}

// ---------------------------------------------------------------------------
// BatchManager — singleton managing all batch jobs.
// ---------------------------------------------------------------------------

type BatchManager struct {
	mu   sync.RWMutex
	jobs map[string]*BatchJob
	svc  *TranscodingService
	cfg  config.Config
}

func NewBatchManager(svc *TranscodingService, cfg config.Config) *BatchManager {
	bm := &BatchManager{
		jobs: make(map[string]*BatchJob),
		svc:  svc,
		cfg:  cfg,
	}
	go bm.reapLoop()
	return bm
}

// reapLoop removes idle batch jobs (no heartbeat for 2 minutes).
func (bm *BatchManager) reapLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		bm.mu.Lock()
		now := time.Now()
		for id, job := range bm.jobs {
			job.mu.RLock()
			idle := now.Sub(job.lastAccess) > 2*time.Minute
			job.mu.RUnlock()
			if idle {
				log.Info().Str("batchId", id).Msg("batch: reaping idle batch")
				job.StopAll()
				delete(bm.jobs, id)
			}
		}
		bm.mu.Unlock()
	}
}

func (bm *BatchManager) Get(id string) *BatchJob {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.jobs[id]
}

func (bm *BatchManager) Create(req *BatchStartRequest) (*BatchJob, string) {
	if len(req.Episodes) == 0 {
		return nil, "No episodes provided"
	}

	id := newJobID()

	episodes := make([]batchEpisodeState, len(req.Episodes))
	for i, ep := range req.Episodes {
		episodes[i] = batchEpisodeState{
			Index:     i,
			URL:       ep.URL,
			Title:     ep.Title,
			State:     "pending",
			AudioIdx:  ep.AudioIndex,
			Headers:   ep.Headers,
			Subtitles: req.Subtitles,
		}
	}

	preload := 3
	if len(episodes) < preload {
		preload = len(episodes)
	}

	job := &BatchJob{
		ID:           id,
		Episodes:     episodes,
		CreatedAt:    time.Now(),
		lastAccess:   time.Now(),
		svc:          bm.svc,
		cfg:          bm.cfg,
		stopCh:       make(chan struct{}),
		preloadAhead: preload,
		currentIndex: 0,
	}

	bm.mu.Lock()
	bm.jobs[id] = job
	bm.mu.Unlock()

	// Start transcoding the first N episodes.
	go job.ensurePreloaded()

	return job, ""
}

func (bm *BatchManager) Remove(id string) {
	bm.mu.Lock()
	job, ok := bm.jobs[id]
	if ok {
		delete(bm.jobs, id)
	}
	bm.mu.Unlock()
	if ok {
		job.StopAll()
	}
}

// ---------------------------------------------------------------------------
// BatchJob methods
// ---------------------------------------------------------------------------

func (bj *BatchJob) Touch() {
	bj.mu.Lock()
	bj.lastAccess = time.Now()
	bj.mu.Unlock()
}

// SetCurrentEpisode advances the playback cursor and triggers pre-loading.
func (bj *BatchJob) SetCurrentEpisode(idx int) {
	bj.mu.Lock()
	if idx >= 0 && idx < len(bj.Episodes) {
		bj.currentIndex = idx
	}
	bj.mu.Unlock()
	go bj.ensurePreloaded()
}

// ensurePreloaded starts transcoding for episodes that should be pre-loaded
// based on current playback position.
func (bj *BatchJob) ensurePreloaded() {
	bj.mu.RLock()
	current := bj.currentIndex
	ahead := bj.preloadAhead
	total := len(bj.Episodes)
	bj.mu.RUnlock()

	end := current + ahead
	if end > total {
		end = total
	}

	for i := current; i < end; i++ {
		bj.mu.RLock()
		state := bj.Episodes[i].State
		bj.mu.RUnlock()

		if state != "pending" {
			continue
		}

		bj.startEpisode(i)
	}
}

// startEpisode starts transcoding for a single episode.
func (bj *BatchJob) startEpisode(idx int) {
	bj.mu.Lock()
	if idx < 0 || idx >= len(bj.Episodes) {
		bj.mu.Unlock()
		return
	}
	ep := &bj.Episodes[idx]
	if ep.State != "pending" {
		bj.mu.Unlock()
		return
	}
	ep.State = "starting"
	bj.mu.Unlock()

	log.Info().Int("episode", idx).Str("url", ep.URL[:min(len(ep.URL), 80)]).Msg("batch: starting episode transcoding")

	// Build request.
	req := &TranscodingStartRequest{
		Src:  ep.URL,
		Live: false,
	}
	if ep.AudioIdx > 0 {
		req.Audio = &reqAudioOpts{Index: ep.AudioIdx}
	}
	if ep.Subtitles {
		sub := true
		req.Subtitles = &sub
	}
	if len(ep.Headers) > 0 {
		req.Headers = ep.Headers
	}

	job, errMsg := bj.svc.Start(req)

	bj.mu.Lock()
	if job == nil {
		ep.State = "error"
		ep.Error = errMsg
		log.Warn().Int("episode", idx).Str("error", errMsg).Msg("batch: episode transcoding failed to start")
		bj.mu.Unlock()
		return
	}

	ep.StreamID = job.StreamID
	ep.State = "running"

	// Get duration from ffprobe.
	duration := ffprobeDuration(job.Context.FFProbe)
	ep.Duration = duration
	if duration > 0 {
		segDur := job.Context.HLS.SegDur
		if segDur <= 0 {
			segDur = 6
		}
		ep.SegCount = duration / segDur
	}
	bj.mu.Unlock()

	log.Info().Int("episode", idx).Str("streamId", job.StreamID).Int("duration", duration).Msg("batch: episode transcoding started")

	// Monitor for completion in background.
	go bj.monitorEpisode(idx, job)
}

// monitorEpisode watches an episode's transcoding job and marks it ready.
func (bj *BatchJob) monitorEpisode(idx int, job *TranscodingJob) {
	// Poll until ffmpeg has produced enough segments or exited.
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-bj.stopCh:
			return
		case <-ticker.C:
			if job.HasExited() {
				bj.mu.Lock()
				if bj.Episodes[idx].State == "running" {
					if job.ExitCode() == 0 {
						bj.Episodes[idx].State = "ready"
						log.Info().Int("episode", idx).Msg("batch: episode transcoding completed")
					} else {
						bj.Episodes[idx].State = "error"
						bj.Episodes[idx].Error = fmt.Sprintf("ffmpeg exit code %d", job.ExitCode())
						log.Warn().Int("episode", idx).Int("exitCode", job.ExitCode()).Msg("batch: episode transcoding failed")
					}
				}
				bj.mu.Unlock()
				return
			}
		}
	}
}

// StopAll stops all episode transcoding jobs in this batch.
func (bj *BatchJob) StopAll() {
	select {
	case <-bj.stopCh:
		return // already stopped
	default:
	}

	bj.mu.Lock()
	bj.stopped = true
	close(bj.stopCh)
	episodes := make([]batchEpisodeState, len(bj.Episodes))
	copy(episodes, bj.Episodes)
	bj.mu.Unlock()

	for _, ep := range episodes {
		if ep.StreamID != "" && (ep.State == "running" || ep.State == "starting") {
			bj.svc.StopAsync(ep.StreamID)
		}
	}
}

// StopEpisode stops a single episode's transcoding.
func (bj *BatchJob) StopEpisode(idx int) {
	bj.mu.Lock()
	if idx < 0 || idx >= len(bj.Episodes) {
		bj.mu.Unlock()
		return
	}
	ep := &bj.Episodes[idx]
	streamID := ep.StreamID
	ep.State = "pending" // Allow restart
	ep.StreamID = ""
	bj.mu.Unlock()

	if streamID != "" {
		bj.svc.StopAsync(streamID)
	}
}

// ---------------------------------------------------------------------------
// HTTP Handlers
// ---------------------------------------------------------------------------

// POST /transcoding/batch
func transcodingBatchStartHandler(cfg config.Config, bm *BatchManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		var req BatchStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
			return
		}

		if len(req.Episodes) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "No episodes provided"})
			return
		}

		// Cap at 20 episodes per batch.
		if len(req.Episodes) > 20 {
			req.Episodes = req.Episodes[:20]
		}

		job, errMsg := bm.Create(&req)
		if job == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": errMsg})
			return
		}

		host := transcodingHost(r)

		resp := map[string]any{
			"batchId":      job.ID,
			"masterUrl":    fmt.Sprintf("%s/transcoding/batch/%s/master.m3u8", host, job.ID),
			"statusUrl":    fmt.Sprintf("%s/transcoding/batch/%s/status", host, job.ID),
			"episodeCount": len(req.Episodes),
		}

		// Return per-episode info.
		episodes := make([]map[string]any, len(job.Episodes))
		job.mu.RLock()
		for i, ep := range job.Episodes {
			epInfo := map[string]any{
				"index": i,
				"title": ep.Title,
				"state": ep.State,
			}
			if ep.StreamID != "" {
				epInfo["streamId"] = ep.StreamID
				epInfo["playlistUrl"] = fmt.Sprintf("%s/transcoding/%s/main.m3u8", host, ep.StreamID)
			}
			episodes[i] = epInfo
		}
		job.mu.RUnlock()
		resp["episodes"] = episodes

		writeJSON(w, http.StatusOK, resp)
	}
}

// GET /transcoding/batch/{batchId}/master.m3u8
// Returns a concatenated HLS VOD playlist with all ready episodes.
func transcodingBatchMasterHandler(cfg config.Config, bm *BatchManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		batchID := chi.URLParam(r, "batchId")
		job := bm.Get(batchID)
		if job == nil {
			http.NotFound(w, r)
			return
		}
		job.Touch()

		host := transcodingHost(r)
		var b strings.Builder

		b.WriteString("#EXTM3U\n")
		b.WriteString("#EXT-X-VERSION:7\n")
		b.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
		b.WriteString("#EXT-X-TARGETDURATION:6\n")
		b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")

		job.mu.RLock()
		allReady := true
		segCounter := 0
		for _, ep := range job.Episodes {
			if ep.State != "ready" && ep.State != "running" {
				if ep.State == "pending" || ep.State == "starting" {
					allReady = false
				}
				continue
			}
			if ep.StreamID == "" || ep.Duration == 0 {
				allReady = false
				continue
			}

			// Add discontinuity between episodes.
			if segCounter > 0 {
				b.WriteString("#EXT-X-DISCONTINUITY\n")
			}

			// Episode title as comment.
			if ep.Title != "" {
				b.WriteString(fmt.Sprintf("#EXTINF:0, %s\n", ep.Title))
			}

			// Point to the per-episode playlist.
			b.WriteString(fmt.Sprintf("#EXT-X-MAP:URI=\"%s/transcoding/%s/init.mp4\"\n", host, ep.StreamID))

			segDur := 6
			numSegs := ep.Duration / segDur
			for si := range numSegs {
				b.WriteString(fmt.Sprintf("#EXTINF:%d.0,\n", segDur))
				b.WriteString(fmt.Sprintf("%s/transcoding/%s/seg_%05d.m4s\n", host, ep.StreamID, si))
				segCounter++
			}
		}
		job.mu.RUnlock()

		if allReady {
			b.WriteString("#EXT-X-ENDLIST\n")
		}

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b.String()))
	}
}

// GET /transcoding/batch/{batchId}/status
func transcodingBatchStatusHandler(cfg config.Config, bm *BatchManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		batchID := chi.URLParam(r, "batchId")
		job := bm.Get(batchID)
		if job == nil {
			http.NotFound(w, r)
			return
		}
		job.Touch()

		host := transcodingHost(r)

		job.mu.RLock()
		episodes := make([]map[string]any, len(job.Episodes))
		for i, ep := range job.Episodes {
			epInfo := map[string]any{
				"index":    i,
				"title":    ep.Title,
				"state":    ep.State,
				"duration": ep.Duration,
				"segCount": ep.SegCount,
			}
			if ep.Error != "" {
				epInfo["error"] = ep.Error
			}
			if ep.StreamID != "" {
				epInfo["streamId"] = ep.StreamID
				epInfo["playlistUrl"] = fmt.Sprintf("%s/transcoding/%s/main.m3u8", host, ep.StreamID)
			}
			episodes[i] = epInfo
		}
		job.mu.RUnlock()

		writeJSON(w, http.StatusOK, map[string]any{
			"batchId":      job.ID,
			"createdAt":    job.CreatedAt,
			"currentIndex": job.currentIndex,
			"episodes":     episodes,
		})
	}
}

// GET /transcoding/batch/{batchId}/heartbeat
func transcodingBatchHeartbeatHandler(cfg config.Config, bm *BatchManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		batchID := chi.URLParam(r, "batchId")
		job := bm.Get(batchID)
		if job == nil {
			http.NotFound(w, r)
			return
		}
		job.Touch()

		// Also touch all running episode jobs.
		job.mu.RLock()
		for _, ep := range job.Episodes {
			if ep.StreamID != "" {
				if tj, ok := bm.svc.TryResolveJob(ep.StreamID); ok {
					bm.svc.Touch(tj)
				}
			}
		}
		job.mu.RUnlock()

		w.WriteHeader(http.StatusOK)
	}
}

// GET /transcoding/batch/{batchId}/stop
func transcodingBatchStopHandler(cfg config.Config, bm *BatchManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		batchID := chi.URLParam(r, "batchId")
		bm.Remove(batchID)
		w.WriteHeader(http.StatusOK)
	}
}

// POST /transcoding/batch/{batchId}/episode/{index}
// Notify the batch that the user is now watching episode at {index}.
// This triggers pre-loading of upcoming episodes.
func transcodingBatchSetEpisodeHandler(cfg config.Config, bm *BatchManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		batchID := chi.URLParam(r, "batchId")
		idxStr := chi.URLParam(r, "index")

		job := bm.Get(batchID)
		if job == nil {
			http.NotFound(w, r)
			return
		}
		job.Touch()

		idx := 0
		fmt.Sscanf(idxStr, "%d", &idx)
		job.SetCurrentEpisode(idx)

		host := transcodingHost(r)

		// Return current episode info.
		job.mu.RLock()
		defer job.mu.RUnlock()

		if idx < 0 || idx >= len(job.Episodes) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid episode index"})
			return
		}

		ep := job.Episodes[idx]
		resp := map[string]any{
			"index": idx,
			"state": ep.State,
			"title": ep.Title,
		}
		if ep.StreamID != "" {
			resp["streamId"] = ep.StreamID
			resp["playlistUrl"] = fmt.Sprintf("%s/transcoding/%s/main.m3u8", host, ep.StreamID)
			if ep.Subtitles {
				resp["subtitlesUrl"] = fmt.Sprintf("%s/transcoding/%s/subtitles", host, ep.StreamID)
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
