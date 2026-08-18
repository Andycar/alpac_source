package tgauth

// Anonymous user issuer — used when auth.mode = "none".
//
// On every request without an existing auth cookie, the middleware asks
// the issuer to mint a new anon session. The token + UID get persisted
// to disk so a process restart doesn't kick existing visitors out, and
// so the admin can see what's been issued.
//
// Anon users are intentionally featureless: no devices array, no group,
// no premium overlay. They exist solely so the rest of the system has a
// stable per-browser identifier (cookie token → user.UID) to key state
// like balancer visibility and history. Groups and premium are
// meaningless in an open-access deployment.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// AnonUser is one row in database/tgauth/anon_users.json.
type AnonUser struct {
	Token     string    `json:"token"`
	UID       string    `json:"uid"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	LastIP    string    `json:"last_ip,omitempty"`
	LastUA    string    `json:"last_ua,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
}

// AnonIssuer mints + tracks anon sessions.
type AnonIssuer struct {
	mu       sync.RWMutex
	users    []AnonUser
	filePath string

	byToken map[string]int
	byUID   map[string]int

	sessionDays int

	dirty    bool
	saveMu   sync.Mutex
	saveStop chan struct{}
	saveDone chan struct{} // closed when backgroundSaver returns
}

// NewAnonIssuer creates an issuer backed by database/tgauth/anon_users.json.
// sessionDays controls the cookie TTL — 0 falls back to 30.
func NewAnonIssuer(dbDir string, sessionDays int) *AnonIssuer {
	if sessionDays <= 0 {
		sessionDays = 30
	}
	a := &AnonIssuer{
		filePath:    filepath.Join(dbDir, "database", "tgauth", "anon_users.json"),
		byToken:     make(map[string]int),
		byUID:       make(map[string]int),
		sessionDays: sessionDays,
		saveStop:    make(chan struct{}),
		saveDone:    make(chan struct{}),
	}
	if err := a.load(); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("path", a.filePath).Msg("tgauth/anon: load failed (starting empty)")
	}
	a.rebuildIndexes()
	go a.backgroundSaver()
	return a
}

// Close stops the saver and flushes pending writes. Blocking — returns
// only after the final flush has hit disk so callers (tests, the binary
// shutting down) can rely on file state being consistent immediately.
func (a *AnonIssuer) Close() {
	select {
	case <-a.saveStop:
		// Already closed — still wait for saveDone if pending.
	default:
		close(a.saveStop)
	}
	<-a.saveDone
}

func (a *AnonIssuer) backgroundSaver() {
	defer close(a.saveDone)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.flushIfDirty()
		case <-a.saveStop:
			a.flushIfDirty()
			return
		}
	}
}

func (a *AnonIssuer) flushIfDirty() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.dirty {
		return
	}
	data, err := json.MarshalIndent(a.users, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("tgauth/anon: marshal failed")
		return
	}
	a.dirty = false
	a.saveMu.Lock()
	err = writeAtomic(a.filePath, data)
	a.saveMu.Unlock()
	if err != nil {
		log.Error().Err(err).Msg("tgauth/anon: save failed")
	}
}

func (a *AnonIssuer) load() error {
	data, err := os.ReadFile(a.filePath)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return json.Unmarshal(data, &a.users)
}

func (a *AnonIssuer) saveNow() error {
	a.dirty = false
	data, err := json.MarshalIndent(a.users, "", "  ")
	if err != nil {
		return err
	}
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	return writeAtomic(a.filePath, data)
}

func (a *AnonIssuer) rebuildIndexes() {
	a.byToken = make(map[string]int, len(a.users))
	a.byUID = make(map[string]int, len(a.users))
	now := time.Now().UTC()
	live := a.users[:0]
	for _, u := range a.users {
		if !u.ExpiresAt.After(now) {
			continue // drop expired entries on load
		}
		live = append(live, u)
	}
	a.users = live
	for i := range a.users {
		if a.users[i].Token != "" {
			a.byToken[a.users[i].Token] = i
		}
		if a.users[i].UID != "" {
			a.byUID[a.users[i].UID] = i
		}
	}
}

// SetSessionDays updates the TTL applied to subsequent Issue calls.
// Existing sessions keep their original expiry until they're touched.
func (a *AnonIssuer) SetSessionDays(days int) {
	if days <= 0 {
		days = 30
	}
	a.mu.Lock()
	a.sessionDays = days
	a.mu.Unlock()
}

// Issue mints a fresh anon session. Returns the cookie token, the UID
// callers can stamp into responses, and the cookie expiry.
func (a *AnonIssuer) Issue(ip, ua string) (token, uid string, expiresAt time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now().UTC()
	token = randomSessionToken()
	uid = generateUniqueUID(a.byUID)
	expiresAt = now.AddDate(0, 0, a.sessionDays)
	u := AnonUser{
		Token:     token,
		UID:       uid,
		CreatedAt: now,
		ExpiresAt: expiresAt,
		LastIP:    ip,
		LastUA:    ua,
		LastSeen:  now,
	}
	a.users = append(a.users, u)
	idx := len(a.users) - 1
	a.byToken[token] = idx
	a.byUID[uid] = idx
	if err := a.saveNow(); err != nil {
		log.Error().Err(err).Msg("tgauth/anon: issue save failed")
	}
	return token, uid, expiresAt
}

// Lookup returns the anon user owning the token. Expired sessions are
// reported as missing.
func (a *AnonIssuer) Lookup(token string) (AnonUser, bool) {
	if token == "" {
		return AnonUser{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	idx, ok := a.byToken[token]
	if !ok {
		return AnonUser{}, false
	}
	u := a.users[idx]
	if !u.ExpiresAt.After(time.Now().UTC()) {
		return AnonUser{}, false
	}
	return u, true
}

// LookupAuth satisfies auth.AnonTokenChecker — returns the bits the
// middleware needs to build a User. Touches LastSeen/LastIP as a side
// effect so the admin list shows recent activity.
func (a *AnonIssuer) LookupAuth(token, ip string) (uid string, expiresAt time.Time, ok bool) {
	if token == "" {
		return "", time.Time{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	idx, exists := a.byToken[token]
	if !exists {
		return "", time.Time{}, false
	}
	u := &a.users[idx]
	if !u.ExpiresAt.After(time.Now().UTC()) {
		return "", time.Time{}, false
	}
	if ip != "" {
		u.LastIP = ip
	}
	u.LastSeen = time.Now().UTC()
	a.dirty = true // background saver picks it up
	return u.UID, u.ExpiresAt, true
}

// Revoke deletes an anon session. Idempotent.
func (a *AnonIssuer) Revoke(token string) error {
	if token == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	idx, ok := a.byToken[token]
	if !ok {
		return nil
	}
	uid := a.users[idx].UID
	a.users = append(a.users[:idx], a.users[idx+1:]...)
	delete(a.byToken, token)
	delete(a.byUID, uid)
	// Index shifts post-idx — rebuild.
	a.rebuildIndexes()
	return a.saveNow()
}

// List returns a snapshot of every issued anon user. Used by the admin
// panel to show "currently anonymous visitors."
func (a *AnonIssuer) List() []AnonUser {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]AnonUser, len(a.users))
	copy(out, a.users)
	return out
}

// Purge wipes every anon session. Called when the admin switches mode
// away from "none" — the leftover cookies would otherwise grant access
// once anonymous mode is meant to be off.
func (a *AnonIssuer) Purge() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.users = nil
	a.byToken = make(map[string]int)
	a.byUID = make(map[string]int)
	return a.saveNow()
}

// CleanExpired removes any session whose ExpiresAt has passed.
func (a *AnonIssuer) CleanExpired() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now().UTC()
	live := a.users[:0]
	removed := 0
	for _, u := range a.users {
		if u.ExpiresAt.After(now) {
			live = append(live, u)
		} else {
			removed++
		}
	}
	if removed == 0 {
		return 0
	}
	a.users = live
	a.rebuildIndexes()
	_ = a.saveNow()
	return removed
}

// ErrAnonDisabled is returned by Issue when the issuer is nil — caller
// dispatches code that should never run with mode≠"none".
var ErrAnonDisabled = errors.New("tgauth: anon issuer disabled")
