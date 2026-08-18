package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// Lift balancer.
//
// Architecture:
//   - Lift = Russian piracy aggregator. Native Android/TV WebView app +
//     SPA front-ends at multiple rotating *.ws mirrors (lateremb, embandr,
//     domem, atomics …). The official APK (`io.lift.app`) resolves a
//     working frontend domain via:
//       1. probe `primary_hosts` in order, accept first that returns 200
//          on `/` with text/html
//       2. fallback: GET sourcecraft list `borov.sourcecraft.site/domains/domains.txt`
//       3. fallback: GitHub Gist `gists/<gist_id>` file `domains.txt`
//     All Lift API/WebView calls require HTTP Basic auth (default lift1:lift1).
//
//   - Streams come from `api.zenithjs.ws/embed/{movie|kp|imdb}/<id>?host=<consumerHost>`.
//     The embed HTML inlines `hls:"…"` / `dash:"…"` URLs and a `seasons:[…]`
//     array for serials — exactly the shape that the Collaps balancer
//     already parses. We reuse Collaps's regexes (collapsHLSRe, collapsDashRe,
//     collapsParseSeasons, …) for parsing.
//
//   - Underlying CDN is `cdnr.interkh.com` (UA-bound — must use the same UA
//     to fetch the embed and to proxy the stream).
//
//   - Lift's own catalog API (`api.<frontend>/{search,list,info,genre,country}`)
//     is used for global search / xsearch only. For playback, we call zenithjs
//     directly with `kinopoisk_id` (verified — `info.id` in Lift API == KP id).

const (
	liftEmbedHostDefault      = "https://api.zenithjs.ws"
	liftConsumerHostDefault   = "lift.com"
	liftBasicAuthUserDefault  = "lift1"
	liftBasicAuthPassDefault  = "lift1"
	liftSourcecraftURLDefault = "https://borov.sourcecraft.site/domains/domains.txt"
	liftGistIDDefault         = "39683e108576be2ef26947055217a29c"
	liftEmbedRefererDefault   = "https://lift3.ws"
	liftAPICacheTTL           = 30 * time.Minute
)

// liftEmbedUA is sent on every embed/CDN request. The interkh.com CDN binds
// stream URLs to the UA that fetched the embed — so it MUST stay in sync
// across embed fetch and stream proxy. Using LiftApp's official UA matches
// what `host=lift.com` consumer would otherwise send.
const liftEmbedUA = "Mozilla/5.0 (Linux; Android 14; SM-S921B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Mobile Safari/537.36"

var liftDefaultPrimaryHosts = []string{"https://lateremb.ws/", "https://embandr.ws/"}

var (
	// Strip the iframe-busting block. Two redundant variants of the check
	// in the embed; we replace each with a no-op so the player initializes
	// inside our proxied iframe without redirecting away.
	liftIframeBustOuterRe = regexp.MustCompile(`(?s)if\s*\(\s*isEmbedded\s*&&\s*!sameOrigin\s*&&\s*window\.fetch.*?\}\s*\)\s*\}`)
	liftIframeBustInnerRe = regexp.MustCompile(`(?s)if\s*\(\s*isEmbedded\s*&&\s*!sameOrigin\s*\)\s*\{[^}]*addEventListener[^}]*\}[^}]*\}`)

	// "info":{"id":"<kp>"…} or "id":<kp> — used to verify a Lift item maps
	// to the requested KP id when iterating /search results.
	liftInfoKPIDRe = regexp.MustCompile(`(?is)"id"\s*:\s*"?(\d{2,9})"?`)
)

type liftChecker struct {
	client *http.Client

	// Resolved zenithjs embed host (rarely rotates).
	embedHost string

	// Brand identifier passed as ?host= to zenithjs. Must match a registered
	// consumer (default lift.com → consumerId 50393).
	consumerHost string

	// Referer to spoof on the proxied iframe (so consumer-host validation passes).
	embedReferer string

	// Lift's own API host candidates and rotation backends. Used for
	// search/list/info — NOT for playback.
	primaryHosts   []string
	sourcecraftURL string
	gistID         string
	basicAuthUser  string
	basicAuthPass  string

	mu          sync.RWMutex
	apiHost     string // resolved, e.g. https://api.embandr.ws
	apiExpiry   time.Time
	apiCacheTTL time.Duration
}

// liftItem matches Lift API /search/list response items. `info.id` carries
// the kinopoisk_id (string).
type liftItem struct {
	ID         int64   `json:"id"`
	Type       int     `json:"type"`
	Name       string  `json:"name"`
	OriginName string  `json:"origin_name"`
	Poster     string  `json:"poster"`
	Year       int     `json:"year"`
	Views      int     `json:"views"`
	IMDBRating float64 `json:"imdb_rating"`
	KPRating   float64 `json:"kp_rating"`
	IframeURI  string  `json:"iframe_uri"`
}

type liftSearchResponse struct {
	TotalCount int        `json:"totalCount"`
	Items      []liftItem `json:"items"`
}

type liftItemFull struct {
	liftItem
	Episodes map[string][]string `json:"episodes,omitempty"`
	Info     stdjson.RawMessage  `json:"info,omitempty"`
}

func NewLiftChecker(cfg config.Config) *liftChecker {
	src := cfg.Online.Lift

	embedHost := strings.TrimRight(strings.TrimSpace(src.EmbedHost), "/")
	if embedHost == "" {
		embedHost = liftEmbedHostDefault
	}
	if !strings.Contains(embedHost, "://") {
		embedHost = "https://" + embedHost
	}

	consumerHost := strings.TrimSpace(src.ConsumerHost)
	if consumerHost == "" {
		consumerHost = liftConsumerHostDefault
	}

	referer := strings.TrimSpace(src.EmbedReferer)
	if referer == "" {
		referer = liftEmbedRefererDefault
	}

	primary := src.PrimaryHosts
	if len(primary) == 0 {
		primary = append([]string{}, liftDefaultPrimaryHosts...)
	}

	user := strings.TrimSpace(src.BasicAuthUser)
	if user == "" {
		user = liftBasicAuthUserDefault
	}
	pass := strings.TrimSpace(src.BasicAuthPass)
	if pass == "" {
		pass = liftBasicAuthPassDefault
	}

	gist := strings.TrimSpace(src.GistID)
	if gist == "" {
		gist = liftGistIDDefault
	}
	scraft := strings.TrimSpace(src.SourcecraftURL)
	if scraft == "" {
		scraft = liftSourcecraftURLDefault
	}

	c := &liftChecker{
		client:         httpclient.NewForBalancer("lift", 12*time.Second),
		embedHost:      embedHost,
		consumerHost:   consumerHost,
		embedReferer:   referer,
		primaryHosts:   primary,
		sourcecraftURL: scraft,
		gistID:         gist,
		basicAuthUser:  user,
		basicAuthPass:  pass,
		apiCacheTTL:    liftAPICacheTTL,
	}

	// Allow override of the resolved API host from config (skip resolver).
	if h := strings.TrimRight(strings.TrimSpace(src.APIHost), "/"); h != "" {
		if !strings.Contains(h, "://") {
			h = "https://" + h
		}
		c.apiHost = h
		c.apiExpiry = time.Now().Add(24 * time.Hour)
	}

	return c
}

// ---------------------------------------------------------------------------
// HTTP routing
// ---------------------------------------------------------------------------

func (l *liftChecker) Handle(cfg config.Config, plugin string, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")

		switch raw {
		case "lift":
			if parseBoolParam(r.URL.Query().Get("checksearch")) {
				show, quality := l.checkSearchQuality(r)
				if quality == "" {
					quality = pluginQualityBadgeGet("lift")
				}
				// rch:true + quality badge → Lampa highlights the source as
				// "found and recommended" (matches Collaps/fancdn/uaflix
				// behavior). rch:false would render the entry greyed out
				// even though clicking it works.
				writeCheckSearchResponse(w, show, quality)
				return
			}
			l.index(w, r, plugin, links)
			return

		case "lift/embed":
			l.proxyEmbed(w, r)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "lift route is not implemented",
			"balanser": raw,
		})
	}
}

// ---------------------------------------------------------------------------
// Embed URL construction & fetch
// ---------------------------------------------------------------------------

// buildEmbedURL produces a zenithjs embed URL keyed by KP id when available,
// IMDB when not, and falling back to Lift's own internal id (passed as `orid`).
// Season/episode are appended as query params understood by zenithjs.
func (l *liftChecker) buildEmbedURL(orid, kinopoiskID int64, imdbID string, season, episode int) string {
	var path string
	switch {
	case kinopoiskID > 0:
		path = "/embed/kp/" + strconv.FormatInt(kinopoiskID, 10)
	case imdbID != "":
		path = "/embed/imdb/" + url.PathEscape(strings.TrimSpace(imdbID))
	case orid > 0:
		path = "/embed/movie/" + strconv.FormatInt(orid, 10)
	default:
		return ""
	}

	q := url.Values{}
	q.Set("host", l.consumerHost)
	if season > 0 {
		q.Set("season", strconv.Itoa(season))
	}
	if episode > 0 {
		q.Set("episode", strconv.Itoa(episode))
	}

	return l.embedHost + path + "?" + q.Encode()
}

// liftSetEmbedHeaders applies the headers used to fetch zenithjs embed pages.
// MUST stay aligned with liftStreamHeaders (UA-binding by interkh CDN).
func (l *liftChecker) liftSetEmbedHeaders(r *http.Request) {
	r.Header.Set("User-Agent", liftEmbedUA)
	r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	r.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	r.Header.Set("Cache-Control", "no-cache")
	r.Header.Set("Pragma", "no-cache")
	if l.embedReferer != "" {
		r.Header.Set("Referer", l.embedReferer)
		r.Header.Set("Origin", strings.TrimRight(l.embedReferer, "/"))
	}
	r.Header.Set("Sec-Fetch-Dest", "iframe")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
}

// liftStreamHeaders returns the headers attached to proxied stream requests.
// `Referer`/`Origin` mimic a browser embed of zenithjs (which is what cdnr.interkh.com sees).
func (l *liftChecker) liftStreamHeaders() map[string]string {
	return map[string]string{
		"User-Agent":      liftEmbedUA,
		"Accept":          "*/*",
		"Accept-Language": "ru-RU,ru;q=0.9,en;q=0.8",
		"Origin":          l.embedHost,
		"Referer":         l.embedHost + "/",
		"Sec-Fetch-Dest":  "empty",
		"Sec-Fetch-Mode":  "cors",
		"Sec-Fetch-Site":  "cross-site",
	}
}

func (l *liftChecker) fetchEmbed(ctx context.Context, embedURL string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, embedURL, nil)
	if err != nil {
		return "", false
	}
	l.liftSetEmbedHeaders(req)

	resp, err := balancerDoWithRetry(ctx, l.client, req, 2)
	if err != nil {
		log.Debug().Err(err).Str("url", truncURL(embedURL)).Msg("lift: fetchEmbed failed")
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("url", truncURL(embedURL)).Msg("lift: fetchEmbed non-2xx")
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// ---------------------------------------------------------------------------
// checkSearch / index
// ---------------------------------------------------------------------------

// liftQualityCacheEntry caches the badge derived from a title's master playlist
// so repeated /lite/events probes don't re-fetch it.
type liftQualityCacheEntry struct {
	badge   string
	expires time.Time
}

var liftQualityCache sync.Map // embedURL -> liftQualityCacheEntry

const liftQualityTTL = 30 * time.Minute

// liftBadgeFromHLS resolves the real badge from the master playlist. Lift
// advertises FHD statically, but its streams top out at 720p (sampled across
// six titles on 2026-08-08: 720p, some 530p) — the static badge overstated
// every title. Costs one small extra request, cached per embed URL.
func (l *liftChecker) liftBadgeFromHLS(ctx context.Context, embedURL, body string) string {
	if entry, ok := liftQualityCache.Load(embedURL); ok {
		if e, valid := entry.(liftQualityCacheEntry); valid && time.Now().Before(e.expires) {
			return e.badge
		}
	}

	badge := ""
	if stream := collapsNormalizeURL(submatch1(collapsHLSRe, body)); stream != "" {
		best := 0
		for label := range l.fetchHLSQualities(ctx, stream) {
			if h, err := strconv.Atoi(strings.TrimSuffix(label, "p")); err == nil && h > best {
				best = h
			}
		}
		badge = liftHeightBadge(best)
	}
	liftQualityCache.Store(embedURL, liftQualityCacheEntry{badge: badge, expires: time.Now().Add(liftQualityTTL)})
	return badge
}

// liftHeightBadge maps a stream height to the canonical badge. Heights are not
// always round (lift serves 530p), so compare by thresholds rather than equality.
// 1440p gets its own "2K" badge rather than being rounded down to FHD.
func liftHeightBadge(height int) string {
	switch {
	case height >= 2000:
		return "4K"
	case height >= 1400:
		return "2K"
	case height >= 1000:
		return "FHD"
	case height >= 700:
		return "HD"
	case height > 0:
		return "SD"
	}
	return ""
}

func (l *liftChecker) checkSearch(r *http.Request) bool {
	show, _ := l.checkSearchQuality(r)
	return show
}

// checkSearchQuality reports availability plus the badge derived from the actual
// stream, reusing the embed fetch checkSearch already performs.
func (l *liftChecker) checkSearchQuality(r *http.Request) (bool, string) {
	q := r.URL.Query()
	kp := parseInt64(q.Get("kinopoisk_id"))
	if kp == 0 {
		kp = parseInt64(q.Get("id"))
	}
	imdb := strings.TrimSpace(q.Get("imdb_id"))

	if kp == 0 && imdb == "" {
		// Probe zenithjs root liveness so the source disappears on outages.
		return l.probeRoot(r.Context()), ""
	}

	embedURL := l.buildEmbedURL(0, kp, imdb, 0, 0)
	if embedURL == "" {
		return false, ""
	}
	body, ok := l.fetchEmbed(r.Context(), embedURL)
	if !ok {
		return false, ""
	}
	// Either a movie source (hls/dash) or a serial seasons array.
	if collapsHLSRe.MatchString(body) || collapsDashRe.MatchString(body) {
		return true, l.liftBadgeFromHLS(r.Context(), embedURL, body)
	}
	if raw := submatch1(collapsSeasonsRe, body); strings.TrimSpace(raw) != "" {
		// Serial: episode streams live one level deeper, so no cheap badge here.
		return true, ""
	}
	return false, ""
}

func (l *liftChecker) probeRoot(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodHead, l.embedHost+"/", nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", liftEmbedUA)
	resp, err := l.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 500
}

func (l *liftChecker) index(w http.ResponseWriter, req *http.Request, plugin string, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	orid := parseInt64(q.Get("orid"))
	if orid == 0 {
		orid = parseInt64(q.Get("id"))
	}
	kinopoiskID := parseInt64(q.Get("kinopoisk_id"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))

	season, seasonSet := getsTVQueryInt(q.Get("s"))
	if !seasonSet {
		season = -1
	}

	if kinopoiskID == 0 && imdbID == "" && orid == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// One embed hit covers both cases: serial (seasons:[…]) and movie (source:{…}).
	embedURL := l.buildEmbedURL(orid, kinopoiskID, imdbID, 0, 0)
	body, ok := l.fetchEmbed(req.Context(), embedURL)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if raw := strings.TrimSpace(submatch1(collapsSeasonsRe, body)); raw != "" {
		if serial, ok := collapsParseSeasons(raw); ok && len(serial) > 0 {
			route := "/lite/lift"
			l.writeSerial(w, req, route, season, rjson, title, originalTitle,
				orid, kinopoiskID, imdbID, serial, links)
			return
		}
	}

	loc := collapsMakePlayerRe.FindStringIndex(body)
	if len(loc) == 2 && loc[1] < len(body) {
		l.writeMovie(w, req, rjson, title, originalTitle, body[loc[1]:], links)
		return
	}

	writeGetsTVEmpty(w, rjson)
}

// ---------------------------------------------------------------------------
// writeMovie
// ---------------------------------------------------------------------------

func (l *liftChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle, content string, links *proxylink.Manager) {
	hls := collapsNormalizeURL(submatch1(collapsHLSRe, content))
	dash := collapsNormalizeURL(submatch1(collapsDashRe, content))

	stream := hls
	if stream == "" {
		stream = dash
	}
	if stream == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	headers := l.liftStreamHeaders()

	var qualMapAny map[string]any
	if strings.Contains(stream, ".m3u8") {
		if quals := l.fetchHLSQualities(req.Context(), stream); len(quals) > 0 {
			qualMapAny = make(map[string]any, len(quals))
			for label, variantURL := range quals {
				qualMapAny[label] = streamProxyURLWithHeaders(req, variantURL, "lift", links, headers)
			}
		}
	}

	streamProxied := streamProxyURLWithHeaders(req, stream, "lift", links, headers)

	name := strings.TrimSpace(submatch1(collapsAudioFirstRe, content))
	if name == "" {
		name = "По умолчанию"
	}

	row := map[string]any{
		"method": "play",
		"url":    streamProxied,
		"stream": streamProxied,
		"name":   name,
		"title":  getsTVJoinName(title, originalTitle),
	}
	if qualMapAny != nil {
		row["quality"] = qualMapAny
		row["qualitys"] = qualMapAny
	}
	if subs := collapsParseSubtitlesRaw(submatch1(collapsCCRe, content)); len(subs) > 0 {
		row["subtitles"] = subs
	}
	if voice := collapsAudioNames(content); voice != "" {
		row["voice_name"] = voice
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
	getsTVAppendMovieHTML(&sb, row, name, true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// fetchHLSQualities reuses Collaps's manifest-parsing helper.
func (l *liftChecker) fetchHLSQualities(ctx context.Context, manifestURL string) map[string]string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil
	}
	for k, v := range l.liftStreamHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := balancerDoWithRetry(ctx, l.client, req, 1)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	return parseHLSQualities(string(body), manifestURL)
}

// ---------------------------------------------------------------------------
// writeSerial
// ---------------------------------------------------------------------------

func (l *liftChecker) writeSerial(
	w http.ResponseWriter,
	req *http.Request,
	route string,
	season int,
	rjson bool,
	title, originalTitle string,
	orid, kinopoiskID int64, imdbID string,
	serial []collapsSeason,
	links *proxylink.Manager,
) {
	if len(serial) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	host := hostFromRequest(req)

	ordered := make([]collapsSeason, 0, len(serial))
	for _, s := range serial {
		if s.Season > 0 {
			ordered = append(ordered, s)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Season < ordered[j].Season })
	if len(ordered) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if season == -1 {
		// Season-list view.
		rows := make([]map[string]any, 0, len(ordered))
		labels := make([]string, 0, len(ordered))
		for _, s := range ordered {
			name := strconv.Itoa(s.Season) + " сезон"
			rows = append(rows, map[string]any{
				"method": "link",
				"id":     s.Season,
				"url":    l.buildSeasonLink(host, route, rjson, title, originalTitle, orid, kinopoiskID, imdbID, s.Season),
				"name":   name,
			})
			labels = append(labels, name)
		}

		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{
				"type": "season",
				"data": rows,
			})
			return
		}

		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range rows {
			getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Episodes view for the requested season.
	var episodes []collapsEpisode
	for _, s := range ordered {
		if s.Season == season {
			episodes = s.Episodes
			break
		}
	}
	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	headers := l.liftStreamHeaders()
	baseTitle := getsTVJoinName(title, originalTitle)

	type epRowT struct {
		data  map[string]any
		label string
		num   int
	}
	rows := make([]epRowT, 0, len(episodes))

	for _, ep := range episodes {
		hls := collapsNormalizeURL(ep.HLS)
		dash := collapsNormalizeURL(ep.Dasha)
		if dash == "" {
			dash = collapsNormalizeURL(ep.Dash)
		}
		stream := hls
		if stream == "" {
			stream = dash
		}
		if stream == "" {
			continue
		}

		streamProxied := streamProxyURLWithHeaders(req, stream, "lift", links, headers)

		epTitle := strings.TrimSpace(ep.Episode)
		if epTitle == "" {
			continue
		}
		epNum := collapsEpisodeNumber(epTitle)

		row := map[string]any{
			"method": "play",
			"url":    streamProxied,
			"stream": streamProxied,
			"title":  fmt.Sprintf("%s S%02dE%s", baseTitle, season, epTitle),
			"s":      season,
			"e":      epNum,
		}

		// Audio voice list (first in `audio.names` is the displayed track).
		if len(ep.Audio.Names) > 0 {
			row["voice_name"] = strings.Join(ep.Audio.Names, " / ")
		}
		if len(ep.CC) > 0 {
			subs := make([]map[string]any, 0, len(ep.CC))
			for _, c := range ep.CC {
				subs = append(subs, map[string]any{
					"method": "link",
					"label":  c.Name,
					"url":    c.URL,
				})
			}
			row["subtitles"] = subs
		}

		rows = append(rows, epRowT{data: row, label: epTitle + " серия", num: epNum})
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].num < rows[j].num })

	if rjson {
		out := make([]map[string]any, len(rows))
		for i, r := range rows {
			out[i] = r.data
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "episode",
			"data": out,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, r := range rows {
		getsTVAppendMovieHTML(&sb, r.data, r.label, i == 0, season, r.num)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (l *liftChecker) buildSeasonLink(host, route string, rjson bool, title, originalTitle string, orid, kinopoiskID int64, imdbID string, season int) string {
	q := url.Values{}
	q.Set("s", strconv.Itoa(season))
	if title != "" {
		q.Set("title", title)
	}
	if originalTitle != "" {
		q.Set("original_title", originalTitle)
	}
	if orid > 0 {
		q.Set("orid", strconv.FormatInt(orid, 10))
	}
	if kinopoiskID > 0 {
		q.Set("kinopoisk_id", strconv.FormatInt(kinopoiskID, 10))
	}
	if imdbID != "" {
		q.Set("imdb_id", imdbID)
	}
	if rjson {
		q.Set("rjson", "true")
	}
	return host + route + "?" + q.Encode()
}

// ---------------------------------------------------------------------------
// Iframe proxy with Referer spoof
// ---------------------------------------------------------------------------
//
// /lite/lift/embed?kinopoisk_id=&imdb_id=&orid=&s=&e=
//
// Returns the zenithjs embed HTML with iframe-busting check stripped, served
// from our origin so cross-origin iframe restrictions don't apply on the
// Lampa client side.
func (l *liftChecker) proxyEmbed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	orid := parseInt64(q.Get("orid"))
	if orid == 0 {
		orid = parseInt64(q.Get("id"))
	}
	kp := parseInt64(q.Get("kinopoisk_id"))
	imdb := strings.TrimSpace(q.Get("imdb_id"))
	season, _ := getsTVQueryInt(q.Get("s"))
	episode, _ := getsTVQueryInt(q.Get("e"))

	if kp == 0 && imdb == "" && orid == 0 {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	embedURL := l.buildEmbedURL(orid, kp, imdb, season, episode)
	if embedURL == "" {
		http.Error(w, "bad embed", http.StatusBadRequest)
		return
	}

	body, ok := l.fetchEmbed(r.Context(), embedURL)
	if !ok {
		http.Error(w, "upstream embed unavailable", http.StatusBadGateway)
		return
	}

	body = liftStripIframeBust(body)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "ALLOWALL")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, body)
}

// liftStripIframeBust removes the cross-origin redirect block that zenithjs
// embeds at the top of every page. Two regex passes (outer = the one with
// fetch+ping, inner = the messaging shim) plus a hard string fallback so
// that minor formatting changes don't reintroduce the redirect.
func liftStripIframeBust(html string) string {
	html = liftIframeBustOuterRe.ReplaceAllString(html, "/* lift: stripped */")
	html = liftIframeBustInnerRe.ReplaceAllString(html, "/* lift: stripped */")
	// Harden: also blank the global flag so any leftover branch using
	// `isEmbedded` still treats us as same-origin.
	html = strings.Replace(html,
		"var isEmbedded; try { isEmbedded = self !== top; } catch (e) {isEmbedded = true}",
		"var isEmbedded = false;",
		1,
	)
	html = strings.Replace(html,
		"var sameOrigin; try { sameOrigin = top.location.origin === location.origin } catch(e) {}",
		"var sameOrigin = true;",
		1,
	)
	return html
}

// ---------------------------------------------------------------------------
// Lift catalog API client (search/list/info) — used by xsearch adapter only.
// Playback path bypasses Lift API entirely and goes via zenithjs.
// ---------------------------------------------------------------------------

func (l *liftChecker) resolveAPIHost(ctx context.Context) string {
	l.mu.RLock()
	if l.apiHost != "" && time.Now().Before(l.apiExpiry) {
		host := l.apiHost
		l.mu.RUnlock()
		return host
	}
	l.mu.RUnlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	// Re-check under write lock (could've been resolved by another goroutine).
	if l.apiHost != "" && time.Now().Before(l.apiExpiry) {
		return l.apiHost
	}

	candidates := append([]string{}, l.primaryHosts...)
	if extra := l.fetchDomainList(ctx, l.sourcecraftURL); len(extra) > 0 {
		candidates = append(candidates, extra...)
	}
	if l.gistID != "" {
		gistURL := "https://api.github.com/gists/" + l.gistID
		if extra := l.fetchGistDomains(ctx, gistURL); len(extra) > 0 {
			candidates = append(candidates, extra...)
		}
	}

	seen := make(map[string]bool, len(candidates))
	for _, raw := range candidates {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		raw = strings.TrimRight(raw, "/")
		if seen[raw] {
			continue
		}
		seen[raw] = true

		api := liftAPIHostFromFrontend(raw)
		if l.probeAPIHost(ctx, api) {
			l.apiHost = api
			l.apiExpiry = time.Now().Add(l.apiCacheTTL)
			log.Info().Str("host", api).Msg("lift: resolved api host")
			return api
		}
	}

	// Nothing worked — keep last known host (may still be partly working).
	return l.apiHost
}

func liftAPIHostFromFrontend(frontend string) string {
	u, err := url.Parse(frontend)
	if err != nil || u.Host == "" {
		return ""
	}
	host := u.Host
	parts := strings.Split(host, ".")
	if len(parts) >= 3 {
		parts[0] = "api"
	} else if len(parts) >= 2 {
		host = "api." + host
		return u.Scheme + "://" + host
	}
	return u.Scheme + "://" + strings.Join(parts, ".")
}

func (l *liftChecker) probeAPIHost(ctx context.Context, apiHost string) bool {
	if apiHost == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, apiHost+"/list?type=film&limit=1", nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", liftEmbedUA)
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(l.basicAuthUser, l.basicAuthPass)

	resp, err := l.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return len(body) > 0 && (body[0] == '[' || body[0] == '{')
}

func (l *liftChecker) fetchDomainList(ctx context.Context, target string) []string {
	if target == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", liftEmbedUA)
	resp, err := l.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil || len(body) == 0 {
		return nil
	}
	out := make([]string, 0, 4)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func (l *liftChecker) fetchGistDomains(ctx context.Context, target string) []string {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", liftEmbedUA)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := l.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<18))
	if err != nil {
		return nil
	}
	var parsed struct {
		Files map[string]struct {
			Content string `json:"content"`
		} `json:"files"`
	}
	if err := stdjson.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	for _, f := range parsed.Files {
		if f.Content == "" {
			continue
		}
		out := make([]string, 0, 4)
		for _, line := range strings.Split(f.Content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			out = append(out, line)
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// searchTitles queries Lift's catalog `/search?q=`. Returns up to TotalCount
// items, each with a populated `info.id` (kinopoisk_id) when /info is fetched.
// Used by the xsearch adapter for global-search aggregation.
func (l *liftChecker) searchTitles(ctx context.Context, query string) ([]liftItem, bool) {
	apiHost := l.resolveAPIHost(ctx)
	if apiHost == "" {
		return nil, false
	}
	q := url.Values{}
	q.Set("q", query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiHost+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", liftEmbedUA)
	req.SetBasicAuth(l.basicAuthUser, l.basicAuthPass)

	resp, err := balancerDoWithRetry(ctx, l.client, req, 2)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	var sr liftSearchResponse
	if err := stdjson.Unmarshal(body, &sr); err != nil {
		return nil, false
	}
	return sr.Items, true
}

// infoByLiftID fetches /info/{id} and extracts the kinopoisk_id from info.id.
// Returns 0 when missing/unparseable.
func (l *liftChecker) infoKPID(ctx context.Context, liftID int64) int64 {
	apiHost := l.resolveAPIHost(ctx)
	if apiHost == "" || liftID <= 0 {
		return 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiHost+"/info/"+strconv.FormatInt(liftID, 10), nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", liftEmbedUA)
	req.SetBasicAuth(l.basicAuthUser, l.basicAuthPass)

	resp, err := balancerDoWithRetry(ctx, l.client, req, 2)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0
	}

	// The "info" sub-object holds string-typed KP id. A regex over the raw
	// body avoids defining the full Yii-style schema (lots of nullable fields).
	var dec struct {
		Info struct {
			ID stdjson.RawMessage `json:"id"`
		} `json:"info"`
	}
	if err := stdjson.Unmarshal(body, &dec); err == nil {
		raw := strings.Trim(string(dec.Info.ID), `"`)
		if v, _ := strconv.ParseInt(raw, 10, 64); v > 0 {
			return v
		}
	}
	// Fallback: regex.
	if m := liftInfoKPIDRe.FindAllStringSubmatch(string(body), -1); len(m) > 0 {
		// Pick the first id that looks like a KP id (5-9 digits).
		for _, mm := range m {
			if len(mm) > 1 {
				v, _ := strconv.ParseInt(mm[1], 10, 64)
				if v >= 1000 && v < 1_000_000_000 {
					return v
				}
			}
		}
	}
	return 0
}
