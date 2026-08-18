package litesrc

import (
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

var animevostItemRe = regexp.MustCompile(`(?is)class="shortstory".*?<a href="https?://[^"]+\.html">([^<]+)</a>.*?(?:<strong>Год выхода: ?</strong>\s*([0-9]{4})</p>)?`)

type animevostChecker struct {
	client *http.Client
	host   string
}

func NewAnimevostChecker(cfg config.Config) *animevostChecker {
	host := strings.TrimSpace(cfg.Online.Animevost.Host)
	if host == "" {
		host = "https://animevost.org"
	}
	host = strings.TrimRight(host, "/")

	return &animevostChecker{
		client: httpclient.NewForBalancer("animevost", 10*time.Second),
		host:   host,
	}
}

func (a *animevostChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := a.checkSearch(req)
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("animevost"))
			return
		}

		a.indexLocal(w, req, links)
	}
}

func (a *animevostChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		return false
	}
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))

	search, ok := a.fetchSearch(req, title)
	if !ok {
		return false
	}

	want := normalizeSearchTitle(title)
	for _, m := range animevostItemRe.FindAllStringSubmatch(search, -1) {
		if len(m) < 2 {
			continue
		}
		rowTitle := normalizeSearchTitle(m[1])
		if rowTitle == "" || !strings.Contains(rowTitle, want) {
			continue
		}
		if year > 0 && len(m) >= 3 && strings.TrimSpace(m[2]) != "" {
			y, _ := strconv.Atoi(strings.TrimSpace(m[2]))
			if y > 0 && y != year && y != year-1 && y != year+1 {
				continue
			}
		}
		return true
	}

	return false
}

func (a *animevostChecker) fetchSearch(req *http.Request, title string) (string, bool) {
	u, err := url.Parse(a.host + "/index.php")
	if err != nil {
		return "", false
	}
	qs := url.Values{}
	qs.Set("do", "search")
	u.RawQuery = qs.Encode()

	form := url.Values{}
	form.Set("do", "search")
	form.Set("subaction", "search")
	form.Set("search_start", "0")
	form.Set("full_search", "0")
	form.Set("result_from", "1")
	form.Set("story", title)

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}
