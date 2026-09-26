package litesrc

// filmix_remint.go — перевыпуск ссылки Filmix ДРУГОЙ учёткой, когда CDN отвечает 429.
//
// CDN Filmix закрепляет подпись поста за первым IP, тронувшим её через эту учётку, и всем
// остальным адресам той же учётки отвечает 429 на ~10 часов ([[filmix-account-ip-binding]]).
// Липкая маршрутизация (пост → одна нода) это не лечит: хватает любой «чужой» попытки — возврата
// зрителя на main, первого уровня на другой ноде, переезда поста при падении ноды. 25.09.2026 ноды
// поймали ~2000 таких 429 за сутки, и возврат на main не помогал: main для подписи тоже чужой.
//
// Подписи разных учёток независимы: та же серия, подписанная другой учёткой, с этого же адреса
// играет. Путь файла после /s/<подпись>/ от учётки не зависит («911.lostfilm.2018-nf20/
// s06e16_1080.mp4»), поэтому при выдаче ссылок запоминаем «путь → пост и токен», а на 429 прокси
// (proxylink.RefreshTarget) зовёт remint: пост запрашивается токеном другой учётки, и в свежем
// ответе находится файл с тем же путём. Другая учётка — из [online.filmix] reserve_tokens или
// обычных токенов (у main их две).

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"
)

var (
	filmixMintTTL      = 48 * time.Hour   // /proxy-ссылка живёт 36 ч — помним с запасом
	filmixMintMax      = 200_000          // запись — пара строк; потолок от разбухания
	filmixRemintOK     = 6 * time.Hour    // свежая подпись служит часами: API не зовём на каждый кусок mp4
	filmixRemintMiss   = 10 * time.Minute // не вышло — не долбим API каждым запросом зрителя
	filmixRemintBudget = 8 * time.Second
)

type filmixMint struct {
	postID int
	token  string // каким токеном подписана ссылка; учётка — из его проверки
	at     time.Time
}

type filmixRemintResult struct {
	fresh string // "" — перевыпустить не вышло
	at    time.Time
}

var (
	filmixMintMu   sync.Mutex
	filmixMints    = map[string]filmixMint{}         // путь файла → пост и токен
	filmixRemints  = map[string]filmixRemintResult{} // протухший адрес → свежий
	filmixRemintSF singleflight.Group
)

// filmixFilePath — путь файла после /s/<подпись>/ без query; "" — ссылка не того вида (HLS-синтез,
// чужой хост).
func filmixFilePath(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	i := strings.Index(u, "/s/")
	if i < 0 {
		return ""
	}
	rest := u[i+3:]
	j := strings.IndexByte(rest, '/')
	if j <= 0 || j == len(rest)-1 {
		return ""
	}
	return rest[j+1:]
}

// filmixRememberMint — запомнить выданные ссылки поста (обе половины «A or B» запасного хоста).
func filmixRememberMint(streams []map[string]string, postID int, token string) {
	if postID <= 0 || len(streams) == 0 {
		return
	}
	now := time.Now()
	filmixMintMu.Lock()
	defer filmixMintMu.Unlock()
	if len(filmixMints) >= filmixMintMax || len(filmixRemints) >= filmixMintMax {
		filmixSweepLocked(now)
	}
	for _, s := range streams {
		for _, part := range strings.Split(s["url"], " or ") {
			if p := filmixFilePath(part); p != "" {
				filmixMints[p] = filmixMint{postID: postID, token: token, at: now}
			}
		}
	}
}

func filmixSweepLocked(now time.Time) {
	for k, m := range filmixMints {
		if now.Sub(m.at) > filmixMintTTL {
			delete(filmixMints, k)
		}
	}
	for k, r := range filmixRemints {
		if now.Sub(r.at) > filmixRemintOK {
			delete(filmixRemints, k)
		}
	}
	// Всё ещё полно — выкидываем произвольную половину: забытая запись стоит лишь того, что её
	// 429 уйдёт прежним путём (возврат на primary).
	if len(filmixMints) >= filmixMintMax {
		n := 0
		for k := range filmixMints {
			delete(filmixMints, k)
			if n++; n >= filmixMintMax/2 {
				break
			}
		}
	}
	if len(filmixRemints) >= filmixMintMax {
		filmixRemints = map[string]filmixRemintResult{}
	}
}

// filmixTokenAccount — учётка токена по последней проверке профиля; "" — ещё не проверяли.
func filmixTokenAccount(token string) string {
	filmixTokenHealthMu.Lock()
	defer filmixTokenHealthMu.Unlock()
	return filmixTokenHealth[token].Account
}

// otherAccountToken — живой PRO-токен учётки, отличной от той, что подписала ссылку. Запасные —
// первыми: они для этого и заведены. Учётка ссылки неизвестна — честно «другую» не выбрать.
func (f *filmixChecker) otherAccountToken(mintToken string) string {
	acct := filmixTokenAccount(mintToken)
	if acct == "" {
		return ""
	}
	cands := make([]string, 0, len(f.reserveTokens)+len(f.tokens))
	cands = append(cands, f.reserveTokens...)
	cands = append(cands, f.tokens...)
	for _, tok := range cands {
		if tok == mintToken {
			continue
		}
		if a := filmixTokenAccount(tok); a == "" || a == acct {
			continue
		}
		if !f.tokenUsable(tok) {
			continue
		}
		return tok
	}
	return ""
}

// remint — proxylink.TargetRefresher для «filmix»: протухший адрес → тот же файл, подписанный
// другой учёткой. ok=false — ссылка не наша, другой учётки нет или в её ответе файла нет; тогда
// прокси ведёт себя как раньше (возврат на primary).
func (f *filmixChecker) remint(_ context.Context, stale string) (string, bool) {
	path := filmixFilePath(stale)
	if path == "" {
		return "", false
	}
	now := time.Now()
	filmixMintMu.Lock()
	if r, ok := filmixRemints[stale]; ok {
		ttl := filmixRemintOK
		if r.fresh == "" {
			ttl = filmixRemintMiss
		}
		if now.Sub(r.at) < ttl {
			filmixMintMu.Unlock()
			return r.fresh, r.fresh != ""
		}
	}
	m, known := filmixMints[path]
	filmixMintMu.Unlock()
	if !known {
		return "", false // выдана до рестарта или не нами — прежний путь
	}

	// Одно обращение к API на адрес: 429 по популярной серии приходят пачкой. Контекст свой, не
	// запроса зрителя: ушедший зритель не должен обрывать перевыпуск для остальных.
	v, _, _ := filmixRemintSF.Do(stale, func() (any, error) {
		return f.remintOnce(m, path), nil
	})
	fresh, _ := v.(string)
	filmixMintMu.Lock()
	filmixRemints[stale] = filmixRemintResult{fresh: fresh, at: time.Now()}
	filmixMintMu.Unlock()
	return fresh, fresh != ""
}

func (f *filmixChecker) remintOnce(m filmixMint, path string) string {
	from := filmixTokenAccount(m.token)
	tok := f.otherAccountToken(m.token)
	if tok == "" {
		log.Warn().Int("post", m.postID).Str("account", from).
			Msg("filmix: 429 — другой учётки на этом сервере нет (reserve_tokens), перевыпуск невозможен")
		return ""
	}
	to := filmixTokenAccount(tok)
	ctx, cancel := context.WithTimeout(context.Background(), filmixRemintBudget)
	defer cancel()
	root, ok := f.post(ctx, m.postID, tok)
	if !ok {
		log.Warn().Int("post", m.postID).Str("from", from).Str("to", to).
			Msg("filmix: 429 — пост другой учёткой не отдался, перевыпуск не вышел")
		return ""
	}
	fresh := f.findFilmixFile(root, path, tok)
	if fresh == "" {
		log.Warn().Int("post", m.postID).Str("from", from).Str("to", to).Str("file", path).
			Msg("filmix: 429 — в ответе другой учётки нет этого файла")
		return ""
	}
	log.Info().Int("post", m.postID).Str("from", from).Str("to", to).Str("file", path).
		Msg("filmix: 429 — ссылка перевыпущена другой учёткой")
	return fresh
}

// findFilmixFile — ссылка на файл с путём path в ответе поста, собранная теми же правилами, что и
// выдача (качество по правам токена, HLS-синтез), без запасного хоста.
func (f *filmixChecker) findFilmixFile(root filmixPostRoot, path, token string) string {
	if root.PlayerLinks == nil {
		return ""
	}
	match := func(streams []map[string]string) string {
		for _, s := range streams {
			for _, part := range strings.Split(s["url"], " or ") {
				if filmixFilePath(part) == path {
					return strings.TrimSpace(part)
				}
			}
		}
		return ""
	}
	for _, mv := range root.PlayerLinks.Movie {
		if u := match(f.buildMovieStreams(mv, token, nil)); u != "" {
			return u
		}
	}
	for _, voices := range root.PlayerLinks.Playlist {
		for _, raw := range voices {
			for _, item := range filmixDecodeEpisodeSet(raw) {
				if u := match(f.buildEpisodeStreams(item, token, nil)); u != "" {
					return u
				}
			}
		}
	}
	return ""
}
