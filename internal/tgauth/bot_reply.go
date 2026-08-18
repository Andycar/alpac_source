package tgauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Two-way admin↔user relay over Telegram, no panel needed. When the bot notifies an admin about a
// specific user (an incident, a feedback ticket) it remembers «this message is about user X». If the
// admin REPLIES to it, the text is relayed to user X — AND the delivered message is itself registered,
// so if the user replies back it returns to that admin, and so on. Each relayed message seeds the next
// hop, giving a full back-and-forth thread right inside Telegram.

// replyKey is (chat, message) — Telegram message ids are per-chat, so the chat MUST be part of the key
// or an admin-chat id could collide with a user-chat id.
type replyKey struct {
	chat int64
	msg  int64
}

type replyTarget struct {
	peerTG  int64     // who a reply to this message should be relayed TO
	label   string    // subject shown both ways, e.g. «инцидент #0621-…»
	toAdmin bool      // true: peer is an admin (this msg lives in a USER chat); false: peer is the user
	at      time.Time // for bounded eviction
}

const replyTargetsMax = 1000

// apiPostResult is like apiPost but returns the sent message's id (result.message_id), needed so a
// later reply can be matched back to the message it answers. 0 on any failure.
func (b *Bot) apiPostResult(method string, payload map[string]any) int64 {
	body, _ := json.Marshal(payload)
	resp, err := b.client.Post(b.baseURL+"/"+method, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Warn().Err(err).Str("method", method).Msg("tgauth: API call failed")
		return 0
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
		Description string `json:"description"`
	}
	if json.Unmarshal(data, &result) != nil || !result.OK {
		log.Warn().Str("method", method).Str("error", result.Description).Msg("tgauth: Telegram API error")
		return 0
	}
	return result.Result.MessageID
}

func (b *Bot) sendMsgReturnID(chatID int64, text string) int64 {
	return b.apiPostResult("sendMessage", map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML"})
}

// isAdminTG reports whether a Telegram id is the super-admin or a delegated admin-panel user.
func (b *Bot) isAdminTG(id int64) bool {
	if id != 0 && id == b.cfg.AdminID {
		return true
	}
	if b.adminIDLister != nil {
		for _, a := range b.adminIDLister.AdminTelegramIDs() {
			if a == id {
				return true
			}
		}
	}
	return false
}

// registerReplyTarget records that a reply to (chat,msgID) should be relayed to peerTG.
func (b *Bot) registerReplyTarget(chat, msgID, peerTG int64, label string, toAdmin bool) {
	if msgID == 0 || peerTG == 0 || chat == 0 {
		return
	}
	b.replyMu.Lock()
	defer b.replyMu.Unlock()
	if b.replyTargets == nil {
		b.replyTargets = make(map[replyKey]replyTarget)
	}
	b.replyTargets[replyKey{chat: chat, msg: msgID}] = replyTarget{peerTG: peerTG, label: label, toAdmin: toAdmin, at: time.Now()}
	if len(b.replyTargets) > replyTargetsMax {
		cut := time.Now().Add(-48 * time.Hour)
		for k, v := range b.replyTargets {
			if v.at.Before(cut) {
				delete(b.replyTargets, k)
			}
		}
		for k := range b.replyTargets { // still too big → trim arbitrarily
			if len(b.replyTargets) <= replyTargetsMax {
				break
			}
			delete(b.replyTargets, k)
		}
	}
}

// NotifyUserContext sends `text` to every admin chat and records each notification as «about userTG»,
// so a reply to it relays to that user (and opens the thread). label identifies the subject. No-op
// without a userTG (nothing to reply to) — falls back to a plain NotifyAdmins.
func (b *Bot) NotifyUserContext(text string, userTG int64, label string) {
	if b == nil || b.cfg.Token == "" || strings.TrimSpace(text) == "" {
		return
	}
	if userTG == 0 {
		b.NotifyAdmins(text)
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
	hint := text + "\n\n<i>↩️ Ответьте на это сообщение — текст уйдёт пользователю.</i>"
	for _, id := range ids {
		if id == 0 {
			continue
		}
		mid := b.sendMsgReturnID(id, hint)
		b.registerReplyTarget(id, mid, userTG, label, false) // admin replies here → relay to the user
	}
}

// AdminReplyHook, if set, is invoked when an admin's reply is relayed to a user — with the relay
// label (e.g. «инцидент #0624-…») and the raw reply text. The httpapi layer sets it to mark the
// matching incident «answered» so the user's «Мои обращения» list updates. Decoupled to avoid a
// tgauth→httpapi import cycle.
var AdminReplyHook func(label, text string)

// tryHandleRelayReply intercepts a reply to a tracked relay message and forwards it the OTHER way,
// registering the delivered message so the thread can continue. Returns true when handled (caller must
// stop). Direction is taken from the stored target, not from who sent it. Cheap no-op otherwise.
func (b *Bot) tryHandleRelayReply(msg *tgMessage) bool {
	if msg == nil || msg.ReplyToMessage == nil || msg.From == nil || msg.Chat == nil || strings.TrimSpace(msg.Text) == "" {
		return false
	}
	b.replyMu.Lock()
	tgt, ok := b.replyTargets[replyKey{chat: msg.Chat.ID, msg: msg.ReplyToMessage.MessageID}]
	b.replyMu.Unlock()
	if !ok {
		return false
	}
	subject := tgt.label
	if subject == "" {
		subject = "обращение"
	}
	body := html.EscapeString(msg.Text) // user/admin free text → escape for parse_mode=HTML

	if !tgt.toAdmin {
		// ADMIN → USER. Only an admin may push a message into a user's chat.
		if !b.isAdminTG(msg.From.ID) {
			return false
		}
		t := T(b.langByID(tgt.peerTG))
		out := fmt.Sprintf(t.FeedbackReplyNotify, html.EscapeString(subject), body) +
			"\n\n<i>↩️ Ответьте на это сообщение, чтобы написать в ответ.</i>"
		mid := b.sendMsgReturnID(tgt.peerTG, out)
		if mid != 0 {
			// the user can now reply → it returns to THIS admin, continuing the thread
			b.registerReplyTarget(tgt.peerTG, mid, msg.From.ID, subject, true)
			b.sendMsg(msg.Chat.ID, fmt.Sprintf("✅ Отправлено пользователю (%s).", subject), nil)
			log.Info().Int64("admin", msg.From.ID).Int64("user", tgt.peerTG).Str("subject", subject).Msg("tgauth: admin→user reply relayed")
			if AdminReplyHook != nil {
				AdminReplyHook(subject, msg.Text) // e.g. let the app mark the linked incident «answered»
			}
		} else {
			b.sendMsg(msg.Chat.ID, "❌ Не удалось доставить (пользователь заблокировал бота?).", nil)
		}
		return true
	}

	// USER → ADMIN. The replier is the user (msg lives in their own chat); relay to the admin.
	out := fmt.Sprintf("💬 Ответ пользователя по «%s»:\n\n%s\n\n<i>↩️ Ответьте на это сообщение, чтобы написать пользователю.</i>",
		html.EscapeString(subject), body)
	mid := b.sendMsgReturnID(tgt.peerTG, out)
	if mid != 0 {
		// the admin can reply again → it returns to THIS user
		b.registerReplyTarget(tgt.peerTG, mid, msg.From.ID, subject, false)
		b.sendMsg(msg.Chat.ID, "✅ Отправлено, ожидайте ответа.", nil)
		log.Info().Int64("user", msg.From.ID).Int64("admin", tgt.peerTG).Str("subject", subject).Msg("tgauth: user→admin reply relayed")
	} else {
		b.sendMsg(msg.Chat.ID, "❌ Не удалось доставить.", nil)
	}
	return true
}
