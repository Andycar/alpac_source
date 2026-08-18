// Package calendar provides per-user TV show tracking with episode cache
// and notification support via TMDB polling.
package calendar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"strconv"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
//  Subscription — a tracked show for one user
// ---------------------------------------------------------------------------

// Kind distinguishes what a subscription tracks. Empty means KindTV — every
// subscription predates the movie support, so the zero value must keep meaning
// "series" or old records would stop being polled.
const (
	KindTV    = "tv"
	KindMovie = "movie"
)

// VoicesRev is the current meaning of Subscription.Voices. BUMP IT whenever the
// voice resolve changes what it returns.
//
// rev 1: the series drill is pinned to a concrete episode. Before that it asked
// for the show with no s/e, so balancers answered with the season/episode LISTING
// and «Дом Дракона» users were told the new "voices" were «Дрифтмарк, Зелёный
// совет, Лорд Приливов…» — episode titles.
const VoicesRev = 1

// Subscription represents a title tracked by a user.
type Subscription struct {
	ImdbID      string    `json:"imdb_id"`
	TmdbID      int       `json:"tmdb_id"`
	Kind        string    `json:"kind,omitempty"` // "tv" (default) | "movie"
	Title       string    `json:"title"`
	PosterPath  string    `json:"poster_path,omitempty"`
	LastSeason  int       `json:"last_season"`
	LastEpisode int       `json:"last_episode"`
	AddedAt     time.Time `json:"added_at"`

	// TrackVoices opts this subscription into new-translation polling, which is
	// far more expensive than the TMDB check (it drills the balancers), so it is
	// per-subscription rather than global.
	TrackVoices bool `json:"track_voices,omitempty"`
	// Voices is the last seen set of voice-over names. A name appearing here for
	// the first time is what "a new translation is out" means.
	Voices []string `json:"voices,omitempty"`
	// WantVoices ограничивает отслеживание КОНКРЕТНЫМИ озвучками, которых ждёт
	// пользователь («жду Кубик в Кубе, остальные не нужны»). Пусто = любая новая.
	// Сравнение нестрогое (см. voiceWanted): источники пишут одну и ту же студию
	// то с пометкой «(18+)», то без, и точное совпадение промахивалось бы.
	WantVoices []string `json:"want_voices,omitempty"`
	// VoicesRev records WHICH definition of "the voice list" produced Voices.
	// When the resolve semantics change, the stored snapshot stops being
	// comparable — diffing against it would announce the entire new list as new.
	// A rev mismatch is treated as "no baseline" so the next poll re-baselines in
	// silence. Old records carry 0, which never matches a shipped rev.
	VoicesRev int `json:"voices_rev,omitempty"`
	// ReleaseNotified guards the one-shot movie-release alert.
	ReleaseNotified bool `json:"release_notified,omitempty"`
}

// TrackKind returns the effective kind, treating the zero value as a series.
func (s Subscription) TrackKind() string {
	if s.Kind == KindMovie {
		return KindMovie
	}
	return KindTV
}

// Key identifies a subscription within a user's list.
//
// It is NOT the imdb id: the subscribe API accepts tmdb_id OR imdb_id, so a
// tmdb-only subscription stores an empty imdb — and matching on that made every
// such record collide with the next one (unsubscribing one removed another).
// tmdb id is the identifier every client actually carries.
func (s Subscription) Key() string {
	if s.TmdbID > 0 {
		return s.TrackKind() + ":" + strconv.Itoa(s.TmdbID)
	}
	return s.TrackKind() + ":imdb:" + s.ImdbID
}

// UserSubs holds all subscriptions for a single Telegram user.
type UserSubs struct {
	TelegramID int64          `json:"telegram_id"`
	Shows      []Subscription `json:"shows"`
	NotifyTG   bool           `json:"notify_tg"`
	// NotifyApp controls the in-app inbox + live push. Pointer so an existing
	// record written before in-app delivery existed reads as "not set" and
	// defaults to on, instead of decoding to a silent false.
	NotifyApp *bool `json:"notify_app,omitempty"`
	// Delivery — когда беспокоить: сразу или сводом, и окно тишины. Пустая
	// структура означает «сразу и всегда», как было до появления расписания.
	Delivery Delivery `json:"delivery,omitempty"`
}

// ---------------------------------------------------------------------------
//  EpisodeInfo — cached upcoming/recent episode from TMDB
// ---------------------------------------------------------------------------

// EpisodeInfo is an upcoming or recently aired episode.
type EpisodeInfo struct {
	TmdbID     int    `json:"tmdb_id"`
	ImdbID     string `json:"imdb_id,omitempty"`
	ShowTitle  string `json:"show_title"`
	PosterPath string `json:"poster_path,omitempty"`
	Season     int    `json:"season"`
	Episode    int    `json:"episode"`
	Name       string `json:"name"`
	AirDate    string `json:"air_date"` // "2026-03-05"
	Overview   string `json:"overview,omitempty"`
}

// ---------------------------------------------------------------------------
//  Store — persistent per-user subscriptions + episode cache
// ---------------------------------------------------------------------------

// Store manages user subscriptions and the TMDB episode cache.
type Store struct {
	mu   sync.RWMutex
	subs []UserSubs // all user subscriptions

	epMu     sync.RWMutex
	episodes map[int]*showCache // tmdbID → cached episode data

	subsPath string
	epsPath  string
}

type showCache struct {
	Upcoming []EpisodeInfo `json:"upcoming"`
	FetchedAt time.Time   `json:"fetched_at"`
}

// New creates a Store, loading persisted data from disk.
func New(repoRoot string) *Store {
	dir := filepath.Join(repoRoot, "database", "calendar")
	_ = os.MkdirAll(dir, 0o755)

	s := &Store{
		episodes: make(map[int]*showCache),
		subsPath: filepath.Join(dir, "subscriptions.json"),
		epsPath:  filepath.Join(dir, "episodes_cache.json"),
	}
	s.load()
	return s
}

// ---------------------------------------------------------------------------
//  User subscriptions
// ---------------------------------------------------------------------------

// Subscribe adds a show to a user's tracking list.
func (s *Store) Subscribe(tgID int64, sub Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sub.AddedAt = time.Now().UTC()
	us := s.findOrCreate(tgID)
	key := sub.Key()
	for i, existing := range us.Shows {
		if existing.Key() == key {
			// Preserve tracking state across a re-subscribe: the client resends
			// only card metadata, and dropping the voice snapshot here would
			// replay every existing translation as "new" on the next poll.
			sub.Voices = existing.Voices
			sub.LastSeason, sub.LastEpisode = existing.LastSeason, existing.LastEpisode
			sub.ReleaseNotified = existing.ReleaseNotified
			sub.AddedAt = existing.AddedAt
			us.Shows[i] = sub
			s.saveSubs()
			return
		}
	}
	us.Shows = append(us.Shows, sub)
	s.saveSubs()
}

// Unsubscribe removes a subscription. `ref` is either a Key() or, for
// backwards compatibility with the original API, a bare imdb id.
func (s *Store) Unsubscribe(tgID int64, ref string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	us := s.findUser(tgID)
	if us == nil || ref == "" {
		return false
	}
	for i, sh := range us.Shows {
		if sh.Key() == ref || (sh.ImdbID != "" && sh.ImdbID == ref) {
			us.Shows = append(us.Shows[:i], us.Shows[i+1:]...)
			s.saveSubs()
			return true
		}
	}
	return false
}

// ListByUser returns the user's subscriptions.
func (s *Store) ListByUser(tgID int64) []Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()

	us := s.findUser(tgID)
	if us == nil {
		return nil
	}
	out := make([]Subscription, len(us.Shows))
	copy(out, us.Shows)
	return out
}

// IsSubscribed checks if a user tracks a title. `ref` is a Key() or an imdb id.
func (s *Store) IsSubscribed(tgID int64, ref string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	us := s.findUser(tgID)
	if us == nil || ref == "" {
		return false
	}
	for _, sh := range us.Shows {
		if sh.Key() == ref || (sh.ImdbID != "" && sh.ImdbID == ref) {
			return true
		}
	}
	return false
}

// SetNotifyTG toggles TG notifications for a user.
func (s *Store) SetNotifyTG(tgID int64, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	us := s.findOrCreate(tgID)
	us.NotifyTG = enabled
	s.saveSubs()
}

// GetNotifyTG returns the notification preference (default true).
func (s *Store) GetNotifyTG(tgID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	us := s.findUser(tgID)
	if us == nil {
		return true // default on
	}
	return us.NotifyTG
}

// SetNotifyApp toggles in-app (inbox + live push) notifications for a user.
func (s *Store) SetNotifyApp(tgID int64, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	us := s.findOrCreate(tgID)
	us.NotifyApp = &enabled
	s.saveSubs()
}

// GetDelivery возвращает расписание доставки пользователя.
func (s *Store) GetDelivery(tgID int64) Delivery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if us := s.findUser(tgID); us != nil {
		return us.Delivery
	}
	return Delivery{}
}

// SetDelivery сохраняет расписание. Создаёт запись пользователя, если её нет:
// расписание можно настроить до первой подписки.
func (s *Store) SetDelivery(tgID int64, d Delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	us := s.findOrCreate(tgID)
	us.Delivery = d
	s.saveSubs()
}

// SetTrackVoices turns per-title translation tracking on or off for ONE
// subscription of one user.
//
// Per subscription, not per user: someone may want dub alerts for the one film
// they are waiting on and nothing else. Turning it OFF also clears the stored
// voice snapshot — otherwise turning it back on months later would diff against
// a stale list and announce every dub that appeared in between as new.
func (s *Store) SetTrackVoices(tgID int64, key string, on bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	us := s.findUser(tgID)
	if us == nil {
		return false
	}
	for i := range us.Shows {
		if us.Shows[i].Key() != key {
			continue
		}
		us.Shows[i].TrackVoices = on
		if !on {
			us.Shows[i].Voices = nil
			us.Shows[i].VoicesRev = 0
			us.Shows[i].WantVoices = nil
		}
		s.saveSubs()
		return true
	}
	return false
}

// SetWantVoices задаёт список ожидаемых озвучек для одной подписки одного
// пользователя. Пустой список = ждать любую новую.
//
// Включает отслеживание автоматически: просить конкретную озвучку и при этом
// держать отслеживание выключенным — заведомо бессмысленная комбинация, и
// пользователю пришлось бы догадываться, что нужны ДВА действия.
func (s *Store) SetWantVoices(tgID int64, key string, names []string) bool {
	clean := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.Join(strings.Fields(n), " ")
		if n == "" || len(n) > 120 {
			continue
		}
		lk := strings.ToLower(n)
		if seen[lk] {
			continue
		}
		seen[lk] = true
		clean = append(clean, n)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	us := s.findUser(tgID)
	if us == nil {
		return false
	}
	for i := range us.Shows {
		if us.Shows[i].Key() != key {
			continue
		}
		us.Shows[i].WantVoices = clean
		if len(clean) > 0 {
			us.Shows[i].TrackVoices = true
		}
		s.saveSubs()
		return true
	}
	return false
}

// VoiceWatcher — подписчик, который действительно ждёт озвучки, со своим списком.
type VoiceWatcher struct {
	TelegramID int64
	Want       []string
}

// VoiceWatchersOfKey возвращает только тех, у кого отслеживание озвучек ВКЛЮЧЕНО.
//
// Раньше здесь использовался SubscribersOfKey, который отдаёт всех подписчиков
// тайтла: стоило одному человеку включить озвучки, и алерты про них летели всем
// остальным, кто подписался только на выход серий.
func (s *Store) VoiceWatchersOfKey(key string) []VoiceWatcher {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []VoiceWatcher
	for i := range s.subs {
		for _, sh := range s.subs[i].Shows {
			if sh.Key() != key || !sh.TrackVoices {
				continue
			}
			out = append(out, VoiceWatcher{
				TelegramID: s.subs[i].TelegramID,
				Want:       append([]string(nil), sh.WantVoices...),
			})
			break
		}
	}
	return out
}

// GetNotifyApp returns the in-app preference. Unset means on — records written
// before in-app delivery existed must not read as "muted".
func (s *Store) GetNotifyApp(tgID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	us := s.findUser(tgID)
	if us == nil || us.NotifyApp == nil {
		return true
	}
	return *us.NotifyApp
}

// UpdateVoices stores the newly observed voice set for a tracked title across
// every subscriber, so the next poll diffs against what we have already told
// people about.
func (s *Store) UpdateVoices(key string, voices []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false
	for i := range s.subs {
		for j := range s.subs[i].Shows {
			if s.subs[i].Shows[j].Key() != key {
				continue
			}
			s.subs[i].Shows[j].Voices = append([]string(nil), voices...)
			s.subs[i].Shows[j].VoicesRev = VoicesRev
			changed = true
		}
	}
	if changed {
		s.saveSubs()
	}
}

// MarkReleaseNotified flips the one-shot movie-release flag for every subscriber
// of the title.
func (s *Store) MarkReleaseNotified(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false
	for i := range s.subs {
		for j := range s.subs[i].Shows {
			if s.subs[i].Shows[j].Key() != key || s.subs[i].Shows[j].ReleaseNotified {
				continue
			}
			s.subs[i].Shows[j].ReleaseNotified = true
			changed = true
		}
	}
	if changed {
		s.saveSubs()
	}
}

// SubscriberSince pairs a subscriber with the moment they subscribed, so the
// cron can avoid announcing something that was already out by then.
type SubscriberSince struct {
	TelegramID int64
	AddedAt    time.Time
}

// SubscribersSince returns each subscriber of a tmdb id together with when they
// subscribed.
func (s *Store) SubscribersSince(tmdbID int) []SubscriberSince {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []SubscriberSince
	for i := range s.subs {
		for _, sh := range s.subs[i].Shows {
			if sh.TmdbID == tmdbID {
				out = append(out, SubscriberSince{TelegramID: s.subs[i].TelegramID, AddedAt: sh.AddedAt})
				break
			}
		}
	}
	return out
}

// SubscribersOfKey returns the Telegram ids tracking a subscription key.
func (s *Store) SubscribersOfKey(key string) []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []int64
	for i := range s.subs {
		for _, sh := range s.subs[i].Shows {
			if sh.Key() == key {
				out = append(out, s.subs[i].TelegramID)
				break
			}
		}
	}
	return out
}

// AllTracked returns one entry per distinct tracked subscription key, across all
// users. Unlike AllTrackedShows it keeps movies and per-key tracking flags, and
// a title is voice-tracked when ANY subscriber asked for it.
func (s *Store) AllTracked() []Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := map[string]int{} // key → index in out
	var out []Subscription
	for i := range s.subs {
		for _, sh := range s.subs[i].Shows {
			key := sh.Key()
			if idx, ok := seen[key]; ok {
				if sh.TrackVoices {
					out[idx].TrackVoices = true
				}
				continue
			}
			seen[key] = len(out)
			out = append(out, sh)
		}
	}
	return out
}

// AllTrackedShows returns a deduplicated list of (tmdbID, imdbID, title, posterPath)
// across all users.
func (s *Store) AllTrackedShows() []Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := map[int]bool{}
	var out []Subscription
	for _, us := range s.subs {
		for _, sh := range us.Shows {
			if sh.TmdbID == 0 || seen[sh.TmdbID] {
				continue
			}
			seen[sh.TmdbID] = true
			out = append(out, sh)
		}
	}
	return out
}

// SubscribersOf returns Telegram IDs of users tracking a given tmdbID.
func (s *Store) SubscribersOf(tmdbID int) []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var ids []int64
	for i := range s.subs {
		for _, sh := range s.subs[i].Shows {
			if sh.TmdbID == tmdbID {
				ids = append(ids, s.subs[i].TelegramID)
				break
			}
		}
	}
	return ids
}

// UpdateLastEpisode updates the last known season/episode for a show
// across all subscribers.
func (s *Store) UpdateLastEpisode(tmdbID, season, episode int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false
	for i := range s.subs {
		for j := range s.subs[i].Shows {
			if s.subs[i].Shows[j].TmdbID == tmdbID {
				if season > s.subs[i].Shows[j].LastSeason ||
					(season == s.subs[i].Shows[j].LastSeason && episode > s.subs[i].Shows[j].LastEpisode) {
					s.subs[i].Shows[j].LastSeason = season
					s.subs[i].Shows[j].LastEpisode = episode
					changed = true
				}
			}
		}
	}
	if changed {
		s.saveSubs()
	}
}

// PopularShows returns the top N shows sorted by subscriber count.
func (s *Store) PopularShows(n int) []PopularShow {
	s.mu.RLock()
	defer s.mu.RUnlock()

	counts := map[int]*PopularShow{}
	for _, us := range s.subs {
		for _, sh := range us.Shows {
			if sh.TmdbID == 0 {
				continue
			}
			p, ok := counts[sh.TmdbID]
			if !ok {
				p = &PopularShow{
					TmdbID:     sh.TmdbID,
					ImdbID:     sh.ImdbID,
					Title:      sh.Title,
					PosterPath: sh.PosterPath,
				}
				counts[sh.TmdbID] = p
			}
			p.Subscribers++
		}
	}

	out := make([]PopularShow, 0, len(counts))
	for _, p := range counts {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Subscribers > out[j].Subscribers
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// PopularShow is a show with subscriber count.
type PopularShow struct {
	TmdbID      int    `json:"tmdb_id"`
	ImdbID      string `json:"imdb_id"`
	Title       string `json:"title"`
	PosterPath  string `json:"poster_path,omitempty"`
	Subscribers int    `json:"subscribers"`
}

// Stats returns total unique users and total unique shows tracked.
func (s *Store) Stats() (users, shows int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	showSet := map[int]bool{}
	for _, us := range s.subs {
		if len(us.Shows) > 0 {
			users++
		}
		for _, sh := range us.Shows {
			showSet[sh.TmdbID] = true
		}
	}
	return users, len(showSet)
}

// ---------------------------------------------------------------------------
//  Episode cache
// ---------------------------------------------------------------------------

// SetEpisodes stores upcoming episodes for a show.
func (s *Store) SetEpisodes(tmdbID int, eps []EpisodeInfo) {
	s.epMu.Lock()
	defer s.epMu.Unlock()

	s.episodes[tmdbID] = &showCache{
		Upcoming:  eps,
		FetchedAt: time.Now().UTC(),
	}
	s.saveEpisodes()
}

// GetEpisodes returns cached episodes for a show.
func (s *Store) GetEpisodes(tmdbID int) []EpisodeInfo {
	s.epMu.RLock()
	defer s.epMu.RUnlock()

	sc := s.episodes[tmdbID]
	if sc == nil {
		return nil
	}
	out := make([]EpisodeInfo, len(sc.Upcoming))
	copy(out, sc.Upcoming)
	return out
}

// AllUpcoming returns all cached upcoming episodes across all tracked shows,
// filtered to [now-recentDays .. now+upcomingDays].
func (s *Store) AllUpcoming(upcomingDays, recentDays int) []EpisodeInfo {
	s.epMu.RLock()
	defer s.epMu.RUnlock()

	now := time.Now().UTC().Truncate(24 * time.Hour)
	from := now.AddDate(0, 0, -recentDays)
	to := now.AddDate(0, 0, upcomingDays)

	var out []EpisodeInfo
	for _, sc := range s.episodes {
		for _, ep := range sc.Upcoming {
			d, err := time.Parse("2006-01-02", ep.AirDate)
			if err != nil {
				continue
			}
			if !d.Before(from) && !d.After(to) {
				out = append(out, ep)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].AirDate < out[j].AirDate
	})
	return out
}

// UserUpcoming returns upcoming episodes only for shows the user tracks.
func (s *Store) UserUpcoming(tgID int64, upcomingDays, recentDays int) []EpisodeInfo {
	subs := s.ListByUser(tgID)
	if len(subs) == 0 {
		return nil
	}
	tracked := map[int]bool{}
	for _, sh := range subs {
		tracked[sh.TmdbID] = true
	}

	all := s.AllUpcoming(upcomingDays, recentDays)
	var out []EpisodeInfo
	for _, ep := range all {
		if tracked[ep.TmdbID] {
			out = append(out, ep)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
//  Internal helpers
// ---------------------------------------------------------------------------

func (s *Store) findUser(tgID int64) *UserSubs {
	for i := range s.subs {
		if s.subs[i].TelegramID == tgID {
			return &s.subs[i]
		}
	}
	return nil
}

func (s *Store) findOrCreate(tgID int64) *UserSubs {
	for i := range s.subs {
		if s.subs[i].TelegramID == tgID {
			return &s.subs[i]
		}
	}
	s.subs = append(s.subs, UserSubs{
		TelegramID: tgID,
		NotifyTG:   true,
	})
	return &s.subs[len(s.subs)-1]
}

func (s *Store) load() {
	if data, err := os.ReadFile(s.subsPath); err == nil {
		_ = json.Unmarshal(data, &s.subs)
	}
	if data, err := os.ReadFile(s.epsPath); err == nil {
		_ = json.Unmarshal(data, &s.episodes)
	}
}

func (s *Store) saveSubs() {
	data, err := json.MarshalIndent(s.subs, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.subsPath, data, 0o644)
}

func (s *Store) saveEpisodes() {
	data, err := json.MarshalIndent(s.episodes, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.epsPath, data, 0o644)
}
