package litesrc

// Мир Кино (ru.mir-kino.pp.ru) — a Jellyfin media server exposed as a Lampac
// balancer. Unlike the HTML-scraping balancers, this talks to Jellyfin's REST
// API: authenticate by login/password → search items (they carry Tmdb/Imdb/
// kinopoisk ProviderIds, so we match by tmdb_id exactly) → resolve a direct
// -play file URL.
//
// Streaming: the account's Policy has EnableVideoPlaybackTranscoding=false, so
// server-side transcoding is NOT available. We therefore emit the raw
// direct-play file (`/videos/{id}/stream?static=true`), which nginx serves with
// Range support. Each MediaSource (release/version) becomes a quality option;
// dubs live as audio tracks inside the file, so the player switches audio
// itself (no separate voice selector needed). The whole file URL — including
// the account api_key — is AES-wrapped through /proxy, so the credential never
// reaches the client.
import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

// tokPrefix returns a short, log-safe prefix of a token (never the full value).
func tokPrefix(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return "(empty)"
	}
	if len(t) > 6 {
		return t[:6] + "…"
	}
	return "…"
}

const (
	mirkinoDefaultHost = "https://ru.mir-kino.pp.ru"
	mirkinoDeviceID    = "lampac-go"
	mirkinoClient      = "Lampac"
	mirkinoDevice      = "lampac-go"
	mirkinoVersion     = "1.0.0"
)

var mirkinoResRe = regexp.MustCompile(`(?i)\b(2160|1080|720|576|480)p?\b`)

type mirkinoChecker struct {
	client    *http.Client
	host      string
	login     string
	password  string
	tokenFile string // data/mirkino.json — persisted token so login runs at most once

	mu            sync.Mutex
	token         string
	userID        string
	authFailUntil time.Time // negative-auth cooldown: don't re-attempt login until this time
}

// mirkinoTokenStore is the on-disk shape of the persisted session.
type mirkinoTokenStore struct {
	Token  string `json:"token"`
	UserID string `json:"user_id"`
}

// mirkinoAuthFailCooldown throttles login retries after a failed auth. Without
// it a wrong/expired credential would trigger a fresh AuthenticateByName on
// EVERY checksearch (one per card × per source) — a burst that can lock out or
// get the account blocked at the Jellyfin/provider side, making the failure
// self-perpetuating.
const mirkinoAuthFailCooldown = 90 * time.Second

// --- Jellyfin API DTOs -----------------------------------------------------

type jfAuthResponse struct {
	AccessToken string `json:"AccessToken"`
	User        struct {
		ID string `json:"Id"`
	} `json:"User"`
}

type jfStream struct {
	Type         string `json:"Type"` // Video / Audio / Subtitle
	Codec        string `json:"Codec"`
	Height       int    `json:"Height"`
	Language     string `json:"Language"`
	DisplayTitle string `json:"DisplayTitle"`
	Index        int    `json:"Index"`
}

type jfMediaSource struct {
	ID           string     `json:"Id"`
	Name         string     `json:"Name"`
	Container    string     `json:"Container"`
	Size         int64      `json:"Size"`
	MediaStreams []jfStream `json:"MediaStreams"`
}

type jfItem struct {
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	OriginalTitle     string            `json:"OriginalTitle"`
	ProductionYear    int               `json:"ProductionYear"`
	Type              string            `json:"Type"` // Movie / Series / Season / Episode
	ProviderIds       map[string]string `json:"ProviderIds"`
	IndexNumber       *int              `json:"IndexNumber"`
	ParentIndexNumber *int              `json:"ParentIndexNumber"`
	MediaSources      []jfMediaSource   `json:"MediaSources"`
}

type jfItemsResponse struct {
	Items            []jfItem `json:"Items"`
	TotalRecordCount int      `json:"TotalRecordCount"`
}

type jfPlaybackInfo struct {
	MediaSources  []jfMediaSource `json:"MediaSources"`
	PlaySessionId string          `json:"PlaySessionId"`
}

// --- construction ----------------------------------------------------------

func NewMirkinoChecker(cfg config.Config) *mirkinoChecker {
	host := strings.TrimSpace(cfg.Online.Mirkino.Host)
	if host == "" {
		host = mirkinoDefaultHost
	}
	host = strings.TrimRight(host, "/")

	m := &mirkinoChecker{
		client:    httpclient.NewForBalancer("mirkino", 15*time.Second),
		host:      host,
		login:     strings.TrimSpace(cfg.Online.Mirkino.Login),
		password:  cfg.Online.Mirkino.Password,
		tokenFile: filepath.Join(cfg.Compat.RepoRoot, "data", "mirkino.json"),
		token:     strings.TrimSpace(cfg.Online.Mirkino.Token),
		userID:    strings.TrimSpace(cfg.Online.Mirkino.UserID),
	}
	// If the config didn't pin a token, reuse the last one we persisted so the
	// login endpoint (rate-limited) is hit at most once ever.
	fromFile := false
	if m.token == "" {
		if st := m.loadToken(); st.Token != "" {
			m.token = st.Token
			m.userID = st.UserID
			fromFile = true
		}
	}
	log.Info().
		Str("host", m.host).
		Bool("has_login", m.login != "").
		Bool("has_token", m.token != "").
		Str("token", tokPrefix(m.token)).
		Str("user_id", m.userID).
		Bool("token_from_file", fromFile).
		Str("token_file", m.tokenFile).
		Msg("mirkino: init")
	return m
}

func (m *mirkinoChecker) loadToken() mirkinoTokenStore {
	var st mirkinoTokenStore
	data, err := os.ReadFile(m.tokenFile)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

func (m *mirkinoChecker) saveToken(token, userID string) {
	if m.tokenFile == "" || token == "" {
		return
	}
	data, err := json.Marshal(mirkinoTokenStore{Token: token, UserID: userID})
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(m.tokenFile), 0o755)
	_ = os.WriteFile(m.tokenFile, data, 0o600)
}

func (m *mirkinoChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := m.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("mirkino"))
			return
		}
		m.index(w, req, links)
	}
}

// --- auth ------------------------------------------------------------------

func (m *mirkinoChecker) authHeader(token string) string {
	h := fmt.Sprintf(`MediaBrowser Client="%s", Device="%s", DeviceId="%s", Version="%s"`,
		mirkinoClient, mirkinoDevice, mirkinoDeviceID, mirkinoVersion)
	if token != "" {
		h += fmt.Sprintf(`, Token="%s"`, token)
	}
	return h
}

// ensureAuth returns a valid (token, userID) pair. It prefers an existing token
// (from config or a prior persisted/interactive login) and only calls the
// rate-limited AuthenticateByName endpoint when no token is available — or when
// force=true because a token just came back 401. Safe for concurrent use.
func (m *mirkinoChecker) ensureAuth(ctx context.Context, force bool) (string, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Happy path: we already have a token. Jellyfin access tokens are long-lived,
	// so reuse indefinitely; a 401 elsewhere triggers force=true to refresh.
	if !force && m.token != "" {
		if m.userID == "" {
			m.userID = m.resolveUserIDLocked(ctx)
			if m.userID == "" {
				log.Warn().Str("token", tokPrefix(m.token)).Msg("mirkino: auth — have token but /Users/Me did not return user_id (token invalid or blocked from this IP?)")
			}
		}
		if m.userID != "" {
			log.Debug().Str("token", tokPrefix(m.token)).Str("user_id", m.userID).Msg("mirkino: auth — using cached token")
			return m.token, m.userID, true
		}
	}

	// Need to (re)login via credentials. Respect the negative-auth cooldown so a
	// blocked/expired credential doesn't hammer the login endpoint.
	if m.login == "" {
		// No creds to mint a fresh token; fall back to whatever we have.
		if !force && m.token != "" && m.userID != "" {
			return m.token, m.userID, true
		}
		log.Warn().Bool("force", force).Bool("has_token", m.token != "").Msg("mirkino: auth — no login configured and no usable token; returning unauthenticated")
		return "", "", false
	}
	if !m.authFailUntil.IsZero() && time.Now().Before(m.authFailUntil) {
		log.Warn().Time("until", m.authFailUntil).Msg("mirkino: auth — in login cooldown, skipping AuthenticateByName")
		return "", "", false
	}
	log.Info().Bool("force", force).Str("login", m.login).Msg("mirkino: auth — calling AuthenticateByName")

	body, _ := json.Marshal(map[string]string{"Username": m.login, "Pw": m.password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.host+"/Users/AuthenticateByName", strings.NewReader(string(body)))
	if err != nil {
		return "", "", false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization", m.authHeader(""))

	resp, err := m.client.Do(req)
	if err != nil {
		m.authFailUntil = time.Now().Add(mirkinoAuthFailCooldown)
		log.Warn().Err(err).Msg("mirkino: auth — AuthenticateByName transport error (network/geo block?), cooldown 90s")
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		m.authFailUntil = time.Now().Add(mirkinoAuthFailCooldown)
		log.Warn().Int("status", resp.StatusCode).Msg("mirkino: auth — AuthenticateByName rejected (401=login blocked/wrong creds), cooldown 90s. Set token+user_id in config to bypass login")
		return "", "", false
	}

	var auth jfAuthResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&auth); err != nil {
		m.authFailUntil = time.Now().Add(mirkinoAuthFailCooldown)
		log.Warn().Err(err).Msg("mirkino: auth — AuthenticateByName body decode failed, cooldown 90s")
		return "", "", false
	}
	if auth.AccessToken == "" || auth.User.ID == "" {
		m.authFailUntil = time.Now().Add(mirkinoAuthFailCooldown)
		log.Warn().Msg("mirkino: auth — AuthenticateByName returned empty token/userid, cooldown 90s")
		return "", "", false
	}
	log.Info().Str("token", tokPrefix(auth.AccessToken)).Str("user_id", auth.User.ID).Msg("mirkino: auth — login OK, token persisted")

	m.token = auth.AccessToken
	m.userID = auth.User.ID
	m.authFailUntil = time.Time{}
	m.saveToken(m.token, m.userID) // persist so we never log in again
	return m.token, m.userID, true
}

// resolveUserIDLocked fetches the owning user id for the current token via
// /Users/Me. Caller must hold m.mu. Returns "" on failure.
func (m *mirkinoChecker) resolveUserIDLocked(ctx context.Context) string {
	if m.token == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.host+"/Users/Me", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Emby-Authorization", m.authHeader(m.token))
	resp, err := m.client.Do(req)
	if err != nil {
		log.Warn().Err(err).Msg("mirkino: /Users/Me transport error (network/geo block from this IP?)")
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		log.Warn().Int("status", resp.StatusCode).Str("token", tokPrefix(m.token)).Msg("mirkino: /Users/Me rejected — token invalid/expired/blocked from this IP")
		return ""
	}
	var u struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		log.Warn().Err(err).Msg("mirkino: /Users/Me body decode failed")
		return ""
	}
	if u.ID != "" {
		m.saveToken(m.token, u.ID) // cache the resolved pairing
	}
	return u.ID
}

// apiGet performs an authenticated GET against a Jellyfin path and decodes JSON
// into out. Retries once on 401 with a fresh token.
func (m *mirkinoChecker) apiGet(ctx context.Context, path string, query url.Values, out any) bool {
	return m.apiDo(ctx, http.MethodGet, path, query, nil, out, false)
}

func (m *mirkinoChecker) apiPost(ctx context.Context, path string, query url.Values, body []byte, out any) bool {
	return m.apiDo(ctx, http.MethodPost, path, query, body, out, false)
}

func (m *mirkinoChecker) apiDo(ctx context.Context, method, path string, query url.Values, body []byte, out any, retried bool) bool {
	token, _, ok := m.ensureAuth(ctx, false)
	if !ok {
		return false
	}

	u := m.host + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Emby-Authorization", m.authHeader(token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.client.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("method", method).Str("path", path).Msg("mirkino: apiDo transport error — if this is 'connection reset by peer' to 127.0.0.1:400xx, a broken SOCKS5 proxy is mapped to the 'mirkino' balancer; remove it (the server is reachable directly)")
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && !retried {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		log.Warn().Str("path", path).Msg("mirkino: apiDo 401 — refreshing token and retrying once")
		if _, _, ok := m.ensureAuth(ctx, true); !ok {
			return false
		}
		return m.apiDo(ctx, method, path, query, body, out, true)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		log.Warn().Int("status", resp.StatusCode).Str("method", method).Str("path", path).Str("body", string(snippet)).Msg("mirkino: apiDo non-2xx")
		return false
	}

	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return true
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("mirkino: apiDo JSON decode failed")
		return false
	}
	return true
}

// --- search / matching -----------------------------------------------------

func mirkinoProviderID(ids map[string]string, key string) string {
	for k, v := range ids {
		if strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (m *mirkinoChecker) searchItems(ctx context.Context, query string) []jfItem {
	_, userID, ok := m.ensureAuth(ctx, false)
	if !ok || strings.TrimSpace(query) == "" {
		return nil
	}
	q := url.Values{}
	q.Set("searchTerm", query)
	q.Set("IncludeItemTypes", "Movie,Series")
	q.Set("Recursive", "true")
	q.Set("Limit", "40")
	q.Set("Fields", "ProviderIds,ProductionYear,OriginalTitle")

	var res jfItemsResponse
	if !m.apiGet(ctx, "/Users/"+userID+"/Items", q, &res) {
		log.Warn().Str("q", query).Str("user_id", userID).Msg("mirkino: search request failed")
		return nil
	}
	log.Info().Str("q", query).Int("results", len(res.Items)).Msg("mirkino: search ok")
	return res.Items
}

// resolveItem finds the best-matching Jellyfin item for the incoming request.
// Provider-id match (tmdb → imdb → kinopoisk) is authoritative; title+year is
// the fallback for items without provider ids.
func (m *mirkinoChecker) resolveItem(ctx context.Context, q url.Values) *jfItem {
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	tmdb := strings.TrimSpace(q.Get("tmdb_id"))
	if tmdb == "" {
		// capi/minimal clients pass the TMDB id as `id` (not `tmdb_id`); for a
		// TMDB-keyed source that's the strongest match. Exact compare against the
		// Tmdb ProviderId → a non-tmdb `id` simply won't match (no false hit).
		tmdb = strings.TrimSpace(q.Get("id"))
	}
	imdb := strings.TrimSpace(q.Get("imdb_id"))
	kp := strings.TrimSpace(q.Get("kinopoisk_id"))
	if kp == "" {
		kp = strings.TrimSpace(q.Get("kp_id"))
	}

	queries := make([]string, 0, 2)
	if title != "" {
		queries = append(queries, title)
	}
	if originalTitle != "" && normalizeSearchTitle(originalTitle) != normalizeSearchTitle(title) {
		queries = append(queries, originalTitle)
	}

	seen := make(map[string]struct{})
	all := make([]jfItem, 0, 16)
	for _, query := range queries {
		for _, it := range m.searchItems(ctx, query) {
			if it.ID == "" {
				continue
			}
			if _, dup := seen[it.ID]; dup {
				continue
			}
			seen[it.ID] = struct{}{}
			all = append(all, it)
		}
	}
	log.Info().
		Str("title", title).Str("original", originalTitle).Int("year", year).
		Str("tmdb", tmdb).Str("imdb", imdb).Str("kp", kp).
		Int("candidates", len(all)).
		Msg("mirkino: resolveItem — searched")

	if len(all) == 0 {
		log.Warn().Str("title", title).Msg("mirkino: resolveItem — 0 candidates from server (title not in library or auth failed)")
		return nil
	}

	// 1) provider-id match (strongest).
	for i := range all {
		if tmdb != "" && mirkinoProviderID(all[i].ProviderIds, "Tmdb") == tmdb {
			log.Info().Str("by", "tmdb").Str("name", all[i].Name).Str("jid", all[i].ID).Msg("mirkino: resolveItem — matched")
			return &all[i]
		}
	}
	for i := range all {
		if imdb != "" && strings.EqualFold(mirkinoProviderID(all[i].ProviderIds, "Imdb"), imdb) {
			log.Info().Str("by", "imdb").Str("name", all[i].Name).Str("jid", all[i].ID).Msg("mirkino: resolveItem — matched")
			return &all[i]
		}
	}
	for i := range all {
		if kp != "" && mirkinoProviderID(all[i].ProviderIds, "kinopoisk") == kp {
			log.Info().Str("by", "kp").Str("name", all[i].Name).Str("jid", all[i].ID).Msg("mirkino: resolveItem — matched")
			return &all[i]
		}
	}

	// 2) title + year fallback.
	wantTitle := normalizeSearchTitle(title)
	wantOriginal := normalizeSearchTitle(originalTitle)
	for i := range all {
		if year != 0 && all[i].ProductionYear != 0 && all[i].ProductionYear != year {
			continue
		}
		norm := normalizeSearchTitle(all[i].Name)
		normOrig := normalizeSearchTitle(all[i].OriginalTitle)
		if wantTitle != "" && (norm == wantTitle || normOrig == wantTitle) {
			log.Info().Str("by", "title+year").Str("name", all[i].Name).Str("jid", all[i].ID).Msg("mirkino: resolveItem — matched")
			return &all[i]
		}
		if wantOriginal != "" && (norm == wantOriginal || normOrig == wantOriginal) {
			log.Info().Str("by", "origtitle+year").Str("name", all[i].Name).Str("jid", all[i].ID).Msg("mirkino: resolveItem — matched")
			return &all[i]
		}
	}
	names := make([]string, 0, len(all))
	for i := range all {
		if i >= 8 {
			break
		}
		names = append(names, fmt.Sprintf("%s[tmdb=%s,y=%d]", all[i].Name, mirkinoProviderID(all[i].ProviderIds, "Tmdb"), all[i].ProductionYear))
	}
	log.Warn().
		Str("want_tmdb", tmdb).Str("want_title", title).Int("want_year", year).
		Strs("candidates", names).
		Msg("mirkino: resolveItem — NO match among candidates (id mismatch / year mismatch)")
	return nil
}

func (m *mirkinoChecker) checkSearch(req *http.Request) bool {
	// Need either a token or credentials to talk to the server.
	if m.login == "" && m.token == "" {
		return false
	}
	return m.resolveItem(req.Context(), req.URL.Query()) != nil
}

// --- request routing -------------------------------------------------------

func (m *mirkinoChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	log.Info().
		Str("id", q.Get("id")).Str("tmdb_id", q.Get("tmdb_id")).
		Str("imdb_id", q.Get("imdb_id")).Str("kp", q.Get("kinopoisk_id")).
		Str("title", title).Str("orig", originalTitle).Str("year", q.Get("year")).
		Str("s", q.Get("s")).Str("jid", q.Get("jid")).
		Msg("mirkino: index in (raw params from caller/capi)")

	season, seasonSet := getsTVQueryInt(q.Get("s"))
	if !seasonSet {
		season = -1
	}

	// Drill-down into an already-resolved series (jid carried in season links)
	// skips the search round-trip.
	if jid := strings.TrimSpace(q.Get("jid")); jid != "" {
		m.writeSerial(w, req, jid, season, rjson, title, originalTitle, links)
		return
	}

	item := m.resolveItem(req.Context(), q)
	if item == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if strings.EqualFold(item.Type, "Series") {
		m.writeSerial(w, req, item.ID, season, rjson, title, originalTitle, links)
		return
	}
	m.writeMovie(w, req, item.ID, rjson, title, originalTitle, links)
}

// --- movie -----------------------------------------------------------------

func (m *mirkinoChecker) playbackSources(ctx context.Context, itemID string) []jfMediaSource {
	_, userID, ok := m.ensureAuth(ctx, false)
	if !ok {
		return nil
	}
	q := url.Values{}
	q.Set("UserId", userID)
	body, _ := json.Marshal(map[string]any{
		"MaxStreamingBitrate": 400000000,
		"AutoOpenLiveStream":  false,
	})
	var pb jfPlaybackInfo
	if !m.apiPost(ctx, "/Items/"+url.PathEscape(itemID)+"/PlaybackInfo", q, body, &pb) {
		return nil
	}
	return pb.MediaSources
}

func (m *mirkinoChecker) writeMovie(w http.ResponseWriter, req *http.Request, itemID string, rjson bool, title, originalTitle string, links *proxylink.Manager) {
	sources := m.playbackSources(req.Context(), itemID)
	quality, defURL, voice := m.buildStreams(req, itemID, sources, links)
	if defURL == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	row := map[string]any{
		"method":   "play",
		"url":      defURL,
		"stream":   defURL,
		"name":     "По умолчанию",
		"title":    getsTVJoinName(title, originalTitle),
		"quality":  quality,
		"qualitys": quality,
	}
	if voice != "" {
		row["voice_name"] = voice
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
	getsTVAppendMovieHTML(&sb, row, toString(row["name"]), true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// --- series ----------------------------------------------------------------

func (m *mirkinoChecker) writeSerial(w http.ResponseWriter, req *http.Request, seriesID string, season int, rjson bool, title, originalTitle string, links *proxylink.Manager) {
	_, userID, ok := m.ensureAuth(req.Context(), false)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	host := hostFromRequest(req)

	// Season list.
	if season == -1 {
		q := url.Values{}
		q.Set("userId", userID)
		var res jfItemsResponse
		if !m.apiGet(req.Context(), "/Shows/"+url.PathEscape(seriesID)+"/Seasons", q, &res) {
			writeGetsTVEmpty(w, rjson)
			return
		}

		type seasonRow struct {
			num int
			row map[string]any
			lbl string
		}
		list := make([]seasonRow, 0, len(res.Items))
		for _, s := range res.Items {
			n := 0
			if s.IndexNumber != nil {
				n = *s.IndexNumber
			}
			if n < 1 {
				continue
			}
			name := strconv.Itoa(n) + " сезон"
			link := host + "/lite/mirkino?rjson=" + getsTVBool(rjson) +
				"&jid=" + url.QueryEscape(seriesID) +
				"&s=" + strconv.Itoa(n) +
				"&title=" + url.QueryEscape(title) +
				"&original_title=" + url.QueryEscape(originalTitle)
			list = append(list, seasonRow{num: n, lbl: name, row: map[string]any{
				"method": "link",
				"id":     n,
				"url":    link,
				"name":   name,
			}})
		}
		if len(list) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		sort.Slice(list, func(i, j int) bool { return list[i].num < list[j].num })

		if rjson {
			rows := make([]map[string]any, len(list))
			for i, s := range list {
				rows[i] = s.row
			}
			writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": rows})
			return
		}
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		for i, s := range list {
			getsTVAppendSeasonHTML(&sb, s.row, s.lbl, i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Episode list for a season.
	q := url.Values{}
	q.Set("userId", userID)
	q.Set("season", strconv.Itoa(season))
	q.Set("Fields", "MediaSources,MediaStreams")
	var res jfItemsResponse
	if !m.apiGet(req.Context(), "/Shows/"+url.PathEscape(seriesID)+"/Episodes", q, &res) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	baseTitle := getsTVJoinName(title, originalTitle)
	type epRow struct {
		num int
		row map[string]any
		lbl string
	}
	list := make([]epRow, 0, len(res.Items))
	for _, ep := range res.Items {
		// Episodes endpoint can return the whole series when a season has no
		// explicit grouping — keep only the requested season.
		if ep.ParentIndexNumber != nil && *ep.ParentIndexNumber != season {
			continue
		}
		quality, defURL, voice := m.buildStreams(req, ep.ID, ep.MediaSources, links)
		if defURL == "" {
			continue
		}
		num := 0
		if ep.IndexNumber != nil {
			num = *ep.IndexNumber
		}
		label := strconv.Itoa(num) + " серия"
		row := map[string]any{
			"method":   "play",
			"url":      defURL,
			"stream":   defURL,
			"s":        season,
			"e":        num,
			"name":     label,
			"title":    baseTitle + " (" + label + ")",
			"quality":  quality,
			"qualitys": quality,
		}
		if voice != "" {
			row["voice_name"] = voice
		}
		list = append(list, epRow{num: num, row: row, lbl: label})
	}
	if len(list) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].num < list[j].num })

	if rjson {
		rows := make([]map[string]any, len(list))
		for i, e := range list {
			rows[i] = e.row
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": rows})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, e := range list {
		getsTVAppendMovieHTML(&sb, e.row, e.lbl, i == 0, season, e.num)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// --- stream building -------------------------------------------------------

// buildStreams turns a list of Jellyfin MediaSources into a proxied quality
// map ({"1080p": url, ...}), the default (highest-resolution) URL, and a
// human-readable list of audio tracks for the voice_name badge.
func (m *mirkinoChecker) buildStreams(req *http.Request, itemID string, sources []jfMediaSource, links *proxylink.Manager) (map[string]any, string, string) {
	token, _, ok := m.ensureAuth(req.Context(), false)
	if !ok || len(sources) == 0 {
		return nil, "", ""
	}

	type variant struct {
		label  string
		height int
		url    string
	}
	variants := make([]variant, 0, len(sources))
	voiceSeen := make(map[string]struct{})
	voices := make([]string, 0, 4)

	for _, src := range sources {
		if src.ID == "" {
			continue
		}
		height := 0
		for _, s := range src.MediaStreams {
			if strings.EqualFold(s.Type, "Video") && s.Height > height {
				height = s.Height
			}
			if strings.EqualFold(s.Type, "Audio") {
				if lang := mirkinoAudioLabel(s); lang != "" {
					if _, dup := voiceSeen[lang]; !dup {
						voiceSeen[lang] = struct{}{}
						voices = append(voices, lang)
					}
				}
			}
		}
		label := mirkinoQualityLabel(src.Name, height)
		raw := fmt.Sprintf("%s/videos/%s/stream?static=true&mediaSourceId=%s&api_key=%s",
			m.host, url.PathEscape(itemID), url.QueryEscape(src.ID), url.QueryEscape(token))
		proxied := streamProxyURL(req, raw, "mirkino", links)
		variants = append(variants, variant{label: label, height: height, url: proxied})
	}
	if len(variants) == 0 {
		return nil, "", ""
	}

	// Highest resolution first = default.
	sort.SliceStable(variants, func(i, j int) bool { return variants[i].height > variants[j].height })

	quality := make(map[string]any, len(variants))
	for _, v := range variants {
		label := v.label
		// Avoid clobbering when two sources share a resolution label.
		if _, exists := quality[label]; exists {
			label = label + " ·"
			for i := 2; ; i++ {
				cand := fmt.Sprintf("%s%d", v.label, i)
				if _, e := quality[cand]; !e {
					label = cand
					break
				}
			}
		}
		quality[label] = v.url
	}

	return quality, variants[0].url, strings.Join(voices, ", ")
}

func mirkinoQualityLabel(name string, height int) string {
	if height >= 2000 {
		return "2160p"
	}
	if height >= 1000 {
		return "1080p"
	}
	if height >= 700 {
		return "720p"
	}
	if height >= 500 {
		return "480p"
	}
	if height > 0 {
		return strconv.Itoa(height) + "p"
	}
	// Fall back to any resolution hint in the source name.
	if mm := mirkinoResRe.FindStringSubmatch(name); len(mm) > 1 {
		return mm[1] + "p"
	}
	return "auto"
}

func mirkinoAudioLabel(s jfStream) string {
	switch strings.ToLower(strings.TrimSpace(s.Language)) {
	case "rus", "ru":
		return "Русский"
	case "eng", "en":
		return "English"
	case "kor", "ko":
		return "Korean"
	case "jpn", "ja":
		return "Japanese"
	case "ukr", "uk":
		return "Українська"
	case "und", "":
		return ""
	default:
		lang := strings.ToLower(strings.TrimSpace(s.Language))
		if lang == "" {
			return ""
		}
		return strings.ToUpper(lang[:1]) + lang[1:]
	}
}
