package httpapi

// mirror_origin.go — the ORIGIN ("beta") side of mirror mode.
//
// Two server-to-server endpoints let a thin mirror edge delegate its auth
// control-plane here, while serving catalog + /proxy video locally:
//
//   GET  /api/auth/validate   — layer 1. Given ?token= or ?uid=, return the
//                               resolved user identity so the mirror's auth
//                               middleware can populate *User in context
//                               (whoami, premium, kit, attribution).
//
//   POST /api/cluster/authgate — layer 2. Given the forwarded request
//                               attributes, run the REAL tgAuthGateMiddleware
//                               against this origin's store (single writer of
//                               device-binding / pending codes / bans) and
//                               return the verdict for the mirror to replay.
//
// Both self-authenticate via isMirrorOriginRequest (the [mirror]/[sync]
// shared secret). They are on gatePreAuthAllowed's allowlist so the gate
// doesn't block them on the origin.

import (
	"crypto/subtle"
	"encoding/base64"
	stdjson "encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/tgauth"
)

// isMirrorOriginRequest authenticates a mirror→origin control call. Accepts a
// localhost request, or a request carrying the shared secret in the
// "localrequest" (or "X-Mirror-Key") header matching either [sync] api_passwd
// or [mirror] api_passwd. Constant-time compared.
func isMirrorOriginRequest(r *http.Request) bool {
	ip := clientIP(r)
	if ip == "127.0.0.1" || ip == "::1" || ip == "localhost" {
		return true
	}
	key := strings.TrimSpace(r.Header.Get("localrequest"))
	if key == "" {
		key = strings.TrimSpace(r.Header.Get("X-Mirror-Key"))
	}
	if key == "" || !serverReady() {
		return false
	}
	cfg := liveConfig(config.Config{})
	for _, secret := range []string{strings.TrimSpace(cfg.Sync.APIPasswd), strings.TrimSpace(cfg.Mirror.APIPasswd)} {
		if secret != "" && subtle.ConstantTimeCompare([]byte(key), []byte(secret)) == 1 {
			return true
		}
	}
	return false
}

// mirrorValidateResp is the JSON returned by /api/auth/validate.
type mirrorValidateResp struct {
	OK           bool   `json:"ok"`
	Type         string `json:"type,omitempty"` // "tg"
	TGID         int64  `json:"tg_id,omitempty"`
	Username     string `json:"username,omitempty"`
	Expires      string `json:"expires,omitempty"` // RFC3339
	PremiumUntil string `json:"premium_until,omitempty"`
	Group        string `json:"group,omitempty"`
	// GroupDef is the user's EFFECTIVE group (premium overlay already applied)
	// — its balancer allow/deny rules. The mirror filters /lite/events by this
	// so source visibility matches the origin's per-user groups. nil = default
	// group / unrestricted (Фаза 5).
	GroupDef *tgauth.UserGroup `json:"group_def,omitempty"`
	// KitVisibility is the user's PERSONAL balancer-visibility map
	// (_balancerVisibility) — the sources they hid for themselves out of those
	// their group allows. The mirror injects it into the kit context so the
	// picker matches the origin exactly (Фаза 6). Only the visibility map is
	// sent — sensitive per-user kit fields stay on the origin.
	KitVisibility map[string]bool `json:"kit_visibility,omitempty"`
}

// mirrorOriginValidateHandler resolves a token or device UID to a user
// identity. store may be nil on a non-origin instance — then it always
// returns ok:false (the mirror treats that as "deny", surfacing a config
// error rather than silently authing everyone).
func mirrorOriginValidateHandler(store *tgauth.Store, kitStore *kit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isMirrorOriginRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "forbidden"})
			return
		}
		if store == nil {
			writeJSON(w, http.StatusOK, mirrorValidateResp{OK: false})
			return
		}
		token := strings.TrimSpace(r.URL.Query().Get("token"))
		uid := strings.TrimSpace(r.URL.Query().Get("uid"))

		var (
			tgID int64
			exp  time.Time
			ok   bool
		)
		switch {
		case token != "":
			tgID, exp, ok = store.LookupAuth(token)
		case uid != "":
			tgID, exp, ok = store.LookupAuthByDeviceUID(uid)
			// Recover the token so we can enrich with username/premium/group.
			if ok {
				token = store.FindTokenByDeviceUID(uid)
			}
		}
		if !ok {
			writeJSON(w, http.StatusOK, mirrorValidateResp{OK: false})
			return
		}

		resp := mirrorValidateResp{OK: true, Type: "tg", TGID: tgID}
		if !exp.IsZero() {
			resp.Expires = exp.UTC().Format(time.RFC3339)
		}
		if token != "" {
			if t, found := store.Lookup(token); found && t != nil {
				resp.Username = t.TGUsername
				resp.Group = t.GroupID
				if !t.PremiumUntil.IsZero() {
					resp.PremiumUntil = t.PremiumUntil.UTC().Format(time.RFC3339)
				}
				// Effective group (premium overlay applied) so the mirror can
				// replicate the origin's per-user source visibility (Фаза 5).
				if groupStoreRef != nil {
					if g, ok := groupStoreRef.Get(t.EffectiveGroupID(currentPremiumGroupID())); ok {
						resp.GroupDef = &g
					}
				}
				// Personal kit visibility (Фаза 6) — only the _balancerVisibility
				// map, not the whole (possibly sensitive) kit config.
				if kitStore != nil && t.TelegramID != 0 {
					if kitCfg, err := kitStore.Load(strconv.FormatInt(t.TelegramID, 10)); err == nil && kitCfg != nil {
						if raw, ok := kitCfg["_balancerVisibility"]; ok {
							var vis map[string]bool
							if stdjson.Unmarshal(raw, &vis) == nil && len(vis) > 0 {
								resp.KitVisibility = vis
							}
						}
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// mirrorGateRequest is the request body of /api/cluster/authgate: the subset
// of the original client request the gate decision depends on.
type mirrorGateRequest struct {
	Path           string `json:"path"`
	RawQuery       string `json:"raw_query"`
	UserAgent      string `json:"user_agent"`
	Accept         string `json:"accept"`
	XRequestedWith string `json:"x_requested_with"`
	ClientIP       string `json:"client_ip"`
	Token          string `json:"token"`
}

// mirrorGateVerdict is the gate decision the mirror replays. When Allow is
// true the mirror sets SetToken's cookies (if any) and forwards to its local
// handler; otherwise it writes Status/ContentType/Location/Body verbatim
// (the QR auth card, ban JSON, device-limit message, or redirect).
type mirrorGateVerdict struct {
	Allow       bool   `json:"allow"`
	SetToken    string `json:"set_token,omitempty"`
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Location    string `json:"location,omitempty"`
	BodyB64     string `json:"body_b64,omitempty"`
}

// captureWriter is a minimal in-memory http.ResponseWriter used to run the
// real gate middleware against a synthetic request and capture its output.
// Lighter than net/http/httptest and keeps that test-only import out of the
// production binary.
type captureWriter struct {
	hdr    http.Header
	status int
	body   []byte
	wrote  bool
}

func (c *captureWriter) Header() http.Header {
	if c.hdr == nil {
		c.hdr = make(http.Header)
	}
	return c.hdr
}
func (c *captureWriter) WriteHeader(status int) {
	if !c.wrote {
		c.status = status
		c.wrote = true
	}
}
func (c *captureWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	c.body = append(c.body, b...)
	return len(b), nil
}

// mirrorGateSentinelHeader is set by the allow-sink when the gate calls
// next() — i.e. the request is authorized.
const mirrorGateSentinelHeader = "X-Mirror-Gate"

// mirrorRemoteAddr builds a RemoteAddr ("ip:0") for the synthetic request so
// the gate's clientIP(r) resolves to the real client IP (directRemoteIP reads
// RemoteAddr). net.JoinHostPort brackets IPv6 correctly.
func mirrorRemoteAddr(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	return net.JoinHostPort(ip, "0")
}

// mirrorOriginAuthGateHandler runs the real gate (gateMW) against a synthetic
// request built from the forwarded attributes and returns the verdict.
// gateMW must be the SAME middleware installed locally, so the decision (and
// any device-binding / pending-code side effects on this origin's store) is
// authoritative and identical to a direct hit.
func mirrorOriginAuthGateHandler(gateMW func(http.Handler) http.Handler) http.HandlerFunc {
	// Sink reached only when the gate authorizes the request (calls next).
	sink := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(mirrorGateSentinelHeader, "allow")
		w.WriteHeader(http.StatusOK)
	})
	wrapped := gateMW(sink)

	return func(w http.ResponseWriter, r *http.Request) {
		if !isMirrorOriginRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var in mirrorGateRequest
		if err := stdjson.Unmarshal(body, &in); err != nil || strings.TrimSpace(in.Path) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}

		// Build the synthetic request the gate evaluates. GET, original path +
		// query so uid/fp/token parsing works; client IP via RemoteAddr (so
		// pending codes/bans key on the real client); UA/Accept for device
		// label + Lampa detection; token as cookies so the gate's cookie path
		// resolves it without re-issuing.
		u := &url.URL{Path: in.Path, RawQuery: in.RawQuery}
		synth := (&http.Request{
			Method:     http.MethodGet,
			URL:        u,
			Header:     make(http.Header),
			Host:       r.Host,
			RemoteAddr: mirrorRemoteAddr(in.ClientIP),
		}).WithContext(r.Context())
		if in.UserAgent != "" {
			synth.Header.Set("User-Agent", in.UserAgent)
		}
		if in.Accept != "" {
			synth.Header.Set("Accept", in.Accept)
		}
		if in.XRequestedWith != "" {
			synth.Header.Set("X-Requested-With", in.XRequestedWith)
		}
		if ip := strings.TrimSpace(in.ClientIP); ip != "" {
			synth.Header.Set("X-Real-IP", ip)
			synth.Header.Set("X-Forwarded-For", ip)
		}
		if in.Token != "" {
			synth.AddCookie(&http.Cookie{Name: "_lampac_auth", Value: in.Token})
			synth.AddCookie(&http.Cookie{Name: "lampac_token", Value: in.Token})
		}

		rec := &captureWriter{}
		wrapped.ServeHTTP(rec, synth)

		verdict := mirrorGateVerdict{}
		if rec.Header().Get(mirrorGateSentinelHeader) == "allow" {
			verdict.Allow = true
			// Extract the token the gate decided to (re)issue, if any.
			for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
				if c.Name == "lampac_token" && strings.TrimSpace(c.Value) != "" {
					verdict.SetToken = c.Value
				}
			}
		} else {
			verdict.Status = rec.status
			if verdict.Status == 0 {
				verdict.Status = http.StatusOK
			}
			verdict.ContentType = rec.Header().Get("Content-Type")
			verdict.Location = rec.Header().Get("Location")
			verdict.BodyB64 = base64.StdEncoding.EncodeToString(rec.body)
		}
		writeJSON(w, http.StatusOK, verdict)
	}
}
