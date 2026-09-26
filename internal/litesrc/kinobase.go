package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"html"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/browser"
	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

var (
	kinobaseItemRe  = regexp.MustCompile(`(?is)<li class="[^"]*item[^"]*">.*?</li>`)
	kinobaseTitleRe = regexp.MustCompile(`(?is)<div class="title"><[^>]+>([^<]+)`)
	kinobaseYearRe  = regexp.MustCompile(`(?is)<span class="year">([0-9]{4})`)
	// The card's year span carries the release quality after the year:
	// `<span class="year">2021, 4K</span>`. Kinobase publishes 4K for a good
	// part of its catalogue, so the static FHD badge understated it.
	kinobaseQualityRe      = regexp.MustCompile(`(?is)<span class="year">[0-9]{4}\s*,\s*([^<]+)</span>`)
	kinobaseLinkRe         = regexp.MustCompile(`(?is)href="/([^"]+)"`)
	kinobaseImgRe          = regexp.MustCompile(`(?is)<img src="/([^"]+)"`)
	kinobasePlayerJSFileRe = regexp.MustCompile(`(?is)id="playerjsfile">([^<]+)<`)
	kinobasePlaylistsRe    = regexp.MustCompile(`(?is)id="playlists">([^<]+)<`)
	kinobaseAlertTextRe    = regexp.MustCompile(`(?is)<div class="alert">\s*<h3>([^<]+)</h3>`)
	kinobaseVoiceRe        = regexp.MustCompile(`(?is)\{([^}]+)\}`)
	kinobaseSubtitleRe     = regexp.MustCompile(`(?is)\[([^\]]+)\]([^\t ]+)`)
	kinobaseEpisodeNumRe   = regexp.MustCompile(`^\s*([0-9]+)`)
	kinobaseSeasonNumRe    = regexp.MustCompile(`^\s*([0-9]+)`)
)

type KinobaseChecker struct {
	client   *http.Client
	host     string
	playerJS bool

	// Search cache. Residential SOCKS5 to kinobase.org is flaky (EOF,
	// connection reset, TLS handshake timeout); without a cache, every
	// /lite/events probe hits the proxy again for the same title and most
	// requests fail mid-flight when Lampa cancels them after ~5s.
	//
	// We cache both hits and explicit misses so unknown titles don't keep
	// retrying through the flaky upstream.
	searchCache sync.Map // key string -> *kinobaseSearchEntry

	// Singleflight: while one goroutine is fetching a search result for a
	// given title:year, other goroutines wait for it instead of stampeding
	// the proxy with duplicate requests.
	searchInflight sync.Map // key string -> chan struct{}

	// Concurrency limiter on outbound search requests. SOCKS5 handshakes
	// pile up when many parallel /lite/events probes hit a single residential
	// upstream; capping to a small pool keeps each handshake fast enough to
	// beat Lampa's ~5s checksearch timeout.
	searchSem chan struct{}
}

type kinobaseSearchEntry struct {
	items    []kinobaseSearchItem
	exact    string
	ok       bool
	cachedAt time.Time
}

const (
	kinobaseSearchTTLHit  = 30 * time.Minute
	kinobaseSearchTTLMiss = 5 * time.Minute
)

type kinobaseSearchItem struct {
	Title   string
	Year    string
	Link    string
	Img     string
	Quality string // canonical badge ("4K"/"FHD"/"HD"/"SD"), "" when unknown
}

type kinobaseEmbedResult struct {
	Content  string
	Serial   []kinobaseSeason
	IsEmpty  bool
	ErrorMsg string
}

type kinobaseSeason struct {
	ID       int64            `json:"id"`
	File     string           `json:"file"`
	Title    string           `json:"title"`
	Comment  string           `json:"comment"`
	Subtitle string           `json:"subtitle"`
	Folder   []kinobaseSeason `json:"folder"`
}

func NewKinobaseChecker(cfg config.Config) *KinobaseChecker {
	host := strings.TrimSpace(cfg.Online.Kinobase.Host)
	if host == "" {
		host = "https://kinobase.org"
	}
	host = strings.TrimRight(host, "/")

	return &KinobaseChecker{
		// 25s timeout: residential SOCKS5 hops can be slow, and the embed page
		// returns id="playerjsfile" inline in SSR HTML so most requests finish
		// fast — but the slow ones used to hit the old 12s ceiling and report
		// "context deadline exceeded" / EOF.
		client: httpclient.NewForBalancer("kinobase", 25*time.Second),
		host:   host,
		// Legacy defaults to playerjs=true unless explicitly disabled in config.
		playerJS:  cfg.Online.Kinobase.PlayerJS,
		searchSem: make(chan struct{}, 3),
	}
}

func (k *KinobaseChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if parseBoolParam(r.URL.Query().Get("checksearch")) {
			show, quality := k.checkSearchQuality(r.Context(), strings.TrimSpace(r.URL.Query().Get("title")), strings.TrimSpace(r.URL.Query().Get("original_title")), strings.TrimSpace(r.URL.Query().Get("year")))
			if quality == "" {
				quality = pluginQualityBadgeGet("kinobase")
			}
			writeCheckSearchResponseNoRCH(w, show, quality)
			return
		}

		k.index(w, r, links)
	}
}

func (k *KinobaseChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	season, seasonSet := getsTVQueryInt(q.Get("s"))
	if !seasonSet {
		season = -1
	}
	href := strings.TrimSpace(q.Get("href"))
	voice := strings.TrimSpace(q.Get("t"))
	similar := parseBoolParam(q.Get("similar"))

	log.Debug().Str("title", title).Str("origTitle", originalTitle).Int("year", year).Str("href", href).Msg("kinobase: index called")

	if href == "" && strings.EqualFold(strings.TrimSpace(q.Get("source")), "kinobase") {
		href = strings.TrimSpace(q.Get("id"))
	}
	if title == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if href == "" {
		items, exact, ok := k.Search(req.Context(), title, originalTitle, year)
		log.Debug().Int("items", len(items)).Str("exact", exact).Bool("ok", ok).Msg("kinobase: search result")
		if !ok || len(items) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		if similar || exact == "" {
			k.writeSimilar(w, req, rjson, title, year, items)
			return
		}
		href = exact
	}

	embed, ok := k.embed(req.Context(), href)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if embed.IsEmpty {
		if embed.ErrorMsg != "" {
			writeJSON(w, http.StatusOK, map[string]any{"error": embed.ErrorMsg})
			return
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	if embed.Content != "" {
		rows, labels := k.movieRows(req, embed.Content, title, links)
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
		return
	}

	if len(embed.Serial) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if k.playerJS {
		k.writeSerialPlayerJS(w, req, rjson, title, href, year, season, voice, embed.Serial, links)
		return
	}
	k.writeSerialLegacy(w, req, rjson, title, href, year, season, embed.Serial, links)
}

// kinobaseQualityBadge maps a kinobase release label to a canonical badge.
// Kinobase names the SOURCE ("4K", "BDRip", "TS"), not the height, so the
// generic normalizer only recognises "4K" — the rip names have to be mapped by
// what they actually are.
func kinobaseQualityBadge(raw string) string {
	label := strings.ToUpper(strings.TrimSpace(raw))
	if label == "" {
		return ""
	}
	switch {
	case strings.Contains(label, "4K"), strings.Contains(label, "2160"), strings.Contains(label, "UHD"):
		return "4K"
	case strings.Contains(label, "1440"), strings.Contains(label, "QHD"), label == "2K":
		return "2K"
	case strings.Contains(label, "1080"), strings.Contains(label, "FULLHD"), strings.Contains(label, "FHD"),
		strings.Contains(label, "BDRIP"), strings.Contains(label, "BLU-RAY"), strings.Contains(label, "BLURAY"),
		strings.Contains(label, "WEB-DL"), strings.Contains(label, "WEBDL"), strings.Contains(label, "WEBRIP"):
		return "FHD"
	case strings.Contains(label, "720"), strings.Contains(label, "HDTV"), strings.Contains(label, "HDRIP"),
		strings.Contains(label, "DVDRIP"):
		return "HD"
	case strings.Contains(label, "TS"), strings.Contains(label, "CAM"), strings.Contains(label, "TC"),
		strings.Contains(label, "SCR"):
		return "SD"
	}
	// Unknown label: let the shared normalizer have a go, else no badge (the
	// caller falls back to the configured default).
	return normalizeQualityBadge(label)
}

// checkSearchQuality returns availability plus the best quality kinobase
// advertises for the matched card, so the client badge reflects the real
// release instead of a hardcoded FHD.
func (k *KinobaseChecker) checkSearchQuality(ctx context.Context, title, originalTitle, year string) (bool, string) {
	if title == "" {
		return false, ""
	}
	items, exact, ok := k.Search(ctx, title, originalTitle, atoiOrZero(year))
	if !ok || len(items) == 0 {
		return false, ""
	}
	// Availability must mean «this card plays», not «the search returned rows».
	//
	// Reporting true on any hit lit the badge white in Lampa while opening the
	// source said «поиск не дал результатов»: kinobase's search page is flaky
	// (614 of 701 probes found no items at all), so a lucky probe cached a
	// positive for 10 minutes — positives live ten times longer than negatives
	// — and the click that followed hit an empty search. Requiring `exact`
	// matches what index() can actually resolve into a playable card.
	if exact == "" {
		return false, ""
	}

	wantTitle := normalizeSearchTitle(title)
	wantOrig := normalizeSearchTitle(originalTitle)
	best := ""
	rank := func(q string) int {
		switch q {
		case "4K":
			return 4
		case "FHD":
			return 3
		case "HD":
			return 2
		case "SD":
			return 1
		}
		return 0
	}
	for _, item := range items {
		norm := normalizeSearchTitle(item.Title)
		titleMatches := norm == wantTitle || (wantOrig != "" && norm == wantOrig)
		yearMatches := strings.TrimSpace(year) == "" || strings.TrimSpace(item.Year) == strings.TrimSpace(year)
		if titleMatches && yearMatches && rank(item.Quality) > rank(best) {
			best = item.Quality
		}
	}
	// Available: an exact card exists (checked above). `best` stays empty when
	// the matching row carried no quality badge — report availability without
	// inventing a quality for someone else's card.
	return true, best
}

func (k *KinobaseChecker) checkSearch(ctx context.Context, title, originalTitle, year string) bool {
	if title == "" {
		return false
	}
	items, _, ok := k.Search(ctx, title, originalTitle, atoiOrZero(year))
	if !ok || len(items) == 0 {
		return false
	}
	if strings.TrimSpace(year) == "" {
		return true
	}

	wantTitle := normalizeSearchTitle(title)
	wantOrig := normalizeSearchTitle(originalTitle)
	for _, item := range items {
		norm := normalizeSearchTitle(item.Title)
		if (norm == wantTitle || (wantOrig != "" && norm == wantOrig)) && strings.TrimSpace(item.Year) == strings.TrimSpace(year) {
			return true
		}
	}
	// Similar results still count as source availability.
	return true
}

func (k *KinobaseChecker) Search(ctx context.Context, title, originalTitle string, year int) ([]kinobaseSearchItem, string, bool) {
	// Cache key is title:year so requests with slightly different
	// original_title (Lampa sometimes sends EN, sometimes RU) share a cache
	// entry. We treat the proxy as fungible across users.
	cacheKey := strings.ToLower(strings.TrimSpace(title)) + "|" + strconv.Itoa(year)
	if items, exact, ok, hit := k.cachedSearchLookup(cacheKey, title); hit {
		return items, exact, ok
	}

	// Singleflight: dedupe parallel /lite/events probes for the same title.
	// While the first probe runs we don't issue duplicate proxy requests;
	// late waiters block on a channel and re-read the cache when it closes.
	waitCh, isLeader := k.singleflightAcquire(cacheKey)
	if !isLeader {
		select {
		case <-waitCh:
			if items, exact, ok, hit := k.cachedSearchLookup(cacheKey, title); hit {
				return items, exact, ok
			}
			return nil, "", false
		case <-ctx.Done():
			// Caller cancelled — but the leader keeps going and will fill
			// the cache for the next probe. We return empty for now.
			return nil, "", false
		}
	}
	defer k.singleflightRelease(cacheKey)

	// Decouple from the caller's context. Lampa cancels /lite/events probes
	// aggressively (~3-5s), but completing the search fully and caching the
	// result means the next probe (often seconds later) gets an instant hit.
	bgCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// Throttle outbound search requests so a burst of parallel probes
	// doesn't pile TLS handshakes onto a flaky residential SOCKS5.
	select {
	case k.searchSem <- struct{}{}:
		defer func() { <-k.searchSem }()
	case <-bgCtx.Done():
		return nil, "", false
	}

	searchURL := k.host + "/search?query=" + url.QueryEscape(title)
	// Referer is the bare host without trailing slash, matching nextgen
	// (httpHydra.Get(..., addheaders: HeadersModel.Init("referer", init.host))).
	page, ok := k.fetchPage(bgCtx, searchURL, k.host)
	if !ok {
		log.Debug().Str("url", searchURL).Msg("kinobase: fetchPage failed")
		// Do not cache network failures — the proxy may recover in seconds.
		// Caller context may also be done by now; return empty.
		return nil, "", false
	}
	log.Debug().Int("pageLen", len(page)).Str("query", title).Msg("kinobase: search page fetched")
	if !strings.Contains(page, `class="item"`) && !strings.Contains(page, `class="x1 item"`) {
		// Capture a snippet so we can diagnose template / endpoint changes
		// remotely. Strip whitespace runs so the log line stays compact.
		preview := page
		if len(preview) > 600 {
			preview = preview[:600]
		}
		preview = regexp.MustCompile(`\s+`).ReplaceAllString(preview, " ")
		log.Debug().Str("preview", preview).Msg("kinobase: no items on search page")
		// Cache the empty result so we don't keep hammering a flaky proxy
		// for an obscure title that kinobase doesn't have.
		k.searchCache.Store(cacheKey, &kinobaseSearchEntry{
			items: nil, exact: "", ok: true, cachedAt: time.Now(),
		})
		return nil, "", true
	}

	items := make([]kinobaseSearchItem, 0, 16)
	exact := ""
	want := normalizeSearchTitle(title)
	wantOrig := normalizeSearchTitle(originalTitle)
	for _, block := range kinobaseItemRe.FindAllString(page, -1) {
		if strings.Contains(block, ">Трейлер</span>") {
			continue
		}

		name := strings.TrimSpace(html.UnescapeString(submatch1(kinobaseTitleRe, block)))
		link := strings.TrimSpace(submatch1(kinobaseLinkRe, block))
		if name == "" || link == "" {
			continue
		}
		row := kinobaseSearchItem{
			Title:   name,
			Year:    strings.TrimSpace(submatch1(kinobaseYearRe, block)),
			Link:    link,
			Quality: kinobaseQualityBadge(submatch1(kinobaseQualityRe, block)),
		}
		if img := strings.TrimSpace(submatch1(kinobaseImgRe, block)); img != "" {
			row.Img = k.host + "/" + strings.TrimLeft(img, "/")
		}
		items = append(items, row)

		// Pass 1: exact match by title + year
		if exact == "" && normalizeSearchTitle(name) == want {
			if year <= 0 || atoiOrZero(row.Year) == year {
				exact = link
			}
		}
	}

	// Pass 2: exact match by original_title + year
	if exact == "" && wantOrig != "" && wantOrig != want {
		for _, item := range items {
			if normalizeSearchTitle(item.Title) == wantOrig {
				if year <= 0 || atoiOrZero(item.Year) == year {
					exact = item.Link
					break
				}
			}
		}
	}

	// Pass 3: fallback — first item with matching year
	if exact == "" && year > 0 {
		for _, item := range items {
			if atoiOrZero(item.Year) == year {
				exact = item.Link
				break
			}
		}
	}

	k.searchCache.Store(cacheKey, &kinobaseSearchEntry{
		items: items, exact: exact, ok: true, cachedAt: time.Now(),
	})
	return items, exact, true
}

// cachedSearchLookup returns a cached search entry if one is fresh, and
// reports hit=true even when the cached value is an empty miss — those are
// still authoritative within their TTL.
func (k *KinobaseChecker) cachedSearchLookup(cacheKey, title string) (items []kinobaseSearchItem, exact string, ok, hit bool) {
	cached, found := k.searchCache.Load(cacheKey)
	if !found {
		return nil, "", false, false
	}
	entry := cached.(*kinobaseSearchEntry)
	ttl := kinobaseSearchTTLMiss
	if len(entry.items) > 0 {
		ttl = kinobaseSearchTTLHit
	}
	if time.Since(entry.cachedAt) >= ttl {
		k.searchCache.Delete(cacheKey)
		return nil, "", false, false
	}
	log.Debug().Int("items", len(entry.items)).Bool("ok", entry.ok).Str("query", title).Msg("kinobase: search cache hit")
	return entry.items, entry.exact, entry.ok, true
}

// singleflightAcquire returns a wait-channel and isLeader=true when the
// caller is responsible for performing the fetch. Followers receive
// isLeader=false and the leader's wait-channel to block on.
func (k *KinobaseChecker) singleflightAcquire(cacheKey string) (<-chan struct{}, bool) {
	myCh := make(chan struct{})
	if actual, loaded := k.searchInflight.LoadOrStore(cacheKey, myCh); loaded {
		return actual.(chan struct{}), false
	}
	return myCh, true
}

// singleflightRelease wakes followers and clears the in-flight slot.
func (k *KinobaseChecker) singleflightRelease(cacheKey string) {
	if ch, ok := k.searchInflight.LoadAndDelete(cacheKey); ok {
		close(ch.(chan struct{}))
	}
}

func (k *KinobaseChecker) embed(ctx context.Context, href string) (kinobaseEmbedResult, bool) {
	var out kinobaseEmbedResult
	target := strings.TrimSpace(href)
	if target == "" {
		return out, false
	}
	if !strings.Contains(target, "://") {
		target = k.host + "/" + strings.TrimLeft(target, "/")
	}

	// Try headless browser first (handles obfuscated JS player init).
	if k.playerJS {
		if raw, err := k.embedViaBrowser(ctx, target); err == nil && raw != "" {
			return k.parsePlayerJSData(raw)
		} else if err != nil {
			log.Debug().Err(err).Str("url", target).Msg("kinobase: browser embed failed, falling back to HTTP")
		}
	}

	// Fallback: try FlareSolverr (when configured) then plain HTTP. The embed
	// page may need JS to populate id="playerjsfile", so a Chrome-rendered
	// version is preferable when available.
	page, ok := k.fetchEmbedPage(ctx, target, k.host+"/")
	if !ok {
		return out, false
	}

	if k.playerJS {
		raw := strings.TrimSpace(html.UnescapeString(submatch1(kinobasePlayerJSFileRe, page)))
		if raw == "" {
			if strings.Contains(page, `<div class="alert"`) {
				out.IsEmpty = true
				out.ErrorMsg = strings.TrimSpace(html.UnescapeString(submatch1(kinobaseAlertTextRe, page)))
				return out, true
			}
			return out, false
		}
		return k.parsePlayerJSData(raw)
	}

	raw := strings.TrimSpace(html.UnescapeString(submatch1(kinobasePlaylistsRe, page)))
	if raw != "" {
		serial, err := kinobaseParseSerial(raw)
		if err != nil || len(serial) == 0 {
			return out, false
		}
		out.Serial = serial
		return out, true
	}

	content := strings.TrimSpace(html.UnescapeString(kinobaseFindLastText(page, `id="videoplayer"`, `</div>`)))
	if content == "" {
		return out, false
	}
	out.Content = content
	return out, true
}

// parsePlayerJSData parses the raw player file string from PlayerJS.
func (k *KinobaseChecker) parsePlayerJSData(raw string) (kinobaseEmbedResult, bool) {
	var out kinobaseEmbedResult
	if strings.HasSuffix(raw, "]") {
		serial, err := kinobaseParseSerial(raw)
		if err != nil || len(serial) == 0 {
			return out, false
		}
		out.Serial = serial
		return out, true
	}
	out.Content = raw
	return out, true
}

// embedViaBrowser uses headless Chrome to extract player data from kinobase.
// Returns the raw file string from PlayerJS, or error.
//
// Dispatch:
//   - default (chromedp engine OR no override) → legacy Node.js +
//     puppeteer-core subprocess (Extract). Preserved verbatim.
//   - any other engine (rod / playwright) → native ExtractFacade,
//     which removes the Node dependency entirely.
//
// The Node path stays the proven default until the facade has been
// live-tested against kinobase. Once verified the legacy code goes.
func (k *KinobaseChecker) embedViaBrowser(ctx context.Context, filmURL string) (string, error) {
	proxyAddr := ""
	if socksAddr := httpclient.SocksAddrForBalancer("kinobase"); socksAddr != "" {
		proxyAddr = "socks5://" + socksAddr
	}
	if eng := browser.ForBalancer("kinobase"); eng != nil && eng.Name() != "chromedp" {
		return defaultKinobaseBrowser.ExtractFacade(ctx, filmURL, proxyAddr)
	}
	return defaultKinobaseBrowser.Extract(ctx, filmURL, proxyAddr)
}

func kinobaseParseSerial(raw string) ([]kinobaseSeason, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty serial payload")
	}
	var out []kinobaseSeason
	if err := stdjson.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (k *KinobaseChecker) movieRows(req *http.Request, content, title string, links *proxylink.Manager) ([]map[string]any, []string) {
	baseTitle := getsTVJoinName(title, "")
	rows := make([]map[string]any, 0, 8)
	labels := make([]string, 0, 8)

	if k.playerJS {
		if strings.Contains(content, "{") {
			for _, voice := range kinobaseUniqueVoices(content) {
				streamQuality := kinobaseExtractPlayerJSQualities(content, voice, k.host)
				if len(streamQuality) == 0 {
					continue
				}
				qualityMap := make(map[string]string, len(streamQuality))
				var firstURL string
				for i, sq := range streamQuality {
					u := streamProxyURL(req, toString(sq["url"]), "kinobase", links)
					streamQuality[i]["url"] = u
					qualityMap[toString(sq["quality"])] = u
					if firstURL == "" {
						firstURL = u
					}
				}
				name := voice
				row := map[string]any{
					"method":  "play",
					"url":     firstURL,
					"stream":  firstURL,
					"name":    name,
					"title":   fmt.Sprintf("%s (%s)", baseTitle, name),
					"quality": qualityMap,
				}
				rows = append(rows, row)
				labels = append(labels, name)
			}
			return rows, labels
		}

		streamQuality := kinobaseExtractPlayerJSQualities(content, "", k.host)
		if len(streamQuality) == 0 {
			return nil, nil
		}
		qualityMap := make(map[string]string, len(streamQuality))
		var firstURL string
		for i, sq := range streamQuality {
			u := streamProxyURL(req, toString(sq["url"]), "kinobase", links)
			streamQuality[i]["url"] = u
			qualityMap[toString(sq["quality"])] = u
			if firstURL == "" {
				firstURL = u
			}
		}
		row := map[string]any{
			"method":  "play",
			"url":     firstURL,
			"stream":  firstURL,
			"name":    "По умолчанию",
			"title":   fmt.Sprintf("%s (%s)", baseTitle, "По умолчанию"),
			"quality": qualityMap,
		}
		return []map[string]any{row}, []string{"По умолчанию"}
	}

	streamQuality := kinobaseExtractLegacyQualities(content, k.host)
	if len(streamQuality) == 0 {
		return nil, nil
	}
	qualityMap := make(map[string]string, len(streamQuality))
	var firstURL string
	for i, sq := range streamQuality {
		u := streamProxyURL(req, toString(sq["url"]), "kinobase", links)
		streamQuality[i]["url"] = u
		qualityMap[toString(sq["quality"])] = u
		if firstURL == "" {
			firstURL = u
		}
	}
	name := toString(streamQuality[0]["quality"])
	row := map[string]any{
		"method":  "play",
		"url":     firstURL,
		"stream":  firstURL,
		"name":    name,
		"title":   fmt.Sprintf("%s (%s)", baseTitle, name),
		"quality": qualityMap,
	}
	return []map[string]any{row}, []string{name}
}

func (k *KinobaseChecker) writeSerialPlayerJS(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	title string,
	href string,
	year int,
	season int,
	voice string,
	serial []kinobaseSeason,
	links *proxylink.Manager,
) {
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encHref := url.QueryEscape(href)

	var episodes []kinobaseSeason
	if len(serial) > 0 && len(serial[0].Folder) == 0 {
		if season == -1 {
			link := fmt.Sprintf("%s/lite/kinobase?rjson=%s&title=%s&year=%d&href=%s&s=1",
				host, getsTVBool(rjson), encTitle, year, encHref,
			)
			row := map[string]any{"method": "link", "id": 1, "url": link, "name": "1 сезон"}
			if rjson {
				writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": []map[string]any{row}})
				return
			}
			var sb strings.Builder
			sb.WriteString(`<div class="videos__line">`)
			getsTVAppendSeasonHTML(&sb, row, "1 сезон", true)
			sb.WriteString(`</div>`)
			writeHTML(w, http.StatusOK, sb.String())
			return
		}
		episodes = serial
	} else {
		if season == -1 {
			rows := make([]map[string]any, 0, len(serial))
			labels := make([]string, 0, len(serial))
			for _, item := range serial {
				sn, ok := kinobaseNumber(kinobaseSeasonNumRe, item.Title)
				if !ok {
					continue
				}
				link := fmt.Sprintf("%s/lite/kinobase?rjson=%s&title=%s&year=%d&href=%s&s=%d",
					host, getsTVBool(rjson), encTitle, year, encHref, sn,
				)
				rows = append(rows, map[string]any{
					"method": "link",
					"id":     sn,
					"url":    link,
					"name":   fmt.Sprintf("%d сезон", sn),
				})
				labels = append(labels, fmt.Sprintf("%d сезон", sn))
			}
			if len(rows) == 0 {
				writeGetsTVEmpty(w, rjson)
				return
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

		for i := range serial {
			if sn, ok := kinobaseNumber(kinobaseSeasonNumRe, serial[i].Title); ok && sn == season {
				episodes = serial[i].Folder
				break
			}
		}
	}

	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	voices := kinobaseUniqueVoices(episodes[0].File)
	if len(voices) == 0 {
		voices = []string{"По умолчанию"}
	}
	if voice == "" || !slicesContains(voices, voice) {
		voice = voices[0]
	}

	voiceRows := make([]map[string]any, 0, len(voices))
	for _, item := range voices {
		link := fmt.Sprintf("%s/lite/kinobase?rjson=%s&title=%s&year=%d&href=%s&s=%d&t=%s",
			host, getsTVBool(rjson), encTitle, year, encHref, season, url.QueryEscape(item),
		)
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   item,
			"active": item == voice,
			"url":    link,
		})
	}

	baseTitle := getsTVJoinName(title, "")
	type episodeRow struct {
		data  map[string]any
		label string
		num   int
	}
	rows := make([]episodeRow, 0, len(episodes))

	for _, episode := range episodes {
		file := strings.TrimSpace(episode.File)
		if file == "" {
			continue
		}
		if voice != "" && voice != "По умолчанию" && !strings.Contains(file, "{"+voice+"}") {
			continue
		}

		voiceForExtract := ""
		if voice != "По умолчанию" {
			voiceForExtract = voice
		}
		streamQuality := kinobaseExtractPlayerJSQualities(file, voiceForExtract, k.host)
		if len(streamQuality) == 0 {
			continue
		}
		qualityMap := make(map[string]string, len(streamQuality))
		var firstURL string
		for i, sq := range streamQuality {
			u := streamProxyURL(req, toString(sq["url"]), "kinobase", links)
			streamQuality[i]["url"] = u
			qualityMap[toString(sq["quality"])] = u
			if firstURL == "" {
				firstURL = u
			}
		}

		label := strings.TrimSpace(episode.Title)
		if label == "" {
			continue
		}
		epNum, _ := kinobaseNumber(kinobaseEpisodeNumRe, label)
		row := map[string]any{
			"method":  "play",
			"url":     firstURL,
			"stream":  firstURL,
			"s":       season,
			"e":       epNum,
			"name":    label,
			"title":   fmt.Sprintf("%s (%s)", baseTitle, label),
			"quality": qualityMap,
		}
		if subs := kinobaseParseSubtitles(episode.Subtitle, k.host); len(subs) > 0 {
			row["subtitles"] = subs
		}
		rows = append(rows, episodeRow{data: row, label: label, num: epNum})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].num == rows[j].num {
			return rows[i].label < rows[j].label
		}
		return rows[i].num < rows[j].num
	})
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	seasons := make([]int, 0, len(rows))
	episodesNums := make([]int, 0, len(rows))
	for _, row := range rows {
		data = append(data, row.data)
		labels = append(labels, row.label)
		seasons = append(seasons, season)
		episodesNums = append(episodesNums, row.num)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type":  "episode",
			"data":  data,
			"voice": voiceRows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for _, row := range voiceRows {
		getsTVAppendVoiceHTML(&sb, row)
	}
	sb.WriteString(`</div><div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodesNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (k *KinobaseChecker) writeSerialLegacy(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	title string,
	href string,
	year int,
	season int,
	serial []kinobaseSeason,
	links *proxylink.Manager,
) {
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encHref := url.QueryEscape(href)

	var episodes []kinobaseSeason
	if len(serial) > 0 && len(serial[0].Folder) == 0 {
		if season == -1 {
			link := fmt.Sprintf("%s/lite/kinobase?rjson=%s&title=%s&year=%d&href=%s&s=1",
				host, getsTVBool(rjson), encTitle, year, encHref,
			)
			row := map[string]any{"method": "link", "id": 1, "url": link, "name": "1 сезон"}
			if rjson {
				writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": []map[string]any{row}})
				return
			}
			var sb strings.Builder
			sb.WriteString(`<div class="videos__line">`)
			getsTVAppendSeasonHTML(&sb, row, "1 сезон", true)
			sb.WriteString(`</div>`)
			writeHTML(w, http.StatusOK, sb.String())
			return
		}
		episodes = serial
	} else {
		if season == -1 {
			rows := make([]map[string]any, 0, len(serial))
			labels := make([]string, 0, len(serial))
			for _, item := range serial {
				sn, ok := kinobaseNumber(kinobaseSeasonNumRe, item.Title)
				if !ok {
					continue
				}
				link := fmt.Sprintf("%s/lite/kinobase?rjson=%s&title=%s&year=%d&href=%s&s=%d",
					host, getsTVBool(rjson), encTitle, year, encHref, sn,
				)
				rows = append(rows, map[string]any{
					"method": "link",
					"id":     sn,
					"url":    link,
					"name":   fmt.Sprintf("%d сезон", sn),
				})
				labels = append(labels, fmt.Sprintf("%d сезон", sn))
			}
			if len(rows) == 0 {
				writeGetsTVEmpty(w, rjson)
				return
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

		for i := range serial {
			if sn, ok := kinobaseNumber(kinobaseSeasonNumRe, serial[i].Title); ok && sn == season {
				episodes = serial[i].Folder
				break
			}
		}
	}

	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	baseTitle := getsTVJoinName(title, "")
	type episodeRow struct {
		data  map[string]any
		label string
		num   int
	}
	rows := make([]episodeRow, 0, len(episodes))
	for _, episode := range episodes {
		streamQuality := kinobaseExtractLegacyQualities(episode.File, k.host)
		if len(streamQuality) == 0 {
			continue
		}
		qualityMap := make(map[string]string, len(streamQuality))
		var firstURL string
		for i, sq := range streamQuality {
			u := streamProxyURL(req, toString(sq["url"]), "kinobase", links)
			streamQuality[i]["url"] = u
			qualityMap[toString(sq["quality"])] = u
			if firstURL == "" {
				firstURL = u
			}
		}
		label := strings.TrimSpace(episode.Title)
		if label == "" {
			continue
		}
		epNum, _ := kinobaseNumber(kinobaseEpisodeNumRe, label)
		row := map[string]any{
			"method":  "play",
			"url":     firstURL,
			"stream":  firstURL,
			"s":       season,
			"e":       epNum,
			"name":    label,
			"title":   fmt.Sprintf("%s (%s)", baseTitle, label),
			"quality": qualityMap,
		}
		rows = append(rows, episodeRow{data: row, label: label, num: epNum})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].num == rows[j].num {
			return rows[i].label < rows[j].label
		}
		return rows[i].num < rows[j].num
	})
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	seasons := make([]int, 0, len(rows))
	episodesNums := make([]int, 0, len(rows))
	for _, row := range rows {
		data = append(data, row.data)
		labels = append(labels, row.label)
		seasons = append(seasons, season)
		episodesNums = append(episodesNums, row.num)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": data})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodesNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (k *KinobaseChecker) writeSimilar(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	title string,
	year int,
	items []kinobaseSearchItem,
) {
	host := hostFromRequest(req)
	rows := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))
	for _, item := range items {
		if item.Link == "" || strings.TrimSpace(item.Title) == "" {
			continue
		}
		link := fmt.Sprintf("%s/lite/kinobase?rjson=%s&title=%s&year=%d&href=%s",
			host,
			getsTVBool(rjson),
			url.QueryEscape(title),
			year,
			url.QueryEscape(item.Link),
		)
		rows = append(rows, map[string]any{
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
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "similar", "data": rows})
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

// fetchPage performs a plain HTTP GET via the balancer's client. The client
// already routes through a SOCKS5 proxy when one is registered for kinobase
// (config.direct_proxy / RegisterDirectProxy), so a RU residential proxy is
// enough to bypass the geo-fence on /search?query=.
//
// We intentionally do NOT route this through FlareSolverr — nextgen kinobase
// (lampac-nextgen Service.cs) fetches /search?query= via plain httpHydra.Get
// with proxy rotation, and Chrome-rendering search results returns a different
// HTML structure than the raw template, which would defeat the parser.
//
// Residential SOCKS5 proxies tend to flake (EOF, connection reset, request
// timeout) on the first attempt; we retry transport-level errors up to 3
// times with a short backoff. HTTP-level errors (4xx/5xx) are returned
// immediately — they're persistent and a retry won't help.
//
// For embed pages (which need playerjs.js/uppod.js execution to populate
// id="playerjsfile"), use fetchEmbedPage instead.
func (k *KinobaseChecker) fetchPage(ctx context.Context, target, referer string) (string, bool) {
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			log.Debug().Err(err).Str("url", target).Msg("kinobase: fetchPage request build failed")
			return "", false
		}
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.7")
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
		if referer != "" {
			req.Header.Set("Referer", referer)
		}

		resp, err := k.client.Do(req)
		if err != nil {
			lastErr = err
			// Bail out immediately if the caller cancelled — no point retrying.
			if ctx.Err() != nil {
				log.Debug().Err(err).Str("url", target).Msg("kinobase: fetchPage cancelled")
				return "", false
			}
			log.Debug().Err(err).Int("attempt", attempt).Str("url", target).Msg("kinobase: fetchPage HTTP error, retrying")
			// Backoff: 400ms, 900ms.
			select {
			case <-ctx.Done():
				return "", false
			case <-time.After(time.Duration(attempt*attempt) * 100 * time.Millisecond):
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			log.Debug().Int("status", resp.StatusCode).Str("url", target).Msg("kinobase: fetchPage bad status")
			return "", false
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return "", false
			}
			log.Debug().Err(err).Int("attempt", attempt).Str("url", target).Msg("kinobase: fetchPage read error, retrying")
			select {
			case <-ctx.Done():
				return "", false
			case <-time.After(time.Duration(attempt*attempt) * 100 * time.Millisecond):
			}
			continue
		}
		return string(body), true
	}
	log.Debug().Err(lastErr).Str("url", target).Msg("kinobase: fetchPage failed after retries")
	return "", false
}

// fetchEmbedPage fetches a content page (movie/serial). Most kinobase pages
// have id="playerjsfile" / id="playlists" / class="alert" already in the SSR
// HTML, so a single raw HTTP fetch is enough — and ~10x faster than spinning
// up FlareSolverr Chrome. Only when the HTML is missing every embed marker
// (sign of JS-rendered content or a CF challenge interstitial) do we fall
// back to FlareSolverr.
func (k *KinobaseChecker) fetchEmbedPage(ctx context.Context, target, referer string) (string, bool) {
	if page, ok := k.fetchPage(ctx, target, referer); ok {
		if kinobaseHasEmbedMarkers(page) {
			return page, true
		}
		// SSR didn't include player data — try FlareSolverr to let Chrome
		// execute the on-page JS that injects id="playerjsfile".
	}
	fsHeaders := map[string]string{
		"Accept-Language": "ru-RU,ru;q=0.9,en;q=0.7",
	}
	if referer != "" {
		fsHeaders["Referer"] = referer
	}
	if res, ok := httpclient.FlareSolverrFetch(ctx, "kinobase", target, httpclient.FlareSolverrFetchOptions{
		Headers: fsHeaders,
	}); ok && len(res.Solution.Response) > 256 {
		return res.Solution.Response, true
	}
	return "", false
}

// kinobaseHasEmbedMarkers reports whether the page contains any of the
// markers we need to parse the embed: playerjs JSON, uppod playlists, or
// an alert block telling the user the content is unavailable.
func kinobaseHasEmbedMarkers(page string) bool {
	return strings.Contains(page, `id="playerjsfile"`) ||
		strings.Contains(page, `id="playlists"`) ||
		strings.Contains(page, `<div class="alert"`)
}

func kinobaseExtractPlayerJSQualities(content, voice, host string) []map[string]any {
	qualityOrder := []string{"2160", "1440", "1080", "720", "480", "360"}
	rows := make([]map[string]any, 0, len(qualityOrder))
	for _, q := range qualityOrder {
		segRe := regexp.MustCompile(`(?is)\[` + q + `p(?: [^\]]+)?\]([^\[]+)`)
		segments := segRe.FindAllStringSubmatch(content, -1)
		link := ""
		for _, m := range segments {
			if len(m) < 2 {
				continue
			}
			segment := strings.TrimSpace(m[1])
			if segment == "" {
				continue
			}

			if voice == "" {
				link = strings.TrimSpace(submatch1(regexp.MustCompile(`(?is)(?:\{[^\}]+\})?([^{}\[,; \t\r\n]+)`), segment))
			} else {
				link = strings.TrimSpace(submatch1(regexp.MustCompile(`(?is)\{`+regexp.QuoteMeta(voice)+`\}([^,;]+)`), segment))
			}
			if link != "" {
				break
			}
		}
		link = kinobaseNormalizeStreamURL(link, host)
		if link == "" {
			continue
		}
		rows = append(rows, map[string]any{
			"quality": q + "p",
			"url":     link,
		})
	}
	return rows
}

func kinobaseExtractLegacyQualities(content, host string) []map[string]any {
	rows := make([]map[string]any, 0, 4)
	for _, q := range []string{"1080", "720", "480", "360"} {
		re := regexp.MustCompile(`(?is)(https?://[^"\[\|,;\n\r\t ]+_` + q + `(_10)?\.(mp4|m3u8))`)
		link := kinobaseNormalizeStreamURL(submatch1(re, content), host)
		if link == "" {
			continue
		}
		rows = append(rows, map[string]any{
			"quality": q + "p",
			"url":     link,
		})
	}
	return rows
}

func kinobaseNormalizeStreamURL(link, host string) string {
	link = strings.TrimSpace(html.UnescapeString(link))
	if link == "" {
		return ""
	}
	link = strings.ReplaceAll(link, `\/`, `/`)
	link = strings.ReplaceAll(link, `\u002F`, `/`)
	link = strings.ReplaceAll(link, `\`, "")
	if strings.HasPrefix(link, "//") {
		return "https:" + link
	}
	if strings.HasPrefix(link, "/") {
		return strings.TrimRight(host, "/") + link
	}
	return link
}

func kinobaseUniqueVoices(file string) []string {
	out := make([]string, 0, 8)
	seen := map[string]struct{}{}
	for _, m := range kinobaseVoiceRe.FindAllStringSubmatch(file, -1) {
		if len(m) < 2 {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func kinobaseParseSubtitles(raw, host string) []map[string]any {
	rows := make([]map[string]any, 0, 4)
	for _, m := range kinobaseSubtitleRe.FindAllStringSubmatch(raw, -1) {
		if len(m) < 3 {
			continue
		}
		label := strings.TrimSpace(m[1])
		link := kinobaseNormalizeStreamURL(m[2], host)
		if label == "" || link == "" {
			continue
		}
		rows = append(rows, map[string]any{
			"method": "link",
			"label":  label,
			"url":    link,
		})
	}
	return rows
}

func kinobaseFindLastText(input, start, end string) string {
	i := strings.LastIndex(input, start)
	if i < 0 {
		return ""
	}
	rest := input[i+len(start):]
	before, _, ok := strings.Cut(rest, end)
	if !ok {
		return ""
	}
	return before
}

func kinobaseNumber(re *regexp.Regexp, title string) (int, bool) {
	m := re.FindStringSubmatch(strings.TrimSpace(title))
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(m[1]))
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func slicesContains(items []string, value string) bool {
	return slices.Contains(items, value)
}

func atoiOrZero(v string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(v))
	return n
}
