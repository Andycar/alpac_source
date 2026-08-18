package litesrc

// api.filmix.tv (api-fx) integration for the filmix balancer.
//
// 2026-07: filmixapp.cyou /api/v2 stopped authorizing >720p — the /s/<hash>/ CDN
// links it returns serve a ~10MB "купите премиум" stub mp4 (HTTP 206, video/mp4)
// for 1080/1440/2160 even with an active PRO+ token, and the post "quality" field
// now reflects the CONTENT's max quality, not the account tier. The api-fx
// video-links endpoint is what the current Filmix clients use: it returns ready
// per-quality HLS links that actually stream. Anonymous access needs only a hash
// from /api-fx/request-token; account access (RKN-blocked titles, guaranteed 4K)
// needs login+password → accessToken (the legacy 32-char device token is NOT
// accepted as Bearer — verified live).

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

const (
	// fxAccessTokenTTL mirrors upstream lampac: reuse the accessToken for 5
	// minutes, then refresh via refreshToken.
	fxAccessTokenTTL = 5 * time.Minute
	// fxHashFailCooldown keeps an unreachable api.filmix.tv from stalling every
	// request on connect timeouts before the legacy fallback kicks in.
	fxHashFailCooldown = time.Minute
	// fxAuthFailCooldown gates full-login retries. Filmix caps active device
	// sessions per account ("Вы не можете добавить больше устройств") — retrying
	// a failing login on every playback request would hammer the API for nothing.
	fxAuthFailCooldown = 5 * time.Minute
	// fxAliveProbeTTL — как часто перепроверять, жива ли сессия (`/api-fx/me`). Не путать с ротацией:
	// hash живёт, пока действует, и перевыпускается ТОЛЬКО по провалу этой проверки.
	fxAliveProbeTTL = 10 * time.Minute
	// fxAliveFailsToDie — сколько ПОДРЯД явных отказов нужно, чтобы признать сессию мёртвой.
	fxAliveFailsToDie = 2
	// fxLoginMinInterval — жёсткий минимум между полными логинами. Логин занимает один из ПЯТИ
	// слотов устройства навсегда, а новая сессия вытесняет прежнюю: 2026-08-06 накопилось 26 логинов
	// за сутки, и у смотрящих ссылки посреди фильма превращались в 403. Лучше час без обновления
	// сессии, чем выжженный аккаунт.
	fxLoginMinInterval = 3 * time.Hour
	fxBrowserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
)

// fxStatePath is where the api-fx session (hash + auth tokens) is persisted.
//
// This is load-bearing, not an optimisation: every /api-fx/auth REGISTERS A NEW
// DEVICE on the account and Filmix hard-caps that at five — the API answers the
// sixth login with «Вы не можете добавить больше 5 устройств!» (verified live
// 2026-08-01). Nothing reuses an existing device, so any lost state costs a slot
// and the account has to be cleaned up by hand. Anchored to repoRoot for the
// same reason the kinopub token store is: a CWD-relative path silently writes
// somewhere else (or nowhere) depending on how the service is started, and every
// restart would then burn another device.
func fxStatePath(repoRoot string) string {
	if strings.TrimSpace(repoRoot) == "" {
		repoRoot = "."
	}
	return filepath.Join(repoRoot, "database", "filmix_fx.json")
}

type fxPersistedState struct {
	User     string    `json:"user"`
	Hash     string    `json:"hash"`
	HashAt   time.Time `json:"hash_at"`
	Access   string    `json:"access"`
	AccessAt time.Time `json:"access_at"`
	// AccessHash — hash, ДЛЯ КОТОРОГО выдан access: логин и refresh уходят с заголовком
	// `hash`, поэтому чужому hash этот токен прав не даёт.
	AccessHash string `json:"access_hash"`
	Refresh    string `json:"refresh"`
}

// fxStateLoad restores persisted session state once. Caller holds fxMu.
// Only meaningful with account credentials (anonymous hash is cheap to refetch),
// and stored tokens are dropped when the configured login changed.
func (f *filmixChecker) fxStateLoad() {
	if f.fxStateLoaded {
		return
	}
	f.fxStateLoaded = true
	if f.fxUser == "" {
		return
	}
	raw, err := os.ReadFile(f.fxStatePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Warn().Err(err).Str("path", f.fxStatePath).
				Msg("filmix: api-fx state unreadable — a fresh login will consume one of the account's 5 device slots")
		}
		return
	}
	var st fxPersistedState
	if err := stdjson.Unmarshal(raw, &st); err != nil || st.User != f.fxUser {
		return
	}
	// Переиспользуем сохранённый hash, но ПОМНИМ ЕГО ВОЗРАСТ: он не вечен (см. fxHashTTL), а
	// раньше время выпуска не сохранялось — после рестарта hash считался свежим и жил до тех пор,
	// пока весь filmix не начинал отвечать 403.
	if st.Hash != "" {
		f.fxHash = st.Hash
		f.fxHashAt = st.HashAt
		if f.fxHashAt.IsZero() {
			f.fxHashAt = time.Now() // состояние из старой версии — считаем свежим один цикл TTL
		}
	}
	f.fxAccess = st.Access
	f.fxAccessAt = st.AccessAt
	f.fxAccessHash = st.AccessHash
	f.fxRefresh = st.Refresh
}

// fxStateSave persists the current session. Caller holds fxMu. Best-effort —
// a read-only data dir just means restarts re-login (the old behavior).
func (f *filmixChecker) fxStateSave() {
	if f.fxUser == "" {
		return
	}
	st := fxPersistedState{
		User:       f.fxUser,
		Hash:       f.fxHash,
		HashAt:     f.fxHashAt,
		Access:     f.fxAccess,
		AccessAt:   f.fxAccessAt,
		AccessHash: f.fxAccessHash,
		Refresh:    f.fxRefresh,
	}
	raw, err := stdjson.Marshal(st)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(f.fxStatePath), 0o755)
	if err := os.WriteFile(f.fxStatePath, raw, 0o600); err != nil {
		// Not debug: an unwritable state file means the next restart logs in again
		// and eats another of the five device slots.
		log.Warn().Err(err).Str("path", f.fxStatePath).Msg("filmix: api-fx state save failed")
	}
}

type fxFile struct {
	URL     string    `json:"url"`
	Quality filmixInt `json:"quality"`
}

type fxMovie struct {
	Voiceover string   `json:"voiceover"`
	Files     []fxFile `json:"files"`
}

type fxSeason struct {
	Season   filmixInt            `json:"season"`
	Episodes map[string]fxEpisode `json:"episodes"`
}

type fxEpisode struct {
	Episode filmixInt `json:"episode"`
	Files   []fxFile  `json:"files"`
}

// fxSerial is voice → seasonKey ("season-1") → season payload.
type fxSerial map[string]map[string]fxSeason

// fxGet fetches an api-fx URL with the hash (+ optional Bearer) headers,
// honoring rhub routing like the legacy API calls.
func (f *filmixChecker) fxGet(ctx context.Context, target string, headers map[string]string, maxBody int64) (string, error) {
	return rchFetchCtx(ctx, f.rhub, target, headers, func() (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return "", err
		}
		for hk, hv := range headers {
			req.Header.Set(hk, hv)
		}
		resp, err := balancerDoWithRetry(ctx, f.client, req, 2)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("fx: status %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return "", err
		}
		return string(b), nil
	})
}

// fxEnsureHash returns the request-token hash, minting one ONLY when there is
// none at all. The hash is the single most precious piece of state here.
//
// THE HASH *IS* THE DEVICE. Verified live 2026-08-01: logging in on a hash the
// account already knows returns tokens straight away, while the same login on a
// freshly minted hash is what answers «Вы не можете добавить больше 5 устройств!».
// So a reused hash costs nothing and a new hash costs one of five slots forever
// (there is no API to release one — the user must delete devices by hand).
//
// Two consequences, both learned the hard way:
//
//   - The `expire` field in the request-token response does NOT bound the hash.
//     It belongs to the short pairing `code` returned next to it. A hash 7 hours
//     past that `expire` still served video-links fine. Honouring it minted a new
//     hash — and therefore a new device — every 3.5 hours.
//   - A {"message": …} answer from video-links (geo/RKN-blocked title) is a fact
//     about the title, not about the hash. Re-minting on it burned a slot every
//     10 minutes. There is deliberately no "force" mode here.
//
// fxVerifyAlive проверяет, что связка hash+access ещё действует, и только по её провалу разрешает
// перевыпуск. `/api-fx/me` отвечает профилем аккаунта (user_id, is_pro_plus) — это дешевле и точнее,
// чем судить по видео-ответам: {"message":…} от video-links говорит о ТАЙТЛЕ (гео/РКН), а не о hash,
// и раньше уже приводил к перевыпуску каждые 10 минут.
func (f *filmixChecker) fxVerifyAlive(ctx context.Context, hash, access string) {
	if hash == "" || access == "" {
		return
	}
	f.fxMu.Lock()
	if f.fxHashDead || time.Since(f.fxAliveAt) < fxAliveProbeTTL {
		f.fxMu.Unlock()
		return
	}
	f.fxAliveAt = time.Now()
	f.fxMu.Unlock()

	// ★Запрос идёт НЕ через fxGet: тот схлопывает сетевую ошибку и HTTP-статус в один err, а нам
	// важно их различать. Таймаут/5xx НЕ означают, что сессия мертва, — а раньше означали, и каждый
	// такой случай стоил полного логина, то есть слота устройства.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.fxHost+"/api-fx/me", nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", fxBrowserUserAgent)
	req.Header.Set("hash", hash)
	req.Header.Set("Authorization", "Bearer "+access)

	resp, err := f.client.Do(req)
	if err != nil {
		return // сеть недоступна — про сессию это не говорит ничего
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusOK && strings.Contains(string(body), "user_id"):
		f.fxMu.Lock()
		f.fxAliveFails = 0
		f.fxMu.Unlock()
		return
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusTooManyRequests:
		return // временная беда апстрима, не отказ авторизации
	}

	// Явный отказ (401/403 или 200 без профиля). Даже ему верим не с первого раза: одиночный сбой
	// не должен стоить слота устройства.
	f.fxMu.Lock()
	f.fxAliveFails++
	fails := f.fxAliveFails
	if fails >= fxAliveFailsToDie {
		f.fxHashDead = true
	}
	f.fxMu.Unlock()

	log.Warn().Int("status", resp.StatusCode).Int("fails", fails).
		Msg("filmix: api-fx сессия не подтвердилась")
}

func (f *filmixChecker) fxEnsureHash(ctx context.Context) string {
	f.fxMu.Lock()
	defer f.fxMu.Unlock()
	f.fxStateLoad()

	// ★Ротация ТОЛЬКО по факту смерти hash (fxHashDead ставит fxVerifyAlive), никаких TTL.
	// Ежечасная ротация 2026-08-05 стоила пяти логинов за три часа: каждый логин занимает слот
	// устройства (их пять), новая сессия вытесняет прежнюю — и ссылки, УЖЕ отданные зрителям,
	// начинали отдавать 403 прямо посреди просмотра. Комментарий выше предупреждал ровно об этом.
	if f.fxHash != "" && !f.fxHashDead {
		return f.fxHash
	}
	if f.fxHash != "" && time.Since(f.fxHashFetchedAt) < fxHashFailCooldown {
		return f.fxHash // перевыпуск только что не удался — мёртвый лучше пустого, дальше легаси
	}
	if time.Since(f.fxHashFetchedAt) < fxHashFailCooldown {
		return "" // recent fetch failure — go straight to the legacy fallback
	}

	body, err := f.fxGet(ctx, f.fxHost+"/api-fx/request-token", map[string]string{"User-Agent": fxBrowserUserAgent}, 1<<20)
	if err != nil || body == "" {
		f.fxHashFetchedAt = time.Now()
		return f.fxHash // expired-but-present beats nothing; the fallback handles a reject
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := stdjson.Unmarshal([]byte(body), &tok); err != nil || strings.TrimSpace(tok.Token) == "" {
		f.fxHashFetchedAt = time.Now()
		return f.fxHash
	}
	f.fxHash = strings.TrimSpace(tok.Token)
	f.fxHashFetchedAt = time.Now()
	f.fxHashAt = f.fxHashFetchedAt
	f.fxHashDead = false
	f.fxAliveAt = f.fxHashFetchedAt
	// The auth tokens are NOT dropped here. Clearing them on every new hash is
	// what made a hash refresh imply a re-login, and a re-login costs a device
	// slot. If the tokens really did become invalid, fxEnsureAccess refreshes
	// (cheap) and only falls back to a full login once the refresh fails.
	f.fxStateSave()
	return f.fxHash
}

// fxEnsureAccess returns a Bearer accessToken for the configured account, or ""
// when no credentials are set / auth fails (anonymous hash access still works).
// Callers must pass the current hash. State transitions: cached token (TTL) →
// refresh via refreshToken → full login.
func (f *filmixChecker) fxEnsureAccess(ctx context.Context, hash string) string {
	if f.fxUser == "" || f.fxPasswd == "" || hash == "" {
		return ""
	}

	f.fxMu.Lock()
	defer f.fxMu.Unlock()
	f.fxStateLoad()

	// ★Кэш годится только если токен выдан ДЛЯ ЭТОГО hash. Иначе api-fx отвечает как анониму
	// («Заблокировано правообладателем!» на РКН-тайтлах, заглушки вместо файлов) — ровно это и
	// случилось, когда hash перевыпустили, а access остался от прежнего.
	if f.fxAccess != "" && f.fxAccessHash == hash && time.Since(f.fxAccessAt) < fxAccessTokenTTL {
		return f.fxAccess
	}
	if f.fxAccessHash != hash {
		f.fxAccess = "" // токен от чужого hash бесполезен
	}

	headers := map[string]string{"User-Agent": fxBrowserUserAgent, "hash": hash}

	if f.fxRefresh != "" {
		body, err := f.fxGet(ctx, f.fxHost+"/api-fx/refresh?refreshToken="+url.QueryEscape(f.fxRefresh), headers, 1<<20)
		if err == nil && f.fxStoreAuth(body, hash) {
			f.fxStateSave()
			return f.fxAccess
		}
		f.fxRefresh = ""
	}

	// Full login registers a DEVICE on the account (Filmix caps those), so a
	// persistently failing login must not be retried per playback request.
	if time.Since(f.fxAuthFailAt) < fxAuthFailCooldown {
		return ""
	}

	// ★★★Жёсткий потолок частоты логинов. Слотов пять, освободить их через API нельзя, а каждая
	// новая сессия ВЫТЕСНЯЕТ прежнюю — у смотрящих ссылки посреди фильма становятся 403. За сутки
	// 2026-08-06 накопилось 26 логинов: сессия периодически «не подтверждалась» (в том числе от
	// обычного таймаута), и каждый раз это стоило слота. Без сессии filmix просто уходит на легаси,
	// это несравнимо дешевле выжженного аккаунта.
	if !f.fxLoginAt.IsZero() && time.Since(f.fxLoginAt) < fxLoginMinInterval {
		log.Warn().Time("last_login", f.fxLoginAt).
			Msg("filmix: логин пропущен — слишком часто (защита слотов устройств)")
		return ""
	}
	f.fxLoginAt = time.Now()

	// Every login here REGISTERS A NEW DEVICE (cap: 5). It should happen about
	// once per install, so log it — a recurring line means state is being lost
	// and the account is filling up with dead devices.
	log.Warn().Str("user", f.fxUser).Str("state", f.fxStatePath).
		Msg("filmix: api-fx full login — это регистрирует НОВОЕ устройство (лимит 5). Повторяется? значит состояние не сохраняется")

	// POST goes direct (rch fetch is GET-only) but through the balancer client,
	// so a proxy mapped to "filmix" covers it.
	payload := fmt.Sprintf(`{"user_name":%s,"user_passw":%s,"session":true}`,
		jsonString(f.fxUser), jsonString(f.fxPasswd))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.fxHost+"/api-fx/auth", strings.NewReader(payload))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	for hk, hv := range headers {
		req.Header.Set(hk, hv)
	}
	resp, err := balancerDoWithRetry(ctx, f.client, req, 2)
	if err != nil {
		f.fxAuthFailAt = time.Now()
		return ""
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		f.fxAuthFailAt = time.Now()
		return ""
	}
	if !f.fxStoreAuth(string(b), hash) {
		f.fxAuthFailAt = time.Now()
		answer := fxErrorMessage(b)
		if fxIsDeviceLimit(answer) {
			// The account is full and THIS hash is not one of the registered
			// devices, so no retry can help — only deleting devices by hand can.
			// Keep the hash anyway: anonymous video-links still works with it.
			log.Error().Str("user", f.fxUser).Str("answer", answer).
				Msg("filmix: лимит устройств на аккаунте исчерпан — удали лишние устройства в кабинете Filmix. До этого источник работает БЕЗ авторизации (РКН-тайтлы уйдут в легаси-фолбэк ≤720p)")
		} else {
			log.Warn().Str("user", f.fxUser).Str("answer", answer).
				Msg("filmix: api-fx auth failed — проверь user_apitv/passwd_apitv")
		}
		return ""
	}
	f.fxStateSave()
	return f.fxAccess
}

// fxIsDeviceLimit recognises «Вы не можете добавить больше 5 устройств!» — the
// answer to a login on a hash the account has never seen once all slots are used.
func fxIsDeviceLimit(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "устройств")
}

// fxErrorMessage extracts the human-readable error from an api-fx answer.
func fxErrorMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if err := stdjson.Unmarshal(body, &m); err != nil {
		s := strings.TrimSpace(string(body))
		if len(s) > 120 {
			s = s[:120]
		}
		return s
	}
	if m.Message != "" {
		return m.Message
	}
	return m.Msg
}

// fxStoreAuth parses an auth/refresh response and stores the tokens.
// Caller holds fxMu.
// fxStoreAuth принимает hash, для которого выдан токен: без этой привязки токен от прежнего hash
// молча используется дальше и api-fx отвечает как анониму.
func (f *filmixChecker) fxStoreAuth(body, hash string) bool {
	var auth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := stdjson.Unmarshal([]byte(body), &auth); err != nil {
		return false
	}
	if strings.TrimSpace(auth.AccessToken) == "" {
		return false
	}
	f.fxAccess = strings.TrimSpace(auth.AccessToken)
	f.fxAccessAt = time.Now()
	f.fxAccessHash = hash
	if strings.TrimSpace(auth.RefreshToken) != "" {
		f.fxRefresh = strings.TrimSpace(auth.RefreshToken)
	}
	return true
}

func jsonString(s string) string {
	b, err := stdjson.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// fxLinksTTL bounds the video-links cache. The urls inside are signed with the
// session hash, so they stay valid as long as it does; 5 minutes is well inside
// that and still collapses the burst a single card open produces (capi drills
// filmix for every viewer, and every quality/episode switch re-enters index()).
const fxLinksTTL = 5 * time.Minute

type fxLinksEntry struct {
	movies []fxMovie
	serial fxSerial
	ok     bool
	at     time.Time
}

// fxLinksCacheKey scopes the entry to the identity the links were minted for —
// an anonymous hash and an authorised account get different answers (RKN-blocked
// titles, quality ladder), so they must not share an entry.
func (f *filmixChecker) fxLinksCacheKey(postID int) string {
	if f.fxUser != "" {
		return f.fxUser + ":" + strconv.Itoa(postID)
	}
	return "anon:" + strconv.Itoa(postID)
}

func (f *filmixChecker) fxLinksCacheGet(key string) (fxLinksEntry, bool) {
	v, ok := f.fxLinks.Load(key)
	if !ok {
		return fxLinksEntry{}, false
	}
	entry, ok := v.(fxLinksEntry)
	if !ok || time.Since(entry.at) >= fxLinksTTL {
		f.fxLinks.Delete(key)
		return fxLinksEntry{}, false
	}
	return entry, true
}

// fxVideoLinks fetches /api-fx/post/{id}/video-links, memoised for fxLinksTTL
// with singleflight so concurrent viewers of one title cause ONE upstream call.
// Exactly one of movies/serial is non-empty on ok. A {"message": …} answer is
// taken at face value (usually a geo/RKN-blocked title) — index() then falls back
// to the legacy API. It is NOT retried with a fresh hash: that retry is what used
// to churn through the account's five device slots (see fxEnsureHash).
func (f *filmixChecker) fxVideoLinks(ctx context.Context, postID int) (movies []fxMovie, serial fxSerial, ok bool) {
	if f.fxHost == "" {
		return nil, nil, false
	}

	key := f.fxLinksCacheKey(postID)
	if entry, hit := f.fxLinksCacheGet(key); hit {
		return entry.movies, entry.serial, entry.ok
	}

	// Singleflight: followers wait for the leader's fetch, then read the cache.
	myCh := make(chan struct{})
	if actual, loaded := f.fxInflight.LoadOrStore(key, myCh); loaded {
		select {
		case <-actual.(chan struct{}):
		case <-ctx.Done():
			return nil, nil, false
		}
		if entry, hit := f.fxLinksCacheGet(key); hit {
			return entry.movies, entry.serial, entry.ok
		}
		return nil, nil, false
	}
	defer func() {
		// Negative results are cached too — a blocked title must not re-hit the
		// API for every viewer, and index() falls back to the legacy path anyway.
		f.fxLinks.Store(key, fxLinksEntry{movies: movies, serial: serial, ok: ok, at: time.Now()})
		if ch, loaded := f.fxInflight.LoadAndDelete(key); loaded {
			close(ch.(chan struct{}))
		}
	}()
	hash := f.fxEnsureHash(ctx)
	if hash == "" {
		return nil, nil, false
	}
	headers := map[string]string{"User-Agent": fxBrowserUserAgent, "hash": hash}
	if access := f.fxEnsureAccess(ctx, hash); access != "" {
		headers["Authorization"] = "Bearer " + access
		// Не чаще раза в fxAliveProbeTTL: подтверждаем, что сессия жива. Пока жива — hash НЕ
		// трогаем, иначе ссылки у смотрящих прямо сейчас превращаются в 403.
		f.fxVerifyAlive(ctx, hash, access)
	}

	body, err := f.fxGet(ctx, fmt.Sprintf("%s/api-fx/post/%d/video-links", f.fxHost, postID), headers, 8<<20)
	if err != nil || strings.TrimSpace(body) == "" {
		return nil, nil, false
	}

	movies, serial, ok = decodeFXVideoLinks([]byte(body))
	return movies, serial, ok
}

// decodeFXVideoLinks parses a video-links body: JSON array = movie voiceovers,
// JSON object = serial voices, {"message": ...} = error.
func decodeFXVideoLinks(body []byte) (movies []fxMovie, serial fxSerial, ok bool) {
	body = bytesTrimSpace(body)
	if len(body) == 0 {
		return nil, nil, false
	}

	if bytes.HasPrefix(body, []byte("[")) {
		if err := stdjson.Unmarshal(body, &movies); err != nil {
			return nil, nil, false
		}
		out := movies[:0]
		for _, m := range movies {
			if len(m.Files) > 0 {
				out = append(out, m)
			}
		}
		if len(out) == 0 {
			return nil, nil, false
		}
		return out, nil, true
	}

	var probe map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal(body, &probe); err != nil {
		return nil, nil, false
	}
	if _, isErr := probe["message"]; isErr {
		return nil, nil, false
	}
	if err := stdjson.Unmarshal(body, &serial); err != nil {
		return nil, nil, false
	}
	for voice, seasons := range serial {
		if len(seasons) == 0 {
			delete(serial, voice)
		}
	}
	if len(serial) == 0 {
		return nil, nil, false
	}
	return nil, serial, true
}

// fxMaxQuality returns the best quality present anywhere in a video-links
// answer — the geo-degradation detector (non-RU IPs get 480p-only lists).
func fxMaxQuality(movies []fxMovie, serial fxSerial) int {
	best := 0
	consider := func(files []fxFile) {
		for _, file := range files {
			if q := file.Quality.Int(); q > best {
				best = q
			}
		}
	}
	for _, movie := range movies {
		consider(movie.Files)
	}
	for _, seasons := range serial {
		for _, season := range seasons {
			for _, episode := range season.Episodes {
				consider(episode.Files)
			}
		}
	}
	return best
}

// writeFX renders a video-links answer (movie or serial).
func (f *filmixChecker) writeFX(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	movies []fxMovie,
	serial fxSerial,
	postID int,
	title string,
	originalTitle string,
	t int,
	sSet bool,
	s int,
	links *proxylink.Manager,
) {
	if len(movies) > 0 {
		f.writeFXMovie(w, req, rjson, movies, postID, title, originalTitle, links)
		return
	}
	f.writeFXSerial(w, req, rjson, serial, postID, title, originalTitle, t, sSet, s, links)
}

// fxStreams converts a files list into the streamquality rows (desc by quality).
func fxStreams(files []fxFile) []map[string]string {
	sorted := make([]fxFile, 0, len(files))
	for _, file := range files {
		if file.Quality.Int() > 0 && strings.TrimSpace(file.URL) != "" {
			sorted = append(sorted, file)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Quality.Int() > sorted[j].Quality.Int() })

	out := make([]map[string]string, 0, len(sorted))
	for _, file := range sorted {
		out = append(out, map[string]string{
			"quality": fmt.Sprintf("%dp", file.Quality.Int()),
			"url":     strings.TrimSpace(file.URL),
		})
	}
	return out
}

func (f *filmixChecker) writeFXMovie(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	movies []fxMovie,
	postID int,
	title string,
	originalTitle string,
	links *proxylink.Manager,
) {
	rows := make([]map[string]any, 0, len(movies))
	labels := make([]string, 0, len(movies))
	baseTitle := getsTVJoinName(title, originalTitle)

	for _, movie := range movies {
		streams := fxStreams(movie.Files)
		if len(streams) == 0 {
			continue
		}
		progOK := false
		// Прогрессив (один mp4) отдаётся ПРЯМОЙ ссылкой: он не привязан к IP, поэтому зритель
		// качает его с CDN сам — медиа мимо сервера, без буферизации. HLS остаётся проксированным
		// (его сегменты IP-привязаны и требуют починки EXT-X-MAP).
		if prog, ok := f.progressiveStreams(req.Context(), streams); ok {
			streams = prog
			progOK = true
		} else {
			for i := range streams {
				streams[i]["url"] = streamProxyURL(req, streams[i]["url"], "filmix", links)
			}
		}

		name := strings.TrimSpace(movie.Voiceover)
		if name == "" {
			name = "По умолчанию"
		}

		row := map[string]any{
			"method":        "play",
			"url":           streams[0]["url"],
			"stream":        streams[0]["url"],
			"name":          name,
			"title":         baseTitle,
			"streamquality": streams,
		}
		// Прогрессив уже прямой — клиентский минт (архитектура B) только перезатёр бы его HLS-ом.
		if f.fxDirectLampa && !progOK {
			row["fxdirect"] = f.filmixRowFxDirect(postID, name, 0, 0)
		}
		if len(streams) > 1 {
			qualMap := filmixBuildQualityMap(streams)
			row["quality"] = qualMap
			row["qualitys"] = qualMap
		}

		rows = append(rows, row)
		labels = append(labels, name)
	}

	// Добираем рипы, которых api-fx не отдаёт вовсе (HEVC и т.п.). Путь берём из легаси, а подпись —
	// свою, авторизованную: с легаси-подписью CDN отдаёт заглушку. См. filmix_merge.go.
	fxHash := filmixFXHash(movies)
	for _, extra := range f.fxLegacyExtra(req.Context(), postID, movies) {
		streams := filmixSignExtra(extra.Link, fxHash)
		if len(streams) == 0 {
			continue
		}

		name := strings.TrimSpace(extra.Translation)
		if name == "" {
			continue // без ярлыка озвучки строка неотличима от прочих — лучше не показывать
		}
		row := map[string]any{
			"method":        "play",
			"url":           streams[0]["url"],
			"stream":        streams[0]["url"],
			"name":          name,
			"title":         baseTitle,
			"streamquality": streams,
		}
		if len(streams) > 1 {
			qualMap := filmixBuildQualityMap(streams)
			row["quality"] = qualMap
			row["qualitys"] = qualMap
		}
		rows = append(rows, row)
		labels = append(labels, name)
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": rows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (f *filmixChecker) writeFXSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	serial fxSerial,
	postID int,
	title string,
	originalTitle string,
	t int,
	sSet bool,
	s int,
	links *proxylink.Manager,
) {
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	if !sSet {
		seen := map[int]struct{}{}
		seasons := make([]int, 0, 8)
		for _, voiceSeasons := range serial {
			for _, season := range voiceSeasons {
				n := season.Season.Int()
				if n == 0 || len(season.Episodes) == 0 {
					continue
				}
				if _, dup := seen[n]; dup {
					continue
				}
				seen[n] = struct{}{}
				seasons = append(seasons, n)
			}
		}
		sort.Ints(seasons)

		if len(seasons) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		data := make([]map[string]any, 0, len(seasons))
		labels := make([]string, 0, len(seasons))
		for _, season := range seasons {
			link := fmt.Sprintf(
				"%s/lite/filmix?rjson=%s&postid=%d&title=%s&original_title=%s&s=%d",
				host,
				getsTVBool(rjson),
				postID,
				encTitle,
				encOriginal,
				season,
			)
			label := fmt.Sprintf("%d сезон", season)
			data = append(data, map[string]any{
				"method": "link",
				"id":     strconv.Itoa(season),
				"url":    link,
				"name":   label,
			})
			labels = append(labels, label)
		}

		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{
				"type": "season",
				"data": data,
			})
			return
		}

		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range data {
			getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Voices that actually carry the requested season.
	type voiceSeason struct {
		name   string
		season fxSeason
	}
	voices := make([]voiceSeason, 0, len(serial))
	for voice, voiceSeasons := range serial {
		for _, season := range voiceSeasons {
			if season.Season.Int() == s && len(season.Episodes) > 0 {
				voices = append(voices, voiceSeason{name: voice, season: season})
				break
			}
		}
	}
	sort.Slice(voices, func(i, j int) bool { return voices[i].name < voices[j].name })

	if len(voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if t < 0 || t >= len(voices) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	voiceRows := make([]map[string]any, 0, len(voices))
	for i, voice := range voices {
		link := fmt.Sprintf(
			"%s/lite/filmix?rjson=%s&postid=%d&title=%s&original_title=%s&s=%d&t=%d",
			host,
			getsTVBool(rjson),
			postID,
			encTitle,
			encOriginal,
			s,
			i,
		)
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   voice.name,
			"active": i == t,
			"url":    link,
		})
	}

	episodes := voices[t].season.Episodes
	episodeKeys := make([]string, 0, len(episodes))
	for key := range episodes {
		episodeKeys = append(episodeKeys, key)
	}
	sort.Slice(episodeKeys, func(i, j int) bool {
		ai := fxEpisodeOrder(episodes[episodeKeys[i]], episodeKeys[i], i)
		bi := fxEpisodeOrder(episodes[episodeKeys[j]], episodeKeys[j], j)
		if ai == bi {
			return episodeKeys[i] < episodeKeys[j]
		}
		return ai < bi
	})

	baseTitle := getsTVJoinName(title, originalTitle)
	episodeData := make([]map[string]any, 0, len(episodeKeys))
	episodeLabels := make([]string, 0, len(episodeKeys))
	seasonsNum := make([]int, 0, len(episodeKeys))
	episodesNum := make([]int, 0, len(episodeKeys))

	for _, key := range episodeKeys {
		item := episodes[key]
		streams := fxStreams(item.Files)
		if len(streams) == 0 {
			continue
		}
		progOK := false
		if prog, ok := f.progressiveStreams(req.Context(), streams); ok {
			streams = prog // прямой mp4, мимо сервера (см. writeFXMovie)
			progOK = true
		} else {
			for i := range streams {
				streams[i]["url"] = streamProxyURL(req, streams[i]["url"], "filmix", links)
			}
		}

		episodeNum := fxEpisodeOrder(item, key, len(episodeData)+1)
		label := fmt.Sprintf("%d серия", episodeNum)

		row := map[string]any{
			"method":        "play",
			"url":           streams[0]["url"],
			"stream":        streams[0]["url"],
			"s":             s,
			"e":             episodeNum,
			"name":          label,
			"title":         fmt.Sprintf("%s (%s)", baseTitle, label),
			"streamquality": streams,
		}
		if f.fxDirectLampa && !progOK {
			row["fxdirect"] = f.filmixRowFxDirect(postID, voices[t].name, s, episodeNum)
		}
		if len(streams) > 1 {
			qualMap := filmixBuildQualityMap(streams)
			row["quality"] = qualMap
			row["qualitys"] = qualMap
		}

		episodeData = append(episodeData, row)
		episodeLabels = append(episodeLabels, label)
		seasonsNum = append(seasonsNum, s)
		episodesNum = append(episodesNum, episodeNum)
	}

	if len(episodeData) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		payload := map[string]any{
			"type": "episode",
			"data": episodeData,
		}
		if len(voiceRows) > 0 {
			payload["voice"] = voiceRows
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	var sb strings.Builder
	if len(voiceRows) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, row := range voiceRows {
			getsTVAppendVoiceHTML(&sb, row)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range episodeData {
		getsTVAppendMovieHTML(&sb, row, episodeLabels[i], i == 0, seasonsNum[i], episodesNum[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// fxEpisodeOrder gets the episode number from the payload or the "eN" key.
func fxEpisodeOrder(item fxEpisode, key string, fallback int) int {
	if n := item.Episode.Int(); n > 0 {
		return n
	}
	return filmixEpisodeOrder(strings.TrimPrefix(strings.TrimSpace(key), "e"), fallback)
}
