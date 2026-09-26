package iptv

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/httpclient"

	"github.com/rs/zerolog/log"
)

// iptvBalancer — имя балансера, под которым IPTV зарегистрирован в прокси-реестре
// (httpclient). Совпадает с plugin в /proxy/-ссылках каналов, поэтому одна
// запись конфига управляет и исходящими запросами, и отдачей потока.
const iptvBalancer = "iptv"

// ---------------------------------------------------------------------------
//  Store — per-user IPTV playlist persistence
// ---------------------------------------------------------------------------

// Store manages playlist data on disk and in memory.
// Layout:
//
//	{repoRoot}/database/iptv/playlists/{md5(tgID)}_{playlistID}.json
//	{repoRoot}/database/iptv/users/{md5(tgID)}.json
type Store struct {
	dir          string
	maxPlaylists int
	defaultProxy string
	globalURLs   []string
	mergeGlobal  bool // present all global playlists as one deduplicated list (id="all")
	// registry — свой реестр каналов («Мои каналы», id="own"): стабильные id,
	// несколько источников на канал, автофейловер. nil когда выключен.
	registry        *Registry
	registryProxy   string // proxy mode для стримов реестра: "none" | "all"
	registryAutoAdd bool   // незнакомые донорские каналы становятся новыми каналами
	registryOnly    bool   // прятать глобальные плейлисты от клиентов — только реестр
	// preferOfficial — предпочитать поток с домена вещателя панельному, даже
	// если панельный выше качеством (см. official.go).
	preferOfficial bool
	official       *officialHostMatcher
	mu             sync.RWMutex
	cache          map[string]*PlaylistCache // playlistCacheKey → cache
	userPlaylists  map[string][]string       // tgHash → []playlistID
	httpClient     *http.Client

	// health-check (opt-in liveness probing of global streams)
	healthOn   bool
	health     *healthState
	healthConc int
	healthMax  int
	// healthCursor — с какого места начинать отбор проб. Живёт только в
	// горутине health-check. Нужен, когда источников больше бюджета: без
	// сдвига цикл каждый раз меряет ПЕРВЫЕ healthMax, а хвост не получает
	// вердикта никогда (прод: 2146 источников при cap 1500 — 646 не
	// проверялись вовсе, и фейловер не знал, живы они или мертвы).
	healthCursor int
	healthClient *http.Client
	// healthDeep walks an HLS channel to its first segment instead of reading
	// two bytes off the manifest. A dead channel usually still serves its
	// playlist — the failure is one fetch deeper. Costs ~16KB per channel.
	healthDeep bool
	// healthStale additionally re-reads the playlist after a pause to catch a
	// FROZEN channel (200s forever, picture is a still frame). Off by default:
	// it adds a per-channel wait, which does not scale to thousands of them.
	healthStale bool
	// freezeAfter — насколько старой должна быть прошлая подпись окна, чтобы её
	// неизменность считалась заморозкой (0 → freezeMinAge). Поле существует
	// ради тестов: ждать десять минут они не могут.
	freezeAfter time.Duration

	// mergedCache memoizes mergedLocked()'s output for a short TTL. Rebuilding the
	// full deduplicated global channel list on every GetChannels/GetGroups call was
	// the single biggest allocation source in the perf audit (~9.7GB churn). Stored
	// as an atomic pointer so it stays safe under the callers' RLock; invalidated on
	// RefreshGlobal / SetGlobalPlaylists.
	mergedCache atomic.Pointer[mergedSnapshot]
}

// StoreConfig holds store initialization parameters.
type StoreConfig struct {
	MaxPlaylists    int
	DefaultProxy    string
	GlobalPlaylists []string
	MergeGlobal     bool // unify global playlists into one deduplicated "Все каналы" list

	// Собственный реестр каналов (см. registry.go).
	Registry        bool   // включить реестр («Мои каналы»)
	RegistryName    string // отображаемое имя плейлиста реестра
	RegistryProxy   string // "none" | "all"; пусто → DefaultProxy
	RegistryAutoAdd bool   // авто-добавление незнакомых донорских каналов при ingest
	RegistryOnly    bool   // клиентам виден только реестр, глобальные списки скрыты

	// PreferOfficial ставит поток вещателя выше панельного (даже более
	// качественного). OfficialHosts дополняет встроенный список доменов.
	PreferOfficial bool
	OfficialHosts  []string
}

// NewStore creates a new IPTV store.
func NewStore(repoRoot string, cfg StoreConfig) *Store {
	dir := filepath.Join(repoRoot, "database", "iptv")
	_ = os.MkdirAll(filepath.Join(dir, "playlists"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "users"), 0755)

	if cfg.MaxPlaylists <= 0 {
		cfg.MaxPlaylists = 10
	}
	if cfg.DefaultProxy == "" {
		cfg.DefaultProxy = "none"
	}

	s := &Store{
		dir:           dir,
		maxPlaylists:  cfg.MaxPlaylists,
		defaultProxy:  cfg.DefaultProxy,
		globalURLs:    cfg.GlobalPlaylists,
		mergeGlobal:   cfg.MergeGlobal,
		cache:         make(map[string]*PlaylistCache),
		userPlaylists: make(map[string][]string),
		// Через прокси-реестр балансера "iptv": одна запись
		// [[proxycore.entries]] balancers=["iptv"] уводит за прокси и скачивание
		// плейлистов, и health-пробы, и (в epg_engine) XMLTV — тем же путём, что
		// уже ходят сами стримы через /proxy/. Dynamic-вариант ре-резолвит
		// прокси на КАЖДОМ запросе: store живёт до перезапуска, а пул прокси
		// пересоздаётся hot-reload'ом конфига.
		httpClient:   httpclient.NewForBalancerDynamic(iptvBalancer, 60*time.Second),
		healthConc:   8,
		healthMax:    2000,
		health:       &healthState{dead: make(map[string]struct{})},
		healthClient: httpclient.NewForBalancerDynamic(iptvBalancer, 10*time.Second),
	}

	s.official = newOfficialMatcher(cfg.OfficialHosts)
	if cfg.Registry {
		s.preferOfficial = cfg.PreferOfficial
		s.registry = LoadRegistry(dir, cfg.RegistryName)
		s.registryProxy = cfg.RegistryProxy
		if s.registryProxy == "" {
			s.registryProxy = cfg.DefaultProxy
		}
		s.registryAutoAdd = cfg.RegistryAutoAdd
		s.registryOnly = cfg.RegistryOnly
	}

	s.loadFromDisk()
	return s
}

// ---------------------------------------------------------------------------
//  Public API
// ---------------------------------------------------------------------------

// ListPlaylists returns all playlists for a user (personal + global).
func (s *Store) ListPlaylists(tgID int64) []Playlist {
	hash := tgHash(tgID)
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Playlist

	// Свой реестр — первым: это «лицо» сервера, глобальные списки лишь доноры.
	if s.registry != nil && s.registry.Len() > 0 {
		result = append(result, s.registryPlaylist())
	}

	if s.registry != nil && s.registryOnly {
		// Глобальные плейлисты скрыты — только реестр и личные списки.
	} else if s.mergeGlobal && len(s.globalURLs) > 0 {
		// All global lists (e.g. several m3u.su categories) presented as ONE deduplicated playlist.
		result = append(result, Playlist{
			ID:           MergedGlobalID,
			Name:         "Все каналы",
			IsGlobal:     true,
			ChannelCount: len(s.mergedLocked()),
		})
	} else {
		// Global playlists, one entry each.
		for _, url := range s.globalURLs {
			id := playlistIDFromURL(url)
			key := "global_" + id
			if c, ok := s.cache[key]; ok {
				result = append(result, c.Playlist)
			} else {
				result = append(result, Playlist{
					ID:       id,
					Name:     "Global",
					URL:      url,
					IsGlobal: true,
				})
			}
		}
	}

	// User playlists.
	if ids, ok := s.userPlaylists[hash]; ok {
		for _, pid := range ids {
			key := hash + "_" + pid
			if c, ok := s.cache[key]; ok {
				result = append(result, c.Playlist)
			}
		}
	}

	return result
}

// AddPlaylist adds a new playlist for a user.
func (s *Store) AddPlaylist(tgID int64, pl Playlist) error {
	hash := tgHash(tgID)
	s.mu.Lock()

	ids := s.userPlaylists[hash]
	if len(ids) >= s.maxPlaylists {
		s.mu.Unlock()
		return fmt.Errorf("iptv: max playlists reached (%d)", s.maxPlaylists)
	}

	if pl.ID == "" {
		pl.ID = playlistIDFromURL(pl.URL)
	}
	if pl.ProxyMode == "" {
		pl.ProxyMode = s.defaultProxy
	}
	pl.CreatedAt = time.Now().Unix()
	pl.UpdatedAt = pl.CreatedAt

	// Check duplicate.
	if slices.Contains(ids, pl.ID) {
		s.mu.Unlock()
		return fmt.Errorf("iptv: playlist %q already exists", pl.ID)
	}

	s.userPlaylists[hash] = append(ids, pl.ID)
	// Метаданные плейлиста сохраняем СРАЗУ, до скачивания. Иначе упавший
	// первый refresh (протухшая ссылка, мёртвая панель) не оставлял о плейлисте
	// ничего, кроме id в индексе: ListPlaylists отдаёт только то, у чего есть
	// кэш, и плейлист пропадал из списка молча — пользователь не мог его ни
	// обновить, ни удалить, а слот из max_playlists он занимал.
	key := hash + "_" + pl.ID
	stub := &PlaylistCache{Playlist: pl, ParsedAt: time.Now()}
	s.cache[key] = stub
	s.mu.Unlock()

	// Save user index.
	s.saveUserIndex(hash)
	s.saveCacheFile(key, stub)

	// Parse playlist in background.
	go s.refreshPlaylist(hash, pl)

	return nil
}

// RemovePlaylist deletes a playlist for a user.
func (s *Store) RemovePlaylist(tgID int64, playlistID string) error {
	hash := tgHash(tgID)
	s.mu.Lock()

	ids := s.userPlaylists[hash]
	found := false
	newIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == playlistID {
			found = true
			continue
		}
		newIDs = append(newIDs, id)
	}
	if !found {
		s.mu.Unlock()
		return fmt.Errorf("iptv: playlist %q not found", playlistID)
	}

	s.userPlaylists[hash] = newIDs
	key := hash + "_" + playlistID
	delete(s.cache, key)
	s.mu.Unlock()

	// Remove cache file.
	path := filepath.Join(s.dir, "playlists", key+".json")
	_ = os.Remove(path)

	s.saveUserIndex(hash)
	return nil
}

// RefreshPlaylist re-downloads and re-parses a playlist.
func (s *Store) RefreshPlaylist(tgID int64, playlistID string) error {
	hash := tgHash(tgID)
	s.mu.RLock()
	key := hash + "_" + playlistID
	c, ok := s.cache[key]
	s.mu.RUnlock()

	if !ok {
		// «Обновить» на реестре = перечитать доноров (ingest сработает сам).
		if s.registry != nil && playlistID == RegistryPlaylistID {
			go s.RefreshGlobal()
			return nil
		}
		// Check global.
		for _, url := range s.globalURLs {
			gid := playlistIDFromURL(url)
			if gid == playlistID {
				go s.refreshPlaylist("global", Playlist{
					ID: gid, URL: url, IsGlobal: true,
					ProxyMode: s.defaultProxy,
				})
				return nil
			}
		}
		return fmt.Errorf("iptv: playlist %q not found", playlistID)
	}

	go s.refreshPlaylist(hash, c.Playlist)
	return nil
}

// GetChannels returns channels for a playlist with optional filtering and pagination.
func (s *Store) GetChannels(tgID int64, playlistID, group, search string, page, limit int) ([]Channel, int) {
	var channels []Channel
	if s.registry != nil && playlistID == RegistryPlaylistID {
		channels = s.registryChannels()
	} else if s.mergeGlobal && playlistID == MergedGlobalID {
		s.mu.RLock()
		channels = s.mergedLocked()
		s.mu.RUnlock()
	} else {
		key := s.resolvePlaylistKey(tgID, playlistID)
		if key == "" {
			return nil, 0
		}
		s.mu.RLock()
		c, ok := s.cache[key]
		s.mu.RUnlock()
		if !ok {
			return nil, 0
		}
		channels = c.Channels
	}

	// Filter.
	var filtered []Channel
	searchLower := strings.ToLower(search)
	for _, ch := range channels {
		// Один параметр бьёт по всем трём осям НАМЕРЕННО. Раньше «Спорт»
		// означало Group=="Спорт", то есть только каналы БЕЗ страны: русский
		// Матч ТВ (Group="Русские", Genre="Спорт") в свой же жанр не попадал.
		// Клиенту при этом не нужно знать, страну он выбрал или жанр.
		if group != "" && ch.Group != group && ch.Country != group && ch.Genre != group {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(ch.Name), searchLower) {
			continue
		}
		filtered = append(filtered, ch)
	}

	total := len(filtered)

	// Paginate.
	if limit <= 0 {
		limit = 100
	}
	if page <= 0 {
		page = 1
	}
	start := (page - 1) * limit
	if start >= total {
		return nil, total
	}
	end := min(start+limit, total)

	return filtered[start:end], total
}

// Группировочные измерения для GetGroupsBy.
const (
	GroupByLegacy  = ""        // единое поле Group — как было до разделения
	GroupByCountry = "country" // только страны
	GroupByGenre   = "genre"   // только жанры
)

// GetGroups returns group summary for a playlist (legacy single dimension).
func (s *Store) GetGroups(tgID int64, playlistID string) []GroupInfo {
	return s.GetGroupsBy(tgID, playlistID, GroupByLegacy)
}

// GetGroupsBy returns the group summary along ONE taxonomy axis. Страны и
// жанры больше не делят один список: спросив "genre", клиент получает только
// жанры и не увидит среди них «Итальянские».
//
// Каналы без значения по запрошенной оси просто не попадают в ответ — в списке
// жанров нет графы «без жанра», потому что это не жанр.
func (s *Store) GetGroupsBy(tgID int64, playlistID, by string) []GroupInfo {
	var channels []Channel
	if s.registry != nil && playlistID == RegistryPlaylistID {
		channels = s.registryChannels()
	} else if s.mergeGlobal && playlistID == MergedGlobalID {
		s.mu.RLock()
		channels = s.mergedLocked()
		s.mu.RUnlock()
	} else {
		key := s.resolvePlaylistKey(tgID, playlistID)
		if key == "" {
			return nil
		}
		s.mu.RLock()
		c, ok := s.cache[key]
		s.mu.RUnlock()
		if !ok {
			return nil
		}
		channels = c.Channels
	}

	return s.groupsFrom(channels, by)
}

// groupsFrom tallies channels along one taxonomy axis.
func (s *Store) groupsFrom(channels []Channel, by string) []GroupInfo {
	groupMap := make(map[string]*GroupInfo)
	for _, ch := range channels {
		var g string
		switch by {
		case GroupByCountry:
			g = ch.Country
		case GroupByGenre:
			g = ch.Genre
		default:
			g = ch.Group
			if g == "" {
				g = "Ungrouped"
			}
		}
		if g == "" {
			continue // по этой оси канал не классифицирован — молчим, а не выдумываем
		}
		if gi, ok := groupMap[g]; ok {
			gi.Count++
		} else {
			groupMap[g] = &GroupInfo{Name: g, Count: 1, Logo: ch.Logo}
		}
	}

	groups := make([]GroupInfo, 0, len(groupMap))
	for _, gi := range groupMap {
		groups = append(groups, *gi)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Name < groups[j].Name
	})
	return groups
}

// GetChannel returns a single channel by ID from any of the user's playlists.
func (s *Store) GetChannel(tgID int64, channelID string) (*Channel, *Playlist) {
	// Канал реестра: id со стабильным префиксом, URL — лучший живой источник.
	if s.registry != nil && IsRegistryChannelID(channelID) {
		return s.GetRegistryChannel(channelID)
	}

	hash := tgHash(tgID)
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Search in global playlists.
	for _, url := range s.globalURLs {
		gid := playlistIDFromURL(url)
		key := "global_" + gid
		if c, ok := s.cache[key]; ok {
			for i := range c.Channels {
				if c.Channels[i].ID == channelID {
					return &c.Channels[i], &c.Playlist
				}
			}
		}
	}

	// Search in user playlists.
	if ids, ok := s.userPlaylists[hash]; ok {
		for _, pid := range ids {
			key := hash + "_" + pid
			if c, ok := s.cache[key]; ok {
				for i := range c.Channels {
					if c.Channels[i].ID == channelID {
						return &c.Channels[i], &c.Playlist
					}
				}
			}
		}
	}

	return nil, nil
}

// Stats returns total users and cached playlists count.
func (s *Store) Stats() (users, playlists int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.userPlaylists), len(s.cache)
}

// ---------------------------------------------------------------------------
//  Internal: refresh / parse
// ---------------------------------------------------------------------------

// defaultFetchUA — чем представляемся панели при скачивании M3U, когда свой UA не задан.
// Go-шный «Go-http-client/1.1» панели режут («не читает плейлист», особенно ссылки без .m3u
// вида get.php?…&type=m3u_plus) — обычные IPTV-плееры проходят, потому что шлют плеерный UA.
const defaultFetchUA = "VLC/3.0.20 LibVLC/3.0.20"

// emptyParseKeepDays — сколько прошлый разбор плейлиста переживает ответ «200
// без единого канала». Ровно столько же реестр держит исчезнувший auto-источник
// (regSourceKeepDays), так что окна складываются, а не спорят.
const emptyParseKeepDays = 7

func strOr(v, def string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func (s *Store) refreshPlaylist(ownerHash string, pl Playlist) {
	log.Info().Str("id", pl.ID).Str("url", pl.URL).Msg("iptv: refreshing playlist")

	req, err := http.NewRequest(http.MethodGet, pl.URL, nil)
	if err != nil {
		log.Error().Err(err).Str("id", pl.ID).Msg("iptv: bad playlist url")
		return
	}
	req.Header.Set("User-Agent", strOr(pl.UserAgent, defaultFetchUA))
	resp, err := s.httpClient.Do(req)
	if err != nil {
		log.Error().Err(err).Str("id", pl.ID).Msg("iptv: failed to fetch playlist")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Error().Int("status", resp.StatusCode).Str("id", pl.ID).Msg("iptv: playlist fetch non-200")
		return
	}

	// Скачиваем плейлист целиком ДО парсинга: раньше ParseM3U читал прямо из
	// resp.Body, и 60-секундный таймаут клиента покрывал скачивание И разбор —
	// огромный плейлист с медленной панели обрывался на середине и целиком
	// выбрасывался. Кап 64MB — защита от бесконечного ответа.
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if rerr != nil && len(data) == 0 {
		log.Error().Err(rerr).Str("id", pl.ID).Msg("iptv: failed to download playlist body")
		return
	}

	var channels []Channel
	groupSet := make(map[string]struct{})

	header, err := ParseM3U(bytes.NewReader(data), func(ch Channel) {
		ch.PlaylistID = pl.ID
		channels = append(channels, ch)
		if ch.Group != "" {
			groupSet[ch.Group] = struct{}{}
		}
	})
	if rerr != nil && err == nil {
		err = rerr
	}
	if err != nil {
		// Спасаем распарсенное: одна битая строка (или обрыв в хвосте) — не
		// повод выкидывать весь плейлист. VLC дочитывает сколько может — теперь
		// и мы. Пустой результат по-прежнему не затирает прошлый кэш.
		if len(channels) == 0 {
			log.Error().Err(err).Str("id", pl.ID).Msg("iptv: parse error")
			return
		}
		log.Warn().Err(err).Str("id", pl.ID).Int("salvaged", len(channels)).
			Msg("iptv: playlist parsed partially — keeping salvaged channels")
	}

	// 200 и ни одного канала — это почти никогда не «плейлист опустел». Так
	// выглядит страница техработ, отданная с кодом 200, редирект на HTML и
	// панель, сообщающая «подписка кончилась». Затерев таким ответом прошлый
	// разбор, мы одним махом выкашиваем источники ВСЕХ каналов донора: реестр
	// пересобирает auto из кэшей, и пустой кэш означает «донор больше ничего не
	// даёт». Прод 19.09.2026: m3u.su ответил 503-страницей, кэш стал нулевым, и
	// 235 источников исчезли молча. Держим прошлый разбор emptyParseKeepDays —
	// донору хватает пережить сбой, а по-настоящему умерший всё равно отпускает
	// источники, просто на неделю позже.
	if len(channels) == 0 {
		s.mu.RLock()
		prev := s.cache[ownerHash+"_"+pl.ID]
		s.mu.RUnlock()
		if prev != nil && len(prev.Channels) > 0 &&
			time.Since(prev.ParsedAt) < emptyParseKeepDays*24*time.Hour {
			log.Error().Str("id", pl.ID).Str("url", pl.URL).
				Int("kept", len(prev.Channels)).
				Time("parsed_at", prev.ParsedAt).
				Msg("iptv: плейлист отдал 200 без единого канала — оставляем прошлый разбор")
			return
		}
	}

	// EPG-адреса: ручные (заданы при добавлении) главнее url-tvg из заголовка M3U.
	if len(pl.EPGManual) > 0 {
		pl.EPGUrls = pl.EPGManual
	} else if len(header.EPGUrls) > 0 {
		pl.EPGUrls = header.EPGUrls
	}
	pl.ChannelCount = len(channels)
	pl.UpdatedAt = time.Now().Unix()

	// Build sorted groups list.
	groups := make([]string, 0, len(groupSet))
	for g := range groupSet {
		groups = append(groups, g)
	}
	sort.Strings(groups)

	cache := &PlaylistCache{
		Playlist: pl,
		Channels: channels,
		Groups:   groups,
		ParsedAt: time.Now(),
	}

	key := ownerHash + "_" + pl.ID
	s.mu.Lock()
	s.cache[key] = cache
	s.mu.Unlock()

	s.saveCacheFile(key, cache)

	// Свежие донорские каналы — сразу в реестр: источники обновляются той же
	// периодикой, что и сами плейлисты (протухшие Xtream-токены и т.п.).
	if ownerHash == "global" && s.registry != nil {
		s.IngestRegistry(false)
	}

	log.Info().Str("id", pl.ID).Int("channels", len(channels)).Int("groups", len(groups)).Msg("iptv: playlist refreshed")
}

// ---------------------------------------------------------------------------
//  Internal: disk persistence
// ---------------------------------------------------------------------------

func (s *Store) saveCacheFile(key string, cache *PlaylistCache) {
	path := filepath.Join(s.dir, "playlists", key+".json")
	data, err := json.Marshal(cache)
	if err != nil {
		log.Error().Err(err).Str("key", key).Msg("iptv: marshal cache error")
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Error().Err(err).Str("key", key).Msg("iptv: write cache error")
	}
}

func (s *Store) saveUserIndex(hash string) {
	s.mu.RLock()
	ids := s.userPlaylists[hash]
	s.mu.RUnlock()

	path := filepath.Join(s.dir, "users", hash+".json")
	data, err := json.Marshal(ids)
	if err != nil {
		log.Error().Err(err).Msg("iptv: marshal user index error")
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Error().Err(err).Msg("iptv: write user index error")
	}
}

func (s *Store) loadFromDisk() {
	// Load user indexes.
	entries, err := os.ReadDir(filepath.Join(s.dir, "users"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		hash := strings.TrimSuffix(e.Name(), ".json")
		data, err := os.ReadFile(filepath.Join(s.dir, "users", e.Name()))
		if err != nil {
			continue
		}
		var ids []string
		if err := json.Unmarshal(data, &ids); err != nil {
			continue
		}
		s.userPlaylists[hash] = ids
	}

	// Load playlist caches.
	entries, err = os.ReadDir(filepath.Join(s.dir, "playlists"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ".json")
		data, err := os.ReadFile(filepath.Join(s.dir, "playlists", e.Name()))
		if err != nil {
			continue
		}
		var cache PlaylistCache
		if err := json.Unmarshal(data, &cache); err != nil {
			log.Warn().Err(err).Str("file", e.Name()).Msg("iptv: corrupt cache file")
			continue
		}
		s.cache[key] = &cache
	}

	log.Info().Int("users", len(s.userPlaylists)).Int("caches", len(s.cache)).Msg("iptv: loaded from disk")
}

// RefreshGlobal downloads and parses all global playlists.
func (s *Store) RefreshGlobal() {
	s.mu.RLock()
	urls := append([]string(nil), s.globalURLs...)
	proxy := s.defaultProxy
	s.mu.RUnlock()
	for _, url := range urls {
		id := playlistIDFromURL(url)
		s.refreshPlaylist("global", Playlist{
			ID: id, Name: "Global", URL: url, IsGlobal: true,
			ProxyMode: proxy,
		})
	}
	s.invalidateMerged() // global caches changed → drop the memoized merged list
}

// RefreshAll re-fetches EVERY cached playlist (global + user) from upstream.
// Xtream-панели ротируют токены внутри URL каналов: без периодического
// перечитывания все ссылки протухают за часы — «в других плеерах работает»,
// потому что те перечитывают m3u при каждом запуске. Последовательно, чтобы
// не бомбить панели пачкой одновременных скачиваний.
func (s *Store) RefreshAll() {
	type job struct {
		owner string
		pl    Playlist
	}
	s.mu.RLock()
	jobs := make([]job, 0, len(s.cache))
	for key, c := range s.cache {
		owner := strings.TrimSuffix(key, "_"+c.Playlist.ID)
		jobs = append(jobs, job{owner: owner, pl: c.Playlist})
	}
	s.mu.RUnlock()
	for _, j := range jobs {
		s.refreshPlaylist(j.owner, j.pl)
	}
	s.invalidateMerged()
	log.Info().Int("playlists", len(jobs)).Msg("iptv: periodic refresh done")
}

// SetGlobalPlaylists replaces the global playlist URL list (and merge mode) at
// runtime — called from the config-reload path so that edits to
// [iptv] global_playlists / merge_global take effect without a full server
// restart. Caches for URLs that are no longer global are pruned, and the new
// URLs are fetched immediately.
func (s *Store) SetGlobalPlaylists(urls []string, mergeGlobal bool) {
	s.mu.Lock()
	keep := make(map[string]struct{}, len(urls))
	for _, u := range urls {
		keep["global_"+playlistIDFromURL(u)] = struct{}{}
	}
	for key := range s.cache {
		if strings.HasPrefix(key, "global_") {
			if _, ok := keep[key]; !ok {
				delete(s.cache, key)
			}
		}
	}
	s.globalURLs = append([]string(nil), urls...)
	s.mergeGlobal = mergeGlobal
	s.mu.Unlock()

	s.invalidateMerged() // playlist set changed → drop the memoized merged list immediately

	// Fetch the (new) global lists so /api/iptv/playlists + /channels reflect
	// them without waiting for a restart or the next scheduled refresh.
	s.RefreshGlobal()
}

// ---------------------------------------------------------------------------
//  Helpers
// ---------------------------------------------------------------------------

func (s *Store) resolvePlaylistKey(tgID int64, playlistID string) string {
	// Check global first.
	for _, url := range s.globalURLs {
		gid := playlistIDFromURL(url)
		if gid == playlistID {
			return "global_" + gid
		}
	}
	// User playlist.
	hash := tgHash(tgID)
	s.mu.RLock()
	ids := s.userPlaylists[hash]
	s.mu.RUnlock()
	if slices.Contains(ids, playlistID) {
		return hash + "_" + playlistID
	}
	return ""
}

func tgHash(tgID int64) string {
	h := md5.Sum(fmt.Appendf(nil, "%d", tgID))
	return hex.EncodeToString(h[:])
}

func playlistIDFromURL(url string) string {
	h := md5.Sum([]byte(url))
	return hex.EncodeToString(h[:8])
}

// FetchM3U downloads and parses an M3U from a URL, returning channels.
func FetchM3U(url string) (M3UHeader, []Channel, error) {
	// Через тот же прокси-балансер, что и остальные исходящие IPTV-запросы —
	// голый http.Get ходил бы мимо прокси и мимо таймаута.
	resp, err := httpclient.NewForBalancerDynamic(iptvBalancer, 60*time.Second).Get(url)
	if err != nil {
		return M3UHeader{}, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		_, _ = io.ReadAll(resp.Body)
		return M3UHeader{}, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var channels []Channel
	header, err := ParseM3U(resp.Body, func(ch Channel) {
		channels = append(channels, ch)
	})
	return header, channels, err
}

// SetPlaylistProxyMode переключает режим прокси у УЖЕ добавленного плейлиста.
//
// До этого режим можно было задать только при добавлении, а клиенты его вообще
// не передавали — поле оставалось пустым, и AddPlaylist подставлял серверный
// default_proxy ("all"). Получалось, что личный плейлист пользователя всегда
// шёл через наш сервер, и отключить это было нечем: настройки не существовало
// ни в одном клиенте.
//
// mode: "all" — гнать через сервер, "none" — отдавать клиенту прямой адрес.
// Глобальные плейлисты и реестр не трогаем: их адреса наружу не отдаются
// вообще (утечка одного raw-URL восстанавливает украденный каталог).
func (s *Store) SetPlaylistProxyMode(tgID int64, playlistID, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "all" && mode != "none" {
		return fmt.Errorf("iptv: bad proxy mode %q (ожидается all или none)", mode)
	}
	hash := tgHash(tgID)
	key := hash + "_" + playlistID

	s.mu.Lock()
	c, ok := s.cache[key]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("iptv: playlist %q not found", playlistID)
	}
	if c.Playlist.IsGlobal {
		s.mu.Unlock()
		return fmt.Errorf("iptv: глобальный плейлист режимом прокси не управляется")
	}
	c.Playlist.ProxyMode = mode
	c.Playlist.UpdatedAt = time.Now().Unix()
	snapshot := *c
	s.mu.Unlock()

	s.saveCacheFile(key, &snapshot)
	return nil
}
