package iptvhttp

import (
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/auth"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// iptv_stream_auth.go — защита ручек реестра после кражи экспортного плейлиста.
//
// Два независимых слоя:
//
//  1. Подписанные ссылки /stream: каждая ссылка несёт sig = HMAC(key,
//     "stream|id|token|exp") и валидный tgauth-токен. Утёкший файл без подписей
//     мёртв; подписанный — привязан к токену (отзыв токена убивает файл) и,
//     при exp, к сроку. Ключ — локальный секрет сервера (database/iptv/stream.key),
//     клиентам не раздаётся.
//
//  2. Аттестация приложения (X-App-Proof): официальный клиент подписывает
//     каждый запрос общим секретом [iptv].app_secret. Generic-плеер/curl с
//     краденым токеном гейт не проходит. Режимы off/log/enforce — см. config.

// ---------------------------------------------------------------------------
//  Ключ подписи стрим-ссылок
// ---------------------------------------------------------------------------

// loadStreamKey читает (или создаёт при первом старте) серверный ключ подписи.
// Потеря файла лишь инвалидирует ранее выданные экспортные ссылки — реэкспорт
// чинит; поэтому никакого специального бэкапа не требуется.
func loadStreamKey(repoRoot string) []byte {
	path := filepath.Join(repoRoot, "database", "iptv", "stream.key")
	if raw, err := os.ReadFile(path); err == nil {
		if key, err := hex.DecodeString(strings.TrimSpace(string(raw))); err == nil && len(key) >= 16 {
			return key
		}
		log.Warn().Str("path", path).Msg("iptv: stream.key повреждён — генерирую новый (старые экспортные ссылки протухнут)")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// Без энтропии подписи не будет — гейт останется закрытым (fail closed).
		log.Error().Err(err).Msg("iptv: не смог сгенерировать stream.key")
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		log.Error().Err(err).Str("path", path).Msg("iptv: не смог сохранить stream.key")
	}
	return key
}

// signStreamLink подписывает ссылку /stream/{id} для конкретного токена.
// exp — unix-секунды срока ссылки, "" = бессрочная (живёт, пока жив токен).
func signStreamLink(key []byte, id, token, exp string) string {
	if len(key) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("stream|" + id + "|" + token + "|" + exp))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyStreamLink сверяет подпись константным временем. Пустой ключ или
// пустая подпись — всегда отказ (fail closed).
func verifyStreamLink(key []byte, id, token, exp, sig string) bool {
	if len(key) == 0 || sig == "" {
		return false
	}
	want := signStreamLink(key, id, token, exp)
	return want != "" && hmac.Equal([]byte(want), []byte(sig))
}

// ---------------------------------------------------------------------------
//  Аттестация приложения: X-App-Proof
// ---------------------------------------------------------------------------

// Формат заголовка: "v1.<unix-ts>.<nonce>.<hex-hmac>", где
// hmac = HMAC-SHA256(app_secret, "v1|<ts>|<nonce>|<path>").
// Окно ±appProofWindow от серверного времени; nonce одноразовый в окне
// (анти-replay). path — r.URL.Path без query, чтобы подпись не кочевала
// между ручками.
const appProofWindow = 5 * time.Minute

// seenNonces — одноразовость nonce в пределах окна. Карта крошечная: живёт
// максимум окно, чистится лениво при вставке.
var (
	nonceMu    sync.Mutex
	seenNonces = map[string]int64{}
)

// Часы телевизоров врут: их ставят руками, NTP доходит не всегда, а после
// отключения питания коробка стартует со времени сборки. Строгое окно
// превращало это в ВЕЧНЫЙ 401 на всех ручках /api/iptv — приложение при этом
// показывало «войдите», хотя вход был выполнен, и человек не мог ни понять
// причину, ни починить (прод 2026-09-09: Xiaomi MiTV, расхождение около часа).
//
// Такую подпись мы принимаем ТОЛЬКО в формате v2 (персональный ключ
// устройства: валидный HMAC уже доказывает, что это наша коробка, а ключ
// отзывается отвязкой). Чтобы не терять анти-replay, их nonce живёт отдельно и
// долго — иначе перехваченный заголовок можно было бы повторять каждые пять
// минут. Карта ограничена: при переполнении такие подписи снова отвергаются,
// то есть худший случай — прежнее строгое поведение.
const (
	skewNonceRetain = 24 * time.Hour
	skewNonceMax    = 8192
)

var (
	skewNonceMu sync.Mutex
	skewNonces  = map[string]int64{}
)

func skewNonceSeen(nonce string, now int64) bool {
	skewNonceMu.Lock()
	defer skewNonceMu.Unlock()
	horizon := now - int64(skewNonceRetain/time.Second)
	for n, ts := range skewNonces {
		if ts < horizon {
			delete(skewNonces, n)
		}
	}
	if _, dup := skewNonces[nonce]; dup {
		return true
	}
	if len(skewNonces) >= skewNonceMax {
		return true
	}
	skewNonces[nonce] = now
	return false
}

func nonceSeen(nonce string, now int64) bool {
	nonceMu.Lock()
	defer nonceMu.Unlock()
	horizon := now - int64(appProofWindow/time.Second)
	for n, ts := range seenNonces {
		if ts < horizon {
			delete(seenNonces, n)
		}
	}
	if _, dup := seenNonces[nonce]; dup {
		return true
	}
	seenNonces[nonce] = now
	return false
}

// deviceProofKey резолвит ПЕРСОНАЛЬНЫЙ ключ устройства по его uid. Ставится
// один раз при старте (из httpapi, где живёт tgauth-стор), чтобы этот файл не
// тянул зависимость на весь стор. nil = формат v2 не принимается.
var deviceProofKey func(uid string) (string, bool)

// SetDeviceProofKeyResolver подключает выдачу персональных ключей устройств.
func SetDeviceProofKeyResolver(fn func(uid string) (string, bool)) { deviceProofKey = fn }

// devicePubKey резолвит ПУБЛИЧНЫЙ ключ устройства (формат v3). nil = v3 не
// принимается.
var devicePubKey func(uid string) (pub string, hardware bool, ok bool)

// SetDevicePubKeyResolver подключает выдачу публичных ключей устройств.
func SetDevicePubKeyResolver(fn func(uid string) (string, bool, bool)) { devicePubKey = fn }

// verifyV3 проверяет ECDSA-подпись, сделанную ключом из AndroidKeyStore.
//
// Ключ создан внутри Keystore и приватной частью устройство не распоряжается:
// операции экспорта у API нет вовсе, а материал лежит в /data/misc/keystore —
// ВНЕ каталога приложения. Поэтому слепок каталога данных (то, чем работал мод)
// подписать ничего не может, сколько бы токенов и proof_key он ни увёз.
func verifyV3(pubB64, signed, sigB64 string) bool {
	der, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return false
	}
	anyPub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return false
	}
	pub, ok := anyPub.(*ecdsa.PublicKey)
	if !ok {
		return false
	}
	// Java отдаёт подпись SHA256withECDSA в ASN.1 DER — VerifyASN1 её и ждёт.
	// base64 берём url-safe без паддинга: значение едет в HTTP-заголовке.
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(signed))
	return ecdsa.VerifyASN1(pub, sum[:], sig)
}

// checkAppProof валидирует заголовок. Возвращает "" при успехе, иначе причину
// отказа (для лога/ответа).
//
// Принимаются два формата:
//
//	v1.<ts>.<nonce>.<hmac>        — общий секрет [iptv].app_secret
//	v2.<uid>.<ts>.<nonce>.<hmac>  — ПЕРСОНАЛЬНЫЙ ключ устройства
//
// v1 живёт только ради переходного периода: его секрет зашит во все клиенты,
// лежит открытым текстом в js-бандле и в APK и вынимается за минуту — то есть
// подделывается кем угодно. v2 такого секрета не имеет вовсе: ключ выдаётся
// сервером конкретному устройству по уже авторизованному каналу, и разбор
// приложения не даёт ничего. Когда клиенты перейдут на v2, приём v1 надо
// выключить — иначе он остаётся дырой, через которую обходится вся аттестация.
func checkAppProof(secret string, r *http.Request) string {
	return checkAppProofV(secret, r, false)
}

// checkAppProofV — та же проверка с явным запретом старого формата. Отдельный
// параметр, а не глобальный флаг: так поведение видно в тестах по вызову.
func checkAppProofV(secret string, r *http.Request, rejectV1 bool) string {
	raw := r.Header.Get("X-App-Proof")
	if raw == "" {
		return "нет X-App-Proof"
	}
	parts := strings.Split(raw, ".")

	var key, tsStr, nonce, sig, signed string
	switch {
	case len(parts) == 5 && parts[0] == "v3":
		// Аппаратный ключ устройства. Проверяется здесь же и полностью: дальше
		// по функции идёт HMAC, а тут подпись асимметричная.
		if devicePubKey == nil {
			return "v3 не поддерживается"
		}
		pub, _, ok := devicePubKey(parts[1])
		if !ok {
			return "unknown device"
		}
		tsStr, sig = parts[2], parts[4]
		signed = "v3|" + parts[1] + "|" + tsStr + "|" + parts[3] + "|" + r.URL.Path
		nonce = parts[1] + ":" + parts[3]
		if !verifyV3(pub, signed, sig) {
			return "bad proof signature"
		}
		return checkProofFreshness(tsStr, nonce, parts[1], r.URL.Path, true)
	case len(parts) == 5 && parts[0] == "v2":
		if deviceProofKey == nil {
			return "v2 не поддерживается"
		}
		k, ok := deviceProofKey(parts[1])
		if !ok {
			// Неизвестное или отвязанное устройство. Отдельная причина: это
			// НЕ подделка подписи, а отозванный/несуществующий uid.
			return "unknown device"
		}
		key, tsStr, sig = k, parts[2], parts[4]
		signed = "v2|" + parts[1] + "|" + tsStr + "|" + parts[3] + "|" + r.URL.Path
		// Одноразовость считаем В ПРЕДЕЛАХ устройства: общий на всех набор
		// nonce означал бы, что случайное совпадение у двух телевизоров рубит
		// живой запрос как «повтор».
		nonce = parts[1] + ":" + parts[3]
	case len(parts) == 4 && parts[0] == "v1":
		if rejectV1 {
			// Старый формат больше не принимаем: его секрет одинаков во всех
			// сборках и извлекаем. Отдельная причина, чтобы в логе было видно
			// «клиент не обновился», а не «подделал подпись».
			return "устаревший формат подписи (обновите приложение)"
		}
		if secret == "" {
			return "app_secret не задан"
		}
		key, tsStr, nonce, sig = secret, parts[1], parts[2], parts[3]
		signed = "v1|" + tsStr + "|" + nonce + "|" + r.URL.Path
	default:
		return "bad proof format"
	}

	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(signed))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sig)) {
		return "bad proof hmac"
	}
	// Окно времени и одноразовость — ПОСЛЕ проверки подписи: иначе кто угодно
	// занимал бы чужие nonce мусорными запросами и валил живые устройства.
	//
	// Послабление по часам только для v2: у v1 секрет одинаков во всех сборках,
	// и подделать его может кто угодно. uid для журнала есть тоже только у v2 —
	// у v1 на этом месте метка времени, а не устройство.
	uid := ""
	if parts[0] == "v2" {
		uid = parts[1]
	}
	return checkProofFreshness(tsStr, nonce, uid, r.URL.Path, parts[0] == "v2")
}

// checkProofFreshness — общая для v2 и v3 часть: окно времени и одноразовость.
// Подпись к этому моменту уже проверена, иначе кто угодно занимал бы чужие
// nonce мусорными запросами и валил живые устройства.
//
// allowSkew послабляет окно ±5 минут: у телевизоров часы врут регулярно (руками
// поставили, NTP не дошёл, после отключения питания коробка стартует со времени
// сборки), а валидная подпись персональным или аппаратным ключом сама по себе
// доказывает, что это наша коробка. Для v1 послабления нет и быть не может —
// его секрет одинаков во всех сборках.
func checkProofFreshness(tsStr, nonce, uid, path string, allowSkew bool) string {
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "bad proof ts"
	}
	now := time.Now().Unix()
	skew := now - ts
	skewed := skew > int64(appProofWindow/time.Second) || skew < -int64(appProofWindow/time.Second)
	if skewed && !allowSkew {
		return "proof вне окна времени"
	}
	if skewed {
		if skewNonceSeen(nonce, now) {
			return "proof replay"
		}
		log.Warn().Str("uid", uid).Int64("skew_sec", skew).Str("path", path).
			Msg("iptv: подпись принята при сбитых часах устройства (ключ устройства)")
		return ""
	}
	if nonceSeen(nonce, now) {
		return "proof replay"
	}
	return ""
}

// appProofMiddleware оборачивает ручки, доступные только официальному
// приложению. Поведение по режимам — см. config.IPTVConfig.AppAttest.
func appProofMiddleware(cfg config.IPTVConfig) func(http.Handler) http.Handler {
	mode := cfg.AppAttest
	return func(next http.Handler) http.Handler {
		if mode != "log" && mode != "enforce" {
			return next // "off" (default)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reason := checkAppProof(cfg.AppSecret, r)
			if reason == "" {
				next.ServeHTTP(w, r)
				return
			}
			if mode == "log" {
				log.Warn().Str("path", r.URL.Path).Str("reason", reason).Str("ua", r.UserAgent()).Msg("iptv: запрос без валидной аттестации приложения (режим log — пропущен)")
				next.ServeHTTP(w, r)
				return
			}
			log.Warn().Str("path", r.URL.Path).Str("reason", reason).Msg("iptv: запрос отклонён аттестацией приложения")
			// Клиенту нужно отличать «сервер не признал приложение» от «ты не
			// вошёл»: раньше оба были голым 401, и приложение звало входить
			// заново там, где вход ни при чём. X-Server-Time позволяет ещё и
			// поправить свои часы и повторить запрос.
			w.Header().Set("X-Attest-Fail", reason)
			w.Header().Set("X-Server-Time", strconv.FormatInt(time.Now().Unix(), 10))
			http.Error(w, "unauthorized client", http.StatusUnauthorized)
		})
	}
}

// streamAuthToken достаёт tgauth-токен запроса: query ?token= главнее, cookie
// lampac_token — фолбэк (web-клиент).
func streamAuthToken(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	// Общий разбор источников токена: HttpOnly-якорь `_lampac_auth` и
	// заголовки сюда тоже входят. На телевизорах при перезапуске приложения
	// вычищается именно JS-видимая банка кук — человек оставался
	// авторизованным для гейта и анонимом здесь.
	return auth.ExtractToken(r)
}

// requireRegistryAuth — жёсткий гейт каналов НАШЕГО реестра. iptvTgID для этого
// не годится: её md5-фолбэк превращает ЛЮБУЮ выдуманную куку в «пользователя» —
// это мягкая идентификация для личных плейлистов, не авторизация. Здесь же
// принимаем только то, что реально прошло tgauth.Lookup (cookie / ?token= /
// ?uid= устройства) — calendarTgID проверяет ровно это. Возвращает false →
// хендлер обязан ответить 401 и не раскрывать даже существование канала.
func requireRegistryAuth(r *http.Request, tgStore *tgauth.Store) bool {
	return calendarTgID(r, tgStore) != 0
}
