package httpapi

// Отметка активности устройства на путях, которые ОБХОДЯТ tg-гейт.
//
// LastSeen у привязанного устройства обновлялся ровно в одном месте — внутри
// tgAuthGateMiddleware. Но первым же правилом гейта стоит gatePreAuthAllowed, и
// в нём среди прочего:
//
//	/capi/*  — «self-authenticates» (подписанные запросы клиента)
//	/tmdb/*  — сквозной прокси к TMDB
//	.m3u8/.ts/.mp4 — сегменты плеера
//
// То есть весь обычный день пользователя — открыть приложение, полистать
// каталог, посмотреть кино — проходил МИМО единственного места, где активность
// записывается. В кабинете напротив живых устройств стояло «нет данных об
// активности»: отметка ставилась при привязке и больше не двигалась.
//
// Здесь мы закрываем именно клиентский API: /capi/* c параметром uid. Сегменты
// плеера сознательно НЕ трогаем — они летят сотнями в минуту, а прогресс
// просмотра клиент и так шлёт через /capi/history.

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/tgauth"
)

const (
	// Как часто вообще заглядывать в стор из-за одного устройства. Активность
	// измеряется минутами, а запросов от клиента — десятки в минуту.
	devActThrottle = time.Minute
	// Как часто сохранять её на диск. LastSeen жил только в памяти, поэтому
	// перезапуск сервиса откатывал активность к тому, что было записано ранее
	// по другим причинам. Раз в 10 минут — компромисс между «переживает
	// рестарт» и «не пишем стор из-за каждой минуты».
	devActPersist = 10 * time.Minute
	// Предел карты: uid'ы приходят снаружи, расти без ограничений ей нельзя.
	devActMaxEntries = 50_000
)

type devActMark struct {
	touched time.Time // когда последний раз трогали стор
	saved   time.Time // когда последний раз просили сохранение
}

var (
	devActMu   sync.Mutex
	devActSeen = make(map[string]devActMark)
)

// devActShould отвечает, надо ли сейчас трогать стор для этого uid и надо ли
// при этом просить сохранение.
func devActShould(uid string, now time.Time) (touch, persist bool) {
	devActMu.Lock()
	defer devActMu.Unlock()
	m, ok := devActSeen[uid]
	if ok && now.Sub(m.touched) < devActThrottle {
		return false, false
	}
	if len(devActSeen) >= devActMaxEntries && !ok {
		// Переполнение — карта сбрасывается целиком. Она лишь троттлит записи:
		// потеря состояния стоит одного лишнего обновления на устройство.
		devActSeen = make(map[string]devActMark)
		m = devActMark{}
	}
	persist = m.saved.IsZero() || now.Sub(m.saved) >= devActPersist
	m.touched = now
	if persist {
		m.saved = now
	}
	devActSeen[uid] = m
	return true, persist
}

// touchDeviceActivity отмечает устройство живым по запросу клиентского API.
// Тихий no-op, если путь не /capi/, нет uid или токен не находится.
func touchDeviceActivity(store *tgauth.Store, r *http.Request) {
	if store == nil || !strings.HasPrefix(r.URL.Path, "/capi/") {
		return
	}
	uid := strings.TrimSpace(r.URL.Query().Get("uid"))
	if uid == "" {
		return
	}
	now := time.Now().UTC()
	touch, persist := devActShould(uid, now)
	if !touch {
		return
	}
	cands := collectLampacTokenCandidates(r)
	for _, tok := range cands {
		if !store.HasDevice(tok, uid) {
			continue
		}
		store.UpdateLastSeen(tok, uid)
		if persist {
			store.PersistLastSeen(tok, uid)
		}
		store.UpdateDeviceIP(tok, uid, clientIP(r))
		return
	}
	// Дошли сюда — запрос принёс uid, но ни один предъявленный токен этим устройством не владеет.
	// Строка диагностическая и намеренно остаётся в проде: молчаливый промах здесь означает
	// «активность снова не пишется», а искать это вслепую я уже один раз пробовал.
	devActMissLog(uid, len(cands), r.URL.Path)
}

// devActMissLog печатает промахи не чаще раза в минуту — иначе на потоке /capi это тысячи строк.
var devActMissAt time.Time

func devActMissLog(uid string, cands int, path string) {
	devActMu.Lock()
	if time.Since(devActMissAt) < time.Minute {
		devActMu.Unlock()
		return
	}
	devActMissAt = time.Now()
	devActMu.Unlock()
	log.Warn().Str("uid", uid).Int("токенов_в_запросе", cands).Str("path", path).
		Msg("активность устройства не отмечена: токен запроса не владеет этим uid")
}
