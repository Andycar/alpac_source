package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// femd balancer — videostorage.xyz / api.femd.ws ecosystem.
//
// Native lookup: GET /embed/kp/{kpID}   or  /embed/imdb/{ttID}
// HTTP 200 + valid <title> ≠ "Title" means the catalog entry exists.
// 404 (size ≈ 5KB, <title>Title</title>) means missing.
//
// The embed HTML carries a `makePlayer({ ... source: { hls: "...", cc: [...] } ... })`
// block from which we extract a tokenised HLS URL on cdnr.interkh.com and an
// array of VTT subtitle tracks. The CDN is open (CORS *) but URL params expire
// (`t=<unix>`) — so we always proxy through /proxy/ to give Lampa a stable URL.
//
// Catalog is movie-only. /embed/kp/{seriesKP} returns 404 even for very popular
// series (Lost, Squid Game). No serial handling implemented.

var (
	femdHLSRe        = regexp.MustCompile(`hls:\s*"([^"]+)"`)
	femdCCRe         = regexp.MustCompile(`cc:\s*(\[[\s\S]*?\])\s*\n`)
	femdTitleRe      = regexp.MustCompile(`(?m)^\s*title:\s*"([^"]+)"`)
	femdEmptyTitleRe = regexp.MustCompile(`<title>\s*Title\s*</title>`)
)

const (
	femdLookupTTL   = 6 * time.Hour
	femdNotFoundTTL = 30 * time.Minute
)

type femdChecker struct {
	client  *http.Client
	host    string
	referer string

	mu    sync.RWMutex
	cache map[int64]femdCacheEntry // kpID → cached lookup result
}

type femdCacheEntry struct {
	exists  bool
	expires time.Time
}

type femdCCItem struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

func NewFemdChecker(cfg config.Config) *femdChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.Femd.Host, "/"))
	if host == "" {
		host = "https://api.femd.ws"
	}
	referer := strings.TrimSpace(cfg.Online.Femd.Referer)
	if referer == "" {
		referer = host + "/"
	}

	// api.femd.ws WAF rejects datacenter IPs with HTTP 422. Operators on
	// blocked egress IPs should route via a residential SOCKS5 proxy.
	socksAddr := strings.TrimSpace(cfg.Online.Femd.Socks)
	var client *http.Client
	if socksAddr != "" {
		httpclient.RegisterProxiedBalancer(socksAddr, []string{"femd"})
		client = httpclient.NewForBalancer("femd", 15*time.Second)
		log.Info().Str("socks", socksAddr).Msg("femd: registered SOCKS5 proxy")
	} else {
		client = httpclient.New(15 * time.Second)
	}

	return &femdChecker{
		client:  client,
		host:    host,
		referer: referer,
		cache:   make(map[int64]femdCacheEntry, 256),
	}
}

func (f *femdChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/"), "/")

		if raw == "femd" {
			if parseBoolParam(req.URL.Query().Get("checksearch")) {
				show := f.checkSearch(req)
				writeCheckSearchResponse(w, show, pluginQualityBadgeGet("femd"))
				return
			}
			f.indexPage(w, req, links)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "femd route not implemented",
			"balanser": raw,
		})
	}
}

// ---------------------------------------------------------------------------
// checksearch
// ---------------------------------------------------------------------------

func (f *femdChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	kpID := femdParseKpID(q)
	if kpID == 0 {
		return false
	}

	// Cache hit
	f.mu.RLock()
	if entry, ok := f.cache[kpID]; ok && time.Now().Before(entry.expires) {
		f.mu.RUnlock()
		return entry.exists
	}
	f.mu.RUnlock()

	exists := f.kpExists(req, kpID)

	ttl := femdLookupTTL
	if !exists {
		ttl = femdNotFoundTTL
	}
	f.mu.Lock()
	f.cache[kpID] = femdCacheEntry{exists: exists, expires: time.Now().Add(ttl)}
	f.mu.Unlock()

	return exists
}

// kpExists issues a lightweight GET to /embed/kp/{kp} and inspects the title.
// femd.ws returns HTTP 200 with `<title>Title</title>` + 5KB stub for missing
// entries; real movies come back ≥ 15KB with a non-stub title and at least one
// `hls:` field.
func (f *femdChecker) kpExists(req *http.Request, kpID int64) bool {
	body, ok := f.fetchEmbedKP(req, kpID)
	if !ok {
		return false
	}
	if femdEmptyTitleRe.MatchString(body) {
		return false
	}
	return strings.Contains(body, "master.m3u8") || strings.Contains(body, ".mpd")
}

// ---------------------------------------------------------------------------
// indexPage — main entry, returns the movie record
// ---------------------------------------------------------------------------

func (f *femdChecker) indexPage(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	kpID := femdParseKpID(q)
	if kpID == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	html, ok := f.fetchEmbedKP(req, kpID)
	if !ok || femdEmptyTitleRe.MatchString(html) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	hlsURL := f.parseHLS(html)
	if hlsURL == "" {
		log.Debug().Int64("kp", kpID).Int("htmlLen", len(html)).Msg("femd: hls not found in embed")
		writeGetsTVEmpty(w, rjson)
		return
	}

	subs := f.parseSubtitles(html)
	stream := streamProxyURL(req, hlsURL, "femd", links)
	baseTitle := getsTVJoinName(title, originalTitle)
	if baseTitle == "" {
		baseTitle = f.parseTitle(html)
	}

	row := map[string]any{
		"method": "play",
		"url":    stream,
		"stream": stream,
		"title":  baseTitle,
		"name":   "По умолчанию",
	}
	if len(subs) > 0 {
		row["subtitles"] = subs
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": []map[string]any{row},
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	getsTVAppendMovieHTML(&sb, row, "По умолчанию", true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
// fetch + parse
// ---------------------------------------------------------------------------

func (f *femdChecker) fetchEmbedKP(req *http.Request, kpID int64) (string, bool) {
	target := fmt.Sprintf("%s/embed/kp/%d", f.host, kpID)
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	httpReq.Header.Set("Referer", f.referer)
	httpReq.Header.Set("Accept", "text/html,*/*;q=0.8")

	resp, err := balancerDoWithRetry(req.Context(), f.client, httpReq, 2)
	if err != nil {
		log.Debug().Err(err).Str("url", target).Msg("femd: embed fetch error")
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

func (f *femdChecker) parseHLS(html string) string {
	m := femdHLSRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func (f *femdChecker) parseTitle(html string) string {
	if m := femdTitleRe.FindStringSubmatch(html); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func (f *femdChecker) parseSubtitles(html string) []map[string]string {
	m := femdCCRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return nil
	}

	var items []femdCCItem
	if err := stdjson.Unmarshal([]byte(m[1]), &items); err != nil {
		log.Debug().Err(err).Msg("femd: cc parse error")
		return nil
	}

	subs := make([]map[string]string, 0, len(items))
	for _, it := range items {
		url := strings.TrimSpace(it.URL)
		name := strings.TrimSpace(it.Name)
		if url == "" {
			continue
		}
		if name == "" {
			name = "Subtitles"
		}
		subs = append(subs, map[string]string{"label": name, "url": url})
	}
	return subs
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func femdParseKpID(q url.Values) int64 {
	kpID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	if kpID == 0 {
		kpID, _ = strconv.ParseInt(strings.TrimSpace(q.Get("id")), 10, 64)
	}
	return kpID
}
