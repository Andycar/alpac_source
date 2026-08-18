package httpapi

// Per-route auth gate for source discovery vs. stream-redirect endpoints.
//
// Background — a user reported watching movies without ever signing in.
// Root cause: `tgAuthGateMiddleware`'s file-extension whitelist (`.m3u8`,
// `.ts`, `.mp4`, `.m4s`, `.m4a` — see auth_tg_api.go ~line 1147) was meant
// to let external players fetch already-issued stream URLs without
// cookies. But the same whitelist also covered `/lite/{balancer}/video.m3u8`
// handlers (lite_sources.go ~134) that act as *lazy resolvers* — they
// take an `id`/`vid`/`kp` query param, talk to the source, and 302 to
// `/proxy/<enc>`. Knowing the bare URL (kp is public on TMDB / a guess)
// was enough to stream.
//
// Strategy chosen (per user request 2026-05-28): leave stream endpoints
// open — external players (VIMU, MX Player, VLC) hit them without
// browser cookies, gating them breaks playback. Instead, gate **source
// discovery** — `/lite/events` and the per-balancer resolve/search
// handlers. Without a list of sources the client can't construct the
// stream-redirect URLs in the first place, so the lazy-resolve paths
// stay safe in practice even though technically open.
//
// What counts as "stream-redirect" here is conservative: only the
// canonical lazy-resolve endpoints (`/video`, `/manifest`, `/stream`,
// `/playlist`, `/mux*`, `/dash.mpd`, etc.). New balancers that add
// extra stream entries must update isLiteStreamPath OR redirect to
// /proxy/ from their resolve step (preferred — keeps the security
// surface minimal).

import (
	"net/http"
	"strings"

	"lampac-go/internal/auth"
)

// isLiteStreamPath reports whether the given /lite/<raw> path is a
// stream-redirect endpoint that external players hit directly. These
// stay accessible without auth because:
//
//   - the handler immediately 302s to /proxy/<AES-encrypted URL> or
//     equivalent — anything the user gets out of the call is already
//     gated by the encryption key on the destination URL;
//   - the params required to make the call useful (`vid`, `kp` plus a
//     balancer-specific session token, `id_movie`, `hash`, etc.) come
//     out of the gated `/lite/events` or `/lite/<balancer>` resolve
//     path. A bare hit with guessed params either errors at the source
//     or wastes one upstream request without leaking anything.
//
// Anything not in this list is treated as source discovery (JSON list
// of voices, search, metadata) and gated.
func isLiteStreamPath(raw string) bool {
	// Extension-suffix probe first — cheap O(1) check covers the bulk of
	// stream endpoints regardless of the balancer prefix.
	if hasStreamExt(raw) {
		return true
	}
	// Non-extension stream entries — exact final segment match.
	const sep = "/"
	last := raw
	if i := strings.LastIndex(raw, sep); i >= 0 {
		last = raw[i+1:]
	}
	switch last {
	case "video", "manifest", "stream", "playlist", "movie",
		"mux", "seg", "feed", "dash.mpd", "img":
		// img included because YouTube thumbnails ride the same handler
		// and external clients fetch them without cookies.
		return true
	}
	// YouTube sub-paths under /mux/* and /feed/* are stream / personalized
	// playlist endpoints — must remain accessible to the player runtime.
	// /channel and /recommend serve the same public catalog data as /feed
	// (trending / any channel's uploads via yt-dlp, no account involved), and
	// native clients open them without Lampa cookies.
	if strings.HasPrefix(raw, "youtube/mux/") || strings.HasPrefix(raw, "youtube/feed/") ||
		strings.HasPrefix(raw, "youtube/channel") || strings.HasPrefix(raw, "youtube/recommend") {
		return true
	}
	// PidTor stream entries (already whitelisted in the global gate by the
	// "/lite/pidtor/s..." prefix, included here so a future caller of
	// isLiteStreamPath also gets the right answer for it).
	if strings.HasPrefix(raw, "pidtor/s") && !strings.HasPrefix(raw, "pidtor/serial") {
		return true
	}
	return false
}

func hasStreamExt(p string) bool {
	// Suffix-match instead of filepath.Ext so URLs with query-string-like
	// trailing data still get caught (we strip query before this, but a
	// few balancers append .m3u8 to a path that already ends in /video.mp4
	// — both legitimate; both should match).
	for _, ext := range []string{".m3u8", ".m4s", ".mp4", ".m4a", ".mp3", ".ts"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

// requireSourceDiscoveryAuth returns true when the request should be
// allowed through the source-discovery gate. Returns true in any of:
//
//   - A user is resolved in the request context (TG / password / anon).
//     Anonymous users from mode="none" pass — that mode is open access
//     by design, anon is issued on first HTML nav.
//   - The server has NO auth method configured: TG disabled in config,
//     no password users created, mode is not explicitly "none". In this
//     compat scenario the user has no way to log in, so blocking source
//     discovery would softlock the entire deployment with a misleading
//     "auth required" banner. Pre-fix behaviour (gate not installed
//     when tgTokenStore=nil) effectively allowed open access; we
//     preserve that for un-configured servers.
//
// Returns false only when an auth method IS configured but the request
// hasn't satisfied it. That's the case where the user genuinely needs
// to authenticate.
func requireSourceDiscoveryAuth(r *http.Request) bool {
	if user, _ := auth.UserFromContext(r.Context()); user != nil {
		return true
	}
	return !isAnyUserAuthConfigured()
}

// isAnyUserAuthConfigured reports whether the server has at least one
// way for a user to authenticate (TG bot, password users, or open-mode).
// When none are configured, the source-discovery gate falls back to
// open access — same shape as the pre-fix behaviour.
func isAnyUserAuthConfigured() bool {
	// mode="none" is the explicit open-access mode; anon issuer handles
	// the cookie. If we ever get here without a user despite mode="none"
	// it's an XHR before the HTML nav — still safe to let through.
	if globalAuthModeStore != nil && globalAuthModeStore.Get() == "none" {
		return false // not "configured for gating" — it's open
	}

	// TG configured? (kitTGTokenStore is set by registerKitRoutes after
	// server.go builds the TG store; non-nil here means TG auth is live).
	if kitTGTokenStore != nil {
		return true
	}

	// Password users created via admin v2? Existence of at least one
	// user means the admin has set up password mode even if Mode is
	// still "tg" in the runtime override (admin in the middle of a
	// migration).
	if globalPwUserStore != nil && len(globalPwUserStore.List()) > 0 {
		return true
	}

	return false
}

// authRequiredLiteHandler answers the synthetic "_auth_required" balancer.
// Returns accsdbResponse so Lampa renders a blue banner on the source-
// picker screen. URL emitted by /lite/events when unauthed → Lampa probes
// it → this fires. Always 200 + accsdb shape (rch=false so checksearch
// dedupe-logic doesn't blacklist it on retry).
func authRequiredLiteHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, accsdbResponse("Требуется авторизация. Откройте Настройки → Аккаунт и нажмите «Войти»."))
}
