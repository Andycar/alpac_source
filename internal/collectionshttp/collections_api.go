package collectionshttp

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/tmdbcache"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  Cache (reusable pattern from kp_catalog.go)
// ---------------------------------------------------------------------------

type collCache struct {
	mu      sync.RWMutex
	entries map[string]*collCacheEntry
}

type collCacheEntry struct {
	data    any
	expires time.Time
}

func newCollCache() *collCache {
	c := &collCache{entries: make(map[string]*collCacheEntry)}
	go c.cleanupLoop()
	return c
}

func (c *collCache) get(key string) (any, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.data, true
}

func (c *collCache) set(key string, data any, ttl time.Duration) {
	c.mu.Lock()
	c.entries[key] = &collCacheEntry{data: data, expires: time.Now().Add(ttl)}
	c.mu.Unlock()
}

func (c *collCache) size() int {
	c.mu.RLock()
	n := len(c.entries)
	c.mu.RUnlock()
	return n
}

func (c *collCache) cleanupLoop() {
	for {
		time.Sleep(10 * time.Minute)
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
//  Pinned persons store
// ---------------------------------------------------------------------------

type pinnedPersons struct {
	mu        sync.RWMutex
	filePath  string
	Directors []int `json:"directors"`
	Actors    []int `json:"actors"`
}

func newPinnedPersons(repoRoot string) *pinnedPersons {
	dir := filepath.Join(repoRoot, "database", "collections")
	fp := filepath.Join(dir, "pinned.json")
	p := &pinnedPersons{filePath: fp}
	data, err := os.ReadFile(fp)
	if err == nil {
		_ = stdjson.Unmarshal(data, p)
	}
	return p
}

func (p *pinnedPersons) save() error {
	p.mu.RLock()
	data, err := stdjson.MarshalIndent(p, "", "  ")
	p.mu.RUnlock()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(p.filePath, data, 0o644)
}

func (p *pinnedPersons) addDirector(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if slices.Contains(p.Directors, id) {
		return
	}
	p.Directors = append(p.Directors, id)
}

func (p *pinnedPersons) addActor(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if slices.Contains(p.Actors, id) {
		return
	}
	p.Actors = append(p.Actors, id)
}

func (p *pinnedPersons) remove(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Directors = removeInt(p.Directors, id)
	p.Actors = removeInt(p.Actors, id)
}

func (p *pinnedPersons) allIDs() []int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	seen := map[int]bool{}
	var ids []int
	for _, id := range p.Directors {
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	for _, id := range p.Actors {
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

func removeInt(s []int, v int) []int {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
//  TMDB response types
// ---------------------------------------------------------------------------

type tmdbPersonSearchResult struct {
	ID                 int     `json:"id"`
	Name               string  `json:"name"`
	ProfilePath        string  `json:"profile_path"`
	KnownForDepartment string  `json:"known_for_department"`
	KnownFor           []any   `json:"known_for"`
	Popularity         float64 `json:"popularity"`
}

type tmdbPersonSearchResponse struct {
	Page         int                      `json:"page"`
	TotalPages   int                      `json:"total_pages"`
	TotalResults int                      `json:"total_results"`
	Results      []tmdbPersonSearchResult `json:"results"`
}

type tmdbPersonDetail struct {
	ID                 int    `json:"id"`
	Name               string `json:"name"`
	Biography          string `json:"biography"`
	ProfilePath        string `json:"profile_path"`
	KnownForDepartment string `json:"known_for_department"`
	Birthday           string `json:"birthday"`
	Deathday           string `json:"deathday"`
	PlaceOfBirth       string `json:"place_of_birth"`
}

type tmdbCreditItem struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"`
	Name          string  `json:"name"`
	OriginalTitle string  `json:"original_title"`
	OriginalName  string  `json:"original_name"`
	PosterPath    string  `json:"poster_path"`
	BackdropPath  string  `json:"backdrop_path"`
	Overview      string  `json:"overview"`
	VoteAverage   float64 `json:"vote_average"`
	VoteCount     int     `json:"vote_count"`
	ReleaseDate   string  `json:"release_date"`
	FirstAirDate  string  `json:"first_air_date"`
	Character     string  `json:"character"`
	Job           string  `json:"job"`
	Department    string  `json:"department"`
	MediaType     string  `json:"media_type"`
}

type tmdbMovieCreditsResponse struct {
	Cast []tmdbCreditItem `json:"cast"`
	Crew []tmdbCreditItem `json:"crew"`
}

type tmdbTVCreditsResponse struct {
	Cast []tmdbCreditItem `json:"cast"`
	Crew []tmdbCreditItem `json:"crew"`
}

type tmdbGenre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type tmdbGenreListResponse struct {
	Genres []tmdbGenre `json:"genres"`
}

type tmdbDiscoverResponse struct {
	Page         int              `json:"page"`
	TotalPages   int              `json:"total_pages"`
	TotalResults int              `json:"total_results"`
	Results      []map[string]any `json:"results"`
}

type tmdbCompanySearchResult struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	LogoPath string `json:"logo_path"`
}

type tmdbCompanySearchResponse struct {
	Page         int                       `json:"page"`
	TotalPages   int                       `json:"total_pages"`
	TotalResults int                       `json:"total_results"`
	Results      []tmdbCompanySearchResult `json:"results"`
}

type tmdbTrendingResponse struct {
	Page         int                      `json:"page"`
	TotalPages   int                      `json:"total_pages"`
	TotalResults int                      `json:"total_results"`
	Results      []tmdbPersonSearchResult `json:"results"`
}

// ---------------------------------------------------------------------------
//  Handler
// ---------------------------------------------------------------------------

type collectionsHandler struct {
	pool   *tmdbcache.Pool
	apiKey string
	cfg    config.CollectionsConfig
	cache  *collCache
	pinned *pinnedPersons
}

func newCollectionsHandler(pool *tmdbcache.Pool, apiKey string, cfg config.CollectionsConfig, repoRoot string) *collectionsHandler {
	if cfg.CacheTTLMin <= 0 {
		cfg.CacheTTLMin = 60
	}
	return &collectionsHandler{
		pool:   pool,
		apiKey: apiKey,
		cfg:    cfg,
		cache:  newCollCache(),
		pinned: newPinnedPersons(repoRoot),
	}
}

func (h *collectionsHandler) baseTTL() time.Duration {
	return time.Duration(h.cfg.CacheTTLMin) * time.Minute
}

// fetch does a TMDB API request via the pool and decodes JSON into dst.
func (h *collectionsHandler) fetch(ctx context.Context, path string, params url.Values, dst any) error {
	if params == nil {
		params = url.Values{}
	}
	// Use configured key first, fall back to key captured from client TMDB requests.
	key := h.apiKey
	if key == "" {
		key = h.pool.APIKey()
	}
	if key == "" {
		return fmt.Errorf("no TMDB API key available")
	}
	params.Set("api_key", key)
	if params.Get("language") == "" {
		params.Set("language", "ru")
	}
	res, err := h.pool.FetchAPI(ctx, path, params.Encode())
	if err != nil {
		return err
	}
	return stdjson.Unmarshal(res.Body, dst)
}

// ---------------------------------------------------------------------------
//  Person endpoints
// ---------------------------------------------------------------------------

func (h *collectionsHandler) handlePersonSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q parameter required"})
		return
	}
	cacheKey := "person_search:" + q
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	params := url.Values{"query": {q}}
	var resp tmdbPersonSearchResponse
	if err := h.fetch(r.Context(), "3/search/person", params, &resp); err != nil {
		log.Warn().Err(err).Str("query", q).Msg("collections: person search failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	result := map[string]any{
		"results":       resp.Results,
		"page":          resp.Page,
		"total_pages":   resp.TotalPages,
		"total_results": resp.TotalResults,
	}
	h.cache.set(cacheKey, result, 30*time.Minute)
	writeJSON(w, http.StatusOK, result)
}

func (h *collectionsHandler) handlePersonDetail(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "personID")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid person id"})
		return
	}
	cacheKey := fmt.Sprintf("person_detail:%d", id)
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	var detail tmdbPersonDetail
	if err := h.fetch(r.Context(), fmt.Sprintf("3/person/%d", id), nil, &detail); err != nil {
		log.Warn().Err(err).Int("id", id).Msg("collections: person detail failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	h.cache.set(cacheKey, detail, 24*time.Hour)
	writeJSON(w, http.StatusOK, detail)
}

func (h *collectionsHandler) handlePersonMovies(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "personID")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid person id"})
		return
	}

	cacheKey := fmt.Sprintf("person_movies:%d", id)
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	var credits tmdbMovieCreditsResponse
	if err := h.fetch(r.Context(), fmt.Sprintf("3/person/%d/movie_credits", id), nil, &credits); err != nil {
		log.Warn().Err(err).Int("id", id).Msg("collections: person movie credits failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	result := h.buildCreditsResponse(credits.Cast, credits.Crew, "movie")
	h.cache.set(cacheKey, result, 2*time.Hour)
	writeJSON(w, http.StatusOK, result)
}

func (h *collectionsHandler) handlePersonTV(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "personID")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid person id"})
		return
	}

	cacheKey := fmt.Sprintf("person_tv:%d", id)
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	var credits tmdbTVCreditsResponse
	if err := h.fetch(r.Context(), fmt.Sprintf("3/person/%d/tv_credits", id), nil, &credits); err != nil {
		log.Warn().Err(err).Int("id", id).Msg("collections: person tv credits failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	result := h.buildCreditsResponse(credits.Cast, credits.Crew, "tv")
	h.cache.set(cacheKey, result, 2*time.Hour)
	writeJSON(w, http.StatusOK, result)
}

func (h *collectionsHandler) handlePersonPopular(w http.ResponseWriter, r *http.Request) {
	page := r.URL.Query().Get("page")
	if page == "" {
		page = "1"
	}
	cacheKey := "person_popular:p" + page
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	params := url.Values{"page": {page}}
	var resp tmdbPersonSearchResponse
	if err := h.fetch(r.Context(), "3/person/popular", params, &resp); err != nil {
		log.Warn().Err(err).Msg("collections: popular persons failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	result := map[string]any{
		"results":       resp.Results,
		"page":          resp.Page,
		"total_pages":   resp.TotalPages,
		"total_results": resp.TotalResults,
	}
	h.cache.set(cacheKey, result, h.baseTTL())
	writeJSON(w, http.StatusOK, result)
}

// ---------------------------------------------------------------------------
//  Genre endpoints
// ---------------------------------------------------------------------------

func (h *collectionsHandler) handleGenreList(w http.ResponseWriter, r *http.Request) {
	const cacheKey = "genre_list"
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	ctx := r.Context()
	var movieGenres, tvGenres tmdbGenreListResponse
	var errM, errT error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errM = h.fetch(ctx, "3/genre/movie/list", nil, &movieGenres) }()
	go func() { defer wg.Done(); errT = h.fetch(ctx, "3/genre/tv/list", nil, &tvGenres) }()
	wg.Wait()

	if errM != nil && errT != nil {
		log.Warn().Err(errM).Msg("collections: genre list failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	result := map[string]any{
		"genres_movie": movieGenres.Genres,
		"genres_tv":    tvGenres.Genres,
	}
	h.cache.set(cacheKey, result, 24*time.Hour)
	writeJSON(w, http.StatusOK, result)
}

func (h *collectionsHandler) handleGenreMovies(w http.ResponseWriter, r *http.Request) {
	genreID := chi.URLParam(r, "genreID")
	page := r.URL.Query().Get("page")
	if page == "" {
		page = "1"
	}
	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "popularity.desc"
	}

	cacheKey := fmt.Sprintf("genre_movies:%s:s%s:p%s", genreID, sortBy, page)
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	params := url.Values{
		"with_genres":    {genreID},
		"sort_by":        {sortBy},
		"vote_count.gte": {"50"},
		"page":           {page},
	}
	var resp tmdbDiscoverResponse
	if err := h.fetch(r.Context(), "3/discover/movie", params, &resp); err != nil {
		log.Warn().Err(err).Str("genre", genreID).Msg("collections: discover movie failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	// Stamp source on each result.
	for i := range resp.Results {
		resp.Results[i]["source"] = "tmdb"
	}

	result := map[string]any{
		"results":       resp.Results,
		"page":          resp.Page,
		"total_pages":   resp.TotalPages,
		"total_results": resp.TotalResults,
	}
	h.cache.set(cacheKey, result, h.baseTTL())
	writeJSON(w, http.StatusOK, result)
}

func (h *collectionsHandler) handleGenreTV(w http.ResponseWriter, r *http.Request) {
	genreID := chi.URLParam(r, "genreID")
	page := r.URL.Query().Get("page")
	if page == "" {
		page = "1"
	}
	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "popularity.desc"
	}

	cacheKey := fmt.Sprintf("genre_tv:%s:s%s:p%s", genreID, sortBy, page)
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	params := url.Values{
		"with_genres":    {genreID},
		"sort_by":        {sortBy},
		"vote_count.gte": {"50"},
		"page":           {page},
	}
	var resp tmdbDiscoverResponse
	if err := h.fetch(r.Context(), "3/discover/tv", params, &resp); err != nil {
		log.Warn().Err(err).Str("genre", genreID).Msg("collections: discover tv failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	for i := range resp.Results {
		resp.Results[i]["source"] = "tmdb"
	}

	result := map[string]any{
		"results":       resp.Results,
		"page":          resp.Page,
		"total_pages":   resp.TotalPages,
		"total_results": resp.TotalResults,
	}
	h.cache.set(cacheKey, result, h.baseTTL())
	writeJSON(w, http.StatusOK, result)
}

// ---------------------------------------------------------------------------
//  Studio endpoints
// ---------------------------------------------------------------------------

func (h *collectionsHandler) handleStudioSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q parameter required"})
		return
	}
	cacheKey := "studio_search:" + q
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	params := url.Values{"query": {q}}
	var resp tmdbCompanySearchResponse
	if err := h.fetch(r.Context(), "3/search/company", params, &resp); err != nil {
		log.Warn().Err(err).Str("query", q).Msg("collections: studio search failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	result := map[string]any{
		"results":       resp.Results,
		"page":          resp.Page,
		"total_pages":   resp.TotalPages,
		"total_results": resp.TotalResults,
	}
	h.cache.set(cacheKey, result, 30*time.Minute)
	writeJSON(w, http.StatusOK, result)
}

func (h *collectionsHandler) handleStudioMovies(w http.ResponseWriter, r *http.Request) {
	studioID := chi.URLParam(r, "studioID")
	page := r.URL.Query().Get("page")
	if page == "" {
		page = "1"
	}

	cacheKey := fmt.Sprintf("studio_movies:%s:p%s", studioID, page)
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	params := url.Values{
		"with_companies": {studioID},
		"sort_by":        {"popularity.desc"},
		"page":           {page},
	}
	var resp tmdbDiscoverResponse
	if err := h.fetch(r.Context(), "3/discover/movie", params, &resp); err != nil {
		log.Warn().Err(err).Str("studio", studioID).Msg("collections: studio movies failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	for i := range resp.Results {
		resp.Results[i]["source"] = "tmdb"
	}

	result := map[string]any{
		"results":       resp.Results,
		"page":          resp.Page,
		"total_pages":   resp.TotalPages,
		"total_results": resp.TotalResults,
	}
	h.cache.set(cacheKey, result, h.baseTTL())
	writeJSON(w, http.StatusOK, result)
}

// ---------------------------------------------------------------------------
//  Trending
// ---------------------------------------------------------------------------

func (h *collectionsHandler) handleTrending(w http.ResponseWriter, r *http.Request) {
	page := r.URL.Query().Get("page")
	if page == "" {
		page = "1"
	}
	cacheKey := "trending:p" + page
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	params := url.Values{"page": {page}}
	var resp tmdbTrendingResponse
	if err := h.fetch(r.Context(), "3/trending/person/week", params, &resp); err != nil {
		log.Warn().Err(err).Msg("collections: trending persons failed")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream error"})
		return
	}

	result := map[string]any{
		"results":       resp.Results,
		"page":          resp.Page,
		"total_pages":   resp.TotalPages,
		"total_results": resp.TotalResults,
	}
	h.cache.set(cacheKey, result, 6*time.Hour)
	writeJSON(w, http.StatusOK, result)
}

// ---------------------------------------------------------------------------
//  Featured (dynamic, no hardcode)
// ---------------------------------------------------------------------------

func (h *collectionsHandler) handleFeatured(w http.ResponseWriter, r *http.Request) {
	const cacheKey = "featured"
	if v, ok := h.cache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}

	ctx := r.Context()

	// Fetch trending + popular + genres in parallel.
	var trending, popular tmdbTrendingResponse
	var movieGenres, tvGenres tmdbGenreListResponse
	var errTrend, errPop, errMG, errTG error

	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); errTrend = h.fetch(ctx, "3/trending/person/week", nil, &trending) }()
	go func() { defer wg.Done(); errPop = h.fetch(ctx, "3/person/popular", nil, &popular) }()
	go func() { defer wg.Done(); errMG = h.fetch(ctx, "3/genre/movie/list", nil, &movieGenres) }()
	go func() { defer wg.Done(); errTG = h.fetch(ctx, "3/genre/tv/list", nil, &tvGenres) }()
	wg.Wait()

	// Merge trending + popular persons, split by department.
	allPersons := trending.Results
	if errTrend != nil {
		allPersons = nil
	}
	if errPop == nil {
		allPersons = append(allPersons, popular.Results...)
	}

	directors := h.filterPersonsByDept(allPersons, "Directing", 10)
	actors := h.filterPersonsByDept(allPersons, "Acting", 10)

	// Prepend pinned persons (resolve details from cache/TMDB).
	directors = h.prependPinned(ctx, h.pinned.Directors, directors, "Directing")
	actors = h.prependPinned(ctx, h.pinned.Actors, actors, "Acting")

	var gm, gt any
	if errMG == nil {
		gm = movieGenres.Genres
	}
	if errTG == nil {
		gt = tvGenres.Genres
	}

	result := map[string]any{
		"directors":    directors,
		"actors":       actors,
		"genres_movie": gm,
		"genres_tv":    gt,
	}
	h.cache.set(cacheKey, result, 6*time.Hour)
	writeJSON(w, http.StatusOK, result)
}

// filterPersonsByDept filters persons by known_for_department and deduplicates.
func (h *collectionsHandler) filterPersonsByDept(persons []tmdbPersonSearchResult, dept string, limit int) []tmdbPersonSearchResult {
	seen := map[int]bool{}
	var out []tmdbPersonSearchResult
	for _, p := range persons {
		if seen[p.ID] || p.KnownForDepartment != dept {
			continue
		}
		seen[p.ID] = true
		out = append(out, p)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// prependPinned fetches details for pinned IDs and prepends them.
func (h *collectionsHandler) prependPinned(ctx context.Context, pinnedIDs []int, existing []tmdbPersonSearchResult, dept string) []tmdbPersonSearchResult {
	if len(pinnedIDs) == 0 {
		return existing
	}

	existingIDs := map[int]bool{}
	for _, p := range existing {
		existingIDs[p.ID] = true
	}

	var prepend []tmdbPersonSearchResult
	for _, id := range pinnedIDs {
		if existingIDs[id] {
			// Move from existing to front.
			for i, p := range existing {
				if p.ID == id {
					prepend = append(prepend, p)
					existing = append(existing[:i], existing[i+1:]...)
					break
				}
			}
			continue
		}
		// Fetch person detail.
		cacheKey := fmt.Sprintf("person_detail:%d", id)
		if v, ok := h.cache.get(cacheKey); ok {
			if detail, ok := v.(tmdbPersonDetail); ok {
				prepend = append(prepend, tmdbPersonSearchResult{
					ID: detail.ID, Name: detail.Name,
					ProfilePath: detail.ProfilePath, KnownForDepartment: dept,
				})
				continue
			}
		}
		var detail tmdbPersonDetail
		if err := h.fetch(ctx, fmt.Sprintf("3/person/%d", id), nil, &detail); err == nil {
			h.cache.set(cacheKey, detail, 24*time.Hour)
			prepend = append(prepend, tmdbPersonSearchResult{
				ID: detail.ID, Name: detail.Name,
				ProfilePath: detail.ProfilePath, KnownForDepartment: dept,
			})
		}
	}

	return append(prepend, existing...)
}

// ---------------------------------------------------------------------------
//  Pin endpoints (admin)
// ---------------------------------------------------------------------------

func (h *collectionsHandler) handlePin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   int    `json:"id"`
		Type string `json:"type"` // "director" or "actor"
	}
	if err := stdjson.NewDecoder(r.Body).Decode(&body); err != nil || body.ID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id and type required"})
		return
	}
	if body.Type == "director" {
		h.pinned.addDirector(body.ID)
	} else {
		h.pinned.addActor(body.ID)
	}
	if err := h.pinned.save(); err != nil {
		log.Warn().Err(err).Msg("collections: failed to save pinned")
	}
	// Invalidate featured cache.
	h.cache.set("featured", nil, 0)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *collectionsHandler) handleUnpin(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "personID")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid person id"})
		return
	}
	h.pinned.remove(id)
	if err := h.pinned.save(); err != nil {
		log.Warn().Err(err).Msg("collections: failed to save pinned")
	}
	h.cache.set("featured", nil, 0)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *collectionsHandler) handlePinnedList(w http.ResponseWriter, r *http.Request) {
	h.pinned.mu.RLock()
	result := map[string]any{
		"directors": h.pinned.Directors,
		"actors":    h.pinned.Actors,
	}
	h.pinned.mu.RUnlock()
	writeJSON(w, http.StatusOK, result)
}

// ---------------------------------------------------------------------------
//  Stats (for admin)
// ---------------------------------------------------------------------------

func (h *collectionsHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":       h.cfg.Enable,
		"cache_entries": h.cache.size(),
		"cache_ttl_min": h.cfg.CacheTTLMin,
		"pinned":        h.pinned.allIDs(),
	})
}

// ---------------------------------------------------------------------------
//  Credits → Lampa cards
// ---------------------------------------------------------------------------

func (h *collectionsHandler) buildCreditsResponse(cast, crew []tmdbCreditItem, mediaType string) map[string]any {
	// Separate: cast items, director items (from crew).
	var castCards, directorCards []map[string]any

	seen := map[int]bool{}
	for _, c := range cast {
		if seen[c.ID] || c.ID == 0 {
			continue
		}
		seen[c.ID] = true
		castCards = append(castCards, creditToCard(c, mediaType, c.Character))
	}

	for _, c := range crew {
		if seen[c.ID] || c.ID == 0 {
			continue
		}
		if c.Job == "Director" || c.Department == "Directing" {
			seen[c.ID] = true
			directorCards = append(directorCards, creditToCard(c, mediaType, c.Job))
		}
	}

	// Sort by vote_count desc.
	sortCardsByVotes(castCards)
	sortCardsByVotes(directorCards)

	return map[string]any{
		"cast":     castCards,
		"directed": directorCards,
		"total":    len(castCards) + len(directorCards),
	}
}

func creditToCard(c tmdbCreditItem, mediaType, role string) map[string]any {
	title := c.Title
	if title == "" {
		title = c.Name
	}
	origTitle := c.OriginalTitle
	if origTitle == "" {
		origTitle = c.OriginalName
	}
	releaseDate := c.ReleaseDate
	if releaseDate == "" {
		releaseDate = c.FirstAirDate
	}

	card := map[string]any{
		"id":             c.ID,
		"title":          title,
		"name":           c.Name,
		"original_title": origTitle,
		"original_name":  c.OriginalName,
		"poster_path":    c.PosterPath,
		"backdrop_path":  c.BackdropPath,
		"overview":       c.Overview,
		"vote_average":   c.VoteAverage,
		"vote_count":     c.VoteCount,
		"release_date":   releaseDate,
		"source":         "tmdb",
		"role":           role,
	}

	// Lampa needs "method" to determine movie vs tv when opening full view.
	if mediaType == "tv" {
		card["first_air_date"] = c.FirstAirDate
	}

	return card
}

func sortCardsByVotes(cards []map[string]any) {
	sort.Slice(cards, func(i, j int) bool {
		vi, _ := cards[i]["vote_count"].(int)
		vj, _ := cards[j]["vote_count"].(int)
		return vi > vj
	})
}
