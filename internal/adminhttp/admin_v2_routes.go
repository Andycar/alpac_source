package adminhttp

import (
	"io"
	"net/http"
	"strings"

	"lampac-go/internal/tgauth"
	adminweb "lampac-go/web/admin"

	"github.com/go-chi/chi/v5"
)

// registerAdminV2Routes wires the v2 admin panel + Telegram WebApp at:
//
//	GET  /{adminPath}/v2/            — Lit-based premium admin SPA (cookie auth)
//	GET  /{adminPath}/v2/*           — static assets (CSS/JS/SVG/fonts)
//	GET  /tg-admin/                  — Telegram WebApp entry (initData auth)
//	GET  /tg-admin/*                 — static assets (same bundle, TG bridge active)
//	POST /api/tg-admin/auth          — exchange validated initData → session cookie
//
// The old admin panel at /{adminPath} is left untouched — both versions
// coexist so we can iterate the new UI without breaking anyone.
//
// All /v2/api/* requests reuse the existing admin API endpoints under
// /{adminPath}/api/* — no API duplication, just a different shell.
func RegisterAdminV2Routes(router chi.Router, adminPath string, store *tgauth.Store, adminIDStore *tgauth.AdminIDStore) {
	fs := http.StripPrefix("/"+adminPath+"/v2/", adminweb.FileServer())

	// Web admin SPA — gated by the same admin auth as the legacy panel.
	router.Get("/"+adminPath+"/v2", func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminIDStore); !ok {
			return // tgAdminAuthCheck already wrote the redirect
		}
		// Trailing-slash canonical redirect so relative asset URLs resolve correctly.
		http.Redirect(w, r, "/"+adminPath+"/v2/", http.StatusFound)
	})
	router.Get("/"+adminPath+"/v2/", func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminIDStore); !ok {
			return
		}
		serveEmbeddedFile(w, "index.html", "text/html; charset=utf-8")
	})
	router.Get("/"+adminPath+"/v2/*", func(w http.ResponseWriter, r *http.Request) {
		// Static assets need NO auth gate beyond the cookie that the SPA shell
		// already established — they're plain JS/CSS/SVG, not data.
		// Set conservative caching so during dev a hard-refresh still picks up changes.
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		fs.ServeHTTP(w, r)
	})

	// Telegram WebApp shell — no cookie auth needed at the HTML level; the
	// JS bridge validates Telegram.WebApp.initData server-side via /api/tg-admin/auth
	// before any data request goes out.
	tgFS := http.StripPrefix("/tg-admin/", adminweb.FileServer())
	router.Get("/tg-admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/tg-admin/", http.StatusFound)
	})
	router.Get("/tg-admin/", func(w http.ResponseWriter, r *http.Request) {
		serveEmbeddedFile(w, "tg.html", "text/html; charset=utf-8")
	})
	router.Get("/tg-admin/*", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		tgFS.ServeHTTP(w, r)
	})

	// TG initData → admin-session exchange. Same endpoint serves both the
	// web admin (for users who arrived via TG mini-app deep link) and the
	// TG WebApp itself.
	router.Post("/api/tg-admin/auth", tgWebAppAuthHandler(store, adminIDStore))

	// Legacy admin SPA — 8k lines of JS that used to be inline <script>
	// blocks inside admin_tg_panel.go's HTML string. Extracted to
	// web/admin/legacy/admin_panel.js for cacheability and editability;
	// admin_tg_panel.go now references this file via `<script src=>`.
	router.Get("/admin-legacy.js", func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminIDStore); !ok {
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
		f, err := adminweb.FS.Open("legacy/admin_panel.js")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		body, _ := io.ReadAll(f)
		_, _ = w.Write(body)
	})
}

// serveEmbeddedFile serves a single file from the adminweb FS with the given
// content type. Used for the SPA entry points (index.html, tg.html) that we
// want to inline rather than let the FileServer pick up — keeps caching and
// content-type behaviour predictable.
func serveEmbeddedFile(w http.ResponseWriter, name, contentType string) {
	f, err := adminweb.FS.Open(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("X-Frame-Options", "ALLOWALL") // TG WebApp needs to iframe us
	_, _ = w.Write(body)
}

// pluginAdminV2Probe is exported so server.go can avoid the import-cycle risk
// of referring to adminweb from inside the embed package indirectly — see
// server.go's registerAdminV2Routes call.
func pluginAdminV2Probe() string {
	return strings.TrimSpace("ok")
}
