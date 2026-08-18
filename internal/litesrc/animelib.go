package litesrc

import (
	stdjson "encoding/json"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

type animelibChecker struct {
	client *http.Client
	host   string
	token  string
}

type animelibSearchResponse struct {
	Data []animelibSearchItem `json:"data"`
}

type animelibSearchItem struct {
	RUName      string `json:"rus_name"`
	ENName      string `json:"eng_name"`
	SlugURL     string `json:"slug_url"`
	ReleaseDate string `json:"releaseDate"`
}

func NewAnimelibChecker(cfg config.Config) *animelibChecker {
	host := strings.TrimSpace(cfg.Online.AnimeLib.Host)
	if host == "" {
		host = "https://api.cdnlibs.org"
	}
	host = strings.TrimRight(host, "/")

	return &animelibChecker{
		client: httpclient.NewForBalancer("animelib", 10*time.Second),
		host:   host,
		token:  strings.TrimSpace(cfg.Online.AnimeLib.Token),
	}
}

func (a *animelibChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := a.checkSearch(req)
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("animelib"))
			return
		}

		a.indexLocal(w, req, links)
	}
}

func (a *animelibChecker) checkSearch(req *http.Request) bool {
	if a.token == "" {
		return false
	}

	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	if title == "" && originalTitle == "" {
		return false
	}

	searches := make([]string, 0, 2)
	if originalTitle != "" {
		searches = append(searches, originalTitle)
	}
	if title != "" && normalizeSearchTitle(title) != normalizeSearchTitle(originalTitle) {
		searches = append(searches, title)
	}

	want := normalizeSearchTitle(title)
	if want == "" {
		want = normalizeSearchTitle(originalTitle)
	}

	for _, query := range searches {
		items, ok := a.search(req, query)
		if !ok || len(items) == 0 {
			continue
		}

		for _, anime := range items {
			if anime.SlugURL == "" {
				continue
			}
			ru := normalizeSearchTitle(anime.RUName)
			en := normalizeSearchTitle(anime.ENName)
			if want != "" && !(strings.Contains(ru, want) || strings.Contains(en, want)) {
				continue
			}
			if year > 0 && anime.ReleaseDate != "" && !strings.HasPrefix(anime.ReleaseDate, strconv.Itoa(year)) {
				continue
			}
			return true
		}
	}

	return false
}

func (a *animelibChecker) search(req *http.Request, query string) ([]animelibSearchItem, bool) {
	u, err := url.Parse(a.host + "/api/anime")
	if err != nil {
		return nil, false
	}
	qs := url.Values{}
	qs.Add("fields[]", "rate_avg")
	qs.Add("fields[]", "rate")
	qs.Add("fields[]", "releaseDate")
	qs.Set("q", query)
	u.RawQuery = qs.Encode()

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, false
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.token)
	httpReq.Header.Set("Accept", "application/json,text/plain,*/*")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	var root animelibSearchResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&root); err != nil {
		return nil, false
	}
	return root.Data, true
}
