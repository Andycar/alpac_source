package calendar

import (
	"testing"
	"time"
)

func item(id string, due time.Time) PendingItem {
	return PendingItem{N: Notification{ID: id, Title: "Т"}, TGText: "текст " + id, DueAt: due}
}

// TestTakeDueRemovesOnlyRipeItems: разбор очереди не должен забирать то, чей срок
// ещё не пришёл.
func TestTakeDueRemovesOnlyRipeItems(t *testing.T) {
	p := NewPendingStore(t.TempDir())
	now := time.Now().UTC()
	p.Add(1, item("ripe", now.Add(-time.Minute)))
	p.Add(1, item("later", now.Add(time.Hour)))

	got := p.TakeDue(now)
	if len(got[1]) != 1 || got[1][0].N.ID != "ripe" {
		t.Fatalf("забрано не то: %+v", got[1])
	}
	if p.Count(1) != 1 {
		t.Fatalf("в очереди должен остаться один, осталось %d", p.Count(1))
	}
}

// TestTakeDueIsAtomic — повторный вызов не должен отдать то же самое ещё раз,
// иначе падение между чтением и очисткой дублировало бы рассылку.
func TestTakeDueIsAtomic(t *testing.T) {
	p := NewPendingStore(t.TempDir())
	now := time.Now().UTC()
	p.Add(1, item("a", now.Add(-time.Minute)))

	if len(p.TakeDue(now)[1]) != 1 {
		t.Fatal("первый вызов должен отдать уведомление")
	}
	if len(p.TakeDue(now)[1]) != 0 {
		t.Fatal("второй вызов отдал то же самое — будет дубликат у пользователя")
	}
}

// TestPendingSurvivesRestart: новость о выходе фильма, ждущая утра, не должна
// исчезать при рестарте сервера.
func TestPendingSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	p := NewPendingStore(dir)
	p.Add(7, item("a", now.Add(time.Hour)))

	if NewPendingStore(dir).Count(7) != 1 {
		t.Fatal("очередь не переживает перезапуск")
	}
}

// TestPendingCapDropsOldest: при долгой тишине и десятках подписок очередь
// реально растёт; свежая новость важнее той, что не смогли доставить сутки назад.
func TestPendingCapDropsOldest(t *testing.T) {
	p := NewPendingStore(t.TempDir())
	now := time.Now().UTC()
	for i := 0; i < maxPendingPerUser+5; i++ {
		p.Add(1, item(string(rune('a'+i%26))+string(rune('0'+i/26)), now.Add(-time.Minute)))
	}
	if p.Count(1) != maxPendingPerUser {
		t.Fatalf("кап не сработал: %d", p.Count(1))
	}
	got := p.TakeDue(now)[1]
	// Последним добавленным был индекс maxPendingPerUser+4 — он обязан остаться.
	last := string(rune('a'+(maxPendingPerUser+4)%26)) + string(rune('0'+(maxPendingPerUser+4)/26))
	if got[len(got)-1].N.ID != last {
		t.Fatalf("выброшено свежее вместо старого: последний %q, ожидался %q", got[len(got)-1].N.ID, last)
	}
}

// TestRescheduleAfterSettingsChange: передвинул свод с 20:00 на 09:00 — очередь
// не должна продолжать жить по старому времени.
func TestRescheduleAfterSettingsChange(t *testing.T) {
	p := NewPendingStore(t.TempDir())
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	p.Add(1, item("a", now.Add(10*time.Hour))) // ждёт 20:00

	p.Reschedule(1, Delivery{}, now) // настройки сняты → отправлять сразу
	if len(p.TakeDue(now)[1]) != 1 {
		t.Fatal("после снятия ограничений очередь должна разобраться немедленно")
	}
}

func TestFlushMakesEverythingDue(t *testing.T) {
	p := NewPendingStore(t.TempDir())
	now := time.Now().UTC()
	p.Add(1, item("a", now.Add(5*time.Hour)))
	p.Add(1, item("b", now.Add(9*time.Hour)))

	if n := p.Flush(1, now); n != 2 {
		t.Fatalf("Flush вернул %d, ожидалось 2", n)
	}
	if len(p.TakeDue(now)[1]) != 2 {
		t.Fatal("после «прислать сейчас» очередь должна отдаться целиком")
	}
}
