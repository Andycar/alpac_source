package calendarhttp

// Переключатель отслеживания озвучек — по КОНКРЕТНОЙ подписке.
//
// Не общий тумблер на пользователя: человек может ждать нормальный дубляж одного
// фильма и не хотеть шума по остальным двадцати подпискам.

import (
	"net/http"
	"strings"

	"lampac-go/internal/calendar"
)

// POST /api/calendar/voices — {key, enabled}
func calendarTrackVoicesHandler(calStore *calendar.Store, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		var req struct {
			Key     string   `json:"key"`
			Enabled bool     `json:"enabled"`
			Names   []string `json:"names"` // ждать только эти озвучки; пусто = любую новую
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		key := strings.TrimSpace(req.Key)
		if key == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "key required"})
			return
		}
		// Список ожидаемых озвучек — отдельная операция: он сам включает отслеживание,
		// поэтому не требует от клиента двух запросов и не может рассинхронизироваться
		// с флагом.
		if req.Names != nil {
			if !calStore.SetWantVoices(tgID, key, req.Names) {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "subscription not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": true, "names": req.Names})
			return
		}

		if !calStore.SetTrackVoices(tgID, key, req.Enabled) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "subscription not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": req.Enabled})
	}
}
