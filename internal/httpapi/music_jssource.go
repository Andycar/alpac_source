package httpapi

import (
	"net/http"
	"strings"

	"lampac-go/internal/jsmodules"
	"lampac-go/internal/proxylink"
)

// musicCustomHandler routes /music/{name}/{action} requests to a goja JS
// music source. Mirrors sisiCustHandler but tailored for music sources:
// the JS contract is `handle(inv)` with `inv.query.action` set from the
// path tail (search/charts/artist/play/bookmarks). Module returns either
// a tracks list or a single playback URL.
//
// Conventions for JS music modules (action = inv.query.action):
//
//	"charts"       → {tracks: [...]}                — homepage / featured
//	"top"          → {tracks: [...]}                — curated chart
//	"search"       → {tracks: [...]}                — full-text search
//	"artist"       → {tracks: [...]}                — discography for inv.query.id
//	"play"         → {url: "..."} or 302 via http.* — resolve audio blob
//
// Track shape: {id, title, artist, artist_id, cover, duration, audio}.
// audio is opaque to the server — the module's own "play" action knows how
// to turn it into a stream URL. This keeps each music backend free to
// pick its own encryption / API quirks without leaking them to the player.
func musicCustomHandler(jsMgr *jsmodules.Manager, proxyLinks *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/music/")
		parts := strings.SplitN(rest, "/", 2)
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		action := "charts"
		if len(parts) > 1 {
			action = strings.TrimRight(parts[1], "/")
		}

		if jsMgr == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "music sources disabled"})
			return
		}
		mod, ok := jsMgr.Get(name)
		if !ok || !mod.Enabled {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "music source not found: " + name})
			return
		}

		// Build inv.query from URL params + injected action.
		q := make(map[string]string, len(r.URL.Query())+1)
		for k, vals := range r.URL.Query() {
			if len(vals) > 0 {
				q[k] = vals[0]
			}
		}
		q["action"] = action

		inv := jsmodules.Invocation{
			Query:     q,
			Host:      hostFromRequest(r),
			RequestIP: clientIP(r),
			Path:      r.URL.Path,
		}

		resp, err := jsMgr.Invoke(r.Context(), name, inv)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}

		// "play" action: if the JS returned {url: "..."} and the request did
		// not opt into JSON, 302 to the URL so <audio> can stream directly.
		// This matches the audiobot built-in's contract.
		if action == "play" && r.URL.Query().Get("json") != "1" {
			body := resp.Body
			if u := extractURLField(body); u != "" {
				http.Redirect(w, r, u, http.StatusFound)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.Body)
	}
}

// extractURLField pulls a top-level "url" string out of a JSON body without
// allocating a full map. Returns "" on any parse glitch — callers fall
// back to raw JSON in that case.
func extractURLField(body []byte) string {
	// Cheap inline parse: scan for `"url"` followed by `:` and a quoted
	// string. Avoids pulling in encoding/json for the hot path; safe
	// because we control the JS contract (single top-level object).
	const key = `"url"`
	i := strings.Index(string(body), key)
	if i < 0 {
		return ""
	}
	rest := string(body[i+len(key):])
	rest = strings.TrimLeft(rest, " \t\n:")
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}
