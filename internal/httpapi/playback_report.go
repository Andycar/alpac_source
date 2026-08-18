package httpapi

// Playback capability reporting.
//
// Clients POST one small report per playback attempt; the aggregate answers
// "what does the built-in player fail on, and on which devices" without anyone
// having to reconstruct it from weblog lines.
//
// Deliberately NOT capi-signed: TV clients report from contexts where a capi
// session may not exist yet (a torrent opened straight from a deep link), and a
// dropped report is worth far less than a dropped playback. The payload carries
// no identity, so an unauthenticated POST costs nothing beyond the rate limit.

import (
	"net/http"
	"strconv"

	"lampac-go/internal/config"
	"lampac-go/internal/playbackstats"
	"lampac-go/internal/tgauth"
)

var playbackStatsStore *playbackstats.Store

// initPlaybackStats creates the aggregate store. Safe to call once at wiring.
func initPlaybackStats(cfg config.Config) {
	if playbackStatsStore == nil {
		playbackStatsStore = playbackstats.New(cfg.Compat.RepoRoot)
	}
}

// POST /lite/playback-report
func playbackReportHandler() http.HandlerFunc {
	limiter := newWeblogLimiter() // same shape of abuse, same defence
	return func(w http.ResponseWriter, r *http.Request) {
		if playbackStatsStore == nil {
			http.NotFound(w, r)
			return
		}
		// allow() returns how many of the requested items fit the budget, not a bool.
		if limiter.allow(clientIP(r), 1) < 1 {
			w.WriteHeader(http.StatusOK) // silently drop; never make the client retry
			return
		}
		var rep playbackstats.Report
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&rep); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		playbackStatsStore.Add(rep)
		w.WriteHeader(http.StatusNoContent)
	}
}

// GET /lite/playback-advice?platform=&device= — what this hardware should avoid.
//
// The point of the whole collection: one device only learns that it cannot play
// a codec by failing in front of its owner. The aggregate already knows, so the
// client can steer BEFORE the black screen instead of after it.
//
// Public (no auth): it returns nothing about the caller, only a codec blacklist
// for a device model — the same answer for everyone with that box. Requiring a
// session would just mean anonymous playback keeps hitting the wall.
func playbackAdviceHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if playbackStatsStore == nil {
			writeJSON(w, http.StatusOK, playbackstats.Advice{AvoidAudio: []string{}, AvoidVideo: []string{}})
			return
		}
		q := r.URL.Query()
		adv := playbackStatsStore.AdviceFor(q.Get("platform"), q.Get("device"))
		// Advice changes only as reports accumulate, so let clients hold it for a
		// while — this is called on every player start.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		writeJSON(w, http.StatusOK, adv)
	}
}

// GET /{adminPath}/api/playback-stats?limit=&all=1 — the roadmap view.
func adminPlaybackStatsHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		if playbackStatsStore == nil {
			writeJSON(w, http.StatusOK, map[string]any{"summary": map[string]any{}, "rows": []any{}})
			return
		}
		limit := 200
		if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 2000 {
			limit = v
		}
		rows := playbackStatsStore.Problems(limit)
		if parseBoolParam(r.URL.Query().Get("all")) {
			rows = playbackStatsStore.All(limit)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"summary": playbackStatsStore.Summary(),
			"rows":    rows,
		})
	}
}

// POST /{adminPath}/api/playback-stats/reset — start fresh after shipping a fix.
func adminPlaybackStatsResetHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		if playbackStatsStore != nil {
			playbackStatsStore.Reset()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

