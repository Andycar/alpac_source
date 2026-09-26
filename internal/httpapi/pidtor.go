package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/torrbalancer"
	"lampac-go/internal/transcodesvc"
	"maps"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  RedAPI response types
// ---------------------------------------------------------------------------

type pidtorRedAPIRoot struct {
	Results []pidtorResult `json:"Results"`
}

type pidtorResult struct {
	Tracker     string     `json:"Tracker"`
	Title       string     `json:"Title"`
	Size        int64      `json:"Size"`
	Seeders     int        `json:"Seeders"`
	MagnetUri   string     `json:"MagnetUri"`
	Info        pidtorInfo `json:"info"`
	PublishDate string     `json:"PublishDate"`
}

type pidtorInfo struct {
	Voices   []string `json:"voices"`
	SizeName string   `json:"sizeName"`
	Seasons  []int    `json:"seasons"`
}

// TorrServer response types
type pidtorTSStat struct {
	Hash      string           `json:"hash"`
	FileStats []pidtorFileStat `json:"file_stats"`
}

type pidtorFileStat struct {
	ID     int    `json:"id"`
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

// ---------------------------------------------------------------------------
//  Parsed torrent entry (cached)
// ---------------------------------------------------------------------------

type pidtorEntry struct {
	Name      string
	Voice     string
	Magnet    string
	Seeders   int
	TrackerQS string // &tr=xxx&tr=yyy
	Quality   string // "2160p", "1080p", "720p"
	Size      int64
	MediaInfo string
	Torrent   pidtorResult
}

// ---------------------------------------------------------------------------
//  Cache
// ---------------------------------------------------------------------------

var pidtorCache = struct {
	sync.RWMutex
	m map[string]*pidtorCacheItem
}{m: make(map[string]*pidtorCacheItem)}

type pidtorCacheItem struct {
	entries []pidtorEntry
	ts      time.Time
}

var pidtorSerialCache = struct {
	sync.RWMutex
	m map[string]*pidtorSerialCacheItem
}{m: make(map[string]*pidtorSerialCacheItem)}

type pidtorSerialCacheItem struct {
	files []pidtorFileStat
	ts    time.Time
}

func pidtorCacheGet(key string) ([]pidtorEntry, bool) {
	pidtorCache.RLock()
	item := pidtorCache.m[key]
	pidtorCache.RUnlock()
	if item == nil || time.Since(item.ts) > 5*time.Minute {
		return nil, false
	}
	return item.entries, true
}

func pidtorCacheSet(key string, entries []pidtorEntry) {
	pidtorCache.Lock()
	pidtorCache.m[key] = &pidtorCacheItem{entries: entries, ts: time.Now()}
	pidtorCache.Unlock()
}

func pidtorSerialCacheGet(key string) ([]pidtorFileStat, bool) {
	pidtorSerialCache.RLock()
	item := pidtorSerialCache.m[key]
	pidtorSerialCache.RUnlock()
	if item == nil || time.Since(item.ts) > 36*time.Hour {
		return nil, false
	}
	return item.files, true
}

func pidtorSerialCacheSet(key string, files []pidtorFileStat) {
	pidtorSerialCache.Lock()
	pidtorSerialCache.m[key] = &pidtorSerialCacheItem{files: files, ts: time.Now()}
	pidtorSerialCache.Unlock()
}

// pidtorFilenameCache caches the base name of each file inside a torrent so
// the TorrServer stream URL can include "/stream/<filename>?..." instead of
// the queryless "/stream?...". The filename in the path lets detectExt()
// pick up the container extension (.mkv/.mp4) when wrapping through /proxy/,
// which external players (Vimu / MX Player) rely on to choose a demuxer.
//
// Key format: "<tsHash>:<fileIndex>".
var pidtorFilenameCache = struct {
	sync.RWMutex
	m map[string]string
}{m: make(map[string]string)}

func pidtorFilenameCacheGet(tsHash string, idx int) string {
	pidtorFilenameCache.RLock()
	defer pidtorFilenameCache.RUnlock()
	return pidtorFilenameCache.m[fmt.Sprintf("%s:%d", tsHash, idx)]
}

func pidtorFilenameCacheSet(tsHash string, idx int, name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	pidtorFilenameCache.Lock()
	pidtorFilenameCache.m[fmt.Sprintf("%s:%d", tsHash, idx)] = name
	pidtorFilenameCache.Unlock()
}

// pidtorResolveFilename polls TorrServer for file metadata and returns the
// base name at tsid (1-based). All files are cached on first hit so neighbour
// indices are free afterwards. Empty return means caller falls back to the
// queryless /stream?link=... form.
func pidtorResolveFilename(ctx context.Context, client *http.Client, tsHost string, headers map[string]string, tsHash string, tsid int) string {
	if name := pidtorFilenameCacheGet(tsHash, tsid); name != "" {
		return name
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		payload := fmt.Sprintf(`{"action":"get","hash":"%s"}`, tsHash)
		req, err := http.NewRequestWithContext(ctx, "POST", tsHost+"/torrents", strings.NewReader(payload))
		if err != nil {
			return ""
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			resp.Body.Close()
			var stat pidtorTSStat
			if json.Unmarshal(body, &stat) == nil && len(stat.FileStats) > 0 {
				for _, f := range stat.FileStats {
					name := path.Base(strings.TrimSpace(f.Path))
					pidtorFilenameCacheSet(tsHash, f.ID, name)
				}
				return pidtorFilenameCacheGet(tsHash, tsid)
			}
		}
		if time.Now().After(deadline) {
			return ""
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// pidtorBuildStreamURL formats a TorrServer /stream URL, embedding the
// filename in the path when known so /proxy/<enc>.{ext} carries the right
// extension for external players.
func pidtorBuildStreamURL(tsHost, link string, tsid int, fn string) string {
	if strings.TrimSpace(fn) != "" {
		return fmt.Sprintf("%s/stream/%s?link=%s&index=%d&play",
			tsHost, url.PathEscape(fn), url.QueryEscape(link), tsid)
	}
	return fmt.Sprintf("%s/stream?link=%s&index=%d&play",
		tsHost, url.QueryEscape(link), tsid)
}

// ---------------------------------------------------------------------------
//  Handler registration
// ---------------------------------------------------------------------------

func newPidTorHandler(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	client := httpclient.New(10 * time.Second)
	// TorrServer API calls (add/get/rem). 20s: MatriX "add" blocks until the
	// torrent metadata arrives (seconds for a seeded release), while a wedged
	// backend hangs forever — the cap is what lets pidtorWithTS fail over to
	// the next pool backend before the player gives up.
	tsClient := httpclient.New(20 * time.Second)

	return func(w http.ResponseWriter, r *http.Request) {
		init := cfg.Online.PidTor
		if !init.Enable {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		// checksearch — quick availability probe
		if parseBoolParam(r.URL.Query().Get("checksearch")) {
			show, qual := pidtorCheckSearch(r, cfg, client)
			writeCheckSearchResponse(w, show, qual)
			return
		}

		raw := strings.TrimPrefix(r.URL.Path, "/lite/")

		switch {
		case raw == "pidtor":
			pidtorIndex(w, r, cfg, client)
		case strings.HasPrefix(raw, "pidtor/serial/"):
			hash := strings.TrimPrefix(raw, "pidtor/serial/")
			pidtorSerial(w, r, cfg, tsClient, hash, links)
		case strings.HasPrefix(raw, "pidtor/s"):
			hash := strings.TrimPrefix(raw, "pidtor/s")
			pidtorStream(w, r, cfg, tsClient, hash, links)
		default:
			http.NotFound(w, r)
		}
	}
}

// ---------------------------------------------------------------------------
//  checksearch — quick availability probe
// ---------------------------------------------------------------------------

func pidtorCheckSearch(r *http.Request, cfg config.Config, client *http.Client) (bool, string) {
	q := r.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year := q.Get("year")
	serial := q.Get("serial")
	origLang := q.Get("original_language")

	init := cfg.Online.PidTor

	memKey := fmt.Sprintf("pidtor:%s:%s:%s", title, originalTitle, year)

	entries, ok := pidtorCacheGet(memKey)
	if !ok {
		entries = pidtorFetchEntries(r, cfg, client, title, originalTitle, year, serial, origLang, init)
		// Cache ONLY non-empty results. A zero-result search is almost always
		// transient for a torrent source (jacred hiccup, an id-only/no-title
		// probe, datacenter rate-limit) — caching it would pin pidtor at 0 for
		// the whole 5-min TTL for every user. Letting empties fall through means
		// the next request with a real title re-searches and self-heals.
		if len(entries) > 0 {
			pidtorCacheSet(memKey, entries)
		}
	}

	if len(entries) == 0 {
		return false, ""
	}

	// Determine best quality from results
	bestQual := ""
	for _, e := range entries {
		switch e.Quality {
		case "2160p":
			bestQual = "4K"
		case "1080p":
			if bestQual != "4K" {
				bestQual = "FHD"
			}
		case "720p":
			if bestQual == "" {
				bestQual = "HD"
			}
		}
		if bestQual == "4K" {
			break
		}
	}

	return true, bestQual
}

// ---------------------------------------------------------------------------
//  GET /lite/pidtor — search torrents
// ---------------------------------------------------------------------------

// pidtorFetchEntries queries torrent indexers for entries, first non-empty
// answer wins. Chain: local jacred (when [parser] jacred_local is up) →
// RedAPI → external JacRed. All three speak the same Jackett-compatible
// /api/v2.0/indexers/all/results format.
func pidtorFetchEntries(r *http.Request, cfg config.Config, client *http.Client,
	title, originalTitle, year, serial, origLang string, init config.PidTorSource) []pidtorEntry {

	// Trackers spell titles in plain ASCII: "Déjà Vu" finds DJVU e-books, "Deja Vu"
	// finds the film (see stripLatinDiacritics).
	title = stripLatinDiacritics(title)
	originalTitle = stripLatinDiacritics(originalTitle)

	// Build is_serial param: ja→5, serial=1→2, else→1
	isSerial := "1"
	if origLang == "ja" {
		isSerial = "5"
	} else if serial == "1" {
		isSerial = "2"
	}

	// query hits one indexer host and returns the filtered entries (nil on
	// transport error). Some hosts (e.g. jacred.stream behind Cloudflare)
	// reject keyless requests with 403, so apikey is appended when set.
	query := func(apiLabel, base, apikey string) []pidtorEntry {
		apiURL := fmt.Sprintf("%s/api/v2.0/indexers/all/results?title=%s&title_original=%s&year=%s&is_serial=%s",
			strings.TrimRight(base, "/"),
			url.QueryEscape(title),
			url.QueryEscape(originalTitle),
			url.QueryEscape(year),
			isSerial,
		)
		if apikey != "" {
			apiURL += "&apikey=" + url.QueryEscape(apikey)
		}

		req, err := http.NewRequestWithContext(r.Context(), "GET", apiURL, nil)
		if err != nil {
			return nil
		}
		// Same browser UA the /api/v2.0 jacred proxy sends — jacred.stream sits
		// behind Cloudflare and 403s tool-looking agents.
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
		resp, err := client.Do(req)
		if err != nil {
			log.Warn().Err(err).Str("api", apiLabel).Str("host", base).Msg("pidtor: indexer request failed")
			return nil
		}
		defer resp.Body.Close()

		// Cap at 16 MB — torrent indexer JSON can be large (many results).
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		status := resp.StatusCode
		var root pidtorRedAPIRoot
		parseErr := json.Unmarshal(body, &root)
		entries := pidtorFilterResults(root.Results, init, serial == "1")
		// Decisive diagnostic for "pidtor finds nothing": status<400 + raw=0 → indexer has
		// no match (title/apikey); raw>0 + filtered=0 → MinSeeders/quality/size filter ate
		// them all; status>=400 → indexer rejected us (bad apikey / egress / Cloudflare).
		log.Debug().Str("api", apiLabel).Str("host", base).Int("status", status).
			Int("bytes", len(body)).Bool("parse_ok", parseErr == nil).Int("raw", len(root.Results)).
			Int("filtered", len(entries)).Int("min_seeders", init.MinSeeders).Bool("force_all", init.ForceAll).
			Str("title", title).Str("year", year).Msg("pidtor: search")
		return entries
	}

	// --- RuTracker (native, merged) --- runs alongside the jacred chain
	// instead of inside it: rutracker is never present in a jacred base, so
	// "first non-empty wins" would either hide it behind rutor or hide rutor
	// behind it. Started first so its listing request overlaps the chain.
	rutrackerCh := pidtorRutrackerEntries(r, cfg, title, originalTitle, serial, init)

	entries := pidtorJacredChain(r, cfg, init, query)

	if rutrackerCh != nil {
		if extra := <-rutrackerCh; len(extra) > 0 {
			entries = pidtorMergeEntries(entries, extra)
		}
	}
	return entries
}

// pidtorJacredChain is the historical "first non-empty answer wins" cascade:
// local jacred (when up) → RedAPI → external JacRed. The second parser
// ([parser] jacred_host2, the one the torrent browser already merges in) is
// queried IN PARALLEL and its rows are merged into whatever the cascade
// found — so a rate-limited primary (prod 2026-08-23: jac.red answered 429
// to half the searches the minute pidtor was switched on) no longer blanks
// the source. A jacred_host identical to RedAPI is not asked twice.
func pidtorJacredChain(r *http.Request, cfg config.Config, init config.PidTorSource,
	query func(apiLabel, base, apikey string) []pidtorEntry) []pidtorEntry {

	var secondCh chan []pidtorEntry
	if host2 := strings.TrimRight(strings.TrimSpace(cfg.Parser.JacRedHost2), "/"); host2 != "" &&
		!pidtorSameIndexer(host2, init.RedAPI) && !pidtorSameIndexer(host2, cfg.Parser.JacRedHost) {
		secondCh = make(chan []pidtorEntry, 1)
		go func() { secondCh <- query("jacred2", host2, strings.TrimSpace(cfg.Parser.JacRedKey2)) }()
	}
	// merge drains the second parser (every return path goes through it) and
	// appends its rows behind the primary's — primary wins on duplicate btih.
	merge := func(primary []pidtorEntry) []pidtorEntry {
		if secondCh == nil {
			return primary
		}
		return pidtorMergeEntries(primary, <-secondCh)
	}

	// --- Local jacred first (0.5) --- an empty answer falls through: the
	// local base may still be bootstrapping/parsing its first trackers.
	if mgr := liveJacredMgr(); cfg.Parser.JacRedLocal && mgr != nil && mgr.Healthy() {
		if entries := query("jacred-local", mgr.BaseURL(), strings.TrimSpace(cfg.Parser.JacRedKey)); len(entries) > 0 {
			return merge(entries)
		}
	}

	// --- RedAPI ---
	if init.RedAPI != "" {
		if entries := query("redapi", init.RedAPI, init.APIKey); len(entries) > 0 {
			return merge(entries)
		}
	}

	// --- External JacRed fallback (skipped when it IS the RedAPI host) ---
	jacHost := strings.TrimSpace(cfg.Parser.JacRedHost)
	if jacHost == "" || pidtorSameIndexer(jacHost, init.RedAPI) {
		return merge(nil)
	}
	return merge(query("jacred", jacHost, strings.TrimSpace(cfg.Parser.JacRedKey)))
}

// pidtorSameIndexer reports whether two indexer base URLs point at the same
// host (scheme/trailing-slash/case insensitive) — asking it twice only burns
// its rate limit.
func pidtorSameIndexer(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
		return strings.TrimRight(s, "/")
	}
	na, nb := norm(a), norm(b)
	return na != "" && na == nb
}

// pidtorRutrackerEntries kicks off the native search and returns a channel with
// the filtered entries, or nil when the source is off. Rows without a real
// magnet are dropped by pidtorFilterResults — deliberate: pidtor needs a btih
// to build its TorrServer stream URL, and the top rows are resolved before the
// search returns.
func pidtorRutrackerEntries(r *http.Request, cfg config.Config,
	title, originalTitle, serial string, init config.PidTorSource) <-chan []pidtorEntry {

	client := rutrackerReady(cfg)
	if client == nil || !rutrackerAllowed(r, cfg) {
		return nil
	}

	ch := make(chan []pidtorEntry, 1)
	go func() {
		defer close(ch)
		ctx, cancel := rutrackerCtx(r)
		defer cancel()

		rows := rutrackerSearch(ctx, client, title, originalTitle)
		if len(rows) == 0 {
			return
		}
		base := hostFromRequest(r)
		results := make([]pidtorResult, 0, len(rows))
		for _, row := range rows {
			m := rutrackerRowV2(row, base)
			res := pidtorResult{
				Tracker:   "rutracker",
				Title:     row.Title,
				Size:      row.SizeBytes,
				Seeders:   row.Seeders,
				MagnetUri: row.Magnet, // parselinks are useless here: no btih
				Info: pidtorInfo{
					SizeName: row.SizeName,
					Seasons:  rutrackerSeasons(row.Title),
				},
			}
			if pd, ok := m["PublishDate"].(string); ok {
				res.PublishDate = pd
			}
			results = append(results, res)
		}
		entries := pidtorFilterResults(results, init, serial == "1")
		log.Debug().Str("api", "rutracker").Int("raw", len(results)).
			Int("filtered", len(entries)).Str("title", title).Msg("pidtor: search")
		ch <- entries
	}()
	return ch
}

// pidtorMergeEntries appends extra entries, skipping magnets already present.
func pidtorMergeEntries(base, extra []pidtorEntry) []pidtorEntry {
	seen := make(map[string]bool, len(base))
	for _, e := range base {
		if m := reMagnetHash.FindStringSubmatch(e.Magnet); len(m) > 1 {
			seen[strings.ToLower(m[1])] = true
		}
	}
	for _, e := range extra {
		m := reMagnetHash.FindStringSubmatch(e.Magnet)
		if len(m) > 1 && seen[strings.ToLower(m[1])] {
			continue
		}
		base = append(base, e)
	}
	return base
}

func pidtorIndex(w http.ResponseWriter, r *http.Request, cfg config.Config, client *http.Client) {
	q := r.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year := q.Get("year")
	serial := q.Get("serial")
	origLang := q.Get("original_language")
	seasonStr := q.Get("s")
	rjson := parseBoolParam(q.Get("rjson"))

	init := cfg.Online.PidTor
	host := hostFromRequest(r)
	// Route playback through the transcoder (so capi/alpac accepts PidTor and incompatible codecs
	// play anywhere). Only when both the source opt-in and the transcoding service are on.
	transcode := pidtorTranscodeFor(r, cfg)

	// Cache key
	memKey := fmt.Sprintf("pidtor:%s:%s:%s", title, originalTitle, year)

	entries, ok := pidtorCacheGet(memKey)
	if !ok {
		entries = pidtorFetchEntries(r, cfg, client, title, originalTitle, year, serial, origLang, init)
		// Cache ONLY non-empty results. A zero-result search is almost always
		// transient for a torrent source (jacred hiccup, an id-only/no-title
		// probe, datacenter rate-limit) — caching it would pin pidtor at 0 for
		// the whole 5-min TTL for every user. Letting empties fall through means
		// the next request with a real title re-searches and self-heals.
		if len(entries) > 0 {
			pidtorCacheSet(memKey, entries)
		}
	}

	if len(entries) == 0 {
		// Canonical empty lite answer: {} for rjson (capi parses it as a valid
		// "no content" → probeEmpty), "" for HTML. A bare "" under rjson made
		// capi count every empty card as a SOURCE FAILURE — five in a row and
		// the checksearch circuit breaker hid pidtor from everyone for 30s
		// (prod 2026-08-23, the minute pidtor was re-enabled).
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{})
		} else {
			writeHTML(w, http.StatusOK, "")
		}
		return
	}

	encTitle := url.QueryEscape(title)
	encOrigTitle := url.QueryEscape(originalTitle)

	// Sort: Дубляж first, then has voice, then has trackers, then by sort preference
	sorted := make([]pidtorEntry, len(entries))
	copy(sorted, entries)
	pidtorSortEntries(sorted, init.Sort)

	if serial == "1" {
		s := -1
		if seasonStr != "" {
			fmt.Sscanf(seasonStr, "%d", &s)
		}

		if s == -1 {
			// Return season list
			pidtorWriteSeasons(w, sorted, host, encTitle, encOrigTitle, year, origLang, rjson)
		} else {
			// Return voice/quality list for season
			pidtorWriteSeasonVoices(w, sorted, host, encTitle, encOrigTitle, s, rjson)
		}
	} else {
		// Movie mode
		pidtorWriteMovie(w, sorted, host, transcode, rjson)
	}
}

// ---------------------------------------------------------------------------
//  GET /lite/pidtor/serial/{hash} — episode list from TorrServer
// ---------------------------------------------------------------------------

func pidtorSerial(w http.ResponseWriter, r *http.Request, cfg config.Config, tsClient *http.Client, hash string, links *proxylink.Manager) {
	q := r.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	seasonStr := q.Get("s")
	rjson := parseBoolParam(q.Get("rjson"))

	host := hostFromRequest(r)
	transcode := pidtorTranscodeFor(r, cfg)

	// Build tracker QS from query params
	trQS := pidtorExtractTRFromQuery(r.URL.RawQuery)

	memKey := fmt.Sprintf("pidtor:serial:%s", hash)
	files, ok := pidtorSerialCacheGet(memKey)
	if !ok {
		magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s&%s", hash, trQS)

		_, err := pidtorWithTS(r.Context(), cfg, q.Get("account_email"), hash, func(t pidtorTSTarget) error {
			var ferr error
			files, ferr = pidtorTSGetFiles(r.Context(), tsClient, t.host, t.headers, magnet, hash)
			return ferr
		})
		if err != nil {
			log.Warn().Err(err).Str("hash", hash).Msg("pidtor: TorrServer get files failed")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		pidtorSerialCacheSet(memKey, files)
	}

	// Build episode list
	data := make([]map[string]any, 0, len(files))
	labels := make([]string, 0, len(files))
	seasons := make([]int, 0, len(files))
	episodes := make([]int, 0, len(files))

	var s int
	fmt.Sscanf(seasonStr, "%d", &s)

	baseTitle := getsTVJoinName(title, originalTitle)
	epIdx := 0
	for _, f := range files {
		ext := strings.ToLower(path.Ext(f.Path))
		if ext == ".srt" || ext == ".txt" || ext == ".jpg" || ext == ".png" {
			continue
		}
		epIdx++
		fileName := path.Base(f.Path)

		streamURL := pidtorClientStreamURL(host, fmt.Sprintf("%s/lite/pidtor/s%s?%s&tsid=%d&fn=%s",
			host, hash, trQS, f.ID, url.QueryEscape(fileName)), transcode)

		row := map[string]any{
			"method": "play",
			"url":    streamURL,
			"stream": streamURL,
			"s":      s,
			"e":      epIdx,
			"name":   fileName,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, fileName),
		}
		data = append(data, row)
		labels = append(labels, fileName)
		seasons = append(seasons, s)
		episodes = append(episodes, epIdx)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": data})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
//  GET /lite/pidtor/s{hash} — stream redirect
// ---------------------------------------------------------------------------

func pidtorStream(w http.ResponseWriter, r *http.Request, cfg config.Config, tsClient *http.Client, hash string, links *proxylink.Manager) {
	q := r.URL.Query()
	accountEmail := q.Get("account_email")

	init := cfg.Online.PidTor

	tsid := 1
	if v := q.Get("tsid"); v != "" {
		fmt.Sscanf(v, "%d", &tsid)
	}

	// fn is the filename hint Lampa serial flow passes through so the proxy
	// URL ends with .mkv/.mp4 — without it, external players (Vimu) fail to
	// determine the container. For movie flow (no fn in query) we resolve
	// asynchronously from TorrServer metadata after AddMagnet.
	fn := strings.TrimSpace(q.Get("fn"))

	trQS := pidtorExtractTRFromQuery(r.URL.RawQuery)
	magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s&%s", hash, trQS)

	autoRemove := pidtorAutoRemoveDelay(cfg)
	maxActive := init.MaxActiveTorrents

	// Determine TorrServer host and auth
	if (len(init.Torrs) == 0) && (len(init.AuthTorrs) == 0) {
		// Local TorrServer / TS-balancer pool (sticky by infohash). Add the
		// magnet; a pool backend that fails at the transport level is
		// quarantined and the add is retried ONCE on the next backend.
		var tsHash string
		t, err := pidtorWithTS(r.Context(), cfg, accountEmail, hash, func(t pidtorTSTarget) error {
			var aerr error
			tsHash, aerr = pidtorTSAddMagnet(r.Context(), tsClient, t.host, t.headers, magnet)
			return aerr
		})
		if err != nil {
			log.Warn().Err(err).Str("hash", hash).Msg("pidtor: TorrServer add magnet failed")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		tsHost, tsHeaders := t.host, t.headers
		pidtorTrackOwn(tsHost, tsHash)

		go pidtorEnforceMaxActive(tsClient, tsHost, tsHash, tsHeaders, maxActive)

		if fn == "" {
			fn = pidtorResolveFilename(r.Context(), tsClient, tsHost, tsHeaders, tsHash, tsid)
		}

		// Build internal TorrServer URL and wrap through /proxy/. The chosen
		// backend's auth must ride INSIDE the proxy token — a pool backend has
		// its own credentials (and a static TorrServer may too), else /proxy
		// fetches the stream anonymously and gets 401.
		tsStreamURL := pidtorBuildStreamURL(tsHost, tsHash, tsid, fn)
		var proxyURL string
		if len(tsHeaders) > 0 {
			proxyURL = streamProxyURLWithHeaders(r, tsStreamURL, "pidtor", links, tsHeaders)
		} else {
			proxyURL = streamProxyURL(r, tsStreamURL, "pidtor", links)
		}
		pidtorScheduleRemove(tsClient, tsHost, tsHash, tsHeaders, autoRemove)
		http.Redirect(w, r, proxyURL, http.StatusFound)
		return
	}

	if len(init.AuthTorrs) > 0 {
		// Use auth_torrs
		ts := pidtorPickAuthTorr(init.AuthTorrs, "")
		if ts == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		login := strings.ReplaceAll(ts.Login, "{account_email}", accountEmail)
		headers := pidtorBuildAuthHeaders(login, ts.Password, ts.Headers)

		tsHash, err := pidtorTSAddMagnet(r.Context(), tsClient, ts.Host, headers, magnet)
		if err != nil {
			log.Warn().Err(err).Msg("pidtor: auth TorrServer add magnet failed")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		pidtorTrackOwn(ts.Host, tsHash)

		go pidtorEnforceMaxActive(tsClient, ts.Host, tsHash, headers, maxActive)

		if fn == "" {
			fn = pidtorResolveFilename(r.Context(), tsClient, ts.Host, headers, tsHash, tsid)
		}

		tsStreamURL := pidtorBuildStreamURL(ts.Host, tsHash, tsid, fn)
		proxyURL := streamProxyURLWithHeaders(r, tsStreamURL, "pidtor", links, headers)
		pidtorScheduleRemove(tsClient, ts.Host, tsHash, headers, autoRemove)
		http.Redirect(w, r, proxyURL, http.StatusFound)
		return
	}

	// torrs without auth (or with base_auth)
	tsHost := init.Torrs[time.Now().UnixNano()%int64(len(init.Torrs))]

	if init.BaseAuth != nil && init.BaseAuth.Enable {
		login := strings.ReplaceAll(init.BaseAuth.Login, "{account_email}", accountEmail)
		headers := pidtorBuildAuthHeaders(login, init.BaseAuth.Password, init.BaseAuth.Headers)

		tsHash, err := pidtorTSAddMagnet(r.Context(), tsClient, tsHost, headers, magnet)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		pidtorTrackOwn(tsHost, tsHash)

		go pidtorEnforceMaxActive(tsClient, tsHost, tsHash, headers, maxActive)

		if fn == "" {
			fn = pidtorResolveFilename(r.Context(), tsClient, tsHost, headers, tsHash, tsid)
		}

		tsStreamURL := pidtorBuildStreamURL(tsHost, tsHash, tsid, fn)
		proxyURL := streamProxyURLWithHeaders(r, tsStreamURL, "pidtor", links, headers)
		pidtorScheduleRemove(tsClient, tsHost, tsHash, headers, autoRemove)
		http.Redirect(w, r, proxyURL, http.StatusFound)
		return
	}

	// No auth, just redirect through proxy. No AddMagnet call here, so we
	// can't probe TorrServer for filenames (no auth handshake) — rely on
	// the Lampa-supplied fn from serial flow only.
	tsStreamURL := pidtorBuildStreamURL(tsHost, magnet, tsid, fn)
	proxyURL := streamProxyURL(r, tsStreamURL, "pidtor", links)
	// magnet (full magnet URI) is what we'd "remove", but TorrServer expects
	// the infohash; for magnet-only mode (no AddMagnet round-trip) we extract
	// the btih hash. enforceMaxActive doesn't need this since we don't know
	// our own hash deterministically — best-effort with empty keep.
	if m := reMagnetHash.FindStringSubmatch(magnet); len(m) == 2 {
		pidtorTrackOwn(tsHost, m[1])
		go pidtorEnforceMaxActive(tsClient, tsHost, m[1], nil, maxActive)
		pidtorScheduleRemove(tsClient, tsHost, m[1], nil, autoRemove)
	}
	http.Redirect(w, r, proxyURL, http.StatusFound)
}

// ---------------------------------------------------------------------------
//  RedAPI filtering
// ---------------------------------------------------------------------------

var (
	reQuality4K  = regexp.MustCompile(`(?i)(4k|uhd)( |\]|,|$)`)
	reMagnetHash = regexp.MustCompile(`(?i)magnet:\?xt=urn:btih:([a-zA-Z0-9]+)`)
	reMagnetTR   = regexp.MustCompile(`[&?]tr=([^&?]+)`)
	reHDR10      = regexp.MustCompile(`(?i)HDR10|10-?bit`)
	reHDR        = regexp.MustCompile(`(?i)HDR`)
	reHEVC       = regexp.MustCompile(`(?i)HEVC|H\.265`)
	reDolbyV     = regexp.MustCompile(`(?i)Dolby.?Vision`)
	reVoiceDub   = regexp.MustCompile(`(?i)( дб| d|дубляж)`)
	reVoiceMulti = regexp.MustCompile(`(?i)( ст| пм)`)
)

func pidtorFilterResults(results []pidtorResult, init config.PidTorSource, isSerial bool) []pidtorEntry {
	var filterRe, filterIgnoreRe *regexp.Regexp
	if init.Filter != "" {
		filterRe, _ = regexp.Compile("(?i)" + init.Filter)
	}
	if init.FilterIgnore != "" {
		filterIgnoreRe, _ = regexp.Compile("(?i)" + init.FilterIgnore)
	}

	entries := make([]pidtorEntry, 0, len(results))

	for _, t := range results {
		magnet := t.MagnetUri
		name := t.Title
		if magnet == "" || name == "" {
			continue
		}

		if strings.EqualFold(t.Tracker, "selezen") {
			continue
		}

		// Size filter
		if init.MaxSerialSize > 0 && init.MaxSize > 0 {
			if isSerial {
				if t.Size > init.MaxSerialSize {
					continue
				}
			} else {
				if t.Size > init.MaxSize {
					continue
				}
			}
		} else if init.MaxSize > 0 && t.Size > init.MaxSize {
			continue
		}

		// Quality filter
		nameLower := strings.ToLower(name)
		if !init.ForceAll {
			if !reQuality4K.MatchString(nameLower) &&
				!strings.Contains(nameLower, "2160p") &&
				!strings.Contains(nameLower, "1080p") &&
				!strings.Contains(nameLower, "720p") {
				continue
			}
		}

		if t.Seeders < init.MinSeeders {
			continue
		}

		// MediaInfo
		mediainfo := ""
		if t.Info.SizeName != "" {
			mediainfo = t.Info.SizeName + " / "
		}

		// Voice detection
		voice := ""
		if len(t.Info.Voices) > 0 {
			voice = strings.Join(t.Info.Voices, ", ")
		}

		if voice == "" {
			voice = pidtorDetectVoice(name, t.Tracker)
		}

		if !init.EmptyVoice && voice == "" {
			continue
		}

		// HDR / HEVC / Dolby Vision
		if reHDR10.MatchString(name) {
			mediainfo += " HDR10 "
		} else if reHDR.MatchString(name) {
			mediainfo += " HDR "
		} else {
			mediainfo += " SDR "
		}
		if reHEVC.MatchString(name) {
			mediainfo += " / H.265 "
		}
		if reDolbyV.MatchString(name) {
			mediainfo += " / Dolby Vision "
		}

		// Extract tracker params from magnet
		trQS := pidtorExtractTR(magnet)

		// Quality
		quality := "720p"
		if strings.Contains(name, "2160p") || reQuality4K.MatchString(nameLower) {
			quality = "2160p"
		} else if strings.Contains(name, "1080p") {
			quality = "1080p"
		}

		// Custom filters
		if filterRe != nil && !filterRe.MatchString(name+":"+voice) {
			continue
		}
		if filterIgnoreRe != nil && filterIgnoreRe.MatchString(name+":"+voice) {
			continue
		}

		entries = append(entries, pidtorEntry{
			Name:      name,
			Voice:     voice,
			Magnet:    magnet,
			Seeders:   t.Seeders,
			TrackerQS: trQS,
			Quality:   quality,
			Size:      t.Size,
			MediaInfo: mediainfo,
			Torrent:   t,
		})
	}

	return entries
}

func pidtorExtractTR(magnet string) string {
	matches := reMagnetTR.FindAllStringSubmatch(magnet, -1)
	if len(matches) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, m := range matches {
		v := strings.TrimSpace(m[1])
		if v == "" {
			continue
		}
		if strings.Contains(v, "/") || strings.Contains(v, ":") {
			sb.WriteString("&tr=" + url.QueryEscape(v))
		} else {
			sb.WriteString("&tr=" + v)
		}
	}
	s := sb.String()
	if len(s) > 0 {
		s = s[1:] // strip leading &
	}
	return s
}

func pidtorExtractTRFromQuery(rawQuery string) string {
	var sb strings.Builder
	for part := range strings.SplitSeq(rawQuery, "&") {
		if strings.HasPrefix(part, "tr=") {
			if sb.Len() > 0 {
				sb.WriteByte('&')
			}
			sb.WriteString(part)
		}
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
//  Voice detection
// ---------------------------------------------------------------------------

var pidtorKnownVoices = []string{
	"Movie Dubbing", "Bravo Records", "Ozz", "Laci", "Kerob", "LE-Production",
	"Parovoz Production", "Paradox", "Omskbird", "LostFilm", "Причудики",
	"BaibaKo", "NewStudio", "AlexFilm", "FocusStudio", "Gears Media",
	"Jaskier", "ViruseProject", "Кубик в Кубе", "IdeaFilm", "Sunshine Studio",
	"Hamster Studio", "Сербин", "To4ka", "Кравец", "Victory-Films",
	"SNK-TV", "GladiolusTV", "Jetvis Studio", "ApofysTeam", "ColdFilm",
	"Agatha Studdio", "KinoView", "Jimmy J.", "Shadow Dub Project", "Amedia",
	"Red Media", "Selena International", "Гоблин", "Universal Russia", "Kiitos",
	"Paramount Comedy", "Кураж-Бамбей", "Студия Пиратского Дубляжа",
	"RecentFilms", "Alternative Production", "NEON Studio", "Колобок",
	"SDI Media", "GreenРай Studio", "Михалев", "ДТВ", "Sunshine Studio",
	"LevshaFilm", "CasStudio", "Володарский", "ColdFilm", "Gravi-TV",
	"1001cinema", "Zone Vision Studio", "Murzilka", "turok1990", "FOX",
	"STEPonee", "Elrom", "HighHopes", "SoftBox", "NovaFilm",
	"Четыре в квадрате", "MUZOBOZ", "ZM-Show", "RecentFilms", "Hamster Studio",
	"New Dream Media", "Игмар", "Котов", "DeadLine Studio", "РенТВ",
	"Trdlo.studio", "Ozeon", "НТВ", "CP Digital", "AniLibria",
	"Levelin", "FanStudio", "Интерфильм", "SunshineStudio", "Kulzvuk Studio",
	"AzOnFilm", "SorzTeam", "DeeAFilm Studio", "zamez", "Иванов",
	"СВ-Дубль", "BadBajo", "Комедия ТВ", "Мастер Тэйп", "RusFilm",
	"XDUB Dorama", "Кansai", "Sound-Group", "Сыендук", "GoldTeam",
	"Dream Records", "Vano", "SilverSnow", "Lord32x", "Filiza Studio",
	"Flux-Team", "NewStation", "DexterTV", "Good People", "Levelin",
	"AniDUB", "SHIZA Project", "AniLibria.TV", "StudioBand", "AniMedia",
	"Kansai", "Onibaku", "JWA Project", "MC Entertainment", "Ancord",
	"ANIvoice", "Nika Lenina", "Cuba77", "KinoGolos", "Fox Crime",
	"AniFilm", "Rain Death", "New Records", "Profix Media", "Tycoon",
	"RealFake", "HDrezka", "AlexFilm", "Discovery", "ShowJet",
	"GREEN TEA", "AlphaProject", "AnimeReactor", "Animegroup", "Persona99",
	"CactusTeam", "AniMaunt", "Voice Project", "VoicePower",
	"FireDub", "AveTurk", "Superbit", "AveDorama", "Cinema Prestige",
	"Amazing Dubbing", "LakeFilms", "Невафильм", "Hallmark",
	"Netflix", "Mallorn Studio", "East Dream", "Bonsai Studio",
	"Lucky Production", "Octopus", "TUMBLER Studio", "Cowabunga Studio",
	"VO-Production", "Sound Film", "Nickelodeon", "MixFilm",
	"Back Board Cinema", "OnisFilms", "Neo-Sound", "Elysium",
	"CoralMedia", "Студия Райдо", "True Dubbing Studio", "IVI",
	"RedDiamond Studio", "Creative Sound", "CLS Media",
	"Intra Communications", "Pazl Voice",
}

func pidtorDetectVoice(name string, tracker string) string {
	var parts []string
	nameLower := strings.ToLower(name)

	if reVoiceDub.MatchString(nameLower) {
		parts = append(parts, "Дубляж")
	}
	if reVoiceMulti.MatchString(nameLower) {
		parts = append(parts, "Многоголосый")
	}

	trackerLower := strings.ToLower(tracker)
	if trackerLower == "lostfilm" {
		parts = append(parts, "LostFilm")
	} else if trackerLower == "toloka" {
		parts = append(parts, "Украинский")
	} else {
		for _, v := range pidtorKnownVoices {
			if len(v) > 4 && strings.Contains(nameLower, strings.ToLower(v)) {
				parts = append(parts, v)
			}
		}
	}

	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
//  Sorting
// ---------------------------------------------------------------------------

func pidtorSortEntries(entries []pidtorEntry, sortMode string) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]

		// Дубляж first
		aHasDub := strings.Contains(a.Voice, "Дубляж")
		bHasDub := strings.Contains(b.Voice, "Дубляж")
		if aHasDub != bHasDub {
			return aHasDub
		}

		// Has voice > no voice
		aHasVoice := a.Voice != ""
		bHasVoice := b.Voice != ""
		if aHasVoice != bHasVoice {
			return aHasVoice
		}

		// Has trackers > no trackers
		aHasTR := strings.Contains(a.Magnet, "&tr=")
		bHasTR := strings.Contains(b.Magnet, "&tr=")
		if aHasTR != bHasTR {
			return aHasTR
		}

		switch sortMode {
		case "size":
			return a.Size > b.Size
		case "sid":
			return a.Seeders > b.Seeders
		default:
			return a.Torrent.PublishDate > b.Torrent.PublishDate
		}
	})
}

// ---------------------------------------------------------------------------
//  Template writers
// ---------------------------------------------------------------------------

// pidtorTranscodeFor decides whether THIS request gets the transcode wrapper.
// A /capi drill always does when [online.pidtor] transcode is on (capi only
// accepts media-looking URLs — see pidtorClientStreamURL). A plain Lampa
// request gets it only with transcode_lampa: prod 2026-08-23 — Lampa web's
// hls.js (10s manifest timeout, one XHR for start+redirect) gave up on the
// box's cold-torrent start (~10-20s) and, lacking a caps= hint, HEVC went
// through a full sw re-encode; the direct resolve URL (→ /proxy mkv) is what
// Lampa players, TVs first of all, handle best.
func pidtorTranscodeFor(r *http.Request, cfg config.Config) bool {
	init := cfg.Online.PidTor
	if !init.Transcode || !cfg.Transcoding.Enable {
		return false
	}
	return capiResolveRequest(r) || init.TranscodeLampa
}

// pidtorClientStreamURL wraps a per-torrent resolve URL (/lite/pidtor/s...) for the client/capi.
// With transcode on it returns a /transcoding/start.m3u8?src=... URL: this both ends in .m3u8 (so
// capi's stream filter accepts PidTor — the bare resolve URL is otherwise dropped, hiding PidTor from
// the alpac flow) and routes playback through the server-side transcoder (native-vs-transcode per
// client; H.264 passes through untouched, HEVC/10-bit is re-encoded). Off → the resolve URL unchanged.
func pidtorClientStreamURL(host, resolveURL string, transcode bool) string {
	if !transcode {
		return resolveURL
	}
	// Points at the dedicated transcode box (signed) when [transcoding] remote_host is set, else local.
	return transcodesvc.StartURL(host, resolveURL)
}

func pidtorWriteMovie(w http.ResponseWriter, entries []pidtorEntry, host string, transcode, rjson bool) {
	data := make([]map[string]any, 0, len(entries))
	labels := make([]string, 0, len(entries))

	for _, e := range entries {
		hashMatch := reMagnetHash.FindStringSubmatch(e.Magnet)
		if len(hashMatch) < 2 {
			continue
		}
		hashMagnet := strings.ToLower(hashMatch[1])

		streamURL := pidtorClientStreamURL(host, fmt.Sprintf("%s/lite/pidtor/s%s?%s", host, hashMagnet, e.TrackerQS), transcode)

		row := map[string]any{
			"method":     "play",
			"url":        streamURL,
			"stream":     streamURL,
			"name":       e.Voice,
			"voice_name": fmt.Sprintf("%s / %s / %d", e.Quality, e.MediaInfo, e.Seeders),
			"maxquality": e.Quality,
			"quality": map[string]any{
				e.Quality: streamURL,
			},
		}

		data = append(data, row)
		labels = append(labels, e.Voice)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": data})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func pidtorWriteSeasons(w http.ResponseWriter, entries []pidtorEntry, host, encTitle, encOrigTitle, year, origLang string, rjson bool) {
	seasonSet := make(map[int]bool)
	for _, e := range entries {
		for _, s := range e.Torrent.Info.Seasons {
			seasonSet[s] = true
		}
	}

	seasons := make([]int, 0, len(seasonSet))
	for s := range seasonSet {
		seasons = append(seasons, s)
	}
	sort.Ints(seasons)

	// Determine best quality for badge
	bestQual := "720p"
	for _, e := range entries {
		if e.Quality == "2160p" {
			bestQual = "2160p"
			break
		}
		if e.Quality == "1080p" {
			bestQual = "1080p"
		}
	}

	data := make([]map[string]any, 0, len(seasons))
	labels := make([]string, 0, len(seasons))

	for _, s := range seasons {
		link := fmt.Sprintf("%s/lite/pidtor?rjson=%s&title=%s&original_title=%s&year=%s&original_language=%s&serial=1&s=%d",
			host, getsTVBool(rjson), encTitle, encOrigTitle, url.QueryEscape(year), url.QueryEscape(origLang), s)

		name := fmt.Sprintf("%d сезон", s)
		data = append(data, map[string]any{
			"method": "link",
			"url":    link,
			"id":     s,
			"name":   name,
		})
		labels = append(labels, name)
	}

	_ = bestQual // quality is in the season badge if needed

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": data})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func pidtorWriteSeasonVoices(w http.ResponseWriter, entries []pidtorEntry, host, encTitle, encOrigTitle string, season int, rjson bool) {
	data := make([]map[string]any, 0)
	labels := make([]string, 0)

	for _, e := range entries {
		if e.Torrent.Info.Seasons == nil || len(e.Torrent.Info.Seasons) == 0 {
			continue
		}
		// Must contain this season AND be single-season torrent
		found := slices.Contains(e.Torrent.Info.Seasons, season)
		if !found || len(e.Torrent.Info.Seasons) != 1 {
			continue
		}

		hashMatch := reMagnetHash.FindStringSubmatch(e.Magnet)
		if len(hashMatch) < 2 {
			continue
		}
		hashMagnet := strings.ToLower(hashMatch[1])

		link := fmt.Sprintf("%s/lite/pidtor/serial/%s?%s&rjson=%s&title=%s&original_title=%s&s=%d",
			host, hashMagnet, e.TrackerQS, getsTVBool(rjson), encTitle, encOrigTitle, season)

		details := fmt.Sprintf("%s / %s / %d", e.Quality, e.MediaInfo, e.Seeders)

		data = append(data, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"title":   e.Voice,
			"details": details,
			"year":    0,
		})
		labels = append(labels, e.Voice)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "similar", "data": data})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
//  TorrServer communication
// ---------------------------------------------------------------------------

// pidtorGetTorrServer resolves the TorrServer host + auth headers for one
// torrent. hash is the torrent's infohash — the sticky routing key when the
// TS-balancer pool is active. Explicit [online.pidtor] torrs/auth_torrs lists
// always win (the operator configured pidtor-specific servers on purpose);
// otherwise the pool picks the same backend the /ts proxy and the transcoder
// would (HRW by infohash, honouring health/quarantine), so add → files →
// stream → remove of one torrent all land on ONE server — and pidtor load
// finally spreads across the pool instead of piling onto the static host.
func pidtorGetTorrServer(cfg config.Config, accountEmail, hash string) (string, map[string]string) {
	t, _ := pidtorPickTS(cfg, accountEmail, hash, nil)
	return t.host, t.headers
}

// pidtorTSTarget is one resolved TorrServer endpoint for a pidtor infohash.
// backend is set only for TS-balancer pool picks (the failover/quarantine
// handle); static config / torrs lists / in-process leave it nil.
type pidtorTSTarget struct {
	host    string
	headers map[string]string
	backend *torrbalancer.Backend
}

// pidtorPickTS resolves the TorrServer for hash. exclude lists pool backend
// IDs already tried (failover); ok=false means the pool is active but every
// allowed backend is excluded — there is nowhere left to retry.
func pidtorPickTS(cfg config.Config, accountEmail, hash string, exclude map[string]bool) (pidtorTSTarget, bool) {
	init := cfg.Online.PidTor
	ts := cfg.TorrServer

	// If auth_torrs or torrs configured, use first one
	if len(init.AuthTorrs) > 0 {
		t := init.AuthTorrs[0]
		login := strings.ReplaceAll(t.Login, "{account_email}", accountEmail)
		headers := pidtorBuildAuthHeaders(login, t.Password, t.Headers)
		return pidtorTSTarget{host: t.Host, headers: headers}, true
	}

	if len(init.Torrs) > 0 {
		host := init.Torrs[0]
		if init.BaseAuth != nil && init.BaseAuth.Enable {
			login := strings.ReplaceAll(init.BaseAuth.Login, "{account_email}", accountEmail)
			headers := pidtorBuildAuthHeaders(login, init.BaseAuth.Password, init.BaseAuth.Headers)
			return pidtorTSTarget{host: host, headers: headers}, true
		}
		return pidtorTSTarget{host: host}, true
	}

	// TS-balancer pool (nil when in-process / pool empty / disabled).
	var allow func(string) bool
	if len(exclude) > 0 {
		allow = func(id string) bool { return !exclude[id] }
	}
	if b := tsPoolBackendForAllow(hash, allow); b != nil {
		host, authHdr := b.Target()
		headers := map[string]string{}
		if authHdr != "" {
			headers["Authorization"] = authHdr
		}
		return pidtorTSTarget{host: strings.TrimRight(host, "/"), headers: headers, backend: b}, true
	} else if len(exclude) > 0 && tsPoolBackendFor(hash) != nil {
		// Pool is active, but the excluded set covers every eligible backend.
		return pidtorTSTarget{}, false
	}

	// In-process torrs: routes are on lampac-go itself at /ts/*.
	if torrsIsInProcess() {
		return pidtorTSTarget{host: "http://127.0.0.1" + cfg.Server.Addr + "/ts", headers: map[string]string{"X-Lampac-Go": "1"}}, true
	}

	// External TorrServer
	tsURL := fmt.Sprintf("http://127.0.0.1:%d", ts.Port)
	if ts.URL != "" {
		tsURL = ts.URL
	}

	if ts.Password != "" {
		return pidtorTSTarget{host: tsURL, headers: pidtorBuildAuthHeaders(ts.Login, ts.Password, nil)}, true
	}

	return pidtorTSTarget{host: tsURL}, true
}

// pidtorIsTransportErr tells a backend that did not answer (dial/reset/
// timeout — every http.Client.Do failure is a *url.Error) from one that
// answered something we could not use (401, odd JSON): only the former is a
// reason to quarantine it and fail over, exactly as the /ts proxy does.
func pidtorIsTransportErr(err error) bool {
	var ue *url.Error
	return errors.As(err, &ue)
}

// pidtorWithTS runs op against the sticky TorrServer for hash. When the pick
// is a TS-balancer pool backend and op fails at the transport level (in
// practice a half-wedged TorrServer: /echo alive, /torrents hanging), the
// backend is quarantined — same handling as the /ts proxy's
// torrentsAPIWithFailover — and op is retried ONCE on the next HRW backend,
// so a sick server doesn't kill the viewer's "открываем…". A viewer who
// went away (context canceled) is not the backend's fault. Returns the
// target op succeeded on.
func pidtorWithTS(ctx context.Context, cfg config.Config, accountEmail, hash string,
	op func(t pidtorTSTarget) error) (pidtorTSTarget, error) {

	tried := map[string]bool{}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		t, ok := pidtorPickTS(cfg, accountEmail, hash, tried)
		if !ok {
			break
		}
		err := op(t)
		if err == nil {
			return t, nil
		}
		lastErr = err
		if t.backend == nil || ctx.Err() != nil || !pidtorIsTransportErr(err) {
			break
		}
		tried[t.backend.ID] = true
		if pool := tsBalancerPoolRef; pool != nil {
			pool.MarkFailed(t.backend)
			pool.QuarantineWedged(t.backend, "pidtor: torrents API не ответил: "+err.Error())
		}
		log.Warn().Err(err).Str("backend", t.host).Str("hash", hash).
			Msg("pidtor: TorrServer backend failed — quarantined, retrying on another pool backend")
	}
	if lastErr == nil {
		lastErr = errors.New("no TorrServer backend available")
	}
	return pidtorTSTarget{}, lastErr
}

func pidtorBuildAuthHeaders(login, password string, extra map[string]string) map[string]string {
	h := make(map[string]string)
	if login != "" || password != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(login + ":" + password))
		h["Authorization"] = "Basic " + cred
	}
	maps.Copy(h, extra)
	return h
}

func pidtorPickAuthTorr(entries []config.PidTorAuthEntry, country string) *config.PidTorAuthEntry {
	eligible := make([]config.PidTorAuthEntry, 0, len(entries))
	for _, e := range entries {
		if !e.Enable {
			continue
		}
		if country != "" && e.Country != "" && !strings.Contains(e.Country, country) {
			continue
		}
		if country != "" && e.NoCountry != "" && strings.Contains(e.NoCountry, country) {
			continue
		}
		eligible = append(eligible, e)
	}
	if len(eligible) == 0 {
		return nil
	}
	pick := eligible[time.Now().UnixNano()%int64(len(eligible))]
	return &pick
}

func pidtorTSAddMagnet(ctx interface{ Deadline() (time.Time, bool) }, client *http.Client, tsHost string, headers map[string]string, magnet string) (string, error) {
	payload := fmt.Sprintf(`{"action":"add","link":"%s","title":"","poster":"","save_to_db":false}`, magnet)
	req, err := http.NewRequest("POST", tsHost+"/torrents", strings.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("add magnet: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))

	m := reMagnetHash.FindStringSubmatch(string(body))
	hashField := ""
	// Parse hash from response JSON
	var addResp struct {
		Hash string `json:"hash"`
	}
	if json.Unmarshal(body, &addResp) == nil && addResp.Hash != "" {
		hashField = addResp.Hash
	} else if m != nil && len(m) > 1 {
		hashField = m[1]
	}

	if hashField == "" {
		return "", fmt.Errorf("empty hash from TorrServer response: %s", string(body))
	}

	return hashField, nil
}

func pidtorTSGetFiles(ctx interface{ Deadline() (time.Time, bool) }, client *http.Client, tsHost string, headers map[string]string, magnet, hash string) ([]pidtorFileStat, error) {
	// First add the magnet
	tsHash, err := pidtorTSAddMagnet(ctx, client, tsHost, headers, magnet)
	if err != nil {
		return nil, err
	}

	// Poll for file_stats (up to 20 seconds)
	deadline := time.Now().Add(20 * time.Second)
	for {
		payload := fmt.Sprintf(`{"action":"get","hash":"%s"}`, tsHash)
		req, err := http.NewRequest("POST", tsHost+"/torrents", strings.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("get torrent: %w", err)
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()

		var stat pidtorTSStat
		if json.Unmarshal(body, &stat) == nil && len(stat.FileStats) > 0 {
			// Remove torrent from TS
			remPayload := fmt.Sprintf(`{"action":"rem","hash":"%s"}`, tsHash)
			remReq, _ := http.NewRequest("POST", tsHost+"/torrents", strings.NewReader(remPayload))
			if remReq != nil {
				remReq.Header.Set("Content-Type", "application/json")
				for k, v := range headers {
					remReq.Header.Set(k, v)
				}
				go func() {
					r, e := client.Do(remReq)
					if e == nil {
						r.Body.Close()
					}
				}()
			}
			return stat.FileStats, nil
		}

		if time.Now().After(deadline) {
			// Timeout — remove torrent and bail
			remPayload := fmt.Sprintf(`{"action":"rem","hash":"%s"}`, tsHash)
			remReq, _ := http.NewRequest("POST", tsHost+"/torrents", strings.NewReader(remPayload))
			if remReq != nil {
				remReq.Header.Set("Content-Type", "application/json")
				for k, v := range headers {
					remReq.Header.Set(k, v)
				}
				go func() {
					r, e := client.Do(remReq)
					if e == nil {
						r.Body.Close()
					}
				}()
			}
			return nil, fmt.Errorf("TorrServer file_stats timeout after 20s")
		}

		time.Sleep(250 * time.Millisecond)
	}
}

// pidtorScheduleRemove removes a torrent from TorrServer after a delay.
// This ensures torrents added for playback don't accumulate indefinitely.
// Default kept short so memory/disk on the TorrServer host doesn't balloon
// when the user opens many titles in a row. Override via
// [online.pidtor].auto_remove_delay_min in config.toml. Set to -1 to disable.
const pidtorDefaultAutoRemoveDelayMin = 60

func pidtorAutoRemoveDelay(cfg config.Config) time.Duration {
	d := cfg.Online.PidTor.AutoRemoveDelayMin
	if d == 0 {
		d = pidtorDefaultAutoRemoveDelayMin
	}
	if d < 0 {
		return 0 // disabled
	}
	return time.Duration(d) * time.Minute
}

// pidtorRemoveTorrent issues a synchronous "rem" against TorrServer.
func pidtorRemoveTorrent(client *http.Client, tsHost, tsHash string, headers map[string]string) {
	remPayload := fmt.Sprintf(`{"action":"rem","hash":"%s"}`, tsHash)
	req, err := http.NewRequest("POST", tsHost+"/torrents", strings.NewReader(remPayload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// pidtorPendingRemoves dedups overlapping schedule-remove timers and lets us
// cancel them when a torrent is removed (or replaced) early. Was: every call
// site spawned `go func() { time.Sleep(delay); … }()` which left ~hundreds of
// goroutines sleeping ~1h each (confirmed via pprof goroutine profile —
// 261 goroutines parked in pidtorScheduleRemove on a live server).
//
// time.AfterFunc uses the runtime timer heap; no goroutine is spawned until
// the timer fires (and even then only briefly).
var (
	pidtorPendingMu    sync.Mutex
	pidtorPendingTimer = map[string]*time.Timer{} // tsHost|tsHash → timer
)

func pidtorScheduleRemove(client *http.Client, tsHost, tsHash string, headers map[string]string, delay time.Duration) {
	if delay <= 0 {
		return
	}
	key := tsHost + "|" + tsHash

	pidtorPendingMu.Lock()
	if prev, ok := pidtorPendingTimer[key]; ok {
		prev.Stop() // replace any existing schedule for this hash
	}
	// Copy headers to insulate from caller mutations between schedule and fire.
	hdrCopy := make(map[string]string, len(headers))
	for k, v := range headers {
		hdrCopy[k] = v
	}
	pidtorPendingTimer[key] = time.AfterFunc(delay, func() {
		pidtorPendingMu.Lock()
		delete(pidtorPendingTimer, key)
		pidtorPendingMu.Unlock()

		pidtorRemoveTorrent(client, tsHost, tsHash, hdrCopy)
		pidtorUntrackOwn(tsHost, tsHash)
		log.Debug().Str("hash", tsHash).Msg("pidtor: auto-removed torrent after timeout")
	})
	pidtorPendingMu.Unlock()
}

// pidtorCancelScheduledRemove cancels any pending auto-remove timer for the
// given torrent. Safe to call when no timer exists. Used when a torrent is
// removed manually so we don't leak entries in pidtorPendingTimer.
func pidtorCancelScheduledRemove(tsHost, tsHash string) {
	key := tsHost + "|" + tsHash
	pidtorPendingMu.Lock()
	if t, ok := pidtorPendingTimer[key]; ok {
		t.Stop()
		delete(pidtorPendingTimer, key)
	}
	pidtorPendingMu.Unlock()
}

// pidtorListTorrents fetches the list of torrents currently held by a TorrServer
// host. Returns the entries sorted oldest-first (by Timestamp ascending).
type pidtorTSEntry struct {
	Hash      string `json:"hash"`
	Timestamp int64  `json:"timestamp"`
}

func pidtorListTorrents(client *http.Client, tsHost string, headers map[string]string) ([]pidtorTSEntry, error) {
	payload := `{"action":"list"}`
	req, err := http.NewRequest("POST", tsHost+"/torrents", strings.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var entries []pidtorTSEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Timestamp < entries[j].Timestamp })
	return entries, nil
}

// pidtorEnforceMaxActive drops the oldest torrents on a TorrServer host so that
// no more than maxActive torrents remain after the new one is added. Skips the
// torrent currently being added (keepHash). Best-effort; logs and returns on
// any error so playback isn't blocked by cleanup glitches.
// pidtorOwn remembers which torrents pidtor itself added on which TorrServer
// host, so max_active_torrents only ever evicts pidtor's OWN torrents. The
// TorrServer is shared (TS-balancer pool, the web/Android torrent browser,
// the transcode box all add torrents there) — prod 2026-08-23: every pidtor
// play dropped the two oldest torrents on the backend, whoever was watching
// them. Entries expire after pidtorOwnTTL (auto-remove fires long before).
var pidtorOwn = struct {
	sync.Mutex
	m map[string]map[string]time.Time // host → hash → added
}{m: map[string]map[string]time.Time{}}

const pidtorOwnTTL = 24 * time.Hour

func pidtorOwnKey(tsHost, hash string) (string, string) {
	return strings.TrimRight(strings.TrimSpace(tsHost), "/"), strings.ToLower(strings.TrimSpace(hash))
}

// pidtorTrackOwn records that pidtor added hash on tsHost.
func pidtorTrackOwn(tsHost, hash string) {
	host, h := pidtorOwnKey(tsHost, hash)
	if host == "" || h == "" {
		return
	}
	now := time.Now()
	pidtorOwn.Lock()
	defer pidtorOwn.Unlock()
	hm := pidtorOwn.m[host]
	if hm == nil {
		hm = map[string]time.Time{}
		pidtorOwn.m[host] = hm
	}
	hm[h] = now
	// Opportunistic expiry so the map stays bounded.
	for k, t := range hm {
		if now.Sub(t) > pidtorOwnTTL {
			delete(hm, k)
		}
	}
}

// pidtorUntrackOwn forgets hash on tsHost (after a remove).
func pidtorUntrackOwn(tsHost, hash string) {
	host, h := pidtorOwnKey(tsHost, hash)
	pidtorOwn.Lock()
	if hm := pidtorOwn.m[host]; hm != nil {
		delete(hm, h)
		if len(hm) == 0 {
			delete(pidtorOwn.m, host)
		}
	}
	pidtorOwn.Unlock()
}

// pidtorIsOwn reports whether pidtor added hash on tsHost (and it hasn't expired).
func pidtorIsOwn(tsHost, hash string) bool {
	host, h := pidtorOwnKey(tsHost, hash)
	pidtorOwn.Lock()
	defer pidtorOwn.Unlock()
	if hm := pidtorOwn.m[host]; hm != nil {
		if t, ok := hm[h]; ok && time.Since(t) <= pidtorOwnTTL {
			return true
		}
	}
	return false
}

// pidtorEnforceMaxActive keeps at most maxActive of pidtor's OWN torrents on
// tsHost (keepHash always survives): oldest-first eviction among the hashes
// pidtor added itself — never a torrent somebody else put on the shared server.
func pidtorEnforceMaxActive(client *http.Client, tsHost, keepHash string, headers map[string]string, maxActive int) {
	if maxActive <= 0 {
		return
	}
	all, err := pidtorListTorrents(client, tsHost, headers)
	if err != nil || len(all) == 0 {
		return
	}
	entries := make([]pidtorTSEntry, 0, len(all))
	for _, e := range all {
		if pidtorIsOwn(tsHost, e.Hash) {
			entries = append(entries, e)
		}
	}
	keep := strings.ToLower(strings.TrimSpace(keepHash))
	// Count torrents excluding keepHash, drop oldest until count <= maxActive-1.
	type rem struct{ hash string }
	var toRemove []rem
	excluding := 0
	for _, e := range entries {
		h := strings.ToLower(strings.TrimSpace(e.Hash))
		if h == keep || h == "" {
			continue
		}
		excluding++
	}
	overflow := excluding - (maxActive - 1)
	if overflow <= 0 {
		return
	}
	removed := 0
	for _, e := range entries {
		if removed >= overflow {
			break
		}
		h := strings.ToLower(strings.TrimSpace(e.Hash))
		if h == keep || h == "" {
			continue
		}
		toRemove = append(toRemove, rem{hash: e.Hash})
		removed++
	}
	for _, r := range toRemove {
		pidtorRemoveTorrent(client, tsHost, r.hash, headers)
		pidtorUntrackOwn(tsHost, r.hash)
		log.Debug().Str("host", tsHost).Str("hash", r.hash).Int("max", maxActive).Msg("pidtor: dropped oldest own torrent (max_active_torrents)")
	}
}
