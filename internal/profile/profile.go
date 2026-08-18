// Package profile implements self-hosted user profiles for cross-device
// synchronization. A profile is an alternative identity to the TG-bot auth:
// users who don't want (or can't) authenticate via Telegram can register a
// username+password and use the resulting session cookie on every device
// to share bookmarks, timecodes, view history, etc.
//
// Two redemption mechanisms are offered to bring a second device into a
// profile:
//
//   1. Username + password — classic login, used when the user has a real
//      keyboard. Issues a session cookie valid for 90 days (rolling).
//
//   2. Sync code — short one-shot string (e.g. "QX7A-K3PM") generated on
//      device A and typed on device B with a TV remote. Bound to a single
//      profile, expires after 1 hour, single use. Convenient on devices
//      without a comfortable text input (TV set-top boxes, sticks).
//
// Both flows produce the same session token shape — once issued, downstream
// HTTP handlers see User{ID:"profile:<uuid>"} and the existing
// /storage, /bookmark, /timecode code paths key on that just like they do
// for TG accounts (tg:<id>).
//
// Persistence: JSON files in <repo_root>/database/profile/
//   - profiles.json    : id, username, bcrypt(password), created_at, last_login
//   - sessions.json    : token, profile_id, created_at, expires_at
//   - sync_codes.json  : code, profile_id, expires_at, used_at
//
// We follow the rest of the project's storage convention (see
// internal/tgauth/store.go, internal/httpapi/accsdb_api.go) instead of
// reaching for SQLite — file size stays bounded by the active user count
// and the access pattern is heavily read-dominated.
package profile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Sentinel errors returned by Store methods. Callers (HTTP handlers in
// particular) match on these so they can map specific failures to clean
// 4xx responses instead of leaking internal text.
var (
	ErrUsernameTaken      = errors.New("profile: username already registered")
	ErrUsernameInvalid    = errors.New("profile: username must be 3-32 characters (letters of any script, digits, _ or -)")
	ErrPasswordWeak       = errors.New("profile: password must be at least 6 characters")
	ErrProfileNotFound    = errors.New("profile: not found")
	ErrInvalidCredentials = errors.New("profile: invalid username or password")
	ErrSessionExpired     = errors.New("profile: session expired")
	ErrSyncCodeInvalid    = errors.New("profile: sync code invalid or expired")
	ErrSyncCodeUsed       = errors.New("profile: sync code already used")
	ErrPINInvalid         = errors.New("profile: PIN must be 4-8 digits")
	ErrPINNotSet          = errors.New("profile: profile has no PIN")
	ErrNotOwner           = errors.New("profile: not owned by this TG account")
)

// Profile is one user record. ID is the durable cross-device identifier.
//
// Two login methods are supported:
//   - PasswordHash: classic username+password (set via Register, mutated via
//     ChangePassword). Empty when profile was created PIN-only via TG bot.
//   - PINHash: short numeric code (4-8 digits, bcrypt-hashed). Optional;
//     when set, the profile can also log in via /api/profile/pin-login.
//     Convenient for TV remotes where typing a strong password is painful.
//
// OwnerTGID, when non-zero, marks the profile as managed by a Telegram user.
// Only that TG user can mutate the PIN / delete the profile via the
// /api/profile/owned/* endpoints. Profiles created by Register (web form)
// have OwnerTGID == 0 and are unmanaged by anyone.
type Profile struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash,omitempty"`
	PINHash      string `json:"pin_hash,omitempty"`
	PINUpdatedAt int64  `json:"pin_updated_at,omitempty"`
	OwnerTGID    int64  `json:"owner_tg_id,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	LastLoginAt  int64  `json:"last_login_at,omitempty"`
}

var pinRe = regexp.MustCompile(`^[0-9]{4,8}$`)

// Session is one active login. The cookie value is the token field.
type Session struct {
	Token     string `json:"token"`
	ProfileID string `json:"profile_id"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	UserAgent string `json:"user_agent,omitempty"`
}

// SyncCode is a short one-shot string for keyboard-less devices.
type SyncCode struct {
	Code      string `json:"code"`
	ProfileID string `json:"profile_id"`
	ExpiresAt int64  `json:"expires_at"`
	UsedAt    int64  `json:"used_at,omitempty"` // 0 = unused
}

// Tunables — kept as package vars (not const) so tests can shrink them.
var (
	SessionTTL      = 90 * 24 * time.Hour
	SessionRollover = 7 * 24 * time.Hour // re-issue when within this window of expiry
	SyncCodeTTL     = 1 * time.Hour
)

// usernameRe accepts letters from any Unicode script (Latin, Cyrillic, Greek,
// CJK, etc.), digits, underscores and hyphens. We normalise via strings.ToLower
// before matching so "Алиса" and "АлИсА" map to the same key in `byName`.
// Length is measured in runes (3-32 visible characters), not bytes — without
// this, an 8-character Cyrillic name would fail because it's 16 UTF-8 bytes.
var usernameRe = regexp.MustCompile(`^[\p{L}\p{N}_-]{3,32}$`)

// Store is the multi-user profile registry. All operations are safe for
// concurrent use.
type Store struct {
	mu           sync.RWMutex
	profilesPath string
	sessionsPath string
	codesPath    string

	profiles map[string]*Profile // keyed by profile.ID
	byName   map[string]string   // lowercased username → profile.ID
	sessions map[string]*Session // keyed by token
	codes    map[string]*SyncCode
}

// NewStore loads (or creates) a Store rooted at <repoRoot>/database/profile/.
// On disk read errors we still return a usable in-memory store — the next
// successful save rewrites the file.
func NewStore(repoRoot string) *Store {
	dir := filepath.Join(repoRoot, "database", "profile")
	s := &Store{
		profilesPath: filepath.Join(dir, "profiles.json"),
		sessionsPath: filepath.Join(dir, "sessions.json"),
		codesPath:    filepath.Join(dir, "sync_codes.json"),
		profiles:     make(map[string]*Profile),
		byName:       make(map[string]string),
		sessions:     make(map[string]*Session),
		codes:        make(map[string]*SyncCode),
	}
	s.load()
	return s
}

// Register creates a new profile. Returns the persisted Profile so callers
// can issue a session in the same request.
func (s *Store) Register(username, password string) (*Profile, error) {
	norm := strings.ToLower(strings.TrimSpace(username))
	if !usernameRe.MatchString(norm) {
		return nil, ErrUsernameInvalid
	}
	if len(password) < 6 {
		return nil, ErrPasswordWeak
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, dup := s.byName[norm]; dup {
		return nil, ErrUsernameTaken
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("profile: bcrypt: %w", err)
	}

	now := time.Now().UnixMilli()
	p := &Profile{
		ID:           randomHex(16), // 32 hex chars; ample collision margin
		Username:     norm,
		PasswordHash: string(hash),
		CreatedAt:    now,
	}
	s.profiles[p.ID] = p
	s.byName[norm] = p.ID
	_ = s.saveProfilesLocked()
	return cloneProfile(p), nil
}

// Login verifies credentials and returns a freshly issued Session.
func (s *Store) Login(username, password, userAgent string) (*Session, *Profile, error) {
	norm := strings.ToLower(strings.TrimSpace(username))

	s.mu.Lock()
	id, ok := s.byName[norm]
	if !ok {
		s.mu.Unlock()
		// Constant-time hash comparison even when user doesn't exist —
		// keeps login latency uniform so attackers can't enumerate names.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidplaceholderhashvaluestillvalidlengthhh"), []byte(password))
		return nil, nil, ErrInvalidCredentials
	}
	p := s.profiles[id]
	if p == nil {
		s.mu.Unlock()
		return nil, nil, ErrInvalidCredentials
	}
	hash := p.PasswordHash
	s.mu.Unlock()

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, nil, ErrInvalidCredentials
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	p.LastLoginAt = time.Now().UnixMilli()
	sess := s.newSessionLocked(p.ID, userAgent)
	_ = s.saveProfilesLocked()
	_ = s.saveSessionsLocked()
	return cloneSession(sess), cloneProfile(p), nil
}

// LookupSession returns the Session+Profile for a cookie value, refreshing
// the session's expiry when it's within SessionRollover of expiring. Returns
// (nil, nil, nil) for an unknown token; (nil, nil, ErrSessionExpired) for an
// expired token (also deleted as a side-effect).
func (s *Store) LookupSession(token string) (*Session, *Profile, error) {
	if token == "" {
		return nil, nil, nil
	}

	s.mu.RLock()
	sess, ok := s.sessions[token]
	if !ok {
		s.mu.RUnlock()
		return nil, nil, nil
	}
	expired := time.UnixMilli(sess.ExpiresAt).Before(time.Now())
	profileSnap := s.profiles[sess.ProfileID]
	needRoll := !expired && time.Until(time.UnixMilli(sess.ExpiresAt)) < SessionRollover
	s.mu.RUnlock()

	if expired {
		s.mu.Lock()
		delete(s.sessions, token)
		_ = s.saveSessionsLocked()
		s.mu.Unlock()
		return nil, nil, ErrSessionExpired
	}

	if needRoll {
		s.mu.Lock()
		if alive, still := s.sessions[token]; still {
			alive.ExpiresAt = time.Now().Add(SessionTTL).UnixMilli()
			_ = s.saveSessionsLocked()
		}
		s.mu.Unlock()
	}

	if profileSnap == nil {
		// Orphaned session — the profile was deleted out from under it.
		s.mu.Lock()
		delete(s.sessions, token)
		_ = s.saveSessionsLocked()
		s.mu.Unlock()
		return nil, nil, ErrProfileNotFound
	}
	return cloneSession(sess), cloneProfile(profileSnap), nil
}

// Logout invalidates a single session token.
func (s *Store) Logout(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[token]; !ok {
		return
	}
	delete(s.sessions, token)
	_ = s.saveSessionsLocked()
}

// IssueSyncCode mints a short one-shot code that another device can redeem
// to obtain a session for the calling profile.
func (s *Store) IssueSyncCode(profileID string) (*SyncCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.profiles[profileID]; !ok {
		return nil, ErrProfileNotFound
	}

	s.purgeExpiredCodesLocked()

	code := randomSyncCode()
	// Vanishingly unlikely collision, but retry once just to be safe.
	for tries := 0; tries < 5; tries++ {
		if _, dup := s.codes[code]; !dup {
			break
		}
		code = randomSyncCode()
	}

	entry := &SyncCode{
		Code:      code,
		ProfileID: profileID,
		ExpiresAt: time.Now().Add(SyncCodeTTL).UnixMilli(),
	}
	s.codes[code] = entry
	_ = s.saveCodesLocked()
	return cloneCode(entry), nil
}

// RedeemSyncCode trades a one-shot code for a Session. The code is marked
// used the first time it's redeemed; subsequent attempts return
// ErrSyncCodeUsed.
func (s *Store) RedeemSyncCode(code, userAgent string) (*Session, *Profile, error) {
	norm := normalizeSyncCode(code)
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.codes[norm]
	if !ok {
		return nil, nil, ErrSyncCodeInvalid
	}
	if entry.UsedAt != 0 {
		return nil, nil, ErrSyncCodeUsed
	}
	if time.UnixMilli(entry.ExpiresAt).Before(time.Now()) {
		delete(s.codes, norm)
		_ = s.saveCodesLocked()
		return nil, nil, ErrSyncCodeInvalid
	}
	p := s.profiles[entry.ProfileID]
	if p == nil {
		return nil, nil, ErrProfileNotFound
	}

	entry.UsedAt = time.Now().UnixMilli()
	sess := s.newSessionLocked(entry.ProfileID, userAgent)
	_ = s.saveCodesLocked()
	_ = s.saveSessionsLocked()
	return cloneSession(sess), cloneProfile(p), nil
}

// GetProfile returns a copy of the profile record. Returns nil if unknown.
func (s *Store) GetProfile(profileID string) *Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.profiles[profileID]; ok {
		return cloneProfile(p)
	}
	return nil
}

// ChangePassword verifies the old password and rotates to a new one. All
// active sessions for the profile survive (we don't force a re-login —
// matches the UX users expect from web apps).
func (s *Store) ChangePassword(profileID, oldPassword, newPassword string) error {
	if len(newPassword) < 6 {
		return ErrPasswordWeak
	}
	s.mu.Lock()
	p := s.profiles[profileID]
	if p == nil {
		s.mu.Unlock()
		return ErrProfileNotFound
	}
	hash := p.PasswordHash
	s.mu.Unlock()

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPassword)) != nil {
		return ErrInvalidCredentials
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("profile: bcrypt: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	p2 := s.profiles[profileID]
	if p2 == nil {
		return ErrProfileNotFound
	}
	p2.PasswordHash = string(newHash)
	return s.saveProfilesLocked()
}

// --- PIN auth & TG-bot management ---

// CreateForTGOwner registers a profile that's managed by a Telegram user.
// The new profile has no password (PasswordHash == ""), only a PIN: the
// owner-TG-user sets PIN via /profiles in the bot and shares it with their
// other devices. Pass empty pin to defer PIN setup to a later SetPIN call.
//
// username collisions return ErrUsernameTaken — the bot UI should retry
// with a different name.
func (s *Store) CreateForTGOwner(ownerTGID int64, username, pin string) (*Profile, error) {
	if ownerTGID == 0 {
		return nil, ErrNotOwner
	}
	norm := strings.ToLower(strings.TrimSpace(username))
	if !usernameRe.MatchString(norm) {
		return nil, ErrUsernameInvalid
	}
	var pinHash string
	if pin != "" {
		if !pinRe.MatchString(pin) {
			return nil, ErrPINInvalid
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("profile: bcrypt pin: %w", err)
		}
		pinHash = string(h)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byName[norm]; dup {
		return nil, ErrUsernameTaken
	}
	now := time.Now().UnixMilli()
	p := &Profile{
		ID:        randomHex(16),
		Username:  norm,
		OwnerTGID: ownerTGID,
		CreatedAt: now,
	}
	if pinHash != "" {
		p.PINHash = pinHash
		p.PINUpdatedAt = now
	}
	s.profiles[p.ID] = p
	s.byName[norm] = p.ID
	_ = s.saveProfilesLocked()
	return cloneProfile(p), nil
}

// SetPIN sets or rotates the PIN for an owned profile. The caller must
// pass the owning TG ID — pass 0 to bypass the ownership check (used by
// the web Register flow where the user is already authenticated as the
// profile owner). Empty pin clears the PIN, disabling PIN login.
func (s *Store) SetPIN(profileID string, ownerTGID int64, pin string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.profiles[profileID]
	if p == nil {
		return ErrProfileNotFound
	}
	if ownerTGID != 0 && p.OwnerTGID != ownerTGID {
		return ErrNotOwner
	}
	if pin == "" {
		p.PINHash = ""
		p.PINUpdatedAt = time.Now().UnixMilli()
		return s.saveProfilesLocked()
	}
	if !pinRe.MatchString(pin) {
		return ErrPINInvalid
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("profile: bcrypt pin: %w", err)
	}
	p.PINHash = string(h)
	p.PINUpdatedAt = time.Now().UnixMilli()
	return s.saveProfilesLocked()
}

// LoginByPIN authenticates with username + PIN. Returns the same shape as
// Login. ErrPINNotSet when the profile has no PIN configured;
// ErrInvalidCredentials on any other auth failure (deliberately vague to
// stop username/PIN enumeration).
func (s *Store) LoginByPIN(username, pin, userAgent string) (*Session, *Profile, error) {
	norm := strings.ToLower(strings.TrimSpace(username))
	if !pinRe.MatchString(pin) {
		return nil, nil, ErrInvalidCredentials
	}

	s.mu.Lock()
	id, ok := s.byName[norm]
	if !ok {
		s.mu.Unlock()
		// Constant-time guard, same rationale as Login.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidplaceholderhashvaluestillvalidlengthhh"), []byte(pin))
		return nil, nil, ErrInvalidCredentials
	}
	p := s.profiles[id]
	if p == nil || p.PINHash == "" {
		s.mu.Unlock()
		if p != nil && p.PINHash == "" {
			return nil, nil, ErrPINNotSet
		}
		return nil, nil, ErrInvalidCredentials
	}
	hash := p.PINHash
	s.mu.Unlock()

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin)) != nil {
		return nil, nil, ErrInvalidCredentials
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	p.LastLoginAt = time.Now().UnixMilli()
	sess := s.newSessionLocked(p.ID, userAgent)
	_ = s.saveProfilesLocked()
	_ = s.saveSessionsLocked()
	return cloneSession(sess), cloneProfile(p), nil
}

// ListByOwnerTG returns all profiles owned by a Telegram user. The slice
// is freshly copied so the caller can sort/mutate without holding the
// store lock.
func (s *Store) ListByOwnerTG(ownerTGID int64) []*Profile {
	if ownerTGID == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Profile, 0, 4)
	for _, p := range s.profiles {
		if p.OwnerTGID == ownerTGID {
			out = append(out, cloneProfile(p))
		}
	}
	return out
}

// DeleteOwned removes a profile, but only if `ownerTGID` matches its
// OwnerTGID. All active sessions for that profile are also invalidated.
func (s *Store) DeleteOwned(profileID string, ownerTGID int64) error {
	if ownerTGID == 0 {
		return ErrNotOwner
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.profiles[profileID]
	if p == nil {
		return ErrProfileNotFound
	}
	if p.OwnerTGID != ownerTGID {
		return ErrNotOwner
	}
	delete(s.profiles, profileID)
	delete(s.byName, p.Username)
	// Cascade: drop all live sessions for this profile.
	for tok, ss := range s.sessions {
		if ss.ProfileID == profileID {
			delete(s.sessions, tok)
		}
	}
	_ = s.saveProfilesLocked()
	_ = s.saveSessionsLocked()
	return nil
}

// PurgeExpired drops dead sessions and codes. Safe to call from a janitor
// goroutine.
func (s *Store) PurgeExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for token, sess := range s.sessions {
		if time.UnixMilli(sess.ExpiresAt).Before(now) {
			delete(s.sessions, token)
		}
	}
	s.purgeExpiredCodesLocked()
	_ = s.saveSessionsLocked()
	_ = s.saveCodesLocked()
}

// --- internals ---

func (s *Store) newSessionLocked(profileID, userAgent string) *Session {
	now := time.Now()
	sess := &Session{
		Token:     randomHex(32), // 64 hex chars
		ProfileID: profileID,
		CreatedAt: now.UnixMilli(),
		ExpiresAt: now.Add(SessionTTL).UnixMilli(),
		UserAgent: truncate(userAgent, 256),
	}
	s.sessions[sess.Token] = sess
	return sess
}

func (s *Store) purgeExpiredCodesLocked() {
	now := time.Now()
	for code, entry := range s.codes {
		if time.UnixMilli(entry.ExpiresAt).Before(now) {
			delete(s.codes, code)
		}
		// Used codes are retained until expiry so the second-device
		// attempt sees ErrSyncCodeUsed rather than ErrSyncCodeInvalid —
		// that's a clearer error message for the user.
	}
}

func (s *Store) load() {
	if data, err := os.ReadFile(s.profilesPath); err == nil {
		var arr []*Profile
		if json.Unmarshal(data, &arr) == nil {
			for _, p := range arr {
				if p == nil || p.ID == "" {
					continue
				}
				s.profiles[p.ID] = p
				if p.Username != "" {
					s.byName[strings.ToLower(p.Username)] = p.ID
				}
			}
		}
	}
	if data, err := os.ReadFile(s.sessionsPath); err == nil {
		var arr []*Session
		if json.Unmarshal(data, &arr) == nil {
			now := time.Now()
			for _, ss := range arr {
				if ss == nil || ss.Token == "" {
					continue
				}
				if time.UnixMilli(ss.ExpiresAt).Before(now) {
					continue
				}
				s.sessions[ss.Token] = ss
			}
		}
	}
	if data, err := os.ReadFile(s.codesPath); err == nil {
		var arr []*SyncCode
		if json.Unmarshal(data, &arr) == nil {
			now := time.Now()
			for _, c := range arr {
				if c == nil || c.Code == "" {
					continue
				}
				if time.UnixMilli(c.ExpiresAt).Before(now) {
					continue
				}
				s.codes[c.Code] = c
			}
		}
	}
}

func (s *Store) saveProfilesLocked() error {
	arr := make([]*Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		arr = append(arr, p)
	}
	return writeJSONAtomic(s.profilesPath, arr)
}

func (s *Store) saveSessionsLocked() error {
	arr := make([]*Session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		arr = append(arr, ss)
	}
	return writeJSONAtomic(s.sessionsPath, arr)
}

func (s *Store) saveCodesLocked() error {
	arr := make([]*SyncCode, 0, len(s.codes))
	for _, c := range s.codes {
		arr = append(arr, c)
	}
	return writeJSONAtomic(s.codesPath, arr)
}

// writeJSONAtomic writes via a tempfile + rename so a crash mid-write
// doesn't leave a half-written JSON file that fails to parse on next boot.
func writeJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// Vanishingly unlikely; fall back to a time-derived value rather
		// than panic. The collision risk is bounded because the surrounding
		// code retries on duplicate.
		now := time.Now().UnixNano()
		for i := range buf {
			buf[i] = byte(now >> (uint(i%8) * 8))
		}
	}
	return hex.EncodeToString(buf)
}

// randomSyncCode produces a code shaped like "QX7A-K3PM" — 8 alphanumerics
// with a separator. The alphabet excludes visually ambiguous characters
// (0/O, 1/I/L) so TV-remote typing is less error-prone.
func randomSyncCode() string {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	buf := make([]byte, 8)
	rand.Read(buf)
	out := make([]byte, 0, 9)
	for i, b := range buf {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, alphabet[int(b)%len(alphabet)])
	}
	return string(out)
}

// normalizeSyncCode canonicalises a user-typed code: upper-cases, strips
// whitespace, and ensures the dash is at position 4. We don't reject input
// without a dash — TV remotes often skip it.
func normalizeSyncCode(code string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r
		case r >= 'a' && r <= 'z':
			return r - ('a' - 'A')
		case r >= '0' && r <= '9':
			return r
		default:
			return -1
		}
	}, code)
	if len(clean) == 8 {
		return clean[:4] + "-" + clean[4:]
	}
	return clean
}

func cloneProfile(p *Profile) *Profile {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

func cloneSession(s *Session) *Session {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

func cloneCode(c *SyncCode) *SyncCode {
	if c == nil {
		return nil
	}
	cc := *c
	return &cc
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
