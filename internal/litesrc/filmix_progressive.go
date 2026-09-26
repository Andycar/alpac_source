package litesrc

// Filmix прогрессивный MP4 — один файл вместо HLS.
//
// Долгое время считалось, что `/s/`-эндпоинт «платно гейтед» и прогрессив невозможен: он отдавал
// 10 871 558-байтную заглушку «купите премиум». Разгадка (2026-08-05): гейт не по качеству и не по
// тарифу, а по СПОСОБУ АВТОРИЗАЦИИ. hash из легаси `/api/v2/post` (device-токен) прав не несёт, а
// hash из api-fx ПОСЛЕ `/api-fx/auth` (логин+пароль → Bearer) — несёт, и тогда тот же путь отдаёт
// настоящие файлы: 4.9ГБ / 15.6ГБ, HDR10+ 5.6ГБ, 206, video/mp4.
//
// Почему это лучше HLS для нас (проверено начисто 2026-08-06 с постороннего IP):
//   - Медиа идёт МИМО сервера. Подпись не привязана к IP — CDN отдаёт файл тому, кто обратился
//     ПЕРВЫМ, поэтому единственное условие: сервер не должен трогать отданную ссылку (ни probe,
//     ни прогрева). Уходит и буферизация, и упор в полосу SOCKS-эксита.
//   - Клиентам не нужно уметь ничего: обычный mp4 играют все, включая ваниль-Lampa/Tizen/webOS.
//   - HDR тривиален: нет манифеста → нет hashless `EXT-X-MAP`, который мы чинили в четырёх местах.
//   - Перемотка — обычным Range.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// filmixHLSToProgRe разбирает api-fx HLS-ссылку:
//
//	https://host/hls/<dir>/<file>.mp4/index.m3u8?hash=H  →  https://host/s/H/<dir>/<file>.mp4
var filmixHLSToProgRe = regexp.MustCompile(`^(https?://[^/]+)/hls/(.+?)/index\.m3u8\?hash=(.+)$`)

// Размеры-маркеры «прав нет». CDN отвечает 206 и `video/mp4`, поэтому по статусу заглушку не
// отличить — только по полному размеру из Content-Range.
const (
	filmixStubPremium = 10871558  // «купите премиум» (всё выше 720p без прав)
	filmixStub720Ad   = 729935706 // 56-минутный рекламный ролик вместо 720p
)

// filmixNoHashDirRe — разделы CDN, чей HLS-манифест не содержит `?hash=` в сегментах (2026-08-22:
// UHD_1313 — 940 сегментов, hash ни у одного). Плеер идёт по манифесту буквально и ловит 403.
//
// ★Такие рипы отдаём ПРОГРЕССИВОМ, а не через прокси: у прогрессива манифеста нет вовсе, значит
// нечему терять подпись, и зритель качает файл прямо с CDN — трафик мимо сервера. Проверено на
// «Supergirl» (UHD_1313, 2160p): первое обращение с постороннего IP → 206, video/mp4,
// 21 997 176 115 Б. Список синхронизирован с httpapi/stream_proxy.go.
var filmixNoHashDirRe = regexp.MustCompile(`(?i)/UHD_1313/`)

// filmixToProgressive конвертирует HLS-ссылку в прогрессивную. "" — если форма не та.
func filmixToProgressive(hlsURL string) string {
	m := filmixHLSToProgRe.FindStringSubmatch(strings.TrimSpace(hlsURL))
	if len(m) != 4 {
		return ""
	}
	return fmt.Sprintf("%s/s/%s/%s", m[1], m[3], m[2])
}

// filmixIsStubSize сообщает, что CDN подсунул заглушку вместо фильма.
func filmixIsStubSize(total int64) bool {
	return total == filmixStubPremium || total == filmixStub720Ad
}

// ★★★probeProgressive УДАЛЁН (2026-08-06). Он тянул один байт по ТОЙ ЖЕ ссылке, которую мы затем
// отдавали зрителю, — и этим забирал её себе: CDN отдаёт подписанный файл тому, кто обратился
// ПЕРВЫМ, а остальным отвечает 403/429. Проверено начисто: ссылка, которой сервер не касался,
// открывается с постороннего IP (прогрессив 206, 5 585 896 082 Б, video/mp4; HLS-манифест 200,
// сегмент 206). Та же ошибка в другом обличье — manifest_only: там манифест качал сервер.
//
// Права теперь подтверждаются профилем аккаунта (`/api-fx/me`, см. fxVerifyAlive), а не пробой по
// медиа-ссылке: заглушка `10871558` появляется только у сессии без прав, а её живость мы и так
// проверяем. filmixIsStubSize остаётся — им ловится заглушка, если она всё же доедет.

// progressiveEnabled — включён ли режим. Требует кредов: анонимный/токенный hash прав не несёт и
// прогрессив выродится в заглушку. Управляется `[online.filmix] progressive`.
func (f *filmixChecker) progressiveEnabled() bool {
	if f.fxUser == "" || f.fxPasswd == "" {
		return false
	}
	if f.fxProgressive == nil {
		return false
	}
	return *f.fxProgressive
}

// progressiveStreams превращает HLS-потоки в прогрессивные (один mp4 на качество).
//
// ★Сервер по этим ссылкам НЕ ходит — ни probe, ни прогрева. Первым к ним обращается зритель, и
// именно за ним CDN закрепляет выдачу.
//
// Для fMP4 (HDR/HEVC) это ЕДИНСТВЕННЫЙ способ отдать медиа напрямую: их HLS-манифест несёт
// EXT-X-MAP без hash, а починить его может только тот, кто манифест скачал, — то есть сервер,
// который тем самым отбирает ссылку у зрителя. У прогрессива манифеста нет вовсе.
//
// Для TS-рипов прямой HLS тоже работает, но прогрессив и им полезен: один файл вместо сотен
// сегментов, перемотка обычным Range, нет per-segment TTFB. Так же поступает и чужой плагин,
// который отдаёт `/s/` во всех озвучках.
func (f *filmixChecker) progressiveStreams(ctx context.Context, streams []map[string]string) ([]map[string]string, bool) {
	if len(streams) == 0 {
		return streams, false
	}
	// Прогрессив включается либо глобально, либо принудительно для дефектных разделов — им
	// HLS-путь всё равно закрыт (сегменты без hash), а прокси съел бы весь 4K-трафик.
	if !f.progressiveEnabled() {
		if f.fxUser == "" || f.fxPasswd == "" || !filmixNoHashDirRe.MatchString(streams[0]["url"]) {
			return streams, false
		}
	}
	out := make([]map[string]string, 0, len(streams))
	for _, s := range streams {
		p := filmixToProgressive(s["url"])
		if p == "" {
			return streams, false // не та форма — не выдумываем ссылку
		}
		out = append(out, map[string]string{"quality": s["quality"], "url": p})
	}
	return out, true
}
