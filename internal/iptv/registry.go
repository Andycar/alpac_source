package iptv

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  Registry — СВОЙ реестр каналов («Мои каналы»).
//
//  Идея: канал перестаёт быть строчкой чужого M3U. Реестр держит собственный
//  список каналов со СТАБИЛЬНЫМИ id, а у каждого канала — несколько
//  источников потока: закреплённые руками (pinned) и собранные автоматически
//  из донорских плейлистов (auto). Донор умер — канал остался: воспроизведение
//  берёт лучший живой источник по данным health-check'а (автофейловер).
//  Наружу клиент видит один неизменный плейлист, id каналов не «уезжают».
//
//  Хранение: {repoRoot}/database/iptv/registry.json — один файл на весь
//  реестр (сотни каналов, не миллионы).
// ---------------------------------------------------------------------------

// RegistryPlaylistID is the synthetic playlist id under which the registry is
// exposed to clients (like MergedGlobalID for the merged global view).
const RegistryPlaylistID = "own"

// regSourceKeepDays — сколько дней auto-источник переживает исчезновение из
// донора. Доноры типа m3u.su временно теряют каналы при своих сбоях — источник
// с недавним LastSeen выкидывать рано, он обычно возвращается.
const regSourceKeepDays = 7

// RegSource is one upstream stream candidate of a registry channel.
type RegSource struct {
	// Resolver — имя динамического резолвера (см. resolver.go). Когда задано,
	// URL добывается на лету, а поле URL служит лишь запасным адресом.
	Resolver string `json:"resolver,omitempty"`
	// GeoLocked — за этим адресом надо ходить через прокси страны вещателя.
	// Адрес статический и известен, но вещатель отдаёт поток только «своим»:
	// Ростелеком, например, редиректит чужие IP на заглушку rtk_block.m3u8.
	// Отличается от Resolver тем, что добывать нечего — нужен лишь маршрут,
	// причём и на воспроизведении, и на health-check'е (иначе цикл сочтёт
	// живой канал мёртвым и снимет его с эфира).
	GeoLocked bool     `json:"geo_locked,omitempty"`
	URL       string   `json:"url"`
	Quality   string   `json:"quality,omitempty"`
	UserAgent string   `json:"user_agent,omitempty"`
	Referer   string   `json:"referer,omitempty"`
	Catchup   *Catchup `json:"catchup,omitempty"`
	From      string   `json:"from,omitempty"` // donor playlist id; "manual" for pinned
	FirstSeen int64    `json:"first_seen,omitempty"`
	LastSeen  int64    `json:"last_seen,omitempty"` // when a donor last listed this URL
}

// RegChannel is a curated channel owned by THIS server.
type RegChannel struct {
	ID      string   `json:"id"`   // stable: "own_" + hash(norm key)
	Name    string   `json:"name"` // display name
	NormKey string   `json:"norm_key"`
	Aliases []string `json:"aliases,omitempty"` // extra donor names matched to this channel
	Logo    string   `json:"logo,omitempty"`
	Group   string   `json:"group,omitempty"`
	// Country / Genre — раздельная таксономия (см. taxonomy.go). Пустые поля
	// у старых записей достраиваются на лету в regChannelView.
	Country  string `json:"country,omitempty"`
	Genre    string `json:"genre,omitempty"`
	TvgID    string `json:"tvg_id,omitempty"`
	Number   int    `json:"number,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	// Pinned — источники, добавленные руками: всегда первые, импорт их не трогает.
	Pinned []RegSource `json:"pinned,omitempty"`
	// Auto — источники, собранные из донорских плейлистов; пересобираются на
	// каждом ingest с переносом FirstSeen/LastSeen по URL.
	Auto      []RegSource `json:"auto,omitempty"`
	CreatedAt int64       `json:"created_at,omitempty"`
	UpdatedAt int64       `json:"updated_at,omitempty"`
}

// Sources returns pinned followed by auto (the resolution order base).
func (c *RegChannel) Sources() []RegSource {
	out := make([]RegSource, 0, len(c.Pinned)+len(c.Auto))
	out = append(out, c.Pinned...)
	out = append(out, c.Auto...)
	return out
}

type registryFile struct {
	Name      string        `json:"name,omitempty"`
	Channels  []*RegChannel `json:"channels"`
	UpdatedAt int64         `json:"updated_at,omitempty"`
}

// Registry holds the curated channel list. Safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	path     string
	name     string
	channels []*RegChannel          // ordered by Number
	byID     map[string]*RegChannel // id → channel
	byKey    map[string]*RegChannel // norm key / alias key → channel
	byTvg    map[string]*RegChannel // tvg-id → channel
}

// regChannelID derives the stable public id of a registry channel.
func regChannelID(normKey string) string {
	h := md5.Sum([]byte("own|" + normKey))
	return "own_" + hex.EncodeToString(h[:8])
}

// IsRegistryChannelID reports whether a channel id belongs to the registry.
func IsRegistryChannelID(id string) bool { return strings.HasPrefix(id, "own_") }

// DefaultRegistryName — как плейлист реестра называется, пока [iptv]
// registry_name не задан. Это ЛИЦО сервиса в списке плейлистов у клиента,
// поэтому имя брендовое, а не описательное.
const DefaultRegistryName = "Alpac IPTV"

// LoadRegistry reads registry.json from the iptv database dir (missing file →
// empty registry, ready to be seeded).
func LoadRegistry(iptvDir, name string) *Registry {
	// Сравнивать пришедшее имя с дефолтом нельзя: тогда оператор, задавший в
	// конфиге ровно дефолтное имя, молча получал бы старое имя из снимка.
	// Признак «конфиг задал имя» — непустая строка, и только он решает.
	configured := strings.TrimSpace(name) != ""
	if !configured {
		name = DefaultRegistryName
	}
	r := &Registry{
		path: filepath.Join(iptvDir, "registry.json"),
		name: name,
	}
	if data, err := os.ReadFile(r.path); err == nil {
		var f registryFile
		if err := json.Unmarshal(data, &f); err != nil {
			log.Error().Err(err).Str("path", r.path).Msg("iptv: corrupt registry.json — starting empty")
		} else {
			r.channels = f.Channels
			// Имя из снимка поднимаем ТОЛЬКО когда конфиг молчит.
			if f.Name != "" && !configured {
				r.name = f.Name
			}
		}
	}
	r.reindexLocked()
	migrated := r.migrateTaxonomyLocked()
	log.Info().Int("channels", len(r.channels)).Msg("iptv: registry loaded")
	if migrated > 0 {
		// Пишем результат на диск, а не только держим в памяти: иначе страна и
		// жанр не видны ни админке, ни экспорту M3U, и разбор пришлось бы
		// повторять на каждом старте.
		r.save()
		log.Info().Int("channels", migrated).Msg("iptv: таксономия разобрана на страну и жанр")
	}
	return r
}

// migrateTaxonomyLocked fills Country/Genre for records written before the two
// axes were split. Каналы, у которых хоть одна ось уже заполнена, не трогаем —
// ручную правку оператора автоматика перебивать не должна. Возвращает число
// изменённых каналов. Вызывается при эксклюзивном доступе (загрузка).
func (r *Registry) migrateTaxonomyLocked() int {
	n := 0
	for _, c := range r.channels {
		if c.Country != "" && c.Genre != "" {
			continue
		}
		country, genre := ClassifyChannel(c)
		changed := false
		// Дозаполняем ПО ОСЯМ, а не запись целиком: канал мог получить жанр
		// прошлым разбором и остаться без страны (её тогда неоткуда было взять).
		// Уже заполненную ось не трогаем — там может быть правка оператора.
		if c.Country == "" && country != "" {
			c.Country = country
			changed = true
		}
		if c.Genre == "" && genre != "" {
			c.Genre = genre
			changed = true
		}
		if changed {
			n++
		}
	}
	return n
}

// Name returns the display name of the registry playlist.
func (r *Registry) Name() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.name
}

// Len returns the number of ENABLED channels.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, c := range r.channels {
		if !c.Disabled {
			n++
		}
	}
	return n
}

// reindexLocked rebuilds lookup maps and sorts channels. Caller holds r.mu (W)
// or has exclusive access (load).
func (r *Registry) reindexLocked() {
	sort.SliceStable(r.channels, func(i, j int) bool {
		if r.channels[i].Number != r.channels[j].Number {
			return r.channels[i].Number < r.channels[j].Number
		}
		return r.channels[i].Name < r.channels[j].Name
	})
	r.byID = make(map[string]*RegChannel, len(r.channels))
	r.byKey = make(map[string]*RegChannel, len(r.channels))
	r.byTvg = make(map[string]*RegChannel, len(r.channels))
	for _, c := range r.channels {
		r.byID[c.ID] = c
		if c.NormKey != "" {
			r.byKey[c.NormKey] = c
		}
		for _, a := range c.Aliases {
			if k := normChannelName(a); k != "" {
				r.byKey[k] = c
			}
		}
		if t := strings.TrimSpace(c.TvgID); t != "" {
			r.byTvg[t] = c
		}
	}
}

// save persists the registry to disk. Caller must NOT hold r.mu.
func (r *Registry) save() {
	r.mu.RLock()
	f := registryFile{Name: r.name, Channels: r.channels, UpdatedAt: time.Now().Unix()}
	data, err := json.MarshalIndent(f, "", " ")
	r.mu.RUnlock()
	if err != nil {
		log.Error().Err(err).Msg("iptv: registry marshal error")
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Error().Err(err).Msg("iptv: registry write error")
		return
	}
	if err := os.Rename(tmp, r.path); err != nil {
		log.Error().Err(err).Msg("iptv: registry rename error")
	}
}

// List returns a deep-enough copy of all channels (sources shared read-only).
func (r *Registry) List() []RegChannel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RegChannel, 0, len(r.channels))
	for _, c := range r.channels {
		out = append(out, *c)
	}
	return out
}

// Get returns a copy of one channel.
func (r *Registry) Get(id string) (RegChannel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byID[id]
	if !ok {
		return RegChannel{}, false
	}
	return *c, true
}

// TvgIDs returns the tvg-ids of enabled channels (EPG load filter feed).
func (r *Registry) TvgIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.channels))
	for _, c := range r.channels {
		if c.Disabled {
			continue
		}
		if t := strings.TrimSpace(c.TvgID); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
//  CRUD (admin)
// ---------------------------------------------------------------------------

// Upsert creates or updates a channel. For creation Name is required; ID and
// NormKey are derived. Returns the stored copy.
func (r *Registry) Upsert(in RegChannel) (RegChannel, error) {
	now := time.Now().Unix()
	r.mu.Lock()
	var c *RegChannel
	if in.ID != "" {
		c = r.byID[in.ID]
	}
	if c == nil {
		key := normChannelName(in.Name)
		if key == "" {
			r.mu.Unlock()
			return RegChannel{}, errEmptyName
		}
		if exist, ok := r.byKey[key]; ok {
			c = exist // same channel under a different spelling → update it
		} else {
			c = &RegChannel{ID: regChannelID(key), NormKey: key, CreatedAt: now}
			r.channels = append(r.channels, c)
		}
	}
	if in.Name != "" {
		c.Name = in.Name
	}
	if in.Logo != "" {
		c.Logo = in.Logo
	}
	if in.Group != "" {
		c.Group = in.Group
	}
	if in.TvgID != "" {
		c.TvgID = in.TvgID
	}
	if in.Number != 0 {
		c.Number = in.Number
	}
	if in.Aliases != nil {
		c.Aliases = in.Aliases
	}
	c.Disabled = in.Disabled
	if in.Pinned != nil {
		for i := range in.Pinned {
			in.Pinned[i].From = "manual"
			if in.Pinned[i].FirstSeen == 0 {
				in.Pinned[i].FirstSeen = now
			}
			in.Pinned[i].LastSeen = now
		}
		c.Pinned = in.Pinned
	}
	c.UpdatedAt = now
	out := *c
	r.reindexLocked()
	r.mu.Unlock()
	r.save()
	return out, nil
}

// Delete removes a channel by id.
func (r *Registry) Delete(id string) bool {
	r.mu.Lock()
	idx := -1
	for i, c := range r.channels {
		if c.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		r.mu.Unlock()
		return false
	}
	r.channels = append(r.channels[:idx], r.channels[idx+1:]...)
	r.reindexLocked()
	r.mu.Unlock()
	r.save()
	return true
}

// SetDisabled toggles a channel without deleting it (keeps sources/history).
func (r *Registry) SetDisabled(id string, disabled bool) bool {
	r.mu.Lock()
	c, ok := r.byID[id]
	if ok {
		c.Disabled = disabled
		c.UpdatedAt = time.Now().Unix()
	}
	r.mu.Unlock()
	if ok {
		r.save()
	}
	return ok
}

type registryError string

func (e registryError) Error() string { return string(e) }

const errEmptyName = registryError("iptv: registry channel name is empty")

// ---------------------------------------------------------------------------
//  Ingest — сбор источников из донорских плейлистов
// ---------------------------------------------------------------------------

// IngestStats summarizes one ingest pass.
type IngestStats struct {
	Donors    int `json:"donor_channels"` // сколько донорских записей просмотрено
	Matched   int `json:"matched"`        // легли источником в существующий канал
	NewAdded  int `json:"new_added"`      // новых каналов создано (addNew)
	Unmatched int `json:"unmatched"`      // донорских записей без своего канала
	Released  int `json:"released"`       // источников донора, убранного из global_playlists, отпущено сразу
}

// Ingest пересобирает auto-источники каналов из полного набора донорских
// записей (все глобальные плейлисты разом). addNew=true — незнакомые донорские
// каналы становятся новыми каналами реестра (первичный посев или auto_add).
// Pinned-источники не трогаются. Auto прежних прогонов, исчезнувшие у доноров,
// доживают regSourceKeepDays от LastSeen — временный сбой донора не выкашивает
// источник.
func (r *Registry) Ingest(donors []Channel, addNew bool) IngestStats {
	return r.IngestActive(donors, addNew, nil)
}

// IngestActive — Ingest, который знает текущий список доноров: active — id плейлистов, стоящих
// сейчас в global_playlists (nil — неизвестно, тогда всё как в Ingest). Auto-источник донора,
// которого убрали из сборщика, отпускается сразу: семь дней дожития нужны донору с временным
// сбоем, а убранный не вернётся. Без этого после отказа от платной панели (20.09.2026) её
// заглушка «продлите подписку» — у всех каналов один и тот же ролик, и проверка живости считает
// его живым — ещё неделю показывалась вместо 179 каналов и отбирала эфир у 151 канала с живыми
// бесплатными источниками, потому что стояла выше них как 4K/FHD.
func (r *Registry) IngestActive(donors []Channel, addNew bool, active map[string]bool) IngestStats {
	now := time.Now().Unix()
	st := IngestStats{Donors: len(donors)}

	r.mu.Lock()
	// Прошлые auto-источники по каналам: перенос FirstSeen/LastSeen и дожитие.
	prevAuto := make(map[string]map[string]RegSource, len(r.channels)) // chID → url → source
	for _, c := range r.channels {
		m := make(map[string]RegSource, len(c.Auto))
		for _, s := range c.Auto {
			m[s.URL] = s
		}
		prevAuto[c.ID] = m
		c.Auto = c.Auto[:0]
	}

	seen := make(map[string]map[string]struct{}, len(r.channels)) // chID → url set (dedup)
	appendSource := func(c *RegChannel, dc Channel) {
		urls := seen[c.ID]
		if urls == nil {
			urls = make(map[string]struct{})
			seen[c.ID] = urls
		}
		if _, dup := urls[dc.URL]; dup || dc.URL == "" {
			return
		}
		urls[dc.URL] = struct{}{}
		src := RegSource{
			URL:       dc.URL,
			Quality:   dc.Quality,
			UserAgent: dc.UserAgent,
			Referer:   dc.Referer,
			Catchup:   dc.Catchup,
			From:      dc.PlaylistID,
			FirstSeen: now,
			LastSeen:  now,
		}
		if old, ok := prevAuto[c.ID][dc.URL]; ok && old.FirstSeen > 0 {
			src.FirstSeen = old.FirstSeen
		}
		c.Auto = append(c.Auto, src)
		// Заполняем пустые поля канала лучшим, что видим у доноров.
		if c.Logo == "" && dc.Logo != "" {
			c.Logo = dc.Logo
		}
		if c.TvgID == "" && dc.TvgID != "" {
			c.TvgID = dc.TvgID
			r.byTvg[c.TvgID] = c
		}
		if c.Group == "" && dc.Group != "" {
			c.Group = dc.Group
		}
	}

	nextNumber := 0
	for _, c := range r.channels {
		if c.Number > nextNumber {
			nextNumber = c.Number
		}
	}

	for _, dc := range donors {
		var c *RegChannel
		if t := strings.TrimSpace(dc.TvgID); t != "" {
			c = r.byTvg[t]
		}
		key := normChannelName(dc.Name)
		if c == nil && key != "" {
			c = r.byKey[key]
		}
		if c == nil {
			if !addNew || key == "" {
				st.Unmatched++
				continue
			}
			nextNumber++
			name := cleanChannelName(dc.Name)
			if name == "" {
				name = dc.Name
			}
			c = &RegChannel{
				ID:        regChannelID(key),
				Name:      name,
				NormKey:   key,
				Logo:      dc.Logo,
				Group:     dc.Group,
				TvgID:     strings.TrimSpace(dc.TvgID),
				Number:    nextNumber,
				CreatedAt: now,
			}
			r.channels = append(r.channels, c)
			r.byID[c.ID] = c
			r.byKey[key] = c
			if c.TvgID != "" {
				r.byTvg[c.TvgID] = c
			}
			st.NewAdded++
			appendSource(c, dc)
			continue
		}
		st.Matched++
		appendSource(c, dc)
	}

	// Дожитие исчезнувших auto-источников.
	keepAfter := now - regSourceKeepDays*86400
	for _, c := range r.channels {
		urls := seen[c.ID]
		for url, old := range prevAuto[c.ID] {
			if urls != nil {
				if _, present := urls[url]; present {
					continue
				}
			}
			if !donorConfigured(old.From, active) {
				st.Released++
				continue
			}
			if old.LastSeen >= keepAfter {
				c.Auto = append(c.Auto, old)
			}
		}
		// Стабильный порядок auto: качество ↓, затем свежесть ↓.
		sort.SliceStable(c.Auto, func(i, j int) bool {
			qi, qj := qualityRank(c.Auto[i].Quality), qualityRank(c.Auto[j].Quality)
			if qi != qj {
				return qi > qj
			}
			return c.Auto[i].LastSeen > c.Auto[j].LastSeen
		})
		c.UpdatedAt = now
	}
	r.reindexLocked()
	r.mu.Unlock()
	r.save()

	log.Info().Int("donors", st.Donors).Int("matched", st.Matched).
		Int("new", st.NewAdded).Int("unmatched", st.Unmatched).Int("released", st.Released).
		Msg("iptv: registry ingest")
	return st
}

// donorConfigured — донор источника всё ещё в сборщике. Список неизвестен (nil) или у источника
// нет пометки донора — считаем, что да: без уверенности источник не выкидываем.
func donorConfigured(from string, active map[string]bool) bool {
	return active == nil || from == "" || active[from]
}
