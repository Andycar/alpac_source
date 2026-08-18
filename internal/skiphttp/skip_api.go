package skiphttp

import (
	"net/http"
	"strconv"

	"lampac-go/internal/config"
	"lampac-go/internal/skipdb"
	"lampac-go/internal/skipsrc"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
// Public API: GET /api/skip — lookup skip segments for a video
// ---------------------------------------------------------------------------

// skipAggregator is process-wide: its cache is the point (one lookup serves every
// client and every device), and it holds no per-request state.
var skipAggregator = skipsrc.New()

func skipLookupHandler(db *skipdb.DB, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		imdbID := r.URL.Query().Get("imdb_id")
		kp := r.URL.Query().Get("kp")
		season, _ := strconv.Atoi(r.URL.Query().Get("s"))
		episode, _ := strconv.Atoi(r.URL.Query().Get("e"))

		// If no imdb_id but have KP, try to resolve via TMDB.
		if imdbID == "" && kp != "" {
			imdbID = resolveKPToIMDb(kp, cfg)
		}

		if imdbID == "" {
			writeJSON(w, http.StatusOK, map[string]any{"segments": []any{}})
			return
		}

		segments := db.Lookup(imdbID, season, episode)

		// Our own curated entries win outright — they were checked against a real
		// file. Only when we have nothing do we ask the public databases, which
		// between them cover far more titles than a hand-maintained list ever will.
		// duration is what lets the duration-aware sources answer in THIS file's
		// coordinates instead of the broadcast cut's; without it they still answer,
		// just less precisely (see skipsrc.CoordBase).
		if len(segments) == 0 {
			duration, _ := strconv.ParseFloat(r.URL.Query().Get("duration"), 64)
			tmdbID := int64(-1)
			if v, err := strconv.ParseInt(r.URL.Query().Get("tmdb_id"), 10, 64); err == nil && v > 0 {
				tmdbID = v
			}
			external := skipAggregator.Lookup(r.Context(), skipsrc.Query{
				ImdbID:   imdbID,
				TmdbID:   tmdbID,
				Season:   season,
				Episode:  episode,
				Duration: duration,
				IsMovie:  season < 1,
			})
			for _, seg := range external {
				segments = append(segments, skipdb.Segment{
					Type:  seg.Category.LegacyType(),
					Start: seg.Start,
					End:   seg.End,
				})
			}
		}

		// Apply defaults if no data found and this looks like a series episode.
		if len(segments) == 0 && season > 0 && episode > 0 {
			defaults := db.GetDefaults()
			if defaults.IntroDefaultSec > 0 {
				segments = append(segments, skipdb.Segment{
					Type:  "intro",
					Start: 0,
					End:   float64(defaults.IntroDefaultSec),
				})
			}
		}

		if segments == nil {
			segments = []skipdb.Segment{}
		}

		writeJSON(w, http.StatusOK, map[string]any{"segments": segments})
	}
}

// resolveKPToIMDb attempts to find an IMDb ID for a Kinopoisk ID via TMDB.
func resolveKPToIMDb(kpID string, cfg config.Config) string {
	// Use the TMDB find endpoint. This is the same proxy infrastructure we already have.
	apiHost := cfg.TMDBProxy.APIHost
	if apiHost == "" {
		apiHost = "apitmdb.cub.red"
	}
	apiKey := cfg.TMDBProxy.APIKey
	if apiKey == "" {
		apiKey = "4ef0d7355d9ffb5151e987764708ce96"
	}

	// TMDB doesn't natively support KP→IMDb lookup via /find.
	// For now, return empty. Users should provide imdb_id when available.
	// TODO: implement KP→IMDb mapping via TMDB search + cache.
	_ = apiHost
	_ = apiKey
	return ""
}

// ---------------------------------------------------------------------------
// Public API: POST /api/skip/mark — user-submitted skip marker
// ---------------------------------------------------------------------------

func skipMarkHandler(db *skipdb.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !db.GetDefaults().EnableUserMarks {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "user marks disabled"})
			return
		}

		var mark skipdb.UserMark
		if err := json.NewDecoder(r.Body).Decode(&mark); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
			return
		}

		if mark.ImdbID == "" || mark.Type == "" || mark.End <= mark.Start {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing required fields"})
			return
		}

		if mark.Type != "intro" && mark.Type != "outro" && mark.Type != "recap" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "type must be intro, outro, or recap"})
			return
		}

		if err := db.AddUserMark(mark); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ---------------------------------------------------------------------------
// Admin API: GET/POST/DELETE /{admin}/api/skip
// ---------------------------------------------------------------------------

// tgAdminSkipHandler returns skip DB stats and optionally a show's data.
func tgAdminSkipHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, db *skipdb.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		imdbID := r.URL.Query().Get("imdb_id")
		shows, episodes := db.Stats()

		resp := map[string]any{
			"total_shows":    shows,
			"total_episodes": episodes,
			"user_marks":     len(db.UserMarks()),
		}

		// If a specific show requested, include its data.
		if imdbID != "" {
			resp["show"] = db.ListShow(imdbID)
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// tgAdminSkipSetHandler sets skip segments for a specific episode.
func tgAdminSkipSetHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, db *skipdb.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		var req struct {
			ImdbID   string           `json:"imdb_id"`
			Season   int              `json:"season"`
			Episode  int              `json:"episode"`
			Segments []skipdb.Segment `json:"segments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
			return
		}
		if req.ImdbID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "imdb_id required"})
			return
		}

		if err := db.Set(req.ImdbID, req.Season, req.Episode, req.Segments); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// tgAdminSkipDeleteHandler removes skip data for a specific episode.
func tgAdminSkipDeleteHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, db *skipdb.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		imdbID := chi.URLParam(r, "imdbID")
		season, _ := strconv.Atoi(chi.URLParam(r, "season"))
		episode, _ := strconv.Atoi(chi.URLParam(r, "episode"))

		if err := db.Delete(imdbID, season, episode); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// tgAdminSkipMarksHandler returns pending user marks.
func tgAdminSkipMarksHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, db *skipdb.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"marks": db.UserMarks()})
	}
}

// tgAdminSkipMarkActionHandler approves or rejects a user mark.
func tgAdminSkipMarkActionHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, db *skipdb.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		var req struct {
			Index  int    `json:"index"`
			Action string `json:"action"` // "approve" or "reject"
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
			return
		}

		var err error
		switch req.Action {
		case "approve":
			err = db.ApproveUserMark(req.Index)
		case "reject":
			err = db.RejectUserMark(req.Index)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "action must be approve or reject"})
			return
		}

		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
