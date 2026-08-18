// Package kpcatalog is the Kinopoisk-Unofficial (kinopoiskapiunofficial.tech)
// catalog/search client, shared by the catalog API, alice, and capi reviews.
// Extracted from httpapi as a reusable leaf.
package kpcatalog

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	jsoniter "github.com/json-iterator/go"

	"lampac-go/internal/httpx"
)

// json mirrors httpapi/server.go — the monolith-compatible encoder.
var json = jsoniter.ConfigCompatibleWithStandardLibrary

// writeJSON writes v as JSON (shared response infra).
func writeJSON(w http.ResponseWriter, status int, v any) {
	httpx.WriteJSON(w, status, v)
}

// ---------------------------------------------------------------------------
//  Constants
// ---------------------------------------------------------------------------

const kpAPIHost = "https://kinopoiskapiunofficial.tech"

// Cache TTLs — aggressive because the free tier is 500 req/day.
const (
	kpTTLCollections = 6 * time.Hour
	kpTTLPremieres   = 3 * time.Hour
	kpTTLSearch      = 1 * time.Hour
	kpTTLFilters     = 3 * time.Hour
	kpTTLCard        = 24 * time.Hour
	kpTTLRefData     = 24 * time.Hour // genres/countries reference
	kpTTLCatalogDef  = 24 * time.Hour
)

// ---------------------------------------------------------------------------
//  KP API response types
// ---------------------------------------------------------------------------

type kpGenre struct {
	Genre string `json:"genre"`
}

type kpCountry struct {
	Country string `json:"country"`
}

type kpFilm struct {
	KinopoiskID      int         `json:"kinopoiskId"`
	FilmID           int         `json:"filmId"` // v2.1 search uses filmId
	NameRu           string      `json:"nameRu"`
	NameEn           string      `json:"nameEn"`
	NameOriginal     string      `json:"nameOriginal"`
	PosterURL        string      `json:"posterUrl"`
	PosterURLPreview string      `json:"posterUrlPreview"`
	CoverURL         string      `json:"coverUrl"`
	Description      string      `json:"description"`
	ShortDescription string      `json:"shortDescription"`
	RatingKinopoisk  float64     `json:"ratingKinopoisk"`
	RatingImdb       float64     `json:"ratingImdb"`
	Year             flexInt     `json:"year"` // KP v2.1 search returns string; v2.2 returns int
	Type             string      `json:"type"` // FILM, TV_SERIES, MINI_SERIES, TV_SHOW
	Genres           []kpGenre   `json:"genres"`
	Countries        []kpCountry `json:"countries"`
	ImdbID           string      `json:"imdbId"`
	FilmLength       flexInt     `json:"filmLength"` // v2.1 returns "02:49" (string), v2.2 returns int
	// v2.1 search fields
	Rating string `json:"rating"` // sometimes string like "8.4"
}

// flexInt is an int that can be decoded from both JSON number and JSON string.
// KP v2.1 search returns year as "2014" (string), v2.2 returns 2014 (number).
type flexInt int

func (fi *flexInt) UnmarshalJSON(data []byte) error {
	// Try number first.
	var n int
	if err := stdjson.Unmarshal(data, &n); err == nil {
		*fi = flexInt(n)
		return nil
	}
	// Try string.
	var s string
	if err := stdjson.Unmarshal(data, &s); err == nil {
		var parsed int
		fmt.Sscanf(s, "%d", &parsed)
		*fi = flexInt(parsed)
		return nil
	}
	return nil // zero on failure
}

type kpCollectionResponse struct {
	Total      int      `json:"total"`
	TotalPages int      `json:"totalPages"`
	Items      []kpFilm `json:"items"`
}

type kpTopResponse struct {
	PagesCount int      `json:"pagesCount"`
	Films      []kpFilm `json:"films"`
}

type kpSearchResponse struct {
	Keyword                string   `json:"keyword"`
	PagesCount             int      `json:"pagesCount"`
	SearchFilmsCountResult int      `json:"searchFilmsCountResult"`
	Films                  []kpFilm `json:"films"`
}

type kpPremieresResponse struct {
	Total int      `json:"total"`
	Items []kpFilm `json:"items"`
}

type kpuEpisode struct {
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	NameRu        string `json:"nameRu"`
	NameEn        string `json:"nameEn"`
	ReleaseDate   string `json:"releaseDate"`
}

type kpuSeason struct {
	Number   int          `json:"number"`
	Episodes []kpuEpisode `json:"episodes"`
}

type kpuSeasonsResponse struct {
	Total int         `json:"total"`
	Items []kpuSeason `json:"items"`
}

type kpSimilarsResponse struct {
	Total int      `json:"total"`
	Items []kpFilm `json:"items"`
}

type kpStaffMember struct {
	StaffID        int    `json:"staffId"`
	NameRu         string `json:"nameRu"`
	NameEn         string `json:"nameEn"`
	PosterURL      string `json:"posterUrl"`
	ProfessionKey  string `json:"professionKey"`  // DIRECTOR, ACTOR, PRODUCER, etc.
	ProfessionText string `json:"professionText"` // localized
	Description    string `json:"description"`    // role name for actors
}

type kpFilterGenre struct {
	ID    int    `json:"id"`
	Genre string `json:"genre"`
}

type kpFilterCountry struct {
	ID      int    `json:"id"`
	Country string `json:"country"`
}

type kpFiltersResponse struct {
	Genres    []kpFilterGenre   `json:"genres"`
	Countries []kpFilterCountry `json:"countries"`
}

// ---------------------------------------------------------------------------
//  Cache
// ---------------------------------------------------------------------------

type kpCacheEntry struct {
	data    any
	expires time.Time
}

type kpCache struct {
	mu      sync.RWMutex
	entries map[string]*kpCacheEntry
}

func newKPCache() *kpCache {
	c := &kpCache{entries: make(map[string]*kpCacheEntry)}
	go c.cleanupLoop()
	return c
}

func (c *kpCache) get(key string) (any, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.data, true
}

func (c *kpCache) set(key string, data any, ttl time.Duration) {
	c.mu.Lock()
	c.entries[key] = &kpCacheEntry{data: data, expires: time.Now().Add(ttl)}
	c.mu.Unlock()
}

func (c *kpCache) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		c.mu.Lock()
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		c.mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
//  KP Client
// ---------------------------------------------------------------------------

// kpQuotaCooldown: how long to stop ALL KP calls after a 402 (daily-quota) response. The free
// kinopoiskapiunofficial tier is 500 req/day, and over-quota calls STILL increment the counter — so
// once exhausted, hammering it (every capi resolve does a kp lookup) just digs deeper. Pausing for an
// hour saves quota, stops the counter climbing, and self-heals after the daily reset.
const kpQuotaCooldown = 1 * time.Hour

type KPClient struct {
	httpClient *http.Client
	apiKey     string
	cache      *kpCache

	quotaMu    sync.Mutex
	quotaUntil time.Time // when set in the future, the daily quota is blown — skip calls until then
}

func NewKPClient(apiKey string) *KPClient {
	return &KPClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		apiKey:     apiKey,
		cache:      newKPCache(),
	}
}

// quotaTripped reports whether we're inside the post-402 cooldown.
func (kp *KPClient) quotaTripped() bool {
	kp.quotaMu.Lock()
	defer kp.quotaMu.Unlock()
	return !kp.quotaUntil.IsZero() && time.Now().Before(kp.quotaUntil)
}

// doGet performs an authenticated GET request to the KP API.
func (kp *KPClient) DoGet(path string) ([]byte, error) {
	return kp.doGetCtx(context.Background(), path)
}

// doGetCtx is doGet with a caller-supplied context — lets latency-sensitive callers (capi's
// kinopoisk_id resolve) bound the request instead of waiting out the 15s client timeout.
func (kp *KPClient) doGetCtx(ctx context.Context, path string) ([]byte, error) {
	// Daily-quota circuit breaker: after a 402 we skip all calls for kpQuotaCooldown so we don't keep
	// burning (and counting) over-quota requests on every capi resolve / catalog hit.
	if kp.quotaTripped() {
		return nil, fmt.Errorf("kp api: quota cooldown")
	}
	reqURL := kpAPIHost + path
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-KEY", kp.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := kp.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kp api: %w", err)
	}
	defer resp.Body.Close()

	// Cap at 10 MB — KP API returns JSON catalog/detail; legitimate payloads
	// are well under 1 MB.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("kp api read: %w", err)
	}

	if resp.StatusCode == 402 {
		kp.quotaMu.Lock()
		kp.quotaUntil = time.Now().Add(kpQuotaCooldown)
		kp.quotaMu.Unlock()
		log.Warn().Dur("cooldown", kpQuotaCooldown).Msg("kp api: daily quota exceeded (402) — pausing KP calls (catalog + capi kinopoisk_id resolve)")
		return nil, fmt.Errorf("kp api: quota exceeded (402)")
	}
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("kp api: invalid token (401)")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("kp api: status %d for %s", resp.StatusCode, path)
	}

	return body, nil
}

// ---------------------------------------------------------------------------
//  API methods (with caching)
// ---------------------------------------------------------------------------

func (kp *KPClient) getFilters() (*kpFiltersResponse, error) {
	const cacheKey = "filters"
	if v, ok := kp.cache.get(cacheKey); ok {
		return v.(*kpFiltersResponse), nil
	}

	data, err := kp.DoGet("/api/v2.2/films/filters")
	if err != nil {
		return nil, err
	}

	var resp kpFiltersResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("kp filters decode: %w", err)
	}

	kp.cache.set(cacheKey, &resp, kpTTLRefData)
	return &resp, nil
}

func (kp *KPClient) getCollections(collectionType string, page int) (*kpCollectionResponse, error) {
	cacheKey := fmt.Sprintf("col:%s:%d", collectionType, page)
	if v, ok := kp.cache.get(cacheKey); ok {
		return v.(*kpCollectionResponse), nil
	}

	path := fmt.Sprintf("/api/v2.2/films/collections?type=%s&page=%d", url.QueryEscape(collectionType), page)
	data, err := kp.DoGet(path)
	if err != nil {
		return nil, err
	}

	var resp kpCollectionResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("kp collections decode: %w", err)
	}

	kp.cache.set(cacheKey, &resp, kpTTLCollections)
	return &resp, nil
}

func (kp *KPClient) getTop(topType string, page int) (*kpTopResponse, error) {
	cacheKey := fmt.Sprintf("top:%s:%d", topType, page)
	if v, ok := kp.cache.get(cacheKey); ok {
		return v.(*kpTopResponse), nil
	}

	path := fmt.Sprintf("/api/v2.2/films/top?type=%s&page=%d", url.QueryEscape(topType), page)
	data, err := kp.DoGet(path)
	if err != nil {
		return nil, err
	}

	var resp kpTopResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("kp top decode: %w", err)
	}

	kp.cache.set(cacheKey, &resp, kpTTLCollections)
	return &resp, nil
}

func (kp *KPClient) getPremieres(year, month int) (*kpPremieresResponse, error) {
	monthName := strings.ToUpper(time.Month(month).String())
	cacheKey := fmt.Sprintf("prem:%d:%s", year, monthName)
	if v, ok := kp.cache.get(cacheKey); ok {
		return v.(*kpPremieresResponse), nil
	}

	path := fmt.Sprintf("/api/v2.2/films/premieres?year=%d&month=%s", year, monthName)
	data, err := kp.DoGet(path)
	if err != nil {
		return nil, err
	}

	var resp kpPremieresResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("kp premieres decode: %w", err)
	}

	kp.cache.set(cacheKey, &resp, kpTTLPremieres)
	return &resp, nil
}

func (kp *KPClient) SearchByKeyword(keyword string, page int) (*kpSearchResponse, error) {
	cacheKey := fmt.Sprintf("search:%s:%d", keyword, page)
	if v, ok := kp.cache.get(cacheKey); ok {
		return v.(*kpSearchResponse), nil
	}

	path := fmt.Sprintf("/api/v2.1/films/search-by-keyword?keyword=%s&page=%d", url.QueryEscape(keyword), page)
	data, err := kp.DoGet(path)
	if err != nil {
		return nil, err
	}

	// Use standard library json for v2.1 search — it returns some fields
	// as strings (year: "2014", filmLength: "02:49") that jsoniter rejects.
	var resp kpSearchResponse
	if err := stdjson.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("kp search decode: %w", err)
	}

	kp.cache.set(cacheKey, &resp, kpTTLSearch)
	return &resp, nil
}

// kpID returns the film's Kinopoisk id, tolerating the two field names the API uses
// (v2.2 → kinopoiskId, v2.1 search → filmId).
func (f kpFilm) kpID() int {
	if f.KinopoiskID > 0 {
		return f.KinopoiskID
	}
	return f.FilmID
}

// filmIDByImdb maps an imdb tt-id → Kinopoisk film id via /api/v2.2/films?imdbId=. Cached (incl.
// misses) since the imdb↔kp mapping is stable. Context-bounded so it can't stall capi's resolve.
func (kp *KPClient) filmIDByImdb(ctx context.Context, imdb string) (int, bool) {
	imdb = strings.TrimSpace(imdb)
	if imdb == "" {
		return 0, false
	}
	cacheKey := "kpid:imdb:" + imdb
	if v, ok := kp.cache.get(cacheKey); ok {
		id := v.(int)
		return id, id > 0
	}
	data, err := kp.doGetCtx(ctx, "/api/v2.2/films?imdbId="+url.QueryEscape(imdb))
	if err != nil {
		return 0, false // transient (quota/timeout) — don't cache, let a later resolve retry
	}
	var resp kpCollectionResponse
	if stdjson.Unmarshal(data, &resp) != nil {
		return 0, false
	}
	id := 0
	for _, f := range resp.Items {
		if k := f.kpID(); k > 0 {
			id = k
			break
		}
	}
	kp.cache.set(cacheKey, id, kpTTLCard) // 24h — covers both a hit and a confirmed miss
	return id, id > 0
}

// filmIDByTitle resolves a Kinopoisk film id from a title (+year) via the v2.1 keyword search.
// Exact-year match wins, else the first hit. Context-bounded + cached (incl. misses).
func (kp *KPClient) filmIDByTitle(ctx context.Context, title string, year int) (int, bool) {
	title = strings.TrimSpace(title)
	if title == "" {
		return 0, false
	}
	cacheKey := "kpid:title:" + strings.ToLower(title) + ":" + strconv.Itoa(year)
	if v, ok := kp.cache.get(cacheKey); ok {
		id := v.(int)
		return id, id > 0
	}
	// v2.1 search returns year/length as strings → stdjson (jsoniter rejects it), same as searchByKeyword.
	data, err := kp.doGetCtx(ctx, "/api/v2.1/films/search-by-keyword?keyword="+url.QueryEscape(title)+"&page=1")
	if err != nil {
		return 0, false
	}
	var resp kpSearchResponse
	if stdjson.Unmarshal(data, &resp) != nil {
		return 0, false
	}
	best, exact := 0, 0
	for _, f := range resp.Films {
		k := f.kpID()
		if k == 0 {
			continue
		}
		if year > 0 && int(f.Year) == year {
			exact = k
			break
		}
		if best == 0 {
			best = k
		}
	}
	id := best
	if exact > 0 {
		id = exact
	}
	kp.cache.set(cacheKey, id, kpTTLSearch)
	return id, id > 0
}

// resolveKinopoiskID finds a Kinopoisk film id for a TMDB card so kp-only sources (hdvb/zetflix)
// can resolve under capi. Prefers the exact imdb→kp map; falls back to a title+year search. Fully
// context-bounded so it can't stall capi's resolve. Returns (0,false) when nothing matches.
func (kp *KPClient) ResolveKinopoiskID(ctx context.Context, imdb, title string, year int) (int, bool) {
	if kp == nil {
		return 0, false
	}
	// title+year FIRST — the tight 500/day quota matters: kinopoiskapiunofficial's imdbId index is
	// sparse (empirically 0 for most films, a wasted call), while keyword+exact-year resolves most
	// cards in ONE call. imdb is the fallback for ambiguous/failed title matches.
	if id, ok := kp.filmIDByTitle(ctx, title, year); ok {
		return id, true
	}
	return kp.filmIDByImdb(ctx, imdb)
}

func (kp *KPClient) searchByFilters(filmType string, genres int, page int) (*kpCollectionResponse, error) {
	cacheKey := fmt.Sprintf("filter:%s:%d:%d", filmType, genres, page)
	if v, ok := kp.cache.get(cacheKey); ok {
		return v.(*kpCollectionResponse), nil
	}

	path := fmt.Sprintf("/api/v2.2/films?type=%s&genres=%d&order=RATING&page=%d",
		url.QueryEscape(filmType), genres, page)
	data, err := kp.DoGet(path)
	if err != nil {
		return nil, err
	}

	var resp kpCollectionResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("kp filter search decode: %w", err)
	}

	kp.cache.set(cacheKey, &resp, kpTTLFilters)
	return &resp, nil
}

func (kp *KPClient) getFilm(id int) (*kpFilm, error) {
	cacheKey := fmt.Sprintf("film:%d", id)
	if v, ok := kp.cache.get(cacheKey); ok {
		return v.(*kpFilm), nil
	}

	data, err := kp.DoGet(fmt.Sprintf("/api/v2.2/films/%d", id))
	if err != nil {
		return nil, err
	}

	var film kpFilm
	if err := json.Unmarshal(data, &film); err != nil {
		return nil, fmt.Errorf("kp film decode: %w", err)
	}

	kp.cache.set(cacheKey, &film, kpTTLCard)
	return &film, nil
}

func (kp *KPClient) getSeasons(id int) (*kpuSeasonsResponse, error) {
	cacheKey := fmt.Sprintf("seasons:%d", id)
	if v, ok := kp.cache.get(cacheKey); ok {
		return v.(*kpuSeasonsResponse), nil
	}

	data, err := kp.DoGet(fmt.Sprintf("/api/v2.2/films/%d/seasons", id))
	if err != nil {
		return nil, err
	}

	var resp kpuSeasonsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("kp seasons decode: %w", err)
	}

	kp.cache.set(cacheKey, &resp, kpTTLCard)
	return &resp, nil
}

func (kp *KPClient) getSimilars(id int) (*kpSimilarsResponse, error) {
	cacheKey := fmt.Sprintf("similars:%d", id)
	if v, ok := kp.cache.get(cacheKey); ok {
		return v.(*kpSimilarsResponse), nil
	}

	data, err := kp.DoGet(fmt.Sprintf("/api/v2.2/films/%d/similars", id))
	if err != nil {
		return nil, err
	}

	var resp kpSimilarsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("kp similars decode: %w", err)
	}

	kp.cache.set(cacheKey, &resp, kpTTLCard)
	return &resp, nil
}

func (kp *KPClient) getStaff(filmID int) ([]kpStaffMember, error) {
	cacheKey := fmt.Sprintf("staff:%d", filmID)
	if v, ok := kp.cache.get(cacheKey); ok {
		return v.([]kpStaffMember), nil
	}

	data, err := kp.DoGet(fmt.Sprintf("/api/v1/staff?filmId=%d", filmID))
	if err != nil {
		return nil, err
	}

	var staff []kpStaffMember
	if err := json.Unmarshal(data, &staff); err != nil {
		return nil, fmt.Errorf("kp staff decode: %w", err)
	}

	kp.cache.set(cacheKey, staff, kpTTLCard)
	return staff, nil
}

// ---------------------------------------------------------------------------
//  KP Film → Lampa card mapping
// ---------------------------------------------------------------------------

func kpFilmToCard(f kpFilm) map[string]any {
	id := f.KinopoiskID
	if id == 0 {
		id = f.FilmID // v2.1 search uses filmId
	}

	title := f.NameRu
	if title == "" {
		title = f.NameOriginal
	}
	if title == "" {
		title = f.NameEn
	}

	origTitle := f.NameOriginal
	if origTitle == "" {
		origTitle = f.NameEn
	}

	poster := f.PosterURLPreview
	if poster == "" {
		poster = f.PosterURL
	}

	overview := f.Description
	if overview == "" {
		overview = f.ShortDescription
	}

	// Build genre objects for Lampa (expects [{name: "..."}, ...]).
	genreObjs := make([]map[string]any, 0, len(f.Genres))
	for _, g := range f.Genres {
		if g.Genre != "" {
			genreObjs = append(genreObjs, map[string]any{"name": g.Genre})
		}
	}

	// Build country objects for Lampa (expects [{name: "..."}, ...] in production_countries).
	countryObjs := make([]map[string]any, 0, len(f.Countries))
	for _, c := range f.Countries {
		if c.Country != "" {
			countryObjs = append(countryObjs, map[string]any{"name": c.Country})
		}
	}

	// Lampa image priority: card.poster > card.img > imagetmdb.com + card.poster_path
	// KP URLs are direct (not TMDB paths), so use "poster"/"img" to bypass TMDB proxy.
	// poster_path and backdrop_path MUST be empty to prevent imagetmdb.com prefix mangling.
	card := map[string]any{
		"id":               id,
		"kinopoisk_id":     id,
		"title":            title,
		"original_title":   origTitle,
		"name":             title,
		"original_name":    origTitle,
		"poster":           poster,
		"img":              poster,
		"poster_path":      "",
		"background_image": f.CoverURL,
		"backdrop_path":    "",
		"overview":         overview,
		"year":             f.Year,
		"source":           "КиноПоиск",

		// Lampa's Descriptiopn.create accesses .length on these — must never be undefined.
		"genres":               genreObjs,
		"production_countries": countryObjs,
		"production_companies": []map[string]any{},
	}

	if f.Year > 0 {
		card["release_date"] = fmt.Sprintf("%d-01-01", f.Year)
	}

	if f.RatingImdb > 0 {
		card["vote_average"] = f.RatingImdb
	} else if f.RatingKinopoisk > 0 {
		card["vote_average"] = f.RatingKinopoisk
	}
	if f.RatingKinopoisk > 0 {
		card["kp_rating"] = f.RatingKinopoisk
	}

	if f.ImdbID != "" {
		card["imdb_id"] = f.ImdbID
	}

	return card
}

// kpFilmsToCards converts a slice of kpFilm to Lampa cards.
func kpFilmsToCards(films []kpFilm) []map[string]any {
	cards := make([]map[string]any, 0, len(films))
	for _, f := range films {
		cards = append(cards, kpFilmToCard(f))
	}
	return cards
}

// ---------------------------------------------------------------------------
//  Catalog definition builder
// ---------------------------------------------------------------------------

func (kp *KPClient) BuildCatalogDef() map[string]any {
	const cacheKey = "catalogdef"
	if v, ok := kp.cache.get(cacheKey); ok {
		return v.(map[string]any)
	}

	mainSection := map[string]string{
		"Топ 250":           "/catalog/list?source=kp&method=top&topType=TOP_250_BEST_FILMS",
		"Популярные":        "/catalog/list?source=kp&method=collections&collectionType=TOP_POPULAR_ALL",
		"Популярные фильмы": "/catalog/list?source=kp&method=collections&collectionType=TOP_POPULAR_MOVIES",
		"Топ сериалы":       "/catalog/list?source=kp&method=collections&collectionType=TOP_250_TV_SHOWS",
		"Скоро в кино":      "/catalog/list?source=kp&method=collections&collectionType=CLOSES_RELEASES",
		"Премьеры":          "/catalog/list?source=kp&method=premieres",
	}

	movieGenres := map[string]string{}
	tvGenres := map[string]string{}

	filters, err := kp.getFilters()
	if err == nil && filters != nil {
		for _, g := range filters.Genres {
			if g.Genre == "" || g.ID == 0 {
				continue
			}
			// Capitalize first letter of genre name.
			label := capitalizeFirst(g.Genre)
			movieGenres[label] = fmt.Sprintf("/catalog/list?source=kp&method=filters&type=FILM&genres=%d", g.ID)
			tvGenres[label] = fmt.Sprintf("/catalog/list?source=kp&method=filters&type=TV_SERIES&genres=%d", g.ID)
		}
	}

	def := map[string]any{
		"main":   mainSection,
		"movie":  movieGenres,
		"tv":     tvGenres,
		"search": "/catalog/list?source=kp&method=search",
		"menu": map[string]string{
			"Фильмы":  "movie",
			"Сериалы": "tv",
		},
	}

	kp.cache.set(cacheKey, def, kpTTLCatalogDef)
	return def
}

// capitalizeFirst returns s with the first Unicode letter uppercased.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	if runes[0] >= 'а' && runes[0] <= 'я' {
		runes[0] -= 'а' - 'А'
	} else if runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] -= 'a' - 'A'
	}
	return string(runes)
}

// ---------------------------------------------------------------------------
//  HTTP Handlers
// ---------------------------------------------------------------------------

// handleList dispatches /catalog/list?source=kp requests.
func (kp *KPClient) HandleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	method := q.Get("method")
	pageStr := q.Get("page")
	page, _ := strconv.Atoi(pageStr)
	if page < 1 {
		page = 1
	}

	switch method {
	case "collections":
		kp.handleListCollections(w, q, page)
	case "top":
		kp.handleListTop(w, q, page)
	case "premieres":
		kp.handleListPremieres(w)
	case "search":
		kp.handleListSearch(w, q, page)
	case "filters":
		kp.handleListFilters(w, q, page)
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"results": []any{}, "page": 1, "total_pages": 1, "total_results": 0,
		})
	}
}

func (kp *KPClient) handleListTop(w http.ResponseWriter, q url.Values, page int) {
	topType := q.Get("topType")
	if topType == "" {
		topType = "TOP_250_BEST_FILMS"
	}

	resp, err := kp.getTop(topType, page)
	if err != nil {
		log.Warn().Err(err).Str("type", topType).Msg("kp: top error")
		writeJSON(w, http.StatusOK, map[string]any{
			"results": []any{}, "page": page, "total_pages": 1, "total_results": 0,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results":       kpFilmsToCards(resp.Films),
		"page":          page,
		"total_pages":   resp.PagesCount,
		"total_results": resp.PagesCount * 20,
	})
}

func (kp *KPClient) handleListCollections(w http.ResponseWriter, q url.Values, page int) {
	colType := q.Get("collectionType")
	if colType == "" {
		colType = "TOP_POPULAR_ALL"
	}

	resp, err := kp.getCollections(colType, page)
	if err != nil {
		log.Warn().Err(err).Str("type", colType).Msg("kp: collections error")
		writeJSON(w, http.StatusOK, map[string]any{
			"results": []any{}, "page": page, "total_pages": 1, "total_results": 0,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results":       kpFilmsToCards(resp.Items),
		"page":          page,
		"total_pages":   resp.TotalPages,
		"total_results": resp.Total,
	})
}

func (kp *KPClient) handleListPremieres(w http.ResponseWriter) {
	now := time.Now()
	resp, err := kp.getPremieres(now.Year(), int(now.Month()))
	if err != nil {
		log.Warn().Err(err).Msg("kp: premieres error")
		writeJSON(w, http.StatusOK, map[string]any{
			"results": []any{}, "page": 1, "total_pages": 1, "total_results": 0,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results":       kpFilmsToCards(resp.Items),
		"page":          1,
		"total_pages":   1,
		"total_results": resp.Total,
	})
}

func (kp *KPClient) handleListSearch(w http.ResponseWriter, q url.Values, page int) {
	keyword := strings.TrimSpace(q.Get("query"))
	if keyword == "" {
		keyword = strings.TrimSpace(q.Get("search"))
	}
	if keyword == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"results": []any{}, "page": 1, "total_pages": 1, "total_results": 0,
		})
		return
	}

	resp, err := kp.SearchByKeyword(keyword, page)
	if err != nil {
		log.Warn().Err(err).Str("keyword", keyword).Msg("kp: search error")
		writeJSON(w, http.StatusOK, map[string]any{
			"results": []any{}, "page": page, "total_pages": 1, "total_results": 0,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results":       kpFilmsToCards(resp.Films),
		"page":          page,
		"total_pages":   resp.PagesCount,
		"total_results": resp.SearchFilmsCountResult,
	})
}

func (kp *KPClient) handleListFilters(w http.ResponseWriter, q url.Values, page int) {
	filmType := q.Get("type")
	if filmType == "" {
		filmType = "FILM"
	}
	genresStr := q.Get("genres")
	genres, _ := strconv.Atoi(genresStr)

	resp, err := kp.searchByFilters(filmType, genres, page)
	if err != nil {
		log.Warn().Err(err).Str("type", filmType).Int("genres", genres).Msg("kp: filter search error")
		writeJSON(w, http.StatusOK, map[string]any{
			"results": []any{}, "page": page, "total_pages": 1, "total_results": 0,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results":       kpFilmsToCards(resp.Items),
		"page":          page,
		"total_pages":   resp.TotalPages,
		"total_results": resp.Total,
	})
}

// handleCard dispatches /catalog/card?plugin=kp&uri={kpID} requests.
func (kp *KPClient) HandleCard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	uriStr := q.Get("uri")
	id, err := strconv.Atoi(uriStr)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	// Fetch film details.
	film, err := kp.getFilm(id)
	if err != nil {
		log.Warn().Err(err).Int("id", id).Msg("kp: card film error")
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	card := kpFilmToCard(*film)

	// Enrich with parallel fetches: seasons, similars, staff.
	type seasonsResult struct {
		resp *kpuSeasonsResponse
		err  error
	}
	type similarsResult struct {
		resp *kpSimilarsResponse
		err  error
	}
	type staffResult struct {
		staff []kpStaffMember
		err   error
	}

	seasonsCh := make(chan seasonsResult, 1)
	similarsCh := make(chan similarsResult, 1)
	staffCh := make(chan staffResult, 1)

	isSeries := film.Type == "TV_SERIES" || film.Type == "MINI_SERIES" || film.Type == "TV_SHOW"

	go func() {
		if isSeries {
			resp, err := kp.getSeasons(id)
			seasonsCh <- seasonsResult{resp, err}
		} else {
			seasonsCh <- seasonsResult{}
		}
	}()

	go func() {
		resp, err := kp.getSimilars(id)
		similarsCh <- similarsResult{resp, err}
	}()

	go func() {
		staff, err := kp.getStaff(id)
		staffCh <- staffResult{staff, err}
	}()

	// Collect results.
	if sr := <-seasonsCh; sr.resp != nil && sr.err == nil {
		seasons := make([]map[string]any, 0, len(sr.resp.Items))
		for _, s := range sr.resp.Items {
			episodes := make([]map[string]any, 0, len(s.Episodes))
			for _, ep := range s.Episodes {
				epName := ep.NameRu
				if epName == "" {
					epName = ep.NameEn
				}
				episodes = append(episodes, map[string]any{
					"season_number":  ep.SeasonNumber,
					"episode_number": ep.EpisodeNumber,
					"name":           epName,
					"air_date":       ep.ReleaseDate,
				})
			}
			seasons = append(seasons, map[string]any{
				"season_number": s.Number,
				"episode_count": len(s.Episodes),
				"episodes":      episodes,
			})
		}
		card["seasons"] = seasons
		card["number_of_seasons"] = len(sr.resp.Items)
	}

	if sr := <-similarsCh; sr.resp != nil && sr.err == nil {
		card["similar"] = map[string]any{
			"results": kpFilmsToCards(sr.resp.Items),
		}
		card["recommendations"] = card["similar"]
	}

	if sr := <-staffCh; sr.staff != nil && sr.err == nil {
		cast := make([]map[string]any, 0)
		crew := make([]map[string]any, 0)
		for _, m := range sr.staff {
			name := m.NameRu
			if name == "" {
				name = m.NameEn
			}
			person := map[string]any{
				"id":           m.StaffID,
				"name":         name,
				"profile_path": m.PosterURL,
			}
			if m.ProfessionKey == "ACTOR" {
				person["character"] = m.Description
				cast = append(cast, person)
			} else {
				person["job"] = m.ProfessionText
				person["department"] = m.ProfessionKey
				crew = append(crew, person)
			}
		}
		card["credits"] = map[string]any{
			"cast": cast,
			"crew": crew,
		}
	}

	writeJSON(w, http.StatusOK, card)
}
