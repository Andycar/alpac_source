package litesrc

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	mathrand "math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// SakhTV (api.sakh.tv) — Russian Pro media provider, Android TV API.
//
// Flow:
//   - POST /v2/users/login {login,password} → {auth,token,user}; token is a UUID
//     stored in Authorization header for subsequent calls.
//   - GET  /v2/common/search?query=&amount=  → {movies[],serials[]}
//   - GET  /v2/movie/{id_alpha}              → MovieDTO with sources.{default,variants[]}
//   - GET  /v1/serials/get?tvshow={slug}     → SeriesDTO with seasons[{id,index}]
//   - GET  /v1/serials/get_episodes?season_id=X → [{id,index,rgs[{id,rg,runame,…}]}]
//   - GET  /v1/serial/watch/get_playlist?season_id=X&rg=Y → [{episode_id,…,episode_playlist}]
//
// All endpoints require headers Authorization, X-Force-Code: 1, X-App-Id: 5,
// plus a User-Agent (any string seems to work).
const (
	sakhtvAppVersion     = "1.2.0"
	sakhtvDefaultAppID   = "5"
	sakhtvSearchAmount   = 20
	sakhtvSeriesCacheTTL = 5 * time.Minute

	// sakhtvLoginMinInterval floors the distance between two logins.
	//
	// The account is single-session upstream: every POST /v2/users/login mints a
	// new token and retires the previous one, which kills the stream links other
	// viewers are already playing. So a login is a DESTRUCTIVE act here, not a
	// cheap retry — better to leave this one source dead for a few minutes than
	// to cut off everybody currently watching. Anything that would log in inside
	// the window fails with errSakhTVLoginCooldown instead.
	sakhtvLoginMinInterval = 3 * time.Minute
)

// sakhtvDeviceProfiles mimic the User-Agent template the SakhTV Android TV APK
// generates from Build.MANUFACTURER / Build.MODEL / Build.VERSION.RELEASE
// (see decompiled BitmapFactoryDecoder$$ExternalSyntheticLambda0 case 28:
// "SakhTVAndroid/<verName>/<MAKE MODEL>/Android <RELEASE>"). Rotating across
// realistic Android TV boxes makes our calls indistinguishable from a real
// install and prevents per-UA throttling.
var sakhtvDeviceProfiles = []struct {
	Device  string
	Release string
}{
	{"Xiaomi Mi BOX 4", "9"},
	{"Xiaomi Mi BOX S", "9"},
	{"Xiaomi MIBOX4K", "11"},
	{"Nvidia SHIELD Android TV", "11"},
	{"Sony BRAVIA 4K GB", "13"},
	{"Sony BRAVIA 4K UR1", "12"},
	{"Onn 4K Streaming Box", "12"},
	{"Hisense H65A6500", "11"},
	{"Tcl TV", "13"},
	{"Realme TV", "10"},
	{"Google ADT-3", "13"},
	{"Google sdk_gphone_x86_tv", "13"},
}

var sakhtvUARand = mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
var sakhtvUARandMu sync.Mutex

// sakhtvUserAgent picks a UA string in the APK's exact format. We pin a single
// random profile per balancer instance so all requests in a session share the
// same identity — switching mid-session looks suspicious.
func sakhtvUserAgent() string {
	sakhtvUARandMu.Lock()
	profile := sakhtvDeviceProfiles[sakhtvUARand.Intn(len(sakhtvDeviceProfiles))]
	sakhtvUARandMu.Unlock()
	return fmt.Sprintf("SakhTVAndroid/%s/%s/Android %s", sakhtvAppVersion, profile.Device, profile.Release)
}

// sakhtvConf is the hot-reloadable half of the checker. The checker itself is a
// process singleton (see SharedSakhTVChecker) so the token survives every
// liteSourceHandler rebuild; the config it was built from does not, so it lives
// behind an atomic pointer and gets swapped on reload.
type sakhtvConf struct {
	host         string
	login        string
	passwd       string
	appID        string
	noForceLogin bool
}

func (c *sakhtvConf) sameAccount(o *sakhtvConf) bool {
	return c.host == o.host && c.login == o.login && c.passwd == o.passwd
}

type sakhtvChecker struct {
	client *http.Client

	conf      atomic.Pointer[sakhtvConf]
	cfgToken  string // config-pinned token (never rotated, never logged out)
	userAgent string // pinned per-instance to match APK identity for a session
	store     *sakhtvTokenStore

	tokenMu   sync.Mutex
	token     string    // current Authorization value
	tokenAt   time.Time // when the current token was minted/loaded
	lastLogin time.Time // last login ATTEMPT (success or not) — cooldown anchor

	cacheMu     sync.Mutex
	seriesCache map[string]sakhtvSeriesCacheEntry
	voicesCache map[string]sakhtvVoicesCacheEntry
}

type sakhtvSeriesCacheEntry struct {
	detail  *sakhtvSeriesDetail
	expires time.Time
}

type sakhtvVoicesCacheEntry struct {
	voices   []sakhtvEpisodeVoice
	previews map[int]string // episode_id → preview URL
	airdates map[int]string // episode_id → ISO YYYY-MM-DD
	expires  time.Time
}

// --- DTOs ---

type sakhtvLoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type sakhtvLoginResponse struct {
	Auth  bool   `json:"auth"`
	Token string `json:"token"`
}

type sakhtvSearchItem struct {
	// Common
	ID int `json:"id"`
	// Movie-only
	IDAlpha     string `json:"id_alpha"`
	RuTitle     string `json:"ru_title"`
	OriginTitle string `json:"origin_title"`
	ReleaseDate string `json:"release_date"`
	// Serial-only
	Tvshow  string `json:"tvshow"`
	Name    string `json:"name"`
	Ename   string `json:"ename"`
	Year    int    `json:"year"`
	YearEnd int    `json:"year_end"`
	KPID    int    `json:"kp_id"`
	ImdbURL string `json:"imdb_url"`
}

type sakhtvSearchResponse struct {
	Movies  []sakhtvSearchItem `json:"movies"`
	Serials []sakhtvSearchItem `json:"serials"`
}

type sakhtvVariant struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type sakhtvTrack struct {
	Label    string `json:"label"`
	Language string `json:"language"`
	Src      string `json:"src"`
}

type sakhtvMovieDetail struct {
	ID          int    `json:"id"`
	IDAlpha     string `json:"id_alpha"`
	RuTitle     string `json:"ru_title"`
	OriginTitle string `json:"origin_title"`
	KPID        int    `json:"kp_id"`
	ImdbID      string `json:"imdb_id"`
	ReleaseDate string `json:"release_date"`
	Sources     struct {
		Default  string          `json:"default"`
		Variants []sakhtvVariant `json:"variants"`
		PD       struct {
			Tracks []sakhtvTrack `json:"tracks"`
		} `json:"pd"`
	} `json:"sources"`
}

type sakhtvSeriesSeason struct {
	ID    int    `json:"id"`
	Index string `json:"index"`
}

type sakhtvSeriesDetail struct {
	ID       int                  `json:"id"`
	Tvshow   string               `json:"tvshow"`
	Name     string               `json:"name"`
	Ename    string               `json:"ename"`
	KPID     int                  `json:"kp_id"`
	ImdbURL  string               `json:"imdb_url"`
	Poster   string               `json:"poster"`
	Backdrop string               `json:"backdrop"`
	Seasons  []sakhtvSeriesSeason `json:"seasons"`
}

type sakhtvEpisodeVoice struct {
	ID     int    `json:"id"`
	RG     string `json:"rg"`
	Runame string `json:"runame"`
}

type sakhtvEpisode struct {
	ID      int                  `json:"id"`
	Index   string               `json:"index"`
	Name    string               `json:"name"`
	Preview string               `json:"preview"`
	Date    string               `json:"date"` // DD.MM.YYYY
	RGs     []sakhtvEpisodeVoice `json:"rgs"`
}

type sakhtvPlaylistItem struct {
	EpisodeID    int    `json:"episode_id"`
	EpisodeIndex string `json:"episode_index"`
	EpisodeName  string `json:"episode_name"`
	MediaID      int    `json:"media_id"`
	RG           string `json:"rg"`
	Playlist     string `json:"episode_playlist"`
}

// --- Constructor / handle ---

func sakhtvConfFrom(cfg config.Config) *sakhtvConf {
	host := strings.TrimSpace(cfg.Online.SakhTV.Host)
	if host == "" {
		host = "https://api.sakh.tv"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	host = strings.TrimRight(host, "/")

	appID := strings.TrimSpace(cfg.Online.SakhTV.AppID)
	if appID == "" {
		appID = sakhtvDefaultAppID
	}
	return &sakhtvConf{
		host:         host,
		login:        strings.TrimSpace(cfg.Online.SakhTV.Login),
		passwd:       cfg.Online.SakhTV.Passwd,
		appID:        appID,
		noForceLogin: cfg.Online.SakhTV.NoForceLogin,
	}
}

var (
	sakhtvSharedOnce sync.Once
	sakhtvShared     *sakhtvChecker
)

// SharedSakhTVChecker returns the process-wide SakhTV checker, building it once.
//
// liteSourceHandler is rebuilt repeatedly — at boot for the /lite routes AND by
// capiLiteSource on every config hot-reload — and it used to mint a FRESH
// checker each time. For SakhTV that is not a wasted allocation but a broken
// stream: a new checker starts with an empty token, logs in, and the upstream
// retires the token the OTHER checker is still handing out, so whoever was
// watching gets cut off. One instance = one session for the whole process.
//
// Config edits still land: applyConfig swaps the hot half on every call.
func SharedSakhTVChecker(cfg config.Config) *sakhtvChecker {
	sakhtvSharedOnce.Do(func() { sakhtvShared = NewSakhTVChecker(cfg) })
	sakhtvShared.applyConfig(cfg)
	return sakhtvShared
}

func NewSakhTVChecker(cfg config.Config) *sakhtvChecker {
	conf := sakhtvConfFrom(cfg)
	s := &sakhtvChecker{
		client:      httpclient.NewForBalancer("sakhtv", 12*time.Second),
		cfgToken:    strings.TrimSpace(cfg.Online.SakhTV.Token),
		userAgent:   sakhtvUserAgent(),
		store:       newSakhtvTokenStore(cfg.Compat.RepoRoot),
		seriesCache: make(map[string]sakhtvSeriesCacheEntry, 64),
		voicesCache: make(map[string]sakhtvVoicesCacheEntry, 64),
	}
	s.conf.Store(conf)

	// Token priority: config-pinned > persisted (same account) > login on demand.
	// The persisted one is what keeps a restart/rebuild from minting a new
	// session and knocking every in-flight viewer off their stream.
	if s.cfgToken != "" {
		s.token = s.cfgToken
		s.tokenAt = time.Now()
		return s
	}
	if saved, err := s.store.load(); err != nil {
		log.Warn().Err(err).Msg("sakhtv: failed to read persisted token; will log in on demand")
	} else if saved.Token != "" && saved.Login == conf.login {
		s.token = saved.Token
		s.tokenAt = saved.IssuedAt
		log.Info().Time("issued", saved.IssuedAt).Msg("sakhtv: reusing persisted session token")
	}
	return s
}

// applyConfig swaps the hot-reloadable config. Credentials or host changing
// means the persisted session belongs to a different account, so the token is
// dropped — that is the one case where a fresh login is the correct answer.
func (s *sakhtvChecker) applyConfig(cfg config.Config) {
	next := sakhtvConfFrom(cfg)
	prev := s.conf.Load()
	if prev != nil && *prev == *next {
		return
	}
	s.conf.Store(next)
	if prev == nil || prev.sameAccount(next) {
		return
	}
	s.tokenMu.Lock()
	s.token = strings.TrimSpace(cfg.Online.SakhTV.Token)
	s.cfgToken = s.token
	s.tokenAt = time.Now()
	s.lastLogin = time.Time{} // new account — the cooldown from the old one must not gate it
	s.tokenMu.Unlock()
	log.Info().Msg("sakhtv: account/host changed in config — session token dropped")
}

// --- Persistent token store ---
//
// File layout: <repoRoot>/database/sakhtv_token.json
//
//	{"token": "<uuid>", "login": "<account>", "issued_at": "2026-07-25T…Z"}
//
// The login is recorded so a credentials change in the admin panel invalidates
// the record instead of resurrecting a session that belongs to another account.

type sakhtvTokenFile struct {
	Token    string    `json:"token"`
	Login    string    `json:"login"`
	IssuedAt time.Time `json:"issued_at"`
}

type sakhtvTokenStore struct {
	mu   sync.Mutex
	path string
}

func newSakhtvTokenStore(repoRoot string) *sakhtvTokenStore {
	if strings.TrimSpace(repoRoot) == "" {
		repoRoot = "."
	}
	return &sakhtvTokenStore{path: filepath.Join(repoRoot, "database", "sakhtv_token.json")}
}

func (s *sakhtvTokenStore) load() (sakhtvTokenFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out sakhtvTokenFile
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := stdjson.Unmarshal(data, &out); err != nil {
		return sakhtvTokenFile{}, err
	}
	return out, nil
}

func (s *sakhtvTokenStore) save(f sakhtvTokenFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := stdjson.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// applyAPKHeaders sets the same four headers the SakhTV Android TV APK sends
// on every request (per its okhttp interceptor): Authorization, X-Force-Code,
// X-App-Id, User-Agent — plus Accept: application/json which the Retrofit
// converter implicitly adds.
func (s *sakhtvChecker) applyAPKHeaders(req *http.Request, token string) {
	s.applyAPKHeadersConf(req, token, s.conf.Load(), false)
}

// applyAPKHeadersConf is applyAPKHeaders with an explicit config snapshot.
// isLogin honours [online.sakhtv] no_force_login: X-Force-Code is what the APK
// uses to CLAIM the single account session, so on the login call it is the
// header most likely to be what evicts the session another viewer is streaming
// on. Dropping it only there lets the upstream refuse a duplicate login instead
// of stealing one — flip the flag if the probe shows the account allows it.
func (s *sakhtvChecker) applyAPKHeadersConf(req *http.Request, token string, conf *sakhtvConf, isLogin bool) {
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	if !isLogin || !conf.noForceLogin {
		req.Header.Set("X-Force-Code", "1")
	}
	req.Header.Set("X-App-Id", conf.appID)
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
}

func (s *sakhtvChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := s.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("sakhtv"))
			return
		}
		if conf := s.conf.Load(); conf.host == "" || !s.hasCredentials() {
			writeGetsTVEmpty(w, parseBoolParam(req.URL.Query().Get("rjson")))
			return
		}
		s.index(w, req, links)
	}
}

func (s *sakhtvChecker) hasCredentials() bool {
	s.tokenMu.Lock()
	tok := s.token
	s.tokenMu.Unlock()
	if tok != "" {
		return true
	}
	conf := s.conf.Load()
	return conf.login != "" && conf.passwd != ""
}

// --- Auth / token ---

var (
	errSakhTVUnauthorized  = errors.New("sakhtv: unauthorized")
	errSakhTVLoginCooldown = errors.New("sakhtv: login suppressed by cooldown (single-session account)")
)

// authToken returns a cached token or performs login. Safe for concurrent use.
func (s *sakhtvChecker) authToken(ctx context.Context) (string, error) {
	s.tokenMu.Lock()
	tok := s.token
	s.tokenMu.Unlock()
	if tok != "" {
		return tok, nil
	}
	return s.refreshToken(ctx, "")
}

// refreshToken performs a login. `stale` is the token that just failed (so we
// don't lose a race where another goroutine already refreshed).
//
// A login retires the previous session upstream, so it is rate-limited to one
// per sakhtvLoginMinInterval. Inside the window the call fails instead of
// re-authenticating: this source going quiet for a few minutes is strictly
// cheaper than every current viewer's playlist dying.
func (s *sakhtvChecker) refreshToken(ctx context.Context, stale string) (string, error) {
	conf := s.conf.Load()

	s.tokenMu.Lock()
	if s.token != "" && s.token != stale {
		// Another goroutine already rotated — use theirs, never stack a second login.
		tok := s.token
		s.tokenMu.Unlock()
		return tok, nil
	}
	if s.cfgToken != "" && s.cfgToken == stale {
		// Operator pinned this token in config; re-logging in would silently
		// diverge from what the admin set. Surface it instead.
		s.tokenMu.Unlock()
		return "", errSakhTVUnauthorized
	}
	if conf.login == "" || conf.passwd == "" {
		s.tokenMu.Unlock()
		return "", errors.New("sakhtv: no credentials configured")
	}
	if !s.lastLogin.IsZero() && time.Since(s.lastLogin) < sakhtvLoginMinInterval {
		since := time.Since(s.lastLogin)
		s.tokenMu.Unlock()
		log.Debug().Dur("since_last_login", since).Msg("sakhtv: login suppressed — cooldown")
		return "", errSakhTVLoginCooldown
	}
	prev := s.token
	s.lastLogin = time.Now() // claim the window before the network call: no thundering herd
	s.tokenMu.Unlock()

	tok, err := s.doLogin(ctx, conf)
	if err != nil {
		return "", err
	}

	s.tokenMu.Lock()
	s.token = tok
	s.tokenAt = time.Now()
	s.tokenMu.Unlock()

	if err := s.store.save(sakhtvTokenFile{Token: tok, Login: conf.login, IssuedAt: time.Now()}); err != nil {
		log.Warn().Err(err).Msg("sakhtv: failed to persist session token")
	}
	if prev != "" && prev != tok {
		// The old session is gone upstream; any link minted under it — including
		// the ones sitting in capi's stream cache — may already be dead.
		log.Warn().Msg("sakhtv: session rotated — in-flight playback links may have been invalidated upstream")
		go purgeStreamCache("sakhtv")
	}
	return tok, nil
}

func (s *sakhtvChecker) doLogin(ctx context.Context, conf *sakhtvConf) (string, error) {
	body, _ := stdjson.Marshal(sakhtvLoginRequest{Login: conf.login, Password: conf.passwd})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, conf.host+"/v2/users/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	s.applyAPKHeadersConf(req, "", conf, true)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("sakhtv: login HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	var lr sakhtvLoginResponse
	if err := stdjson.Unmarshal(raw, &lr); err != nil {
		return "", err
	}
	if !lr.Auth || lr.Token == "" {
		return "", errSakhTVUnauthorized
	}
	return lr.Token, nil
}

// doJSON issues a GET against the SakhTV API and decodes JSON.
//
// Only 401 is treated as "the token died" — 403 is the upstream's answer for
// content that is geo-/plan-/rate-gated and says nothing about the session. The
// old code refreshed on both, so a single 403 from one user's request minted a
// new session and knocked every other viewer off their stream.
func (s *sakhtvChecker) doJSON(ctx context.Context, path string, out any) error {
	target := s.conf.Load().host + path
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := s.authToken(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		s.applyAPKHeaders(req, tok)

		resp, err := s.client.Do(req)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode == http.StatusUnauthorized {
			if attempt == 0 {
				if _, err := s.refreshToken(ctx, tok); err != nil {
					return err
				}
				continue
			}
			return errSakhTVUnauthorized
		}
		if resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("sakhtv: HTTP 403 on %s (content/plan gate, not auth)", path)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("sakhtv: HTTP %d on %s", resp.StatusCode, path)
		}
		return stdjson.Unmarshal(body, out)
	}
	return errors.New("sakhtv: retry exhausted")
}

// --- Search / matching ---

func (s *sakhtvChecker) search(ctx context.Context, query string) (*sakhtvSearchResponse, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("amount", strconv.Itoa(sakhtvSearchAmount))
	var out sakhtvSearchResponse
	if err := s.doJSON(ctx, "/v2/common/search?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *sakhtvChecker) checkSearch(req *http.Request) bool {
	if conf := s.conf.Load(); conf.host == "" || !s.hasCredentials() {
		return false
	}
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	if title == "" && originalTitle == "" {
		return false
	}
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	imdb := strings.TrimSpace(q.Get("imdb_id"))
	kp := strings.TrimSpace(q.Get("kinopoisk_id"))
	serial := parseBoolParam(q.Get("serial"))

	ctx, cancel := context.WithTimeout(req.Context(), 8*time.Second)
	defer cancel()

	for _, query := range sakhtvSearchQueries(title, originalTitle) {
		res, err := s.search(ctx, query)
		if err != nil || res == nil {
			continue
		}
		if serial {
			if pickSakhtvSerial(res.Serials, year, imdb, kp) != nil {
				return true
			}
		} else {
			if pickSakhtvMovie(res.Movies, year, imdb, kp, originalTitle) != nil {
				return true
			}
			// Some titles are mis-classified as serials; allow fallback.
			if pickSakhtvSerial(res.Serials, year, imdb, kp) != nil {
				return true
			}
		}
	}
	return false
}

func sakhtvSearchQueries(title, originalTitle string) []string {
	out := make([]string, 0, 2)
	seen := map[string]struct{}{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		key := strings.ToLower(v)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	add(title)
	add(originalTitle)
	return out
}

func pickSakhtvMovie(items []sakhtvSearchItem, year int, imdb, kp, originalTitle string) *sakhtvSearchItem {
	if len(items) == 0 {
		return nil
	}
	// Year match preferred; otherwise first result.
	originalLC := strings.ToLower(strings.TrimSpace(originalTitle))
	var byYear, byOrigin *sakhtvSearchItem
	for i := range items {
		it := &items[i]
		if it.IDAlpha == "" {
			continue
		}
		iy := sakhtvParseYear(it.ReleaseDate)
		if year > 0 && iy == year && byYear == nil {
			byYear = it
		}
		if originalLC != "" && strings.EqualFold(strings.TrimSpace(it.OriginTitle), originalTitle) && byOrigin == nil {
			byOrigin = it
		}
	}
	if byYear != nil {
		return byYear
	}
	if byOrigin != nil {
		return byOrigin
	}
	for i := range items {
		if items[i].IDAlpha != "" {
			return &items[i]
		}
	}
	_ = imdb
	_ = kp
	return nil
}

func pickSakhtvSerial(items []sakhtvSearchItem, year int, imdb, kp string) *sakhtvSearchItem {
	if len(items) == 0 {
		return nil
	}
	kpInt, _ := strconv.Atoi(kp)
	imdbLC := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(imdb), "tt"))

	var byKP, byImdb, byYear *sakhtvSearchItem
	for i := range items {
		it := &items[i]
		if it.Tvshow == "" {
			continue
		}
		if kpInt > 0 && it.KPID == kpInt && byKP == nil {
			byKP = it
		}
		if imdbLC != "" && byImdb == nil {
			lc := strings.ToLower(it.ImdbURL)
			if strings.Contains(lc, "tt"+imdbLC) || strings.Contains(lc, "/"+imdbLC) {
				byImdb = it
			}
		}
		if year > 0 && it.Year == year && byYear == nil {
			byYear = it
		}
	}
	if byKP != nil {
		return byKP
	}
	if byImdb != nil {
		return byImdb
	}
	if byYear != nil {
		return byYear
	}
	for i := range items {
		if items[i].Tvshow != "" {
			return &items[i]
		}
	}
	return nil
}

func sakhtvParseYear(release string) int {
	release = strings.TrimSpace(release)
	if len(release) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(release[:4])
	return y
}

// --- Movie / serial fetchers ---

func (s *sakhtvChecker) movieDetail(ctx context.Context, idAlpha string) (*sakhtvMovieDetail, error) {
	var out sakhtvMovieDetail
	if err := s.doJSON(ctx, "/v2/movie/"+url.PathEscape(idAlpha), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *sakhtvChecker) seriesDetail(ctx context.Context, tvshow string) (*sakhtvSeriesDetail, error) {
	s.cacheMu.Lock()
	if ent, ok := s.seriesCache[tvshow]; ok && time.Now().Before(ent.expires) {
		s.cacheMu.Unlock()
		return ent.detail, nil
	}
	s.cacheMu.Unlock()

	var out sakhtvSeriesDetail
	q := url.Values{"tvshow": {tvshow}}
	if err := s.doJSON(ctx, "/v1/serials/get?"+q.Encode(), &out); err != nil {
		return nil, err
	}

	s.cacheMu.Lock()
	s.seriesCache[tvshow] = sakhtvSeriesCacheEntry{detail: &out, expires: time.Now().Add(sakhtvSeriesCacheTTL)}
	s.cacheMu.Unlock()
	return &out, nil
}

// seasonVoices returns the union of voice tracks across all episodes of a
// season + a map of episode_id → preview thumbnail URL and ISO air date
// (extracted from the same get_episodes payload so we avoid a second
// round-trip).
func (s *sakhtvChecker) seasonVoices(ctx context.Context, seasonID int) ([]sakhtvEpisodeVoice, map[int]string, map[int]string, error) {
	key := strconv.Itoa(seasonID)
	s.cacheMu.Lock()
	if ent, ok := s.voicesCache[key]; ok && time.Now().Before(ent.expires) {
		s.cacheMu.Unlock()
		return ent.voices, ent.previews, ent.airdates, nil
	}
	s.cacheMu.Unlock()

	var eps []sakhtvEpisode
	q := url.Values{"season_id": {key}}
	if err := s.doJSON(ctx, "/v1/serials/get_episodes?"+q.Encode(), &eps); err != nil {
		return nil, nil, nil, err
	}

	seen := map[string]struct{}{}
	voices := make([]sakhtvEpisodeVoice, 0, 8)
	previews := make(map[int]string, len(eps))
	airdates := make(map[int]string, len(eps))
	for _, ep := range eps {
		if ep.ID > 0 && strings.TrimSpace(ep.Preview) != "" {
			previews[ep.ID] = ep.Preview
		}
		if ep.ID > 0 {
			if iso := sakhtvDateToISO(ep.Date); iso != "" {
				airdates[ep.ID] = iso
			}
		}
		for _, v := range ep.RGs {
			if v.RG == "" {
				continue
			}
			if _, ok := seen[v.RG]; ok {
				continue
			}
			seen[v.RG] = struct{}{}
			voices = append(voices, v)
		}
	}

	s.cacheMu.Lock()
	s.voicesCache[key] = sakhtvVoicesCacheEntry{
		voices:   voices,
		previews: previews,
		airdates: airdates,
		expires:  time.Now().Add(sakhtvSeriesCacheTTL),
	}
	s.cacheMu.Unlock()
	return voices, previews, airdates, nil
}

// sakhtvDateToISO converts a SakhTV episode date ("20.01.2008", "0", "")
// to ISO "YYYY-MM-DD" expected by Lampa.Utils.parseTime. Returns "" on
// missing/invalid input.
func sakhtvDateToISO(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return ""
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return ""
	}
	dd, mm, yyyy := parts[0], parts[1], parts[2]
	if len(dd) == 1 {
		dd = "0" + dd
	}
	if len(mm) == 1 {
		mm = "0" + mm
	}
	if len(yyyy) != 4 {
		return ""
	}
	return yyyy + "-" + mm + "-" + dd
}

func (s *sakhtvChecker) playlist(ctx context.Context, seasonID int, rg string) ([]sakhtvPlaylistItem, error) {
	q := url.Values{"season_id": {strconv.Itoa(seasonID)}, "rg": {rg}}
	var out []sakhtvPlaylistItem
	if err := s.doJSON(ctx, "/v1/serial/watch/get_playlist?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- Top-level dispatch ---

func (s *sakhtvChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	imdb := strings.TrimSpace(q.Get("imdb_id"))
	kp := strings.TrimSpace(q.Get("kinopoisk_id"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	serial := parseBoolParam(q.Get("serial"))
	season, sSet := getsTVQueryInt(q.Get("s"))
	voiceIdx, tSet := getsTVQueryInt(q.Get("t"))
	if !sSet {
		season = -1
	}
	if !tSet {
		voiceIdx = -1
	}

	if title == "" && originalTitle == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	ctx, cancel := context.WithTimeout(req.Context(), 12*time.Second)
	defer cancel()

	for _, query := range sakhtvSearchQueries(title, originalTitle) {
		res, err := s.search(ctx, query)
		if err != nil || res == nil {
			continue
		}
		if !serial {
			if m := pickSakhtvMovie(res.Movies, year, imdb, kp, originalTitle); m != nil {
				s.writeMovie(ctx, w, req, rjson, m.IDAlpha, title, originalTitle, links)
				return
			}
		}
		if ser := pickSakhtvSerial(res.Serials, year, imdb, kp); ser != nil {
			s.writeSerial(ctx, w, req, rjson, ser.Tvshow, title, originalTitle, season, voiceIdx, imdb, kp, links)
			return
		}
	}

	writeGetsTVEmpty(w, rjson)
}

// --- Movie renderer ---

func (s *sakhtvChecker) writeMovie(ctx context.Context, w http.ResponseWriter, req *http.Request, rjson bool, idAlpha, title, originalTitle string, links *proxylink.Manager) {
	movie, err := s.movieDetail(ctx, idAlpha)
	if err != nil || movie == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	stream := sakhtvPickStream(movie)
	if stream == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	stream = streamProxyURL(req, stream, "sakhtv", links)

	row := map[string]any{
		"method": "play",
		"url":    stream,
		"stream": stream,
		"name":   "По умолчанию",
		"title":  getsTVJoinName(sakhtvFirst(movie.RuTitle, title), sakhtvFirst(movie.OriginTitle, originalTitle)),
	}
	if subs := sakhtvSubtitles(movie.Sources.PD.Tracks); len(subs) > 0 {
		row["subtitles"] = subs
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
}

func sakhtvPickStream(movie *sakhtvMovieDetail) string {
	// Prefer the "hls" variant (plain master with full quality ladder); fall back
	// to "default" (which may be hlsmov01 — same content, slightly different
	// segment layout) and finally any other variant.
	for _, v := range movie.Sources.Variants {
		if strings.EqualFold(v.Type, "hls") && v.URL != "" {
			return v.URL
		}
	}
	if movie.Sources.Default != "" {
		return movie.Sources.Default
	}
	for _, v := range movie.Sources.Variants {
		if v.URL != "" {
			return v.URL
		}
	}
	return ""
}

func sakhtvSubtitles(tracks []sakhtvTrack) []map[string]string {
	if len(tracks) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(tracks))
	for _, t := range tracks {
		if t.Src == "" {
			continue
		}
		label := strings.TrimSpace(t.Label)
		if label == "" {
			label = strings.ToUpper(t.Language)
		}
		out = append(out, map[string]string{"label": label, "url": t.Src})
	}
	return out
}

// --- Serial renderer ---

func (s *sakhtvChecker) writeSerial(
	ctx context.Context,
	w http.ResponseWriter, req *http.Request, rjson bool,
	tvshow, title, originalTitle string,
	season, voiceIdx int,
	imdb, kp string,
	links *proxylink.Manager,
) {
	detail, err := s.seriesDetail(ctx, tvshow)
	if err != nil || detail == nil || len(detail.Seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	idParam := ""
	if imdb != "" {
		idParam += "&imdb_id=" + url.QueryEscape(imdb)
	}
	if kp != "" {
		idParam += "&kinopoisk_id=" + url.QueryEscape(kp)
	}

	// Season list.
	if season < 1 {
		type seasonItem struct {
			num   int
			title string
			link  string
		}
		items := make([]seasonItem, 0, len(detail.Seasons))
		for _, sn := range detail.Seasons {
			num, _ := strconv.Atoi(strings.TrimLeft(sn.Index, "0"))
			if num <= 0 {
				continue
			}
			link := fmt.Sprintf("%s/lite/sakhtv?rjson=%s&serial=true%s&title=%s&original_title=%s&s=%d",
				host, getsTVBool(rjson), idParam, encTitle, encOriginal, num)
			items = append(items, seasonItem{
				num:   num,
				title: fmt.Sprintf("%d сезон", num),
				link:  link,
			})
		}
		if len(items) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		sort.Slice(items, func(i, j int) bool { return items[i].num < items[j].num })

		// All season cards reuse the series poster — SakhTV has no per-season art.
		seasonImg := strings.TrimSpace(detail.Poster)
		rows := make([]map[string]any, 0, len(items))
		labels := make([]string, 0, len(items))
		for _, it := range items {
			row := map[string]any{
				"method": "link",
				"id":     it.num,
				"url":    it.link,
				"name":   it.title,
			}
			if seasonImg != "" {
				row["img"] = seasonImg
			}
			rows = append(rows, row)
			labels = append(labels, it.title)
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

	// Episode list.
	seasonID := 0
	for _, sn := range detail.Seasons {
		if num, _ := strconv.Atoi(strings.TrimLeft(sn.Index, "0")); num == season {
			seasonID = sn.ID
			break
		}
	}
	if seasonID == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	voices, previews, airdates, err := s.seasonVoices(ctx, seasonID)
	if err != nil || len(voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if previews == nil {
		previews = map[int]string{}
	}
	if airdates == nil {
		airdates = map[int]string{}
	}
	// Series-level poster as a fallback when an episode has no frame snapshot.
	seriesPoster := strings.TrimSpace(detail.Poster)
	if voiceIdx < 0 || voiceIdx >= len(voices) {
		voiceIdx = 0
	}

	voiceRows := make([]map[string]any, 0, len(voices))
	for i, v := range voices {
		link := fmt.Sprintf("%s/lite/sakhtv?rjson=%s&serial=true%s&title=%s&original_title=%s&s=%d&t=%d",
			host, getsTVBool(rjson), idParam, encTitle, encOriginal, season, i)
		name := strings.TrimSpace(v.Runame)
		if name == "" {
			name = v.RG
		}
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   name,
			"active": i == voiceIdx,
			"url":    link,
		})
	}

	pl, err := s.playlist(ctx, seasonID, voices[voiceIdx].RG)
	if err != nil || len(pl) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	baseTitle := getsTVJoinName(title, originalTitle)
	type epRow struct {
		data  map[string]any
		label string
		ep    int
	}
	episodes := make([]epRow, 0, len(pl))
	for _, p := range pl {
		stream := strings.TrimSpace(p.Playlist)
		if stream == "" {
			continue
		}
		epNum, _ := strconv.Atoi(strings.TrimLeft(p.EpisodeIndex, "0"))
		label := strings.TrimSpace(p.EpisodeName)
		if label == "" {
			label = fmt.Sprintf("%d серия", epNum)
		}
		stream = streamProxyURL(req, stream, "sakhtv", links)
		row := map[string]any{
			"method": "play",
			"url":    stream,
			"stream": stream,
			"s":      season,
			"e":      epNum,
			"name":   label,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, label),
		}
		// Duplicate preview URL into both fields — different Lampa UIs read
		// different keys. (Most modern UIs ignore plugin-side episode art
		// and use TMDB still_path instead, but some forks honour these.)
		var img string
		if v := previews[p.EpisodeID]; v != "" {
			img = v
		} else if seriesPoster != "" {
			img = seriesPoster
		}
		if img != "" {
			row["img"] = img
			row["thumbnail"] = img
		}
		if iso := airdates[p.EpisodeID]; iso != "" {
			row["air_date"] = iso
			row["release_date"] = iso
		}
		episodes = append(episodes, epRow{data: row, label: label, ep: epNum})
	}
	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	sort.Slice(episodes, func(i, j int) bool { return episodes[i].ep < episodes[j].ep })

	rows := make([]map[string]any, 0, len(episodes))
	labels := make([]string, 0, len(episodes))
	episodeNums := make([]int, 0, len(episodes))
	for _, r := range episodes {
		rows = append(rows, r.data)
		labels = append(labels, r.label)
		episodeNums = append(episodeNums, r.ep)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type":  "episode",
			"data":  rows,
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
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, season, episodeNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func sakhtvFirst(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
