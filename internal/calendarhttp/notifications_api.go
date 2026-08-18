package calendarhttp

// In-app notification inbox endpoints.
//
// Telegram delivery needs no server-side state — the bot pushes and forgets.
// The app does: a user who wasn't looking at their phone when an episode
// dropped still needs to find out. These endpoints back a bell/badge in the
// clients and are gated by the same Telegram identity as the rest of
// /api/calendar (calendarTgID resolves it from the lampac token the app already
// carries, so no separate login is involved).

import (
	"io"
	"net/http"

	"lampac-go/internal/calendar"
)

// GET /api/calendar/notifications — list + unread count.
func calendarNotificationsHandler(inbox *calendar.Inbox, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		items := inbox.List(tgID)
		writeJSON(w, http.StatusOK, map[string]any{
			"items":  items,
			"unread": inbox.Unread(tgID),
		})
	}
}

// POST /api/calendar/notifications/read — mark one read, or all when id is absent.
func calendarNotificationsReadHandler(inbox *calendar.Inbox, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		if body, err := io.ReadAll(io.LimitReader(r.Body, 1024)); err == nil && len(body) > 0 {
			_ = json.Unmarshal(body, &req) // empty/!json body = "mark all"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"changed": inbox.MarkRead(tgID, req.ID),
			"unread":  inbox.Unread(tgID),
		})
	}
}

// POST /api/calendar/notifications/clear — empty the inbox.
func calendarNotificationsClearHandler(inbox *calendar.Inbox, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		inbox.Clear(tgID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
