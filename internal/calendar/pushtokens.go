package calendar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// PushTokens maps a Telegram user to the device tokens that should receive push
// notifications.
//
// Per user, not per device session: someone with a phone and a TV box wants the
// alert on both, and FCM tokens outlive our own device records (a token survives
// a re-login but dies on reinstall). So this is its own registry rather than a
// field on the device list.
type PushTokens struct {
	mu    sync.RWMutex
	users []pushUser
	path  string
}

type pushUser struct {
	TelegramID int64       `json:"telegram_id"`
	Devices    []PushToken `json:"devices"`
}

// PushToken is one delivery endpoint.
type PushToken struct {
	Token string `json:"token"`
	// Platform is informational ("android"), kept so a future APNs channel can
	// filter without a schema change.
	Platform string `json:"platform,omitempty"`
	// DeviceUID ties the token to our own device record when the client knows
	// it, so "unbind this device" can drop its push too.
	DeviceUID string    `json:"device_uid,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// maxTokensPerUser bounds the fan-out. Tokens are refreshed, not accumulated —
// a user genuinely owning more than this many live devices is not a real case,
// but a client bug re-registering a fresh token each launch is, and unbounded
// growth would turn one alert into thousands of sends.
const maxTokensPerUser = 20

// NewPushTokens loads (or creates) the registry under <repoRoot>/database/calendar.
func NewPushTokens(repoRoot string) *PushTokens {
	dir := filepath.Join(repoRoot, "database", "calendar")
	_ = os.MkdirAll(dir, 0o755)
	p := &PushTokens{path: filepath.Join(dir, "push_tokens.json")}
	p.load()
	return p
}

// Register records a token for a user.
//
// The token is removed from every OTHER user first. FCM tokens are per app
// install, so the same token turning up under a new Telegram id means the device
// was handed over or re-logged-in — leaving the old binding would keep pushing
// one person's subscriptions to somebody else's phone.
func (p *PushTokens) Register(tgID int64, t PushToken) {
	if tgID == 0 || t.Token == "" {
		return
	}
	t.UpdatedAt = time.Now().UTC()

	p.mu.Lock()
	defer p.mu.Unlock()

	p.removeTokenLocked(t.Token, tgID)

	u := p.findOrCreateLocked(tgID)
	for i := range u.Devices {
		if u.Devices[i].Token == t.Token {
			u.Devices[i] = t
			p.saveLocked()
			return
		}
	}
	u.Devices = append(u.Devices, t)

	if len(u.Devices) > maxTokensPerUser {
		// Drop the least recently refreshed — a live device re-registers on every
		// launch, so the stale entries are the ones that stopped checking in.
		sort.SliceStable(u.Devices, func(i, j int) bool {
			return u.Devices[i].UpdatedAt.After(u.Devices[j].UpdatedAt)
		})
		u.Devices = u.Devices[:maxTokensPerUser]
	}
	p.saveLocked()
}

// Unregister drops a token wherever it is bound. Called on logout and whenever
// FCM reports the token as dead.
func (p *PushTokens) Unregister(token string) {
	if token == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.removeTokenLocked(token, 0) {
		p.saveLocked()
	}
}

// UnregisterDevice drops whatever token belongs to one of our device records,
// so unbinding a device in the cabinet also stops its push.
func (p *PushTokens) UnregisterDevice(tgID int64, deviceUID string) {
	if deviceUID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	changed := false
	for i := range p.users {
		if tgID != 0 && p.users[i].TelegramID != tgID {
			continue
		}
		kept := p.users[i].Devices[:0]
		for _, d := range p.users[i].Devices {
			if d.DeviceUID == deviceUID {
				changed = true
				continue
			}
			kept = append(kept, d)
		}
		p.users[i].Devices = kept
	}
	if changed {
		p.saveLocked()
	}
}

// TokensOf returns the delivery endpoints for a user.
func (p *PushTokens) TokensOf(tgID int64) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for i := range p.users {
		if p.users[i].TelegramID != tgID {
			continue
		}
		out := make([]string, 0, len(p.users[i].Devices))
		for _, d := range p.users[i].Devices {
			out = append(out, d.Token)
		}
		return out
	}
	return nil
}

// Count reports how many devices a user has registered (for the cabinet UI).
func (p *PushTokens) Count(tgID int64) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := range p.users {
		if p.users[i].TelegramID == tgID {
			return len(p.users[i].Devices)
		}
	}
	return 0
}

// removeTokenLocked strips a token from every user except `keep` (0 = all).
// Returns whether anything changed.
func (p *PushTokens) removeTokenLocked(token string, keep int64) bool {
	changed := false
	for i := range p.users {
		if keep != 0 && p.users[i].TelegramID == keep {
			continue
		}
		kept := p.users[i].Devices[:0]
		for _, d := range p.users[i].Devices {
			if d.Token == token {
				changed = true
				continue
			}
			kept = append(kept, d)
		}
		p.users[i].Devices = kept
	}
	return changed
}

func (p *PushTokens) findOrCreateLocked(tgID int64) *pushUser {
	for i := range p.users {
		if p.users[i].TelegramID == tgID {
			return &p.users[i]
		}
	}
	p.users = append(p.users, pushUser{TelegramID: tgID})
	return &p.users[len(p.users)-1]
}

func (p *PushTokens) load() {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &p.users)
}

func (p *PushTokens) saveLocked() {
	data, err := json.MarshalIndent(p.users, "", "  ")
	if err != nil {
		return
	}
	// 0600, not 0644: a device token is a send-anything-to-this-phone capability.
	_ = os.WriteFile(p.path, data, 0o600)
}
