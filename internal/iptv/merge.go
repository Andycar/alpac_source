package iptv

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/hlsprobe"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/mediaprobe"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  Merged global view — unify several global playlists (e.g. multiple m3u.su
//  category lists) into ONE deduplicated channel list, with optional liveness
//  health-checking so dead/duplicate streams fall away.
// ---------------------------------------------------------------------------

// MergedGlobalID is the synthetic playlist id under which every global playlist is presented
// as a single deduplicated list. The web client tunes this one instead of N separate lists.
const MergedGlobalID = "all"

const (
	// healthDeadGuardPct — доля мёртвых (в процентах), начиная с которой
	// результат цикла считается недостоверным и отбрасывается целиком.
	healthDeadGuardPct = 90
	// healthMinSampleForGuard — на маленькой выборке 90% мёртвых вполне реальны
	// (три канала, все умерли), поэтому guard включается только на объёме.
	healthMinSampleForGuard = 20
)

var (
	// quality/codec tags stripped from a name before dedup ("Первый HD" == "Первый канал")
	qualityTagRe = regexp.MustCompile(`(?i)(^|\s|\()(uhd|fhd|hd|sd|4k|2k|hevc|h\.?265|h\.?264|1080p?|720p?|576p?|480p?|360p?)(\)|\s|$)`)
	// статус-маркеры в квадратных скобках у iptv-org-подобных списков:
	// "Первый канал (1080p) [Not 24/7]", "[Geo-blocked]" — не часть имени
	bracketTagRe = regexp.MustCompile(`\[[^\]]*\]`)
	// Уточнение в круглых скобках: город, орбита, дубль («(Камчатка)», «(+2)»).
	// Отбрасывается только во ВТОРОЙ попытке резолва — см. ResolveIDByName.
	parenSuffixRe = regexp.MustCompile(`\([^)]*\)`)
	nonAlnumRe    = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

// normChannelName collapses a channel name to a dedup key: lowercased, bracketed status
// markers ([Not 24/7]), quality/codec tags and all punctuation/emoji (flags) removed,
// whitespace folded. Empty → no dedup (kept as-is).
func normChannelName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = bracketTagRe.ReplaceAllString(s, " ")
	// run twice: adjacent tags ("Первый HD FHD") need a second pass since the regex consumes the separator
	s = qualityTagRe.ReplaceAllString(s, " ")
	s = qualityTagRe.ReplaceAllString(s, " ")
	s = nonAlnumRe.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// cyrToLat — транслитерация кириллицы для сопоставления имён каналов.
//
// Плейлисты сплошь пишут русские каналы латиницей («Rodnoe Kino», «Russkiy
// Detektiv», «Nashe muzhskoe»), а XMLTV — кириллицей. Из-за этого гид не
// находился у каналов, которые смотрят чаще всего: среди двух сотен самых
// запускаемых программа была лишь у 36%.
var cyrToLat = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	// украинские и белорусские буквы — те же плейлисты
	'і': "i", 'ї': "yi", 'є': "e", 'ґ': "g", 'ў': "u",
}

// translitCanon схлопывает написания, которые различаются лишь схемой
// транслитерации: «Rodnoye»/«Rodnoe», «Russkiy»/«Russki», «Kharkov»/«Harkov».
// Применяется к ОБЕИМ сторонам сравнения, поэтому важна не «правильность»
// записи, а её одинаковость.
var translitCanon = []struct{ from, to string }{
	{"sch", "sh"}, {"shh", "sh"}, {"tsch", "sh"},
	{"kh", "h"}, {"ts", "c"}, {"zh", "j"},
	{"yo", "e"}, {"jo", "e"},
	{"iy", "i"}, {"yi", "i"}, {"ij", "i"},
	{"ye", "e"}, {"je", "e"},
	{"yu", "u"}, {"ju", "u"},
	{"ya", "a"}, {"ja", "a"},
	{"y", "i"}, {"w", "v"}, {"x", "ks"},
}

// translitKey приводит имя канала к виду, одинаковому для кириллицы и латиницы.
func translitKey(name string) string {
	base := normChannelName(name)
	if base == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(base) + 8)
	for _, r := range base {
		if lat, ok := cyrToLat[r]; ok {
			b.WriteString(lat)
			continue
		}
		b.WriteRune(r)
	}
	s := b.String()
	for _, rule := range translitCanon {
		s = strings.ReplaceAll(s, rule.from, rule.to)
	}
	return strings.Join(strings.Fields(s), " ")
}

func qualityRank(q string) int {
	switch strings.ToUpper(strings.TrimSpace(q)) {
	case "4K", "UHD", "2160P":
		return 4
	case "FHD", "1080P":
		return 3
	case "HD", "720P":
		return 2
	case "SD", "480P":
		return 1
	}
	return 0
}

// mergedLocked combines all global playlist caches into one deduplicated list. The CALLER must
// hold s.mu (R or W). Known-dead URLs are dropped before dedup so a dead duplicate yields to a
// healthy one from another source (implicit failover); among live duplicates the higher quality
// wins; otherwise the first source's entry is kept (source order = config order).
// mergedSnapshot is a memoized mergedLocked() result with the time it was built.
type mergedSnapshot struct {
	channels []Channel
	at       time.Time
}

// mergedCacheTTL bounds how stale the memoized global channel list may be. Config
// changes (RefreshGlobal/SetGlobalPlaylists) invalidate immediately; only a
// health-check dead-marking can be up to this stale, which the player tolerates.
const mergedCacheTTL = 30 * time.Second

// mergedLocked returns the deduplicated global channel list, memoized for
// mergedCacheTTL. The CALLER must hold s.mu (R or W). The returned slice is shared
// and MUST NOT be mutated by callers (they filter/paginate into fresh slices).
func (s *Store) mergedLocked() []Channel {
	if snap := s.mergedCache.Load(); snap != nil && time.Since(snap.at) < mergedCacheTTL {
		return snap.channels
	}
	out := s.buildMergedLocked()
	s.mergedCache.Store(&mergedSnapshot{channels: out, at: time.Now()})
	return out
}

// invalidateMerged drops the memoized merged list so the next mergedLocked()
// rebuilds. Call after any change to the global playlists / their caches.
func (s *Store) invalidateMerged() {
	s.mergedCache.Store(nil)
}

func (s *Store) buildMergedLocked() []Channel {
	var all []Channel
	for _, gu := range s.globalURLs {
		key := "global_" + playlistIDFromURL(gu)
		if c, ok := s.cache[key]; ok {
			all = append(all, c.Channels...)
		}
	}

	idx := make(map[string]int, len(all))
	out := make([]Channel, 0, len(all))
	for _, ch := range all {
		if s.isDeadURL(ch.URL) {
			continue
		}
		k := normChannelName(ch.Name)
		if k == "" {
			out = append(out, ch)
			continue
		}
		if i, ok := idx[k]; ok {
			if qualityRank(ch.Quality) > qualityRank(out[i].Quality) {
				out[i] = ch // prefer the higher-quality duplicate
			}
			continue
		}
		idx[k] = len(out)
		out = append(out, ch)
	}
	return out
}

// ---------------------------------------------------------------------------
//  Health-check (opt-in) — periodically probe stream liveness, mark dead URLs.
// ---------------------------------------------------------------------------

// freezeMinAge — сколько должно пройти с прошлой пробы, чтобы неподвижное окно
// считалось заморозкой. Живой эфир двигает окно каждые несколько секунд, так
// что 10 минут — заведомо больше любого легального target duration, но меньше
// часового интервала цикла.
const freezeMinAge = 10 * time.Minute

const (
	// maxWindowEntries — потолок карты подписей окна (URL их переживают: панели
	// ротируют id каналов, и адрес исчезает навсегда).
	maxWindowEntries = 8000
	// windowRetention — после этого срока подпись бесполезна: канал давно не
	// попадал в выборку, сравнивать не с чем.
	windowRetention = 24 * time.Hour
)

// windowSnap — подпись живого окна канала и время, когда она снята.
type windowSnap struct {
	sig string
	at  time.Time
}

type healthState struct {
	mu   sync.RWMutex
	dead map[string]struct{} // stream URL → currently unreachable
	// windows — подпись окна с ПРОШЛОГО цикла, per URL. Заморозку (200 на всё,
	// сегменты качаются, но окно стоит) одна проба увидеть не может; сравнение
	// с прошлым циклом видит её даром, не платя паузой как ProbeLive.
	windows map[string]windowSnap
	// frozen — URL, у которых окно не сдвинулось между циклами. Отдельно от
	// dead, чтобы отличать «не отвечает» от «отвечает, но эфир стоит».
	frozen map[string]struct{}
	// codecs is what the deep probe saw INSIDE the stream, per URL. Free to
	// collect (the probe already downloaded the bytes) and the answer to the
	// most common IPTV complaint that is not a dead channel: the picture plays
	// and the sound does not, because the channel ships E-AC-3 to a box that
	// cannot decode it.
	codecs map[string]StreamInfo
}

// StreamInfo is what the last deep probe found inside a channel's stream.
type StreamInfo struct {
	Container string             `json:"container,omitempty"`
	Codecs    string             `json:"codecs,omitempty"` // RFC 6381 form: "hvc1.1.6.L120,ec-3"
	Tracks    []mediaprobe.Track `json:"tracks,omitempty"`
	ProbedAt  time.Time          `json:"probed_at,omitempty"`
}

// maxCodecEntries bounds the map for playlists with thousands of channels; the
// probe cap (healthMax) is the real limit, this is the backstop.
const maxCodecEntries = 8000

// StreamInfoFor returns what the last health cycle saw inside this stream URL.
// Empty when health-checking is off, the channel has not been probed yet, or
// the probe was the shallow kind.
func (s *Store) StreamInfoFor(u string) (StreamInfo, bool) {
	if u == "" || s.health == nil {
		return StreamInfo{}, false
	}
	s.health.mu.RLock()
	defer s.health.mu.RUnlock()
	info, ok := s.health.codecs[u]
	return info, ok
}

func (s *Store) recordStreamInfo(u string, tracks mediaprobe.Tracks) {
	if u == "" || len(tracks.Tracks) == 0 || s.health == nil {
		return
	}
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	if s.health.codecs == nil {
		s.health.codecs = make(map[string]StreamInfo)
	}
	if len(s.health.codecs) >= maxCodecEntries {
		if _, known := s.health.codecs[u]; !known {
			return
		}
	}
	s.health.codecs[u] = StreamInfo{
		Container: tracks.Container,
		Codecs:    tracks.CodecString(),
		Tracks:    tracks.Tracks,
		ProbedAt:  time.Now().UTC(),
	}
}

// codecCensus counts how many probed channels carry each codec. Only the codecs
// that decide whether a device can play at all are counted — the long tail
// would turn one log line into twenty.
func (s *Store) codecCensus() map[string]int {
	interesting := map[string]bool{
		"hevc": true, "h264": true, "av1": true, "dolbyvision": true,
		"eac3": true, "ac3": true, "aac": true, "mp2": true,
	}
	out := map[string]int{}
	s.health.mu.RLock()
	defer s.health.mu.RUnlock()
	for _, info := range s.health.codecs {
		seen := map[string]bool{}
		for _, tr := range info.Tracks {
			if interesting[tr.Name] && !seen[tr.Name] {
				seen[tr.Name] = true
				out[tr.Name]++
			}
		}
	}
	return out
}

// markWindow записывает подпись окна канала и отвечает, ЗАМОРОЖЕН ли он:
// подпись та же, что была снята не меньше freezeMinAge назад. Первая проба
// (или слишком свежая предыдущая) заморозкой не считается — судить не о чем.
func (s *Store) markWindow(u, sig string) bool {
	if s.health == nil || u == "" || sig == "" {
		return false
	}
	now := time.Now()
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	if s.health.windows == nil {
		s.health.windows = make(map[string]windowSnap)
	}
	if s.health.frozen == nil {
		s.health.frozen = make(map[string]struct{})
	}
	after := s.freezeAfter
	if after <= 0 {
		after = freezeMinAge
	}
	prev, seen := s.health.windows[u]
	frozen := seen && prev.sig == sig && now.Sub(prev.at) >= after
	if frozen {
		s.health.frozen[u] = struct{}{}
		// Снимок НЕ обновляем: иначе следующий цикл сравнивал бы с только что
		// записанным временем и канал «оживал» бы через цикл, дёргая зрителя
		// туда-обратно. Пока окно не сдвинулось, канал остаётся замороженным.
		return true
	}
	if !seen || prev.sig != sig {
		if !seen && len(s.health.windows) >= maxWindowEntries {
			s.pruneWindowsLocked(now)
		}
		s.health.windows[u] = windowSnap{sig: sig, at: now}
		delete(s.health.frozen, u)
	}
	return false
}

// pruneWindowsLocked выбрасывает подписи, к которым давно не возвращались.
// Нужен не «на всякий случай»: Xtream-панели ротируют id каналов в URL, так что
// без чистки карта копила бы по записи на каждый исчезнувший адрес.
// Вызывающий держит s.health.mu.
func (s *Store) pruneWindowsLocked(now time.Time) {
	for u, w := range s.health.windows {
		if now.Sub(w.at) > windowRetention {
			delete(s.health.windows, u)
			delete(s.health.frozen, u)
		}
	}
	// Ротация могла оставить карту переполненной и после чистки по возрасту —
	// тогда сносим всё: подписи восстановятся за следующий цикл, это дешевле
	// неограниченного роста.
	if len(s.health.windows) >= maxWindowEntries {
		s.health.windows = make(map[string]windowSnap)
		s.health.frozen = make(map[string]struct{})
	}
}

// FrozenCount returns how many sources the last cycles found frozen (эфир стоит
// при исправном HTTP). Для диагностики в админке/логах.
func (s *Store) FrozenCount() int {
	if s.health == nil {
		return 0
	}
	s.health.mu.RLock()
	defer s.health.mu.RUnlock()
	return len(s.health.frozen)
}

// StreamHealth — что health-check знает про адрес: "" (не проверяли), "ok", "dead" (не отвечает)
// или "frozen" (отвечает, но эфир стоит на месте). Отдаём клиенту, чтобы в списке и в телегиде
// было видно, что канал сломан, ДО того как зритель нажмёт OK и упрётся в чёрный экран.
func (s *Store) StreamHealth(u string) string {
	if !s.healthOn || u == "" || s.health == nil {
		return ""
	}
	s.health.mu.RLock()
	defer s.health.mu.RUnlock()
	if _, dead := s.health.dead[u]; dead {
		return "dead"
	}
	if _, frozen := s.health.frozen[u]; frozen {
		return "frozen"
	}
	// «ok» говорим только про то, что ДЕЙСТВИТЕЛЬНО проверяли: непроверенный адрес и живой —
	// разные вещи, и выдавать второе за первое значит врать зрителю.
	if _, seen := s.health.windows[u]; seen {
		return "ok"
	}
	return ""
}

func (s *Store) isDeadURL(u string) bool {
	if !s.healthOn || u == "" || s.health == nil {
		return false
	}
	s.health.mu.RLock()
	defer s.health.mu.RUnlock()
	_, dead := s.health.dead[u]
	return dead
}

// SetHealthLimits tunes the prober's footprint: max URLs per cycle and how many
// probes run at once. Подписочные панели ограничивают число ОДНОВРЕМЕННЫХ
// сессий на аккаунт, и пробы конкурируют за те же слоты, что и живые зрители —
// на таком доноре высокая параллельность и портит просмотр людям, и возвращает
// ложное «канал мёртв». Значения <= 0 оставляют текущие. Call before StartHealthCheck.
func (s *Store) SetHealthLimits(maxURLs, concurrency int) {
	if maxURLs > 0 {
		s.healthMax = maxURLs
	}
	if concurrency > 0 {
		s.healthConc = concurrency
	}
}

// SetHealthDepth configures how thoroughly the liveness prober checks an HLS
// channel. Call before StartHealthCheck.
//
//	deep  — walk manifest → variant → first segment (~16KB per channel)
//	stale — additionally re-read a live playlist after a pause to catch a frozen
//	        channel; adds an 8s wait per channel, so it is for small lists only
func (s *Store) SetHealthDepth(deep, stale bool) {
	s.healthDeep = deep
	s.healthStale = stale
}

// StartHealthCheck launches the background liveness prober (bounded concurrency + a hard cap on
// URLs per cycle). Probing public streams from the server IP is brief (a ranged 0-1 GET), but it
// IS server-IP traffic — keep it opt-in and conservative so upstreams don't flag the box.
func (s *Store) StartHealthCheck(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	s.healthOn = true
	if s.health == nil {
		s.health = &healthState{dead: make(map[string]struct{})}
	}
	go func() {
		// let the initial playlist refresh settle before the first sweep
		select {
		case <-ctx.Done():
			return
		case <-time.After(90 * time.Second):
		}
		s.healthCycle(ctx)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.healthCycle(ctx)
			}
		}
	}()
}

func (s *Store) healthCycle(ctx context.Context) {
	type probe struct {
		url, ua, ref string
		geo          bool // ходить через прокси страны вещателя (см. RegSource.GeoLocked)
	}
	seen := make(map[string]struct{})
	// Пробы РАЗДЕЛЬНО по донорам: cap применяется к каждому донору по очереди
	// (round-robin), иначе первый донор съедает весь бюджет и источники
	// остальных не проверяются НИКОГДА. Прод: 4919 источников при cap 2000 —
	// проверялась ровно одна (первая) панель.
	var perDonor [][]probe
	// registry_only: клиенты играют только каналы реестра — пробовать ВСЕ
	// донорские каналы незачем (крупный донор вроде iptv-org съел бы весь
	// healthMax на потоки, которые никто не смотрит). Источники реестра —
	// подмножество донорских URL, их и пробуем ниже.
	if !(s.registryOnly && s.registry != nil) {
		s.mu.RLock()
		for _, gu := range s.globalURLs {
			key := "global_" + playlistIDFromURL(gu)
			c, ok := s.cache[key]
			if !ok {
				continue
			}
			var group []probe
			for _, ch := range c.Channels {
				if ch.URL == "" {
					continue
				}
				if _, dup := seen[ch.URL]; dup {
					continue
				}
				seen[ch.URL] = struct{}{}
				group = append(group, probe{url: ch.URL, ua: ch.UserAgent, ref: ch.Referer})
			}
			if len(group) > 0 {
				perDonor = append(perDonor, group)
			}
		}
		s.mu.RUnlock()
	}

	// Источники реестра — тоже под пробу: фейловер выбирает по этой же карте
	// dead-URL, без проб каналы реестра не переключались бы на живые зеркала.
	var regGroup []probe
	for _, rp := range s.registrySourceProbes() {
		if _, dup := seen[rp.URL]; dup {
			continue
		}
		seen[rp.URL] = struct{}{}
		regGroup = append(regGroup, probe{url: rp.URL, ua: rp.UA, ref: rp.Referer, geo: rp.GeoLocked})
	}
	if len(regGroup) > 0 {
		perDonor = append(perDonor, regGroup)
	}

	// Round-robin отбор до healthMax: каждый донор получает свою долю бюджета.
	var probes []probe
	total := 0
	for _, g := range perDonor {
		total += len(g)
	}
	// Начало отбора сдвигаем от цикла к циклу: при одном доноре (registry_only
	// — обычный случай) отбор иначе всегда берёт первые healthMax источников, и
	// хвост не проверяется НИКОГДА. Со сдвигом очередь обходит весь список за
	// несколько циклов, а бюджет и нагрузка остаются прежними.
	cursor := s.healthCursor
	for i := 0; len(probes) < s.healthMax; i++ {
		progressed := false
		for _, g := range perDonor {
			if i < len(g) {
				probes = append(probes, g[(cursor+i)%len(g)])
				progressed = true
				if len(probes) >= s.healthMax {
					break
				}
			}
		}
		if !progressed {
			break
		}
	}
	if total > len(probes) {
		s.healthCursor = cursor + len(probes)
		log.Warn().Int("total", total).Int("probed", len(probes)).Int("cap", s.healthMax).
			Int("donors", len(perDonor)).Int("next_from", s.healthCursor).
			Msg("iptv: health-check capped — budget split across donors")
	} else {
		// Влезли целиком — сдвигать нечего, следующий цикл начинает сначала.
		s.healthCursor = 0
	}

	dead := make(map[string]struct{})
	var dmu sync.Mutex
	sem := make(chan struct{}, s.healthConc)
	var wg sync.WaitGroup
	for _, p := range probes {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(p probe) {
			defer wg.Done()
			defer func() { <-sem }()
			if !s.probeAlive(ctx, p.url, p.ua, p.ref, p.geo) {
				dmu.Lock()
				dead[p.url] = struct{}{}
				dmu.Unlock()
			}
		}(p)
	}
	wg.Wait()

	// Sanity-guard. Если цикл объявил мёртвым ПОЧТИ ВСЁ — верить ему нельзя:
	// одновременная смерть всех каналов практически невозможна, а вот причин на
	// нашей стороне полно. Прод дал ровно этот случай: подписочная панель
	// ограничивает число одновременных сессий, зрители держат слоты, и пробы
	// получают пустой манифест → 2000 из 2000 «мертвы». Применить такой
	// результат — значит выкинуть все рабочие источники разом.
	if len(probes) >= healthMinSampleForGuard && len(dead)*100 >= len(probes)*healthDeadGuardPct {
		log.Error().Int("checked", len(probes)).Int("dead", len(dead)).
			Msg("iptv: health-check рапортует ~все источники мёртвыми — результат ОТБРОШЕН (вероятно сеть сервера или лимит сессий панели, а не каналы)")
		return
	}

	s.health.mu.Lock()
	s.health.dead = dead
	s.health.mu.Unlock()

	// The codec census is the cheap by-product of a deep cycle, and it is what
	// tells an operator whether "у меня половина каналов без звука" is a client
	// bug or a park of E-AC-3 channels meeting boxes without a Dolby licence.
	ev := log.Info().Int("checked", len(probes)).Int("dead", len(dead)).Int("frozen", s.FrozenCount())
	if s.healthDeep {
		for codec, n := range s.codecCensus() {
			ev = ev.Int("codec_"+codec, n)
		}
	}
	ev.Msg("iptv: health-check cycle")
}

// healthClientFor picks the client the probe must speak through. Гео-запертый
// источник, померенный напрямую, отвечает редиректом на заглушку — цикл честно
// признает его мёртвым и снимет живой канал с эфира. Меряем его тем же
// маршрутом, каким потом играем.
func (s *Store) healthClientFor(geo bool) *http.Client {
	if geo {
		return httpclient.NewForBalancerDynamic(iptvRegionBalancer, 25*time.Second)
	}
	return s.healthClient
}

func (s *Store) probeAlive(ctx context.Context, url, ua, ref string, geo bool) bool {
	cl := s.healthClientFor(geo)
	if s.healthDeep && looksHLS(url) {
		return s.probeHLSAlive(ctx, url, ua, ref, cl)
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", healthUA(ua))
	if ref != "" {
		req.Header.Set("Referer", ref)
	}
	req.Header.Set("Range", "bytes=0-1") // we only need the response status, not the stream
	resp, err := cl.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode < 400 || resp.StatusCode == 405 // some hosts reject Range/HEAD but the stream is live
}

// probeHLSAlive walks the channel the way a player would. A two-byte read off
// the manifest calls a channel alive whenever its web server is alive — which
// is nearly always, including for channels whose segments have 403'd for weeks
// and channels serving an empty window.
func (s *Store) probeHLSAlive(ctx context.Context, url, ua, ref string, cl *http.Client) bool {
	c, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	fetch := func(ctx context.Context, u, rangeHdr string) (int, []byte, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return 0, nil, "", err
		}
		req.Header.Set("User-Agent", healthUA(ua))
		if ref != "" {
			req.Header.Set("Referer", ref)
		}
		if rangeHdr != "" {
			req.Header.Set("Range", rangeHdr)
		}
		resp, err := cl.Do(req)
		if err != nil {
			return 0, nil, "", err
		}
		defer resp.Body.Close()
		// Cap the read: a sweep over thousands of channels must not pull whole
		// segments off other people's CDNs.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		final := u
		if resp.Request != nil && resp.Request.URL != nil {
			final = resp.Request.URL.String()
		}
		return resp.StatusCode, body, final, nil
	}

	var res hlsprobe.Result
	if s.healthStale {
		res = hlsprobe.ProbeLive(c, url, fetch, 8*time.Second)
	} else {
		res = hlsprobe.Probe(c, url, fetch)
	}
	s.recordStreamInfo(url, res.Media)
	if !res.OK {
		log.Debug().
			Str("url", url).Str("stage", string(res.Stage)).
			Int("manifest", res.ManifestStatus).Int("segment", res.SegmentStatus).
			Bool("stale", res.Stale).Str("err", res.Err).
			Msg("iptv: channel failed deep probe")
		return false
	}
	// Канал ответил на всё — но живой ли эфир? Панель отдаёт замороженный
	// канал неотличимо от рабочего: 200 на манифест, сегменты качаются, просто
	// окно стоит часами. Ловим сравнением с прошлым циклом (см. freezeMinAge);
	// на первом цикле сравнивать не с чем — канал считается живым.
	if res.Live && res.Window != "" && s.markWindow(url, res.Window) {
		log.Debug().Str("url", url).Str("window", res.Window).
			Msg("iptv: channel frozen — live window did not advance since last cycle")
		return false
	}
	return true
}

func looksHLS(u string) bool {
	l := strings.ToLower(u)
	if i := strings.IndexByte(l, '?'); i >= 0 {
		l = l[:i]
	}
	return strings.HasSuffix(l, ".m3u8") || strings.HasSuffix(l, ".m3u")
}

func healthUA(ua string) string {
	if ua == "" {
		return "Mozilla/5.0 (Linux; Android 10) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Mobile Safari/537.36"
	}
	return ua
}
