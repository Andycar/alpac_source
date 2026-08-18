package xsearch

import (
	"context"
	stdjson "encoding/json"
	"net/url"
	"strings"

	"lampac-go/internal/tmdbcache"
)

// TMDBSearcher implements TextSearcher using the TMDB /3/search/multi endpoint.
type TMDBSearcher struct {
	pool   *tmdbcache.Pool
	apiKey string
}

// NewTMDBSearcher creates a TMDB text searcher.
func NewTMDBSearcher(pool *tmdbcache.Pool, apiKey string) *TMDBSearcher {
	return &TMDBSearcher{pool: pool, apiKey: apiKey}
}

func (t *TMDBSearcher) Name() string { return "tmdb" }

func (t *TMDBSearcher) SearchText(ctx context.Context, query string) ([]SearchResult, error) {
	key := t.apiKey
	if key == "" {
		key = t.pool.APIKey()
	}
	if t.pool == nil || key == "" {
		return nil, nil
	}

	qs := url.Values{}
	qs.Set("api_key", key)
	qs.Set("query", query)
	qs.Set("language", "ru")
	qs.Set("page", "1")

	res, err := t.pool.FetchAPI(ctx, "3/search/multi", qs.Encode())
	if err != nil {
		return nil, err
	}

	var resp tmdbSearchResponse
	if err := stdjson.Unmarshal(res.Body, &resp); err != nil {
		return nil, err
	}

	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.MediaType != "movie" && r.MediaType != "tv" {
			continue
		}

		sr := SearchResult{
			TmdbID:  r.ID,
			Poster:  tmdbPoster(r.PosterPath),
			Quality: "", // TMDB is a metadata source, not a balancer
		}

		if r.MediaType == "movie" {
			sr.Title = r.Title
			sr.OriginalTitle = r.OriginalTitle
			sr.Year = parseYear(r.ReleaseDate)
			sr.ContentType = "movie"
		} else {
			sr.Title = r.Name
			sr.OriginalTitle = r.OriginalName
			sr.Year = parseYear(r.FirstAirDate)
			sr.ContentType = "serial"
		}

		if sr.Title == "" {
			continue
		}

		// TMDB is a metadata source — mark as such in balancer field.
		sr.Balancer = "tmdb"

		out = append(out, sr)
	}

	return out, nil
}

type tmdbSearchResponse struct {
	Results []tmdbSearchItem `json:"results"`
}

type tmdbSearchItem struct {
	ID            int    `json:"id"`
	MediaType     string `json:"media_type"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	Name          string `json:"name"`
	OriginalName  string `json:"original_name"`
	ReleaseDate   string `json:"release_date"`
	FirstAirDate  string `json:"first_air_date"`
	PosterPath    string `json:"poster_path"`
}

func tmdbPoster(path string) string {
	if path == "" {
		return ""
	}
	return "https://image.tmdb.org/t/p/w300" + path
}

func parseYear(date string) int {
	if len(date) < 4 {
		return 0
	}
	y := 0
	for i := range 4 {
		c := date[i]
		if c < '0' || c > '9' {
			return 0
		}
		y = y*10 + int(c-'0')
	}
	return y
}

// EnrichWithExternalIDs fetches TMDB external_ids for movies/TV to get imdb_id.
// This is called after initial search to enrich merged results with IDs.
func EnrichWithExternalIDs(ctx context.Context, pool *tmdbcache.Pool, apiKey string, results []MergedResult) {
	if apiKey == "" && pool != nil {
		apiKey = pool.APIKey()
	}
	if pool == nil || apiKey == "" {
		return
	}

	type job struct {
		idx  int
		path string
	}

	var jobs []job
	for i, r := range results {
		if r.TmdbID > 0 && r.ImdbID == "" {
			mediaType := "movie"
			if r.ContentType == "serial" {
				mediaType = "tv"
			}
			jobs = append(jobs, job{idx: i, path: "3/" + mediaType + "/" + itoa(r.TmdbID) + "/external_ids"})
		}
	}

	if len(jobs) == 0 {
		return
	}

	// Limit enrichment to 10 concurrent calls.
	sem := make(chan struct{}, 10)
	type result struct {
		idx  int
		imdb string
		kpID int64
	}
	ch := make(chan result, len(jobs))

	for _, j := range jobs {
		go func(j job) {
			sem <- struct{}{}
			defer func() { <-sem }()

			qs := url.Values{}
			qs.Set("api_key", apiKey)

			res, err := pool.FetchAPI(ctx, j.path, qs.Encode())
			if err != nil {
				ch <- result{idx: j.idx}
				return
			}

			var ext struct {
				ImdbID string `json:"imdb_id"`
			}
			if err := stdjson.Unmarshal(res.Body, &ext); err != nil {
				ch <- result{idx: j.idx}
				return
			}
			ch <- result{idx: j.idx, imdb: strings.TrimSpace(ext.ImdbID)}
		}(j)
	}

	for range jobs {
		r := <-ch
		if r.imdb != "" {
			results[r.idx].ImdbID = r.imdb
		}
	}
}
