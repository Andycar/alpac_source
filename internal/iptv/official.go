package iptv

import "strings"

// official.go — «первоисточник» против перепродавца.
//
// Источники канала неравноценны. Поток с домена САМОГО вещателя (или его CDN)
// не зависит ни от чьей подписки: он не исчезнет, когда у панели кончится
// оплата, не отвалится из-за лимита сессий и не сменит внутренние id каналов.
// Панель даёт больше каналов и выше битрейт, но она посредник — и падает
// целиком (прод: 3507 каналов из 3955 держались на одной панели, и когда та
// перестала отдавать сегменты, столько же и легло).
//
// Поэтому при прочих равных играем с первоисточника, а панель держим резервом.

// defaultOfficialHosts — подстроки хоста, по которым источник считается
// первоисточником. Домены вещателей и их вещательных CDN; панели и реселлеры
// сюда не попадают, потому что живут на собственных доменах и голых IP.
var defaultOfficialHosts = []string{
	"cdnvideo.ru",  // вещательный CDN: ВГТРК, МИР, Союз, региональные
	"smotrim.ru",   // ВГТРК
	"vgtrk",        // ВГТРК
	"1tv.ru",       // Первый канал
	"1internet.tv", // Первый канал (раздача)
	"ntv.ru",       // НТВ
	"matchtv.ru",   // Матч ТВ
	"mirtv",        // МИР
	"1tvcrimea.ru", // Первый Крымский
	"tvc.ru",       // ТВ Центр
	"otr-online.ru",
	"zvezda",
	"5-tv.ru",
	"spastv",
	"tvsoyuz",
	"rt.com",
	"russian.rt.com",
}

// officialHostMatcher хранит подготовленный список подстрок хоста.
type officialHostMatcher struct {
	hosts []string
}

func newOfficialMatcher(extra []string) *officialHostMatcher {
	hosts := make([]string, 0, len(defaultOfficialHosts)+len(extra))
	for _, h := range defaultOfficialHosts {
		hosts = append(hosts, strings.ToLower(h))
	}
	// Оператор дополняет список своими доменами (например, вещателем, которого
	// мы не знаем), а не заменяет: дефолт полезен всем.
	for _, h := range extra {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts = append(hosts, h)
		}
	}
	return &officialHostMatcher{hosts: hosts}
}

// isOfficial reports whether the URL points at a broadcaster's own host.
// Сравниваем ТОЛЬКО хост: подстрока вроде "1tv.ru" в пути чужой панели
// (…/playlists/1tv.ru.m3u8) первоисточником канал не делает.
func (m *officialHostMatcher) isOfficial(rawURL string) bool {
	if m == nil || rawURL == "" {
		return false
	}
	host := hostOf(rawURL)
	if host == "" {
		return false
	}
	for _, h := range m.hosts {
		if strings.Contains(host, h) {
			return true
		}
	}
	return false
}

// hostOf вытаскивает хост из URL без net/url: тот на битых строках возвращает
// ошибку, а нам достаточно куска между "//" и первым "/".
func hostOf(rawURL string) string {
	s := strings.ToLower(strings.TrimSpace(rawURL))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 { // user:pass@host
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, ":"); i > 0 && !strings.Contains(s[i:], "]") {
		s = s[:i] // порт
	}
	return s
}
