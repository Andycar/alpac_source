package calendarhttp

// Расписание доставки: мгновенно или сводом, во сколько, и когда не беспокоить.

import (
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/calendar"
)

// pendingRef — очередь отложенных, чтобы показывать счётчик и уметь «прислать сейчас».
var pendingRef *calendar.PendingStore

// GET /api/calendar/delivery — текущее расписание + сколько ждёт отправки.
func calendarDeliveryGetHandler(calStore *calendar.Store, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		d := calStore.GetDelivery(tgID)
		queued := 0
		if pendingRef != nil {
			queued = pendingRef.Count(tgID)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"delivery": d,
			"queued":   queued,
		})
	}
}

// POST /api/calendar/delivery — сохранить расписание.
func calendarDeliverySetHandler(calStore *calendar.Store, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		var req calendar.Delivery
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		if req.Mode != calendar.DeliveryDigest {
			req.Mode = calendar.DeliveryInstant
		}
		req.DigestAt = strings.TrimSpace(req.DigestAt)
		req.QuietFrom = strings.TrimSpace(req.QuietFrom)
		req.QuietTo = strings.TrimSpace(req.QuietTo)
		// Пояс за пределами ±14 часов — мусор от клиента; лучше считать «UTC», чем
		// сдвинуть все расчёты на случайное число.
		if req.TZOffset < -14*60 || req.TZOffset > 14*60 {
			req.TZOffset = 0
		}

		calStore.SetDelivery(tgID, req)
		// ★Пересчитываем уже стоящее в очереди: иначе человек передвинул свод с
		// 20:00 на 09:00 и всё равно ждал бы до вечера, не понимая почему.
		if pendingRef != nil {
			pendingRef.Reschedule(tgID, req, time.Now())
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delivery": req})
	}
}

// POST /api/calendar/delivery/flush — «прислать всё отложенное сейчас».
func calendarDeliveryFlushHandler(whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		n := 0
		if pendingRef != nil {
			n = pendingRef.Flush(tgID, time.Now())
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "flushed": n})
	}
}
