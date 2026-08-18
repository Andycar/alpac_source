package calendar

import (
	"testing"
	"time"
)

// utcAt строит момент UTC с заданным часом и минутой.
func utcAt(h, m int) time.Time {
	return time.Date(2026, 7, 30, h, m, 0, 0, time.UTC)
}

// TestQuietWindowAcrossMidnight — то, из-за чего вся логика вынесена отдельно:
// «тихо с 23:00 до 08:00» при наивном сравнении from <= x < to не работает никогда.
func TestQuietWindowAcrossMidnight(t *testing.T) {
	d := Delivery{QuietFrom: "23:00", QuietTo: "08:00"} // TZ = UTC

	cases := map[string]struct {
		at   time.Time
		want bool
	}{
		"полночь":          {utcAt(0, 0), true},
		"три ночи":         {utcAt(3, 0), true},
		"без минуты восемь": {utcAt(7, 59), true},
		"ровно восемь":     {utcAt(8, 0), false}, // конец окна не включается
		"полдень":          {utcAt(12, 0), false},
		"ровно 23:00":      {utcAt(23, 0), true}, // начало включается
		"22:59":            {utcAt(22, 59), false},
	}
	for name, tc := range cases {
		if got := d.InQuietHours(tc.at); got != tc.want {
			t.Errorf("%s: InQuietHours = %v, want %v", name, got, tc.want)
		}
	}
}

func TestQuietWindowSameDay(t *testing.T) {
	d := Delivery{QuietFrom: "09:00", QuietTo: "18:00"}
	if !d.InQuietHours(utcAt(10, 0)) {
		t.Error("внутри обычного окна должно быть тихо")
	}
	if d.InQuietHours(utcAt(20, 0)) {
		t.Error("вне окна тишины быть не должно")
	}
}

func TestQuietUnsetOrZeroLength(t *testing.T) {
	if (Delivery{}).InQuietHours(utcAt(3, 0)) {
		t.Error("ненастроенная тишина не должна срабатывать")
	}
	// Окно нулевой длины трактуем как «не задано», а не как «тихо круглые сутки»:
	// иначе случайно совпавшие значения замолчали бы навсегда.
	if (Delivery{QuietFrom: "22:00", QuietTo: "22:00"}).InQuietHours(utcAt(22, 0)) {
		t.Error("окно нулевой длины не должно глушить всё")
	}
	if (Delivery{QuietFrom: "мусор", QuietTo: "08:00"}).InQuietHours(utcAt(3, 0)) {
		t.Error("непонятное значение должно означать «не задано»")
	}
}

// TestQuietRespectsTimezone: сервер живёт в UTC, а «не будить ночью» означает
// ночь у пользователя. Без сдвига настройка была бы бессмысленной.
func TestQuietRespectsTimezone(t *testing.T) {
	// Москва: UTC+3. 23:00 по Москве = 20:00 UTC.
	d := Delivery{QuietFrom: "23:00", QuietTo: "08:00", TZOffset: 180}

	if !d.InQuietHours(utcAt(20, 30)) {
		t.Error("20:30 UTC — это 23:30 в Москве, должно быть тихо")
	}
	if d.InQuietHours(utcAt(17, 0)) {
		t.Error("17:00 UTC — это 20:00 в Москве, тишины быть не должно")
	}
}

func TestQuietRespectsNegativeTimezone(t *testing.T) {
	// UTC-5. 23:00 местного = 04:00 UTC следующих суток.
	d := Delivery{QuietFrom: "23:00", QuietTo: "08:00", TZOffset: -300}
	if !d.InQuietHours(utcAt(4, 0)) {
		t.Error("04:00 UTC — это 23:00 при UTC-5, должно быть тихо")
	}
	if d.InQuietHours(utcAt(20, 0)) {
		t.Error("20:00 UTC — это 15:00 при UTC-5, тишины быть не должно")
	}
}

// TestDeferredNotDropped — уведомление, попавшее в тихие часы, должно уйти ПОСЛЕ
// окна, а не исчезнуть: потерянную новость о выходе фильма никто не заметит.
func TestDeferredNotDropped(t *testing.T) {
	d := Delivery{QuietFrom: "23:00", QuietTo: "08:00"}

	if due := d.DueAt(utcAt(12, 0)); !due.IsZero() {
		t.Fatalf("днём должно уходить сразу, а отложено до %v", due)
	}

	due := d.DueAt(utcAt(3, 0))
	if due.IsZero() {
		t.Fatal("ночью уведомление должно быть отложено")
	}
	if due.Hour() != 8 || due.Minute() != 0 {
		t.Fatalf("отложено до %02d:%02d, ожидалось 08:00", due.Hour(), due.Minute())
	}
	if !due.After(utcAt(3, 0)) {
		t.Fatal("момент отправки должен быть в будущем")
	}
}

func TestQuietEndCrossesToNextDay(t *testing.T) {
	// 23:30 — окно кончается в 08:00 УЖЕ СЛЕДУЮЩИХ суток.
	d := Delivery{QuietFrom: "23:00", QuietTo: "08:00"}
	due, ok := d.QuietEndsAt(utcAt(23, 30))
	if !ok {
		t.Fatal("в 23:30 тишина активна")
	}
	if due.Day() != 31 || due.Hour() != 8 {
		t.Fatalf("ожидалось 31-е 08:00, получено %v", due)
	}
}

func TestDigestModeQueuesUntilItsTime(t *testing.T) {
	d := Delivery{Mode: DeliveryDigest, DigestAt: "20:00"}

	due := d.DueAt(utcAt(9, 0))
	if due.IsZero() {
		t.Fatal("в режиме свода мгновенных отправок быть не должно")
	}
	if due.Hour() != 20 {
		t.Fatalf("свод назначен на %02d ч, ожидалось 20", due.Hour())
	}

	// Уже после времени свода — значит следующий свод завтра.
	late := d.DueAt(utcAt(21, 0))
	if late.Day() != 31 || late.Hour() != 20 {
		t.Fatalf("ожидался свод завтра в 20:00, получено %v", late)
	}
}

// TestDigestWithoutTimeFallsBackToInstant: режим свода без времени — битая
// настройка. Молчать вечно хуже, чем прислать сразу.
func TestDigestWithoutTimeFallsBackToInstant(t *testing.T) {
	if due := (Delivery{Mode: DeliveryDigest}).DueAt(utcAt(9, 0)); !due.IsZero() {
		t.Fatalf("без времени свода надо отправлять сразу, отложено до %v", due)
	}
}

// TestEmptyDeliveryIsInstant: записи, созданные до появления расписания, не
// должны внезапно замолчать.
func TestEmptyDeliveryIsInstant(t *testing.T) {
	var d Delivery
	if !d.Instant() {
		t.Fatal("пустая настройка должна означать «сразу»")
	}
	if due := d.DueAt(utcAt(3, 0)); !due.IsZero() {
		t.Fatalf("без настроек отправляем сразу, отложено до %v", due)
	}
}

func TestNextDigestSecondsAreTruncated(t *testing.T) {
	// Момент со секундами: время срабатывания должно быть ровным, иначе повторный
	// расчёт мог бы промахнуться на сутки.
	now := time.Date(2026, 7, 30, 9, 0, 37, 500, time.UTC)
	due, ok := (Delivery{Mode: DeliveryDigest, DigestAt: "20:00"}).NextDigestAt(now)
	if !ok {
		t.Fatal("время свода задано")
	}
	if due.Second() != 0 || due.Nanosecond() != 0 {
		t.Fatalf("время срабатывания не обнулено: %v", due)
	}
}
