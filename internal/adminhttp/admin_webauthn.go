package adminhttp

import (
	"encoding/base64"
	stdjson "encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/rs/zerolog/log"
)

// RegisterWebAuthnRoutes builds the WebAuthn (passkey) instance and wires the
// register/login/credential routes. serverAddr is the host's listen address
// (e.g. ":9118") used to derive the RP origin. No-op when webauthnStore is nil.
// Moved out of the host's admin route setup together with the handlers, so the
// waInstance/waStore/waUser package state stays local to internal/adminhttp.
func RegisterWebAuthnRoutes(router chi.Router, adminPath string, webauthnStore *tgauth.WebAuthnStore, secret []byte, serverAddr string) {
	if webauthnStore == nil {
		return
	}
	waInst, waErr := webauthn.New(&webauthn.Config{
		RPID:          "localhost",
		RPDisplayName: "Alpac Admin",
		RPOrigins:     []string{"http://localhost:" + strings.TrimPrefix(serverAddr, ":")},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationPreferred,
		},
	})
	if waErr != nil {
		log.Error().Err(waErr).Msg("webauthn: failed to create instance")
		return
	}
	waInstance = waInst
	waStore = webauthnStore
	waUser = &tgauth.AdminWebAuthnUser{Store: webauthnStore}
	// Registration (requires full session).
	router.Post("/"+adminPath+"/api/webauthn/register/begin", webauthnRegisterBeginHandler(secret))
	router.Post("/"+adminPath+"/api/webauthn/register/complete", webauthnRegisterCompleteHandler(secret))
	// Authentication (from login page, no session).
	router.Post("/"+adminPath+"/api/webauthn/login/begin", webauthnLoginBeginHandler())
	router.Post("/"+adminPath+"/api/webauthn/login/complete", webauthnLoginCompleteHandler(secret, adminPath))
	// Credential management (requires full session).
	router.Get("/"+adminPath+"/api/webauthn/credentials", webauthnCredentialsListHandler(secret))
	router.Delete("/"+adminPath+"/api/webauthn/credentials", webauthnCredentialDeleteHandler(secret))
	// Public endpoint: check if passkeys exist (for login page conditional UI).
	router.Get("/"+adminPath+"/api/webauthn/has-credentials", webauthnHasCredentialsHandler())
	log.Info().Msg("webauthn: passkey routes registered")
}

// --- Package-level vars set by server.go ---

var (
	waInstance *webauthn.WebAuthn        // WebAuthn RP instance
	waStore    *tgauth.WebAuthnStore     // credential persistence
	waUser     *tgauth.AdminWebAuthnUser // single admin user
)

// --- Challenge session store (in-memory, 2-min TTL) ---

type waSessionEntry struct {
	data    webauthn.SessionData
	created time.Time
}

var (
	waSessions   = map[string]waSessionEntry{} // challenge → session
	waSessionsMu sync.Mutex
)

const waSessionTTL = 2 * time.Minute

func waStoreSession(sd *webauthn.SessionData) {
	waSessionsMu.Lock()
	defer waSessionsMu.Unlock()
	// Cleanup expired entries.
	now := time.Now()
	for k, v := range waSessions {
		if now.Sub(v.created) > waSessionTTL {
			delete(waSessions, k)
		}
	}
	waSessions[sd.Challenge] = waSessionEntry{data: *sd, created: now}
}

func waLoadSession(challenge string) (webauthn.SessionData, bool) {
	waSessionsMu.Lock()
	defer waSessionsMu.Unlock()
	entry, ok := waSessions[challenge]
	if !ok {
		return webauthn.SessionData{}, false
	}
	delete(waSessions, challenge) // one-time use
	if time.Since(entry.created) > waSessionTTL {
		return webauthn.SessionData{}, false
	}
	return entry.data, true
}

// waLoadAnySession returns the most recent session (for cases where client doesn't send challenge back).
// Used because the browser doesn't include the challenge in the response — we track it server-side.
func waLoadAnySession() (webauthn.SessionData, bool) {
	waSessionsMu.Lock()
	defer waSessionsMu.Unlock()
	var best waSessionEntry
	var bestKey string
	for k, v := range waSessions {
		if best.created.IsZero() || v.created.After(best.created) {
			best = v
			bestKey = k
		}
	}
	if bestKey == "" {
		return webauthn.SessionData{}, false
	}
	delete(waSessions, bestKey)
	if time.Since(best.created) > waSessionTTL {
		return webauthn.SessionData{}, false
	}
	return best.data, true
}

// --- Handlers ---

// webauthnRegisterBeginHandler starts the passkey registration ceremony.
// Requires a full admin session (already authenticated via password+TOTP).
func webauthnRegisterBeginHandler(secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFullSession(w, r, secret) {
			return
		}
		if waInstance == nil || waUser == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "WebAuthn not configured"})
			return
		}

		// Exclude already-registered credentials to prevent re-registration.
		existingCreds := waUser.WebAuthnCredentials()
		var excludeList []protocol.CredentialDescriptor
		for _, cred := range existingCreds {
			excludeList = append(excludeList, cred.Descriptor())
		}

		creation, session, err := waInstance.BeginRegistration(
			waUser,
			webauthn.WithExclusions(excludeList),
			webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
		)
		if err != nil {
			log.Error().Err(err).Msg("webauthn: begin registration failed")
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Registration failed"})
			return
		}

		waStoreSession(session)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		stdjson.NewEncoder(w).Encode(creation)
	}
}

// webauthnRegisterCompleteHandler finishes the passkey registration ceremony.
// Requires a full admin session.
func webauthnRegisterCompleteHandler(secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFullSession(w, r, secret) {
			return
		}
		if waInstance == nil || waUser == nil || waStore == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "WebAuthn not configured"})
			return
		}

		session, ok := waLoadAnySession()
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Challenge expired or missing. Retry registration."})
			return
		}

		credential, err := waInstance.FinishRegistration(waUser, session, r)
		if err != nil {
			log.Warn().Err(err).Msg("webauthn: finish registration failed")
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Registration verification failed"})
			return
		}

		// Parse optional display_name from query.
		displayName := r.URL.Query().Get("name")
		if displayName == "" {
			displayName = "Passkey"
		}

		if err := waStore.AddCredential(tgauth.WebAuthnCred{
			CredentialID:    credential.ID,
			PublicKey:       credential.PublicKey,
			AttestationType: credential.AttestationType,
			SignCount:       credential.Authenticator.SignCount,
			AAGUID:          credential.Authenticator.AAGUID,
			DisplayName:     displayName,
			BackupEligible:  credential.Flags.BackupEligible,
			BackupState:     credential.Flags.BackupState,
		}); err != nil {
			log.Error().Err(err).Msg("webauthn: save credential failed")
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Failed to save credential"})
			return
		}

		log.Info().Str("name", displayName).Msg("webauthn: passkey registered")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// webauthnLoginBeginHandler starts the passkey authentication ceremony.
// No session required — this is on the login page.
func webauthnLoginBeginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if waInstance == nil || waUser == nil || waStore == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "WebAuthn not configured"})
			return
		}
		if !waStore.HasCredentials() {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "No passkeys registered"})
			return
		}

		ip := clientIP(r)
		if loginLimiter.isBlocked(ip) {
			log.Warn().Str("ip", ip).Str("auth", "blocked").Msg("admin login: rate limit exceeded (webauthn)")
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"ok": false, "error": "Too many attempts. Wait 15 minutes.",
			})
			return
		}

		assertion, session, err := waInstance.BeginLogin(waUser)
		if err != nil {
			log.Error().Err(err).Msg("webauthn: begin login failed")
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Login failed"})
			return
		}

		waStoreSession(session)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		stdjson.NewEncoder(w).Encode(assertion)
	}
}

// webauthnLoginCompleteHandler finishes the passkey authentication ceremony.
// On success, issues a full admin session cookie.
func webauthnLoginCompleteHandler(secret []byte, adminPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if waInstance == nil || waUser == nil || waStore == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "WebAuthn not configured"})
			return
		}

		ip := clientIP(r)
		if loginLimiter.isBlocked(ip) {
			log.Warn().Str("ip", ip).Str("auth", "blocked").Msg("admin login: rate limit exceeded (webauthn)")
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"ok": false, "error": "Too many attempts. Wait 15 minutes.",
			})
			return
		}

		session, ok := waLoadAnySession()
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Challenge expired. Retry login."})
			return
		}

		credential, err := waInstance.FinishLogin(waUser, session, r)
		if err != nil {
			loginLimiter.recordFailure(ip)
			log.Warn().Str("ip", ip).Str("auth", "failed").Msg("admin login: webauthn assertion failed")
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Authentication failed"})
			return
		}

		// Update sign count for clone detection.
		if credential.Authenticator.CloneWarning {
			log.Warn().Str("ip", ip).Msg("webauthn: possible credential cloning detected")
		}
		_ = waStore.UpdateSignCount(credential.ID, credential.Authenticator.SignCount)

		// Issue full admin session — passkey replaces password+TOTP.
		sessionToken := createAdminSession(secret, false)
		setSessionCookie(w, r, sessionToken, int(sessionFullTTL.Seconds()))

		log.Info().Str("ip", ip).Msg("webauthn: passkey login successful")
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "status": "ok",
			"redirect": "/" + adminPath,
		})
	}
}

// webauthnCredentialsListHandler returns the list of registered passkeys.
// Requires a full admin session.
func webauthnCredentialsListHandler(secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFullSession(w, r, secret) {
			return
		}
		if waStore == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "credentials": []any{}})
			return
		}

		creds := waStore.ListCredentials()
		type credInfo struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			CreatedAt   string `json:"created_at"`
			LastUsedAt  string `json:"last_used_at"`
		}
		var list []credInfo
		for _, c := range creds {
			list = append(list, credInfo{
				ID:          base64.RawURLEncoding.EncodeToString(c.CredentialID),
				DisplayName: c.DisplayName,
				CreatedAt:   c.CreatedAt.Format(time.RFC3339),
				LastUsedAt:  c.LastUsedAt.Format(time.RFC3339),
			})
		}
		if list == nil {
			list = []credInfo{} // ensure JSON array, not null
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "credentials": list})
	}
}

// webauthnCredentialDeleteHandler removes a passkey by ID.
// Requires a full admin session.
func webauthnCredentialDeleteHandler(secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireFullSession(w, r, secret) {
			return
		}
		if waStore == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "WebAuthn not configured"})
			return
		}

		idStr := r.URL.Query().Get("id")
		if idStr == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Missing credential ID"})
			return
		}

		credID, err := base64.RawURLEncoding.DecodeString(idStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid credential ID"})
			return
		}

		if err := waStore.RemoveCredential(credID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Failed to remove credential"})
			return
		}

		log.Info().Str("id", idStr).Msg("webauthn: passkey removed")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// webauthnHasCredentialsHandler returns whether any passkeys are registered.
// No session required — used by login page to show/hide passkey button.
func webauthnHasCredentialsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		has := waStore != nil && waStore.HasCredentials()
		writeJSON(w, http.StatusOK, map[string]any{"has_passkeys": has})
	}
}

// --- Helpers ---

// requireFullSession validates that the request has a non-partial admin session.
func requireFullSession(w http.ResponseWriter, r *http.Request, secret []byte) bool {
	cookie, err := r.Cookie(AdminSessionCookie)
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Not authenticated"})
		return false
	}
	partial, ok := ValidateAdminSession(cookie.Value, secret)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Session expired"})
		return false
	}
	if partial {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "Complete 2FA setup first"})
		return false
	}
	return true
}
