package proxylink

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Options struct {
	CacheDir   string
	VerifyIP   bool
	EncryptAES bool
	// SharedSecret, when non-empty, deterministically derives the AES key
	// (SHA256(secret) → 32 bytes for AES-256). All lampac-go instances using
	// the same secret can decrypt each other's /proxy/ URLs. Used in
	// cluster mode where a forwarded request may land on any node.
	SharedSecret string
	// TTLHours ограничивает жизнь токена, выданного БЕЗ привязки к IP.
	// Такие токены (IPTV, lite-источники) раньше были вечными: утёкшая ссылка
	// играла бесконечно с любого адреса. 0 = прежнее поведение (без срока).
	TTLHours int
	// RequireTTLPlugins — плагины, у которых токен БЕЗ срока считается
	// недействительным. TTLHours защищает только новые ссылки; уже утёкшие
	// выданы без поля E, а пустое E = вечное. Перечисленные здесь плагины
	// перестают принимать такие токены — это добивает украденный экспорт, не
	// трогая ни кластерный shared_secret, ни ссылки остальных источников.
	// Легальный клиент просто перезапросит /play и получит свежий токен.
	RequireTTLPlugins []string
	// BindNetworkPlugins — плагины, чьи токены привязываются к СЕТИ подписчика
	// (/24, /48). Ровно те, чьи ссылки перепродают: IPTV. Кино сюда не годится —
	// там ссылку часто открывает другое устройство/сеть, чем то, что её получило.
	BindNetworkPlugins []string
	// SessionPlugins — плагины, чьи ссылки обязаны нести живую потоковую
	// сессию (см. session.go). Запрос без sid у такого плагина отвергается,
	// иначе вор просто срезал бы параметр.
	SessionPlugins []string
	// SessionFlipSec — окно, в котором возврат в брошенную сеть считается
	// чередованием (т.е. ссылку делят), а не переездом. 0 = 90 с.
	SessionFlipSec int
	// SessionIdleMin — через сколько молчания сессия убирается. 0 = 12 ч.
	SessionIdleMin int
	// TrustedNetworks — СВОЯ инфраструктура: адреса, которые нельзя считать
	// «сетью абонента». Сервер ходит в собственный /proxy (вложенные плейлисты,
	// транскодер, превью) и пробрасывает при этом User-Agent клиента — снаружи
	// это неотличимо от зрителя, и привязка к сети рубила такие запросы.
	// Формат: CIDR или голый адрес. Петля и приватные диапазоны включены всегда.
	TrustedNetworks []string
}

type Model struct {
	ReqIP    string
	URI      string
	Plugin   string
	VerifyIP bool
	Expires  time.Time
	Headers  map[string]string
	Edge     string // клиентский адрес ноды, добывшей ссылку (см. aesPayload.D)
	CBCSKey  []byte // CBCS decryption key (16 bytes AES-128)
	CBCSIV   []byte // CBCS constant IV (16 bytes)
}

// keyMaterial holds the AES key + IV used for /proxy/<enc> tokens. Pointed
// to atomically by Manager.keys so it can be swapped at runtime (cluster
// shared_secret rotation, hot-reload after first secret generation).
type keyMaterial struct {
	key []byte
	iv  []byte
}

type Manager struct {
	mu         sync.RWMutex
	links      map[string]Model
	verifyIP   bool
	encryptAES bool
	// ttlNanos — срок жизни токенов без IP-привязки (наносекунды); 0 = бессрочно.
	// Атомик: Reload меняет его на горячую, а шифрование читает без блокировки.
	ttlNanos atomic.Int64
	// bindNetPlugins — множество плагинов из BindNetworkPlugins (атомик, hot-reload).
	bindNetPlugins atomic.Pointer[map[string]struct{}]
	// requireTTL — множество плагинов из RequireTTLPlugins (атомик ради hot-reload).
	requireTTL atomic.Pointer[map[string]struct{}]
	// trustedNets — своя инфраструктура (см. Options.TrustedNetworks).
	trustedNets atomic.Pointer[[]*net.IPNet]
	keys        atomic.Pointer[keyMaterial]
	// sessions — потоковые сессии (см. session.go). Живут вне AES-payload:
	// sid едет в query, а не в токене, поэтому добавление сессии не меняет
	// ни формат ссылок, ни сигнатуры Encrypt*.
	sessions *sessionStore

	edgeMu sync.RWMutex
	edge   string // см. SetEdge

}

// setRequireTTL replaces the "token must carry an expiry" plugin set.
func (m *Manager) setRequireTTL(plugins []string) {
	set := make(map[string]struct{}, len(plugins))
	for _, p := range plugins {
		if p = strings.TrimSpace(p); p != "" {
			set[p] = struct{}{}
		}
	}
	m.requireTTL.Store(&set)
}

// ReasonEternalRejected prefixes the DebugDecrypt reason for a token that was
// minted without an expiry for a plugin that now requires one. It is a stable
// marker on purpose: the proxy turns this specific failure into 410 Gone
// instead of 404, because it can never succeed on retry — the link is one of
// the pre-TTL IPTV links, and a player that reads 404 as "try again" loops
// forever (measured 2026-09-01: one client retried a single dead token 622
// times in an hour, showing the user an endless spinner).
const ReasonEternalRejected = "eternal token rejected"

// IsEternalRejection reports whether a DebugDecrypt reason is the permanent
// eternal-token rejection.
// IsForeignNetworkRejection reports whether the token was minted for another
// subscriber network. Как и вечный токен, это отказ НАВСЕГДА для этого клиента:
// повтор из той же чужой сети не сработает никогда, поэтому прокси отвечает
// 410, а не 404 — иначе плеер уходит в бесконечный ретрай.
func IsForeignNetworkRejection(reason string) bool {
	return strings.HasPrefix(reason, ReasonForeignNetwork)
}

func IsEternalRejection(reason string) bool {
	return strings.HasPrefix(strings.TrimSpace(reason), ReasonEternalRejected)
}

// ReasonExpired маркирует истёкший по сроку токен. Это тоже отказ НАВСЕГДА:
// повтор той же ссылки не оживит её никогда. Раньше срок падал в общий 404,
// и плеер уходил в тот же бесконечный ретрай, ради которого вводили 410 для
// вечных токенов, — а с коротким TTL это стало бы массовым.
const ReasonExpired = "expired at "

// IsExpiredRejection reports whether a DebugDecrypt reason is the permanent
// expiry rejection (в том числе для md5-ссылок, где причина идёт с префиксом).
func IsExpiredRejection(reason string) bool {
	return strings.Contains(reason, ReasonExpired)
}

// setTrustedNets разбирает список своей инфраструктуры. Петля и приватные
// диапазоны добавляются всегда: запрос оттуда физически не может быть
// абонентским, а «сетью абонента» его посчитать — значит рубить свои же
// внутренние вызовы.
func (m *Manager) setTrustedNets(cidrs []string) {
	nets := make([]*net.IPNet, 0, len(cidrs)+8)
	for _, c := range append([]string{
		"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
	}, cidrs...) {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			if ip := net.ParseIP(c); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				c += "/" + strconv.Itoa(bits)
			}
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	m.trustedNets.Store(&nets)
}

// subscriberNet — сеть АБОНЕНТА или "" для своей инфраструктуры. Единственная
// точка, где решается «чья это сеть»: разъехавшиеся проверки в Decrypt и
// DebugDecrypt дали бы отказ без объяснимой причины в логе.
func (m *Manager) subscriberNet(ip string) string {
	if m.IsTrustedNetwork(ip) {
		return ""
	}
	return NetPrefix(ip)
}

// IsTrustedNetwork reports whether the address belongs to our own infrastructure
// rather than to a subscriber. Такие запросы не привязываются к сети и не
// считаются сменой сети в потоковой сессии.
func (m *Manager) IsTrustedNetwork(ip string) bool {
	if m == nil {
		return false
	}
	nets := m.trustedNets.Load()
	if nets == nil {
		return false
	}
	if h, _, err := net.SplitHostPort(ip); err == nil {
		ip = h
	}
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil {
		return false
	}
	for _, n := range *nets {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

// setBindNet replaces the "bind token to subscriber network" plugin set.
func (m *Manager) setBindNet(plugins []string) {
	set := make(map[string]struct{}, len(plugins))
	for _, p := range plugins {
		if p = strings.TrimSpace(p); p != "" {
			set[p] = struct{}{}
		}
	}
	m.bindNetPlugins.Store(&set)
}

// bindNet возвращает сеть, которую надо вшить в токен этого плагина, или "" —
// если плагин не привязывается или адрес неразбираем.
// bindNetAll — маркер «привязывать ВСЕ плагины». Перечислять источники поимённо
// не годится: их десятки, и каждый новый оказывался бы незащищённым по
// умолчанию — а защита по умолчанию должна быть включена, а не выключена.
const bindNetAll = "*"

// imageLikePlugins — плагины, чьи ссылки НЕЛЬЗЯ привязывать к сети даже под "*".
// Постеры и логотипы кэшируются клиентами и раздаются через CDN: замер на проде
// показал одну картинку с 7–9 разных адресов. Привязка сломала бы кэш, ничего
// не защитив — картинка не товар.
var imageLikePlugins = map[string]struct{}{
	"posterapi": {},
	"proxyimg":  {},
}

func (m *Manager) bindNet(plugin, reqip string) string {
	set := m.bindNetPlugins.Load()
	if set == nil {
		return ""
	}
	if _, isImage := imageLikePlugins[plugin]; isImage {
		return ""
	}
	if _, all := (*set)[bindNetAll]; !all {
		if _, ok := (*set)[plugin]; !ok {
			return ""
		}
	}
	if m.IsTrustedNetwork(reqip) {
		return ""
	}
	return NetPrefix(reqip)
}

// ReasonForeignNetwork — токен предъявлен из ЧУЖОЙ сети. Отдельный маркер, как
// и ReasonEternalRejected: повтор запроса из той же сети не поможет никогда,
// поэтому прокси отвечает 410, а не 404 (иначе плеер уходит в вечный ретрай).
const ReasonForeignNetwork = "foreign network"

// rejectsEternal reports whether `plugin` refuses tokens minted without an
// expiry — i.e. the pre-TTL links that leaked.
func (m *Manager) rejectsEternal(plugin string) bool {
	set := m.requireTTL.Load()
	if set == nil {
		return false
	}
	// Пустое имя плагина под "*" отвергать НЕЛЬЗЯ: постеры чеканятся payload'ом
	// без поля P (см. ветку posterapi в EncryptURI), и при расшифровке приходят
	// с пустым плагином. Без этой оговорки "*" погасил бы все картинки в
	// интерфейсе разом.
	if plugin == "" {
		return false
	}
	if _, isImage := imageLikePlugins[plugin]; isImage {
		return false
	}
	if _, all := (*set)[bindNetAll]; all {
		return true
	}
	_, ok := (*set)[plugin]
	return ok
}

// expiry returns the deadline stamped into tokens that are NOT IP-bound, or the
// zero time when no TTL is configured (legacy: token lives forever).
func (m *Manager) expiry() time.Time {
	ttl := time.Duration(m.ttlNanos.Load())
	if ttl <= 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}

// currentKey returns a snapshot of the active AES key + IV. Cheap (atomic load).
func (m *Manager) currentKey() ([]byte, []byte) {
	km := m.keys.Load()
	if km == nil {
		return nil, nil
	}
	return km.key, km.iv
}

type aesPayload struct {
	P string            `json:"p,omitempty"`
	U string            `json:"u,omitempty"`
	I string            `json:"i,omitempty"`
	V bool              `json:"v,omitempty"`
	E time.Time         `json:"e"`
	H map[string]string `json:"h,omitempty"`
	K []byte            `json:"K,omitempty"` // CBCS decryption key
	W []byte            `json:"W,omitempty"` // CBCS constant IV
	// N — СЕТЬ подписчика (/24 у IPv4, /48 у IPv6), которой выдан токен.
	// Непустое поле = ссылка играет только из этой сети. Это ответ на перепродажу:
	// украденный плейлист уносят в чужой дом и чужой город, а там он мёртв.
	// Привязка именно к сети, а не к точному адресу: у абонента IP внутри /24
	// меняется сам по себе (переподключение, CGNAT, вторая точка доступа), и
	// точная привязка рвала бы поток своим же зрителям.
	N string `json:"n,omitempty"`
	// D — клиентский адрес НОДЫ, которая добыла ссылку (например
	// "https://edge-a.example.org"). Часть CDN подписывает поток на адрес, с которого
	// пришли за манифестом (obrut, sevstar, videodb): такую ссылку сможет отдать
	// только тот сервер, что её добыл. Primary читает поле и отправляет
	// зрителя именно туда. Пусто = добыл сам primary.
	D string `json:"d,omitempty"`
}

// sameFamily reports whether two prefixes are both IPv4 or both IPv6.
// У клиента с двойным стеком манифест может прийти по IPv4, а сегмент — по
// IPv6 (или наоборот): сравнивать такие префиксы бессмысленно, и отказ порвал
// бы поток своему же зрителю. Сравниваем только внутри одного семейства —
// перепродажу это не пропускает: вор сидит в другой сети того же семейства.
func sameFamily(a, b string) bool {
	return strings.HasSuffix(a, "/24") == strings.HasSuffix(b, "/24")
}

// NetPrefix сводит адрес к сети: /24 для IPv4, /48 для IPv6. Пустая строка —
// адрес неразбираем, привязку в этом случае не ставим и не проверяем.
// NetPrefix сводит адрес к сети абонента: /24 у IPv4, /48 у IPv6. Экспортирован
// потому, что тем же определением сети пользуются и гейт сессии, и ключ
// устройства в iptvhttp — две расходящиеся копии тихо развалили бы обоих.
func NetPrefix(ip string) string {
	ip = strings.TrimSpace(ip)
	if h, _, err := net.SplitHostPort(ip); err == nil {
		ip = h
	}
	addr := net.ParseIP(ip)
	if addr == nil {
		return ""
	}
	if v4 := addr.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return addr.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

// SetEdge задаёт, каким адресом подписывать выпускаемые токены: нода пишет
// сюда свой клиентский URL, primary оставляет пустым.
func (m *Manager) SetEdge(host string) {
	m.edgeMu.Lock()
	m.edge = strings.TrimRight(strings.TrimSpace(host), "/")
	m.edgeMu.Unlock()
}

func (m *Manager) mintedBy() string {
	m.edgeMu.RLock()
	defer m.edgeMu.RUnlock()
	return m.edge
}

func New(opts Options) (*Manager, error) {
	km, err := buildKeyMaterial(opts)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		links:      make(map[string]Model, 1024),
		verifyIP:   opts.VerifyIP,
		encryptAES: opts.EncryptAES,
		sessions:   newSessionStore(),
	}
	m.ApplyPolicy(opts)
	m.keys.Store(km)
	return m, nil
}

// ApplyPolicy применяет политику ссылок БЕЗ смены ключей: срок жизни, обязательный TTL, привязку к
// сети, свои сети, потоковые сессии. Все поля атомарные — безопасно на живом сервере. Зовётся при
// каждом перечитывании конфига: Reload трогает ещё и ключи, поэтому идёт только при смене
// shared_secret, и до этого метода правка trusted_networks по SIGHUP молча не действовала до
// перезапуска (24.09.2026: новый бокс транскодинга получал 410 «foreign network» на IPTV-ссылки).
func (m *Manager) ApplyPolicy(opts Options) {
	m.ttlNanos.Store(int64(time.Duration(opts.TTLHours) * time.Hour))
	m.setRequireTTL(opts.RequireTTLPlugins)
	m.setBindNet(opts.BindNetworkPlugins)
	m.setTrustedNets(opts.TrustedNetworks)
	m.SetSessionPlugins(opts.SessionPlugins, opts.SessionFlipSec, opts.SessionIdleMin)
}

// Reload swaps the AES key/iv with values derived from `opts`. Safe to call
// concurrently — encryption/decryption operations atomically pick up the new
// material on their next call. Used to recover from the cluster bootstrap
// case where shared_secret was empty at startup and got set later.
//
// Caveat: tokens minted before Reload become undecryptable. Active streams
// will break and need to re-resolve. This is expected — call Reload only
// when the cluster setup actually changes (e.g. shared_secret rotation).
func (m *Manager) Reload(opts Options) error {
	km, err := buildKeyMaterial(opts)
	if err != nil {
		return err
	}
	m.ApplyPolicy(opts)
	m.keys.Store(km)
	return nil
}

// buildKeyMaterial produces a fresh keyMaterial from Options.
func buildKeyMaterial(opts Options) (*keyMaterial, error) {
	if strings.TrimSpace(opts.SharedSecret) != "" {
		// Cluster mode — derive deterministic 32-byte AES-256 key + IV from
		// the shared secret so every node ends up with the same encryption
		// material. The IV is a separate hash to avoid using the key bytes
		// for both purposes.
		k := sha256.Sum256([]byte("proxylink-key:" + opts.SharedSecret))
		i := sha256.Sum256([]byte("proxylink-iv:" + opts.SharedSecret))
		key := append([]byte(nil), k[:]...)
		iv := append([]byte(nil), i[:16]...)
		return &keyMaterial{key: key, iv: iv}, nil
	}
	cacheDir := resolveCacheDir(opts.CacheDir)
	key, iv, err := loadOrCreateAESKey(cacheDir)
	if err != nil {
		return nil, err
	}
	return &keyMaterial{key: key, iv: iv}, nil
}

func (m *Manager) EncryptURI(uri, reqip, plugin string, verifyip, forceMD5, isProxyImg bool) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}

	uriClear := uri
	if before, _, ok := strings.Cut(uri, "#"); ok {
		uriClear = strings.TrimSpace(before)
	}

	hash := ""
	md5Mode := false
	if plugin == "posterapi" {
		payload, _ := json.Marshal(aesPayload{U: uriClear})
		hash = m.encryptAESPayload(string(payload))
	} else if !forceMD5 && m.encryptAES && !strings.Contains(uriClear, " or ") {
		if verifyip && m.verifyIP {
			payload, _ := json.Marshal(aesPayload{
				P: plugin,
				U: uriClear,
				I: reqip,
				V: true,
				D: m.mintedBy(),
				E: time.Now().Add(36 * time.Hour),
			})
			hash = m.encryptAESPayload(string(payload))
		} else {
			payload, _ := json.Marshal(aesPayload{
				P: plugin, U: uriClear, E: m.expiry(), N: m.bindNet(plugin, reqip), D: m.mintedBy(),
			})
			hash = m.encryptAESPayload(string(payload))
		}
	} else {
		md5Mode = true
		ipPart := ""
		if verifyip && m.verifyIP {
			ipPart = reqip
		}
		hash = md5Hex(uriClear + ipPart)
	}

	if hash == "" {
		hash = uriClear
	}

	hash += detectExt(uri, isProxyImg)
	if md5Mode {
		expires := time.Now().Add(20 * time.Hour)
		m.mu.Lock()
		m.links[hash] = Model{
			ReqIP:    reqip,
			URI:      uriClear,
			Plugin:   plugin,
			VerifyIP: verifyip,
			Expires:  expires,
		}
		m.mu.Unlock()
	}

	return hash
}

// EncryptURIWithHeaders works like EncryptURI but embeds custom upstream
// headers into the AES payload. The proxy handler reads these and sets them
// on the upstream request (e.g. Origin, Referer for CDNs that require them).
func (m *Manager) EncryptURIWithHeaders(uri, reqip, plugin string, headers map[string]string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	uriClear := uri
	if before, _, ok := strings.Cut(uri, "#"); ok {
		uriClear = strings.TrimSpace(before)
	}

	payload, _ := json.Marshal(aesPayload{
		P: plugin,
		U: uriClear,
		H: headers,
		E: m.expiry(),
		N: m.bindNet(plugin, reqip),
		D: m.mintedBy(),
	})
	hash := m.encryptAESPayload(string(payload))
	if hash == "" {
		return ""
	}
	return hash + detectExt(uri, false)
}

// EncryptURIWithCBCS embeds custom headers AND CBCS decryption key/IV into
// the AES payload. Used for Kinescope fMP4 segments that need server-side
// CBCS decryption in the proxy handler.
func (m *Manager) EncryptURIWithCBCS(uri, reqip, plugin string, headers map[string]string, cbcsKey, cbcsIV []byte) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	uriClear := uri
	if before, _, ok := strings.Cut(uri, "#"); ok {
		uriClear = strings.TrimSpace(before)
	}

	payload, _ := json.Marshal(aesPayload{
		P: plugin,
		U: uriClear,
		H: headers,
		N: m.bindNet(plugin, reqip),
		D: m.mintedBy(),
		K: cbcsKey,
		W: cbcsIV,
		E: m.expiry(),
	})
	hash := m.encryptAESPayload(string(payload))
	if hash == "" {
		return ""
	}
	return hash + detectExt(uri, false)
}

func (m *Manager) Decrypt(hash, reqip string) *Model {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return nil
	}

	if q := strings.IndexAny(hash, "?#"); q >= 0 {
		hash = hash[:q]
	}

	if IsAes(hash) {
		key := hash
		if dot := strings.LastIndex(key, "."); dot > 0 {
			key = key[:dot]
		}
		dec := m.decryptAESPayload(key)
		if dec == "" {
			return nil
		}

		var payload aesPayload
		if err := json.Unmarshal([]byte(dec), &payload); err != nil {
			return nil
		}
		if payload.U == "" {
			return nil
		}

		if payload.V && reqip != "" && payload.I != reqip {
			return nil
		}
		// Сетевая привязка: ссылка играет только из той сети, которой выдана.
		// Проверяем лишь когда клиентский адрес известен — внутренние вызовы
		// (health-check, превью) идут без него и не должны отваливаться. Своя
		// инфраструктура тоже освобождена: сервер тянет собственный /proxy ЗА
		// клиента, и токен при этом выдан на сеть клиента, а предъявлен с нашего
		// адреса — снаружи неотличимо от кражи, изнутри это обычный вложенный
		// плейлист (замерено на проде: 50 отказов подряд с адреса сервера).
		if cur := m.subscriberNet(reqip); payload.N != "" && cur != "" &&
			sameFamily(cur, payload.N) && cur != payload.N {
			return nil
		}
		// Срок проверяем ДЛЯ ЛЮБОГО токена, а не только IP-привязанного.
		// Раньше эта проверка жила внутри `if payload.V`, поэтому у токенов без
		// привязки (IPTV, lite-источники) поле E не смотрели вовсе — украденная
		// ссылка играла вечно даже при проставленном сроке.
		if !payload.E.IsZero() && time.Now().After(payload.E) {
			return nil
		}
		// Бессрочный токен у плагина из RequireTTLPlugins — это ссылка,
		// выданная до введения TTL (в том числе украденная). Не принимаем.
		if payload.E.IsZero() && m.rejectsEternal(payload.P) {
			return nil
		}

		return &Model{
			ReqIP:    reqip,
			URI:      payload.U,
			Plugin:   payload.P,
			VerifyIP: payload.V,
			Expires:  payload.E,
			Headers:  payload.H,
			Edge:     payload.D,
			CBCSKey:  payload.K,
			CBCSIV:   payload.W,
		}
	}

	m.mu.RLock()
	val, ok := m.links[hash]
	m.mu.RUnlock()
	if !ok {
		return nil
	}

	if !val.Expires.IsZero() && time.Now().After(val.Expires) {
		m.mu.Lock()
		delete(m.links, hash)
		m.mu.Unlock()
		return nil
	}

	if val.VerifyIP && m.verifyIP && val.ReqIP != "" && reqip != "" && val.ReqIP != reqip {
		return nil
	}
	cp := val
	return &cp
}

// DebugDecrypt returns a human-readable reason why Decrypt failed, or "ok" on
// success. It mirrors Decrypt's verify-IP and expiry checks so the reason is
// accurate: previously it reported "ok (aes)" whenever the ciphertext merely
// decrypted to valid JSON, even when Decrypt actually rejected the link on an
// IP mismatch or expiry — which masked the real cause of /proxy 404s. Pass the
// same reqip Decrypt would see (the requester IP) so mismatches are explicit.
func (m *Manager) DebugDecrypt(hash, reqip string) string {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return "empty hash"
	}
	if q := strings.IndexAny(hash, "?#"); q >= 0 {
		hash = hash[:q]
	}
	if !IsAes(hash) {
		// MD5 path
		m.mu.RLock()
		val, ok := m.links[hash]
		m.mu.RUnlock()
		if !ok {
			return "md5: not found in map (len=" + fmt.Sprintf("%d", len(hash)) + ")"
		}
		if !val.Expires.IsZero() && time.Now().After(val.Expires) {
			return fmt.Sprintf("md5: "+ReasonExpired+"%s (now %s)", val.Expires.Format(time.RFC3339), time.Now().Format(time.RFC3339))
		}
		if val.VerifyIP && m.verifyIP && val.ReqIP != "" && reqip != "" && val.ReqIP != reqip {
			return fmt.Sprintf("md5: ip mismatch (minted for %s, request from %s)", val.ReqIP, reqip)
		}
		return "ok (md5)"
	}
	key := hash
	if dot := strings.LastIndex(key, "."); dot > 0 {
		key = key[:dot]
	}
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		raw2, err2 := base64.StdEncoding.DecodeString(key)
		if err2 != nil {
			return "base64 decode failed: " + err.Error()
		}
		raw = raw2
	}
	if len(raw) == 0 {
		return "base64 decoded to empty"
	}
	aesKey, aesIV := m.currentKey()
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "aes cipher init failed: " + err.Error()
	}
	if len(raw)%block.BlockSize() != 0 {
		return "block size mismatch: decoded=" + fmt.Sprintf("%d", len(raw)) + " blockSize=16"
	}
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, aesIV).CryptBlocks(out, raw)
	out, err = pkcs7Unpad(out, block.BlockSize())
	if err != nil {
		return "pkcs7 unpad failed: " + err.Error()
	}
	var payload aesPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		return "json unmarshal failed: " + err.Error()
	}
	if payload.U == "" {
		return "payload.U is empty"
	}
	if cur := m.subscriberNet(reqip); payload.N != "" && cur != "" &&
		sameFamily(cur, payload.N) && cur != payload.N {
		return fmt.Sprintf("%s (выдан для %s, предъявлен из %s)", ReasonForeignNetwork, payload.N, cur)
	}
	if payload.V && reqip != "" && payload.I != reqip {
		return fmt.Sprintf("ip mismatch (minted for %s, request from %s)", payload.I, reqip)
	}
	// Как и в Decrypt: срок смотрим у любого токена, не только IP-привязанного.
	if !payload.E.IsZero() && time.Now().After(payload.E) {
		return fmt.Sprintf(ReasonExpired+"%s (now %s)", payload.E.Format(time.RFC3339), time.Now().Format(time.RFC3339))
	}
	if payload.E.IsZero() && m.rejectsEternal(payload.P) {
		return fmt.Sprintf("%s (plugin %q requires an expiry; re-resolve the stream)", ReasonEternalRejected, payload.P)
	}
	return "ok (aes)"
}

func IsAes(hash string) bool {
	if hash == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(hash), "http") {
		return false
	}

	if i := strings.IndexAny(hash, "?&."); i >= 0 {
		hash = hash[:i]
	}
	return len(hash) != 32
}

func (m *Manager) encryptAESPayload(plain string) string {
	if strings.TrimSpace(plain) == "" {
		return plain
	}

	aesKey, aesIV := m.currentKey()
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return plain
	}

	p := pkcs7Pad([]byte(plain), block.BlockSize())
	out := make([]byte, len(p))
	cipher.NewCBCEncrypter(block, aesIV).CryptBlocks(out, p)
	// Use RawURLEncoding (no padding, URL-safe alphabet: - and _ instead of + and /)
	// to prevent base64 characters from being interpreted as URL path separators
	// or query parameter delimiters by browsers and HLS players.
	return base64.RawURLEncoding.EncodeToString(out)
}

func (m *Manager) decryptAESPayload(cipherText string) string {
	// Try RawURLEncoding first (new format: URL-safe, no padding).
	// Fall back to StdEncoding for backward compatibility with existing tokens.
	raw, err := base64.RawURLEncoding.DecodeString(cipherText)
	if err != nil || len(raw) == 0 {
		raw, err = base64.StdEncoding.DecodeString(cipherText)
		if err != nil || len(raw) == 0 {
			return ""
		}
	}

	aesKey, aesIV := m.currentKey()
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return ""
	}
	if len(raw)%block.BlockSize() != 0 {
		return ""
	}

	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, aesIV).CryptBlocks(out, raw)
	out, err = pkcs7Unpad(out, block.BlockSize())
	if err != nil {
		return ""
	}
	return string(out)
}

func resolveCacheDir(cacheDir string) string {
	cacheDir = strings.TrimSpace(cacheDir)
	candidates := []string{}
	if cacheDir != "" {
		candidates = append(candidates, cacheDir)
	}
	candidates = append(candidates, "cache", "/home/cache")

	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return candidates[0]
}

func loadOrCreateAESKey(cacheDir string) ([]byte, []byte, error) {
	if cacheDir == "" {
		return nil, nil, errors.New("empty cache dir")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, nil, err
	}

	path := filepath.Join(cacheDir, "aeskey")
	if data, err := os.ReadFile(path); err == nil {
		parts := strings.Split(strings.TrimSpace(string(data)), "/")
		if len(parts) == 2 && len(parts[0]) == 16 && len(parts[1]) == 16 {
			return []byte(parts[0]), []byte(parts[1]), nil
		}
	}

	key, err := randomString(16)
	if err != nil {
		return nil, nil, err
	}
	iv, err := randomString(16)
	if err != nil {
		return nil, nil, err
	}

	if err := os.WriteFile(path, []byte(key+"/"+iv), 0o600); err != nil {
		return nil, nil, err
	}
	return []byte(key), []byte(iv), nil
}

func randomString(n int) (string, error) {
	const alphabet = "qwertyuioplkjhgfdsazxcvbnmQWERTYUIOPLKJHGFDSAZXCVBNM1234567890"
	if n <= 0 {
		return "", nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i := range buf {
		out[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(out), nil
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func detectExt(uri string, isProxyImg bool) string {
	u := strings.ToLower(uri)
	if isProxyImg {
		switch {
		case strings.Contains(u, ".png"):
			return ".png"
		case strings.Contains(u, ".webp"):
			return ".webp"
		default:
			return ".jpg"
		}
	}

	extensions := []string{
		".m3u8", ".m3u", ".mpd", ".webm", ".ts", ".m4s", ".mp4",
		".mov", ".mkv", ".aac", ".vtt", ".srt", ".jpg", ".jpeg", ".png", ".webp",
	}
	for _, ext := range extensions {
		if strings.Contains(u, ext) {
			if ext == ".jpeg" {
				return ".jpg"
			}
			return ext
		}
	}
	return ""
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - (len(data) % blockSize)
	if pad == 0 {
		pad = blockSize
	}
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid block size")
	}
	pad := int(data[len(data)-1])
	if pad <= 0 || pad > blockSize || pad > len(data) {
		return nil, errors.New("invalid padding")
	}
	for i := len(data) - pad; i < len(data); i++ {
		if int(data[i]) != pad {
			return nil, errors.New("invalid padding")
		}
	}
	return data[:len(data)-pad], nil
}

// --- Cache management ---

const maxLinkEntries = 50_000

// Cleanup removes expired entries and enforces max size.
func (m *Manager) Cleanup() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	removed := 0
	for k, v := range m.links {
		if !v.Expires.IsZero() && now.After(v.Expires) {
			delete(m.links, k)
			removed++
		}
	}
	if len(m.links) > maxLinkEntries {
		over := len(m.links) - maxLinkEntries
		for k := range m.links {
			if over <= 0 {
				break
			}
			delete(m.links, k)
			over--
			removed++
		}
	}
	return removed
}

// Len returns the current number of cached entries.
func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.links)
}
