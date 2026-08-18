package calendar

// Когда именно беспокоить пользователя.
//
// До этого любое уведомление уходило в Telegram и в push в момент обнаружения.
// Для одного сериала это нормально, для двадцати подписок — поток сообщений,
// причём в три часа ночи, когда сериал выложили за океаном.
//
// Здесь только правила времени: чистые функции, без хранилища и без отправки.
// Из-за перехода через полночь («тихо с 23:00 до 08:00») это ровно то место,
// где заводятся ошибки, поэтому логика вынесена отдельно и покрыта тестами.

import (
	"fmt"
	"strings"
	"time"
)

// Режимы доставки.
const (
	// DeliveryInstant — сразу по выходу (поведение по умолчанию, как было).
	DeliveryInstant = "instant"
	// DeliveryDigest — накопить и прислать одним сводом в заданное время.
	DeliveryDigest = "digest"
)

// Delivery — пользовательские настройки «когда беспокоить».
//
// Хранится строками "ЧЧ:ММ", а не минутами: значение читаемо в JSON-файле
// хранилища, и его можно поправить руками, не пересчитывая.
type Delivery struct {
	Mode string `json:"mode,omitempty"` // instant | digest
	// DigestAt — во сколько присылать свод (местное время пользователя).
	DigestAt string `json:"digest_at,omitempty"`
	// QuietFrom/QuietTo — окно тишины. Может переходить через полночь.
	QuietFrom string `json:"quiet_from,omitempty"`
	QuietTo   string `json:"quiet_to,omitempty"`
	// TZOffset — сдвиг часового пояса пользователя от UTC в минутах.
	//
	// ★Без него настройка бессмысленна: сервер живёт в UTC, а «в 20:00» означает
	// восемь вечера У ПОЛЬЗОВАТЕЛЯ. Telegram часовой пояс не сообщает, поэтому
	// значение присылает клиент (в браузере это -getTimezoneOffset()).
	TZOffset int `json:"tz_offset,omitempty"`
}

// Instant отвечает, ждёт ли пользователь уведомления сразу.
// Пустой режим = instant: записи, созданные до появления расписания, не должны
// внезапно замолчать.
func (d Delivery) Instant() bool { return d.Mode != DeliveryDigest }

// parseHHMM разбирает "ЧЧ:ММ" в минуты от полуночи. ok=false для мусора и пустой
// строки — вызывающий тогда считает, что настройка не задана.
func parseHHMM(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// localMinutes переводит момент времени в минуты от полуночи в поясе пользователя.
func localMinutes(t time.Time, tzOffset int) int {
	mins := t.UTC().Hour()*60 + t.UTC().Minute() + tzOffset
	// Приводим в [0, 1440): сдвиг может вынести и вперёд (UTC+14), и назад (UTC-11).
	mins %= 24 * 60
	if mins < 0 {
		mins += 24 * 60
	}
	return mins
}

// InQuietHours — попадает ли момент в окно тишины.
//
// Окно НОРМАЛЬНО переходит через полночь: 23:00–08:00 — самый частый вариант,
// и именно он ломается при наивном сравнении from <= x < to.
func (d Delivery) InQuietHours(now time.Time) bool {
	from, okFrom := parseHHMM(d.QuietFrom)
	to, okTo := parseHHMM(d.QuietTo)
	if !okFrom || !okTo || from == to {
		return false // не задано (или окно нулевой длины) — тишины нет
	}
	x := localMinutes(now, d.TZOffset)
	if from < to {
		return x >= from && x < to
	}
	// Переход через полночь: тихо ПОСЛЕ from ИЛИ ДО to.
	return x >= from || x < to
}

// QuietEndsAt возвращает момент окончания окна тишины, наступающий после now.
// Второе значение false, если тишина не настроена или сейчас не тихий час.
func (d Delivery) QuietEndsAt(now time.Time) (time.Time, bool) {
	if !d.InQuietHours(now) {
		return time.Time{}, false
	}
	to, ok := parseHHMM(d.QuietTo)
	if !ok {
		return time.Time{}, false
	}
	return nextLocalTime(now, to, d.TZOffset), true
}

// NextDigestAt — когда наступит следующее время свода после now.
func (d Delivery) NextDigestAt(now time.Time) (time.Time, bool) {
	at, ok := parseHHMM(d.DigestAt)
	if !ok {
		return time.Time{}, false
	}
	return nextLocalTime(now, at, d.TZOffset), true
}

// nextLocalTime — ближайший момент в БУДУЩЕМ, когда местное время пользователя
// станет равно targetMin минут от полуночи.
func nextLocalTime(now time.Time, targetMin, tzOffset int) time.Time {
	cur := localMinutes(now, tzOffset)
	delta := targetMin - cur
	if delta <= 0 {
		delta += 24 * 60 // уже прошло сегодня — значит завтра
	}
	// Секунды и наносекунды обнуляем: иначе «20:00» срабатывало бы в 20:00:37,
	// и повторный расчёт мог бы дать сдвиг на сутки.
	return now.UTC().Truncate(time.Minute).Add(time.Duration(delta) * time.Minute)
}

// DueAt решает, КОГДА отправлять уведомление, появившееся в момент now.
//
// Возвращает нулевое время, если отправлять надо немедленно. Иначе — момент, до
// которого уведомление лежит в очереди.
//
// ★Отложенное НЕ выбрасывается. Тихие часы означают «не будить сейчас», а не
// «потерять новость о выходе фильма»: пропавшее уведомление пользователь не
// сможет ни заметить, ни попросить повторить.
func (d Delivery) DueAt(now time.Time) time.Time {
	if !d.Instant() {
		if at, ok := d.NextDigestAt(now); ok {
			return at
		}
		// Режим свода без времени — некорректная настройка; чем молчать вечно,
		// лучше вести себя как instant.
		return time.Time{}
	}
	if at, ok := d.QuietEndsAt(now); ok {
		return at
	}
	return time.Time{}
}
