package litesrc

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/proxy"
)

const kinogoUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// kinogoDefaultHost is the only mirror that still serves the DLE template this
// parser understands (verified 2026-08-07).
const kinogoDefaultHost = "https://kinogo.la"

// kinogoDeadHosts are mirrors that no longer answer scrapers: kinogo.biz and
// kinogo.luxury sit behind a Cloudflare JS challenge (403 `cf-mitigated:
// challenge`), kinogo.ec the same, kinogo.app is a parked stub. They are demoted
// below kinogoDefaultHost instead of being dropped, so a revived mirror is still
// tried after the live one.
var kinogoDeadHosts = map[string]struct{}{
	"kinogo.biz":     {},
	"kinogo.luxury":  {},
	"kinogo.ec":      {},
	"kinogo.app":     {},
	"kinogo-go.tv":   {},
	"kinogo.media":   {}, // videodb host, redirects away from the search template
	"kinogo.inc":     {},
	"kinogo.uno":     {},
	"kinogo.pro":     {},
	"kinogo.dev":     {},
	"kinogo.cc":      {},
	"kinogo1.biz":    {},
	"www.kinogo.biz": {},
}

var (
	// kinogoSearchBlockRe matches one search result block in the DLE template
	// used by kinogo.la. Captures: id, href, title (may include " (YYYY)"),
	// poster (data-src), year. Order matches actual HTML emission: opening div
	// → h2 zagolovki → movie__info-img/data-src → movie__info-item with year.
	kinogoSearchBlockRe = regexp.MustCompile(
		`(?is)<div class="movie" id="(\d+)">.*?` +
			`<h2 class="zagolovki"><a href="([^"]+)"[^>]*>([^<]+)</a>.*?` +
			`data-src="([^"]+)".*?` +
			`Год выпуска:</b>\s*<a[^>]*>(\d{4})</a>`,
	)
	// kinogoIframeRe extracts data-src URLs from <iframe>.
	kinogoIframeRe = regexp.MustCompile(`(?is)<iframe[^>]*data-src="([^"]+)"`)
	// kinogoTitleSuffixRe strips a trailing parenthesised qualifier from a search
	// result title: the year ("Веном (2018)") or a season range, which kinogo
	// bakes into the title of every series ("Реальные пацаны (1-10 сезон)",
	// "Укрытие (3 сезон)"). Leaving the season suffix in place meant NO series
	// ever produced an exact match — every one of them fell through to the
	// `similar` list, which /capi then drops when it cannot pin the title.
	kinogoTitleSuffixRe = regexp.MustCompile(`(?i)\s*\((?:\d{4}|[^()]*(?:сезон|season|сери[ияй])[^()]*)\)\s*$`)
)

type kinogoChecker struct {
	client *http.Client
	// embedClients fetch the player iframe (api.ortified.ws). They are separate
	// from the main client because the two legs need OPPOSITE routing: ortified
	// answers 422 to this server's datacenter IP but 200 through a SOCKS egress,
	// while kinogo.la is reachable directly and NOT through that same proxy.
	//
	// A LIST, not one client: ortified bans egress IPs one after another (three
	// times in two days), so a single configured proxy is a single point of
	// failure. Empty when nothing is configured — then only the direct client
	// is used.
	embedClients []kinogoEmbedRoute
	// lastEmbedRoute remembers which route last worked, so the healthy one is
	// tried first instead of paying for the dead ones every time.
	lastEmbedRoute atomic.Int32
	// host is the primary mirror (config host, or kinogoDefaultHost when the
	// configured one is known-dead).
	host string
	// hosts is the ordered candidate list, primary first.
	hosts []string
	// active remembers the mirror that last served a page so subsequent
	// requests skip the dead ones.
	active atomic.Value // string
}

type kinogoSimilar struct {
	Title string
	Year  string
	Href  string
	Img   string
}

type kinogoSearch struct {
	ExactHref string
	Items     []kinogoSimilar
}

// kinogoEmbed holds the parsed VenomPlayer payload from an api.ortified.ws iframe.
// kinogo.la now embeds the same Collaps-style player, so we reuse collaps* types
// and regexes for parsing seasons/episodes/hls/dash/audio/cc.
type kinogoEmbed struct {
	// Content is the body slice starting right after `makePlayer({` for movies.
	Content string
	// Serial is the parsed seasons array for serial iframes.
	Serial []collapsSeason
}

func NewKinogoChecker(cfg config.Config) *kinogoChecker {
	configured := kinogoNormalizeHost(cfg.Online.Kinogo.Host)

	hosts := make([]string, 0, 2)
	seen := map[string]struct{}{}
	add := func(h string) {
		if h == "" {
			return
		}
		if _, ok := seen[h]; ok {
			return
		}
		seen[h] = struct{}{}
		hosts = append(hosts, h)
	}
	// A configured mirror wins only while it is not on the known-dead list;
	// otherwise the live default goes first and the configured one is kept as a
	// trailing candidate (in case it comes back).
	if configured != "" && !kinogoHostIsDead(configured) {
		add(configured)
	}
	add(kinogoDefaultHost)
	add(configured)

	k := &kinogoChecker{
		client:       httpclient.NewForBalancer("kinogo", 12*time.Second),
		embedClients: kinogoEmbedClients(cfg.Online.Kinogo.EmbedSocksProxy),
		host:         hosts[0],
		hosts:        hosts,
	}
	if len(hosts) > 1 {
		log.Debug().Strs("hosts", hosts).Msg("kinogo: mirror candidates")
	}
	return k
}

// kinogoEmbedRoute is one SOCKS egress for the embed leg.
type kinogoEmbedRoute struct {
	addr   string
	client *http.Client
}

// kinogoEmbedClients builds the embed-leg clients. The setting accepts several
// comma/space separated proxies ("socks5://127.0.0.1:40009, 127.0.0.1:41000"):
// ortified keeps banning egress IPs, and a list lets the source survive losing
// one without a config edit.
func kinogoEmbedClients(raw string) []kinogoEmbedRoute {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
	routes := make([]kinogoEmbedRoute, 0, len(fields))
	seen := map[string]struct{}{}
	for _, f := range fields {
		addr := strings.TrimSpace(f)
		if addr == "" {
			continue
		}
		// Dedupe on the NORMALISED form: "socks5://127.0.0.1:40009" and
		// "127.0.0.1:40009" are the same egress, and listing both would just
		// make the failover try a dead proxy twice.
		key := addr
		if !strings.Contains(key, "://") {
			key = "socks5://" + key
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if c := kinogoEmbedClient(addr); c != nil {
			routes = append(routes, kinogoEmbedRoute{addr: addr, client: c})
		}
	}
	return routes
}

// kinogoEmbedClient builds the SOCKS5-routed client for the embed leg, or nil
// when the address is unusable.
func kinogoEmbedClient(raw string) *http.Client {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return nil
	}
	if !strings.Contains(addr, "://") {
		addr = "socks5://" + addr
	}
	proxyURL, err := url.Parse(addr)
	if err != nil {
		log.Error().Err(err).Str("proxy", raw).Msg("kinogo: bad embed_socks_proxy, embed will go direct")
		return nil
	}
	dialer, err := proxy.FromURL(proxyURL, proxy.Direct)
	if err != nil {
		log.Error().Err(err).Str("proxy", raw).Msg("kinogo: cannot use embed_socks_proxy, embed will go direct")
		return nil
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		log.Error().Str("proxy", raw).Msg("kinogo: embed proxy dialer has no context support")
		return nil
	}
	log.Info().Str("proxy", addr).Msg("kinogo: player embed may route through SOCKS5 (site stays direct)")
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext:         contextDialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns:        20,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// kinogoNormalizeHost trims and schemes a configured host value.
func kinogoNormalizeHost(raw string) string {
	host := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if host == "" {
		return ""
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return host
}

func kinogoHostIsDead(host string) bool {
	u, err := url.Parse(host)
	if err != nil {
		return false
	}
	_, dead := kinogoDeadHosts[strings.ToLower(u.Hostname())]
	return dead
}

// hostCandidates returns the mirrors to try, most-recently-working one first.
func (k *kinogoChecker) hostCandidates() []string {
	base := k.hosts
	if len(base) == 0 {
		base = []string{k.host}
	}
	active, _ := k.active.Load().(string)
	if active == "" || active == base[0] {
		return base
	}
	out := make([]string, 0, len(base))
	out = append(out, active)
	for _, h := range base {
		if h != active {
			out = append(out, h)
		}
	}
	return out
}

func (k *kinogoChecker) Handle(_ config.Config, proxyLinks *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := k.checkSearch(req)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if show {
				_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		k.index(w, req, proxyLinks)
	}
}

func (k *kinogoChecker) checkSearch(req *http.Request) bool {
	title := strings.TrimSpace(req.URL.Query().Get("title"))
	if title == "" {
		return k.probe(req)
	}
	body, ok := k.fetchSearch(req.Context(), title)
	if !ok {
		return k.probe(req)
	}
	results := k.parseSearch(body, []string{title}, 0)
	if len(results.Items) > 0 || results.ExactHref != "" {
		return true
	}
	return k.probe(req)
}

func (k *kinogoChecker) probe(req *http.Request) bool {
	_, ok := k.fetchSite(req.Context(), "/")
	return ok
}

func (k *kinogoChecker) setHeaders(req *http.Request, referer string) {
	req.Header.Set("User-Agent", kinogoUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
}

func (k *kinogoChecker) fetchSearch(ctx context.Context, title string) (string, bool) {
	return k.fetchSite(ctx, "/search/"+url.PathEscape(strings.TrimSpace(title)))
}

// fetchSite requests a site-relative path, walking the mirror list until one
// answers with a real page (a Cloudflare interstitial does not count). The
// mirror that answered is remembered for subsequent calls.
func (k *kinogoChecker) fetchSite(ctx context.Context, path string) (string, bool) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	candidates := k.hostCandidates()
	for _, host := range candidates {
		body, ok := k.fetchText(ctx, strings.TrimRight(host, "/")+path, host+"/")
		if !ok {
			continue
		}
		if kinogoIsChallenge(body) {
			log.Warn().Str("host", host).Str("path", path).Msg("kinogo: mirror answered with a Cloudflare challenge")
			continue
		}
		k.active.Store(host)
		return body, true
	}
	log.Warn().Strs("hosts", candidates).Str("path", path).Msg("kinogo: no mirror served the page")
	return "", false
}

// fetchEmbedBody fetches the player iframe. It tries the direct client first and
// falls back to the SOCKS-routed one, because api.ortified.ws rejects this
// server's datacenter IP with a bodyless 422 (verified 2026-08-07: 422 on every
// header combination direct, 200 through the proxy). Direct-first keeps installs
// whose IP is fine from paying for a proxy hop.
func (k *kinogoChecker) fetchEmbedBody(ctx context.Context, target, referer string) (string, bool) {
	if body, ok := k.fetchTextWith(ctx, k.client, target, referer); ok {
		return body, true
	}
	if len(k.embedClients) == 0 {
		return "", false
	}

	// Start from the route that worked last time, then walk the rest.
	start := int(k.lastEmbedRoute.Load())
	if start < 0 || start >= len(k.embedClients) {
		start = 0
	}
	for i := 0; i < len(k.embedClients); i++ {
		idx := (start + i) % len(k.embedClients)
		route := k.embedClients[idx]
		body, ok := k.fetchTextWith(ctx, route.client, target, referer)
		if !ok {
			continue
		}
		k.lastEmbedRoute.Store(int32(idx))
		log.Debug().Str("iframe", target).Str("proxy", route.addr).
			Msg("kinogo: embed served via SOCKS after a direct failure")
		return body, true
	}
	log.Warn().Str("iframe", target).Int("routes", len(k.embedClients)).
		Msg("kinogo: embed refused directly AND through every configured proxy — the egress IPs are banned, add a fresh one to [online.kinogo] embed_socks_proxy")
	return "", false
}

func (k *kinogoChecker) fetchText(ctx context.Context, target, referer string) (string, bool) {
	return k.fetchTextWith(ctx, k.client, target, referer)
}

func (k *kinogoChecker) fetchTextWith(ctx context.Context, client *http.Client, target, referer string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	k.setHeaders(httpReq, referer)
	if client == nil {
		client = k.client
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		log.Debug().Err(err).Str("url", target).Msg("kinogo: fetch failed")
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("cf", resp.Header.Get("cf-mitigated")).Str("url", target).Msg("kinogo: non-2xx response")
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// kinogoIsChallenge reports whether a 2xx body is actually a Cloudflare
// interstitial rather than site content (CF sometimes serves the challenge with
// status 200). Challenge pages are small; real pages are ~100KB.
func kinogoIsChallenge(body string) bool {
	if len(body) > 64<<10 {
		return false
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "just a moment") ||
		strings.Contains(lower, "challenges.cloudflare.com") ||
		strings.Contains(lower, "/cdn-cgi/challenge-platform")
}

func (k *kinogoChecker) index(w http.ResponseWriter, req *http.Request, proxyLinks *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	href := strings.TrimSpace(q.Get("href"))
	year, _ := getsTVQueryInt(q.Get("year"))
	s, sOK := getsTVQueryInt(q.Get("s"))
	if !sOK {
		s = -1
	}

	log.Debug().Str("href", href).Str("title", title).Int("year", year).Msg("kinogo: index called")

	if href == "" {
		searchTitle := title
		if searchTitle == "" {
			searchTitle = originalTitle
		}
		if searchTitle == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}
		searchHTML, ok := k.fetchSearch(req.Context(), searchTitle)
		if !ok {
			log.Warn().Str("title", searchTitle).Str("host", k.host).Msg("kinogo: search fetch failed (network/CF/RKN block?)")
			writeGetsTVEmpty(w, rjson)
			return
		}
		// Match against both names the card is known under — kinogo titles its
		// entries in Russian, but the client sometimes sends only the original.
		wantTitles := []string{title, originalTitle}
		results := k.parseSearch(searchHTML, wantTitles, year)
		// The site indexes Russian titles; if the Russian query found nothing,
		// retry with the original one before giving up.
		if len(results.Items) == 0 && originalTitle != "" && !strings.EqualFold(originalTitle, searchTitle) {
			if retryHTML, retryOK := k.fetchSearch(req.Context(), originalTitle); retryOK {
				results = k.parseSearch(retryHTML, wantTitles, year)
				log.Debug().Str("original_title", originalTitle).Int("items", len(results.Items)).
					Msg("kinogo: retried search with original title")
			}
		}
		if len(results.Items) == 0 {
			log.Warn().Str("title", searchTitle).Int("body_len", len(searchHTML)).Msg("kinogo: search parsed zero results — check site template or geo")
		} else {
			log.Debug().Int("items", len(results.Items)).Str("exact", results.ExactHref).Msg("kinogo: search parsed")
		}
		if similar || results.ExactHref == "" {
			if len(results.Items) == 0 {
				writeGetsTVEmpty(w, rjson)
				return
			}
			k.writeSimilar(w, req, rjson, results.Items, title, originalTitle, year)
			return
		}
		href = results.ExactHref
	}

	embed, ok := k.fetchEmbed(req.Context(), href)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if len(embed.Serial) > 0 {
		k.writeSerial(w, req, rjson, href, embed.Serial, title, originalTitle, year, s, proxyLinks)
		return
	}
	k.writeMovie(w, req, rjson, embed.Content, title, originalTitle, proxyLinks)
}

// parseSearch extracts the result blocks and picks the exact card, if any.
// wantTitles are the names the requested card is known under (title,
// original_title); a candidate matching ANY of them counts.
//
// Year handling is deliberately loose: kinogo dates a series by its FIRST
// season and a re-release by its own year, so demanding an exact year meant the
// exact match almost never fired and every lookup degraded to a `similar` list
// that /capi then dropped. A one-year gap is accepted, and among several
// same-titled entries the one closest to the requested year wins.
func (k *kinogoChecker) parseSearch(htmlBody string, wantTitles []string, year int) kinogoSearch {
	matches := kinogoSearchBlockRe.FindAllStringSubmatch(htmlBody, -1)
	if len(matches) == 0 {
		return kinogoSearch{}
	}
	wanted := make([]string, 0, len(wantTitles))
	for _, t := range wantTitles {
		if n := normalizeSearchTitle(kinogoCleanTitle(t)); n != "" {
			wanted = append(wanted, n)
		}
	}

	out := kinogoSearch{Items: make([]kinogoSimilar, 0, len(matches))}
	bestGap := -1
	for _, m := range matches {
		if len(m) != 6 {
			continue
		}
		href := strings.TrimSpace(m[2])
		nameRaw := strings.TrimSpace(html.UnescapeString(m[3]))
		nameClean := kinogoCleanTitle(nameRaw)
		if nameClean == "" {
			nameClean = nameRaw
		}
		img := strings.TrimSpace(m[4])
		blockYear := strings.TrimSpace(m[5])
		if href == "" || nameClean == "" {
			continue
		}
		if strings.HasPrefix(img, "/") {
			img = strings.TrimRight(k.currentHost(), "/") + img
		}
		out.Items = append(out.Items, kinogoSimilar{
			Title: nameClean,
			Year:  blockYear,
			Href:  href,
			Img:   img,
		})

		candidate := normalizeSearchTitle(nameClean)
		if candidate == "" || !slices.Contains(wanted, candidate) {
			continue
		}
		gap := kinogoYearGap(year, blockYear)
		if gap < 0 {
			continue // same title, but a different release entirely
		}
		if bestGap < 0 || gap < bestGap {
			bestGap, out.ExactHref = gap, href
		}
	}
	return out
}

// kinogoCleanTitle strips the trailing "(2018)" / "(1-10 сезон)" qualifiers
// kinogo appends to every entry title.
func kinogoCleanTitle(raw string) string {
	out := strings.TrimSpace(raw)
	for {
		trimmed := strings.TrimSpace(kinogoTitleSuffixRe.ReplaceAllString(out, ""))
		if trimmed == out || trimmed == "" {
			return out
		}
		out = trimmed
	}
}

// kinogoYearGap scores a candidate year against the requested one: 0 = exact,
// 1 = off by a year (accepted), -1 = too far apart or unparsable. An unknown
// requested year matches anything.
func kinogoYearGap(want int, got string) int {
	if want <= 0 {
		return 0
	}
	gotYear, err := strconv.Atoi(strings.TrimSpace(got))
	if err != nil || gotYear <= 0 {
		return 1
	}
	gap := want - gotYear
	if gap < 0 {
		gap = -gap
	}
	if gap > 1 {
		return -1
	}
	return gap
}

func (k *kinogoChecker) fetchEmbed(ctx context.Context, href string) (kinogoEmbed, bool) {
	sitePath, external := k.splitHref(href)
	if sitePath == "" && external == "" {
		return kinogoEmbed{}, false
	}

	// moviePageURL is only used as the iframe Referer.
	moviePageURL := external
	var body string
	var ok bool
	if sitePath != "" {
		body, ok = k.fetchSite(ctx, sitePath)
		moviePageURL = strings.TrimRight(k.currentHost(), "/") + sitePath
	} else {
		body, ok = k.fetchText(ctx, external, k.host+"/")
	}
	if !ok {
		log.Warn().Str("url", moviePageURL).Msg("kinogo: movie page fetch failed")
		return kinogoEmbed{}, false
	}

	// A page usually carries several embeds (ortified, then mirrors of it). The
	// first one is not always usable — api.ortified.ws answers 422 for part of
	// the catalogue — so walk them instead of giving up on the first failure.
	iframes := k.pickIframes(body)
	if len(iframes) == 0 {
		log.Warn().Int("body_len", len(body)).Msg("kinogo: no embed iframe in movie page (page template changed or auth gate?)")
		return kinogoEmbed{}, false
	}

	for _, iframeURL := range iframes {
		iframeBody, ok := k.fetchEmbedBody(ctx, iframeURL, moviePageURL)
		if !ok {
			log.Warn().Str("iframe", iframeURL).Msg("kinogo: embed iframe fetch failed")
			continue
		}

		if raw := strings.TrimSpace(submatch1(collapsSeasonsRe, iframeBody)); raw != "" {
			if serial, ok := collapsParseSeasons(raw); ok && len(serial) > 0 {
				log.Debug().Int("seasons", len(serial)).Msg("kinogo: parsed serial iframe")
				return kinogoEmbed{Serial: serial}, true
			}
		}

		loc := collapsMakePlayerRe.FindStringIndex(iframeBody)
		if len(loc) == 2 && loc[1] < len(iframeBody) {
			content := strings.TrimSpace(iframeBody[loc[1]:])
			if content != "" {
				return kinogoEmbed{Content: content}, true
			}
		}

		log.Warn().Str("iframe", iframeURL).Int("body_len", len(iframeBody)).Msg("kinogo: iframe parsed but no hls/dash/seasons found (player format changed?)")
	}
	return kinogoEmbed{}, false
}

// currentHost is the mirror that last served a page, falling back to the primary.
func (k *kinogoChecker) currentHost() string {
	if active, _ := k.active.Load().(string); active != "" {
		return active
	}
	return k.host
}

// splitHref classifies a stored href. Links that point at any kinogo mirror are
// reduced to a site-relative path so they keep working after a mirror dies
// (cached hrefs and `similar` links carry the absolute URL of whatever mirror
// was live when they were produced). Anything else is returned as an absolute
// URL to fetch as-is.
func (k *kinogoChecker) splitHref(href string) (sitePath, external string) {
	href = strings.TrimSpace(href)
	if idx := strings.Index(href, "#"); idx >= 0 {
		href = href[:idx]
	}
	if href == "" {
		return "", ""
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	if !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") {
		return "/" + strings.TrimLeft(href, "/"), ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", href
	}
	if strings.Contains(strings.ToLower(u.Hostname()), "kinogo") {
		path := u.EscapedPath()
		if path == "" {
			path = "/"
		}
		if u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
		return path, ""
	}
	return "", href
}

// pickIframe returns the first usable VenomPlayer-style embed, or "" when the
// page has none.
func (k *kinogoChecker) pickIframe(body string) string {
	if all := k.pickIframes(body); len(all) > 0 {
		return all[0]
	}
	return ""
}

// pickIframes collects every VenomPlayer-style /embed/ iframe (api.ortified.ws
// and friends), in page order. Trailer and external player iframes (walking-as,
// api.domem.ws/trailer, etc.) are skipped.
func (k *kinogoChecker) pickIframes(body string) []string {
	matches := kinogoIframeRe.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		raw := html.UnescapeString(m[1])
		raw = strings.ReplaceAll(raw, "&amp;", "&")
		if strings.HasPrefix(raw, "//") {
			raw = "https:" + raw
		}
		lower := strings.ToLower(raw)
		if strings.Contains(lower, "/trailer/") {
			continue
		}
		if strings.Contains(lower, "/embed/") && !slices.Contains(out, raw) {
			out = append(out, raw)
		}
	}
	return out
}

func (k *kinogoChecker) writeMovie(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	content string,
	title, originalTitle string,
	links *proxylink.Manager,
) {
	hls := collapsNormalizeURL(submatch1(collapsHLSRe, content))
	if hls == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	stream := streamProxyURLWithHeaders(req, hls, "kinogo", links, kinogoStreamHeaders())

	name := strings.TrimSpace(submatch1(collapsAudioFirstRe, content))
	if name == "" {
		name = "По умолчанию"
	}

	row := map[string]any{
		"method": "play",
		"url":    stream,
		"stream": stream,
		"name":   name,
		"title":  getsTVJoinName(title, originalTitle),
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

func (k *kinogoChecker) writeSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	href string,
	serial []collapsSeason,
	title, originalTitle string,
	year, season int,
	links *proxylink.Manager,
) {
	if len(serial) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	ordered := make([]collapsSeason, 0, len(serial))
	for _, s := range serial {
		if s.Season <= 0 {
			continue
		}
		ordered = append(ordered, s)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Season < ordered[j].Season })
	if len(ordered) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if season == -1 {
		rows := make([]map[string]any, 0, len(ordered))
		labels := make([]string, 0, len(ordered))
		for _, s := range ordered {
			name := strconv.Itoa(s.Season) + " сезон"
			rows = append(rows, map[string]any{
				"method": "link",
				"id":     s.Season,
				"url":    k.buildSeasonLink(host, rjson, title, originalTitle, year, href, s.Season),
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

	type episodeRow struct {
		data  map[string]any
		label string
		num   int
	}
	baseTitle := getsTVJoinName(title, originalTitle)
	epRows := make([]episodeRow, 0, len(episodes))

	for _, ep := range episodes {
		rawStream := collapsNormalizeURL(ep.HLS)
		if rawStream == "" {
			continue
		}
		stream := streamProxyURLWithHeaders(req, rawStream, "kinogo", links, kinogoStreamHeaders())
		epTitle := strings.TrimSpace(ep.Episode)
		if epTitle == "" {
			continue
		}
		epNum := collapsEpisodeNumber(epTitle)
		label := epTitle
		if !strings.Contains(strings.ToLower(label), "сер") {
			label += " серия"
		}
		row := map[string]any{
			"method": "play",
			"url":    stream,
			"stream": stream,
			"s":      season,
			"e":      epNum,
			"name":   label,
			"title":  baseTitle + " (" + label + ")",
		}
		if subs := collapsSubtitleRows(ep.CC); len(subs) > 0 {
			row["subtitles"] = subs
		}
		if voice := collapsJoinEpisodeVoices(ep.Audio.Names); voice != "" {
			row["voice_name"] = voice
		}
		epRows = append(epRows, episodeRow{data: row, label: label, num: epNum})
	}
	sort.Slice(epRows, func(i, j int) bool {
		if epRows[i].num == epRows[j].num {
			return epRows[i].label < epRows[j].label
		}
		return epRows[i].num < epRows[j].num
	})
	if len(epRows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	data := make([]map[string]any, 0, len(epRows))
	labels := make([]string, 0, len(epRows))
	seasons := make([]int, 0, len(epRows))
	episodesNum := make([]int, 0, len(epRows))
	for _, row := range epRows {
		data = append(data, row.data)
		labels = append(labels, row.label)
		seasons = append(seasons, season)
		episodesNum = append(episodesNum, row.num)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "episode",
			"data": data,
		})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodesNum[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (k *kinogoChecker) buildSeasonLink(host string, rjson bool, title, originalTitle string, year int, href string, season int) string {
	return fmt.Sprintf("%s/lite/kinogo?rjson=%s&title=%s&original_title=%s&year=%d&href=%s&s=%d",
		host, getsTVBool(rjson),
		url.QueryEscape(title), url.QueryEscape(originalTitle),
		year, url.QueryEscape(href), season,
	)
}

func (k *kinogoChecker) writeSimilar(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	items []kinogoSimilar,
	title, originalTitle string,
	year int,
) {
	if len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	host := hostFromRequest(req)
	data := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))
	for _, item := range items {
		link := fmt.Sprintf("%s/lite/kinogo?href=%s&rjson=%s&title=%s&original_title=%s&year=%d",
			host,
			url.QueryEscape(item.Href),
			getsTVBool(rjson),
			url.QueryEscape(title),
			url.QueryEscape(originalTitle),
			year,
		)
		data = append(data, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    item.Year,
			"details": "",
			"title":   item.Title,
			"img":     item.Img,
		})
		labels = append(labels, item.Title)
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "similar",
			"data": data,
		})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// kinogoStreamHeaders returns the headers used when proxying kinogo HLS streams.
// CDN (cdnr.interkh.com) is shared with Collaps and binds tokens loosely to UA,
// so we send a stable Chrome UA + the embed origin.
func kinogoStreamHeaders() map[string]string {
	return map[string]string{
		"User-Agent":      kinogoUA,
		"Origin":          "https://api.ortified.ws",
		"Referer":         "https://api.ortified.ws/",
		"Accept":          "*/*",
		"Accept-Language": "ru-RU,ru;q=0.9,en;q=0.8",
		"Cache-Control":   "no-cache",
		"Pragma":          "no-cache",
		"Sec-Fetch-Dest":  "empty",
		"Sec-Fetch-Mode":  "cors",
		"Sec-Fetch-Site":  "cross-site",
	}
}
