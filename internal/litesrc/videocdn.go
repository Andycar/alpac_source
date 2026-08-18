package litesrc

import (
	"context"
	stdjson "encoding/json"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
)

type VideoCDNChecker struct {
	client     *http.Client
	iframeHost string
	token      string
}

// Client/IframeHost/Token expose the fields the xsearch adapter snapshots.
func (c *VideoCDNChecker) Client() *http.Client { return c.client }
func (c *VideoCDNChecker) IframeHost() string   { return c.iframeHost }
func (c *VideoCDNChecker) Token() string        { return c.token }

func NewVideoCDNChecker(cfg config.Config) *VideoCDNChecker {
	iframeHost := strings.TrimSpace(cfg.Online.VideoCDN.IframeHost)
	if iframeHost == "" {
		iframeHost = "https://portal.lumex.host"
	}

	return &VideoCDNChecker{
		client:     httpclient.NewForBalancer("videocdn", 10*time.Second),
		iframeHost: iframeHost,
		token:      strings.TrimSpace(cfg.Online.VideoCDN.Token),
	}
}

func (v *VideoCDNChecker) Handle(cfg config.Config, plugin string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := v.checkSearch(req.Context(), req.URL.Query())
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet(plugin))
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "videocdn full mode is not yet implemented",
			"balanser": plugin,
		})
	}
}

type videoCDNShortResponse struct {
	Data []stdjson.RawMessage `json:"data"`
}

func (v *VideoCDNChecker) checkSearch(ctx context.Context, q url.Values) bool {
	if v.token == "" {
		return false
	}

	base := NormalizeVideoCDNBase(v.iframeHost)
	if base == "" {
		return false
	}

	u, err := url.Parse(base + "/api/short")
	if err != nil {
		return false
	}

	qs := url.Values{}
	qs.Set("api_token", v.token)
	if kp := strings.TrimSpace(q.Get("kinopoisk_id")); kp != "" {
		if _, err := strconv.ParseInt(kp, 10, 64); err == nil {
			qs.Set("kinopoisk_id", kp)
		}
	}
	if imdb := strings.TrimSpace(q.Get("imdb_id")); imdb != "" {
		qs.Set("imdb_id", imdb)
	}
	if qs.Get("kinopoisk_id") == "" && qs.Get("imdb_id") == "" {
		title := strings.TrimSpace(q.Get("title"))
		if title == "" {
			title = strings.TrimSpace(q.Get("original_title"))
		}
		if title != "" {
			qs.Set("title", title)
		}
	}
	if qs.Get("kinopoisk_id") == "" && qs.Get("imdb_id") == "" && qs.Get("title") == "" {
		return false
	}

	u.RawQuery = qs.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}

	var root videoCDNShortResponse
	if err := stdjson.NewDecoder(resp.Body).Decode(&root); err != nil {
		return false
	}
	return len(root.Data) > 0
}

func NormalizeVideoCDNBase(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimRight(host, "/")
	}
	return "https://" + strings.TrimRight(host, "/")
}
