package transcodesvc

import (
	"net/http"

	"lampac-go/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// registerTranscodingRoutes wires /tracks.js, /ffprobe, /transcoding.js and
// (when cfg.Transcoding.Enable is true) the full /transcoding/* API plus the
// batch-mode endpoints. Returns the TranscodingService so NewServer can hold
// a reference for shutdown and live-reload.
// RegisterRoutes wires /tracks*, /ffprobe and (when enabled) the /transcoding/*
// API, returning the TranscodingService (nil when disabled). deps injects
// everything this package needs from the host — see Deps.
func RegisterRoutes(router chi.Router, cfg config.Config, deps Deps) *TranscodingService {
	setProviders(deps)
	router.Get("/tracks.js", genericPluginJSHandler("tracks.js", "", cfg))
	router.Get("/tracks/js/{token}", genericPluginJSHandler("tracks.js", "", cfg))
	router.Get("/ffprobe", ffprobeHandler(cfg))
	router.Get("/transcoding.js", genericPluginJSHandler("transcoding.js", "", cfg))
	router.Get("/transcoding/js/{token}", genericPluginJSHandler("transcoding.js", "", cfg))

	if !cfg.Transcoding.Enable {
		return nil
	}

	transSvc := NewTranscodingService(cfg)
	pipeMgr := newTranscodingPipeManager(transSvc, cfg)
	transSvc.pipeMgr = pipeMgr
	batchMgr := NewBatchManager(transSvc, cfg)

	// All /transcoding/* API routes share a CORS group so the TV web client can fetch the playlist +
	// segments cross-origin when transcoding runs on a SEPARATE box (config remote_host). A wildcard
	// OPTIONS handles the preflight a ranged Shaka/MSE fetch may send. Same-origin (local) is unaffected.
	router.Group(func(r chi.Router) {
		r.Use(transcodeCORSMiddleware)
		r.Options("/transcoding/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

		r.Post("/transcoding/start", transcodingStartHandler(cfg, transSvc))
		r.Get("/transcoding/start.m3u8", transcodingStartM3U8Handler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/seek/{ss}", transcodingSeekHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/live.m3u8", transcodingLiveHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/main.m3u8", transcodingMainHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/master.m3u8", transcodingMasterHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/subs_{si}.m3u8", transcodingSubsRenditionHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/subtitles", transcodingSubtitlesHandler(cfg, transSvc))
		// Embedded-font extraction for client-side ASS rendering (JASSUB) —
		// the static "fonts" segment wins over the {file} catch-all in chi.
		r.Get("/transcoding/{streamId}/fonts", transcodingFontsListHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/fonts/{name}", transcodingFontFileHandler(cfg, transSvc))
		// Trickplay seekbar previews (sprite sheet + manifest), lazy per job.
		r.Get("/transcoding/{streamId}/trickplay", transcodingTrickplayManifestHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/trickplay/sprite.jpg", transcodingTrickplaySpriteHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/heartbeat", transcodingHeartbeatHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/stop", transcodingStopHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/status", transcodingStatusHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/diagnostics", transcodingDiagnosticsHandler(cfg, transSvc))
		r.Get("/transcoding/stats", transcodingStatsHandler(cfg, transSvc))
		r.Get("/transcoding/selftest", transcodingSelfTestHandler(cfg, transSvc))

		// Experimental in-memory fMP4 pipe path (opt-in via pipe_enable). Registered before the disk
		// catch-all so the literal "pipe" segment takes precedence over the {streamId} param.
		r.Post("/transcoding/pipe/start", transcodingPipeStartHandler(cfg, pipeMgr))
		r.Get("/transcoding/pipe/{streamId}/main.m3u8", transcodingPipePlaylistHandler(cfg, pipeMgr))
		r.Get("/transcoding/pipe/{streamId}/init.mp4", transcodingPipeInitHandler(cfg, pipeMgr))
		r.Get("/transcoding/pipe/{streamId}/heartbeat", transcodingPipeHeartbeatHandler(cfg, pipeMgr))
		r.Get("/transcoding/pipe/{streamId}/stop", transcodingPipeStopHandler(cfg, pipeMgr))
		r.Get("/transcoding/pipe/{streamId}/status", transcodingPipeStatusHandler(cfg, pipeMgr))
		r.Get("/transcoding/pipe/{streamId}/{file}", transcodingPipeSegmentHandler(cfg, pipeMgr))

		// Multi-audio HLS renditions (hls4): lazy per-track AAC playlists +
		// segments. Static "aud_" prefix — no clash with v{rung} below.
		r.Get("/transcoding/{streamId}/aud_{aidx}/{file}", transcodingAudioRenditionHandler(cfg, transSvc))

		// P3.K2: ABR multi-rung variant subdir — the {rung} path param prefixes the lookup with v<N>/
		// inside the job's output dir. Registered BEFORE the catch-all so chi matches it first.
		r.Get("/transcoding/{streamId}/v{rung}/{file}", transcodingSegmentHandler(cfg, transSvc))
		r.Get("/transcoding/{streamId}/{file}", transcodingSegmentHandler(cfg, transSvc))
		r.Get("/transcoding", transcodingDocHandler(cfg))

		// Batch Transcoding API.
		r.Post("/transcoding/batch", transcodingBatchStartHandler(cfg, batchMgr))
		r.Get("/transcoding/batch/{batchId}/master.m3u8", transcodingBatchMasterHandler(cfg, batchMgr))
		r.Get("/transcoding/batch/{batchId}/status", transcodingBatchStatusHandler(cfg, batchMgr))
		r.Get("/transcoding/batch/{batchId}/heartbeat", transcodingBatchHeartbeatHandler(cfg, batchMgr))
		r.Get("/transcoding/batch/{batchId}/stop", transcodingBatchStopHandler(cfg, batchMgr))
		r.Post("/transcoding/batch/{batchId}/episode/{index}", transcodingBatchSetEpisodeHandler(cfg, batchMgr))
	})

	log.Info().Str("ffmpeg", cfg.Transcoding.FFmpeg).Int("max_jobs", cfg.Transcoding.MaxConcurrent).Msg("transcoding API enabled")
	return transSvc
}
