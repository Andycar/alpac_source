package opensubs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/subvtt"

	"github.com/rs/zerolog/log"
)

const openSubsAPI = "https://api.opensubtitles.com/api/v1"

// openSubsSearchHandler proxies subtitle search requests to OpenSubtitles REST API.
// GET /api/opensubs/search?imdb_id=tt1234567&lang=en,ru&query=...&season=1&episode=2
func openSubsSearchHandler(cfg config.Config) http.HandlerFunc {
	client := httpclient.New(15 * time.Second)
	cache := &openSubsCache{
		entries: make(map[string]*osCacheEntry),
		ttl:     time.Duration(cfg.OpenSubs.CacheTTLMin) * time.Minute,
	}
	if cache.ttl <= 0 {
		cache.ttl = 60 * time.Minute
	}

	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		imdbID := q.Get("imdb_id")
		query := q.Get("query")
		langs := q.Get("lang")
		season := q.Get("season")
		episode := q.Get("episode")

		if imdbID == "" && query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "imdb_id or query required"})
			return
		}

		// Build upstream URL
		params := []string{}
		if imdbID != "" {
			params = append(params, "imdb_id="+imdbID)
		}
		if query != "" {
			params = append(params, "query="+query)
		}
		if langs != "" {
			params = append(params, "languages="+langs)
		}
		if season != "" {
			params = append(params, "season_number="+season)
		}
		if episode != "" {
			params = append(params, "episode_number="+episode)
		}
		params = append(params, "order_by=download_count", "order_direction=desc")

		target := openSubsAPI + "/subtitles?" + strings.Join(params, "&")

		// Check cache
		cacheKey := target
		if cached, ok := cache.get(cacheKey); ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("X-Cache", "HIT")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(cached)
			return
		}

		log.Debug().Str("target", target).Msg("opensubs: searching")

		req, err := http.NewRequestWithContext(r.Context(), "GET", target, nil)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
			return
		}
		req.Header.Set("Api-Key", cfg.OpenSubs.APIKey)
		req.Header.Set("User-Agent", cfg.OpenSubs.UserAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			log.Warn().Err(err).Msg("opensubs: search request failed")
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "read error"})
			return
		}

		if resp.StatusCode == http.StatusOK {
			cache.set(cacheKey, body)
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	}
}

// openSubsSegDurSec is the HLS subtitle segment length for the segmented
// rendition. 10s matches our default video segment duration so subtitle
// segments line up with the video timeline.
const openSubsSegDurSec = 10

// openSubsService holds shared state for the OpenSubtitles endpoints — the HTTP
// client, a cache of raw downloaded subtitle bytes keyed by file_id (so the
// segmented rendition doesn't re-hit the API for every segment), and a cache of
// the fragmented WebVTT segments.
type openSubsService struct {
	cfg    config.Config
	client *http.Client
	subs   *openSubsCache // raw subtitle bytes by file_id

	fragMu sync.Mutex
	frag   map[string][]string // "fileID|durSec" -> per-segment VTT
}

func newOpenSubsService(cfg config.Config) *openSubsService {
	return &openSubsService{
		cfg:    cfg,
		client: httpclient.New(30 * time.Second),
		subs:   &openSubsCache{entries: make(map[string]*osCacheEntry), ttl: 30 * time.Minute},
		frag:   make(map[string][]string),
	}
}

// fetchSub returns the raw subtitle bytes for a file_id via the 2-step
// OpenSubtitles flow (download-link, then file fetch), cached for the TTL. On
// failure returns (nil, statusCode, upstreamBody) so callers can relay it.
func (s *openSubsService) fetchSub(ctx context.Context, fileID string) ([]byte, int, []byte) {
	if data, ok := s.subs.get(fileID); ok {
		return data, http.StatusOK, nil
	}

	dlBody := fmt.Sprintf(`{"file_id":%s}`, fileID)
	dlReq, err := http.NewRequestWithContext(ctx, "POST", openSubsAPI+"/download", strings.NewReader(dlBody))
	if err != nil {
		return nil, http.StatusBadRequest, nil
	}
	dlReq.Header.Set("Api-Key", s.cfg.OpenSubs.APIKey)
	dlReq.Header.Set("User-Agent", s.cfg.OpenSubs.UserAgent)
	dlReq.Header.Set("Content-Type", "application/json")
	dlReq.Header.Set("Accept", "application/json")

	dlResp, err := s.client.Do(dlReq)
	if err != nil {
		log.Warn().Err(err).Msg("opensubs: download link request failed")
		return nil, http.StatusBadGateway, nil
	}
	defer dlResp.Body.Close()

	dlRespBody, err := io.ReadAll(io.LimitReader(dlResp.Body, 1*1024*1024))
	if err != nil {
		return nil, http.StatusBadGateway, nil
	}
	if dlResp.StatusCode != http.StatusOK {
		return nil, dlResp.StatusCode, dlRespBody
	}

	var dlResult struct {
		Link     string `json:"link"`
		FileName string `json:"file_name"`
	}
	if err := json.Unmarshal(dlRespBody, &dlResult); err != nil || dlResult.Link == "" {
		log.Warn().Err(err).Str("body", string(dlRespBody)).Msg("opensubs: failed to parse download link")
		return nil, http.StatusBadGateway, nil
	}

	subReq, err := http.NewRequestWithContext(ctx, "GET", dlResult.Link, nil)
	if err != nil {
		return nil, http.StatusBadGateway, nil
	}
	subResp, err := s.client.Do(subReq)
	if err != nil {
		log.Warn().Err(err).Str("link", dlResult.Link).Msg("opensubs: subtitle fetch failed")
		return nil, http.StatusBadGateway, nil
	}
	defer subResp.Body.Close()

	subData, err := io.ReadAll(io.LimitReader(subResp.Body, 5*1024*1024))
	if err != nil {
		return nil, http.StatusBadGateway, nil
	}
	s.subs.set(fileID, subData)
	return subData, http.StatusOK, nil
}

// fragments returns the cached per-segment WebVTT for (fileID, durSec),
// computing and caching it on first request. count is the number of segments.
func (s *openSubsService) fragments(ctx context.Context, fileID string, durSec, count int) ([]string, bool) {
	key := fileID + "|" + strconv.Itoa(durSec)
	s.fragMu.Lock()
	if segs, ok := s.frag[key]; ok {
		s.fragMu.Unlock()
		return segs, true
	}
	s.fragMu.Unlock()

	data, _, _ := s.fetchSub(ctx, fileID)
	if data == nil {
		return nil, false
	}
	segs, ok := subvtt.BuildFragmentedVTTSegments(data, time.Duration(openSubsSegDurSec)*time.Second, count)
	if !ok {
		return nil, false
	}

	s.fragMu.Lock()
	if len(s.frag) > 128 { // crude bound — entries are cheap to recompute
		s.frag = make(map[string][]string)
	}
	s.frag[key] = segs
	s.fragMu.Unlock()
	return segs, true
}

func osSubsCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
}

// osSegmentCount returns the number of segments for the requested duration.
func osSegmentCount(r *http.Request) (durSec, count int) {
	durSec, _ = strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("dur")))
	if durSec <= 0 {
		durSec = 9999 // unknown → one big window
	}
	count = (durSec + openSubsSegDurSec - 1) / openSubsSegDurSec
	if count < 1 {
		count = 1
	}
	return durSec, count
}

// handleDownload serves the whole subtitle as one WebVTT blob (works in every
// player). GET /api/opensubs/download?file_id=XXX
func (s *openSubsService) handleDownload(w http.ResponseWriter, r *http.Request) {
	fileID := r.URL.Query().Get("file_id")
	if fileID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_id required"})
		return
	}
	data, status, upstream := s.fetchSub(r.Context(), fileID)
	if data == nil {
		osSubsCORS(w)
		if upstream != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(status)
			_, _ = w.Write(upstream)
			return
		}
		writeJSON(w, status, map[string]string{"error": "opensubs fetch failed"})
		return
	}
	vtt := subvtt.ConvertSubtitlesToVTT(data)
	osSubsCORS(w)
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(vtt))
}

// handleRendition serves a segmented WebVTT HLS media playlist so strict HLS
// players (native Tizen/Apple) can consume the subtitle as timeline-aligned
// segments instead of one blob. The dur query is the media duration in seconds.
// GET /api/opensubs/rendition/{fileID}/index.m3u8?dur=SEC
func (s *openSubsService) handleRendition(w http.ResponseWriter, r *http.Request) {
	fileID := extractPathParam(r, "fileID")
	if fileID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_id required"})
		return
	}
	durSec, count := osSegmentCount(r)

	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", openSubsSegDurSec)
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MEDIA-SEQUENCE:0\n")
	for i := 0; i < count; i++ {
		fmt.Fprintf(&b, "#EXTINF:%d.000,\nseg_%d.vtt?dur=%d\n", openSubsSegDurSec, i, durSec)
	}
	b.WriteString("#EXT-X-ENDLIST\n")

	osSubsCORS(w)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

// handleSegment serves fragment i of the subtitle track as a self-contained
// WebVTT document. Out-of-range or fetch failure yields an empty (valid) VTT
// segment so the player timeline stays intact.
// GET /api/opensubs/rendition/{fileID}/seg_{i}.vtt?dur=SEC
func (s *openSubsService) handleSegment(w http.ResponseWriter, r *http.Request) {
	fileID := extractPathParam(r, "fileID")
	idx, err := strconv.Atoi(extractPathParam(r, "i"))
	if fileID == "" || err != nil || idx < 0 {
		http.NotFound(w, r)
		return
	}
	durSec, count := osSegmentCount(r)

	osSubsCORS(w)
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")

	segs, ok := s.fragments(r.Context(), fileID, durSec, count)
	if !ok || idx >= len(segs) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("WEBVTT\n\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(segs[idx]))
}

// --- Simple in-memory cache for search results ---

type osCacheEntry struct {
	data    []byte
	expires time.Time
}

type openSubsCache struct {
	mu      sync.RWMutex
	entries map[string]*osCacheEntry
	ttl     time.Duration
}

func (c *openSubsCache) get(key string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.data, true
}

func (c *openSubsCache) set(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Evict expired entries if cache grows large
	if len(c.entries) > 500 {
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expires) {
				delete(c.entries, k)
			}
		}
	}
	c.entries[key] = &osCacheEntry{
		data:    data,
		expires: time.Now().Add(c.ttl),
	}
}
