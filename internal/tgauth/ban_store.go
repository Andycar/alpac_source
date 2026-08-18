package tgauth

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// BanRule represents a single blocking rule.
type BanRule struct {
	ID        string    `json:"id"`     // UUID
	Type      string    `json:"type"`   // "uid", "ip", "cidr", "tg_id", "fingerprint", "country"
	Value     string    `json:"value"`  // the blocked value
	Reason    string    `json:"reason"` // admin note
	CreatedAt time.Time `json:"created_at"`
	CreatedBy int64     `json:"created_by"` // admin TG ID
}

// BanStore persists ban rules to a JSON file and provides fast lookups via pre-computed indexes.
type BanStore struct {
	mu       sync.RWMutex
	rules    []BanRule
	filePath string

	// Pre-computed indexes (rebuilt on load/add/remove)
	uidMap         map[string]bool
	ipExact        map[string]bool
	cidrNets       []*net.IPNet
	tgIDMap        map[int64]bool
	fingerprintMap map[string]bool
	countrySet     map[string]bool
}

// NewBanStore creates a BanStore backed by database/tgauth/bans.json inside dbDir.
func NewBanStore(dbDir string) *BanStore {
	s := &BanStore{
		filePath: filepath.Join(dbDir, "database", "tgauth", "bans.json"),
	}
	_ = s.load()
	s.rebuildIndexes()
	log.Info().Int("rules", len(s.rules)).Msg("banstore: loaded")
	return s
}

// Add persists a new ban rule.
func (s *BanStore) Add(rule BanRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Deduplicate: don't add if same type+value exists
	for _, r := range s.rules {
		if r.Type == rule.Type && strings.EqualFold(r.Value, rule.Value) {
			return fmt.Errorf("ban already exists: %s=%s", rule.Type, rule.Value)
		}
	}

	s.rules = append(s.rules, rule)
	s.rebuildIndexesLocked()
	return s.save()
}

// Remove deletes a ban rule by ID.
func (s *BanStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rules {
		if r.ID == id {
			s.rules = append(s.rules[:i], s.rules[i+1:]...)
			s.rebuildIndexesLocked()
			return s.save()
		}
	}
	return nil
}

// List returns a copy of all ban rules.
func (s *BanStore) List() []BanRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]BanRule, len(s.rules))
	copy(out, s.rules)
	return out
}

// IsBanned checks whether the given identifiers match any ban rule.
// Returns (true, reason) if banned, (false, "") otherwise.
func (s *BanStore) IsBanned(uid, ip string, tgID int64, fingerprint, country string) (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// UID ban
	if uid != "" && s.uidMap[uid] {
		return true, "device banned (uid)"
	}

	// IP exact ban
	if ip != "" && s.ipExact[ip] {
		return true, "IP banned"
	}

	// CIDR ban
	if ip != "" && len(s.cidrNets) > 0 {
		parsedIP := net.ParseIP(ip)
		if parsedIP != nil {
			for _, cidr := range s.cidrNets {
				if cidr.Contains(parsedIP) {
					return true, "IP in banned range (" + cidr.String() + ")"
				}
			}
		}
	}

	// TG ID ban
	if tgID != 0 && s.tgIDMap[tgID] {
		return true, "Telegram ID banned"
	}

	// Fingerprint ban
	if fingerprint != "" && s.fingerprintMap[fingerprint] {
		return true, "device fingerprint banned"
	}

	// Country ban
	if country != "" && s.countrySet[strings.ToUpper(country)] {
		return true, "country blocked (" + country + ")"
	}

	return false, ""
}

// Count returns the total number of ban rules.
func (s *BanStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.rules)
}

// CountByType returns ban count per type.
func (s *BanStore) CountByType() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := make(map[string]int)
	for _, r := range s.rules {
		m[r.Type]++
	}
	return m
}

// rebuildIndexes rebuilds lookup indexes. Caller must hold s.mu for write.
func (s *BanStore) rebuildIndexesLocked() {
	s.uidMap = make(map[string]bool)
	s.ipExact = make(map[string]bool)
	s.cidrNets = nil
	s.tgIDMap = make(map[int64]bool)
	s.fingerprintMap = make(map[string]bool)
	s.countrySet = make(map[string]bool)

	for _, r := range s.rules {
		switch r.Type {
		case "uid":
			s.uidMap[r.Value] = true
		case "ip":
			s.ipExact[r.Value] = true
		case "cidr":
			_, ipNet, err := net.ParseCIDR(r.Value)
			if err == nil {
				s.cidrNets = append(s.cidrNets, ipNet)
			}
		case "tg_id":
			if id, err := strconv.ParseInt(r.Value, 10, 64); err == nil {
				s.tgIDMap[id] = true
			}
		case "fingerprint":
			s.fingerprintMap[r.Value] = true
		case "country":
			s.countrySet[strings.ToUpper(r.Value)] = true
		}
	}
}

// rebuildIndexes is a convenience method that acquires no lock (for init).
func (s *BanStore) rebuildIndexes() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rebuildIndexesLocked()
}

// --------------- Persistence ---------------

func (s *BanStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	var rules []BanRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return err
	}
	s.rules = rules
	return nil
}

func (s *BanStore) save() error {
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.rules, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0644)
}
