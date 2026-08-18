package tgauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// MembershipChecker verifies that users are subscribed to required Telegram
// channels/groups. It caches results and runs a periodic background sweep
// to revoke tokens for users who left.
type MembershipChecker struct {
	mu       sync.RWMutex
	chats    []config.RequiredChat
	interval time.Duration
	botToken string
	store    *Store
	client   *http.Client
	cache    map[int64]*memberCacheEntry // tgID → last check
	stopCh   chan struct{}
	doneCh   chan struct{}

	// notifyFn sends a TG message to a user (set by Bot).
	notifyFn func(tgID int64, text string)
}

type memberCacheEntry struct {
	ok          bool      // true = member of all required chats
	failedMsg   string    // user-facing message (for auth rejection)
	failedTitle string    // which chat title failed (for revoke message)
	checkedAt   time.Time // when the check was performed
}

// NewMembershipChecker creates a checker. Start() must be called separately.
func NewMembershipChecker(botToken string, store *Store, chats []config.RequiredChat, intervalMin int) *MembershipChecker {
	if intervalMin <= 0 {
		intervalMin = 60
	}
	return &MembershipChecker{
		chats:    chats,
		interval: time.Duration(intervalMin) * time.Minute,
		botToken: botToken,
		store:    store,
		client:   &http.Client{Timeout: 10 * time.Second},
		cache:    make(map[int64]*memberCacheEntry),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// SetNotifyFn sets the function used to send messages to users (typically bot.SendToUser).
func (mc *MembershipChecker) SetNotifyFn(fn func(tgID int64, text string)) {
	mc.notifyFn = fn
}

// SetChats hot-reloads the list of required chats and check interval.
func (mc *MembershipChecker) SetChats(chats []config.RequiredChat, intervalMin int) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.chats = chats
	if intervalMin > 0 {
		mc.interval = time.Duration(intervalMin) * time.Minute
	}
	// Clear cache — new requirements
	mc.cache = make(map[int64]*memberCacheEntry)
}

// RequiredChats returns the current list (thread-safe).
func (mc *MembershipChecker) RequiredChats() []config.RequiredChat {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.chats
}

// HasRequirements returns true if there are any required chats configured.
func (mc *MembershipChecker) HasRequirements() bool {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return len(mc.chats) > 0
}

// CheckMembership verifies that the user is a member of all required chats.
// Returns (true, "") if OK, or (false, userMessage) if not subscribed.
// Updates cache on success/failure.
func (mc *MembershipChecker) CheckMembership(tgID int64) (bool, string) {
	mc.mu.RLock()
	chats := mc.chats
	mc.mu.RUnlock()

	if len(chats) == 0 {
		return true, ""
	}

	for _, chat := range chats {
		ok, err := mc.isMember(chat.ChatID, tgID)
		if err != nil {
			log.Warn().Err(err).Int64("tg_id", tgID).Int64("chat_id", chat.ChatID).Msg("membership: getChatMember error")
			// On API error, be lenient — don't block the user
			continue
		}
		if !ok {
			msg := mc.buildRequiredChatsMessage(chat.Title)
			mc.cacheResult(tgID, false, msg, chat.Title)
			return false, msg
		}
	}

	mc.cacheResult(tgID, true, "", "")
	return true, ""
}

// CachedCheck returns a cached membership result. Returns (ok, msg, found).
// If the cached entry is older than the check interval, found=false.
func (mc *MembershipChecker) CachedCheck(tgID int64) (ok bool, msg string, found bool) {
	mc.mu.RLock()
	entry, exists := mc.cache[tgID]
	interval := mc.interval
	mc.mu.RUnlock()

	if !exists {
		return false, "", false
	}
	if time.Since(entry.checkedAt) > interval {
		return false, "", false // stale
	}
	return entry.ok, entry.failedMsg, true
}

// Start launches the background membership sweep goroutine.
func (mc *MembershipChecker) Start(ctx context.Context) {
	go func() {
		defer close(mc.doneCh)

		// Initial delay — let the server start up
		select {
		case <-time.After(2 * time.Minute):
		case <-ctx.Done():
			return
		case <-mc.stopCh:
			return
		}

		mc.sweep()

		for {
			mc.mu.RLock()
			interval := mc.interval
			mc.mu.RUnlock()

			select {
			case <-time.After(interval):
				mc.sweep()
			case <-ctx.Done():
				return
			case <-mc.stopCh:
				return
			}
		}
	}()
	log.Info().Int("chats", len(mc.chats)).Dur("interval", mc.interval).Msg("membership: checker started")
}

// Stop halts the background sweep.
func (mc *MembershipChecker) Stop() {
	select {
	case <-mc.stopCh:
	default:
		close(mc.stopCh)
	}
	<-mc.doneCh
}

// sweep iterates all tokens, checks membership, revokes non-subscribers.
func (mc *MembershipChecker) sweep() {
	mc.mu.RLock()
	chats := mc.chats
	mc.mu.RUnlock()

	if len(chats) == 0 {
		return
	}

	tokens := mc.store.List()
	now := time.Now().UTC()
	checked := 0
	revoked := 0

	for _, tok := range tokens {
		if tok.TelegramID == 0 || now.After(tok.ExpiresAt) {
			continue
		}

		ok, _ := mc.CheckMembership(tok.TelegramID)
		checked++
		if ok {
			continue
		}

		// User is not subscribed — revoke token
		_ = mc.store.Remove(tok.Token)
		revoked++

		// Get failed title from cache for a more specific revoke message
		failedTitle := mc.cachedFailedTitle(tok.TelegramID)

		log.Info().
			Int64("tg_id", tok.TelegramID).
			Str("username", tok.TGUsername).
			Str("failed_chat", failedTitle).
			Msg("membership: token revoked (not subscribed)")

		// Notify user with detailed revocation message
		if mc.notifyFn != nil {
			revokeMsg := mc.buildRevokedMessage(failedTitle)
			mc.notifyFn(tok.TelegramID, revokeMsg)
		}

		// Rate limit: don't hammer TG API
		time.Sleep(100 * time.Millisecond)
	}

	if checked > 0 {
		log.Info().Int("checked", checked).Int("revoked", revoked).Msg("membership: sweep complete")
	}
}

// isMember calls Telegram Bot API getChatMember and returns true if the user
// has status "member", "administrator", or "creator".
func (mc *MembershipChecker) isMember(chatID, userID int64) (bool, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getChatMember", mc.botToken)
	payload, _ := json.Marshal(map[string]any{
		"chat_id": chatID,
		"user_id": userID,
	})

	resp, err := mc.client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return false, fmt.Errorf("bad response: %s", string(data))
	}

	if !result.OK {
		// "Bad Request: user not found" means the user never interacted with the chat
		if strings.Contains(result.Description, "user not found") {
			return false, nil
		}
		return false, fmt.Errorf("API error: %s", result.Description)
	}

	switch result.Result.Status {
	case "member", "administrator", "creator", "restricted":
		// "restricted" means still in the chat (just limited), so we consider OK
		return true, nil
	default: // "left", "kicked"
		return false, nil
	}
}

func (mc *MembershipChecker) cachedFailedTitle(tgID int64) string {
	mc.mu.RLock()
	entry, exists := mc.cache[tgID]
	mc.mu.RUnlock()
	if !exists {
		return ""
	}
	return entry.failedTitle
}

func (mc *MembershipChecker) cacheResult(tgID int64, ok bool, msg, failedTitle string) {
	mc.mu.Lock()
	mc.cache[tgID] = &memberCacheEntry{
		ok:          ok,
		failedMsg:   msg,
		failedTitle: failedTitle,
		checkedAt:   time.Now(),
	}
	mc.mu.Unlock()
}

// buildRequiredChatsMessage builds a user-facing message listing all required chats.
// failedTitle highlights which chat the user is not subscribed to.
func (mc *MembershipChecker) buildRequiredChatsMessage(failedTitle string) string {
	mc.mu.RLock()
	chats := mc.chats
	mc.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("Для доступа к серверу необходимо быть подписанным на:\n\n")
	for _, c := range chats {
		marker := "✅ "
		if c.Title == failedTitle {
			marker = "❌ "
		}
		sb.WriteString(marker)
		sb.WriteString(c.Title)
		if c.Link != "" {
			sb.WriteString(" — ")
			sb.WriteString(tgLinkDNSSafe(c.Link))
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("\nПодпишитесь и отправьте код повторно.")
	sb.WriteString("\n\n⚠️ Не выходите из каналов/групп — подписка проверяется периодически. При выходе доступ будет аннулирован.")
	return sb.String()
}

// buildRevokedMessage builds a message for when access is revoked because user left.
// tgLinkDNSSafe rewrites t.me links to the telegram.me alias: several ISP resolvers
// fail on t.me (user reports «проблема с резолвом t.me из DNS»), while telegram.me
// resolves reliably. Admin-entered channel links in existing configs keep working as-is.
func tgLinkDNSSafe(link string) string {
	for _, p := range []string{"https://t.me/", "http://t.me/", "t.me/"} {
		if strings.HasPrefix(link, p) {
			return "https://telegram.me/" + strings.TrimPrefix(link, p)
		}
	}
	return link
}

func (mc *MembershipChecker) buildRevokedMessage(failedTitle string) string {
	mc.mu.RLock()
	chats := mc.chats
	mc.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("❌ Доступ аннулирован\n\n")
	sb.WriteString("Вы вышли из обязательного канала/группы: <b>")
	sb.WriteString(failedTitle)
	sb.WriteString("</b>\n\n")
	sb.WriteString("Для восстановления доступа подпишитесь на:\n\n")
	for _, c := range chats {
		sb.WriteString("• ")
		sb.WriteString(c.Title)
		if c.Link != "" {
			sb.WriteString(" — ")
			sb.WriteString(tgLinkDNSSafe(c.Link))
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("\nПосле подписки запросите новый код доступа.")
	return sb.String()
}
