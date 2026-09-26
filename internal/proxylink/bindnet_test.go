package proxylink

import (
	"encoding/json"
	"testing"
	"time"
)

// Кража плейлиста повторилась: файл унесли в TiviMate. TTL 36 ч только сокращает
// окно — внутри него украденная ссылка играет откуда угодно. Сетевая привязка
// закрывает именно это: ссылка мертва вне сети, которой выдана.

func newTestManager(t *testing.T, bind []string) *Manager {
	t.Helper()
	m, err := New(Options{
		CacheDir: t.TempDir(), EncryptAES: true,
		SharedSecret:       "0123456789abcdef0123456789abcdef",
		TTLHours:           36,
		BindNetworkPlugins: bind,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNetPrefix(t *testing.T) {
	cases := map[string]string{
		"46.148.255.19":          "46.148.255.0/24",
		"46.148.255.200":         "46.148.255.0/24",
		"46.148.254.19":          "46.148.254.0/24", // соседняя сеть — уже другая
		"2a00:1450:4010:c07::8b": "2a00:1450:4010::/48",
		"127.0.0.1:5050":         "127.0.0.0/24", // адрес с портом
		"не адрес":               "",
		"":                       "",
	}
	for in, want := range cases {
		if got := NetPrefix(in); got != want {
			t.Fatalf("NetPrefix(%q) = %q, ждали %q", in, got, want)
		}
	}
}

// Главное свойство: свой играет, чужой — нет.
func TestTokenBoundToSubscriberNetwork(t *testing.T) {
	m := newTestManager(t, []string{"iptv", "iptv-ru"})
	const owner = "46.148.255.19"
	tok := m.EncryptURIWithHeaders("http://cdn.example/live.m3u8", owner, "iptv",
		map[string]string{"User-Agent": "x"})
	if tok == "" {
		t.Fatal("токен не выдан")
	}

	if m.Decrypt(tok, owner) == nil {
		t.Fatal("владелец не смог открыть свою же ссылку")
	}
	// Тот же дом, другой адрес внутри /24 — обязан работать: у абонента адрес
	// меняется сам, и точная привязка рвала бы поток своим же зрителям.
	if m.Decrypt(tok, "46.148.255.200") == nil {
		t.Fatal("сосед по /24 (тот же абонент после переподключения) отвергнут")
	}
	// Чужая сеть — тот самый TiviMate у другого человека.
	if m.Decrypt(tok, "95.27.193.33") != nil {
		t.Fatal("украденная ссылка играет из чужой сети")
	}
	// Соседняя /24 — тоже чужая.
	if m.Decrypt(tok, "46.148.254.19") != nil {
		t.Fatal("ссылка играет из соседней сети")
	}
	// Внутренние вызовы идут без адреса (health-check, превью) — их не рвём.
	if m.Decrypt(tok, "") == nil {
		t.Fatal("внутренний вызов без адреса отвергнут")
	}
}

// Привязка только у перечисленных плагинов: кино открывают с другого устройства
// и из другой сети, чем то, что получило ссылку.
func TestOnlyListedPluginsAreBound(t *testing.T) {
	m := newTestManager(t, []string{"iptv"})
	tok := m.EncryptURIWithHeaders("http://cdn.example/movie.mp4", "46.148.255.19", "filmix", nil)
	if m.Decrypt(tok, "95.27.193.33") == nil {
		t.Fatal("непривязанный плагин зря ограничен сетью")
	}
}

// Пустой список = прежнее поведение. Нужен для отката одной строкой конфига.
func TestBindingOffByDefault(t *testing.T) {
	m := newTestManager(t, nil)
	tok := m.EncryptURIWithHeaders("http://cdn.example/live.m3u8", "46.148.255.19", "iptv", nil)
	if m.Decrypt(tok, "95.27.193.33") == nil {
		t.Fatal("без списка плагинов привязка не должна включаться")
	}
}

// Диагностика обязана называть причину, а не молча возвращать «ok»: на этом уже
// обжигались — проверка срока жила внутри ветки IP и Debug врал.
func TestDebugNamesForeignNetwork(t *testing.T) {
	m := newTestManager(t, []string{"iptv"})
	tok := m.EncryptURIWithHeaders("http://cdn.example/live.m3u8", "46.148.255.19", "iptv", nil)
	reason := m.DebugDecrypt(tok, "95.27.193.33")
	if !IsForeignNetworkRejection(reason) {
		t.Fatalf("Debug не назвал чужую сеть: %q", reason)
	}
}

// Двойной стек: манифест пришёл по IPv4, сегмент по IPv6 (или наоборот).
// Сравнивать такие префиксы нечем, и отказ порвал бы поток своему зрителю.
func TestDualStackNotRejected(t *testing.T) {
	m := newTestManager(t, []string{"iptv"})
	tok := m.EncryptURIWithHeaders("http://cdn.example/live.m3u8", "46.148.255.19", "iptv", nil)
	if m.Decrypt(tok, "2a00:1450:4010:c07::8b") == nil {
		t.Fatal("клиент с двойным стеком отвергнут")
	}
	// Но внутри IPv6 привязка обязана работать так же.
	tok6 := m.EncryptURIWithHeaders("http://cdn.example/live.m3u8", "2a00:1450:4010:c07::8b", "iptv", nil)
	if m.Decrypt(tok6, "2a00:1450:4010:c07::99") == nil {
		t.Fatal("свой /48 отвергнут")
	}
	if m.Decrypt(tok6, "2a00:1450:9999:c07::8b") != nil {
		t.Fatal("чужой /48 принят")
	}
}

// Каналы БЕЗ кастомных заголовков идут другим конструктором (EncryptURI).
// Пропустить его — значит оставитьполовину дыры: часть ссылок осталась бы непривязанной.
func TestPlainConstructorAlsoBinds(t *testing.T) {
	m := newTestManager(t, []string{"iptv"})
	tok := m.EncryptURI("http://cdn.example/live.m3u8", "46.148.255.19", "iptv", false, false, false)
	if tok == "" {
		t.Fatal("токен не выдан")
	}
	if m.Decrypt(tok, "46.148.255.77") == nil {
		t.Fatal("свой /24 отвергнут")
	}
	if m.Decrypt(tok, "95.27.193.33") != nil {
		t.Fatal("EncryptURI выдал непривязанный токен — ссылка играет из чужой сети")
	}
}

// Истёкший токен обязан опознаваться как ПОСТОЯННЫЙ отказ: прокси отвечает на
// него 410, и наш клиент перезапрашивает /api/iptv/play. Пока причина падала в
// общий 404, плеер уходил в бесконечный ретрай — с коротким TTL это стало бы
// массовым, поэтому проверяем оба формата причины, md5 и AES.
func TestExpiredAESIsPermanentRejection(t *testing.T) {
	m := newTestManager(t, nil)

	raw, err := json.Marshal(aesPayload{
		P: "iptv",
		U: "https://example.com/stream.m3u8",
		E: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	token := m.encryptAESPayload(string(raw))

	if got := m.Decrypt(token, "203.0.108.1"); got != nil {
		t.Fatalf("истёкший токен расшифровался: %+v", got)
	}
	reason := m.DebugDecrypt(token, "203.0.108.1")
	if !IsExpiredRejection(reason) {
		t.Fatalf("причина %q не опознана как истечение срока", reason)
	}
	if IsEternalRejection(reason) || IsForeignNetworkRejection(reason) {
		t.Fatalf("причина %q спутана с другим видом отказа", reason)
	}
}

func TestExpiredMD5IsPermanentRejection(t *testing.T) {
	m := newTestManager(t, nil)
	const key = "0123456789abcdef0123456789abcdef" // 32 символа — путь md5, не AES
	m.mu.Lock()
	m.links[key] = Model{URI: "https://example.com/s.m3u8", Plugin: "iptv", Expires: time.Now().Add(-time.Minute)}
	m.mu.Unlock()

	// Порядок важен: Decrypt удаляет истёкшую запись из карты, после чего
	// причина читается уже как «не найдено». На проде IPTV-ссылки идут по
	// AES-пути (encrypt_aes=true), поэтому спрашиваем причину до удаления.
	if reason := m.DebugDecrypt(key, "203.0.108.1"); !IsExpiredRejection(reason) {
		t.Fatalf("причина %q не опознана как истечение срока", reason)
	}
	if got := m.Decrypt(key, "203.0.108.1"); got != nil {
		t.Fatalf("истёкшая md5-ссылка расшифровалась: %+v", got)
	}
}

// Живой токен под тот же признак попадать не должен, иначе 410 полетит в
// работающие ссылки.
func TestFreshTokenIsNotExpired(t *testing.T) {
	m := newTestManager(t, nil)
	token := m.EncryptURI("https://example.com/s.m3u8", "203.0.108.1", "iptv", false, false, false)
	if reason := m.DebugDecrypt(token, "203.0.108.1"); IsExpiredRejection(reason) {
		t.Fatalf("живой токен опознан как истёкший: %q", reason)
	}
}

// "*" защищает ВСЕ источники, включая те, что появятся завтра: перечислять
// плагины поимённо значит оставлять каждый новый незащищённым по умолчанию.
func TestWildcardBindsEveryPlugin(t *testing.T) {
	m := newTestManager(t, []string{"*"})
	for _, plugin := range []string{"iptv", "filmix", "kodik", "совсем-новый-источник"} {
		link := m.EncryptURI("https://cdn.example/s.m3u8", "203.0.100.5", plugin, false, false, false)
		if got := m.Decrypt(link, "203.0.100.5"); got == nil {
			t.Fatalf("%s: своя сеть отвергнута", plugin)
		}
		if got := m.Decrypt(link, "203.0.103.9"); got != nil {
			t.Fatalf("%s: чужая сеть принята под '*'", plugin)
		}
	}
}

// Картинки под "*" привязывать нельзя: постеры кэшируются клиентами и
// раздаются через CDN — замер на проде показал одну картинку с 7–9 адресов.
// Привязка сломала бы кэш, не защитив ничего: картинка не товар.
func TestWildcardSkipsImages(t *testing.T) {
	m := newTestManager(t, []string{"*"})
	for _, plugin := range []string{"posterapi", "proxyimg"} {
		link := m.EncryptURI("https://cdn.example/poster.jpg", "203.0.100.5", plugin, false, false, true)
		if got := m.Decrypt(link, "203.0.103.9"); got == nil {
			t.Fatalf("%s: картинка привязана к сети", plugin)
		}
	}
}

// То же для обязательного срока: "*" покрывает всех, картинки — нет.
func TestWildcardRequireTTL(t *testing.T) {
	m, err := New(Options{
		CacheDir: t.TempDir(), EncryptAES: true,
		SharedSecret:      "0123456789abcdef0123456789abcdef",
		RequireTTLPlugins: []string{"*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !m.rejectsEternal("какой-угодно-источник") {
		t.Fatal("'*' не покрыл источник")
	}
	if m.rejectsEternal("posterapi") {
		t.Fatal("картинки попали под обязательный срок")
	}
}

// Постеры чеканятся payload'ом БЕЗ поля P и при расшифровке приходят с пустым
// плагином. Без оговорки на пустое имя "*" погасил бы все картинки разом.
func TestWildcardKeepsPosterTokensAlive(t *testing.T) {
	m, err := New(Options{
		CacheDir: t.TempDir(), EncryptAES: true,
		SharedSecret:      "0123456789abcdef0123456789abcdef",
		RequireTTLPlugins: []string{"*"},
		TTLHours:          36,
	})
	if err != nil {
		t.Fatal(err)
	}
	poster := m.EncryptURI("https://img.example/poster.jpg", "203.0.100.5", "posterapi", false, false, true)
	if got := m.Decrypt(poster, "203.0.103.9"); got == nil {
		t.Fatalf("постер отвергнут под '*': %s", m.DebugDecrypt(poster, "203.0.103.9"))
	}
}

// Своя инфраструктура не должна считаться сетью абонента. Сервер ходит в
// собственный /proxy за вложенным плейлистом и пробрасывает User-Agent клиента —
// снаружи это неотличимо от зрителя, и привязка рубила такие запросы (замерено
// на проде: 50 отказов подряд с адреса самого сервера).
func TestTrustedNetworkNotBound(t *testing.T) {
	m, err := New(Options{
		CacheDir: t.TempDir(), EncryptAES: true,
		SharedSecret:       "0123456789abcdef0123456789abcdef",
		TTLHours:           36,
		BindNetworkPlugins: []string{"*"},
		TrustedNetworks:    []string{"198.51.100.7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Токен, выданный своей инфраструктуре, не привязывается вовсе.
	fromUs := m.EncryptURI("https://cdn.example/s.m3u8", "198.51.100.7", "iptv", false, false, false)
	if got := m.Decrypt(fromUs, "203.0.113.9"); got == nil {
		t.Fatal("ссылка, выданная своей инфраструктуре, привязана к сети")
	}
	// Петля и приватные диапазоны — тоже своя инфраструктура, без настройки.
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.5"} {
		link := m.EncryptURI("https://cdn.example/s.m3u8", ip, "iptv", false, false, false)
		if got := m.Decrypt(link, "203.0.113.9"); got == nil {
			t.Fatalf("%s посчитан сетью абонента", ip)
		}
	}
	// А настоящий абонент по-прежнему привязан.
	real := m.EncryptURI("https://cdn.example/s.m3u8", "203.0.113.5", "iptv", false, false, false)
	if got := m.Decrypt(real, "203.0.113.5"); got == nil {
		t.Fatal("своя сеть абонента отвергнута")
	}
	if got := m.Decrypt(real, "198.51.100.200"); got != nil {
		t.Fatal("чужая сеть принята у абонента")
	}
}

// Тот же запрос со своей инфраструктуры не должен гасить потоковую сессию как
// «чередование сетей».
func TestTrustedNetworkDoesNotRevokeSession(t *testing.T) {
	m, err := New(Options{
		CacheDir: t.TempDir(), EncryptAES: true,
		SharedSecret:    "0123456789abcdef0123456789abcdef",
		SessionPlugins:  []string{"iptv"},
		TrustedNetworks: []string{"198.51.100.7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := m.OpenSession("203.0.113.5", 1, "tv", 0)
	for i := 0; i < 5; i++ {
		if r := m.CheckSession(sid, "203.0.113.5"); r != "" {
			t.Fatalf("абонент отвергнут: %s", r)
		}
		if r := m.CheckSession(sid, "198.51.100.7"); r != "" {
			t.Fatalf("внутренний вызов отвергнут: %s", r)
		}
	}
	if _, revoked := m.SessionStats(); revoked != 0 {
		t.Fatal("внутренние вызовы погасили поток")
	}
}

// Полный случай с прода: токен выдан НА СЕТЬ АБОНЕНТА, а предъявлен со своего
// сервера (он тянет вложенный плейлист за клиента). Освобождать надо не только
// чеканку, но и проверку — иначе отказ остаётся.
func TestTrustedNetworkMayPresentSubscriberToken(t *testing.T) {
	m, err := New(Options{
		CacheDir: t.TempDir(), EncryptAES: true,
		SharedSecret:       "0123456789abcdef0123456789abcdef",
		TTLHours:           36,
		BindNetworkPlugins: []string{"*"},
		TrustedNetworks:    []string{"198.51.100.7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	link := m.EncryptURI("https://cdn.example/s.m3u8", "203.0.113.5", "iptv", false, false, false)
	if got := m.Decrypt(link, "198.51.100.7"); got == nil {
		t.Fatalf("свой сервер не смог предъявить абонентский токен: %s",
			m.DebugDecrypt(link, "198.51.100.7"))
	}
	// Посторонняя сеть — по-прежнему отказ.
	if got := m.Decrypt(link, "192.0.2.9"); got != nil {
		t.Fatal("чужая сеть принята")
	}
}
