package transcodesvc

import (
	"context"
	"fmt"
	"image/jpeg"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// Trickplay — seekbar thumbnail previews, adapted from Jellyfin's
// TrickplayManager for a streaming aggregator: Jellyfin batch-decodes local
// library files ahead of time, but our sources are remote URLs, so a full
// keyframe-only decode would re-download the whole file per title. Instead we
// take ~trickplayMaxThumbs sparse frames with ffmpeg *input* seeking
// (`-ss T -i url -frames:v 1`): each grab range-requests only the bytes
// around one keyframe (an HLS source fetches ~one segment), so a full run
// costs tens of MB, not gigabytes. Thumbs are tiled into a single sprite
// sheet (trickplayTileCols wide) + a JSON manifest, generated lazily on the
// first /trickplay request and stored in the job's OutputDir (shared cleanup
// lifecycle). By the time a viewer starts scrubbing — a minute or two in —
// the sprite is ready; the client shows previews from one cached image.

const (
	trickplayMaxThumbs   = 100
	trickplayMinInterval = 10  // seconds between thumbs, floor
	trickplayThumbWidth  = 320 // px; height follows source aspect
	trickplayTileCols    = 10
	trickplayConcurrency = 2               // parallel ffmpeg grabs, global
	trickplayTotalBudget = 5 * time.Minute // hard cap per generation run
	trickplayGrabTimeout = 20 * time.Second
)

// trickplayGrabSlots throttles concurrent frame grabs across ALL jobs so
// trickplay never competes with real transcodes for CPU/network.
var trickplayGrabSlots = make(chan struct{}, trickplayConcurrency)

// trickplayState tracks one job's generation lifecycle.
type trickplayState struct {
	mu       sync.Mutex
	started  bool
	ready    bool
	failed   bool
	count    int // thumbs in the sprite
	interval int // seconds per thumb
	thumbW   int
	thumbH   int
	rows     int
}

var (
	trickplayStatesMu sync.Mutex
	trickplayStates   = map[string]*trickplayState{} // OutputDir → state
)

func trickplayStateFor(job *TranscodingJob) *trickplayState {
	trickplayStatesMu.Lock()
	defer trickplayStatesMu.Unlock()
	// Opportunistic prune: job cleanup removes OutputDir, so a state whose
	// dir is gone is dead weight. Keeps the map bounded without a janitor.
	if len(trickplayStates) > 32 {
		for dir := range trickplayStates {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				delete(trickplayStates, dir)
			}
		}
	}
	st, ok := trickplayStates[job.OutputDir]
	if !ok {
		st = &trickplayState{}
		trickplayStates[job.OutputDir] = st
	}
	return st
}

// trickplayPlan derives thumb count + interval from the source duration.
// interval = max(10s, dur/100) so a feature film gets 100 thumbs and a short
// episode one per 10 seconds.
func trickplayPlan(durationSec int) (count, interval int) {
	if durationSec <= trickplayMinInterval {
		return 0, 0
	}
	interval = durationSec / trickplayMaxThumbs
	if interval < trickplayMinInterval {
		interval = trickplayMinInterval
	}
	count = durationSec / interval
	if count > trickplayMaxThumbs {
		count = trickplayMaxThumbs
	}
	return count, interval
}

// startTrickplayGeneration kicks the background generator once per job.
func startTrickplayGeneration(cfg config.Config, job *TranscodingJob) *trickplayState {
	st := trickplayStateFor(job)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.started {
		return st
	}

	durationSec := ffprobeDuration(job.Context.FFProbe)
	count, interval := trickplayPlan(durationSec)
	if count < 3 || job.Context.Live || job.Context.Source == "pipe:0" {
		// Too short / live / unseekable stdin source — nothing sensible to build.
		st.started, st.failed = true, true
		return st
	}
	st.started = true
	st.count, st.interval = count, interval

	go generateTrickplay(cfg, job, st, count, interval)
	return st
}

func generateTrickplay(cfg config.Config, job *TranscodingJob, st *trickplayState, count, interval int) {
	dir := filepath.Join(job.OutputDir, "trickplay")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		trickplayFail(st, "mkdir", err)
		return
	}
	ffmpegBin := strings.TrimSpace(cfg.Transcoding.FFmpeg)
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}

	ctx, cancel := context.WithTimeout(context.Background(), trickplayTotalBudget)
	defer cancel()

	// Phase 1: sparse frame grabs (bounded parallelism).
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		select {
		case trickplayGrabSlots <- struct{}{}:
		case <-ctx.Done():
			trickplayFail(st, "budget", ctx.Err())
			return
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-trickplayGrabSlots }()
			ts := idx*interval + interval/2 // mid-interval frame
			out := filepath.Join(dir, fmt.Sprintf("thumb_%03d.jpg", idx))
			gctx, gcancel := context.WithTimeout(ctx, trickplayGrabTimeout)
			defer gcancel()
			// Input-side -ss: demuxer seeks (HTTP Range / HLS segment jump),
			// decodes one GOP, emits one frame. -noaccurate_seek keeps it to
			// the nearest keyframe — cheapest possible grab, and thumbnail
			// accuracy of ±one GOP is invisible on a seekbar preview.
			cmd := exec.CommandContext(gctx, ffmpegBin,
				"-y", "-hide_banner", "-loglevel", "error",
				"-noaccurate_seek", "-ss", fmt.Sprintf("%d", ts),
				"-an", "-sn", "-i", job.Context.Source,
				"-frames:v", "1",
				"-vf", fmt.Sprintf("scale=%d:-2", trickplayThumbWidth),
				"-qscale:v", "5", out)
			if err := cmd.Run(); err != nil {
				log.Debug().Err(err).Int("idx", idx).Msg("trickplay: frame grab failed")
			}
		}(i)
	}
	wg.Wait()
	if ctx.Err() != nil {
		trickplayFail(st, "budget", ctx.Err())
		return
	}

	// Phase 2: fill gaps (a failed grab breaks the %03d sequence the tile
	// filter consumes) with the nearest previous thumb; leading gaps get the
	// first available one.
	var firstOK string
	okCount := 0
	for i := 0; i < count; i++ {
		p := filepath.Join(dir, fmt.Sprintf("thumb_%03d.jpg", i))
		if stat, err := os.Stat(p); err == nil && stat.Size() > 0 {
			if firstOK == "" {
				firstOK = p
			}
			okCount++
		}
	}
	if okCount < count/2 || firstOK == "" {
		trickplayFail(st, "grabs", fmt.Errorf("only %d/%d thumbs extracted", okCount, count))
		return
	}
	prev := firstOK
	for i := 0; i < count; i++ {
		p := filepath.Join(dir, fmt.Sprintf("thumb_%03d.jpg", i))
		if stat, err := os.Stat(p); err == nil && stat.Size() > 0 {
			prev = p
			continue
		}
		if data, err := os.ReadFile(prev); err == nil {
			_ = os.WriteFile(p, data, 0o644)
		}
	}

	// Thumb dimensions from the first jpg (aspect preserved by scale=:-2).
	thumbW, thumbH := trickplayThumbWidth, 180
	if f, err := os.Open(firstOK); err == nil {
		if cfgImg, err := jpeg.DecodeConfig(f); err == nil {
			thumbW, thumbH = cfgImg.Width, cfgImg.Height
		}
		_ = f.Close()
	}

	// Phase 3: tile into one sprite sheet.
	rows := (count + trickplayTileCols - 1) / trickplayTileCols
	sprite := filepath.Join(dir, "sprite.jpg")
	tctx, tcancel := context.WithTimeout(ctx, 30*time.Second)
	defer tcancel()
	tileCmd := exec.CommandContext(tctx, ffmpegBin,
		"-y", "-hide_banner", "-loglevel", "error",
		"-framerate", "1", "-start_number", "0",
		"-i", filepath.Join(dir, "thumb_%03d.jpg"),
		"-frames:v", "1",
		"-vf", fmt.Sprintf("tile=%dx%d", trickplayTileCols, rows),
		"-qscale:v", "4", sprite)
	if err := tileCmd.Run(); err != nil {
		trickplayFail(st, "tile", err)
		return
	}

	// Thumb files served their purpose — the sprite carries everything.
	for i := 0; i < count; i++ {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("thumb_%03d.jpg", i)))
	}

	st.mu.Lock()
	st.ready, st.thumbW, st.thumbH, st.rows = true, thumbW, thumbH, rows
	st.mu.Unlock()
	log.Info().Int("thumbs", count).Int("interval_s", interval).Msg("trickplay: sprite ready")
}

func trickplayFail(st *trickplayState, stage string, err error) {
	st.mu.Lock()
	st.failed = true
	st.mu.Unlock()
	log.Warn().Err(err).Str("stage", stage).Msg("trickplay: generation failed")
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/trickplay            — manifest (starts gen)
// GET /transcoding/{streamId}/trickplay/sprite.jpg — sprite sheet
// ---------------------------------------------------------------------------

func transcodingTrickplayManifestHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}
		streamID := extractPathParam(r, "streamId")
		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		svc.Touch(job)

		st := startTrickplayGeneration(cfg, job)
		st.mu.Lock()
		defer st.mu.Unlock()
		out := map[string]any{
			"ready":    st.ready,
			"failed":   st.failed,
			"interval": st.interval,
			"count":    st.count,
		}
		if st.ready {
			out["thumbWidth"] = st.thumbW
			out["thumbHeight"] = st.thumbH
			out["tileCols"] = trickplayTileCols
			out["tileRows"] = st.rows
			out["spriteUrl"] = fmt.Sprintf("%s/transcoding/%s/trickplay/sprite.jpg", transcodingHost(r), streamID)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func transcodingTrickplaySpriteHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}
		streamID := extractPathParam(r, "streamId")
		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		svc.Touch(job)

		path := filepath.Join(job.OutputDir, "trickplay", "sprite.jpg")
		if st, err := os.Stat(path); err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, path)
	}
}
