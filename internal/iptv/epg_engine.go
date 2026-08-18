package iptv

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  EPG Engine — background loader + in-memory query
// ---------------------------------------------------------------------------

// EPGEngine loads and caches EPG data from XMLTV sources.
// Programs are stored in a sorted slice per channel for fast binary-search
// lookups (NowNext, Timeline).
type EPGEngine struct {
	mu         sync.RWMutex
	channels   map[string]EPGChannel   // xmltvID → channel metadata
	programs   map[string][]EPGProgram // xmltvID → sorted by Start
	nameToIcon map[string]string       // lowercased display-name → icon URL
	loadedAt   time.Time
	lastStatus EPGStatus // diagnostics from the last refresh (exposed via Status())

	urls           []string
	updateInterval time.Duration
	store          *Store // for extracting playlist-level EPG URLs
	httpClient     *http.Client
	cancel         context.CancelFunc
}

// EPGSourceStatus is the per-source outcome of the last refresh (diagnostics).
type EPGSourceStatus struct {
	URL      string `json:"url"`      // host + path tail only (never the full query — may carry a key)
	OK       bool   `json:"ok"`       // fetched + parsed without error
	Status   int    `json:"status"`   // HTTP status code (0 = never connected)
	Channels int    `json:"channels"` // <channel> entries parsed from this source
	Programs int    `json:"programs"` // <programme> entries kept (post-filter, post-window)
	Err      string `json:"err,omitempty"`
}

// EPGStatus is a browser-readable snapshot of the EPG engine's last refresh — surfaced at
// GET /api/iptv/epg/status so an empty guide can be diagnosed without server log access.
type EPGStatus struct {
	Channels      int               `json:"channels"`       // total channels currently held
	Programs      int               `json:"programs"`       // total programmes currently held
	LoadedAt      time.Time         `json:"loaded_at"`      // when the current data was committed
	KnownTvgIDs   int               `json:"known_tvg_ids"`  // playlist tvg-ids used as the load filter
	MatchedTvgIDs int               `json:"matched_tvg_ids"` // how many of those actually got programmes
	Filtered      bool              `json:"filtered"`       // tvg-id filter was applied this refresh
	SelfHealed    bool              `json:"self_healed"`    // filter dropped everything → reloaded unfiltered
	Sources       []EPGSourceStatus `json:"sources"`
}

// EPGConfig holds configuration for the EPG engine.
type EPGConfig struct {
	URLs           []string      // static EPG source URLs
	UpdateInterval time.Duration // how often to refresh; default 6h
}

// NewEPGEngine creates a new EPG engine.
func NewEPGEngine(store *Store, cfg EPGConfig) *EPGEngine {
	if cfg.UpdateInterval <= 0 {
		cfg.UpdateInterval = 6 * time.Hour
	}
	return &EPGEngine{
		channels:       make(map[string]EPGChannel),
		programs:       make(map[string][]EPGProgram),
		urls:           cfg.URLs,
		updateInterval: cfg.UpdateInterval,
		store:          store,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Start begins the background EPG update loop.
func (e *EPGEngine) Start(ctx context.Context) {
	ctx, e.cancel = context.WithCancel(ctx)

	// Initial load.
	go func() {
		e.refresh()

		ticker := time.NewTicker(e.updateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.refresh()
			}
		}
	}()
}

// Stop stops the background loop.
func (e *EPGEngine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
}

// ---------------------------------------------------------------------------
//  Query API
// ---------------------------------------------------------------------------

// NowNext returns the current and next program for each given XMLTV channel ID.
func (e *EPGEngine) NowNext(channelIDs []string, now time.Time) []EPGNowNext {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]EPGNowNext, 0, len(channelIDs))
	for _, id := range channelIDs {
		nn := EPGNowNext{ChannelID: id}
		progs := e.programs[id]
		if len(progs) > 0 {
			idx := e.findCurrentIdx(progs, now)
			if idx >= 0 {
				nn.Now = &progs[idx]
				if idx+1 < len(progs) {
					nn.Next = &progs[idx+1]
				}
			} else {
				// No current program; find the next upcoming.
				nextIdx := sort.Search(len(progs), func(i int) bool {
					return progs[i].Start.After(now)
				})
				if nextIdx < len(progs) {
					nn.Next = &progs[nextIdx]
				}
			}
		}
		result = append(result, nn)
	}
	return result
}

// Timeline returns programs for a channel within [from, to].
func (e *EPGEngine) Timeline(channelID string, from, to time.Time) []EPGProgram {
	e.mu.RLock()
	defer e.mu.RUnlock()

	progs := e.programs[channelID]
	if len(progs) == 0 {
		return nil
	}

	// Binary search for first program that overlaps with [from, to].
	// A program overlaps if prog.Stop > from AND prog.Start < to.
	startIdx := sort.Search(len(progs), func(i int) bool {
		return progs[i].Stop.After(from)
	})

	var result []EPGProgram
	for i := startIdx; i < len(progs); i++ {
		if progs[i].Start.After(to) || progs[i].Start.Equal(to) {
			break
		}
		result = append(result, progs[i])
	}
	return result
}

// Search searches program titles across all channels.
func (e *EPGEngine) Search(query string, limit int) []EPGProgram {
	if limit <= 0 {
		limit = 50
	}
	queryLower := strings.ToLower(query)

	e.mu.RLock()
	defer e.mu.RUnlock()

	now := time.Now().UTC()
	var results []EPGProgram
	for _, progs := range e.programs {
		for _, p := range progs {
			if p.Stop.Before(now) {
				continue // skip past programs
			}
			if strings.Contains(strings.ToLower(p.Title), queryLower) ||
				strings.Contains(strings.ToLower(p.Description), queryLower) {
				results = append(results, p)
				if len(results) >= limit {
					return results
				}
			}
		}
	}

	// Sort by start time.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Start.Before(results[j].Start)
	})
	return results
}

// GetChannel returns EPG channel metadata by XMLTV ID.
func (e *EPGEngine) GetChannel(xmltvID string) (EPGChannel, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ch, ok := e.channels[xmltvID]
	return ch, ok
}

// GetIconByName looks up a channel icon by display name (case-insensitive).
func (e *EPGEngine) GetIconByName(name string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.nameToIcon[strings.ToLower(strings.TrimSpace(name))]
}

// Stats returns EPG statistics.
func (e *EPGEngine) Stats() (channels, programs int, loadedAt time.Time) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	totalProgs := 0
	for _, progs := range e.programs {
		totalProgs += len(progs)
	}
	return len(e.channels), totalProgs, e.loadedAt
}

// Status returns a diagnostic snapshot of the last refresh (per-source results, filter outcome).
func (e *EPGEngine) Status() EPGStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastStatus
}

// ---------------------------------------------------------------------------
//  Internal: binary search helper
// ---------------------------------------------------------------------------

// findCurrentIdx returns the index of the program airing at time t,
// or -1 if none is currently airing.
func (e *EPGEngine) findCurrentIdx(progs []EPGProgram, t time.Time) int {
	// Binary search for the last program with Start <= t.
	idx := sort.Search(len(progs), func(i int) bool {
		return progs[i].Start.After(t)
	}) - 1

	if idx < 0 {
		return -1
	}

	// Check if the program is still airing (Stop > t).
	if progs[idx].Stop.After(t) {
		return idx
	}
	return -1
}

// ---------------------------------------------------------------------------
//  Internal: refresh from sources
// ---------------------------------------------------------------------------

func (e *EPGEngine) refresh() {
	// Collect all EPG URLs: static config + playlist-level x-tvg-url.
	// `pinned` are sources we must never drop: operator [iptv] epg_urls + the CURATED GLOBAL
	// playlist's own x-tvg-url list. The cap exists to bound UNBOUNDED user-playlist fan-in (many
	// users), NOT to starve the single curated playlist — which lists e.g. ~50 epgshare01 country
	// feeds, and a blind count-cap dropped the alphabetically-late RU feed, blanking every RU
	// channel's guide while keeping BG/BR/CY/DE/ID/ES.
	urls := make(map[string]struct{})
	pinned := append([]string(nil), e.urls...)
	for _, u := range e.urls {
		u = strings.TrimSpace(u)
		if u != "" {
			urls[u] = struct{}{}
		}
	}

	// Collect from playlists (if store available).
	if e.store != nil {
		e.store.mu.RLock()
		for _, cache := range e.store.cache {
			for _, u := range cache.Playlist.EPGUrls {
				u = strings.TrimSpace(u)
				if u == "" {
					continue
				}
				urls[u] = struct{}{}
				if cache.Playlist.IsGlobal {
					pinned = append(pinned, u) // curated global playlist → never capped
				}
			}
		}
		e.store.mu.RUnlock()
	}

	// Fallback: if no EPG URLs at all, use a popular public source that
	// provides logos and program data for most CIS channels.
	if len(urls) == 0 {
		urls["http://epg.it999.ru/edem.xml.gz"] = struct{}{}
	}

	// Cap only the unpinned (user-playlist) fan-in; pinned sources always survive.
	urls = capEPGSources(pinned, urls)

	log.Info().Int("sources", len(urls)).Msg("epg: refreshing")

	knownChannelIDs := e.collectKnownChannelIDs()
	stringsPool := newEPGStringPool()

	// Window: keep programs from -1 day to +2 days. Forward was +3d but now/next + the
	// forward timeline never need 3 days of schedule; -1d keeps catchup's recent slots.
	windowStart := time.Now().UTC().Add(-24 * time.Hour)
	windowEnd := time.Now().UTC().Add(2 * 24 * time.Hour)

	// load() fetches+parses every source into fresh maps; the per-known-channel filter is optional so
	// we can retry unfiltered if it strands everything.
	load := func(filter map[string]struct{}) (map[string]EPGChannel, map[string][]EPGProgram, int, []EPGSourceStatus) {
		chs := make(map[string]EPGChannel)
		progs := make(map[string][]EPGProgram)
		srcs := make([]EPGSourceStatus, 0, len(urls))
		for url := range urls {
			srcs = append(srcs, e.loadSource(url, windowStart, windowEnd, filter, stringsPool, chs, progs))
		}
		// Sort each channel's programmes by Start, then cap per channel: a backstop against a
		// pathological source publishing thousands of micro-programmes for one channel.
		total := 0
		for id := range progs {
			sort.Slice(progs[id], func(i, j int) bool { return progs[id][i].Start.Before(progs[id][j].Start) })
			if len(progs[id]) > epgMaxProgramsPerChannel {
				progs[id] = progs[id][:epgMaxProgramsPerChannel]
			}
			total += len(progs[id])
		}
		return chs, progs, total, srcs
	}

	newChannels, newPrograms, totalProgs, sources := load(knownChannelIDs)

	// Self-heal: the per-known-channel filter keeps only programmes whose XMLTV id EXACTLY matches a
	// playlist tvg-id. If the playlist's tvg-ids don't line up with the EPG source's channel ids, the
	// filter strands every programme and the guide + now/next go blank on ALL channels. When sources
	// parsed fine but nothing survived the filter, reload UNFILTERED so EPG still works (bounded by the
	// source cap + window + per-channel cap). Lookups by tvg-id still only hit what the source carries —
	// but at least matching-id channels and name-based icons come back instead of a total blackout.
	selfHealed := false
	if totalProgs == 0 && knownChannelIDs != nil && len(newChannels) > 0 {
		log.Warn().Int("channels", len(newChannels)).Msg("epg: 0 programmes after tvg-id filter — reloading unfiltered (playlist tvg-ids may not match EPG source)")
		newChannels, newPrograms, totalProgs, sources = load(nil)
		selfHealed = true
	}

	matched := 0
	for _, progs := range newPrograms {
		if len(progs) > 0 {
			matched++
		}
	}

	// Build name→icon index from all display names.
	newNameToIcon := make(map[string]string, len(newChannels)*3)
	for _, ch := range newChannels {
		if ch.Icon == "" {
			continue
		}
		for _, name := range ch.AltNames {
			key := strings.ToLower(strings.TrimSpace(name))
			if key != "" {
				if _, exists := newNameToIcon[key]; !exists {
					newNameToIcon[key] = ch.Icon
				}
			}
		}
	}

	status := EPGStatus{
		Channels:      len(newChannels),
		Programs:      totalProgs,
		LoadedAt:      time.Now(),
		KnownTvgIDs:   len(knownChannelIDs),
		MatchedTvgIDs: matched,
		Filtered:      knownChannelIDs != nil && !selfHealed,
		SelfHealed:    selfHealed,
		Sources:       sources,
	}

	e.mu.Lock()
	e.channels = newChannels
	e.programs = newPrograms
	e.nameToIcon = newNameToIcon
	e.loadedAt = status.LoadedAt
	e.lastStatus = status
	e.mu.Unlock()

	log.Info().Int("channels", len(newChannels)).Int("programs", totalProgs).Int("matched_channels", matched).
		Int("known_tvg_ids", len(knownChannelIDs)).Bool("self_healed", selfHealed).Int("logo_names", len(newNameToIcon)).Msg("epg: loaded")
}

func (e *EPGEngine) collectKnownChannelIDs() map[string]struct{} {
	if e.store == nil {
		return nil
	}
	ids := make(map[string]struct{})
	e.store.mu.RLock()
	for _, cache := range e.store.cache {
		for _, ch := range cache.Channels {
			id := strings.TrimSpace(ch.TvgID)
			if id != "" {
				ids[id] = struct{}{}
			}
		}
	}
	e.store.mu.RUnlock()
	if len(ids) == 0 {
		return nil
	}
	return ids
}

func (e *EPGEngine) loadSource(url string, windowStart, windowEnd time.Time, knownChannelIDs map[string]struct{}, stringsPool *epgStringPool,
	channels map[string]EPGChannel, programs map[string][]EPGProgram) EPGSourceStatus {

	st := EPGSourceStatus{URL: truncURL(url)}

	resp, err := e.httpClient.Get(url)
	if err != nil {
		log.Error().Err(err).Str("url", truncURL(url)).Msg("epg: fetch failed")
		st.Err = "fetch: " + err.Error()
		return st
	}
	defer resp.Body.Close()

	st.Status = resp.StatusCode
	if resp.StatusCode != 200 {
		log.Error().Int("status", resp.StatusCode).Str("url", truncURL(url)).Msg("epg: fetch non-200")
		st.Err = "http status"
		return st
	}

	// Detect gzip from URL or Content-Type.
	isGzip := strings.HasSuffix(url, ".gz") ||
		strings.HasSuffix(url, ".gzip") ||
		strings.Contains(resp.Header.Get("Content-Type"), "gzip")

	channelCb := func(ch EPGChannel) {
		if ch.ID != "" {
			ch = normalizeEPGChannel(ch, stringsPool)
			channels[ch.ID] = ch
			st.Channels++
		}
	}

	programFilter := func(prog EPGProgram) bool {
		channelID := strings.TrimSpace(prog.ChannelID)
		if channelID == "" || prog.Start.IsZero() {
			return false
		}
		if knownChannelIDs != nil {
			if _, ok := knownChannelIDs[channelID]; !ok {
				return false
			}
		}
		return epgProgramInWindow(prog, windowStart, windowEnd)
	}

	programCb := func(prog EPGProgram) {
		if !programFilter(prog) {
			return
		}
		prog = normalizeEPGProgram(prog, stringsPool)
		programs[prog.ChannelID] = append(programs[prog.ChannelID], prog)
		st.Programs++
	}

	if isGzip {
		err = ParseXMLTVGzipFiltered(resp.Body, channelCb, programFilter, programCb)
	} else {
		err = ParseXMLTVFiltered(resp.Body, channelCb, programFilter, programCb)
	}

	if err != nil {
		log.Error().Err(err).Str("url", truncURL(url)).Msg("epg: parse error")
		st.Err = "parse: " + err.Error()
		return st
	}
	st.OK = true
	return st
}

func epgProgramInWindow(prog EPGProgram, windowStart, windowEnd time.Time) bool {
	// Programmes without a stop attr fall back to the start time for the lower
	// bound; otherwise a stop-less multi-week archive would be retained in full.
	if !prog.Stop.IsZero() {
		if prog.Stop.Before(windowStart) {
			return false
		}
	} else if prog.Start.Before(windowStart) {
		return false
	}
	return !prog.Start.After(windowEnd)
}

const (
	// epgMaxFanIn bounds how many UNPINNED (playlist x-tvg-url) EPG feeds one refresh ingests.
	// Operator-pinned [iptv] epg_urls are ALWAYS kept on top of this — see capEPGSources. Each
	// large feed costs memory, so this guards against unbounded user-playlist fan-in.
	epgMaxFanIn = 6
	// epgMaxProgramsPerChannel backstops a pathological source (a normal channel has
	// ~150 programmes across the 3-day window; 800 is generous).
	epgMaxProgramsPerChannel = 800
)

// capEPGSources keeps EVERY operator-pinned static-config URL (never capped — the operator chose
// these, and a blind count-cap can strand the one source that matches the playlist, blanking the
// guide on every channel — exactly the prod incident where 6 alphabetically-first country feeds
// loaded but the RU feed got dropped). The remaining UNPINNED playlist x-tvg-url fan-in is sorted
// (deterministic — map order was random, so which feeds survived changed per restart) and capped.
func capEPGSources(staticURLs []string, all map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(all))
	pinned := make(map[string]struct{}, len(staticURLs))
	for _, u := range staticURLs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		pinned[u] = struct{}{}
		if _, ok := all[u]; ok {
			out[u] = struct{}{} // operator-pinned → always kept
		}
	}

	// Unpinned fan-in (playlist x-tvg-url), deterministically ordered.
	rest := make([]string, 0, len(all))
	for u := range all {
		if _, isPinned := pinned[u]; !isPinned {
			rest = append(rest, u)
		}
	}
	sort.Strings(rest)

	if len(rest) > epgMaxFanIn {
		log.Warn().Int("fan_in", len(rest)).Int("kept", epgMaxFanIn).Int("pinned", len(out)).
			Msg("epg: playlist EPG fan-in capped — pin the sources you need in [iptv] epg_urls")
		rest = rest[:epgMaxFanIn]
	}
	for _, u := range rest {
		out[u] = struct{}{}
	}
	return out
}

type epgStringPool struct {
	values map[string]string
}

func newEPGStringPool() *epgStringPool {
	return &epgStringPool{values: make(map[string]string, 4096)}
}

func (p *epgStringPool) intern(s string) string {
	if s == "" || p == nil {
		return s
	}
	if v, ok := p.values[s]; ok {
		return v
	}
	p.values[s] = s
	return s
}

func normalizeEPGChannel(ch EPGChannel, pool *epgStringPool) EPGChannel {
	ch.ID = pool.intern(strings.TrimSpace(ch.ID))
	ch.Name = pool.intern(strings.TrimSpace(ch.Name))
	ch.Icon = pool.intern(strings.TrimSpace(ch.Icon))
	if len(ch.AltNames) > epgMaxAltNames {
		ch.AltNames = ch.AltNames[:epgMaxAltNames]
	}
	for i, name := range ch.AltNames {
		ch.AltNames[i] = pool.intern(strings.TrimSpace(name))
	}
	return ch
}

func normalizeEPGProgram(prog EPGProgram, pool *epgStringPool) EPGProgram {
	prog.ChannelID = pool.intern(strings.TrimSpace(prog.ChannelID))
	prog.Category = pool.intern(strings.TrimSpace(prog.Category))
	prog.Icon = pool.intern(strings.TrimSpace(prog.Icon))
	prog.Title = strings.TrimSpace(prog.Title)
	prog.Description = strings.TrimSpace(prog.Description)
	return prog
}

func truncURL(url string) string {
	if len(url) > 80 {
		return url[:80] + "..."
	}
	return url
}
