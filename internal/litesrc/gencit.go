package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Regex for parsing horsez.org HTML
// ---------------------------------------------------------------------------

var (
	gencitDataConfigRe = regexp.MustCompile(`data-config='(\{[^']+\})'`)
	gencitInputDataRe  = regexp.MustCompile(`id="inputData"[^>]*>([\s\S]+?)</div>`)
	gencitDataFilmRe   = regexp.MustCompile(`data-film='(\{[^']+\})'`)
	gencitSubRuRe      = regexp.MustCompile(`data-ru_subtitle="([^"]*)"`)
	gencitSubEnRe      = regexp.MustCompile(`data-en_subtitle="([^"]*)"`)
	gencitSubUaRe      = regexp.MustCompile(`data-ua_subtitle="([^"]*)"`)
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type gencitChecker struct {
	client  *http.Client
	host    string // https://horsez.org
	referer string
	index   *gencitIndex
}

type gencitDataConfig struct {
	HLS string `json:"hls"`
}

type gencitEpisode struct {
	VideoID   int        `json:"video_id"`
	Season    int        `json:"season"`
	Episode   int        `json:"episode"`
	VoiceName string     `json:"voice_name"`
	VoiceID   int        `json:"voice_id"`
	Duration  int        `json:"duration"`
	Skip      [][]string `json:"skip"`
}

type gencitDataFilm struct {
	KpID   int64 `json:"kp_id"`
	ImdbID int64 `json:"imdb_id"`
}

// ---------------------------------------------------------------------------
// Index: kpID → playlistID mapping built by scanning horsez.org
// ---------------------------------------------------------------------------

type gencitIndex struct {
	mu        sync.RWMutex
	kpToPlay  map[int64]string // kp_id → playlist ID string
	maxScan   int              // highest playlist ID scanned so far
	cachePath string           // path to persist index on disk
	host      string
	referer   string
	client    *http.Client
	ready     chan struct{} // closed when initial scan finishes
}

const (
	gencitMaxPlaylistID = 25000     // upper bound for scan
	gencitScanWorkers   = 20        // concurrent fetchers
	gencitReadLimit     = 16 * 1024 // read first 16KB (data-film appears ~12KB in)
	gencitCacheFile     = "gencit_index.json"
)

func newGencitIndex(host, referer, repoRoot string, client *http.Client) *gencitIndex {
	cachePath := ""
	if repoRoot != "" {
		cachePath = filepath.Join(repoRoot, "database", gencitCacheFile)
	}

	idx := &gencitIndex{
		kpToPlay:  make(map[int64]string, 4096),
		cachePath: cachePath,
		host:      host,
		referer:   referer,
		client:    client,
		ready:     make(chan struct{}),
	}

	// Try loading from disk cache first
	if idx.loadFromDisk() {
		log.Info().Int("entries", len(idx.kpToPlay)).Msg("gencit: index loaded from disk cache")
		close(idx.ready)
		// Still rescan in background to pick up new entries
		go idx.scan()
	} else {
		// No cache — scan and block checksearch until done
		go func() {
			idx.scan()
			close(idx.ready)
		}()
	}

	return idx
}

// lookup returns the horsez playlist ID for a given kp_id, or "".
// Waits up to 3s for initial scan if not ready.
func (idx *gencitIndex) lookup(kpID int64) string {
	// Wait for initial scan with timeout
	select {
	case <-idx.ready:
	case <-time.After(3 * time.Second):
		// Index not ready yet, try what we have
	}

	idx.mu.RLock()
	pid := idx.kpToPlay[kpID]
	idx.mu.RUnlock()
	return pid
}

func (idx *gencitIndex) scan() {
	start := time.Now()
	log.Info().Int("max", gencitMaxPlaylistID).Int("workers", gencitScanWorkers).Msg("gencit: starting index scan")

	type result struct {
		playlistID int
		kpID       int64
	}

	jobs := make(chan int, gencitScanWorkers*2)
	results := make(chan result, 256)

	var wg sync.WaitGroup
	for w := 0; w < gencitScanWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pid := range jobs {
				kpID := idx.fetchKpID(pid)
				if kpID > 0 {
					results <- result{playlistID: pid, kpID: kpID}
				}
			}
		}()
	}

	// Collector
	done := make(chan struct{})
	var count int
	go func() {
		for r := range results {
			idx.mu.Lock()
			idx.kpToPlay[r.kpID] = strconv.Itoa(r.playlistID)
			idx.mu.Unlock()
			count++
		}
		close(done)
	}()

	// Send jobs
	for id := 1; id <= gencitMaxPlaylistID; id++ {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
	close(results)
	<-done

	idx.mu.Lock()
	idx.maxScan = gencitMaxPlaylistID
	idx.mu.Unlock()

	// Persist to disk
	idx.saveToDisk()

	log.Info().
		Int("entries", count).
		Dur("elapsed", time.Since(start)).
		Msg("gencit: index scan complete")
}

// fetchKpID reads first 16KB of horsez.org/lat/{id} and extracts kp_id.
func (idx *gencitIndex) fetchKpID(playlistID int) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	targetURL := fmt.Sprintf("%s/lat/%d", idx.host, playlistID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	req.Header.Set("Referer", idx.referer)

	resp, err := idx.client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, gencitReadLimit))
	if err != nil {
		return 0
	}

	m := gencitDataFilmRe.FindSubmatch(body)
	if len(m) < 2 {
		return 0
	}

	var film gencitDataFilm
	if err := stdjson.Unmarshal(m[1], &film); err != nil {
		return 0
	}
	return film.KpID
}

// Disk cache persistence

type gencitDiskCache struct {
	KpToPlay map[string]string `json:"kp_to_play"` // "kpID" → "playlistID"
	MaxScan  int               `json:"max_scan"`
}

func (idx *gencitIndex) loadFromDisk() bool {
	if idx.cachePath == "" {
		return false
	}
	data, err := os.ReadFile(idx.cachePath)
	if err != nil {
		return false
	}
	var cache gencitDiskCache
	if err := stdjson.Unmarshal(data, &cache); err != nil {
		return false
	}
	if len(cache.KpToPlay) == 0 {
		return false
	}
	idx.mu.Lock()
	for k, v := range cache.KpToPlay {
		kpID, err := strconv.ParseInt(k, 10, 64)
		if err == nil && kpID > 0 {
			idx.kpToPlay[kpID] = v
		}
	}
	idx.maxScan = cache.MaxScan
	idx.mu.Unlock()
	return true
}

func (idx *gencitIndex) saveToDisk() {
	if idx.cachePath == "" {
		return
	}
	idx.mu.RLock()
	cache := gencitDiskCache{
		KpToPlay: make(map[string]string, len(idx.kpToPlay)),
		MaxScan:  idx.maxScan,
	}
	for k, v := range idx.kpToPlay {
		cache.KpToPlay[strconv.FormatInt(k, 10)] = v
	}
	idx.mu.RUnlock()

	data, err := stdjson.Marshal(cache)
	if err != nil {
		log.Warn().Err(err).Msg("gencit: failed to marshal index cache")
		return
	}

	dir := filepath.Dir(idx.cachePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn().Err(err).Msg("gencit: failed to create cache dir")
		return
	}

	if err := os.WriteFile(idx.cachePath, data, 0o644); err != nil {
		log.Warn().Err(err).Msg("gencit: failed to write index cache")
	}
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

func NewGencitChecker(cfg config.Config) *gencitChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.Gencit.Host, "/"))
	if host == "" || strings.Contains(host, "horsez.org") {
		// horsez.org redirected to ylitron.pro 2026-05; same DLE engine, same
		// /lat/{id} layout, same kp_id JSON in data attrs.
		host = "https://ylitron.pro"
	}
	referer := "https://kinomix.web.app/"

	// horsez.org blocks datacenter IPs; SOCKS5 proxy is required on servers
	socksAddr := strings.TrimSpace(cfg.Online.Gencit.Socks)
	var client *http.Client
	if socksAddr != "" {
		httpclient.RegisterProxiedBalancer(socksAddr, []string{"gencit"})
		client = httpclient.NewForBalancer("gencit", 15*time.Second)
		log.Info().Str("socks", socksAddr).Msg("gencit: registered SOCKS5 proxy")
	} else {
		client = httpclient.New(15 * time.Second)
	}

	return &gencitChecker{
		client:  client,
		host:    host,
		referer: referer,
		index:   newGencitIndex(host, referer, cfg.Compat.RepoRoot, client),
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (g *gencitChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/"), "/")

		if raw == "gencit" {
			if parseBoolParam(req.URL.Query().Get("checksearch")) {
				show := g.checkSearch(req)
				writeCheckSearchResponse(w, show, pluginQualityBadgeGet("gencit"))
				return
			}
			g.indexPage(w, req, links)
			return
		}

		if after, ok := strings.CutPrefix(raw, "gencit/"); ok && after == "video" {
			g.video(w, req, links)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "gencit route not implemented",
			"balanser": raw,
		})
	}
}

// ---------------------------------------------------------------------------
// checksearch
// ---------------------------------------------------------------------------

func (g *gencitChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	kpID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	if kpID == 0 {
		kpID, _ = strconv.ParseInt(strings.TrimSpace(q.Get("id")), 10, 64)
	}
	if kpID == 0 {
		return false
	}
	return g.index.lookup(kpID) != ""
}

// ---------------------------------------------------------------------------
// index — main entry point
// ---------------------------------------------------------------------------

func (g *gencitChecker) indexPage(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	kpID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	if kpID == 0 {
		kpID, _ = strconv.ParseInt(strings.TrimSpace(q.Get("id")), 10, 64)
	}
	if kpID == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	s, sOK := getsTVQueryInt(q.Get("s"))
	if !sOK {
		s = -1
	}
	t, tOK := getsTVQueryInt(q.Get("t"))
	if !tOK {
		t = -1
	}

	// 1. Lookup playlist ID from index
	playlistID := g.index.lookup(kpID)
	if playlistID == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	iframeURL := fmt.Sprintf("%s/lat/%s", g.host, playlistID)

	// 2. Fetch horsez HTML
	html, ok := g.fetchHorsezURL(req, iframeURL)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// 3. Parse inputData
	playlist := g.parseInputData(html)
	if len(playlist) == 0 {
		log.Debug().Int("htmlLen", len(html)).
			Bool("hasInputData", strings.Contains(html, "inputData")).
			Str("playlistID", playlistID).
			Msg("gencit: no inputData found, trying movie")
		g.writeMovie(w, req, rjson, title, originalTitle, html, playlistID, links)
		return
	}

	// 4. Serial
	g.writeSerial(w, req, rjson, title, originalTitle, playlistID, playlist, s, t, links)
}

// ---------------------------------------------------------------------------
// video — method:call callback
// ---------------------------------------------------------------------------

func (g *gencitChecker) video(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	playlist := strings.TrimSpace(q.Get("playlist"))
	season := strings.TrimSpace(q.Get("s"))
	episode := strings.TrimSpace(q.Get("e"))
	voice := strings.TrimSpace(q.Get("v"))
	title := strings.TrimSpace(q.Get("title"))

	if playlist == "" || season == "" || episode == "" || voice == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	epURL := fmt.Sprintf("%s/lat/%s?season=%s&episode=%s&voice=%s",
		g.host, playlist, season, episode, voice)

	html, ok := g.fetchHorsezURL(req, epURL)
	if !ok {
		log.Warn().Str("url", epURL).Msg("gencit: video fetch failed")
		writeGetsTVEmpty(w, rjson)
		return
	}

	hlsURL := g.parseDataConfig(html)
	if hlsURL == "" {
		log.Warn().Int("htmlLen", len(html)).
			Bool("hasDataConfig", strings.Contains(html, "data-config")).
			Bool("has404", strings.Contains(html, "404 Not Found")).
			Str("url", epURL).
			Msg("gencit: video HLS URL not found")
		writeGetsTVEmpty(w, rjson)
		return
	}

	subs := g.parseSubtitles(html)
	stream := streamProxyURL(req, hlsURL, "gencit", links)

	log.Info().Str("hlsURL", hlsURL).Str("stream", stream).Msg("gencit: video resolved")

	row := map[string]any{
		"method": "play",
		"url":    stream,
		"stream": stream,
		"title":  title,
		"name":   "По умолчанию",
	}
	if len(subs) > 0 {
		row["subtitles"] = subs
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": []map[string]any{row},
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	getsTVAppendMovieHTML(&sb, row, "По умолчанию", true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
// writeMovie
// ---------------------------------------------------------------------------

func (g *gencitChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool,
	title, originalTitle, html, playlistID string, links *proxylink.Manager) {

	hlsURL := g.parseDataConfig(html)
	if hlsURL == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	subs := g.parseSubtitles(html)
	stream := streamProxyURL(req, hlsURL, "gencit", links)
	baseTitle := getsTVJoinName(title, originalTitle)

	row := map[string]any{
		"method": "play",
		"url":    stream,
		"stream": stream,
		"title":  baseTitle,
		"name":   "По умолчанию",
	}
	if len(subs) > 0 {
		row["subtitles"] = subs
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": []map[string]any{row},
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	getsTVAppendMovieHTML(&sb, row, "По умолчанию", true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
// writeSerial
// ---------------------------------------------------------------------------

type gencitPlaylist map[string]map[string][]gencitEpisode

func (g *gencitChecker) writeSerial(w http.ResponseWriter, req *http.Request, rjson bool,
	title, originalTitle, playlistID string, playlist gencitPlaylist,
	season, voiceIdx int, links *proxylink.Manager) {

	host := hostFromRequest(req)
	baseTitle := getsTVJoinName(title, originalTitle)
	q := req.URL.Query()

	seasonKeys := make([]int, 0, len(playlist))
	for sk := range playlist {
		if n, err := strconv.Atoi(sk); err == nil {
			seasonKeys = append(seasonKeys, n)
		}
	}
	sort.Ints(seasonKeys)

	if len(seasonKeys) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// --- Season list ---
	if season == -1 {
		data := make([]map[string]any, 0, len(seasonKeys))
		for _, sn := range seasonKeys {
			link := fmt.Sprintf("%s/lite/gencit?%s&s=%d", host, gencitBaseQuery(q), sn)
			data = append(data, map[string]any{
				"method": "link",
				"url":    link,
				"name":   fmt.Sprintf("%d сезон", sn),
				"id":     sn,
			})
		}

		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": data})
			return
		}
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range data {
			getsTVAppendSeasonHTML(&sb, row, row["name"].(string), i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// --- Episodes ---
	seasonStr := strconv.Itoa(season)
	episodes, ok := playlist[seasonStr]
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	firstEpKey := gencitFirstEpKey(episodes)
	voices := gencitCollectVoices(episodes, firstEpKey)

	selectedVoice := 0
	if voiceIdx >= 0 && voiceIdx < len(voices) {
		selectedVoice = voiceIdx
	}
	activeVoiceID := 0
	if len(voices) > 0 {
		activeVoiceID = voices[selectedVoice].VoiceID
	}

	voiceData := make([]map[string]any, 0, len(voices))
	for i, v := range voices {
		link := fmt.Sprintf("%s/lite/gencit?%s&s=%d&t=%d", host, gencitBaseQuery(q), season, i)
		voiceData = append(voiceData, map[string]any{
			"method": "link",
			"url":    link,
			"name":   v.VoiceName,
			"active": i == selectedVoice,
		})
	}

	epKeys := make([]int, 0, len(episodes))
	for ek := range episodes {
		if n, err := strconv.Atoi(ek); err == nil {
			epKeys = append(epKeys, n)
		}
	}
	sort.Ints(epKeys)

	// Resolve HLS URLs for all episodes concurrently (method:play)
	type epResolved struct {
		epNum int
		ep    *gencitEpisode
		hls   string
		subs  []map[string]string
	}

	resolvedCh := make(chan epResolved, len(epKeys))
	sem := make(chan struct{}, 5) // 5 concurrent fetches

	var fetchWg sync.WaitGroup
	for _, epNum := range epKeys {
		epList := episodes[strconv.Itoa(epNum)]
		var ep *gencitEpisode
		for i := range epList {
			if epList[i].VoiceID == activeVoiceID {
				ep = &epList[i]
				break
			}
		}
		if ep == nil && len(epList) > 0 {
			ep = &epList[0]
		}
		if ep == nil {
			continue
		}

		fetchWg.Add(1)
		go func(num int, e gencitEpisode) {
			defer fetchWg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			epURL := fmt.Sprintf("%s/lat/%s?season=%d&episode=%d&voice=%d",
				g.host, playlistID, season, num, e.VoiceID)
			html, ok := g.fetchHorsezURL(req, epURL)
			if !ok {
				return
			}
			hls := g.parseDataConfig(html)
			if hls == "" {
				return
			}
			resolvedCh <- epResolved{
				epNum: num,
				ep:    &e,
				hls:   hls,
				subs:  g.parseSubtitles(html),
			}
		}(epNum, *ep)
	}

	go func() {
		fetchWg.Wait()
		close(resolvedCh)
	}()

	// Collect results
	resolvedMap := make(map[int]epResolved, len(epKeys))
	for r := range resolvedCh {
		resolvedMap[r.epNum] = r
	}

	episodeData := make([]map[string]any, 0, len(epKeys))
	for _, epNum := range epKeys {
		r, ok := resolvedMap[epNum]
		if !ok {
			continue
		}

		stream := streamProxyURL(req, r.hls, "gencit", links)
		row := map[string]any{
			"method": "play",
			"url":    stream,
			"stream": stream,
			"title":  fmt.Sprintf("%s / %d сезон %d серия", baseTitle, season, epNum),
			"name":   r.ep.VoiceName,
			"s":      season,
			"e":      epNum,
		}
		if len(r.subs) > 0 {
			row["subtitles"] = r.subs
		}
		episodeData = append(episodeData, row)
	}

	if len(episodeData) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		result := map[string]any{"type": "episode", "data": episodeData}
		if len(voiceData) > 1 {
			result["voice"] = voiceData
		}
		writeJSON(w, http.StatusOK, result)
		return
	}

	var sb strings.Builder
	if len(voiceData) > 1 {
		sb.WriteString(`<div class="videos__line">`)
		for _, v := range voiceData {
			getsTVAppendVoiceHTML(&sb, v)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range episodeData {
		getsTVAppendMovieHTML(&sb, row, row["name"].(string), i == 0, season, row["e"].(int))
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
// horsez.org fetch
// ---------------------------------------------------------------------------

func (g *gencitChecker) fetchHorsezURL(req *http.Request, targetURL string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	httpReq.Header.Set("Referer", g.referer)

	resp, err := balancerDoWithRetry(req.Context(), g.client, httpReq, 2)
	if err != nil {
		log.Debug().Err(err).Str("url", targetURL).Msg("gencit: horsez fetch error")
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// ---------------------------------------------------------------------------
// HTML parsers
// ---------------------------------------------------------------------------

func (g *gencitChecker) parseDataConfig(html string) string {
	m := gencitDataConfigRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	var cfg gencitDataConfig
	if err := stdjson.Unmarshal([]byte(m[1]), &cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.HLS)
}

func (g *gencitChecker) parseInputData(html string) gencitPlaylist {
	m := gencitInputDataRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return nil
	}
	raw := strings.TrimSpace(m[1])
	if raw == "" || raw[0] != '{' {
		return nil
	}
	var parsed map[string]map[string][]gencitEpisode
	if err := stdjson.Unmarshal([]byte(raw), &parsed); err != nil {
		log.Debug().Err(err).Msg("gencit: inputData parse error")
		return nil
	}
	return gencitPlaylist(parsed)
}

func (g *gencitChecker) parseSubtitles(html string) []map[string]string {
	var subs []map[string]string
	if m := gencitSubRuRe.FindStringSubmatch(html); len(m) > 1 && m[1] != "" {
		subs = append(subs, map[string]string{"label": "Русские", "url": m[1]})
	}
	if m := gencitSubUaRe.FindStringSubmatch(html); len(m) > 1 && m[1] != "" {
		subs = append(subs, map[string]string{"label": "Українські", "url": m[1]})
	}
	if m := gencitSubEnRe.FindStringSubmatch(html); len(m) > 1 && m[1] != "" {
		subs = append(subs, map[string]string{"label": "English", "url": m[1]})
	}
	return subs
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func gencitBaseQuery(q url.Values) string {
	var parts []string
	for _, key := range []string{"kinopoisk_id", "title", "original_title", "rjson"} {
		if v := q.Get(key); v != "" {
			parts = append(parts, key+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func gencitFirstEpKey(episodes map[string][]gencitEpisode) string {
	keys := make([]int, 0, len(episodes))
	for k := range episodes {
		if n, err := strconv.Atoi(k); err == nil {
			keys = append(keys, n)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Ints(keys)
	return strconv.Itoa(keys[0])
}

type gencitVoiceInfo struct {
	VoiceID   int
	VoiceName string
}

func gencitCollectVoices(episodes map[string][]gencitEpisode, epKey string) []gencitVoiceInfo {
	epList, ok := episodes[epKey]
	if !ok {
		return nil
	}
	seen := make(map[int]bool)
	var voices []gencitVoiceInfo
	for _, ep := range epList {
		if !seen[ep.VoiceID] {
			seen[ep.VoiceID] = true
			voices = append(voices, gencitVoiceInfo{VoiceID: ep.VoiceID, VoiceName: ep.VoiceName})
		}
	}
	return voices
}
