package litesrc

import (
	"context"
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
)

// English balancers (vidlink, videasy, hydraflix, twoembed). These embed an
// obfuscated player that only reveals the real HLS at runtime, so — like the
// upstream C# `black_magic` — we resolve streams by loading the embed page in a
// headless browser and sniffing the manifest request (engBrowserSniff).
//
// checksearch returns `data-json=` (BaseENG parity, no rch). The catalog is
// TMDB-driven: a movie is one "English" call-entry; a series lists seasons then
// episodes from TMDB, each a call-entry into /lite/{plugin}/video which does the
// browser sniff. Results are cached ~15m (a browser resolve is expensive).
//
// NOTE: the browser sniff can only be exercised where headless Chrome runs
// (prod), so the deterministic pieces here — embed-URL shape, TMDB listing,
// header filtering — carry the unit tests; the resolve itself is prod-verified.

var (
	engWantM3U8  = regexp.MustCompile(`\.m3u8`)
	engWantMedia = regexp.MustCompile(`\.(m3u8|mpd|mp4)(\?|/|$)`)
	engAbortAds  = regexp.MustCompile(`(?i)(doubleclick|googletagmanager|google-analytics|/ads/|adsco\.|pubtrky\.|clarity\.|rtmark|pixel\.|histats|popads|propeller|onclick|fonts\.googleapis)`)
	// engPlayClickJS clicks the first play button (a <button> wrapping an svg),
	// which vidsrc/videasy-style players require before requesting the manifest.
	engPlayClickJS = `(function(){var b=document.querySelector('button svg');if(b&&b.closest('button')){b.closest('button').click();return true;}var v=document.querySelector('video');if(v){v.play&&v.play();return true;}return false;})()`
)

// engSource is the per-balancer configuration.
type engSource struct {
	plugin   string
	host     func(cfg config.Config) string
	movieURL func(host string, id int64) string
	tvURL    func(host string, id int64, s, e int) string
	wantRe   *regexp.Regexp
	clickJS  string
}

var engSourceDefs = map[string]*engSource{
	"vidlink": {
		plugin:   "vidlink",
		host:     func(c config.Config) string { return engHost(c.Online.VidLink.Host, "https://vidlink.pro") },
		movieURL: func(h string, id int64) string { return fmt.Sprintf("%s/movie/%d", h, id) },
		tvURL:    func(h string, id int64, s, e int) string { return fmt.Sprintf("%s/tv/%d/%d/%d", h, id, s, e) },
		wantRe:   engWantMedia,
	},
	"videasy": {
		plugin:   "videasy",
		host:     func(c config.Config) string { return engHost(c.Online.Videasy.Host, "https://player.videasy.net") },
		movieURL: func(h string, id int64) string { return fmt.Sprintf("%s/movie/%d", h, id) },
		tvURL:    func(h string, id int64, s, e int) string { return fmt.Sprintf("%s/tv/%d/%d/%d", h, id, s, e) },
		wantRe:   engWantMedia,
		clickJS:  engPlayClickJS,
	},
	"hydraflix": {
		plugin:   "hydraflix",
		host:     func(c config.Config) string { return engHost(c.Online.HydraFlix.Host, "https://vidfast.pro") },
		movieURL: func(h string, id int64) string { return fmt.Sprintf("%s/movie/%d?autoPlay=true&theme=e1216d", h, id) },
		tvURL: func(h string, id int64, s, e int) string {
			return fmt.Sprintf("%s/tv/%d/%d/%d?autoPlay=true&theme=e1216d", h, id, s, e)
		},
		wantRe: engWantMedia,
	},
	"twoembed": {
		plugin:   "twoembed",
		host:     func(c config.Config) string { return engHost(c.Online.TwoEmbed.Host, "https://embed.su") },
		movieURL: func(h string, id int64) string { return fmt.Sprintf("%s/embed/movie/%d", h, id) },
		tvURL:    func(h string, id int64, s, e int) string { return fmt.Sprintf("%s/embed/tv/%d/%d/%d", h, id, s, e) },
		wantRe:   engWantM3U8,
	},
}

func engHost(configured, fallback string) string {
	h := strings.TrimSpace(strings.TrimRight(configured, "/"))
	if h == "" {
		h = fallback
	}
	if !strings.Contains(h, "://") {
		h = "https://" + h
	}
	return h
}

// engSourceChecker serves one ENG balancer's /lite/{plugin} routes.
type engSourceChecker struct {
	def    *engSource
	client *http.Client // TMDB metadata only
	cache  *engResolveCache
}

func NewEngSourceChecker(plugin string) *engSourceChecker {
	def := engSourceDefs[plugin]
	if def == nil {
		return nil
	}
	return &engSourceChecker{
		def:    def,
		client: httpclient.NewForBalancer(plugin, 12*time.Second),
		cache:  newEngResolveCache(),
	}
}

func (e *engSourceChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			// BaseENG parity: data-json= (no rch auto-recommend).
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data-json="))
			return
		}
		if strings.Contains(req.URL.Path, "/video") {
			e.video(w, req, cfg, links)
			return
		}
		e.index(w, req, cfg)
	}
}

func (e *engSourceChecker) index(w http.ResponseWriter, req *http.Request, cfg config.Config) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	serial := q.Get("serial") == "1" || parseBoolParam(q.Get("serial"))
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !sSet {
		s = -1
	}

	id, _ := getsTVQueryInt(q.Get("id"))
	if v, ok := getsTVQueryInt(q.Get("tmdb_id")); ok && v > 0 {
		id = v
	}
	if id <= 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	baseTitle := getsTVJoinName(title, originalTitle)
	if baseTitle == "" {
		baseTitle = "English"
	}

	if !serial {
		// Movie: single call-entry into the video resolver.
		link := fmt.Sprintf("%s/lite/%s/video?id=%d&imdb_id=%s", host, e.def.plugin, id, url.QueryEscape(imdbID))
		row := map[string]any{
			"method": "call",
			"url":    link,
			"stream": link + "&play=true",
			"name":   "English",
			"title":  baseTitle,
		}
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": []map[string]any{row}})
			return
		}
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		getsTVAppendMovieHTML(&sb, row, "English", true, 0, 0)
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	seasons := engTmdbSeasons(req.Context(), e.client, int64(id))
	if s == -1 {
		e.writeSeasons(w, req, rjson, seasons, id, imdbID, title, originalTitle)
		return
	}
	e.writeEpisodes(w, req, rjson, seasons, id, s, baseTitle)
}

func (e *engSourceChecker) writeSeasons(w http.ResponseWriter, req *http.Request, rjson bool, seasons []engTmdbSeason, id int, imdbID, title, originalTitle string) {
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	encImdb := url.QueryEscape(imdbID)

	rows := make([]map[string]any, 0, len(seasons))
	labels := make([]string, 0, len(seasons))
	for _, sn := range seasons {
		if sn.Number < 1 {
			continue
		}
		link := fmt.Sprintf("%s/lite/%s?rjson=%s&serial=1&id=%d&imdb_id=%s&title=%s&original_title=%s&s=%d",
			host, e.def.plugin, getsTVBool(rjson), id, encImdb, encTitle, encOriginal, sn.Number)
		label := fmt.Sprintf("%d сезон", sn.Number)
		rows = append(rows, map[string]any{"method": "link", "id": sn.Number, "url": link, "name": label})
		labels = append(labels, label)
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
}

func (e *engSourceChecker) writeEpisodes(w http.ResponseWriter, req *http.Request, rjson bool, seasons []engTmdbSeason, id, s int, baseTitle string) {
	host := hostFromRequest(req)
	count := 0
	for _, sn := range seasons {
		if sn.Number == s {
			count = sn.EpisodeCount
			break
		}
	}
	if count < 1 {
		count = 1
	}

	rows := make([]map[string]any, 0, count)
	labels := make([]string, 0, count)
	seasonsCol := make([]int, 0, count)
	epNums := make([]int, 0, count)
	for i := 1; i <= count; i++ {
		link := fmt.Sprintf("%s/lite/%s/video?id=%d&s=%d&e=%d", host, e.def.plugin, id, s, i)
		label := fmt.Sprintf("%d серия", i)
		rows = append(rows, map[string]any{
			"method": "call",
			"url":    link,
			"stream": link + "&play=true",
			"s":      s,
			"e":      i,
			"name":   label,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, label),
		})
		labels = append(labels, label)
		seasonsCol = append(seasonsCol, s)
		epNums = append(epNums, i)
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": rows})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasonsCol[i], epNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// video resolves the stream via the headless-browser sniff and proxies it.
func (e *engSourceChecker) video(w http.ResponseWriter, req *http.Request, cfg config.Config, links *proxylink.Manager) {
	q := req.URL.Query()
	id, _ := getsTVQueryInt(q.Get("id"))
	if id <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	s, _ := getsTVQueryInt(q.Get("s"))
	ep, _ := getsTVQueryInt(q.Get("e"))

	host := e.def.host(cfg)
	embedURL := e.def.movieURL(host, int64(id))
	if s > 0 {
		embedURL = e.def.tvURL(host, int64(id), s, ep)
	}

	res, ok := e.cache.get(embedURL)
	if !ok {
		sniff, sniffed := engBrowserSniff(req.Context(), e.def.plugin, embedURL, e.def.wantRe, engAbortAds, e.def.clickJS, 35*time.Second)
		if !sniffed || sniff.URL == "" {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		res = sniff
		e.cache.put(embedURL, res)
	}

	stream := res.URL
	if links != nil {
		if len(res.Headers) > 0 {
			stream = streamProxyURLWithHeaders(req, res.URL, e.def.plugin, links, res.Headers)
		} else {
			stream = streamProxyURL(req, res.URL, e.def.plugin, links)
		}
	}

	if parseBoolParam(q.Get("play")) {
		http.Redirect(w, req, stream, http.StatusFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"method": "play", "url": stream})
}

// ---------------------------------------------------------------------------
// TMDB season/episode metadata
// ---------------------------------------------------------------------------

type engTmdbSeason struct {
	Number       int
	EpisodeCount int
}

type engTmdbTVResponse struct {
	Seasons []struct {
		SeasonNumber int `json:"season_number"`
		EpisodeCount int `json:"episode_count"`
	} `json:"seasons"`
}

// engTmdbSeasons fetches the season list (number + episode count) for a TV id
// via the public TMDB mirror. Returns a single default season on failure so
// series still yield at least one playable path.
func engTmdbSeasons(ctx context.Context, client *http.Client, id int64) []engTmdbSeason {
	fallback := []engTmdbSeason{{Number: 1, EpisodeCount: 1}}
	if id <= 0 || client == nil {
		return fallback
	}
	target := "https://tmdb.mirror-kurwa.men/3/tv/" + strconv.FormatInt(id, 10) + "?api_key=4ef0d7355d9ffb5151e987764708ce96"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fallback
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := client.Do(req)
	if err != nil {
		return fallback
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fallback
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fallback
	}
	var root engTmdbTVResponse
	if err := stdjson.Unmarshal(body, &root); err != nil {
		return fallback
	}
	out := make([]engTmdbSeason, 0, len(root.Seasons))
	for _, sn := range root.Seasons {
		if sn.SeasonNumber < 1 {
			continue // skip "Specials" (season 0)
		}
		out = append(out, engTmdbSeason{Number: sn.SeasonNumber, EpisodeCount: sn.EpisodeCount})
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

// ---------------------------------------------------------------------------
// Resolve cache — a browser sniff is expensive, so cache per embed URL.
// ---------------------------------------------------------------------------

type engResolveCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]engResolveEntry
}

type engResolveEntry struct {
	res     engSniffResult
	expires time.Time
}

func newEngResolveCache() *engResolveCache {
	return &engResolveCache{ttl: 15 * time.Minute, m: make(map[string]engResolveEntry)}
}

func (c *engResolveCache) get(key string) (engSniffResult, bool) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	ent, ok := c.m[key]
	if !ok || now.After(ent.expires) {
		if ok {
			delete(c.m, key)
		}
		return engSniffResult{}, false
	}
	return ent.res, true
}

func (c *engResolveCache) put(key string, res engSniffResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Opportunistic prune of expired entries so the map can't grow unbounded.
	if len(c.m) > 64 {
		now := time.Now()
		for k, v := range c.m {
			if now.After(v.expires) {
				delete(c.m, k)
			}
		}
	}
	c.m[key] = engResolveEntry{res: res, expires: time.Now().Add(c.ttl)}
}
