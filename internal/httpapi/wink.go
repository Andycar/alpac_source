package httpapi

// wink.go — Wink (Rostelecom "Interactive TV" / Restream/Smotreshka) source.
//
// Protocol reverse-engineered from Wink_ATV_v.1.38.1.apk (the unobfuscated
// `ru.rt.video.app.*` package, esp. IRemoteApi) and confirmed against the live
// API with a Russian residential IP. Bootstrap chain:
//
//	GET  {discovery}/discover                      -> {api_url, img_url, ...}
//	POST {discovery}/itv/devices  {model,...}      -> {uid}            (device, cached forever — rate-limited!)
//	POST {discovery}/itv/sessions {deviceUid}      -> {sessionId}      (anonymous session)
//	POST {api}/api/v2/user/send_sms_code {phone,action:"AUTH"}         (phone login: SMS)
//	POST {api}/api/v2/user/sessions {login,password,loginType}         -> {sessionId} (authenticated)
//
// Auth = a single `session_id` request header (+ User-Agent). No request signing.
// `user/*` calls go to the discovered api node under /api/v2/; `itv/*` + discover
// stay on the discovery host. Streams carry an `isCrypted` flag: false = clear HLS
// (`url`, playable), true = Widevine DASH (`urls.widevine`, NOT playable in Lampa).

import (
	"bytes"
	"context"
	"crypto/tls"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/proxy"
)

// ---------------------------------------------------------------------------
// Bootstrap response models (JSON shapes confirmed against the live API)
// ---------------------------------------------------------------------------

type winkDiscoverResponse struct {
	APIURL       string `json:"api_url"`
	WSURL        string `json:"ws_url"`
	SpyURL       string `json:"spy_url"`
	ImgURL       string `json:"img_url"`
	ProxyURL     string `json:"proxy_url"`
	APKStorgeURL string `json:"apk_storage_url"`
}

type winkDeviceResponse struct {
	UID string `json:"uid"`
}

// winkSessionResponse — the app's Gson uses LOWER_CASE_WITH_UNDERSCORES, so the
// SessionResponse model (unannotated fields sessionId/isRestricted) serializes as
// session_id / is_restricted. (discover, by contrast, uses explicit @SerializedName
// names like api_url — see winkDiscoverResponse.)
type winkSessionResponse struct {
	SessionID    string `json:"session_id"`
	IsRestricted bool   `json:"is_restricted"`
}

// winkError is the platform's error envelope, e.g.
// {"error_code":1000023,"description":"Необходима авторизация",...}.
type winkError struct {
	ErrorCode    int    `json:"error_code"`
	Description  string `json:"description"`
	DebugMessage string `json:"debug_message"`
}

const (
	winkErrSessionMissing = 1000023 // missed session_id header / session expired
	winkErrForbiddenIP    = 6000001 // datacenter IP blocked
	winkErrRateLimited    = 6000002 // "вы слишком часто выполняете это действие"
)

// ---------------------------------------------------------------------------
// Persistent session store
//
// The device UID is the precious, rate-limited resource: itv/devices answers
// 6000002 ("too often") if you re-register, so we register ONCE and persist it.
// The session_id is cheap to recreate, but we cache it too to avoid re-login on
// every restart.
// ---------------------------------------------------------------------------

type winkSessionState struct {
	DeviceUID string `json:"device_uid"`
	SessionID string `json:"session_id"`
	APIURL    string `json:"api_url"`
	Authed    bool   `json:"authed"`
	SavedAt   int64  `json:"saved_at"`
}

type winkSessionStore struct {
	path string
	mu   sync.Mutex
	st   winkSessionState
}

func newWinkSessionStore(repoRoot string) *winkSessionStore {
	dir := filepath.Join(repoRoot, "database")
	return &winkSessionStore{path: filepath.Join(dir, "wink_session.json")}
}

func (s *winkSessionStore) Load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	_ = stdjson.Unmarshal(data, &s.st)
}

func (s *winkSessionStore) Get() winkSessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

func (s *winkSessionStore) Save(st winkSessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.SavedAt = time.Now().Unix()
	s.st = st
	data, err := stdjson.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		log.Warn().Err(err).Msg("wink: failed to persist session store")
	}
}

// ---------------------------------------------------------------------------
// Checker
// ---------------------------------------------------------------------------

type winkChecker struct {
	src           config.WinkSource
	client        *http.Client
	discoveryHost string // https://itv.svc.iptv.rt.ru/api/v2 (no trailing slash)
	ua            string
	store         *winkSessionStore

	mu        sync.Mutex // guards the live session fields below
	apiBase   string     // {api_url}/api/v2 (no trailing slash)
	deviceUID string
	sessionID string
	authed    bool
}

func newWinkChecker(cfg config.Config) *winkChecker {
	src := cfg.Online.Wink

	discovery := strings.TrimRight(strings.TrimSpace(src.DiscoveryHost), "/")
	if discovery == "" {
		discovery = "https://itv.svc.iptv.rt.ru/api/v2"
	}
	ua := strings.TrimSpace(src.UserAgent)
	if ua == "" {
		ua = "Wink/1.38.1 (Android 9; ANDROIDTV)"
	}

	w := &winkChecker{
		src:           src,
		client:        httpclient.NewForBalancer("wink", 25*time.Second),
		discoveryHost: discovery,
		ua:            ua,
		store:         newWinkSessionStore(cfg.Compat.RepoRoot),
	}

	// RU residential SOCKS5 egress. The API blocks datacenter IPs (451), so this
	// is required in practice; "none"/"off"/empty = direct.
	socksAddr := strings.TrimSpace(src.SocksProxy)
	if socksAddr != "" && !strings.EqualFold(socksAddr, "none") && !strings.EqualFold(socksAddr, "off") {
		if dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct); err == nil {
			t := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
			if cd, ok := dialer.(proxy.ContextDialer); ok {
				t.DialContext = cd.DialContext
			}
			w.client = &http.Client{Timeout: 30 * time.Second, Transport: t}
			// Route /proxy/?pl=wink stream fetches through the same RU residential
			// SOCKS5 — Wink CDN URLs are IP-bound to the session's egress.
			httpclient.RegisterProxiedBalancer(socksAddr, []string{"wink"})
			log.Info().Str("socks", socksAddr).Msg("wink: HTTP client + stream proxy using SOCKS5")
		} else {
			log.Warn().Err(err).Str("socks", socksAddr).Msg("wink: failed to create SOCKS5 dialer")
		}
	}

	w.store.Load()
	st := w.store.Get()
	w.deviceUID = st.DeviceUID
	w.sessionID = st.SessionID
	w.apiBase = st.APIURL
	w.authed = st.Authed

	return w
}

// ---------------------------------------------------------------------------
// Low-level HTTP
// ---------------------------------------------------------------------------

// rawRequest performs a single HTTP request with Wink headers. When withSession
// is true the current session_id is attached. Returns body + status code.
func (w *winkChecker) rawRequest(ctx context.Context, method, fullURL string, body []byte, withSession bool) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", w.ua)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if withSession {
		w.mu.Lock()
		sid := w.sessionID
		w.mu.Unlock()
		if sid != "" {
			req.Header.Set("session_id", sid)
		}
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// requestRetry wraps rawRequest with retry on transient 502s (the
// restream-media LB pool has flapping nodes that 502 intermittently).
func (w *winkChecker) requestRetry(ctx context.Context, method, fullURL string, body []byte, withSession bool) ([]byte, int, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		data, code, err := w.rawRequest(ctx, method, fullURL, body, withSession)
		if err != nil {
			lastErr = err
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 600 * time.Millisecond):
			}
			continue
		}
		// 502 or empty body == unhealthy LB node; retry on a fresh connection.
		if code == http.StatusBadGateway || len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "{}" {
			lastErr = fmt.Errorf("wink: transient response (code=%d)", code)
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 600 * time.Millisecond):
			}
			continue
		}
		return data, code, nil
	}
	return nil, 0, lastErr
}

// parseWinkError extracts the platform error envelope, if present.
func parseWinkError(data []byte) *winkError {
	var e winkError
	if err := stdjson.Unmarshal(data, &e); err != nil || e.ErrorCode == 0 {
		return nil
	}
	return &e
}

// ---------------------------------------------------------------------------
// URL helpers
// ---------------------------------------------------------------------------

// itvURL builds a URL against the discovery host (discover, itv/devices,
// itv/sessions, itv/lifecheck).
func (w *winkChecker) itvURL(path string) string {
	return w.discoveryHost + "/" + strings.TrimPrefix(path, "/")
}

// userURL builds a URL against the discovered api node (all user/* endpoints).
func (w *winkChecker) userURL(path string) string {
	w.mu.Lock()
	base := w.apiBase
	w.mu.Unlock()
	return base + "/" + strings.TrimPrefix(path, "/")
}

// ---------------------------------------------------------------------------
// Bootstrap steps
// ---------------------------------------------------------------------------

func (w *winkChecker) discover(ctx context.Context) (string, error) {
	data, code, err := w.requestRetry(ctx, http.MethodGet, w.itvURL("discover"), nil, false)
	if err != nil {
		return "", fmt.Errorf("wink: discover: %w", err)
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("wink: discover http %d: %s", code, snippet(data))
	}
	var d winkDiscoverResponse
	if err := stdjson.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("wink: discover decode: %w", err)
	}
	if d.APIURL == "" {
		return "", fmt.Errorf("wink: discover returned empty api_url")
	}
	apiBase := strings.TrimRight(d.APIURL, "/") + "/api/v2"
	w.mu.Lock()
	w.apiBase = apiBase
	w.mu.Unlock()
	return apiBase, nil
}

// registerDevice registers a new device and returns its UID. Rate-limited
// (6000002) — callers must cache the result and never re-register unnecessarily.
func (w *winkChecker) registerDevice(ctx context.Context) (string, error) {
	platform := orDefault(w.src.Platform, "ANDROID")
	devType := orDefault(w.src.DeviceType, "ANDROIDTV")
	model := orDefault(w.src.DeviceModel, "Nexus 9")
	aid := randomAndroidID()
	body, _ := stdjson.Marshal(map[string]string{
		"model":        model,
		"platform":     platform,
		"realUid":      aid,
		"type":         devType,
		"vendor":       "google",
		"terminalName": "google " + model,
		"sn":           aid,
	})
	data, code, err := w.requestRetry(ctx, http.MethodPost, w.itvURL("itv/devices"), body, false)
	if err != nil {
		return "", fmt.Errorf("wink: register device: %w", err)
	}
	if e := parseWinkError(data); e != nil {
		if e.ErrorCode == winkErrRateLimited {
			return "", fmt.Errorf("wink: device registration rate-limited (6000002); reuse the cached uid and retry later")
		}
		return "", fmt.Errorf("wink: register device error %d: %s", e.ErrorCode, e.Description)
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("wink: register device http %d: %s", code, snippet(data))
	}
	var dr winkDeviceResponse
	if err := stdjson.Unmarshal(data, &dr); err != nil || dr.UID == "" {
		return "", fmt.Errorf("wink: register device: no uid in %s", snippet(data))
	}
	return dr.UID, nil
}

// createItvSession opens an anonymous session bound to the device UID.
func (w *winkChecker) createItvSession(ctx context.Context, deviceUID string) (string, error) {
	body, _ := stdjson.Marshal(map[string]string{"deviceUid": deviceUID})
	data, code, err := w.requestRetry(ctx, http.MethodPost, w.itvURL("itv/sessions"), body, false)
	if err != nil {
		return "", fmt.Errorf("wink: itv session: %w", err)
	}
	if e := parseWinkError(data); e != nil {
		return "", fmt.Errorf("wink: itv session error %d: %s", e.ErrorCode, e.Description)
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("wink: itv session http %d: %s", code, snippet(data))
	}
	var sr winkSessionResponse
	if err := stdjson.Unmarshal(data, &sr); err != nil || sr.SessionID == "" {
		return "", fmt.Errorf("wink: itv session: no sessionId in %s", snippet(data))
	}
	return sr.SessionID, nil
}

// sendSmsCode triggers an SMS code for phone login (action defaults to AUTH).
// Requires an active (anonymous) session.
func (w *winkChecker) sendSmsCode(ctx context.Context, phone string) ([]byte, error) {
	body, _ := stdjson.Marshal(map[string]string{"phone": normalizePhone(phone), "action": "AUTH"})
	data, code, err := w.requestRetry(ctx, http.MethodPost, w.userURL("user/send_sms_code"), body, true)
	if err != nil {
		return nil, fmt.Errorf("wink: send_sms_code: %w", err)
	}
	if e := parseWinkError(data); e != nil {
		return data, fmt.Errorf("wink: send_sms_code error %d: %s", e.ErrorCode, e.Description)
	}
	if code != http.StatusOK {
		return data, fmt.Errorf("wink: send_sms_code http %d: %s", code, snippet(data))
	}
	return data, nil
}

// createUserSession logs in (createUserSession). For phone login `password` is
// the SMS code; for email/login it is the account password. Returns the new
// authenticated session_id.
func (w *winkChecker) createUserSession(ctx context.Context, login, password, loginType string) (string, error) {
	if loginType == "" {
		loginType = detectLoginType(login)
	}
	reqLogin := login
	if loginType == "PHONE" {
		reqLogin = normalizePhone(login)
	}
	body, _ := stdjson.Marshal(map[string]string{
		"login":     reqLogin,
		"password":  password,
		"loginType": loginType,
	})
	data, code, err := w.requestRetry(ctx, http.MethodPost, w.userURL("user/sessions"), body, true)
	if err != nil {
		return "", fmt.Errorf("wink: user session: %w", err)
	}
	if e := parseWinkError(data); e != nil {
		return "", fmt.Errorf("wink: user session error %d: %s", e.ErrorCode, e.Description)
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("wink: user session http %d: %s", code, snippet(data))
	}
	var sr winkSessionResponse
	if err := stdjson.Unmarshal(data, &sr); err != nil || sr.SessionID == "" {
		return "", fmt.Errorf("wink: user session: no sessionId in %s", snippet(data))
	}
	return sr.SessionID, nil
}

// ---------------------------------------------------------------------------
// Session lifecycle
// ---------------------------------------------------------------------------

// ensureDevice guarantees a registered device UID (cached forever once obtained).
func (w *winkChecker) ensureDevice(ctx context.Context) (string, error) {
	w.mu.Lock()
	uid := w.deviceUID
	w.mu.Unlock()
	if uid != "" {
		return uid, nil
	}
	uid, err := w.registerDevice(ctx)
	if err != nil {
		return "", err
	}
	w.mu.Lock()
	w.deviceUID = uid
	w.mu.Unlock()
	w.persist()
	return uid, nil
}

// ensureSession guarantees a usable session. It (re)discovers the api node,
// opens an anonymous session, and — when static credentials are configured —
// logs in. Phone+SMS accounts (no static password) stay anonymous here and must
// be activated out-of-band via Activate(); the cached authenticated session is
// reused on subsequent boots.
func (w *winkChecker) ensureSession(ctx context.Context) error {
	w.mu.Lock()
	haveSession := w.sessionID != "" && w.apiBase != ""
	w.mu.Unlock()

	if haveSession && w.sessionValid(ctx) {
		return nil
	}

	if _, err := w.discover(ctx); err != nil {
		return err
	}
	uid, err := w.ensureDevice(ctx)
	if err != nil {
		return err
	}
	anon, err := w.createItvSession(ctx, uid)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.sessionID = anon
	w.authed = false
	w.mu.Unlock()

	// Static-credential login (email/password, or accounts with a real password).
	login := strings.TrimSpace(w.src.Login)
	password := w.src.Password
	if login != "" && password != "" {
		authSID, err := w.createUserSession(ctx, login, password, strings.ToUpper(strings.TrimSpace(w.src.LoginType)))
		if err != nil {
			log.Warn().Err(err).Msg("wink: static login failed; continuing with anonymous session")
		} else {
			w.mu.Lock()
			w.sessionID = authSID
			w.authed = true
			w.mu.Unlock()
		}
	}
	w.persist()
	return nil
}

// sessionValid does a cheap authenticated probe (system_info). Returns false on
// a session-missing error so the caller re-bootstraps.
func (w *winkChecker) sessionValid(ctx context.Context) bool {
	data, code, err := w.rawRequest(ctx, http.MethodGet, w.userURL("user/system_info"), nil, true)
	if err != nil {
		return false
	}
	if e := parseWinkError(data); e != nil && e.ErrorCode == winkErrSessionMissing {
		return false
	}
	return code == http.StatusOK
}

// persist writes the current session fields to the on-disk store.
func (w *winkChecker) persist() {
	w.mu.Lock()
	st := winkSessionState{
		DeviceUID: w.deviceUID,
		SessionID: w.sessionID,
		APIURL:    w.apiBase,
		Authed:    w.authed,
	}
	w.mu.Unlock()
	w.store.Save(st)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func snippet(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

// normalizePhone mirrors the app: strip non-digits, map a leading 8 to 7.
func normalizePhone(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if strings.HasPrefix(digits, "8") {
		digits = "7" + digits[1:]
	}
	return digits
}

// detectLoginType mirrors the app: an '@' => EMAIL, otherwise PHONE.
func detectLoginType(login string) string {
	if strings.Contains(login, "@") {
		return "EMAIL"
	}
	return "PHONE"
}

// randomAndroidID returns a 16-hex-char value resembling Settings.Secure.ANDROID_ID.
func randomAndroidID() string {
	// Time-seeded, non-cryptographic — uniqueness across registrations is enough.
	n := time.Now().UnixNano()
	return strconv.FormatUint(uint64(n)&0xffffffffffffffff, 16) + strconv.FormatInt(n%0x10000, 16)
}

// winkQuery builds a query string from a param map (used by the VOD balancer).
func winkQuery(params map[string]string) string {
	vals := url.Values{}
	for k, v := range params {
		vals.Set(k, v)
	}
	return vals.Encode()
}

// ---------------------------------------------------------------------------
// Shared singleton — one checker for both the VOD balancer and the TV routes,
// so the device UID / session live in a single place (and one disk store).
// ---------------------------------------------------------------------------

var (
	winkOnce     sync.Once
	winkInstance *winkChecker
)

func sharedWinkChecker(cfg config.Config) *winkChecker {
	winkOnce.Do(func() { winkInstance = newWinkChecker(cfg) })
	return winkInstance
}
