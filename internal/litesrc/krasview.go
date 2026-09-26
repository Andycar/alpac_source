package litesrc

import (
	"encoding/base64"
	stdjson "encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/text/encoding/charmap"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
)

// krasview.go — нативный порт JS-модуля krasview (сеть зеркал
// hlamer.ru/krasview.ru/smartkino.ru/sersoap.ru/zseek.ru).
//
// Поиск по названию+году, страница видео содержит video_Init('BASE64-JSON') →
// DASH/HLS на media*.krasview.ru (multi-audio внутри потока). CDN режет hot-link:
// стрим проксируется с Referer.
//
// ★Сайты отдают cp1251 — здесь тело ДЕКОДИРУЕТСЯ по-настоящему (charmap), так
// что матчинг работает и по русскому названию (JS-модуль был вынужден жить на
// ASCII-полях). ★Зеркала режут не-RU egress (боевой сервер напрямую получает
// connection reset) — на таких машинах обязателен socks_proxy (RU SOCKS5);
// проверено: через прод-sidecar 127.0.0.1:40002 отвечает 200.

type krasviewChecker struct {
	client        *http.Client
	searchHost    string
	movieHost     string
	serialHost    string
	streamReferer string
	preferHLS     bool
	cacheTTL      time.Duration

	mu    sync.Mutex
	cache map[string]krasviewCacheEntry
}

type krasviewCacheEntry struct {
	text string
	exp  time.Time
}

func NewKrasviewChecker(cfg config.Config) *krasviewChecker {
	kc := cfg.Online.Krasview
	search := strings.TrimRight(strings.TrimSpace(kc.SearchHost), "/")
	if search == "" {
		search = "https://hlamer.ru"
	}
	movie := strings.TrimRight(strings.TrimSpace(kc.MovieHost), "/")
	if movie == "" {
		movie = "https://smartkino.ru"
	}
	serial := strings.TrimRight(strings.TrimSpace(kc.SerialHost), "/")
	if serial == "" {
		serial = "https://sersoap.ru"
	}
	referer := strings.TrimSpace(kc.StreamReferer)
	if referer == "" {
		referer = movie + "/"
	}

	var client *http.Client
	if socks := strings.TrimSpace(kc.SocksProxy); socks != "" && !strings.EqualFold(socks, "none") && !strings.EqualFold(socks, "off") {
		httpclient.RegisterProxiedBalancer(socks, []string{"krasview"})
		client = httpclient.NewForBalancer("krasview", 20*time.Second)
		log.Info().Str("socks", socks).Msg("krasview: site via SOCKS5 (mirrors reject non-RU egress)")
	} else {
		client = httpclient.New(20 * time.Second)
	}

	return &krasviewChecker{
		client:        client,
		searchHost:    search,
		movieHost:     movie,
		serialHost:    serial,
		streamReferer: referer,
		preferHLS:     !kc.PreferDASH,
		cacheTTL:      30 * time.Minute,
		cache:         map[string]krasviewCacheEntry{},
	}
}

const krasviewUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// fetch возвращает страницу, ДЕКОДИРОВАННУЮ из cp1251 в UTF-8, с кэшем.
func (c *krasviewChecker) fetch(req *http.Request, target, referer string) string {
	c.mu.Lock()
	if e, ok := c.cache[target]; ok && time.Now().Before(e.exp) {
		c.mu.Unlock()
		return e.text
	}
	c.mu.Unlock()

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return ""
	}
	httpReq.Header.Set("User-Agent", krasviewUA)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Accept-Language", "ru,en;q=0.9")
	if referer != "" {
		httpReq.Header.Set("Referer", referer)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return ""
	}
	text := krasviewDecodeCP1251(raw, resp.Header.Get("Content-Type"))
	if text != "" {
		c.mu.Lock()
		c.cache[target] = krasviewCacheEntry{text: text, exp: time.Now().Add(c.cacheTTL)}
		if len(c.cache) > 2048 {
			now := time.Now()
			for k, e := range c.cache {
				if now.After(e.exp) {
					delete(c.cache, k)
				}
			}
		}
		c.mu.Unlock()
	}
	return text
}

func krasviewDecodeCP1251(raw []byte, contentType string) string {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "1251") || !strings.Contains(ct, "utf") {
		if dec, err := charmap.Windows1251.NewDecoder().Bytes(raw); err == nil {
			return string(dec)
		}
	}
	return string(raw)
}

// ---------------------------------------------------------------------------
// Search + matching
// ---------------------------------------------------------------------------

type krasviewHit struct {
	url  string
	host string
	kind string // movie | series
	slug string
	ru   string
	en   string
	year int
}

var (
	krasviewSearchRe = regexp.MustCompile(`<a[^>]+href='(https?://[^']+/(?:movie|series)/[^']+)'\s+title='([^']*)'`)
	krasviewHostRe   = regexp.MustCompile(`https?://([^/]+)`)
	krasviewPathRe   = regexp.MustCompile(`/(movie|series)/([^/?#]+)`)
	krasviewYearRe   = regexp.MustCompile(`(?:^|\s|\()((?:18|19|20|21)\d{2})(?:\)|\s|$)`)
	krasviewMirrorRe = regexp.MustCompile(`(smartkino|sersoap|zseek|krasview|hlamer)\.ru$`)
)

func (c *krasviewChecker) searchOnce(req *http.Request, kind, query string) []krasviewHit {
	target := c.searchHost + "/" + kind + "?mode=search&ajax&query=" + url.QueryEscape(query)
	html := c.fetch(req, target, c.searchHost+"/")
	if html == "" {
		return nil
	}
	var out []krasviewHit
	for _, m := range krasviewSearchRe.FindAllStringSubmatch(html, -1) {
		href, title := m[1], m[2]
		h := krasviewHit{url: href}
		if hm := krasviewHostRe.FindStringSubmatch(href); hm != nil {
			h.host = hm[1]
		}
		if pm := krasviewPathRe.FindStringSubmatch(href); pm != nil {
			h.kind, h.slug = pm[1], pm[2]
		} else {
			h.kind = kind
		}
		if ym := krasviewYearRe.FindStringSubmatch(title); ym != nil {
			h.year, _ = strconv.Atoi(ym[1])
		}
		// title = «Русское название / English Title 2002» — режем на половины.
		parts := strings.SplitN(title, "/", 2)
		h.ru = strings.TrimSpace(krasviewYearRe.ReplaceAllString(parts[0], " "))
		if len(parts) > 1 {
			h.en = strings.TrimSpace(krasviewYearRe.ReplaceAllString(parts[1], " "))
		}
		out = append(out, h)
	}
	return out
}

func krasviewNorm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'а' && r <= 'я', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func krasviewPickBest(hits []krasviewHit, wantTitle, wantOrig string, wantYear, tol int) *krasviewHit {
	if len(hits) == 0 {
		return nil
	}
	nTitle, nOrig := krasviewNorm(wantTitle), krasviewNorm(wantOrig)
	var best *krasviewHit
	bestScore := -1
	for i := range hits {
		h := &hits[i]
		score := 0
		nRu, nEn := krasviewNorm(h.ru), krasviewNorm(h.en)
		switch {
		case nOrig != "" && nEn == nOrig:
			score += 100
		case nTitle != "" && nRu == nTitle:
			score += 100
		case nOrig != "" && nEn != "" && strings.Contains(nEn, nOrig):
			score += 50
		case nTitle != "" && nRu != "" && strings.Contains(nRu, nTitle):
			score += 40
		case nOrig != "" && len(nEn) > 3 && strings.Contains(nOrig, nEn):
			score += 30
		}
		if wantYear > 0 && h.year > 0 {
			diff := wantYear - h.year
			if diff < 0 {
				diff = -diff
			}
			switch {
			case diff == 0:
				score += 30
			case diff <= tol:
				score += 10
			default:
				score -= 20
			}
		}
		if score > bestScore {
			bestScore = score
			best = h
		}
	}
	if bestScore >= 30 {
		return best
	}
	return nil
}

func (c *krasviewChecker) findMatch(req *http.Request, title, orig string, year int, kind string) *krasviewHit {
	query := firstNonEmptyStr(orig, title)
	if query == "" {
		return nil
	}
	return krasviewPickBest(c.searchOnce(req, kind, query), title, orig, year, 1)
}

// ---------------------------------------------------------------------------
// Video page → stream config
// ---------------------------------------------------------------------------

var krasviewInitRe = regexp.MustCompile(`video_Init\('([A-Za-z0-9+/=]+)'`)

type krasviewVideoConf struct {
	URL       string         `json:"url"`
	Duration  float64        `json:"duration"`
	AudioInfo map[string]any `json:"audio_info"`
}

func (c *krasviewChecker) videoConfig(req *http.Request, host, href string) *krasviewVideoConf {
	html := c.fetch(req, host+href, host+"/")
	if html == "" {
		return nil
	}
	m := krasviewInitRe.FindStringSubmatch(html)
	if m == nil {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		return nil
	}
	var conf krasviewVideoConf
	if stdjson.Unmarshal(raw, &conf) != nil {
		return nil
	}
	return &conf
}

var krasviewMPDRe = regexp.MustCompile(`(?i)\.mpd(\?|$)`)

func (c *krasviewChecker) streamURL(conf *krasviewVideoConf) string {
	if conf == nil || conf.URL == "" {
		return ""
	}
	u := conf.URL
	if c.preferHLS {
		u = krasviewMPDRe.ReplaceAllString(u, ".m3u8$1")
	}
	return u
}

func (c *krasviewChecker) proxied(req *http.Request, links *proxylink.Manager, u string) string {
	if u == "" {
		return ""
	}
	return streamProxyURLWithHeaders(req, u, "krasview", links, map[string]string{
		"Referer":    c.streamReferer,
		"Origin":     strings.TrimRight(c.streamReferer, "/"),
		"User-Agent": krasviewUA,
	})
}

// ---------------------------------------------------------------------------
// Movie / serial page parsing
// ---------------------------------------------------------------------------

var (
	krasviewVideoLinkRe = regexp.MustCompile(`href='(/video/(\d+)-([^']+))'`)
	krasviewSlugJunkRe  = regexp.MustCompile(`(?i)(treiyler|tizer|obzor|review|reklam|trailer|teaser|preview|fragment|sopustvuyusch|making|behind)`)
	krasviewSlugFilmRe  = regexp.MustCompile(`(?i)(^|[._-])film\b|[._-]Film($|[._-])`)
	krasviewCategoryRe  = regexp.MustCompile(`<li[^>]+id='c-(\d+)'[^>]*>\s*<a[^>]+href='/(?:series|movie)/[^']+\?category=\d+`)
	// Слаги встречаются и с точками (2.sezon.8.seriya), и с подчёркиваниями
	// (1_sezon_4_seriya) — живые страницы 2026 используют вторые.
	krasviewSezonRe  = regexp.MustCompile(`(?i)(\d+)[._-]sezon`)
	krasviewSeriyaRe = regexp.MustCompile(`(?i)(\d+)[._-]seriya`)
)

type krasviewVideoRef struct {
	id   string
	href string
	slug string
	s, e int
}

func krasviewParseMovieVideos(html string) []krasviewVideoRef {
	var primary, fallback []krasviewVideoRef
	seen := map[string]bool{}
	for _, m := range krasviewVideoLinkRe.FindAllStringSubmatch(html, -1) {
		href, id, slug := m[1], m[2], m[3]
		if seen[id] {
			continue
		}
		seen[id] = true
		if krasviewSlugJunkRe.MatchString(slug) {
			continue
		}
		ref := krasviewVideoRef{id: id, href: href, slug: slug}
		if krasviewSlugFilmRe.MatchString(slug) {
			primary = append(primary, ref)
		} else {
			fallback = append(fallback, ref)
		}
	}
	if len(primary) > 0 {
		return primary
	}
	return fallback
}

func krasviewParseSeasonCategories(html string) []int {
	var out []int
	seen := map[int]bool{}
	for _, m := range krasviewCategoryRe.FindAllStringSubmatch(html, -1) {
		id, _ := strconv.Atoi(m[1])
		if id == 0 || seen[id] {
			continue // category=0 — «Без категории» (трейлеры/прочее)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func krasviewParseSeriesEpisodes(html string) []krasviewVideoRef {
	var out []krasviewVideoRef
	seen := map[string]bool{}
	for _, m := range krasviewVideoLinkRe.FindAllStringSubmatch(html, -1) {
		href, id, slug := m[1], m[2], m[3]
		if seen[id] {
			continue
		}
		seen[id] = true
		if krasviewSlugJunkRe.MatchString(slug) {
			continue
		}
		sm := krasviewSezonRe.FindStringSubmatch(slug)
		em := krasviewSeriyaRe.FindStringSubmatch(slug)
		if sm == nil || em == nil {
			continue
		}
		s, _ := strconv.Atoi(sm[1])
		e, _ := strconv.Atoi(em[1])
		out = append(out, krasviewVideoRef{id: id, href: href, slug: slug, s: s, e: e})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].s != out[j].s {
			return out[i].s < out[j].s
		}
		return out[i].e < out[j].e
	})
	return out
}

func (c *krasviewChecker) fetchSeasonEpisodes(req *http.Request, host, slug string, catID int) []krasviewVideoRef {
	var all []krasviewVideoRef
	seen := map[string]bool{}
	for page := 1; page <= 10; page++ {
		target := host + "/series/" + slug + "/?category=" + strconv.Itoa(catID)
		if page > 1 {
			target += "&page=" + strconv.Itoa(page)
		}
		html := c.fetch(req, target, host+"/series/"+slug)
		if html == "" {
			break
		}
		part := krasviewParseSeriesEpisodes(html)
		if len(part) == 0 {
			break
		}
		added := 0
		for _, p := range part {
			if !seen[p.id] {
				seen[p.id] = true
				all = append(all, p)
				added++
			}
		}
		if added == 0 {
			break
		}
		nextRe := regexp.MustCompile(`href='[^']*\?(?:category=\d+&)?page=` + strconv.Itoa(page+1) + `'`)
		if !nextRe.MatchString(html) {
			break
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].s != all[j].s {
			return all[i].s < all[j].s
		}
		return all[i].e < all[j].e
	})
	return all
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (c *krasviewChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		rjson := parseBoolParam(q.Get("rjson"))
		title := strings.TrimSpace(q.Get("title"))
		orig := strings.TrimSpace(q.Get("original_title"))
		year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
		serial := strings.TrimSpace(q.Get("serial")) == "1"
		s, sSet := getsTVQueryInt(q.Get("s"))
		if !sSet {
			s = -1
		}

		kind := "movie"
		if serial {
			kind = "series"
		}
		match := c.findMatch(req, title, orig, year, kind)
		if match == nil {
			other := "series"
			if kind == "series" {
				other = "movie"
			}
			if match = c.findMatch(req, title, orig, year, other); match != nil {
				kind = match.kind
			}
		}

		if parseBoolParam(q.Get("checksearch")) {
			writeCheckSearchResponse(w, match != nil, pluginQualityBadgeGet("krasview"))
			return
		}
		if match == nil {
			writeGetsTVEmpty(w, rjson)
			return
		}

		host := c.movieHost
		if kind == "series" {
			host = c.serialHost
		}
		if match.host != "" && krasviewMirrorRe.MatchString(match.host) {
			host = "https://" + match.host
		}

		pageHTML := c.fetch(req, host+"/"+match.kind+"/"+match.slug, host+"/")
		if pageHTML == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}
		baseTitle := getsTVJoinName(title, orig)

		if kind != "series" {
			c.writeMovie(w, req, rjson, baseTitle, host, krasviewParseMovieVideos(pageHTML), links)
			return
		}

		seasons := krasviewParseSeasonCategories(pageHTML)
		if len(seasons) == 0 {
			eps := krasviewParseSeriesEpisodes(pageHTML)
			if len(eps) == 0 {
				c.writeMovie(w, req, rjson, baseTitle, host, krasviewParseMovieVideos(pageHTML), links)
				return
			}
			c.writeEpisodes(w, req, rjson, baseTitle, host, eps, eps[0].s, links)
			return
		}
		if s == -1 {
			if len(seasons) == 1 {
				eps := c.fetchSeasonEpisodes(req, host, match.slug, seasons[0])
				if len(eps) == 0 {
					writeGetsTVEmpty(w, rjson)
					return
				}
				c.writeEpisodes(w, req, rjson, baseTitle, host, eps, eps[0].s, links)
				return
			}
			c.writeSeasonList(w, req, rjson, title, orig, len(seasons))
			return
		}
		idx := s - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(seasons) {
			idx = len(seasons) - 1
		}
		eps := c.fetchSeasonEpisodes(req, host, match.slug, seasons[idx])
		if len(eps) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		realS := eps[0].s
		if realS == 0 {
			realS = s
		}
		c.writeEpisodes(w, req, rjson, baseTitle, host, eps, realS, links)
	}
}

func (c *krasviewChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, baseTitle, host string, vids []krasviewVideoRef, links *proxylink.Manager) {
	for _, v := range vids {
		conf := c.videoConfig(req, host, v.href)
		su := c.streamURL(conf)
		if su == "" {
			continue
		}
		sup := c.proxied(req, links, su)
		label := baseTitle
		if n := len(conf.AudioInfo); n > 1 {
			label += " [озвучек: " + strconv.Itoa(n) + "]"
		}
		row := map[string]any{
			"method": "play",
			"url":    sup,
			"stream": sup,
			"name":   baseTitle,
			"title":  label,
		}
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": []map[string]any{row}})
			return
		}
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		getsTVAppendMovieHTML(&sb, row, baseTitle, true, 0, 0)
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}
	writeGetsTVEmpty(w, rjson)
}

func (c *krasviewChecker) writeSeasonList(w http.ResponseWriter, req *http.Request, rjson bool, title, orig string, count int) {
	host := hostFromRequest(req)
	q := req.URL.Query()
	rows := make([]map[string]any, 0, count)
	labels := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		params := url.Values{}
		if rjson {
			params.Set("rjson", "true")
		}
		params.Set("title", title)
		params.Set("original_title", orig)
		if y := strings.TrimSpace(q.Get("year")); y != "" {
			params.Set("year", y)
		}
		params.Set("serial", "1")
		params.Set("s", strconv.Itoa(i))
		name := "Сезон " + strconv.Itoa(i)
		rows = append(rows, map[string]any{
			"method": "link",
			"id":     i,
			"s":      i,
			"name":   name,
			"url":    host + "/lite/krasview?" + params.Encode(),
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
}

func (c *krasviewChecker) writeEpisodes(w http.ResponseWriter, req *http.Request, rjson bool, baseTitle, host string, eps []krasviewVideoRef, sNum int, links *proxylink.Manager) {
	rows := make([]map[string]any, 0, len(eps))
	labels := make([]string, 0, len(eps))
	epNums := make([]int, 0, len(eps))
	for _, ep := range eps {
		if ep.s != sNum {
			continue
		}
		conf := c.videoConfig(req, host, ep.href)
		su := c.streamURL(conf)
		if su == "" {
			continue
		}
		sup := c.proxied(req, links, su)
		label := "Серия " + strconv.Itoa(ep.e)
		titleFull := baseTitle + " (S" + strconv.Itoa(ep.s) + "E" + strconv.Itoa(ep.e) + ")"
		if n := len(conf.AudioInfo); n > 1 {
			titleFull += " [" + strconv.Itoa(n) + " озвучек]"
		}
		rows = append(rows, map[string]any{
			"method": "play",
			"url":    sup,
			"stream": sup,
			"s":      ep.s,
			"e":      ep.e,
			"name":   label,
			"title":  titleFull,
		})
		labels = append(labels, label)
		epNums = append(epNums, ep.e)
	}
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": rows})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, sNum, epNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}
