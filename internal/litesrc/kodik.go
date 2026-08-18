package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

type KodikChecker struct {
	client  *http.Client
	apiHost string
	token   string
}

// Token/APIHost/Client expose the fields the xsearch adapter snapshots.
func (c *KodikChecker) Token() string        { return c.token }
func (c *KodikChecker) APIHost() string      { return c.apiHost }
func (c *KodikChecker) Client() *http.Client { return c.client }

func NewKodikChecker(cfg config.Config) *KodikChecker {
	apiHost := strings.TrimSpace(cfg.Online.Kodik.APIHost)
	if apiHost == "" {
		apiHost = "https://kodik-api.com"
	}
	apiHost = strings.TrimRight(apiHost, "/")
	return &KodikChecker{
		client:  httpclient.NewForBalancer("kodik", 10*time.Second),
		apiHost: apiHost,
		token:   strings.TrimSpace(cfg.Online.Kodik.Token),
	}
}

func (k *KodikChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := k.checkSearch(req.Context(), req.URL.Query())
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if show {
				quality := pluginQualityBadgeGet("kodik")
				if quality != "" {
					_, _ = fmt.Fprintf(w, `{"type":"movie","rch":false,"quality":"%s"}`, quality)
				} else {
					_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
				}
				return
			}
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		k.indexLocal(w, req, links)
	}
}

type kodikSearchResponse struct {
	Results []stdjson.RawMessage `json:"results"`
}

func (k *KodikChecker) checkSearch(ctx context.Context, q url.Values) bool {
	if k.token == "" {
		return false
	}

	reqURL, ok := k.buildSearchURL(q)
	if !ok {
		return false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := k.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}

	var root kodikSearchResponse
	if err := stdjson.NewDecoder(resp.Body).Decode(&root); err != nil {
		return false
	}
	return len(root.Results) > 0
}

func (k *KodikChecker) buildSearchURL(q url.Values) (string, bool) {
	u, err := url.Parse(k.apiHost + "/search")
	if err != nil {
		return "", false
	}
	qs := url.Values{}
	qs.Set("token", k.token)
	qs.Set("limit", "1")
	qs.Set("with_episodes", "true")

	if kp := strings.TrimSpace(q.Get("kinopoisk_id")); kp != "" {
		if _, err := strconv.ParseInt(kp, 10, 64); err == nil {
			qs.Set("kinopoisk_id", kp)
		}
	}
	if imdb := strings.TrimSpace(q.Get("imdb_id")); imdb != "" {
		qs.Set("imdb_id", imdb)
	}
	if title := strings.TrimSpace(q.Get("title")); title != "" {
		qs.Set("title", title)
	}

	if qs.Get("kinopoisk_id") == "" && qs.Get("imdb_id") == "" && qs.Get("title") == "" {
		return "", false
	}

	u.RawQuery = qs.Encode()
	return u.String(), true
}
