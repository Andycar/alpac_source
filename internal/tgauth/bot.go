package tgauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// escapeHTML escapes a string for safe interpolation into Telegram HTML-mode
// messages.
func escapeHTML(s string) string { return html.EscapeString(s) }

// BotConfig holds Telegram bot credentials and admin info.
type BotConfig struct {
	Token             string // Bot API token
	AdminID           int64  // Admin Telegram user ID
	BotName           string // Bot username (without @)
	MaxDevicesPerUser int    // max devices per user (for auto-approve check)
	KitServerHost     string // Public server URL for Kit WebApp (e.g. "https://example.com")
	PublicHost        string // canonical SPA URL for bot links (room invites); falls back to KitServerHost
	AutoApprove       bool   // auto-approve new users without admin confirmation
	AutoApproveDays   int    // default approval duration in days (default 30)
}

// renameState tracks a user awaiting device rename input.
type renameState struct {
	Token string
	UID   string
}

// FeedbackSubmitter is the interface for creating feedback tickets from the bot.
// Implemented by httpapi.FeedbackStore — avoids circular import.
type FeedbackSubmitter interface {
	CreateTicket(userID, userName string, category, priority, subject, message string) (ticketID string, err error)
	UserTickets(userID string) []FeedbackTicketInfo
	GetTicket(id string) (FeedbackTicketDetail, bool)
	AddUserReply(ticketID, userID, userName, message string) error
}

// YouTubeAuthProvider handles YouTube OAuth Device Flow.
// Implemented by ytauth.Provider — avoids circular import.
type YouTubeAuthProvider interface {
	StartAuth(tgID int64) (userCode, verificationURL string, pollDone <-chan error, err error)
	IsLinked(tgID int64) bool
	Unlink(tgID int64) error
	ChannelTitle(tgID int64) string
}


// CalendarHandler provides calendar data for the bot.
// Implemented by httpapi.calendarBotAdapter — avoids circular import.
type CalendarHandler interface {
	// TrackedCount returns how many shows a user tracks.
	TrackedCount(tgID int64) int
	// TrackedTitles returns show titles (up to limit).
	TrackedTitles(tgID int64, limit int) []string
	// GetNotifyTG returns notification preference.
	GetNotifyTG(tgID int64) bool
	// SetNotifyTG toggles TG notifications.
	SetNotifyTG(tgID int64, enabled bool)
	// UpcomingSummary returns formatted upcoming episode lines for bot display.
	UpcomingSummary(tgID int64, limit int) []string
}

// FeedbackTicketInfo carries just enough info for bot list display.
type FeedbackTicketInfo struct {
	ID        string
	Subject   string
	Status    string // admin taxonomy: open | in_progress | resolved | closed
	Category  string
	CreatedAt string
	Replies   int
	// LastReplyAdmin says the LAST word in the thread is support's — i.e. there
	// is something for the user to read. The admin statuses above describe the
	// operator's workflow ("в работе"), not the only thing the user wants to
	// know: has anyone answered me yet.
	LastReplyAdmin bool
	// UpdatedAt is the last activity (RFC3339); the list is ordered by it so a
	// thread that just got an answer is not buried under older ones.
	UpdatedAt string
}

// FeedbackTicketDetail carries full info for single ticket display.
type FeedbackTicketDetail struct {
	ID        string
	UserID    string
	Subject   string
	Message   string
	Status    string
	Category  string
	Priority  string
	CreatedAt string
	Replies   []FeedbackReplyInfo
}

// FeedbackReplyInfo represents a single reply in the thread.
type FeedbackReplyInfo struct {
	IsAdmin   bool
	Author    string
	Message   string
	CreatedAt string
}

// feedbackState tracks multi-step feedback creation / reply dialog.
type feedbackState struct {
	Step     int    // 0=category, 1=subject, 2=message, 10=reply to ticket
	Category string // "help"|"thanks"|"bug"|"feature"|"other"
	Subject  string
	TicketID string // for reply mode (Step=10)
	// Client/Platform — «где именно». Без них тикет приходил без контекста, и первым
	// ответом всегда был вопрос «у вас плагин или приложение, и на чём?».
	Client   string // "lampa"|"app"|"msx"|"other"
	Platform string // "androidtv"|"phone"|"samsung"|"lg"|"hisense"|"appletv"|"browser"|"other"
}

// Bot implements Telegram long-polling and admin approval flow.
type Bot struct {
	cfg                 BotConfig
	store               *Store
	pending             *PendingStore
	devicePending       *DevicePendingStore
	langStore           *LangStore
	client              *http.Client
	baseURL             string
	stopCh              chan struct{}
	doneCh              chan struct{}
	adminPath           string                                    // random admin panel path (e.g. "cp_xYz12Ab3cd")
	renameWait          map[int64]*renameState                    // tgID → waiting for rename input
	feedbackSub         FeedbackSubmitter                         // feedback store (set via SetFeedbackStore)
	feedbackWait        map[int64]*feedbackState                  // tgID → feedback creation dialog
	ytAuth              YouTubeAuthProvider                       // YouTube OAuth (set via SetYouTubeAuth)
	calendarH           CalendarHandler                           // Calendar store (set via SetCalendar)
	membership          *MembershipChecker                        // required channel/group subscription checker
	sisiConfirmCallback func(requestID string, allowed bool) bool // parental control callback
	groupStore          *GroupStore                               // user groups (set via SetGroupStore)
	adminIDLister       AdminIDLister                             // returns telegram IDs of admin-panel users (set via SetAdminIDLister)
	profileMgr          ProfileManager                            // sync profiles (set via SetProfileManager)
	profileWait         map[int64]*profileBotState                // tgID → multi-step /profiles dialog

	notifyMu        sync.Mutex
	notifyLastFlood map[int64]time.Time // per-chat throttle for NotifyAdmins

	migNotifyMu sync.Mutex
	migNotifyAt map[string]time.Time // "tgID|label" → last DeviceMigrated notice (anti-ping-pong throttle)

	// admin→user reply: a notification the bot sent to an admin's chat maps its message_id to the
	// user it's about, so when the admin REPLIES to it the bot relays the text to that user. See
	// bot_reply.go. Bounded; oldest entries drop.
	replyMu      sync.Mutex
	replyTargets map[replyKey]replyTarget
}

// SetProfileManager wires the sync-profile store. Used by the /profiles
// command to let TG users manage their family / per-device profiles
// without leaving the chat. Safe to call with nil — /profiles then
// responds with a polite "feature not configured" reply.
func (b *Bot) SetProfileManager(m ProfileManager) {
	if b == nil {
		return
	}
	b.profileMgr = m
	if b.profileWait == nil {
		b.profileWait = make(map[int64]*profileBotState)
	}
}

// AdminIDLister abstracts the admin store so NotifyAdmins can broadcast to all
// configured admins (super-admin + delegates). The concrete implementation is
// *AdminIDStore.List() projected to telegram IDs.
type AdminIDLister interface {
	AdminTelegramIDs() []int64
}

// SetAdminIDLister wires the admin source for NotifyAdmins broadcasts.
func (b *Bot) SetAdminIDLister(l AdminIDLister) {
	if b == nil {
		return
	}
	b.adminIDLister = l
}

// NotifyAdmins sends an HTML-formatted alert to every configured admin panel
// user (super admin + delegates). Falls back to b.cfg.AdminID alone if no
// AdminIDLister was wired. Per-chat throttle: at most one alert / 30s — use
// this for repeatable health/degraded alerts where flooding is the risk.
func (b *Bot) NotifyAdmins(text string) {
	b.notifyAdmins(text, true)
}

// NotifyAdminsNow is NotifyAdmins WITHOUT the 30s per-chat flood throttle.
// Use for important, low-volume, one-shot transactional events (e.g. an
// invoice was paid) that must never be silently dropped just because another
// alert happened to fire in the last 30 seconds. It does not touch the flood
// throttle bookkeeping, so it can't starve or be starved by NotifyAdmins.
func (b *Bot) NotifyAdminsNow(text string) {
	b.notifyAdmins(text, false)
}

// notifyAdmins is the shared implementation. When throttle is true, each chat
// receives at most one message per 30s (degraded-alert flood guard); when
// false, every message is dispatched.
func (b *Bot) notifyAdmins(text string, throttle bool) {
	if b == nil || b.cfg.Token == "" || strings.TrimSpace(text) == "" {
		return
	}
	if len(text) > 4000 {
		text = text[:4000] + "…"
	}
	ids := []int64{}
	if b.adminIDLister != nil {
		ids = b.adminIDLister.AdminTelegramIDs()
	}
	if len(ids) == 0 && b.cfg.AdminID != 0 {
		ids = []int64{b.cfg.AdminID}
	}
	dispatch := make([]int64, 0, len(ids))
	if throttle {
		now := time.Now()
		b.notifyMu.Lock()
		if b.notifyLastFlood == nil {
			b.notifyLastFlood = make(map[int64]time.Time)
		}
		for _, id := range ids {
			if id == 0 {
				continue
			}
			if last, ok := b.notifyLastFlood[id]; ok && now.Sub(last) < 30*time.Second {
				continue
			}
			b.notifyLastFlood[id] = now
			dispatch = append(dispatch, id)
		}
		b.notifyMu.Unlock()
	} else {
		seen := make(map[int64]bool, len(ids))
		for _, id := range ids {
			if id == 0 || seen[id] {
				continue
			}
			seen[id] = true
			dispatch = append(dispatch, id)
		}
	}
	for _, id := range dispatch {
		// HTML-mode is what fireDegraded composes. We use the lower-level
		// apiPost rather than sendMsg so the parse_mode is HTML, not Markdown.
		b.apiPost("sendMessage", map[string]any{
			"chat_id":    id,
			"text":       text,
			"parse_mode": "HTML",
		})
	}
}

// NewBot creates a new Telegram bot.
func NewBot(cfg BotConfig, store *Store, pending *PendingStore, devicePending *DevicePendingStore, langStore *LangStore) *Bot {
	return &Bot{
		cfg:           cfg,
		store:         store,
		pending:       pending,
		devicePending: devicePending,
		langStore:     langStore,
		client:        httpclient.New(60 * time.Second),
		baseURL:       "https://api.telegram.org/bot" + cfg.Token,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
		renameWait:    make(map[int64]*renameState),
		feedbackWait:  make(map[int64]*feedbackState),
	}
}

// setupMenuButton sets the bot's Menu Button to open Kit WebApp (if configured).
func (b *Bot) setupMenuButton() {
	kitURL := b.kitURL()
	if kitURL == "" {
		return
	}
	menuButton := map[string]any{
		"type":    "web_app",
		"text":    "Settings",
		"web_app": map[string]string{"url": kitURL},
	}
	b.apiPost("setChatMenuButton", map[string]any{
		"menu_button": menuButton,
	})
	log.Info().Str("url", kitURL).Msg("tgauth: menu button set to Kit WebApp")
}

// Start begins long-polling for updates in a goroutine.
func (b *Bot) Start(ctx context.Context) {
	b.setupMenuButton()
	go func() {
		defer close(b.doneCh)
		var offset int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-b.stopCh:
				return
			default:
			}

			updates, err := b.getUpdates(ctx, offset)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Warn().Err(err).Msg("tgauth: getUpdates error")
				time.Sleep(3 * time.Second)
				continue
			}

			for _, u := range updates {
				if u.UpdateID >= offset {
					offset = u.UpdateID + 1
				}
				if u.Message != nil {
					b.handleMessage(u.Message)
				}
				if u.CallbackQuery != nil {
					b.handleCallbackQuery(u.CallbackQuery)
				}
			}
		}
	}()
	log.Info().Str("bot", b.cfg.BotName).Msg("tgauth: bot started")
}

// SetAdminPath sets the admin panel path so the bot can send links via /admin.
func (b *Bot) SetAdminPath(path string) {
	b.adminPath = path
}

// NotifyFeedbackReply notifies a user about admin reply to their ticket.
func (b *Bot) NotifyFeedbackReply(userTgID int64, subject, message string) {
	if b == nil || b.cfg.Token == "" {
		return
	}
	lang := b.langByID(userTgID)
	t := T(lang)
	text := fmt.Sprintf(t.FeedbackReplyNotify, subject, message)
	if len(text) > 4000 {
		text = text[:4000] + "..."
	}
	b.sendMsg(userTgID, text, nil)
}

// langByID returns the user's language by TG ID (no auto-detect).
func (b *Bot) langByID(tgID int64) Lang {
	l, ok := b.langStore.Get(tgID)
	if !ok {
		return LangRU
	}
	return l
}

// SetFeedbackStore connects the feedback store to the bot for ticket submission.
func (b *Bot) SetFeedbackStore(s FeedbackSubmitter) {
	b.feedbackSub = s
}

// SetAutoApprove updates the auto-approve setting at runtime (hot-reload from admin panel).
func (b *Bot) SetAutoApprove(enabled bool, days int) {
	b.cfg.AutoApprove = enabled
	b.cfg.AutoApproveDays = days
}

// SetYouTubeAuth connects the YouTube OAuth provider to the bot.
func (b *Bot) SetYouTubeAuth(p YouTubeAuthProvider) {
	b.ytAuth = p
}

// SetCalendar connects the content calendar to the bot.
func (b *Bot) SetCalendar(h CalendarHandler) {
	b.calendarH = h
}

// SetGroupStore connects the user group store to the bot for profile display.
func (b *Bot) SetGroupStore(gs *GroupStore) {
	b.groupStore = gs
}

// SetMembershipChecker connects the membership checker to the bot.
func (b *Bot) SetMembershipChecker(mc *MembershipChecker) {
	b.membership = mc
}

// checkMembershipOrReject verifies required channel/group subscriptions.
// Returns true if the user passes (or no requirements). If false,
// sends a message to the user and returns false.
func (b *Bot) checkMembershipOrReject(chatID int64, tgID int64) bool {
	if b.membership == nil || !b.membership.HasRequirements() {
		return true
	}
	ok, msg := b.membership.CheckMembership(tgID)
	if ok {
		return true
	}
	b.sendMsg(chatID, "❌ "+msg, nil)
	return false
}

// SetSisiConfirmCallback sets the callback for sisi parental control inline buttons.
func (b *Bot) SetSisiConfirmCallback(fn func(requestID string, allowed bool) bool) {
	b.sisiConfirmCallback = fn
}

// SetHTTPClient replaces the bot's HTTP client (e.g. to route through a proxy).
func (b *Bot) SetHTTPClient(c *http.Client) {
	b.client = c
}

// SendToUser sends an HTML message to a specific Telegram user.
// Used by calendar cron for episode notifications.
func (b *Bot) SendToUser(tgID int64, text string) {
	b.sendMsg(tgID, text, nil)
}

// SendSisiConfirm sends an inline keyboard with Allow/Deny buttons for sisi parental control.
func (b *Bot) SendSisiConfirm(tgID int64, requestID string) {
	keyboard := &tgInlineKeyboardMarkup{
		InlineKeyboard: [][]tgInlineKeyboardButton{
			{
				{Text: "✅ Разрешить", CallbackData: "sisi_allow:" + requestID},
				{Text: "❌ Запретить", CallbackData: "sisi_deny:" + requestID},
			},
		},
	}
	b.sendMsg(tgID, "🔐 <b>Родительский контроль</b>\n\nЗапрос на вход в раздел Клубничка.\nРазрешить доступ?", keyboard)
}

// BroadcastResult holds the result of a broadcast message operation.
type BroadcastResult struct {
	Total  int `json:"total"`
	Sent   int `json:"sent"`
	Failed int `json:"failed"`
}

// Broadcast sends an HTML message to all active (non-expired) users.
// Deduplicates by TelegramID to avoid sending multiple messages to the same user.
func (b *Bot) Broadcast(store *Store, text string) BroadcastResult {
	tokens := store.List()
	now := time.Now().UTC()
	seen := map[int64]bool{}
	var res BroadcastResult
	for _, t := range tokens {
		if now.After(t.ExpiresAt) || t.TelegramID == 0 || seen[t.TelegramID] {
			continue
		}
		seen[t.TelegramID] = true
		res.Total++
		b.sendMsg(t.TelegramID, text, nil)
		res.Sent++
		// Telegram rate limit: ~30 msg/sec
		time.Sleep(35 * time.Millisecond)
	}
	log.Info().Int("total", res.Total).Int("sent", res.Sent).Msg("tgauth: broadcast completed")
	return res
}

// ActiveUserCount returns the number of unique active (non-expired) TG users.
func (b *Bot) ActiveUserCount(store *Store) int {
	tokens := store.List()
	now := time.Now().UTC()
	seen := map[int64]bool{}
	for _, t := range tokens {
		if now.After(t.ExpiresAt) || t.TelegramID == 0 || seen[t.TelegramID] {
			continue
		}
		seen[t.TelegramID] = true
	}
	return len(seen)
}

// NotifyDeviceVerification sends a device-verification code to the user via TG.
func (b *Bot) NotifyDeviceVerification(tgID int64, code, label string) {
	t := T(b.langByID(tgID))
	b.sendMsg(tgID, fmt.Sprintf(t.DeviceVerifyCode, label, code), nil)
}

// NotifyNewDeviceBound informs the user that a new device was auto-bound.
func (b *Bot) NotifyNewDeviceBound(tgID int64, label, uid string) {
	t := T(b.langByID(tgID))
	b.sendMsg(tgID, fmt.Sprintf(t.DeviceBoundAuto, label, uid), nil)
}

// NotifyDeviceMigrated informs the user that a device was recognized
// by fingerprint and its UID was updated (e.g. after localStorage wipe).
// Rate-limited per (user, label): UID migration is a background bookkeeping
// event, and two devices that ever alias each other (same fingerprint or a
// legacy no-fp client pair) would otherwise ping-pong the slot on EVERY
// request — testers got a bot beep each time they switched a source
// («меняешь источник на одном — бибикает, на другом — бибикает»).
func (b *Bot) NotifyDeviceMigrated(tgID int64, label, oldUID, newUID string) {
	key := fmt.Sprintf("%d|%s", tgID, label)
	now := time.Now()
	b.migNotifyMu.Lock()
	if b.migNotifyAt == nil {
		b.migNotifyAt = make(map[string]time.Time)
	}
	last, seen := b.migNotifyAt[key]
	if seen && now.Sub(last) < 12*time.Hour {
		b.migNotifyMu.Unlock()
		return
	}
	b.migNotifyAt[key] = now
	if len(b.migNotifyAt) > 4096 { // bound the map; stale entries only delay a notice
		for k, v := range b.migNotifyAt {
			if now.Sub(v) > 24*time.Hour {
				delete(b.migNotifyAt, k)
			}
		}
	}
	b.migNotifyMu.Unlock()
	t := T(b.langByID(tgID))
	b.sendMsg(tgID, fmt.Sprintf(t.DeviceMigrated, label, oldUID, newUID), nil)
}

// Stop gracefully shuts down the bot.
func (b *Bot) Stop() {
	select {
	case <-b.stopCh:
	default:
		close(b.stopCh)
	}
	<-b.doneCh
}

// mainMenuKeyboard returns the persistent reply keyboard for users.
func (b *Bot) mainMenuKeyboard(lang Lang) *tgReplyKeyboardMarkup {
	t := T(lang)
	rows := [][]tgKeyboardButton{
		{{Text: t.BtnProfile}},
		// Устройства вернулись на главную клавиатуру: спрятанные во второй уровень
		// (inline-кнопка внутри «Профиля») их просто не находили — «добавьте в бота
		// отображение всех устройств» пришло про экран, который уже существовал.
		{{Text: t.BtnDevices}},
	}
	if b.kitURL() != "" {
		rows = append(rows, []tgKeyboardButton{
			{Text: t.BtnSettings},
			{Text: t.BtnRemote},
		})
	}
	if b.feedbackSub != nil {
		// ОДНА кнопка: «Обратная связь» открывает раздел, а уже внутри — новое
		// обращение и список своих со всеми ответами. Отдельная кнопка «Мои
		// обращения» в главном меню решала ту же задачу двумя входами и
		// оставляла раздел таким же плоским.
		rows = append(rows, []tgKeyboardButton{
			{Text: t.BtnFeedback},
		})
	}
	// Language still lives in the Profile inline keyboard; Devices is here.
	return &tgReplyKeyboardMarkup{
		Keyboard:       rows,
		ResizeKeyboard: true,
	}
}

// kitURL returns the Kit WebApp URL if configured.
// Returns empty string if host is not HTTPS (Telegram WebApp requirement).
func (b *Bot) kitURL() string {
	if b.cfg.KitServerHost == "" {
		log.Debug().Msg("tgauth: kit URL empty — KitServerHost not set")
		return ""
	}
	if !strings.HasPrefix(b.cfg.KitServerHost, "https://") {
		log.Warn().Str("host", b.cfg.KitServerHost).Msg("tgauth: kit URL skipped — not HTTPS")
		return ""
	}
	return b.cfg.KitServerHost + "/kit"
}

// lang resolves the user's language: stored preference > auto-detect from Telegram > default RU.
func (b *Bot) lang(from *tgUser) Lang {
	if from == nil {
		return LangRU
	}
	l, ok := b.langStore.Get(from.ID)
	if ok {
		return l
	}
	// Auto-detect from Telegram language_code on first contact
	detected := LangFromTG(from.LanguageCode)
	b.langStore.Set(from.ID, detected)
	return detected
}

// isMenuButton checks if text matches any localized version of a menu button.
func isMenuButton(text, ruBtn, ukBtn, enBtn string) bool {
	return text == ruBtn || text == ukBtn || text == enBtn
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

func (b *Bot) handleMessage(msg *tgMessage) {
	if msg.From == nil || msg.Chat == nil {
		return
	}

	// Only respond in private chats — ignore groups/supergroups/channels.
	// This prevents keyboard buttons from appearing in group chats.
	if msg.Chat.Type != "" && msg.Chat.Type != "private" {
		return
	}

	// A reply to a tracked relay message (admin↔user thread over an incident/feedback) → relay it the
	// other way and stop (don't treat the reply as a code/command). Handles BOTH directions.
	if b.tryHandleRelayReply(msg) {
		return
	}

	text := strings.TrimSpace(msg.Text)
	lang := b.lang(msg.From)
	t := T(lang)

	// ---- Admin commands ----
	if text == "/admin" {
		if msg.From.ID != b.cfg.AdminID {
			b.sendMsg(msg.Chat.ID, t.AdminOnly, nil)
			return
		}
		if b.adminPath == "" {
			b.sendMsg(msg.Chat.ID, t.AdminPanelNone, nil)
			return
		}
		// Build the admin links. When KitServerHost is set we send full
		// clickable URLs (host + path); otherwise fall back to relative paths.
		// The WebApp button below only fires when host is HTTPS — that's a
		// Telegram requirement, not a Lampac choice.
		var keyboard *tgInlineKeyboardMarkup
		host := strings.TrimRight(strings.TrimSpace(b.cfg.KitServerHost), "/")
		var legacyURL, v2URL string
		if host != "" {
			legacyURL = host + "/" + b.adminPath
			v2URL = host + "/" + b.adminPath + "/v2"
		} else {
			legacyURL = "/" + b.adminPath
			v2URL = "/" + b.adminPath + "/v2"
		}
		body := fmt.Sprintf(t.AdminPanelLink, legacyURL) + "\n\nv2: " + v2URL
		if strings.HasPrefix(host, "https://") {
			keyboard = &tgInlineKeyboardMarkup{
				InlineKeyboard: [][]tgInlineKeyboardButton{
					{{Text: "✨ Открыть админ (WebApp)", WebApp: &tgWebApp{URL: host + "/tg-admin/"}}},
				},
			}
		}
		b.sendMsg(msg.Chat.ID, body, keyboard)
		return
	}

	if strings.HasPrefix(text, "/token") {
		if msg.From.ID != b.cfg.AdminID {
			b.sendMsg(msg.Chat.ID, t.AdminOnly, nil)
			return
		}
		days := 30
		parts := strings.Fields(text)
		if len(parts) >= 2 {
			if d, err := strconv.Atoi(parts[1]); err == nil && d > 0 {
				days = d
			}
		}
		token := uuid.New().String()
		approved := ApprovedToken{
			Token:      token,
			TelegramID: msg.From.ID,
			TGUsername: "device:" + msg.From.Username,
			CreatedAt:  time.Now().UTC(),
			ExpiresAt:  time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour),
			ApprovedBy: b.cfg.AdminID,
		}
		if err := b.store.Add(approved); err != nil {
			b.sendMsg(msg.Chat.ID, t.ErrorGeneric+err.Error(), nil)
			return
		}
		label := DurationLabelLang(lang, days)
		b.sendMsg(msg.Chat.ID, fmt.Sprintf(t.AdminTokenCreated, label, token, token), nil)
		return
	}

	// ---- Language button ----
	if isMenuButton(text, messagesRU.BtnLanguage, messagesUK.BtnLanguage, messagesEN.BtnLanguage) || text == "/lang" {
		b.handleLanguageMenu(msg, lang)
		return
	}

	// ---- Button menus (match all languages) ----
	if isMenuButton(text, messagesRU.BtnProfile, messagesUK.BtnProfile, messagesEN.BtnProfile) || text == "/profile" {
		b.handleProfile(msg)
		return
	}
	if isMenuButton(text, messagesRU.BtnDevices, messagesUK.BtnDevices, messagesEN.BtnDevices) || text == "/devices" {
		b.handleDevices(msg)
		return
	}
	if isMenuButton(text, messagesRU.BtnSettings, messagesUK.BtnSettings, messagesEN.BtnSettings) || text == "/kit" {
		b.sendKitButton(msg)
		return
	}
	if isMenuButton(text, messagesRU.BtnRemote, messagesUK.BtnRemote, messagesEN.BtnRemote) || text == "/remote" {
		b.sendRemoteButton(msg)
		return
	}

	// ---- Feedback ----
	if isMenuButton(text, messagesRU.BtnFeedback, messagesUK.BtnFeedback, messagesEN.BtnFeedback) || text == "/feedback" {
		b.handleFeedbackStart(msg)
		return
	}
	if isMenuButton(text, messagesRU.BtnTickets, messagesUK.BtnTickets, messagesEN.BtnTickets) || text == "/mytickets" {
		b.handleMyTickets(msg)
		return
	}

	// ---- Check if user is in feedback-waiting state ----
	if fs, ok := b.feedbackWait[msg.From.ID]; ok {
		b.handleFeedbackInput(msg, fs)
		return
	}

	// ---- ProxyCore commands ----
	if b.handleProxyCommand(msg) {
		return
	}

	// ---- YouTube auth commands ----
	if text == "/youtube_auth" || text == "/youtube" {
		b.handleYouTubeAuth(msg)
		return
	}
	if text == "/youtube_unbind" {
		b.handleYouTubeUnbind(msg)
		return
	}

	// ---- Calendar commands ----
	if text == "/calendar" {
		b.handleCalendar(msg)
		return
	}
	if strings.HasPrefix(text, "/calendar_notify") {
		b.handleCalendarNotify(msg)
		return
	}

	// ---- /unbind command ----
	if strings.HasPrefix(text, "/unbind") {
		b.handleUnbindCommand(msg, text)
		return
	}

	// ---- /profiles — manage sync profiles (PIN-based devices) ----
	if strings.HasPrefix(text, "/profiles") {
		b.handleProfilesCommand(msg)
		return
	}

	// ---- multi-step /profiles dialog input (username / PIN) ----
	if b.handleProfileWaitInput(msg, text) {
		return
	}

	// ---- /rename command ----
	if strings.HasPrefix(text, "/rename") {
		b.handleRenameCommand(msg, text)
		return
	}

	// ---- Check if user is in rename-waiting state ----
	if rs, ok := b.renameWait[msg.From.ID]; ok {
		delete(b.renameWait, msg.From.ID)
		newLabel := text
		if len(newLabel) > 50 {
			newLabel = newLabel[:50]
		}
		if err := b.store.RenameDevice(rs.Token, rs.UID, newLabel); err != nil {
			b.sendMsg(msg.Chat.ID, t.ErrorGeneric+err.Error(), nil)
			return
		}
		b.sendMsgWithReply(msg.Chat.ID, fmt.Sprintf(t.DeviceRenamed, newLabel), b.mainMenuKeyboard(lang))
		return
	}

	// ---- /start ----
	if strings.HasPrefix(text, "/start") {
		parts := strings.Fields(text)
		if len(parts) == 1 {
			b.sendStartMenu(msg)
			return
		}
		// /start kit → send Kit WebApp button
		if parts[1] == "kit" {
			b.sendKitButton(msg)
			return
		}
		// /start room_<CODE> → watch-party invite: open the room in the SPA
		if code, ok := strings.CutPrefix(parts[1], "room_"); ok {
			b.sendRoomJoinButton(msg, code)
			return
		}
		text = parts[1] // the code
	}

	// ---- Code processing ----
	code := strings.ToUpper(strings.TrimSpace(text))
	if len(code) < 4 || len(code) > 8 {
		b.sendMsgWithReply(msg.Chat.ID, t.CodeWrongFormat, b.mainMenuKeyboard(lang))
		return
	}

	// First: auth pending code
	req, ok := b.pending.ClaimByCode(code, msg.From.ID, msg.Chat.ID, msg.From.Username)
	if ok {
		// Check required channel/group subscriptions before approving
		if !b.checkMembershipOrReject(msg.Chat.ID, msg.From.ID) {
			// Unclaim the code so the user can retry after subscribing
			b.pending.Unclaim(code)
			return
		}

		// Check if user already has a valid token → auto-approve.
		// IMPORTANT: refresh the expiry on re-pair. Without this, a user who
		// uses the bot every few months sees their session "слетает" when the
		// original expiry hits — they'd have to wait for an admin re-approve
		// even though they're still active. Extend by the configured default
		// (or 30 days if not configured) on every successful re-pair.
		if existing := b.store.FindByTelegramID(msg.From.ID); existing != nil {
			extendDays := b.cfg.AutoApproveDays
			if extendDays <= 0 {
				extendDays = 30
			}
			_ = b.store.Extend(existing.Token, extendDays)
			b.pending.Approve(code, existing.Token)
			b.sendMsgWithReply(msg.Chat.ID, t.CodeAccepted, b.mainMenuKeyboard(lang))
			log.Info().Int64("tg_id", msg.From.ID).Str("code", code).Int("extend_days", extendDays).Msg("tgauth: auto-approved (existing user, extended)")
			return
		}
		// New user — check auto-approve setting
		if b.cfg.AutoApprove && b.cfg.AutoApproveDays > 0 {
			days := b.cfg.AutoApproveDays
			token := uuid.New().String()
			approved := ApprovedToken{
				Token:      token,
				TelegramID: req.TelegramID,
				TGUsername: req.TGUsername,
				CreatedAt:  time.Now().UTC(),
				ExpiresAt:  time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour),
				ApprovedBy: b.cfg.AdminID,
			}
			if err := b.store.Add(approved); err != nil {
				log.Error().Err(err).Msg("tgauth: auto-approve failed")
				b.sendMsgWithReply(msg.Chat.ID, t.CodeErrorApprove, b.mainMenuKeyboard(lang))
				return
			}
			b.pending.Approve(code, token)
			label := DurationLabelLang(lang, days)
			b.sendMsgWithReply(msg.Chat.ID, fmt.Sprintf(t.CodeAutoApproved, label), b.mainMenuKeyboard(lang))
			// Notify admin (informational, no action buttons) — always in admin's lang
			username := req.TGUsername
			if username == "" {
				username = fmt.Sprintf("ID:%d", req.TelegramID)
			} else {
				username = "@" + username
			}
			adminLang := b.langByID(b.cfg.AdminID)
			adminT := T(adminLang)
			b.sendMsg(b.cfg.AdminID,
				fmt.Sprintf(adminT.AdminAutoInfo, username, DurationLabelLang(adminLang, days), req.ClientIP),
				nil)
			log.Info().Int64("tg_id", msg.From.ID).Str("code", code).Int("days", days).Msg("tgauth: auto-approved (new user)")
			return
		}
		// Manual approval — send to admin
		b.sendMsgWithReply(msg.Chat.ID, t.CodeWaitAdmin, b.mainMenuKeyboard(lang))
		b.notifyAdmin(req)
		return
	}

	// Second: device pending code
	if b.devicePending != nil {
		dreq, found := b.devicePending.FindByCode(code)
		if found {
			if dreq.TelegramID != msg.From.ID {
				b.sendMsg(msg.Chat.ID, t.CodeOtherAccount, nil)
				return
			}
			b.devicePending.Confirm(code)
			dev := DeviceInfo{
				UID:      dreq.UID,
				Label:    dreq.Label,
				BoundAt:  time.Now().UTC(),
				LastSeen: time.Now().UTC(),
			}
			added, err := b.store.AddDevice(dreq.Token, dev)
			if err != nil {
				log.Error().Err(err).Msg("tgauth: failed to bind device")
				b.sendMsg(msg.Chat.ID, t.CodeBindError, nil)
				return
			}
			_ = added
			b.sendMsgWithReply(msg.Chat.ID, fmt.Sprintf(t.CodeDeviceBound, dreq.Label), b.mainMenuKeyboard(lang))
			return
		}
	}

	b.sendMsgWithReply(msg.Chat.ID, t.CodeNotFound, b.mainMenuKeyboard(lang))
}

func (b *Bot) sendStartMenu(msg *tgMessage) {
	lang := b.lang(msg.From)
	tr := T(lang)
	tok := b.store.FindByTelegramID(msg.From.ID)
	var text string
	if tok != nil {
		text = fmt.Sprintf(tr.StartWelcome, msg.From.FirstName, fmtDate(tok.ExpiresAt), len(tok.Devices))
	} else {
		text = fmt.Sprintf(tr.StartWelcomeNew, msg.From.FirstName)
	}
	b.sendMsgWithReply(msg.Chat.ID, text, b.mainMenuKeyboard(lang))
}

// sendRoomJoinButton delivers a watch-party invite: a button opening the ALPAC
// SPA directly on the room route (#/room/<CODE>). The friend taps it and lands
// in the cinema, syncing to the host. Prefers a Telegram WebApp button (opens
// in-chat); falls back to a plain URL button when the host isn't HTTPS.
func (b *Bot) sendRoomJoinButton(msg *tgMessage, code string) {
	lang := b.lang(msg.From)
	tr := T(lang)
	code = strings.ToUpper(strings.TrimSpace(code))
	// PublicHost (falls back to KitServerHost). A plain https URL button always works;
	// a WebApp button would require the domain to be registered as a Mini App in BotFather
	// and otherwise makes sendMessage fail entirely — so we use a URL button and ALSO print
	// the code so a TV viewer can type it into "Войти по коду" on the big screen.
	host := strings.TrimRight(b.cfg.PublicHost, "/")
	if host == "" {
		host = strings.TrimRight(b.cfg.KitServerHost, "/")
	}
	if host == "" || !strings.HasPrefix(host, "https://") {
		b.sendMsgWithReply(msg.Chat.ID, tr.RoomUnavailable, b.mainMenuKeyboard(lang))
		return
	}
	// host = the FULL SPA base URL (no hardcoded /app prefix): on tv.alcopa.cc the SPA is
	// served at root → https://tv.alcopa.cc/#/room/<code>. If a deployment serves the SPA
	// under a sub-path, the admin includes it in public_host (e.g. https://host/app).
	roomURL := host + "/#/room/" + code
	prompt := tr.RoomPrompt + "\n\n" + fmt.Sprintf(tr.RoomCodeLine, code)
	keyboard := &tgInlineKeyboardMarkup{
		InlineKeyboard: [][]tgInlineKeyboardButton{
			{{Text: tr.RoomOpenBtn, URL: roomURL}},
		},
	}
	b.sendMsg(msg.Chat.ID, prompt, keyboard)
}

func (b *Bot) sendKitButton(msg *tgMessage) {
	lang := b.lang(msg.From)
	tr := T(lang)
	kitURL := b.kitURL()
	if kitURL == "" {
		b.sendMsgWithReply(msg.Chat.ID, tr.KitUnavailable, b.mainMenuKeyboard(lang))
		return
	}
	// Pass user language to Kit WebApp via query param
	kitURLWithLang := kitURL + "?lang=" + string(lang)
	keyboard := &tgInlineKeyboardMarkup{
		InlineKeyboard: [][]tgInlineKeyboardButton{
			{{Text: tr.KitOpenBtn, WebApp: &tgWebApp{URL: kitURLWithLang}}},
		},
	}
	b.sendMsg(msg.Chat.ID, tr.KitPrompt, keyboard)
}

func (b *Bot) remoteURL() string {
	if b.cfg.KitServerHost == "" {
		return ""
	}
	if !strings.HasPrefix(b.cfg.KitServerHost, "https://") {
		return ""
	}
	return b.cfg.KitServerHost + "/remote"
}

func (b *Bot) sendRemoteButton(msg *tgMessage) {
	lang := b.lang(msg.From)
	tr := T(lang)
	remoteURL := b.remoteURL()
	if remoteURL == "" {
		b.sendMsgWithReply(msg.Chat.ID, tr.KitUnavailable, b.mainMenuKeyboard(lang))
		return
	}
	keyboard := &tgInlineKeyboardMarkup{
		InlineKeyboard: [][]tgInlineKeyboardButton{
			{{Text: tr.RemoteOpenBtn, WebApp: &tgWebApp{URL: remoteURL}}},
		},
	}
	b.sendMsg(msg.Chat.ID, tr.RemotePrompt, keyboard)
}

// profileViewModel is the pure-data input for renderProfileMessage —
// gathered in handleProfile and passed in so the text-building logic is
// trivially testable without spinning up a fake Telegram API.
type profileViewModel struct {
	FirstName    string
	Username     string
	TelegramID   int64
	CreatedAt    time.Time
	ExpiresAt    time.Time
	PremiumUntil time.Time // zero = never had premium
	DeviceCount  int
	MaxDevices   int       // <= 0 = "unknown / no limit shown"
	DeviceLabels []string  // labels for the device-icon row
	Integrations []string  // pre-formatted HTML lines, one per integration
	Now          time.Time // injected so tests are deterministic
}

// handleProfile renders the "Профиль" message. The layout is a
// hand-tuned premium-style card built from inline HTML — Telegram does
// not give us proper layout, so we lean on:
//
//   - a single hero line (name + status pills),
//   - section blocks framed with the "▌" sidebar glyph,
//   - <code>-wrapped progress bars (▰▱) so the bar columns line up,
//   - bold dates and counts so the eye lands on the numbers first,
//   - a muted footer with TG ID + signup date in italics.
//
// The base "Стандарт" timer is always rendered. The premium overlay
// block only renders if the user has ever activated premium (so non-
// paying users don't see an empty section).
func (b *Bot) handleProfile(msg *tgMessage) {
	lang := b.lang(msg.From)
	tr := T(lang)
	tok := b.store.FindByTelegramID(msg.From.ID)
	if tok == nil {
		b.sendMsgWithReply(msg.Chat.ID, tr.ProfileNoAccess, b.mainMenuKeyboard(lang))
		return
	}

	now := time.Now().UTC()
	premiumActive := !tok.PremiumUntil.IsZero() && tok.PremiumUntil.After(now)

	// Resolve effective device cap from the user's group (premium overlay
	// wins, then base, then default group).
	maxDev := tok.MaxDevices
	if maxDev <= 0 && b.groupStore != nil {
		gid := tok.GroupID
		if premiumActive {
			gid = "premium"
		}
		if gid == "" {
			gid = b.groupStore.GetDefault().ID
		}
		if g, ok := b.groupStore.Get(gid); ok && g.MaxDevices > 0 {
			maxDev = g.MaxDevices
		}
	}

	// Build the integrations list — order matters (group first, then
	// each external account, then calendar).
	var integrations []string
	if b.groupStore != nil {
		groupID := tok.GroupID
		if premiumActive {
			groupID = "premium"
		}
		if groupID == "" {
			groupID = b.groupStore.GetDefault().ID
		}
		if g, ok := b.groupStore.Get(groupID); ok && g.Name != "" {
			integrations = append(integrations, "🏷   "+escapeHTML(g.Name))
		}
	}
	if b.ytAuth != nil && b.ytAuth.IsLinked(msg.From.ID) {
		title := b.ytAuth.ChannelTitle(msg.From.ID)
		if title == "" {
			title = "—"
		}
		integrations = append(integrations, "🎬   YouTube — <b>"+escapeHTML(title)+"</b>")
	}
	cubLinked := tok.CubUserID != ""
	if cubLinked {
		cubLabel := tok.CubEmail
		if cubLabel == "" {
			cubLabel = "id " + tok.CubUserID
		}
		integrations = append(integrations, "☁️   CUB — <b>"+escapeHTML(cubLabel)+"</b>")
	}
	if b.calendarH != nil {
		cnt := b.calendarH.TrackedCount(msg.From.ID)
		if cnt > 0 {
			notify := tr.ProfileCalendarNotifyOn
			if !b.calendarH.GetNotifyTG(msg.From.ID) {
				notify = tr.ProfileCalendarNotifyOff
			}
			integrations = append(integrations,
				fmt.Sprintf("📅   <b>%d</b> %s  ·  %s", cnt, tr.ProfileCalendarSeriesWord, notify))
		}
	}

	// Device labels for the icon row.
	labels := make([]string, 0, len(tok.Devices))
	for _, d := range tok.Devices {
		labels = append(labels, d.Label)
	}

	vm := profileViewModel{
		FirstName:    strings.TrimSpace(msg.From.FirstName),
		Username:     tok.TGUsername,
		TelegramID:   tok.TelegramID,
		CreatedAt:    tok.CreatedAt,
		ExpiresAt:    tok.ExpiresAt,
		PremiumUntil: tok.PremiumUntil,
		DeviceCount:  len(tok.Devices),
		MaxDevices:   maxDev,
		DeviceLabels: labels,
		Integrations: integrations,
		Now:          now,
	}
	profileKB := &tgInlineKeyboardMarkup{
		InlineKeyboard: [][]tgInlineKeyboardButton{
			{
				{Text: tr.BtnDevices, CallbackData: "profile:devices"},
				{Text: tr.BtnLanguage, CallbackData: "profile:lang"},
			},
		},
	}
	if cubLinked {
		profileKB.InlineKeyboard = append(profileKB.InlineKeyboard, []tgInlineKeyboardButton{
			{Text: tr.BtnCubUnlink, CallbackData: "profile:cubunlink"},
		})
	}
	b.sendMsg(msg.Chat.ID, renderProfileMessage(vm, tr), profileKB)
}

// renderProfileMessage is the pure HTML-builder for the profile card.
// Lives outside handleProfile so unit tests can pin the layout without
// instantiating a fake bot + TG API.
func renderProfileMessage(vm profileViewModel, tr *Messages) string {
	now := vm.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	premiumActive := !vm.PremiumUntil.IsZero() && vm.PremiumUntil.After(now)
	expired := now.After(vm.ExpiresAt)

	var sb strings.Builder

	// ─── Hero block: name + status pills ───────────────────────────
	firstName := vm.FirstName
	if firstName == "" {
		firstName = "—"
	}
	sb.WriteString("✨  <b>")
	sb.WriteString(escapeHTML(firstName))
	sb.WriteString("</b>")
	if vm.Username != "" {
		sb.WriteString("   <i>@")
		sb.WriteString(escapeHTML(vm.Username))
		sb.WriteString("</i>")
	}
	sb.WriteString("\n")
	if expired {
		sb.WriteString("🔴  <b>")
		sb.WriteString(tr.ProfileExpired)
		sb.WriteString("</b>")
	} else {
		sb.WriteString("🟢  <b>")
		sb.WriteString(tr.ProfileActive)
		sb.WriteString("</b>")
	}
	if premiumActive {
		sb.WriteString("   ·   ⭐ <b>")
		sb.WriteString(tr.ProfilePremiumBadge)
		sb.WriteString("</b>  ")
		sb.WriteString(tr.ProfileUntilWord)
		sb.WriteString(" <b>")
		sb.WriteString(fmtDateShort(vm.PremiumUntil))
		sb.WriteString("</b>")
	}
	sb.WriteString("\n\n")

	// ─── Standard timer (hidden while premium is active) ───────────
	// When the user has active premium, the standard timer is redundant
	// noise — hide it and show only the premium block below.
	if !premiumActive {
		basePct := remainingPercent(now, vm.CreatedAt, vm.ExpiresAt)
		sb.WriteString("▌  📅  <b>")
		sb.WriteString(tr.ProfileBaseTitle)
		sb.WriteString("</b>\n")
		sb.WriteString("▌  <code>")
		sb.WriteString(renderBar(basePct, 12))
		sb.WriteString("</code>  <b>")
		sb.WriteString(strconv.Itoa(basePct))
		sb.WriteString("%</b>\n▌  ")
		if expired {
			sb.WriteString("<s>")
			sb.WriteString(tr.ProfileExpiredSince)
			sb.WriteString(" ")
			sb.WriteString(fmtDate(vm.ExpiresAt))
			sb.WriteString("</s>")
		} else {
			sb.WriteString(tr.ProfileUntilWord)
			sb.WriteString(" <b>")
			sb.WriteString(fmtDate(vm.ExpiresAt))
			sb.WriteString("</b>")
			days := ceilDays(vm.ExpiresAt.Sub(now))
			if days > 0 {
				sb.WriteString("  ·  ")
				sb.WriteString(tr.ProfileRemainingPrefix)
				sb.WriteString(" <b>")
				sb.WriteString(strconv.Itoa(days))
				sb.WriteString("</b> ")
				sb.WriteString(pluralDays(days, tr.ProfileDayForms))
			}
		}
		sb.WriteString("\n\n")
	}

	// ─── Premium overlay (only if the user has ever had premium) ───
	if !vm.PremiumUntil.IsZero() {
		sb.WriteString("▌  ⭐  <b>")
		sb.WriteString(tr.ProfilePremiumTitle)
		sb.WriteString("</b>\n")
		if premiumActive {
			// Bar is scaled to a 365-day max so a freshly-bought yearly
			// subscription fills the bar; common 30-day buy = ~8%.
			days := ceilDays(vm.PremiumUntil.Sub(now))
			pct := days * 100 / 365
			if pct > 100 {
				pct = 100
			}
			sb.WriteString("▌  <code>")
			sb.WriteString(renderBar(pct, 12))
			sb.WriteString("</code>  <b>")
			sb.WriteString(strconv.Itoa(pct))
			sb.WriteString("%</b>\n▌  ")
			sb.WriteString(tr.ProfileUntilWord)
			sb.WriteString(" <b>")
			sb.WriteString(fmtDate(vm.PremiumUntil))
			sb.WriteString("</b>  ·  ")
			sb.WriteString(tr.ProfileRemainingPrefix)
			sb.WriteString(" <b>")
			sb.WriteString(strconv.Itoa(days))
			sb.WriteString("</b> ")
			sb.WriteString(pluralDays(days, tr.ProfileDayForms))
		} else {
			sb.WriteString("▌  <code>")
			sb.WriteString(renderBar(0, 12))
			sb.WriteString("</code>  <b>0%</b>\n▌  <s>")
			sb.WriteString(tr.ProfileExpiredSince)
			sb.WriteString(" ")
			sb.WriteString(fmtDate(vm.PremiumUntil))
			sb.WriteString("</s>")
		}
		sb.WriteString("\n\n")
	}

	// ─── Devices ───────────────────────────────────────────────────
	sb.WriteString("📱  <b>")
	sb.WriteString(tr.ProfileDevicesTitle)
	sb.WriteString("</b>   <b>")
	sb.WriteString(strconv.Itoa(vm.DeviceCount))
	sb.WriteString("</b>")
	if vm.MaxDevices > 0 {
		sb.WriteString(" / ")
		sb.WriteString(strconv.Itoa(vm.MaxDevices))
	}
	sb.WriteString("\n")
	// Icon row — visualises slots like a phone-row pictogram.
	if vm.MaxDevices > 0 && vm.MaxDevices <= 10 {
		sb.WriteString("       ")
		for i := 0; i < vm.DeviceCount && i < vm.MaxDevices; i++ {
			label := ""
			if i < len(vm.DeviceLabels) {
				label = vm.DeviceLabels[i]
			}
			sb.WriteString(deviceIcon(label))
			sb.WriteString(" ")
		}
		for i := vm.DeviceCount; i < vm.MaxDevices; i++ {
			sb.WriteString("◌ ")
		}
		sb.WriteString("\n")
	} else if vm.DeviceCount > 0 {
		sb.WriteString("       ")
		shown := vm.DeviceCount
		if shown > 8 {
			shown = 8
		}
		for i := 0; i < shown; i++ {
			label := ""
			if i < len(vm.DeviceLabels) {
				label = vm.DeviceLabels[i]
			}
			sb.WriteString(deviceIcon(label))
			sb.WriteString(" ")
		}
		if vm.DeviceCount > shown {
			sb.WriteString(fmt.Sprintf("<i>+%d</i>", vm.DeviceCount-shown))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	// ─── Integrations ──────────────────────────────────────────────
	if len(vm.Integrations) > 0 {
		sb.WriteString("🔗  <b>")
		sb.WriteString(tr.ProfileIntegrationsTitle)
		sb.WriteString("</b>\n")
		for _, item := range vm.Integrations {
			sb.WriteString("▌   ")
			sb.WriteString(item)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// ─── Muted footer ──────────────────────────────────────────────
	sb.WriteString("<i>TG: <code>")
	sb.WriteString(strconv.FormatInt(vm.TelegramID, 10))
	sb.WriteString("</code>   ·   ")
	sb.WriteString(tr.ProfileSinceWord)
	sb.WriteString(" ")
	sb.WriteString(fmtDate(vm.CreatedAt))
	sb.WriteString("</i>")

	return sb.String()
}

// renderBar draws a fixed-width progress bar using ▰ (filled) and ▱
// (empty). percent is clamped to [0,100]; width is the total cell count.
// Width 12 looks balanced in Telegram's monospace block.
func renderBar(percent, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	// Round to nearest integer cell (+50 trick).
	filled := (percent*width + 50) / 100
	if filled > width {
		filled = width
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", width-filled)
}

// remainingPercent returns the share of [start, end] still in the
// future, as an integer percent. Returns 0 if end <= start or end < now.
func remainingPercent(now, start, end time.Time) int {
	total := end.Sub(start)
	if total <= 0 {
		return 0
	}
	remaining := end.Sub(now)
	if remaining <= 0 {
		return 0
	}
	pct := int(remaining * 100 / total)
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	return pct
}

// ceilDays converts a duration to a "ceiling" day count so that anything
// up to 24 h shows as "1 day left" — matches the user's intuition for
// "ещё N дней" labels.
func ceilDays(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	full := int(d / (24 * time.Hour))
	if d%(24*time.Hour) > 0 {
		full++
	}
	return full
}

// pluralDays picks the correct day word for the locale.
// Ru/Uk forms: [0]=1 day, [1]=2-4 days, [2]=5+ days (and 11-14 → [2]).
// English: forms[0]="day" for n=1, forms[1]="days" for n>1.
func pluralDays(n int, forms [3]string) string {
	if n < 0 {
		n = -n
	}
	mod100 := n % 100
	if mod100 >= 11 && mod100 <= 14 {
		return forms[2]
	}
	switch n % 10 {
	case 1:
		return forms[0]
	case 2, 3, 4:
		return forms[1]
	default:
		return forms[2]
	}
}

// deviceIcon picks a single emoji that gives the user a glance at what
// kind of device this slot is. The Label is set by the auth code path
// (see kit/lampac_index_api) and is usually short like "Android Phone".
func deviceIcon(label string) string {
	l := strings.ToLower(label)
	switch {
	case strings.Contains(l, "android"):
		return "🤖"
	case strings.Contains(l, "iphone"), strings.Contains(l, "ipad"), strings.Contains(l, "ios"):
		return "🍎"
	case strings.Contains(l, "smart"), strings.Contains(l, " tv"), strings.Contains(l, "roku"),
		strings.Contains(l, "lg "), strings.Contains(l, "samsung"), strings.Contains(l, "appletv"):
		return "📺"
	case strings.Contains(l, "web"), strings.Contains(l, "browser"), strings.Contains(l, "chrome"),
		strings.Contains(l, "firefox"), strings.Contains(l, "safari"), strings.Contains(l, "edge"):
		return "💻"
	default:
		return "📱"
	}
}

func (b *Bot) handleDevices(msg *tgMessage) {
	lang := b.lang(msg.From)
	tr := T(lang)
	tok := b.store.FindByTelegramID(msg.From.ID)
	if tok == nil {
		b.sendMsgWithReply(msg.Chat.ID, tr.ProfileNoAccess, b.mainMenuKeyboard(lang))
		return
	}
	if len(tok.Devices) == 0 {
		b.sendMsgWithReply(msg.Chat.ID, tr.DevicesNone, b.mainMenuKeyboard(lang))
		return
	}

	text, keyboard := devicesView(tok, tr)
	b.sendMsg(msg.Chat.ID, text, keyboard)
}

// devicesView renders the device list and its buttons. One function, because the
// list is shown from three places (the command, the profile button, and after an
// unbind) and the three copies had already drifted apart.
func devicesView(tok *ApprovedToken, tr *Messages) (string, *tgInlineKeyboardMarkup) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(tr.DevicesTitle, len(tok.Devices)))

	var rows [][]tgInlineKeyboardButton
	for i, d := range tok.Devices {
		lastSeen := "—"
		if !d.LastSeen.IsZero() {
			lastSeen = fmtDateTime(d.LastSeen)
		}
		sb.WriteString(fmt.Sprintf("%d. <b>%s</b>\n   <code>%s</code> · %s\n\n",
			i+1, d.Label, d.UID, lastSeen))

		rows = append(rows, []tgInlineKeyboardButton{
			{Text: fmt.Sprintf("✏️ %s", d.Label), CallbackData: "rename:" + tok.Token + ":" + d.UID},
			{Text: tr.DeviceUnbindBtn, CallbackData: "unbind:" + tok.Token + ":" + d.UID},
		})
	}
	if len(tok.Devices) > 1 {
		// Only with something to sweep: on a single device this is just a more
		// dangerous version of the button right above it.
		rows = append(rows, []tgInlineKeyboardButton{
			{Text: tr.DeviceUnbindAllBtn, CallbackData: "unbindall:" + tok.Token},
		})
	}
	return sb.String(), &tgInlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) handleUnbindCommand(msg *tgMessage, text string) {
	lang := b.lang(msg.From)
	tr := T(lang)
	parts := strings.Fields(text)
	if len(parts) < 2 {
		b.sendMsg(msg.Chat.ID, tr.DeviceUsage, nil)
		return
	}
	uid := parts[1]
	tok := b.store.FindByTelegramID(msg.From.ID)
	if tok == nil {
		b.sendMsg(msg.Chat.ID, tr.ProfileNoAccess, nil)
		return
	}
	found := false
	for _, d := range tok.Devices {
		if d.UID == uid {
			found = true
			break
		}
	}
	if !found {
		b.sendMsg(msg.Chat.ID, tr.DeviceNotFound, nil)
		return
	}
	if err := b.store.RemoveDevice(tok.Token, uid); err != nil {
		b.sendMsg(msg.Chat.ID, tr.ErrorGeneric+err.Error(), nil)
		return
	}
	b.sendMsgWithReply(msg.Chat.ID, tr.DeviceUnbound, b.mainMenuKeyboard(lang))
}

func (b *Bot) handleRenameCommand(msg *tgMessage, text string) {
	lang := b.lang(msg.From)
	tr := T(lang)
	parts := strings.Fields(text)
	if len(parts) < 2 {
		b.sendMsg(msg.Chat.ID, tr.DeviceUsage, nil)
		return
	}
	uid := parts[1]
	tok := b.store.FindByTelegramID(msg.From.ID)
	if tok == nil {
		b.sendMsg(msg.Chat.ID, tr.ProfileNoAccess, nil)
		return
	}
	found := false
	for _, d := range tok.Devices {
		if d.UID == uid {
			found = true
			break
		}
	}
	if !found {
		b.sendMsg(msg.Chat.ID, tr.DeviceNotFound, nil)
		return
	}
	if len(parts) >= 3 {
		newLabel := strings.Join(parts[2:], " ")
		if len(newLabel) > 50 {
			newLabel = newLabel[:50]
		}
		if err := b.store.RenameDevice(tok.Token, uid, newLabel); err != nil {
			b.sendMsg(msg.Chat.ID, tr.ErrorGeneric+err.Error(), nil)
			return
		}
		b.sendMsgWithReply(msg.Chat.ID, fmt.Sprintf(tr.DeviceRenamed, newLabel), b.mainMenuKeyboard(lang))
		return
	}
	// No name provided — wait for input
	b.renameWait[msg.From.ID] = &renameState{Token: tok.Token, UID: uid}
	b.sendMsg(msg.Chat.ID, fmt.Sprintf(tr.DeviceRenameAsk, uid, uid), nil)
}

func (b *Bot) handleCallbackQuery(cb *tgCallbackQuery) {
	if cb.From == nil {
		return
	}

	data := cb.Data
	lang := b.lang(cb.From)
	tr := T(lang)

	// ---- /profiles inline buttons ----
	// All callback data with this prefix is owned by bot_profiles.go;
	// other dispatch branches must never see it.
	if strings.HasPrefix(data, profilesCBPrefix) {
		if b.handleProfilesCallback(cb, data) {
			return
		}
	}

	// ---- Profile inline buttons: Devices / Language (moved off the reply keyboard) ----
	if data == "profile:devices" || data == "profile:lang" {
		b.answerCallback(cb.ID, "")
		if cb.Message != nil {
			m := &tgMessage{From: cb.From, Chat: cb.Message.Chat}
			if data == "profile:lang" {
				b.handleLanguageMenu(m, lang)
			} else {
				b.handleDevices(m)
			}
		}
		return
	}

	// ---- CUB anchor unlink (button only appears when linked) ----
	if data == "profile:cubunlink" {
		cleared := false
		if tok := b.store.FindByTelegramID(cb.From.ID); tok != nil {
			cleared = b.store.ClearCubUser(tok.Token)
		}
		if cleared {
			b.answerCallback(cb.ID, "✅ "+tr.CubUnlinkedToast)
			if cb.Message != nil {
				// Re-render the profile card so the CUB line and button disappear.
				b.handleProfile(&tgMessage{From: cb.From, Chat: cb.Message.Chat})
			}
		} else {
			b.answerCallback(cb.ID, tr.CubUnlinkedToast)
		}
		return
	}

	// ---- Sisi parental control confirm ----
	if after, ok := strings.CutPrefix(data, "sisi_allow:"); ok {
		if b.sisiConfirmCallback != nil && b.sisiConfirmCallback(after, true) {
			b.answerCallback(cb.ID, "✅ Доступ разрешён")
			if cb.Message != nil {
				b.editMsg(cb.Message.Chat.ID, cb.Message.MessageID, "🔐 Родительский контроль\n\n✅ <b>Доступ разрешён</b>")
			}
		} else {
			b.answerCallback(cb.ID, "Запрос истёк")
		}
		return
	}
	if after, ok := strings.CutPrefix(data, "sisi_deny:"); ok {
		if b.sisiConfirmCallback != nil && b.sisiConfirmCallback(after, false) {
			b.answerCallback(cb.ID, "❌ Доступ запрещён")
			if cb.Message != nil {
				b.editMsg(cb.Message.Chat.ID, cb.Message.MessageID, "🔐 Родительский контроль\n\n❌ <b>Доступ запрещён</b>")
			}
		} else {
			b.answerCallback(cb.ID, "Запрос истёк")
		}
		return
	}

	// ---- Language selection ----
	if after, ok := strings.CutPrefix(data, "lang:"); ok {
		newLang := Lang(after)
		if !ValidLang(newLang) {
			b.answerCallback(cb.ID, "")
			return
		}
		b.langStore.Set(cb.From.ID, newLang)
		lang = newLang
		tr = T(lang)
		b.answerCallback(cb.ID, LangFlagName(newLang))
		b.sendMsgWithReply(cb.From.ID, fmt.Sprintf(tr.LangChanged, LangFlagName(newLang)), b.mainMenuKeyboard(lang))
		return
	}

	// ---- User: rename device ----
	if strings.HasPrefix(data, "rename:") {
		parts := strings.SplitN(data, ":", 3)
		if len(parts) != 3 {
			b.answerCallback(cb.ID, tr.ErrorData)
			return
		}
		token, uid := parts[1], parts[2]
		tok := b.store.FindByTelegramID(cb.From.ID)
		if tok == nil || tok.Token != token {
			b.answerCallback(cb.ID, tr.NoAccess)
			return
		}
		b.renameWait[cb.From.ID] = &renameState{Token: token, UID: uid}
		b.answerCallback(cb.ID, tr.EnterNewName)
		currentLabel := uid
		for _, d := range tok.Devices {
			if d.UID == uid {
				currentLabel = d.Label
				break
			}
		}
		b.sendMsg(cb.From.ID, fmt.Sprintf(tr.DeviceRenameAsk, currentLabel, uid), nil)
		return
	}

	// ---- User: unbind device ----
	if strings.HasPrefix(data, "unbind:") {
		parts := strings.SplitN(data, ":", 3)
		if len(parts) != 3 {
			b.answerCallback(cb.ID, tr.ErrorData)
			return
		}
		token, uid := parts[1], parts[2]
		tok := b.store.FindByTelegramID(cb.From.ID)
		if tok == nil || tok.Token != token {
			b.answerCallback(cb.ID, tr.NoAccess)
			return
		}
		if err := b.store.RemoveDevice(token, uid); err != nil {
			b.answerCallback(cb.ID, tr.ErrorGeneric)
			return
		}
		b.answerCallback(cb.ID, tr.DeviceUnbound)
		if cb.Message != nil {
			tok = b.store.FindByTelegramID(cb.From.ID)
			if tok != nil && len(tok.Devices) > 0 {
				text, kb := devicesView(tok, tr)
				b.editMsgWithKeyboard(cb.Message.Chat.ID, cb.Message.MessageID, text, kb)
			} else {
				b.editMsg(cb.Message.Chat.ID, cb.Message.MessageID, tr.DevicesNone)
			}
		}
		return
	}

	// ---- User: unbind ALL devices (ask, then do) ----
	if after, ok := strings.CutPrefix(data, "unbindall:"); ok {
		token := after
		tok := b.store.FindByTelegramID(cb.From.ID)
		if tok == nil || tok.Token != token {
			b.answerCallback(cb.ID, tr.NoAccess)
			return
		}
		b.answerCallback(cb.ID, "")
		if cb.Message != nil {
			// Confirm first: this logs every TV, phone and box out at once, and the
			// re-pairing code can only be shown by a device that still works.
			kb := &tgInlineKeyboardMarkup{InlineKeyboard: [][]tgInlineKeyboardButton{{
				{Text: tr.DeviceUnbindAllYes, CallbackData: "unbindallyes:" + token},
				{Text: tr.CatCancel, CallbackData: "unbindallno:" + token},
			}}}
			b.editMsgWithKeyboard(cb.Message.Chat.ID, cb.Message.MessageID,
				fmt.Sprintf(tr.DeviceUnbindAllAsk, len(tok.Devices)), kb)
		}
		return
	}
	if after, ok := strings.CutPrefix(data, "unbindallyes:"); ok {
		token := after
		tok := b.store.FindByTelegramID(cb.From.ID)
		if tok == nil || tok.Token != token {
			b.answerCallback(cb.ID, tr.NoAccess)
			return
		}
		n := b.store.RemoveAllDevices(token)
		b.answerCallback(cb.ID, fmt.Sprintf(tr.DeviceUnbindAllDone, n))
		if cb.Message != nil {
			b.editMsg(cb.Message.Chat.ID, cb.Message.MessageID, fmt.Sprintf(tr.DeviceUnbindAllDone, n)+"\n\n"+tr.DevicesNone)
		}
		return
	}
	if after, ok := strings.CutPrefix(data, "unbindallno:"); ok {
		tok := b.store.FindByTelegramID(cb.From.ID)
		if tok == nil || tok.Token != after {
			b.answerCallback(cb.ID, tr.NoAccess)
			return
		}
		b.answerCallback(cb.ID, "")
		if cb.Message != nil {
			text, kb := devicesView(tok, tr)
			b.editMsgWithKeyboard(cb.Message.Chat.ID, cb.Message.MessageID, text, kb)
		}
		return
	}

	// ---- Feedback section: hub / new ticket / my tickets ----
	if data == "fbhub" || data == "fbnew" || data == "fbmine" {
		b.answerCallback(cb.ID, "")
		if b.feedbackSub == nil || cb.Message == nil {
			return
		}
		switch data {
		case "fbhub":
			delete(b.feedbackWait, cb.From.ID) // leaving a half-written ticket behind
			text, kb := b.feedbackHub(cb.From.ID, lang)
			b.editMsgWithKeyboard(cb.Message.Chat.ID, cb.Message.MessageID, text, kb)
		case "fbnew":
			b.editMsgWithKeyboard(cb.Message.Chat.ID, cb.Message.MessageID, tr.FeedbackTitle, b.feedbackCategoryKeyboard(lang))
		case "fbmine":
			text, kb := b.ticketsView(cb.From.ID, lang)
			if kb == nil {
				// No tickets: the hub is the honest place to be, not an empty list.
				text, kb = b.feedbackHub(cb.From.ID, lang)
			}
			b.editMsgWithKeyboard(cb.Message.Chat.ID, cb.Message.MessageID, text, kb)
		}
		return
	}

	// ---- Feedback: category selection ----
	if after, ok := strings.CutPrefix(data, "fbcat:"); ok {
		cat := after
		fs := &feedbackState{Category: cat}
		b.feedbackWait[cb.From.ID] = fs
		b.answerCallback(cb.ID, FbCatLabel(lang, cat))
		// ★«Спасибо» не допрашиваем: благодарности не нужен ни клиент, ни платформа,
		// а три лишних вопроса превращают доброе слово в бюрократию.
		if cat == "thanks" {
			fs.Step = 1
			b.sendMsg(cb.From.ID, tr.FeedbackStep2, nil)
			return
		}
		fs.Step = 3 // ждём выбор «где именно»
		b.sendMsg(cb.From.ID, tr.FeedbackWhere, b.feedbackClientKeyboard(lang))
		return
	}

	// ---- Feedback: клиент («где именно») ----
	if after, ok := strings.CutPrefix(data, "fbcl:"); ok {
		fs := b.feedbackWait[cb.From.ID]
		if fs == nil {
			b.answerCallback(cb.ID, tr.Cancelled)
			return
		}
		fs.Client = after
		b.answerCallback(cb.ID, FbClientLabel(lang, after))
		// Платформа осмысленна только для нашего приложения: плагин живёт внутри Lampa,
		// а MSX — сам себе платформа, и спрашивать там нечего.
		if after != "app" {
			fs.Step = 1
			b.sendMsg(cb.From.ID, tr.FeedbackStep2, nil)
			return
		}
		fs.Step = 4
		b.sendMsg(cb.From.ID, tr.FeedbackPlatform, b.feedbackPlatformKeyboard(lang))
		return
	}

	// ---- Feedback: платформа ----
	if after, ok := strings.CutPrefix(data, "fbpl:"); ok {
		fs := b.feedbackWait[cb.From.ID]
		if fs == nil {
			b.answerCallback(cb.ID, tr.Cancelled)
			return
		}
		fs.Platform = after
		fs.Step = 1
		b.answerCallback(cb.ID, FbPlatformLabel(lang, after))
		b.sendMsg(cb.From.ID, tr.FeedbackStep2, nil)
		return
	}

	// ---- Feedback: cancel ----
	if data == "fbcancel" {
		delete(b.feedbackWait, cb.From.ID)
		b.answerCallback(cb.ID, tr.Cancelled)
		if cb.Message != nil {
			// Back to the section, not to a dead "отменено" message: cancelling one
			// ticket is not the same as leaving support.
			if b.feedbackSub != nil {
				text, kb := b.feedbackHub(cb.From.ID, lang)
				b.editMsgWithKeyboard(cb.Message.Chat.ID, cb.Message.MessageID, text, kb)
			} else {
				b.editMsg(cb.Message.Chat.ID, cb.Message.MessageID, tr.FeedbackCancelled)
			}
		}
		return
	}

	// ---- Feedback: view ticket ----
	if after, ok := strings.CutPrefix(data, "fbview:"); ok {
		ticketID := after
		b.answerCallback(cb.ID, "")
		b.handleViewTicket(cb.From.ID, ticketID)
		return
	}

	// ---- Feedback: start reply to ticket ----
	if after, ok := strings.CutPrefix(data, "fbreply:"); ok {
		ticketID := after
		b.feedbackWait[cb.From.ID] = &feedbackState{Step: 10, TicketID: ticketID}
		b.answerCallback(cb.ID, "")
		b.sendMsg(cb.From.ID, tr.FeedbackReplyAsk, nil)
		return
	}

	// ---- Feedback: back to ticket list ----
	if data == "fblist" {
		b.answerCallback(cb.ID, "")
		if b.feedbackSub == nil {
			return
		}
		text, kb := b.ticketsView(cb.From.ID, lang)
		if kb == nil {
			text, kb = b.feedbackHub(cb.From.ID, lang)
		}
		// Edit in place: pushing a fresh copy of the list on every «назад» buried
		// the ticket the person was reading under three identical messages.
		if cb.Message != nil {
			b.editMsgWithKeyboard(cb.Message.Chat.ID, cb.Message.MessageID, text, kb)
		} else {
			b.sendMsg(cb.From.ID, text, kb)
		}
		return
	}

	// ---- Admin only below ----
	if cb.From.ID != b.cfg.AdminID {
		b.answerCallback(cb.ID, tr.AdminOnly)
		return
	}

	// reject:<code>
	if after, ok := strings.CutPrefix(data, "reject:"); ok {
		code := after
		req, ok := b.pending.FindByCode(code)
		if !ok {
			b.answerCallback(cb.ID, tr.CodeNotFound)
			return
		}
		b.pending.Reject(code)
		b.answerCallback(cb.ID, tr.ApproveReject)

		// Notify user in their language
		if req.TGChatID != 0 {
			userLang := b.langByID(req.TelegramID)
			b.sendMsg(req.TGChatID, T(userLang).AccessRejected, nil)
		}

		if cb.Message != nil {
			username := req.TGUsername
			if username == "" {
				username = fmt.Sprintf("ID:%d", req.TelegramID)
			} else {
				username = "@" + username
			}
			b.editMsg(cb.Message.Chat.ID, cb.Message.MessageID,
				fmt.Sprintf(tr.AdminRejected, username))
		}
		return
	}

	// approve:<code>:<days>
	if strings.HasPrefix(data, "approve:") {
		parts := strings.SplitN(data, ":", 3)
		if len(parts) != 3 {
			b.answerCallback(cb.ID, tr.ErrorData)
			return
		}
		code := parts[1]
		days, err := strconv.Atoi(parts[2])
		if err != nil || days <= 0 {
			b.answerCallback(cb.ID, tr.ErrorData)
			return
		}

		req, ok := b.pending.FindByCode(code)
		if !ok {
			b.answerCallback(cb.ID, tr.CodeNotFound)
			return
		}

		var token string
		if existing := b.store.FindByTelegramID(req.TelegramID); existing != nil {
			token = existing.Token
			_ = b.store.Extend(token, days)
		} else {
			token = uuid.New().String()
			approved := ApprovedToken{
				Token:      token,
				TelegramID: req.TelegramID,
				TGUsername: req.TGUsername,
				CreatedAt:  time.Now().UTC(),
				ExpiresAt:  time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour),
				ApprovedBy: b.cfg.AdminID,
			}
			if err := b.store.Add(approved); err != nil {
				log.Error().Err(err).Msg("tgauth: failed to save approved token")
				b.answerCallback(cb.ID, tr.ErrorGeneric)
				return
			}
		}

		b.pending.Approve(code, token)

		label := DurationLabelLang(lang, days)
		b.answerCallback(cb.ID, "✅ "+label)

		// Notify user in their language
		if req.TGChatID != 0 {
			userLang := b.langByID(req.TelegramID)
			userLabel := DurationLabelLang(userLang, days)
			b.sendMsg(req.TGChatID, fmt.Sprintf(T(userLang).AccessApproved, userLabel), nil)
		}

		if cb.Message != nil {
			username := req.TGUsername
			if username == "" {
				username = fmt.Sprintf("ID:%d", req.TelegramID)
			} else {
				username = "@" + username
			}
			b.editMsg(cb.Message.Chat.ID, cb.Message.MessageID,
				fmt.Sprintf(tr.AdminApproved, username, label))
		}
		return
	}
}

func (b *Bot) handleLanguageMenu(msg *tgMessage, _ Lang) {
	lang := b.lang(msg.From)
	tr := T(lang)
	keyboard := &tgInlineKeyboardMarkup{
		InlineKeyboard: [][]tgInlineKeyboardButton{
			{
				{Text: "🇷🇺 Русский", CallbackData: "lang:ru"},
				{Text: "🇺🇦 Українська", CallbackData: "lang:uk"},
				{Text: "🇬🇧 English", CallbackData: "lang:en"},
			},
		},
	}
	b.sendMsg(msg.Chat.ID, tr.LangSelectTitle, keyboard)
}

func (b *Bot) notifyAdmin(req *PendingRequest) {
	adminLang := b.langByID(b.cfg.AdminID)
	tr := T(adminLang)

	username := req.TGUsername
	if username == "" {
		username = fmt.Sprintf("ID:%d", req.TelegramID)
	} else {
		username = "@" + username
	}

	text := fmt.Sprintf(tr.AdminRequest, username, req.TelegramID, req.ClientIP, req.Code)

	code := req.Code
	keyboard := &tgInlineKeyboardMarkup{
		InlineKeyboard: [][]tgInlineKeyboardButton{
			{
				{Text: tr.Approve1d, CallbackData: "approve:" + code + ":1"},
				{Text: tr.Approve7d, CallbackData: "approve:" + code + ":7"},
				{Text: tr.Approve30d, CallbackData: "approve:" + code + ":30"},
			},
			{
				{Text: tr.Approve90d, CallbackData: "approve:" + code + ":90"},
				{Text: tr.Approve365d, CallbackData: "approve:" + code + ":365"},
				{Text: tr.ApproveReject, CallbackData: "reject:" + code},
			},
		},
	}

	b.sendMsg(b.cfg.AdminID, text, keyboard)
}

// ---------- Feedback handlers ----------

// handleFeedbackStart opens the support SECTION rather than the new-ticket
// wizard. The button used to drop straight into «Шаг 1/3», so the tickets a
// person had already written — and the answers to them — had no entrance at
// all: the list existed only behind the /mytickets command.
func (b *Bot) handleFeedbackStart(msg *tgMessage) {
	lang := b.lang(msg.From)
	tr := T(lang)
	if b.feedbackSub == nil {
		b.sendMsgWithReply(msg.Chat.ID, tr.FeedbackUnavailable, b.mainMenuKeyboard(lang))
		return
	}

	delete(b.feedbackWait, msg.From.ID)
	text, kb := b.feedbackHub(msg.From.ID, lang)
	b.sendMsg(msg.Chat.ID, text, kb)
}

// feedbackHub renders the section: what you have, and the two things you can do.
func (b *Bot) feedbackHub(tgID int64, lang Lang) (string, *tgInlineKeyboardMarkup) {
	tr := T(lang)
	tickets := b.feedbackSub.UserTickets(fmt.Sprintf("%d", tgID))

	rows := [][]tgInlineKeyboardButton{
		{{Text: tr.BtnFbNew, CallbackData: "fbnew"}},
	}
	if len(tickets) == 0 {
		return tr.FeedbackHubEmpty, &tgInlineKeyboardMarkup{InlineKeyboard: rows}
	}

	// Answered threads are the reason to come back here, so the headline counts
	// THOSE — a total reply count includes the user's own messages and reads as
	// "3 unread" when nothing is waiting.
	answered := 0
	for _, t := range tickets {
		if FbUserStatusRank(t.Status, t.LastReplyAdmin) == 0 {
			answered++
		}
	}
	extra := ""
	if answered > 0 {
		extra = fmt.Sprintf(" · 💬 %d", answered)
	}
	rows = append(rows, []tgInlineKeyboardButton{
		{Text: fmt.Sprintf("%s (%d)", tr.BtnFbMine, len(tickets)), CallbackData: "fbmine"},
	})
	return fmt.Sprintf(tr.FeedbackHubTitle, len(tickets), extra), &tgInlineKeyboardMarkup{InlineKeyboard: rows}
}

// feedbackCategoryKeyboard is step 1 of the new-ticket wizard.
func (b *Bot) feedbackCategoryKeyboard(lang Lang) *tgInlineKeyboardMarkup {
	tr := T(lang)
	return &tgInlineKeyboardMarkup{
		InlineKeyboard: [][]tgInlineKeyboardButton{
			{{Text: tr.CatHelp, CallbackData: "fbcat:help"}},
			{{Text: tr.CatBug, CallbackData: "fbcat:bug"}},
			{{Text: tr.CatFeature, CallbackData: "fbcat:feature"}},
			{{Text: tr.CatThanks, CallbackData: "fbcat:thanks"}},
			{{Text: tr.CatOther, CallbackData: "fbcat:other"}},
			{{Text: tr.BtnFbBack, CallbackData: "fbhub"}},
		},
	}
}

func (b *Bot) feedbackClientKeyboard(lang Lang) *tgInlineKeyboardMarkup {
	tr := T(lang)
	return &tgInlineKeyboardMarkup{InlineKeyboard: [][]tgInlineKeyboardButton{
		{{Text: tr.CliApp, CallbackData: "fbcl:app"}},
		{{Text: tr.CliLampa, CallbackData: "fbcl:lampa"}},
		{{Text: tr.CliMsx, CallbackData: "fbcl:msx"}},
		{{Text: tr.CliOther, CallbackData: "fbcl:other"}},
		{{Text: tr.CatCancel, CallbackData: "fbcancel"}},
	}}
}

func (b *Bot) feedbackPlatformKeyboard(lang Lang) *tgInlineKeyboardMarkup {
	tr := T(lang)
	// По две в ряд: восемь кнопок в столбик не влезают на экран телефона без прокрутки.
	return &tgInlineKeyboardMarkup{InlineKeyboard: [][]tgInlineKeyboardButton{
		{{Text: tr.PlatAndroidTv, CallbackData: "fbpl:androidtv"}, {Text: tr.PlatPhone, CallbackData: "fbpl:phone"}},
		{{Text: tr.PlatSamsung, CallbackData: "fbpl:samsung"}, {Text: tr.PlatLg, CallbackData: "fbpl:lg"}},
		{{Text: tr.PlatHisense, CallbackData: "fbpl:hisense"}, {Text: tr.PlatAppleTv, CallbackData: "fbpl:appletv"}},
		{{Text: tr.PlatBrowser, CallbackData: "fbpl:browser"}, {Text: tr.PlatOther, CallbackData: "fbpl:other"}},
		{{Text: tr.CatCancel, CallbackData: "fbcancel"}},
	}}
}

// FbClientLabel — человекочитаемое «где именно» для тикета и уведомления админу.
func FbClientLabel(lang Lang, id string) string {
	tr := T(lang)
	switch id {
	case "app":
		return tr.CliApp
	case "lampa":
		return tr.CliLampa
	case "msx":
		return tr.CliMsx
	default:
		return tr.CliOther
	}
}

// FbPlatformLabel — то же для платформы. Пустая строка, если не спрашивали.
func FbPlatformLabel(lang Lang, id string) string {
	tr := T(lang)
	switch id {
	case "androidtv":
		return tr.PlatAndroidTv
	case "phone":
		return tr.PlatPhone
	case "samsung":
		return tr.PlatSamsung
	case "lg":
		return tr.PlatLg
	case "hisense":
		return tr.PlatHisense
	case "appletv":
		return tr.PlatAppleTv
	case "browser":
		return tr.PlatBrowser
	case "other":
		return tr.PlatOther
	default:
		return ""
	}
}

// feedbackContext — строка «где именно», которая уезжает в тикет и админу.
func feedbackContext(lang Lang, fs *feedbackState) string {
	if fs.Client == "" {
		return ""
	}
	out := FbClientLabel(lang, fs.Client)
	if p := FbPlatformLabel(lang, fs.Platform); p != "" {
		out += " · " + p
	}
	return out
}

func (b *Bot) handleFeedbackInput(msg *tgMessage, fs *feedbackState) {
	text := strings.TrimSpace(msg.Text)
	lang := b.lang(msg.From)
	tr := T(lang)

	if text == "/cancel" || text == messagesRU.CatCancel || text == messagesUK.CatCancel || text == messagesEN.CatCancel {
		delete(b.feedbackWait, msg.From.ID)
		b.sendMsgWithReply(msg.Chat.ID, tr.Cancelled, b.mainMenuKeyboard(lang))
		return
	}

	switch fs.Step {
	case 1: // waiting for subject
		if text == "" || len(text) > 200 {
			b.sendMsg(msg.Chat.ID, tr.FeedbackSubjectLen, nil)
			return
		}
		fs.Subject = text
		fs.Step = 2
		b.sendMsg(msg.Chat.ID, tr.FeedbackStep3, nil)

	case 2: // waiting for message
		if text == "" || len(text) > 5000 {
			b.sendMsg(msg.Chat.ID, tr.FeedbackMsgLen, nil)
			return
		}

		delete(b.feedbackWait, msg.From.ID)

		priority := "medium"
		if fs.Category == "bug" {
			priority = "high"
		} else if fs.Category == "thanks" {
			priority = "low"
		}

		userID := fmt.Sprintf("%d", msg.From.ID)
		userName := msg.From.FirstName
		if msg.From.Username != "" {
			userName = "@" + msg.From.Username
		}

		// ★Контекст пишем в НАЧАЛО тела тикета, а не отдельным полем: хранилище тикетов
		// про клиент и платформу не знает, а тому, кто читает обращение, это нужно первой
		// строкой — иначе первым ответом снова будет «а у вас плагин или приложение?».
		body := text
		if ctx := feedbackContext(lang, fs); ctx != "" {
			body = "📍 " + ctx + "\n\n" + text
		}

		ticketID, err := b.feedbackSub.CreateTicket(userID, userName, fs.Category, priority, fs.Subject, body)
		if err != nil {
			b.sendMsgWithReply(msg.Chat.ID, tr.ErrorGeneric+err.Error(), b.mainMenuKeyboard(lang))
			return
		}

		var sb strings.Builder
		sb.WriteString(tr.FeedbackCreated)
		sb.WriteString(fmt.Sprintf("🆔 <code>%s</code>\n", ticketID[:8]))
		sb.WriteString(fmt.Sprintf("📂 %s\n", FbCatLabel(lang, fs.Category)))
		if ctx := feedbackContext(lang, fs); ctx != "" {
			sb.WriteString(fmt.Sprintf("📍 %s\n", ctx))
		}
		sb.WriteString(fmt.Sprintf("📌 %s\n", fs.Subject))

		keyboard := &tgInlineKeyboardMarkup{
			InlineKeyboard: [][]tgInlineKeyboardButton{
				{{Text: tr.FeedbackTicketView, CallbackData: "fblist"}},
				{{Text: tr.FeedbackViewTktBtn, CallbackData: "fbview:" + ticketID}},
			},
		}
		b.sendMsg(msg.Chat.ID, sb.String(), keyboard)

		// Notify admin in admin's language
		if b.cfg.AdminID > 0 {
			adminLang := b.langByID(b.cfg.AdminID)
			adminT := T(adminLang)
			adminCtx := feedbackContext(adminLang, fs)
			adminBody := text
			if adminCtx != "" {
				adminBody = "📍 " + adminCtx + "\n\n" + text
			}
			adminText := fmt.Sprintf(adminT.FeedbackAdminNew,
				userName, msg.From.ID, FbCatLabel(adminLang, fs.Category), fs.Subject, adminBody)
			if len(adminText) > 4000 {
				adminText = adminText[:4000] + "..."
			}
			b.sendMsg(b.cfg.AdminID, adminText, nil)
		}

		log.Info().Str("ticket", ticketID).Int64("tg_id", msg.From.ID).Str("cat", fs.Category).
			Str("client", fs.Client).Str("platform", fs.Platform).
			Msg("tgauth: feedback ticket created via bot")

	case 10: // reply to existing ticket
		if text == "" || len(text) > 5000 {
			b.sendMsg(msg.Chat.ID, tr.FeedbackMsgLen, nil)
			return
		}

		delete(b.feedbackWait, msg.From.ID)

		userID := fmt.Sprintf("%d", msg.From.ID)
		userName := msg.From.FirstName
		if msg.From.Username != "" {
			userName = "@" + msg.From.Username
		}

		if err := b.feedbackSub.AddUserReply(fs.TicketID, userID, userName, text); err != nil {
			b.sendMsgWithReply(msg.Chat.ID, tr.ErrorGeneric+err.Error(), b.mainMenuKeyboard(lang))
			return
		}

		keyboard := &tgInlineKeyboardMarkup{
			InlineKeyboard: [][]tgInlineKeyboardButton{
				{{Text: tr.FeedbackViewTktBtn, CallbackData: "fbview:" + fs.TicketID}},
			},
		}
		b.sendMsg(msg.Chat.ID, tr.FeedbackReplySent, keyboard)

		// Notify admin in admin's language
		if b.cfg.AdminID > 0 {
			adminLang := b.langByID(b.cfg.AdminID)
			adminT := T(adminLang)
			tkt, ok := b.feedbackSub.GetTicket(fs.TicketID)
			subj := fs.TicketID[:8]
			if ok {
				subj = tkt.Subject
			}
			adminText := fmt.Sprintf(adminT.FeedbackAdminReply, userName, userID, subj, text)
			if len(adminText) > 4000 {
				adminText = adminText[:4000] + "..."
			}
			b.sendMsg(b.cfg.AdminID, adminText, nil)
		}

		log.Info().Str("ticket", fs.TicketID).Int64("tg_id", msg.From.ID).Msg("tgauth: user replied to ticket via bot")
	}
}

func (b *Bot) handleMyTickets(msg *tgMessage) {
	lang := b.lang(msg.From)
	tr := T(lang)
	if b.feedbackSub == nil {
		b.sendMsgWithReply(msg.Chat.ID, tr.FeedbackUnavailable, b.mainMenuKeyboard(lang))
		return
	}
	text, kb := b.ticketsView(msg.From.ID, lang)
	if kb == nil {
		b.sendMsgWithReply(msg.Chat.ID, text, b.mainMenuKeyboard(lang))
		return
	}
	b.sendMsg(msg.Chat.ID, text, kb)
}

// ticketsView renders the ticket list. A nil keyboard means "nothing to list" —
// the caller decides whether that is a plain message or an edit.
// sortUserTickets orders the list the way its author reads it: answered first
// (there is something to read), then still waiting, then finished — and inside
// each group, most recent activity first. The store returns creation order,
// which buries a just-answered ticket under everything written since.
func sortUserTickets(t []FeedbackTicketInfo) {
	sort.SliceStable(t, func(i, j int) bool {
		ri, rj := FbUserStatusRank(t[i].Status, t[i].LastReplyAdmin), FbUserStatusRank(t[j].Status, t[j].LastReplyAdmin)
		if ri != rj {
			return ri < rj
		}
		return ticketActivity(t[i]) > ticketActivity(t[j])
	})
}

// ticketActivity is the sortable timestamp: last movement, falling back to
// creation for tickets nobody has touched since.
func ticketActivity(t FeedbackTicketInfo) string {
	if t.UpdatedAt != "" {
		return t.UpdatedAt
	}
	return t.CreatedAt
}

func (b *Bot) ticketsView(tgID int64, lang Lang) (string, *tgInlineKeyboardMarkup) {
	tr := T(lang)
	tickets := b.feedbackSub.UserTickets(fmt.Sprintf("%d", tgID))
	if len(tickets) == 0 {
		return tr.FeedbackNoTickets, nil
	}

	sortUserTickets(tickets)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(tr.FeedbackTicketsTitle, len(tickets)))

	limit := min(len(tickets), 10)

	var rows [][]tgInlineKeyboardButton

	for i := range limit {
		tkt := tickets[i]
		st := FbUserStatus(lang, tkt.Status, tkt.LastReplyAdmin)
		replyInfo := ""
		if tkt.Replies > 0 {
			replyInfo = fmt.Sprintf(" · 💬 %d", tkt.Replies)
		}
		// Дата последнего движения, а не создания: в списке, отсортированном по
		// активности, дата создания выглядит как ошибка сортировки.
		date := fmtISODate(tkt.UpdatedAt)
		if date == "" || tkt.UpdatedAt == "" {
			date = fmtISODate(tkt.CreatedAt)
		}
		sb.WriteString(fmt.Sprintf("%d. %s <b>%s</b>\n   %s · %s%s\n\n",
			i+1, FbCatLabel(lang, tkt.Category), tkt.Subject, st, date, replyInfo))

		btnText := fmt.Sprintf("%d. %s", i+1, truncate(tkt.Subject, 25))
		if tkt.LastReplyAdmin {
			btnText = "💬 " + btnText // то, что надо прочитать, видно по кнопке
		} else if tkt.Replies > 0 {
			btnText += fmt.Sprintf(" · %d", tkt.Replies)
		}
		rows = append(rows, []tgInlineKeyboardButton{
			{Text: btnText, CallbackData: "fbview:" + tkt.ID},
		})
	}

	if len(tickets) > 10 {
		sb.WriteString(fmt.Sprintf("... +%d\n", len(tickets)-10))
	}

	sb.WriteString(tr.ClickBelow)

	// Always a way back into the section — a list you can only leave by typing a
	// command is a dead end on a phone.
	rows = append(rows, []tgInlineKeyboardButton{
		{Text: tr.BtnFbBack, CallbackData: "fbhub"},
	})
	return sb.String(), &tgInlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) handleViewTicket(chatID int64, ticketID string) {
	lang := b.langByID(chatID)
	tr := T(lang)
	if b.feedbackSub == nil {
		b.sendMsg(chatID, tr.FeedbackUnavailable, nil)
		return
	}

	tkt, ok := b.feedbackSub.GetTicket(ticketID)
	if !ok {
		b.sendMsg(chatID, tr.DeviceNotFound, nil)
		return
	}

	userID := fmt.Sprintf("%d", chatID)
	if tkt.UserID != userID {
		b.sendMsg(chatID, tr.NoAccess, nil)
		return
	}

	// Тот же статус, что и в списке: «🟡 В работе» на карточке рядом с «💬 Есть
	// ответ» в списке читалось как два разных тикета.
	lastAdmin := len(tkt.Replies) > 0 && tkt.Replies[len(tkt.Replies)-1].IsAdmin
	st := FbUserStatus(lang, tkt.Status, lastAdmin)
	date := fmtISODateTime(tkt.CreatedAt)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📌 <b>%s</b>\n\n", tkt.Subject))
	sb.WriteString(fmt.Sprintf("%s · %s · %s\n", FbCatLabel(lang, tkt.Category), st, date))
	sb.WriteString(fmt.Sprintf(tr.FeedbackYourMsg, tkt.Message))

	if len(tkt.Replies) > 0 {
		sb.WriteString(fmt.Sprintf(tr.FeedbackThread, len(tkt.Replies)))
		for _, r := range tkt.Replies {
			rDate := fmtISODateTime(r.CreatedAt)
			if r.IsAdmin {
				sb.WriteString(fmt.Sprintf(tr.FeedbackSupport, rDate))
			} else {
				author := r.Author
				if author == "" {
					author = "You"
				}
				sb.WriteString(fmt.Sprintf(tr.FeedbackYou, author, rDate))
			}
			sb.WriteString(r.Message + "\n\n")
		}
	} else {
		sb.WriteString(tr.FeedbackNoReplies)
	}

	text := sb.String()
	if len(text) > 4000 {
		text = text[:4000] + "\n..."
	}

	keyboard := &tgInlineKeyboardMarkup{
		InlineKeyboard: [][]tgInlineKeyboardButton{
			{
				{Text: tr.FeedbackReplyBtn, CallbackData: "fbreply:" + ticketID},
				{Text: tr.FeedbackListBtn, CallbackData: "fblist"},
			},
		},
	}
	b.sendMsg(chatID, text, keyboard)
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}

// ---------- Telegram API helpers ----------

func (b *Bot) getUpdates(ctx context.Context, offset int64) ([]tgUpdate, error) {
	// allowed_updates: listing the types we actually consume is defensive —
	// future Telegram additions (e.g. message_reaction) won't auto-flow in.
	body, _ := json.Marshal(map[string]any{
		"offset":  offset,
		"timeout": 30,
		"allowed_updates": []string{
			"message",
			"callback_query",
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/getUpdates", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result.Result, nil
}

func (b *Bot) sendMsg(chatID int64, text string, keyboard *tgInlineKeyboardMarkup) {
	payload := map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	b.apiPost("sendMessage", payload)
}

func (b *Bot) sendMsgWithReply(chatID int64, text string, keyboard any) {
	payload := map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	b.apiPost("sendMessage", payload)
}

func (b *Bot) editMsg(chatID int64, messageID int64, text string) {
	b.apiPost("editMessageText", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
		"parse_mode": "HTML",
	})
}

func (b *Bot) editMsgWithKeyboard(chatID int64, messageID int64, text string, keyboard *tgInlineKeyboardMarkup) {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
		"parse_mode": "HTML",
	}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	b.apiPost("editMessageText", payload)
}

func (b *Bot) answerCallback(callbackID, text string) {
	b.apiPost("answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
	})
}

func (b *Bot) apiPost(method string, payload map[string]any) {
	body, _ := json.Marshal(payload)
	resp, err := b.client.Post(b.baseURL+"/"+method, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Warn().Err(err).Str("method", method).Msg("tgauth: API call failed")
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	// Telegram always returns HTTP 200; errors are in JSON {"ok":false,"description":"..."}
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if json.Unmarshal(data, &result) == nil && !result.OK {
		log.Warn().Str("method", method).Str("error", result.Description).Msg("tgauth: Telegram API error")
	}
}

// ---------- Telegram types (minimal subset) ----------

type tgUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *tgMessage       `json:"message"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

type tgMessage struct {
	MessageID      int64      `json:"message_id"`
	From           *tgUser    `json:"from"`
	Chat           *tgChat    `json:"chat"`
	Text           string     `json:"text"`
	Caption        string     `json:"caption"`          // подпись к фото/видео
	ReplyToMessage *tgMessage `json:"reply_to_message"` // set when this message replies to another — drives admin→user reply
}

type tgCallbackQuery struct {
	ID      string     `json:"id"`
	From    *tgUser    `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

type tgUser struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	Username     string `json:"username"`
	FirstName    string `json:"first_name"`
	LanguageCode string `json:"language_code"`
}

type tgChat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"` // "private", "group", "supergroup", "channel"
	Title string `json:"title"`
}

type tgInlineKeyboardMarkup struct {
	InlineKeyboard [][]tgInlineKeyboardButton `json:"inline_keyboard"`
}

type tgInlineKeyboardButton struct {
	Text         string    `json:"text"`
	CallbackData string    `json:"callback_data,omitempty"`
	WebApp       *tgWebApp `json:"web_app,omitempty"`
	URL          string    `json:"url,omitempty"` // direct URL button (e.g. CryptoCloud pay link)
}

type tgReplyKeyboardMarkup struct {
	Keyboard        [][]tgKeyboardButton `json:"keyboard"`
	ResizeKeyboard  bool                 `json:"resize_keyboard"`
	OneTimeKeyboard bool                 `json:"one_time_keyboard,omitempty"`
}

type tgKeyboardButton struct {
	Text   string    `json:"text"`
	WebApp *tgWebApp `json:"web_app,omitempty"`
}

type tgWebApp struct {
	URL string `json:"url"`
}

// handleYouTubeAuth starts Google Device Flow for YouTube authorization.
func (b *Bot) handleYouTubeAuth(msg *tgMessage) {
	lang := b.lang(msg.From)

	if b.ytAuth == nil {
		b.sendMsg(msg.Chat.ID, "YouTube OAuth не настроен на сервере.", nil)
		return
	}

	if b.ytAuth.IsLinked(msg.From.ID) {
		title := b.ytAuth.ChannelTitle(msg.From.ID)
		if title == "" {
			title = "—"
		}
		b.sendMsg(msg.Chat.ID, fmt.Sprintf("✅ YouTube подключён: %s\n\nДля отключения: /youtube_unbind", title), nil)
		return
	}

	userCode, verURL, pollDone, err := b.ytAuth.StartAuth(msg.From.ID)
	if err != nil {
		b.sendMsg(msg.Chat.ID, "❌ Ошибка: "+err.Error(), nil)
		return
	}

	b.sendMsg(msg.Chat.ID,
		fmt.Sprintf("🔗 Для подключения YouTube:\n\n1. Перейдите: %s\n2. Введите код: <code>%s</code>\n\nОжидаю авторизацию...", verURL, userCode),
		nil,
	)

	// Wait for result in background.
	go func() {
		err := <-pollDone
		if err != nil {
			b.sendMsgWithReply(msg.Chat.ID, "❌ Авторизация не удалась: "+err.Error(), b.mainMenuKeyboard(lang))
			return
		}
		title := b.ytAuth.ChannelTitle(msg.From.ID)
		if title == "" {
			title = "YouTube"
		}
		b.sendMsgWithReply(msg.Chat.ID, fmt.Sprintf("✅ YouTube подключён: %s", title), b.mainMenuKeyboard(lang))
	}()
}

// handleYouTubeUnbind removes the YouTube connection.
func (b *Bot) handleYouTubeUnbind(msg *tgMessage) {
	lang := b.lang(msg.From)

	if b.ytAuth == nil {
		b.sendMsg(msg.Chat.ID, "YouTube OAuth не настроен.", nil)
		return
	}
	if !b.ytAuth.IsLinked(msg.From.ID) {
		b.sendMsg(msg.Chat.ID, "YouTube не подключён.", nil)
		return
	}
	if err := b.ytAuth.Unlink(msg.From.ID); err != nil {
		b.sendMsg(msg.Chat.ID, "❌ Ошибка: "+err.Error(), nil)
		return
	}
	b.sendMsgWithReply(msg.Chat.ID, "YouTube отключён.", b.mainMenuKeyboard(lang))
}

// ---- Calendar handlers ----

func (b *Bot) handleCalendar(msg *tgMessage) {
	if b.calendarH == nil {
		b.sendMsg(msg.Chat.ID, "Контент-календарь не включён на сервере.", nil)
		return
	}

	upcoming := b.calendarH.UpcomingSummary(msg.From.ID, 10)
	count := b.calendarH.TrackedCount(msg.From.ID)
	titles := b.calendarH.TrackedTitles(msg.From.ID, 15)

	var sb strings.Builder
	sb.WriteString("📅 <b>Контент-календарь</b>\n\n")

	if len(upcoming) > 0 {
		sb.WriteString("<b>Ближайшие эпизоды:</b>\n")
		for _, line := range upcoming {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	if count > 0 {
		sb.WriteString(fmt.Sprintf("<b>Отслеживаемые:</b> %d сериалов\n", count))
		for _, t := range titles {
			sb.WriteString("• " + t + "\n")
		}
		if count > len(titles) {
			sb.WriteString(fmt.Sprintf("... и ещё %d\n", count-len(titles)))
		}
	} else {
		sb.WriteString("Вы ещё не отслеживаете сериалы.\n")
		sb.WriteString("Откройте карточку сериала в Lampa и нажмите «📅 Отслеживать».\n")
	}

	notify := b.calendarH.GetNotifyTG(msg.From.ID)
	if notify {
		sb.WriteString("\n🔔 Уведомления: включены")
		sb.WriteString("\nОтключить: /calendar_notify off")
	} else {
		sb.WriteString("\n🔕 Уведомления: выключены")
		sb.WriteString("\nВключить: /calendar_notify on")
	}

	b.sendMsg(msg.Chat.ID, sb.String(), nil)
}

func (b *Bot) handleCalendarNotify(msg *tgMessage) {
	if b.calendarH == nil {
		b.sendMsg(msg.Chat.ID, "Контент-календарь не включён на сервере.", nil)
		return
	}

	text := strings.TrimSpace(msg.Text)
	parts := strings.Fields(text)
	if len(parts) < 2 {
		current := b.calendarH.GetNotifyTG(msg.From.ID)
		if current {
			b.sendMsg(msg.Chat.ID, "🔔 Уведомления включены.\nОтключить: /calendar_notify off", nil)
		} else {
			b.sendMsg(msg.Chat.ID, "🔕 Уведомления выключены.\nВключить: /calendar_notify on", nil)
		}
		return
	}

	arg := strings.ToLower(parts[1])
	switch arg {
	case "on", "1", "true", "вкл":
		b.calendarH.SetNotifyTG(msg.From.ID, true)
		b.sendMsg(msg.Chat.ID, "🔔 Уведомления о новых эпизодах включены.", nil)
	case "off", "0", "false", "выкл":
		b.calendarH.SetNotifyTG(msg.From.ID, false)
		b.sendMsg(msg.Chat.ID, "🔕 Уведомления о новых эпизодах выключены.", nil)
	default:
		b.sendMsg(msg.Chat.ID, "Использование: /calendar_notify on или /calendar_notify off", nil)
	}
}
