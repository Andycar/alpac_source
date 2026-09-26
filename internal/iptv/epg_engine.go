package iptv

import (
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/httpclient"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  EPG Engine — background loader + in-memory query
// ---------------------------------------------------------------------------

// EPGEngine loads and caches EPG data from XMLTV sources.
// Programs are stored in a sorted slice per channel for fast binary-search
// lookups (NowNext, Timeline).
type EPGEngine struct {
	mu         sync.RWMutex
	channels   map[string]EPGChannel   // xmltvID → channel metadata
	programs   map[string][]EPGProgram // xmltvID → sorted by Start
	nameToIcon map[string]string       // lowercased display-name → icon URL
	// nameToID — нормализованное имя канала → xmltv-id, У КОТОРОГО ЕСТЬ программы.
	// Нужен каналам без tvg-id: панели часто не отдают tvg-id вовсе, а склейка с
	// латиничным донором («Russia 1» ≠ «Россия 1») их не проставляет — без этого
	// индекса у такого канала гид пуст навсегда.
	nameToID map[string]string
	// translitToID — тот же индекс, но по транслитерированному ключу: связывает
	// «Rodnoe Kino» из плейлиста с «Родное кино» из XMLTV (см. translitKey).
	translitToID map[string]string
	loadedAt     time.Time
	lastStatus   EPGStatus // diagnostics from the last refresh (exposed via Status())

	urls           []string
	updateInterval time.Duration
	store          *Store // for extracting playlist-level EPG URLs
	httpClient     *http.Client
	cancel         context.CancelFunc
	// cacheDir — дисковый кэш скачанных XMLTV ({iptv}/epg_cache/<md5(url)>.xml.gz):
	// источник упал или сервер стартовал офлайн — гид поднимается из кэша.
	// Пусто (нет store) — кэширование выключено.
	cacheDir string
}

// EPGSourceStatus is the per-source outcome of the last refresh (diagnostics).
type EPGSourceStatus struct {
	URL      string `json:"url"`             // host + path tail only (never the full query — may carry a key)
	OK       bool   `json:"ok"`              // fetched + parsed without error
	Status   int    `json:"status"`          // HTTP status code (0 = never connected)
	Channels int    `json:"channels"`        // <channel> entries parsed from this source
	Programs int    `json:"programs"`        // <programme> entries kept (post-filter, post-window)
	Stale    bool   `json:"stale,omitempty"` // источник недоступен — данные подняты из дискового кэша
	Err      string `json:"err,omitempty"`
}

// EPGStatus is a browser-readable snapshot of the EPG engine's last refresh — surfaced at
// GET /api/iptv/epg/status so an empty guide can be diagnosed without server log access.
type EPGStatus struct {
	Channels      int               `json:"channels"`                // total channels currently held
	Programs      int               `json:"programs"`                // total programmes currently held
	LoadedAt      time.Time         `json:"loaded_at"`               // when the current data was committed
	KnownTvgIDs   int               `json:"known_tvg_ids"`           // playlist tvg-ids used as the load filter
	MatchedTvgIDs int               `json:"matched_tvg_ids"`         // how many of those actually got programmes
	Filtered      bool              `json:"filtered"`                // tvg-id filter was applied this refresh
	SelfHealed    bool              `json:"self_healed"`             // filter dropped everything → reloaded unfiltered
	KeptPrevious  bool              `json:"kept_previous,omitempty"` // все источники упали — держим прошлый гид
	Sources       []EPGSourceStatus `json:"sources"`
}

// EPGConfig holds configuration for the EPG engine.
type EPGConfig struct {
	URLs           []string      // static EPG source URLs
	UpdateInterval time.Duration // how often to refresh; default 6h
}

// NewEPGEngine creates a new EPG engine.
func NewEPGEngine(store *Store, cfg EPGConfig) *EPGEngine {
	if cfg.UpdateInterval <= 0 {
		cfg.UpdateInterval = 6 * time.Hour
	}
	e := &EPGEngine{
		channels:       make(map[string]EPGChannel),
		programs:       make(map[string][]EPGProgram),
		urls:           cfg.URLs,
		updateInterval: cfg.UpdateInterval,
		store:          store,
		// Тот же прокси-балансер, что у плейлистов/стримов: XMLTV-фиды бывают
		// закрыты для дата-центров ровно так же, как и потоки.
		httpClient: httpclient.NewForBalancerDynamic(iptvBalancer, 120*time.Second),
	}
	if store != nil {
		e.cacheDir = filepath.Join(store.dir, "epg_cache")
		_ = os.MkdirAll(e.cacheDir, 0755)
	}
	return e
}

// Start begins the background EPG update loop.
func (e *EPGEngine) Start(ctx context.Context) {
	ctx, e.cancel = context.WithCancel(ctx)

	// Initial load.
	go func() {
		e.refresh()

		ticker := time.NewTicker(e.updateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.refresh()
			}
		}
	}()
}

// Stop stops the background loop.
func (e *EPGEngine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
}

// ---------------------------------------------------------------------------
//  Query API
// ---------------------------------------------------------------------------

// NowNext returns the current and next program for each given XMLTV channel ID.
func (e *EPGEngine) NowNext(channelIDs []string, now time.Time) []EPGNowNext {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]EPGNowNext, 0, len(channelIDs))
	for _, id := range channelIDs {
		nn := EPGNowNext{ChannelID: id}
		progs := e.programs[id]
		if len(progs) > 0 {
			idx := e.findCurrentIdx(progs, now)
			if idx >= 0 {
				nn.Now = &progs[idx]
				if idx+1 < len(progs) {
					nn.Next = &progs[idx+1]
				}
			} else {
				// No current program; find the next upcoming.
				nextIdx := sort.Search(len(progs), func(i int) bool {
					return progs[i].Start.After(now)
				})
				if nextIdx < len(progs) {
					nn.Next = &progs[nextIdx]
				}
			}
		}
		result = append(result, nn)
	}
	return result
}

// Timeline returns programs for a channel within [from, to].
func (e *EPGEngine) Timeline(channelID string, from, to time.Time) []EPGProgram {
	e.mu.RLock()
	defer e.mu.RUnlock()

	progs := e.programs[channelID]
	if len(progs) == 0 {
		return nil
	}

	// Binary search for first program that overlaps with [from, to].
	// A program overlaps if prog.Stop > from AND prog.Start < to.
	startIdx := sort.Search(len(progs), func(i int) bool {
		return progs[i].Stop.After(from)
	})

	var result []EPGProgram
	for i := startIdx; i < len(progs); i++ {
		if progs[i].Start.After(to) || progs[i].Start.Equal(to) {
			break
		}
		result = append(result, progs[i])
	}
	return result
}

// Search searches program titles across all channels.
func (e *EPGEngine) Search(query string, limit int) []EPGProgram {
	if limit <= 0 {
		limit = 50
	}
	queryLower := strings.ToLower(query)

	e.mu.RLock()
	defer e.mu.RUnlock()

	now := time.Now().UTC()
	var results []EPGProgram
	for _, progs := range e.programs {
		for _, p := range progs {
			if p.Stop.Before(now) {
				continue // skip past programs
			}
			if strings.Contains(strings.ToLower(p.Title), queryLower) ||
				strings.Contains(strings.ToLower(p.Description), queryLower) {
				results = append(results, p)
				if len(results) >= limit {
					return results
				}
			}
		}
	}

	// Sort by start time.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Start.Before(results[j].Start)
	})
	return results
}

// GetChannel returns EPG channel metadata by XMLTV ID.
func (e *EPGEngine) GetChannel(xmltvID string) (EPGChannel, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ch, ok := e.channels[xmltvID]
	return ch, ok
}

// ResolveIDByName maps a channel NAME to an XMLTV id that actually carries
// programmes. Empty when nothing matches. Используется как фолбэк для каналов
// без tvg-id.
func (e *EPGEngine) ResolveIDByName(name string) string {
	key := normChannelName(name)
	if key == "" {
		return ""
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if id := e.nameToID[key]; id != "" {
		return id
	}
	// Вторая попытка — без уточнения в круглых скобках: «41 Регион (Камчатка)»,
	// «Россия 1 (Москва)». Именно второй, а не в общей нормализации: у орбит и
	// регионов расписание РАЗНОЕ, и точное совпадение с уточнением обязано
	// выигрывать. Сюда доходят только те, кому иначе не досталось бы гида вовсе.
	if bare := normChannelName(parenSuffixRe.ReplaceAllString(name, " ")); bare != "" && bare != key {
		if id := e.nameToID[bare]; id != "" {
			return id
		}
	}
	// Третья попытка — через транслит: имя латиницей против кириллицы в XMLTV.
	if tk := translitKey(name); tk != "" {
		if id := e.translitToID[tk]; id != "" {
			return id
		}
		if bare := translitKey(parenSuffixRe.ReplaceAllString(name, " ")); bare != "" && bare != tk {
			return e.translitToID[bare]
		}
	}
	return ""
}

// SeedForTest наполняет движок готовыми данными, минуя загрузку XMLTV.
// Только для тестов: разбор фидов сюда не относится, а проверять порядок
// разрешения канала нужно на предсказуемом наборе.
func (e *EPGEngine) SeedForTest(programs map[string][]EPGProgram, nameToID map[string]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.programs = programs
	e.nameToID = make(map[string]string, len(nameToID))
	e.translitToID = make(map[string]string, len(nameToID))
	for name, id := range nameToID {
		e.nameToID[normChannelName(name)] = id
		if tk := translitKey(name); tk != "" {
			if _, exists := e.translitToID[tk]; !exists {
				e.translitToID[tk] = id
			}
		}
	}
}

// HasPrograms сообщает, есть ли у этого xmltv-id хоть одна передача.
//
// Нужен, чтобы отличить РАБОЧИЙ tvg-id от мусорного. Плейлисты сплошь пишут в
// tvg-id само имя канала («41 Регион (Камчатка)»): поле непустое, XMLTV его не
// знает, и резолв, доверившись непустому значению, возвращал заведомо пустой
// гид вместо того, чтобы поискать канал по имени.
func (e *EPGEngine) HasPrograms(xmltvID string) bool {
	if xmltvID == "" {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.programs[xmltvID]) > 0
}

// GetIconByName looks up a channel icon by display name (case-insensitive).
func (e *EPGEngine) GetIconByName(name string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.nameToIcon[strings.ToLower(strings.TrimSpace(name))]
}

// Stats returns EPG statistics.
func (e *EPGEngine) Stats() (channels, programs int, loadedAt time.Time) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	totalProgs := 0
	for _, progs := range e.programs {
		totalProgs += len(progs)
	}
	return len(e.channels), totalProgs, e.loadedAt
}

// Status returns a diagnostic snapshot of the last refresh (per-source results, filter outcome).
func (e *EPGEngine) Status() EPGStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastStatus
}

// ---------------------------------------------------------------------------
//  Internal: binary search helper
// ---------------------------------------------------------------------------

// findCurrentIdx returns the index of the program airing at time t,
// or -1 if none is currently airing.
func (e *EPGEngine) findCurrentIdx(progs []EPGProgram, t time.Time) int {
	// Binary search for the last program with Start <= t.
	idx := sort.Search(len(progs), func(i int) bool {
		return progs[i].Start.After(t)
	}) - 1

	if idx < 0 {
		return -1
	}

	// Check if the program is still airing (Stop > t).
	if progs[idx].Stop.After(t) {
		return idx
	}
	return -1
}

// ---------------------------------------------------------------------------
//  Internal: refresh from sources
// ---------------------------------------------------------------------------

// Refresh forces a synchronous reload of all EPG sources (the background loop
// calls the same code on its ticker). Экспортировано для админки и тестов.
func (e *EPGEngine) Refresh() { e.refresh() }

func (e *EPGEngine) refresh() {
	// Collect all EPG URLs: static config + playlist-level x-tvg-url.
	// `pinned` are sources we must never drop: operator [iptv] epg_urls + the CURATED GLOBAL
	// playlist's own x-tvg-url list. The cap exists to bound UNBOUNDED user-playlist fan-in (many
	// users), NOT to starve the single curated playlist — which lists e.g. ~50 epgshare01 country
	// feeds, and a blind count-cap dropped the alphabetically-late RU feed, blanking every RU
	// channel's guide while keeping BG/BR/CY/DE/ID/ES.
	urls := make(map[string]struct{})
	pinned := append([]string(nil), e.urls...)
	for _, u := range e.urls {
		u = strings.TrimSpace(u)
		if u != "" {
			urls[u] = struct{}{}
		}
	}

	// Collect from playlists (if store available).
	if e.store != nil {
		e.store.mu.RLock()
		for _, cache := range e.store.cache {
			for _, u := range cache.Playlist.EPGUrls {
				u = strings.TrimSpace(u)
				if u == "" {
					continue
				}
				urls[u] = struct{}{}
				if cache.Playlist.IsGlobal {
					pinned = append(pinned, u) // curated global playlist → never capped
				}
			}
		}
		e.store.mu.RUnlock()
	}

	// Fallback: if no EPG URLs at all, use a popular public source that
	// provides logos and program data for most CIS channels.
	if len(urls) == 0 {
		urls["http://epg.it999.ru/edem.xml.gz"] = struct{}{}
	}

	// Cap only the unpinned (user-playlist) fan-in; pinned sources always survive.
	urls = capEPGSources(pinned, urls)

	log.Info().Int("sources", len(urls)).Msg("epg: refreshing")

	knownChannelIDs := e.collectKnownChannelIDs()
	stringsPool := newEPGStringPool()

	// Подсказки для алиасинга чужих id-схем: нормализованное имя канала → наш
	// tvg-id. XMLTV-источники с собственными id (числа edem, слаги iptvx) без
	// этого не матчатся ни с одним каналом плейлистов/реестра.
	var nameHints map[string]string
	if e.store != nil {
		nameHints = e.store.EPGNameHints()
	}

	// Window: keep programs from -1 day to +2 days. Forward was +3d but now/next + the
	// forward timeline never need 3 days of schedule; -1d keeps catchup's recent slots.
	windowStart := time.Now().UTC().Add(-24 * time.Hour)
	windowEnd := time.Now().UTC().Add(2 * 24 * time.Hour)

	// load() fetches+parses every source into fresh maps; the per-known-channel filter is optional so
	// we can retry unfiltered if it strands everything.
	load := func(filter map[string]struct{}) (map[string]EPGChannel, map[string][]EPGProgram, int, []EPGSourceStatus) {
		chs := make(map[string]EPGChannel)
		progs := make(map[string][]EPGProgram)
		srcs := make([]EPGSourceStatus, 0, len(urls))
		aliases := make(map[string]string) // чужой xmltv-id → наш tvg-id (по имени)
		for url := range urls {
			srcs = append(srcs, e.loadSource(url, windowStart, windowEnd, filter, nameHints, aliases, stringsPool, chs, progs))
		}
		// Sort each channel's programmes by Start, then cap per channel: a backstop against a
		// pathological source publishing thousands of micro-programmes for one channel.
		total := 0
		for id := range progs {
			sort.Slice(progs[id], func(i, j int) bool { return progs[id][i].Start.Before(progs[id][j].Start) })
			progs[id] = dedupePrograms(progs[id])
			if len(progs[id]) > epgMaxProgramsPerChannel {
				progs[id] = progs[id][:epgMaxProgramsPerChannel]
			}
			total += len(progs[id])
		}
		return chs, progs, total, srcs
	}

	newChannels, newPrograms, totalProgs, sources := load(knownChannelIDs)

	// Self-heal: the per-known-channel filter keeps only programmes whose XMLTV id EXACTLY matches a
	// playlist tvg-id. If the playlist's tvg-ids don't line up with the EPG source's channel ids, the
	// filter strands every programme and the guide + now/next go blank on ALL channels. When sources
	// parsed fine but nothing survived the filter, reload UNFILTERED so EPG still works (bounded by the
	// source cap + window + per-channel cap). Lookups by tvg-id still only hit what the source carries —
	// but at least matching-id channels and name-based icons come back instead of a total blackout.
	selfHealed := false
	if totalProgs == 0 && knownChannelIDs != nil && len(newChannels) > 0 {
		log.Warn().Int("channels", len(newChannels)).Msg("epg: 0 programmes after tvg-id filter — reloading unfiltered (playlist tvg-ids may not match EPG source)")
		newChannels, newPrograms, totalProgs, sources = load(nil)
		selfHealed = true
	}

	matched := 0
	for _, progs := range newPrograms {
		if len(progs) > 0 {
			matched++
		}
	}

	// Build name→icon index from all display names.
	newNameToIcon := make(map[string]string, len(newChannels)*3)
	// …и name→id, но ТОЛЬКО по каналам, у которых реально есть программы: иначе
	// фолбэк уводил бы на пустой одноимённый канал вместо работающего.
	newNameToID := make(map[string]string, len(newChannels))
	newTranslitToID := make(map[string]string, len(newChannels))
	for _, ch := range newChannels {
		for _, name := range ch.AltNames {
			if ch.Icon != "" {
				key := strings.ToLower(strings.TrimSpace(name))
				if key != "" {
					if _, exists := newNameToIcon[key]; !exists {
						newNameToIcon[key] = ch.Icon
					}
				}
			}
			if len(newPrograms[ch.ID]) == 0 {
				continue
			}
			if k := normChannelName(name); k != "" {
				if _, exists := newNameToID[k]; !exists {
					newNameToID[k] = ch.ID
				}
			}
			if tk := translitKey(name); tk != "" {
				if _, exists := newTranslitToID[tk]; !exists {
					newTranslitToID[tk] = ch.ID
				}
			}
		}
	}

	status := EPGStatus{
		Channels:      len(newChannels),
		Programs:      totalProgs,
		LoadedAt:      time.Now(),
		KnownTvgIDs:   len(knownChannelIDs),
		MatchedTvgIDs: matched,
		Filtered:      knownChannelIDs != nil && !selfHealed,
		SelfHealed:    selfHealed,
		Sources:       sources,
	}

	// Все источники упали (сеть/апстримы легли разом, и кэша нет) — НЕ затираем
	// живой гид пустотой: старые программы на пару часов полезнее пустого экрана.
	anyOK := false
	for _, s := range sources {
		if s.OK {
			anyOK = true
			break
		}
	}

	e.mu.Lock()
	if !anyOK && totalProgs == 0 && len(e.programs) > 0 {
		status.KeptPrevious = true
		status.Channels = len(e.channels)
		prev := 0
		for _, p := range e.programs {
			prev += len(p)
		}
		status.Programs = prev
		status.LoadedAt = e.loadedAt // данные не менялись — время оставляем честное
		e.lastStatus = status
		e.mu.Unlock()
		log.Warn().Int("sources", len(sources)).Msg("epg: every source failed — keeping previous guide")
		return
	}
	e.channels = newChannels
	e.programs = newPrograms
	e.nameToIcon = newNameToIcon
	e.nameToID = newNameToID
	e.translitToID = newTranslitToID
	e.loadedAt = status.LoadedAt
	e.lastStatus = status
	e.mu.Unlock()

	log.Info().Int("channels", len(newChannels)).Int("programs", totalProgs).Int("matched_channels", matched).
		Int("known_tvg_ids", len(knownChannelIDs)).Bool("self_healed", selfHealed).Int("logo_names", len(newNameToIcon)).Msg("epg: loaded")
}

// dedupePrograms выкидывает повторы одной и той же передачи. Один канал приходит из НЕСКОЛЬКИХ
// XMLTV-источников (плюс алиасы по tvg-id), и раньше каждая копия просто дописывалась в список:
// на проде у канала оказывалось по 4 одинаковых передачи из 112 записей, то есть 84 лишних.
//
// Чем это было видно: в двумерной сетке телегида блоки рисовались друг поверх друга и выглядели
// вчетверо ярче задуманного (замер: белый на 55% вместо 10%), в списке архива каждая передача
// повторялась четыре раза, а движок держал в памяти втрое-вчетверо больше нужного (2,5 млн
// записей). Список уже отсортирован по началу, поэтому дубли идут подряд — хватает одного прохода.
//
// Совпадением считаем начало+конец+название: один и тот же слот с РАЗНЫМ названием в разных
// источниках — это не дубль, а расхождение данных, и выбрасывать вторую версию нельзя.
func dedupePrograms(progs []EPGProgram) []EPGProgram {
	if len(progs) < 2 {
		return progs
	}
	out := progs[:1]
	for _, p := range progs[1:] {
		last := out[len(out)-1]
		if p.Start.Equal(last.Start) && p.Stop.Equal(last.Stop) && p.Title == last.Title {
			// Описание бывает заполнено только в одном из источников — оставляем непустое.
			if last.Description == "" && p.Description != "" {
				out[len(out)-1].Description = p.Description
			}
			if last.Icon == "" && p.Icon != "" {
				out[len(out)-1].Icon = p.Icon
			}
			continue
		}
		// Пересечение по времени — тоже склейка источников, только с РАЗНЫМИ названиями: у
		// молдавских каналов один и тот же слот приходил на румынском и на русском. В эфире
		// две передачи одновременно не идут, поэтому вторую версию отбрасываем — иначе в сетке
		// телегида два блока рисуются друг поверх друга и текст превращается в кашу.
		// Стык встык (Stop == Start) пересечением НЕ считается: это нормальная сетка вещания.
		if p.Start.Before(last.Stop) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (e *EPGEngine) collectKnownChannelIDs() map[string]struct{} {
	if e.store == nil {
		return nil
	}
	ids := make(map[string]struct{})
	e.store.mu.RLock()
	for _, cache := range e.store.cache {
		for _, ch := range cache.Channels {
			id := strings.TrimSpace(ch.TvgID)
			if id != "" {
				ids[id] = struct{}{}
			}
		}
	}
	e.store.mu.RUnlock()
	// Каналы реестра: tvg-id закреплённых руками каналов может не встречаться
	// ни в одном донорском плейлисте — без него фильтр загрузки EPG оставил бы
	// такой канал без программы.
	if e.store.registry != nil {
		for _, id := range e.store.registry.TvgIDs() {
			ids[id] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

func (e *EPGEngine) loadSource(url string, windowStart, windowEnd time.Time, knownChannelIDs map[string]struct{},
	nameHints map[string]string, aliases map[string]string, stringsPool *epgStringPool,
	channels map[string]EPGChannel, programs map[string][]EPGProgram) EPGSourceStatus {

	st := EPGSourceStatus{URL: truncURL(url)}

	channelCb := func(ch EPGChannel) {
		if ch.ID == "" {
			return
		}
		ch = normalizeEPGChannel(ch, stringsPool)
		channels[ch.ID] = ch
		st.Channels++
		// Алиасинг по имени: XMLTV-канал с чужим id, но знакомым display-name,
		// дополнительно регистрируется под НАШИМ tvg-id — иконки и программы
		// становятся доступны по id, которым оперирует клиент. В XMLTV каналы
		// идут раньше программ, так что алиас успевает до programCb.
		if len(nameHints) > 0 {
			for _, n := range ch.AltNames {
				want, ok := nameHints[normChannelName(n)]
				if !ok || want == ch.ID {
					continue
				}
				if _, taken := aliases[ch.ID]; !taken {
					aliases[ch.ID] = want
				}
				if _, exists := channels[want]; !exists {
					alias := ch
					alias.ID = want
					channels[want] = alias
				}
				break
			}
		}
	}

	programFilter := func(prog EPGProgram) bool {
		channelID := strings.TrimSpace(prog.ChannelID)
		if channelID == "" || prog.Start.IsZero() {
			return false
		}
		if knownChannelIDs != nil {
			if _, ok := knownChannelIDs[channelID]; !ok {
				if _, aliased := aliases[channelID]; !aliased {
					return false
				}
			}
		}
		return epgProgramInWindow(prog, windowStart, windowEnd)
	}

	programCb := func(prog EPGProgram) {
		if !programFilter(prog) {
			return
		}
		prog = normalizeEPGProgram(prog, stringsPool)
		programs[prog.ChannelID] = append(programs[prog.ChannelID], prog)
		st.Programs++
		// Дублируем программу под нашим tvg-id: клиент спрашивает /epg/now
		// ровно по нему. Оригинальный id тоже остаётся — по нему ходят
		// пользовательские плейлисты с «родными» tvg-id этого источника.
		if want, ok := aliases[prog.ChannelID]; ok && want != prog.ChannelID {
			aliasProg := prog
			aliasProg.ChannelID = want
			programs[want] = append(programs[want], aliasProg)
		}
	}

	resp, err := e.httpClient.Get(url)
	if err != nil {
		log.Error().Err(err).Str("url", truncURL(url)).Msg("epg: fetch failed")
		st.Err = "fetch: " + err.Error()
		e.loadFromCache(url, &st, channelCb, programFilter, programCb)
		return st
	}
	defer resp.Body.Close()

	st.Status = resp.StatusCode
	if resp.StatusCode != 200 {
		log.Error().Int("status", resp.StatusCode).Str("url", truncURL(url)).Msg("epg: fetch non-200")
		st.Err = "http status"
		e.loadFromCache(url, &st, channelCb, programFilter, programCb)
		return st
	}

	// Detect gzip from URL or Content-Type.
	isGzip := strings.HasSuffix(url, ".gz") ||
		strings.HasSuffix(url, ".gzip") ||
		strings.Contains(resp.Header.Get("Content-Type"), "gzip")

	// Зеркалим скачиваемое в дисковый кэш ПРЯМО во время разбора: отдельного
	// повторного скачивания нет, а упавший в следующий раз источник поднимется
	// из этого файла.
	var body io.Reader = resp.Body
	commit := func(bool) {}
	if e.cacheDir != "" {
		if r, c, terr := e.teeToCache(url, resp.Body, isGzip); terr == nil {
			body, commit = r, c
		}
	}

	if isGzip {
		err = ParseXMLTVGzipFiltered(body, channelCb, programFilter, programCb)
	} else {
		err = ParseXMLTVFiltered(body, channelCb, programFilter, programCb)
	}

	if err != nil {
		commit(false)
		log.Error().Err(err).Str("url", truncURL(url)).Msg("epg: parse error")
		st.Err = "parse: " + err.Error()
		// Кэш пробуем только если из живого ответа НИЧЕГО не легло в maps —
		// поверх частичного разбора он надублировал бы программы.
		if st.Channels == 0 && st.Programs == 0 {
			e.loadFromCache(url, &st, channelCb, programFilter, programCb)
		}
		return st
	}
	commit(true)
	st.OK = true
	return st
}

// ---------------------------------------------------------------------------
//  Дисковый кэш XMLTV — источник упал, гид остался
// ---------------------------------------------------------------------------

// epgCacheMaxAge — старше этого кэш не поднимаем: окно программ -1д..+2д, в
// четырёхдневном файле ещё есть чем наполнить «сейчас», дальше — пусто.
const epgCacheMaxAge = 4 * 24 * time.Hour

func (e *EPGEngine) cachePathFor(url string) string {
	h := md5.Sum([]byte(url))
	return filepath.Join(e.cacheDir, hex.EncodeToString(h[:8])+".xml.gz")
}

// teeToCache mirrors everything read from src into a temp file (gzipping plain
// XML so the cache is always .xml.gz) and returns the wrapped reader plus a
// commit: commit(true) publishes the file atomically, commit(false) discards.
func (e *EPGEngine) teeToCache(url string, src io.Reader, srcIsGzip bool) (io.Reader, func(ok bool), error) {
	tmp, err := os.CreateTemp(e.cacheDir, "dl-*")
	if err != nil {
		return nil, nil, err
	}
	var w io.Writer = tmp
	var gz *gzip.Writer
	if !srcIsGzip {
		gz = gzip.NewWriter(tmp)
		w = gz
	}
	target := e.cachePathFor(url)
	done := false
	commit := func(ok bool) {
		if done {
			return
		}
		done = true
		if gz != nil {
			_ = gz.Close()
		}
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmp.Name())
			return
		}
		if err := os.Rename(tmp.Name(), target); err != nil {
			_ = os.Remove(tmp.Name())
		}
	}
	return io.TeeReader(src, w), commit, nil
}

// loadFromCache parses the last cached download of this source (if fresh
// enough) through the SAME callbacks the live path uses. Success → st.OK with
// st.Stale so /epg/status shows the guide is running on cached data.
func (e *EPGEngine) loadFromCache(url string, st *EPGSourceStatus, channelCb func(EPGChannel), programFilter func(EPGProgram) bool, programCb func(EPGProgram)) {
	if e.cacheDir == "" {
		return
	}
	path := e.cachePathFor(url)
	fi, err := os.Stat(path)
	if err != nil || time.Since(fi.ModTime()) > epgCacheMaxAge {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if perr := ParseXMLTVGzipFiltered(f, channelCb, programFilter, programCb); perr != nil {
		log.Warn().Err(perr).Str("url", truncURL(url)).Msg("epg: disk cache unreadable")
		return
	}
	st.OK = true
	st.Stale = true
	log.Warn().Str("url", truncURL(url)).Int("programs", st.Programs).
		Msg("epg: source down — guide served from disk cache")
}

func epgProgramInWindow(prog EPGProgram, windowStart, windowEnd time.Time) bool {
	// Programmes without a stop attr fall back to the start time for the lower
	// bound; otherwise a stop-less multi-week archive would be retained in full.
	if !prog.Stop.IsZero() {
		if prog.Stop.Before(windowStart) {
			return false
		}
	} else if prog.Start.Before(windowStart) {
		return false
	}
	return !prog.Start.After(windowEnd)
}

const (
	// epgMaxFanIn bounds how many UNPINNED (playlist x-tvg-url) EPG feeds one refresh ingests.
	// Operator-pinned [iptv] epg_urls are ALWAYS kept on top of this — see capEPGSources. Each
	// large feed costs memory, so this guards against unbounded user-playlist fan-in.
	epgMaxFanIn = 6
	// epgMaxProgramsPerChannel backstops a pathological source (a normal channel has
	// ~150 programmes across the 3-day window; 800 is generous).
	epgMaxProgramsPerChannel = 800
)

// capEPGSources keeps EVERY operator-pinned static-config URL (never capped — the operator chose
// these, and a blind count-cap can strand the one source that matches the playlist, blanking the
// guide on every channel — exactly the prod incident where 6 alphabetically-first country feeds
// loaded but the RU feed got dropped). The remaining UNPINNED playlist x-tvg-url fan-in is sorted
// (deterministic — map order was random, so which feeds survived changed per restart) and capped.
func capEPGSources(staticURLs []string, all map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(all))
	pinned := make(map[string]struct{}, len(staticURLs))
	for _, u := range staticURLs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		pinned[u] = struct{}{}
		if _, ok := all[u]; ok {
			out[u] = struct{}{} // operator-pinned → always kept
		}
	}

	// Unpinned fan-in (playlist x-tvg-url), deterministically ordered.
	rest := make([]string, 0, len(all))
	for u := range all {
		if _, isPinned := pinned[u]; !isPinned {
			rest = append(rest, u)
		}
	}
	sort.Strings(rest)

	if len(rest) > epgMaxFanIn {
		log.Warn().Int("fan_in", len(rest)).Int("kept", epgMaxFanIn).Int("pinned", len(out)).
			Msg("epg: playlist EPG fan-in capped — pin the sources you need in [iptv] epg_urls")
		rest = rest[:epgMaxFanIn]
	}
	for _, u := range rest {
		out[u] = struct{}{}
	}
	return out
}

type epgStringPool struct {
	values map[string]string
}

func newEPGStringPool() *epgStringPool {
	return &epgStringPool{values: make(map[string]string, 4096)}
}

func (p *epgStringPool) intern(s string) string {
	if s == "" || p == nil {
		return s
	}
	if v, ok := p.values[s]; ok {
		return v
	}
	p.values[s] = s
	return s
}

func normalizeEPGChannel(ch EPGChannel, pool *epgStringPool) EPGChannel {
	ch.ID = pool.intern(strings.TrimSpace(ch.ID))
	ch.Name = pool.intern(strings.TrimSpace(ch.Name))
	ch.Icon = pool.intern(strings.TrimSpace(ch.Icon))
	if len(ch.AltNames) > epgMaxAltNames {
		ch.AltNames = ch.AltNames[:epgMaxAltNames]
	}
	for i, name := range ch.AltNames {
		ch.AltNames[i] = pool.intern(strings.TrimSpace(name))
	}
	return ch
}

func normalizeEPGProgram(prog EPGProgram, pool *epgStringPool) EPGProgram {
	prog.ChannelID = pool.intern(strings.TrimSpace(prog.ChannelID))
	prog.Category = pool.intern(strings.TrimSpace(prog.Category))
	prog.Icon = pool.intern(strings.TrimSpace(prog.Icon))
	prog.Title = strings.TrimSpace(prog.Title)
	prog.Description = strings.TrimSpace(prog.Description)
	return prog
}

func truncURL(url string) string {
	if len(url) > 80 {
		return url[:80] + "..."
	}
	return url
}
