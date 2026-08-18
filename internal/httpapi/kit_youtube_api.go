package httpapi

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/litesrc"
	"lampac-go/internal/tgauth"
)

// YouTube account linking from the Telegram mini-app.
//
// The bot already exposes /youtube_auth, but that means typing a command and
// then copying a code out of a chat message. These endpoints back a button in
// the mini-app: it starts the same Google Device Flow, shows the code and the
// link, and polls until Google reports the grant.
//
// Device Flow is inherently two-stage — the user approves on google.com/device
// while we poll in the background — so the start call parks the pending state
// here and /status reports it. Everything is keyed by the Telegram ID proven by
// the mini-app's signed initData (kitAuthFromRequest), never by anything the
// client sends in the body.

// ytLinkState is one in-flight device-flow attempt.
type ytLinkState struct {
	Code    string
	URL     string
	Started time.Time
	Expires time.Time
	Done    bool
	Err     string
}

var (
	ytLinkMu      sync.Mutex
	ytLinkPending = map[int64]*ytLinkState{}
)

// ytLinkTTL bounds how long a started-but-unfinished attempt is remembered.
// Google's device codes live 30 minutes; keeping ours a touch shorter means a
// stale card in the mini-app expires before the code it shows stops working.
const ytLinkTTL = 25 * time.Minute

// ytLinkGet returns the live pending attempt for a user, dropping expired ones.
func ytLinkGet(tgID int64) *ytLinkState {
	ytLinkMu.Lock()
	defer ytLinkMu.Unlock()
	st := ytLinkPending[tgID]
	if st == nil {
		return nil
	}
	if !st.Done && time.Now().After(st.Expires) {
		delete(ytLinkPending, tgID)
		return nil
	}
	return st
}

// registerKitYouTubeRoutes wires the mini-app's YouTube linking endpoints.
// ytProvider is nil when [youtube_oauth] isn't configured — the handlers then
// report that plainly instead of 404ing, so the mini-app can hide the section.
func registerKitYouTubeRoutes(router interface {
	Get(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
}, cfg config.Config, ytProvider tgauth.YouTubeAuthProvider) {
	// tgID proven by the mini-app's signed initData; 0 when unauthenticated.
	kitID := func(r *http.Request) int64 {
		raw, _, err := kitAuthFromRequest(r, liveConfig(cfg))
		if err != nil {
			return 0
		}
		id, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil {
			return 0
		}
		return id
	}

	router.Get("/api/kit/youtube/status", func(w http.ResponseWriter, r *http.Request) {
		if ytProvider == nil {
			writeJSON(w, http.StatusOK, map[string]any{"available": false})
			return
		}
		tgID := kitID(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		out := map[string]any{"available": true, "linked": ytProvider.IsLinked(tgID)}
		if out["linked"].(bool) {
			out["channel"] = ytProvider.ChannelTitle(tgID)
			// A finished attempt has served its purpose once we report linked.
			ytLinkMu.Lock()
			delete(ytLinkPending, tgID)
			ytLinkMu.Unlock()
		} else if st := ytLinkGet(tgID); st != nil {
			out["pending"] = true
			out["code"] = st.Code
			out["url"] = st.URL
			out["expires_in"] = int(time.Until(st.Expires).Seconds())
			if st.Err != "" {
				out["error"] = st.Err
			}
		}
		writeJSON(w, http.StatusOK, out)
	})

	router.Post("/api/kit/youtube/start", func(w http.ResponseWriter, r *http.Request) {
		if ytProvider == nil {
			writeJSON(w, http.StatusOK, map[string]any{"available": false})
			return
		}
		tgID := kitID(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if ytProvider.IsLinked(tgID) {
			writeJSON(w, http.StatusOK, map[string]any{
				"linked":  true,
				"channel": ytProvider.ChannelTitle(tgID),
			})
			return
		}
		// Reuse a live attempt instead of burning a second device code: the
		// user re-opening the mini-app should see the SAME code they were
		// given, not a fresh one that invalidates what they already typed.
		if st := ytLinkGet(tgID); st != nil && !st.Done && st.Err == "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"code": st.Code, "url": st.URL,
				"expires_in": int(time.Until(st.Expires).Seconds()),
			})
			return
		}

		code, verURL, pollDone, err := ytProvider.StartAuth(tgID)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
			return
		}
		st := &ytLinkState{
			Code:    code,
			URL:     verURL,
			Started: time.Now(),
			Expires: time.Now().Add(ytLinkTTL),
		}
		ytLinkMu.Lock()
		ytLinkPending[tgID] = st
		ytLinkMu.Unlock()

		// StartAuth polls Google in its own goroutine and reports once; park
		// the outcome so /status can show it. The token itself is stored by the
		// provider, so nothing here touches credentials.
		go func() {
			pollErr := <-pollDone
			ytLinkMu.Lock()
			st.Done = true
			if pollErr != nil {
				st.Err = pollErr.Error()
			}
			ytLinkMu.Unlock()
		}()

		writeJSON(w, http.StatusOK, map[string]any{
			"code": code, "url": verURL,
			"expires_in": int(ytLinkTTL.Seconds()),
		})
	})

	// Shorts: «all» (default) | «hide» — resolved server-side for every client, so Lampa, web,
	// Android and tvOS all honour the choice without shipping a new build.
	router.Get("/api/kit/youtube/shorts", func(w http.ResponseWriter, r *http.Request) {
		tgID := kitID(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		mode := litesrc.ShortsPrefs(cfg.Compat.RepoRoot).Get(tgID)
		if mode == "" {
			mode = "all"
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": mode})
	})

	router.Post("/api/kit/youtube/shorts", func(w http.ResponseWriter, r *http.Request) {
		tgID := kitID(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		var body struct {
			Mode string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body.Mode {
		case "hide", "only", "all", "":
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad mode"})
			return
		}
		if err := litesrc.ShortsPrefs(cfg.Compat.RepoRoot).Set(tgID, body.Mode); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
			return
		}
		mode := body.Mode
		if mode == "" {
			mode = "all"
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": mode})
	})

	router.Post("/api/kit/youtube/unlink", func(w http.ResponseWriter, r *http.Request) {
		if ytProvider == nil {
			writeJSON(w, http.StatusOK, map[string]any{"available": false})
			return
		}
		tgID := kitID(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if err := ytProvider.Unlink(tgID); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
			return
		}
		ytLinkMu.Lock()
		delete(ytLinkPending, tgID)
		ytLinkMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"linked": false})
	})
}
