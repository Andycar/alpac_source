package proxylink

import (
	"strings"
	"testing"
)

func newSessionManager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(Options{
		CacheDir: t.TempDir(), EncryptAES: true,
		SharedSecret:   "0123456789abcdef0123456789abcdef",
		TTLHours:       36,
		SessionPlugins: []string{"iptv", "iptv-ru"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Ради этого всё и затевалось: одной ссылкой пользуются двое. Первый чужой
// запрос сойдёт за переезд, но возврат в брошенную сеть выдаёт, что обе живы, —
// и поток гаснет ЦЕЛИКОМ, включая того, кто ссылку раздал.
func TestSharedLinkRevokesWholeStream(t *testing.T) {
	m := newSessionManager(t)
	sid, _ := m.OpenSession("31.202.60.49", 100, "dev1", 0)
	if sid == "" {
		t.Fatal("сессия не заведена")
	}
	if r := m.CheckSession(sid, "31.202.60.49"); r != "" {
		t.Fatalf("свой запрос отвергнут: %s", r)
	}
	// Ссылку унесли в другую сеть — сама по себе это ещё может быть переезд.
	if r := m.CheckSession(sid, "195.211.215.140"); r != "" {
		t.Fatalf("первый запрос из новой сети должен пройти как переезд: %s", r)
	}
	// А вот возврат в прежнюю сеть означает, что смотрят одновременно.
	r := m.CheckSession(sid, "31.202.60.49")
	if !IsSessionRejection(r) {
		t.Fatalf("чередование сетей не поймано: %q", r)
	}
	// Поток погашен для ВСЕХ, включая исходную сеть — в этом и смысл.
	if r := m.CheckSession(sid, "195.211.215.140"); !IsSessionRejection(r) {
		t.Fatalf("после отзыва вторая сеть всё ещё играет: %q", r)
	}
	if r := m.CheckSession(sid, "31.202.60.49"); !strings.HasPrefix(r, ReasonSessionRevoked) {
		t.Fatalf("исходная сеть не погашена: %q", r)
	}
}

// Законный переезд Wi-Fi → LTE не должен гасить поток: старую сеть абонент
// бросает и обратно не возвращается.
func TestRoamingIsAllowed(t *testing.T) {
	m := newSessionManager(t)
	sid, _ := m.OpenSession("203.0.100.5", 100, "dev1", 0)
	for _, ip := range []string{"203.0.100.5", "203.0.100.7", "203.0.101.9", "203.0.101.9", "203.0.102.1", "203.0.102.1"} {
		if r := m.CheckSession(sid, ip); r != "" {
			t.Fatalf("переезд на %s отвергнут: %s", ip, r)
		}
	}
}

// Срезать ?sid= не должно помогать: для плагина с обязательной сессией голый
// токен — отказ.
func TestMissingSidRejected(t *testing.T) {
	m := newSessionManager(t)
	if r := m.CheckSession("", "203.0.100.5"); r != ReasonSessionMissing {
		t.Fatalf("пустой sid принят: %q", r)
	}
	if r := m.CheckSession("подделка", "203.0.100.5"); r != ReasonSessionUnknown {
		t.Fatalf("выдуманный sid принят: %q", r)
	}
}

// Механизм выключен по умолчанию: без session_plugins ссылки ведут себя
// по-прежнему, иначе включение сборки сломало бы все источники разом.
func TestSessionsOffByDefault(t *testing.T) {
	m := newTestManager(t, nil)
	if m.RequiresSession("iptv") {
		t.Fatal("сессия требуется без настройки")
	}
	if sid, _ := m.OpenSession("203.0.100.5", 1, "dev1", 0); sid != "" {
		t.Fatalf("сессия заведена при выключенном механизме: %q", sid)
	}
}

// Кино и прочие источники под гейт не попадают: там ссылку штатно открывает
// другое устройство, чем то, что её получило. Сессия при этом заводится одна
// на запрос независимо от плагина — разделяет их именно гейт.
func TestOnlyListedPluginsAreGated(t *testing.T) {
	m := newSessionManager(t)
	if m.RequiresSession("filmix") {
		t.Fatal("сессия требуется от неперечисленного плагина")
	}
	if !m.RequiresSession("iptv") || !m.RequiresSession("iptv-ru") {
		t.Fatal("перечисленные плагины остались без гейта")
	}
}

// Внутренние вызовы (health-check, превью) идут без клиентского адреса — они
// не должны считаться сменой сети, иначе гасили бы живые потоки.
func TestInternalCallsDoNotRevoke(t *testing.T) {
	m := newSessionManager(t)
	sid, _ := m.OpenSession("203.0.100.5", 1, "dev1", 0)
	if r := m.CheckSession(sid, ""); r != "" {
		t.Fatalf("внутренний вызов отвергнут: %s", r)
	}
	if r := m.CheckSession(sid, "203.0.100.5"); r != "" {
		t.Fatalf("после внутреннего вызова свой запрос отвергнут: %s", r)
	}
}

func TestSessionStatsCountsRevoked(t *testing.T) {
	m := newSessionManager(t)
	a, _ := m.OpenSession("203.0.100.5", 1, "devA", 0)
	b, _ := m.OpenSession("203.0.103.9", 2, "devB", 0)
	m.CheckSession(b, "203.0.104.8")
	m.CheckSession(b, "203.0.103.9") // чередование → отзыв
	live, revoked := m.SessionStats()
	if live != 1 || revoked != 1 {
		t.Fatalf("статистика неверна: живых %d, погашенных %d (a=%s b=%s)", live, revoked, a, b)
	}
}

// Двойной стек: манифест по IPv4, сегменты по IPv6 — штатное поведение клиента,
// а не дележ ссылки. Сравнение префиксов «в лоб» видело бы здесь вечное
// чередование и гасило бы поток честному зрителю.
func TestDualStackDoesNotRevoke(t *testing.T) {
	m := newSessionManager(t)
	sid, _ := m.OpenSession("31.202.60.49", 7, "dev1", 0)
	for i := 0; i < 6; i++ {
		if r := m.CheckSession(sid, "31.202.60.49"); r != "" {
			t.Fatalf("IPv4-запрос отвергнут: %s", r)
		}
		if r := m.CheckSession(sid, "2a02:2168:8a4f:1200::5"); r != "" {
			t.Fatalf("IPv6-запрос отвергнут: %s", r)
		}
	}
	if live, revoked := m.SessionStats(); live != 1 || revoked != 0 {
		t.Fatalf("двойной стек погасил поток: живых %d, погашенных %d", live, revoked)
	}
}

// А вот чередование ВНУТРИ семейства ловится по-прежнему — в том числе по IPv6.
func TestSharingCaughtWithinIPv6(t *testing.T) {
	m := newSessionManager(t)
	sid, _ := m.OpenSession("2a02:2168:8a4f:1200::5", 7, "dev1", 0)
	if r := m.CheckSession(sid, "2a02:2168:8a4f:1200::5"); r != "" {
		t.Fatalf("свой IPv6 отвергнут: %s", r)
	}
	if r := m.CheckSession(sid, "2001:db8:dead::1"); r != "" {
		t.Fatalf("переезд по IPv6 отвергнут: %s", r)
	}
	if r := m.CheckSession(sid, "2a02:2168:8a4f:1200::5"); !IsSessionRejection(r) {
		t.Fatalf("чередование по IPv6 не поймано: %q", r)
	}
}

// Лимит одновременных потоков = число устройств профиля. Третье устройство при
// лимите 2 получает отказ, а не тихо играет.
func TestConcurrentStreamLimit(t *testing.T) {
	m := newSessionManager(t)
	const tg, limit = int64(500), 2
	if _, r := m.OpenSession("203.0.105.5", tg, "tv", limit); r != "" {
		t.Fatalf("первое устройство отвергнуто: %s", r)
	}
	if _, r := m.OpenSession("203.0.106.5", tg, "phone", limit); r != "" {
		t.Fatalf("второе устройство отвергнуто: %s", r)
	}
	sid, r := m.OpenSession("203.0.107.5", tg, "laptop", limit)
	if r != ReasonTooManyStreams || sid != "" {
		t.Fatalf("третье устройство пропущено сверх лимита: sid=%q reason=%q", sid, r)
	}
	if n := m.ActiveStreams(tg); n != limit {
		t.Fatalf("активных потоков %d, ожидалось %d", n, limit)
	}
}

// Переключение канала — это то же устройство, а не новый поток. Иначе лимит
// съедался бы перещёлкиванием за минуту.
func TestChannelZappingDoesNotEatLimit(t *testing.T) {
	m := newSessionManager(t)
	const tg, limit = int64(501), 1
	prev := ""
	for i := 0; i < 20; i++ {
		sid, r := m.OpenSession("203.0.105.5", tg, "tv", limit)
		if r != "" || sid == "" {
			t.Fatalf("переключение #%d отвергнуто: %q", i, r)
		}
		if sid == prev {
			t.Fatalf("sid не обновился на переключении")
		}
		prev = sid
	}
	if n := m.ActiveStreams(tg); n != 1 {
		t.Fatalf("одно устройство дало %d активных потоков", n)
	}
	// Старые sid должны умереть вместе с заменой: иначе украденная ссылка
	// пережила бы переключение канала.
	if r := m.CheckSession(prev, "203.0.105.5"); r != "" {
		t.Fatalf("свежий sid не работает: %s", r)
	}
}

// Разные профили не мешают друг другу.
func TestLimitIsPerProfile(t *testing.T) {
	m := newSessionManager(t)
	if _, r := m.OpenSession("203.0.105.5", 600, "tv", 1); r != "" {
		t.Fatalf("профиль A отвергнут: %s", r)
	}
	if _, r := m.OpenSession("203.0.105.6", 601, "tv", 1); r != "" {
		t.Fatalf("профиль B отвергнут из-за чужого лимита: %s", r)
	}
}

// Нулевой лимит = без ограничения: механизм не должен внезапно резать тех,
// у кого лимит не настроен.
func TestZeroLimitMeansUnlimited(t *testing.T) {
	m := newSessionManager(t)
	for i := 0; i < 12; i++ {
		if _, r := m.OpenSession("203.0.105.5", 700, string(rune('a'+i)), 0); r != "" {
			t.Fatalf("устройство #%d отвергнуто при нулевом лимите: %s", i, r)
		}
	}
}
