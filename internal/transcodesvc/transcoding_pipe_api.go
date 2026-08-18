package transcodesvc

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
)

// ---------------------------------------------------------------------------
// HTTP surface for the in-memory fMP4 pipe transcoding path.
//
//	POST /transcoding/pipe/start                       → spawn session
//	GET  /transcoding/pipe/{streamId}/main.m3u8        → synthetic VOD playlist
//	GET  /transcoding/pipe/{streamId}/init.mp4         → fMP4 init segment
//	GET  /transcoding/pipe/{streamId}/seg_{n}.m4s      → media segment (from RAM)
//	GET  /transcoding/pipe/{streamId}/heartbeat        → keep-alive
//	GET  /transcoding/pipe/{streamId}/stop             → tear down
//	GET  /transcoding/pipe/{streamId}/status           → diagnostics
//
// The playlist is a full synthetic VOD list (every seg_NNNNN.m4s up to the
// probed duration, with ENDLIST) — identical in spirit to the disk path's
// main.m3u8, so the player can seek anywhere immediately and segment requests
// drive on-demand production/seek.
// ---------------------------------------------------------------------------

const pipeSegmentWait = 60 * time.Second

func transcodingPipeStartHandler(cfg config.Config, mgr *transcodingPipeManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable || !cfg.Transcoding.PipeEnable {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Pipe transcoding disabled"})
			return
		}

		var req TranscodingStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Request body is required"})
			return
		}
		// Stamp the HTTP User-Agent when the body didn't carry one.
		if getHeader(req.Headers, "userAgent") == "" {
			if hua := r.Header.Get("User-Agent"); hua != "" {
				if req.Headers == nil {
					req.Headers = map[string]string{}
				}
				req.Headers["userAgent"] = hua
			}
		}

		s, errMsg := mgr.Start(&req)
		if s == nil {
			status := http.StatusBadRequest
			if errMsg == ErrSchedulerBusy.Error() {
				w.Header().Set("Retry-After", "5")
				status = http.StatusServiceUnavailable
			}
			writeJSON(w, status, map[string]any{"error": errMsg})
			return
		}

		host := transcodingHost(r)
		resp := map[string]any{
			"streamId":            s.streamID,
			"mode":                "pipe",
			"selectedAudio":       s.audioIdx,
			"playlistUrl":         fmt.Sprintf("%s/transcoding/pipe/%s/main.m3u8", host, s.streamID),
			"hls_timeout_seconds": 60,
		}
		if s.streams != nil {
			resp["streams"] = s.streams
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func transcodingPipePlaylistHandler(cfg config.Config, mgr *transcodingPipeManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := mgr.resolve(extractPathParam(r, "streamId"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		s.touch()

		segDur := s.segDur
		if segDur <= 0 {
			segDur = 6
		}
		numSegments := s.duration / segDur
		if numSegments <= 0 {
			numSegments = 1
		}

		var b strings.Builder
		b.WriteString("#EXTM3U\n")
		b.WriteString("#EXT-X-VERSION:7\n")
		b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
		fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", segDur)
		b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
		b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
		for i := 0; i < numSegments; i++ {
			fmt.Fprintf(&b, "#EXTINF:%d.0,\n", segDur)
			fmt.Fprintf(&b, "seg_%05d.m4s\n", i)
		}
		b.WriteString("#EXT-X-ENDLIST\n")

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b.String()))
	}
}

func transcodingPipeInitHandler(cfg config.Config, mgr *transcodingPipeManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := mgr.resolve(extractPathParam(r, "streamId"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, errMsg := s.getInit(30 * time.Second)
		if errMsg != "" {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": errMsg})
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func transcodingPipeSegmentHandler(cfg config.Config, mgr *transcodingPipeManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := mgr.resolve(extractPathParam(r, "streamId"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		file := extractPathParam(r, "file")

		// init.mp4 and main.m3u8 have dedicated routes, but the catch-all may
		// still receive them depending on router matching — dispatch here too.
		switch {
		case file == "init.mp4":
			transcodingPipeInitHandler(cfg, mgr)(w, r)
			return
		case file == "main.m3u8":
			transcodingPipePlaylistHandler(cfg, mgr)(w, r)
			return
		}

		m := reSegIndex.FindStringSubmatch(file)
		if m == nil {
			http.NotFound(w, r)
			return
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			http.NotFound(w, r)
			return
		}

		data, errMsg := s.getSegment(idx, pipeSegmentWait)
		if errMsg != "" {
			http.NotFound(w, r)
			return
		}
		ct := transcodingContentTypes[".m4s"]
		if ct == "" {
			ct = "video/mp4"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func transcodingPipeHeartbeatHandler(cfg config.Config, mgr *transcodingPipeManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := mgr.resolve(extractPathParam(r, "streamId"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		s.touch()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func transcodingPipeStopHandler(cfg config.Config, mgr *transcodingPipeManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok := mgr.stopSession(extractPathParam(r, "streamId"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"stopped": true})
	}
}

func transcodingPipeStatusHandler(cfg config.Config, mgr *transcodingPipeManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := mgr.resolve(extractPathParam(r, "streamId"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, s.status())
	}
}
