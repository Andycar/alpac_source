package litesrc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"lampac-go/internal/httpclient"
	"math"
	mathrand "math/rand"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

var (
	filmixIDRe             = regexp.MustCompile(`/([0-9]+)-`)
	filmixCDNRe            = regexp.MustCompile(`^(https?://[^/]+)`)
	filmixHostPrefixRe     = regexp.MustCompile(`^https?://[^/]+`)
	filmixMovieQualitiesRe = regexp.MustCompile(`_\[[0-9,]+\]\.mp4`)
	filmixHLSRe            = regexp.MustCompile(`^(https?://[^/]+)/s/([^/]+)/(.*)`)
	filmixHdrHEVCRe        = regexp.MustCompile(`/(HDR10p?|HEVC)/`)
	// filmixDeadHLSHostRe matches filmix's live CDNs (cdnsqu/werkecdn). They dropped the
	// /hls/<file>.mp4/index.m3u8?hash= rewrite — it now returns 403/404 (verified 2026-06-26
	// against a real pro token), while the direct /s/<token>/...mp4 the API returns streams 206.
	filmixDeadHLSHostRe = regexp.MustCompile(`(?i)//[^/]*(?:cdnsqu|werkecdn)`)
	filmixEpisodeNumRe  = regexp.MustCompile(`([0-9]+)`)
)

// filmixDefaultHosts are the known Filmix API mirrors. The API is served
// identically from several domains (verified 2026-07-20: cyou + vip both
// return live JSON); mirrors go dark individually (DNS/blocking), so the
// checker fails over on TRANSPORT errors — a live mirror's HTTP status
// (4xx/5xx) is a real answer and never rotates. Sticky index so we don't
// re-probe a dead host every request.
var filmixDefaultHosts = []string{
	"http://filmixapp.cyou",
	"http://filmixapp.vip",
}

// filmixHostIdx is the process-wide sticky mirror index (mod len(hosts)).
var filmixHostIdx atomic.Int32

// errFilmixTransport marks a dial/TLS/timeout failure — the only class that
// rotates the mirror.
var errFilmixTransport = errors.New("filmix: transport error")

// filmixAPIHosts returns the failover-ordered list: configured host first
// (when set), then the default mirrors, deduped.
func filmixAPIHosts(configured string) []string {
	configured = strings.TrimRight(strings.TrimSpace(configured), "/")
	hosts := make([]string, 0, len(filmixDefaultHosts)+1)
	if configured != "" {
		hosts = append(hosts, configured)
	}
	for _, h := range filmixDefaultHosts {
		if h != configured {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// filmixAdvanceHost rotates to the next mirror after a transport failure of
// `failed`. CAS keeps concurrent failures from skipping mirrors.
func filmixAdvanceHost(hosts []string, failed string) {
	if len(hosts) < 2 {
		return
	}
	idx := filmixHostIdx.Load()
	if hosts[int(idx)%len(hosts)] != failed {
		return
	}
	if filmixHostIdx.CompareAndSwap(idx, idx+1) {
		log.Warn().Str("failed", failed).Str("next", hosts[int(idx+1)%len(hosts)]).Msg("filmix: API mirror failover")
	}
}

type filmixChecker struct {
	client *http.Client
	hosts  []string // failover-ordered API mirrors (configured first)
	token  string
	tokens []string
	// reserveTokens — другая учётка, только для перевыпуска ссылки на 429 (filmix_remint.go).
	reserveTokens []string
	pro           bool
	// fxPrimary keeps the old api-fx-first order (config fx_mode = 'primary');
	// default is legacy first with api-fx as the fallback.
	fxPrimary bool
	reserve   bool
	hls       bool
	rhub      bool
	userIDs   sync.Map
	tokenMux  sync.Mutex

	// api.filmix.tv (api-fx) state — the path that still serves >720p, see filmix_fx.go.
	fxHost        string
	fxUser        string
	fxPasswd      string
	fxDirectLampa bool  // отдавать Lampa рецепт для клиентского минта (архитектура B)
	fxProgressive *bool // прогрессивный MP4 вместо HLS (nil = авто при наличии кредов)
	fxMu          sync.Mutex
	fxHash        string    // /api-fx/request-token session hash
	fxHashAt      time.Time // когда hash выпущен (для диагностики)
	fxHashDead    bool      // сессия не прошла проверку живости — разрешён перевыпуск
	fxAliveFails  int       // подряд идущие явные отказы /api-fx/me
	// fxUpstreamBusy — подряд идущие 429/5xx от апстрима. Нужен только для лога:
	// одиночный отказ ничего не значит, а серия означает, что филмикс нас режет.
	fxUpstreamBusy  int
	fxLoginAt       time.Time // когда последний раз тратили слот устройства
	fxAliveAt       time.Time // когда живость проверялась
	fxHashFetchedAt time.Time
	fxStatePath     string // database/filmix_fx.json — see fxStatePath()
	fxAccess        string // account accessToken (Bearer), TTL-bounded
	fxAccessHash    string // hash, для которого выдан fxAccess (чужому hash прав не даёт)
	fxAccessAt      time.Time
	fxRefresh       string
	fxAuthFailAt    time.Time // last full-login failure (device limit etc.) — cooldown gate
	fxStateLoaded   bool      // state file read attempted
	fxLinks         sync.Map  // cacheKey → fxLinksEntry (video-links, TTL-bounded)
	fxInflight      sync.Map  // cacheKey → chan struct{} (singleflight)
}

type filmixSearchModel struct {
	ID            filmixInt `json:"id"`
	Title         string    `json:"title"`
	OriginalTitle string    `json:"original_title"`
	OriginalName  string    `json:"original_name"`
	Poster        string    `json:"poster"`
	Year          filmixInt `json:"year"`
}

type filmixSearchEnvelope struct {
	Items []filmixSearchModel `json:"items"`
	Data  []filmixSearchModel `json:"data"`
}

type filmixSearchResult struct {
	ID       int
	Similars []filmixSimilarItem
}

type filmixSimilarItem struct {
	ID    int
	Title string
	Year  string
	Img   string
}

type filmixPostRoot struct {
	PlayerLinks *filmixPlayerLinks `json:"player_links"`
	Quality     string             `json:"quality"`
}

type filmixPlayerLinks struct {
	Movie    []filmixMovie                            `json:"movie"`
	Playlist map[string]map[string]stdjson.RawMessage `json:"playlist"`
}

type filmixMovie struct {
	Link        string         `json:"link"`
	Translation string         `json:"translation"`
	Qualities   filmixIntSlice `json:"qualities"`
}

type filmixEpisodeItem struct {
	Link      string         `json:"link"`
	Qualities filmixIntSlice `json:"qualities"`
}

type filmixInt int

func (n *filmixInt) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*n = 0
		return nil
	}
	if strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`) {
		raw = strings.Trim(raw, `"`)
		if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			*n = filmixInt(v)
			return nil
		}
		*n = 0
		return nil
	}
	var num stdjson.Number
	if err := stdjson.Unmarshal(data, &num); err == nil {
		if i64, err := num.Int64(); err == nil {
			*n = filmixInt(i64)
			return nil
		}
		if f64, err := num.Float64(); err == nil {
			*n = filmixInt(int(math.Round(f64)))
			return nil
		}
	}
	*n = 0
	return nil
}

func (n filmixInt) Int() int {
	return int(n)
}

type filmixIntSlice []int

func (s *filmixIntSlice) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*s = nil
		return nil
	}

	var anyItems []any
	if err := stdjson.Unmarshal(data, &anyItems); err != nil {
		*s = nil
		return nil
	}

	out := make([]int, 0, len(anyItems))
	for _, item := range anyItems {
		switch t := item.(type) {
		case float64:
			out = append(out, int(t))
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
				out = append(out, n)
			}
		}
	}
	*s = out
	return nil
}

func NewFilmixChecker(cfg config.Config) *filmixChecker {
	hosts := filmixAPIHosts(cfg.Online.Filmix.Host)

	token := strings.TrimSpace(cfg.Online.Filmix.Token)
	seen := map[string]struct{}{}
	tokens := make([]string, 0, 1+len(cfg.Online.Filmix.Tokens))

	if token != "" {
		tokens = append(tokens, token)
		seen[token] = struct{}{}
	}
	for _, item := range cfg.Online.Filmix.Tokens {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		tokens = append(tokens, item)
		seen[item] = struct{}{}
	}
	// Запасные — только для перевыпуска на 429 (filmix_remint.go); совпавшие с обычными не берём.
	var reserveTokens []string
	for _, item := range cfg.Online.Filmix.ReserveTokens {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		reserveTokens = append(reserveTokens, item)
		seen[item] = struct{}{}
	}

	fxHost := strings.TrimRight(strings.TrimSpace(cfg.Online.FilmixTV.Host), "/")
	if fxHost == "" {
		fxHost = "https://api.filmix.tv"
	}

	f := &filmixChecker{
		// Dynamic: a SOCKS5/vless mapped to "filmix" in the admin Proxy panel
		// applies live. Needed because api.filmix.tv geo-blocks non-RU/datacenter
		// IPs (video-links answers "Видео заблокировано!" even for free titles) —
		// hosting abroad requires routing this balancer through a RU exit.
		client:        httpclient.NewForBalancerDynamic("filmix", 10*time.Second),
		hosts:         hosts,
		token:         token,
		tokens:        tokens,
		reserveTokens: reserveTokens,
		pro:           cfg.Online.Filmix.Pro,
		fxPrimary:     strings.EqualFold(strings.TrimSpace(cfg.Online.Filmix.FXMode), "primary"),
		reserve:       cfg.Online.Filmix.Reserve,
		hls:           cfg.Online.Filmix.HLS,
		rhub:          cfg.Online.Filmix.Rhub,
		fxHost:        fxHost,
		fxUser:        strings.TrimSpace(cfg.Online.Filmix.UserAPITV),
		fxPasswd:      strings.TrimSpace(cfg.Online.Filmix.PasswdAPITV),
		fxDirectLampa: cfg.Online.Filmix.DirectLampa,
		fxProgressive: cfg.Online.Filmix.Progressive,
		fxStatePath:   fxStatePath(cfg.Compat.RepoRoot),
	}
	// Проверка токенов — с самого старта, а не с первого запроса: на primary /lite/filmix
	// уезжает на ноды раньше обработчика, и его токены иначе остались бы непроверенными.
	f.startTokenProbe()
	// 429 CDN → та же ссылка, подписанная другой учёткой (прокси зовёт на горячем пути).
	proxylink.RegisterTargetRefresher("filmix", f.remint)
	return f
}

// apiHost returns the sticky-selected Filmix API mirror.
func (f *filmixChecker) apiHost() string {
	if len(f.hosts) == 0 {
		return filmixDefaultHosts[0]
	}
	return f.hosts[int(filmixHostIdx.Load())%len(f.hosts)]
}

func (f *filmixChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		f.startTokenProbe()
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := f.checkSearch(req.Context(), req.URL.Query())
			badge := "SD"
			if f.token != "" || len(f.tokens) > 0 {
				badge = "HD"
			}
			if f.legacyRights() || (f.fxUser != "" && f.fxPasswd != "") {
				badge = "4K"
			}
			writeCheckSearchResponse(w, show, badge)
			return
		}

		// rc/filmix remains legacy-bound in current compatibility contract.
		if strings.Contains(strings.ToLower(req.URL.Path), "/lite/rc/filmix") {
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"error":    "rc/filmix requires legacy upstream",
				"balanser": "rc/filmix",
			})
			return
		}

		f.index(w, req, links)
	}
}

func (f *filmixChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	clarification, _ := getsTVQueryInt(q.Get("clarification"))
	year, _ := getsTVQueryInt(q.Get("year"))
	similar := parseBoolParam(q.Get("similar"))
	postID, _ := getsTVQueryInt(q.Get("postid"))
	t, tSet := getsTVQueryInt(q.Get("t"))
	if !tSet {
		t = 0
	}
	s, sSet := getsTVQueryInt(q.Get("s"))

	if postID == 0 {
		source := strings.ToLower(strings.TrimSpace(q.Get("source")))
		id := strings.TrimSpace(q.Get("id"))
		if id != "" && (source == "filmix" || source == "filmixapp") {
			if n, err := strconv.Atoi(id); err == nil {
				postID = n
			} else {
				postID, _ = getsTVQueryInt(submatch1(filmixIDRe, id))
			}
		}
	}

	token := f.pickToken()

	// Kit: per-user token override
	if kitToken, ok := kit.TokenOverride(req.Context(), "Filmix"); ok {
		token = kitToken
	}

	// With rhub on, the API calls below run on the user's device (RU IP). Gate
	// the handshake, then thread the request through ctx so the ctx-based fetch
	// helpers (searchV2/searchFallback/post) can reach the hub.
	if rchGate(w, req, f.rhub) {
		return
	}
	ctx := rchWithRequest(req)

	if postID == 0 {
		search, ok := f.search(ctx, title, originalTitle, clarification, year, similar, token)
		if !ok {
			writeGetsTVEmpty(w, rjson)
			return
		}
		if search.ID == 0 {
			f.writeSimilar(w, req, rjson, title, originalTitle, search.Similars)
			return
		}
		postID = search.ID
	}

	// Прямой CDN (архитектура B): клиент, умеющий сам минтить hash, просит fxdirect=1 и получает
	// РЕЦЕПТ вместо проксированных потоков — дальше он качает CDN напрямую, минуя сервер (см.
	// filmix_direct.go).
	//
	// ★Ветка обязана смотреть на флаг, а не только на параметр. Сниппет живёт СТАТИКОЙ в
	// plugins/online.js, поэтому Лампа продолжает просить `fxdirect=1` и после выключения
	// `direct_lampa` — раньше сервер честно отдавал рецепт, браузеры минтили сами и упирались в
	// 429 от werkecdn, тогда как приложение (параметр не шлёт) спокойно играло через /proxy.
	// Теперь выключатель работает без переката клиентов, как и обещано в config.go.
	if parseBoolParam(q.Get("fxdirect")) && f.fxDirectLampa {
		f.writeFilmixDirect(w, req, postID)
		return
	}

	if f.fxPrimary {
		// fx_mode = 'primary' — the pre-2026-09 order. api-fx first: since 2026-07
		// the legacy /s/ hashes are capped at 720p server-side (higher qualities
		// stream a "купите премиум" stub mp4), while video-links returns per-quality
		// HLS including 4K. Geo guard: api.filmix.tv serves non-RU/datacenter IPs
		// a degraded answer (480p-only or "Видео заблокировано!"), so fx wins only
		// when it actually offers ≥720p; legacy is the fallback.
		movies, serial, fxOK := f.fxVideoLinks(ctx, postID)
		if fxOK && fxMaxQuality(movies, serial) >= 720 {
			f.writeFX(w, req, rjson, movies, serial, postID, title, originalTitle, t, sSet, s, links)
			return
		}
		post, ok := f.post(ctx, postID, token)
		if !ok {
			if fxOK {
				f.writeFX(w, req, rjson, movies, serial, postID, title, originalTitle, t, sSet, s, links)
				return
			}
			writeGetsTVEmpty(w, rjson)
			return
		}
		f.writeTpl(w, req, rjson, post, postID, title, originalTitle, t, sSet, s, token, links)
		return
	}

	// Default (fx_mode = 'fallback'): the legacy filmixapp API is the stable
	// backend — its /s/ links play reliably (≤720p with a token). api.filmix.tv
	// is consulted only when legacy cannot serve the title: the API refused
	// (403 / outage), the title is RKN-blocked there, or every row is a
	// "купите премиум" stub. Mirrors lampac-nextgen, where Filmix (legacy +
	// pro token) and the api-fx contour are separate and legacy is the default.
	post, ok := f.post(ctx, postID, token)
	if ok && f.legacyPlayable(post) {
		f.writeTpl(w, req, rjson, post, postID, title, originalTitle, t, sSet, s, token, links)
		return
	}
	movies, serial, fxOK := f.fxVideoLinks(ctx, postID)
	if fxOK {
		log.Info().Int("post_id", postID).Bool("legacy_ok", ok).
			Msg("filmix: legacy has nothing playable, falling back to api-fx")
		f.writeFX(w, req, rjson, movies, serial, postID, title, originalTitle, t, sSet, s, links)
		return
	}
	if ok {
		// Neither backend has streams — let the legacy writer produce its own
		// (blocked / empty) answer so the client sees the same shape as before.
		f.writeTpl(w, req, rjson, post, postID, title, originalTitle, t, sSet, s, token, links)
		return
	}
	writeGetsTVEmpty(w, rjson)
}

// legacyPlayable reports whether the legacy post carries at least one row the
// legacy writer would actually emit: a movie translation that is not blocked
// and not a premium stub, or a serial with a playlist.
func (f *filmixChecker) legacyPlayable(root filmixPostRoot) bool {
	if root.PlayerLinks == nil {
		return false
	}
	if len(root.PlayerLinks.Movie) > 0 {
		if len(root.PlayerLinks.Movie) == 1 &&
			strings.HasPrefix(strings.ToLower(strings.TrimSpace(root.PlayerLinks.Movie[0].Translation)), "заблокировано ") {
			return false
		}
		for _, m := range root.PlayerLinks.Movie {
			if !f.filmixHideUnplayable(m.Translation, m.Link) {
				return true
			}
		}
		return false
	}
	return len(root.PlayerLinks.Playlist) > 0
}

func (f *filmixChecker) checkSearch(ctx context.Context, q url.Values) bool {
	stories := buildFilmixStories(q)
	if len(stories) == 0 {
		return false
	}
	if f.rhub {
		// The search runs on the user's device at play time; no device is bound
		// to this server-side probe, so advertise availability and let the
		// play-path handshake do the real fetch.
		return true
	}

	checkTokens := f.tokens
	if len(checkTokens) == 0 {
		checkTokens = []string{f.token}
	}
	if len(checkTokens) == 0 {
		checkTokens = []string{""}
	}

	for _, token := range checkTokens {
		for _, story := range stories {
			if strings.TrimSpace(story) == "" {
				continue
			}
			if items, ok := f.searchV2(ctx, story, token); ok && len(items) > 0 {
				return true
			}
			if items, ok := f.searchFallback(ctx, story); ok && len(items) > 0 {
				return true
			}
		}
	}
	return false
}

func buildFilmixStories(q url.Values) []string {
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	clarification := 0
	if raw := strings.TrimSpace(q.Get("clarification")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			clarification = n
		}
	}

	primary := title
	if clarification != 1 {
		primary = originalTitle
		if primary == "" {
			primary = title
		}
	}

	out := make([]string, 0, 2)
	seen := map[string]struct{}{}
	for _, item := range []string{primary, title, originalTitle} {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		out = append(out, item)
		seen[item] = struct{}{}
	}
	return out
}

func (f *filmixChecker) search(
	ctx context.Context,
	title string,
	originalTitle string,
	clarification int,
	year int,
	similar bool,
	token string,
) (filmixSearchResult, bool) {
	if strings.TrimSpace(title) == "" && strings.TrimSpace(originalTitle) == "" {
		return filmixSearchResult{}, false
	}

	primary := strings.TrimSpace(title)
	if clarification != 1 {
		primary = strings.TrimSpace(originalTitle)
		if primary == "" {
			primary = strings.TrimSpace(title)
		}
	}

	if primary != "" {
		if rows, ok := f.searchV2(ctx, primary, token); ok && len(rows) > 0 {
			return f.buildSearchResult(rows, title, originalTitle, year, similar, false), true
		}
	}

	alt1 := strings.TrimSpace(title)
	alt2 := strings.TrimSpace(originalTitle)
	if clarification == 1 {
		alt1 = strings.TrimSpace(originalTitle)
		alt2 = strings.TrimSpace(title)
	}

	if alt1 != "" {
		if rows, ok := f.searchFallback(ctx, alt1); ok && len(rows) > 0 {
			return f.buildSearchResult(rows, title, originalTitle, year, similar, true), true
		}
	}
	if alt2 != "" && alt2 != alt1 {
		if rows, ok := f.searchFallback(ctx, alt2); ok && len(rows) > 0 {
			return f.buildSearchResult(rows, title, originalTitle, year, similar, true), true
		}
	}

	return filmixSearchResult{}, false
}

func (f *filmixChecker) buildSearchResult(
	rows []filmixSearchModel,
	title string,
	originalTitle string,
	year int,
	similar bool,
	forcePickSingle bool,
) filmixSearchResult {
	out := filmixSearchResult{
		Similars: make([]filmixSimilarItem, 0, len(rows)),
	}

	wantTitle := filmixNormalizeName(title)
	wantOriginal := filmixNormalizeName(originalTitle)
	if wantTitle == "" {
		wantTitle = wantOriginal
	}
	if wantOriginal == "" {
		wantOriginal = wantTitle
	}

	matches := make([]int, 0, len(rows))
	for _, item := range rows {
		id := item.ID.Int()
		if id <= 0 {
			continue
		}

		nameRU := strings.TrimSpace(item.Title)
		nameEN := strings.TrimSpace(item.OriginalTitle)
		if nameEN == "" {
			nameEN = strings.TrimSpace(item.OriginalName)
		}

		label := strings.TrimSpace(strings.Trim(nameRU+" / "+nameEN, " /"))
		if label == "" {
			label = fmt.Sprintf("Filmix #%d", id)
		}

		itemYear := item.Year.Int()
		out.Similars = append(out.Similars, filmixSimilarItem{
			ID:    id,
			Title: label,
			Year:  strconv.Itoa(itemYear),
			Img:   strings.TrimSpace(item.Poster),
		})

		nameRUnorm := filmixNormalizeName(nameRU)
		nameENnorm := filmixNormalizeName(nameEN)
		if nameRUnorm == "" && nameENnorm == "" {
			continue
		}
		if (wantTitle != "" && (nameRUnorm == wantTitle || nameENnorm == wantTitle)) ||
			(wantOriginal != "" && (nameRUnorm == wantOriginal || nameENnorm == wantOriginal)) {
			if year > 0 && itemYear != year {
				continue
			}
			matches = append(matches, id)
		}
	}

	if len(matches) == 1 && (forcePickSingle || !similar) {
		out.ID = matches[0]
	}
	return out
}

// filmixCDNUserAgent is the Android/Dalvik UA filmix expects. A desktop/browser UA gets empty
// player_links from the API — and the CDN (werkecdn/cdnsqu) likewise 403s a browser UA, so the
// /capi proxy must replay this when fetching segments (see capiStreamHeaders).
const filmixCDNUserAgent = "Dalvik/2.1.0 (Linux; U; Android 11; Xiaomi Build/RP1A.200720.011)"

// filmixStreamHeaders are baked into every filmix /proxy token so the proxy
// replays the Dalvik UA on segment fetches for ANY client. Until 2026-09 only
// the /capi path did this (capiStreamHeaders); Lampa clients got header-less
// tokens and the CDN answered 403 — 100+ «upstream blocked playback» a day,
// all werkecdn/cdnsqu with token_headers=0.
func filmixStreamHeaders() map[string]string {
	return map[string]string{"User-Agent": filmixCDNUserAgent}
}

// filmixHeaderMap is the single source of truth for filmix request headers,
// shared by the direct path and the rch path. The Dalvik UA is required —
// desktop UAs get empty player_links from filmix.
func filmixHeaderMap() map[string]string {
	return map[string]string{
		"User-Agent":  filmixCDNUserAgent,
		"X-Lampac-Go": "1",
	}
}

// apiGet fetches path?qs from the active Filmix mirror, failing over to the
// next mirror on a TRANSPORT error (dead host). A live mirror's non-2xx
// status is a real answer — it stops the loop (retrying another host with a
// bad token/postid would just fail the same way). rhub routing and the 2x
// backoff-retry are preserved per attempt.
func (f *filmixChecker) apiGet(ctx context.Context, path string, qs url.Values, maxBody int64) (string, bool) {
	attempts := len(f.hosts)
	if attempts < 1 {
		attempts = 1
	}
	var body string
	for i := 0; i < attempts; i++ {
		host := f.apiHost()
		u, err := url.Parse(host + path)
		if err != nil {
			return "", false
		}
		u.RawQuery = qs.Encode()
		target := u.String()

		var attemptErr error
		body, attemptErr = rchFetchCtx(ctx, f.rhub, target, filmixHeaderMap(), func() (string, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			if err != nil {
				return "", err
			}
			for hk, hv := range filmixHeaderMap() {
				req.Header.Set(hk, hv)
			}
			resp, err := balancerDoWithRetry(ctx, f.client, req, 2)
			if err != nil {
				filmixAdvanceHost(f.hosts, host)
				return "", fmt.Errorf("%w: %v", errFilmixTransport, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				// 429 отделяем от прочих кодов: «нас режут» и «источник сломался» —
				// разные беды, и лечатся по-разному. Без этого в логе была бы одна
				// безликая строка про статус.
				if resp.StatusCode == http.StatusTooManyRequests {
					log.Warn().Str("url", u.String()).Msg("filmix: апстрим ограничивает запросы (429)")
				}
				return "", fmt.Errorf("filmix: status %d", resp.StatusCode)
			}
			b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
			if err != nil {
				return "", err
			}
			return string(b), nil
		})
		if attemptErr == nil && body != "" {
			return body, true
		}
		if !errors.Is(attemptErr, errFilmixTransport) {
			return "", false // live mirror answered — another host won't help
		}
	}
	return "", false
}

func (f *filmixChecker) searchV2(ctx context.Context, story, token string) ([]filmixSearchModel, bool) {
	qs := f.makeAuthQuery(token)
	qs.Set("story", story)
	body, ok := f.apiGet(ctx, "/api/v2/search", qs, 2<<20)
	if !ok {
		return nil, false
	}
	return decodeFilmixSearchItems([]byte(body))
}

func (f *filmixChecker) searchFallback(ctx context.Context, story string) ([]filmixSearchModel, bool) {
	u, err := url.Parse("https://api.filmix.tv/api-fx/list")
	if err != nil {
		return nil, false
	}
	qs := url.Values{}
	qs.Set("search", story)
	qs.Set("limit", "48")
	u.RawQuery = qs.Encode()
	target := u.String()

	body, err := rchFetchCtx(ctx, f.rhub, target, filmixHeaderMap(), func() (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return "", err
		}
		for hk, hv := range filmixHeaderMap() {
			req.Header.Set(hk, hv)
		}
		resp, err := balancerDoWithRetry(ctx, f.client, req, 2)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if resp.StatusCode == http.StatusTooManyRequests {
				log.Warn().Msg("filmix: апстрим ограничивает поиск (429)")
			}
			return "", fmt.Errorf("filmix: searchFallback status %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil {
			return "", err
		}
		return string(b), nil
	})
	if err != nil || body == "" {
		return nil, false
	}
	items, ok := decodeFilmixSearchItems([]byte(body))
	return items, ok
}

func decodeFilmixSearchItems(body []byte) ([]filmixSearchModel, bool) {
	body = bytesTrimSpace(body)
	if len(body) == 0 {
		return nil, false
	}

	var arr []filmixSearchModel
	if err := stdjson.Unmarshal(body, &arr); err == nil {
		return arr, true
	}

	var env filmixSearchEnvelope
	if err := stdjson.Unmarshal(body, &env); err == nil {
		if len(env.Items) > 0 {
			return env.Items, true
		}
		if len(env.Data) > 0 {
			return env.Data, true
		}
		return []filmixSearchModel{}, true
	}

	var obj map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal(body, &obj); err != nil {
		return nil, false
	}
	for _, key := range []string{"items", "data", "results"} {
		raw, ok := obj[key]
		if !ok || len(raw) == 0 {
			continue
		}
		var nested []filmixSearchModel
		if err := stdjson.Unmarshal(raw, &nested); err == nil {
			return nested, true
		}
	}
	return []filmixSearchModel{}, true
}

func hasFilmixItems(body []byte) bool {
	items, ok := decodeFilmixSearchItems(body)
	if ok {
		return len(items) > 0
	}
	show, _, _ := evaluateSearchResult(string(body))
	return show
}

func (f *filmixChecker) post(ctx context.Context, postID int, token string) (filmixPostRoot, bool) {
	body, ok := f.apiGet(ctx, fmt.Sprintf("/api/v2/post/%d", postID), f.makeAuthQuery(token), 8<<20)
	if !ok || len(body) == 0 {
		return filmixPostRoot{}, false
	}

	jsonFix := strings.ReplaceAll(body, `"playlist":[],`, `"playlist":null,`)
	jsonFix = strings.ReplaceAll(jsonFix, `"playlist":[]}`, `"playlist":null}`)

	var root filmixPostRoot
	if err := stdjson.Unmarshal([]byte(jsonFix), &root); err != nil {
		return filmixPostRoot{}, false
	}
	if root.PlayerLinks == nil {
		return filmixPostRoot{}, false
	}
	if len(root.PlayerLinks.Movie) == 0 && len(root.PlayerLinks.Playlist) == 0 {
		return filmixPostRoot{}, false
	}
	return root, true
}

func (f *filmixChecker) writeTpl(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	root filmixPostRoot,
	postID int,
	title string,
	originalTitle string,
	t int,
	sSet bool,
	s int,
	token string,
	links *proxylink.Manager,
) {
	if root.PlayerLinks == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// NOTE: the response "quality" field is not usable for pro detection —
	// since 2026-07 it reports the CONTENT's max quality ("2160" even anonymous),
	// not the account tier. The tier comes from config (`pro`) and is applied by
	// allowQuality; see its doc for the 2026-09 re-verification of legacy 4K.

	if len(root.PlayerLinks.Movie) > 0 {
		f.writeMovie(w, req, rjson, root.PlayerLinks.Movie, postID, title, originalTitle, token, links)
		return
	}

	f.writeSerial(w, req, rjson, root, postID, title, originalTitle, t, sSet, s, token, links)
}

func (f *filmixChecker) writeMovie(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	movies []filmixMovie,
	postID int,
	title string,
	originalTitle string,
	token string,
	links *proxylink.Manager,
) {
	if len(movies) == 1 {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(movies[0].Translation)), "заблокировано ") {
			writeGetsTVEmpty(w, rjson)
			return
		}
	}

	cdns := f.collectMovieCDNs(movies)
	rows := make([]map[string]any, 0, len(movies))
	labels := make([]string, 0, len(movies))
	baseTitle := getsTVJoinName(title, originalTitle)

	for _, movie := range movies {
		// Без подтверждённых прав HEVC/HDR-рипы отдают заглушку «купите премиум» — не показываем.
		if f.filmixHideUnplayable(movie.Translation, movie.Link) {
			continue
		}
		streams := f.buildMovieStreams(movie, token, cdns)
		if len(streams) == 0 {
			continue
		}
		filmixRememberMint(streams, postID, token)

		for i := range streams {
			streams[i]["url"] = streamProxyURLWithHeaders(req, streams[i]["url"], "filmix", links, filmixStreamHeaders())
		}

		name := strings.TrimSpace(movie.Translation)
		if name == "" {
			name = "По умолчанию"
		}

		row := map[string]any{
			"method":        "play",
			"url":           streams[0]["url"],
			"stream":        streams[0]["url"],
			"name":          name,
			"title":         baseTitle,
			"streamquality": streams,
		}
		// ★Даже когда сервер отдал легаси ≤720 (его api-fx гео-заблокирован из ДЦ), RU-клиент
		// сам НЕ заблокирован → браузерный минт добудет прямой 4K. Поэтому fxdirect тут особенно ценен.
		if f.fxDirectLampa {
			row["fxdirect"] = f.filmixRowFxDirect(postID, name, 0, 0)
		}
		if len(streams) > 1 {
			qualMap := filmixBuildQualityMap(streams)
			row["quality"] = qualMap
			row["qualitys"] = qualMap
		}

		rows = append(rows, row)
		labels = append(labels, name)
	}

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

func (f *filmixChecker) writeSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	root filmixPostRoot,
	postID int,
	title string,
	originalTitle string,
	t int,
	sSet bool,
	s int,
	token string,
	links *proxylink.Manager,
) {
	playlist := root.PlayerLinks.Playlist
	if len(playlist) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	if !sSet {
		seasonKeys := make([]string, 0, len(playlist))
		for key := range playlist {
			seasonKeys = append(seasonKeys, key)
		}
		sort.Slice(seasonKeys, func(i, j int) bool {
			ai, aok := getsTVQueryInt(seasonKeys[i])
			bi, bok := getsTVQueryInt(seasonKeys[j])
			if aok && bok {
				return ai < bi
			}
			return seasonKeys[i] < seasonKeys[j]
		})

		data := make([]map[string]any, 0, len(seasonKeys))
		labels := make([]string, 0, len(seasonKeys))

		for _, seasonKey := range seasonKeys {
			link := fmt.Sprintf(
				"%s/lite/filmix?rjson=%s&postid=%d&title=%s&original_title=%s&s=%s",
				host,
				getsTVBool(rjson),
				postID,
				encTitle,
				encOriginal,
				url.QueryEscape(seasonKey),
			)

			labelSeason := strings.ReplaceAll(seasonKey, "-1", "1") + " сезон"
			data = append(data, map[string]any{
				"method": "link",
				"id":     seasonKey,
				"url":    link,
				"name":   labelSeason,
			})
			labels = append(labels, labelSeason)
		}

		if len(data) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
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

	seasonKey := strconv.Itoa(s)
	voicesMap, ok := playlist[seasonKey]
	if !ok || len(voicesMap) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	voiceNames := make([]string, 0, len(voicesMap))
	for key := range voicesMap {
		voiceNames = append(voiceNames, key)
	}
	sort.Strings(voiceNames)

	if t < 0 || t >= len(voiceNames) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	voiceRows := make([]map[string]any, 0, len(voiceNames))
	for i, voice := range voiceNames {
		link := fmt.Sprintf(
			"%s/lite/filmix?rjson=%s&postid=%d&title=%s&original_title=%s&s=%d&t=%d",
			host,
			getsTVBool(rjson),
			postID,
			encTitle,
			encOriginal,
			s,
			i,
		)
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   voice,
			"active": i == t,
			"url":    link,
		})
	}

	episodes := filmixDecodeEpisodeSet(voicesMap[voiceNames[t]])
	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	cdns := f.collectEpisodeCDNs(episodes)
	episodeKeys := make([]string, 0, len(episodes))
	for key := range episodes {
		episodeKeys = append(episodeKeys, key)
	}
	sort.Slice(episodeKeys, func(i, j int) bool {
		ai := filmixEpisodeOrder(episodeKeys[i], i)
		bi := filmixEpisodeOrder(episodeKeys[j], j)
		if ai == bi {
			return episodeKeys[i] < episodeKeys[j]
		}
		return ai < bi
	})

	baseTitle := getsTVJoinName(title, originalTitle)
	episodeData := make([]map[string]any, 0, len(episodeKeys))
	episodeLabels := make([]string, 0, len(episodeKeys))
	seasons := make([]int, 0, len(episodeKeys))
	episodesNum := make([]int, 0, len(episodeKeys))

	seasonNum := s
	if seasonNum == -1 {
		seasonNum = 1
	}

	for _, key := range episodeKeys {
		item := episodes[key]
		streams := f.buildEpisodeStreams(item, token, cdns)
		if len(streams) == 0 {
			continue
		}
		filmixRememberMint(streams, postID, token)

		for i := range streams {
			streams[i]["url"] = streamProxyURLWithHeaders(req, streams[i]["url"], "filmix", links, filmixStreamHeaders())
		}

		episodeNum := filmixEpisodeOrder(key, len(episodeData)+1)
		label := strings.TrimSpace(key) + " серия"
		if strings.TrimSpace(key) == "" {
			label = strconv.Itoa(episodeNum) + " серия"
		}

		row := map[string]any{
			"method":        "play",
			"url":           streams[0]["url"],
			"stream":        streams[0]["url"],
			"s":             seasonNum,
			"e":             episodeNum,
			"name":          label,
			"title":         fmt.Sprintf("%s (%s)", baseTitle, label),
			"streamquality": streams,
		}
		if f.fxDirectLampa {
			row["fxdirect"] = f.filmixRowFxDirect(postID, voiceNames[t], seasonNum, episodeNum)
		}
		if len(streams) > 1 {
			qualMap := filmixBuildQualityMap(streams)
			row["quality"] = qualMap
			row["qualitys"] = qualMap
		}

		episodeData = append(episodeData, row)
		episodeLabels = append(episodeLabels, label)
		seasons = append(seasons, seasonNum)
		episodesNum = append(episodesNum, episodeNum)
	}

	if len(episodeData) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		payload := map[string]any{
			"type": "episode",
			"data": episodeData,
		}
		if len(voiceRows) > 0 {
			payload["voice"] = voiceRows
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	var sb strings.Builder
	if len(voiceRows) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, row := range voiceRows {
			getsTVAppendVoiceHTML(&sb, row)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range episodeData {
		getsTVAppendMovieHTML(&sb, row, episodeLabels[i], i == 0, seasons[i], episodesNum[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (f *filmixChecker) writeSimilar(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	title string,
	originalTitle string,
	items []filmixSimilarItem,
) {
	if len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	data := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))
	for _, item := range items {
		if item.ID <= 0 {
			continue
		}
		link := fmt.Sprintf(
			"%s/lite/filmix?postid=%d&title=%s&original_title=%s",
			host,
			item.ID,
			encTitle,
			encOriginal,
		)
		data = append(data, map[string]any{
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

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "similar",
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
}

func (f *filmixChecker) makeAuthQuery(token string) url.Values {
	qs := url.Values{}
	qs.Set("app_lang", "ru_RU")
	qs.Set("user_dev_apk", "2.2.13")
	qs.Set("user_dev_id", f.userDevID(token))
	qs.Set("user_dev_name", "Xiaomi 24069PC21G")
	qs.Set("user_dev_os", "14")
	qs.Set("user_dev_token", strings.TrimSpace(token))
	qs.Set("user_dev_vendor", "Xiaomi")
	return qs
}

func (f *filmixChecker) pickToken() string {
	if len(f.tokens) == 0 {
		return f.token
	}
	if len(f.tokens) == 1 {
		return f.tokens[0]
	}

	// Мёртвые и бесправные токены — в самый конец: пока есть хоть один живой PRO, выбираем
	// среди живых. Все мёртвые — берём любой: ≤720p по нему всё ещё настоящие.
	usable := make([]string, 0, len(f.tokens))
	for _, tok := range f.tokens {
		if f.tokenUsable(tok) {
			usable = append(usable, tok)
		}
	}
	if len(usable) == 0 {
		usable = f.tokens
	}
	f.tokenMux.Lock()
	defer f.tokenMux.Unlock()
	return usable[mathrand.Intn(len(usable))]
}

func (f *filmixChecker) userDevID(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		token = "_anonymous_"
	}
	if cached, ok := f.userIDs.Load(token); ok {
		if v, ok := cached.(string); ok && v != "" {
			return v
		}
	}

	id := randomHex16()
	f.userIDs.Store(token, id)
	return id
}

func randomHex16() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "a1b2c3d4e5f60708"
	}
	return hex.EncodeToString(b[:])
}

// allowQuality gates the LEGACY filmixapp.cyou path.
//
//   - no token: 480p only (filmix's free tier);
//   - token without `pro`: up to 720p;
//   - token + `pro = true`: every quality the link template lists — the same
//     rule lampac-nextgen applies (`if (!pro) cap at 720`).
//
// The 2026-07 note that /s/ hashes "never authorize >720p" was re-verified
// 2026-09-02 against the production pro token: SDR rips stream the real file
// (Дюна 2160p = 7.7 GB, 1440p = 5.8 GB, 1080p = 3.6 GB, all HTTP 206). What
// DOES stub without subscription rights is the HEVC/HDR/Dolby family — those
// rows are still filtered by filmixHideUnplayable, not by a quality cap. The
// post "quality" field is not consulted: it reports the content's max quality
// even for anonymous requests.
//
// 22.09.2026: права — ещё и по здоровью токена (filmix_tokens.go): по мёртвому/бесправному
// токену CDN отдаёт ≤720p настоящими, а выше — заглушку, так что `pro` из конфига ему не указ.
func (f *filmixChecker) allowQuality(q int, token string) bool {
	if strings.TrimSpace(token) == "" {
		return q <= 480
	}
	if f.pro && f.tokenUsable(token) {
		return true
	}
	return q <= 720
}

func (f *filmixChecker) collectMovieCDNs(movies []filmixMovie) []string {
	if !f.reserve {
		return nil
	}
	out := make([]string, 0, len(movies))
	seen := map[string]struct{}{}
	for _, movie := range movies {
		cdn := strings.TrimSpace(submatch1(filmixCDNRe, movie.Link))
		if cdn == "" {
			continue
		}
		if _, ok := seen[cdn]; ok {
			continue
		}
		seen[cdn] = struct{}{}
		out = append(out, cdn)
	}
	return out
}

func (f *filmixChecker) collectEpisodeCDNs(episodes map[string]filmixEpisodeItem) []string {
	if !f.reserve {
		return nil
	}
	out := make([]string, 0, len(episodes))
	seen := map[string]struct{}{}
	for _, item := range episodes {
		cdn := strings.TrimSpace(submatch1(filmixCDNRe, item.Link))
		if cdn == "" {
			continue
		}
		if _, ok := seen[cdn]; ok {
			continue
		}
		seen[cdn] = struct{}{}
		out = append(out, cdn)
	}
	return out
}

func (f *filmixChecker) buildMovieStreams(movie filmixMovie, token string, cdns []string) []map[string]string {
	link := strings.TrimSpace(movie.Link)
	if link == "" {
		return nil
	}

	// Every quality filmix knows, highest first; the link template
	// (`…_[2160,1440,1080,720,480,].mp4`) says which ones this rip has and
	// allowQuality says which ones this account may play.
	qualities := []int{2160, 1440, 1080, 720, 480}
	out := make([]map[string]string, 0, len(qualities))
	for _, q := range qualities {
		if !f.allowQuality(q, token) {
			continue
		}
		if !strings.Contains(link, fmt.Sprintf("%d,", q)) {
			continue
		}

		resolved := filmixMovieQualitiesRe.ReplaceAllString(link, fmt.Sprintf("_%d.mp4", q))
		resolved = f.toHLS(resolved)
		resolved = filmixReserveLink(resolved, cdns)

		out = append(out, map[string]string{
			"quality": fmt.Sprintf("%dp", q),
			"url":     resolved,
		})
	}
	return out
}

func (f *filmixChecker) buildEpisodeStreams(item filmixEpisodeItem, token string, cdns []string) []map[string]string {
	link := strings.TrimSpace(item.Link)
	if link == "" {
		return nil
	}

	qualities := make([]int, 0, len(item.Qualities))
	for _, q := range item.Qualities {
		if q > 0 {
			qualities = append(qualities, q)
		}
	}
	sort.Slice(qualities, func(i, j int) bool { return qualities[i] > qualities[j] })

	out := make([]map[string]string, 0, len(qualities))
	for _, q := range qualities {
		if !f.allowQuality(q, token) {
			continue
		}

		resolved := strings.Replace(link, "_%s.mp4", fmt.Sprintf("_%d.mp4", q), 1)
		if resolved == link {
			resolved = filmixMovieQualitiesRe.ReplaceAllString(link, fmt.Sprintf("_%d.mp4", q))
		}
		resolved = f.toHLS(resolved)
		resolved = filmixReserveLink(resolved, cdns)

		out = append(out, map[string]string{
			"quality": fmt.Sprintf("%dp", q),
			"url":     resolved,
		})
	}
	return out
}

func (f *filmixChecker) toHLS(link string) string {
	if !f.hls {
		return link
	}
	if filmixHdrHEVCRe.MatchString(link) {
		return link
	}
	// cdnsqu/werkecdn killed HLS: synthesizing /hls/...index.m3u8?hash= here produces a dead
	// url (403/404). Keep the direct /s/<token>/...mp4 the API returned — it streams 206.
	if filmixDeadHLSHostRe.MatchString(link) {
		return link
	}
	m := filmixHLSRe.FindStringSubmatch(link)
	if len(m) != 4 {
		return link
	}
	return fmt.Sprintf("%s/hls/%s/index.m3u8?hash=%s", m[1], m[3], m[2])
}

func filmixReserveLink(link string, cdns []string) string {
	if len(cdns) == 0 {
		return link
	}
	for _, cdn := range cdns {
		if strings.Contains(link, cdn) {
			continue
		}
		return link + " or " + filmixHostPrefixRe.ReplaceAllString(link, cdn)
	}
	return link
}

func filmixDecodeEpisodeSet(raw stdjson.RawMessage) map[string]filmixEpisodeItem {
	if len(raw) == 0 {
		return nil
	}

	out := make(map[string]filmixEpisodeItem)

	var byName map[string]filmixEpisodeItem
	if err := stdjson.Unmarshal(raw, &byName); err == nil && len(byName) > 0 {
		for k, v := range byName {
			if strings.TrimSpace(v.Link) == "" {
				continue
			}
			out[k] = v
		}
		if len(out) > 0 {
			return out
		}
	}

	var byOrder []filmixEpisodeItem
	if err := stdjson.Unmarshal(raw, &byOrder); err == nil && len(byOrder) > 0 {
		for i, v := range byOrder {
			if strings.TrimSpace(v.Link) == "" {
				continue
			}
			out[strconv.Itoa(i+1)] = v
		}
		if len(out) > 0 {
			return out
		}
	}

	return nil
}

// filmixBuildQualityMap converts a streamquality slice into a quality map for Lampa.
func filmixBuildQualityMap(streams []map[string]string) map[string]any {
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

func filmixEpisodeOrder(key string, fallback int) int {
	if n, ok := getsTVQueryInt(strings.TrimSpace(key)); ok && n > 0 {
		return n
	}
	m := filmixEpisodeNumRe.FindStringSubmatch(key)
	if len(m) == 2 {
		if n, err := strconv.Atoi(strings.TrimSpace(m[1])); err == nil && n > 0 {
			return n
		}
	}
	if fallback < 1 {
		return 1
	}
	return fallback
}

func filmixNormalizeName(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(v))
	for _, r := range v {
		if r == 'ё' {
			r = 'е'
		} else if r == 'щ' {
			r = 'ш'
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func bytesTrimSpace(b []byte) []byte {
	start := 0
	for start < len(b) {
		switch b[start] {
		case ' ', '\n', '\r', '\t':
			start++
		default:
			goto tail
		}
	}
	return b[:0]

tail:
	end := len(b) - 1
	for end >= start {
		switch b[end] {
		case ' ', '\n', '\r', '\t':
			end--
		default:
			return b[start : end+1]
		}
	}
	return b[start : start+1]
}

// FilmixAPIHosts — failover-упорядоченные зеркала легаси-API для вызывающих вне пакета
// (kit-привязка). Зеркала гаснут по отдельности, поэтому единственный захардкоженный хост
// превращает частный сбой зеркала в «filmix сломался» для всех.
func FilmixAPIHosts(configured string) []string { return filmixAPIHosts(configured) }
