package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"net/http"

	"lampac-go/internal/tgauth"
)

// FeedbackOps bundles the host-side feedback-store operations, built where the
// concrete *FeedbackStore + tgBot live (see RegisterFeedbackRoutes call site).
// Each mutating op returns (httpStatus, errMsg); errMsg=="" means success. This
// keeps the internal Feedback* types (FeedbackStore/FeedbackFilter/FeedbackReply/
// FeedbackStatus/…) out of this package.
type FeedbackOps struct {
	List        func(status, category, priority, search string) any
	SetStatus   func(ticketID, status string) (int, string)
	AddReply    func(ticketID, message string) (int, string)
	SetPriority func(ticketID, priority string) (int, string)
	Delete      func(ticketID string) (int, string)
	Stats       func() any
}

func respondFB(w http.ResponseWriter, status int, errMsg string) {
	if errMsg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": errMsg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /{adminPath}/api/feedback  — list tickets (query filters)
// POST /{adminPath}/api/feedback — admin actions: set_status, reply, set_priority, delete
func tgAdminFeedbackHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, ops FeedbackOps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, isSuper, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			q := r.URL.Query()
			writeJSON(w, http.StatusOK, ops.List(q.Get("status"), q.Get("category"), q.Get("priority"), q.Get("search")))

		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 65536))
			var req struct {
				Action   string `json:"action"`
				TicketID string `json:"ticket_id"`
				Status   string `json:"status"`
				Priority string `json:"priority"`
				Message  string `json:"message"`
			}
			if stdjson.Unmarshal(body, &req) != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad json"})
				return
			}

			if req.TicketID == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ticket_id required"})
				return
			}

			switch req.Action {
			case "set_status":
				st, msg := ops.SetStatus(req.TicketID, req.Status)
				respondFB(w, st, msg)
			case "reply":
				st, msg := ops.AddReply(req.TicketID, req.Message)
				respondFB(w, st, msg)
			case "set_priority":
				st, msg := ops.SetPriority(req.TicketID, req.Priority)
				respondFB(w, st, msg)
			case "delete":
				if !isSuper {
					writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "super admin required"})
					return
				}
				st, msg := ops.Delete(req.TicketID)
				respondFB(w, st, msg)
			default:
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown action"})
			}
		}
	}
}

// GET /{adminPath}/api/feedback/stats — dashboard statistics.
func tgAdminFeedbackStatsHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, ops FeedbackOps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, ops.Stats())
	}
}
