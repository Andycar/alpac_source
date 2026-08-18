package tgauth

// Password-based users — the alternative to TG auth.
//
// One user = one row in database/tgauth/password_users.json. Each user owns
// a primary UID (auto-generated 8-char lampac_unic_id), a bcrypt password
// hash, a subscription expiry, and zero-or-more active session tokens
// (multi-device login). Devices are bound the same way TG users bind theirs
// — via DeviceInfo entries — so all existing /api/users device controls
// transfer over verbatim.
//
// This store is intentionally separate from Store (TG tokens). Mixing the
// two would force every TG-only path (membership checks, /tg/auth/check,
// bot.go) to handle empty TelegramID rows. The auth middleware queries both
// stores and merges results into one User context.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
)

// Errors returned by PasswordUserStore.
var (
	ErrUsernameExists    = errors.New("password: username already exists")
	ErrUsernameInvalid   = errors.New("password: username invalid (3-32 chars, [a-zA-Z0-9_.-])")
	ErrPasswordTooShort  = errors.New("password: password too short")
	ErrPasswordTooLong   = errors.New("password: password too long")
	ErrUserNotFound      = errors.New("password: user not found")
	ErrPasswordIncorrect = errors.New("password: incorrect password")
	ErrUserBanned        = errors.New("password: user banned")
	ErrUserExpired       = errors.New("password: subscription expired")
	ErrSessionNotFound   = errors.New("password: session not found")
)

// PasswordSession is a single active login (per browser/device).
type PasswordSession struct {
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	LastIP    string    `json:"last_ip,omitempty"`
	LastUA    string    `json:"last_ua,omitempty"`
}

// PasswordUser mirrors ApprovedToken minus the TG-specific bits.
//
// Fields with the same name/semantics as ApprovedToken (GroupID,
// PremiumUntil, Devices, MaxDevices, TorrServerDisabled) keep the same
// JSON encoding so the admin v2 frontend can render both lists with
// the same component.
type PasswordUser struct {
	Username           string            `json:"username"`
	PasswordHash       string            `json:"password_hash"`
	UID                string            `json:"uid"` // primary 8-char lampac_unic_id assigned at creation
	CreatedAt          time.Time         `json:"created_at"`
	ExpiresAt          time.Time         `json:"expires_at"`
	GroupID            string            `json:"group_id,omitempty"`
	PremiumUntil       time.Time         `json:"premium_until,omitempty"`
	Devices            []DeviceInfo      `json:"devices,omitempty"`
	MaxDevices         int               `json:"max_devices,omitempty"`
	TorrServerDisabled bool              `json:"torrserver_disabled,omitempty"`
	Ban                bool              `json:"ban,omitempty"`
	BanReason          string            `json:"ban_reason,omitempty"`
	Comment            string            `json:"comment,omitempty"`
	Sessions           []PasswordSession `json:"sessions,omitempty"`
	LastLoginAt        time.Time         `json:"last_login_at,omitempty"`
	LastLoginIP        string            `json:"last_login_ip,omitempty"`
}

// EffectiveGroupID mirrors ApprovedToken.EffectiveGroupID for code paths
// that don't care which store the user came from.
func (u *PasswordUser) EffectiveGroupID(premiumGroupID string) string {
	if premiumGroupID != "" && !u.PremiumUntil.IsZero() && u.PremiumUntil.After(time.Now().UTC()) {
		return premiumGroupID
	}
	return u.GroupID
}

// CreateOpts is admin-supplied data when minting a new user.
type CreateOpts struct {
	GroupID        string
	ExpiresAt      time.Time // zero = permanent
	PremiumUntil   time.Time
	MaxDevices     int
	Comment        string
	MinPasswordLen int // if 0, defaults to 8
	MaxPasswordLen int // if 0, defaults to 128
}

// PasswordUserStore is the disk-backed CRUD for password accounts.
type PasswordUserStore struct {
	mu       sync.RWMutex
	users    []PasswordUser
	filePath string

	byUsername map[string]int // username (lowercased) → users index
	byToken    map[string]int // session token → users index (the user that owns the session)
	byUID      map[string]int // device UID → users index (matches Store.byUID semantics)

	dirty    bool
	saveMu   sync.Mutex
	saveStop chan struct{}
	saveDone chan struct{}
}

// NewPasswordUserStore loads (or creates) database/tgauth/password_users.json.
// Mirrors NewStore semantics: background flush every 10s, atomic writes,
// O(1) lookups via indexes.
func NewPasswordUserStore(dbDir string) *PasswordUserStore {
	s := &PasswordUserStore{
		filePath:   filepath.Join(dbDir, "database", "tgauth", "password_users.json"),
		byUsername: make(map[string]int),
		byToken:    make(map[string]int),
		byUID:      make(map[string]int),
		saveStop:   make(chan struct{}),
		saveDone:   make(chan struct{}),
	}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("path", s.filePath).Msg("tgauth/password: failed to load (starting empty)")
	}
	s.rebuildIndexes()
	go s.backgroundSaver()
	return s
}

// Close stops the background saver and flushes pending changes. Blocking
// — returns only after the final flush has hit disk.
func (s *PasswordUserStore) Close() {
	select {
	case <-s.saveStop:
		// already closed
	default:
		close(s.saveStop)
	}
	<-s.saveDone
}

func (s *PasswordUserStore) backgroundSaver() {
	defer close(s.saveDone)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.flushIfDirty()
		case <-s.saveStop:
			s.flushIfDirty()
			return
		}
	}
}

func (s *PasswordUserStore) flushIfDirty() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return
	}
	data, err := json.MarshalIndent(s.users, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("tgauth/password: marshal failed")
		return
	}
	s.dirty = false
	s.saveMu.Lock()
	err = writeAtomic(s.filePath, data)
	s.saveMu.Unlock()
	if err != nil {
		log.Error().Err(err).Msg("tgauth/password: save failed")
	}
}

func (s *PasswordUserStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Unmarshal(data, &s.users)
}

func (s *PasswordUserStore) saveNow() error {
	s.dirty = false
	data, err := json.MarshalIndent(s.users, "", "  ")
	if err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return writeAtomic(s.filePath, data)
}

// rebuildIndexes is callable only with s.mu held (write lock).
func (s *PasswordUserStore) rebuildIndexes() {
	s.byUsername = make(map[string]int, len(s.users))
	s.byToken = make(map[string]int, len(s.users)*2)
	s.byUID = make(map[string]int, len(s.users)*2)
	now := time.Now().UTC()
	for i := range s.users {
		u := &s.users[i]
		if u.Username != "" {
			s.byUsername[strings.ToLower(u.Username)] = i
		}
		// Drop sessions that have expired in-memory so subsequent LookupSession
		// short-circuits. Disk format keeps them until the next save trims.
		live := u.Sessions[:0]
		for _, sess := range u.Sessions {
			if sess.ExpiresAt.After(now) {
				live = append(live, sess)
				if sess.Token != "" {
					s.byToken[sess.Token] = i
				}
			}
		}
		u.Sessions = live
		if u.UID != "" {
			s.byUID[u.UID] = i
		}
		for _, d := range u.Devices {
			if d.UID != "" {
				s.byUID[d.UID] = i
			}
		}
	}
}

// ---------------- Validation ----------------

// ValidateUsername returns nil if name passes the 3..maxLen + [a-zA-Z0-9_.-]
// charset check. Whitespace is trimmed by the caller, not here.
func ValidateUsername(name string, maxLen int) error {
	if maxLen <= 0 {
		maxLen = 32
	}
	if len(name) < 3 || len(name) > maxLen {
		return ErrUsernameInvalid
	}
	for _, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '_' || ch == '.' || ch == '-':
		default:
			return ErrUsernameInvalid
		}
	}
	return nil
}

// ValidatePassword enforces length bounds. We deliberately don't require
// complexity rules — bcrypt + per-IP lockout already cover the threat
// model for an inline-LAN deployment.
func ValidatePassword(plain string, minLen, maxLen int) error {
	if minLen <= 0 {
		minLen = 8
	}
	if maxLen <= 0 {
		maxLen = 128
	}
	if len(plain) < minLen {
		return ErrPasswordTooShort
	}
	if len(plain) > maxLen {
		return ErrPasswordTooLong
	}
	return nil
}

// ---------------- CRUD ----------------

// Create mints a new password user. Returns the persisted record (including
// the freshly minted UID). The plaintext password is never stored.
func (s *PasswordUserStore) Create(username, plaintext string, opts CreateOpts) (PasswordUser, error) {
	username = strings.TrimSpace(username)
	if err := ValidateUsername(username, 32); err != nil {
		return PasswordUser{}, err
	}
	if err := ValidatePassword(plaintext, opts.MinPasswordLen, opts.MaxPasswordLen); err != nil {
		return PasswordUser{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return PasswordUser{}, fmt.Errorf("password: bcrypt: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.byUsername[strings.ToLower(username)]; exists {
		return PasswordUser{}, ErrUsernameExists
	}

	now := time.Now().UTC()
	u := PasswordUser{
		Username:     username,
		PasswordHash: string(hash),
		UID:          generateUniqueUID(s.byUID),
		CreatedAt:    now,
		ExpiresAt:    opts.ExpiresAt,
		GroupID:      strings.TrimSpace(opts.GroupID),
		PremiumUntil: opts.PremiumUntil,
		MaxDevices:   opts.MaxDevices,
		Comment:      strings.TrimSpace(opts.Comment),
	}

	s.users = append(s.users, u)
	idx := len(s.users) - 1
	s.byUsername[strings.ToLower(username)] = idx
	s.byUID[u.UID] = idx
	if err := s.saveNow(); err != nil {
		log.Error().Err(err).Msg("tgauth/password: create save failed")
	}
	return u, nil
}

// LookupUser returns a snapshot of the user by username (case-insensitive).
func (s *PasswordUserStore) LookupUser(username string) (PasswordUser, bool) {
	if username == "" {
		return PasswordUser{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byUsername[strings.ToLower(username)]
	if !ok {
		return PasswordUser{}, false
	}
	return s.users[idx], true
}

// LookupSession returns the user owning the given session token, and a
// stamp of the session entry itself. The session is checked for liveness
// (ExpiresAt > now); expired sessions are reported missing.
func (s *PasswordUserStore) LookupSession(token string) (PasswordUser, PasswordSession, bool) {
	if token == "" {
		return PasswordUser{}, PasswordSession{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byToken[token]
	if !ok {
		return PasswordUser{}, PasswordSession{}, false
	}
	u := s.users[idx]
	now := time.Now().UTC()
	for _, sess := range u.Sessions {
		if sess.Token == token && sess.ExpiresAt.After(now) {
			return u, sess, true
		}
	}
	return PasswordUser{}, PasswordSession{}, false
}

// LookupAuth satisfies auth.PasswordTokenChecker — returns the bits
// the middleware needs to build a User: stable id (UID), expiry, ok.
//
// Expiry returned is min(session.ExpiresAt, user.ExpiresAt) so a
// 365-day session on a 30-day subscription still expires when the
// subscription does.
func (s *PasswordUserStore) LookupAuth(token string) (username, uid string, expiresAt time.Time, ok bool) {
	u, sess, found := s.LookupSession(token)
	if !found {
		return "", "", time.Time{}, false
	}
	if u.Ban {
		return "", "", time.Time{}, false
	}
	exp := sess.ExpiresAt
	if !u.ExpiresAt.IsZero() && (exp.IsZero() || u.ExpiresAt.Before(exp)) {
		exp = u.ExpiresAt
	}
	if !exp.IsZero() && time.Now().UTC().After(exp) {
		return "", "", time.Time{}, false
	}
	return u.Username, u.UID, exp, true
}

// CheckPassword verifies plaintext against the stored hash. Returns the
// user snapshot and ok=true on match. Doesn't issue a session.
func (s *PasswordUserStore) CheckPassword(username, plaintext string) (PasswordUser, bool) {
	u, ok := s.LookupUser(username)
	if !ok {
		return PasswordUser{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(plaintext)) != nil {
		return PasswordUser{}, false
	}
	return u, true
}

// Login verifies the password and issues a new session token. Use this
// from /auth/password/login. Caller is responsible for translating the
// returned token into cookies.
//
// The returned User snapshot reflects the post-login state (LastLoginAt /
// LastLoginIP updated, new session appended).
func (s *PasswordUserStore) Login(username, plaintext, ip, ua string, sessionDays int) (PasswordUser, string, error) {
	username = strings.TrimSpace(username)
	if err := ValidateUsername(username, 32); err != nil {
		return PasswordUser{}, "", ErrPasswordIncorrect // don't leak username validity vs. password validity to the user
	}
	if sessionDays <= 0 {
		sessionDays = 365
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.byUsername[strings.ToLower(username)]
	if !ok {
		return PasswordUser{}, "", ErrPasswordIncorrect
	}
	u := &s.users[idx]
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(plaintext)) != nil {
		return PasswordUser{}, "", ErrPasswordIncorrect
	}
	if u.Ban {
		return PasswordUser{}, "", ErrUserBanned
	}
	if !u.ExpiresAt.IsZero() && time.Now().UTC().After(u.ExpiresAt) {
		return PasswordUser{}, "", ErrUserExpired
	}

	now := time.Now().UTC()
	token := randomSessionToken()
	sess := PasswordSession{
		Token:     token,
		CreatedAt: now,
		ExpiresAt: now.AddDate(0, 0, sessionDays),
		LastIP:    ip,
		LastUA:    ua,
	}
	u.Sessions = append(u.Sessions, sess)
	u.LastLoginAt = now
	u.LastLoginIP = ip
	s.byToken[token] = idx
	if err := s.saveNow(); err != nil {
		log.Error().Err(err).Msg("tgauth/password: login save failed")
	}
	return *u, token, nil
}

// Logout invalidates a single session. Returns ErrSessionNotFound if the
// token doesn't exist (idempotent caller can ignore).
func (s *PasswordUserStore) Logout(token string) error {
	if token == "" {
		return ErrSessionNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byToken[token]
	if !ok {
		return ErrSessionNotFound
	}
	u := &s.users[idx]
	for i, sess := range u.Sessions {
		if sess.Token == token {
			u.Sessions = append(u.Sessions[:i], u.Sessions[i+1:]...)
			break
		}
	}
	delete(s.byToken, token)
	return s.saveNow()
}

// LogoutAll invalidates every session for the given username — used after
// a forced password reset by the admin.
func (s *PasswordUserStore) LogoutAll(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byUsername[strings.ToLower(username)]
	if !ok {
		return ErrUserNotFound
	}
	u := &s.users[idx]
	for _, sess := range u.Sessions {
		delete(s.byToken, sess.Token)
	}
	u.Sessions = nil
	return s.saveNow()
}

// ResetPassword sets a new password without verifying the old (admin op).
// All active sessions are revoked.
func (s *PasswordUserStore) ResetPassword(username, newPlain string, minLen, maxLen int) error {
	if err := ValidatePassword(newPlain, minLen, maxLen); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPlain), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("password: bcrypt: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byUsername[strings.ToLower(username)]
	if !ok {
		return ErrUserNotFound
	}
	u := &s.users[idx]
	u.PasswordHash = string(hash)
	for _, sess := range u.Sessions {
		delete(s.byToken, sess.Token)
	}
	u.Sessions = nil
	return s.saveNow()
}

// ChangePassword verifies the old password before setting the new one. Use
// this from the user-facing change-password flow (currently the admin
// panel only — kept here so a future user UI doesn't need a new method).
func (s *PasswordUserStore) ChangePassword(username, oldPlain, newPlain string, minLen, maxLen int) error {
	s.mu.RLock()
	idx, ok := s.byUsername[strings.ToLower(username)]
	if !ok {
		s.mu.RUnlock()
		return ErrUserNotFound
	}
	currentHash := s.users[idx].PasswordHash
	s.mu.RUnlock()
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(oldPlain)) != nil {
		return ErrPasswordIncorrect
	}
	return s.ResetPassword(username, newPlain, minLen, maxLen)
}

// Delete removes a user and all their sessions.
func (s *PasswordUserStore) Delete(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byUsername[strings.ToLower(username)]
	if !ok {
		return ErrUserNotFound
	}
	u := &s.users[idx]
	for _, sess := range u.Sessions {
		delete(s.byToken, sess.Token)
	}
	if u.UID != "" {
		delete(s.byUID, u.UID)
	}
	for _, d := range u.Devices {
		delete(s.byUID, d.UID)
	}
	s.users = append(s.users[:idx], s.users[idx+1:]...)
	// Indexes hold stale post-idx values now — easiest fix is rebuild.
	s.rebuildIndexes()
	return s.saveNow()
}

// List returns a defensive copy of all users.
func (s *PasswordUserStore) List() []PasswordUser {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PasswordUser, len(s.users))
	copy(out, s.users)
	return out
}

// ---------------- Mutators ----------------

func (s *PasswordUserStore) mutate(username string, fn func(*PasswordUser)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byUsername[strings.ToLower(username)]
	if !ok {
		return ErrUserNotFound
	}
	fn(&s.users[idx])
	return s.saveNow()
}

// Extend adds days to ExpiresAt. Zero ExpiresAt is treated as "now" so
// extending an unset record sets it to now+days.
func (s *PasswordUserStore) Extend(username string, days int) error {
	if days == 0 {
		return nil
	}
	return s.mutate(username, func(u *PasswordUser) {
		base := u.ExpiresAt
		if base.IsZero() {
			base = time.Now().UTC()
		}
		u.ExpiresAt = base.AddDate(0, 0, days)
	})
}

// SetExpires sets ExpiresAt explicitly. Pass zero time for unlimited.
func (s *PasswordUserStore) SetExpires(username string, t time.Time) error {
	return s.mutate(username, func(u *PasswordUser) { u.ExpiresAt = t.UTC() })
}

// SetGroup updates GroupID.
func (s *PasswordUserStore) SetGroup(username, groupID string) error {
	return s.mutate(username, func(u *PasswordUser) { u.GroupID = strings.TrimSpace(groupID) })
}

// SetPremium updates PremiumUntil. Pass zero time to clear premium.
func (s *PasswordUserStore) SetPremium(username string, t time.Time) error {
	return s.mutate(username, func(u *PasswordUser) { u.PremiumUntil = t.UTC() })
}

// SetBan toggles the ban flag and optional reason.
func (s *PasswordUserStore) SetBan(username string, ban bool, reason string) error {
	return s.mutate(username, func(u *PasswordUser) {
		u.Ban = ban
		u.BanReason = strings.TrimSpace(reason)
	})
}

// SetMaxDevices updates the per-user device cap (0 = use server default).
func (s *PasswordUserStore) SetMaxDevices(username string, n int) error {
	if n < 0 {
		n = 0
	}
	return s.mutate(username, func(u *PasswordUser) { u.MaxDevices = n })
}

// SetTorrServerDisabled toggles per-user torrserver access.
func (s *PasswordUserStore) SetTorrServerDisabled(username string, disabled bool) error {
	return s.mutate(username, func(u *PasswordUser) { u.TorrServerDisabled = disabled })
}

// SetComment edits the admin-only freeform note.
func (s *PasswordUserStore) SetComment(username, comment string) error {
	return s.mutate(username, func(u *PasswordUser) { u.Comment = strings.TrimSpace(comment) })
}

// AddDevice appends a device to the user. Returns (true, nil) on success;
// (false, ErrUIDCrossToken) if the UID is already owned by another user.
func (s *PasswordUserStore) AddDevice(username string, dev DeviceInfo) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byUsername[strings.ToLower(username)]
	if !ok {
		return false, ErrUserNotFound
	}
	if dev.UID == "" {
		return false, fmt.Errorf("password: AddDevice requires UID")
	}
	if owner, exists := s.byUID[dev.UID]; exists && owner != idx {
		return false, ErrUIDCrossToken
	}
	u := &s.users[idx]
	now := time.Now().UTC()
	if dev.BoundAt.IsZero() {
		dev.BoundAt = now
	}
	if dev.LastSeen.IsZero() {
		dev.LastSeen = now
	}
	// Replace existing entry by UID.
	for i := range u.Devices {
		if u.Devices[i].UID == dev.UID {
			u.Devices[i] = dev
			return false, s.saveNow()
		}
	}
	u.Devices = append(u.Devices, dev)
	s.byUID[dev.UID] = idx
	return true, s.saveNow()
}

// RemoveDevice removes a device by UID.
func (s *PasswordUserStore) RemoveDevice(username, uid string) error {
	return s.mutate(username, func(u *PasswordUser) {
		for i := range u.Devices {
			if u.Devices[i].UID == uid {
				u.Devices = append(u.Devices[:i], u.Devices[i+1:]...)
				delete(s.byUID, uid)
				return
			}
		}
	})
}

// TouchDevice updates LastSeen / LastIP for the named UID. Silent no-op if
// the UID isn't owned by this user.
func (s *PasswordUserStore) TouchDevice(username, uid, ip string) error {
	return s.mutate(username, func(u *PasswordUser) {
		now := time.Now().UTC()
		for i := range u.Devices {
			if u.Devices[i].UID == uid {
				u.Devices[i].LastSeen = now
				if ip != "" {
					u.Devices[i].LastIP = ip
				}
				return
			}
		}
	})
}

// FindUserByUID locates the password user owning the given device UID.
// Used by the admin device-management page when admin clicks a device row.
func (s *PasswordUserStore) FindUserByUID(uid string) (PasswordUser, bool) {
	if uid == "" {
		return PasswordUser{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byUID[uid]
	if !ok {
		return PasswordUser{}, false
	}
	return s.users[idx], true
}

// CleanExpiredSessions drops any session whose ExpiresAt has passed.
// Returns the count removed.
func (s *PasswordUserStore) CleanExpiredSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	removed := 0
	for i := range s.users {
		u := &s.users[i]
		live := u.Sessions[:0]
		for _, sess := range u.Sessions {
			if sess.ExpiresAt.After(now) {
				live = append(live, sess)
			} else {
				delete(s.byToken, sess.Token)
				removed++
			}
		}
		u.Sessions = live
	}
	if removed > 0 {
		_ = s.saveNow()
	}
	return removed
}

// ---------------- helpers ----------------

// generateUniqueUID returns a fresh 8-char hex UID not present in byUID.
// Caller must hold the appropriate lock.
func generateUniqueUID(taken map[string]int) string {
	for range 50 {
		b := make([]byte, 4)
		if _, err := rand.Read(b); err != nil {
			continue
		}
		uid := hex.EncodeToString(b)
		if _, exists := taken[uid]; !exists {
			return uid
		}
	}
	// Fallback to a longer UID if we somehow collided 50 times in a row.
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// randomSessionToken returns a 64-char hex string (32 bytes of entropy).
// Same format Store uses for TG tokens so all downstream cookie/header
// handling stays unchanged.
func randomSessionToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
