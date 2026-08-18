package calendar

import (
	"context"
	"testing"
	"time"
)

// stubNotifier records what would have been sent to Telegram.
type stubNotifier struct{ sent []string }

func (s *stubNotifier) SendToUser(_ int64, text string) { s.sent = append(s.sent, text) }

func tvWithLastEpisode(season, episode int, airDate string) tmdbTVResponse {
	var tv tmdbTVResponse
	tv.LastEpisodeToAir = &struct {
		AirDate       string `json:"air_date"`
		EpisodeNumber int    `json:"episode_number"`
		SeasonNumber  int    `json:"season_number"`
		Name          string `json:"name"`
		Overview      string `json:"overview"`
	}{AirDate: airDate, EpisodeNumber: episode, SeasonNumber: season, Name: "Эпизод"}
	return tv
}

func newTestCron(t *testing.T) (*Cron, *Store, *stubNotifier) {
	t.Helper()
	store := New(t.TempDir())
	notifier := &stubNotifier{}
	c := &Cron{store: store, notifier: notifier}
	c.cfg.NotifyTG = true
	return c, store, notifier
}

// TestFirstPollAfterSubscribingIsSilent is the regression for the live report:
// subscribing on 28 July fired alerts for episodes that had aired on the 26th
// and the 22nd. A fresh subscription carries S0E0, so every already-aired
// episode compared as "newer" than nothing.
func TestFirstPollAfterSubscribingIsSilent(t *testing.T) {
	c, store, notifier := newTestCron(t)
	store.Subscribe(1, Subscription{TmdbID: 42, Title: "Дом Дракона"})

	sub := store.ListByUser(1)[0]
	c.checkAndNotify(sub, tvWithLastEpisode(3, 6, "2026-07-26"), "Дом Дракона")

	if len(notifier.sent) != 0 {
		t.Fatalf("an already-aired episode was announced on subscribe: %v", notifier.sent)
	}
	// The baseline must be recorded, or the same episode would fire next poll.
	got := store.ListByUser(1)[0]
	if got.LastSeason != 3 || got.LastEpisode != 6 {
		t.Fatalf("baseline not recorded: S%dE%d", got.LastSeason, got.LastEpisode)
	}
}

// TestNextEpisodeAfterBaselineNotifies: staying quiet on the first poll must not
// mute the feature — the following episode is the real event.
//
// The new episode airs TODAY: Subscribe() stamps AddedAt=now, and an episode
// dated in the future is skipped as unaired, so "today" is the only date that
// represents "dropped after this subscription existed".
func TestNextEpisodeAfterBaselineNotifies(t *testing.T) {
	c, store, notifier := newTestCron(t)
	store.Subscribe(1, Subscription{TmdbID: 42, Title: "Дом Дракона"})

	// poll 1: baseline
	c.checkAndNotify(store.ListByUser(1)[0], tvWithLastEpisode(3, 6, "2026-07-26"), "Дом Дракона")
	// poll 2: a genuinely new episode
	today := time.Now().UTC().Format("2006-01-02")
	c.checkAndNotify(store.ListByUser(1)[0], tvWithLastEpisode(3, 7, today), "Дом Дракона")

	if len(notifier.sent) != 1 {
		t.Fatalf("want exactly one alert for the new episode, got %d: %v", len(notifier.sent), notifier.sent)
	}
}

// TestEpisodeIsNewsFor covers the air-date guard directly: it is what stops a
// late subscriber being told about an episode that was already out when they
// joined. Tested as a unit because Subscribe() always stamps AddedAt=now, so the
// "subscribed long ago" case cannot be staged through the store.
func TestEpisodeIsNewsFor(t *testing.T) {
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("bad date %q: %v", s, err)
		}
		return d
	}

	// Subscribed before it aired → news.
	if !episodeIsNewsFor(day("2026-07-01"), day("2026-07-26")) {
		t.Error("an episode aired after subscribing must be news")
	}
	// Subscribed after it aired → the reported bug; must stay silent.
	if episodeIsNewsFor(day("2026-07-28"), day("2026-07-26")) {
		t.Error("an episode already out at subscribe time must NOT be news")
	}
	// Same day → news: it genuinely dropped while they were subscribed.
	if !episodeIsNewsFor(day("2026-07-26"), day("2026-07-26")) {
		t.Error("a same-day episode must be news")
	}
	// Time-of-day must not matter — only the calendar day.
	if !episodeIsNewsFor(day("2026-07-26").Add(23*time.Hour), day("2026-07-26")) {
		t.Error("subscribing later the same day must still count as news")
	}
	// Missing metadata must not silence a real release.
	if !episodeIsNewsFor(time.Time{}, day("2026-07-26")) || !episodeIsNewsFor(day("2026-07-01"), time.Time{}) {
		t.Error("unknown dates must fall through as news")
	}
}

// TestSameDaySubscriberStillNotified: the air-date guard compares by day, so an
// episode dropping the same day someone subscribes is genuinely news for them.
func TestSameDaySubscriberStillNotified(t *testing.T) {
	c, store, notifier := newTestCron(t)
	store.Subscribe(1, Subscription{TmdbID: 42, Title: "Шоу"})
	c.checkAndNotify(store.ListByUser(1)[0], tvWithLastEpisode(1, 1, "2026-07-01"), "Шоу")

	today := time.Now().UTC().Format("2006-01-02")
	c.checkAndNotify(store.ListByUser(1)[0], tvWithLastEpisode(1, 2, today), "Шоу")

	if len(notifier.sent) != 1 {
		t.Fatalf("same-day episode must reach a same-day subscriber, got %d", len(notifier.sent))
	}
}

// ---------------------------------------------------------------------------
//  Voice tracking
// ---------------------------------------------------------------------------

// TestVoicesSkipSeriesWithoutEpisode is the regression for the «Дом Дракона»
// report: subscribers were told the new voices were «Дрифтмарк, Зелёный совет,
// Лорд Приливов…» — the episode titles. The drill had been asked for the show
// with no season/episode, so balancers answered with the episode listing.
func TestVoicesSkipSeriesWithoutEpisode(t *testing.T) {
	c, store, _ := newTestCron(t)
	store.Subscribe(1, Subscription{TmdbID: 42, Title: "Дом Дракона", TrackVoices: true})

	called := false
	c.SetVoiceLister(func(_ context.Context, _ int, _ string, _, _ int) []string {
		called = true
		return nil
	})

	// No baseline episode yet → the drill must not run at all.
	c.checkVoices(context.Background(), store.ListByUser(1)[0])
	if called {
		t.Fatal("a series was drilled for voices without an episode to pin to")
	}
}

// TestVoicesPinToLastAiredEpisode: once the episode baseline exists, the drill
// runs and carries it.
func TestVoicesPinToLastAiredEpisode(t *testing.T) {
	c, store, _ := newTestCron(t)
	store.Subscribe(1, Subscription{
		TmdbID: 42, Title: "Дом Дракона", TrackVoices: true,
		LastSeason: 3, LastEpisode: 6,
	})

	var gotS, gotE int
	c.SetVoiceLister(func(_ context.Context, _ int, _ string, s, e int) []string {
		gotS, gotE = s, e
		return []string{"HDrezka Studio"}
	})

	c.checkVoices(context.Background(), store.ListByUser(1)[0])
	if gotS != 3 || gotE != 6 {
		t.Fatalf("drill not pinned to the last aired episode: S%dE%d", gotS, gotE)
	}
}

// TestStaleVoicesRevRebaselinesSilently: subscriptions that already stored the
// bogus episode-title list must not have every real voice announced as new the
// first time the fixed resolve runs.
func TestStaleVoicesRevRebaselinesSilently(t *testing.T) {
	c, store, notifier := newTestCron(t)
	store.Subscribe(1, Subscription{
		TmdbID: 42, Title: "Дом Дракона", TrackVoices: true,
		LastSeason: 3, LastEpisode: 6,
		Voices: []string{"Дрифтмарк", "Зелёный совет"}, // poisoned, rev 0
	})

	c.SetVoiceLister(func(_ context.Context, _ int, _ string, _, _ int) []string {
		return []string{"LostFilm", "HDrezka Studio"}
	})

	c.checkVoices(context.Background(), store.ListByUser(1)[0])
	if len(notifier.sent) != 0 {
		t.Fatalf("stale baseline was diffed instead of replaced: %v", notifier.sent)
	}

	// Re-baselined and stamped — a genuinely new voice now works normally.
	got := store.ListByUser(1)[0]
	if got.VoicesRev != VoicesRev {
		t.Fatalf("rev not stamped: %d", got.VoicesRev)
	}
	c.SetVoiceLister(func(_ context.Context, _ int, _ string, _, _ int) []string {
		return []string{"LostFilm", "HDrezka Studio", "Кубик в Кубе"}
	})
	c.checkVoices(context.Background(), store.ListByUser(1)[0])
	if len(notifier.sent) != 1 {
		t.Fatalf("want 1 alert for the genuinely new voice, got %d: %v", len(notifier.sent), notifier.sent)
	}
}

// TestSetTrackVoicesClearsSnapshotWhenOff: выключение должно ЗАБЫТЬ накопленный
// список озвучек. Иначе включив отслеживание через полгода пользователь получил
// бы разом все дубляжи, появившиеся за это время, как «новые».
func TestSetTrackVoicesClearsSnapshotWhenOff(t *testing.T) {
	store := New(t.TempDir())
	store.Subscribe(1, Subscription{
		TmdbID: 42, Title: "Шоу", TrackVoices: true,
		Voices: []string{"LostFilm"}, VoicesRev: VoicesRev,
	})
	key := store.ListByUser(1)[0].Key()

	if !store.SetTrackVoices(1, key, false) {
		t.Fatal("подписка не найдена")
	}
	got := store.ListByUser(1)[0]
	if got.TrackVoices {
		t.Fatal("флаг не снят")
	}
	if len(got.Voices) != 0 || got.VoicesRev != 0 {
		t.Fatalf("снимок озвучек не сброшен: %v rev=%d", got.Voices, got.VoicesRev)
	}
}

// TestSetTrackVoicesOnUnknownKeyFails: молча «успешно» ничего не сделать — худший
// исход: интерфейс покажет включённый тумблер, а уведомлений не будет.
func TestSetTrackVoicesOnUnknownKeyFails(t *testing.T) {
	store := New(t.TempDir())
	store.Subscribe(1, Subscription{TmdbID: 42, Title: "Шоу"})

	if store.SetTrackVoices(1, "tv:999", true) {
		t.Fatal("несуществующая подписка отрапортовала успех")
	}
	if store.SetTrackVoices(777, "tv:42", true) {
		t.Fatal("чужая подписка отрапортовала успех")
	}
}

// ---------------------------------------------------------------------------
//  Ожидание КОНКРЕТНОЙ озвучки
// ---------------------------------------------------------------------------

func TestVoiceWantedIsForgiving(t *testing.T) {
	// Пользователь набирает коротко, источники дописывают пометки — точное
	// совпадение промахнулось бы в обе стороны.
	if !voiceWanted("Кубик в Кубе (18+)", []string{"кубик в кубе"}) {
		t.Error("пометка источника сорвала совпадение")
	}
	if !voiceWanted("Кубик в Кубе", []string{"КУБИК"}) {
		t.Error("короткий ввод пользователя не сработал")
	}
	if voiceWanted("LostFilm", []string{"кубик в кубе"}) {
		t.Error("совпало то, что совпадать не должно")
	}
	if voiceWanted("", []string{"кубик"}) {
		t.Error("пустое имя не может совпасть")
	}
}

func TestFilterWantedEmptyMeansAll(t *testing.T) {
	fresh := []string{"LostFilm", "Кубик в Кубе"}
	if got := filterWanted(fresh, nil); len(got) != 2 {
		t.Fatalf("пустой список ожиданий должен пропускать всё, got %v", got)
	}
	if got := filterWanted(fresh, []string{"кубик"}); len(got) != 1 || got[0] != "Кубик в Кубе" {
		t.Fatalf("фильтр не отобрал ожидаемую озвучку: %v", got)
	}
}

// TestVoiceWatchersOnlyThoseWhoAskedFor: регрессия на найденную попутно ошибку —
// стоило ОДНОМУ включить озвучки, и алерты летели всем подписчикам тайтла.
func TestVoiceWatchersOnlyThoseWhoAskedFor(t *testing.T) {
	store := New(t.TempDir())
	store.Subscribe(1, Subscription{TmdbID: 42, Title: "Шоу", TrackVoices: true})
	store.Subscribe(2, Subscription{TmdbID: 42, Title: "Шоу"}) // только серии
	key := store.ListByUser(1)[0].Key()

	w := store.VoiceWatchersOfKey(key)
	if len(w) != 1 || w[0].TelegramID != 1 {
		t.Fatalf("в наблюдатели попал не тот: %+v", w)
	}
}

func TestSetWantVoicesEnablesTracking(t *testing.T) {
	store := New(t.TempDir())
	store.Subscribe(1, Subscription{TmdbID: 42, Title: "Фильм", Kind: KindMovie})
	key := store.ListByUser(1)[0].Key()

	if !store.SetWantVoices(1, key, []string{" Кубик  в Кубе ", "кубик в кубе", ""}) {
		t.Fatal("подписка не найдена")
	}
	got := store.ListByUser(1)[0]
	if !got.TrackVoices {
		t.Fatal("просить конкретную озвучку с выключенным отслеживанием бессмысленно — надо включать само")
	}
	// Пробелы схлопнуты, дубликат и пустая строка отброшены.
	if len(got.WantVoices) != 1 || got.WantVoices[0] != "Кубик в Кубе" {
		t.Fatalf("список не нормализован: %#v", got.WantVoices)
	}
}

func TestTurningVoicesOffClearsWantList(t *testing.T) {
	store := New(t.TempDir())
	store.Subscribe(1, Subscription{TmdbID: 42, Title: "Шоу"})
	key := store.ListByUser(1)[0].Key()
	store.SetWantVoices(1, key, []string{"кубик"})

	store.SetTrackVoices(1, key, false)
	if got := store.ListByUser(1)[0]; len(got.WantVoices) != 0 {
		t.Fatalf("список ожиданий переживёт выключение и удивит при повторном включении: %v", got.WantVoices)
	}
}
