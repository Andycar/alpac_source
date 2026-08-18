package litesrc

import (
	"context"
	"encoding/base64"
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
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

var (
	videodbPlayerRe       = regexp.MustCompile(`new\s+Player\("([^"\r\n]+)"\);`)
	videodbGarbageRe      = regexp.MustCompile(`//[^=]+=`)
	videodbStreamsRe      = regexp.MustCompile(`\[(Авто|Auto|2160|1440|1080|720|480|360|HD|SD|FHD|4K)p?\]([^"\,\[\{ ;]+)`)
	videodbSeasonNumberRe = regexp.MustCompile(`([0-9]+)`)
	videodbEpisodeNumber  = regexp.MustCompile(`([0-9]+)`)
	videodbVoicePrefixRe  = regexp.MustCompile(`^[a-zA-Z]{3}\s*\|\s*`)

	// videodb.cloud stream format: [HD]{Voice}url;{Voice}url;,[SD]...
	videodbCloudEntryRe         = regexp.MustCompile(`\{([^}]+)\}([^;,\[\{]+)`)
	videodbCloudQualRe          = regexp.MustCompile(`\[(HD|SD|FHD|4K)\]`)
	videodbCloudFileRe          = regexp.MustCompile(`"file"\s*:\s*(\[[\s\S]*?\])\s*,\s*"`)
	videodbCloudFileStrRe       = regexp.MustCompile(`"file"\s*:\s*"(\[[^\n]+?)",`)
	videodbCloudTrailingCommaRe = regexp.MustCompile(`,\s*([}\]])`)
)

// videodbObrutEndpoint is one obrut.show catalogue: the host plus the site id
// registered on it. The pair is fixed — a host only answers for its own site id,
// so they must travel together (swapping hosts in the config used to leave the
// site ids behind, addressing every host with the wrong catalogue).
type videodbObrutEndpoint struct {
	Host string
	Site string
}

// zetflixDBDefaultHost is the obrut catalogue upstream's ZetflixDB module uses.
const zetflixDBDefaultHost = "https://54243ba5.obrut.show"

// videodbObrutSites maps a known obrut host to its site id.
var videodbObrutSites = map[string]string{
	"30bf3790.obrut.show": "AN",
	"d6dd387e.obrut.show": "cjM",
	"54243ba5.obrut.show": "AO", // ZetflixDB's catalogue upstream
}

// videodbDeadObrutHosts answer 404 for every id (verified 2026-08-07 across five
// kinopoisk ids, both URL forms). They are demoted to the end of the candidate
// list rather than removed, so a revived host is still tried — and so a config
// that still names one as the primary does not cost every lookup a wasted hop.
var videodbDeadObrutHosts = map[string]struct{}{
	"30bf3790.obrut.show": {},
}

type videodbChecker struct {
	client      *http.Client
	cloudClient *http.Client // separate client for videodb.cloud (may route through proxy)
	host        string
	// plugin is the balancer name used for stream-proxy tokens, per-balancer
	// proxies and route matching — "videodb", or "zetflixdb" for the AO-only
	// variant that mirrors upstream's ZetflixDB module.
	plugin string
	// obrutEndpoints are the obrut.show catalogues to try, in order.
	obrutEndpoints []videodbObrutEndpoint
	// lastObrutOK remembers the endpoint that last returned a playable embed so
	// it is tried first next time.
	lastObrutOK  atomic.Value // string (host)
	apiHost      string       // first obrut endpoint host (Origin/Referer for CDN)
	apiHost2     string       // secondary obrut endpoint host (kept for config round-trip)
	fallbackHost string       // collaps embed API (variyt.ws) for interkh CDN fallback
	collapsToken string       // collaps token for fallback API (optional)
	videodbCloud string       // videodb.cloud host for Georgian/multi-lang dubs
	cfgRoot      string       // repo root for externalids.json lookup
	tmdbAPIHost  string       // TMDB API host (e.g. apitmdb.cub.red) for imdb_id→tmdb_id resolution
	tmdbAPIKey   string       // TMDB API key
	useHLS       bool         // true → HLS (m3u8), false → direct MP4 for obrut CDN
	links        *proxylink.Manager
}

// videodbTMDBIDCache caches imdb_id → tmdb_id resolutions from TMDB find API.
var videodbTMDBIDCache = struct {
	sync.RWMutex
	m map[string]string
}{m: make(map[string]string)}

type videodbRoot struct {
	Title    string         `json:"title"`
	File     string         `json:"file"`
	Subtitle string         `json:"subtitle"`
	Folder   []videodbEntry `json:"folder"`
}

type videodbEntry struct {
	Title  string         `json:"title"`
	File   string         `json:"file"`
	Folder []videodbEntry `json:"folder"`
}

type videodbEmbed struct {
	Items   []videodbRoot
	Movie   bool
	Quality string
}

func NewVideodbChecker(cfg config.Config) *videodbChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.VideoDB.Host, "/"))
	if host == "" {
		host = "https://kinogo.media"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}

	apiHost := strings.TrimSpace(strings.TrimRight(cfg.Online.VideoDB.APIHost, "/"))
	if apiHost == "" {
		apiHost = "https://30bf3790.obrut.show"
	}
	if !strings.Contains(apiHost, "://") {
		apiHost = "https://" + apiHost
	}

	apiHost2 := strings.TrimSpace(strings.TrimRight(cfg.Online.VideoDB.APIHost2, "/"))
	if apiHost2 == "" {
		apiHost2 = "https://d6dd387e.obrut.show"
	}
	if !strings.Contains(apiHost2, "://") {
		apiHost2 = "https://" + apiHost2
	}

	fallbackHost := strings.TrimSpace(strings.TrimRight(cfg.Online.VideoDB.FallbackHost, "/"))
	if fallbackHost == "" {
		fallbackHost = "https://api.variyt.ws"
	}
	if !strings.Contains(fallbackHost, "://") {
		fallbackHost = "https://" + fallbackHost
	}

	videodbCloud := strings.TrimSpace(strings.TrimRight(cfg.Online.VideoDB.VideoDBCloud, "/"))
	if videodbCloud == "" {
		videodbCloud = "https://videodb.cloud"
	}
	if !strings.Contains(videodbCloud, "://") {
		videodbCloud = "https://" + videodbCloud
	}

	tmdbAPIHost := strings.TrimSpace(cfg.TMDBProxy.APIHost)
	if tmdbAPIHost == "" {
		tmdbAPIHost = "apitmdb.cub.red"
	}
	tmdbAPIKey := strings.TrimSpace(cfg.TMDBProxy.APIKey)

	return &videodbChecker{
		client:         httpclient.NewForBalancer("videodb", 12*time.Second),
		cloudClient:    httpclient.NewForBalancer("videodb", 12*time.Second),
		plugin:         "videodb",
		obrutEndpoints: videodbBuildObrutEndpoints(apiHost, apiHost2),
		host:           host,
		apiHost:        apiHost,
		apiHost2:       apiHost2,
		fallbackHost:   fallbackHost,
		collapsToken:   strings.TrimSpace(cfg.Online.Collaps.Token),
		videodbCloud:   videodbCloud,
		cfgRoot:        cfg.Compat.RepoRoot,
		tmdbAPIHost:    tmdbAPIHost,
		tmdbAPIKey:     tmdbAPIKey,
		useHLS:         cfg.Online.VideoDB.HLS,
	}
}

// NewZetflixDBChecker builds the ZetflixDB source: the same obrut player engine
// as videodb, pinned to the AO catalogue (54243ba5.obrut.show) that upstream's
// ZetflixDB module redirects into. The videodb.cloud and collaps fallbacks are
// off on purpose — with them this would just be a second, slower videodb.
func NewZetflixDBChecker(cfg config.Config) *videodbChecker {
	apiHost := strings.TrimSpace(strings.TrimRight(cfg.Online.ZetflixDB.Host, "/"))
	if apiHost == "" {
		apiHost = zetflixDBDefaultHost
	}
	if !strings.Contains(apiHost, "://") {
		apiHost = "https://" + apiHost
	}
	site := videodbObrutSiteFor(apiHost)

	host := strings.TrimSpace(strings.TrimRight(cfg.Online.VideoDB.Host, "/"))
	if host == "" {
		host = "https://kinogo.media" // Referer the obrut CDN expects
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}

	return &videodbChecker{
		client:         httpclient.NewForBalancer("zetflixdb", 12*time.Second),
		cloudClient:    httpclient.NewForBalancer("zetflixdb", 12*time.Second),
		plugin:         "zetflixdb",
		obrutEndpoints: []videodbObrutEndpoint{{Host: apiHost, Site: site}},
		host:           host,
		apiHost:        apiHost,
		cfgRoot:        cfg.Compat.RepoRoot,
		useHLS:         cfg.Online.ZetflixDB.HLS,
	}
}

// videodbBuildObrutEndpoints orders the obrut catalogues to try: the configured
// hosts first (each with the site id that host actually serves), then the other
// known ones, with dead hosts pushed to the back.
func videodbBuildObrutEndpoints(configured ...string) []videodbObrutEndpoint {
	live := make([]videodbObrutEndpoint, 0, 4)
	dead := make([]videodbObrutEndpoint, 0, 2)
	seen := map[string]struct{}{}

	add := func(rawHost string) {
		host := strings.TrimSpace(strings.TrimRight(rawHost, "/"))
		if host == "" {
			return
		}
		if !strings.Contains(host, "://") {
			host = "https://" + host
		}
		key := videodbObrutHostname(host)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		ep := videodbObrutEndpoint{Host: host, Site: videodbObrutSiteFor(host)}
		if _, isDead := videodbDeadObrutHosts[key]; isDead {
			dead = append(dead, ep)
			return
		}
		live = append(live, ep)
	}

	for _, h := range configured {
		add(h)
	}
	// Known catalogues, so a config naming only a dead host still resolves.
	add("https://d6dd387e.obrut.show")
	add(zetflixDBDefaultHost)
	add("https://30bf3790.obrut.show")

	return append(live, dead...)
}

func videodbObrutHostname(rawHost string) string {
	u, err := url.Parse(rawHost)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// videodbObrutSiteFor resolves the site id for a host. Unknown hosts fall back
// to the AN id the .NET module used, which is also what a self-hosted mirror of
// the original site would register.
func videodbObrutSiteFor(rawHost string) string {
	if site, ok := videodbObrutSites[videodbObrutHostname(rawHost)]; ok {
		return site
	}
	return "AN"
}

// obrutCandidates returns the endpoints to try, last-working one first.
func (v *videodbChecker) obrutCandidates() []videodbObrutEndpoint {
	base := v.obrutEndpoints
	if len(base) < 2 {
		return base
	}
	lastOK, _ := v.lastObrutOK.Load().(string)
	if lastOK == "" || lastOK == base[0].Host {
		return base
	}
	out := make([]videodbObrutEndpoint, 0, len(base))
	for _, ep := range base {
		if ep.Host == lastOK {
			out = append(out, ep)
		}
	}
	if len(out) == 0 {
		return base
	}
	for _, ep := range base {
		if ep.Host != lastOK {
			out = append(out, ep)
		}
	}
	return out
}

func (v *videodbChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	v.links = links
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/"), "/")

		switch raw {
		case v.plugin + "/manifest", v.plugin + "/manifest.m3u8", v.plugin + "/manifest.mp4":
			v.manifest(w, req)
			return
		case v.plugin:
			// continue below
		default:
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"error":    v.plugin + " route is not implemented in local mode",
				"balanser": raw,
			})
			return
		}

		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show, quality := v.checkSearch(req)
			writeCheckSearchResponse(w, show, quality)
			return
		}

		v.index(w, req)
	}
}

func (v *videodbChecker) manifest(w http.ResponseWriter, req *http.Request) {
	lower := strings.ToLower(req.URL.Path)
	play := strings.HasSuffix(lower, ".m3u8") || strings.HasSuffix(lower, ".mp4") ||
		parseBoolParam(req.URL.Query().Get("play"))

	// Try multi-quality links param first (JSON map: {"240p":"url","720p":"url",...}).
	linksJSON := strings.TrimSpace(req.URL.Query().Get("links"))
	if linksJSON != "" {
		var qualLinks map[string]string
		if stdjson.Unmarshal([]byte(linksJSON), &qualLinks) == nil && len(qualLinks) > 0 {
			v.manifestMulti(w, req, qualLinks, play)
			return
		}
	}

	// Fallback: single link param.
	link := strings.TrimSpace(req.URL.Query().Get("link"))
	if link == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	target := v.resolveLocation(req, link)
	if target == "" {
		target = link
	}
	if target == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	// Strip HLS suffix for MP4 mode — superdupercdn serves direct MP4 without the suffix.
	if !v.useHLS {
		target = strings.ReplaceAll(target, ":hls:manifest.m3u8", "")
	}
	// Wrap through /proxy/ if proxylink available.
	if v.links != nil {
		target = v.proxyWithHeaders(req, target)
	}

	if play {
		http.Redirect(w, req, target, http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"method":  "play",
		"url":     target,
		"quality": "auto",
	})
}

// manifestMulti resolves multiple CDN quality links and returns a quality map.
func (v *videodbChecker) manifestMulti(w http.ResponseWriter, req *http.Request, qualLinks map[string]string, play bool) {
	host := hostFromRequest(req)
	qualityMap := make(map[string]any, len(qualLinks))
	var bestURL string
	var bestRes int

	for label, cdnURL := range qualLinks {
		target := strings.TrimSpace(cdnURL)
		isAuto := strings.EqualFold(strings.TrimSpace(label), "auto")
		if strings.Contains(target, "obrut.show") || strings.Contains(target, "superdupercdn") {
			// For obrut CDN URLs: return per-quality on-demand manifest endpoint.
			// This refreshes redirect resolution at play time and avoids stale CDN URLs.
			// Use .mp4 extension when HLS is disabled so the player uses MP4 source, not HLS parser.
			//
			// EXCEPT "auto": that link is the CDN's HLS master playlist — it answers
			// with application/vnd.apple.mpegurl whatever extension we advertise.
			// Serving it as .mp4 makes the player pick a parser by extension, hit a
			// playlist, and report «видео не найдено или повреждено».
			ext := ".m3u8"
			if !v.useHLS && !isAuto {
				ext = ".mp4"
			}
			target = host + "/lite/" + v.plugin + "/manifest" + ext + "?link=" + url.QueryEscape(target)
			qualityMap[label] = target
		} else if videodbIsCollapsCDN(target) && v.links != nil {
			// Collaps CDN (interkh.com): wrap with kinogo.media origin headers.
			target = streamProxyURLWithHeaders(req, target, v.plugin, v.links, videodbInterkhStreamHeaders(v.host))
			qualityMap[label] = target
		} else if videodbIsCloudCDN(target) && v.links != nil {
			// videodb.cloud CDN: wrap with videodb.cloud Referer.
			target = streamProxyURLWithHeaders(req, target, v.plugin, v.links, videodbCloudStreamHeaders(v.videodbCloud))
			qualityMap[label] = target
		} else if v.links != nil {
			target = v.proxyWithHeaders(req, target)
			qualityMap[label] = target
		} else {
			if resolved := v.resolveLocation(req, target); resolved != "" {
				target = resolved
			}
			if !v.useHLS {
				target = strings.ReplaceAll(target, ":hls:manifest.m3u8", "")
			}
			qualityMap[label] = target
		}

		res := videodbLabelRank(label)
		if isAuto {
			// In HLS mode the master playlist is the best default. In MP4 mode it
			// must NOT win: the default "url" would then be a playlist the player
			// was told to treat as MP4.
			res = -1
			if v.useHLS {
				res = 10000
			}
		}
		if res > bestRes {
			bestRes = res
			bestURL, _ = qualityMap[label].(string)
		}
	}

	if bestURL == "" {
		for _, u := range qualityMap {
			bestURL, _ = u.(string)
			break
		}
	}

	if play {
		http.Redirect(w, req, bestURL, http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"method":   "play",
		"url":      bestURL,
		"quality":  qualityMap,
		"qualitys": qualityMap,
	})
}

// videodbLabelRank scores a quality label so the best one becomes the default
// stream. obrut labels them by name ("FHD"/"HD"/"SD"), not by height, so a bare
// "%dp" scan returned 0 for every one of them and the winner came down to map
// iteration order.
func videodbLabelRank(label string) int {
	switch strings.ToUpper(strings.TrimSpace(label)) {
	case "4K", "UHD", "2160P":
		return 2160
	case "FHD", "FULLHD":
		return 1080
	case "HD":
		return 720
	case "SD":
		return 480
	}
	res := 0
	fmt.Sscanf(label, "%dp", &res)
	return res
}

func (v *videodbChecker) checkSearch(req *http.Request) (bool, string) {
	kp := strings.TrimSpace(req.URL.Query().Get("kinopoisk_id"))
	var kpid int64
	if kp != "" {
		var err error
		kpid, err = strconv.ParseInt(kp, 10, 64)
		if err != nil {
			kpid = 0
		}
	}
	// Resolve kinopoisk_id from imdb_id if not provided (e.g. source=tmdb).
	if kpid <= 0 {
		kpid = videodbResolveKinopoiskID(v.cfgRoot, req)
	}

	// fetchEmbed handles both obrut.show (via kpid) and videodb.cloud (via tmdbID).
	embed, ok := v.fetchEmbed(req, kpid)
	if !ok {
		return false, ""
	}
	return true, normalizeQualityBadge(embed.Quality)
}

// videodbResolveTMDBID extracts the TMDB ID from the request.
// Lampa sends it as "tmdb_id" param, but when source=tmdb the TMDB ID
// is in the generic "id" param instead.
func videodbResolveTMDBID(req *http.Request) string {
	q := req.URL.Query()
	tmdb := strings.TrimSpace(q.Get("tmdb_id"))
	if tmdb != "" {
		return tmdb
	}
	// Lampa with source=tmdb puts TMDB ID in "id" param.
	if strings.TrimSpace(q.Get("source")) == "tmdb" {
		return strings.TrimSpace(q.Get("id"))
	}
	// Also try "id" if it looks like a numeric ID and no kinopoisk_id is present.
	id := strings.TrimSpace(q.Get("id"))
	if id != "" && strings.TrimSpace(q.Get("kinopoisk_id")) == "" {
		if _, err := strconv.ParseInt(id, 10, 64); err == nil {
			return id
		}
	}
	return ""
}

// videodbResolveKinopoiskID looks up kinopoisk_id from imdb_id via externalids.json.
// Returns 0 if not found.
func videodbResolveKinopoiskID(cfgRoot string, req *http.Request) int64 {
	imdbID := strings.TrimSpace(req.URL.Query().Get("imdb_id"))
	if imdbID == "" {
		return 0
	}
	kpStr := externalKPForIMDB(cfgRoot, imdbID)
	if kpStr == "" {
		return 0
	}
	kp, err := strconv.ParseInt(kpStr, 10, 64)
	if err != nil || kp <= 0 {
		return 0
	}
	return kp
}

func (v *videodbChecker) index(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	kpid, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	tmdbID := videodbResolveTMDBID(req)
	if kpid <= 0 && tmdbID == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	voice := strings.TrimSpace(q.Get("t"))
	s, sOK := getsTVQueryInt(q.Get("s"))
	if !sOK {
		s = -1
	}
	sid, sidOK := getsTVQueryInt(q.Get("sid"))
	if !sidOK {
		sid = -1
	}

	embed, ok := v.fetchEmbed(req, kpid)
	if !ok || len(embed.Items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if embed.Movie {
		v.writeMovie(w, req, rjson, title, originalTitle, embed.Items)
		return
	}

	v.writeSerial(w, req, rjson, kpid, title, originalTitle, voice, s, sid, embed.Items)
}

func (v *videodbChecker) fetchEmbed(req *http.Request, kinopoiskID int64) (videodbEmbed, bool) {
	// Launch videodb.cloud fetch in parallel with obrut.show —
	// videodb.cloud uses TMDB ID and doesn't depend on obrut results.
	// This avoids checksearch timeouts when obrut.show is slow/unreachable.
	tmdbID := videodbResolveTMDBID(req)
	isSerial := strings.TrimSpace(req.URL.Query().Get("serial")) == "1"

	// Resolve kinopoisk_id from imdb_id if not provided (e.g. source=tmdb).
	if kinopoiskID <= 0 {
		kinopoiskID = videodbResolveKinopoiskID(v.cfgRoot, req)
	}

	// When source=cub, tmdb_id is often missing. Resolve from imdb_id via TMDB find API
	// so videodb.cloud can be queried (it has Georgian/multi-lang dubs not on obrut.show).
	if tmdbID == "" {
		imdbID := strings.TrimSpace(req.URL.Query().Get("imdb_id"))
		if imdbID != "" {
			tmdbID = v.resolveTMDBIDFromIMDB(req.Context(), imdbID)
		}
	}

	if kinopoiskID <= 0 && tmdbID == "" {
		return videodbEmbed{}, false
	}

	type cloudResult struct {
		embed videodbEmbed
		ok    bool
	}
	cloudCh := make(chan cloudResult, 1)
	if v.videodbCloud != "" && tmdbID != "" {
		go func() {
			if cloud, ok := v.fetchVideoDBCloudEmbed(req, tmdbID, isSerial); ok && len(cloud.Items) > 0 {
				cloudCh <- cloudResult{cloud, true}
				return
			}
			// Retry with opposite type.
			if cloud2, ok2 := v.fetchVideoDBCloudEmbed(req, tmdbID, !isSerial); ok2 && len(cloud2.Items) > 0 {
				cloudCh <- cloudResult{cloud2, true}
				return
			}
			cloudCh <- cloudResult{}
		}()
	} else {
		cloudCh <- cloudResult{}
	}

	var primary videodbEmbed
	hasPrimary := false

	// 1. obrut.show catalogues, best-known-working first — each host answers
	// only for its own site id, and a title present in one may be absent
	// from another, so walk them until one yields items.
	if kinopoiskID > 0 {
		for _, ep := range v.obrutCandidates() {
			embed, ok := v.fetchObrutEmbed(req, ep.Host, ep.Site, kinopoiskID)
			if !ok || len(embed.Items) == 0 {
				continue
			}
			v.lastObrutOK.Store(ep.Host)
			primary = embed
			hasPrimary = true
			break
		}
	}

	// 3. Collect videodb.cloud result (already running in parallel).
	// Cloud is preferred (has 4K, TMDB-based). If cloud returns results,
	// use it exclusively — no merge with obrut (user request: "1в1 как у TMDB").
	cr := <-cloudCh
	if cr.ok {
		return cr.embed, true
	}

	if hasPrimary {
		return primary, true
	}

	// 4. Fallback: collaps embed API (variyt.ws → interkh.com CDN).
	if kinopoiskID > 0 && v.fallbackHost != "" {
		if embed, ok := v.fetchCollapsEmbed(req, kinopoiskID); ok && len(embed.Items) > 0 {
			return embed, true
		}
	}

	return videodbEmbed{}, false
}

// fetchObrutEmbed fetches video data from an obrut.show CDN host.
// siteID is the site identifier (e.g. "AN", "cjM") registered on that obrut host.
func (v *videodbChecker) fetchObrutEmbed(req *http.Request, obrutHost, siteID string, kinopoiskID int64) (videodbEmbed, bool) {
	target := strings.TrimRight(obrutHost, "/") + "/embed/" + siteID + "?kinopoisk_id=" + strconv.FormatInt(kinopoiskID, 10)
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return videodbEmbed{}, false
	}
	httpReq.Header.Set("sec-fetch-dest", "iframe")
	httpReq.Header.Set("sec-fetch-mode", "navigate")
	httpReq.Header.Set("sec-fetch-site", "cross-site")
	httpReq.Header.Set("referer", strings.TrimRight(v.host, "/")+"/")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return videodbEmbed{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return videodbEmbed{}, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return videodbEmbed{}, false
	}

	decoded, ok := videodbDecodePlayerPayload(string(body))
	if !ok || strings.TrimSpace(decoded) == "" {
		return videodbEmbed{}, false
	}

	quality := "480p"
	switch {
	case strings.Contains(decoded, "2160p"):
		quality = "2160p"
	case strings.Contains(decoded, "1080p"):
		quality = "1080p"
	case strings.Contains(decoded, "720p"):
		quality = "720p"
	}

	var wrapper struct {
		File []videodbRoot `json:"file"`
	}
	if err := stdjson.Unmarshal([]byte(decoded), &wrapper); err == nil && len(wrapper.File) > 0 {
		return videodbEmbed{
			Items:   wrapper.File,
			Movie:   !strings.Contains(decoded, `"folder":`),
			Quality: quality,
		}, true
	}

	var plain []videodbRoot
	if err := stdjson.Unmarshal([]byte(decoded), &plain); err == nil && len(plain) > 0 {
		movie := true
		for _, item := range plain {
			if len(item.Folder) > 0 {
				movie = false
				break
			}
		}
		return videodbEmbed{
			Items:   plain,
			Movie:   movie,
			Quality: quality,
		}, true
	}

	return videodbEmbed{}, false
}

// fetchCollapsEmbed fetches video data from the collaps embed API (luxembd.ws)
// as a fallback when obrut.show doesn't have the content.
// Returns streams from interkh.com CDN in videodb-compatible format.
func (v *videodbChecker) fetchCollapsEmbed(req *http.Request, kinopoiskID int64) (videodbEmbed, bool) {
	target := strings.TrimRight(v.fallbackHost, "/") + "/embed/kp/" + strconv.FormatInt(kinopoiskID, 10)
	if v.collapsToken != "" {
		target += "?token=" + url.QueryEscape(v.collapsToken)
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return videodbEmbed{}, false
	}
	// Use collaps embed headers but with kinogo.media origin for CDN CORS compat.
	collapsSetEmbedHeaders(httpReq)
	httpReq.Header.Set("Origin", strings.TrimRight(v.host, "/"))
	httpReq.Header.Set("Referer", strings.TrimRight(v.host, "/")+"/")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return videodbEmbed{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return videodbEmbed{}, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return videodbEmbed{}, false
	}
	html := string(body)

	// Try serial format: seasons: [{season: 1, episodes: [...]}]
	if raw := strings.TrimSpace(submatch1(collapsSeasonsRe, html)); raw != "" {
		if serial, ok := collapsParseSeasons(raw); ok && len(serial) > 0 {
			items := videodbCollapsSerialToItems(serial)
			if len(items) > 0 {
				return videodbEmbed{
					Items:   items,
					Movie:   false,
					Quality: "1080p",
				}, true
			}
		}
	}

	// Movie format: makePlayer({hls: "...", audio: {...}, ...})
	loc := collapsMakePlayerRe.FindStringIndex(html)
	if len(loc) == 2 && loc[1] < len(html) {
		content := strings.TrimSpace(html[loc[1]:])
		if content != "" {
			hls := collapsNormalizeURL(submatch1(collapsHLSRe, content))
			if hls == "" {
				// Try DASH as fallback.
				hls = collapsNormalizeURL(submatch1(collapsDashRe, content))
			}
			if hls != "" {
				audioName := strings.TrimSpace(submatch1(collapsAudioFirstRe, content))
				if audioName == "" {
					audioName = "По умолчанию"
				}
				return videodbEmbed{
					Items: []videodbRoot{{
						Title: audioName,
						File:  "[Auto]" + hls,
					}},
					Movie:   true,
					Quality: "1080p",
				}, true
			}
		}
	}

	return videodbEmbed{}, false
}

// videodbCollapsSerialToItems converts collaps serial data (seasons/episodes)
// to the videodb folder structure expected by writeSerial.
// Structure: items[season] → Folder[episode] → Folder[voice] → File
func videodbCollapsSerialToItems(serial []collapsSeason) []videodbRoot {
	items := make([]videodbRoot, 0, len(serial))
	for _, s := range serial {
		if s.Season <= 0 {
			continue
		}
		root := videodbRoot{
			Title: strconv.Itoa(s.Season) + " сезон",
		}
		eps := make([]videodbEntry, 0, len(s.Episodes))
		for _, ep := range s.Episodes {
			hls := collapsNormalizeURL(ep.HLS)
			if hls == "" {
				hls = collapsNormalizeURL(ep.Dasha)
				if hls == "" {
					hls = collapsNormalizeURL(ep.Dash)
				}
			}
			if hls == "" {
				continue
			}

			voiceName := "По умолчанию"
			if len(ep.Audio.Names) > 0 && strings.TrimSpace(ep.Audio.Names[0]) != "" {
				voiceName = strings.TrimSpace(ep.Audio.Names[0])
			}

			epTitle := strings.TrimSpace(ep.Episode)
			if epTitle == "" {
				continue
			}

			epEntry := videodbEntry{
				Title: epTitle,
				Folder: []videodbEntry{{
					Title: voiceName,
					File:  "[Auto]" + hls,
				}},
			}
			eps = append(eps, epEntry)
		}
		if len(eps) > 0 {
			root.Folder = eps
			items = append(items, root)
		}
	}
	return items
}

func videodbDecodePlayerPayload(html string) (string, bool) {
	m := videodbPlayerRe.FindStringSubmatch(html)
	if len(m) != 2 {
		return "", false
	}
	raw := strings.TrimSpace(m[1])
	if len(raw) > 73 {
		raw = raw[73:]
	}
	raw = videodbGarbageRe.ReplaceAllString(raw, "")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}

	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		if len(raw)%4 != 0 {
			raw += strings.Repeat("=", 4-(len(raw)%4))
		}
		decoded, err = base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return "", false
		}
	}
	return string(decoded), true
}

func (v *videodbChecker) writeMovie(w http.ResponseWriter, r *http.Request, rjson bool, title, originalTitle string, rows []videodbRoot) {
	data := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	baseTitle := getsTVJoinName(title, originalTitle)

	for _, row := range rows {
		streams := videodbParseStreams(row.File)
		if len(streams) == 0 {
			continue
		}
		name := strings.TrimSpace(row.Title)
		if name == "" {
			name = "По умолчанию"
		}
		isObrut := v.proxyStreams(r, streams)
		first := streams[0]
		method := "play"
		if isObrut {
			method = "call"
		}
		streamURL := first["url"]
		if s, ok := first["stream"]; ok && s != "" {
			streamURL = s
		}
		item := map[string]any{
			"method":        method,
			"url":           first["url"],
			"stream":        streamURL,
			"name":          name,
			"title":         baseTitle + " (" + name + ")",
			"streamquality": streams,
		}
		if !isObrut && len(streams) > 1 {
			qualMap := videodbBuildQualityMap(streams)
			item["quality"] = qualMap
			item["qualitys"] = qualMap
		}
		data = append(data, item)
		labels = append(labels, name)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": data,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (v *videodbChecker) writeSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	kpid int64,
	title string,
	originalTitle string,
	t string,
	s int,
	sid int,
	items []videodbRoot,
) {
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	tmdbID := videodbResolveTMDBID(req)

	if s == -1 {
		type seasonRow struct {
			id    int
			name  string
			index int
		}
		rows := make([]seasonRow, 0, len(items))
		for i, item := range items {
			n, ok := videodbNumber(videodbSeasonNumberRe, item.Title)
			if !ok {
				continue
			}
			rows = append(rows, seasonRow{
				id:    n,
				name:  strings.TrimSpace(item.Title),
				index: i,
			})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
		if len(rows) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		data := make([]map[string]any, 0, len(rows))
		labels := make([]string, 0, len(rows))
		for _, row := range rows {
			data = append(data, map[string]any{
				"method": "link",
				"id":     row.id,
				"name":   row.name,
				"url": host + "/lite/" + v.plugin + "?rjson=" + getsTVBool(rjson) +
					"&kinopoisk_id=" + strconv.FormatInt(kpid, 10) +
					"&tmdb_id=" + url.QueryEscape(tmdbID) +
					"&title=" + encTitle +
					"&original_title=" + encOriginal +
					"&s=" + strconv.Itoa(row.id) +
					"&sid=" + strconv.Itoa(row.index),
			})
			labels = append(labels, row.name)
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

	if sid < 0 || sid >= len(items) {
		for i, item := range items {
			n, ok := videodbNumber(videodbSeasonNumberRe, item.Title)
			if ok && n == s {
				sid = i
				break
			}
		}
	}
	if sid < 0 || sid >= len(items) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	season := items[sid]
	if len(season.Folder) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	type voiceInfo struct {
		name string
	}
	voiceOrder := make([]voiceInfo, 0, 8)
	voiceSeen := make(map[string]struct{}, 8)

	for _, ep := range season.Folder {
		for _, tr := range ep.Folder {
			name := videodbNormalizeVoice(tr.Title)
			if name == "" {
				continue
			}
			if _, ok := voiceSeen[name]; ok {
				continue
			}
			voiceSeen[name] = struct{}{}
			voiceOrder = append(voiceOrder, voiceInfo{name: name})
		}
	}
	if len(voiceOrder) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if t == "" {
		t = voiceOrder[0].name
	}

	voiceRows := make([]map[string]any, 0, len(voiceOrder))
	for _, vi := range voiceOrder {
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   vi.name,
			"active": vi.name == t,
			"url": host + "/lite/" + v.plugin + "?rjson=" + getsTVBool(rjson) +
				"&kinopoisk_id=" + strconv.FormatInt(kpid, 10) +
				"&tmdb_id=" + url.QueryEscape(tmdbID) +
				"&title=" + encTitle +
				"&original_title=" + encOriginal +
				"&s=" + strconv.Itoa(s) +
				"&sid=" + strconv.Itoa(sid) +
				"&t=" + url.QueryEscape(vi.name),
		})
	}

	baseTitle := getsTVJoinName(title, originalTitle)
	episodeRows := make([]map[string]any, 0, len(season.Folder))
	episodeLabels := make([]string, 0, len(season.Folder))
	seasons := make([]int, 0, len(season.Folder))
	episodes := make([]int, 0, len(season.Folder))

	for _, ep := range season.Folder {
		label, epNum := videodbEpisodeLabel(ep.Title)
		if label == "" {
			continue
		}

		var matched videodbEntry
		found := false
		for _, tr := range ep.Folder {
			if videodbNormalizeVoice(tr.Title) == t {
				matched = tr
				found = true
				break
			}
		}
		if !found {
			continue
		}

		streams := videodbParseStreams(matched.File)
		if len(streams) == 0 {
			continue
		}
		isObrut := v.proxyStreams(req, streams)
		first := streams[0]
		method := "play"
		if isObrut {
			method = "call"
		}
		streamURL := first["url"]
		if s, ok := first["stream"]; ok && s != "" {
			streamURL = s
		}
		epData := map[string]any{
			"method":        method,
			"url":           first["url"],
			"stream":        streamURL,
			"name":          label,
			"s":             s,
			"e":             epNum,
			"title":         baseTitle + " (" + label + ")",
			"streamquality": streams,
		}
		if !isObrut && len(streams) > 1 {
			qualMap := videodbBuildQualityMap(streams)
			epData["quality"] = qualMap
			epData["qualitys"] = qualMap
		}
		episodeRows = append(episodeRows, epData)
		episodeLabels = append(episodeLabels, label)
		seasons = append(seasons, s)
		episodes = append(episodes, epNum)
	}

	if len(episodeRows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type":  "episode",
			"data":  episodeRows,
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
	for i, row := range episodeRows {
		getsTVAppendMovieHTML(&sb, row, episodeLabels[i], i == 0, seasons[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func videodbParseStreams(raw string) []map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	type stream struct {
		url  string
		qual string
		rank int
	}
	parseRank := func(q string) int {
		switch q {
		case "Авто":
			return 10_000
		case "4K", "2160":
			return 2160
		case "FHD", "1440":
			return 1440
		case "HD", "1080":
			return 1080
		case "720":
			return 720
		case "SD", "480":
			return 480
		case "360":
			return 360
		default:
			return 0
		}
	}

	tmp := make([]stream, 0, 6)
	seen := make(map[string]struct{}, 6)
	for _, m := range videodbStreamsRe.FindAllStringSubmatch(raw, -1) {
		if len(m) != 3 {
			continue
		}
		q := strings.TrimSpace(m[1])
		link := strings.TrimSpace(m[2])
		if link == "" {
			continue
		}
		if _, ok := seen[q+"|"+link]; ok {
			continue
		}
		seen[q+"|"+link] = struct{}{}
		tmp = append(tmp, stream{
			url:  link,
			qual: q,
			rank: parseRank(q),
		})
	}
	sort.Slice(tmp, func(i, j int) bool {
		if tmp[i].rank == tmp[j].rank {
			return tmp[i].qual < tmp[j].qual
		}
		return tmp[i].rank > tmp[j].rank
	})
	if len(tmp) == 0 {
		return nil
	}

	out := make([]map[string]string, 0, len(tmp))
	for _, s := range tmp {
		quality := normalizeQualityLabel(s.qual)
		out = append(out, map[string]string{
			"url":     s.url,
			"quality": quality,
		})
	}
	return out
}

// videodbBuildQualityMap converts a streamquality slice into a quality map for Lampa.
func videodbBuildQualityMap(streams []map[string]string) map[string]any {
	m := make(map[string]any, len(streams))
	for _, s := range streams {
		label := s["quality"]
		u := s["url"]
		if label != "" && u != "" {
			m[label] = u
		}
	}
	return m
}

func videodbNumber(re *regexp.Regexp, title string) (int, bool) {
	m := re.FindStringSubmatch(strings.TrimSpace(title))
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func videodbEpisodeLabel(raw string) (string, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0
	}
	n, ok := videodbNumber(videodbEpisodeNumber, raw)
	if ok {
		return strconv.Itoa(n) + " серия", n
	}
	return raw, 0
}

func videodbNormalizeVoice(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = videodbVoicePrefixRe.ReplaceAllString(raw, "")
	return strings.TrimSpace(raw)
}

// ---------------------------------------------------------------------------
// TMDB ID resolution from imdb_id (for source=cub where tmdb_id is missing)
// ---------------------------------------------------------------------------

// resolveTMDBIDFromIMDB uses the TMDB find API to resolve a TMDB ID from an IMDB ID.
// Results are cached in-memory. Returns empty string if resolution fails.
func (v *videodbChecker) resolveTMDBIDFromIMDB(ctx context.Context, imdbID string) string {
	if v.tmdbAPIHost == "" || imdbID == "" {
		return ""
	}

	// Check cache.
	videodbTMDBIDCache.RLock()
	if id, ok := videodbTMDBIDCache.m[imdbID]; ok {
		videodbTMDBIDCache.RUnlock()
		return id
	}
	videodbTMDBIDCache.RUnlock()

	// Build TMDB find URL.
	findURL := "https://" + v.tmdbAPIHost + "/3/find/" + url.PathEscape(imdbID) + "?external_source=imdb_db"
	if v.tmdbAPIKey != "" {
		findURL += "&api_key=" + url.QueryEscape(v.tmdbAPIKey)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, findURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return ""
	}

	// TMDB find response: {"movie_results":[{"id":123}],"tv_results":[{"id":456}]}
	var result struct {
		MovieResults []struct {
			ID int `json:"id"`
		} `json:"movie_results"`
		TVResults []struct {
			ID int `json:"id"`
		} `json:"tv_results"`
	}
	if err := stdjson.Unmarshal(body, &result); err != nil {
		return ""
	}

	var tmdbID string
	if len(result.MovieResults) > 0 && result.MovieResults[0].ID > 0 {
		tmdbID = strconv.Itoa(result.MovieResults[0].ID)
	} else if len(result.TVResults) > 0 && result.TVResults[0].ID > 0 {
		tmdbID = strconv.Itoa(result.TVResults[0].ID)
	}

	// Cache result (even empty to avoid repeated lookups for unknown IDs).
	videodbTMDBIDCache.Lock()
	videodbTMDBIDCache.m[imdbID] = tmdbID
	videodbTMDBIDCache.Unlock()

	return tmdbID
}

// ---------------------------------------------------------------------------
// videodb.cloud integration (Georgian/multi-lang dubs, TMDB IDs)
// ---------------------------------------------------------------------------

// fetchVideoDBCloudEmbed fetches video data from videodb.cloud using TMDB ID.
// Movies use player.php (file as string), serials use splayer.php (file as JSON array).
func (v *videodbChecker) fetchVideoDBCloudEmbed(req *http.Request, tmdbID string, isSerial bool) (videodbEmbed, bool) {
	if isSerial {
		return v.fetchVideoDBCloudSerial(req, tmdbID)
	}
	return v.fetchVideoDBCloudMovie(req, tmdbID)
}

// fetchVideoDBCloudMovie fetches a movie from videodb.cloud player.php.
// Response: "file":"[HD]{Voice}url;...,[SD]{Voice}url;..."
func (v *videodbChecker) fetchVideoDBCloudMovie(req *http.Request, tmdbID string) (videodbEmbed, bool) {
	target := v.videodbCloud + "/embed/player.php?type=movie&id=" + url.QueryEscape(tmdbID)

	html, ok := v.fetchVideoDBCloudHTML(req, target)
	if !ok {
		return videodbEmbed{}, false
	}

	// Extract "file":"..." string from Playerjs constructor.
	m := videodbCloudFileStrRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return videodbEmbed{}, false
	}
	fileStr := strings.TrimSpace(m[1])
	if fileStr == "" {
		return videodbEmbed{}, false
	}

	voiceFiles := videodbCloudParseFile(fileStr)
	if len(voiceFiles) == 0 {
		return videodbEmbed{}, false
	}

	items := make([]videodbRoot, 0, len(voiceFiles))
	for voice, file := range voiceFiles {
		items = append(items, videodbRoot{
			Title: voice,
			File:  file,
		})
	}

	quality := videodbCloudDetectQuality(fileStr)
	return videodbEmbed{
		Items:   items,
		Movie:   true,
		Quality: quality,
	}, true
}

// fetchVideoDBCloudSerial fetches a serial from videodb.cloud splayer.php.
// Response: "file": [{title:"Season",folder:[{title:"Episode",file:"[HD]{Voice}url;..."}]}]
func (v *videodbChecker) fetchVideoDBCloudSerial(req *http.Request, tmdbID string) (videodbEmbed, bool) {
	target := v.videodbCloud + "/embed/splayer.php?type=serial&id=" + url.QueryEscape(tmdbID)

	html, ok := v.fetchVideoDBCloudHTML(req, target)
	if !ok {
		return videodbEmbed{}, false
	}

	// Extract JSON file array from Playerjs constructor.
	m := videodbCloudFileRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return videodbEmbed{}, false
	}
	fileJSON := strings.TrimSpace(m[1])
	if fileJSON == "[]" || fileJSON == "" {
		return videodbEmbed{}, false
	}

	// videodb.cloud generates JSON with trailing commas (e.g. [1,2,] and {a:1,})
	fileJSON = videodbCloudTrailingCommaRe.ReplaceAllString(fileJSON, "$1")

	var items []videodbRoot
	if err := stdjson.Unmarshal([]byte(fileJSON), &items); err != nil {
		return videodbEmbed{}, false
	}
	if len(items) == 0 {
		return videodbEmbed{}, false
	}

	// Convert flat format (voices in file string) to nested folder format.
	for i, season := range items {
		for j, ep := range season.Folder {
			voiceFiles := videodbCloudParseFile(ep.File)
			if len(voiceFiles) > 0 {
				folder := make([]videodbEntry, 0, len(voiceFiles))
				for voice, file := range voiceFiles {
					folder = append(folder, videodbEntry{
						Title: voice,
						File:  file,
					})
				}
				items[i].Folder[j].Folder = folder
				items[i].Folder[j].File = ""
			}
		}
	}

	return videodbEmbed{
		Items:   items,
		Movie:   false,
		Quality: "720p",
	}, true
}

// fetchVideoDBCloudHTML is a shared HTTP helper for videodb.cloud requests.
func (v *videodbChecker) fetchVideoDBCloudHTML(req *http.Request, target string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Referer", "https://kinoflix.tv/")
	httpReq.Header.Set("Accept", "text/html,*/*")

	resp, err := v.cloudClient.Do(httpReq)
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

// videodbCloudDetectQuality detects the best quality from a videodb.cloud file string.
func videodbCloudDetectQuality(fileStr string) string {
	switch {
	case strings.Contains(fileStr, "[4K]"):
		return "2160p"
	case strings.Contains(fileStr, "[FHD]"):
		return "1080p"
	case strings.Contains(fileStr, "[HD]"):
		return "720p"
	default:
		return "480p"
	}
}

// videodbCloudParseFile parses videodb.cloud file format:
// "[HD]{Voice1}url1;{Voice2}url2;,[SD]{Voice1}url3;{Voice2}url4;,"
// Returns map of voice → reassembled file string compatible with videodbParseStreams.
// e.g. {"Грузинский": "[HD]url_hd,[SD]url_sd", "Русский": "[HD]url_hd2,[SD]url_sd2"}
func videodbCloudParseFile(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	// Split by quality groups: "[HD]...,  [SD]..."
	// Find positions of quality tags.
	locs := videodbCloudQualRe.FindAllStringIndex(raw, -1)
	if len(locs) == 0 {
		return nil
	}

	// voice → quality → url
	type qualURL struct {
		qual string
		url  string
	}
	voiceStreams := make(map[string][]qualURL)

	for i, loc := range locs {
		qual := raw[loc[0]+1 : loc[1]-1] // "HD", "SD", etc.
		// Find the segment for this quality group.
		start := loc[1]
		end := len(raw)
		if i+1 < len(locs) {
			// Find the comma separator between quality groups.
			nextStart := locs[i+1][0]
			end = nextStart
		}
		segment := strings.TrimRight(raw[start:end], " ,")

		// Parse {Voice}url pairs within this quality segment.
		for _, m := range videodbCloudEntryRe.FindAllStringSubmatch(segment, -1) {
			if len(m) != 3 {
				continue
			}
			voice := strings.TrimSpace(m[1])
			streamURL := strings.TrimRight(strings.TrimSpace(m[2]), ";,")
			if voice == "" || streamURL == "" {
				continue
			}
			voiceStreams[voice] = append(voiceStreams[voice], qualURL{qual: qual, url: streamURL})
		}
	}

	if len(voiceStreams) == 0 {
		return nil
	}

	// Reassemble: voice → "[HD]url,[SD]url"
	result := make(map[string]string, len(voiceStreams))
	for voice, quals := range voiceStreams {
		parts := make([]string, 0, len(quals))
		for _, q := range quals {
			parts = append(parts, "["+q.qual+"]"+q.url)
		}
		result[voice] = strings.Join(parts, ",")
	}
	return result
}

// videodbMergeEmbeds merges two videodbEmbed results.
// For serials: adds voices from secondary to primary episodes.
// For movies: appends secondary voice rows after primary ones.
func videodbMergeEmbeds(primary, secondary videodbEmbed) videodbEmbed {
	if primary.Movie && secondary.Movie {
		// Movies: append voice rows.
		primary.Items = append(primary.Items, secondary.Items...)
		return primary
	}

	if primary.Movie != secondary.Movie {
		// Type mismatch — just return primary.
		return primary
	}

	// Serials: merge voice folders per episode.
	// Both have the same structure: items[season].Folder[episode].Folder[voice]
	for si, season := range primary.Items {
		if si >= len(secondary.Items) {
			break
		}
		secSeason := secondary.Items[si]
		for ei, ep := range season.Folder {
			if ei >= len(secSeason.Folder) {
				break
			}
			secEp := secSeason.Folder[ei]
			// Collect existing voice names in primary.
			existing := make(map[string]struct{}, len(ep.Folder))
			for _, v := range ep.Folder {
				existing[videodbNormalizeVoice(v.Title)] = struct{}{}
			}
			// Add secondary voices not already present.
			for _, v := range secEp.Folder {
				name := videodbNormalizeVoice(v.Title)
				if _, ok := existing[name]; !ok {
					primary.Items[si].Folder[ei].Folder = append(primary.Items[si].Folder[ei].Folder, v)
				}
			}
		}
	}

	return primary
}

func (v *videodbChecker) probe(req *http.Request, method, target string) bool {
	if strings.TrimSpace(target) == "" {
		return false
	}

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

// proxyWithHeaders wraps a videodb stream URL through /proxy/ with
// Origin/Referer headers that the CDN requires (obrut.show embed origin).
func (v *videodbChecker) proxyWithHeaders(r *http.Request, rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || v.links == nil {
		return rawURL
	}
	if isStreamProxyDisabled(v.plugin) {
		return rawURL
	}
	host := hostFromRequest(r)
	reqIP := clientIP(r)

	// Use the apiHost domain as Origin/Referer — the CDN (cdn-*.obrut.show)
	// returns CORS headers for the embed origin, not the site host.
	apiParsed, err := url.Parse(v.apiHost)
	if err != nil || apiParsed.Host == "" {
		return rawURL
	}
	refDomain := apiParsed.Host

	headers := map[string]string{
		"Origin":  "https://" + refDomain,
		"Referer": "https://" + refDomain + "/",
	}
	encrypted := v.links.EncryptURIWithHeaders(rawURL, reqIP, v.plugin, headers)
	if encrypted == "" {
		return rawURL
	}
	return host + "/proxy/" + encrypted
}

// proxyStreams wraps all stream quality URLs through /proxy/ with proper headers.
// For obrut CDN URLs, uses the manifest endpoint (method:"call") for on-demand
// redirect resolution. For collaps/interkh CDN URLs, wraps directly with collaps headers.
// For videodb.cloud CDN, wraps with kinoflix.tv Referer.
// Returns true if streams contain obrut CDN URLs (determines method:"call" vs "play").
func (v *videodbChecker) proxyStreams(r *http.Request, streams []map[string]string) bool {
	if v.links == nil {
		return false
	}
	host := hostFromRequest(r)
	hasObrut := false
	hasCollaps := false
	hasVideoDBCloud := false

	// Detect CDN type from URLs.
	qualLinks := make(map[string]string, len(streams))
	for _, s := range streams {
		u := s["url"]
		if strings.Contains(u, "obrut.show") || strings.Contains(u, "superdupercdn") {
			hasObrut = true
			label := s["quality"]
			if label == "" {
				label = "auto"
			}
			qualLinks[label] = u
		} else if videodbIsCollapsCDN(u) {
			hasCollaps = true
		} else if videodbIsCloudCDN(u) {
			hasVideoDBCloud = true
		}
	}

	if hasObrut && len(qualLinks) > 0 {
		// Build one manifest URL with all quality links encoded as JSON.
		linksJSON, _ := stdjson.Marshal(qualLinks)
		manifestURL := host + "/lite/" + v.plugin + "/manifest?links=" + url.QueryEscape(string(linksJSON))
		// play=true triggers redirect mode for external players
		manifestPlayURL := manifestURL + "&play=true"

		for i := range streams {
			u := streams[i]["url"]
			if strings.Contains(u, "obrut.show") || strings.Contains(u, "superdupercdn") {
				streams[i]["url"] = manifestURL
				streams[i]["stream"] = manifestPlayURL
			} else {
				streams[i]["url"] = v.proxyWithHeaders(r, u)
			}
		}
	} else if hasCollaps {
		// Collaps CDN (interkh.com): wrap with kinogo.media origin headers.
		interkhHeaders := videodbInterkhStreamHeaders(v.host)
		for i := range streams {
			streams[i]["url"] = streamProxyURLWithHeaders(r, streams[i]["url"], v.plugin, v.links, interkhHeaders)
		}
	} else if hasVideoDBCloud {
		// videodb.cloud CDN: wrap with kinoflix.tv Referer for CORS/geo compat.
		cloudHeaders := videodbCloudStreamHeaders(v.videodbCloud)
		for i := range streams {
			streams[i]["url"] = streamProxyURLWithHeaders(r, streams[i]["url"], v.plugin, v.links, cloudHeaders)
		}
	} else {
		for i := range streams {
			streams[i]["url"] = v.proxyWithHeaders(r, streams[i]["url"])
		}
	}
	return hasObrut
}

// videodbIsCollapsCDN returns true if the URL points to a collaps-style CDN
// (interkh.com or known collaps CDN hosts).
func videodbIsCollapsCDN(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	return strings.Contains(lower, "interkh.com") ||
		strings.Contains(lower, "luxembd.ws") ||
		strings.Contains(lower, "kinokrad.my")
}

// videodbInterkhStreamHeaders returns headers for proxying interkh.com CDN streams.
// The CDN responds with Access-Control-Allow-Origin for the videodb site host.
func videodbInterkhStreamHeaders(siteHost string) map[string]string {
	siteHost = strings.TrimRight(siteHost, "/")
	return map[string]string{
		"Origin":  siteHost,
		"Referer": siteHost + "/",
	}
}

// videodbIsCloudCDN returns true if the URL points to a videodb.cloud CDN host.
func videodbIsCloudCDN(rawURL string) bool {
	return strings.Contains(rawURL, "videodb.cloud")
}

// videodbCloudStreamHeaders returns headers for proxying videodb.cloud CDN streams.
// The CDN expects Referer from the embed host (videodb.cloud) or kinoflix.tv.
func videodbCloudStreamHeaders(cloudHost string) map[string]string {
	cloudHost = strings.TrimRight(cloudHost, "/")
	if cloudHost == "" {
		cloudHost = "https://videodb.cloud"
	}
	return map[string]string{
		"Origin":  cloudHost,
		"Referer": cloudHost + "/",
	}
}

func (v *videodbChecker) resolveLocation(req *http.Request, link string) string {
	baseURL, err := url.Parse(link)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return ""
	}

	resolveClient := httpclient.NewNoRedirect(12 * time.Second)
	try := func(method string) (string, bool) {
		httpReq, err := http.NewRequestWithContext(req.Context(), method, link, nil)
		if err != nil {
			return "", false
		}
		httpReq.Header.Set("sec-fetch-dest", "empty")
		httpReq.Header.Set("sec-fetch-mode", "cors")
		httpReq.Header.Set("sec-fetch-site", "same-site")
		httpReq.Header.Set("origin", strings.TrimRight(v.host, "/"))
		httpReq.Header.Set("referer", strings.TrimRight(v.host, "/")+"/")
		httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
		httpReq.Header.Set("X-Lampac-Go", "1")

		resp, err := resolveClient.Do(httpReq)
		if err != nil {
			return "", false
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := strings.TrimSpace(resp.Header.Get("Location"))
			if loc == "" {
				return "", false
			}
			locURL, err := url.Parse(loc)
			if err != nil {
				return "", false
			}
			return baseURL.ResolveReference(locURL).String(), true
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return link, true
		}
		return "", false
	}

	if location, ok := try(http.MethodHead); ok {
		return location
	}
	if location, ok := try(http.MethodGet); ok {
		return location
	}
	return ""
}
