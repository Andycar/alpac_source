package litesrc

import (
	"context"
	stdjson "encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"lampac-go/internal/config"
	"lampac-go/internal/xsearch"
)

// ---------------------------------------------------------------------------
//  Collaps adapter — uses searchList endpoint (returns full result data)
// ---------------------------------------------------------------------------

type xsCollapsAdapter struct {
	checker *CollapsChecker
}

func newXSCollapsAdapter(c *CollapsChecker) *xsCollapsAdapter {
	return &xsCollapsAdapter{checker: c}
}

func (a *xsCollapsAdapter) Name() string { return "collaps" }

func (a *xsCollapsAdapter) SearchText(ctx context.Context, query string) ([]xsearch.SearchResult, error) {
	rows, ok := a.checker.SearchList(ctx, query)
	if !ok || len(rows) == 0 {
		return nil, nil
	}

	out := make([]xsearch.SearchResult, 0, len(rows))
	for _, r := range rows {
		sr := xsearch.SearchResult{
			Title:         r.Name,
			OriginalTitle: r.OriginName,
			Year:          r.Year,
			Poster:        r.Poster,
			Balancer:      "collaps",
			Quality:       "FHD",
		}
		if r.KinopoiskID != "" {
			sr.KinopoiskID, _ = strconv.ParseInt(r.KinopoiskID, 10, 64)
		}
		if r.IMDBID != "" {
			sr.ImdbID = r.IMDBID
		}
		// Detect type from iframe_url.
		if strings.Contains(r.IframeURL, "/serial/") {
			sr.ContentType = "serial"
		} else {
			sr.ContentType = "movie"
		}
		if sr.Title == "" {
			continue
		}
		out = append(out, sr)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
//  VideoCDN adapter — uses /api/short with title search
// ---------------------------------------------------------------------------

type xsVideoCDNAdapter struct {
	client *http.Client
	host   string
	token  string
}

func newXSVideoCDNAdapter(c *VideoCDNChecker) *xsVideoCDNAdapter {
	return &xsVideoCDNAdapter{
		client: c.Client(),
		host:   c.IframeHost(),
		token:  c.Token(),
	}
}

func (a *xsVideoCDNAdapter) Name() string { return "videocdn" }

func (a *xsVideoCDNAdapter) SearchText(ctx context.Context, query string) ([]xsearch.SearchResult, error) {
	if a.token == "" {
		return nil, nil
	}

	base := NormalizeVideoCDNBase(a.host)
	if base == "" {
		return nil, nil
	}

	u, err := url.Parse(base + "/api/short")
	if err != nil {
		return nil, nil
	}
	qs := url.Values{}
	qs.Set("api_token", a.token)
	qs.Set("title", query)
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil
	}

	var root struct {
		Data []stdjson.RawMessage `json:"data"`
	}
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&root); err != nil {
		return nil, nil
	}

	out := make([]xsearch.SearchResult, 0, len(root.Data))
	for _, raw := range root.Data {
		var item struct {
			Title         string `json:"title"`
			OriginalTitle string `json:"orig_title"`
			Year          int    `json:"start_date"` // sometimes used as year
			KinopoiskID   string `json:"kp_id"`
			ImdbID        string `json:"imdb_id"`
			ContentType   string `json:"type"` // "movie", "serial", etc.
		}
		if err := stdjson.Unmarshal(raw, &item); err != nil {
			continue
		}

		sr := xsearch.SearchResult{
			Title:         item.Title,
			OriginalTitle: item.OriginalTitle,
			Balancer:      "videocdn",
			Quality:       "FHD",
		}
		if item.KinopoiskID != "" {
			sr.KinopoiskID, _ = strconv.ParseInt(item.KinopoiskID, 10, 64)
		}
		if item.ImdbID != "" {
			sr.ImdbID = item.ImdbID
		}
		if item.ContentType == "serial" || item.ContentType == "anime" {
			sr.ContentType = "serial"
		} else {
			sr.ContentType = "movie"
		}
		if sr.Title == "" {
			continue
		}
		out = append(out, sr)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
//  Kodik adapter — uses /search endpoint
// ---------------------------------------------------------------------------

type xsKodikAdapter struct {
	client  *http.Client
	apiHost string
	token   string
}

func newXSKodikAdapter(c *KodikChecker) *xsKodikAdapter {
	return &xsKodikAdapter{
		client:  c.Client(),
		apiHost: c.APIHost(),
		token:   c.Token(),
	}
}

func (a *xsKodikAdapter) Name() string { return "kodik" }

func (a *xsKodikAdapter) SearchText(ctx context.Context, query string) ([]xsearch.SearchResult, error) {
	if a.token == "" {
		return nil, nil
	}

	u, err := url.Parse(a.apiHost + "/search")
	if err != nil {
		return nil, nil
	}
	qs := url.Values{}
	qs.Set("token", a.token)
	qs.Set("title", query)
	qs.Set("limit", "20")
	qs.Set("with_episodes", "false")
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil
	}

	var root struct {
		Results []stdjson.RawMessage `json:"results"`
	}
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&root); err != nil {
		return nil, nil
	}

	seen := make(map[string]bool)
	out := make([]xsearch.SearchResult, 0, len(root.Results))
	for _, raw := range root.Results {
		var item struct {
			Title         string `json:"title"`
			OriginalTitle string `json:"title_orig"`
			Year          int    `json:"year"`
			KinopoiskID   string `json:"kinopoisk_id"`
			ImdbID        string `json:"imdb_id"`
			Type          string `json:"type"` // "foreign-movie", "soviet-cartoon", "anime-serial", etc.
		}
		if err := stdjson.Unmarshal(raw, &item); err != nil {
			continue
		}

		// Deduplicate by title+year within kodik results.
		dedup := strings.ToLower(item.Title) + "|" + strconv.Itoa(item.Year)
		if seen[dedup] {
			continue
		}
		seen[dedup] = true

		sr := xsearch.SearchResult{
			Title:         item.Title,
			OriginalTitle: item.OriginalTitle,
			Year:          item.Year,
			Balancer:      "kodik",
			Quality:       "FHD",
		}
		if item.KinopoiskID != "" {
			sr.KinopoiskID, _ = strconv.ParseInt(item.KinopoiskID, 10, 64)
		}
		if item.ImdbID != "" {
			sr.ImdbID = item.ImdbID
		}
		if strings.Contains(item.Type, "serial") || strings.Contains(item.Type, "anime") {
			sr.ContentType = "serial"
		} else {
			sr.ContentType = "movie"
		}
		if sr.Title == "" {
			continue
		}
		out = append(out, sr)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
//  Mirage adapter — uses search API
// ---------------------------------------------------------------------------

type xsMirageAdapter struct {
	checker *MirageChecker
}

func newXSMirageAdapter(c *MirageChecker) *xsMirageAdapter {
	return &xsMirageAdapter{checker: c}
}

func (a *xsMirageAdapter) Name() string { return "mirage" }

func (a *xsMirageAdapter) SearchText(ctx context.Context, query string) ([]xsearch.SearchResult, error) {
	if a.checker.Token() == "" {
		return nil, nil
	}

	// Mirage searchByTitle requires *http.Request for context propagation.
	// Create a minimal request with our context.
	fakeReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/", nil)

	// Search both movies and serials.
	var all []xsearch.SearchResult
	for _, serial := range []bool{false, true} {
		items, ok := a.checker.SearchByTitle(fakeReq, query, serial)
		if !ok {
			continue
		}
		for _, item := range items {
			sr := xsearch.SearchResult{
				Title:         item.Name,
				OriginalTitle: item.Original,
				Year:          item.Year,
				Poster:        item.Poster,
				Balancer:      "mirage",
				Quality:       "4K",
			}
			if serial {
				sr.ContentType = "serial"
			} else {
				sr.ContentType = "movie"
			}
			if sr.Title == "" {
				continue
			}
			all = append(all, sr)
		}
	}
	return all, nil
}

// ---------------------------------------------------------------------------
//  Kinobase adapter — uses search endpoint (HTML scraping)
// ---------------------------------------------------------------------------

type xsKinobaseAdapter struct {
	checker *KinobaseChecker
}

func newXSKinobaseAdapter(c *KinobaseChecker) *xsKinobaseAdapter {
	return &xsKinobaseAdapter{checker: c}
}

func (a *xsKinobaseAdapter) Name() string { return "kinobase" }

func (a *xsKinobaseAdapter) SearchText(ctx context.Context, query string) ([]xsearch.SearchResult, error) {
	items, _, ok := a.checker.Search(ctx, query, "", 0)
	if !ok || len(items) == 0 {
		return nil, nil
	}

	out := make([]xsearch.SearchResult, 0, len(items))
	for _, item := range items {
		yr, _ := strconv.Atoi(strings.TrimSpace(item.Year))
		sr := xsearch.SearchResult{
			Title:    item.Title,
			Year:     yr,
			Poster:   item.Img,
			Balancer: "kinobase",
			Quality:  "FHD",
		}
		if sr.Title == "" {
			continue
		}
		out = append(out, sr)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
//  Loopback checksearch adapter — generic adapter for boolean-only balancers
//  Used to verify if a specific title (with known IDs) is available on a balancer
// ---------------------------------------------------------------------------

// XSLoopbackAdapter probes a local /lite/{balancer}?checksearch=true endpoint.
// It doesn't do text search — it checks availability by known IDs.
// Used in phase 2 to enrich merged results with additional sources.
type XSLoopbackAdapter struct {
	client   *http.Client
	balancer string
	quality  string
	addr     string // server listen addr for loopback
}

// NewXSLoopbackAdapter creates a loopback checksearch adapter.
func NewXSLoopbackAdapter(client *http.Client, balancer, quality, addr string) *XSLoopbackAdapter {
	return &XSLoopbackAdapter{
		client:   client,
		balancer: balancer,
		quality:  quality,
		addr:     addr,
	}
}

func (a *XSLoopbackAdapter) Name() string { return a.balancer }

// CheckAvailable probes the balancer via loopback for a specific title.
func (a *XSLoopbackAdapter) CheckAvailable(ctx context.Context, kpID int64, imdbID string, title string) bool {
	host := loopbackHostPort(a.addr)
	if host == "" {
		return false
	}

	u := "http://" + host + "/lite/" + a.balancer
	qs := url.Values{}
	qs.Set("checksearch", "true")
	if kpID > 0 {
		qs.Set("kinopoisk_id", strconv.FormatInt(kpID, 10))
		qs.Set("id", strconv.FormatInt(kpID, 10))
	}
	if imdbID != "" {
		qs.Set("imdb_id", imdbID)
	}
	if title != "" {
		qs.Set("title", title)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+qs.Encode(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := a.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return false
	}

	s := string(body)
	return strings.Contains(s, `"rch":true`) ||
		strings.Contains(s, `data-json=`) ||
		strings.Contains(s, `"type":"movie"`)
}

// ---------------------------------------------------------------------------
//  Factory: build all xsearch adapters from existing checkers
// ---------------------------------------------------------------------------

// XSearchAdapters holds all adapters created from existing balancer checkers.
type XSearchAdapters struct {
	TextSearchers  []xsearch.TextSearcher
	LoopbackChecks []*XSLoopbackAdapter
}

// BuildXSearchAdapters creates fresh checker instances and wraps them as TextSearchers.
// Called from server.go during init.
func BuildXSearchAdapters(cfg config.Config) []xsearch.TextSearcher {
	var searchers []xsearch.TextSearcher

	collapsC := NewCollapsChecker(cfg)
	if collapsC.Token() != "" {
		searchers = append(searchers, newXSCollapsAdapter(collapsC))
	}

	videoCDNC := NewVideoCDNChecker(cfg)
	if videoCDNC.Token() != "" {
		searchers = append(searchers, newXSVideoCDNAdapter(videoCDNC))
	}

	kodikC := NewKodikChecker(cfg)
	if kodikC.Token() != "" {
		searchers = append(searchers, newXSKodikAdapter(kodikC))
	}

	mirageC := NewMirageChecker(cfg)
	if mirageC.Token() != "" {
		searchers = append(searchers, newXSMirageAdapter(mirageC))
	}

	kinobaseC := NewKinobaseChecker(cfg)
	searchers = append(searchers, newXSKinobaseAdapter(kinobaseC))

	return searchers
}
