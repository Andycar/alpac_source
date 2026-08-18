package litesrc

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// UaKino (uakino.cx) — Ukrainian DLE catalogue.
//
// Two things shape this implementation, both verified against production on
// 2026-08-09:
//
//   - The site sits behind a Cloudflare JS challenge, so every page goes through
//     FlareSolverr (~4s, "Challenge not detected" + 200). Playwright, which the
//     upstream .NET module requires, is not needed.
//   - The player it embeds is api.ortified.ws — the SAME VenomPlayer backend
//     kinogo uses (identical embed ids: Venom is movie/2705 on both). ortified
//     answers 422 to this server's datacenter IP, so the embed leg reuses the
//     kinogo SOCKS-failover client. Without a working embed proxy this source
//     finds titles but cannot play them.
//
// Upstream also ships Tortuga and HdvbUA modules. They are NOT sources: their
// ModInit has no IModuleOnline, they only resolve an embed passed as `uri`.
// Here they are internal resolvers reached from the rows we emit, not entries
// in the balancer list.
const uakinoDefaultHost = "https://uakino.cx"

var (
	// uakinoSearchRowRe matches a DLE search hit: the anchor carries both the
	// page URL and the title text.
	uakinoSearchRowRe = regexp.MustCompile(`(?is)<a[^>]+href="(https?://[^"]*uakino[^"]*?/(\d+)-[^"]+\.html)"[^>]*>([^<]{2,120})</a>`)
	// uakinoYearRe pulls the release year out of a card/page.
	uakinoYearRe = regexp.MustCompile(`(?is)(?:Рік виходу|Год выхода|Рік)\s*:?\s*</?[^>]*>?\s*<a[^>]*>(\d{4})</a>`)
	// uakinoIframeRe collects player iframes (data-src, like kinogo's template).
	uakinoIframeRe = regexp.MustCompile(`(?is)<iframe[^>]*(?:data-src|src)="([^"]+)"`)
	// uakinoTitleSuffixRe strips the trailing "(2018)" / season qualifier.
	uakinoTitleSuffixRe = regexp.MustCompile(`(?i)\s*\((?:\d{4}|[^()]*(?:сезон|season|сери[ияйї])[^()]*)\)\s*$`)
)

type uakinoChecker struct {
	host string
	// embedClients resolve the player iframe. ortified bans datacenter IPs, so
	// this mirrors kinogo's list-with-failover instead of a single proxy.
	embedClients   []kinogoEmbedRoute
	lastEmbedRoute atomic.Int32
	client         *http.Client
	// solverTimeoutMS bounds each FlareSolverr page load.
	solverTimeoutMS int
}

type uakinoItem struct {
	Title string
	Year  string
	Href  string
}

func NewUaKinoChecker(cfg config.Config) *uakinoChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.UaKino.Host, "/"))
	if host == "" {
		host = uakinoDefaultHost
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	// Fall back to kinogo's embed proxies: it is the same ortified backend, so
	// an install that already configured one does not have to repeat it.
	embed := strings.TrimSpace(cfg.Online.UaKino.EmbedSocksProxy)
	if embed == "" {
		embed = strings.TrimSpace(cfg.Online.Kinogo.EmbedSocksProxy)
	}
	return &uakinoChecker{
		host:            host,
		embedClients:    kinogoEmbedClients(embed),
		client:          httpclient.NewForBalancer("uakino", 20*time.Second),
		solverTimeoutMS: 60000,
	}
}

func (u *uakinoChecker) Handle(_ config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/"), "/")

		// Internal embed resolvers — reached from our own rows, never listed as
		// sources (mirrors upstream, where they have no IModuleOnline).
		switch raw {
		case "tortuga", "hdvbua", "uakino/embed":
			u.embedRoute(w, req, links)
			return
		}

		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show, quality := u.checkSearchQuality(req)
			if quality == "" {
				quality = pluginQualityBadgeGet("uakino")
			}
			writeCheckSearchResponse(w, show, quality)
			return
		}
		u.index(w, req, links)
	}
}

// fetchPage loads a page through FlareSolverr (the site is CF-gated).
func (u *uakinoChecker) fetchPage(ctx context.Context, target string) (string, bool) {
	res, ok := httpclient.FlareSolverrFetch(ctx, "uakino", target, httpclient.FlareSolverrFetchOptions{
		Force:        true, // the source is unusable without the solver
		MaxTimeoutMS: u.solverTimeoutMS,
	})
	if !ok {
		log.Warn().Str("url", target).Msg("uakino: FlareSolverr unavailable — set [flaresolverr] and keep it running, the site is Cloudflare-gated")
		return "", false
	}
	body := res.Solution.Response
	if strings.TrimSpace(body) == "" {
		log.Warn().Str("url", target).Int("status", res.Solution.Status).Msg("uakino: solver returned an empty page")
		return "", false
	}
	if res.Solution.Status < 200 || res.Solution.Status >= 300 {
		log.Warn().Str("url", target).Int("status", res.Solution.Status).Msg("uakino: non-2xx page")
		return "", false
	}
	return body, true
}

func (u *uakinoChecker) search(ctx context.Context, query string) []uakinoItem {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	target := u.host + "/index.php?do=search&subaction=search&story=" + url.QueryEscape(q)
	body, ok := u.fetchPage(ctx, target)
	if !ok {
		return nil
	}
	return u.parseSearch(body)
}

// parseSearch extracts result rows. The DLE template repeats the same link for
// poster and title, so rows are de-duplicated by href.
func (u *uakinoChecker) parseSearch(body string) []uakinoItem {
	matches := uakinoSearchRowRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]uakinoItem, 0, len(matches))
	for _, m := range matches {
		href := strings.TrimSpace(m[1])
		name := strings.TrimSpace(html.UnescapeString(m[3]))
		if href == "" || name == "" {
			continue
		}
		if _, dup := seen[href]; dup {
			continue
		}
		seen[href] = struct{}{}
		year := ""
		if ym := regexp.MustCompile(`\((\d{4})\)`).FindStringSubmatch(name); len(ym) == 2 {
			year = ym[1]
		}
		out = append(out, uakinoItem{
			Title: strings.TrimSpace(uakinoTitleSuffixRe.ReplaceAllString(name, "")),
			Year:  year,
			Href:  href,
		})
	}
	return out
}

// pickItem chooses the entry matching the requested card. Same rules as kinogo:
// the title may carry a qualifier, and the year may drift by one.
func (u *uakinoChecker) pickItem(items []uakinoItem, titles []string, year int) *uakinoItem {
	wanted := make([]string, 0, len(titles))
	for _, t := range titles {
		if n := normalizeSearchTitle(kinogoCleanTitle(t)); n != "" {
			wanted = append(wanted, n)
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	best := -1
	bestGap := -1
	for i := range items {
		if !containsNormalized(wanted, items[i].Title) {
			continue
		}
		gap := kinogoYearGap(year, items[i].Year)
		if gap < 0 {
			continue
		}
		if bestGap < 0 || gap < bestGap {
			best, bestGap = i, gap
		}
	}
	if best < 0 {
		return nil
	}
	return &items[best]
}

func containsNormalized(wanted []string, candidate string) bool {
	norm := normalizeSearchTitle(candidate)
	if norm == "" {
		return false
	}
	for _, w := range wanted {
		if w == norm {
			return true
		}
	}
	return false
}

func (u *uakinoChecker) checkSearchQuality(req *http.Request) (bool, string) {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	original := strings.TrimSpace(q.Get("original_title"))
	year, _ := getsTVQueryInt(q.Get("year"))

	query := title
	if query == "" {
		query = original
	}
	if query == "" {
		return false, ""
	}
	items := u.search(req.Context(), query)
	if len(items) == 0 && original != "" && !strings.EqualFold(original, query) {
		items = u.search(req.Context(), original)
	}
	if len(items) == 0 {
		return false, ""
	}
	if u.pickItem(items, []string{title, original}, year) == nil {
		// Something is there, but not this card — do not claim availability.
		return false, ""
	}
	return true, ""
}

func (u *uakinoChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	original := strings.TrimSpace(q.Get("original_title"))
	year, _ := getsTVQueryInt(q.Get("year"))
	href := strings.TrimSpace(q.Get("href"))

	if href == "" {
		query := title
		if query == "" {
			query = original
		}
		items := u.search(req.Context(), query)
		if len(items) == 0 && original != "" && !strings.EqualFold(original, query) {
			items = u.search(req.Context(), original)
		}
		if len(items) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		if picked := u.pickItem(items, []string{title, original}, year); picked != nil {
			href = picked.Href
		} else {
			u.writeSimilar(w, req, rjson, items, title, original, year)
			return
		}
	}

	page, ok := u.fetchPage(req.Context(), href)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	embed, ok := u.resolveEmbed(req.Context(), page)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if len(embed.Serial) > 0 {
		s, sOK := getsTVQueryInt(q.Get("s"))
		if !sOK {
			s = -1
		}
		u.writeSerial(w, req, rjson, href, embed.Serial, title, original, year, s, links)
		return
	}
	u.writeMovie(w, req, rjson, embed.Content, title, original, links)
}

// resolveEmbed walks the player iframes on a film page. uakino embeds ortified
// (VenomPlayer), so the collaps parsers apply unchanged.
func (u *uakinoChecker) resolveEmbed(ctx context.Context, page string) (kinogoEmbed, bool) {
	for _, iframeURL := range u.pickIframes(page) {
		body, ok := u.fetchEmbedBody(ctx, iframeURL)
		if !ok {
			continue
		}
		if raw := strings.TrimSpace(submatch1(collapsSeasonsRe, body)); raw != "" {
			if serial, ok := collapsParseSeasons(raw); ok && len(serial) > 0 {
				return kinogoEmbed{Serial: serial}, true
			}
		}
		if loc := collapsMakePlayerRe.FindStringIndex(body); len(loc) == 2 && loc[1] < len(body) {
			if content := strings.TrimSpace(body[loc[1]:]); content != "" {
				return kinogoEmbed{Content: content}, true
			}
		}
	}
	log.Warn().Msg("uakino: no playable embed on the page (all iframes failed or the player changed)")
	return kinogoEmbed{}, false
}

// pickIframes returns player iframes, skipping trailers and YouTube.
func (u *uakinoChecker) pickIframes(page string) []string {
	matches := uakinoIframeRe.FindAllStringSubmatch(page, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		raw := strings.ReplaceAll(html.UnescapeString(m[1]), "&amp;", "&")
		if strings.HasPrefix(raw, "//") {
			raw = "https:" + raw
		}
		lower := strings.ToLower(raw)
		if strings.Contains(lower, "youtube.") || strings.Contains(lower, "/trailer/") {
			continue
		}
		if !strings.HasPrefix(lower, "http") {
			continue
		}
		if strings.Contains(lower, "/embed/") || strings.Contains(lower, "hdvbua") || strings.Contains(lower, "tortuga") {
			if !containsString(out, raw) {
				out = append(out, raw)
			}
		}
	}
	return out
}

// fetchEmbedBody fetches the embed directly first, then through the SOCKS
// routes — ortified answers 422 to this server's IP (see kinogo).
func (u *uakinoChecker) fetchEmbedBody(ctx context.Context, target string) (string, bool) {
	get := func(client *http.Client) (string, bool) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return "", false
		}
		req.Header.Set("User-Agent", kinogoUA)
		req.Header.Set("Referer", u.host+"/")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		resp, err := client.Do(req)
		if err != nil {
			return "", false
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Debug().Int("status", resp.StatusCode).Str("url", target).Msg("uakino: embed non-2xx")
			return "", false
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return "", false
		}
		return string(body), true
	}

	if body, ok := get(u.client); ok {
		return body, true
	}
	if len(u.embedClients) == 0 {
		return "", false
	}
	start := int(u.lastEmbedRoute.Load())
	if start < 0 || start >= len(u.embedClients) {
		start = 0
	}
	for i := 0; i < len(u.embedClients); i++ {
		idx := (start + i) % len(u.embedClients)
		if body, ok := get(u.embedClients[idx].client); ok {
			u.lastEmbedRoute.Store(int32(idx))
			return body, true
		}
	}
	log.Warn().Str("url", target).Msg("uakino: embed refused directly and through every proxy — add a fresh one to embed_socks_proxy")
	return "", false
}

// embedRoute serves /lite/tortuga and /lite/hdvbua: resolve one embed URL passed
// as `uri`. These are internal resolvers, not listed sources.
func (u *uakinoChecker) embedRoute(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	target := strings.TrimSpace(q.Get("uri"))
	if target == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if strings.HasPrefix(target, "//") {
		target = "https:" + target
	}
	body, ok := u.fetchEmbedBody(req.Context(), target)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if raw := strings.TrimSpace(submatch1(collapsSeasonsRe, body)); raw != "" {
		if serial, ok := collapsParseSeasons(raw); ok && len(serial) > 0 {
			s, sOK := getsTVQueryInt(q.Get("s"))
			if !sOK {
				s = -1
			}
			u.writeSerial(w, req, rjson, target, serial, q.Get("title"), q.Get("original_title"), 0, s, links)
			return
		}
	}
	if loc := collapsMakePlayerRe.FindStringIndex(body); len(loc) == 2 && loc[1] < len(body) {
		u.writeMovie(w, req, rjson, strings.TrimSpace(body[loc[1]:]), q.Get("title"), q.Get("original_title"), links)
		return
	}
	writeGetsTVEmpty(w, rjson)
}

func (u *uakinoChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, content, title, original string, links *proxylink.Manager) {
	hls := collapsNormalizeURL(submatch1(collapsHLSRe, content))
	if hls == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	stream := streamProxyURLWithHeaders(req, hls, "uakino", links, kinogoStreamHeaders())
	name := strings.TrimSpace(submatch1(collapsAudioFirstRe, content))
	if name == "" {
		name = "Українська"
	}
	row := map[string]any{
		"method": "play",
		"url":    stream,
		"stream": stream,
		"name":   name,
		"title":  getsTVJoinName(title, original),
	}
	if subs := collapsParseSubtitlesRaw(submatch1(collapsCCRe, content)); len(subs) > 0 {
		row["subtitles"] = subs
	}
	if voice := collapsAudioNames(content); voice != "" {
		row["voice_name"] = voice
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": []map[string]any{row}})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	getsTVAppendMovieHTML(&sb, row, name, true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (u *uakinoChecker) writeSerial(w http.ResponseWriter, req *http.Request, rjson bool, href string, serial []collapsSeason, title, original string, year, season int, links *proxylink.Manager) {
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

	host := hostFromRequest(req)
	if season == -1 {
		rows := make([]map[string]any, 0, len(ordered))
		labels := make([]string, 0, len(ordered))
		for _, s := range ordered {
			name := strconv.Itoa(s.Season) + " сезон"
			rows = append(rows, map[string]any{
				"method": "link",
				"id":     s.Season,
				"name":   name,
				"url": fmt.Sprintf("%s/lite/uakino?rjson=%s&title=%s&original_title=%s&year=%d&href=%s&s=%d",
					host, getsTVBool(rjson), url.QueryEscape(title), url.QueryEscape(original), year, url.QueryEscape(href), s.Season),
			})
			labels = append(labels, name)
		}
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": rows})
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

	baseTitle := getsTVJoinName(title, original)
	data := make([]map[string]any, 0, len(episodes))
	labels := make([]string, 0, len(episodes))
	nums := make([]int, 0, len(episodes))
	for _, ep := range episodes {
		stream := collapsNormalizeURL(ep.HLS)
		if stream == "" || strings.TrimSpace(ep.Episode) == "" {
			continue
		}
		proxied := streamProxyURLWithHeaders(req, stream, "uakino", links, kinogoStreamHeaders())
		label := strings.TrimSpace(ep.Episode)
		if !strings.Contains(strings.ToLower(label), "сер") {
			label += " серія"
		}
		num := collapsEpisodeNumber(label)
		row := map[string]any{
			"method": "play",
			"url":    proxied,
			"stream": proxied,
			"s":      season,
			"e":      num,
			"name":   label,
			"title":  baseTitle + " (" + label + ")",
		}
		if voice := collapsJoinEpisodeVoices(ep.Audio.Names); voice != "" {
			row["voice_name"] = voice
		}
		data = append(data, row)
		labels = append(labels, label)
		nums = append(nums, num)
	}
	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": data})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, season, nums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (u *uakinoChecker) writeSimilar(w http.ResponseWriter, req *http.Request, rjson bool, items []uakinoItem, title, original string, year int) {
	host := hostFromRequest(req)
	data := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))
	for _, item := range items {
		data = append(data, map[string]any{
			"method":  "link",
			"similar": true,
			"title":   item.Title,
			"year":    item.Year,
			"details": "",
			"url": fmt.Sprintf("%s/lite/uakino?rjson=%s&title=%s&original_title=%s&year=%d&href=%s",
				host, getsTVBool(rjson), url.QueryEscape(title), url.QueryEscape(original), year, url.QueryEscape(item.Href)),
		})
		labels = append(labels, item.Title)
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "similar", "data": data})
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
