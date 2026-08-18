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

// fancdnChecker is FanCDN aka xVideoCDN. It was originally fanserial.me with
// DLE auth + reCAPTCHA v3 + image_verify captcha + IP-bans + browser-
// register subprocess — a constantly breaking pile of workarounds. We then
// found out the real backend behind the xVideoCDN .NET plugin (and behind
// the iframe that fanserial.me was rendering all along) is lomont.site,
// which answers with NO authentication at all:
//
//	GET https://lomont.site/gt/{kinopoisk_id}
//	→ HTML with <div id="inputData" data-*=…>{JSON_TREE}</div>
//	  JSON shape: map[season_str]map[episode_str][]fancdnRawVoice
//
//	GET https://lomont.site/player/responce.php?video_id={X}
//	→ {"src":"https://sN.lomont.site/v/<sig>/<ts>/<vid>/index.m3u8","spare":…}
//
// The HLS src URL is IP-bound (the CDN ties signature to caller IP via the
// `<sig>/<ts>` path segments), so segments MUST be replayed to the user
// through /proxy/ from the same egress IP that called responce.php. When
// socks_proxy is configured both legs route through it; otherwise both
// stay on the server's direct egress.
//
// Lazy resolution: the index never calls responce.php — every row's stream
// URL points to /lite/fancdn/video.m3u8?vid={X}, resolved on play. This
// keeps a 1000-episode serial cheap to browse (no upfront 1000 HTTP
// requests to upstream).
//
// Routes /lite/xvideocdn* are aliases (the original .NET plugin name) so
// configs that already reference xvideocdn keep working.
type fancdnChecker struct {
	client    *http.Client
	host      string
	userAgent string

	treeCache sync.Map // kpID(int) → *fancdnTreeEntry
	geo       fancdnGeoState
}

type fancdnTreeEntry struct {
	tree     *fancdnTree
	cachedAt time.Time
}

// fancdnGeoState tracks whether the server's egress IP appears to be
// geo-blocked by lomont.site. Set to true the first time we see the
// "200 OK + body says 404" stub pattern; cleared (back to false) the
// first time we see a real catalogue page. Surfaced via the once-per-
// 5-minute warning log so admins notice the misconfiguration.
type fancdnGeoState struct {
	mu         sync.Mutex
	blocked    bool
	lastWarnAt time.Time
}

const (
	fancdnTreeTTLHit   = 4 * time.Hour
	fancdnTreeTTLMiss  = 5 * time.Minute
	fancdnHTMLMaxBytes = 8 << 20 // huge serials like One Piece are ~550 KB; cap at 8 MB
)

type fancdnTree struct {
	kpID     int
	imdbID   int
	isMovie  bool
	seasons  []int                            // sorted season numbers
	episodes map[int][]int                    // season → sorted episode numbers (negative first, then positive)
	voices   map[int]map[int][]fancdnRawVoice // season → episode → voices (sorted by voice_id)
	// voiceLabels lists unique (voice_id, voice_name) pairs found anywhere
	// in the tree, sorted by id, so the per-season episode list can render
	// voice tabs even when one voice is incomplete in some episodes.
	voiceLabels []fancdnVoiceLabel
}

type fancdnRawVoice struct {
	VideoID   int    `json:"video_id"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	VoiceName string `json:"voice_name"`
	VoiceID   int    `json:"voice_id"`
	Duration  int    `json:"duration"`
}

type fancdnVoiceLabel struct {
	ID   int
	Name string
}

// fancdnInputDataRe captures the JSON body of <div id="inputData" ...>X</div>.
// X is one of:
//   - `{...}` object (serial / season-only / single-voice shapes)
//   - `[...]` array (single-video movie shape)
//
// The div also carries `data-film='{"kp_id":…}'` and other data-* params;
// those are matched by separate regexes that don't conflict because they
// match attributes (`=`), not text content (`>`).
var fancdnInputDataRe = regexp.MustCompile(`(?s)<div id="inputData"[^>]*>(\{.*?\}|\[.*?\])\s*</div>`)

// fancdnDataFilmRe pulls kp_id / imdb_id from the data-film attribute so
// we can verify the page truly belongs to the kp we asked for (defends
// against any future redirect behaviour where /gt/{wrong_kp} returns a
// different page silently).
var fancdnDataFilmRe = regexp.MustCompile(`data-film=['"]\{[^}]*"kp_id":\s*(\d+)(?:[^}]*"imdb_id":\s*(\d+))?[^}]*\}['"]`)

// fancdnContentTypeRe pulls the `data-content-type="N"` attribute. N=0 has
// been observed for serials; non-zero alone isn't enough to declare a movie
// (some content-type bits are unrelated), so it only nudges the heuristic.
var fancdnContentTypeRe = regexp.MustCompile(`data-content-type="(\d+)"`)

// fancdnGeoBlockRe matches lomont.site's "200 OK but body says 404 Not Found"
// geo-block pattern. lomont uses a tiny (<2 KB) status-200 stub for IPs
// outside the catalogue's allowed regions, which would otherwise pass the
// HTTP status check and reach the inputData parser as a confusing "no
// content" miss. Detecting this lets us emit a loud "configure socks_proxy"
// warning at log time instead of silently negative-caching.
var fancdnGeoBlockRe = regexp.MustCompile(`(?s)<title>\s*404\s*Not\s*Found\s*</title>.*<center><h1>\s*404\s*Not\s*Found`)

// fancdnLegacyHosts lists hostnames the balancer used to point at (back when
// it was fanserial.me with DLE auth) or any other dead hosts we want to
// silently redirect to the current live mirror, so users with stale TOML
// keep working without manual edits.
var fancdnLegacyHosts = []string{
	"fanserial.me",
	"fanserial.cc",
	"fanserial.tv",
	"fanserial.io",
	"fanserial.net",
	"1fanserials.com",
	"1fanserials.net",
	"r.xsmart.tv", // the closed xsmart API the .NET plugin used; we go direct now
}

func NewFancdnChecker(cfg config.Config) *fancdnChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.FanCDN.Host, "/"))
	legacy := false
	for _, d := range fancdnLegacyHosts {
		if host != "" && strings.Contains(host, d) {
			legacy = true
			break
		}
	}
	if host == "" || legacy {
		host = "https://lomont.site"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	ua := strings.TrimSpace(cfg.Online.FanCDN.UserAgent)
	if ua == "" {
		ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	}
	// Stream-source URLs from /player/responce.php are IP-bound; segments
	// served by sN.lomont.site refuse any IP other than the one that
	// signed the manifest. If a socks_proxy is configured we route BOTH
	// /gt index probes AND /proxy/ stream replays through it so the
	// egress IP stays consistent across both legs.
	socks := strings.TrimSpace(cfg.Online.FanCDN.SocksProxy)
	var client *http.Client
	if socks != "" {
		httpclient.RegisterProxiedBalancer(socks, []string{"fancdn"})
		client = httpclient.NewUTLSForBalancer("fancdn", 15*time.Second)
		log.Info().Str("socks", socks).Msg("fancdn: routing lomont.site + CDN through SOCKS5 (IP-bind safe)")
	} else {
		httpclient.RegisterRoundTripper("fancdn", httpclient.UTLSTransportIPv4)
		client = httpclient.NewUTLSIPv4(15 * time.Second)
	}
	return &fancdnChecker{client: client, host: host, userAgent: ua}
}

func (f *fancdnChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if parseBoolParam(r.URL.Query().Get("checksearch")) {
			show := f.checkSearch(r.Context(), r.URL.Query().Get("kinopoisk_id"))
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("fancdn"))
			return
		}
		f.index(w, r, links)
	}
}

func (f *fancdnChecker) checkSearch(ctx context.Context, kpStr string) bool {
	tree := f.resolve(ctx, kpStr)
	return tree != nil && len(tree.seasons) > 0
}

// resolve fetches /gt/{kp} (or returns cached) and parses the inputData
// JSON into the fancdnTree shape we use for index rendering. Caches hits
// 4 h (catalogue is stable), misses 5 min (a kp just added on lomont
// should appear within 5 min of the first probe).
func (f *fancdnChecker) resolve(ctx context.Context, kpStr string) *fancdnTree {
	kp, err := strconv.Atoi(strings.TrimSpace(kpStr))
	if err != nil || kp <= 0 {
		return nil
	}
	if v, ok := f.treeCache.Load(kp); ok {
		entry := v.(*fancdnTreeEntry)
		if entry.tree != nil && time.Since(entry.cachedAt) < fancdnTreeTTLHit {
			return entry.tree
		}
		if entry.tree == nil && time.Since(entry.cachedAt) < fancdnTreeTTLMiss {
			return nil // negative cache still warm
		}
	}
	target := fmt.Sprintf("%s/gt/%d", f.host, kp)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	resp, err := f.client.Do(req)
	if err != nil {
		// Transport-class failure: TLS reset (utls dial EOF), context
		// cancel, network unreachable, etc. WITHOUT a negative cache
		// here every Lampa /lite/events probe would re-attempt the
		// fetch, which is what made lomont start TCP-resetting us in
		// the first place. Cache as miss so we back off for 5 min;
		// surface the first occurrence as a WARN with an actionable
		// hint instead of staying silent at DEBUG.
		f.transportErrorNow(kp, err)
		f.treeCache.Store(kp, &fancdnTreeEntry{tree: nil, cachedAt: time.Now()})
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Negative cache so a probe storm doesn't translate to a request storm.
		f.treeCache.Store(kp, &fancdnTreeEntry{tree: nil, cachedAt: time.Now()})
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, fancdnHTMLMaxBytes))
	if err != nil {
		return nil
	}
	// Geo-block detection: lomont returns HTTP 200 with a tiny body that
	// SAYS "404 Not Found" when the caller IP is outside the catalogue's
	// allowed regions. Without this check the parser just sees "no
	// inputData" and silently negative-caches — operators waste hours
	// thinking the balancer is broken when actually it just needs a
	// SOCKS5 to a served region (RU is the safest bet).
	if len(body) < 2048 && fancdnGeoBlockRe.Match(body) {
		f.geoBlockedNow(kp)
		f.treeCache.Store(kp, &fancdnTreeEntry{tree: nil, cachedAt: time.Now()})
		return nil
	}
	tree := parseFancdnTree(kp, body)
	if tree != nil {
		// Real catalogue page reached — clear any prior geo-block state
		// so the next probe re-warns if the situation regresses.
		f.geoOK()
	}
	f.treeCache.Store(kp, &fancdnTreeEntry{tree: tree, cachedAt: time.Now()})
	return tree
}

// geoBlockedNow records that lomont served us its "no catalogue for you"
// 200-with-404-body stub. Logs once every 5 minutes at WARN with an
// actionable hint so silent negative-caching doesn't waste operator time.
func (f *fancdnChecker) geoBlockedNow(kp int) {
	f.geo.mu.Lock()
	defer f.geo.mu.Unlock()
	f.geo.blocked = true
	if time.Since(f.geo.lastWarnAt) > 5*time.Minute {
		f.geo.lastWarnAt = time.Now()
		log.Warn().
			Int("kp", kp).
			Str("host", f.host).
			Msg("fancdn: lomont.site geo-blocks this server's egress IP (HTTP 200 with body=404 Not Found). Configure [online.fancdn] socks_proxy to a RU IP so the catalogue is visible.")
	}
}

// geoOK clears the blocked flag the first time a real catalogue page is
// parsed successfully. Doesn't log (no news = good news).
func (f *fancdnChecker) geoOK() {
	f.geo.mu.Lock()
	f.geo.blocked = false
	f.geo.mu.Unlock()
}

// transportErrorNow records that the TCP/TLS leg to lomont failed (EOF,
// connection reset, context canceled, DNS lookup, etc.). Logged once per
// 5 minutes at WARN with a hint about the likely causes:
//
//   - lomont CDN reset-banned the server IP (too many probes without socks)
//   - DPI / firewall on the egress dropping TLS handshakes
//   - SOCKS5 proxy itself unreachable / down
//
// We share fancdnGeoState's mutex+timer because both errors point at the
// same fix (configure / fix socks_proxy) and we don't want to spam the
// log with two warning lines per minute when the IP is both blocked AND
// the connection times out.
func (f *fancdnChecker) transportErrorNow(kp int, err error) {
	f.geo.mu.Lock()
	defer f.geo.mu.Unlock()
	if time.Since(f.geo.lastWarnAt) > 5*time.Minute {
		f.geo.lastWarnAt = time.Now()
		log.Warn().
			Int("kp", kp).
			Str("host", f.host).
			Err(err).
			Msg("fancdn: cannot reach lomont.site (TLS/TCP error). Likely the IP is reset-banned or DPI/firewall is in the way. Configure [online.fancdn] socks_proxy to a clean RU egress.")
	}
}

// parseFancdnTree pulls the inputData JSON + data-film/data-content-type
// attrs out of a /gt page and reshapes them into our season-keyed view.
// Returns nil when the page doesn't carry the expected markup (404 page,
// completely different DLE template, etc.).
func parseFancdnTree(kp int, body []byte) *fancdnTree {
	dfM := fancdnDataFilmRe.FindSubmatch(body)
	if dfM != nil {
		gotKP, _ := strconv.Atoi(string(dfM[1]))
		if gotKP != kp {
			// Page rendered but for a DIFFERENT kp — shouldn't happen with
			// the /gt/{kp} path, but log it so we notice upstream changes.
			log.Debug().Int("requested_kp", kp).Int("got_kp", gotKP).Msg("fancdn: data-film kp mismatch")
			return nil
		}
	}
	m := fancdnInputDataRe.FindSubmatch(body)
	if m == nil {
		return nil
	}
	// lomont's inputData is type-unstable: for serials it's
	// `{season: {episode: [voices]}}` (object), for single-video movies
	// it's a flat array `[voices]` keyed by neither season nor episode.
	// Some titles also use `{"1": [voices]}` (object, season only, no
	// episode level) and `{"1": {"1": voices}}` (where the inner value
	// is a SINGLE voice object instead of an array). We probe each
	// shape in order and normalise into the serial-tree representation
	// the rest of the code expects.
	raw := parseFancdnInputData(m[1])
	if raw == nil {
		log.Debug().Int("kp", kp).Str("head", inputDataHead(m[1])).Msg("fancdn: inputData JSON shape unrecognised")
		return nil
	}

	tree := &fancdnTree{
		kpID:     kp,
		episodes: map[int][]int{},
		voices:   map[int]map[int][]fancdnRawVoice{},
	}
	if dfM != nil && len(dfM) >= 3 && len(dfM[2]) > 0 {
		tree.imdbID, _ = strconv.Atoi(string(dfM[2]))
	}
	// voiceLabels is built across the whole tree so a per-season episode
	// list can still render all the voice tabs even when the user landed
	// on a season where one voice is incomplete.
	seenVoice := map[int]string{}

	for sStr, eMap := range raw {
		s, err := strconv.Atoi(sStr)
		if err != nil {
			continue
		}
		tree.voices[s] = map[int][]fancdnRawVoice{}
		for eStr, voices := range eMap {
			e, err := strconv.Atoi(eStr)
			if err != nil {
				continue
			}
			// Sort voices by voice_id for stable UI ordering.
			vs := append([]fancdnRawVoice(nil), voices...)
			sort.Slice(vs, func(i, j int) bool { return vs[i].VoiceID < vs[j].VoiceID })
			tree.voices[s][e] = vs
			tree.episodes[s] = append(tree.episodes[s], e)
			for _, v := range vs {
				if _, ok := seenVoice[v.VoiceID]; !ok {
					seenVoice[v.VoiceID] = v.VoiceName
				}
			}
		}
		// Negative episodes (specials/movies attached to the series) come
		// first, then positives. writeSerial renders all of them in this
		// order; the UI separates specials with the "Спецвыпуск N" label.
		sort.Ints(tree.episodes[s])
		tree.seasons = append(tree.seasons, s)
	}
	sort.Ints(tree.seasons)

	for id, name := range seenVoice {
		tree.voiceLabels = append(tree.voiceLabels, fancdnVoiceLabel{ID: id, Name: name})
	}
	sort.Slice(tree.voiceLabels, func(i, j int) bool {
		return tree.voiceLabels[i].ID < tree.voiceLabels[j].ID
	})

	// Movie heuristic: ≤ 1 positive episode total. data-content-type can
	// nudge but isn't strictly necessary; the heuristic is robust.
	totalPos := 0
	for _, eps := range tree.episodes {
		for _, e := range eps {
			if e >= 1 {
				totalPos++
			}
		}
	}
	if totalPos == 0 {
		// All episodes negative → treat as movie (single positive entry, etc.)
		tree.isMovie = true
	} else if totalPos == 1 {
		tree.isMovie = true
	}
	if cmM := fancdnContentTypeRe.FindSubmatch(body); cmM != nil {
		ct, _ := strconv.Atoi(string(cmM[1]))
		if ct != 0 && totalPos <= 3 {
			// Non-zero content-type AND few positive episodes — call it a
			// movie even if the heuristic disagreed.
			tree.isMovie = true
		}
	}

	if len(tree.seasons) == 0 {
		return nil
	}
	return tree
}

// parseFancdnInputData decodes the lomont inputData JSON in any of the
// shapes we've observed and returns it as the canonical season→episode→
// voices map. Returns nil only when none of the shape probes succeeded.
//
// Shapes seen on the wire (2026-05-25, kp 382731 / 77044 / 460586):
//
//  1. Full serial: `{"1": {"1": [{...},...], "2": [...], ...}, "2": {...}}`
//  2. Single-video movie: `[{...},...]` (flat voice array at root)
//  3. Season-only: `{"1": [{...},...]}` (one level, no episode dict)
//  4. Single-voice-per-ep: `{"1": {"1": {voice obj} }}` (inner is one
//     object instead of an array — rare but observed)
//
// The fallback ordering tries the richest shape first; only if every
// strict-typed Unmarshal fails do we use a generic any-decoder + manual
// walk. That keeps the happy path branch-prediction-friendly.
func parseFancdnInputData(b []byte) map[string]map[string][]fancdnRawVoice {
	// Shape 1: full serial.
	{
		var raw map[string]map[string][]fancdnRawVoice
		if err := stdjson.Unmarshal(b, &raw); err == nil && len(raw) > 0 {
			return raw
		}
	}
	// Shape 2: flat voice array → wrap as season 1 episode 1.
	{
		var voices []fancdnRawVoice
		if err := stdjson.Unmarshal(b, &voices); err == nil && len(voices) > 0 {
			ep := voices[0].Episode
			if ep == 0 {
				ep = 1
			}
			season := voices[0].Season
			if season == 0 {
				season = 1
			}
			return map[string]map[string][]fancdnRawVoice{
				strconv.Itoa(season): {strconv.Itoa(ep): voices},
			}
		}
	}
	// Shape 3: season-only (`{"1": [voices]}`).
	{
		var raw map[string][]fancdnRawVoice
		if err := stdjson.Unmarshal(b, &raw); err == nil && len(raw) > 0 {
			out := map[string]map[string][]fancdnRawVoice{}
			for sStr, voices := range raw {
				if len(voices) == 0 {
					continue
				}
				// All voices in this bucket share the same episode (or
				// it's a movie with episode=1). Group by episode just
				// in case multiple episodes ended up in one bucket.
				inner := map[string][]fancdnRawVoice{}
				for _, v := range voices {
					ep := v.Episode
					if ep == 0 {
						ep = 1
					}
					eStr := strconv.Itoa(ep)
					inner[eStr] = append(inner[eStr], v)
				}
				out[sStr] = inner
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	// Shape 4 + anything else: decode to generic any, walk it.
	{
		var any any
		if err := stdjson.Unmarshal(b, &any); err != nil {
			return nil
		}
		out := walkFancdnInputDataAny(any)
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// walkFancdnInputDataAny handles the long-tail shape variants by descending
// into a generic any-tree. Each leaf (single voice object OR array of voice
// objects) is normalised into the canonical map[season][episode][]voice.
// Tolerant of inner values being either a single map (one voice) or an
// array of maps (multi-voice).
func walkFancdnInputDataAny(v any) map[string]map[string][]fancdnRawVoice {
	root, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]map[string][]fancdnRawVoice{}
	for sStr, seasonAny := range root {
		seasonMap, ok := seasonAny.(map[string]any)
		if !ok {
			continue
		}
		inner := map[string][]fancdnRawVoice{}
		for eStr, episodeAny := range seasonMap {
			switch ep := episodeAny.(type) {
			case []any:
				for _, voiceAny := range ep {
					if v := decodeFancdnVoiceAny(voiceAny); v != nil {
						inner[eStr] = append(inner[eStr], *v)
					}
				}
			case map[string]any:
				if v := decodeFancdnVoiceAnyMap(ep); v != nil {
					inner[eStr] = append(inner[eStr], *v)
				}
			}
		}
		if len(inner) > 0 {
			out[sStr] = inner
		}
	}
	return out
}

// decodeFancdnVoiceAny normalises whatever the inner element holds into a
// fancdnRawVoice. Re-marshals through JSON for type safety — cheap (one
// voice ~150 B) and lets stdjson's number-conversion handle int/float
// quirks.
func decodeFancdnVoiceAny(v any) *fancdnRawVoice {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return decodeFancdnVoiceAnyMap(m)
}

func decodeFancdnVoiceAnyMap(m map[string]any) *fancdnRawVoice {
	b, err := stdjson.Marshal(m)
	if err != nil {
		return nil
	}
	var v fancdnRawVoice
	if err := stdjson.Unmarshal(b, &v); err != nil {
		return nil
	}
	if v.VideoID == 0 {
		return nil
	}
	return &v
}

// inputDataHead returns a short, single-line preview of the inputData
// payload so the "unrecognised shape" log line is actionable (operators
// can paste the head into a bug report instead of dumping 500 KB).
func inputDataHead(b []byte) string {
	s := strings.ReplaceAll(string(b), "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// pickFirstPlayable picks an episode/voice pair to use as the "default" play
// row in movie mode. Returns (season, episode, voice) or zeroes on miss.
func (t *fancdnTree) pickFirstPlayable() (int, int, *fancdnRawVoice) {
	for _, s := range t.seasons {
		eps := t.episodes[s]
		// Prefer first positive episode; fall back to first (most negative).
		for _, e := range eps {
			if e < 1 {
				continue
			}
			if vs := t.voices[s][e]; len(vs) > 0 {
				v := vs[0]
				return s, e, &v
			}
		}
		if len(eps) > 0 {
			e := eps[0]
			if vs := t.voices[s][e]; len(vs) > 0 {
				v := vs[0]
				return s, e, &v
			}
		}
	}
	return 0, 0, nil
}

// index renders the JSON / HTML response for /lite/fancdn. The query shape
// follows getstv / kinotochka: kinopoisk_id + optional s,t + optional rjson.
//
//	rjson=false: return Lampa-compat HTML rows
//	rjson=true:  return {type:"movie"|"season"|"episode", data:[…]}
func (f *fancdnChecker) index(w http.ResponseWriter, r *http.Request, links *proxylink.Manager) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	kpStr := strings.TrimSpace(q.Get("kinopoisk_id"))
	title := strings.TrimSpace(q.Get("title"))
	original := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	tree := f.resolve(r.Context(), kpStr)
	if tree == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	s, sSet := getsTVQueryInt(q.Get("s"))
	t, tSet := getsTVQueryInt(q.Get("t"))
	if !sSet {
		s = -1
	}
	if !tSet {
		t = -1
	}

	if tree.isMovie {
		f.writeMovie(w, r, rjson, title, original, tree, links)
		return
	}
	f.writeSerial(w, r, rjson, kpStr, title, original, year, tree, s, t, links)
}

// writeMovie collapses all positive (or the single) episode's voices into a
// single movie play-list. Each voice → one row with method=play and a lazy
// resolve URL pointing at /lite/fancdn/video.m3u8?vid=X.
func (f *fancdnChecker) writeMovie(w http.ResponseWriter, r *http.Request, rjson bool, title, original string, tree *fancdnTree, links *proxylink.Manager) {
	_ = links // resolution happens lazily — links used by the /video handler
	_, _, def := tree.pickFirstPlayable()
	if def == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}
	host := hostFromRequest(r)
	baseTitle := getsTVJoinName(title, original)

	// Build one row per voice present in the picked (season, episode).
	rows := make([]map[string]any, 0)
	labels := make([]string, 0)
	for _, v := range tree.voices[def.Season][def.Episode] {
		streamURL := fancdnVideoURL(host, v.VideoID, v.VoiceID, v.VoiceName)
		name := strings.TrimSpace(v.VoiceName)
		if name == "" {
			name = "По умолчанию"
		}
		rows = append(rows, map[string]any{
			"method": "play",
			"url":    streamURL,
			"stream": streamURL,
			"name":   name,
			"title":  baseTitle,
		})
		labels = append(labels, name)
	}
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": rows})
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

// writeSerial mirrors the three-stage rendering Lampa expects:
//   - s=-1 → list of seasons (link rows)
//   - s=N, t=-1 → list of episodes of season N (default voice = first voice_id)
//   - s=N, t=V → list of episodes of season N with voice V
//
// Voice tabs (`voice` field on the JSON response) carry the full label
// list so Lampa renders a tab bar; clicking switches t=.
func (f *fancdnChecker) writeSerial(
	w http.ResponseWriter,
	r *http.Request,
	rjson bool,
	kpStr, title, original string,
	year int,
	tree *fancdnTree,
	s, t int,
	links *proxylink.Manager,
) {
	_ = links
	host := hostFromRequest(r)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(original)
	yearParam := ""
	if year > 0 {
		yearParam = "&year=" + strconv.Itoa(year)
	}

	// Stage 1: seasons.
	if s == -1 {
		rows := make([]map[string]any, 0, len(tree.seasons))
		labels := make([]string, 0, len(tree.seasons))
		for _, sn := range tree.seasons {
			name := strconv.Itoa(sn) + " сезон"
			link := host + "/lite/fancdn?rjson=" + getsTVBool(rjson) +
				"&kinopoisk_id=" + url.QueryEscape(kpStr) +
				"&title=" + encTitle + "&original_title=" + encOriginal +
				yearParam + "&s=" + strconv.Itoa(sn)
			rows = append(rows, map[string]any{
				"method": "link", "id": sn, "url": link, "name": name,
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

	// Stage 2/3: episodes for season s, voice t (or default voice).
	episodes := tree.episodes[s]
	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	// Pick voice. Default = first voice_id observed anywhere in the tree.
	// Lampa passes t as the voice_id (we use it directly).
	defaultVoice := -1
	if len(tree.voiceLabels) > 0 {
		defaultVoice = tree.voiceLabels[0].ID
	}
	pickedVoice := t
	if pickedVoice == -1 {
		pickedVoice = defaultVoice
	}

	// Voice-tab list (UI bar at the top of the episode list).
	voiceTabs := make([]map[string]any, 0, len(tree.voiceLabels))
	for _, vl := range tree.voiceLabels {
		voiceTabs = append(voiceTabs, map[string]any{
			"name":   vl.Name,
			"active": vl.ID == pickedVoice,
			"url": host + "/lite/fancdn?rjson=" + getsTVBool(rjson) +
				"&kinopoisk_id=" + url.QueryEscape(kpStr) +
				"&title=" + encTitle + "&original_title=" + encOriginal +
				yearParam + "&s=" + strconv.Itoa(s) +
				"&t=" + strconv.Itoa(vl.ID),
		})
	}

	baseTitle := getsTVJoinName(title, original)
	rows := make([]map[string]any, 0, len(episodes))
	labels := make([]string, 0, len(episodes))
	for _, e := range episodes {
		var pick *fancdnRawVoice
		for i := range tree.voices[s][e] {
			if tree.voices[s][e][i].VoiceID == pickedVoice {
				pick = &tree.voices[s][e][i]
				break
			}
		}
		// Fallback: first available voice (sparsely-voiced episodes).
		if pick == nil && len(tree.voices[s][e]) > 0 {
			v := tree.voices[s][e][0]
			pick = &v
		}
		if pick == nil {
			continue
		}
		streamURL := fancdnVideoURL(host, pick.VideoID, pick.VoiceID, pick.VoiceName)
		name := fancdnEpisodeName(e)
		rows = append(rows, map[string]any{
			"method": "play",
			"url":    streamURL,
			"stream": streamURL,
			"name":   name,
			"title":  baseTitle,
			"s":      s,
			"e":      e,
		})
		labels = append(labels, name)
	}
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		out := map[string]any{
			"type": "episode",
			"data": rows,
		}
		if len(voiceTabs) > 1 {
			out["voice"] = voiceTabs
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, s, fancdnEpisodeIntSafe(row["e"]))
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// fancdnEpisodeName turns a raw episode integer into the human label.
// Negative episodes are specials/movies attached to the series; we tag
// them as "Спецвыпуск N" so the listing doesn't show -18 as a series
// episode title.
func fancdnEpisodeName(e int) string {
	if e < 0 {
		return fmt.Sprintf("Спецвыпуск %d", -e)
	}
	return fmt.Sprintf("%d серия", e)
}

func fancdnEpisodeIntSafe(v any) int {
	if i, ok := v.(int); ok {
		return i
	}
	return 0
}

// fancdnVideoURL builds the lazy-resolve URL embedded in index responses.
// The /lite/fancdn/video handler reads vid + (optional) label and replies
// with a 302 redirect to /proxy/{enc} after calling /player/responce.php
// on the fly. label is the voice name surfaced in logs for readability.
func fancdnVideoURL(host string, videoID, voiceID int, label string) string {
	return host + "/lite/fancdn/video.m3u8?vid=" + strconv.Itoa(videoID) +
		"&voice_id=" + strconv.Itoa(voiceID) +
		"&label=" + url.QueryEscape(label) +
		"&play=true"
}

// fancdnResponce is the JSON shape returned by /player/responce.php?video_id=X.
// Note: `subtitles` and `thumbnail` are TYPE-UNSTABLE on the upstream — when
// empty they're rendered as JSON arrays (`[]`), when populated they're
// objects (`{ru:"url", en:"url"}` for subtitles; `{src,width,height,rows}`
// for thumbnail). We accept any shape and read them later via type
// assertions so a parse error here doesn't sink the whole video resolve.
type fancdnResponce struct {
	Src       string `json:"src"`
	Spare     string `json:"spare"`
	Subtitles any    `json:"subtitles"`
	Thumbnail any    `json:"thumbnail"`
	Error     string `json:"error"`
}

// fancdnSubtitle is one Lampa-side subtitle entry.
type fancdnSubtitle struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

// extractFancdnSubtitles flattens the polymorphic `subtitles` field from
// responce.php into a stable list. Currently lomont uses two shapes:
//
//   - [] (empty)                              → returns nil
//   - {"ru":"https://…","en":"https://…"}     → returns [{ru, url}, {en, url}]
//
// Other shapes (older array-of-objects form, weirdly nested maps, etc.) we
// ignore to keep playback robust against another upstream schema flip.
func extractFancdnSubtitles(raw any) []fancdnSubtitle {
	if raw == nil {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := make([]fancdnSubtitle, 0, len(obj))
	for lang, v := range obj {
		url, ok := v.(string)
		if !ok || strings.TrimSpace(url) == "" {
			continue
		}
		out = append(out, fancdnSubtitle{URL: url, Label: lang})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// resolveVideo calls /player/responce.php?video_id=X and returns the signed
// HLS src + the headers needed to replay it + any subtitles. The URL is
// IP-bound to the caller, so we MUST call this from the same egress that
// will later replay the m3u8 to the user (the SOCKS5 binding takes care
// of that when set).
func (f *fancdnChecker) resolveVideo(ctx context.Context, videoID int) (string, map[string]string, []fancdnSubtitle, error) {
	target := fmt.Sprintf("%s/player/responce.php?video_id=%d", f.host, videoID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", nil, nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	req.Header.Set("Referer", f.host+"/")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", nil, nil, fmt.Errorf("fancdn responce.php: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, nil, fmt.Errorf("fancdn responce.php: status %d", resp.StatusCode)
	}
	var res fancdnResponce
	if err := stdjson.Unmarshal(body, &res); err != nil {
		return "", nil, nil, fmt.Errorf("fancdn responce.php: parse %w (body: %s)", err, string(body))
	}
	if res.Error != "" {
		return "", nil, nil, fmt.Errorf("fancdn responce.php: %s", res.Error)
	}
	src := strings.TrimSpace(res.Src)
	if src == "" {
		return "", nil, nil, fmt.Errorf("fancdn responce.php: empty src")
	}
	headers := map[string]string{
		"Referer":    f.host + "/",
		"User-Agent": f.userAgent,
	}
	return src, headers, extractFancdnSubtitles(res.Subtitles), nil
}

// FancdnVideoHandler powers /lite/fancdn/video and /lite/fancdn/video.m3u8
// (and the legacy xvideocdn aliases). Reads ?vid={video_id}, calls
// responce.php, encrypts the HLS URL via the shared proxylink.Manager, and
// 302-redirects to /proxy/{enc} so the player fetches m3u8 segments through
// the same egress IP that signed the URL.
//
// Subtitles (when lomont returns them as a {lang:url} object) are logged at
// debug for now — wiring them through to Lampa needs the writeMovie/Serial
// path to know about them at INDEX time, not at play-time 302. Leaving the
// extraction in place so it's a one-line change when we add UI support.
func FancdnVideoHandler(f *fancdnChecker, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vid, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("vid")))
		if err != nil || vid <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing or invalid vid"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		src, headers, subs, err := f.resolveVideo(ctx, vid)
		if err != nil {
			log.Warn().Err(err).Int("vid", vid).Msg("fancdn: video resolve failed")
			writeGetsTVEmpty(w, false)
			return
		}
		if len(subs) > 0 {
			log.Debug().Int("vid", vid).Int("subs", len(subs)).Msg("fancdn: subtitles available (not yet wired into Lampa UI)")
		}
		// EncryptURIWithHeaders requires a real proxylink.Manager; if the
		// caller wired a nil (test path), fall back to a 302 to the raw
		// CDN URL — tests use httptest mocks where IP-binding is moot.
		if links == nil {
			http.Redirect(w, r, src, http.StatusFound)
			return
		}
		token := links.EncryptURIWithHeaders(src, clientIP(r), "fancdn", headers)
		proxied := hostFromRequest(r) + "/proxy/" + token
		http.Redirect(w, r, proxied, http.StatusFound)
	}
}
