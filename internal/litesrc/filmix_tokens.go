package litesrc

import (
	"context"
	"crypto/md5"
	stdjson "encoding/json"
	"flag"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// Здоровье легаси-токенов Filmix.
//
// 22.09.2026: нода FI полсуток отдавала «купите премиум» вместо кино — её токен умер (профиль
// по нему пуст), а `pro = true` в конфиге продолжал обещать права: строки HEVC/HDR не прятались,
// 1080p+ не резались, и CDN по бесправной подписи отдавал 11 МБ заглушки (720p/480p при этом
// настоящие: проверено на «Моя няня — супергерой», 1440p = 11 МБ по мёртвому токену и 4,6 ГБ
// по живому). Никакой лог этого не видел — токен «терялся» молча.
//
// Отсюда: правам верим не конфигу, а профилю (/api/v2/user_profile → user_data.is_pro /
// is_pro_plus / pro_date). Проверяем при старте и каждые filmixTokenProbeEvery; мёртвые токены
// не выбираем; без единого живого PRO режем выдачу как без прав (≤720p, HEVC/HDR спрятаны) и
// пишем в лог ошибкой. Срез уезжает в /api/cluster/vitals → «Мощности» на main.

var (
	filmixTokenProbeEvery = 30 * time.Minute
	filmixTokenProbeDelay = 3 * time.Second
	// filmixTokenExpiryWarn — за сколько до конца подписки предупреждать.
	filmixTokenExpiryWarn = 7 * 24 * time.Hour
)

type filmixTokenState struct {
	Checked  time.Time
	Alive    bool      // профиль отдал user_data с логином
	Pro      bool      // is_pro || is_pro_plus
	ProUntil time.Time // pro_date; нулевое — неизвестно
	Account  string    // md5(login)[:6] — различать учётки, не раскрывая логин
}

// FilmixTokenStatus — срез одного токена для vitals и админки. Сам токен наружу не выходит.
type FilmixTokenStatus struct {
	Token    string `json:"token"` // md5(token)[:8]
	Account  string `json:"account,omitempty"`
	Alive    bool   `json:"alive"`
	Pro      bool   `json:"pro"`
	ProUntil string `json:"pro_until,omitempty"` // ГГГГ-ММ-ДД
	Checked  string `json:"checked,omitempty"`   // RFC3339
}

var filmixTokenSnapshot atomic.Value // []FilmixTokenStatus

// Состояние — на уровне пакета: чекеров в процессе два (lite и capi) с одним и тем же списком
// токенов, а проба нужна одна, и права должны сходиться у обоих.
var (
	filmixTokenHealthMu  sync.Mutex
	filmixTokenHealth    = map[string]filmixTokenState{}
	filmixTokenProbeOnce sync.Once
)

// FilmixTokens — последний срез здоровья токенов этого процесса (nil, пока не проверяли).
func FilmixTokens() []FilmixTokenStatus {
	v, _ := filmixTokenSnapshot.Load().([]FilmixTokenStatus)
	return v
}

func filmixTokenID(token string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(strings.TrimSpace(token))))[:8]
}

// filmixFlexBool — filmix отдаёт флаги то bool, то 0/1, то "1".
type filmixFlexBool bool

func (b *filmixFlexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	switch strings.ToLower(s) {
	case "true", "1", "yes":
		*b = true
	default:
		*b = false
	}
	return nil
}

// tokenList — токены, между которыми выбирает pickToken (конфиг уже склеил token + tokens).
func (f *filmixChecker) tokenList() []string {
	if len(f.tokens) > 0 {
		return f.tokens
	}
	if f.token != "" {
		return []string{f.token}
	}
	return nil
}

// probeList — всё, что проверяем: обычные токены и запасные (учётка запасного должна быть известна,
// чтобы перевыпуск на 429 брал именно ДРУГУЮ учётку).
func (f *filmixChecker) probeList() []string {
	base := f.tokenList()
	if len(f.reserveTokens) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(f.reserveTokens))
	out = append(out, base...)
	return append(out, f.reserveTokens...)
}

// probeToken — профиль по одному токену. Сетевой сбой/мёртвое зеркало — не вердикт: ok=false,
// прежнее состояние остаётся.
func (f *filmixChecker) probeToken(ctx context.Context, token string) (filmixTokenState, bool) {
	body, ok := f.apiGet(ctx, "/api/v2/user_profile", f.makeAuthQuery(token), 1<<20)
	if !ok || strings.TrimSpace(body) == "" {
		return filmixTokenState{}, false
	}
	var prof struct {
		UserData *struct {
			Login     string         `json:"login"`
			IsPro     filmixFlexBool `json:"is_pro"`
			IsProPlus filmixFlexBool `json:"is_pro_plus"`
			ProDate   string         `json:"pro_date"`
		} `json:"user_data"`
	}
	if err := stdjson.Unmarshal([]byte(body), &prof); err != nil {
		return filmixTokenState{}, false // не JSON — зеркало отдало мусор, не вердикт
	}
	st := filmixTokenState{Checked: time.Now()}
	if prof.UserData == nil || strings.TrimSpace(prof.UserData.Login) == "" {
		return st, true // осмысленный ответ без профиля = токен мёртв
	}
	st.Alive = true
	st.Pro = bool(prof.UserData.IsPro) || bool(prof.UserData.IsProPlus)
	st.Account = fmt.Sprintf("%x", md5.Sum([]byte(prof.UserData.Login)))[:6]
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(prof.UserData.ProDate)); err == nil {
		st.ProUntil = t
		// pro_date — последний день подписки; после него прав нет.
		if st.Pro && time.Now().After(t.Add(24*time.Hour)) {
			st.Pro = false
		}
	}
	return st, true
}

// checkTokens — обойти все токены, обновить состояние, залогировать переходы, опубликовать срез.
func (f *filmixChecker) checkTokens(ctx context.Context) {
	tokens := f.probeList()
	if len(tokens) == 0 {
		return
	}
	for _, tok := range tokens {
		st, ok := f.probeToken(ctx, tok)
		if !ok {
			continue
		}
		filmixTokenHealthMu.Lock()
		prev, known := filmixTokenHealth[tok]
		filmixTokenHealth[tok] = st
		filmixTokenHealthMu.Unlock()

		id := filmixTokenID(tok)
		until := ""
		if !st.ProUntil.IsZero() {
			until = st.ProUntil.Format("2006-01-02")
		}
		switch {
		case !st.Alive && (!known || prev.Alive):
			log.Error().Str("token", id).
				Msg("filmix: токен МЁРТВ — профиль по нему не отдаётся; >720p с него — заглушка «купите премиум», режу до 720p и прячу HEVC/HDR. Замени token в [online.filmix]")
		case st.Alive && !st.Pro && (!known || prev.Pro || !prev.Alive):
			log.Error().Str("token", id).Str("account", st.Account).Str("pro_until", until).
				Msg("filmix: токен жив, но без PRO — 4K/HEVC с него заглушка; режу до 720p")
		case st.Alive && st.Pro && known && (!prev.Alive || !prev.Pro):
			log.Info().Str("token", id).Str("account", st.Account).Str("pro_until", until).
				Msg("filmix: токен снова с правами")
		case !known:
			log.Info().Str("token", id).Str("account", st.Account).Bool("pro", st.Pro).Str("pro_until", until).
				Msg("filmix: токен проверен")
		}
		if st.Alive && st.Pro && !st.ProUntil.IsZero() {
			if left := time.Until(st.ProUntil.Add(24 * time.Hour)); left < filmixTokenExpiryWarn {
				log.Warn().Str("token", id).Str("account", st.Account).Str("pro_until", until).
					Int("days_left", int(left.Hours()/24)).
					Msg("filmix: подписка учётки скоро кончится — продли, иначе 4K превратится в заглушку")
			}
		}
	}
	f.publishTokenSnapshot()
}

func (f *filmixChecker) publishTokenSnapshot() {
	filmixTokenHealthMu.Lock()
	defer filmixTokenHealthMu.Unlock()
	out := make([]FilmixTokenStatus, 0, len(f.probeList()))
	for _, tok := range f.probeList() {
		st, ok := filmixTokenHealth[tok]
		if !ok {
			continue
		}
		s := FilmixTokenStatus{Token: filmixTokenID(tok), Account: st.Account, Alive: st.Alive, Pro: st.Pro,
			Checked: st.Checked.UTC().Format(time.RFC3339)}
		if !st.ProUntil.IsZero() {
			s.ProUntil = st.ProUntil.Format("2006-01-02")
		}
		out = append(out, s)
	}
	filmixTokenSnapshot.Store(out)
}

// tokenUsable — можно ли выдавать по токену полное качество. Непроверенный (в том числе чужой
// kit-токен) считается пригодным: без данных не ломаем то, что работало.
func (f *filmixChecker) tokenUsable(token string) bool {
	filmixTokenHealthMu.Lock()
	defer filmixTokenHealthMu.Unlock()
	st, ok := filmixTokenHealth[token]
	if !ok {
		return true
	}
	return st.Alive && st.Pro
}

// legacyRights — есть ли у легаси-контура права подписки: `pro` из конфига И хотя бы один
// токен, который проверка не признала мёртвым/бесправным.
func (f *filmixChecker) legacyRights() bool {
	if !f.pro {
		return false
	}
	tokens := f.tokenList()
	if len(tokens) == 0 {
		return true // прав не с чем сверять — как раньше, верим конфигу
	}
	for _, tok := range tokens {
		if f.tokenUsable(tok) {
			return true
		}
	}
	return false
}

// startTokenProbe — фоновая проверка; одна на процесс, заводит первый созданный чекер.
// Под `go test` (флаг test.v зарегистрирован) не стартует: тестовые зеркала её не ждут.
func (f *filmixChecker) startTokenProbe() {
	filmixTokenProbeOnce.Do(func() {
		if len(f.probeList()) == 0 || flag.Lookup("test.v") != nil {
			return
		}
		go func() {
			time.Sleep(filmixTokenProbeDelay)
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
				f.checkTokens(ctx)
				cancel()
				time.Sleep(filmixTokenProbeEvery)
			}
		}()
	})
}
