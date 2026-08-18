package httpapi

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// kitBindsDisabled reports whether the user-side "Привязки" (source-account
// binding) tab is turned off by the admin. Reads LIVE config via liveConfig so
// the toggle applies without a restart; falls back to the startup cfg the
// handler captured when the server isn't wired yet (tests / early boot).
func kitBindsDisabled(cfg config.Config) bool {
	if serverReady() {
		return liveConfig(config.Config{}).Kit.BindsDisabled
	}
	return cfg.Kit.BindsDisabled
}

// kitBindsGuard is a chi middleware that 403s every request in the group when
// binding is disabled. Applied to the /api/kit/bind/* routes so hiding the tab
// in the UI is backed by real server-side enforcement.
func kitBindsGuard(cfg config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if kitBindsDisabled(cfg) {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "binding disabled"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bkitSessions is set during server setup to enable browser Kit auth fallback.
var bkitSessions *BKitSessionStore

// kitTGTokenStore is set during server setup to enable TG cookie auth in Kit API.
var kitTGTokenStore *tgauth.Store

// kitAuthFromRequest validates the user via Telegram WebApp initData or browser Kit bearer token.
func kitAuthFromRequest(r *http.Request, cfg config.Config) (tgID string, userName string, err error) {
	// 1. Try Telegram WebApp initData.
	initData := r.Header.Get("X-Telegram-Init-Data")
	if initData != "" {
		botToken := cfg.TelegramAuth.BotToken
		if botToken == "" {
			return "", "", fmt.Errorf("bot token not configured")
		}
		user, verr := kit.ValidateInitData(initData, botToken, 24*time.Hour)
		if verr != nil {
			return "", "", fmt.Errorf("auth failed: %w", verr)
		}
		return strconv.FormatInt(user.ID, 10), strings.TrimSpace(user.FirstName + " " + user.LastName), nil
	}

	// 2. Try browser Kit bearer token.
	if bkitSessions != nil {
		bearer := r.Header.Get("Authorization")
		token := strings.TrimPrefix(bearer, "Bearer ")
		if token == "" || token == bearer {
			// Also check query param for simple link-based auth.
			token = r.URL.Query().Get("bkit_token")
		}
		if token != "" {
			sess, ok := bkitSessions.Lookup(token)
			if ok {
				return "bkit:" + sess.ID, sess.Name, nil
			}
			return "", "", fmt.Errorf("invalid browser kit token")
		}
	}

	// 3. Try TG auth cookie (user already logged in via browser).
	if kitTGTokenStore != nil {
		if cookie, cerr := r.Cookie("lampac_token"); cerr == nil && cookie.Value != "" {
			if approved, ok := kitTGTokenStore.Lookup(cookie.Value); ok {
				id := strconv.FormatInt(approved.TelegramID, 10)
				return id, approved.TGUsername, nil
			}
		}
	}

	return "", "", fmt.Errorf("missing authentication")
}

// kitConfigGetHandler returns the user's kit config as JSON.
// GET /api/kit/config
func kitConfigGetHandler(store *kit.Store, cfg config.Config, langStore *tgauth.LangStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		data, err := store.Load(tgID)
		if err != nil {
			log.Warn().Err(err).Str("tg_id", tgID).Msg("kit: load config failed")
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if data == nil {
			data = make(map[string]stdjson.RawMessage)
		}

		// Inject user's bot language preference
		if langStore != nil {
			if id, err := strconv.ParseInt(tgID, 10, 64); err == nil {
				if lang, ok := langStore.Get(id); ok {
					data["_lang"] = stdjson.RawMessage(`"` + string(lang) + `"`)
				}
			}
		}

		// Inject kit hidden services list for frontend filtering.
		if len(cfg.Kit.HiddenServices) > 0 {
			if hs, err := stdjson.Marshal(cfg.Kit.HiddenServices); err == nil {
				data["_kitHiddenServices"] = stdjson.RawMessage(hs)
			}
		}

		// Inject the "Привязки" tab kill-switch. Read from LIVE config so the
		// admin toggle takes effect on the next page load without a restart
		// (the handler captured cfg at startup). When set, /kit and /bkit hide
		// the binds tab and the /api/kit/bind/* routes 403 (see registerKitRoutes).
		if kitBindsDisabled(cfg) {
			data["_bindsDisabled"] = stdjson.RawMessage("true")
		}

		// Re-serialize the map to get clean JSON.
		out, err := json.Marshal(data)
		if err != nil {
			_, _ = w.Write([]byte("{}"))
			return
		}
		_, _ = w.Write(out)
	}
}

// kitConfigSaveHandler saves the user's kit config from JSON body.
// POST /api/kit/config
func kitConfigSaveHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024)) // 512KB max
		if err != nil {
			http.Error(w, `{"error":"read body failed"}`, http.StatusBadRequest)
			return
		}

		// Defense-in-depth: drop _balancerVisibility entries that the user's
		// group denies or admin has disabled. Request-time checks in /lite/*
		// already enforce these — this just prevents stale "true" flags from
		// piling up in the saved config (and from being silently re-honored if
		// admin policy later relaxes).
		body = sanitizeKitBalancerVisibility(r, tgID, body)

		if err := store.Save(tgID, body); err != nil {
			log.Warn().Err(err).Str("tg_id", tgID).Msg("kit: save config failed")
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"success":true}`))
	}
}

// sanitizeKitBalancerVisibility strips entries from the saved
// _balancerVisibility map that the caller is not entitled to enable:
//   - balancers admin-disabled (config enable=false or healthcheck auto-disabled)
//   - balancers denied by the caller's user group
//
// Returns the (possibly rewritten) body. On any parse error or when there is no
// group store configured, the input is returned unchanged — request-time
// enforcement in /lite/* is the actual gatekeeper; this sanitize step exists
// purely to keep the saved prefs consistent with what the user can actually use.
func sanitizeKitBalancerVisibility(r *http.Request, tgID string, body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	var root map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal(body, &root); err != nil {
		return body
	}
	raw, ok := root["_balancerVisibility"]
	if !ok || len(raw) == 0 {
		return body
	}
	var vis map[string]bool
	if err := stdjson.Unmarshal(raw, &vis); err != nil {
		return body
	}
	if len(vis) == 0 {
		return body
	}

	group := resolveUserGroupForKit(r, tgID)

	changed := false
	for key, val := range vis {
		if !val {
			continue // an explicit false just hides — always allowed
		}
		canon := tgauth.CanonicalPluginKey(key)
		if canon == "" {
			continue
		}
		if isBalancerDisabled(canon) {
			delete(vis, key)
			changed = true
			continue
		}
		if group != nil && !group.BalancerAllowed(canon) {
			delete(vis, key)
			changed = true
		}
	}
	if !changed {
		return body
	}

	if len(vis) == 0 {
		delete(root, "_balancerVisibility")
	} else {
		newRaw, err := stdjson.Marshal(vis)
		if err != nil {
			return body
		}
		root["_balancerVisibility"] = newRaw
	}
	out, err := stdjson.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

// resolveUserGroupForKit resolves the user's group for kit-save validation.
// Tries every credential source the request might carry (same set as
// resolveUserGroup: lampac_token cookie, _lampac_auth cookie, ?token= URL
// param), and additionally walks the bearer-auth path (TG numeric ID from
// kitAuthFromRequest — for initData / bkit callers that don't carry a token
// cookie). Falls back to the default group so anonymous-cookie / browser-kit
// callers are still subject to default-group restrictions.
func resolveUserGroupForKit(r *http.Request, tgID string) *tgauth.UserGroup {
	if groupStoreRef == nil {
		return nil
	}
	if tgTokenStoreRef != nil {
		// Reuse the same token-extraction logic as request-time
		// resolveUserGroup so kit save validation stays in lock-step with
		// what the /lite/* gate sees.
		// EffectiveGroupID applies the premium overlay: while
		// PremiumUntil is active the user sees the premium tier; after
		// it expires they fall back to their base GroupID automatically.
		premium := currentPremiumGroupID()
		for _, tok := range collectLampacTokenCandidates(r) {
			if approved, ok := tgTokenStoreRef.Lookup(tok); ok && approved != nil {
				g, _ := groupStoreRef.Get(approved.EffectiveGroupID(premium))
				return &g
			}
		}
		// Bearer/initData path — look up by TG numeric ID.
		if !strings.HasPrefix(tgID, "bkit:") {
			if id, perr := strconv.ParseInt(tgID, 10, 64); perr == nil {
				if approved := tgTokenStoreRef.FindByTelegramID(id); approved != nil {
					g, _ := groupStoreRef.Get(approved.EffectiveGroupID(premium))
					return &g
				}
			}
		}
	}
	// Last resort: default group restrictions still apply.
	g := groupStoreRef.GetDefault()
	return &g
}

// kitProfileHandler returns user profile information.
// GET /api/kit/profile
func kitProfileHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, userName, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		type deviceRow struct {
			UID      string `json:"uid"`
			Label    string `json:"label"`
			LastSeen string `json:"last_seen"`
		}
		type profileResp struct {
			Name        string      `json:"name"`
			TelegramID  int64       `json:"telegram_id"`
			CreatedAt   string      `json:"created_at"`
			ExpiresAt   string      `json:"expires_at"`
			DaysLeft    int         `json:"days_left"`
			Expired     bool        `json:"expired"`
			GroupID     string      `json:"group_id"`
			GroupName   string      `json:"group_name"`
			Devices     []deviceRow `json:"devices"`
			DeviceCount int         `json:"device_count"`
			MaxDevices  int         `json:"max_devices"`
		}

		resp := profileResp{
			Name:    userName,
			Devices: []deviceRow{},
		}

		// Lookup full token data from tgauth store.
		if kitTGTokenStore != nil {
			var approved *tgauth.ApprovedToken

			// Try by TG ID first.
			if !strings.HasPrefix(tgID, "bkit:") {
				if id, parseErr := strconv.ParseInt(tgID, 10, 64); parseErr == nil {
					approved = kitTGTokenStore.FindByTelegramID(id)
				}
			}

			// Try by cookie token.
			if approved == nil {
				if cookie, cerr := r.Cookie("lampac_token"); cerr == nil && cookie.Value != "" {
					if a, ok := kitTGTokenStore.Lookup(cookie.Value); ok {
						approved = a
					}
				}
			}

			if approved != nil {
				resp.TelegramID = approved.TelegramID
				resp.CreatedAt = approved.CreatedAt.Format("2006-01-02")
				resp.ExpiresAt = approved.ExpiresAt.Format("2006-01-02")
				resp.Expired = time.Now().UTC().After(approved.ExpiresAt)
				resp.MaxDevices = approved.MaxDevices
				// Report the EFFECTIVE group (premium overlay > base)
				// so the Lampa client knows the actual current tier.
				resp.GroupID = approved.EffectiveGroupID(currentPremiumGroupID())
				resp.DeviceCount = len(approved.Devices)

				daysLeft := int(time.Until(approved.ExpiresAt).Hours() / 24)
				if daysLeft < 0 {
					daysLeft = 0
				}
				resp.DaysLeft = daysLeft

				if resp.Name == "" {
					resp.Name = approved.TGUsername
				}

				for _, d := range approved.Devices {
					resp.Devices = append(resp.Devices, deviceRow{
						UID:      d.UID,
						Label:    d.Label,
						LastSeen: d.LastSeen.Format("2006-01-02 15:04"),
					})
				}

				// Resolve group name from the EFFECTIVE group id so the
				// label shown to the user matches their actual tier.
				if groupStoreRef != nil {
					gid := approved.EffectiveGroupID(currentPremiumGroupID())
					if gid == "" {
						g := groupStoreRef.GetDefault()
						resp.GroupID = g.ID
						resp.GroupName = g.Name
					} else if g, ok := groupStoreRef.Get(gid); ok {
						resp.GroupName = g.Name
					} else {
						resp.GroupName = gid
					}
				}
			}
		}

		out, _ := stdjson.Marshal(resp)
		_, _ = w.Write(out)
	}
}

// kitDeleteBindHandler removes a service binding from the user's kit config.
// DELETE /api/kit/bind/{service}
func kitDeleteBindHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		// Extract service from path: /api/kit/bind/{service}
		path := strings.TrimPrefix(r.URL.Path, "/api/kit/bind/")
		service := strings.TrimRight(path, "/")

		sectionKey := kitServiceToSection(service)
		if sectionKey == "" {
			http.Error(w, `{"error":"unknown service"}`, http.StatusBadRequest)
			return
		}

		if err := store.DeleteSection(tgID, sectionKey); err != nil {
			log.Warn().Err(err).Str("tg_id", tgID).Str("service", service).Msg("kit: delete bind failed")
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"success":true}`))
	}
}

// kitBalancersHandler returns the grouped list of all known balancers with global enable status.
// GET /api/kit/balancers
func kitBalancersHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		mergedConf := loadMergedConf()

		// Load per-user kit config to check user-bound tokens.
		userCfg, _ := store.Load(tgID)

		type groupInfo struct {
			Key   string `json:"key"`
			Label string `json:"label"`
			Icon  string `json:"icon"`
		}
		type balancerInfo struct {
			Key           string `json:"key"`
			Name          string `json:"name"`
			Group         string `json:"group"`
			Quality       string `json:"quality,omitempty"`
			GlobalEnabled bool   `json:"globalEnabled"`
			UserBound     bool   `json:"userBound,omitempty"`
		}

		groups := make([]groupInfo, 0, len(balancerGroupOrder))
		for _, g := range balancerGroupOrder {
			groups = append(groups, groupInfo{Key: g.Key, Label: g.Label, Icon: g.Icon})
		}

		balancers := make([]balancerInfo, 0, len(knownBalancers))
		for _, name := range knownBalancers {
			key := PluginKeyFor(name)
			group := balancerGroupMap[name]
			if group == "" {
				group = "other"
			}
			quality := pluginQualityBadgeGet(key)

			// Check global enable from merged config.
			globalEnabled := true
			if section, ok := mergedConf[name].(map[string]any); ok {
				if e, ok := section["enable"]; ok {
					if b, ok := e.(bool); ok {
						globalEnabled = b
					}
				}
			}

			// Check if user has a personal token/cookie bound for this balancer.
			userBound := false
			if userCfg != nil {
				if raw, exists := userCfg[name]; exists && len(raw) > 0 {
					var sec struct {
						Enable bool   `json:"enable"`
						Token  string `json:"token"`
						Cookie string `json:"cookie"`
					}
					if stdjson.Unmarshal(raw, &sec) == nil && sec.Enable && (strings.TrimSpace(sec.Token) != "" || strings.TrimSpace(sec.Cookie) != "") {
						userBound = true
					}
				}
			}

			balancers = append(balancers, balancerInfo{
				Key:           key,
				Name:          name,
				Group:         group,
				Quality:       quality,
				GlobalEnabled: globalEnabled,
				UserBound:     userBound,
			})
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"groups":    groups,
			"balancers": balancers,
		})
	}
}

// kitServiceToSection maps URL service name to kit config section key.
func kitServiceToSection(service string) string {
	switch strings.ToLower(service) {
	case "filmix":
		return "Filmix"
	case "kinopub":
		return "KinoPub"
	case "rezka":
		return "Rezka"
	case "rezkaprem":
		return "RezkaPrem"
	case "vokino":
		return "VoKino"
	case "getstv":
		return "GetsTV"
	case "iptvonline":
		return "IptvOnline"
	case "collaps":
		return "Collaps"
	case "videoseed":
		return "Videoseed"
	case "mirage":
		return "Mirage"
	default:
		return ""
	}
}

// ---- Sync-profile CRUD for the TG WebApp ----
//
// These endpoints mirror /api/profile/owned/* but accept the Kit auth
// modes (X-Telegram-Init-Data header / bkit Bearer / lampac_token cookie).
// The TG WebApp at /kit talks to /api/kit/* exclusively because that's
// where `api()` in kit_page.go is rooted — duplicating the routes under
// the Kit prefix is the cleanest way to keep that contract.
//
// Auth pivot: kitAuthFromRequest returns tgID as a string ("12345" or
// "bkit:NN"). For sync-profile ownership we only honor real TG accounts
// — browser-kit sessions can't own profiles because they have no durable
// Telegram identity to attribute mutations to.
//
// All handlers reach into the package-level profileStoreRef (set in
// server.New). When the store isn't wired the routes return 503, same
// shape as /api/profile/*.

func kitProfileTGOwner(r *http.Request, cfg config.Config) (int64, error) {
	tgIDStr, _, err := kitAuthFromRequest(r, cfg)
	if err != nil {
		return 0, err
	}
	if strings.HasPrefix(tgIDStr, "bkit:") {
		return 0, fmt.Errorf("bkit session cannot own sync profiles")
	}
	id, err := strconv.ParseInt(tgIDStr, 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid tg id")
	}
	return id, nil
}

func kitProfileOwnedListHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "profile_store_disabled"})
			return
		}
		tgID, err := kitProfileTGOwner(r, cfg)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "tg_required"})
			return
		}
		list := profileStoreRef.ListByOwnerTG(tgID)
		out := make([]map[string]any, 0, len(list))
		for _, p := range list {
			out = append(out, map[string]any{
				"id":             p.ID,
				"username":       p.Username,
				"has_pin":        p.PINHash != "",
				"pin_updated_at": p.PINUpdatedAt,
				"created_at":     p.CreatedAt,
				"last_login_at":  p.LastLoginAt,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "profiles": out})
	}
}

func kitProfileOwnedCreateHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "profile_store_disabled"})
			return
		}
		tgID, err := kitProfileTGOwner(r, cfg)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "tg_required"})
			return
		}
		var req struct {
			Username string `json:"username"`
			PIN      string `json:"pin"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
			return
		}
		p, err := profileStoreRef.CreateForTGOwner(tgID, req.Username, req.PIN)
		if err != nil {
			code, slug := mapProfileError(err)
			writeJSON(w, code, map[string]any{"error": slug})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"profile_id": p.ID,
			"username":   p.Username,
			"has_pin":    p.PINHash != "",
		})
	}
}

func kitProfileOwnedSetPINHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "profile_store_disabled"})
			return
		}
		tgID, err := kitProfileTGOwner(r, cfg)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "tg_required"})
			return
		}
		var req struct {
			ProfileID string `json:"profile_id"`
			PIN       string `json:"pin"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
			return
		}
		if err := profileStoreRef.SetPIN(req.ProfileID, tgID, req.PIN); err != nil {
			code, slug := mapProfileError(err)
			writeJSON(w, code, map[string]any{"error": slug})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

func kitProfileOwnedDeleteHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "profile_store_disabled"})
			return
		}
		tgID, err := kitProfileTGOwner(r, cfg)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "tg_required"})
			return
		}
		var req struct {
			ProfileID string `json:"profile_id"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
			return
		}
		if err := profileStoreRef.DeleteOwned(req.ProfileID, tgID); err != nil {
			code, slug := mapProfileError(err)
			writeJSON(w, code, map[string]any{"error": slug})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}
