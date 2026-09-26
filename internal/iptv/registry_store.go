package iptv

import (
	"strings"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  Store ↔ Registry — представление реестра клиентам и автофейловер.
//
//  Реестр наружу выглядит как ещё один глобальный плейлист (id="own"): та же
//  модель Channel, те же ручки /api/iptv/*. URL канала в отдаваемом Channel —
//  ЛУЧШИЙ ЖИВОЙ источник на момент запроса: pinned раньше auto, живой раньше
//  мёртвого (по карте health-check), выше качеством раньше. Умер источник —
//  следующий /play отдаёт другой URL при том же id канала.
// ---------------------------------------------------------------------------

// Registry returns the store's channel registry (nil when disabled).
func (s *Store) Registry() *Registry { return s.registry }

// SourceAlive reports whether a source URL is NOT marked dead by the last
// health cycle (never-probed counts as alive — "unknown", not "broken").
func (s *Store) SourceAlive(url string) bool { return !s.isDeadURL(url) }

// SourceFrozen reports whether the last cycles saw this source answer 200 with
// a live window that never advanced — эфир стоит при исправном HTTP. Такой
// источник тоже мёртв для зрителя, но по совсем другой причине, чем недоступный,
// и оператору это надо различать.
func (s *Store) SourceFrozen(url string) bool {
	if url == "" || s.health == nil {
		return false
	}
	s.health.mu.RLock()
	defer s.health.mu.RUnlock()
	_, frozen := s.health.frozen[url]
	return frozen
}

// RegistryProxyMode returns the proxy mode applied to registry streams.
func (s *Store) RegistryProxyMode() string { return s.registryProxy }

// resolveRegSource picks the best playable source. Порядок предпочтения:
//
//  1. pinned живые              — закреплено руками, значит так и хотели
//  2. ПЕРВОИСТОЧНИКИ живые      — домен вещателя не зависит от чужой подписки
//  3. остальные живые           — панели и агрегаторы
//  4. …то же самое без проверки живости (health мог не успеть или врать)
//
// Первоисточник обгоняет панельный 4K НАМЕРЕННО: канал вещателя переживает
// смерть подписки, а панельный — нет. Выключается через [iptv] prefer_official.
// ArchiveSourceFor подменяет источник канала реестра на тот, у которого ЕСТЬ архив.
//
// У канала реестра обычно несколько адресов от разных доноров, и обычный выбор берёт первый
// живой — наличие архива он не учитывает вовсе. В итоге запись не запускалась даже тогда, когда
// среди источников catchup-адрес был: сервер переписывал URL источника БЕЗ архива и отдавал
// живой эфир вместо записи.
//
// false — архива нет ни у одного источника; вызывающий тогда просто играет эфир.
func (s *Store) ArchiveSourceFor(channelID string) (Channel, bool) {
	if s.registry == nil || !IsRegistryChannelID(channelID) {
		return Channel{}, false
	}
	rc, ok := s.registry.Get(channelID)
	if !ok {
		return Channel{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Живые источники впереди мёртвых: играть архив по неотвечающему адресу бессмысленно.
	for _, aliveOnly := range []bool{true, false} {
		for _, list := range [][]RegSource{rc.Pinned, rc.Auto} {
			for i := range list {
				src := &list[i]
				if src.Catchup == nil || src.URL == "" {
					continue
				}
				if aliveOnly && s.isDeadURL(src.URL) {
					continue
				}
				if ch, ok := s.regChannelViewWith(&rc, src); ok {
					return ch, true
				}
			}
		}
	}
	return Channel{}, false
}

func (s *Store) resolveRegSource(c *RegChannel) *RegSource {
	pick := func(list []RegSource, aliveOnly, officialOnly bool) *RegSource {
		for i := range list {
			// Резолвер сам добывает адрес — пустой URL для него норма, а
			// health-карта про него ничего не знает (она мерит статичные ссылки).
			dyn := list[i].Resolver != "" && HasResolver(list[i].Resolver)
			if list[i].URL == "" && !dyn {
				continue
			}
			if aliveOnly && !dyn && s.isDeadURL(list[i].URL) {
				continue
			}
			if officialOnly && !s.official.isOfficial(list[i].URL) {
				continue
			}
			return &list[i]
		}
		return nil
	}
	if src := pick(c.Pinned, true, false); src != nil {
		return src
	}
	if s.preferOfficial {
		if src := pick(c.Auto, true, true); src != nil {
			return src
		}
	}
	if src := pick(c.Auto, true, false); src != nil {
		return src
	}
	if src := pick(c.Pinned, false, false); src != nil {
		return src
	}
	if s.preferOfficial {
		if src := pick(c.Auto, false, true); src != nil {
			return src
		}
	}
	return pick(c.Auto, false, false)
}

// SourceOfficial reports whether a source URL is a broadcaster's own stream.
// Нужно админке, чтобы показать, на чём канал держится.
func (s *Store) SourceOfficial(url string) bool { return s.official.isOfficial(url) }

// regChannelView converts a registry channel to the client-facing Channel with
// the currently-best source URL substituted in.
func (s *Store) regChannelView(c *RegChannel) (Channel, bool) {
	src := s.resolveRegSource(c)
	if src == nil {
		return Channel{}, false // канал без единого источника не показываем
	}
	return s.regChannelViewWith(c, src)
}

// regChannelViewWith собирает канал по ЗАДАННОМУ источнику — тем же кодом, что и обычный показ.
// Нужен архиву: там источник выбирается по наличию catchup, а не по здоровью.
func (s *Store) regChannelViewWith(c *RegChannel, src *RegSource) (Channel, bool) {
	if src == nil {
		return Channel{}, false
	}
	url := src.URL
	// Гео-замок и резолвер — разные причины одного следствия: за апстримом надо
	// идти из нужной страны. У резолвера адрес ещё и добывается на лету.
	// Хост проверяем НАРЯДУ с флагом: источник мог приехать из донора без
	// пометки, и тогда зритель вместо канала видит заглушку блокировки.
	regionRoute := src.GeoLocked || IsGeoLockedHost(url)
	// Источник без постоянного адреса: спрашиваем актуальный у резолвера.
	// Не смогли — отдаём запасной URL, если он есть; пустой канал прятать
	// нельзя, иначе он молча исчезнет из плейлиста при первом же сбое.
	if src.Resolver != "" {
		regionRoute = true // вещателю с резолвером нужен свой маршрут (см. NeedsRegionRoute)
		if got, ok := s.ResolveSource(src.Resolver); ok {
			url = got
		} else if url == "" {
			return Channel{}, false
		}
	}
	// Страна и жанр: у записей, заведённых до разделения таксономии, поля
	// пустые — достраиваем из того, что известно (старая группа, tvg-id, имя).
	// Считать на лету дёшево, а миграция файла тогда перестаёт быть условием
	// работоспособности: реестр остаётся читаемым и без неё.
	country, genre := c.Country, c.Genre
	if country == "" && genre == "" {
		country, genre = ClassifyChannel(c)
	}
	group := c.Group
	if group == "" {
		group = GroupFor(country, genre)
	}
	// Гео-запертому вещателю мало правильного маршрута: Ростелеком смотрит и на
	// User-Agent, отвечая роботам 301 на заглушку. Клиентский UA до апстрима
	// доходит не всегда, а «свой» UA у таких источников почти никогда не задан —
	// подставляем браузерный, не затирая явно заданный оператором.
	ua := src.UserAgent
	if ua == "" && regionRoute {
		ua = geoLockedUA
	}
	return Channel{
		ID:               c.ID,
		Name:             c.Name,
		CleanName:        cleanChannelName(c.Name),
		URL:              url,
		Logo:             c.Logo,
		Group:            group,
		Country:          country,
		CountryCode:      CountryCode(country),
		Genre:            genre,
		TvgID:            c.TvgID,
		Number:           c.Number,
		Quality:          src.Quality,
		UserAgent:        ua,
		Referer:          src.Referer,
		Catchup:          src.Catchup,
		PlaylistID:       RegistryPlaylistID,
		NeedsRegionRoute: regionRoute,
	}, true
}

// registryChannels returns the client view of all enabled registry channels.
func (s *Store) registryChannels() []Channel {
	if s.registry == nil {
		return nil
	}
	list := s.registry.List()
	out := make([]Channel, 0, len(list))
	for i := range list {
		if list[i].Disabled {
			continue
		}
		if ch, ok := s.regChannelView(&list[i]); ok {
			out = append(out, ch)
		}
	}
	return out
}

// registryPlaylist is the synthetic Playlist row shown in the client's list.
func (s *Store) registryPlaylist() Playlist {
	return Playlist{
		ID:           RegistryPlaylistID,
		Name:         s.registry.Name(),
		IsGlobal:     true,
		ProxyMode:    s.registryProxy,
		ChannelCount: s.registry.Len(),
	}
}

// GetRegistryChannel resolves an "own_*" channel id to its client view plus the
// synthetic registry playlist (carrying the registry proxy mode).
func (s *Store) GetRegistryChannel(channelID string) (*Channel, *Playlist) {
	if s.registry == nil || !IsRegistryChannelID(channelID) {
		return nil, nil
	}
	rc, ok := s.registry.Get(channelID)
	if !ok || rc.Disabled {
		return nil, nil
	}
	ch, ok := s.regChannelView(&rc)
	if !ok {
		return nil, nil
	}
	pl := s.registryPlaylist()
	return &ch, &pl
}

// allGlobalChannelsSnapshot collects every channel of every cached global
// playlist (duplicates included — each is a source candidate).
func (s *Store) allGlobalChannelsSnapshot() []Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var all []Channel
	for _, gu := range s.globalURLs {
		key := "global_" + playlistIDFromURL(gu)
		if c, ok := s.cache[key]; ok {
			all = append(all, c.Channels...)
		}
	}
	return all
}

// IngestRegistry harvests sources for registry channels from the current
// global playlist caches. Called after every global refresh; also the admin
// "reimport now" action. addNew forces creating channels for unmatched donors
// (seed / one-shot import) regardless of the auto-add setting.
func (s *Store) IngestRegistry(addNew bool) IngestStats {
	if s.registry == nil {
		return IngestStats{}
	}
	donors := s.allGlobalChannelsSnapshot()
	if len(donors) == 0 {
		return IngestStats{}
	}
	// Кто сейчас в сборщике: источники убранных доноров реестр отпускает сразу, не дожидаясь недели.
	s.mu.RLock()
	active := make(map[string]bool, len(s.globalURLs))
	for _, gu := range s.globalURLs {
		active[playlistIDFromURL(gu)] = true
	}
	s.mu.RUnlock()
	return s.registry.IngestActive(donors, addNew || s.registryAutoAdd, active)
}

// EPGNameHints maps normalized channel names → the tvg-id clients ask the EPG
// by. Реестр первым (его tvg-id — контракт клиента), затем каналы плейлистов.
// Нужно EPG-движку для алиасинга ЧУЖИХ id-схем XMLTV: ни один живой RU-фид не
// использует iptv-org-овские tvg-id доноров — совпадение имён это чинит.
func (s *Store) EPGNameHints() map[string]string {
	hints := make(map[string]string)
	add := func(name, tvg string) {
		tvg = strings.TrimSpace(tvg)
		if tvg == "" {
			return
		}
		if k := normChannelName(name); k != "" {
			if _, ok := hints[k]; !ok {
				hints[k] = tvg
			}
		}
	}
	if s.registry != nil {
		for _, c := range s.registry.List() {
			if c.Disabled {
				continue
			}
			add(c.Name, c.TvgID)
			for _, a := range c.Aliases {
				add(a, c.TvgID)
			}
		}
	}
	s.mu.RLock()
	for _, cache := range s.cache {
		for _, ch := range cache.Channels {
			add(ch.Name, ch.TvgID)
		}
	}
	s.mu.RUnlock()
	return hints
}

// registrySourceProbes lists every source URL of enabled registry channels for
// the health cycle (pinned + auto, deduped by the caller's seen-set).
type sourceProbe struct {
	URL, UA, Referer string
	// GeoLocked — пробовать через прокси страны вещателя. Без этого цикл меряет
	// не канал, а заглушку блокировки, и живой источник уходит в мёртвые.
	GeoLocked bool
}

func (s *Store) registrySourceProbes() []sourceProbe {
	if s.registry == nil {
		return nil
	}
	var out []sourceProbe
	for _, c := range s.registry.List() {
		if c.Disabled {
			continue
		}
		for _, src := range c.Sources() {
			// Динамический источник пробовать нечем: его адрес живёт минуты и
			// добывается резолвером, а не лежит в реестре.
			if src.URL == "" || src.Resolver != "" {
				continue
			}
			// Гео-замок определяем и по хосту: иначе цикл померил бы источник
			// без флага напрямую, получил заглушку и снял живой канал с эфира.
			geo := src.GeoLocked || IsGeoLockedHost(src.URL)
			ua := src.UserAgent
			if ua == "" && geo {
				ua = geoLockedUA
			}
			out = append(out, sourceProbe{
				URL: src.URL, UA: ua, Referer: src.Referer, GeoLocked: geo,
			})
		}
	}
	return out
}

// SeedRegistryIfEmpty performs the one-shot bootstrap: an empty enabled
// registry is filled from the global playlists once they are parsed. Called
// after RefreshGlobal on startup so «Мои каналы» appears without any manual
// step. No-op when the registry already has channels.
func (s *Store) SeedRegistryIfEmpty() {
	if s.registry == nil {
		return
	}
	s.registry.mu.RLock()
	empty := len(s.registry.channels) == 0
	s.registry.mu.RUnlock()
	if !empty {
		return
	}
	st := s.IngestRegistry(true)
	if st.NewAdded > 0 {
		log.Info().Int("channels", st.NewAdded).Msg("iptv: registry seeded from global playlists")
	}
}

// ExportM3U renders the registry as an #EXTM3U playlist. streamURL builds the
// per-channel URL (points at THIS server's stable /api/iptv/stream/{id} — the
// exported file never goes stale when upstream sources rotate).
func (s *Store) ExportM3U(epgURLs []string, streamURL func(ch Channel) string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U")
	if len(epgURLs) > 0 {
		b.WriteString(` url-tvg="` + strings.Join(epgURLs, ",") + `"`)
	}
	b.WriteString("\n")
	for _, ch := range s.registryChannels() {
		b.WriteString("#EXTINF:-1")
		if ch.TvgID != "" {
			b.WriteString(` tvg-id="` + m3uAttrEscape(ch.TvgID) + `"`)
		}
		if ch.Logo != "" {
			b.WriteString(` tvg-logo="` + m3uAttrEscape(ch.Logo) + `"`)
		}
		if ch.Group != "" {
			b.WriteString(` group-title="` + m3uAttrEscape(ch.Group) + `"`)
		}
		b.WriteString("," + ch.Name + "\n")
		b.WriteString(streamURL(ch) + "\n")
	}
	return b.String()
}

func m3uAttrEscape(s string) string { return strings.ReplaceAll(s, `"`, "'") }
