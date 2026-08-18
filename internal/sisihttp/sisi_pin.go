package sisihttp

import (
	"crypto/rand"
	stdjson "encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

var pinDigitsRe = regexp.MustCompile(`^\d{6}$`)

// ── Rate limiter ──

type pinAttemptTracker struct {
	mu       sync.Mutex
	attempts []time.Time
}

var pinAttempts sync.Map // tgID → *pinAttemptTracker

func (t *pinAttemptTracker) allow(maxAttempts int, window time.Duration) (bool, int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := time.Now().Add(-window)
	filtered := t.attempts[:0]
	for _, a := range t.attempts {
		if a.After(cutoff) {
			filtered = append(filtered, a)
		}
	}
	t.attempts = filtered

	if len(t.attempts) >= maxAttempts {
		retryAfter := int(t.attempts[0].Add(window).Sub(time.Now()).Seconds()) + 1
		if retryAfter < 1 {
			retryAfter = 1
		}
		return false, retryAfter
	}

	t.attempts = append(t.attempts, time.Now())
	return true, 0
}

func (t *pinAttemptTracker) clear() {
	t.mu.Lock()
	t.attempts = nil
	t.mu.Unlock()
}

// ── Reset codes ──

type pinResetCode struct {
	code      string
	expiresAt time.Time
}

var pinResetCodes sync.Map // tgID → pinResetCode

// ── PIN section helper ──

type sisiPinSection struct {
	Pin    string `json:"pin"`
	Method string `json:"method"` // "pin" or "tg_confirm"
}

func loadPin(store *kit.Store, tgID string) string {
	cfg, err := store.Load(tgID)
	if err != nil || cfg == nil {
		return ""
	}
	raw, ok := cfg["SisiPin"]
	if !ok || len(raw) == 0 {
		return ""
	}
	var s sisiPinSection
	if stdjson.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s.Pin
}

// ── Handlers ──

func loadPinSection(store *kit.Store, tgID string) *sisiPinSection {
	cfg, err := store.Load(tgID)
	if err != nil || cfg == nil {
		return nil
	}
	raw, ok := cfg["SisiPin"]
	if !ok || len(raw) == 0 {
		return nil
	}
	var s sisiPinSection
	if stdjson.Unmarshal(raw, &s) != nil {
		return nil
	}
	return &s
}

// sisiPinStatusHandler returns protection status.
// GET /sisi/pin/status → {"has_pin": true/false, "method": "pin"|"tg_confirm"|""}
func sisiPinStatusHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"has_pin": false, "method": ""})
			return
		}

		sec := loadPinSection(store, tgID)
		if sec == nil || (sec.Pin == "" && sec.Method != "tg_confirm") {
			writeJSON(w, http.StatusOK, map[string]any{"has_pin": false, "method": ""})
			return
		}

		method := sec.Method
		if method == "" && sec.Pin != "" {
			method = "pin"
		}
		writeJSON(w, http.StatusOK, map[string]any{"has_pin": true, "method": method})
	}
}

// sisiPinSetHandler sets or updates the user's PIN.
// POST /sisi/pin/set  body: {"pin":"123456"}
func sisiPinSetHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Pin    string `json:"pin"`
			Method string `json:"method"`
		}
		if err := stdjson.NewDecoder(io.LimitReader(r.Body, 256)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
			return
		}

		// TG confirm mode — no PIN needed.
		if req.Method == "tg_confirm" {
			if err := store.UpdateSection(tgID, "SisiPin", sisiPinSection{Method: "tg_confirm"}); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed"})
				return
			}
			log.Info().Str("tgID", tgID).Msg("sisi: TG confirm mode enabled")
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
			return
		}

		if !pinDigitsRe.MatchString(req.Pin) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "PIN must be exactly 6 digits"})
			return
		}

		if err := store.UpdateSection(tgID, "SisiPin", sisiPinSection{Pin: req.Pin, Method: "pin"}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed"})
			return
		}

		log.Info().Str("tgID", tgID).Msg("sisi: PIN set")
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

// sisiPinCheckHandler verifies the user's PIN.
// POST /sisi/pin/check  body: {"pin":"123456"} → {"success": true/false}
func sisiPinCheckHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "unauthorized"})
			return
		}

		// Rate limit: 5 attempts per 60 seconds.
		raw, _ := pinAttempts.LoadOrStore(tgID, &pinAttemptTracker{})
		tracker := raw.(*pinAttemptTracker)
		if ok, retryAfter := tracker.allow(5, 60*time.Second); !ok {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":       "too_many_attempts",
				"retry_after": retryAfter,
			})
			return
		}

		var req struct {
			Pin string `json:"pin"`
		}
		if err := stdjson.NewDecoder(io.LimitReader(r.Body, 256)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid request"})
			return
		}

		savedPin := loadPin(store, tgID)
		if savedPin == "" {
			writeJSON(w, http.StatusOK, map[string]any{"success": true}) // no PIN = always open
			return
		}

		if req.Pin == savedPin {
			tracker.clear()
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"success": false})
		}
	}
}

// sisiPinRemoveHandler removes the user's PIN.
// POST /sisi/pin/remove → {"success": true}
func sisiPinRemoveHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		_ = store.DeleteSection(tgID, "SisiPin")
		log.Info().Str("tgID", tgID).Msg("sisi: PIN removed")
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

// sisiPinResetHandler handles PIN reset via Telegram.
// POST /sisi/pin/reset  body: {} → sends code to TG → {"sent": true}
// POST /sisi/pin/reset  body: {"code":"123456"} → verifies code → {"success": true}
func sisiPinResetHandler(store *kit.Store, cfg config.Config, bot *tgauth.Bot) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Code string `json:"code"`
		}
		_ = stdjson.NewDecoder(io.LimitReader(r.Body, 256)).Decode(&req)

		if req.Code != "" {
			// Verify reset code.
			raw, ok := pinResetCodes.Load(tgID)
			if !ok {
				writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "no_code_sent"})
				return
			}
			rc := raw.(pinResetCode)
			if time.Now().After(rc.expiresAt) {
				pinResetCodes.Delete(tgID)
				writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "code_expired"})
				return
			}
			if req.Code != rc.code {
				writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "wrong_code"})
				return
			}

			// Code matches — remove PIN.
			pinResetCodes.Delete(tgID)
			_ = store.DeleteSection(tgID, "SisiPin")
			log.Info().Str("tgID", tgID).Msg("sisi: PIN reset via TG code")
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
			return
		}

		// Generate and send reset code via TG.
		if bot == nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": "telegram bot not configured"})
			return
		}

		code := generatePinCode()
		pinResetCodes.Store(tgID, pinResetCode{
			code:      code,
			expiresAt: time.Now().Add(5 * time.Minute),
		})

		tgIDint, _ := strconv.ParseInt(tgID, 10, 64)
		if tgIDint > 0 {
			bot.SendToUser(tgIDint, fmt.Sprintf("🔐 Код сброса PIN для Клубнички: <b>%s</b>\n\nКод действует 5 минут.", code))
			writeJSON(w, http.StatusOK, map[string]any{"sent": true})
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"error": "telegram ID not available for this account type"})
		}
	}
}

func generatePinCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return fmt.Sprintf("%06d", n.Int64())
}

// ── TG Confirm Flow ──

type sisiTGConfirmRequest struct {
	status    string // "pending", "allowed", "denied"
	expiresAt time.Time
}

var sisiTGConfirmRequests sync.Map // requestID → *sisiTGConfirmRequest

// sisiPinTGConfirmHandler creates a TG confirmation request and sends inline buttons.
// POST /sisi/pin/tg-confirm → {"request_id": "abc123"} (creates request, sends TG message)
// GET  /sisi/pin/tg-confirm?id=abc123 → {"status": "pending"|"allowed"|"denied"}
func sisiPinTGConfirmHandler(store *kit.Store, cfg config.Config, bot *tgauth.Bot) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": "unauthorized"})
			return
		}

		if r.Method == http.MethodGet {
			// Poll status.
			reqID := r.URL.Query().Get("id")
			if reqID == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id required"})
				return
			}
			raw, ok := sisiTGConfirmRequests.Load(reqID)
			if !ok {
				writeJSON(w, http.StatusOK, map[string]any{"status": "expired"})
				return
			}
			cr := raw.(*sisiTGConfirmRequest)
			if time.Now().After(cr.expiresAt) {
				sisiTGConfirmRequests.Delete(reqID)
				writeJSON(w, http.StatusOK, map[string]any{"status": "expired"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": cr.status})
			// Cleanup after final status.
			if cr.status == "allowed" || cr.status == "denied" {
				sisiTGConfirmRequests.Delete(reqID)
			}
			return
		}

		// POST — create confirm request.
		if bot == nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": "telegram bot not configured"})
			return
		}

		tgIDint, _ := strconv.ParseInt(tgID, 10, 64)
		if tgIDint <= 0 {
			writeJSON(w, http.StatusOK, map[string]any{"error": "tg_id_not_available"})
			return
		}

		// Generate request ID.
		reqIDBytes := make([]byte, 8)
		_, _ = rand.Read(reqIDBytes)
		reqID := fmt.Sprintf("%x", reqIDBytes)

		cr := &sisiTGConfirmRequest{
			status:    "pending",
			expiresAt: time.Now().Add(2 * time.Minute),
		}
		sisiTGConfirmRequests.Store(reqID, cr)

		// Send TG message with inline buttons.
		bot.SendSisiConfirm(tgIDint, reqID)

		writeJSON(w, http.StatusOK, map[string]any{"request_id": reqID})
	}
}

// SisiConfirmCallback is called by the bot when user presses allow/deny.
func SisiConfirmCallback(reqID string, allowed bool) bool {
	raw, ok := sisiTGConfirmRequests.Load(reqID)
	if !ok {
		return false
	}
	cr := raw.(*sisiTGConfirmRequest)
	if time.Now().After(cr.expiresAt) {
		sisiTGConfirmRequests.Delete(reqID)
		return false
	}
	if allowed {
		cr.status = "allowed"
	} else {
		cr.status = "denied"
	}
	return true
}
