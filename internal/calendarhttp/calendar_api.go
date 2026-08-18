package calendarhttp

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"lampac-go/internal/calendar"
	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// ---------------------------------------------------------------------------
//  Helper: resolve TG ID from request (token or uid query param)
// ---------------------------------------------------------------------------

// tgResolver отвечает на «кто это». Параметром, а не жёстко зашитой cookie-проверкой:
// у подписок теперь ДВА входа с разной авторизацией — клиенты приходят с lampac_token,
// мини-апп бота с подписанным initData Telegram. Сама работа с подписками одна.
type tgResolver func(*http.Request) int64

// kitResolver разбирает авторизацию мини-аппа. Ставится хостом (только он умеет
// проверять подпись initData); пока не установлен — 0, и kit-роуты честно отвечают
// «не авторизован» вместо того, чтобы пускать кого угодно.
var kitResolver tgResolver

// SetKitResolver подключает авторизацию мини-аппа бота.
func SetKitResolver(fn func(*http.Request) int64) { kitResolver = fn }

func resolveKit(r *http.Request) int64 {
	if kitResolver == nil {
		return 0
	}
	return kitResolver(r)
}

func calendarTgID(r *http.Request, store *tgauth.Store) int64 {
	if store == nil {
		return 0
	}

	// 1. Try lampac_token cookie (web users with TG auth).
	if cookie, err := r.Cookie("lampac_token"); err == nil && cookie.Value != "" {
		if at, ok := store.Lookup(cookie.Value); ok && at.TelegramID != 0 {
			return at.TelegramID
		}
	}

	// 2. Try explicit token query param.
	token := r.URL.Query().Get("token")
	if token != "" {
		if at, ok := store.Lookup(token); ok && at.TelegramID != 0 {
			return at.TelegramID
		}
	}

	// 3. Fallback: resolve via device UID (works even if wrong token was sent).
	uid := r.URL.Query().Get("uid")
	if uid != "" {
		if found := store.FindTokenByDeviceUID(uid); found != "" {
			if at, ok := store.Lookup(found); ok && at.TelegramID != 0 {
				return at.TelegramID
			}
		}
	}

	return 0
}

// ---------------------------------------------------------------------------
//  GET /api/calendar/upcoming — upcoming episodes for the user
// ---------------------------------------------------------------------------

func calendarUpcomingHandler(calStore *calendar.Store, tgStore *tgauth.Store, cfg config.CalendarConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := calendarTgID(r, tgStore)
		var eps []calendar.EpisodeInfo
		if tgID != 0 {
			eps = calStore.UserUpcoming(tgID, cfg.UpcomingDays, cfg.RecentDays)
		} else {
			eps = calStore.AllUpcoming(cfg.UpcomingDays, cfg.RecentDays)
		}
		if eps == nil {
			eps = []calendar.EpisodeInfo{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"episodes": eps,
		})
	}
}

// ---------------------------------------------------------------------------
//  GET /api/calendar/subscriptions — user's tracked shows
// ---------------------------------------------------------------------------

func calendarSubscriptionsHandler(calStore *calendar.Store, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"shows": []any{}, "msg": "Авторизуйтесь через бот"})
			return
		}
		shows := calStore.ListByUser(tgID)
		if shows == nil {
			shows = []calendar.Subscription{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"shows":      shows,
			"notify_tg":  calStore.GetNotifyTG(tgID),
			"notify_app": calStore.GetNotifyApp(tgID),
		})
	}
}

// ---------------------------------------------------------------------------
//  POST /api/calendar/subscribe — track a show
// ---------------------------------------------------------------------------

func calendarSubscribeHandler(calStore *calendar.Store, tgStore *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := calendarTgID(r, tgStore)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		var req struct {
			ImdbID      string `json:"imdb_id"`
			TmdbID      int    `json:"tmdb_id"`
			Kind        string `json:"kind"`         // "tv" (default) | "movie"
			TrackVoices bool   `json:"track_voices"` // also alert on new translations
			Title       string `json:"title"`
			PosterPath  string `json:"poster_path"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
			return
		}
		if req.TmdbID == 0 && req.ImdbID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tmdb_id or imdb_id required"})
			return
		}
		kind := calendar.KindTV
		if req.Kind == calendar.KindMovie {
			kind = calendar.KindMovie
		}

		sub := calendar.Subscription{
			ImdbID:      req.ImdbID,
			TmdbID:      req.TmdbID,
			Kind:        kind,
			TrackVoices: req.TrackVoices,
			Title:       req.Title,
			PosterPath:  req.PosterPath,
		}
		calStore.Subscribe(tgID, sub)
		// Echo the key back: it's what unsubscribe/status take, and the client
		// shouldn't have to re-derive how a subscription is identified.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": sub.Key()})
	}
}

// ---------------------------------------------------------------------------
//  POST /api/calendar/unsubscribe — stop tracking a show
// ---------------------------------------------------------------------------

func calendarUnsubscribeHandler(calStore *calendar.Store, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		var req struct {
			Key    string `json:"key"`     // preferred: what subscribe echoed back
			ImdbID string `json:"imdb_id"` // legacy callers
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
			return
		}
		ref := req.Key
		if ref == "" {
			ref = req.ImdbID
		}
		if ref == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "key or imdb_id required"})
			return
		}

		if calStore.Unsubscribe(tgID, ref) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "msg": "not found"})
		}
	}
}

// ---------------------------------------------------------------------------
//  POST /api/calendar/notify — toggle TG notifications
// ---------------------------------------------------------------------------

func calendarNotifyHandler(calStore *calendar.Store, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 256))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}
		// `channel` selects which delivery to toggle; absent means "tg", so the
		// original single-channel callers keep working unchanged.
		var req struct {
			Enabled bool   `json:"enabled"`
			Channel string `json:"channel"` // "tg" (default) | "app"
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
			return
		}
		if req.Channel == "app" {
			calStore.SetNotifyApp(tgID, req.Enabled)
		} else {
			calStore.SetNotifyTG(tgID, req.Enabled)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"notify_tg":  calStore.GetNotifyTG(tgID),
			"notify_app": calStore.GetNotifyApp(tgID),
		})
	}
}

// ---------------------------------------------------------------------------
//  GET /api/calendar/popular — top shows by subscriber count
// ---------------------------------------------------------------------------

func calendarPopularHandler(calStore *calendar.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		popular := calStore.PopularShows(30)
		if popular == nil {
			popular = []calendar.PopularShow{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"shows": popular,
		})
	}
}

// ---------------------------------------------------------------------------
//  GET /api/calendar/check — check if user tracks a specific show
// ---------------------------------------------------------------------------

func calendarCheckHandler(calStore *calendar.Store, tgStore *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := calendarTgID(r, tgStore)
		imdbID := strings.TrimSpace(r.URL.Query().Get("imdb_id"))
		if tgID == 0 || imdbID == "" {
			writeJSON(w, http.StatusOK, map[string]any{"subscribed": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"subscribed": calStore.IsSubscribed(tgID, imdbID),
		})
	}
}

// ---------------------------------------------------------------------------
//  Admin: GET /{admin}/api/calendar — stats + popular shows
// ---------------------------------------------------------------------------

func tgAdminCalendarHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, calStore *calendar.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore)
		if !ok {
			return
		}
		users, shows := calStore.Stats()
		popular := calStore.PopularShows(20)
		if popular == nil {
			popular = []calendar.PopularShow{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"users":   users,
			"shows":   shows,
			"popular": popular,
		})
	}
}

// ---------------------------------------------------------------------------
//  Admin: POST /{admin}/api/calendar/check-now — trigger manual poll
// ---------------------------------------------------------------------------

func tgAdminCalendarCheckNowHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, cron *calendar.Cron) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore)
		if !ok {
			return
		}
		go cron.RunNow(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": "poll started"})
	}
}

// ---------------------------------------------------------------------------
//  calendarBotAdapter bridges calendar.Store → tgauth.CalendarHandler
// ---------------------------------------------------------------------------

type calendarBotAdapter struct {
	store *calendar.Store
	cfg   config.CalendarConfig
}

func newCalendarBotAdapter(store *calendar.Store, cfg config.CalendarConfig) *calendarBotAdapter {
	return &calendarBotAdapter{store: store, cfg: cfg}
}

func (a *calendarBotAdapter) TrackedCount(tgID int64) int {
	return len(a.store.ListByUser(tgID))
}

func (a *calendarBotAdapter) TrackedTitles(tgID int64, limit int) []string {
	subs := a.store.ListByUser(tgID)
	var out []string
	for i, s := range subs {
		if i >= limit {
			break
		}
		out = append(out, s.Title)
	}
	return out
}

func (a *calendarBotAdapter) GetNotifyTG(tgID int64) bool {
	return a.store.GetNotifyTG(tgID)
}

func (a *calendarBotAdapter) SetNotifyTG(tgID int64, enabled bool) {
	a.store.SetNotifyTG(tgID, enabled)
}

func (a *calendarBotAdapter) UpcomingSummary(tgID int64, limit int) []string {
	eps := a.store.UserUpcoming(tgID, a.cfg.UpcomingDays, a.cfg.RecentDays)
	var out []string
	for i, ep := range eps {
		if i >= limit {
			break
		}
		name := ep.Name
		if name != "" {
			name = " «" + name + "»"
		}
		out = append(out, fmt.Sprintf("• <b>%s</b> S%02dE%02d%s — %s",
			ep.ShowTitle, ep.Season, ep.Episode, name, ep.AirDate))
	}
	return out
}
