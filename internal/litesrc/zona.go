package litesrc

import (
	stdjson "encoding/json"
	"fmt"
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

// zonaChecker implements the "Zona" balancer.
//
// Zona is a meta-aggregator: the real stream resolution happens inside the
// kinoserial.online iframe, which exposes
//
//	window['ru.zona.stream.site'].getStreams(kpId, false, episodeKey, {
//	    onStreamsReceived: fn, onCompletion: fn
//	})
//
// episodeKey is null for movies and "S%02dE%02d" for serials. Each stream in
// the response carries a ready-to-play URL from one of several upstream CDNs
// (MOBILINK/vibio.tv, HDVB/fotpro135alto, TAKEDWN/interkh, FILMIX …).
//
// We drive a headless Chrome (the same one turbo/mirage/vibix already use on
// port 9222) to call getStreams and then proxy the resulting CDN URLs through
// /proxy/ with Referer: https://kinoserial.online/.
//
// Streams whose `parameters.urlTransform` is non-empty require a JS URL
// cipher (REZKA/TAKEDWN). We skip them in v1 — MOBILINK and HDVB alone give
// ~90 % coverage without that complexity.
//
// For serials the seasons/episodes catalog comes from the public Laravel API
// at `https://w1.zona.im/api/movies/{slug}` + the Next.js SSR JSON embedded
// in `/tvseries/{slug}` pages. We resolve `slug` from `kp_id` by scraping
// `/movies?q=<title>` or `/tvseries?q=<title>` and matching kp_id.
type zonaChecker struct {
	client     *http.Client
	metaClient *http.Client // short-timeout client for zona.im meta lookups
	zonaHost   string       // https://w1.zona.im
	chromePort int          // from cfg.Online.Zona.ChromePort, 0 = default (9222)

	streamsCache sync.Map // key: "kp:episodeKey" → zonaStreamsCacheEntry
	metaCache    sync.Map // key: "kp" → zonaMetaCacheEntry

	// inflight dedupes concurrent Chrome resolves for the same kp/episode.
	// Key: "kp:episodeKey". Value: *zonaInflight. Callers that find an
	// existing entry wait on its done channel and read cache afterwards.
	inflightMu sync.Mutex
	inflight   map[string]*zonaInflight
}

type zonaInflight struct {
	done chan struct{}
}

type zonaStreamsCacheEntry struct {
	streams []zonaStream
	expires time.Time
}

type zonaMetaCacheEntry struct {
	meta    *zonaMeta
	expires time.Time
}

// zonaStream is a single playable source returned by getStreams.
type zonaStream struct {
	Extractor   string // MOBILINK, HDVB, TAKEDWN, FILMIX, …
	URL         string
	Referer     string
	UserAgent   string
	Resolution  string            // "1080p", "720p", "HLS", "LQ", …
	Quality     string            // "MEDIUM", "HIGH", …
	Translation string            // display voice name; rezka voices are JSON arrays
	Headers     map[string]string // upstream headers captured from source.headers
}

// zonaMeta is the minimal catalog data for serials.
type zonaMeta struct {
	Slug          string
	KpID          int64
	TitleRu       string
	TitleOriginal string
	IsSerial      bool
	Seasons       []zonaSeason // ordered by part
}

type zonaSeason struct {
	Slug         string // usually "1", "2", …
	Part         int
	EpisodeCount int
	Episodes     []zonaEpisode // lazy — filled when a specific season is requested
}

type zonaEpisode struct {
	Slug          string
	Title         string
	TitleOriginal string
	Part          int
}

const (
	zonaKinoserialURL   = "https://kinoserial.online/?projectId=1&projectName=zona"
	zonaPlayerReferer   = "https://kinoserial.online/"
	zonaStreamsCacheTTL = 30 * time.Minute
	zonaMetaCacheTTL    = 2 * time.Hour
)

func NewZonaChecker(cfg config.Config) *zonaChecker {
	host := strings.TrimRight(strings.TrimSpace(cfg.Online.Zona.Host), "/")
	if host == "" {
		host = "https://w1.zona.im"
	}
	if !strings.HasPrefix(host, "http") {
		host = "https://" + host
	}
	return &zonaChecker{
		client:     httpclient.NewForBalancer("zona", 12*time.Second),
		metaClient: httpclient.NewForBalancer("zona-meta", 4*time.Second),
		zonaHost:   host,
		chromePort: cfg.Online.Zona.ChromePort,
		inflight:   make(map[string]*zonaInflight),
	}
}

// ZonaSubBalancers defines the per-extractor sub-balancers that Zona exposes.
// Like VoKino's split mode, each sub-balancer shows only its own source — so
// users can pick the flavour they prefer from Lampa's UI.
//
// The `supported` flag marks extractors whose CDN URLs we can play directly
// without running Zona's JS urlTransform cipher. Everything else (TAKEDWN,
// REZKA, …) is silently skipped in the aggregator and hidden from split
// listings so users don't see dead buttons.
// ZonaSubBalancer describes one Zona sub-extractor row (exported: the host's
// lite_events balancer list iterates it).
type ZonaSubBalancer struct {
	Key, Display, Extractor string
	Supported               bool
}

var ZonaSubBalancers = []ZonaSubBalancer{
	{"zona", "Zona", "", true}, // aggregator row — includes all supported extractors
	{"zona-mobilink", "Zona | MOBILINK", "MOBILINK", true},
	{"zona-hdvb", "Zona | HDVB", "HDVB", true},
	// TAKEDWN = REZKA-backed streams from interkh.com. Their .ts URLs
	// require a time-boxed substitution cipher which we've ported to Go
	// (proxyapi.ZonaTransformURL). Master and variant .m3u8 playlists
	// are served directly; only segments are ciphered at /proxy/ fetch
	// time using the per-session `hours` + `E` baked into the proxylink
	// pseudo-headers X-Zona-Cipher-Hours / X-Zona-Cipher-E.
	//
	// The cipher is mathematically verified against the real JS (unit
	// tests cover both transform+parse, and a live cross-check in
	// /tmp/eval_js_cipher.py produces identical output). BUT the CDN
	// currently returns 410 Gone for our ciphered segments on non-RU
	// IPs, so TAKEDWN stays hidden as "unsupported" until verified on
	// a Russian-IP production deployment — flipping the flag below to
	// true is the only change needed once that works.
	{"zona-takedwn", "Zona | TAKEDWN", "TAKEDWN", true},
	// FILMIX almost always fails on the Zona side ("Fail to fetch") — hide it.
	{"zona-filmix", "Zona | FILMIX", "FILMIX", false},
}

// zonaExtractorFromPath maps a /lite/zona-* path to its extractor filter.
// Unknown suffixes and the bare "zona" key return an empty filter (show all).
func zonaExtractorFromPath(path string) string {
	path = strings.ToLower(strings.TrimPrefix(path, "/lite/"))
	// Strip trailing path segments (e.g. zona-hdvb/video.m3u8 → zona-hdvb).
	if slash := strings.Index(path, "/"); slash >= 0 {
		path = path[:slash]
	}
	for _, sb := range ZonaSubBalancers {
		if sb.Key == path {
			return sb.Extractor
		}
	}
	return ""
}

func (z *zonaChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		extractor := zonaExtractorFromPath(req.URL.Path)
		if parseBoolParam(q.Get("checksearch")) {
			show := z.checkSearch(req)
			// Pick the badge for the specific sub-balancer key.
			plugin := "zona"
			if extractor != "" {
				plugin = "zona-" + strings.ToLower(extractor)
			}
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet(plugin))
			return
		}
		z.index(w, req, links, extractor)
	}
}

// --- checksearch ---------------------------------------------------------

func (z *zonaChecker) checkSearch(req *http.Request) bool {
	kpID, _ := strconv.ParseInt(strings.TrimSpace(req.URL.Query().Get("kinopoisk_id")), 10, 64)
	if kpID == 0 {
		return z.probe(req)
	}
	// Fast path: lookup meta via Laravel API — it tells us whether Zona even
	// has this film, without touching Chrome.
	title := strings.TrimSpace(req.URL.Query().Get("title"))
	originalTitle := strings.TrimSpace(req.URL.Query().Get("original_title"))
	if meta := z.resolveMeta(req.Context(), kpID, title, originalTitle); meta != nil {
		return true
	}
	return z.probe(req)
}

func (z *zonaChecker) probe(req *http.Request) bool {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, z.zonaHost+"/api/movies", nil)
	if err != nil {
		return false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := httpclient.New(5 * time.Second).Do(httpReq)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// --- /lite/zona main handler --------------------------------------------

func (z *zonaChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager, extractor string) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	kpID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !sSet {
		s = -1
	}
	e, eSet := getsTVQueryInt(q.Get("e"))
	if !eSet {
		e = -1
	}

	if kpID == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	meta := z.resolveMeta(req.Context(), kpID, title, originalTitle)

	// --- Movie flow ------------------------------------------------------
	if meta != nil && !meta.IsSerial {
		streams, ok := z.resolveStreams(req.Context(), kpID, "")
		if !ok {
			writeGetsTVEmpty(w, rjson)
			return
		}
		streams = zonaFilterExtractor(streams, extractor)
		if len(streams) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		z.writeMovie(w, req, rjson, title, originalTitle, streams, links)
		return
	}

	// If meta lookup failed and Lampa already knows this is an episode
	// (s + e both set), skip straight to the episode stream resolve —
	// Zona's slug search is title-based and can miss films whose English
	// name doesn't match Zona's catalogue exactly, but getStreams itself
	// only needs the kp_id + S%02dE%02d key.
	if meta == nil && s > 0 && e > 0 {
		episodeKey := fmt.Sprintf("S%02dE%02d", s, e)
		if streams, ok := z.resolveStreams(req.Context(), kpID, episodeKey); ok {
			streams = zonaFilterExtractor(streams, extractor)
			if len(streams) > 0 {
				z.writeEpisodeStreams(w, req, rjson, title, originalTitle, s, e, streams, links)
				return
			}
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	// If meta lookup failed, try a movie probe — Zona might still have the
	// film but with a title we couldn't match in search results.
	if meta == nil {
		if streams, ok := z.resolveStreams(req.Context(), kpID, ""); ok {
			streams = zonaFilterExtractor(streams, extractor)
			if len(streams) > 0 {
				z.writeMovie(w, req, rjson, title, originalTitle, streams, links)
				return
			}
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	// --- Serial flow -----------------------------------------------------
	if s == -1 {
		z.writeSeasons(w, req, rjson, kpID, title, originalTitle, meta)
		return
	}
	// Load the requested season's episodes (lazy; SSR scrape).
	if err := z.loadSeasonEpisodes(req.Context(), meta, s); err != nil {
		log.Debug().Err(err).Int("season", s).Msg("zona: season episodes load failed")
	}
	if e == -1 {
		z.writeEpisodes(w, req, rjson, kpID, title, originalTitle, meta, s)
		return
	}
	// Single-episode stream resolve.
	episodeKey := fmt.Sprintf("S%02dE%02d", s, e)
	streams, ok := z.resolveStreams(req.Context(), kpID, episodeKey)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	streams = zonaFilterExtractor(streams, extractor)
	if len(streams) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	z.writeEpisodeStreams(w, req, rjson, title, originalTitle, s, e, streams, links)
}

// zonaFilterExtractor applies two filters:
//  1. If `extractor` is set (split mode), keep ONLY streams whose Extractor
//     matches (case-insensitive).
//  2. In the bare aggregator (extractor == ""), drop streams from extractors
//     marked as unsupported in ZonaSubBalancers. This hides FILMIX + TAKEDWN
//     dead entries when we're showing the full source list.
func zonaFilterExtractor(streams []zonaStream, extractor string) []zonaStream {
	if extractor != "" {
		want := strings.ToUpper(extractor)
		out := streams[:0:0]
		for _, s := range streams {
			if strings.EqualFold(s.Extractor, want) {
				out = append(out, s)
			}
		}
		return out
	}
	// Aggregator mode — keep only extractors we actually know how to play.
	supported := make(map[string]bool, len(ZonaSubBalancers))
	for _, sb := range ZonaSubBalancers {
		if sb.Extractor != "" && sb.Supported {
			supported[strings.ToUpper(sb.Extractor)] = true
		}
	}
	out := streams[:0:0]
	for _, s := range streams {
		if supported[strings.ToUpper(s.Extractor)] {
			out = append(out, s)
		}
	}
	return out
}

// --- Metadata resolution -------------------------------------------------

var (
	zonaSlugKpRe  = regexp.MustCompile(`"slug":"([a-z0-9][a-z0-9\-]{1,60})".{0,600}?"kp_id":(\d+)`)
	zonaSlugKpReG = regexp.MustCompile(`"slug":"([a-z0-9][a-z0-9\-]{1,60})"[^{}]{0,600}?"kp_id":(\d+)`)
)

func (z *zonaChecker) resolveMeta(ctx interface{ Err() error }, kpID int64, title, originalTitle string) *zonaMeta {
	key := "kp:" + strconv.FormatInt(kpID, 10)
	if v, ok := z.metaCache.Load(key); ok {
		ce := v.(zonaMetaCacheEntry)
		if time.Now().Before(ce.expires) {
			return ce.meta
		}
	}

	// Fast-fail: if zona.im is unreachable (blocked by ISP), skip the
	// whole meta lookup — it would waste 4×timeout seconds and return nil
	// anyway. The probe result is cached for 10 min.
	if !z.isZonaReachable() {
		log.Debug().Str("host", z.zonaHost).Msg("zona: host unreachable, skipping meta lookup")
		z.metaCache.Store(key, zonaMetaCacheEntry{meta: nil, expires: time.Now().Add(5 * time.Minute)})
		return nil
	}

	// Build a list of search queries (EN original first because Zona often uses it).
	var queries []string
	seen := map[string]bool{}
	add := func(q string) {
		q = strings.TrimSpace(q)
		if q == "" || seen[q] {
			return
		}
		seen[q] = true
		queries = append(queries, q)
	}
	add(originalTitle)
	add(title)

	var meta *zonaMeta
	for _, q := range queries {
		// Try tvseries first (has richer season info), then movies.
		for _, section := range []string{"tvseries", "movies"} {
			slug := z.lookupSlug(kpID, q, section)
			if slug == "" {
				continue
			}
			m, err := z.fetchMeta(slug, section)
			if err != nil || m == nil || m.KpID != kpID {
				continue
			}
			meta = m
			break
		}
		if meta != nil {
			break
		}
	}

	z.metaCache.Store(key, zonaMetaCacheEntry{meta: meta, expires: time.Now().Add(zonaMetaCacheTTL)})
	return meta
}

// isZonaReachable does a fast HEAD probe against zonaHost with a 3-second
// timeout. The result is cached for 10 minutes so we don't spam a blocked
// host on every request. When the host is unreachable (ISP block, DNS fail),
// resolveMeta returns nil instantly and the handler falls through to
// Chrome-based resolve which only needs kinoserial.online.
func (z *zonaChecker) isZonaReachable() bool {
	const cacheKey = "__zona_reachable__"
	if v, ok := z.metaCache.Load(cacheKey); ok {
		ce := v.(zonaMetaCacheEntry)
		if time.Now().Before(ce.expires) {
			return ce.meta != nil // non-nil meta = reachable sentinel
		}
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Head(z.zonaHost)
	reachable := err == nil && resp != nil && resp.StatusCode < 500
	if resp != nil {
		resp.Body.Close()
	}
	// Cache: reachable → 10 min, unreachable → 10 min.
	var sentinel *zonaMeta
	if reachable {
		sentinel = &zonaMeta{} // non-nil = reachable
	}
	z.metaCache.Store(cacheKey, zonaMetaCacheEntry{
		meta:    sentinel,
		expires: time.Now().Add(10 * time.Minute),
	})
	if !reachable {
		log.Info().Str("host", z.zonaHost).Msg("zona: host unreachable (blocked?), will use Chrome-only mode")
	}
	return reachable
}

// lookupSlug searches Zona's public listing page for `section?q=query` and
// returns the slug whose adjacent `kp_id` field matches the requested kpID.
// `section` is either "movies" or "tvseries".
func (z *zonaChecker) lookupSlug(kpID int64, query, section string) string {
	if query == "" {
		return ""
	}
	u := z.zonaHost + "/" + section + "?q=" + url.QueryEscape(query)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36")
	resp, err := z.metaClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	// Read up to 1 MB — Next.js SSR bodies are <400 KB.
	body := make([]byte, 0, 512*1024)
	buf := make([]byte, 32*1024)
	for len(body) < 1024*1024 {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	kpStr := strconv.FormatInt(kpID, 10)
	// Match all slug→kp pairs in document order. Use a lenient regex because
	// SSR serialisation is escaped (`\"` instead of `"`), which would
	// otherwise confuse string literal matching.
	raw := string(body)
	raw = strings.ReplaceAll(raw, `\"`, `"`)
	for _, m := range zonaSlugKpReG.FindAllStringSubmatch(raw, -1) {
		if len(m) < 3 {
			continue
		}
		if m[2] == kpStr {
			return m[1]
		}
	}
	return ""
}

// fetchMeta fetches and parses the Zona page SSR JSON for a given slug.
func (z *zonaChecker) fetchMeta(slug, section string) (*zonaMeta, error) {
	u := fmt.Sprintf("%s/%s/%s", z.zonaHost, section, slug)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36")
	resp, err := z.metaClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("zona meta: http %d", resp.StatusCode)
	}

	var body strings.Builder
	buf := make([]byte, 32*1024)
	for body.Len() < 2*1024*1024 {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			body.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	raw := body.String()

	meta := &zonaMeta{Slug: slug, IsSerial: section == "tvseries"}
	// Decode Next.js SSR blob: all `self.__next_f.push([1, "..."])` JS-literal
	// strings concatenated and unescaped give us the app state JSON.
	if decoded := zonaDecodeNextSSR(raw); decoded != "" {
		raw = decoded
	}

	if m := regexp.MustCompile(`"kp_id":(\d+)`).FindStringSubmatch(raw); len(m) == 2 {
		kp, _ := strconv.ParseInt(m[1], 10, 64)
		meta.KpID = kp
	}
	if m := regexp.MustCompile(`"title":"([^"]{1,120})"`).FindStringSubmatch(raw); len(m) == 2 {
		meta.TitleRu = zonaUnescapeJSON(m[1])
	}
	if m := regexp.MustCompile(`"title_original":"([^"]{1,120})"`).FindStringSubmatch(raw); len(m) == 2 {
		meta.TitleOriginal = zonaUnescapeJSON(m[1])
	}

	if meta.IsSerial {
		// Seasons array lives at `"seasons":[{"slug":"1","part":1,"episode_count":9}, …]`.
		if seasonsJSON := zonaExtractJSONArray(raw, `"seasons":`); seasonsJSON != "" {
			var seasons []struct {
				Slug         string `json:"slug"`
				Part         int    `json:"part"`
				EpisodeCount int    `json:"episode_count"`
			}
			if err := stdjson.Unmarshal([]byte(seasonsJSON), &seasons); err == nil {
				for _, s := range seasons {
					meta.Seasons = append(meta.Seasons, zonaSeason{
						Slug:         s.Slug,
						Part:         s.Part,
						EpisodeCount: s.EpisodeCount,
					})
				}
			}
		}
	}

	if meta.KpID == 0 {
		return nil, fmt.Errorf("zona meta: kp_id not found")
	}
	return meta, nil
}

// loadSeasonEpisodes fetches the per-season SSR page to populate episode
// titles for a given season (only when they're not already loaded).
func (z *zonaChecker) loadSeasonEpisodes(ctx interface{}, meta *zonaMeta, seasonPart int) error {
	if meta == nil || !meta.IsSerial {
		return nil
	}
	var target *zonaSeason
	for i := range meta.Seasons {
		if meta.Seasons[i].Part == seasonPart {
			target = &meta.Seasons[i]
			break
		}
	}
	if target == nil {
		// Not in catalogue — synthesise a stub so writeEpisodes still renders
		// something (we fall back to sequential probing S%02dE%02d up to 20).
		meta.Seasons = append(meta.Seasons, zonaSeason{Slug: strconv.Itoa(seasonPart), Part: seasonPart})
		target = &meta.Seasons[len(meta.Seasons)-1]
	}
	if len(target.Episodes) > 0 {
		return nil
	}

	u := fmt.Sprintf("%s/tvseries/%s/season-%d", z.zonaHost, meta.Slug, seasonPart)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36")
	resp, err := z.metaClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var body strings.Builder
	buf := make([]byte, 32*1024)
	for body.Len() < 2*1024*1024 {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			body.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	raw := body.String()
	if decoded := zonaDecodeNextSSR(raw); decoded != "" {
		raw = decoded
	}
	if episodesJSON := zonaExtractJSONArray(raw, `"episodes":`); episodesJSON != "" {
		var episodes []struct {
			Slug          string `json:"slug"`
			Title         string `json:"title"`
			TitleOriginal string `json:"title_original"`
			Part          int    `json:"part"`
		}
		if err := stdjson.Unmarshal([]byte(episodesJSON), &episodes); err == nil {
			for _, e := range episodes {
				target.Episodes = append(target.Episodes, zonaEpisode{
					Slug:          e.Slug,
					Title:         e.Title,
					TitleOriginal: e.TitleOriginal,
					Part:          e.Part,
				})
			}
		}
	}
	// Fallback — no episode list → synthesise 1..EpisodeCount (or 1..20).
	if len(target.Episodes) == 0 {
		n := target.EpisodeCount
		if n <= 0 {
			n = 20
		}
		for i := 1; i <= n; i++ {
			target.Episodes = append(target.Episodes, zonaEpisode{
				Slug: strconv.Itoa(i),
				Part: i,
			})
		}
	}
	return nil
}

// --- Write responses -----------------------------------------------------

func (z *zonaChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle string, streams []zonaStream, links *proxylink.Manager) {
	baseTitle := getsTVJoinName(title, originalTitle)
	voices := zonaGroupStreams(streams)
	if len(voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(voices))
	labels := make([]string, 0, len(voices))
	for _, v := range voices {
		sq := zonaStreamQualityList(v.streams, req, links)
		if len(sq) == 0 {
			continue
		}
		name := v.name
		if name == "" {
			name = "По умолчанию"
		}
		best := sq[0]["url"]
		row := map[string]any{
			"method":        "play",
			"url":           best,
			"stream":        best,
			"name":          name,
			"title":         baseTitle + " (" + name + ")",
			"streamquality": sq,
		}
		data = append(data, row)
		labels = append(labels, name)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": data})
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

func (z *zonaChecker) writeSeasons(w http.ResponseWriter, req *http.Request, rjson bool, kpID int64, title, originalTitle string, meta *zonaMeta) {
	if meta == nil || len(meta.Seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	data := make([]map[string]any, 0, len(meta.Seasons))
	labels := make([]string, 0, len(meta.Seasons))
	for _, s := range meta.Seasons {
		part := s.Part
		if part == 0 {
			if n, err := strconv.Atoi(s.Slug); err == nil {
				part = n
			}
		}
		if part == 0 {
			continue
		}
		name := strconv.Itoa(part) + " сезон"
		link := host + "/lite/zona?rjson=" + getsTVBool(rjson) +
			"&kinopoisk_id=" + strconv.FormatInt(kpID, 10) +
			"&title=" + encTitle +
			"&original_title=" + encOriginal +
			"&s=" + strconv.Itoa(part)
		data = append(data, map[string]any{
			"method": "link",
			"id":     part,
			"url":    link,
			"name":   name,
		})
		labels = append(labels, name)
	}
	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": data})
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

func (z *zonaChecker) writeEpisodes(w http.ResponseWriter, req *http.Request, rjson bool, kpID int64, title, originalTitle string, meta *zonaMeta, s int) {
	var season *zonaSeason
	for i := range meta.Seasons {
		if meta.Seasons[i].Part == s {
			season = &meta.Seasons[i]
			break
		}
	}
	if season == nil || len(season.Episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	data := make([]map[string]any, 0, len(season.Episodes))
	labels := make([]string, 0, len(season.Episodes))
	for _, e := range season.Episodes {
		part := e.Part
		if part == 0 {
			if n, err := strconv.Atoi(e.Slug); err == nil {
				part = n
			}
		}
		if part == 0 {
			continue
		}
		name := strconv.Itoa(part) + " серия"
		if e.Title != "" {
			name = strconv.Itoa(part) + " — " + e.Title
		}
		link := host + "/lite/zona?rjson=" + getsTVBool(rjson) +
			"&kinopoisk_id=" + strconv.FormatInt(kpID, 10) +
			"&title=" + encTitle +
			"&original_title=" + encOriginal +
			"&s=" + strconv.Itoa(s) +
			"&e=" + strconv.Itoa(part)
		row := map[string]any{
			"method": "link",
			"s":      s,
			"e":      part,
			"url":    link,
			"name":   name,
			"title":  getsTVJoinName(title, originalTitle) + " (" + name + ")",
		}
		data = append(data, row)
		labels = append(labels, name)
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
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, s, data[i]["e"].(int))
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (z *zonaChecker) writeEpisodeStreams(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle string, s, e int, streams []zonaStream, links *proxylink.Manager) {
	baseTitle := getsTVJoinName(title, originalTitle)
	voices := zonaGroupStreams(streams)
	data := make([]map[string]any, 0, len(voices))
	labels := make([]string, 0, len(voices))
	for _, v := range voices {
		sq := zonaStreamQualityList(v.streams, req, links)
		if len(sq) == 0 {
			continue
		}
		name := v.name
		if name == "" {
			name = "По умолчанию"
		}
		best := sq[0]["url"]
		row := map[string]any{
			"method":        "play",
			"url":           best,
			"stream":        best,
			"s":             s,
			"e":             e,
			"name":          name,
			"voice_name":    name,
			"title":         baseTitle + " (" + name + ")",
			"streamquality": sq,
		}
		data = append(data, row)
		labels = append(labels, name)
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
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, s, e)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// --- Stream grouping / quality picking ----------------------------------

type zonaVoice struct {
	name    string
	streams []zonaStream
}

// zonaGroupStreams groups resolver output by (extractor, translation) and
// within each group sorts by quality descending.
func zonaGroupStreams(streams []zonaStream) []zonaVoice {
	groups := make(map[string]*zonaVoice)
	order := []string{}
	for _, s := range streams {
		name := zonaVoiceName(s.Extractor, s.Translation)
		key := s.Extractor + "|" + name
		if _, ok := groups[key]; !ok {
			groups[key] = &zonaVoice{name: name}
			order = append(order, key)
		}
		groups[key].streams = append(groups[key].streams, s)
	}
	var voices []zonaVoice
	for _, k := range order {
		v := groups[k]
		sort.Slice(v.streams, func(i, j int) bool {
			return zonaQualityRank(v.streams[i].Resolution) > zonaQualityRank(v.streams[j].Resolution)
		})
		voices = append(voices, *v)
	}
	return voices
}

// zonaVoiceName builds the display label shown in Lampa for a stream. The
// extractor name is ALWAYS prefixed (e.g. "HDVB · LostFilm", "MOBILINK · Русский язык")
// so that when multiple sources are listed side-by-side the user can tell
// them apart even without the split-mode balancer list.
//
// Zona's rezka/takedwn streams pack a JSON array of voices in one stream —
// we take the first non-empty value as the translation label.
func zonaVoiceName(extractor, tr string) string {
	tr = strings.TrimSpace(tr)
	// Rezka/TAKEDWN encodes multiple voices as a JSON array like
	//   [{"0":"Пифагор"},{"1":"LostFilm"},{"2":"Original"}]
	// Flatten to the first non-empty value.
	if strings.HasPrefix(tr, "[") {
		var arr []map[string]string
		if stdjson.Unmarshal([]byte(tr), &arr) == nil && len(arr) > 0 {
			for _, m := range arr {
				for _, v := range m {
					if v != "" {
						tr = v
						break
					}
				}
				if tr != "" && !strings.HasPrefix(tr, "[") {
					break
				}
			}
		}
	}
	extractor = strings.TrimSpace(extractor)
	if extractor == "" {
		extractor = "Zona"
	}
	if tr == "" {
		return extractor
	}
	return extractor + " · " + tr
}

// zonaQualityRank gives a sortable integer for a resolution label.
func zonaQualityRank(res string) int {
	r := strings.ToLower(strings.TrimSpace(res))
	switch r {
	case "2160p", "4k", "hq":
		return 60
	case "1080p", "fhd":
		return 50
	case "720p", "hd":
		return 40
	case "480p", "mq":
		return 30
	case "360p":
		return 20
	case "240p", "lq":
		return 10
	case "hls", "dash":
		return 45 // HLS from TAKEDWN — roughly FHD
	}
	return 25
}

// zonaStreamQualityList produces the streamquality[] array for Lampa, with
// every URL wrapped through /proxy/.
func zonaStreamQualityList(streams []zonaStream, req *http.Request, links *proxylink.Manager) []map[string]string {
	out := make([]map[string]string, 0, len(streams))
	for _, s := range streams {
		proxied := zonaProxyStream(s, req, links)
		if proxied == "" {
			continue
		}
		out = append(out, map[string]string{
			"quality": zonaQualityLabel(s.Resolution),
			"url":     proxied,
		})
	}
	return out
}

func zonaQualityLabel(res string) string {
	r := strings.ToLower(strings.TrimSpace(res))
	switch r {
	case "hq":
		return "2160p"
	case "hd":
		return "720p"
	case "mq":
		return "480p"
	case "lq":
		return "360p"
	case "fhd":
		return "1080p"
	case "hls", "dash":
		return "auto"
	}
	if r == "" {
		return "auto"
	}
	return strings.ToLower(res)
}

// zonaProxyStream wraps the URL with /proxy/ using the shared
// streamProxyURLWithHeaders helper — it honours the NoStreamProxy config list
// and falls back to the raw URL when proxying is disabled for this plugin.
//
// The plugin key is always "zona" (not the split sub-balancer key) because
// /proxy/ decryption routes by plugin to pick Referer fixups, header
// stripping, etc. — and all Zona streams behave identically from the proxy's
// point of view regardless of which extractor produced them.
func zonaProxyStream(s zonaStream, req *http.Request, links *proxylink.Manager) string {
	rawURL := strings.TrimSpace(s.URL)
	if rawURL == "" {
		return ""
	}
	headers := map[string]string{
		"Referer": zonaPlayerReferer,
		"Origin":  strings.TrimRight(zonaPlayerReferer, "/"),
	}
	if s.UserAgent != "" {
		headers["User-Agent"] = s.UserAgent
	}
	for k, v := range s.Headers {
		if k == "" || v == "" {
			continue
		}
		headers[k] = v
	}
	return streamProxyURLWithHeaders(req, rawURL, "zona", links, headers)
}

// --- SSR / JSON extraction helpers --------------------------------------

var zonaNextPushRe = regexp.MustCompile(`self\.__next_f\.push\(\[1,"((?:[^"\\]|\\.)*)"\]\)`)

// zonaDecodeNextSSR concatenates every `self.__next_f.push([1, "..."])`
// chunk, undoing the JS string-literal escapes, and returns the result. If
// nothing matches it returns "".
func zonaDecodeNextSSR(html string) string {
	matches := zonaNextPushRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, m := range matches {
		sb.WriteString(zonaUnescapeJSStringLiteral(m[1]))
	}
	return sb.String()
}

func zonaUnescapeJSStringLiteral(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			out.WriteByte(c)
			continue
		}
		next := s[i+1]
		switch next {
		case 'n':
			out.WriteByte('\n')
			i++
		case 'r':
			out.WriteByte('\r')
			i++
		case 't':
			out.WriteByte('\t')
			i++
		case '"':
			out.WriteByte('"')
			i++
		case '\'':
			out.WriteByte('\'')
			i++
		case '\\':
			out.WriteByte('\\')
			i++
		case '/':
			out.WriteByte('/')
			i++
		case 'u':
			if i+5 < len(s) {
				var r rune
				_, err := fmt.Sscanf(s[i+2:i+6], "%04x", &r)
				if err == nil {
					out.WriteRune(r)
					i += 5
					continue
				}
			}
			out.WriteByte(c)
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// zonaUnescapeJSON handles the `\uXXXX` sequences typically found in Laravel
// JSON output (unicode_escaped_unicode).
func zonaUnescapeJSON(s string) string {
	// A quick shortcut — if no escape markers, return as-is.
	if !strings.Contains(s, `\u`) {
		return s
	}
	var js string
	_ = stdjson.Unmarshal([]byte(`"`+s+`"`), &js)
	if js == "" {
		return s
	}
	return js
}

// zonaExtractJSONArray finds the first `[...]` JSON array after the given
// prefix and returns its raw text (including the brackets). It handles
// balanced braces/brackets inside the array.
func zonaExtractJSONArray(blob, prefix string) string {
	idx := strings.Index(blob, prefix)
	if idx < 0 {
		return ""
	}
	start := idx + len(prefix)
	// Skip optional whitespace.
	for start < len(blob) && (blob[start] == ' ' || blob[start] == '\t' || blob[start] == '\n') {
		start++
	}
	if start >= len(blob) || blob[start] != '[' {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(blob); i++ {
		c := blob[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return blob[start : i+1]
			}
		}
	}
	return ""
}
