package litesrc

import (
	"context"
	stdjson "encoding/json"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"strings"
	"time"

	"lampac-go/internal/config"
)

type filmixTVChecker struct {
	client       *http.Client
	filmixTVHost string
	filmixHost   string
	filmixToken  string
}

type filmixTVListResponse struct {
	Items []stdjson.RawMessage `json:"items"`
}

func NewFilmixTVChecker(cfg config.Config) *filmixTVChecker {
	filmixTVHost := strings.TrimSpace(cfg.Online.FilmixTV.Host)
	if filmixTVHost == "" {
		filmixTVHost = "https://api.filmix.tv"
	}
	filmixTVHost = strings.TrimRight(filmixTVHost, "/")

	filmixHost := strings.TrimSpace(cfg.Online.Filmix.Host)
	if filmixHost == "" {
		filmixHost = "http://filmixapp.cyou"
	}
	filmixHost = strings.TrimRight(filmixHost, "/")

	return &filmixTVChecker{
		client:       httpclient.NewForBalancer("filmixtv", 10*time.Second),
		filmixTVHost: filmixTVHost,
		filmixHost:   filmixHost,
		filmixToken:  strings.TrimSpace(cfg.Online.Filmix.Token),
	}
}

func (f *filmixTVChecker) Handle(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := f.checkSearch(req.Context(), req.URL.Query())
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("filmixtv"))
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "filmixtv full mode is not implemented in local mode",
			"balanser": "filmixtv",
		})
	}
}

func (f *filmixTVChecker) checkSearch(ctx context.Context, q url.Values) bool {
	stories := buildFilmixStories(q)
	if len(stories) == 0 {
		return false
	}

	for _, story := range stories {
		if story == "" {
			continue
		}
		if f.searchFilmixTV(ctx, story) {
			return true
		}
		if f.searchFilmixV2(ctx, story) {
			return true
		}
	}
	return false
}

func (f *filmixTVChecker) searchFilmixTV(ctx context.Context, story string) bool {
	u, err := url.Parse(f.filmixTVHost + "/api-fx/list")
	if err != nil {
		return false
	}
	qs := url.Values{}
	qs.Set("search", story)
	qs.Set("limit", "48")
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := f.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}

	var root filmixTVListResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&root); err != nil {
		return false
	}
	return len(root.Items) > 0
}

func (f *filmixTVChecker) searchFilmixV2(ctx context.Context, story string) bool {
	u, err := url.Parse(f.filmixHost + "/api/v2/search")
	if err != nil {
		return false
	}
	qs := url.Values{}
	qs.Set("story", story)
	qs.Set("user_dev_apk", "2.0.1")
	qs.Set("user_dev_id", "")
	qs.Set("user_dev_name", "Xiaomi")
	qs.Set("user_dev_os", "11")
	qs.Set("user_dev_token", f.filmixToken)
	qs.Set("user_dev_vendor", "Xiaomi")
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := f.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return false
	}
	return hasFilmixItems(body)
}
