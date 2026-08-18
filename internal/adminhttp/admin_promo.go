package adminhttp

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"lampac-go/internal/tgauth"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// tgAdminPromoHandler handles GET (list) and POST (generate/delete) for promo codes.
func tgAdminPromoHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, promoStore *tgauth.PromoStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			codes := promoStore.List()
			type promoRow struct {
				Code      string `json:"code"`
				Days      int    `json:"days"`
				MaxUses   int    `json:"max_uses"`
				UsedCount int    `json:"used_count"`
				ExpiresAt string `json:"expires_at"`
				CreatedAt string `json:"created_at"`
				Valid     bool   `json:"valid"`
			}
			rows := make([]promoRow, 0, len(codes))
			for _, c := range codes {
				exp := ""
				if !c.ExpiresAt.IsZero() {
					exp = c.ExpiresAt.Format("2006-01-02 15:04")
				}
				rows = append(rows, promoRow{
					Code:      c.Code,
					Days:      c.Days,
					MaxUses:   c.MaxUses,
					UsedCount: c.UsedCount,
					ExpiresAt: exp,
					CreatedAt: c.CreatedAt.Format("2006-01-02 15:04"),
					Valid:     c.IsValid(),
				})
			}
			writeJSON(w, http.StatusOK, rows)

		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 65536))
			var req struct {
				Action     string `json:"action"`
				Count      int    `json:"count"`       // how many codes to generate
				Days       int    `json:"days"`        // access duration in days
				MaxUses    int    `json:"max_uses"`    // 0 = single-use, >0 = multi-use
				ValidHours int    `json:"valid_hours"` // code validity in hours (0 = no expiry)
				Code       string `json:"code"`        // for delete action
			}
			if stdjson.Unmarshal(body, &req) != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}

			switch req.Action {
			case "generate":
				if req.Count <= 0 {
					req.Count = 1
				}
				if req.Count > 100 {
					req.Count = 100
				}
				if req.Days <= 0 {
					req.Days = 30
				}
				if req.MaxUses <= 0 {
					req.MaxUses = 1 // single-use by default
				}
				codes := promoStore.Generate(req.Count, req.Days, req.MaxUses, req.ValidHours, tid)
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":    true,
					"codes": codes,
				})

			case "delete":
				if req.Code == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code required"})
					return
				}
				promoStore.Delete(req.Code)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})

			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
			}

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// promoBruteForce tracks failed promo attempts per IP for auto-ban.
type promoBruteForce struct {
	mu       sync.Mutex
	attempts map[string]*promoAttemptInfo // key = IP
}

type promoAttemptInfo struct {
	count   int
	firstAt time.Time
	lastAt  time.Time
}

const (
	promoBruteMaxAttempts = 10              // max failed attempts before ban
	promoBruteWindow      = 5 * time.Minute // time window for counting attempts
	promoBruteCooldown    = 1 * time.Minute // min delay between responses after 5 failures
)

var promoBrute = &promoBruteForce{attempts: make(map[string]*promoAttemptInfo)}

func (bf *promoBruteForce) record(ip string) (count int, shouldBan bool) {
	bf.mu.Lock()
	defer bf.mu.Unlock()

	now := time.Now()
	info, ok := bf.attempts[ip]
	if !ok || now.Sub(info.firstAt) > promoBruteWindow {
		bf.attempts[ip] = &promoAttemptInfo{count: 1, firstAt: now, lastAt: now}
		return 1, false
	}
	info.count++
	info.lastAt = now
	return info.count, info.count >= promoBruteMaxAttempts
}

func (bf *promoBruteForce) isThrottled(ip string) bool {
	bf.mu.Lock()
	defer bf.mu.Unlock()
	info, ok := bf.attempts[ip]
	if !ok {
		return false
	}
	return info.count >= 5 && time.Since(info.lastAt) < promoBruteCooldown
}

func (bf *promoBruteForce) clear(ip string) {
	bf.mu.Lock()
	defer bf.mu.Unlock()
	delete(bf.attempts, ip)
}

// Periodic cleanup of stale entries (called from init goroutine).
func init() {
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			promoBrute.mu.Lock()
			now := time.Now()
			for ip, info := range promoBrute.attempts {
				if now.Sub(info.lastAt) > promoBruteWindow*2 {
					delete(promoBrute.attempts, ip)
				}
			}
			promoBrute.mu.Unlock()
		}
	}()
}

// PromoRedeemHandler validates a promo code and creates an approved token.
// POST /tg/auth/promo  {"code":"P-ABC123"}
// or GET /tg/auth/promo?code=P-ABC123 (for simplicity from auth page form)
//
// Brute-force protection: after 10 failed attempts in 5 minutes, the IP
// is automatically added to the ban list.
func PromoRedeemHandler(promoStore *tgauth.PromoStore, tokenStore *tgauth.Store, banStore *tgauth.BanStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)

		// Check if IP is already banned
		if banStore != nil {
			if banned, _ := banStore.IsBanned("", ip, 0, "", ""); banned {
				writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "blocked"})
				return
			}
		}

		// Throttle: delay response after 5+ failures
		if promoBrute.isThrottled(ip) {
			time.Sleep(2 * time.Second)
		}

		var code string
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			var req struct {
				Code string `json:"code"`
			}
			if stdjson.Unmarshal(body, &req) == nil {
				code = req.Code
			}
		}
		if code == "" {
			code = r.URL.Query().Get("code")
		}
		code = trimCode(code)
		if code == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing code"})
			return
		}

		// Generate a new token
		newToken := uuid.New().String()

		days, ok := promoStore.Redeem(code, newToken)
		if !ok {
			// Record failed attempt
			count, shouldBan := promoBrute.record(ip)
			if shouldBan && banStore != nil {
				_ = banStore.Add(tgauth.BanRule{
					ID:        uuid.New().String(),
					Type:      "ip",
					Value:     ip,
					Reason:    fmt.Sprintf("Brute-force промокодов: %d попыток за 5 мин", count),
					CreatedAt: time.Now().UTC(),
					CreatedBy: 0, // system
				})
				log.Warn().Str("ip", ip).Int("attempts", count).Msg("promo: IP banned for brute-force")
				promoBrute.clear(ip) // clear counter after ban
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"ok":    false,
				"error": "invalid_code",
			})
			return
		}

		// Success — clear brute-force counter
		promoBrute.clear(ip)

		approved := tgauth.ApprovedToken{
			Token:      newToken,
			TelegramID: 0, // no TG association
			TGUsername: "promo:" + code,
			CreatedAt:  time.Now().UTC(),
			ExpiresAt:  time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour),
			ApprovedBy: 0,
		}
		if err := tokenStore.Add(approved); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "internal",
			})
			return
		}

		// Set auth cookies
		setAuthCookies(w, r, newToken)

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    true,
			"token": newToken,
			"days":  days,
		})
	}
}

func trimCode(s string) string {
	// Trim whitespace
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\n' && s[i] != '\r' {
			out = append(out, s[i])
		}
	}
	return string(out)
}
