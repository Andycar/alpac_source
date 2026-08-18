package opensubs

// Subtitle auto-sync.
//
// Downloaded subtitles are timed against some other release of the same film, so
// they routinely run a second or three off the stream we play. Nothing on our
// side is broken, which is why the complaint ("субтитры уехали") had no answer:
// there was nothing to fix, only two files that never agreed.
//
// internal/subsync measures the offset by correlating WHERE THERE IS SPEECH in
// the audio against where the cues are. This file is the plumbing: fetch the
// subtitle we already serve, run one bounded ffmpeg pass over the stream the
// viewer is actually watching, and hand back either the number or an already
// shifted VTT.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/subsync"
	"lampac-go/internal/subvtt"

	"github.com/rs/zerolog/log"
)

// One ffmpeg pass costs real CPU on a box that is often already transcoding for
// somebody. Two at a time, and never longer than syncTimeout.
var syncSlots = make(chan struct{}, 2)

const (
	syncTimeout  = 3 * time.Minute
	syncCacheTTL = 6 * time.Hour
	syncCacheMax = 512
)

type syncEntry struct {
	res subsync.Result
	at  time.Time
}

var (
	syncMu    sync.Mutex
	syncCache = map[string]syncEntry{}
)

func syncCacheGet(key string) (subsync.Result, bool) {
	syncMu.Lock()
	defer syncMu.Unlock()
	e, ok := syncCache[key]
	if !ok || time.Since(e.at) > syncCacheTTL {
		return subsync.Result{}, false
	}
	return e.res, true
}

func syncCacheSet(key string, res subsync.Result) {
	syncMu.Lock()
	defer syncMu.Unlock()
	if len(syncCache) >= syncCacheMax {
		// The measurement is per (subtitle, stream) pair and cheap to redo;
		// dropping the whole table beats keeping an LRU for it.
		syncCache = map[string]syncEntry{}
	}
	syncCache[key] = syncEntry{res: res, at: time.Now()}
}

// safeSyncSource resolves the caller-supplied stream reference to something we
// are willing to hand to ffmpeg.
//
// This is the SSRF gate: `src` comes from the client, and ffmpeg would happily
// open anything — an internal admin port, a file:// path, a metadata endpoint.
// Only our own stream paths are allowed, and they are rebuilt from OUR host
// rather than trusted as given.
func safeSyncSource(r *http.Request, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	path := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return "" // someone else's host — not ours to fetch
		}
		path = u.RequestURI()
	}
	if !strings.HasPrefix(path, "/") {
		return ""
	}
	allowed := false
	for _, p := range []string{"/proxy/", "/proxy-dash/", "/ts/", "/stream/", "/transcoding/", "/api/iptv/"} {
		if strings.HasPrefix(path, p) {
			allowed = true
			break
		}
	}
	if !allowed {
		return ""
	}
	scheme := "http" // loopback: our own listener, no TLS round-trip needed
	return scheme + "://" + r.Host + path
}

// measure runs the alignment for one (subtitle, stream) pair, with caching.
func (s *openSubsService) measure(ctx context.Context, fileID, src string) (subsync.Result, string) {
	key := fileID + "|" + src
	if res, ok := syncCacheGet(key); ok {
		return res, ""
	}

	data, _, _ := s.fetchSub(ctx, fileID)
	if data == nil {
		return subsync.Result{}, "subtitle fetch failed"
	}
	subs, ok := subvtt.ParseSubtitles(data)
	if !ok || len(subs.Items) == 0 {
		return subsync.Result{}, "subtitle parse failed"
	}

	cues := make([]subsync.Cue, 0, len(subs.Items))
	for _, it := range subs.Items {
		if it.EndAt > it.StartAt {
			cues = append(cues, subsync.Cue{Start: it.StartAt, End: it.EndAt})
		}
	}
	if len(cues) == 0 {
		return subsync.Result{}, "subtitle has no timed cues"
	}

	select {
	case syncSlots <- struct{}{}:
		defer func() { <-syncSlots }()
	case <-ctx.Done():
		return subsync.Result{}, "busy"
	}

	cctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()

	audio, err := subsync.DetectSpeech(cctx, s.cfg.Transcoding.FFmpeg, src, subsync.SampleDuration, subsync.DefaultStep)
	if err != nil {
		log.Debug().Err(err).Str("src", src).Msg("subsync: speech detection failed")
		return subsync.Result{}, "audio analysis failed"
	}
	// The cue track must span the same window as the audio sample, or the
	// correlation compares a 12-minute signal with a two-hour one.
	subTrack := subsync.TrackFromCues(cues, subsync.SampleDuration, subsync.DefaultStep)

	res := subsync.BestOffset(audio, subTrack, subsync.MaxShift, subsync.DefaultStep)
	syncCacheSet(key, res)
	log.Info().
		Str("file_id", fileID).
		Dur("offset", res.Offset).
		Float64("confidence", res.Confidence).
		Msg("subsync: measured subtitle offset")
	return res, ""
}

// GET /api/opensubs/sync?file_id=<id>&src=<our stream url>[&apply=1]
//
// Without apply → the measurement as JSON, for a client that would rather shift
// the subtitle itself (and for us, to see what it decided). With apply → the
// shifted WebVTT, for clients that cannot offset text tracks at all.
//
// A measurement below subsync.MinConfidence is reported but never applied:
// moving subtitles on a guess is worse than leaving a known constant lag, which
// a viewer can at least compensate for by hand.
func (s *openSubsService) handleSync(w http.ResponseWriter, r *http.Request) {
	fileID := r.URL.Query().Get("file_id")
	if fileID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_id required"})
		return
	}
	src := safeSyncSource(r, r.URL.Query().Get("src"))
	if src == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "src must be a stream url on this server"})
		return
	}

	res, errMsg := s.measure(r.Context(), fileID, src)
	if errMsg != "" {
		osSubsCORS(w)
		writeJSON(w, http.StatusOK, map[string]any{
			"offset_ms": 0, "confidence": 0, "applied": false, "error": errMsg,
		})
		return
	}
	confident := res.Confidence >= subsync.MinConfidence

	if r.URL.Query().Get("apply") != "1" {
		osSubsCORS(w)
		writeJSON(w, http.StatusOK, map[string]any{
			"offset_ms":  res.Offset.Milliseconds(),
			"confidence": round2(res.Confidence),
			"score":      round2(res.Score),
			"applied":    false,
			"sampled_s":  int(subsync.SampleDuration.Seconds()),
			"confident":  confident,
		})
		return
	}

	data, status, _ := s.fetchSub(r.Context(), fileID)
	if data == nil {
		writeJSON(w, status, map[string]string{"error": "opensubs fetch failed"})
		return
	}
	vtt := ""
	if confident && res.Offset != 0 {
		if subs, ok := subvtt.ParseSubtitles(data); ok {
			subs.Add(res.Offset)
			vtt = subvtt.RenderVTT(subs)
		}
	}
	if vtt == "" {
		vtt = subvtt.ConvertSubtitlesToVTT(data) // unshifted: unmeasurable or already aligned
	}
	osSubsCORS(w)
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	// Header, not just body: the client can show «сдвинуто на -2.4 с» without
	// parsing the cues back out.
	w.Header().Set("X-Sub-Offset-Ms", strconv.FormatInt(res.Offset.Milliseconds(), 10))
	w.Header().Set("X-Sub-Offset-Confidence", strconv.FormatFloat(round2(res.Confidence), 'f', 2, 64))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(vtt))
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
