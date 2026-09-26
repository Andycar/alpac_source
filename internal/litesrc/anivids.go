package litesrc

// anivids — общий слой для площадок, которые крутят видео через плеер
// «Best player» (player.ladonyvesna2005.info) с раздачей на cdnN.anivids.link.
//
// На нём сидят как минимум RuDub (rudub.go) и AniDub (anidub.go) — у обеих
// студий свой каталог, но одна и та же механика видео:
//
//	<iframe src="{player}/index.php?v=/{shard}/{hash}">   — страница серии
//	GET {player}/index.php?v=/{shard}/{hash}&playlist     — ВСЕ серии сезона
//	GET {player}/vid.php?v=/{shard}/{hash}                — master m3u8
//
// Две особенности, выясненные на живых ссылках (см. rudub.go):
//
//   - CDN отвечает 403 на запрос без User-Agent — именно на него, а не на
//     Referer: с любым UA плейлист отдаётся даже при чужом Referer.
//   - RESOLUTION в master-плейлисте занижен: у варианта fhd.mp4 написано
//     1280x720, а сегмент по факту 1920x1080. Поэтому метка качества берётся
//     из имени релиза в плеере (event-filename), а не из манифеста.

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

const (
	anividsDefaultPlayer = "https://player.ladonyvesna2005.info"

	anividsUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

var (
	// Плейлист сезона внутри плеера.
	anividsPlaylistSpanRe = regexp.MustCompile(`<span[^>]+data="([^"]+)"[^>]*>\s*(\d+)\s*сери`)
	anividsFilenameRe     = regexp.MustCompile(`event-filename="([^"]*)"`)
	// Адрес плеера в iframe: только он даёт shard/hash.
	anividsPlayerSrcRe = regexp.MustCompile(`^(https?://[\w.-]+(?::\d+)?)/index\.php\?v=/([^"&]+)`)
	// Варианты в master-плейлисте: .../fhd.mp4/chunk.m3u8, .../sd.mp4/chunk.m3u8
	anividsVariantRe = regexp.MustCompile(`(?m)^(https?://[^\s]+)$`)
	// «Vigil.s03e06.HD1080p.WEBRip.Rus.RuDub.tv.mkv» — метка качества релиза.
	anividsReleaseQualityRe = regexp.MustCompile(`(?i)(?:HD)?(2160|1080|720|480)[pр]`)
	// Разрешённая форма идентификатора видео: «s10/82ed92…».
	anividsHashRe = regexp.MustCompile(`^s\d{1,4}/[A-Za-z0-9]{8,64}$`)
)

type anividsEpisode struct {
	num  int
	hash string // «s10/82ed926165ca970566b1515f5231079a»
}

type anividsVariant struct {
	label string
	url   string
}

// anividsStreamHeaders — заголовки, с которыми CDN отдаёт поток. Обязателен
// User-Agent; Referer/Origin площадка не проверяет — держим их как дешёвую
// страховку на случай, если проверку включат.
func anividsStreamHeaders(player string) map[string]string {
	player = strings.TrimRight(strings.TrimSpace(player), "/")
	return map[string]string{
		"User-Agent": anividsUA,
		"Referer":    player + "/",
		"Origin":     player,
	}
}

// anividsMasterURL — адрес master-плейлиста серии.
func anividsMasterURL(player, hash string) string {
	return strings.TrimRight(player, "/") + "/vid.php?v=/" + hash
}

// anividsPlaylistURL — страница плеера со ВСЕМИ сериями сезона.
func anividsPlaylistURL(player, hash string) string {
	return strings.TrimRight(player, "/") + "/index.php?v=/" + hash + "&playlist"
}

// anividsParseIframe достаёт из страницы серии адрес плеера и идентификатор
// видео. Заодно отсеивает чужие плееры: у обеих площадок часть старых сезонов
// висит на умерших cdnmovies.nl / vio.to / hdgo.cc, играть по ним нечего.
func anividsParseIframe(html string) (player, hash string, ok bool) {
	for _, m := range anividsIframeRe.FindAllStringSubmatch(html, -1) {
		src := strings.TrimSpace(unescapeHTMLAmp(m[1]))
		if strings.HasPrefix(src, "//") {
			src = "https:" + src
		}
		pm := anividsPlayerSrcRe.FindStringSubmatch(src)
		if len(pm) < 3 {
			continue
		}
		h := strings.TrimPrefix(pm[2], "/")
		if !anividsHashRe.MatchString(h) {
			continue
		}
		return strings.TrimRight(pm[1], "/"), h, true
	}
	return "", "", false
}

var anividsIframeRe = regexp.MustCompile(`<iframe[^>]+src="([^"]+)"`)

// unescapeHTMLAmp разворачивает &amp; в ссылках: AniDub печатает iframe уже
// экранированным, и без этого «?v=/s10/hash&amp;playlist» не разбирается.
func unescapeHTMLAmp(v string) string {
	return strings.ReplaceAll(v, "&amp;", "&")
}

// anividsParsePlaylist разбирает список серий сезона со страницы плеера.
func anividsParsePlaylist(html string) []anividsEpisode {
	matches := anividsPlaylistSpanRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]anividsEpisode, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		hash := strings.TrimSpace(strings.TrimPrefix(m[1], "/"))
		if !anividsHashRe.MatchString(hash) {
			continue
		}
		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}
		num, _ := strconv.Atoi(m[2])
		if num <= 0 {
			continue
		}
		out = append(out, anividsEpisode{num: num, hash: hash})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].num < out[j].num })
	return out
}

// anividsQualityFromRelease достаёт метку качества из имени файла релиза:
// «Vigil.s03e06.HD1080p.WEBRip.Rus.RuDub.tv.mkv» → «1080p».
func anividsQualityFromRelease(filename string) string {
	m := anividsReleaseQualityRe.FindStringSubmatch(filename)
	if len(m) < 2 {
		return ""
	}
	return m[1] + "p"
}

func anividsQualityLabel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "2160p", "2160":
		return "2160p"
	case "1080p", "1080":
		return "1080p"
	case "720p", "720":
		return "720p"
	case "480p", "480":
		return "480p"
	}
	return ""
}

// anividsParseVariants разбирает master-плейлист. Метки берутся из имени
// варианта, а верхняя — из качества релиза, когда оно известно: RESOLUTION в
// манифесте занижен и ему верить нельзя.
func anividsParseVariants(body, releaseQuality string) []anividsVariant {
	urls := anividsVariantRe.FindAllString(body, -1)
	if len(urls) == 0 {
		return nil
	}
	top := releaseQuality
	if top == "" {
		top = "1080p"
	}

	out := make([]anividsVariant, 0, len(urls))
	seen := make(map[string]struct{}, len(urls))
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if !strings.HasPrefix(u, "http") {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}

		label := "auto"
		switch {
		case strings.Contains(u, "/fhd.mp4/"):
			label = top
		case strings.Contains(u, "/hd.mp4/"):
			label = "720p"
		case strings.Contains(u, "/sd.mp4/"):
			label = "480p"
		}
		out = append(out, anividsVariant{label: label, url: u})
	}

	// По убыванию качества — клиент берёт первый элемент как основной.
	sort.SliceStable(out, func(i, j int) bool {
		return anividsQualityRank(out[i].label) > anividsQualityRank(out[j].label)
	})
	return out
}

func anividsQualityRank(label string) int {
	switch label {
	case "2160p":
		return 4
	case "1080p":
		return 3
	case "720p":
		return 2
	case "480p":
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// Общий обработчик воспроизведения
// ---------------------------------------------------------------------------

// anividsPlayOptions — всё, что отличает одну площадку от другой в ответе play.
type anividsPlayOptions struct {
	plugin  string // ключ балансера: «rudub», «anidub»
	voice   string // имя озвучки в списке: «RuDub», «AniDUB»
	player  string // хост плеера, узнанный из iframe
	hash    string // «s10/82ed92…»
	quality string // подсказка качества из имени релиза
	title   string // название карточки — НЕ студии (см. ниже)
	fetch   func(ctx context.Context, target, referer string) (string, bool)
}

// anividsWritePlay минтит master-плейлист серии и пишет ответ плеера.
//
// Отдельная функция, потому что у обеих площадок это один и тот же код: минт,
// разбор вариантов, /proxy с заголовками. Единственная тонкость — поле title:
// туда идёт название карточки, а не студии. capi сверяет его с тем, что
// просил клиент, и выбрасывает источник, у которого там что-то своё (имя
// «RuDub» в этом поле однажды стоило источнику всей выдачи).
func anividsWritePlay(w http.ResponseWriter, req *http.Request, links *proxylink.Manager, o anividsPlayOptions) {
	// Хэш приходит с нашей же страницы серий — проверяем форму, чтобы ?h= не
	// стал дырой в произвольный путь на плеерном хосте.
	if !anividsHashRe.MatchString(o.hash) || o.fetch == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	player := strings.TrimRight(strings.TrimSpace(o.player), "/")
	if player == "" {
		player = anividsDefaultPlayer
	}
	master := anividsMasterURL(player, o.hash)

	body, ok := o.fetch(req.Context(), master, player+"/")
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	variants := anividsParseVariants(body, anividsQualityLabel(o.quality))
	if len(variants) == 0 {
		log.Debug().Str("bal", o.plugin).Str("hash", o.hash).
			Msg("anivids: master playlist without variants")
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	headers := anividsStreamHeaders(player)
	streams := make([]map[string]any, 0, len(variants))
	qualityMap := make(map[string]string, len(variants))
	for _, v := range variants {
		u := streamProxyURLWithHeaders(req, v.url, o.plugin, links, headers)
		streams = append(streams, map[string]any{"quality": v.label, "url": u})
		qualityMap[v.label] = u
	}
	stream := streamProxyURLWithHeaders(req, master, o.plugin, links, headers)

	row := map[string]any{
		"method":        "play",
		"url":           stream,
		"stream":        stream,
		"name":          o.voice,
		"translate":     o.voice,
		"quality":       qualityMap,
		"streamquality": streams,
	}
	if title := strings.TrimSpace(o.title); title != "" && len(title) <= 200 {
		row["title"] = title
	}
	writeJSON(w, http.StatusOK, row)
}
