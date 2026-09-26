package proxylink

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"sync"
	"time"
)

// session.go — потоковая сессия: сверка «клиент ↔ сервер» поверх /proxy,
// как токен-аутентификация у CDN.
//
// Токен сам по себе предъявительский: кто держит строку, тот и смотрит. Срок
// (E) и привязка к сети (N) сужают окно, но не замечают главного — что одной
// ссылкой пользуются двое. Сессия замечает: ссылка из /api/iptv/play несёт
// sid, все сегменты его наследуют, и сервер видит поток целиком, а не
// разрозненные запросы.
//
// Правило блокировки — ОДНОВРЕМЕННОСТЬ, а не число сетей. Это важно: абонент,
// переехавший с Wi-Fi на LTE, старую сеть бросает навсегда, и запрещать ему
// переезд нельзя (у нас уже есть живой пример абонента, чей трафик выходит
// из двух сетей). Расшаренная же ссылка живёт в двух сетях ОДНОВРЕМЕННО, и
// это видно по возврату в недавно брошенную сеть. Поэтому переезд принимается
// молча, а чередование гасит поток целиком — и у вора, и у того, кто дал
// ссылку. Восстановление — один вызов /api/iptv/play (замерено: 2–4 секунды),
// но пока ссылку продолжают делить, поток будет гаснуть снова.

// Причины отказа. Как ReasonEternalRejected и ReasonForeignNetwork, это
// стабильные маркеры: прокси превращает их в 410, а не 404, потому что
// повтор того же запроса не поможет — нужна новая ссылка.
const (
	ReasonSessionMissing = "session missing"
	ReasonSessionUnknown = "session unknown"
	ReasonSessionRevoked = "session revoked"
	// ReasonTooManyStreams — исчерпан лимит одновременных потоков профиля.
	// В отличие от прочих, это НЕ признак кражи ссылки: аккаунтом пользуются
	// с большего числа устройств, чем оплачено. Отдаётся клиенту отдельным
	// ответом, а не 410, чтобы человек увидел причину, а не «поток умер».
	ReasonTooManyStreams = "too many concurrent streams"
)

// IsSessionRejection reports whether a DebugDecrypt reason is one of the
// permanent stream-session refusals.
func IsSessionRejection(reason string) bool {
	r := strings.TrimSpace(reason)
	return strings.HasPrefix(r, ReasonSessionMissing) ||
		strings.HasPrefix(r, ReasonSessionUnknown) ||
		strings.HasPrefix(r, ReasonSessionRevoked)
}

// sessionDefaults — окна по умолчанию, если конфиг молчит.
const (
	defaultSessionFlip = 90 * time.Second // возврат в брошенную сеть за это время = чередование
	defaultSessionIdle = 12 * time.Hour   // молчащая сессия убирается
	sessionRingSize    = 4                // сколько брошенных сетей помним
	// sessionActiveWindow — сколько сессия считается ИДУЩИМ потоком после
	// последнего запроса. Живой HLS перечитывает манифест каждые несколько
	// секунд, так что остановленный просмотр выпадает из счёта быстро.
	sessionActiveWindow = 2 * time.Minute
)

type netSeen struct {
	net string
	at  time.Time // когда эта сеть в последний раз что-то просила
}

// famState — состояние сессии в пределах ОДНОГО семейства адресов. Считать
// v4 и v6 одной осью нельзя: клиент с двойным стеком берёт манифест по одному
// протоколу, а сегменты по другому — и выглядел бы как вечное чередование
// сетей, то есть мы гасили бы поток честному зрителю.
type famState struct {
	cur string // текущая сеть этого семейства
	// recent — брошенные сети, свежие первыми. Возврат в любую из них внутри
	// окна означает, что обе живы одновременно.
	recent   []netSeen
	lastSeen time.Time
}

type streamSession struct {
	mu       sync.Mutex
	tgID     int64
	device   string // ключ устройства внутри профиля
	created  time.Time
	lastSeen time.Time
	// fam — состояние по семействам адресов ("v4"/"v6").
	fam         map[string]*famState
	hits        int64
	transitions int
	revoked     bool
	reason      string
}

// netFamily returns "v4"/"v6" for a prefix produced by NetPrefix, "" otherwise.
func netFamily(prefix string) string {
	switch {
	case strings.HasSuffix(prefix, "/24"):
		return "v4"
	case strings.HasSuffix(prefix, "/48"):
		return "v6"
	default:
		return ""
	}
}

type sessionStore struct {
	mu   sync.RWMutex
	byID map[string]*streamSession
	// byOwner — профиль → устройство → sid. Без этого индекса каждое
	// переключение канала заводило бы новую сессию, и лимит одновременных
	// потоков считал бы переключения, а не зрителей.
	byOwner map[int64]map[string]string
	flip    time.Duration
	idle    time.Duration
	// plugins — плагины, для которых сессия ОБЯЗАТЕЛЬНА. Для них запрос без
	// sid отвергается: иначе вор просто срезал бы ?sid= и остался бы с голым
	// предъявительским токеном.
	plugins map[string]struct{}
}

func newSessionStore() *sessionStore {
	return &sessionStore{
		byID:    map[string]*streamSession{},
		byOwner: map[int64]map[string]string{},
		flip:    defaultSessionFlip,
		idle:    defaultSessionIdle,
		plugins: map[string]struct{}{},
	}
}

// SetSessionPlugins задаёт плагины, чьи ссылки обязаны нести живую сессию.
// Пустой список полностью выключает механизм (поведение по умолчанию).
func (m *Manager) SetSessionPlugins(plugins []string, flipSec, idleMin int) {
	if m == nil || m.sessions == nil {
		return
	}

	set := make(map[string]struct{}, len(plugins))
	for _, p := range plugins {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			set[p] = struct{}{}
		}
	}
	s := m.sessions
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plugins = set
	s.flip = defaultSessionFlip
	if flipSec > 0 {
		s.flip = time.Duration(flipSec) * time.Second
	}
	s.idle = defaultSessionIdle
	if idleMin > 0 {
		s.idle = time.Duration(idleMin) * time.Minute
	}
}

// RequiresSession reports whether links of this plugin must carry a live sid.
func (m *Manager) RequiresSession(plugin string) bool {
	if m == nil || m.sessions == nil {
		return false
	}

	s := m.sessions
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.plugins) == 0 {
		return false
	}
	_, ok := s.plugins[strings.ToLower(strings.TrimSpace(plugin))]
	return ok
}

// OpenSession заводит ОДНУ сессию на запрос и возвращает её sid. Все ссылки
// этого запроса обязаны нести именно его: иначе список каналов плодил бы по
// сессии на канал — две тысячи штук на одно открытие списка.
// Пустая строка = механизм выключен (звать можно всегда).
func (m *Manager) SessionsEnabled() bool {
	if m == nil || m.sessions == nil {
		return false
	}
	s := m.sessions
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.plugins) > 0
}

// maxSessions — потолок на число живых сессий. Список каналов выдаёт ссылки
// пачками, и без потолка перебирающий клиент раздул бы карту.
const maxSessions = 200_000

func (m *Manager) OpenSession(reqip string, tgID int64, device string, limit int) (string, string) {
	if !m.SessionsEnabled() {
		return "", ""
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		// Без энтропии предсказуемый sid хуже, чем никакого: он дал бы вору
		// возможность угадать чужую сессию. Возвращаем пусто — ссылка тогда
		// не пройдёт гейт, и клиент получит честный отказ вместо дырки.
		return "", ""
	}
	sid := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now()
	s := m.sessions
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	devices := s.byOwner[tgID]
	if devices == nil {
		devices = map[string]string{}
		s.byOwner[tgID] = devices
	}
	// Это же устройство пришло снова (переключило канал) — старую сессию
	// заменяем, слот профиля не тратим. Иначе перещёлкивание каналов съело бы
	// лимит за минуту.
	if old, ok := devices[device]; ok {
		delete(s.byID, old)
		delete(devices, device)
	} else if limit > 0 && s.activeDevicesLocked(tgID, now) >= limit {
		// Новое устройство сверх оплаченного числа одновременных просмотров.
		return "", ReasonTooManyStreams
	}
	if len(s.byID) >= maxSessions {
		// Потолок достигнут — новую не заводим. Отказ честнее тихого роста:
		// ссылка уйдёт без sid и упрётся в гейт, а не съест память сервера.
		return "", ""
	}

	sess := &streamSession{
		tgID:     tgID,
		device:   device,
		created:  now,
		lastSeen: now,
		fam:      map[string]*famState{},
	}
	if p := NetPrefix(reqip); p != "" {
		sess.fam[netFamily(p)] = &famState{cur: p, lastSeen: now}
	}
	s.byID[sid] = sess
	devices[device] = sid
	return sid, ""
}

// activeDevicesLocked считает устройства профиля, чей поток ИДЁТ сейчас.
// Считаем именно активные, а не все заведённые: закрытый вчера просмотр не
// должен занимать слот, а живой HLS сам себя подтверждает каждые несколько
// секунд. Вызывать под s.mu.
func (s *sessionStore) activeDevicesLocked(tgID int64, now time.Time) int {
	n := 0
	for dev, sid := range s.byOwner[tgID] {
		sess := s.byID[sid]
		if sess == nil {
			delete(s.byOwner[tgID], dev)
			continue
		}
		sess.mu.Lock()
		alive := !sess.revoked && now.Sub(sess.lastSeen) <= sessionActiveWindow
		sess.mu.Unlock()
		if alive {
			n++
		}
	}
	return n
}

// ActiveStreams reports how many devices of this profile are streaming now.
// Для показа человеку («занято 3 из 3»), не для гейта.
func (m *Manager) ActiveStreams(tgID int64) int {
	if m == nil || m.sessions == nil {
		return 0
	}
	s := m.sessions
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeDevicesLocked(tgID, time.Now())
}

// CheckSession сверяет запрос с сессией. Возвращает "" когда всё сходится,
// иначе причину отказа. Несостыковка гасит поток целиком: сессия помечается
// отозванной, и все её ссылки — в том числе у законного держателя — начинают
// отвечать 410, пока он не перезапросит /api/iptv/play.
func (m *Manager) CheckSession(sid, reqip string) string {
	if m == nil || m.sessions == nil {
		return ""
	}

	s := m.sessions
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return ReasonSessionMissing
	}
	s.mu.RLock()
	sess := s.byID[sid]
	s.mu.RUnlock()
	if sess == nil {
		return ReasonSessionUnknown
	}

	now := time.Now()
	cur := NetPrefix(reqip)
	// Своя инфраструктура (сервер тянет собственный /proxy за вложенным
	// плейлистом, транскодер, превью) сменой сети не является: иначе внутренний
	// вызов чередовался бы с сетью зрителя и гасил живой поток.
	if m.IsTrustedNetwork(reqip) {
		cur = ""
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.revoked {
		return ReasonSessionRevoked + " (" + sess.reason + ")"
	}
	// Внутренние вызовы (health-check, превью) идут без клиентского адреса.
	// Считать их сменой сети нельзя — они бы гасили живые потоки.
	if cur == "" {
		sess.lastSeen = now
		sess.hits++
		return ""
	}
	// Сверяем в пределах семейства адресов: v4 и v6 у одного зрителя
	// чередуются штатно и конфликтом не являются.
	fam := netFamily(cur)
	st := sess.fam[fam]
	if st == nil {
		st = &famState{}
		sess.fam[fam] = st
	}
	if st.cur == "" || cur == st.cur {
		st.cur = cur
		st.lastSeen = now
		sess.lastSeen = now
		sess.hits++
		return ""
	}
	// Сеть другая. Возврат в недавно брошенную = обе сети живы одновременно,
	// то есть ссылку делят. Переезд же в новую сеть законен.
	for _, r := range st.recent {
		if r.net == cur && now.Sub(r.at) < s.flip {
			sess.revoked = true
			sess.reason = "чередование сетей " + r.net + " и " + st.cur
			return ReasonSessionRevoked + " (" + sess.reason + ")"
		}
	}
	st.recent = append([]netSeen{{net: st.cur, at: st.lastSeen}}, st.recent...)
	if len(st.recent) > sessionRingSize {
		st.recent = st.recent[:sessionRingSize]
	}
	st.cur = cur
	st.lastSeen = now
	sess.transitions++
	sess.lastSeen = now
	sess.hits++
	return ""
}

// sweepLocked убирает молчащие сессии. Зовётся при заведении новой — отдельный
// таймер ради этого держать незачем, ссылки выдаются постоянно.
func (s *sessionStore) sweepLocked(now time.Time) {
	if len(s.byID) < 256 {
		return
	}
	for id, sess := range s.byID {
		sess.mu.Lock()
		dead := now.Sub(sess.lastSeen) > s.idle
		sess.mu.Unlock()
		if dead {
			delete(s.byID, id)
			if devs := s.byOwner[sess.tgID]; devs != nil {
				if devs[sess.device] == id {
					delete(devs, sess.device)
				}
				if len(devs) == 0 {
					delete(s.byOwner, sess.tgID)
				}
			}
		}
	}
}

// SessionStats — для лога и диагностики: сколько сессий живо и сколько погашено.
func (m *Manager) SessionStats() (live, revoked int) {
	if m == nil || m.sessions == nil {
		return 0, 0
	}

	s := m.sessions
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sess := range s.byID {
		sess.mu.Lock()
		if sess.revoked {
			revoked++
		} else {
			live++
		}
		sess.mu.Unlock()
	}
	return live, revoked
}
