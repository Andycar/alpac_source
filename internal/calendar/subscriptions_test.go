package calendar

import "testing"

// TestSubscriptionKeyDistinguishesTmdbOnlyRecords: the subscribe API accepts
// tmdb_id OR imdb_id, so matching on the imdb id made every tmdb-only record
// collide on "" — unsubscribing one silently removed a different title.
func TestSubscriptionKeyDistinguishesTmdbOnlyRecords(t *testing.T) {
	a := Subscription{TmdbID: 111}
	b := Subscription{TmdbID: 222}
	if a.Key() == b.Key() {
		t.Fatalf("distinct tmdb-only subscriptions share a key: %q", a.Key())
	}
	// Same tmdb id, different medium, is genuinely two subscriptions.
	movie := Subscription{TmdbID: 111, Kind: KindMovie}
	if movie.Key() == a.Key() {
		t.Fatalf("movie and series collide on key %q", a.Key())
	}
	// Zero Kind must keep meaning "tv" — every pre-existing record has it empty.
	if a.TrackKind() != KindTV {
		t.Fatalf("zero Kind = %q, want %q", a.TrackKind(), KindTV)
	}
}

func TestSubscribeUnsubscribeByKeyAndImdb(t *testing.T) {
	s := New(t.TempDir())
	movie := Subscription{TmdbID: 500, Kind: KindMovie, Title: "Фильм"}
	show := Subscription{TmdbID: 500, ImdbID: "tt500", Title: "Сериал"}
	s.Subscribe(1, movie)
	s.Subscribe(1, show)

	if got := len(s.ListByUser(1)); got != 2 {
		t.Fatalf("want 2 subscriptions, got %d", got)
	}
	// Removing the movie must leave the series alone despite the shared tmdb id.
	if !s.Unsubscribe(1, movie.Key()) {
		t.Fatal("unsubscribe by key failed")
	}
	left := s.ListByUser(1)
	if len(left) != 1 || left[0].Title != "Сериал" {
		t.Fatalf("wrong subscription removed: %+v", left)
	}
	// Legacy callers pass a bare imdb id.
	if !s.Unsubscribe(1, "tt500") {
		t.Fatal("unsubscribe by imdb id failed")
	}
	if got := len(s.ListByUser(1)); got != 0 {
		t.Fatalf("want 0 left, got %d", got)
	}
}

// TestResubscribeKeepsTrackingState: the client resends card metadata on every
// subscribe. If that wiped the voice snapshot, the next poll would re-announce
// every translation the title already had.
func TestResubscribeKeepsTrackingState(t *testing.T) {
	s := New(t.TempDir())
	sub := Subscription{TmdbID: 7, TrackVoices: true, Title: "T"}
	s.Subscribe(1, sub)
	s.UpdateVoices(sub.Key(), []string{"Дубляж", "LostFilm"})
	s.UpdateLastEpisode(7, 2, 5)

	s.Subscribe(1, Subscription{TmdbID: 7, TrackVoices: true, Title: "T (обновлённое)"})

	got := s.ListByUser(1)[0]
	if len(got.Voices) != 2 {
		t.Fatalf("voice snapshot lost on re-subscribe: %+v", got.Voices)
	}
	if got.LastSeason != 2 || got.LastEpisode != 5 {
		t.Fatalf("episode progress lost: S%dE%d", got.LastSeason, got.LastEpisode)
	}
	if got.Title != "T (обновлённое)" {
		t.Fatalf("metadata not refreshed: %q", got.Title)
	}
}

// TestNotifyAppDefaultsOnForExistingRecords: records written before in-app
// delivery existed have no flag, and must not decode as "muted".
func TestNotifyAppDefaultsOnForExistingRecords(t *testing.T) {
	s := New(t.TempDir())
	s.Subscribe(42, Subscription{TmdbID: 1})
	if !s.GetNotifyApp(42) {
		t.Fatal("in-app notifications default to off for an existing user")
	}
	if !s.GetNotifyApp(999) {
		t.Fatal("in-app notifications default to off for an unknown user")
	}
	s.SetNotifyApp(42, false)
	if s.GetNotifyApp(42) {
		t.Fatal("explicit mute not honoured")
	}
}

// TestAllTrackedMergesVoiceFlagAcrossUsers: polling is per title, so one
// subscriber asking for translation tracking must enable it for that title.
func TestAllTrackedMergesVoiceFlagAcrossUsers(t *testing.T) {
	s := New(t.TempDir())
	s.Subscribe(1, Subscription{TmdbID: 9, TrackVoices: false})
	s.Subscribe(2, Subscription{TmdbID: 9, TrackVoices: true})

	tracked := s.AllTracked()
	if len(tracked) != 1 {
		t.Fatalf("want 1 distinct tracked title, got %d", len(tracked))
	}
	if !tracked[0].TrackVoices {
		t.Fatal("voice tracking not merged across subscribers")
	}
	if subs := s.SubscribersOfKey(tracked[0].Key()); len(subs) != 2 {
		t.Fatalf("want 2 subscribers, got %d", len(subs))
	}
}

func TestMarkReleaseNotifiedOnlyTargetsItsKey(t *testing.T) {
	s := New(t.TempDir())
	target := Subscription{TmdbID: 1, Kind: KindMovie}
	other := Subscription{TmdbID: 2, Kind: KindMovie}
	s.Subscribe(1, target)
	s.Subscribe(1, other)

	s.MarkReleaseNotified(target.Key())

	for _, sub := range s.ListByUser(1) {
		want := sub.TmdbID == 1
		if sub.ReleaseNotified != want {
			t.Fatalf("tmdb %d: ReleaseNotified=%v, want %v", sub.TmdbID, sub.ReleaseNotified, want)
		}
	}
}

func TestNormalizeVoiceIgnoresCosmeticChurn(t *testing.T) {
	// Sources re-case and re-space the same studio constantly; without this each
	// variant would read as a brand-new translation.
	if normalizeVoice("  LostFilm  ") != normalizeVoice("lostfilm") {
		t.Fatal("case/whitespace variants treated as different voices")
	}
	if normalizeVoice("Кубик в Кубе") != normalizeVoice("кубик  в   кубе") {
		t.Fatal("inner whitespace variants treated as different voices")
	}
	if normalizeVoice("Дубляж") == normalizeVoice("Многоголосый") {
		t.Fatal("distinct voices collapsed")
	}
}
