package calendar

// Очередь отложенных уведомлений.
//
// Нужна из-за двух настроек: тихих часов («не будить ночью») и режима свода
// («присылай одним сообщением в восемь вечера»). В обоих случаях уведомление
// уже найдено, но отправлять его сейчас нельзя.
//
// ★Очередь на диске, а не в памяти: рестарт сервера не должен съедать новость о
// выходе фильма, которая ждала утра.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PendingItem — уведомление, ждущее своего часа.
type PendingItem struct {
	N      Notification `json:"n"`
	TGText string       `json:"tg_text"`
	DueAt  time.Time    `json:"due_at"`
}

type pendingUser struct {
	TelegramID int64         `json:"telegram_id"`
	Items      []PendingItem `json:"items"`
}

// maxPendingPerUser ограничивает очередь. Свод из двухсот пунктов никто читать
// не станет, а расти без границ она не должна: при долгой тишине и десятках
// подписок это реальный объём.
const maxPendingPerUser = 50

// PendingStore хранит очередь по пользователям.
type PendingStore struct {
	mu    sync.Mutex
	users []pendingUser
	path  string
}

func NewPendingStore(repoRoot string) *PendingStore {
	dir := filepath.Join(repoRoot, "database", "calendar")
	_ = os.MkdirAll(dir, 0o755)
	p := &PendingStore{path: filepath.Join(dir, "pending.json")}
	p.load()
	return p
}

// Add кладёт уведомление в очередь пользователя.
func (p *PendingStore) Add(tgID int64, item PendingItem) {
	if tgID == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	u := p.findOrCreateLocked(tgID)
	u.Items = append(u.Items, item)
	if len(u.Items) > maxPendingPerUser {
		// Отбрасываем САМЫЕ СТАРЫЕ: свежая новость важнее той, что не смогли
		// доставить сутки назад.
		u.Items = u.Items[len(u.Items)-maxPendingPerUser:]
	}
	p.saveLocked()
}

// TakeDue забирает всё, чей срок пришёл, и удаляет из очереди.
//
// Забирает и удаляет одной операцией: если бы вызывающий читал, отправлял и
// потом чистил, падение между шагами повторило бы рассылку.
func (p *PendingStore) TakeDue(now time.Time) map[int64][]PendingItem {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := map[int64][]PendingItem{}
	changed := false
	for i := range p.users {
		keep := p.users[i].Items[:0]
		for _, it := range p.users[i].Items {
			if it.DueAt.After(now) {
				keep = append(keep, it)
				continue
			}
			out[p.users[i].TelegramID] = append(out[p.users[i].TelegramID], it)
			changed = true
		}
		p.users[i].Items = keep
	}
	if changed {
		p.saveLocked()
	}
	return out
}

// Count — сколько уведомлений ждёт отправки (для интерфейса настроек).
func (p *PendingStore) Count(tgID int64) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.users {
		if p.users[i].TelegramID == tgID {
			return len(p.users[i].Items)
		}
	}
	return 0
}

// Flush переносит срок всех отложенных уведомлений пользователя на «сейчас» —
// «прислать не дожидаясь». Возвращает сколько.
func (p *PendingStore) Flush(tgID int64, now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.users {
		if p.users[i].TelegramID != tgID {
			continue
		}
		n := len(p.users[i].Items)
		for j := range p.users[i].Items {
			p.users[i].Items[j].DueAt = now
		}
		if n > 0 {
			p.saveLocked()
		}
		return n
	}
	return 0
}

// Reschedule пересчитывает сроки после смены настроек.
//
// Иначе очередь жила бы по старым правилам: человек передвинул свод с 20:00 на
// 09:00 — и всё равно ждал бы до восьми вечера, не понимая почему.
func (p *PendingStore) Reschedule(tgID int64, d Delivery, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.users {
		if p.users[i].TelegramID != tgID {
			continue
		}
		if len(p.users[i].Items) == 0 {
			return
		}
		due := d.DueAt(now)
		if due.IsZero() {
			due = now // новые настройки разрешают отправку — отдаём при первом же разборе
		}
		for j := range p.users[i].Items {
			p.users[i].Items[j].DueAt = due
		}
		p.saveLocked()
		return
	}
}

func (p *PendingStore) findOrCreateLocked(tgID int64) *pendingUser {
	for i := range p.users {
		if p.users[i].TelegramID == tgID {
			return &p.users[i]
		}
	}
	p.users = append(p.users, pendingUser{TelegramID: tgID})
	return &p.users[len(p.users)-1]
}

func (p *PendingStore) load() {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &p.users)
}

func (p *PendingStore) saveLocked() {
	data, err := json.MarshalIndent(p.users, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p.path, data, 0o644)
}
