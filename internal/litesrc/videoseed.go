package litesrc

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

var (
	videoseedStreamRe   = regexp.MustCompile(`https?://[^"' \t\r\n]+(?:\.m3u8|\.mp4)[^"' \t\r\n]*`)
	videoseedPlayerjsRe = regexp.MustCompile(`new\s+Playerjs\s*\(\s*["']([^"']+)["']\s*\)`)
	// Token embedded in iframe URLs. Since 2026-08 the API key (apiv2.php) and
	// the player key (embed token) are SEPARATE — the embed 404s ("invalid
	// token") when given the API key, so the API-supplied iframe token must be
	// preserved, never overwritten with the config token.
	videoseedTokenValRe = regexp.MustCompile(`token=([a-z0-9]{32})`)
	// PlayerJS obfuscation garbage block: |||BASE64ISH== (no '=' or '|' inside).
	// Same pattern the .NET reference removes; applied to fixed point.
	videoseedGarbageRe = regexp.MustCompile(`\|\|\|[^=|]+==`)
)

type videoseedChecker struct {
	client    *http.Client // standard client for API (videoseed.tv)
	cdnClient *http.Client // uTLS client for CDN (kinoserial.net) — Chrome TLS fingerprint
	host      string
	token     string // API key for apiv2.php
	// limitUntil — до этого момента API отвечает «request limit expired» на
	// ВСЁ. Без этой памяти каждый промах делал три платных запроса (kp, imdb,
	// title) и получал три отказа, а в журнале это выглядело как «пусто».
	limitMu     sync.Mutex
	limitUntil  time.Time
	playerToken string // separate embed/player key (optional; iframes carry their own)
	rhub        bool

	cacheMu     sync.RWMutex
	searchCache map[string]videoseedCacheSearch
	videoCache  map[string]videoseedCacheString
	// learnedPlayerToken is the player token last seen in a search-API iframe;
	// used for the embed_auto fallback when a node lacks an iframe.
	learnedPlayerToken string
}

type videoseedCacheSearch struct {
	Expire time.Time
	Value  videoseedDataNode
}

type videoseedCacheString struct {
	Expire time.Time
	Value  []videoseedTrack
}

type videoseedSearchRoot struct {
	Status string              `json:"status"`
	Data   []videoseedDataNode `json:"data"`
}

type videoseedDataNode struct {
	Iframe  string                       `json:"iframe"`
	Seasons map[string]videoseedSeasonVM `json:"seasons"`
}

type videoseedSeasonVM struct {
	Videos map[string]videoseedVideoVM `json:"videos"`
}

type videoseedVideoVM struct {
	Iframe string `json:"iframe"`
}

func NewVideoseedChecker(cfg config.Config) *videoseedChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.Videoseed.Host, "/"))
	if host == "" || host == "https://videoseed.tv" || host == "http://videoseed.tv" {
		// apiv2.php moved to api.videoseed.tv subdomain 2026-05; root host
		// returns 404 on all API paths now.
		host = "https://api.videoseed.tv"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return &videoseedChecker{
		client:      httpclient.NewForBalancer("videoseed", 20*time.Second),
		cdnClient:   httpclient.NewUTLSForBalancer("videoseed", 25*time.Second), // Chrome TLS via SOCKS5 for DDoS-Guard bypass
		host:        host,
		token:       strings.TrimSpace(cfg.Online.Videoseed.Token),
		playerToken: strings.TrimSpace(cfg.Online.Videoseed.PlayerToken),
		rhub:        cfg.Online.Videoseed.Rhub,
		searchCache: make(map[string]videoseedCacheSearch, 256),
		videoCache:  make(map[string]videoseedCacheString, 512),
	}
}

func (v *videoseedChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/"), "/")

		if raw == "videoseed" {
			if parseBoolParam(req.URL.Query().Get("checksearch")) {
				show := v.checkSearch(req)
				writeCheckSearchResponse(w, show, pluginQualityBadgeGet("videoseed"))
				return
			}

			token := v.token
			if kitToken, ok := kit.TokenOverride(req.Context(), "Videoseed"); ok {
				token = kitToken
			}
			if token == "" {
				writeGetsTVEmpty(w, parseBoolParam(req.URL.Query().Get("rjson")))
				return
			}
			if rchGate(w, req, v.rhub) {
				return
			}
			v.index(w, req)
			return
		}

		if after, ok := strings.CutPrefix(raw, "videoseed/video/"); ok {
			v.video(w, req, after, links)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "videoseed route is not implemented in local mode",
			"balanser": raw,
		})
	}
}

func (v *videoseedChecker) checkSearch(req *http.Request) bool {
	if strings.TrimSpace(v.host) == "" {
		return false
	}
	if strings.TrimSpace(v.token) == "" {
		return false
	}
	if v.rhub {
		// With rhub the search runs on the user's device at play time; no device
		// is bound to this server-side aggregation probe, so we can't verify here.
		// Advertise availability and let the play-path handshake do the work.
		return true
	}

	serial := strings.TrimSpace(req.URL.Query().Get("serial")) == "1"
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(req.URL.Query().Get("kinopoisk_id")), 10, 64)
	imdbID := strings.TrimSpace(req.URL.Query().Get("imdb_id"))
	title := strings.TrimSpace(req.URL.Query().Get("title"))
	originalTitle := strings.TrimSpace(req.URL.Query().Get("original_title"))
	year, _ := getsTVQueryInt(req.URL.Query().Get("year"))

	node, ok := v.search(req, serial, kinopoiskID, imdbID, title, originalTitle, year)
	if !ok {
		return false
	}

	if serial {
		return len(node.Seasons) > 0
	}
	return strings.TrimSpace(node.Iframe) != ""
}

func (v *videoseedChecker) index(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	serial := strings.TrimSpace(q.Get("serial")) == "1"
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := getsTVQueryInt(q.Get("year"))
	s, sOK := getsTVQueryInt(q.Get("s"))
	if !sOK {
		s = -1
	}

	node, ok := v.search(req, serial, kinopoiskID, imdbID, title, originalTitle, year)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	baseTitle := getsTVJoinName(title, originalTitle)

	if !serial {
		iframe := strings.TrimSpace(node.Iframe)

		// Use search API iframe (with internal ID like /embed/1045931/) as-is —
		// it already carries the correct PLAYER token, which differs from the
		// API key. Only repair a zeroed/missing token; never overwrite a real one
		// (the embed answers "invalid token" to the API key).
		if iframe != "" {
			iframe = videoseedNormalizeIframe(iframe, v.playerTokenFor())
			log.Debug().Str("searchIframe", iframe).Int64("kp", kinopoiskID).Msg("videoseed: using search API iframe")
		}

		// Fallback to embed_auto format when search didn't return an iframe.
		// Requires the player token — the API key is rejected by the embed.
		if iframe == "" && kinopoiskID > 0 {
			if pt := v.playerTokenFor(); pt != "" {
				iframe = fmt.Sprintf("https://tv-2-kinoserial.net/embed_auto/%d/?token=%s", kinopoiskID, url.QueryEscape(pt))
				log.Debug().Str("embedAuto", iframe).Int64("kp", kinopoiskID).Msg("videoseed: fallback to embed_auto for movie")
			}
		}

		if iframe == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}
		link := host + "/lite/videoseed/video/" + url.QueryEscape(videoseedEncode(iframe))
		row := map[string]any{
			"method": "call",
			"url":    link,
			"stream": link,
			"title":  baseTitle,
			"name":   "По умолчанию",
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
		return
	}

	if len(node.Seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if s == -1 {
		type seasonRow struct {
			id int
		}
		rows := make([]seasonRow, 0, len(node.Seasons))
		for key := range node.Seasons {
			id, err := strconv.Atoi(strings.TrimSpace(key))
			if err != nil || id < 1 {
				continue
			}
			rows = append(rows, seasonRow{id: id})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
		if len(rows) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		data := make([]map[string]any, 0, len(rows))
		labels := make([]string, 0, len(rows))
		for _, row := range rows {
			name := strconv.Itoa(row.id) + " сезон"
			link := host + "/lite/videoseed?rjson=" + getsTVBool(rjson) +
				"&serial=1" +
				"&kinopoisk_id=" + strconv.FormatInt(kinopoiskID, 10) +
				"&imdb_id=" + url.QueryEscape(imdbID) +
				"&title=" + url.QueryEscape(title) +
				"&original_title=" + url.QueryEscape(originalTitle) +
				"&year=" + strconv.Itoa(year) +
				"&s=" + strconv.Itoa(row.id)
			data = append(data, map[string]any{
				"method": "link",
				"id":     row.id,
				"url":    link,
				"name":   name,
			})
			labels = append(labels, name)
		}

		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{
				"type": "season",
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
		return
	}

	season, ok := node.Seasons[strconv.Itoa(s)]
	if !ok {
		for key, val := range node.Seasons {
			id, err := strconv.Atoi(strings.TrimSpace(key))
			if err == nil && id == s {
				season = val
				ok = true
				break
			}
		}
	}
	if !ok || len(season.Videos) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	type episodeRow struct {
		id     int
		iframe string
	}
	eps := make([]episodeRow, 0, len(season.Videos))
	for key, val := range season.Videos {
		id, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil || id < 1 {
			continue
		}
		if strings.TrimSpace(val.Iframe) == "" {
			continue
		}
		eps = append(eps, episodeRow{id: id, iframe: strings.TrimSpace(val.Iframe)})
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].id < eps[j].id })
	if len(eps) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(eps))
	labels := make([]string, 0, len(eps))
	seasons := make([]int, 0, len(eps))
	episodes := make([]int, 0, len(eps))
	epPlayerToken := v.playerTokenFor()
	for _, ep := range eps {
		name := strconv.Itoa(ep.id) + " серия"
		epIframe := videoseedNormalizeIframe(ep.iframe, epPlayerToken)
		link := host + "/lite/videoseed/video/" + url.QueryEscape(videoseedEncode(epIframe))
		data = append(data, map[string]any{
			"method": "call",
			"url":    link,
			"stream": link,
			"s":      s,
			"e":      ep.id,
			"name":   name,
			"title":  baseTitle + " (" + name + ")",
		})
		labels = append(labels, name)
		seasons = append(seasons, s)
		episodes = append(episodes, ep.id)
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
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (v *videoseedChecker) video(w http.ResponseWriter, req *http.Request, enc string, links *proxylink.Manager) {
	// Deferred form: /lite/videoseed/video/<enc>.m3u8?track=N&name=<voice>.
	// The CDN links storage.videoseedcdn.com hands out outlive neither the
	// 5-minute /capi/streams cache nor a paused episode: viewers hit 404 on a
	// link resolved a minute earlier (10–40 «proxy: upstream 404» an hour). So
	// the client is given THIS url instead of the CDN link and the redirect
	// below re-resolves (90 s track cache, then a fresh embed) at play time —
	// the same deferred pattern alloha and vibix use.
	enc = strings.TrimSpace(enc)
	deferred := strings.HasSuffix(strings.ToLower(enc), ".m3u8")
	if deferred {
		enc = enc[:len(enc)-len(".m3u8")]
	}
	iframe := videoseedDecode(enc)
	if iframe == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	tracks, cached := v.getVideoCache(iframe)
	if !cached {
		tracks = v.resolveVideoTracks(req, iframe)
		if len(tracks) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}

		// Wrap all stream URLs through /proxy/ via SOCKS5 (if registered for "videoseed").
		// storage.videoseedcdn.com is GEO-locked — needs CIS exit IP from VLESS proxy.
		// Both embed fetch and stream access must use the same proxy so IP matches the hash.
		// Include Referer/Origin from the embed page — CDN checks these headers.
		var cdnHeaders map[string]string
		if u, err := url.Parse(iframe); err == nil && u.Host != "" {
			origin := u.Scheme + "://" + u.Host
			cdnHeaders = map[string]string{
				"Referer": origin + "/",
				"Origin":  origin,
			}
		}
		for i := range tracks {
			if cdnHeaders != nil {
				tracks[i].URL = streamProxyURLWithHeaders(req, tracks[i].URL, "videoseed", links, cdnHeaders)
			} else {
				tracks[i].URL = streamProxyURL(req, tracks[i].URL, "videoseed", links)
			}
		}
		// Cache every track, not just the first: a cache hit used to collapse the
		// voice list to one anonymous stream (and starved the capi drill).
		v.setVideoCache(iframe, tracks, 90*time.Second)
	}

	firstURL := tracks[0].URL

	if deferred {
		// Pick the voice by name first (the list order can change between
		// resolves), then by index, then the first track.
		chosen := tracks[0]
		wantName := strings.ToLower(strings.TrimSpace(req.URL.Query().Get("name")))
		matched := false
		if wantName != "" {
			for _, t := range tracks {
				if strings.ToLower(strings.TrimSpace(t.Name)) == wantName {
					chosen, matched = t, true
					break
				}
			}
		}
		if !matched {
			if idx, err := strconv.Atoi(strings.TrimSpace(req.URL.Query().Get("track"))); err == nil && idx >= 0 && idx < len(tracks) {
				chosen = tracks[idx]
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, req, chosen.URL, http.StatusFound)
		return
	}

	if parseBoolParam(req.URL.Query().Get("play")) {
		http.Redirect(w, req, firstURL, http.StatusFound)
		return
	}

	// The /capi drill appends rjson=true to every continuation URL and reads
	// voices out of data[] — a bare {"method":"play"} object carries no name and
	// is dropped as «0 voices» (capi.go: `name != "" && len(byq) > 0`), which is
	// why alpac tv saw the source as empty while Lampa played it fine. Answer the
	// drill with one named row per voice; Lampa keeps the play shape below.
	if parseBoolParam(req.URL.Query().Get("rjson")) {
		rows := make([]map[string]any, 0, len(tracks))
		host := hostFromRequest(req)
		for i, t := range tracks {
			name := strings.TrimSpace(t.Name)
			if name == "" {
				name = "По умолчанию"
			}
			// The drill gets the DEFERRED url (see the top of video()): the CDN
			// link is re-resolved when the viewer presses play, not when the
			// title was drilled. .m3u8 in the path keeps capi's stream sniff happy;
			// capiSameOrigin passes /lite/videoseed/video/ through unwrapped.
			deferredURL := host + "/lite/videoseed/video/" + videoseedEncodeHex(iframe) + ".m3u8?track=" + strconv.Itoa(i) +
				"&name=" + url.QueryEscape(strings.TrimSpace(t.Name))
			// Deliberately NO "title": with an empty/movie response type the drill
			// arms its wrong-film guard on each item's title, and a voice label
			// ("LostFilm") matches no requested title — every row would be dropped.
			// "name" alone feeds capi's voice label (firstNonEmpty(Name,Title,…)).
			rows = append(rows, map[string]any{
				"method":  "play",
				"name":    name,
				"url":     deferredURL,
				"quality": map[string]string{"auto": deferredURL},
			})
		}
		// Deliberately NO "type" either: this endpoint serves both movies and
		// episodes, and capiTypeMismatch drops a "movie" response outright on a
		// series card — which is exactly why alpac tv saw serials as empty.
		writeJSON(w, http.StatusOK, map[string]any{"data": rows})
		return
	}

	result := map[string]any{
		"method": "play",
		"url":    firstURL,
	}

	// Build voice map for quality selector — each voice is a separate stream.
	if len(tracks) > 1 {
		voiceMap := make(map[string]any, len(tracks))
		for _, t := range tracks {
			voiceMap[t.Name] = t.URL
		}
		result["quality"] = voiceMap
		result["qualitys"] = voiceMap
	}

	writeJSON(w, http.StatusOK, result)
}

// videoseedTrack represents a single voice/audio track with its stream URL.
type videoseedTrack struct {
	Name string
	URL  string
}

// videoseedIsCDNHost returns true if the URL host belongs to the videoseed CDN
// that requires uTLS (Chrome TLS fingerprint) to bypass DDoS-Guard.
func videoseedIsCDNHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.Contains(u.Host, "kinoserial.net") || strings.Contains(u.Host, "kinolot.net")
}

// videoseedFetchCDN fetches a URL from the videoseed CDN using uTLS with
// Chrome TLS fingerprint and full Chrome HTTP headers. Follows redirects
// manually (up to 6 hops) and decompresses gzip responses.
//
// Uses uTLS with SOCKS5 proxy (if registered for "videoseed" balancer) so that
// stream URLs in the PlayerJS config are bound to the same exit IP used by the
// proxy handler when clients fetch the actual streams.
func (v *videoseedChecker) videoseedFetchCDN(ctx context.Context, targetURL, referer string) (string, string, error) {
	socksAddr := httpclient.SocksAddrForBalancer("videoseed")
	log.Debug().Str("url", targetURL).Str("socks", socksAddr).Msg("videoseed: CDN fetch start")
	client := httpclient.NewUTLSForBalancerNoRedirect("videoseed", 25*time.Second)

	currentURL := targetURL
	for i := range 6 {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, currentURL, nil)
		if err != nil {
			return "", "", err
		}
		httpReq.Header = httpclient.ChromeHeaders(referer)

		resp, err := client.Do(httpReq)
		if err != nil {
			log.Warn().Err(err).Str("url", currentURL).Int("hop", i).Msg("videoseed: CDN fetch error")
			return "", "", err
		}
		log.Debug().Int("status", resp.StatusCode).Str("url", currentURL).Int("hop", i).
			Str("ct", resp.Header.Get("Content-Type")).
			Int64("cl", resp.ContentLength).
			Msg("videoseed: CDN hop response")

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := strings.TrimSpace(resp.Header.Get("Location"))
			resp.Body.Close()
			if loc == "" {
				return "", currentURL, fmt.Errorf("empty redirect location from %s", currentURL)
			}
			base, _ := url.Parse(currentURL)
			locURL, err := url.Parse(loc)
			if err != nil {
				return "", currentURL, err
			}
			referer = currentURL
			currentURL = base.ResolveReference(locURL).String()
			continue
		}

		var reader io.Reader = resp.Body
		if strings.Contains(resp.Header.Get("Content-Encoding"), "gzip") {
			gr, err := gzip.NewReader(resp.Body)
			if err != nil {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
				resp.Body.Close()
				return string(body), currentURL, nil
			}
			reader = gr
			defer gr.Close()
		}
		body, _ := io.ReadAll(io.LimitReader(reader, 2<<20))
		resp.Body.Close()

		return string(body), currentURL, nil
	}
	return "", currentURL, fmt.Errorf("too many redirects")
}

// resolveVideoTracks fetches the embed page and extracts all voice tracks
// from PlayerJS config. Returns a slice of tracks (voice name + stream URL).
func (v *videoseedChecker) resolveVideoTracks(req *http.Request, iframe string) []videoseedTrack {
	if strings.Contains(iframe, ".m3u8") || strings.Contains(iframe, ".mp4") {
		return []videoseedTrack{{Name: "По умолчанию", URL: iframe}}
	}

	if _, err := url.Parse(iframe); err != nil {
		return nil
	}

	isCDN := videoseedIsCDNHost(iframe)
	log.Info().Str("iframe", iframe).Bool("cdn", isCDN).Msg("videoseed: resolving embed")

	var htmlBody string
	if isCDN {
		body, _, err := v.videoseedFetchCDN(req.Context(), iframe, strings.TrimRight(v.host, "/")+"/")
		if err != nil {
			log.Warn().Err(err).Str("iframe", iframe).Msg("videoseed: CDN fetch failed")
			return nil
		}
		htmlBody = body
	} else {
		headers := map[string]string{
			"Referer":    strings.TrimRight(v.host, "/"),
			"User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		}
		// Embed page lives on the DDoS-Guarded site → route through the hub when
		// available (the request carries ?nws_id once the client is in rch mode).
		body, err := rchFetch(req, v.rhub, iframe, headers, func() (string, error) {
			httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, iframe, nil)
			if err != nil {
				return "", err
			}
			for hk, hv := range headers {
				httpReq.Header.Set(hk, hv)
			}
			resp, err := v.client.Do(httpReq)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			return string(b), nil
		})
		if err != nil {
			return nil
		}
		htmlBody = body
	}

	log.Info().Int("bodyLen", len(htmlBody)).Str("iframe", iframe).Msg("videoseed: embed fetched")

	if len(htmlBody) == 0 {
		log.Warn().Str("iframe", iframe).Msg("videoseed: embed returned empty body")
		return nil
	}

	// Log body preview for debugging.
	preview := htmlBody
	if len(preview) > 500 {
		preview = preview[:500]
	}
	log.Debug().Str("iframe", iframe).Int("bodyLen", len(htmlBody)).Str("preview", preview).Msg("videoseed: embed body preview")

	// GEO block / DDoS-Guard detection.
	if strings.Contains(htmlBody, "451.php") || strings.Contains(htmlBody, "недоступно в вашем регионе") {
		log.Warn().Str("iframe", iframe).Msg("videoseed: GEO-blocked (451)")
		return nil
	}
	if strings.Contains(htmlBody, "DDoS-Guard") || strings.Contains(htmlBody, "ddos-guard") {
		log.Warn().Str("iframe", iframe).Msg("videoseed: DDoS-Guard challenge")
		return nil
	}

	// Try PlayerJS config first: new Playerjs("#2BASE64_JSON")
	if tracks := videoseedParsePlayerJS(htmlBody); len(tracks) > 0 {
		log.Info().Int("tracks", len(tracks)).Str("iframe", iframe).Msg("videoseed: parsed PlayerJS config")
		for i, t := range tracks {
			u := t.URL
			if len(u) > 150 {
				u = u[:150]
			}
			log.Debug().Int("i", i).Str("name", t.Name).Str("url", u).Msg("videoseed: track")
		}

		// Probe first track via same uTLS+SOCKS5 that fetched embed —
		// if CDN returns 404 even through this client, the URL itself is bad.
		if isCDN && len(tracks) > 0 && strings.HasPrefix(tracks[0].URL, "http") {
			go videoseedProbeCDN(req.Context(), tracks[0].URL, iframe)
		}

		return tracks
	}

	// Try alternate player patterns (Lampac, HDVBPlayer, etc.)
	log.Debug().Str("iframe", iframe).
		Bool("hasPlayerjs", strings.Contains(htmlBody, "Playerjs")).
		Bool("hasPlayer", strings.Contains(htmlBody, "Player(")).
		Bool("hasFile", strings.Contains(htmlBody, `"file"`)).
		Bool("hasM3u8", strings.Contains(htmlBody, ".m3u8")).
		Bool("hasMp4", strings.Contains(htmlBody, ".mp4")).
		Bool("hasHLS", strings.Contains(htmlBody, "hls")).
		Msg("videoseed: PlayerJS parse failed, checking alternatives")

	// Fallback: look for direct stream URLs in the page.
	htmlBody = strings.ReplaceAll(htmlBody, `\\/`, `/`)
	htmlBody = strings.ReplaceAll(htmlBody, `\u0026`, "&")
	if direct := strings.TrimSpace(videoseedStreamRe.FindString(htmlBody)); direct != "" {
		direct = videoseedNormalizeURLField(direct)
		if direct != "" {
			log.Info().Str("direct", direct).Msg("videoseed: found direct stream URL")
			return []videoseedTrack{{Name: "По умолчанию", URL: direct}}
		}
	}

	log.Warn().Str("iframe", iframe).Int("bodyLen", len(htmlBody)).Msg("videoseed: no stream found in embed")
	return nil
}

// videoseedParsePlayerJS extracts voice tracks from a PlayerJS embed page.
// Format: new Playerjs("#2BASE64_OBFUSCATED_JSON")
// The base64 contains obfuscation markers |||GARBAGE=== that must be removed.
// The decoded JSON has a "file" field: "{ Voice1 } URL1;{ Voice2 } URL2;..."
func videoseedParsePlayerJS(html string) []videoseedTrack {
	m := videoseedPlayerjsRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return nil
	}
	data := m[1]

	// Log raw captured data for diagnostics.
	rawPrefix := data
	if len(rawPrefix) > 80 {
		rawPrefix = rawPrefix[:80]
	}
	rawSuffix := ""
	if len(data) > 80 {
		rawSuffix = data[len(data)-80:]
	}
	log.Debug().Int("rawLen", len(data)).Str("prefix", rawPrefix).Str("suffix", rawSuffix).
		Msg("videoseed: PlayerJS raw captured data")

	// Strip the #N prefix (e.g. "#2").
	if len(data) > 0 && data[0] == '#' {
		idx := 1
		for idx < len(data) && data[idx] >= '0' && data[idx] <= '9' {
			idx++
		}
		log.Debug().Str("hashPrefix", data[:idx]).Msg("videoseed: PlayerJS prefix stripped")
		data = data[idx:]
	}

	// Remove obfuscation garbage blocks |||GARBAGE== to fixed point (the .NET
	// reference applies the same regex; a single pass can miss blocks that
	// become matchable only after a neighbouring block is removed — seen live
	// on tv-3-kinolot.net embeds where blocks nest as "|||A|||B==").
	beforeCleanLen := len(data)
	pipeCount := strings.Count(data, "|||")
	for range 8 {
		next := videoseedGarbageRe.ReplaceAllString(data, "")
		if next == data {
			break
		}
		data = next
	}
	log.Debug().Int("beforeClean", beforeCleanLen).Int("afterClean", len(data)).
		Int("pipeMarkers", pipeCount).Bool("pipesLeft", strings.Contains(data, "|||")).
		Msg("videoseed: PlayerJS garbage removal")

	// After garbage removal the payload is one contiguous base64 stream with
	// only trailing padding (verified live on both kinoserial and kinolot
	// embeds). Decode whole; fall back to the legacy segmented decoder for the
	// older "AAAA==BBBB==" interior-padding format. Valid JSON with a "file"
	// field is the arbiter — a decoder can return garbage bytes without error.
	var cfg struct {
		File string `json:"file"`
	}
	tryDecode := func(decode func(string) ([]byte, error)) error {
		decoded, err := decode(data)
		if err != nil {
			return err
		}
		cfg.File = ""
		if err := stdjson.Unmarshal(decoded, &cfg); err != nil {
			return err
		}
		if cfg.File == "" {
			return fmt.Errorf("decoded JSON has empty file field")
		}
		return nil
	}
	if wholeErr := tryDecode(videoseedDecodeWholeBase64); wholeErr != nil {
		if segErr := tryDecode(videoseedDecodeSegmentedBase64); segErr != nil {
			log.Warn().AnErr("whole", wholeErr).AnErr("segmented", segErr).Int("dataLen", len(data)).
				Msg("videoseed: PlayerJS decode failed")
			return nil
		}
	}

	// Log a sample of the file field for diagnostics.
	fileSample := cfg.File
	if len(fileSample) > 200 {
		fileSample = fileSample[:200]
	}
	log.Debug().Int("fileLen", len(cfg.File)).Str("sample", fileSample).
		Msg("videoseed: PlayerJS file field")

	return videoseedParseFileTracks(cfg.File)
}

// videoseedParseFileTracks parses the PlayerJS "file" field format:
// "{ Voice Name } https://...m3u8;{ Voice2 } https://...m3u8"
// or just a plain URL without voice labels.
func videoseedParseFileTracks(file string) []videoseedTrack {
	parts := strings.Split(file, ";")
	var tracks []videoseedTrack
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		var name, streamURL string
		// Format: "{ Voice Name } URL" or "[quality]URL"
		if idx := strings.Index(part, "} "); idx >= 0 {
			name = strings.TrimSpace(part[1:idx]) // trim leading {
			name = strings.Trim(name, "{ }")
			streamURL = strings.TrimSpace(part[idx+2:])
		} else {
			name = "По умолчанию"
			streamURL = part
		}

		if !strings.HasPrefix(streamURL, "http") {
			continue
		}

		tracks = append(tracks, videoseedTrack{Name: name, URL: streamURL})
	}
	return tracks
}

func (v *videoseedChecker) search(
	req *http.Request,
	serial bool,
	kinopoiskID int64,
	imdbID string,
	title string,
	originalTitle string,
	year int,
) (videoseedDataNode, bool) {
	cacheKey := videoseedSearchKey(serial, kinopoiskID, imdbID, title, originalTitle, year)
	if out, ok := v.getSearchCache(cacheKey); ok {
		log.Debug().Str("key", cacheKey).Int("seasons", len(out.Seasons)).
			Bool("hasIframe", strings.TrimSpace(out.Iframe) != "").
			Msg("videoseed: search cache hit")
		return out, true
	}

	item := "movie"
	if serial {
		item = "serial"
	}

	// Kit: per-user token override
	effectiveToken := v.token
	if kitToken, ok := kit.TokenOverride(req.Context(), "Videoseed"); ok {
		effectiveToken = kitToken
	}

	trySearch := func(arg string) (videoseedDataNode, bool) {
		if v.quotaExhausted() {
			return videoseedDataNode{}, false // квота кончилась на прошлой попытке — не жечь остаток
		}
		if arg == "" {
			return videoseedDataNode{}, false
		}
		target := strings.TrimRight(v.host, "/") + "/apiv2.php?item=" + item + "&token=" + url.QueryEscape(effectiveToken) + "&" + arg
		headers := map[string]string{
			"Accept":      "application/json,text/plain,*/*",
			"User-Agent":  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
			"X-Lampac-Go": "1",
		}
		// With rhub on + device bound, the search runs on the user's device (RU
		// IP) and bypasses DDoS-Guard without our rented SOCKS5; otherwise falls
		// back to the uTLS+SOCKS5 client.
		body, err := rchFetch(req, v.rhub, target, headers, func() (string, error) {
			httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
			if err != nil {
				return "", err
			}
			for hk, hv := range headers {
				httpReq.Header.Set(hk, hv)
			}
			resp, err := v.client.Do(httpReq)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return "", fmt.Errorf("videoseed: search status %d", resp.StatusCode)
			}
			b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			if err != nil {
				return "", err
			}
			return string(b), nil
		})
		if err != nil || body == "" {
			return videoseedDataNode{}, false
		}

		// На ошибке API кладёт в data СТРОКУ («request limit expired»), а не
		// список — прямой Unmarshal в videoseedSearchRoot падал, и причина отказа
		// терялась. Сначала снимаем статус, потом разбираем данные.
		var head struct {
			Status string             `json:"status"`
			Data   stdjson.RawMessage `json:"data"`
		}
		if err := stdjson.Unmarshal([]byte(body), &head); err != nil {
			return videoseedDataNode{}, false
		}
		if strings.EqualFold(strings.TrimSpace(head.Status), "error") {
			var msg string
			_ = stdjson.Unmarshal(head.Data, &msg)
			if strings.Contains(strings.ToLower(msg), "limit") {
				v.noteQuotaExhausted(msg)
			} else {
				log.Warn().Str("api_error", truncStr(msg, 120)).Msg("videoseed: API отказал")
			}
			return videoseedDataNode{}, false
		}
		var root videoseedSearchRoot
		if err := stdjson.Unmarshal([]byte(body), &root); err != nil {
			return videoseedDataNode{}, false
		}
		if len(root.Data) == 0 {
			return videoseedDataNode{}, false
		}
		v.learnPlayerToken(root.Data[0])
		return root.Data[0], true
	}

	if v.quotaExhausted() {
		// Квота API кончилась: любой запрос вернёт тот же отказ, но спишется.
		return videoseedDataNode{}, false
	}

	if kinopoiskID > 0 {
		if out, ok := trySearch("kp=" + strconv.FormatInt(kinopoiskID, 10)); ok {
			log.Debug().Str("iframe", out.Iframe).Int("seasons", len(out.Seasons)).Int64("kp", kinopoiskID).Msg("videoseed: search found by KP ID")
			v.setSearchCache(cacheKey, out, 40*time.Second)
			return out, true
		}
		log.Debug().Int64("kp", kinopoiskID).Msg("videoseed: search not found by KP ID")
	}
	if strings.TrimSpace(imdbID) != "" {
		if out, ok := trySearch("imdb=" + url.QueryEscape(strings.TrimSpace(imdbID))); ok {
			log.Debug().Str("iframe", out.Iframe).Int("seasons", len(out.Seasons)).Str("imdb", imdbID).Msg("videoseed: search found by IMDB ID")
			v.setSearchCache(cacheKey, out, 40*time.Second)
			return out, true
		}
		log.Debug().Str("imdb", imdbID).Msg("videoseed: search not found by IMDB ID")
	}

	query := strings.TrimSpace(originalTitle)
	if query == "" {
		query = strings.TrimSpace(title)
	}
	if query != "" {
		if year <= 0 {
			year = time.Now().Year()
		}
		arg := "q=" + url.QueryEscape(query) +
			"&release_year_from=" + strconv.Itoa(year-1) +
			"&release_year_to=" + strconv.Itoa(year+1)
		if out, ok := trySearch(arg); ok {
			log.Debug().Str("q", query).Int("year", year).Int("seasons", len(out.Seasons)).
				Msg("videoseed: search found by title")
			v.setSearchCache(cacheKey, out, 40*time.Second)
			return out, true
		}
		log.Debug().Str("q", query).Int("year", year).Msg("videoseed: search not found by title")
	}

	log.Debug().Bool("serial", serial).Int64("kp", kinopoiskID).Str("imdb", imdbID).
		Str("title", title).Str("orig", originalTitle).Int("year", year).
		Msg("videoseed: search exhausted — no match")

	return videoseedDataNode{}, false
}

// videoseedDecodeWholeBase64 decodes the cleaned payload as a single base64
// stream: strips every "=" (they carry no data) and re-pads to a multiple of
// four. Fails loudly on leftover non-base64 bytes so the caller can fall back.
func videoseedDecodeWholeBase64(data string) ([]byte, error) {
	data = strings.ReplaceAll(data, "=", "")
	switch len(data) % 4 {
	case 1:
		return nil, fmt.Errorf("stripped payload length %d invalid for base64", len(data))
	case 2:
		data += "=="
	case 3:
		data += "="
	}
	return base64.StdEncoding.DecodeString(data)
}

// videoseedDecodeSegmentedBase64 decodes base64 data that may contain multiple
// concatenated segments with padding (e.g., "AAAA==BBBB==CCCC==").
// Each segment is decoded independently and bytes are concatenated.
// This is critical because removing "=" and decoding as one block shifts
// character alignment boundaries and corrupts the output.
func videoseedDecodeSegmentedBase64(data string) ([]byte, error) {
	var result []byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '=' {
			// Find end of padding run (= or ==)
			j := i
			for j < len(data) && data[j] == '=' {
				j++
			}
			// data[start:j] is one complete segment with padding
			segment := data[start:j]
			if len(segment) > 0 {
				dec, err := base64.StdEncoding.DecodeString(segment)
				if err != nil {
					return nil, fmt.Errorf("segment at byte %d (len %d): %w", start, len(segment), err)
				}
				result = append(result, dec...)
			}
			start = j
			i = j - 1
		}
	}
	// Handle remaining segment without trailing padding
	if start < len(data) {
		remaining := data[start:]
		switch len(remaining) % 4 {
		case 1:
			// Single trailing byte can't form valid base64 — trim it
			remaining = remaining[:len(remaining)-1]
		case 2:
			remaining += "=="
		case 3:
			remaining += "="
		}
		if len(remaining) > 0 {
			dec, err := base64.StdEncoding.DecodeString(remaining)
			if err != nil {
				return nil, fmt.Errorf("final segment at byte %d (len %d): %w", start, len(remaining), err)
			}
			result = append(result, dec...)
		}
	}
	return result, nil
}

// videoseedProbeCDN makes a HEAD request to a CDN URL using the same
// uTLS+SOCKS5 transport that fetched the embed page. This diagnostic
// tells us if the CDN URL is valid from the same IP/TLS context.
func videoseedProbeCDN(parentCtx context.Context, cdnURL, iframe string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = parentCtx // probe runs independently

	// Same uTLS+SOCKS5 as videoseedFetchCDN
	client := httpclient.NewUTLSForBalancerNoRedirect("videoseed", 10*time.Second)

	// Parse iframe for Referer
	referer := "https://videoseed.tv/"
	if u, err := url.Parse(iframe); err == nil && u.Host != "" {
		referer = u.Scheme + "://" + u.Host + "/"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, cdnURL, nil)
	if err != nil {
		log.Debug().Err(err).Msg("videoseed: probe create request failed")
		return
	}
	httpReq.Header.Set("Referer", referer)
	httpReq.Header.Set("Origin", strings.TrimSuffix(referer, "/"))
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := client.Do(httpReq)
	if err != nil {
		log.Warn().Err(err).Str("url", cdnURL).Msg("videoseed: CDN probe failed")
		return
	}
	defer resp.Body.Close()
	// Read a small amount to see Content-Type
	peek, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	log.Info().
		Int("status", resp.StatusCode).
		Int64("cl", resp.ContentLength).
		Str("ct", resp.Header.Get("Content-Type")).
		Str("server", resp.Header.Get("Server")).
		Int("bodyLen", len(peek)).
		Str("bodyPreview", func() string {
			s := string(peek)
			if len(s) > 200 {
				return s[:200]
			}
			return s
		}()).
		Str("url", cdnURL).
		Msg("videoseed: CDN probe result")
}

// videoseedNormalizeIframe keeps the API-supplied iframe URL intact (the .NET
// reference uses it verbatim — host included). The iframe token is the PLAYER
// key and must not be replaced with the API key; only a zeroed or missing
// token is repaired with playerToken when one is known.
func videoseedNormalizeIframe(iframe, playerToken string) string {
	if m := videoseedTokenValRe.FindStringSubmatch(iframe); m != nil {
		if !videoseedIsZeroToken(m[1]) {
			return iframe
		}
		if playerToken != "" {
			return strings.Replace(iframe, m[0], "token="+playerToken, 1)
		}
		return iframe
	}
	if playerToken != "" {
		sep := "?"
		if strings.Contains(iframe, "?") {
			sep = "&"
		}
		return iframe + sep + "token=" + url.QueryEscape(playerToken)
	}
	return iframe
}

// videoseedIsZeroToken reports whether tok is the all-zeros placeholder the
// API historically used when the account token was hidden.
func videoseedIsZeroToken(tok string) bool {
	return strings.Trim(tok, "0") == ""
}

// playerTokenFor returns the embed/player token: explicit config value first,
// otherwise the one learned from search-API iframe URLs.
func (v *videoseedChecker) playerTokenFor() string {
	if v.playerToken != "" {
		return v.playerToken
	}
	v.cacheMu.RLock()
	defer v.cacheMu.RUnlock()
	return v.learnedPlayerToken
}

// learnPlayerToken remembers the player token embedded in search-result iframe
// URLs so the embed_auto fallback can work without explicit config.
func (v *videoseedChecker) learnPlayerToken(node videoseedDataNode) {
	tok := ""
	if m := videoseedTokenValRe.FindStringSubmatch(node.Iframe); m != nil && !videoseedIsZeroToken(m[1]) {
		tok = m[1]
	}
	if tok == "" {
		for _, season := range node.Seasons {
			for _, vid := range season.Videos {
				if m := videoseedTokenValRe.FindStringSubmatch(vid.Iframe); m != nil && !videoseedIsZeroToken(m[1]) {
					tok = m[1]
					break
				}
			}
			if tok != "" {
				break
			}
		}
	}
	if tok == "" {
		return
	}
	v.cacheMu.Lock()
	v.learnedPlayerToken = tok
	v.cacheMu.Unlock()
}

func videoseedEncode(v string) string {
	return url.QueryEscape(v)
}

// videoseedHexPrefix marks the hex form of the embed url used by the deferred
// stream link (see video()). Hex is chosen because the route lowercases the
// path and reverse proxies may normalise percent-escapes — both are harmless to
// [0-9a-f], while base64url or %2F would not survive.
const videoseedHexPrefix = "h."

func videoseedEncodeHex(v string) string {
	return videoseedHexPrefix + hex.EncodeToString([]byte(v))
}

func videoseedDecode(v string) string {
	if v == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(v, videoseedHexPrefix); ok {
		if b, err := hex.DecodeString(rest); err == nil && strings.TrimSpace(string(b)) != "" {
			return string(b)
		}
		return ""
	}
	if u, err := url.QueryUnescape(v); err == nil && strings.TrimSpace(u) != "" {
		return u
	}
	return ""
}

func videoseedNormalizeURLField(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.Trim(raw, `"'`)
	raw = strings.ReplaceAll(raw, `\\u0026`, "&")
	raw = strings.ReplaceAll(raw, `\\u002F`, "/")
	raw = strings.ReplaceAll(raw, `\\&`, "&")
	raw = strings.ReplaceAll(raw, `\\/`, `/`)
	raw = strings.ReplaceAll(raw, `\/`, `/`)
	raw = strings.ReplaceAll(raw, `\u0026`, "&")
	raw = strings.ReplaceAll(raw, `\u002F`, "/")
	raw = strings.ReplaceAll(raw, `\&`, "&")
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	return raw
}

func videoseedSearchKey(serial bool, kinopoiskID int64, imdbID, title, originalTitle string, year int) string {
	return strings.Join([]string{
		getsTVBool(serial),
		strconv.FormatInt(kinopoiskID, 10),
		strings.ToLower(strings.TrimSpace(imdbID)),
		strings.ToLower(strings.TrimSpace(title)),
		strings.ToLower(strings.TrimSpace(originalTitle)),
		strconv.Itoa(year),
	}, "|")
}

func (v *videoseedChecker) getSearchCache(key string) (videoseedDataNode, bool) {
	v.cacheMu.RLock()
	item, ok := v.searchCache[key]
	v.cacheMu.RUnlock()
	if !ok {
		return videoseedDataNode{}, false
	}
	if time.Now().After(item.Expire) {
		v.cacheMu.Lock()
		delete(v.searchCache, key)
		v.cacheMu.Unlock()
		return videoseedDataNode{}, false
	}
	return item.Value, true
}

func (v *videoseedChecker) setSearchCache(key string, value videoseedDataNode, ttl time.Duration) {
	if key == "" || ttl <= 0 {
		return
	}
	exp := time.Now().Add(ttl)
	v.cacheMu.Lock()
	v.searchCache[key] = videoseedCacheSearch{Expire: exp, Value: value}
	for k, item := range v.searchCache {
		if time.Now().After(item.Expire) {
			delete(v.searchCache, k)
		}
	}
	v.cacheMu.Unlock()
}

func (v *videoseedChecker) getVideoCache(key string) ([]videoseedTrack, bool) {
	v.cacheMu.RLock()
	item, ok := v.videoCache[key]
	v.cacheMu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(item.Expire) {
		v.cacheMu.Lock()
		delete(v.videoCache, key)
		v.cacheMu.Unlock()
		return nil, false
	}
	return item.Value, true
}

func (v *videoseedChecker) setVideoCache(key string, value []videoseedTrack, ttl time.Duration) {
	if key == "" || len(value) == 0 || ttl <= 0 {
		return
	}
	exp := time.Now().Add(ttl)
	v.cacheMu.Lock()
	v.videoCache[key] = videoseedCacheString{Expire: exp, Value: value}
	for k, item := range v.videoCache {
		if time.Now().After(item.Expire) {
			delete(v.videoCache, k)
		}
	}
	v.cacheMu.Unlock()
}

func (v *videoseedChecker) probe(req *http.Request, method, target string) bool {
	httpReq, err := http.NewRequestWithContext(req.Context(), method, target, nil)
	if err != nil {
		return false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "text/html,application/json,*/*")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if method == http.MethodHead && resp.StatusCode == http.StatusMethodNotAllowed {
		return false
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return false
	}
	if method == http.MethodHead {
		return true
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(body))) > 0
}

// videoseedQuotaHold — сколько не трогать API после «request limit expired».
// Квота у videoseed суточная, но точное время сброса неизвестно; десять минут
// между пробами не дают выжечь остаток и при этом быстро замечают возврат.
const videoseedQuotaHold = 10 * time.Minute

func (v *videoseedChecker) noteQuotaExhausted(msg string) {
	v.limitMu.Lock()
	fresh := time.Now().After(v.limitUntil)
	v.limitUntil = time.Now().Add(videoseedQuotaHold)
	v.limitMu.Unlock()
	if fresh {
		log.Warn().Str("api", truncStr(msg, 80)).Dur("hold", videoseedQuotaHold).
			Msg("videoseed: квота API исчерпана — источник молчит, пока квота не вернётся")
	}
}

func (v *videoseedChecker) quotaExhausted() bool {
	v.limitMu.Lock()
	defer v.limitMu.Unlock()
	return time.Now().Before(v.limitUntil)
}
