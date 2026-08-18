package iptvhttp

import (
	"bytes"
	"context"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/iptv"
	"lampac-go/internal/tgauth"
)

// ---------------------------------------------------------------------------
//  Live channel-card thumbnails (opt-in: iptv.previews)
//
//  Playlists that ship NO tvg-logo / tvg-id / EPG (e.g. fluxeras uplist) leave
//  cards blank. We can't extract a preview from such a playlist — so we GENERATE
//  one: grab a single frame from the live stream via ffmpeg, on demand when a
//  card is visible, and cache it. ffmpeg jobs are bounded by a semaphore and the
//  result is cached so repeat views are free; off by default to protect the box.
// ---------------------------------------------------------------------------

const (
	iptvPreviewMaxEntries  = 500              // bounded cache (~500 * ~60KB ≈ 30MB worst case)
	iptvPreviewGrabTimeout = 12 * time.Second // hard ffmpeg deadline per grab
	iptvPreviewQueueWait   = 2 * time.Second  // give up if every ffmpeg slot is busy this long
	iptvPreviewMinBytes    = 256              // smaller than this isn't a real JPEG
	iptvPreviewMaxBytes    = 2 << 20          // cap a single frame at 2MB
)

type iptvPreviewEntry struct {
	data []byte
	ct   string
	ts   time.Time
}

// iptvPreviewCache holds rendered frames with TTL + a single-flight guard so N
// simultaneous requests for the same channel spawn at most one ffmpeg.
type iptvPreviewCache struct {
	mu       sync.Mutex
	entries  map[string]*iptvPreviewEntry
	inflight map[string]chan struct{}
	sem      chan struct{}
	ffmpeg   string
	ttl      time.Duration
}

func newIPTVPreviewCache(ffmpeg string, concurrency, ttlSec int) *iptvPreviewCache {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if concurrency <= 0 {
		concurrency = 3
	}
	if ttlSec <= 0 {
		ttlSec = 600
	}
	return &iptvPreviewCache{
		entries:  make(map[string]*iptvPreviewEntry),
		inflight: make(map[string]chan struct{}),
		sem:      make(chan struct{}, concurrency),
		ffmpeg:   ffmpeg,
		ttl:      time.Duration(ttlSec) * time.Second,
	}
}

// get returns a (possibly freshly grabbed) JPEG frame for the channel, or ok=false
// when no frame could be produced (timeout / every slot busy). Stale frames are
// served immediately while a refresh runs in the background.
func (c *iptvPreviewCache) get(ctx context.Context, ch *iptv.Channel) (data []byte, ct string, ok bool) {
	c.mu.Lock()
	if e := c.entries[ch.ID]; e != nil && time.Since(e.ts) < c.ttl {
		data, ct = e.data, e.ct
		c.mu.Unlock()
		return data, ct, len(data) > 0
	}
	// (Re)grab needed. Single-flight: if a grab is already running for this channel,
	// serve any stale frame immediately, otherwise wait for the in-flight grab.
	if done, busy := c.inflight[ch.ID]; busy {
		if e := c.entries[ch.ID]; e != nil && len(e.data) > 0 {
			data, ct = e.data, e.ct
			c.mu.Unlock()
			return data, ct, true // stale-while-revalidate
		}
		c.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, "", false
		case <-time.After(iptvPreviewGrabTimeout + iptvPreviewQueueWait):
			return nil, "", false
		}
		c.mu.Lock()
		e := c.entries[ch.ID]
		c.mu.Unlock()
		if e != nil && len(e.data) > 0 {
			return e.data, e.ct, true
		}
		return nil, "", false
	}
	// We own the grab.
	done := make(chan struct{})
	c.inflight[ch.ID] = done
	c.mu.Unlock()

	data, ct = c.grab(ch)

	c.mu.Lock()
	if len(data) > 0 {
		c.entries[ch.ID] = &iptvPreviewEntry{data: data, ct: ct, ts: time.Now()}
		c.evictLocked()
	}
	delete(c.inflight, ch.ID)
	close(done)
	c.mu.Unlock()
	return data, ct, len(data) > 0
}

// evictLocked drops the oldest entry when over the cap (caller holds c.mu).
func (c *iptvPreviewCache) evictLocked() {
	if len(c.entries) <= iptvPreviewMaxEntries {
		return
	}
	var oldestKey string
	var oldestTS time.Time
	for k, e := range c.entries {
		if oldestKey == "" || e.ts.Before(oldestTS) {
			oldestKey, oldestTS = k, e.ts
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// grab spawns ffmpeg to pull one frame from the live stream as a JPEG. Returns
// nil when ffmpeg fails, times out, or every slot is saturated.
func (c *iptvPreviewCache) grab(ch *iptv.Channel) ([]byte, string) {
	// Bounded concurrency — bail rather than queue forever when saturated.
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-time.After(iptvPreviewQueueWait):
		return nil, ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), iptvPreviewGrabTimeout)
	defer cancel()

	args := []string{"-nostdin", "-loglevel", "error", "-y"}
	if ch.UserAgent != "" {
		args = append(args, "-user_agent", ch.UserAgent)
	}
	if ch.Referer != "" {
		args = append(args, "-headers", "Referer: "+ch.Referer+"\r\n")
	}
	args = append(args,
		"-rw_timeout", "8000000", // 8s I/O timeout (µs) so a dead stream doesn't hold the slot
		"-i", ch.URL,
		"-frames:v", "1", // exactly one frame
		"-an",                 // no audio
		"-vf", "scale=480:-2", // downscale for a light thumbnail
		"-q:v", "6",
		"-f", "mjpeg", "pipe:1", // one JPEG to stdout
	)

	cmd := exec.CommandContext(ctx, c.ffmpeg, args...)
	var buf bytes.Buffer
	cmd.Stdout = &cappedWriter{buf: &buf, max: iptvPreviewMaxBytes}
	cmd.Stderr = nil
	_ = cmd.Run() // ignore exit code: a partial-but-valid first frame is still usable

	if buf.Len() < iptvPreviewMinBytes {
		return nil, ""
	}
	return buf.Bytes(), "image/jpeg"
}

// cappedWriter discards bytes past max so a misbehaving codec can't blow up memory.
type cappedWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil // always claim full consumption so ffmpeg keeps writing/exits cleanly
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/preview?channel_id=...
//  Returns a JPEG frame, 204 when none is ready yet, 404 when previews are off.
// ---------------------------------------------------------------------------

func iptvPreviewHandler(cache *iptvPreviewCache, iptvStore *iptv.Store, tgStore *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cache == nil {
			http.Error(w, "previews disabled", http.StatusNotFound)
			return
		}
		tgID := iptvTgID(r, tgStore)
		channelID := r.URL.Query().Get("channel_id")
		if channelID == "" {
			http.Error(w, "channel_id required", http.StatusBadRequest)
			return
		}
		ch, _ := iptvStore.GetChannel(tgID, channelID)
		if ch == nil {
			http.Error(w, "channel not found", http.StatusNotFound)
			return
		}
		low := strings.ToLower(ch.URL)
		if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
			http.Error(w, "unsupported stream", http.StatusNotFound)
			return
		}

		data, ct, ok := cache.get(r.Context(), ch)
		if !ok || len(data) == 0 {
			// Not ready (slot saturated / grab failed). 204 lets the client retry
			// later without surfacing an error in the console.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}
