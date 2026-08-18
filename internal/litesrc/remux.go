package litesrc

import (
	"context"
	"fmt"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

var (
	remuxTitleRe     = regexp.MustCompile(`(?is)class="item__title[^"]*"><a href="https?://[^"]+">([^<]+)</a>`)
	remuxSearchRowRe = regexp.MustCompile(`(?is)class="item__title[^"]*"><a href="(https?://[^"]+)">([^<]+)</a>`)
	remuxQuoteRe     = regexp.MustCompile(`(?is)<div[^>]+class="quote"[^>]*>(.*?)</div>`)
	remuxCloudIDRe   = regexp.MustCompile(`(?is)href="https?://cloud\.mail\.ru/public/([^"]+)"`)
	// weblink_view is the streaming base ("https://clocloNN.cloud.mail.ru/weblink/view/");
	// weblink_get is the download base with a per-session token — using it (the old bug)
	// produced a nonsense URL that 404'd. weblink_view + <public id> 301-redirects to the
	// signed CDN file (206, Accept-Ranges) which plays and seeks.
	remuxWeblinkViewRe = regexp.MustCompile(`(?is)"weblink_view"\s*:\s*\{.*?"url"\s*:\s*"([^"]+)"`)
)

type remuxChecker struct {
	client *http.Client
	host   string
	cookie string
}

type remuxEmbedResult struct {
	content  string
	similars []remuxSimilar
}

type remuxSimilar struct {
	Title string
	Year  string
	Href  string
}

func NewRemuxChecker(cfg config.Config) *remuxChecker {
	host := strings.TrimSpace(cfg.Online.Remux.Host)
	if host == "" {
		host = "https://megaoblako.com"
	}
	host = strings.TrimRight(host, "/")

	return &remuxChecker{
		client: httpclient.NewForBalancer("remux", 10*time.Second),
		host:   host,
		cookie: strings.TrimSpace(cfg.Online.Remux.Cookie),
	}
}

func (r *remuxChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/"), "/")
		// Accept both the canonical route ("remux") and the config/admin spelling
		// ("iremux", matching [online.iremux] / with_search).
		switch raw {
		case "remux", "iremux":
			if parseBoolParam(req.URL.Query().Get("checksearch")) {
				show, quality := r.checkSearchQuality(req.Context(), req.URL.Query())
				if quality == "" {
					quality = pluginQualityBadgeGet("remux")
				}
				writeCheckSearchResponseNoRCH(w, show, quality)
				return
			}

			r.index(w, req)
			return

		case "remux/movie", "iremux/movie":
			r.movie(w, req, links)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "remux route is not implemented in local mode",
			"balanser": raw,
		})
	}
}

func (r *remuxChecker) index(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	href := strings.TrimSpace(q.Get("href"))
	similar := parseBoolParam(q.Get("similar"))

	if href == "" && strings.EqualFold(strings.TrimSpace(q.Get("source")), "remux") {
		href = strings.TrimSpace(q.Get("id"))
	}

	if (title == "" && originalTitle == "") || year == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	result, ok := r.embed(req.Context(), title, originalTitle, year, href)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if result.content == "" {
		if len(result.similars) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		if !similar && len(result.similars) == 1 {
			result, ok = r.embed(req.Context(), title, originalTitle, year, result.similars[0].Href)
			if !ok || result.content == "" {
				writeGetsTVEmpty(w, rjson)
				return
			}
		} else {
			r.writeSimilar(w, req, rjson, title, originalTitle, year, result.similars)
			return
		}
	}

	rows, labels := r.movieRows(req, title, originalTitle, result.content)
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": rows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (r *remuxChecker) writeSimilar(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle string, year int, similars []remuxSimilar) {
	if len(similars) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	rows := make([]map[string]any, 0, len(similars))
	labels := make([]string, 0, len(similars))
	for _, s := range similars {
		if s.Href == "" || strings.TrimSpace(s.Title) == "" {
			continue
		}
		link := host + "/lite/remux?rjson=" + getsTVBool(rjson) +
			"&title=" + url.QueryEscape(title) +
			"&original_title=" + url.QueryEscape(originalTitle) +
			"&year=" + strconv.Itoa(year) +
			"&href=" + url.QueryEscape(s.Href)
		rows = append(rows, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    s.Year,
			"details": "",
			"title":   s.Title,
		})
		labels = append(labels, s.Title)
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "similar",
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
}

func (r *remuxChecker) movie(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	linkID := strings.TrimSpace(q.Get("linkid"))
	quality := strings.TrimSpace(q.Get("quality"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	if linkID == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	weblink, ok := r.weblink(req.Context(), linkID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if quality == "" {
		quality = "480p"
	}

	displayTitle := getsTVJoinName(title, originalTitle)
	if strings.TrimSpace(displayTitle) == "" || displayTitle == "Untitled" {
		displayTitle = linkID
	}

	// Web clients can't fetch the cloud.mail.ru weblink directly (CORS + the
	// CDN's referer/geo checks), so route it through the stream proxy: the
	// server fetches and streams it. apk clients (?rchtype=apk) keep the raw
	// URL and play the CDN themselves. Mirrors lampac-nextgen's iRemux fix
	// (stream_access "apk,cors,web" → "apk,cors" + rchstreamproxy="web").
	// Mirror lampac-nextgen's iRemux Controller: HostStreamProxy(weblink), no
	// transcoding. Web clients (rchstreamproxy) get the weblink wrapped in our
	// stream proxy (the CDN 301-redirects and needs referer-less server fetch);
	// apk/native (?rchtype=apk) get the raw URL and let their player decode the
	// .mkv/.avi container directly.
	playURL := weblink
	if links != nil && !rchClientStreamCapable(req) {
		headers := map[string]string{"Referer": "https://cloud.mail.ru/"}
		if enc := links.EncryptURIWithHeaders(weblink, clientIP(req), "remux", headers); enc != "" {
			playURL = streamHostFromRequest(req) + "/proxy/" + enc
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"title":  displayTitle,
		"method": "play",
		"url":    playURL,
		"quality": map[string]string{
			quality: playURL,
		},
	})
}

// remuxQualityBadge reads the release quality out of a search-result title.
// megaoblako bakes it into the name — "(2024/4K/WEB-DL/WEB-DLRip)",
// "(2021) 4K HDR BD-Remux + Dolby Vision", "(2018/BD-Remux/BDRip/HDRip/3D)" —
// so a title can list SEVERAL sources at once; the best one wins.
// The static badge for this source was "SD", which is wildly wrong for remuxes.
func remuxQualityBadge(name string) string {
	up := strings.ToUpper(name)
	best := ""
	rank := map[string]int{"4K": 5, "2K": 4, "FHD": 3, "HD": 2, "SD": 1}
	consider := func(badge string) {
		if rank[badge] > rank[best] {
			best = badge
		}
	}
	switch {
	case strings.Contains(up, "2160"), strings.Contains(up, "4K"), strings.Contains(up, "ULTRAHD"),
		strings.Contains(up, "ULTRA HD"), strings.Contains(up, "UHD"):
		consider("4K")
	}
	if strings.Contains(up, "1440") || strings.Contains(up, "QHD") {
		consider("2K")
	}
	// Remux/BDRip/WEB-DL are 1080p-class releases.
	if strings.Contains(up, "REMUX") || strings.Contains(up, "BDRIP") || strings.Contains(up, "BLURAY") ||
		strings.Contains(up, "BLU-RAY") || strings.Contains(up, "WEB-DL") || strings.Contains(up, "WEBDL") ||
		strings.Contains(up, "1080") {
		consider("FHD")
	}
	if strings.Contains(up, "HDRIP") || strings.Contains(up, "WEBRIP") || strings.Contains(up, "720") {
		consider("HD")
	}
	if strings.Contains(up, "DVDRIP") || strings.Contains(up, "TVRIP") || strings.Contains(up, "CAMRIP") ||
		strings.Contains(up, "480") {
		consider("SD")
	}
	return best
}

func (r *remuxChecker) checkSearch(ctx context.Context, q url.Values) bool {
	show, _ := r.checkSearchQuality(ctx, q)
	return show
}

// checkSearchQuality reports availability plus the badge parsed from the matched
// card's title — the search page checkSearch already fetches, so it is free.
func (r *remuxChecker) checkSearchQuality(ctx context.Context, q url.Values) (bool, string) {
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	if year == 0 || (title == "" && originalTitle == "") {
		return false, ""
	}

	searches := make([]string, 0, 2)
	if title != "" {
		searches = append(searches, title)
	}
	if originalTitle != "" && normalizeSearchTitle(originalTitle) != normalizeSearchTitle(title) {
		searches = append(searches, originalTitle)
	}

	wantTitle := normalizeSearchTitle(title)
	wantOriginal := normalizeSearchTitle(originalTitle)
	found := false
	bestBadge := ""
	for _, query := range searches {
		body, ok := r.fetchSearch(ctx, query)
		if !ok {
			continue
		}
		if !strings.Contains(strings.ToLower(body), "поиск") {
			continue
		}

		for _, item := range remuxTitleRe.FindAllStringSubmatch(body, -1) {
			if len(item) < 2 {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(item[1]))
			if name == "" {
				continue
			}
			if strings.Contains(name, "сезон") || strings.Contains(name, "серия") || strings.Contains(name, "серии") {
				continue
			}
			if !strings.Contains(name, "("+strconv.Itoa(year)+"/") {
				continue
			}

			norm := normalizeSearchTitle(name)
			if (wantTitle != "" && strings.Contains(norm, wantTitle)) ||
				(wantOriginal != "" && strings.Contains(norm, wantOriginal)) {
				// Several cards exist per title (BDRip, 4K WEB-DL, remux…) —
				// keep scanning and report the best quality among the matches.
				if badge := remuxQualityBadge(item[1]); qualityBadgeRank(badge) > qualityBadgeRank(bestBadge) {
					bestBadge = badge
				}
				found = true
			}
		}
	}
	return found, bestBadge
}

func (r *remuxChecker) embed(ctx context.Context, title, originalTitle string, year int, href string) (remuxEmbedResult, bool) {
	out := remuxEmbedResult{}

	if href == "" {
		query := strings.TrimSpace(title)
		if query == "" {
			query = strings.TrimSpace(originalTitle)
		}
		if query == "" {
			return out, false
		}

		search, ok := r.fetchSearch(ctx, query)
		if !ok {
			return out, false
		}
		reqOK := strings.Contains(strings.ToLower(search), "поиск по сайту")
		out.similars = r.findSimilars(search, title, originalTitle, year)
		if len(out.similars) == 0 {
			if reqOK {
				return out, true
			}
			return out, false
		}
		if len(out.similars) > 1 {
			return out, true
		}
		href = out.similars[0].Href
	}

	news, ok := r.fetchURL(ctx, href)
	if !ok {
		return out, false
	}
	if !strings.Contains(news, "cloud.mail.ru/public/") {
		return out, false
	}
	out.content = news
	return out, true
}

func (r *remuxChecker) findSimilars(search, title, originalTitle string, year int) []remuxSimilar {
	rows := remuxSearchRowRe.FindAllStringSubmatch(search, -1)
	if len(rows) == 0 {
		return nil
	}

	wantTitle := normalizeSearchTitle(title)
	wantOriginal := normalizeSearchTitle(originalTitle)
	out := make([]remuxSimilar, 0, len(rows))

	for _, row := range rows {
		if len(row) < 3 {
			continue
		}
		href := strings.TrimSpace(row[1])
		name := strings.ToLower(strings.TrimSpace(row[2]))
		if href == "" || name == "" {
			continue
		}
		if strings.Contains(name, "сезон") || strings.Contains(name, "серии") || strings.Contains(name, "серия") {
			continue
		}

		norm := normalizeSearchTitle(name)
		match := false
		if wantTitle != "" && strings.Contains(norm, wantTitle) {
			match = true
		}
		if !match && wantOriginal != "" && strings.Contains(norm, wantOriginal) {
			match = true
		}
		if !match {
			continue
		}
		if !strings.Contains(name, "("+strconv.Itoa(year)+"/") {
			continue
		}

		out = append(out, remuxSimilar{
			Title: strings.TrimSpace(row[2]),
			Year:  strconv.Itoa(year),
			Href:  href,
		})
	}
	return out
}

func (r *remuxChecker) movieRows(req *http.Request, title, originalTitle, content string) ([]map[string]any, []string) {
	host := hostFromRequest(req)
	blocks := remuxQuoteRe.FindAllStringSubmatch(content, -1)
	if len(blocks) == 0 {
		return nil, nil
	}

	rows := make([]map[string]any, 0, len(blocks))
	labels := make([]string, 0, len(blocks))
	baseTitle := getsTVJoinName(title, originalTitle)
	for _, block := range blocks {
		if len(block) < 2 {
			continue
		}
		linkID := strings.TrimSpace(submatch1(remuxCloudIDRe, block[1]))
		if linkID == "" {
			continue
		}

		quality := "480p"
		for _, q := range []string{"2160p", "1080p", "720p", "480p"} {
			marker := q
			if q == "480p" {
				marker = "1400"
			}
			if strings.Contains(block[1], marker) {
				quality = q
				break
			}
		}

		stream := host + "/lite/remux/movie?linkid=" + url.QueryEscape(linkID) +
			"&quality=" + url.QueryEscape(quality) +
			"&title=" + url.QueryEscape(title) +
			"&original_title=" + url.QueryEscape(originalTitle)

		rows = append(rows, map[string]any{
			"method": "call",
			"url":    stream,
			"stream": stream,
			"name":   quality,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, quality),
		})
		labels = append(labels, quality)
	}

	slices.Reverse(rows)
	slices.Reverse(labels)
	return rows, labels
}

func (r *remuxChecker) weblink(ctx context.Context, linkID string) (string, bool) {
	linkID = strings.TrimSpace(linkID)
	if linkID == "" {
		return "", false
	}

	publicID := remuxPublicID(linkID)
	if publicID == "" {
		return "", false
	}

	html, ok := r.fetchURL(ctx, "https://cloud.mail.ru/public/"+publicID)
	if !ok {
		return "", false
	}
	base := strings.TrimSpace(submatch1(remuxWeblinkViewRe, html))
	if base == "" {
		return "", false
	}
	base = strings.ReplaceAll(base, `\/`, `/`) // JSON-escaped slashes
	base = strings.TrimRight(base, "/")
	if base == "" {
		return "", false
	}
	// base is the weblink_view endpoint; append the public id to get the
	// streamable file URL (redirects to the signed datacloudmail.ru CDN URL).
	return base + "/" + publicID, true
}

// remuxPublicID turns a stored linkid into the path cloud.mail.ru expects.
//
// The id looks like "AqWs/7vzft5tmo?Venom.2018.BDRip.1080p.mkv": the slash is
// PART OF the public id and the tail after '?' is just the file name. The old
// code ran url.PathEscape over the whole string, which turned both into %2F/%3F
// and made cloud.mail.ru answer 404 — /lite/remux/movie then returned `{}` and
// nothing played. Escape per segment so the separators survive.
func remuxPublicID(linkID string) string {
	if i := strings.IndexByte(linkID, '?'); i >= 0 {
		linkID = linkID[:i]
	}
	linkID = strings.Trim(strings.TrimSpace(linkID), "/")
	if linkID == "" {
		return ""
	}
	parts := strings.Split(linkID, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func (r *remuxChecker) fetchSearch(ctx context.Context, query string) (string, bool) {
	u, err := url.Parse(r.host + "/index.php")
	if err != nil {
		return "", false
	}
	qs := url.Values{}
	qs.Set("do", "search")
	qs.Set("subaction", "search")
	qs.Set("from_page", "0")
	qs.Set("story", query)
	u.RawQuery = qs.Encode()
	return r.fetchURL(ctx, u.String())
}

func (r *remuxChecker) fetchURL(ctx context.Context, target string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	if r.cookie != "" {
		req.Header.Set("Cookie", r.cookie)
	}
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := r.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}
