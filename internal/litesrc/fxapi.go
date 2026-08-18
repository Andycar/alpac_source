package litesrc

import (
	"context"
	stdjson "encoding/json"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
)

type fxapiChecker struct {
	client       *http.Client
	filmixHost   string
	filmixTVHost string
	token        string
}

type fxapiSearchItem struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`
	Year          int    `json:"year"`
}

type fxapiListResponse struct {
	Items []fxapiSearchItem `json:"items"`
}

func NewFXAPIChecker(cfg config.Config) *fxapiChecker {
	filmixHost := strings.TrimSpace(cfg.Online.Filmix.Host)
	if filmixHost == "" {
		filmixHost = "http://filmixapp.cyou"
	}
	filmixHost = strings.TrimRight(filmixHost, "/")

	filmixTVHost := strings.TrimSpace(cfg.Online.FilmixTV.Host)
	if filmixTVHost == "" {
		filmixTVHost = "https://api.filmix.tv"
	}
	filmixTVHost = strings.TrimRight(filmixTVHost, "/")

	return &fxapiChecker{
		client:       httpclient.NewForBalancer("fxapi", 10*time.Second),
		filmixHost:   filmixHost,
		filmixTVHost: filmixTVHost,
		token:        strings.TrimSpace(cfg.Online.FilmixPartner.Token),
	}
}

func (f *fxapiChecker) Handle(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			if f.checkSearch(req.Context(), req.URL.Query()) {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("data-json="))
				return
			}

			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "fxapi full mode is not yet implemented",
			"balanser": "fxapi",
		})
	}
}

func (f *fxapiChecker) checkSearch(ctx context.Context, q url.Values) bool {
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	if title == "" && originalTitle == "" {
		return false
	}

	year := parseYear(q.Get("year"))

	// Parity with legacy fxapi checksearch:
	// only a unique exact match (id) is treated as positive.
	if items := f.searchFilmixV2(ctx, title); len(items) > 0 {
		return hasUniqueFXAPIID(items, title, originalTitle, year)
	}

	if items := f.searchFilmixTV(ctx, originalTitle); len(items) > 0 {
		return hasUniqueFXAPIID(items, title, originalTitle, year)
	}
	if items := f.searchFilmixTV(ctx, title); len(items) > 0 {
		return hasUniqueFXAPIID(items, title, originalTitle, year)
	}

	return false
}

func parseYear(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	year, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return year
}

func (f *fxapiChecker) searchFilmixV2(ctx context.Context, story string) []fxapiSearchItem {
	story = strings.TrimSpace(story)
	if story == "" {
		return nil
	}

	u, err := url.Parse(f.filmixHost + "/api/v2/search")
	if err != nil {
		return nil
	}
	qs := url.Values{}
	qs.Set("story", story)
	qs.Set("user_dev_apk", "2.0.1")
	qs.Set("user_dev_id", "")
	qs.Set("user_dev_name", "Xiaomi")
	qs.Set("user_dev_os", "11")
	qs.Set("user_dev_token", f.token)
	qs.Set("user_dev_vendor", "Xiaomi")
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil
	}

	var items []fxapiSearchItem
	if err := stdjson.Unmarshal(body, &items); err != nil {
		return nil
	}
	return items
}

func (f *fxapiChecker) searchFilmixTV(ctx context.Context, story string) []fxapiSearchItem {
	story = strings.TrimSpace(story)
	if story == "" {
		return nil
	}

	u, err := url.Parse(f.filmixTVHost + "/api-fx/list")
	if err != nil {
		return nil
	}
	qs := url.Values{}
	qs.Set("search", story)
	qs.Set("limit", "48")
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	var root fxapiListResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&root); err != nil {
		return nil
	}
	return root.Items
}

func hasUniqueFXAPIID(items []fxapiSearchItem, title, originalTitle string, year int) bool {
	if len(items) == 0 || year == 0 {
		return false
	}

	wantTitle := normalizeSearchTitle(title)
	wantOriginal := normalizeSearchTitle(originalTitle)

	ids := make(map[int]struct{}, 2)
	for _, item := range items {
		if item.ID == 0 {
			continue
		}
		if item.Year != year {
			continue
		}

		itemTitle := normalizeSearchTitle(item.Title)
		itemOriginal := normalizeSearchTitle(item.OriginalTitle)
		if itemOriginal == "" {
			itemOriginal = normalizeSearchTitle(item.OriginalName)
		}

		matchTitle := wantTitle != "" && itemTitle == wantTitle
		matchOriginal := wantOriginal != "" && itemOriginal == wantOriginal
		if matchTitle || matchOriginal {
			ids[item.ID] = struct{}{}
		}
	}

	return len(ids) == 1
}
