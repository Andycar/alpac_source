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
)

type plvideoChecker struct {
	client *http.Client
	host   string
}

type plvideoResponse struct {
	Items []plvideoItem `json:"items"`
}

type plvideoItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Visible string `json:"visible"`
	Upload  struct {
		VideoDuration int64 `json:"videoDuration"`
	} `json:"uploadFile"`
}

func NewPlvideoChecker(cfg config.Config) *plvideoChecker {
	host := strings.TrimSpace(cfg.Online.Plvideo.Host)
	if host == "" {
		host = "https://api.g1.plvideo.ru"
	}
	host = strings.TrimRight(host, "/")

	return &plvideoChecker{
		client: httpclient.NewForBalancer("plvideo", 10*time.Second),
		host:   host,
	}
}

func (p *plvideoChecker) Handle(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := p.checkSearch(req)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if show {
				_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "plvideo full mode is not yet implemented",
			"balanser": "plvideo",
		})
	}
}

func (p *plvideoChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	if strings.TrimSpace(q.Get("serial")) == "1" {
		return false
	}

	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		return false
	}

	year, err := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	if err != nil || year == 0 {
		return false
	}

	u, err := url.Parse(p.host + "/v1/videos")
	if err != nil {
		return false
	}
	qs := url.Values{}
	qs.Set("Type", "video")
	qs.Set("Query", title+" "+strconv.Itoa(year))
	qs.Set("From", "0")
	qs.Set("Size", "20")
	qs.Set("Aud", "16")
	qs.Set("Qf", "false")
	u.RawQuery = qs.Encode()

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := p.client.Do(httpReq)
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

	var root plvideoResponse
	if err := stdjson.Unmarshal(body, &root); err != nil {
		return false
	}
	if len(root.Items) == 0 {
		return false
	}

	searchTitle := normalizeSearchTitle(title)
	for _, movie := range root.Items {
		name := normalizeSearchTitle(movie.Title)
		if name == "" || !strings.HasPrefix(name, searchTitle) {
			continue
		}
		if !(strings.Contains(name, strconv.Itoa(year)) ||
			strings.Contains(name, strconv.Itoa(year+1)) ||
			strings.Contains(name, strconv.Itoa(year-1))) {
			continue
		}
		if movie.Upload.VideoDuration <= 1900000 { // ~30 min, same as legacy
			continue
		}
		if nameContainsAny(name, "трейлер", "премьера", "сезон", "сериал", "серия", "серий") {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(movie.Visible), "public") {
			continue
		}
		return true
	}

	return false
}
