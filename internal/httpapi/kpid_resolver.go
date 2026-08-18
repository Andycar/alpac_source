package httpapi

import (
	"context"
	stdjson "encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Self-hosted, quota-free TMDB/IMDB → Kinopoisk-id resolver. capi is TMDB-keyed, but several sources
// (hdvb/zetflix/cdnvideohub/kubikvkube/rezka) resolve ONLY by Kinopoisk id. TMDB gives us title/year/
// imdb for free; the only missing piece is the kp id. We get it WITHOUT a paid API:
//
//	Tier 1  persistent local store  — kp ids are immutable, so a film is resolved at most once, ever.
//	Tier 2  Wikidata (P345/P4947/P4983 → P2603) — free, no key; covers most released content.
//	Tier 3  kinopoiskapiunofficial — rare fallback (behind its 500/day quota breaker), only if a token.
//
// Every external hit is persisted, so steady-state lookups are local and cost nothing.

const (
	wikidataSPARQL   = "https://query.wikidata.org/sparql"
	kpStoreNegTTL    = 12 * time.Hour   // in-memory negative cache (NOT persisted — a miss may resolve later)
	kpStoreFlushTick = 20 * time.Second // batch disk flushes
	kpStoreUA        = "lampac-go/1.0 kp-id resolver"
)

var wikidataHTTP = &http.Client{Timeout: 10 * time.Second}

// ---------------------------------------------------------------------------
//  Tier 1 — persistent store
// ---------------------------------------------------------------------------

type kpIDStore struct {
	mu     sync.RWMutex
	path   string
	byTmdb map[string]int // "<type>:<tmdbid>" -> kp (movie/tv TMDB ids share numbers, so namespace by type)
	byImdb map[string]int // "ttXXXX" -> kp (imdb is globally unique)
	dirty  bool

	negMu sync.Mutex
	neg   map[string]time.Time // key -> time it was cached as "not found"
}

var kpIDStoreSingleton *kpIDStore

func initKpIDStore(path string) {
	s := &kpIDStore{
		path:   path,
		byTmdb: map[string]int{},
		byImdb: map[string]int{},
		neg:    map[string]time.Time{},
	}
	s.load()
	kpIDStoreSingleton = s
	go s.flushLoop()
}

func tmdbKey(mediaType string, id int) string {
	t := "movie"
	if mediaType == "tv" {
		t = "tv"
	}
	return t + ":" + strconv.Itoa(id)
}

type kpStoreDisk struct {
	ByTmdb map[string]int `json:"by_tmdb"`
	ByImdb map[string]int `json:"by_imdb"`
}

func (s *kpIDStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return // first run / missing → start empty
	}
	var disk kpStoreDisk
	if stdjson.Unmarshal(data, &disk) != nil {
		return
	}
	if disk.ByTmdb != nil {
		s.byTmdb = disk.ByTmdb
	}
	if disk.ByImdb != nil {
		s.byImdb = disk.ByImdb
	}
	log.Info().Int("tmdb", len(s.byTmdb)).Int("imdb", len(s.byImdb)).Str("path", s.path).Msg("kp-id store loaded")
}

func (s *kpIDStore) flushLoop() {
	t := time.NewTicker(kpStoreFlushTick)
	defer t.Stop()
	for range t.C {
		s.flush()
	}
}

func (s *kpIDStore) flush() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	data, err := stdjson.Marshal(kpStoreDisk{ByTmdb: s.byTmdb, ByImdb: s.byImdb})
	s.dirty = false // cleared now; a concurrent put will re-set it for the next tick
	s.mu.Unlock()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		log.Warn().Err(err).Msg("kp-id store: mkdir failed")
		s.markDirty()
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Warn().Err(err).Msg("kp-id store: write failed")
		s.markDirty()
		return
	}
	if err := os.Rename(tmp, s.path); err != nil { // atomic replace
		log.Warn().Err(err).Msg("kp-id store: rename failed")
		s.markDirty()
	}
}

func (s *kpIDStore) markDirty() {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

func (s *kpIDStore) get(mediaType string, tmdbID int, imdb string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if imdb != "" {
		if kp, ok := s.byImdb[imdb]; ok && kp > 0 {
			return kp, true
		}
	}
	if tmdbID > 0 {
		if kp, ok := s.byTmdb[tmdbKey(mediaType, tmdbID)]; ok && kp > 0 {
			return kp, true
		}
	}
	return 0, false
}

func (s *kpIDStore) put(mediaType string, tmdbID int, imdb string, kp int) {
	if kp <= 0 {
		return
	}
	s.mu.Lock()
	if imdb != "" && s.byImdb[imdb] != kp {
		s.byImdb[imdb] = kp
		s.dirty = true
	}
	if tmdbID > 0 {
		k := tmdbKey(mediaType, tmdbID)
		if s.byTmdb[k] != kp {
			s.byTmdb[k] = kp
			s.dirty = true
		}
	}
	s.mu.Unlock()
}

func (s *kpIDStore) negCached(key string) bool {
	s.negMu.Lock()
	defer s.negMu.Unlock()
	t, ok := s.neg[key]
	if !ok {
		return false
	}
	if time.Since(t) > kpStoreNegTTL {
		delete(s.neg, key)
		return false
	}
	return true
}

func (s *kpIDStore) markNeg(key string) {
	s.negMu.Lock()
	s.neg[key] = time.Now()
	s.negMu.Unlock()
}

// ---------------------------------------------------------------------------
//  Tier 2 — Wikidata (free, no key)
// ---------------------------------------------------------------------------

func sparqlEsc(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// wikidataKP resolves a Kinopoisk film id (P2603) from a TMDB id (P4947 movie / P4983 tv) or imdb
// (P345). Free and key-less. Returns (0,false) on miss/error/timeout.
func wikidataKP(ctx context.Context, mediaType string, tmdbID int, imdb string) (int, bool) {
	var clauses []string
	if imdb = strings.TrimSpace(imdb); imdb != "" {
		clauses = append(clauses, `{ ?f wdt:P345 "`+sparqlEsc(imdb)+`" }`)
	}
	if tmdbID > 0 {
		prop := "P4947" // TMDB movie id
		if mediaType == "tv" {
			prop = "P4983" // TMDB TV series id
		}
		clauses = append(clauses, `{ ?f wdt:`+prop+` "`+strconv.Itoa(tmdbID)+`" }`)
	}
	if len(clauses) == 0 {
		return 0, false
	}
	q := "SELECT ?kp WHERE { " + strings.Join(clauses, " UNION ") + " ?f wdt:P2603 ?kp } LIMIT 1"
	reqURL := wikidataSPARQL + "?format=json&query=" + url.QueryEscape(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Accept", "application/sparql-results+json")
	req.Header.Set("User-Agent", kpStoreUA) // Wikidata 403s requests without a descriptive UA
	resp, err := wikidataHTTP.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, false
	}
	var res struct {
		Results struct {
			Bindings []struct {
				Kp struct {
					Value string `json:"value"`
				} `json:"kp"`
			} `json:"bindings"`
		} `json:"results"`
	}
	if stdjson.Unmarshal(body, &res) != nil {
		return 0, false
	}
	for _, b := range res.Results.Bindings {
		if n, err := strconv.Atoi(strings.TrimSpace(b.Kp.Value)); err == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
}

// ---------------------------------------------------------------------------
//  Tier 2b — configured TMDB→kp endpoints (upn.stull.xyz / apbugall / …)
// ---------------------------------------------------------------------------

// kpByTmdbEndpoints are base URLs (each WITH its ?token=...) from [kinopoisk] kp_by_tmdb; the code
// appends &tmdb=<id>. They resolve kp DIRECTLY by tmdb id — cleaner than a title search.
var kpByTmdbEndpoints []string

func setKpByTmdbEndpoints(eps []string) {
	var clean []string
	for _, e := range eps {
		if e = strings.TrimSpace(e); e != "" {
			clean = append(clean, e)
		}
	}
	kpByTmdbEndpoints = clean
}

type kpByTmdbResp struct {
	Status string `json:"status"`
	Data   struct {
		IDKp     int    `json:"id_kp"`
		IDTmdb   int    `json:"id_tmdb"`
		Category int    `json:"category"` // 1=movie, 2=tv — rejects a wrong-namespace collision
		Quality  string `json:"quality"`  // source format: WEB-DL / BDRip / TS / CAMRip / …
		UHD      bool   `json:"uhd"`      // 4K available
	} `json:"data"`
}

// kpByTmdbData is the useful slice of a kp_by_tmdb response.
type kpByTmdbData struct {
	IDKp     int
	Quality  string
	UHD      bool
	Category int
}

// kpByTmdbMeta queries the configured kp_by_tmdb endpoints for a tmdb id (first success wins) and
// returns kp id + quality/uhd. Rejects a clear movie/tv category mismatch: TMDB movie/tv ids are
// separate namespaces but these endpoints key only by the number, so a bare id could return the wrong work.
func kpByTmdbMeta(ctx context.Context, mediaType string, tmdbID int) (kpByTmdbData, bool) {
	if tmdbID <= 0 {
		return kpByTmdbData{}, false
	}
	for _, ep := range kpByTmdbEndpoints {
		sep := "&"
		if !strings.Contains(ep, "?") {
			sep = "?"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep+sep+"tmdb="+strconv.Itoa(tmdbID), nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", kpStoreUA)
		req.Header.Set("Accept", "application/json")
		resp, err := wikidataHTTP.Do(req) // plain client → transparently gunzips
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		var r kpByTmdbResp
		if stdjson.Unmarshal(body, &r) != nil || r.Data.IDKp <= 0 {
			continue
		}
		if (r.Data.Category == 1 && mediaType == "tv") || (r.Data.Category == 2 && mediaType == "movie") {
			continue // wrong type for this tmdb number
		}
		return kpByTmdbData{IDKp: r.Data.IDKp, Quality: r.Data.Quality, UHD: r.Data.UHD, Category: r.Data.Category}, true
	}
	return kpByTmdbData{}, false
}

// kpByTmdbLookup is the kp-id-only wrapper of kpByTmdbMeta (used by the resolver chain).
func kpByTmdbLookup(ctx context.Context, mediaType string, tmdbID int) (int, bool) {
	d, ok := kpByTmdbMeta(ctx, mediaType, tmdbID)
	return d.IDKp, ok && d.IDKp > 0
}

// kpQualityBadge maps a kp_by_tmdb source-format string (+ uhd flag) to the card badge (4K/FHD/HD/SD).
// `uhd` is the only reliable resolution signal; the format string is a heuristic for the rest.
func kpQualityBadge(quality string, uhd bool) string {
	q := strings.ToLower(strings.TrimSpace(quality))
	if uhd || strings.Contains(q, "4k") || strings.Contains(q, "2160") || strings.Contains(q, "uhd") {
		return "4K"
	}
	if q == "" {
		return ""
	}
	for _, cam := range []string{"cam", "camrip", "ts", "telesync", "tc", "telecine", "scr", "dvdscr", "hdcam", "workprint"} {
		if q == cam {
			return "SD" // screener/cam tier
		}
	}
	if strings.Contains(q, "720") || q == "dvdrip" || q == "dvd" || q == "satrip" || q == "tvrip" || q == "hdtvrip" {
		return "HD"
	}
	return "FHD" // WEB-DL / BDRip / BluRay / HDRip / WEBRip … — typically 1080p
}

// ---------------------------------------------------------------------------
//  Orchestrator
// ---------------------------------------------------------------------------

// resolveKpID maps a TMDB card → Kinopoisk film id for kp-only sources. Tier 1 store → Tier 2
// Wikidata → Tier 3 kinopoiskapiunofficial. Persists every external hit. Works even with NO
// [kinopoisk] token (Wikidata is the free primary; the token only powers the rare Tier-3 fallback).
func resolveKpID(ctx context.Context, mediaType string, tmdbID int, imdb, title string, year int) (int, bool) {
	store := kpIDStoreSingleton

	// Tier 1 — persistent store (forever; kp ids are immutable)
	if store != nil {
		if kp, ok := store.get(mediaType, tmdbID, imdb); ok {
			return kp, true
		}
	}
	negKey := tmdbKey(mediaType, tmdbID) + "|" + imdb
	if store != nil && store.negCached(negKey) {
		return 0, false // recently failed everywhere → don't re-hammer Wikidata/KP this window
	}

	// Tier 2 — Wikidata (free)
	if kp, ok := wikidataKP(ctx, mediaType, tmdbID, imdb); ok {
		if store != nil {
			store.put(mediaType, tmdbID, imdb, kp)
		}
		log.Debug().Int("tmdb", tmdbID).Str("imdb", imdb).Int("kp", kp).Msg("kp-id: resolved via Wikidata")
		return kp, true
	}

	// Tier 2b — configured TMDB→kp endpoints (upn.stull.xyz / apbugall / …), direct by tmdb id
	if kp, ok := kpByTmdbLookup(ctx, mediaType, tmdbID); ok {
		if store != nil {
			store.put(mediaType, tmdbID, imdb, kp)
		}
		log.Debug().Int("tmdb", tmdbID).Int("kp", kp).Msg("kp-id: resolved via kp_by_tmdb endpoint")
		return kp, true
	}

	// Tier 3 — kinopoiskapiunofficial (title+year, then imdb), only if a token is set
	if kpClientSingleton != nil {
		if kp, ok := kpClientSingleton.ResolveKinopoiskID(ctx, imdb, title, year); ok {
			if store != nil {
				store.put(mediaType, tmdbID, imdb, kp)
			}
			log.Debug().Int("tmdb", tmdbID).Int("kp", kp).Msg("kp-id: resolved via kinopoiskapiunofficial (fallback)")
			return kp, true
		}
	}

	if store != nil {
		store.markNeg(negKey)
	}
	return 0, false
}
