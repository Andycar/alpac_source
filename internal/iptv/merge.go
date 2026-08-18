package iptv

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/hlsprobe"
	"lampac-go/internal/mediaprobe"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  Merged global view — unify several global playlists (e.g. multiple m3u.su
//  category lists) into ONE deduplicated channel list, with optional liveness
//  health-checking so dead/duplicate streams fall away.
// ---------------------------------------------------------------------------

// MergedGlobalID is the synthetic playlist id under which every global playlist is presented
// as a single deduplicated list. The web client tunes this one instead of N separate lists.
const MergedGlobalID = "all"

var (
	// quality/codec tags stripped from a name before dedup ("Первый HD" == "Первый канал")
	qualityTagRe = regexp.MustCompile(`(?i)(^|\s|\()(uhd|fhd|hd|sd|4k|2k|hevc|h\.?265|h\.?264|1080p?|720p?|576p?|480p?|360p?)(\)|\s|$)`)
	nonAlnumRe   = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

// normChannelName collapses a channel name to a dedup key: lowercased, quality/codec tags and
// all punctuation/emoji (flags) removed, whitespace folded. Empty → no dedup (kept as-is).
func normChannelName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	// run twice: adjacent tags ("Первый HD FHD") need a second pass since the regex consumes the separator
	s = qualityTagRe.ReplaceAllString(s, " ")
	s = qualityTagRe.ReplaceAllString(s, " ")
	s = nonAlnumRe.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func qualityRank(q string) int {
	switch strings.ToUpper(strings.TrimSpace(q)) {
	case "4K", "UHD", "2160P":
		return 4
	case "FHD", "1080P":
		return 3
	case "HD", "720P":
		return 2
	case "SD", "480P":
		return 1
	}
	return 0
}

// mergedLocked combines all global playlist caches into one deduplicated list. The CALLER must
// hold s.mu (R or W). Known-dead URLs are dropped before dedup so a dead duplicate yields to a
// healthy one from another source (implicit failover); among live duplicates the higher quality
// wins; otherwise the first source's entry is kept (source order = config order).
// mergedSnapshot is a memoized mergedLocked() result with the time it was built.
type mergedSnapshot struct {
	channels []Channel
	at       time.Time
}

// mergedCacheTTL bounds how stale the memoized global channel list may be. Config
// changes (RefreshGlobal/SetGlobalPlaylists) invalidate immediately; only a
// health-check dead-marking can be up to this stale, which the player tolerates.
const mergedCacheTTL = 30 * time.Second

// mergedLocked returns the deduplicated global channel list, memoized for
// mergedCacheTTL. The CALLER must hold s.mu (R or W). The returned slice is shared
// and MUST NOT be mutated by callers (they filter/paginate into fresh slices).
func (s *Store) mergedLocked() []Channel {
	if snap := s.mergedCache.Load(); snap != nil && time.Since(snap.at) < mergedCacheTTL {
		return snap.channels
	}
	out := s.buildMergedLocked()
	s.mergedCache.Store(&mergedSnapshot{channels: out, at: time.Now()})
	return out
}

// invalidateMerged drops the memoized merged list so the next mergedLocked()
// rebuilds. Call after any change to the global playlists / their caches.
func (s *Store) invalidateMerged() {
	s.mergedCache.Store(nil)
}

func (s *Store) buildMergedLocked() []Channel {
	var all []Channel
	for _, gu := range s.globalURLs {
		key := "global_" + playlistIDFromURL(gu)
		if c, ok := s.cache[key]; ok {
			all = append(all, c.Channels...)
		}
	}

	idx := make(map[string]int, len(all))
	out := make([]Channel, 0, len(all))
	for _, ch := range all {
		if s.isDeadURL(ch.URL) {
			continue
		}
		k := normChannelName(ch.Name)
		if k == "" {
			out = append(out, ch)
			continue
		}
		if i, ok := idx[k]; ok {
			if qualityRank(ch.Quality) > qualityRank(out[i].Quality) {
				out[i] = ch // prefer the higher-quality duplicate
			}
			continue
		}
		idx[k] = len(out)
		out = append(out, ch)
	}
	return out
}

// ---------------------------------------------------------------------------
//  Health-check (opt-in) — periodically probe stream liveness, mark dead URLs.
// ---------------------------------------------------------------------------

type healthState struct {
	mu   sync.RWMutex
	dead map[string]struct{} // stream URL → currently unreachable
	// codecs is what the deep probe saw INSIDE the stream, per URL. Free to
	// collect (the probe already downloaded the bytes) and the answer to the
	// most common IPTV complaint that is not a dead channel: the picture plays
	// and the sound does not, because the channel ships E-AC-3 to a box that
	// cannot decode it.
	codecs map[string]StreamInfo
}

// StreamInfo is what the last deep probe found inside a channel's stream.
type StreamInfo struct {
	Container string             `json:"container,omitempty"`
	Codecs    string             `json:"codecs,omitempty"` // RFC 6381 form: "hvc1.1.6.L120,ec-3"
	Tracks    []mediaprobe.Track `json:"tracks,omitempty"`
	ProbedAt  time.Time          `json:"probed_at,omitempty"`
}

// maxCodecEntries bounds the map for playlists with thousands of channels; the
// probe cap (healthMax) is the real limit, this is the backstop.
const maxCodecEntries = 8000

// StreamInfoFor returns what the last health cycle saw inside this stream URL.
// Empty when health-checking is off, the channel has not been probed yet, or
// the probe was the shallow kind.
func (s *Store) StreamInfoFor(u string) (StreamInfo, bool) {
	if u == "" || s.health == nil {
		return StreamInfo{}, false
	}
	s.health.mu.RLock()
	defer s.health.mu.RUnlock()
	info, ok := s.health.codecs[u]
	return info, ok
}

func (s *Store) recordStreamInfo(u string, tracks mediaprobe.Tracks) {
	if u == "" || len(tracks.Tracks) == 0 || s.health == nil {
		return
	}
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	if s.health.codecs == nil {
		s.health.codecs = make(map[string]StreamInfo)
	}
	if len(s.health.codecs) >= maxCodecEntries {
		if _, known := s.health.codecs[u]; !known {
			return
		}
	}
	s.health.codecs[u] = StreamInfo{
		Container: tracks.Container,
		Codecs:    tracks.CodecString(),
		Tracks:    tracks.Tracks,
		ProbedAt:  time.Now().UTC(),
	}
}

// codecCensus counts how many probed channels carry each codec. Only the codecs
// that decide whether a device can play at all are counted — the long tail
// would turn one log line into twenty.
func (s *Store) codecCensus() map[string]int {
	interesting := map[string]bool{
		"hevc": true, "h264": true, "av1": true, "dolbyvision": true,
		"eac3": true, "ac3": true, "aac": true, "mp2": true,
	}
	out := map[string]int{}
	s.health.mu.RLock()
	defer s.health.mu.RUnlock()
	for _, info := range s.health.codecs {
		seen := map[string]bool{}
		for _, tr := range info.Tracks {
			if interesting[tr.Name] && !seen[tr.Name] {
				seen[tr.Name] = true
				out[tr.Name]++
			}
		}
	}
	return out
}

func (s *Store) isDeadURL(u string) bool {
	if !s.healthOn || u == "" || s.health == nil {
		return false
	}
	s.health.mu.RLock()
	defer s.health.mu.RUnlock()
	_, dead := s.health.dead[u]
	return dead
}

// SetHealthDepth configures how thoroughly the liveness prober checks an HLS
// channel. Call before StartHealthCheck.
//
//	deep  — walk manifest → variant → first segment (~16KB per channel)
//	stale — additionally re-read a live playlist after a pause to catch a frozen
//	        channel; adds an 8s wait per channel, so it is for small lists only
func (s *Store) SetHealthDepth(deep, stale bool) {
	s.healthDeep = deep
	s.healthStale = stale
}

// StartHealthCheck launches the background liveness prober (bounded concurrency + a hard cap on
// URLs per cycle). Probing public streams from the server IP is brief (a ranged 0-1 GET), but it
// IS server-IP traffic — keep it opt-in and conservative so upstreams don't flag the box.
func (s *Store) StartHealthCheck(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	s.healthOn = true
	if s.health == nil {
		s.health = &healthState{dead: make(map[string]struct{})}
	}
	go func() {
		// let the initial playlist refresh settle before the first sweep
		select {
		case <-ctx.Done():
			return
		case <-time.After(90 * time.Second):
		}
		s.healthCycle(ctx)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.healthCycle(ctx)
			}
		}
	}()
}

func (s *Store) healthCycle(ctx context.Context) {
	type probe struct{ url, ua, ref string }
	seen := make(map[string]struct{})
	var probes []probe
	s.mu.RLock()
	for _, gu := range s.globalURLs {
		key := "global_" + playlistIDFromURL(gu)
		c, ok := s.cache[key]
		if !ok {
			continue
		}
		for _, ch := range c.Channels {
			if ch.URL == "" {
				continue
			}
			if _, dup := seen[ch.URL]; dup {
				continue
			}
			seen[ch.URL] = struct{}{}
			probes = append(probes, probe{ch.URL, ch.UserAgent, ch.Referer})
		}
	}
	s.mu.RUnlock()

	if len(probes) > s.healthMax {
		log.Warn().Int("total", len(probes)).Int("cap", s.healthMax).Msg("iptv: health-check capped — not all streams probed this cycle")
		probes = probes[:s.healthMax]
	}

	dead := make(map[string]struct{})
	var dmu sync.Mutex
	sem := make(chan struct{}, s.healthConc)
	var wg sync.WaitGroup
	for _, p := range probes {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(p probe) {
			defer wg.Done()
			defer func() { <-sem }()
			if !s.probeAlive(ctx, p.url, p.ua, p.ref) {
				dmu.Lock()
				dead[p.url] = struct{}{}
				dmu.Unlock()
			}
		}(p)
	}
	wg.Wait()

	s.health.mu.Lock()
	s.health.dead = dead
	s.health.mu.Unlock()

	// The codec census is the cheap by-product of a deep cycle, and it is what
	// tells an operator whether "у меня половина каналов без звука" is a client
	// bug or a park of E-AC-3 channels meeting boxes without a Dolby licence.
	ev := log.Info().Int("checked", len(probes)).Int("dead", len(dead))
	if s.healthDeep {
		for codec, n := range s.codecCensus() {
			ev = ev.Int("codec_"+codec, n)
		}
	}
	ev.Msg("iptv: health-check cycle")
}

func (s *Store) probeAlive(ctx context.Context, url, ua, ref string) bool {
	if s.healthDeep && looksHLS(url) {
		return s.probeHLSAlive(ctx, url, ua, ref)
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", healthUA(ua))
	if ref != "" {
		req.Header.Set("Referer", ref)
	}
	req.Header.Set("Range", "bytes=0-1") // we only need the response status, not the stream
	resp, err := s.healthClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode < 400 || resp.StatusCode == 405 // some hosts reject Range/HEAD but the stream is live
}

// probeHLSAlive walks the channel the way a player would. A two-byte read off
// the manifest calls a channel alive whenever its web server is alive — which
// is nearly always, including for channels whose segments have 403'd for weeks
// and channels serving an empty window.
func (s *Store) probeHLSAlive(ctx context.Context, url, ua, ref string) bool {
	c, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	fetch := func(ctx context.Context, u, rangeHdr string) (int, []byte, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return 0, nil, "", err
		}
		req.Header.Set("User-Agent", healthUA(ua))
		if ref != "" {
			req.Header.Set("Referer", ref)
		}
		if rangeHdr != "" {
			req.Header.Set("Range", rangeHdr)
		}
		resp, err := s.healthClient.Do(req)
		if err != nil {
			return 0, nil, "", err
		}
		defer resp.Body.Close()
		// Cap the read: a sweep over thousands of channels must not pull whole
		// segments off other people's CDNs.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		final := u
		if resp.Request != nil && resp.Request.URL != nil {
			final = resp.Request.URL.String()
		}
		return resp.StatusCode, body, final, nil
	}

	var res hlsprobe.Result
	if s.healthStale {
		res = hlsprobe.ProbeLive(c, url, fetch, 8*time.Second)
	} else {
		res = hlsprobe.Probe(c, url, fetch)
	}
	s.recordStreamInfo(url, res.Media)
	if !res.OK {
		log.Debug().
			Str("url", url).Str("stage", string(res.Stage)).
			Int("manifest", res.ManifestStatus).Int("segment", res.SegmentStatus).
			Bool("stale", res.Stale).Str("err", res.Err).
			Msg("iptv: channel failed deep probe")
	}
	return res.OK
}

func looksHLS(u string) bool {
	l := strings.ToLower(u)
	if i := strings.IndexByte(l, '?'); i >= 0 {
		l = l[:i]
	}
	return strings.HasSuffix(l, ".m3u8") || strings.HasSuffix(l, ".m3u")
}

func healthUA(ua string) string {
	if ua == "" {
		return "Mozilla/5.0 (Linux; Android 10) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Mobile Safari/537.36"
	}
	return ua
}
