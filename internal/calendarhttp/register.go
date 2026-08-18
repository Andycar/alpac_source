package calendarhttp

import (
	"net/http"

	"lampac-go/internal/calendar"
	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/tmdbcache"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// inbox holds the in-app notification store for the lifetime of the process.
// Package-level for the same reason deps is: the handlers are constructed inside
// RegisterRoutes, and the host needs a handle afterwards to attach live push.
var inbox *calendar.Inbox

// Inbox returns the in-app notification store, or nil when the calendar is
// disabled. The host uses it to wire live (websocket) delivery on top of the
// persisted inbox.
func Inbox() *calendar.Inbox { return inbox }

// RegisterRoutes wires the public /api/calendar/* endpoints when
// cfg.Calendar.Enable and returns the store + cron so the host's admin routes and
// Server lifecycle can access them (nil,nil when disabled). hostDeps injects the
// admin-auth gate used by the admin routes registered later via RegisterAdminRoutes.
func RegisterRoutes(router chi.Router, cfg config.Config, hostDeps Deps, tmdbPool *tmdbcache.Pool, tgTokenStore *tgauth.Store, tgBot *tgauth.Bot) (*calendar.Store, *calendar.Cron) {
	if !cfg.Calendar.Enable {
		return nil, nil
	}
	setDeps(hostDeps)
	store := calendar.New(cfg.Compat.RepoRoot)
	var notifier calendar.Notifier
	if tgBot != nil {
		notifier = tgBot
	}
	cron := calendar.NewCron(store, tmdbPool, notifier, cfg.Calendar, cfg.TMDBProxy.APIKey)

	// In-app channel. Wired unconditionally so alerts reach the app even on a
	// deployment with no Telegram bot configured; the live-push half stays nil
	// until the host supplies it (see calendar.Cron.SetAppDelivery).
	inbox = calendar.NewInbox(cfg.Compat.RepoRoot)
	cron.SetAppDelivery(inbox, nil)

	// Очередь отложенных уведомлений: её разбирает крон раз в минуту.
	pendingRef = calendar.NewPendingStore(cfg.Compat.RepoRoot)
	cron.SetPending(pendingRef)

	// Cookie-авторизация: web и нативные клиенты.
	byCookie := func(r *http.Request) int64 { return calendarTgID(r, tgTokenStore) }

	router.Get("/api/calendar/upcoming", calendarUpcomingHandler(store, tgTokenStore, cfg.Calendar))
	router.Get("/api/calendar/subscriptions", calendarSubscriptionsHandler(store, byCookie))
	router.Post("/api/calendar/subscribe", calendarSubscribeHandler(store, tgTokenStore))
	router.Post("/api/calendar/unsubscribe", calendarUnsubscribeHandler(store, byCookie))
	router.Post("/api/calendar/notify", calendarNotifyHandler(store, byCookie))
	router.Get("/api/calendar/popular", calendarPopularHandler(store))
	router.Get("/api/calendar/check", calendarCheckHandler(store, tgTokenStore))
	router.Get("/api/calendar/notifications", calendarNotificationsHandler(inbox, byCookie))
	router.Post("/api/calendar/notifications/read", calendarNotificationsReadHandler(inbox, byCookie))
	router.Post("/api/calendar/notifications/clear", calendarNotificationsClearHandler(inbox, byCookie))
	// Те же операции для мини-аппа бота: управлять подписками и уведомлениями в
	// Telegram удобнее, чем пультом. Роуты фиксируются на старте, поэтому
	// регистрируем безусловно — до SetKitResolver они отвечают «не авторизован».
	router.Get("/api/kit/calendar/subscriptions", calendarSubscriptionsHandler(store, resolveKit))
	router.Post("/api/kit/calendar/unsubscribe", calendarUnsubscribeHandler(store, resolveKit))
	router.Post("/api/kit/calendar/notify", calendarNotifyHandler(store, resolveKit))
	router.Post("/api/kit/calendar/voices", calendarTrackVoicesHandler(store, resolveKit))
	router.Get("/api/kit/calendar/notifications", calendarNotificationsHandler(inbox, resolveKit))
	router.Post("/api/kit/calendar/notifications/read", calendarNotificationsReadHandler(inbox, resolveKit))
	router.Post("/api/kit/calendar/notifications/clear", calendarNotificationsClearHandler(inbox, resolveKit))
	// И для обычных клиентов — переключатель озвучек по подписке.
	router.Post("/api/calendar/voices", calendarTrackVoicesHandler(store, byCookie))
	router.Get("/api/calendar/delivery", calendarDeliveryGetHandler(store, byCookie))
	router.Post("/api/calendar/delivery", calendarDeliverySetHandler(store, byCookie))
	router.Post("/api/calendar/delivery/flush", calendarDeliveryFlushHandler(byCookie))
	router.Get("/api/kit/calendar/delivery", calendarDeliveryGetHandler(store, resolveKit))
	router.Post("/api/kit/calendar/delivery", calendarDeliverySetHandler(store, resolveKit))
	router.Post("/api/kit/calendar/delivery/flush", calendarDeliveryFlushHandler(resolveKit))

	if tgBot != nil {
		tgBot.SetCalendar(newCalendarBotAdapter(store, cfg.Calendar))
	}
	users, shows := store.Stats()
	log.Info().Int("users", users).Int("shows", shows).Msg("calendar: loaded")
	return store, cron
}

// RegisterAdminRoutes wires the admin-panel /{adminPath}/api/calendar* endpoints.
// Called from the host's admin router (which owns adminPath + the AdminIDStore);
// only invoked when the calendar store exists, i.e. after RegisterRoutes ran setDeps.
func RegisterAdminRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, calStore *calendar.Store, calCron *calendar.Cron) {
	router.Get("/"+adminPath+"/api/calendar", tgAdminCalendarHandler(tgStore, adminStore, calStore))
	if calCron != nil {
		router.Post("/"+adminPath+"/api/calendar/check-now", tgAdminCalendarCheckNowHandler(tgStore, adminStore, calCron))
	}
}
