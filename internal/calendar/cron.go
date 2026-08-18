package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/tmdbcache"

	"github.com/rs/zerolog/log"
)

// Notifier sends a TG message to a user.
type Notifier interface {
	SendToUser(tgID int64, text string)
}

// Cron periodically polls TMDB for new episodes of tracked shows
// and sends TG notifications to subscribers.
type Cron struct {
	store    *Store
	pool     *tmdbcache.Pool
	notifier Notifier
	cfg      config.CalendarConfig
	apiKey   string

	// In-app delivery + translation tracking. Both optional and injected after
	// construction (SetAppDelivery / SetVoiceLister) so NewCron's signature — and
	// every existing caller — stays untouched.
	inbox  *Inbox
	push   []PushFunc // live channels (websocket, FCM…) — all fired, none authoritative
	pending *PendingStore // очередь отложенных (тихие часы / свод)
	voices VoiceLister

	stop    chan struct{}
	done    chan struct{}
	running int32
}

// NewCron creates a calendar background worker.
func NewCron(store *Store, pool *tmdbcache.Pool, notifier Notifier, cfg config.CalendarConfig, apiKey string) *Cron {
	return &Cron{
		store:    store,
		pool:     pool,
		notifier: notifier,
		cfg:      cfg,
		apiKey:   apiKey,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start launches the background polling loop.
func (c *Cron) Start(ctx context.Context) {
	interval := max(time.Duration(c.cfg.CheckIntervalMin)*time.Minute, 10*time.Minute)

	go func() {
		defer close(c.done)

		// Initial run after 30 seconds.
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()

		flush := time.NewTicker(time.Minute)
		defer flush.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-c.stop:
				return
			case <-timer.C:
				if atomic.CompareAndSwapInt32(&c.running, 0, 1) {
					c.poll(ctx)
					atomic.StoreInt32(&c.running, 0)
				}
				timer.Reset(interval)
			case now := <-flush.C:
				// Отдельный тик раз в минуту: опрос TMDB идёт раз в пару часов, а
				// «прислать свод в 20:00» с такой точностью означало бы разброс до
				// двух часов — настройка перестала бы значить то, что написано.
				c.FlushPending(now)
			}
		}
	}()

	log.Info().Dur("interval", interval).Msg("calendar: cron started")
}

// Stop gracefully shuts down the cron.
func (c *Cron) Stop() {
	close(c.stop)
	<-c.done
}

// RunNow triggers an immediate poll (for admin panel).
func (c *Cron) RunNow(ctx context.Context) {
	if atomic.CompareAndSwapInt32(&c.running, 0, 1) {
		defer atomic.StoreInt32(&c.running, 0)
		c.poll(ctx)
	}
}

// ---------------------------------------------------------------------------
//  Polling logic
// ---------------------------------------------------------------------------

func (c *Cron) poll(ctx context.Context) {
	// AllTracked (not AllTrackedShows): one entry per distinct subscription key,
	// keeping movies and the per-title voice-tracking flag.
	tracked := c.store.AllTracked()
	if len(tracked) == 0 {
		return
	}

	log.Debug().Int("tracked", len(tracked)).Msg("calendar: polling TMDB")

	const maxParallel = 3
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	for _, sub := range tracked {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			if sub.TrackKind() == KindMovie {
				// An unreleased film has no sources at all, so drilling every
				// balancer for its dubs each cycle finds nothing and costs a full
				// aggregator fan-out. Wait until it is actually out.
				if !c.checkMovie(ctx, sub) {
					return
				}
			} else {
				c.checkShow(ctx, sub)
			}
			// Translation tracking is independent of kind — a film gets new dubs
			// long after release just as a series does.
			c.checkVoices(ctx, sub)
		})
	}

	wg.Wait()
	log.Debug().Int("tracked", len(tracked)).Msg("calendar: poll completed")
}

// tmdbTVResponse is the minimal TMDB /tv/{id} response structure.
type tmdbTVResponse struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	PosterPath       string `json:"poster_path"`
	NextEpisodeToAir *struct {
		AirDate       string `json:"air_date"`
		EpisodeNumber int    `json:"episode_number"`
		SeasonNumber  int    `json:"season_number"`
		Name          string `json:"name"`
		Overview      string `json:"overview"`
	} `json:"next_episode_to_air"`
	LastEpisodeToAir *struct {
		AirDate       string `json:"air_date"`
		EpisodeNumber int    `json:"episode_number"`
		SeasonNumber  int    `json:"season_number"`
		Name          string `json:"name"`
		Overview      string `json:"overview"`
	} `json:"last_episode_to_air"`
	ExternalIDs *struct {
		ImdbID string `json:"imdb_id"`
	} `json:"external_ids"`
	Seasons []struct {
		SeasonNumber int    `json:"season_number"`
		AirDate      string `json:"air_date"`
		EpisodeCount int    `json:"episode_count"`
		Name         string `json:"name"`
	} `json:"seasons"`
}

func (c *Cron) checkShow(ctx context.Context, sub Subscription) {
	q := url.Values{}
	q.Set("language", "ru")
	if c.apiKey != "" {
		q.Set("api_key", c.apiKey)
	}
	q.Set("append_to_response", "external_ids")

	path := fmt.Sprintf("3/tv/%d", sub.TmdbID)
	fr, err := c.pool.FetchAPI(ctx, path, q.Encode())
	if err != nil {
		log.Debug().Err(err).Int("tmdb_id", sub.TmdbID).Msg("calendar: TMDB fetch failed")
		return
	}
	if fr.Status != 200 {
		log.Debug().Int("status", fr.Status).Int("tmdb_id", sub.TmdbID).Msg("calendar: TMDB non-200")
		return
	}

	var tv tmdbTVResponse
	if err := json.Unmarshal(fr.Body, &tv); err != nil {
		log.Debug().Err(err).Int("tmdb_id", sub.TmdbID).Msg("calendar: JSON parse failed")
		return
	}

	// Resolve IMDB ID.
	imdbID := sub.ImdbID
	if imdbID == "" && tv.ExternalIDs != nil {
		imdbID = tv.ExternalIDs.ImdbID
	}

	title := tv.Name
	if title == "" {
		title = sub.Title
	}
	poster := tv.PosterPath
	if poster == "" {
		poster = sub.PosterPath
	}

	// Build episode cache entries.
	var episodes []EpisodeInfo
	now := time.Now().UTC().Truncate(24 * time.Hour)
	recentFrom := now.AddDate(0, 0, -c.cfg.RecentDays)
	upcomingTo := now.AddDate(0, 0, c.cfg.UpcomingDays)

	addEp := func(airDate string, season, episode int, name, overview string) {
		if airDate == "" {
			return
		}
		d, err := time.Parse("2006-01-02", airDate)
		if err != nil {
			return
		}
		if d.Before(recentFrom) || d.After(upcomingTo) {
			return
		}
		episodes = append(episodes, EpisodeInfo{
			TmdbID:     sub.TmdbID,
			ImdbID:     imdbID,
			ShowTitle:  title,
			PosterPath: poster,
			Season:     season,
			Episode:    episode,
			Name:       name,
			AirDate:    airDate,
			Overview:   overview,
		})
	}

	if ep := tv.LastEpisodeToAir; ep != nil {
		addEp(ep.AirDate, ep.SeasonNumber, ep.EpisodeNumber, ep.Name, ep.Overview)
	}
	if ep := tv.NextEpisodeToAir; ep != nil {
		addEp(ep.AirDate, ep.SeasonNumber, ep.EpisodeNumber, ep.Name, ep.Overview)
	}

	c.store.SetEpisodes(sub.TmdbID, episodes)

	// Check if there's a NEW episode that aired (notify subscribers).
	c.checkAndNotify(sub, tv, title)
}

func (c *Cron) checkAndNotify(sub Subscription, tv tmdbTVResponse, title string) {
	// No global TG gate here any more: the in-app inbox is a separate channel and
	// must keep working when Telegram delivery is off. deliver() applies the
	// per-channel and per-user mutes.
	if (!c.cfg.NotifyTG || c.notifier == nil) && c.inbox == nil {
		return
	}

	ep := tv.LastEpisodeToAir
	if ep == nil {
		return
	}

	// FIRST POLL AFTER SUBSCRIBING = baseline, not news.
	//
	// A fresh subscription carries S0E0, so every comparison below says "newer"
	// and the show's last aired episode gets announced the moment someone
	// subscribes — reported live: subscribing on 28 July produced alerts for
	// episodes that aired on the 26th and the 22nd. Record where the show stands
	// and stay quiet; the NEXT episode is the first real event. Real episodes are
	// always ≥ S1E1, so the zero pair unambiguously means "never polled".
	if sub.LastSeason == 0 && sub.LastEpisode == 0 {
		c.store.UpdateLastEpisode(sub.TmdbID, ep.SeasonNumber, ep.EpisodeNumber)
		log.Debug().
			Str("show", title).
			Int("season", ep.SeasonNumber).
			Int("episode", ep.EpisodeNumber).
			Msg("calendar: baseline recorded for a new subscription, no alert")
		return
	}

	// Is this newer than what we've seen?
	isNew := ep.SeasonNumber > sub.LastSeason ||
		(ep.SeasonNumber == sub.LastSeason && ep.EpisodeNumber > sub.LastEpisode)

	if !isNew {
		return
	}

	// Check that it actually aired (not in the future).
	if ep.AirDate != "" {
		d, err := time.Parse("2006-01-02", ep.AirDate)
		if err == nil && d.After(time.Now().UTC().Truncate(24*time.Hour)) {
			return // future episode, skip
		}
	}

	// Update store.
	c.store.UpdateLastEpisode(sub.TmdbID, ep.SeasonNumber, ep.EpisodeNumber)

	// Second guard: never tell someone about an episode that was already out when
	// they subscribed. The baseline above covers the first subscriber of a show;
	// this covers everyone who joins an already-tracked one, and any case where
	// the shared pointer lags behind. Compared by DAY — an episode airing the same
	// day someone subscribes is genuinely news for them.
	airDay := time.Time{}
	if ep.AirDate != "" {
		if d, err := time.Parse("2006-01-02", ep.AirDate); err == nil {
			airDay = d
		}
	}
	var subscribers []int64
	for _, ss := range c.store.SubscribersSince(sub.TmdbID) {
		if !episodeIsNewsFor(ss.AddedAt, airDay) {
			continue
		}
		subscribers = append(subscribers, ss.TelegramID)
	}
	if len(subscribers) == 0 {
		return
	}

	// Format date.
	dateStr := ep.AirDate
	if d, err := time.Parse("2006-01-02", ep.AirDate); err == nil {
		dateStr = formatDateRu(d)
	}

	epName := ep.Name
	if epName == "" {
		epName = fmt.Sprintf("Эпизод %d", ep.EpisodeNumber)
	}

	text := fmt.Sprintf(
		"📺 <b>%s</b>\nS%02dE%02d «%s»\n📅 Вышла %s",
		escapeHTML(title),
		ep.SeasonNumber, ep.EpisodeNumber,
		escapeHTML(epName),
		dateStr,
	)

	notified := c.deliver(subscribers, Notification{
		Kind:       NotifyEpisode,
		TmdbID:     sub.TmdbID,
		MediaType:  KindTV,
		Title:      title,
		Text:       fmt.Sprintf("S%02dE%02d «%s» — вышла %s", ep.SeasonNumber, ep.EpisodeNumber, epName, dateStr),
		PosterPath: sub.PosterPath,
		Season:     ep.SeasonNumber,
		Episode:    ep.EpisodeNumber,
	}, text)

	log.Info().
		Str("show", title).
		Int("season", ep.SeasonNumber).
		Int("episode", ep.EpisodeNumber).
		Int("notified", notified).
		Msg("calendar: new episode notification sent")
}

// ---------------------------------------------------------------------------
//  Helpers
// ---------------------------------------------------------------------------

// episodeIsNewsFor reports whether an episode that aired on airDay is news for a
// subscriber who subscribed at addedAt.
//
// Compared by DAY, not instant: an episode dropping the same day someone
// subscribes is genuinely news for them, while one that was already out is not.
// Unknown dates fall through as "news" — refusing to notify because metadata is
// missing would be a worse failure than a redundant alert.
func episodeIsNewsFor(addedAt, airDay time.Time) bool {
	if addedAt.IsZero() || airDay.IsZero() {
		return true
	}
	return !addedAt.UTC().Truncate(24 * time.Hour).After(airDay.UTC().Truncate(24 * time.Hour))
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

var ruMonths = [...]string{
	"", "января", "февраля", "марта", "апреля", "мая", "июня",
	"июля", "августа", "сентября", "октября", "ноября", "декабря",
}

func formatDateRu(t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), ruMonths[t.Month()], t.Year())
}
