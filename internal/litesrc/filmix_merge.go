package litesrc

// Добор озвучек из легаси-API.
//
// Источники не дублируют, а ДОПОЛНЯЮТ друг друга — замерено 2026-08-06 на «Обсессии» (postid 183311):
//
//	api-fx (авторизованный) → 5 озвучек, HEVC-рипа среди них НЕТ;
//	легаси /api/v2/post/183311 → «Заблокировано правообладателем!» + «HEVC 4K AC3 2CH DUB RU UKR».
//
// То есть api-fx даёт права, 4K и HDR10+, но не знает про часть рипов, а легаси знает про них, хотя
// по правам почти всё закрыто. Чужой плагин показывает шесть озвучек именно потому, что объединяет
// оба списка; у нас api-fx полностью вытеснял легаси, и HEVC терялся.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// filmixLegacySRe разбирает легаси-ссылку `https://host/s/<hash>/<path>` на хост и путь.
var filmixLegacySRe = regexp.MustCompile(`^(https?://[^/]+)/s/[^/]+/(.+)$`)

// filmixQualityListRe — шаблон качеств в имени файла: `..._[2160,,,,,].mp4`.
var filmixQualityListRe = regexp.MustCompile(`_\[([0-9,]*)\]\.mp4`)

// filmixFXHashRe достаёт подпись из api-fx ссылки (`…/index.m3u8?hash=H`).
var filmixFXHashRe = regexp.MustCompile(`[?&]hash=(.+)$`)

// filmixSignExtra строит прогрессивные потоки для легаси-озвучки, подписывая их АВТОРИЗОВАННЫМ
// hash из api-fx.
//
// ★Без подмены подписи это бессмысленно: собственный hash легаси-выдачи прав не несёт, и CDN
// отдаёт по нему заглушку. Замер 2026-08-06 на HEVC-рипе «Обсессии», один и тот же путь:
//
//	легаси-hash → 206, но 10 871 558 Б («купите премиум»);
//	наш api-fx hash → 206, 9 744 877 207 Б — настоящий 9.7ГБ файл.
//
// Именно так и выглядит выдача чужого плагина: все озвучки, включая HEVC, подписаны одним hash.
func filmixSignExtra(legacyLink, fxHash string) []map[string]string {
	m := filmixLegacySRe.FindStringSubmatch(strings.TrimSpace(legacyLink))
	if len(m) != 3 || strings.TrimSpace(fxHash) == "" {
		return nil
	}
	host, path := m[1], m[2]

	qm := filmixQualityListRe.FindStringSubmatch(path)
	if len(qm) != 2 {
		return nil // без шаблона качеств не угадываем имя файла
	}
	qualities := make([]int, 0, 4)
	for _, part := range strings.Split(qm[1], ",") {
		if q, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && q > 0 {
			qualities = append(qualities, q)
		}
	}
	if len(qualities) == 0 {
		return nil
	}
	sort.Sort(sort.Reverse(sort.IntSlice(qualities)))

	out := make([]map[string]string, 0, len(qualities))
	for _, q := range qualities {
		file := filmixQualityListRe.ReplaceAllString(path, fmt.Sprintf("_%d.mp4", q))
		out = append(out, map[string]string{
			"quality": fmt.Sprintf("%dp", q),
			"url":     fmt.Sprintf("%s/s/%s/%s", host, fxHash, file),
		})
	}
	return out
}

// filmixFXHash достаёт подпись из любой api-fx ссылки набора.
func filmixFXHash(movies []fxMovie) string {
	for _, m := range movies {
		for _, f := range m.Files {
			if hm := filmixFXHashRe.FindStringSubmatch(strings.TrimSpace(f.URL)); len(hm) == 2 {
				return hm[1]
			}
		}
	}
	return ""
}

// filmixLegacyExtraTTL — легаси-ответ меняется редко, а запрос уходит на КАЖДОГО зрителя карточки,
// поэтому кэшируем (у api-fx-ветки такой кэш уже есть — fxLinks).
const filmixLegacyExtraTTL = 5 * time.Minute

type filmixExtraEntry struct {
	movies []filmixMovie
	at     time.Time
}

var (
	filmixExtraMu    sync.Mutex
	filmixExtraCache = map[int]filmixExtraEntry{}
)

// filmixIsBlockedName распознаёт служебную запись легаси-выдачи: это не озвучка, а сообщение
// («Заблокировано правообладателем!»), и показывать её как вариант нельзя.
func filmixIsBlockedName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "" || strings.HasPrefix(n, "заблокировано ") || strings.HasPrefix(n, "видео заблокировано")
}

// filmixExtraMovies возвращает легаси-озвучки, которых НЕТ в api-fx-ответе. Сравнение по имени:
// именно оно совпадает между источниками (у одного и того же рипа один и тот же ярлык озвучки).
func filmixExtraMovies(fx []fxMovie, legacy []filmixMovie) []filmixMovie {
	if len(legacy) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(fx))
	for _, m := range fx {
		if n := strings.ToLower(strings.TrimSpace(m.Voiceover)); n != "" {
			seen[n] = struct{}{}
		}
	}
	out := make([]filmixMovie, 0, len(legacy))
	for _, m := range legacy {
		name := strings.TrimSpace(m.Translation)
		if filmixIsBlockedName(name) {
			continue
		}
		if _, dup := seen[strings.ToLower(name)]; dup {
			continue
		}
		out = append(out, m)
	}
	return out
}

// fxLegacyExtra тянет легаси-выдачу и оставляет только то, чего нет в api-fx. Сетевой сбой легаси —
// не повод ронять основной ответ: возвращаем nil и показываем то, что дал api-fx.
func (f *filmixChecker) fxLegacyExtra(ctx context.Context, postID int, fx []fxMovie) []filmixMovie {
	filmixExtraMu.Lock()
	entry, hit := filmixExtraCache[postID]
	filmixExtraMu.Unlock()

	if !hit || time.Since(entry.at) > filmixLegacyExtraTTL {
		root, ok := f.post(ctx, postID, f.pickToken())
		var movies []filmixMovie
		if ok && root.PlayerLinks != nil {
			movies = root.PlayerLinks.Movie
		}
		entry = filmixExtraEntry{movies: movies, at: time.Now()}
		filmixExtraMu.Lock()
		filmixExtraCache[postID] = entry
		filmixExtraMu.Unlock()
	}

	return filmixExtraMovies(fx, entry.movies)
}

// filmixHEVCNameRe — рипы, которые ЛЕГАСИ отдаёт с бесправной подписью. Проверено 2026-08-06:
// один и тот же путь под легаси-подписью → 206, но 10 871 558 Б («купите премиум»), а под
// авторизованной api-fx → 9 744 877 207 Б. То есть без прав такая строка заведомо неиграбельна.
var filmixHEVCNameRe = regexp.MustCompile(`(?i)(hevc|hdr(10)?[+p]?|dolby|dovi|dvp[0-9])([^a-z]|$)`)

// fxRightsConfirmed — есть ли у нас сейчас подтверждённые права аккаунта. Пока api-fx недоступен
// (2026-08-06 он лежал: TLS проходит, HTTP-ответа нет), access протухает, и всё, что требует
// подписки, вырождается в заглушку.
func (f *filmixChecker) fxRightsConfirmed() bool {
	if f.fxUser == "" || f.fxPasswd == "" {
		return false
	}
	f.fxMu.Lock()
	defer f.fxMu.Unlock()
	f.fxStateLoad()
	return f.fxAccess != "" && f.fxAccessHash == f.fxHash && !f.fxHashDead
}

// filmixHideUnplayable сообщает, что озвучку показывать не надо: она требует подписки, а прав у нас
// сейчас нет — зритель получит «купите премиум» вместо фильма.
func (f *filmixChecker) filmixHideUnplayable(name, link string) bool {
	if f.fxRightsConfirmed() {
		return false
	}
	return filmixHEVCNameRe.MatchString(name) || filmixHEVCNameRe.MatchString(link)
}
