package tgauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// PromoCode represents a promo code for device registration without Telegram.
type PromoCode struct {
	Code      string    `json:"code"`
	Days      int       `json:"days"`       // access duration in days
	MaxUses   int       `json:"max_uses"`   // 0 = unlimited
	UsedCount int       `json:"used_count"` // how many times redeemed
	ExpiresAt time.Time `json:"expires_at"` // code validity (not access duration)
	CreatedAt time.Time `json:"created_at"`
	CreatedBy int64     `json:"created_by"` // admin TG ID
	UsedBy    []string  `json:"used_by"`    // list of generated tokens
}

// IsValid checks if the promo code can still be redeemed.
func (p *PromoCode) IsValid() bool {
	if !p.ExpiresAt.IsZero() && time.Now().UTC().After(p.ExpiresAt) {
		return false
	}
	if p.MaxUses > 0 && p.UsedCount >= p.MaxUses {
		return false
	}
	return true
}

// PromoStore persists promo codes to a JSON file.
type PromoStore struct {
	mu       sync.RWMutex
	codes    []PromoCode
	byCode   map[string]int // code string → index
	filePath string
}

// NewPromoStore creates a store backed by database/tgauth/promo_codes.json.
func NewPromoStore(dbDir string) *PromoStore {
	s := &PromoStore{
		filePath: filepath.Join(dbDir, "database", "tgauth", "promo_codes.json"),
		byCode:   make(map[string]int),
	}
	if err := s.load(); err != nil {
		log.Warn().Err(err).Str("path", s.filePath).Msg("promo: failed to load (starting empty)")
	}
	s.rebuildIndex()
	log.Info().Int("count", len(s.codes)).Msg("promo: loaded codes")
	return s
}

func (s *PromoStore) rebuildIndex() {
	s.byCode = make(map[string]int, len(s.codes))
	for i := range s.codes {
		s.byCode[s.codes[i].Code] = i
	}
}

// Generate creates n promo codes with the given parameters.
// Returns the list of generated codes.
func (s *PromoStore) Generate(n, days, maxUses int, validHours int, adminTGID int64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var expiresAt time.Time
	if validHours > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(validHours) * time.Hour)
	}

	generated := make([]string, 0, n)
	for range n {
		code := s.generateUniqueCode()
		pc := PromoCode{
			Code:      code,
			Days:      days,
			MaxUses:   maxUses,
			ExpiresAt: expiresAt,
			CreatedAt: time.Now().UTC(),
			CreatedBy: adminTGID,
		}
		s.codes = append(s.codes, pc)
		generated = append(generated, code)
	}
	s.rebuildIndex()
	_ = s.saveNow()
	return generated
}

// Redeem validates and uses a promo code. Returns (days, true) on success.
func (s *PromoStore) Redeem(code string, token string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.byCode[code]
	if !ok {
		return 0, false
	}
	pc := &s.codes[idx]
	if !pc.IsValid() {
		return 0, false
	}

	pc.UsedCount++
	pc.UsedBy = append(pc.UsedBy, token)
	_ = s.saveNow()
	return pc.Days, true
}

// Lookup returns a promo code by its code string.
func (s *PromoStore) Lookup(code string) (*PromoCode, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byCode[code]
	if !ok {
		return nil, false
	}
	pc := s.codes[idx] // copy
	return &pc, true
}

// List returns all promo codes.
func (s *PromoStore) List() []PromoCode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PromoCode, len(s.codes))
	copy(out, s.codes)
	return out
}

// Delete removes a promo code by its code string.
func (s *PromoStore) Delete(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byCode[code]
	if !ok {
		return false
	}
	s.codes = append(s.codes[:idx], s.codes[idx+1:]...)
	s.rebuildIndex()
	_ = s.saveNow()
	return true
}

func (s *PromoStore) generateUniqueCode() string {
	for range 100 {
		code := "P-" + randomAlphaNum(6)
		if _, exists := s.byCode[code]; !exists {
			return code
		}
	}
	return "P-" + randomAlphaNum(8)
}

func (s *PromoStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	var codes []PromoCode
	if err := json.Unmarshal(data, &codes); err != nil {
		return err
	}
	s.codes = codes
	return nil
}

func (s *PromoStore) saveNow() error {
	data, err := json.MarshalIndent(s.codes, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.filePath, data)
}
