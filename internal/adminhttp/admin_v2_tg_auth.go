package adminhttp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// tgWebAppAuthHandler validates an initData payload from
// Telegram.WebApp.initData and, if the embedded user is a known admin,
// issues the standard lampac_token cookies so subsequent /api/* calls
// authenticate normally.
//
// The validation algorithm is the official one documented at
// https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app:
//
//  1. Parse initData (URL-encoded key=value pairs).
//  2. Pop the `hash` field.
//  3. Sort remaining fields alphabetically as "key=value\n…".
//  4. Compute HMAC-SHA256 with secret_key = HMAC-SHA256("WebAppData", botToken).
//  5. Constant-time compare to the popped hash.
//
// Also enforces `auth_date` is within 24h to reject replay attacks.
func tgWebAppAuthHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type req struct {
			InitData string `json:"init_data"`
		}
		var body req
		if err := stdjson.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.InitData) == "" {
			http.Error(w, "missing init_data", http.StatusBadRequest)
			return
		}

		botToken := ""
		if serverReady() {
			botToken = strings.TrimSpace(liveConfig(config.Config{}).TelegramAuth.BotToken)
		}
		if botToken == "" {
			http.Error(w, "telegram bot not configured", http.StatusServiceUnavailable)
			return
		}

		user, err := verifyTelegramInitData(body.InitData, botToken)
		if err != nil {
			http.Error(w, "invalid init_data: "+err.Error(), http.StatusUnauthorized)
			return
		}

		if adminStore == nil || !adminStore.IsAdmin(user.ID) {
			http.Error(w, "not admin", http.StatusForbidden)
			return
		}

		// We never mint tokens from a WebApp opening — that would let any
		// admin with TG access bypass the normal `/tg/auth` onboarding which
		// binds a device fingerprint. Instead require an existing token; the
		// admin must do the regular `/tg/auth` flow once to provision it.
		existing := store.FindByTelegramID(user.ID)
		if existing == nil {
			http.Error(w, "no active session; please open /tg/auth in a browser first", http.StatusForbidden)
			return
		}
		setAuthCookies(w, r, existing.Token)
		// activeAdminPath is the secret URL prefix where the admin API lives
		// (e.g. cp_AbCdEfGhIj). The TG WebApp bundle is served at /tg-admin/
		// but its API calls need to go through /{adminPath}/api/* — without
		// this hint, the client falls back to /api/* which is unrouted and
		// 404s every request after login. tg-bridge.js stores api_base in
		// window.__adminApiBase before any other API call goes out.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"telegram_id": user.ID,
			"username":    user.Username,
			"is_admin":    true,
			"is_super":    adminStore.IsSuperAdmin(user.ID),
			"admin_path":  activeAdminPath(),
			"api_base":    "/" + activeAdminPath() + "/api",
		})
	}
}

// tgInitDataUser is the subset of fields we care about. Telegram sends a
// JSON object under the `user` key with a richer structure; we only need ID
// and display name.
type tgInitDataUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

// verifyTelegramInitData runs the full HMAC verification and replay-window
// check. Returns the parsed user on success.
//
// Spec: https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app
const tgInitDataMaxAge = 24 * time.Hour

func verifyTelegramInitData(initData, botToken string) (*tgInitDataUser, error) {
	parsed, err := url.ParseQuery(initData)
	if err != nil {
		return nil, err
	}
	hash := parsed.Get("hash")
	if hash == "" {
		return nil, errMissing("hash")
	}
	parsed.Del("hash")

	// auth_date freshness — defend against replay.
	authDate := parsed.Get("auth_date")
	if authDate == "" {
		return nil, errMissing("auth_date")
	}
	ts, err := strconv.ParseInt(authDate, 10, 64)
	if err != nil {
		return nil, err
	}
	if time.Since(time.Unix(ts, 0)) > tgInitDataMaxAge {
		return nil, errStale
	}

	// Build the data-check-string: sort keys, "key=value" joined by \n.
	keys := make([]string, 0, len(parsed))
	for k := range parsed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		lines = append(lines, k+"="+parsed.Get(k))
	}
	dataCheck := strings.Join(lines, "\n")

	// secret_key = HMAC_SHA256("WebAppData", botToken)
	mac := hmac.New(sha256.New, []byte("WebAppData"))
	mac.Write([]byte(botToken))
	secret := mac.Sum(nil)

	// computed = HMAC_SHA256(secret, dataCheck)
	mac2 := hmac.New(sha256.New, secret)
	mac2.Write([]byte(dataCheck))
	computed := hex.EncodeToString(mac2.Sum(nil))

	if !hmac.Equal([]byte(computed), []byte(hash)) {
		return nil, errBadSig
	}

	userRaw := parsed.Get("user")
	if userRaw == "" {
		return nil, errMissing("user")
	}
	var u tgInitDataUser
	if err := stdjson.Unmarshal([]byte(userRaw), &u); err != nil {
		return nil, err
	}
	return &u, nil
}

type tgAuthErr struct{ msg string }

func (e *tgAuthErr) Error() string { return e.msg }

func errMissing(field string) *tgAuthErr { return &tgAuthErr{"missing " + field} }

var (
	errStale  = &tgAuthErr{"auth_date older than 24h"}
	errBadSig = &tgAuthErr{"bad signature"}
)
