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

// kinobadi balancer — kinobadi.im / vip.kinobadi.im aggregator.
//
// Entry point: GET https://vip.kinobadi.im/hd_pars/pleer_on.php?kp={kpID}
//   - kinobadi resolves KP → id_file internally; serial flows return a ~2KB
//     stub (no episode tree), so we treat it as movie-only.
//   - The returned HTML carries up to two players:
//       Плеер 1     → api.femd.ws/embed/movie/{id_file}?host=kinotik.top
//                     (femd CDN bound to kinotik consumer — often wider
//                     catalog than /embed/kp/ used by the femd balancer)
//       Плеер 2 (4K) → wonder-as.stloadi.live/?token_movie=...&hd_4K
//                     (Mirage stloadi.live; needs browser-side header capture
//                     for a real m3u8 — skipped in v1)
//
// v1 resolves Плеер 1 only: fetches the femd embed via the same SOCKS5
// configured for this balancer and parses HLS+subtitles out of the inline
// makePlayer({ source: { hls, cc } }) block — identical layout to femd.go.
//
// Badge: FHD (matches Плеер 1). When the Mirage 4K path is wired the badge
// can be promoted.

var (
	// Host-agnostic /embed/movie/{id_file} — works against api.femd.ws in prod
	// and against any test httptest server in unit tests.
	kinobadiFemdEmbedRe  = regexp.MustCompile(`/embed/movie/(\d+)`)
	kinobadiStloadiRe    = regexp.MustCompile(`(https?://[\w.-]*stloadi\.live/\?[^"' ]+)`)
	kinobadiHLSRe        = regexp.MustCompile(`hls:\s*"([^"]+)"`)
	kinobadiCCRe         = regexp.MustCompile(`cc:\s*(\[[\s\S]*?\])\s*\n`)
	kinobadiEmptyTitleRe = regexp.MustCompile(`<title>\s*Title\s*</title>`)
)

const (
	kinobadiLookupTTL   = 6 * time.Hour
	kinobadiNotFoundTTL = 30 * time.Minute
)

type kinobadiChecker struct {
	client      *http.Client
	pleerHost   string // https://vip.kinobadi.im
	femdHost    string // https://api.femd.ws
	consumer    string // kinotik.top
	siteReferer string // https://mm.kinobadi.im/

	mu    sync.RWMutex
	cache map[int64]kinobadiCacheEntry
}

type kinobadiCacheEntry struct {
	exists  bool
	idFile  string
	expires time.Time
}

type kinobadiCCItem struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

func NewKinobadiChecker(cfg config.Config) *kinobadiChecker {
	pleerHost := strings.TrimSpace(strings.TrimRight(cfg.Online.Kinobadi.PleerHost, "/"))
	if pleerHost == "" {
		pleerHost = "https://vip.kinobadi.im"
	}
	femdHost := strings.TrimSpace(strings.TrimRight(cfg.Online.Kinobadi.FemdHost, "/"))
	if femdHost == "" {
		femdHost = "https://api.femd.ws"
	}
	consumer := strings.TrimSpace(cfg.Online.Kinobadi.Consumer)
	if consumer == "" {
		consumer = "kinotik.top"
	}
	siteReferer := strings.TrimSpace(cfg.Online.Kinobadi.Referer)
	if siteReferer == "" {
		siteReferer = "https://mm.kinobadi.im/"
	}

	// Both pleer_on.php and api.femd.ws may need a residential SOCKS5 — the
	// kinobadi/femd network blocks many datacenter IPs (HTTP 422). Operators
	// on blocked egress should point socks at the same VLESS/Xray exit that
	// works for femd.
	socksAddr := strings.TrimSpace(cfg.Online.Kinobadi.Socks)
	var client *http.Client
	if socksAddr != "" {
		httpclient.RegisterProxiedBalancer(socksAddr, []string{"kinobadi"})
		client = httpclient.NewForBalancer("kinobadi", 15*time.Second)
		log.Info().Str("socks", socksAddr).Msg("kinobadi: registered SOCKS5 proxy")
	} else {
		client = httpclient.New(15 * time.Second)
	}

	return &kinobadiChecker{
		client:      client,
		pleerHost:   pleerHost,
		femdHost:    femdHost,
		consumer:    consumer,
		siteReferer: siteReferer,
		cache:       make(map[int64]kinobadiCacheEntry, 256),
	}
}

func (k *kinobadiChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/"), "/")

		if raw == "kinobadi" {
			if parseBoolParam(req.URL.Query().Get("checksearch")) {
				show := k.checkSearch(req)
				writeCheckSearchResponse(w, show, pluginQualityBadgeGet("kinobadi"))
				return
			}
			k.indexPage(w, req, links)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "kinobadi route not implemented",
			"balanser": raw,
		})
	}
}

// ---------------------------------------------------------------------------
// checksearch — pleer_on.php?kp=X returns ~4.5KB with "Плеер 1" iframe when
// kinobadi has the film, ~2KB stub otherwise.
// ---------------------------------------------------------------------------

func (k *kinobadiChecker) checkSearch(req *http.Request) bool {
	kpID := kinobadiParseKpID(req.URL.Query())
	if kpID == 0 {
		return false
	}

	k.mu.RLock()
	if entry, ok := k.cache[kpID]; ok && time.Now().Before(entry.expires) {
		k.mu.RUnlock()
		return entry.exists
	}
	k.mu.RUnlock()

	idFile, exists := k.lookupIDFile(req, kpID)

	ttl := kinobadiLookupTTL
	if !exists {
		ttl = kinobadiNotFoundTTL
	}
	k.mu.Lock()
	k.cache[kpID] = kinobadiCacheEntry{exists: exists, idFile: idFile, expires: time.Now().Add(ttl)}
	k.mu.Unlock()

	return exists
}

// lookupIDFile returns the kinobadi id_file mapped to a kpID (and whether it
// exists). The id_file comes from the api.femd.ws/embed/movie/{id_file}
// iframe URL parsed out of pleer_on.php.
func (k *kinobadiChecker) lookupIDFile(req *http.Request, kpID int64) (string, bool) {
	body, ok := k.fetchPleerOn(req, kpID)
	if !ok {
		return "", false
	}
	// Real listings carry a femd /embed/movie/{id_file} iframe; stub responses
	// for unknown KPs do not. Regex absence is the authoritative signal.
	m := kinobadiFemdEmbedRe.FindStringSubmatch(body)
	if len(m) < 2 {
		return "", false
	}
	return m[1], true
}

// ---------------------------------------------------------------------------
// indexPage — resolve kp → Плеер 1 HLS, return single movie row
// ---------------------------------------------------------------------------

func (k *kinobadiChecker) indexPage(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	kpID := kinobadiParseKpID(q)
	if kpID == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	idFile, exists := k.lookupIDFile(req, kpID)
	if !exists {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Fetch femd embed bound to kinotik consumer — this is what kinobadi's
	// own iframe loads. Often serves films that /embed/kp/{kp} does not.
	embedURL := fmt.Sprintf("%s/embed/movie/%s?host=%s&sharing=false",
		k.femdHost, idFile, url.QueryEscape(k.consumer))
	html, ok := k.fetchEmbed(req, embedURL)
	if !ok || kinobadiEmptyTitleRe.MatchString(html) {
		log.Debug().Int64("kp", kpID).Str("id_file", idFile).
			Bool("ok", ok).Msg("kinobadi: femd embed unavailable")
		writeGetsTVEmpty(w, rjson)
		return
	}

	hlsURL := kinobadiParseHLS(html)
	if hlsURL == "" {
		log.Debug().Int64("kp", kpID).Int("htmlLen", len(html)).
			Msg("kinobadi: hls not found in embed")
		writeGetsTVEmpty(w, rjson)
		return
	}

	subs := kinobadiParseSubtitles(html)
	stream := streamProxyURL(req, hlsURL, "kinobadi", links)
	baseTitle := getsTVJoinName(title, originalTitle)

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
// HTTP fetch
// ---------------------------------------------------------------------------

func (k *kinobadiChecker) fetchPleerOn(req *http.Request, kpID int64) (string, bool) {
	target := fmt.Sprintf("%s/hd_pars/pleer_on.php?kp=%d", k.pleerHost, kpID)
	return k.doGET(req, target, k.siteReferer)
}

func (k *kinobadiChecker) fetchEmbed(req *http.Request, target string) (string, bool) {
	return k.doGET(req, target, k.pleerHost+"/")
}

func (k *kinobadiChecker) doGET(req *http.Request, target, referer string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	httpReq.Header.Set("Referer", referer)
	httpReq.Header.Set("Accept", "text/html,*/*;q=0.8")

	resp, err := balancerDoWithRetry(req.Context(), k.client, httpReq, 2)
	if err != nil {
		log.Debug().Err(err).Str("url", target).Msg("kinobadi: fetch error")
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("url", target).Msg("kinobadi: non-2xx")
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// ---------------------------------------------------------------------------
// Parsers — same makePlayer({ source: { hls, cc } }) layout as femd.go.
// ---------------------------------------------------------------------------

func kinobadiParseHLS(html string) string {
	m := kinobadiHLSRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func kinobadiParseSubtitles(html string) []map[string]string {
	m := kinobadiCCRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return nil
	}
	var items []kinobadiCCItem
	if err := stdjson.Unmarshal([]byte(m[1]), &items); err != nil {
		return nil
	}
	subs := make([]map[string]string, 0, len(items))
	for _, it := range items {
		u := strings.TrimSpace(it.URL)
		name := strings.TrimSpace(it.Name)
		if u == "" {
			continue
		}
		if name == "" {
			name = "Subtitles"
		}
		subs = append(subs, map[string]string{"label": name, "url": u})
	}
	return subs
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func kinobadiParseKpID(q url.Values) int64 {
	kpID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	if kpID == 0 {
		kpID, _ = strconv.ParseInt(strings.TrimSpace(q.Get("id")), 10, 64)
	}
	return kpID
}
