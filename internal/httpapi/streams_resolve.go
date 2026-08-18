package httpapi

import (
	"context"
	ejson "encoding/json"
	stdhtml "html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/transcodesvc"
)

// adminIDStoreRef lets the resolver tell an admin from a normal user, so the
// streams payload can un-mask which balancer backs «Сервер N» for admins only.
var adminIDStoreRef *tgauth.AdminIDStore

// capiInt is a lenient atoi for query params.
func capiInt(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

// capi.go — secured first-party client API (/capi/*).
//
// The SPA never sees balancer names or raw CDN URLs: this layer aggregates
// online sources server-side, dedupes by voice, drops the balancer identity,
// and re-mints each stream as an opaque, same-origin /proxy token (never
// IP-bound — Lampa parity, see capiSameOrigin).
//
// Protection layers (max tier):
//   1. user-auth        — reuse the global auth middleware (TG/password/anon)
//   2. signing/crypto   — per-session HMAC request signing + AES-GCM response bodies
//   3. anonymization    — `balanser` is never serialized to the client
//   4. opaque streams   — /proxy/<aes> tokens (or raw CDN for no_stream_proxy sources)

// capiSubTrack is one external subtitle track of a voice — label + same-origin /proxy url.
// Sources emit them in the Lampa row convention (subtitles: [{label,url}]); the aggregator
// used to DROP them entirely, which is why «субтитры нигде не работают»: clients only ever
// saw in-manifest HLS tracks, which most sources don't have.
type capiSubTrack struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type capiVoice struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	ByQuality map[string]string `json:"byQuality"`
	Subtitles []capiSubTrack    `json:"subtitles,omitempty"`
	Torrent   bool              `json:"torrent,omitempty"` // a torrent source (PidTor) backs a played quality — UI stars it
	// Server is the ANONYMISED source label every client shows («Сервер 3»). Users report a broken
	// «Сервер 3» and the admin can map it back — without ever exposing balancer names publicly.
	Server int `json:"server,omitempty"`
	// Source is the real balancer behind Server. Emitted ONLY for admins (capiIsAdmin).
	Source string `json:"source,omitempty"`
	// Tech markers harvested from the RAW source voice names / quality labels BEFORE
	// capiCleanVoice strips them as junk — the UI badges HDR/Dolby Vision/Atmos with them.
	// Voice-level granularity: "some stream of this voice carries the marker".
	HDR   bool `json:"hdr,omitempty"`
	DV    bool `json:"dv,omitempty"`
	Atmos bool `json:"atmos,omitempty"`
	// Rip is the normalized source-quality token («Remux», «WEB-DL», «TS», «CAM»…) harvested the
	// same way. Its main job is the warning: a viewer should know it's an экранка BEFORE pressing
	// play. Best (lowest-rank) marker across the voice's raw labels wins — see capiRipType.
	Rip string `json:"rip,omitempty"`
}

// capiTechFlags detects HDR / Dolby Vision / Atmos markers in a raw voice name or quality
// label. \b keeps "dv" from matching inside hdvb/DVO; Cyrillic release words never collide.
var (
	capiHDRRe   = regexp.MustCompile(`(?i)\bhdr(10\+?)?\b`)
	capiDVRe    = regexp.MustCompile(`(?i)dolby\s*vision|\bdv\b`)
	capiAtmosRe = regexp.MustCompile(`(?i)\batmos\b`)
)

func capiTechFlags(s string) (hdr, dv, atmos bool) {
	return capiHDRRe.MatchString(s), capiDVRe.MatchString(s), capiAtmosRe.MatchString(s)
}

// capiRipTypes: порядок = ранг исходника, ЛУЧШИЙ первым. Отдаём клиентам лучший найденный у
// озвучки маркер: если у неё есть WEB-DL 1080p и затесавшийся TS 480p, зритель выберет 1080p —
// пугать его бейджем «TS» было бы враньём. NB: \bhdr\b НЕ ловит «HDRip» (за hdr идёт словесный
// символ), поэтому HDR-детект выше с этим списком не конфликтует.
var capiRipTypes = []struct {
	label string
	re    *regexp.Regexp
}{
	{"Remux", regexp.MustCompile(`(?i)remux\b`)}, // без левой \b: «BDRemux» — одно слово
	{"BDRip", regexp.MustCompile(`(?i)\bbd\s?rip\b|\bblu[- ]?ray\b`)},
	{"WEB-DL", regexp.MustCompile(`(?i)\bweb[\s._-]?dl\b`)},
	{"WEBRip", regexp.MustCompile(`(?i)\bweb[\s._-]?rip\b`)},
	{"HDTV", regexp.MustCompile(`(?i)\bhdtv(rip)?\b`)},
	{"HDRip", regexp.MustCompile(`(?i)\bhd\s?rip\b`)},
	{"DVDRip", regexp.MustCompile(`(?i)\bdvd\s?(rip|scr)\b`)},
	{"TVRip", regexp.MustCompile(`(?i)\b(tv|sat)\s?rip\b`)},
	{"TC", regexp.MustCompile(`(?i)\btelecine\b|\bhdtc\b|\btc\s?rip\b`)},
	{"TS", regexp.MustCompile(`(?i)\bts\b|\btelesync\b|\bhdts\b|телесинк`)},
	{"CAM", regexp.MustCompile(`(?i)\bcam\s?(rip)?\b|экранк|камрип`)},
}

// capiRipType returns the normalized rip token and its rank (lower = better source master),
// or "" when the string carries no recognizable marker.
func capiRipType(s string) (string, int) {
	for i, rt := range capiRipTypes {
		if rt.re.MatchString(s) {
			return rt.label, i
		}
	}
	return "", 0
}

// capiIsTorrentSource reports whether a balancer is a torrent-backed source (PidTor → TorrServer), so
// the picker can star it — torrent streams behave differently (seeders, slower start, transcode box).
func capiIsTorrentSource(bal string) bool {
	return bal == "pidtor"
}

// animePreferredSources are balancers that match anime ACCURATELY — by kinopoisk/imdb/shikimori id
// rather than fuzzy TMDB title. For anime content capi serves these and falls back to the generic
// balancers only when none of them answered (they tend to return a wrong title for an anime tmdbid).
// Superset of animePlugins (anilibria/animego/…) plus the general-but-anime-accurate kodik & veoveo.
var animePreferredSources = func() map[string]bool {
	m := map[string]bool{"kodik": true, "veoveo": true}
	for k := range animePlugins {
		m[k] = true
	}
	return m
}()

// capiIsAnimeRequest reports whether a resolve is for anime animation. The web client sends `anime=1`
// (it knows: genre Animation + original_language ja/ko/zh/cn). As a server-side belt we also infer it
// from original_language ∈ anime langs together with a genre_ids=16 hint, so a client that only sends
// metadata still routes correctly.
func capiIsAnimeRequest(r *http.Request) bool {
	q := r.URL.Query()
	switch strings.ToLower(strings.TrimSpace(q.Get("anime"))) {
	case "1", "true", "yes":
		return true
	}
	if _, ok := animeLanguages[firstLang(q.Get("original_language"))]; !ok {
		return false
	}
	for _, g := range strings.Split(q.Get("genre_ids"), ",") {
		if strings.TrimSpace(g) == "16" { // TMDB genre 16 = Animation
			return true
		}
	}
	return false
}

type capiStreams struct {
	Qualities []string    `json:"qualities"`
	Voices    []capiVoice `json:"voices"`
}
type capiCacheEntry struct {
	data  capiStreams
	fresh time.Time // serve as-is until this moment
	stale time.Time // after this the entry is unusable (hard miss)
}

var (
	capiStreamsTTL = 5 * time.Minute // fresh window
	// Stale window 3h (was 30m): a stale hit is served INSTANTLY and refreshed
	// behind the response, so the only cost of a longer window is how long a
	// genuinely dead voice can linger — and every stale hit re-resolves, so the
	// data self-heals on use. 30m meant an evening-long browsing session paid
	// the full multi-second re-drill for every card revisited after half an hour.
	capiStreamsStale   = 3 * time.Hour
	capiStreamsCache   = map[string]capiCacheEntry{}
	capiStreamsCacheMu sync.Mutex
	capiStreamsSF      singleflight.Group
)

// Expired stream-cache entries were only deleted when the SAME key was requested again —
// per-(ip,card,episode) keys that are never revisited lived for the process lifetime.
// A periodic sweep keeps the map bounded regardless of key churn.
func init() {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			now := time.Now()
			capiStreamsCacheMu.Lock()
			for k, e := range capiStreamsCache {
				if now.After(e.stale) {
					delete(capiStreamsCache, k)
				}
			}
			capiStreamsCacheMu.Unlock()
			capiStreamsForceMu.Lock()
			for k, at := range capiStreamsForceAt {
				if now.Sub(at) > capiStreamsForceCooldown {
					delete(capiStreamsForceAt, k)
				}
			}
			capiStreamsForceMu.Unlock()
		}
	}()
}

// A CDN's SIGNED url can die long before our cache entry does: kinopub hands out
// per-node links (the reporter's «10-й сервер») whose signature expires — or whose node
// flaps — inside the 3h stale window, so the same cached entry plays fine one minute and
// 403s the next («то норм, то 403»). The player can't tell that from a dead source, and
// re-drilling every source on its own would be a DoS lever, so a client that just hit
// 401/403/404 on a manifest may ask for ONE foreground re-resolve per card per cooldown.
var (
	capiStreamsForceMu sync.Mutex
	capiStreamsForceAt = map[string]time.Time{}
)

const capiStreamsForceCooldown = 30 * time.Second

func capiStreamsForceAllowed(key string) bool {
	now := time.Now()
	capiStreamsForceMu.Lock()
	defer capiStreamsForceMu.Unlock()
	if last, ok := capiStreamsForceAt[key]; ok && now.Sub(last) < capiStreamsForceCooldown {
		return false
	}
	capiStreamsForceAt[key] = now
	return true
}

// capiPurgeStreamsCache drops every cached resolve. Called when a balancer's upstream
// session rotates (see litesrc Deps.PurgeStreamCache): the cache holds ready-made stream
// links, and links minted under a retired session can be dead — serving them for the rest
// of the 3h stale window looks to the user exactly like "playback broke by itself". The
// cost is one re-drill per card, and rotations are rare by design.
func capiPurgeStreamsCache(balancer string) {
	capiStreamsCacheMu.Lock()
	n := len(capiStreamsCache)
	capiStreamsCache = map[string]capiCacheEntry{}
	capiStreamsCacheMu.Unlock()
	if n > 0 {
		log.Warn().Str("balancer", balancer).Int("entries", n).Msg("capi: stream cache purged — upstream session rotated")
	}
}

// capiStreamsKey deliberately has NO client IP: tokens are IP-free, so the resolve is
// identical for everyone with the same card+episode+group — sharing turns the 2nd…Nth
// viewer's TTFB into a cache hit instead of a multi-second re-drill.
func capiStreamsKey(r *http.Request) string {
	q := r.URL.Query()
	gid := ""
	if g := resolveUserGroup(r); g != nil {
		gid = g.ID
	}
	// The key MUST be unique per title+episode: the shared cache + singleflight collapse on it, so a
	// weak key hands one card's streams to a DIFFERENT request («вместо фильма/серии — совсем другое»).
	// id/imdb/kinopoisk can all be empty on a search-by-title resolve, which collapsed the key and
	// served a random previously-cached card. Fold the human identity (title+year) in unconditionally
	// as a collision guard — it never breaks cache sharing (the same card always sends the same title).
	//
	// imdb/kinopoisk are resolve HINTS, not identity: with a TMDB id present they add nothing
	// (id+serial+title+year already pin the card) — and folding them in split the rail-focus
	// prewarm (card only: no imdb yet) and the Details resolve (imdb from external ids) onto
	// DIFFERENT entries, wasting every prewarm. They still guard the id-less title-search case.
	id := q.Get("id")
	imdb, kp := q.Get("imdb_id"), q.Get("kinopoisk_id")
	if id != "" {
		imdb, kp = "", ""
	}
	title := strings.ToLower(strings.TrimSpace(q.Get("title")))
	// Admin payloads carry the real balancer name per voice (capiVoice.Source). They MUST NOT be
	// served from the shared cache to a normal user, so admin-ness is part of the key.
	adm := ""
	if capiIsAdmin(r) {
		adm = "adm"
	}
	return strings.Join([]string{
		id, imdb, kp, title, q.Get("year"),
		q.Get("serial"), q.Get("s"), q.Get("e"), gid, adm,
	}, "|")
}

// capiStreamsCacheGet returns the entry and whether it is still FRESH. A stale-but-usable
// entry comes back with ok=true, fresh=false — the caller serves it and refreshes in the
// background. A missing/expired entry is (zero, false, false).
func capiStreamsCacheGet(key string) (capiStreams, bool, bool) {
	capiStreamsCacheMu.Lock()
	defer capiStreamsCacheMu.Unlock()
	e, ok := capiStreamsCache[key]
	if !ok {
		return capiStreams{}, false, false
	}
	now := time.Now()
	if now.After(e.stale) {
		delete(capiStreamsCache, key)
		return capiStreams{}, false, false
	}
	return e.data, true, now.Before(e.fresh)
}

// capiStreamCount is the total number of playable (voice, quality) urls in a result —
// the shrink-guard metric. Voice count alone misses the «озвучки на месте, но у половины
// пропал 2160p» case.
func capiStreamCount(s capiStreams) int {
	n := 0
	for _, v := range s.Voices {
		n += len(v.ByQuality)
	}
	return n
}

// capiStreamsCachePut stores a resolve result, but never lets a SMALLER set replace a
// bigger still-usable one (shrink guard). Every SWR refresh re-drills from scratch, and
// its 4.5s soft-deadline partial used to overwrite the complete cached set; add per-round
// source flaps (upstream 403/timeout, healthcheck auto-disable window) and the picker
// «дышал»: 6 озвучек в 4K → через пять минут 2 → снова 6. Now a shrunken re-resolve just
// re-freshens the existing bigger entry (fresh capped at its stale horizon, so genuinely
// dead voices still drop within capiStreamsStale via the hard miss), and the backfill
// publisher replaces it the moment the new round actually catches up.
func capiStreamsCachePut(key string, data capiStreams) {
	ttl, stale := capiStreamsTTL, capiStreamsStale
	if len(data.Voices) == 0 {
		ttl, stale = 30*time.Second, 30*time.Second // empties expire fast (no SWR) so a transient miss can re-resolve
	}
	now := time.Now()
	capiStreamsCacheMu.Lock()
	defer capiStreamsCacheMu.Unlock()
	if e, ok := capiStreamsCache[key]; ok && now.Before(e.stale) &&
		(len(data.Voices) < len(e.data.Voices) || capiStreamCount(data) < capiStreamCount(e.data)) {
		fresh := now.Add(capiStreamsTTL)
		if fresh.After(e.stale) {
			fresh = e.stale
		}
		e.fresh = fresh
		capiStreamsCache[key] = e
		log.Info().Int("kept_voices", len(e.data.Voices)).Int("new_voices", len(data.Voices)).
			Int("kept_streams", capiStreamCount(e.data)).Int("new_streams", capiStreamCount(data)).
			Msg("capi: streams cache shrink-guarded (kept bigger set)")
		return
	}
	capiStreamsCache[key] = capiCacheEntry{data: data, fresh: now.Add(ttl), stale: now.Add(stale)}
}

// capiStreamsFor serves a streams resolve for the request from the shared cache,
// with stale-while-revalidate and singleflight collapsing. Both the encrypted and
// the lite handler funnel through here so they share one cache and one in-flight
// resolve per card.
func capiStreamsFor(r *http.Request, cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry) capiStreams {
	ip := clientIP(r)
	ck := capiStreamsKey(r)
	// `refresh=1`: the player got a manifest refusal on a url that came from THIS cache, so the
	// entry is worthless no matter how fresh it looks. Re-drill in the foreground and hand back
	// newly-minted tokens. Rate-limited per card — beyond the cooldown it degrades to a normal
	// (possibly cached) read rather than an error, so a retry loop can't hammer the balancers.
	if parseBoolParam(r.URL.Query().Get("refresh")) {
		if capiStreamsForceAllowed(ck) {
			log.Info().Str("key", ck).Msg("capi: streams force-refresh (client hit a dead stream url)")
			v, _, _ := capiStreamsSF.Do(ck+"|force", func() (any, error) {
				return capiResolveStreams(r, cfg, proxyLinks, dynRoutes, ip, ck), nil
			})
			return v.(capiStreams)
		}
		log.Debug().Str("key", ck).Msg("capi: streams force-refresh throttled")
	}
	if v, ok, fresh := capiStreamsCacheGet(ck); ok {
		if !fresh {
			// Serve the stale copy NOW, refresh behind the response. Voices/qualities
			// barely change; a viewer should never pay re-drill latency for a card
			// somebody opened within the last half hour.
			//
			// The request must not be retained past the handler return — clone it.
			// WithoutCancel keeps the auth/group context VALUES (group filtering must
			// shape the refreshed result exactly like a foreground resolve) while
			// detaching from the response lifecycle.
			bg := r.Clone(context.WithoutCancel(r.Context()))
			go func() {
				_, _, _ = capiStreamsSF.Do(ck, func() (any, error) {
					if _, _, stillFresh := capiStreamsCacheGet(ck); stillFresh {
						return capiStreams{}, nil // a concurrent refresh already landed
					}
					return capiResolveStreams(bg, cfg, proxyLinks, dynRoutes, ip, ck), nil
				})
			}()
		}
		return v
	}
	v, _, _ := capiStreamsSF.Do(ck, func() (any, error) {
		if cached, ok, _ := capiStreamsCacheGet(ck); ok {
			return cached, nil
		}
		return capiResolveStreams(r, cfg, proxyLinks, dynRoutes, ip, ck), nil
	})
	return v.(capiStreams)
}

// capiPrewarmSem caps concurrent prewarm-triggered background resolves.
// Prewarm is best-effort: on saturation the request is silently dropped and
// the user just pays the normal resolve latency when they actually press play.
var capiPrewarmSem = make(chan struct{}, 6)

// capiStreamsPrewarm fires a background resolve for the card so the streams
// cache is warm by the time the user presses «смотреть». Called by the details
// screen on open (…&prewarm=1). Returns whether a resolve was actually started
// (false = already fresh, or the prewarm pool is saturated).
func capiStreamsPrewarm(r *http.Request, cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry) bool {
	ck := capiStreamsKey(r)
	if _, _, fresh := capiStreamsCacheGet(ck); fresh {
		return false
	}
	select {
	case capiPrewarmSem <- struct{}{}:
	default:
		return false
	}
	ip := clientIP(r)
	// Clone like the SWR refresh does: keep auth/group context VALUES (group
	// filtering must shape the result exactly like a foreground resolve) while
	// detaching from the response lifecycle.
	bg := r.Clone(context.WithoutCancel(r.Context()))
	go func() {
		defer func() { <-capiPrewarmSem }()
		_, _, _ = capiStreamsSF.Do(ck, func() (any, error) {
			if _, _, stillFresh := capiStreamsCacheGet(ck); stillFresh {
				return capiStreams{}, nil // a concurrent resolve already landed
			}
			return capiResolveStreams(bg, cfg, proxyLinks, dynRoutes, ip, ck), nil
		})
	}()
	return true
}

var capiLiteSrc struct {
	sync.Mutex
	gen *config.Config // live-config pointer identity the handler was built against
	h   http.HandlerFunc
}

func capiLiteSource(cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry) http.HandlerFunc {
	if !serverReady() {
		// tests / early startup: no live-config generation to key on — build fresh
		return liteSourceHandler(cfg, proxyLinks, dynRoutes)
	}
	gen := liveConfigGen()
	capiLiteSrc.Lock()
	defer capiLiteSrc.Unlock()
	if capiLiteSrc.h == nil || capiLiteSrc.gen != gen {
		capiLiteSrc.h = liteSourceHandler(cfg, proxyLinks, dynRoutes)
		capiLiteSrc.gen = gen
	}
	return capiLiteSrc.h
}

// Resolve latency model: most real sources land within the first ~2-3 seconds; the tail
// is slow/empty balancers. Instead of holding the response hostage for the full 8s wall:
//   - all drills finish < soft deadline → full result (common case);
//   - soft deadline hits with ≥1 voice  → return the partial NOW, keep drilling in the
//     background and overwrite the cache with the complete set when done (the aggregate
//     only grows, so full ⊇ partial) — next request sees everything;
//   - nothing yet → keep waiting up to the hard deadline (a slow single source beats
//     an empty picker).
const (
	capiSoftDeadline = 4500 * time.Millisecond // first partial; RU sources (filmix/rezka/kodik multi-hop) often land 3-6s
	capiHardDeadline = 8 * time.Second
	capiDrillBudget  = 25 * time.Second // per-balancer drill-chain ceiling (background included)

	// TTL for a drill-derived "source has nothing for this card/episode"
	// verdict in csCache. Long enough (> capiStreamsTTL) that the SWR
	// background refresh skips known-empty sources instead of re-drilling all
	// of them; the all-skipped-and-still-empty purge in capiResolveStreamsDiag
	// keeps a freshly released episode from hiding behind stale negatives.
	capiDrillNegTTL = 10 * time.Minute
)

// capiDeadlines resolves the soft/hard deadlines, honouring the optional
// [capi] soft_deadline_ms / hard_deadline_ms overrides (0 = compiled default).
func capiDeadlines(cfg config.Config) (soft, hard time.Duration) {
	soft, hard = capiSoftDeadline, capiHardDeadline
	if ms := cfg.Capi.SoftDeadlineMS; ms > 0 {
		soft = time.Duration(ms) * time.Millisecond
	}
	if ms := cfg.Capi.HardDeadlineMS; ms > 0 {
		hard = time.Duration(ms) * time.Millisecond
	}
	if hard < soft {
		hard = soft
	}
	return soft, hard
}

// capiSourceDiag is one balancer's drill outcome — surfaced by the admin
// "sources debug" endpoint and the resolve log so dead/blocked sources are visible.
type capiSourceDiag struct {
	Balancer string `json:"balancer"`
	Voices   int    `json:"voices"`
	Ms       int64  `json:"ms"`
	// diag-only fields (populated when wantDiag): how this source's streams reach the client.
	Streams   int            `json:"streams,omitempty"` // count of capi-playable urls produced
	Forms     map[string]int `json:"forms,omitempty"`   // proxy / raw / transcoding / dropped tallies
	Quals     []string       `json:"quals,omitempty"`   // qualities THIS source offered — find who serves 2160p
	Sample    string         `json:"sample,omitempty"`  // one same-origin url form (truncated)
	SampleRaw string         `json:"-"`                 // one raw upstream url — for the live /proxy probe
}

// capiClassifyForm names how a capiSameOrigin result reaches the client: a same-origin /proxy
// token (server fetches), a /transcoding HLS (pidtor), a raw CDN url (no_stream_proxy → client
// fetches directly), or dropped (capiSameOrigin returned "" — couldn't mint).
func capiClassifyForm(u string) string {
	switch {
	case u == "":
		return "dropped"
	case strings.HasPrefix(u, "/proxy/"):
		return "proxy"
	case strings.HasPrefix(u, "/transcoding/"):
		return "transcoding"
	case strings.HasPrefix(u, "http"):
		return "raw"
	default:
		return "other"
	}
}

func capiTrunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// capiResolveAllow builds the canonical resolve_sources allowlist. When configured,
// /capi only resolves these balancers server-side — curate to server-resolvable sources
// and exclude browser-heavy/broken ones (mirage/alloha). Empty = resolve all. Entries
// are canonicalized (filmixpro→filmix, rhs→rhsprem, rc/x→x, …) and matched against the
// CANONICAL events name, so an alias on either side can't silently drop a source.
// ── anonymised source labels («Сервер N») ────────────────────────────────────────────────────
//
// Clients must never learn WHICH balancer served a stream, but support needs a handle: the user
// says «Сервер 3 не работает» and the admin resolves it. Numbers come from a process-wide registry
// seeded in sorted order, so they stay put for the life of the process and are reproducible from
// the config allowlist. The mapping is echoed inline for admins (capiVoice.Source) — no lookup
// table to maintain by hand.
var (
	capiServerMu  sync.Mutex
	capiServerNos = map[string]int{}
)

// capiServerNo returns the stable 1-based number for a balancer's canonical key.
func capiServerNo(balancer string) int {
	key := tgauth.CanonicalPluginKey(strings.TrimSpace(balancer))
	if key == "" {
		return 0
	}
	capiServerMu.Lock()
	defer capiServerMu.Unlock()
	if n, ok := capiServerNos[key]; ok {
		return n
	}
	n := len(capiServerNos) + 1
	capiServerNos[key] = n
	return n
}

// capiSeedServerNos pre-assigns numbers in sorted allowlist order, so «Сервер N» doesn't depend on
// which source happened to answer first after a restart. Called once per resolve (cheap, idempotent).
func capiSeedServerNos(cfg config.Config) {
	if len(cfg.Capi.ResolveSources) == 0 {
		return
	}
	keys := make([]string, 0, len(cfg.Capi.ResolveSources))
	for _, s := range cfg.Capi.ResolveSources {
		if k := tgauth.CanonicalPluginKey(strings.TrimSpace(s)); k != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	capiServerMu.Lock()
	defer capiServerMu.Unlock()
	for _, k := range keys {
		if _, ok := capiServerNos[k]; !ok {
			capiServerNos[k] = len(capiServerNos) + 1
		}
	}
}

// capiServerMap is the full «Сервер N» → balancer table (admin diagnostics).
func capiServerMap() map[int]string {
	capiServerMu.Lock()
	defer capiServerMu.Unlock()
	out := make(map[int]string, len(capiServerNos))
	for k, n := range capiServerNos {
		out[n] = k
	}
	return out
}

// capiIsAdmin reports whether the request's lampac token belongs to an admin — the only case where
// the real balancer name is allowed to leave the server.
func capiIsAdmin(r *http.Request) bool {
	if adminIDStoreRef == nil || tgTokenStoreRef == nil {
		return false
	}
	for _, tok := range collectLampacTokenCandidates(r) {
		if at, ok := tgTokenStoreRef.Lookup(tok); ok && at != nil && at.TelegramID != 0 {
			if adminIDStoreRef.IsAdmin(at.TelegramID) {
				return true
			}
		}
	}
	return false
}

func capiResolveAllow(cfg config.Config) map[string]bool {
	allow := map[string]bool{}
	for _, s := range cfg.Capi.ResolveSources {
		if s = tgauth.CanonicalPluginKey(strings.TrimSpace(s)); s != "" {
			allow[s] = true
		}
	}
	return allow
}

// capiResolveStreams drills every visible balancer for the card, dedupes by voice,
// drops balancer identity, and re-mints each stream as an opaque same-origin /proxy URL.
// Writes its result(s) into the streams cache under cacheKey ("" = don't cache — diag mode).
func capiResolveStreams(r *http.Request, cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry, ip, cacheKey string) capiStreams {
	out, _ := capiResolveStreamsDiag(r, cfg, proxyLinks, dynRoutes, ip, cacheKey, false)
	return out
}

// capiEnrichSearchMeta fills the MISSING identity fields (title/original_title/year/imdb_id, and
// serial when the id turns out to be a tv card) on the resolve request from TMDB. Most RU sources
// (filmix, rezka, kinopub, sakhtv, …) search by TITLE, not by tmdb/imdb id — given only an id they
// return an empty body ("{}"); others match by imdb_id or original_title, which capi clients never
// send (Lampa does — hence «в лампе находит, по capi нет»). Each field is filled independently, so
// a client that sends title+year still gets original_title/imdb_id backfilled. It backstops minimal
// clients AND the admin /capi/sources probe (which passes only id) so the diagnostic mirrors a real
// play.
// capiAltTitleSep joins alternative titles in the `alt_titles` query param. A non-printable unit
// separator avoids colliding with commas/spaces that appear inside real titles.
const capiAltTitleSep = "\x1f"

// capiPickAltTitles returns RU alternative titles worth retrying with — those that DIFFER (case-
// insensitively) from the primary title and original_title we already search by, deduped and capped.
// Cap keeps the on-miss retry cheap (Filmix etc. usually need just the one localized name, e.g.
// «Формула 1» for F1 whose TMDB title is "F1").
func capiPickAltTitles(alts []string, title, originalTitle string) []string {
	return capiPickTitlesN(alts, title, originalTitle, 2) // bound the on-miss retries
}

// capiPickMatchTitles is the wider match-only variant: these are never searched with, only
// matched against (similar filter / wrong-film guard), so the cap is generous enough to keep
// the UA alternatives that ride after the RU ones.
func capiPickMatchTitles(alts []string, title, originalTitle string) []string {
	return capiPickTitlesN(alts, title, originalTitle, 6)
}

func capiPickTitlesN(alts []string, title, originalTitle string, max int) []string {
	have := map[string]bool{
		strings.ToLower(strings.TrimSpace(title)):         true,
		strings.ToLower(strings.TrimSpace(originalTitle)): true,
	}
	var out []string
	for _, a := range alts {
		a = strings.TrimSpace(a)
		k := strings.ToLower(a)
		if a == "" || have[k] {
			continue
		}
		have[k] = true
		out = append(out, a)
		if len(out) >= max {
			break
		}
	}
	return out
}

// capiEnrichSearchMeta injects a kinopoisk_id for kp-only sources
// (hdvb/zetflix/cdnvideohub/…): they resolve ONLY by Kinopoisk id while the
// resolver is TMDB-keyed, so without this they bail instantly (0 voices, ms:0).
// Self-hosted & quota-free: persistent store → Wikidata (free) →
// kinopoiskapiunofficial fallback. No match = silent no-op. (See kpid_resolver.go.)
func capiEnrichSearchMeta(r *http.Request) *http.Request {
	q := r.URL.Query()
	id := capiInt(q.Get("id"))
	if id == 0 {
		return r // no tmdb id → nothing to resolve
	}
	typ := "movie"
	switch strings.TrimSpace(q.Get("serial")) {
	case "1", "true":
		typ = "tv"
	default:
		// A season/episode in the query can only mean a series, even without `serial`.
		if strings.TrimSpace(q.Get("s")) != "" || strings.TrimSpace(q.Get("e")) != "" {
			typ = "tv"
		}
	}

	if strings.TrimSpace(q.Get("kinopoisk_id")) != "" {
		return r
	}
	imdb := strings.TrimSpace(q.Get("imdb_id"))
	title := strings.TrimSpace(q.Get("title"))
	year := capiInt(q.Get("year"))
	kctx, kcancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer kcancel()
	kp, ok := resolveKpID(kctx, typ, id, imdb, title, year)
	if !ok {
		return r
	}
	q.Set("kinopoisk_id", strconv.Itoa(kp))
	clone := r.Clone(r.Context())
	clone.URL.RawQuery = q.Encode()
	log.Debug().Int("id", id).Int("kp", kp).Str("imdb", imdb).Msg("resolve: mapped kinopoisk_id for kp-only sources")
	return clone
}

func capiResolveStreamsDiag(r *http.Request, cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry, ip, cacheKey string, wantDiag bool) (capiStreams, []capiSourceDiag) {
	r = capiEnrichSearchMeta(r) // title-search sources (filmix/rezka/…) need a title, not just an id
	// Mark the whole resolve as a capi drill so eventPluginVisible lets resolve_sources govern
	// (not the per-user Lampa kit whitelist) — else admin-listed sources get silently hidden.
	r = r.WithContext(capiWithResolve(r.Context()))
	plugins := resolveEventsPlugins(r, cfg, dynRoutes)
	empty := capiStreams{Qualities: []string{}, Voices: []capiVoice{}}
	if len(plugins) == 0 {
		if cacheKey != "" {
			capiStreamsCachePut(cacheKey, empty)
		}
		return empty, nil
	}
	lang := firstLang(r.URL.Query().Get("original_language"))
	items := buildEventItems(r, cfg, plugins, hostFromRequest(r), lang, false, dynRoutes)
	log.Info().Int("plugins", len(plugins)).Int("items", len(items)).
		Str("id", r.URL.Query().Get("id")).Str("imdb_id", r.URL.Query().Get("imdb_id")).
		Str("s", r.URL.Query().Get("s")).Str("e", r.URL.Query().Get("e")).
		Msg("capi: streams resolve")

	liteSource := capiLiteSource(cfg, proxyLinks, dynRoutes)

	// «Сервер N» labels: seed the numbering from the sorted allowlist (stable across restarts for a
	// given config) and decide once whether this caller may see the real balancer names.
	capiSeedServerNos(cfg)
	isAdmin := capiIsAdmin(r)

	type vagg struct {
		name    string
		src     string // balancer that first backed this voice → «Сервер N»
		byq     map[string]string
		subs    []capiSubTrack // external tracks, urls already re-minted same-origin
		torrent bool           // a torrent source (pidtor) won at least one quality for this voice
		hdr     bool           // raw name/labels carried an HDR marker (stripped from the clean name)
		dv      bool           // Dolby Vision marker
		atmos   bool           // Dolby Atmos marker
		rip     string         // normalized rip token («WEB-DL», «TS»…) — best rank seen
		ripRank int
	}
	voices := map[string]*vagg{}
	quals := map[string]bool{}
	// Anime routing: generic balancers fuzzy-match an anime tmdbid by title and often return a WRONG
	// title («вместо одного выдаёт другое»). For anime content we keep generic-source voices in a
	// SEPARATE bucket and serve them only as a fallback when no anime-accurate source (kodik/veoveo/
	// anilibria/…, which match by kinopoisk/imdb/shikimori) answered. For non-anime nothing diverts.
	genericVoices := map[string]*vagg{}
	genericQuals := map[string]bool{}
	animeContent := capiIsAnimeRequest(r)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var diags []capiSourceDiag

	allow := capiResolveAllow(cfg)

	// Shared availability layer (csCache/csBreaker) hookup. Keys are the SAME
	// path+query form the /lite/events checksearch probes use, so the two
	// resolve paths warm each other; drill-negatives additionally get an
	// episode suffix — «в источнике нет S2E9» must not hide S1E1.
	csExtra := buildCheckSearchQuery(r.URL.Query())
	reqS := strings.TrimSpace(r.URL.Query().Get("s"))
	reqE := strings.TrimSpace(r.URL.Query().Get("e"))
	epSuffix := ""
	if reqS != "" || reqE != "" {
		epSuffix = "#s=" + reqS + "&e=" + reqE
	}
	var skippedNegKeys []string // purged if the whole resolve comes back empty

	for _, it := range items {
		if it.Balanser == "" {
			continue
		}
		if len(allow) > 0 && !allow[tgauth.CanonicalPluginKey(it.Balanser)] {
			continue // not in the /capi allowlist
		}
		// rch sources are NOT skipped — the drill carries a capi-resolve marker so rchGate
		// does a direct server-side fetch instead of the device handshake.
		wg.Add(1)
		go func(it liteEventItem) {
			defer wg.Done()
			bal := it.Balanser
			balKey := bal
			if i := strings.IndexByte(balKey, '|'); i > 0 {
				balKey = balKey[:i] // vokino|sub splits share the parent's breaker
			}
			cardKey := checksearchTargetPath(appendCheckQuery(it.URL, csExtra))
			epKey := cardKey + epSuffix
			if !wantDiag {
				// Same circuit breaker the checksearch probes use: a balancer
				// that's been hard-failing gets skipped instead of burning a
				// 25s drill budget on every cold resolve.
				if csBreaker.isOpen(balKey) {
					log.Debug().Str("bal", bal).Msg("capi: drill skipped — breaker open")
					return
				}
				// Our own fresh «этот источник пуст для этой карточки/серии»
				// verdict — no point re-drilling for another few minutes.
				if csCache.drillNegativeActive(epKey) {
					mu.Lock()
					skippedNegKeys = append(skippedNegKeys, epKey)
					mu.Unlock()
					log.Debug().Str("bal", bal).Msg("capi: drill skipped — fresh drill-negative")
					return
				}
			}
			started := time.Now()
			raw, outcome := capiDrill(liteSource, r, bal, ip)
			elapsed := time.Since(started)
			// per-source detail for the admin/terminal: which balancer, how many voices, which
			// quality labels it actually offered, and how long it took. A 0-voices source is the
			// usual «нет в источниках» culprit, so make it stand out.
			qset := map[string]struct{}{}
			for _, v := range raw {
				for q := range v.byq {
					qset[q] = struct{}{}
				}
			}
			qlist := make([]string, 0, len(qset))
			for q := range qset {
				qlist = append(qlist, q)
			}
			ev := log.Info()
			if len(raw) == 0 {
				ev = log.Warn()
			}
			ev.Str("bal", bal).Int("voices", len(raw)).Strs("quals", qlist).Dur("took", elapsed).Msg("capi: drill source")
			if !wantDiag {
				// Feed the shared availability layer + breaker (diag mode stays
				// side-effect-free for the admin debug endpoint). A positive is
				// card-level — content exists at the source, warms /lite/events
				// too; a negative is episode-level and capi-private (see
				// setFromDrill / drillNegativeActive for the asymmetry).
				switch outcome {
				case probeOk:
					qb := make(map[string]bool, len(qset))
					for q := range qset {
						qb[q] = true
					}
					top := ""
					if sorted := capiSortQualities(qb); len(sorted) > 0 {
						top = sorted[0]
					}
					csCache.setFromDrill(cardKey, true, top, checksearchCacheTTL)
					csBreaker.recordSuccess(balKey)
				case probeEmpty:
					csCache.setFromDrill(epKey, false, "", capiDrillNegTTL)
					csBreaker.recordSuccess(balKey)
				case probeErr:
					csBreaker.recordFail(balKey)
				}
			}
			for _, v := range raw {
				name := capiCleanVoice(v.name)
				if name == "" {
					continue // release/title junk with no voice meaning — drop
				}
				key := capiNormVoice(name)
				mu.Lock()
				// For anime, a generic (non-anime-accurate) source's voices go to the fallback bucket.
				vmap, qmap := voices, quals
				if animeContent && !animePreferredSources[bal] {
					vmap, qmap = genericVoices, genericQuals
				}
				agg := vmap[key]
				if agg == nil {
					agg = &vagg{name: name, byq: map[string]string{}}
					vmap[key] = agg
				}
				// Remember WHICH balancer first backed this voice — that's what «Сервер N» names.
				// First writer wins so the label matches whoever actually serves the streams below
				// (a later source only fills qualities the first one didn't have).
				if agg.src == "" {
					agg.src = bal
				}
				// harvest HDR/DV/Atmos + rip type from the RAW name before it was cleaned, and labels
				if h, dvf, at := capiTechFlags(v.name); h || dvf || at {
					agg.hdr, agg.dv, agg.atmos = agg.hdr || h, agg.dv || dvf, agg.atmos || at
				}
				if r, rk := capiRipType(v.name); r != "" && (agg.rip == "" || rk < agg.ripRank) {
					agg.rip, agg.ripRank = r, rk
				}
				for q, ru := range v.byq {
					if h, dvf, at := capiTechFlags(q); h || dvf || at {
						agg.hdr, agg.dv, agg.atmos = agg.hdr || h, agg.dv || dvf, agg.atmos || at
					}
					if r, rk := capiRipType(q); r != "" && (agg.rip == "" || rk < agg.ripRank) {
						agg.rip, agg.ripRank = r, rk
					}
					qmap[q] = true
					if _, exists := agg.byq[q]; exists {
						continue
					}
					if u := capiSameOrigin(ru, proxyLinks, ip, bal); u != "" {
						agg.byq[q] = u
						if capiIsTorrentSource(bal) {
							agg.torrent = true
						}
					}
				}
				// External subtitle tracks ride along with the voice (they apply to every
				// quality). First source to bring tracks wins; extra labels merge in deduped.
				if len(v.subs) > 0 {
					have := make(map[string]bool, len(agg.subs))
					for _, st := range agg.subs {
						have[st.Label] = true
					}
					for _, st := range v.subs {
						if st.URL == "" || have[st.Label] {
							continue
						}
						if u := capiSameOrigin(st.URL, proxyLinks, ip, bal); u != "" {
							agg.subs = append(agg.subs, capiSubTrack{Label: st.Label, URL: u})
							have[st.Label] = true
						}
					}
				}
				mu.Unlock()
			}
			d := capiSourceDiag{Balancer: bal, Voices: len(raw), Ms: elapsed.Milliseconds()}
			if wantDiag {
				// Classify how THIS source's streams reach the client (independent of the
				// cross-source dedup above), so the admin sees per-source proxy/raw/transcode form.
				forms := map[string]int{}
				q2u := map[string]string{} // quality → one CLIENT-FACING url (first non-dropped)
				for _, v := range raw {
					for q, ru := range v.byq {
						u := capiSameOrigin(ru, proxyLinks, ip, bal)
						f := capiClassifyForm(u)
						forms[f]++
						if f != "dropped" {
							d.Streams++
							if q2u[q] == "" {
								q2u[q] = u
							}
						}
					}
				}
				d.Forms = forms
				qset := make(map[string]bool, len(q2u))
				for q := range q2u {
					qset[q] = true
				}
				d.Quals = capiSortQualities(qset) // which qualities THIS source serves (→ who has 2160p)
				// Sample the HIGHEST quality — the 4K variant is the one that tends to 404/undecode
				// (incident 0622: «Марсианин» 2160p → /proxy 404), so the probe tests the failing url.
				// Probe the CLIENT-FACING url (after " or "/reserve split + /proxy wrap), not the raw
				// upstream — else a multi-CDN "url1 or url2" string is fetched whole and 400s.
				if len(d.Quals) > 0 {
					top := q2u[d.Quals[0]]
					d.Sample = capiTrunc(top, 90)
					d.SampleRaw = top
				}
			}
			mu.Lock()
			diags = append(diags, d)
			mu.Unlock()
		}(it)
	}

	// snapshot assembles the response from whatever has aggregated so far. byq maps are
	// COPIED — background drills keep mutating the aggregates after a partial return.
	snapshot := func() capiStreams {
		out := capiStreams{Qualities: []string{}, Voices: make([]capiVoice, 0, len(voices))}
		mu.Lock()
		defer mu.Unlock()
		// Anime: use the anime-source bucket; fall back to generic ONLY when no anime source produced
		// a stream (so an anime with no kodik/veoveo hit still plays, but a normal one never mixes in
		// a wrong generic title). Non-anime: genericVoices is empty, so this is the usual single set.
		chosen, chosenQuals := voices, quals
		if animeContent {
			animeStreams := false
			for _, a := range voices {
				if len(a.byq) > 0 {
					animeStreams = true
					break
				}
			}
			if !animeStreams {
				chosen, chosenQuals = genericVoices, genericQuals
			}
		}
		out.Qualities = capiSortQualities(chosenQuals)
		for key, a := range chosen {
			if len(a.byq) == 0 {
				continue
			}
			byq := make(map[string]string, len(a.byq))
			for q, u := range a.byq {
				byq[q] = u
			}
			subs := append([]capiSubTrack(nil), a.subs...)
			cv := capiVoice{ID: key, Name: a.name, ByQuality: byq, Subtitles: subs, Torrent: a.torrent, HDR: a.hdr, DV: a.dv, Atmos: a.atmos, Rip: a.rip}
			if a.src != "" {
				cv.Server = capiServerNo(a.src)
				if isAdmin { // real balancer name never leaves the server for normal users
					cv.Source = tgauth.CanonicalPluginKey(a.src)
				}
			}
			out.Voices = append(out.Voices, cv)
		}
		sort.Slice(out.Voices, func(i, j int) bool { return out.Voices[i].Name < out.Voices[j].Name })
		return out
	}

	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()

	softDL, hardDL := capiDeadlines(cfg)
	finished := false
	if wantDiag {
		// diag mode: the caller wants the COMPLETE per-balancer picture — wait it out.
		select {
		case <-drained:
			finished = true
		case <-time.After(hardDL):
		}
	} else {
		select {
		case <-drained:
			finished = true
		case <-time.After(softDL):
		}
	}

	out := snapshot()
	if !finished && len(out.Voices) == 0 && !wantDiag {
		// Nothing yet — a slow single source still beats an empty picker. Hold on
		// up to the hard deadline.
		select {
		case <-drained:
			finished = true
		case <-time.After(hardDL - softDL):
		}
		out = snapshot()
	}

	// EMPTY result while some sources were skipped on drill-negatives → drop
	// those negatives now. A freshly released episode appears at exactly one
	// source, and that source may be among the skipped; empties are only cached
	// 30s, so the very next attempt re-drills everything instead of staring at
	// stale «пусто» verdicts for their full TTL.
	if len(out.Voices) == 0 {
		mu.Lock()
		purge := skippedNegKeys
		skippedNegKeys = nil
		mu.Unlock()
		for _, k := range purge {
			csCache.delete(k)
		}
	}

	if cacheKey != "" {
		capiStreamsCachePut(cacheKey, out)
		if !finished {
			// The slow tail keeps drilling (each capiDrill carries its own budget). Re-snapshot
			// PERIODICALLY and update the cache whenever the set grows — NOT only at full drain.
			// The old all-or-nothing backfill (`<-drained`) waited on the SLOWEST balancer (up to the
			// 25s budget), so a single hanging source kept the partial cached for its whole budget
			// even though every other source had already landed seconds earlier — the «выводит меньше,
			// чем есть; упал один источник → сразу появилась куча других» report (the straggler finally
			// died, drain fired, the long-ready set got published). Now the cache catches up within ~1s.
			go func() {
				best := out
				tick := time.NewTicker(800 * time.Millisecond)
				defer tick.Stop()
				budget := time.After(capiDrillBudget)
				publish := func(tag string) {
					s := snapshot()
					// Stream count catches growth the other two miss: an EXISTING voice gaining a
					// 2160p url whose label another voice already contributed to Qualities.
					if len(s.Voices) > len(best.Voices) || len(s.Qualities) > len(best.Qualities) ||
						capiStreamCount(s) > capiStreamCount(best) {
						best = s
						capiStreamsCachePut(cacheKey, best)
						log.Info().Int("voices", len(best.Voices)).Int("qualities", len(best.Qualities)).
							Str("at", tag).Msg("capi: streams backfilled")
					}
				}
				for {
					select {
					case <-drained:
						publish("drained")
						return
					case <-budget:
						return
					case <-tick.C:
						publish("tick")
					}
				}
			}()
		}
	}

	log.Info().Int("voices", len(out.Voices)).Int("qualities", len(out.Qualities)).
		Bool("complete", finished).Msg("capi: streams done")
	if wantDiag {
		<-drained // diag callers report exact numbers — make sure the slice is final
		mu.Lock()
		d := make([]capiSourceDiag, len(diags))
		copy(d, diags)
		mu.Unlock()
		sort.Slice(d, func(i, j int) bool { return d[i].Balancer < d[j].Balancer })
		return snapshot(), d
	}
	return out, nil
}

type capiRawVoice struct {
	name string
	byq  map[string]string // quality -> raw upstream url (pre re-mint)
	subs []capiSubTrack    // external subtitle tracks (raw upstream urls)
}

// capiResolveCtxKey marks a request as a server-side /capi aggregation drill. Balancers'
// rchGate checks it (capiResolveRequest) and does a direct fetch instead of the device
// (reverse-client-hub) handshake — so rch sources can resolve server-side.
type capiResolveCtxKey struct{}

func capiWithResolve(ctx context.Context) context.Context {
	return context.WithValue(ctx, capiResolveCtxKey{}, true)
}

func capiResolveRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	v, _ := r.Context().Value(capiResolveCtxKey{}).(bool)
	return v
}

type capiDrillItem struct {
	Method    string           `json:"method"`
	Name      string           `json:"name"`
	Title     string           `json:"title"`
	Translate string           `json:"translate"`
	Voice     string           `json:"voice"`
	Details   string           `json:"details"`
	Quality   ejson.RawMessage `json:"quality"`
	URL       string           `json:"url"`
	Stream    string           `json:"stream"`
	S         any              `json:"s"`
	E         any              `json:"e"`
	Year      any              `json:"year"` // present on "similar" search-result candidates — used to disambiguate same-title films
	Subtitles []capiSubTrack   `json:"subtitles"`
}

// capiVoiceSel is one entry of a series translation selector (top-level "voice" array).
type capiVoiceSel struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
	URL    string `json:"url"`
}

type capiLiteResp struct {
	Type  string          `json:"type"`
	Data  []capiDrillItem `json:"data"`
	Voice []capiVoiceSel  `json:"voice"`
}

const (
	capiMaxDepth   = 2 // follow call/link continuations this many hops (Rezka/Filmix multi-hop)
	capiMaxFollows = 4 // and at most this many continuation nodes per response
	capiMaxVoices  = 8 // cap series translation-selector expansion
)

// capiDrill runs an in-process /lite/{balancer}?...&rjson=true and extracts
// {voice -> {quality -> url}}, following call/link continuation nodes (search hit →
// film → translations) up to a bounded depth. The synthetic request carries the real
// client IP (proxy minting binds to the user) + a capi-resolve marker (rch balancers
// fetch directly server-side instead of the device handshake).
// capiDrill's second return classifies the drill for the shared availability
// layer (csCache/csBreaker): probeOk = playable voices extracted; probeEmpty =
// every fetch parsed fine but produced nothing (a strong "source has no
// content for this card", alt-title retries included); probeErr = not even one
// hop returned a parseable 200 (handler/upstream failure — breaker material).
func capiDrill(liteSource http.HandlerFunc, r *http.Request, balancer, ip string) ([]capiRawVoice, probeOutcome) {
	q := r.URL.Query()
	q.Set("rjson", "true")
	// Hard deadline for the WHOLE drill chain of one balancer. The aggregate responds at
	// the soft/hard deadline, but late drills KEEP RUNNING on purpose — their results
	// backfill the shared cache (see capiResolveStreamsDiag). The budget caps how long
	// that tail may pin upstream connections and buffers.
	ctx, cancel := context.WithTimeout(capiWithResolve(context.Background()), capiDrillBudget)
	defer cancel()
	out, fetched := capiDrillTarget(ctx, liteSource, r, "/lite/"+balancer+"?"+q.Encode(), ip, balancer, 0)
	if len(out) > 0 {
		return out, probeOk
	}
	// On-miss only: retry with RU alternative titles. Title-search sources (filmix/rezka/sakhtv/…)
	// index the localized name, which often differs from the TMDB title we just searched (e.g. Filmix
	// lists the F1 movie as «Формула 1»). Bounded to alt titles that differ from the primary (set by
	// capiEnrichSearchMeta, capped at 2), and only when the primary search found nothing — so popular
	// content and id-search sources pay nothing extra.
	for _, alt := range strings.Split(r.URL.Query().Get("alt_titles"), capiAltTitleSep) {
		alt = strings.TrimSpace(alt)
		if alt == "" || ctx.Err() != nil {
			continue
		}
		q2 := r.URL.Query()
		q2.Set("rjson", "true")
		q2.Set("title", alt)
		q2.Set("original_title", alt)
		q2.Del("alt_titles") // don't recurse alt-titles into the source request
		v, altOK := capiDrillTarget(ctx, liteSource, r, "/lite/"+balancer+"?"+q2.Encode(), ip, balancer, 0)
		fetched = fetched || altOK
		if len(v) > 0 {
			log.Debug().Str("bal", balancer).Str("alt", alt).Msg("capi: recovered via alt title")
			return v, probeOk
		}
	}
	if !fetched {
		return out, probeErr
	}
	return out, probeEmpty
}

// capiFetchLite does one in-process /lite drill and returns the parsed response.
func capiFetchLite(ctx context.Context, liteSource http.HandlerFunc, r *http.Request, target, ip string) (capiLiteResp, int, string, bool) {
	if ctx.Err() != nil {
		return capiLiteResp{}, 0, "", false // chain deadline already spent — don't start another hop
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(ctx)
	// httptest defaults Host to "example.com". Balancers that embed a
	// self-referential URL into the stream (PidTor's transcode src =
	// .../lite/pidtor/s…, wrapped as /transcoding/start.m3u8?src=…) would bake
	// "example.com" into it; the server-side transcoder then can't fetch the src
	// and the playback 502s. Point the drill at the real loopback host so those
	// self-URLs resolve back to this lampac. CDN-based balancers build absolute
	// upstream URLs from their own config host, so this doesn't affect them.
	if serverReady() {
		if lb := loopbackHostPort(liveConfig(config.Config{}).Server.Addr); lb != "" {
			req.Host = lb
		}
	}
	req.Header.Set("X-Real-IP", ip)
	if t := capiToken(r); t != "" {
		req.Header.Set("X-Lampac-Token", t)
	}
	rec := httptest.NewRecorder()
	liteSource.ServeHTTP(rec, req)
	body := rec.Body.Bytes()
	sample := capiTrunc(strings.TrimSpace(string(body)), 220) // for the "0 voices — raw body" diagnostic
	if rec.Code != http.StatusOK {
		return capiLiteResp{}, len(body), sample, false
	}
	var parsed capiLiteResp
	if err := ejson.Unmarshal(body, &parsed); err != nil {
		return capiLiteResp{}, len(body), sample, false
	}
	if len(parsed.Data) == 0 && len(parsed.Voice) == 0 {
		// Two rjson shapes the strict {type,data,voice} parse misses:
		//  - a single top-level play object ({"method":"play","url":…}) — what
		//    call-continuation endpoints like cdnvideohub's video.m3u8 return;
		//  - {"html":"<div … data-json='{…}'>…"} — Lampa-oriented balancers
		//    (mikai/animeon) ship the item list as rendered HTML whose data-json
		//    attributes hold the same play/call objects.
		// Both carried real streams that the drill used to drop as «0 voices».
		if item, ok := capiParseSinglePlay(body); ok {
			parsed.Data = []capiDrillItem{item}
		} else if items := capiParseHTMLItems(body); len(items) > 0 {
			parsed.Data = items
		}
	}
	return parsed, len(body), sample, true
}

// capiParseSinglePlay parses a bare top-level play/call object into one drill item.
// Some endpoints carry the stream only in the quality map (anwap /play returns
// {"method":"play","quality":{"auto":…}} with no url) — that counts as playable too.
func capiParseSinglePlay(body []byte) (capiDrillItem, bool) {
	var item capiDrillItem
	if err := ejson.Unmarshal(body, &item); err != nil {
		return capiDrillItem{}, false
	}
	if item.Method == "" || (item.URL == "" && item.Stream == "" && len(capiParseQuality(item.Quality)) == 0) {
		return capiDrillItem{}, false
	}
	return item, true
}

// Visible label of an html item — the voice/episode name lives in the element's
// TEXT, not in data-json: either the button text right after the attribute
// (`'>Дубляж<`) or the first videos__item-title div in the same element.
var (
	capiHTMLBtnLabelRe  = regexp.MustCompile(`^'[^>]*>([^<]+)<`)
	capiHTMLItemTitleRe = regexp.MustCompile(`videos__item-title[^"]*">([^<]+)<`)
)

// capiParseHTMLItems extracts play/call/link items from an {"html": …} rjson body.
// Writers (litehtml.AppendMovieHTML, jsmodules) single-quote the data-json attribute
// and HTML-escape the value, so an inner apostrophe can't terminate the split early.
func capiParseHTMLItems(body []byte) []capiDrillItem {
	var wrap struct {
		HTML string `json:"html"`
	}
	if ejson.Unmarshal(body, &wrap) != nil || wrap.HTML == "" {
		return nil
	}
	parts := strings.Split(wrap.HTML, "data-json='")
	var out []capiDrillItem
	for _, part := range parts[1:] {
		end := strings.IndexByte(part, '\'')
		if end <= 0 {
			continue
		}
		var item capiDrillItem
		if ejson.Unmarshal([]byte(stdhtml.UnescapeString(part[:end])), &item) != nil {
			continue
		}
		if item.URL == "" && item.Stream == "" {
			continue
		}
		if item.Name == "" && item.Voice == "" && item.Translate == "" {
			tail := part[end:] // runs up to the NEXT data-json attribute — this element only
			label := ""
			if m := capiHTMLItemTitleRe.FindStringSubmatch(tail); m != nil {
				label = m[1]
			} else if m := capiHTMLBtnLabelRe.FindStringSubmatch(tail); m != nil {
				label = m[1]
			}
			item.Name = strings.TrimSpace(stdhtml.UnescapeString(label))
		}
		out = append(out, item)
	}
	return out
}

// The bool return reports whether THIS level's fetch returned a parseable 200 —
// capiDrill uses the depth-0 value to distinguish "source failed" (probeErr)
// from "source answered but has nothing" (probeEmpty).
func capiDrillTarget(ctx context.Context, liteSource http.HandlerFunc, r *http.Request, target, ip, balancer string, depth int) ([]capiRawVoice, bool) {
	resp, blen, body, ok := capiFetchLite(ctx, liteSource, r, target, ip)
	if depth == 0 {
		log.Info().Str("bal", balancer).Int("len", blen).Bool("ok", ok).Msg("capi: drill")
	}
	if !ok {
		if depth == 0 {
			// Non-200 or unparseable — show what came back so a dead/blocked drill is greppable.
			log.Warn().Str("bal", balancer).Int("bytes", blen).Str("body", body).
				Msg("capi: drill failed — raw body")
		}
		return nil, false
	}

	// Content-type guard: a source that fuzzy-matched a DIFFERENT same-named title
	// of the wrong kind (a film for a series card, or a series for a movie card)
	// must not contribute its voices to the merge — that's the «выбрал озвучку,
	// играет другой фильм» class. Dropped as fetched-but-empty so it caches a
	// negative instead of re-drilling. (Same-kind wrong matches — e.g. an old
	// same-named series — need per-source year/id pinning; this only catches the
	// movie↔serial contradiction.)
	if capiTypeMismatch(r.URL.Query(), resp.Type) {
		if depth == 0 {
			log.Info().Str("bal", balancer).Str("resp_type", resp.Type).
				Bool("card_serial", capiCardIsSerial(r.URL.Query())).
				Msg("capi: dropped source — content type mismatch")
		}
		return nil, true
	}

	reqS := strings.TrimSpace(r.URL.Query().Get("s"))
	reqE := strings.TrimSpace(r.URL.Query().Get("e"))

	// Series translation selector: the response carries a top-level "voice" array (each a
	// named dub with a re-drill url), while "data" holds the active dub's episodes. Expand
	// every dub into the requested episode's stream so the picker shows real озвучки instead
	// of one "Основная" bucket.
	if reqE != "" && len(resp.Voice) > 0 {
		return capiExpandSeriesVoices(ctx, liteSource, r, resp, ip, reqS, reqE, depth), true
	}

	// "similar" = ambiguous search results → the balancer couldn't pin the exact card and
	// returned a CANDIDATE LIST. In the real Lampa client the user eyeballs these and taps the
	// right one; a /capi resolve has no user, so it auto-follows the top hit. Following it
	// BLINDLY hands back a DIFFERENT title whenever the search is fuzzy — «ткнул мандалорца,
	// заиграл майор гром». Keep only candidates whose title (and year) match the requested card,
	// best-match first; if none match, follow NONE — an empty source beats the wrong film.
	maxFollow := capiMaxFollows
	if resp.Type == "similar" {
		maxFollow = 1
		resp.Data = capiPickSimilar(resp.Data, r.URL.Query())
		if len(resp.Data) == 0 {
			if depth == 0 {
				log.Info().Str("bal", balancer).Str("title", r.URL.Query().Get("title")).
					Str("year", r.URL.Query().Get("year")).
					Msg("capi: similar — no candidate matched requested title, dropping source")
			}
			return nil, true
		}
	}

	// Wrong-film guard. A non-"similar" response can still be a fuzzy search's wrong pick —
	// anwap answered «Закулисье реальности» with a direct type:"movie" item titled «Человек-паук»,
	// which the similar filter never sees. When the balancer labels an item with the film it
	// actually FOUND and that title matches none of the requested names (title/original_title/
	// alt/match titles), don't play or follow it — the wrong film is worse than an empty source.
	// Items that echo the requested title match trivially; items with no title (voice/season
	// rows) are never guarded; episode-indexed items are already pinned to the card.
	var guardTitles []string
	if resp.Type == "movie" || resp.Type == "" {
		guardTitles = capiRequestTitles(r.URL.Query())
	}
	guardMismatch := func(d capiDrillItem) bool {
		if len(guardTitles) == 0 {
			return false
		}
		t := strings.TrimSpace(d.Title)
		if t == "" || capiNum(d.E) != "" {
			return false
		}
		return !capiTitleMatchesAny(guardTitles, t)
	}

	var out []capiRawVoice
	var followTargets []string
	follows := 0
	for _, d := range resp.Data {
		if guardMismatch(d) {
			if depth == 0 {
				log.Info().Str("bal", balancer).Str("item_title", capiTrunc(d.Title, 80)).
					Str("title", r.URL.Query().Get("title")).
					Msg("capi: dropped item — balancer returned a different film")
			}
			continue
		}
		if capiItemYearMismatch(r.URL.Query(), d) {
			if depth == 0 {
				log.Info().Str("bal", balancer).Str("item_year", capiNum(d.Year)).
					Str("card_year", r.URL.Query().Get("year")).
					Msg("capi: dropped item — year mismatch (different same-named release)")
			}
			continue
		}
		if d.Method == "call" || d.Method == "link" {
			if depth >= capiMaxDepth || follows >= maxFollow {
				continue
			}
			if es := capiNum(d.E); reqE != "" && es != "" && es != reqE {
				continue // serial: only descend the requested episode's branch
			}
			if ss := capiNum(d.S); reqS != "" && ss != "" && ss != reqS {
				continue
			}
			next := capiLitePath(d.URL)
			if next == "" {
				continue
			}
			follows++
			followTargets = append(followTargets, next)
			continue
		}
		var name string
		if es := capiNum(d.E); reqE != "" && es != "" {
			// Episode-indexed play-item (no voice selector). Keep ONLY the requested episode;
			// use the real translation if present, else one anonymized bucket.
			if es != reqE {
				continue
			}
			if ss := capiNum(d.S); reqS != "" && ss != "" && ss != reqS {
				continue
			}
			name = firstNonEmpty(d.Voice, d.Translate, d.Details)
			if name == "" {
				name = "Основная"
			}
		} else {
			name = firstNonEmpty(d.Name, d.Title, d.Translate)
		}
		byq := capiParseQuality(d.Quality)
		if len(byq) == 0 {
			if u := firstNonEmpty(d.URL, d.Stream); capiIsStream(u) {
				byq = map[string]string{"auto": u}
			}
		}
		if name != "" && len(byq) > 0 {
			out = append(out, capiRawVoice{name: name, byq: byq, subs: d.Subtitles})
		}
	}
	// Continuation hops fan out CONCURRENTLY — sequential follows made a multi-translation
	// drill pay N×RTT (search hit → film → each translation). Results keep target order.
	switch len(followTargets) {
	case 0:
	case 1:
		res, _ := capiDrillTarget(ctx, liteSource, r, followTargets[0], ip, balancer, depth+1)
		out = append(out, res...)
	default:
		results := make([][]capiRawVoice, len(followTargets))
		var fwg sync.WaitGroup
		for i, next := range followTargets {
			fwg.Add(1)
			go func(i int, next string) {
				defer fwg.Done()
				results[i], _ = capiDrillTarget(ctx, liteSource, r, next, ip, balancer, depth+1)
			}(i, next)
		}
		fwg.Wait()
		for _, res := range results {
			out = append(out, res...)
		}
	}
	if depth == 0 && len(out) == 0 {
		// The drill parsed OK but yielded no playable voice. Show the raw body + structure so the
		// cause is unmistakable: empty body (search miss / auth / proxy), a quality shape we don't
		// parse, or url/stream fields that aren't a media url (capiIsStream dropped them).
		log.Warn().Str("bal", balancer).Int("bytes", blen).Int("data", len(resp.Data)).
			Int("voice_sel", len(resp.Voice)).Str("type", resp.Type).Str("body", body).
			Msg("capi: drill 0 voices — raw body")
	}
	return out, true
}

// capiExpandSeriesVoices turns a series translation selector into one voice per dub: the
// active dub uses the already-fetched episode data; the rest are drilled by their url —
// CONCURRENTLY, since a sequential expansion paid up to capiMaxVoices×RTT per serial card.
func capiExpandSeriesVoices(ctx context.Context, liteSource http.HandlerFunc, r *http.Request, resp capiLiteResp, ip, reqS, reqE string, depth int) []capiRawVoice {
	n := len(resp.Voice)
	if n > capiMaxVoices {
		n = capiMaxVoices
	}
	results := make([][]capiDrillItem, n) // per-dub episode data, index-aligned with resp.Voice
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		vc := resp.Voice[i]
		if strings.TrimSpace(vc.Name) == "" {
			continue
		}
		if vc.Active || depth >= capiMaxDepth {
			results[i] = resp.Data // active dub: episodes already in hand (no extra fetch)
			continue
		}
		next := capiLitePath(vc.URL)
		if next == "" {
			continue
		}
		wg.Add(1)
		go func(i int, next string) {
			defer wg.Done()
			if r2, _, _, ok := capiFetchLite(ctx, liteSource, r, next, ip); ok {
				results[i] = r2.Data
			}
		}(i, next)
	}
	wg.Wait()

	var out []capiRawVoice
	for i := 0; i < n; i++ {
		name := strings.TrimSpace(resp.Voice[i].Name)
		if name == "" {
			continue
		}
		for _, d := range results[i] {
			if es := capiNum(d.E); es != "" && es != reqE {
				continue
			}
			if ss := capiNum(d.S); reqS != "" && ss != "" && ss != reqS {
				continue
			}
			byq := capiParseQuality(d.Quality)
			if len(byq) == 0 {
				if u := firstNonEmpty(d.URL, d.Stream); capiIsStream(u) {
					byq = map[string]string{"auto": u}
				}
			}
			if len(byq) > 0 {
				out = append(out, capiRawVoice{name: name, byq: byq, subs: d.Subtitles})
				break // one stream (the requested episode) per dub
			}
		}
	}
	return out
}

// capiLitePath turns a continuation URL (often absolute, e.g.
// "https://host/lite/rezka?href=...") into an in-process "/lite/...&rjson=true" path.
func capiLitePath(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.Index(u, "/lite/"); i >= 0 {
		p := u[i:]
		if !strings.Contains(p, "rjson=") {
			if strings.Contains(p, "?") {
				p += "&rjson=true"
			} else {
				p += "?rjson=true"
			}
		}
		return p
	}
	return ""
}

// capiNum stringifies a JSON season/episode field that may arrive as a number or string.
func capiNum(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case ejson.Number:
		return string(x)
	}
	return ""
}

var capiYearRe = regexp.MustCompile(`(?:19|20)\d{2}`)

// capiYear4 extracts the first 4-digit year (19xx/20xx) from a value that may be a number,
// a string, or a range like "2019-2023" (series carry ranges) — returns "" when none.
func capiYear4(v any) string {
	return capiYearRe.FindString(toString(v))
}

func capiYearGap(a, b string) int {
	ai, _ := strconv.Atoi(a)
	bi, _ := strconv.Atoi(b)
	if ai == 0 || bi == 0 {
		return 1 << 29 // one side unknown — neutral, sorts after exact-year matches
	}
	if ai > bi {
		return ai - bi
	}
	return bi - ai
}

// capiNormTitle lowercases a title and reduces it to space-separated alphanumeric tokens:
// punctuation, ё→е and a trailing "(2019)" all collapse to separators. Two titles differing
// only in casing/punctuation normalize to comparable token streams.
func capiNormTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 'ё':
			b.WriteRune('е')
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// capiTitleTokensMatch is a lenient, symmetric title comparison: the shorter title's tokens must
// (almost) all appear in the longer one. It tolerates subtitle/article differences ("Звёздные
// войны: Мандалорец" ~ "Мандалорец", "Веном" ~ "Веном 2") while rejecting unrelated titles
// ("Мандалорец" vs "Майор Гром" share no tokens → no match).
func capiTitleTokensMatch(a, b string) bool {
	at := strings.Fields(capiNormTitle(a))
	bt := strings.Fields(capiNormTitle(b))
	if len(at) == 0 || len(bt) == 0 {
		return false
	}
	short, long := at, bt
	if len(bt) < len(at) {
		short, long = bt, at
	}
	set := make(map[string]bool, len(long))
	for _, t := range long {
		set[t] = true
	}
	common := 0
	for _, t := range short {
		if set[t] {
			common++
		}
	}
	need := len(short)
	if need > 3 {
		need-- // longer titles: allow one slack token (an article, a punctuation-split word)
	}
	return common >= need
}

// capiCardIsSerial reports whether the requested card is a series. serial=1/true
// (client or capiEnrichSearchMeta's tv lookup sets it) or a concrete episode
// (s/e present) means series; everything else is treated as a movie — the same
// default capiEnrichSearchMeta and the title guard already assume.
func capiCardIsSerial(q url.Values) bool {
	switch strings.ToLower(strings.TrimSpace(q.Get("serial"))) {
	case "1", "true":
		return true
	}
	return strings.TrimSpace(q.Get("s")) != "" || strings.TrimSpace(q.Get("e")) != ""
}

// capiTypeMismatch reports whether a source's response type contradicts the card
// kind: a same-named MOVIE resolved for a SERIES card, or a same-named SERIES for
// a MOVIE card. That's the «выбрал озвучку — играет другой фильм» class where a
// fuzzy title search hit the wrong same-named title of the other kind. Empty and
// "similar" types are not judged (the title/similar guards handle those).
func capiTypeMismatch(q url.Values, respType string) bool {
	serial := capiCardIsSerial(q)
	switch strings.ToLower(strings.TrimSpace(respType)) {
	case "movie":
		return serial // series card, movie response → wrong same-named film
	case "serial", "season", "episode":
		return !serial // movie card, serial response → wrong same-named series
	}
	return false
}

// capiItemYearMismatch reports whether a play item's own year is off from the
// card year by more than a year — a different same-named release. Applied to
// MOVIE cards only: series seasons legitimately air in later years, and few
// sources echo a per-item year anyway, so this is a best-effort net.
func capiItemYearMismatch(q url.Values, d capiDrillItem) bool {
	if capiCardIsSerial(q) {
		return false
	}
	reqYear := capiYear4(q.Get("year"))
	itemYear := capiYear4(capiNum(d.Year))
	if reqYear == "" || itemYear == "" {
		return false
	}
	return capiYearGap(reqYear, itemYear) > 1
}

func capiTitleMatchesAny(reqTitles []string, cand string) bool {
	for _, req := range reqTitles {
		if req != "" && capiTitleTokensMatch(req, cand) {
			return true
		}
	}
	return false
}

// capiRequestTitles collects every name the requested card is known under: the client's
// title/original_title plus the TMDB alternative titles the enrichment attached (alt_titles =
// the bounded RU retry list, match_titles = the wider RU+UA match-only list). Similar-candidate
// filtering and the wrong-film guard match against ALL of them — UA sources title their entries
// by the localized name («Проєкт …»), which matches no RU/EN request title by tokens.
func capiRequestTitles(q url.Values) []string {
	out := make([]string, 0, 8)
	add := func(t string) {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	add(q.Get("title"))
	add(q.Get("original_title"))
	for _, param := range []string{"alt_titles", "match_titles"} {
		for _, t := range strings.Split(q.Get(param), capiAltTitleSep) {
			add(t)
		}
	}
	return out
}

// capiPickSimilar filters a "similar" candidate list down to entries whose title matches the
// requested card and returns them best-match-first (closest year wins ties). A /capi resolve
// auto-follows the top candidate, so a fuzzy search that surfaces an unrelated film first must
// not be played — dropping non-matching candidates is what keeps «Мандалорец» from resolving to
// «Майор Гром». With no requested title to match against, the original order is preserved (the
// pre-fix behavior) so minimal clients that send only an id are unaffected.
func capiPickSimilar(data []capiDrillItem, q url.Values) []capiDrillItem {
	reqTitles := capiRequestTitles(q)
	if len(reqTitles) == 0 {
		return data
	}
	reqYear := capiYear4(q.Get("year"))

	type scored struct {
		item    capiDrillItem
		yearGap int
	}
	matches := make([]scored, 0, len(data))
	for _, d := range data {
		cand := firstNonEmpty(d.Title, d.Name)
		if cand == "" || !capiTitleMatchesAny(reqTitles, cand) {
			continue
		}
		gap := 1 << 30
		if reqYear != "" {
			if cy := capiYear4(d.Year); cy != "" {
				gap = capiYearGap(reqYear, cy)
			}
		}
		matches = append(matches, scored{item: d, yearGap: gap})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].yearGap < matches[j].yearGap })
	out := make([]capiDrillItem, len(matches))
	for i, m := range matches {
		out[i] = m.item
	}
	return out
}

func capiParseQuality(raw ejson.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]string
	if err := ejson.Unmarshal(raw, &m); err == nil && len(m) > 0 {
		out := map[string]string{}
		for q, u := range m {
			if capiIsStream(u) {
				out[q] = u
			}
		}
		return out
	}
	return nil
}

// capiSameOrigin turns a balancer stream URL into an opaque, same-origin /proxy link.
//
//   - already proxied (contains "/proxy/", minted by the balancer bound to the drill
//     IP = the end user): just strip the host → relative "/proxy/<token>". The token
//     is served by this same lampac, so it resolves on whatever host the SPA runs on.
//   - a raw CDN url: mint our own /proxy token (never IP-bound — see below).
//
// capiStreamHeaders returns the upstream headers a balancer's CDN gates segment fetches on, baked
// into the /proxy token so the server replays them. filmix's CDN (werkecdn/cdnsqu) rejects a plain
// browser UA the same way its API does ("desktop UAs get empty player_links") → send the Dalvik UA.
func capiStreamHeaders(bal string) map[string]string {
	switch tgauth.CanonicalPluginKey(bal) {
	case "filmix", "filmixtv":
		return map[string]string{"User-Agent": filmixCDNUserAgent}
	}
	return nil
}

// capiTagTranscodingPool stamps the capi/TV pool marker (tcpool=capi) onto a
// same-origin /transcoding URL so the server routes that job to the separate
// concurrency pool (max_concurrent_capi) instead of competing with standard
// Lampa traffic. The marker rides the player's start.m3u8 fetch; a no-op when
// the pool isn't configured server-side.
func capiTagTranscodingPool(u string) string {
	if u == "" || strings.Contains(u, transcodesvc.CapiPoolParam+"=") {
		return u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + transcodesvc.CapiPoolParam + "=" + transcodesvc.CapiPoolValue
}

func capiSameOrigin(raw string, proxyLinks *proxylink.Manager, ip, bal string) string {
	// Lampa multi-CDN format: balancers (filmix, hdvb, kinovod, …) emit "<url1> or <url2>" so the
	// LAMPA client can try the second CDN if the first fails. Our client is a plain <video>/Shaka —
	// it fetched the WHOLE "url1 or url2" string as one URL → 403 (tester: werkecdn.me…%20or%20…).
	// Mirror what Lampa's own proxy + animevost do: keep only the FIRST (primary) CDN. Applied before
	// every branch below so both the raw passthrough AND the minted token carry a single playable URL.
	if i := strings.Index(raw, " or "); i > 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	// Alloha direct-iframe (iframe://host/...): not a stream at all. The client
	// strips the scheme and embeds the page in a webview/iframe; the alloha
	// player resolves the CDN itself. Pass the marker through untouched — never
	// /proxy-wrap it, mint a token, or split on " or ". Must precede every branch
	// below (they'd mangle the non-http url).
	if strings.HasPrefix(raw, "iframe://") {
		return raw
	}
	// Remote transcode box (config remote_host): the minted start URL is ABSOLUTE
	// (https://tc.alcopa.cc/transcoding/start.m3u8?src=…&sig=…). Keep it whole — the client fetches HLS
	// straight from the box. Checked before the /proxy + /transcoding(relative) branches below so the
	// host isn't stripped off.
	if rh := transcodesvc.RemoteHost(); rh != "" && strings.HasPrefix(raw, rh) {
		return capiTagTranscodingPool(raw)
	}
	if i := strings.Index(raw, "/proxy/"); i >= 0 {
		return raw[i:]
	}
	// Server-side transcoder HLS (e.g. PidTor with transcode=on): already same-origin and multi-URL
	// (start.m3u8 → master → segments), so it can't be wrapped in a single /proxy token. Keep it
	// relative as-is — the transcoder serves it from this same lampac origin.
	if i := strings.Index(raw, "/transcoding/"); i >= 0 {
		return capiTagTranscodingPool(raw[i:])
	}
	// Alloha deferred stream: streamHLS serves it from THIS origin and proxies the
	// CDN itself (headless-Chrome resolve on first hit), so it can't be wrapped in a
	// single /proxy token. Keep it relative like /transcoding — same-origin passthrough.
	if i := strings.Index(raw, "/lite/alloha/stream"); i >= 0 {
		return raw[i:]
	}
	// Vibix deferred stream: /lite/vibix/stream.m3u8 runs the coldfilm/rendex
	// browser resolve on first hit and serves the m3u8 from THIS origin (segments
	// already re-proxied), so keep it relative — same-origin passthrough.
	if i := strings.Index(raw, "/lite/vibix/stream"); i >= 0 {
		return raw[i:]
	}
	// no_stream_proxy sources: hand the raw url straight to the client. Only correct when the CDN
	// token is NOT bound to the resolving IP (see config.go note) — most sources here aren't.
	if isStreamProxyDisabled(bal) {
		return raw
	}
	// Mint a same-origin /proxy token. verifyip=false: IP-bound stream tokens 403 when the client's
	// IP changes mid-session (LTE↔WiFi, CGNAT) — Lampa never binds them. Some CDNs gate the SEGMENT
	// fetch on the same headers used to resolve (filmix's Dalvik UA: a browser UA gets 403), so we
	// bake those into the token → the proxy replays them server-side.
	if h := capiStreamHeaders(bal); len(h) > 0 {
		if tok := proxyLinks.EncryptURIWithHeaders(raw, ip, bal, h); tok != "" {
			return "/proxy/" + tok
		}
	}
	if tok := proxyLinks.EncryptURI(raw, ip, bal, false, false, false); tok != "" {
		return "/proxy/" + tok
	}
	return ""
}

func capiIsStream(u string) bool {
	if !strings.HasPrefix(u, "http") {
		return false
	}
	low := strings.ToLower(u)
	return strings.Contains(low, ".m3u8") || strings.Contains(low, ".mp4") || strings.Contains(low, ".mpd") || strings.Contains(low, "/proxy/")
}

func capiNormVoice(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(name)), " ")
}

var (
	// release/quality tokens to strip when no voice type is detected
	capiVoiceJunkRe  = regexp.MustCompile(`(?i)[\[\(][^\]\)]*[\]\)]|\b\d{3,4}p\b|\b(4k|2k|fhd|uhd|hdr10?\+?|hdr|hevc|bluray|web-?dl|bd-?rip|hd-?rip|x?26[45]|dolby|atmos|ac3|aac|dts|sdr|dv)\b|\|.*$`)
	capiVoiceResRe   = regexp.MustCompile(`(?i)\b(\d{3,4}p|4k|2k|fhd|uhd|hdr)\b`)
	capiVoiceYearRe  = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	capiBracketRe    = regexp.MustCompile(`[\[\(]([^\]\)]+)[\]\)]`)
	capiVoiceCodecRe = regexp.MustCompile(`(?i)^(4k|2k|fhd|uhd|sdr|hdr10?\+?|hdr|dv|hevc|h\.?26[45]|x?26[45]|av1|ac-?3|e-?ac-?3|aac|dts(-hd)?|atmos|mp3|web-?dl|bd-?rip|hd-?rip|bluray|dolby|\+?ua|\+?ru|\+?eng?|\d{3,4}p|4к|2к)$`)
)

// capiVoiceTag pulls a distinguisher (language/studio) out of (...) or [...] — e.g.
// "Дубляж [Rus, 4K, SDR]" → "Rus", "(Red Head Sound)" → "Red Head Sound" — skipping
// quality/codec/year tokens so genuinely different translations don't merge.
func capiVoiceTag(s string) string {
	for _, m := range capiBracketRe.FindAllStringSubmatch(s, -1) {
		for _, part := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ';' || r == '/' }) {
			part = strings.TrimSpace(part)
			if part == "" || capiVoiceYearRe.MatchString(part) || capiVoiceCodecRe.MatchString(part) {
				continue
			}
			pl := strings.ToLower(part)
			if strings.Contains(pl, "сезон") || strings.Contains(pl, "season") {
				continue
			}
			if len([]rune(part)) <= 28 {
				return part
			}
		}
	}
	return ""
}

// capiCleanVoice normalizes a balancer's translation label into a clean, anonymized
// озвучка name ("Дубляж · Rus", "Многоголосый · LostFilm", "Грузинский"). Clear series
// junk ("1 серия", "…сезон") is dropped; a quality-only/title/release name that isn't an
// озвучка ("1080p", "Дюна: Часть вторая") falls back to a generic "Основная" bucket so
// the source still contributes a stream rather than being lost.
func capiCleanVoice(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	low := strings.ToLower(s)

	typ := ""
	switch {
	case strings.Contains(low, "дубл") || strings.Contains(low, "dub"):
		typ = "Дубляж"
	case strings.Contains(low, "многоголос") || strings.Contains(low, "mvo"):
		typ = "Многоголосый"
	case strings.Contains(low, "двухголос") || strings.Contains(low, "dvo"):
		typ = "Двухголосый"
	case strings.Contains(low, "одноголос"):
		typ = "Одноголосый"
	case strings.Contains(low, "авторск"):
		typ = "Авторский"
	case strings.Contains(low, "закадр"):
		typ = "Закадровый"
	case strings.Contains(low, "ориг") || strings.Contains(low, "субтит") || strings.Contains(low, "original") || strings.Contains(low, "sub"):
		typ = "Оригинал"
	}

	tag := capiVoiceTag(s)

	if typ != "" {
		if tag != "" && !strings.EqualFold(tag, typ) {
			return typ + " · " + tag
		}
		return typ
	}
	// clear series / list junk → not a movie voice
	for _, w := range []string{"сезон", "season", "серия", "серии", "эпизод", "episode"} {
		if strings.Contains(low, w) {
			return ""
		}
	}
	if strings.Count(s, "|") >= 2 {
		return ""
	}
	if tag != "" {
		return tag
	}
	stripped := strings.TrimSpace(capiVoiceJunkRe.ReplaceAllString(s, ""))
	if stripped != "" && len([]rune(stripped)) <= 28 && !strings.Contains(s, ":") &&
		!(capiVoiceYearRe.MatchString(s) && capiVoiceResRe.MatchString(s)) && !strings.ContainsAny(stripped, `|/\`) {
		return stripped
	}
	return "Основная" // quality-only / title-like / release name → generic default audio
}

// capiQualityHeight ranks a quality label by its vertical resolution. Letter grades map
// to their real heights — the digit-parse alone ranked "4K" as 4, sorting it BELOW 270p
// (observed live: [540p 270p 4K SD HD]).
func capiQualityHeight(q string) int {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "8k":
		return 4320
	case "4k", "4к", "uhd", "ultra", "ultrahd":
		return 2160
	case "2k", "2к", "qhd":
		return 1440
	case "fhd", "fullhd":
		return 1080
	case "hd", "hdready":
		return 720
	case "sd":
		return 480
	case "ld":
		return 360
	}
	n := 0
	for _, c := range q {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else if n > 0 {
			break
		}
	}
	return n
}

func capiSortQualities(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for q := range set {
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return capiQualityHeight(out[i]) > capiQualityHeight(out[j]) })
	return out
}

// capiToken extracts the caller's token (cookie/header/query) to forward to the drill.
func capiToken(r *http.Request) string {
	for _, name := range []string{"alpac_token", "lampac_token"} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value
		}
	}
	for _, h := range []string{"X-Alpac-Token", "X-Lampac-Token"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

