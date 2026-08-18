package calendar

// In-app notification inbox.
//
// Telegram delivery is fire-and-forget: the bot pushes a message and the story
// ends. The app needs the opposite — a list the client can open later, with a
// read/unread state — so alerts survive the user not having the app open when a
// title dropped. The cron writes here in the same breath as it sends to TG; the
// two channels are independent and either can be muted per user.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Notification kinds, mirrored by the clients for iconography.
const (
	NotifyEpisode = "episode" // a new episode aired
	NotifyRelease = "release" // a tracked movie is out
	NotifyVoice   = "voice"   // a new translation appeared
)

// Notification is one entry in a user's inbox.
type Notification struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	TmdbID     int       `json:"tmdb_id,omitempty"`
	MediaType  string    `json:"media_type,omitempty"` // "tv" | "movie" — lets the client open the card
	Title      string    `json:"title"`
	Text       string    `json:"text"`
	PosterPath string    `json:"poster_path,omitempty"`
	Season     int       `json:"season,omitempty"`
	Episode    int       `json:"episode,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Read       bool      `json:"read"`
}

// maxInboxPerUser caps retention. Notifications are disposable — the card is the
// source of truth — so an unbounded log would only cost disk and load time.
const maxInboxPerUser = 100

type inboxUser struct {
	TelegramID int64          `json:"telegram_id"`
	Items      []Notification `json:"items"`
}

// Inbox stores per-user notifications, persisted next to the subscriptions.
type Inbox struct {
	mu    sync.RWMutex
	users []inboxUser
	path  string
	seq   uint64
}

// NewInbox loads (or creates) the inbox under <repoRoot>/database/calendar.
func NewInbox(repoRoot string) *Inbox {
	dir := filepath.Join(repoRoot, "database", "calendar")
	_ = os.MkdirAll(dir, 0o755)
	ib := &Inbox{path: filepath.Join(dir, "notifications.json")}
	ib.load()
	return ib
}

// Push prepends a notification for a user and returns the stored copy (with its
// id filled in) so the caller can hand the same object to a live push.
func (ib *Inbox) Push(tgID int64, n Notification) Notification {
	ib.mu.Lock()
	defer ib.mu.Unlock()

	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	ib.seq++
	n.ID = strconv.FormatInt(n.CreatedAt.UnixMilli(), 36) + "-" + strconv.FormatUint(ib.seq, 36)

	u := ib.findOrCreate(tgID)
	u.Items = append([]Notification{n}, u.Items...)
	if len(u.Items) > maxInboxPerUser {
		u.Items = u.Items[:maxInboxPerUser]
	}
	ib.save()
	return n
}

// List returns a user's notifications, newest first.
func (ib *Inbox) List(tgID int64) []Notification {
	ib.mu.RLock()
	defer ib.mu.RUnlock()

	u := ib.find(tgID)
	if u == nil {
		return []Notification{}
	}
	out := make([]Notification, len(u.Items))
	copy(out, u.Items)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Unread counts a user's unread notifications (for the badge).
func (ib *Inbox) Unread(tgID int64) int {
	ib.mu.RLock()
	defer ib.mu.RUnlock()

	u := ib.find(tgID)
	if u == nil {
		return 0
	}
	n := 0
	for _, it := range u.Items {
		if !it.Read {
			n++
		}
	}
	return n
}

// MarkRead marks one notification read, or all of them when id is empty.
// Returns how many entries changed.
func (ib *Inbox) MarkRead(tgID int64, id string) int {
	ib.mu.Lock()
	defer ib.mu.Unlock()

	u := ib.find(tgID)
	if u == nil {
		return 0
	}
	changed := 0
	for i := range u.Items {
		if u.Items[i].Read || (id != "" && u.Items[i].ID != id) {
			continue
		}
		u.Items[i].Read = true
		changed++
	}
	if changed > 0 {
		ib.save()
	}
	return changed
}

// Clear drops every notification for a user.
func (ib *Inbox) Clear(tgID int64) {
	ib.mu.Lock()
	defer ib.mu.Unlock()

	if u := ib.find(tgID); u != nil {
		u.Items = nil
		ib.save()
	}
}

// ---------------------------------------------------------------------------
//  internals — callers hold the lock
// ---------------------------------------------------------------------------

func (ib *Inbox) find(tgID int64) *inboxUser {
	for i := range ib.users {
		if ib.users[i].TelegramID == tgID {
			return &ib.users[i]
		}
	}
	return nil
}

func (ib *Inbox) findOrCreate(tgID int64) *inboxUser {
	if u := ib.find(tgID); u != nil {
		return u
	}
	ib.users = append(ib.users, inboxUser{TelegramID: tgID})
	return &ib.users[len(ib.users)-1]
}

func (ib *Inbox) load() {
	data, err := os.ReadFile(ib.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &ib.users)
}

func (ib *Inbox) save() {
	data, err := json.MarshalIndent(ib.users, "", "  ")
	if err != nil {
		return
	}
	tmp := ib.path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, ib.path)
	}
}
