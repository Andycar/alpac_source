package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type tmdbClient struct {
	host    string
	apiKey  string
	lang    string
	client  *http.Client
	retries int
	verbose bool
}

func newTMDB(host, apiKey, lang string, timeout time.Duration, retries int, verbose bool) *tmdbClient {
	return &tmdbClient{
		host:    strings.TrimRight(host, "/"),
		apiKey:  apiKey,
		lang:    lang,
		client:  &http.Client{Timeout: timeout},
		retries: retries,
		verbose: verbose,
	}
}

type tmdbSearchResp struct {
	Results []tmdbResult `json:"results"`
}

type tmdbResult struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`          // movie
	Name          string `json:"name"`           // tv
	OriginalTitle string `json:"original_title"` // movie
	OriginalName  string `json:"original_name"`  // tv
	ReleaseDate   string `json:"release_date"`   // movie YYYY-MM-DD
	FirstAirDate  string `json:"first_air_date"` // tv YYYY-MM-DD
	MediaType     string `json:"media_type"`     // от search/multi: "movie"/"tv"/"person"
	Popularity    float64 `json:"popularity"`
	VoteCount     int     `json:"vote_count"`
}

func (t *tmdbClient) SearchMovie(ctx context.Context, query string, year int) ([]tmdbResult, error) {
	q := url.Values{}
	q.Set("api_key", t.apiKey)
	q.Set("query", query)
	q.Set("language", t.lang)
	q.Set("include_adult", "true")
	if year > 0 {
		q.Set("year", fmt.Sprintf("%d", year))
	}
	return t.search(ctx, "/3/search/movie", q)
}

func (t *tmdbClient) SearchTV(ctx context.Context, query string, year int) ([]tmdbResult, error) {
	q := url.Values{}
	q.Set("api_key", t.apiKey)
	q.Set("query", query)
	q.Set("language", t.lang)
	q.Set("include_adult", "true")
	if year > 0 {
		q.Set("first_air_date_year", fmt.Sprintf("%d", year))
	}
	results, err := t.search(ctx, "/3/search/tv", q)
	if err != nil {
		return nil, err
	}
	for i := range results {
		results[i].MediaType = "tv"
	}
	return results, nil
}

func (t *tmdbClient) search(ctx context.Context, path string, q url.Values) ([]tmdbResult, error) {
	u := t.host + path + "?" + q.Encode()
	body, err := t.do(ctx, u)
	if err != nil {
		return nil, err
	}
	var resp tmdbSearchResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %v (head: %s)", err, snippet(body, 200))
	}
	return resp.Results, nil
}

type tmdbExternalIDs struct {
	IMDBID string `json:"imdb_id"`
}

func (t *tmdbClient) ExternalIDs(ctx context.Context, kind string, id int) (*tmdbExternalIDs, error) {
	if kind != "movie" && kind != "tv" {
		return nil, fmt.Errorf("invalid kind: %s", kind)
	}
	u := fmt.Sprintf("%s/3/%s/%d/external_ids?api_key=%s", t.host, kind, id, t.apiKey)
	body, err := t.do(ctx, u)
	if err != nil {
		return nil, err
	}
	var resp tmdbExternalIDs
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (t *tmdbClient) do(ctx context.Context, urlStr string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= t.retries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "sctsenrich/1.0")
		resp, err := t.client.Do(req)
		if err != nil {
			lastErr = err
			if attempt < t.retries {
				time.Sleep(time.Duration(1<<attempt) * 500 * time.Millisecond)
				continue
			}
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		switch {
		case resp.StatusCode == 200:
			return body, nil
		case resp.StatusCode == 404:
			return nil, errors.New("404")
		case resp.StatusCode == 429 || resp.StatusCode >= 500:
			if attempt < t.retries {
				wait := time.Duration(1<<attempt) * 500 * time.Millisecond
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					// упрощённо — секундное значение
					if d, err := time.ParseDuration(ra + "s"); err == nil {
						wait = d
					}
				}
				time.Sleep(wait)
				continue
			}
			return nil, fmt.Errorf("HTTP %d (исчерпано retries): %s", resp.StatusCode, snippet(body, 200))
		default:
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(body, 200))
		}
	}
	return nil, lastErr
}
