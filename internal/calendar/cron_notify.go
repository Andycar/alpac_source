package calendar

// Movie-release and new-translation tracking, plus two-channel delivery.
//
// The original cron did one thing: poll TMDB for a series' last aired episode
// and push a Telegram message. This adds the two events users actually asked
// for besides episodes — "the movie is out" and "a new voice-over appeared" —
// and makes every alert land in the in-app inbox as well as Telegram, each
// channel independently mutable per user.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// PushFunc delivers a live in-app event (websocket) to a user. Optional: when
// nil, notifications still land in the inbox and are picked up on next open.
type PushFunc func(tgID int64, n Notification)

// VoiceLister returns the voice-over names currently available for a title.
//
// Injected rather than imported: enumerating voices means drilling the
// balancers, which lives in the httpapi layer — importing it here would be a
// cycle. Returning nil is "could not determine", which is deliberately NOT the
// same as "no voices" and never produces a notification.
//
// season/episode pin a series drill to one concrete episode. They are NOT
// optional decoration: asked for a show with no episode, balancers answer with
// the season/episode LISTING, and its entries then read back as "voices".
type VoiceLister func(ctx context.Context, tmdbID int, kind string, season, episode int) []string

// SetAppDelivery wires the in-app channel. Safe to omit — TG-only still works.
func (c *Cron) SetAppDelivery(inbox *Inbox, push PushFunc) {
	c.inbox = inbox
	c.push = nil
	if push != nil {
		c.push = []PushFunc{push}
	}
}

// AddPush registers one more live-delivery channel alongside whatever
// SetAppDelivery wired. Websocket and Firebase are peers, not alternatives: the
// socket reaches an open app instantly, FCM reaches a closed one.
func (c *Cron) AddPush(fn PushFunc) {
	if fn != nil {
		c.push = append(c.push, fn)
	}
}

// SetVoiceLister enables new-translation tracking.
func (c *Cron) SetVoiceLister(fn VoiceLister) { c.voices = fn }

// TestPush delivers a synthetic notification to one user through the SAME path a
// real alert takes — inbox, Telegram, and every live channel.
//
// It exists because the last seam of the push chain is otherwise unverifiable:
// Firebase's own "send test message" produces a `notification` message, which
// the OS renders itself without ever entering our service, so it proves the
// token works and nothing about our payload or our notification builder.
func (c *Cron) TestPush(tgID int64) int {
	return c.deliver([]int64{tgID}, Notification{
		Kind:      "episode",
		Title:     "Проверка уведомлений",
		Text:      "Если вы это видите — push настроен правильно.",
		MediaType: KindTV,
	}, "<b>Проверка уведомлений</b>\nЕсли вы это видите — push настроен правильно.")
}

// deliver fans one event out to both channels, honouring each user's mute.
// tgText is the HTML-formatted Telegram body; n is the structured inbox entry.
func (c *Cron) deliver(subscribers []int64, n Notification, tgText string) int {
	delivered := 0
	for _, tgID := range subscribers {
		sent := false

		// ★Инбокс наполняется ВСЕГДА и сразу, независимо от расписания: это список,
		// который человек открывает сам, а не то, что его будит. Тихие часы и режим
		// свода касаются шумных каналов — Telegram и push.
		var stored Notification
		hasStored := false
		if c.inbox != nil && c.store.GetNotifyApp(tgID) {
			stored = c.inbox.Push(tgID, n)
			hasStored = true
			sent = true
		}

		// Расписание: отправлять сейчас или положить в очередь.
		due := c.store.GetDelivery(tgID).DueAt(time.Now())
		if !due.IsZero() && c.pending != nil {
			toQueue := n
			if hasStored {
				toQueue = stored // с id — клиент сможет отметить прочитанным
			}
			c.pending.Add(tgID, PendingItem{N: toQueue, TGText: tgText, DueAt: due})
			delivered++ // уведомление НЕ потеряно, просто придёт позже
			continue
		}

		if c.cfg.NotifyTG && c.notifier != nil && c.store.GetNotifyTG(tgID) {
			c.notifier.SendToUser(tgID, tgText)
			sent = true
			time.Sleep(35 * time.Millisecond) // TG rate limit
		}

		if hasStored {
			for _, push := range c.push {
				push(tgID, stored)
			}
		}

		if sent {
			delivered++
		}
	}
	return delivered
}

// SetPending подключает очередь отложенных уведомлений. Без неё расписание не
// работает и всё уходит сразу — так ведут себя старые развёртывания.
func (c *Cron) SetPending(p *PendingStore) { c.pending = p }

// FlushPending разбирает очередь: всё, чей срок пришёл, уходит адресату.
//
// Несколько накопленных пунктов склеиваются в ОДНО сообщение — в этом и смысл
// свода. Пуш при этом тоже один: двадцать баннеров подряд ничем не лучше
// двадцати сообщений, от которых человек и уходил в режим свода.
func (c *Cron) FlushPending(now time.Time) {
	if c.pending == nil {
		return
	}
	for tgID, items := range c.pending.TakeDue(now) {
		if len(items) == 0 {
			continue
		}

		if c.cfg.NotifyTG && c.notifier != nil && c.store.GetNotifyTG(tgID) {
			if len(items) == 1 {
				c.notifier.SendToUser(tgID, items[0].TGText)
			} else {
				var b strings.Builder
				b.WriteString(fmt.Sprintf("🔔 <b>Новостей за это время: %d</b>", len(items)))
				for _, it := range items {
					b.WriteString("\n\n")
					b.WriteString(it.TGText)
				}
				c.notifier.SendToUser(tgID, b.String())
			}
			time.Sleep(35 * time.Millisecond) // TG rate limit
		}

		for _, push := range c.push {
			if len(items) == 1 {
				push(tgID, items[0].N)
				continue
			}
			// Сводный пуш: ведёт в список, а не в одну случайную карточку из
			// двадцати, поэтому tmdb_id намеренно пустой.
			push(tgID, Notification{
				ID:        items[len(items)-1].N.ID,
				Kind:      NotifyEpisode,
				Title:     "Новости по подпискам",
				Text:      fmt.Sprintf("Пока вас не беспокоили, вышло: %d", len(items)),
				CreatedAt: now,
			})
		}

		log.Info().Int64("tg_id", tgID).Int("items", len(items)).
			Msg("calendar: отложенные уведомления доставлены")
	}
}

// ---------------------------------------------------------------------------
//  Movies — notify once, when a tracked film actually comes out
// ---------------------------------------------------------------------------

type tmdbMovieResponse struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	PosterPath  string `json:"poster_path"`
	ReleaseDate string `json:"release_date"`
	Status      string `json:"status"`
	ExternalIDs *struct {
		ImdbID string `json:"imdb_id"`
	} `json:"external_ids"`
	ReleaseDates *struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Dates   []struct {
				Type int    `json:"type"`
				Date string `json:"release_date"`
			} `json:"release_dates"`
		} `json:"results"`
	} `json:"release_dates"`
}

// digitalDate — самый ранний цифровой релиз (тип 4) в России или США. Цифра для нашего
// зрителя американская: у крупных студий российского проката с 2022-го нет.
func (mv tmdbMovieResponse) digitalDate() (time.Time, bool) {
	var best time.Time
	if mv.ReleaseDates == nil {
		return best, false
	}
	for _, c := range mv.ReleaseDates.Results {
		if c.Country != "RU" && c.Country != "US" {
			continue
		}
		for _, d := range c.Dates {
			if d.Type != 4 || len(d.Date) < 10 {
				continue
			}
			t, err := time.Parse("2006-01-02", d.Date[:10])
			if err == nil && (best.IsZero() || t.Before(best)) {
				best = t
			}
		}
	}
	return best, !best.IsZero()
}

// digitalFreshDays — «вышел в цифре» шлём только по свежему релизу. Флаг появился позже
// подписок: без этого окна первый же опрос после выкатки разослал бы новости про фильмы,
// вышедшие в цифре полгода назад.
const digitalFreshDays = 7

// checkMovie announces the release and reports whether the film is actually out
// — the caller uses that to decide whether drilling sources for new dubs is
// worth anything yet.
//
// Вестей две: «вышел» (премьера) и «вышел в цифре» (можно смотреть в хорошем качестве —
// это обещает кнопка «Напомнить о цифре» на экране «Скоро»). Если к моменту премьеры цифра
// уже вышла, уходит одно сообщение, а не два подряд.
func (c *Cron) checkMovie(ctx context.Context, sub Subscription) (released bool) {
	if sub.ReleaseNotified && sub.DigitalNotified {
		return true // обе вести уже разосланы
	}

	q := url.Values{}
	q.Set("language", "ru")
	if c.apiKey != "" {
		q.Set("api_key", c.apiKey)
	}
	q.Set("append_to_response", "external_ids,release_dates")

	fr, err := c.pool.FetchAPI(ctx, fmt.Sprintf("3/movie/%d", sub.TmdbID), q.Encode())
	if err != nil || fr.Status != 200 {
		log.Debug().Err(err).Int("tmdb_id", sub.TmdbID).Msg("calendar: TMDB movie fetch failed")
		return sub.ReleaseNotified
	}

	var mv tmdbMovieResponse
	if err := json.Unmarshal(fr.Body, &mv); err != nil {
		return sub.ReleaseNotified
	}

	title := mv.Title
	if title == "" {
		title = sub.Title
	}
	poster := mv.PosterPath
	if poster == "" {
		poster = sub.PosterPath
	}
	key := sub.Key()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	digital, hasDigital := mv.digitalDate()
	digitalOut := hasDigital && !digital.After(today)

	if !sub.ReleaseNotified {
		// "Released" alone is not enough: TMDB flips it on the first festival or
		// limited screening, often months before anything is watchable. Require the
		// release date to have actually passed too.
		if !strings.EqualFold(mv.Status, "Released") || mv.ReleaseDate == "" {
			return false
		}
		d, err := time.Parse("2006-01-02", mv.ReleaseDate)
		if err != nil || d.After(today) {
			return false
		}
		subscribers := c.store.SubscribersOfKey(key)
		if len(subscribers) > 0 {
			tgText := fmt.Sprintf("🎬 <b>%s</b>\nФильм вышел\n📅 %s", escapeHTML(title), formatDateRu(d))
			text := "Фильм вышел — " + formatDateRu(d)
			switch {
			case digitalOut:
				tgText = fmt.Sprintf("🎬 <b>%s</b>\nВышел в цифре — можно смотреть в хорошем качестве\n📅 %s", escapeHTML(title), formatDateRu(digital))
				text = "Вышел в цифре — " + formatDateRu(digital)
			case hasDigital:
				tgText = fmt.Sprintf("🎬 <b>%s</b>\nВышел в кино\n📅 %s\nЦифровой релиз — %s: напомним, когда можно будет смотреть в хорошем качестве",
					escapeHTML(title), formatDateRu(d), formatDateRu(digital))
				text = "Вышел в кино — " + formatDateRu(d) + ". Цифровой релиз — " + formatDateRu(digital)
			}
			n := Notification{Kind: NotifyRelease, TmdbID: sub.TmdbID, MediaType: KindMovie, Title: title, Text: text, PosterPath: poster}
			delivered := c.deliver(subscribers, n, tgText)
			log.Info().Str("movie", title).Int("notified", delivered).Bool("digital", digitalOut).Msg("calendar: movie release notification sent")
		}
		// Mark regardless of delivery count: the release happened, and retrying
		// forever would re-alert everyone each poll once one user muted both channels.
		c.store.MarkReleaseNotified(key)
		if digitalOut {
			c.store.MarkDigitalNotified(key) // уже сказали «вышел в цифре» в том же сообщении
		}
		return true
	}

	if sub.DigitalNotified || !digitalOut {
		return true
	}
	if today.Sub(digital) > digitalFreshDays*24*time.Hour {
		c.store.MarkDigitalNotified(key) // давно в цифре — новость устарела, гасим молча
		return true
	}
	var to []int64
	for _, ss := range c.store.SubscribersOfKeySince(key) {
		if ss.AddedAt.Before(digital.Add(24 * time.Hour)) {
			to = append(to, ss.TelegramID)
		}
	}
	if len(to) > 0 {
		tgText := fmt.Sprintf("🎬 <b>%s</b>\nВышел в цифре — можно смотреть в хорошем качестве\n📅 %s", escapeHTML(title), formatDateRu(digital))
		n := Notification{Kind: NotifyRelease, TmdbID: sub.TmdbID, MediaType: KindMovie, Title: title,
			Text: "Вышел в цифре — " + formatDateRu(digital), PosterPath: poster}
		delivered := c.deliver(to, n, tgText)
		log.Info().Str("movie", title).Int("notified", delivered).Msg("calendar: movie digital release notification sent")
	}
	c.store.MarkDigitalNotified(key)
	return true
}

// ---------------------------------------------------------------------------
//  Translations — notify when a voice-over that wasn't there before shows up
// ---------------------------------------------------------------------------

func (c *Cron) checkVoices(ctx context.Context, sub Subscription) {
	if !sub.TrackVoices || c.voices == nil {
		return
	}

	// A series must be drilled at a specific episode. LastSeason/LastEpisode are
	// filled by the first TMDB poll, so at worst voice tracking starts one cycle
	// later — far better than diffing against the episode list.
	if sub.TrackKind() == KindTV && (sub.LastSeason == 0 || sub.LastEpisode == 0) {
		return
	}

	current := c.voices(ctx, sub.TmdbID, sub.TrackKind(), sub.LastSeason, sub.LastEpisode)
	if len(current) == 0 {
		// nil/empty = the drill failed or found nothing yet. Treat as "unknown"
		// and keep the previous snapshot: overwriting it with an empty set would
		// replay the whole voice list as new the moment sources come back.
		return
	}

	key := sub.Key()
	curS, curE := sub.LastSeason, sub.LastEpisode

	// First poll after subscribing: record the baseline silently. Announcing
	// everything that already exists is noise, not news. A stale VoicesRev counts
	// as "no baseline" — the stored names came from a different resolve and are
	// not comparable with these. Смена серии — тоже «нет базы»: снимок от прошлой
	// серии несравним с дорожками новой.
	if len(sub.Voices) == 0 || sub.VoicesRev != VoicesRev ||
		sub.VoicesSeason != curS || sub.VoicesEpisode != curE {
		c.store.UpdateVoices(key, current, curS, curE)
		return
	}

	known := make(map[string]struct{}, len(sub.Voices))
	for _, v := range sub.Voices {
		known[normalizeVoice(v)] = struct{}{}
	}
	var fresh []string
	for _, v := range current {
		if _, ok := known[normalizeVoice(v)]; !ok {
			fresh = append(fresh, v)
		}
	}

	// ★База НАКОПИТЕЛЬНАЯ, а не «последний ответ». Список озвучек приходит сверлением
	// балансеров, и от опроса к опросу отвечают разные — набор скачет. С перезаписью
	// два набора объявляли друг друга по кругу: «Гангстерленд» 2026-09-10 прислал в
	// 20:17 «DniproFilm, HDRezka Studio 18+, RHS», а через час — «1WIN Studio,
	// NewComers, Red Head Sound, Амедиа, Яроцкий», и это одни и те же студии под
	// разными именами. Отсюда 519 рассылок за неделю. Теперь «новая» — та, которой
	// не было НИ РАЗУ для этой серии; пропавшая и вернувшаяся новостью не считается.
	// Persist the baseline first: if delivery panics or the process dies mid-fanout,
	// the worst case is a missed alert rather than an endless loop.
	c.store.UpdateVoices(key, mergeVoices(sub.Voices, current), curS, curE)

	if len(fresh) == 0 {
		return
	}

	// ★Доставка ПО ПОДПИСЧИКАМ, а не одним списком: у каждого свой набор ожидаемых
	// озвучек, и «жду Кубик в Кубе» не должно превращаться в «получаю все двадцать».
	// Заодно берём только тех, у кого отслеживание включено — раньше здесь стоял
	// SubscribersOfKey, и алерты про озвучки летели всем подписчикам тайтла.
	watchers := c.store.VoiceWatchersOfKey(key)
	if len(watchers) == 0 {
		return
	}

	// Группируем по итоговому списку: если все ждут «любую», это по-прежнему одна
	// рассылка, а не по одной на человека.
	groups := map[string][]int64{}
	texts := map[string][]string{}
	for _, wch := range watchers {
		mine := filterWanted(fresh, wch.Want)
		if len(mine) == 0 {
			continue
		}
		sig := strings.Join(mine, "\x00")
		groups[sig] = append(groups[sig], wch.TelegramID)
		texts[sig] = mine
	}
	if len(groups) == 0 {
		return
	}

	title := sub.Title
	for sig, ids := range groups {
		list := strings.Join(texts[sig], ", ")
		tgText := fmt.Sprintf("🎙 <b>%s</b>\nНовая озвучка: %s",
			escapeHTML(title), escapeHTML(list))

		n := Notification{
			Kind:       NotifyVoice,
			TmdbID:     sub.TmdbID,
			MediaType:  sub.TrackKind(),
			Title:      title,
			Text:       "Новая озвучка: " + list,
			PosterPath: sub.PosterPath,
		}

		delivered := c.deliver(ids, n, tgText)
		log.Info().
			Str("title", title).
			Strs("voices", texts[sig]).
			Int("notified", delivered).
			Msg("calendar: new translation notification sent")
	}
}

// filterWanted оставляет из новых озвучек только те, которых ждёт подписчик.
// Пустой список ожиданий = ждать любую.
func filterWanted(fresh, want []string) []string {
	if len(want) == 0 {
		return fresh
	}
	out := make([]string, 0, len(fresh))
	for _, v := range fresh {
		if voiceWanted(v, want) {
			out = append(out, v)
		}
	}
	return out
}

// voiceWanted — НЕСТРОГОЕ сравнение в обе стороны.
//
// Точное совпадение здесь не работает: источники пишут одну и ту же студию как
// «Кубик в Кубе», «Кубик в кубе (18+)» и «Кубик в Кубе [MVO]», а пользователь
// наберёт просто «кубик». Поэтому совпадением считается вхождение одной строки
// в другую после нормализации.
func voiceWanted(voice string, want []string) bool {
	v := normalizeVoice(voice)
	if v == "" {
		return false
	}
	for _, w := range want {
		w = normalizeVoice(w)
		if w == "" {
			continue
		}
		if strings.Contains(v, w) || strings.Contains(w, v) {
			return true
		}
	}
	return false
}

// normalizeVoice makes voice comparison stable across cosmetic churn — sources
// re-case and re-space the same studio name constantly, and each variant would
// otherwise read as a brand-new translation.
// mergeVoices — объединение «всё, что видели» и текущего ответа, без повторов по
// нормализованному имени. Список ограничен: у популярного тайтла имён много, но
// подписка не должна расти без предела.
func mergeVoices(known, current []string) []string {
	const maxVoices = 300
	out := make([]string, 0, len(known)+len(current))
	seen := make(map[string]struct{}, len(known)+len(current))
	for _, list := range [][]string{known, current} {
		for _, v := range list {
			n := normalizeVoice(v)
			if n == "" {
				continue
			}
			if _, dup := seen[n]; dup {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, v)
			if len(out) >= maxVoices {
				return out
			}
		}
	}
	return out
}

func normalizeVoice(v string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(v))), " ")
}
