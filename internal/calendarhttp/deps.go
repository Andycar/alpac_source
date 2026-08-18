package calendarhttp

import (
	"net/http"

	"lampac-go/internal/httpx"
	"lampac-go/internal/tgauth"

	jsoniter "github.com/json-iterator/go"
)

// deps.go — the injected seam from the former host (httpapi). The moved calendar
// handlers reach into the host only for the admin-auth gate; it is a function
// value injected once via Deps at RegisterRoutes time, so this package never
// imports httpapi. writeJSON/json are local copies of the host helpers.
// (calendarTgID lives in calendar_api.go — this is its origin package.)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

func writeJSON(w http.ResponseWriter, code int, payload any) { httpx.WriteJSON(w, code, payload) }

// Deps bundles host dependencies injected at RegisterRoutes time.
type Deps struct {
	// AdminAuthCheck is the host's tgAdminAuthCheck — it runs in the host's
	// context (closing over its admin config), so this package needs only the
	// exported tgauth arg types.
	AdminAuthCheck func(w http.ResponseWriter, r *http.Request, store *tgauth.Store, adminStore *tgauth.AdminIDStore) (int64, bool, bool)
}

var deps Deps

func setDeps(d Deps) { deps = d }

// tgAdminAuthCheck forwards to the injected host check. The unwired default
// denies (ok=false) — admin routes are only registered when RegisterRoutes has
// already run setDeps, so the default is a fail-closed safety net.
func tgAdminAuthCheck(w http.ResponseWriter, r *http.Request, store *tgauth.Store, adminStore *tgauth.AdminIDStore) (int64, bool, bool) {
	if deps.AdminAuthCheck != nil {
		return deps.AdminAuthCheck(w, r, store, adminStore)
	}
	http.Error(w, "forbidden", http.StatusForbidden)
	return 0, false, false
}
