package tgauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ErrUIDCrossToken is returned by AddDevice when the UID is already bound to
// a different token (cub-backup-clones-UID case). The caller should signal the
// client to regenerate its UID instead of silently sharing it across accounts.
var ErrUIDCrossToken = errors.New("uid already bound to another token")

// DeviceInfo describes a single bound device (identified by lampac_unic_id).
type DeviceInfo struct {
	UID         string    `json:"uid"`   // lampac_unic_id (8 chars)
	Label       string    `json:"label"` // "Lampa TV", "Chrome", etc.
	BoundAt     time.Time `json:"bound_at"`
	LastSeen    time.Time `json:"last_seen"`
	LastIP      string    `json:"last_ip,omitempty"`     // last known IP address
	Fingerprint string    `json:"fingerprint,omitempty"` // FNV-1a hash of the full device fingerprint (canvas/WebGL/audio — precise but can drift on firmware updates)
	// StableFP is a coarse anchor built from attributes that DON'T drift
	// (screen geometry, hardwareConcurrency, platform, timezone) folded with a
	// native device id (webOS LGUDID / Tizen DUID / Android bridge) when the
	// platform exposes one. It survives a full client wipe (localStorage +
	// cookies) that kills the UID and the durable token anchor — the only thing
	// left to re-identify a physical TV. Looked up ONLY when the precise
	// Fingerprint fails, and only when it maps to a single account.
	StableFP string `json:"stable_fp,omitempty"`
}

// ApprovedToken represents an authorized device token created after admin approval.
//
// Subscription model has TWO independent timers:
//
//   - ExpiresAt   — until when the token is valid AT ALL. Set by
//     auto-approve (e.g. 365 days from /start). When this
//     passes, the token is purged and the user is logged out.
//   - PremiumUntil — until when the user is in the PREMIUM tier. Set when
//     they buy a paid subscription. When this passes,
//     GetEffectiveGroup falls back to GroupID (= the user's
//     base tier, typically default).
//
// Example: user joins (ExpiresAt = now+365d), buys 90 days of premium
// (PremiumUntil = now+90d). Effective group is "premium" for the next
// 90 days; then automatically falls back to GroupID (default) for the
// remaining 275 days. Base access continues for the full year regardless
// of premium status.
type ApprovedToken struct {
	Token              string       `json:"token"`
	TelegramID         int64        `json:"telegram_id"`
	TGUsername         string       `json:"tg_username"`
	CreatedAt          time.Time    `json:"created_at"`
	ExpiresAt          time.Time    `json:"expires_at"`
	ApprovedBy         int64        `json:"approved_by"`
	Devices            []DeviceInfo `json:"devices,omitempty"`
	MaxDevices         int          `json:"max_devices,omitempty"`         // personal device limit (0 = use server default)
	TorrServerDisabled bool         `json:"torrserver_disabled,omitempty"` // if true, user cannot use TorrServer
	GroupID            string       `json:"group_id,omitempty"`            // BASE group (empty = default group); shown when PremiumUntil has passed
	PremiumUntil       time.Time    `json:"premium_until,omitempty"`       // when premium overlay expires; zero = not in premium
	// CubUserID links a CUB (cub.red) account to this token. CUB accounts
	// outlive any client-side state — after a full TV wipe the user re-enters
	// CUB with a short site code, and the gate recovers the binding through
	// this anchor instead of the TG bot flow. Learned passively when an
	// authorized device presents a CUB token; looked up only via the
	// ambiguity-guarded FindTokenByCubUser.
	CubUserID string `json:"cub_user_id,omitempty"`
	CubEmail  string `json:"cub_email,omitempty"` // display/debug only, never used for lookups
	// CubAutoLinkOptOut is set when the user explicitly unlinks CUB. Without
	// it the passive learner would silently re-link on the very next boot
	// (the device still presents its CUB token). Cleared by an explicit
	// re-link (SetCubUser); honored only by the passive path (SetCubUserAuto).
	CubAutoLinkOptOut bool `json:"cub_optout,omitempty"`
}

// EffectiveGroupID returns the group the user is effectively in, taking
// the premium overlay into account. Same semantics as Store.GetEffectiveGroup
// but available directly on the value-typed ApprovedToken — useful when
// the caller already has a token snapshot from Lookup/FindByTelegramID
// and doesn't want a second store read.
func (t *ApprovedToken) EffectiveGroupID(premiumGroupID string) string {
	if premiumGroupID != "" && !t.PremiumUntil.IsZero() && t.PremiumUntil.After(time.Now().UTC()) {
		return premiumGroupID
	}
	return t.GroupID
}

// Store persists approved tokens to a JSON file and provides thread-safe lookups.
// Optimized for scale: O(1) lookups via indexes, batched file writes.
type Store struct {
	mu       sync.RWMutex
	tokens   []ApprovedToken
	filePath string

	// --- Indexes for O(1) lookups (rebuilt on load/mutation) ---
	byToken    map[string]int // token string → index in tokens slice
	byTGID     map[int64]int  // telegram_id → index in tokens slice (first non-expired)
	byUID      map[string]int // device UID → index in tokens slice
	byFP       map[string]int // device fingerprint → index in tokens slice
	byStableFP map[string]int // device stable fingerprint → index in tokens slice
	byCubUID   map[string]int // CUB user id → index in tokens slice

	// --- Batched persistence ---
	dirty    bool          // true if in-memory state differs from disk
	saveMu   sync.Mutex    // serializes file writes
	saveStop chan struct{} // signals the background saver to stop
	saveDone chan struct{} // closed when backgroundSaver returns
}

// NewStore creates a Store backed by database/tgauth/tokens.json inside dbDir.
// Existing tokens are loaded from disk on creation; duplicates are merged.
func NewStore(dbDir string) *Store {
	s := &Store{
		filePath:   filepath.Join(dbDir, "database", "tgauth", "tokens.json"),
		byToken:    make(map[string]int),
		byTGID:     make(map[int64]int),
		byUID:      make(map[string]int),
		byFP:       make(map[string]int),
		byStableFP: make(map[string]int),
		byCubUID:   make(map[string]int),
		saveStop:   make(chan struct{}),
		saveDone:   make(chan struct{}),
	}
	if err := s.load(); err != nil {
		log.Warn().Err(err).Str("path", s.filePath).Msg("tgauth: failed to load tokens (starting empty)")
	}
	log.Info().Int("count", len(s.tokens)).Str("path", s.filePath).Msg("tgauth: loaded tokens")
	s.rebuildIndexes()
	if n := s.MergeByTelegramID(); n > 0 {
		log.Info().Int("removed", n).Msg("tgauth: merged duplicate tokens")
	}
	// Start background saver: flushes dirty state to disk every 10s.
	go s.backgroundSaver()
	return s
}

// backgroundSaver periodically flushes dirty in-memory state to disk.
func (s *Store) backgroundSaver() {
	defer close(s.saveDone)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.flushIfDirty()
		case <-s.saveStop:
			s.flushIfDirty() // final flush
			return
		}
	}
}

// flushIfDirty writes tokens to disk if the in-memory state has changed.
// Holds mu.Lock() during the entire operation (serialize + write) to prevent
// saveNow from interleaving and producing stale overwrites.
func (s *Store) flushIfDirty() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return
	}
	data, err := json.MarshalIndent(s.tokens, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("tgauth: failed to marshal tokens for batch save")
		return
	}
	s.dirty = false
	s.saveMu.Lock()
	err = writeAtomic(s.filePath, data)
	s.saveMu.Unlock()
	if err != nil {
		log.Error().Err(err).Msg("tgauth: batch save failed")
	}
}

// Close stops the background saver and flushes any pending changes.
// Blocking — returns after the final flush has hit disk, so tests using
// t.TempDir() can rely on no stray writes racing with cleanup.
func (s *Store) Close() {
	select {
	case <-s.saveStop:
		// already closed by an earlier call
	default:
		close(s.saveStop)
	}
	<-s.saveDone
}

// rebuildIndexes rebuilds all O(1) lookup maps from the tokens slice.
// Must be called with s.mu held (write lock).
func (s *Store) rebuildIndexes() {
	s.byToken = make(map[string]int, len(s.tokens))
	s.byTGID = make(map[int64]int, len(s.tokens))
	s.byUID = make(map[string]int, len(s.tokens)*2)
	s.byFP = make(map[string]int, len(s.tokens)*2)
	s.byStableFP = make(map[string]int, len(s.tokens)*2)
	s.byCubUID = make(map[string]int, len(s.tokens))

	now := time.Now().UTC()
	for i := range s.tokens {
		t := &s.tokens[i]
		if t.Token != "" {
			s.byToken[t.Token] = i
		}
		if t.CubUserID != "" {
			// Presence marker only; cross-account ambiguity is handled at
			// lookup time (real scan), same as byFP/byStableFP.
			s.byCubUID[t.CubUserID] = i
		}
		if t.TelegramID != 0 && now.Before(t.ExpiresAt) {
			// First non-expired token for this TG ID wins.
			if _, exists := s.byTGID[t.TelegramID]; !exists {
				s.byTGID[t.TelegramID] = i
			}
		}
		for _, d := range t.Devices {
			if d.UID != "" {
				s.byUID[d.UID] = i
			}
			if d.Fingerprint != "" {
				// Keep last-seen entry for fingerprint (within same token = same user).
				// Cross-token ambiguity is handled at lookup time (real scan).
				s.byFP[d.Fingerprint] = i
			}
			if d.StableFP != "" {
				s.byStableFP[d.StableFP] = i
			}
		}
	}
}

// markDirty marks in-memory state as changed (will be flushed by backgroundSaver).
func (s *Store) markDirty() {
	s.dirty = true
}

// saveNow persists immediately (for critical operations like Add/Remove).
// Must be called with s.mu held (write lock).
func (s *Store) saveNow() error {
	s.dirty = false
	data, err := json.MarshalIndent(s.tokens, "", "  ")
	if err != nil {
		return err
	}
	// Serialize file writes to prevent flushIfDirty from overwriting with stale data.
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return writeAtomic(s.filePath, data)
}

// writeAtomic writes data to a temp file then renames, preventing corruption
// if the process is killed mid-write.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --------------- Lookup (O(1) via indexes) ---------------

// LookupAuth satisfies auth.TGTokenChecker — returns basic fields for the auth middleware.
func (s *Store) LookupAuth(token string) (telegramID int64, expiresAt time.Time, ok bool) {
	t, found := s.Lookup(token)
	if !found {
		return 0, time.Time{}, false
	}
	return t.TelegramID, t.ExpiresAt, true
}

// LookupAuthByDeviceUID satisfies auth.TGDeviceUIDChecker — resolves
// a user by a bound device UID. Used by the middleware as a fallback
// when the request carries `?uid=` but the auth cookie was eaten by
// cross-origin XHR. Returns the device's owning token's TG ID + expiry.
func (s *Store) LookupAuthByDeviceUID(uid string) (telegramID int64, expiresAt time.Time, ok bool) {
	if uid == "" {
		return 0, time.Time{}, false
	}
	tok, _, found := s.FindDeviceByUID(uid)
	if !found || tok == "" {
		return 0, time.Time{}, false
	}
	return s.LookupAuth(tok)
}

// Lookup returns the token entry if it exists and has not expired.
func (s *Store) Lookup(token string) (*ApprovedToken, bool) {
	if token == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byToken[token]
	if !ok || idx >= len(s.tokens) {
		// Log only once per unknown token (avoid flooding).
		prefix := token
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}
		log.Debug().Str("token_prefix", prefix).Int("store_size", len(s.tokens)).Int("index_size", len(s.byToken)).Msg("tgauth: Lookup miss (token not in index)")
		return nil, false
	}
	if time.Now().UTC().After(s.tokens[idx].ExpiresAt) {
		return nil, false
	}
	t := s.tokens[idx] // copy
	return &t, true
}

// FindByTelegramID returns the first non-expired token for the given TG user, or nil.
func (s *Store) FindByTelegramID(tgID int64) *ApprovedToken {
	if tgID == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byTGID[tgID]
	if !ok || idx >= len(s.tokens) {
		return nil
	}
	if time.Now().UTC().After(s.tokens[idx].ExpiresAt) {
		return nil
	}
	t := s.tokens[idx] // copy
	return &t
}

// FindTokenByDeviceUID returns the token string that has the given uid bound, or empty.
func (s *Store) FindTokenByDeviceUID(uid string) string {
	if uid == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byUID[uid]
	if !ok || idx >= len(s.tokens) {
		return ""
	}
	if time.Now().UTC().After(s.tokens[idx].ExpiresAt) {
		return ""
	}
	return s.tokens[idx].Token
}

// FindDeviceByUID returns (token, device, ok) for the device that has the given UID.
// Returns ok=false if the UID is not bound or its token has expired.
// The returned DeviceInfo is a copy — safe to inspect without holding the lock.
func (s *Store) FindDeviceByUID(uid string) (string, *DeviceInfo, bool) {
	if uid == "" {
		return "", nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byUID[uid]
	if !ok || idx >= len(s.tokens) {
		return "", nil, false
	}
	if time.Now().UTC().After(s.tokens[idx].ExpiresAt) {
		return "", nil, false
	}
	for j := range s.tokens[idx].Devices {
		if s.tokens[idx].Devices[j].UID == uid {
			d := s.tokens[idx].Devices[j] // copy
			return s.tokens[idx].Token, &d, true
		}
	}
	return "", nil, false
}

// FindTokenByDeviceFingerprint scans all non-expired tokens for a device
// matching the given fingerprint. Returns (token, device, true) only if
// exactly one DISTINCT token matches: this is the last line of defence for a
// fully-wiped client (no cookie, no localStorage), so an ambiguous
// fingerprint must never silently re-auth as someone else's account. The
// byFP index keeps only the last writer and cannot detect collisions — do a
// real scan (the token list is a user fleet, tens to low thousands; fine).
func (s *Store) FindTokenByDeviceFingerprint(fp string) (string, *DeviceInfo, bool) {
	if fp == "" {
		return "", nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Fast path: no index entry at all → no device ever wrote this fp.
	if _, ok := s.byFP[fp]; !ok {
		return "", nil, false
	}
	now := time.Now().UTC()
	var foundToken string
	var foundDev *DeviceInfo
	for i := range s.tokens {
		if now.After(s.tokens[i].ExpiresAt) {
			continue
		}
		for j := range s.tokens[i].Devices {
			if s.tokens[i].Devices[j].Fingerprint != fp {
				continue
			}
			if foundToken != "" && foundToken != s.tokens[i].Token {
				// Same fingerprint on two different accounts — ambiguous,
				// refuse to auto-restore either.
				return "", nil, false
			}
			if foundToken == "" {
				d := s.tokens[i].Devices[j] // copy
				foundToken = s.tokens[i].Token
				foundDev = &d
			}
		}
	}
	if foundToken == "" {
		return "", nil, false
	}
	return foundToken, foundDev, true
}

// FindTokenByStableFP recovers a token from the coarse, drift-resistant
// fingerprint (screen/hardware/native-device-id). Same single-account
// ambiguity guard as FindTokenByDeviceFingerprint: because StableFP is
// deliberately less unique (two identical TVs with no native id collide), the
// guard is what keeps it safe — an ambiguous match restores nobody rather than
// the wrong account. It's the last-resort anchor after a full client wipe.
func (s *Store) FindTokenByStableFP(sfp string) (string, *DeviceInfo, bool) {
	if sfp == "" {
		return "", nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.byStableFP[sfp]; !ok {
		return "", nil, false
	}
	now := time.Now().UTC()
	var foundToken string
	var foundDev *DeviceInfo
	for i := range s.tokens {
		if now.After(s.tokens[i].ExpiresAt) {
			continue
		}
		for j := range s.tokens[i].Devices {
			if s.tokens[i].Devices[j].StableFP != sfp {
				continue
			}
			if foundToken != "" && foundToken != s.tokens[i].Token {
				return "", nil, false // ambiguous across accounts
			}
			if foundToken == "" {
				d := s.tokens[i].Devices[j] // copy
				foundToken = s.tokens[i].Token
				foundDev = &d
			}
		}
	}
	if foundToken == "" {
		return "", nil, false
	}
	return foundToken, foundDev, true
}

// FindTokenByCubUser recovers a token from a linked CUB account id. CUB is
// the longest-lived anchor (a server-side account, not client state), but the
// same ambiguity guard applies: one CUB account observed on two different
// alpac accounts (family sharing, resold TV) restores nobody rather than the
// wrong user.
func (s *Store) FindTokenByCubUser(cubUID string) (string, bool) {
	if cubUID == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.byCubUID[cubUID]; !ok {
		return "", false
	}
	now := time.Now().UTC()
	var foundToken string
	for i := range s.tokens {
		if now.After(s.tokens[i].ExpiresAt) {
			continue
		}
		if s.tokens[i].CubUserID != cubUID {
			continue
		}
		if foundToken != "" && foundToken != s.tokens[i].Token {
			return "", false // ambiguous across accounts
		}
		foundToken = s.tokens[i].Token
	}
	if foundToken == "" {
		return "", false
	}
	return foundToken, true
}

// SetCubUser explicitly links a CUB account to the token, replacing any
// previous link and clearing an unlink opt-out. Reports whether anything
// changed.
func (s *Store) SetCubUser(token, cubUID, email string) bool {
	return s.setCubUser(token, cubUID, email, false)
}

// SetCubUserAuto is the passive-learning variant: it refuses to re-link an
// account whose user explicitly unlinked CUB (CubAutoLinkOptOut).
func (s *Store) SetCubUserAuto(token, cubUID, email string) bool {
	return s.setCubUser(token, cubUID, email, true)
}

func (s *Store) setCubUser(token, cubUID, email string, auto bool) bool {
	if token == "" || cubUID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.byToken[token]
	if !ok {
		return false
	}
	if auto && s.tokens[i].CubAutoLinkOptOut {
		return false
	}
	// The passive path only fills an EMPTY anchor (or refreshes the same
	// one). Replacing a different existing link is explicit-only: otherwise
	// two CUB accounts seen on the same user's devices ping-pong the anchor,
	// and a guest's CUB login on your TV would silently steal it.
	if auto && s.tokens[i].CubUserID != "" && s.tokens[i].CubUserID != cubUID {
		return false
	}
	if s.tokens[i].CubUserID == cubUID && s.tokens[i].CubEmail == email {
		// Already linked identically. Still a change for the explicit path
		// when there's an opt-out to clear or a duplicate to steal below.
		duplicate := false
		for j := range s.tokens {
			if j != i && s.tokens[j].CubUserID == cubUID {
				duplicate = true
				break
			}
		}
		if auto || (!s.tokens[i].CubAutoLinkOptOut && !duplicate) {
			return false
		}
	}
	s.tokens[i].CubUserID = cubUID
	s.tokens[i].CubEmail = email
	if !auto {
		s.tokens[i].CubAutoLinkOptOut = false
		// Explicit link claims the anchor: strip the same CUB id from every
		// other token. A stale duplicate (old test account, re-sold device)
		// otherwise makes the anchor permanently ambiguous — the collision
		// guard refuses BOTH tokens and the user has no way to fix it. The
		// passive path never steals; only a deliberate button press does.
		for j := range s.tokens {
			if j != i && s.tokens[j].CubUserID == cubUID {
				s.tokens[j].CubUserID = ""
				s.tokens[j].CubEmail = ""
			}
		}
	}
	s.rebuildIndexes()
	s.markDirty()
	return true
}

// CubLinkStats reports how many tokens currently carry the given CUB id —
// split by active/expired. Diagnostic for the recovery path: 0 active means
// "not linked", 2+ means "ambiguous, collision guard refuses".
func (s *Store) CubLinkStats(cubUID string) (active, expired int) {
	if cubUID == "" {
		return 0, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	for i := range s.tokens {
		if s.tokens[i].CubUserID != cubUID {
			continue
		}
		if now.After(s.tokens[i].ExpiresAt) {
			expired++
		} else {
			active++
		}
	}
	return active, expired
}

// ClearCubUser removes the CUB link from a token (user-initiated unlink).
// Reports whether a link was actually removed.
func (s *Store) ClearCubUser(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.byToken[token]
	if !ok || s.tokens[i].CubUserID == "" {
		return false
	}
	s.tokens[i].CubUserID = ""
	s.tokens[i].CubEmail = ""
	// Opt out of passive re-linking — the device still presents its CUB
	// token on every boot, and without this flag the learner would undo the
	// unlink immediately. An explicit SetCubUser clears the flag.
	s.tokens[i].CubAutoLinkOptOut = true
	// Full rebuild instead of a map delete: another token may share the same
	// CubUserID (previously-ambiguous link) and must keep its index entry.
	s.rebuildIndexes()
	s.markDirty()
	return true
}

// GetCubUser returns the linked CUB account id and email for a token.
func (s *Store) GetCubUser(token string) (cubUID, email string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if i, ok := s.byToken[token]; ok {
		return s.tokens[i].CubUserID, s.tokens[i].CubEmail
	}
	return "", ""
}

// --------------- CRUD ---------------

// Add persists a new approved token.
func (s *Store) Add(t ApprovedToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, t)
	s.rebuildIndexes()
	return s.saveNow() // critical: persist immediately
}

// Remove deletes a token by its value.
func (s *Store) Remove(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return nil
	}
	s.tokens = append(s.tokens[:idx], s.tokens[idx+1:]...)
	s.rebuildIndexes()
	return s.saveNow() // critical: persist immediately
}

// List returns a copy of all tokens (including expired ones).
func (s *Store) List() []ApprovedToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ApprovedToken, len(s.tokens))
	copy(out, s.tokens)
	return out
}

// Extend adds the given number of days to a token's expiration.
func (s *Store) Extend(token string, days int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return nil
	}
	s.tokens[idx].ExpiresAt = s.tokens[idx].ExpiresAt.Add(time.Duration(days) * 24 * time.Hour)
	if s.tokens[idx].ExpiresAt.Before(time.Now().UTC()) {
		s.tokens[idx].ExpiresAt = time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
	}
	s.rebuildIndexes()
	s.markDirty()
	return nil
}

// CleanExpired removes all expired tokens and returns how many were removed.
func (s *Store) CleanExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	var kept []ApprovedToken
	for _, t := range s.tokens {
		if now.Before(t.ExpiresAt) {
			kept = append(kept, t)
		}
	}
	removed := len(s.tokens) - len(kept)
	if removed > 0 {
		s.tokens = kept
		s.rebuildIndexes()
		s.markDirty()
	}
	return removed
}

// --------------- Device management ---------------

// HasDevice checks whether the given uid is in the Devices list of the specified token.
func (s *Store) HasDevice(token, uid string) bool {
	if token == "" || uid == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byToken[token]
	if !ok {
		return false
	}
	for _, d := range s.tokens[idx].Devices {
		if d.UID == uid {
			return true
		}
	}
	return false
}

// HasDeviceByTGID checks whether any token of the given TG user has the uid bound.
func (s *Store) HasDeviceByTGID(tgID int64, uid string) bool {
	if tgID == 0 || uid == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Check UID index first — O(1).
	uidIdx, ok := s.byUID[uid]
	if !ok {
		return false
	}
	return s.tokens[uidIdx].TelegramID == tgID
}

// FindDeviceByFingerprint returns the first device with matching fingerprint
// within the given token's device list, or nil if not found.
func (s *Store) FindDeviceByFingerprint(token, fp string) *DeviceInfo {
	if token == "" || fp == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byToken[token]
	if !ok {
		return nil
	}
	for j := range s.tokens[idx].Devices {
		if s.tokens[idx].Devices[j].Fingerprint == fp {
			d := s.tokens[idx].Devices[j] // copy
			return &d
		}
	}
	return nil
}

// FindDeviceByLabel returns the first device with matching label.
// Used for label-based dedup when fingerprint doesn't match or is absent.
func (s *Store) FindDeviceByLabel(token, label string) *DeviceInfo {
	if token == "" || label == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byToken[token]
	if !ok {
		return nil
	}
	for j := range s.tokens[idx].Devices {
		if s.tokens[idx].Devices[j].Label == label {
			d := s.tokens[idx].Devices[j] // copy
			return &d
		}
	}
	return nil
}

// AddDevice appends a device to the token's Devices list and persists.
// Returns (true, nil) if the device was added, (false, nil) if it was
// already bound (duplicate UID).
// If a device with the same fingerprint already exists under a different UID,
// the existing device is updated (UID migrated) instead of creating a duplicate.
//
// If the UID is already bound to a different token (cross-token collision —
// typically a cub-backup-restored UID being silently re-bound to another
// account), AddDevice returns (false, ErrUIDCrossToken) without modifying
// state. The caller should signal the client to regenerate its UID.
func (s *Store) AddDevice(token string, dev DeviceInfo) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return false, nil
	}
	// Cross-token UID collision: refuse to bind a UID that already belongs
	// to a different token. Without this check, byUID gets overwritten and
	// FindTokenByDeviceUID returns whichever token mutated last — which causes
	// silent account-hopping after cub backup restore.
	if dev.UID != "" {
		if otherIdx, exists := s.byUID[dev.UID]; exists && otherIdx != idx {
			return false, ErrUIDCrossToken
		}
	}
	// Check for duplicate UID.
	for i, d := range s.tokens[idx].Devices {
		if d.UID == dev.UID {
			// Update fingerprint(s)/label if provided.
			changed := false
			if dev.Fingerprint != "" && d.Fingerprint != dev.Fingerprint {
				s.tokens[idx].Devices[i].Fingerprint = dev.Fingerprint
				changed = true
			}
			if dev.StableFP != "" && d.StableFP != dev.StableFP {
				s.tokens[idx].Devices[i].StableFP = dev.StableFP
				changed = true
			}
			if changed {
				s.tokens[idx].Devices[i].LastSeen = dev.LastSeen
				s.rebuildIndexes()
				s.markDirty()
			}
			return false, nil // already bound
		}
	}
	// Fingerprint dedup: if the same precise OR stable fingerprint exists under
	// a different UID, migrate it (same physical device that lost its UID).
	// Stable-fp is checked too so a firmware update that shifted the precise
	// canvas/WebGL fp still re-attaches to the existing device record instead
	// of piling up a duplicate on every relaunch.
	if dev.Fingerprint != "" || dev.StableFP != "" {
		for i, d := range s.tokens[idx].Devices {
			match := (dev.Fingerprint != "" && d.Fingerprint == dev.Fingerprint) ||
				(dev.StableFP != "" && d.StableFP == dev.StableFP)
			if !match {
				continue
			}
			// Same physical device, different UID — update UID instead of adding new.
			s.tokens[idx].Devices[i].UID = dev.UID
			s.tokens[idx].Devices[i].LastSeen = dev.LastSeen
			if dev.Fingerprint != "" {
				s.tokens[idx].Devices[i].Fingerprint = dev.Fingerprint
			}
			if dev.StableFP != "" {
				s.tokens[idx].Devices[i].StableFP = dev.StableFP
			}
			if dev.LastIP != "" {
				s.tokens[idx].Devices[i].LastIP = dev.LastIP
			}
			if dev.Label != "" {
				s.tokens[idx].Devices[i].Label = dev.Label
			}
			s.rebuildIndexes()
			s.markDirty()
			return false, nil // migrated, not new
		}
	}

	// Label-based dedup — ONLY between fingerprint-less entries. When fingerprints
	// exist and differ, the devices are provably distinct hardware: merging by label
	// collapsed every same-platform pair (two Android phones) into one slot that the
	// devices then stole from each other on every request (ping-pong migrations, a bot
	// notification per source switch, device count stuck at 1).
	if dev.Label != "" && dev.Fingerprint == "" {
		for i, d := range s.tokens[idx].Devices {
			if d.Label == dev.Label && d.Fingerprint == "" {
				// No fingerprints to tell them apart — assume the same physical device
				// reconnecting with a new UID (localStorage wiped).
				s.tokens[idx].Devices[i].UID = dev.UID
				s.tokens[idx].Devices[i].LastSeen = dev.LastSeen
				if dev.LastIP != "" {
					s.tokens[idx].Devices[i].LastIP = dev.LastIP
				}
				s.rebuildIndexes()
				s.markDirty()
				return false, nil // replaced, not new
			}
		}
	}

	s.tokens[idx].Devices = append(s.tokens[idx].Devices, dev)
	s.rebuildIndexes()
	s.markDirty()
	return true, nil
}

// RemoveDevice removes a device by uid from the token's Devices list.
// OnDeviceRemoved is called after a device is unbound, with the account's user
// id (tg:<id>) and the device UID. The account store hooks in here to forget
// that device's profile choice: a re-paired device must land on the profile
// picker again rather than silently resume where the previous owner left off.
// Set once at wiring; nil disables the hook.
var OnDeviceRemoved func(userID, uid string)

func (s *Store) RemoveDevice(token string, uid string) error {
	s.mu.Lock()
	idx, ok := s.byToken[token]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	tgID := s.tokens[idx].TelegramID
	devices := s.tokens[idx].Devices
	removed := false
	for j := range devices {
		if devices[j].UID == uid {
			s.tokens[idx].Devices = append(devices[:j], devices[j+1:]...)
			s.rebuildIndexes()
			s.markDirty()
			removed = true
			break
		}
	}
	s.mu.Unlock()

	// Outside the lock: the hook reaches into another store, and holding this
	// one across that call is how two stores deadlock on each other.
	if removed && OnDeviceRemoved != nil {
		OnDeviceRemoved(fmt.Sprintf("tg:%d", tgID), uid)
	}
	return nil
}

// RemoveAllDevices unbinds every device of an account and returns how many went
// away. "Отвязать все" is the button people actually want when a box is lost or
// sold — doing it one by one through a list of eight UIDs is the same operation
// with more chances to unbind the wrong one.
func (s *Store) RemoveAllDevices(token string) int {
	s.mu.Lock()
	idx, ok := s.byToken[token]
	if !ok {
		s.mu.Unlock()
		return 0
	}
	tgID := s.tokens[idx].TelegramID
	gone := make([]string, 0, len(s.tokens[idx].Devices))
	for _, d := range s.tokens[idx].Devices {
		gone = append(gone, d.UID)
	}
	s.tokens[idx].Devices = nil
	s.rebuildIndexes()
	s.markDirty()
	s.mu.Unlock()

	if OnDeviceRemoved != nil {
		for _, uid := range gone {
			OnDeviceRemoved(fmt.Sprintf("tg:%d", tgID), uid)
		}
	}
	return len(gone)
}

// MigrateDeviceUID atomically changes the UID of an existing device
// (fingerprint-based dedup after localStorage wipe). Updates LastSeen and LastIP.
func (s *Store) MigrateDeviceUID(token, oldUID, newUID, newIP string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return nil
	}
	// Ensure newUID is not already taken.
	for _, d := range s.tokens[idx].Devices {
		if d.UID == newUID {
			return nil // already exists — no-op
		}
	}
	for j := range s.tokens[idx].Devices {
		if s.tokens[idx].Devices[j].UID == oldUID {
			s.tokens[idx].Devices[j].UID = newUID
			s.tokens[idx].Devices[j].LastSeen = time.Now().UTC()
			if newIP != "" {
				s.tokens[idx].Devices[j].LastIP = newIP
			}
			s.rebuildIndexes()
			s.markDirty()
			return nil
		}
	}
	return nil
}

// RenameDevice changes the Label of a device identified by uid.
func (s *Store) RenameDevice(token, uid, newLabel string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return nil
	}
	for j := range s.tokens[idx].Devices {
		if s.tokens[idx].Devices[j].UID == uid {
			s.tokens[idx].Devices[j].Label = newLabel
			s.markDirty()
			return nil
		}
	}
	return nil
}

// SetMaxDevices sets a personal device limit for the token (0 = use server default).
func (s *Store) SetMaxDevices(token string, limit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return nil
	}
	s.tokens[idx].MaxDevices = limit
	s.markDirty()
	return nil
}

// GetMaxDevices returns the personal device limit for the token (0 = server default).
func (s *Store) GetMaxDevices(token string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byToken[token]
	if !ok {
		return 0
	}
	return s.tokens[idx].MaxDevices
}

// SetTorrServerDisabled sets whether TorrServer is disabled for the given token.
func (s *Store) SetTorrServerDisabled(token string, disabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return
	}
	s.tokens[idx].TorrServerDisabled = disabled
	s.markDirty()
}

// IsTorrServerDisabled returns true if TorrServer is disabled for the given token.
func (s *Store) IsTorrServerDisabled(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byToken[token]
	if !ok {
		return false
	}
	return s.tokens[idx].TorrServerDisabled
}

// SetGroupID assigns a user to a group.
func (s *Store) SetGroupID(token, groupID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return
	}
	s.tokens[idx].GroupID = groupID
	s.markDirty()
}

// GetGroupID returns the BASE group ID for a token (empty = default
// group). For access-control decisions you usually want GetEffectiveGroup
// — it accounts for the active premium overlay. GetGroupID is for admin
// UIs that want to display the base tier ignoring premium.
func (s *Store) GetGroupID(token string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byToken[token]
	if !ok {
		return ""
	}
	return s.tokens[idx].GroupID
}

// GetEffectiveGroup returns the group the user is currently in, taking
// the premium overlay into account.
//
//   - If PremiumUntil > now → premiumGroupID (the user is in premium)
//   - Else                  → GroupID (the user's base tier)
//
// Grandfathering: if GroupID was directly set to premiumGroupID (legacy
// behavior before PremiumUntil existed), the user stays in premium
// indefinitely — we don't strip that group, we just stop adding new
// tokens that way. Admins can fix legacy records by clearing GroupID
// and setting PremiumUntil via the admin tool.
//
// Token not found → returns "" (caller falls back to default group).
func (s *Store) GetEffectiveGroup(token, premiumGroupID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byToken[token]
	if !ok {
		return ""
	}
	t := &s.tokens[idx]
	if premiumGroupID != "" && !t.PremiumUntil.IsZero() && t.PremiumUntil.After(time.Now().UTC()) {
		return premiumGroupID
	}
	return t.GroupID
}

// ExtendPremium pushes the PremiumUntil timestamp forward by `days`
// from MAX(now, current PremiumUntil). This means:
//
//   - Premium not active or expired → starts a new premium period of
//     `days` from now.
//   - Premium currently active      → adds `days` on top of the
//     remaining premium time (stacking like a magazine subscription).
//
// Returns the new PremiumUntil so callers can include it in
// confirmation messages. Token not found → returns zero time.
func (s *Store) ExtendPremium(token string, days int) time.Time {
	if days <= 0 {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return time.Time{}
	}
	now := time.Now().UTC()
	base := s.tokens[idx].PremiumUntil
	if base.Before(now) {
		base = now
	}
	newUntil := base.Add(time.Duration(days) * 24 * time.Hour)
	s.tokens[idx].PremiumUntil = newUntil
	s.markDirty()
	return newUntil
}

// SetPremiumUntil is the explicit setter for the premium overlay timer —
// used by the admin UI when an operator wants to manually adjust premium
// (e.g. extend, shorten, or zero out). Pass time.Time{} to clear (drops
// the user back to their base GroupID on next access check).
func (s *Store) SetPremiumUntil(token string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return
	}
	s.tokens[idx].PremiumUntil = until.UTC()
	s.markDirty()
}

// GetPremiumUntil returns the current premium-overlay expiry, or zero
// time if not in premium. Used by admin UIs and notifications.
func (s *Store) GetPremiumUntil(token string) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byToken[token]
	if !ok {
		return time.Time{}
	}
	return s.tokens[idx].PremiumUntil
}

// DeviceCount returns the number of devices bound to the given token.
func (s *Store) DeviceCount(token string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byToken[token]
	if !ok {
		return 0
	}
	return len(s.tokens[idx].Devices)
}

// UpdateLastSeen updates the LastSeen timestamp for a device (in-memory only, no save).
func (s *Store) UpdateLastSeen(token, uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return
	}
	now := time.Now().UTC()
	for j := range s.tokens[idx].Devices {
		if s.tokens[idx].Devices[j].UID == uid {
			s.tokens[idx].Devices[j].LastSeen = now
			return
		}
	}
}

// UpdateDeviceIP updates the LastIP for a device (batched persist).
func (s *Store) UpdateDeviceIP(token, uid, ip string) {
	if token == "" || uid == "" || ip == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return
	}
	for j := range s.tokens[idx].Devices {
		if s.tokens[idx].Devices[j].UID == uid {
			if s.tokens[idx].Devices[j].LastIP != ip {
				s.tokens[idx].Devices[j].LastIP = ip
				s.markDirty()
			}
			return
		}
	}
}

// UpdateDeviceFingerprint updates the Fingerprint for a device (batched persist).
func (s *Store) UpdateDeviceFingerprint(token, uid, fp string) {
	if token == "" || uid == "" || fp == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return
	}
	for j := range s.tokens[idx].Devices {
		if s.tokens[idx].Devices[j].UID == uid {
			if s.tokens[idx].Devices[j].Fingerprint != fp {
				s.tokens[idx].Devices[j].Fingerprint = fp
				s.rebuildIndexes()
				s.markDirty()
			}
			return
		}
	}
}

// UpdateDeviceStableFP updates the coarse StableFP anchor for a device
// (batched persist). Kept separate from the precise fp so callers can refresh
// whichever they received without clobbering the other.
func (s *Store) UpdateDeviceStableFP(token, uid, sfp string) {
	if token == "" || uid == "" || sfp == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return
	}
	for j := range s.tokens[idx].Devices {
		if s.tokens[idx].Devices[j].UID == uid {
			if s.tokens[idx].Devices[j].StableFP != sfp {
				s.tokens[idx].Devices[j].StableFP = sfp
				s.rebuildIndexes()
				s.markDirty()
			}
			return
		}
	}
}

// --------------- Merge duplicates ---------------

// MergeByTelegramID deduplicates tokens: for each TelegramID with multiple tokens,
// keeps the one with the latest ExpiresAt and merges all Devices.
// Returns the number of removed duplicate tokens.
func (s *Store) MergeByTelegramID() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Group by TelegramID (skip TG ID 0 — device tokens created by admin)
	groups := make(map[int64][]int) // tgID → indices
	for i := range s.tokens {
		tgID := s.tokens[i].TelegramID
		if tgID == 0 {
			continue
		}
		groups[tgID] = append(groups[tgID], i)
	}

	removed := 0
	var keep []bool
	if len(groups) > 0 {
		keep = make([]bool, len(s.tokens))
		for i := range keep {
			keep[i] = true
		}
	} else {
		return 0
	}

	for _, indices := range groups {
		if len(indices) <= 1 {
			continue
		}
		// Find the token with max ExpiresAt
		bestIdx := indices[0]
		for _, idx := range indices[1:] {
			if s.tokens[idx].ExpiresAt.After(s.tokens[bestIdx].ExpiresAt) {
				bestIdx = idx
			}
		}
		// Merge devices from all others into best
		uidSet := make(map[string]bool)
		for _, d := range s.tokens[bestIdx].Devices {
			uidSet[d.UID] = true
		}
		for _, idx := range indices {
			if idx == bestIdx {
				continue
			}
			for _, d := range s.tokens[idx].Devices {
				if d.UID != "" && !uidSet[d.UID] {
					s.tokens[bestIdx].Devices = append(s.tokens[bestIdx].Devices, d)
					uidSet[d.UID] = true
				}
			}
			keep[idx] = false
			removed++
		}
	}

	if removed > 0 {
		var merged []ApprovedToken
		for i, t := range s.tokens {
			if keep[i] {
				merged = append(merged, t)
			}
		}
		s.tokens = merged
		s.rebuildIndexes()
		_ = s.saveNow()
	}
	return removed
}

// --------------- Persistence ---------------

func (s *Store) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	var tokens []ApprovedToken
	if err := json.Unmarshal(data, &tokens); err != nil {
		return err
	}
	s.tokens = tokens
	return nil
}

// save is kept for backward compatibility but now just marks dirty for batch save.
func (s *Store) save() error {
	s.markDirty()
	return nil
}
