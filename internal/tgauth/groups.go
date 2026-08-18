package tgauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// UserGroup defines a set of permissions that apply to all users in the group.
type UserGroup struct {
	ID         string          `json:"id"`          // slug: "default", "premium", "vip"
	Name       string          `json:"name"`        // display: "Стандарт", "Премиум"
	IsDefault  bool            `json:"is_default"`  // new users go here
	MaxDevices int             `json:"max_devices"` // 0 = server default, -1 = unlimited
	TorrServer bool            `json:"torrserver"`  // TorrServer access (master on/off)
	Balancers  map[string]bool `json:"balancers"`   // plugin_key → allowed; empty = all allowed
	// StrictBalancers flips the Balancers map from denylist (default: a balancer
	// absent from a non-empty map is allowed — keeps dynamically-added JS-module
	// balancers visible) to allowlist (only balancers explicitly mapped to true
	// are visible; absent → denied). Opt-in per group; false = legacy behaviour.
	StrictBalancers bool            `json:"strict_balancers"`
	TorrServers     map[string]bool `json:"torrservers"` // backend ID → allowed; empty = all allowed
	SISI            bool            `json:"sisi"`        // 18+ content access
	Description     string          `json:"description"` // admin panel description
	CreatedAt       time.Time       `json:"created_at"`
}

var groupIDRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

// GroupStore manages user groups persisted to database/tgauth/groups.json.
type GroupStore struct {
	mu       sync.RWMutex
	groups   []UserGroup
	filePath string
}

// NewGroupStore creates or loads the group store.
// If the file doesn't exist, a default group is created.
func NewGroupStore(dbDir string) *GroupStore {
	s := &GroupStore{
		filePath: filepath.Join(dbDir, "database", "tgauth", "groups.json"),
	}
	_ = os.MkdirAll(filepath.Dir(s.filePath), 0o755)
	if err := s.load(); err != nil || len(s.groups) == 0 {
		s.groups = []UserGroup{defaultGroup()}
		s.saveLocked()
	}
	// Ensure exactly one default group exists.
	s.ensureDefault()
	return s
}

func defaultGroup() UserGroup {
	return UserGroup{
		ID:          "default",
		Name:        "Стандарт",
		IsDefault:   true,
		MaxDevices:  0, // inherit server default
		TorrServer:  true,
		Balancers:   map[string]bool{}, // empty = all allowed
		TorrServers: map[string]bool{}, // empty = all allowed
		SISI:        true,
		CreatedAt:   time.Now(),
	}
}

func (s *GroupStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.groups)
}

func (s *GroupStore) saveLocked() {
	data, _ := json.MarshalIndent(s.groups, "", "  ")
	_ = os.WriteFile(s.filePath, data, 0o644)
}

func (s *GroupStore) ensureDefault() {
	hasDefault := false
	for _, g := range s.groups {
		if g.IsDefault {
			hasDefault = true
			break
		}
	}
	if !hasDefault && len(s.groups) > 0 {
		s.groups[0].IsDefault = true
		s.saveLocked()
	}
}

// List returns all groups.
func (s *GroupStore) List() []UserGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]UserGroup, len(s.groups))
	copy(out, s.groups)
	return out
}

// Get returns a group by ID. If not found, returns the default group.
func (s *GroupStore) Get(id string) (UserGroup, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id == "" {
		return s.getDefaultLocked(), true
	}
	for _, g := range s.groups {
		if g.ID == id {
			return g, true
		}
	}
	return s.getDefaultLocked(), false
}

// GetDefault returns the default group.
func (s *GroupStore) GetDefault() UserGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getDefaultLocked()
}

func (s *GroupStore) getDefaultLocked() UserGroup {
	for _, g := range s.groups {
		if g.IsDefault {
			return g
		}
	}
	if len(s.groups) > 0 {
		return s.groups[0]
	}
	return defaultGroup()
}

// Create adds a new group.
func (s *GroupStore) Create(g UserGroup) error {
	if !groupIDRe.MatchString(g.ID) {
		return fmt.Errorf("invalid group ID: must be lowercase alphanumeric with - or _")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.groups {
		if existing.ID == g.ID {
			return fmt.Errorf("group %q already exists", g.ID)
		}
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	if g.Balancers == nil {
		g.Balancers = map[string]bool{}
	}
	if g.TorrServers == nil {
		g.TorrServers = map[string]bool{}
	}
	s.groups = append(s.groups, g)
	s.saveLocked()
	return nil
}

// Update replaces an existing group (matched by ID).
func (s *GroupStore) Update(g UserGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, existing := range s.groups {
		if existing.ID == g.ID {
			g.CreatedAt = existing.CreatedAt
			g.IsDefault = existing.IsDefault // can't change default via Update
			if g.Balancers == nil {
				g.Balancers = map[string]bool{}
			}
			if g.TorrServers == nil {
				g.TorrServers = map[string]bool{}
			}
			s.groups[i] = g
			s.saveLocked()
			return nil
		}
	}
	return fmt.Errorf("group %q not found", g.ID)
}

// Delete removes a group. Cannot delete the default group.
func (s *GroupStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, g := range s.groups {
		if g.ID == id {
			if g.IsDefault {
				return fmt.Errorf("cannot delete the default group")
			}
			s.groups = append(s.groups[:i], s.groups[i+1:]...)
			s.saveLocked()
			return nil
		}
	}
	return fmt.Errorf("group %q not found", id)
}

// SetDefault marks a group as default (and unsets previous default).
func (s *GroupStore) SetDefault(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	found := false
	for i := range s.groups {
		if s.groups[i].ID == id {
			s.groups[i].IsDefault = true
			found = true
		} else {
			s.groups[i].IsDefault = false
		}
	}
	if !found {
		return fmt.Errorf("group %q not found", id)
	}
	s.saveLocked()
	return nil
}

// BalancerAllowed checks if a balancer is allowed for the given group.
// Empty balancers map means all are allowed.
//
// Both the input plugin key and the keys stored in g.Balancers are run
// through CanonicalPluginKey before comparison. This handles three classes
// of lookup mismatch:
//
//  1. URL-path aliases:       /lite/rhs → "rhsprem", /lite/filmixpro → "filmix"
//  2. "rc/" premium prefix:   /lite/rc/filmix → "filmix"
//  3. Legacy admin-panel keys: "anilibriaonline" → "anilibria"
//
// Without canonicalization, denying "rhsprem" leaves /lite/rhs reachable,
// and saving "AnilibriaOnline" via the admin UI stores a key that
// "/lite/anilibria" never hits.
func (g *UserGroup) BalancerAllowed(plugin string) bool {
	if len(g.Balancers) == 0 {
		return true // empty = all allowed
	}
	canonical := CanonicalPluginKey(plugin)
	if canonical == "" {
		return true
	}

	// Fast path: direct canonical-keyed lookup.
	if allowed, exists := g.Balancers[canonical]; exists {
		return allowed
	}

	// Legacy / mixed-key path: scan stored keys and canonicalize them.
	// Supports groups.json files written before CanonicalPluginKey existed,
	// where a key like "anilibriaonline" or "rhs" may be present instead of
	// the canonical form.
	for key, val := range g.Balancers {
		if CanonicalPluginKey(key) == canonical {
			return val
		}
	}

	// Not in the map (even after canonicalization). In the default denylist mode
	// that means "allowed" (keeps dynamically-added balancers visible). In strict
	// allowlist mode it means "denied" (only explicitly-true keys are visible).
	return !g.StrictBalancers
}

// TorrServerAllowed reports whether the TorrServer backend with the given ID is
// usable by this group. Strict allowlist semantics: an empty map means every
// backend is allowed; a non-empty map allows ONLY the backends explicitly
// mapped to true.
//
// This is intentionally stricter than BalancerAllowed (which treats a key
// absent from a non-empty map as allowed). The admin chip UI only ever writes
// `true` entries for selected servers, so "select servers X, Y" must restrict
// the group to exactly X and Y — that is what makes per-group server isolation
// (e.g. premium-only backends) actually take effect.
func (g *UserGroup) TorrServerAllowed(id string) bool {
	if len(g.TorrServers) == 0 {
		return true // empty = all allowed
	}
	return g.TorrServers[id]
}

// CountByGroup returns a map of group_id → number of users.
func CountByGroup(tokens []ApprovedToken) map[string]int {
	counts := map[string]int{}
	for _, t := range tokens {
		gid := t.GroupID
		if gid == "" {
			gid = "default"
		}
		counts[gid]++
	}
	return counts
}
