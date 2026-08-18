package tgauth

import (
	"fmt"
	"html"
	"strings"
)

// profileBotState tracks a user's in-progress multi-step dialog under
// /profiles. Stored on Bot.profileWait keyed by tgID.
//
// Step values:
//
//	1 — waiting for username (create flow)
//	2 — waiting for PIN to set (create or change flow); PendingProfileID set
type profileBotState struct {
	Step             int
	PendingProfileID string
}

const (
	profilesCBNew    = "profiles:new"
	profilesCBPrefix = "profiles:" // pattern: profiles:<action>:<profile-id>
)

// handleProfilesCommand renders the /profiles screen for the calling TG
// user: a short header + inline keyboard listing each owned profile and
// a "Create new" button. Per-profile rows offer Set/Change PIN + Delete.
func (b *Bot) handleProfilesCommand(msg *tgMessage) {
	if b == nil {
		return
	}
	if b.profileMgr == nil {
		b.sendMsg(msg.Chat.ID, "Управление профилями не настроено в этой инсталляции.", nil)
		return
	}
	list := b.profileMgr.OwnedList(msg.From.ID)

	var lines []string
	lines = append(lines, "🗂 <b>Ваши профили синхронизации</b>")
	if len(list) == 0 {
		lines = append(lines, "")
		lines = append(lines, "У вас пока нет профилей. Создайте один, чтобы синхронизировать закладки и историю просмотров между устройствами без TG-логина.")
	} else {
		lines = append(lines, "")
		for i, p := range list {
			pinTag := "без PIN"
			if p.HasPIN {
				pinTag = "PIN установлен"
			}
			lines = append(lines, fmt.Sprintf("%d. <b>%s</b> — %s", i+1, html.EscapeString(p.Username), pinTag))
		}
	}

	kb := b.buildProfilesKeyboard(list)
	b.sendMsg(msg.Chat.ID, strings.Join(lines, "\n"), kb)
}

func (b *Bot) buildProfilesKeyboard(list []ProfileSummary) *tgInlineKeyboardMarkup {
	rows := make([][]tgInlineKeyboardButton, 0, len(list)*2+1)
	for _, p := range list {
		setPINText := "🔢 " + p.Username + ": задать PIN"
		if p.HasPIN {
			setPINText = "🔄 " + p.Username + ": сменить PIN"
		}
		rows = append(rows, []tgInlineKeyboardButton{
			{Text: setPINText, CallbackData: "profiles:setpin:" + p.ID},
		})
		rows = append(rows, []tgInlineKeyboardButton{
			{Text: "🗑 Удалить " + p.Username, CallbackData: "profiles:delete:" + p.ID},
		})
	}
	rows = append(rows, []tgInlineKeyboardButton{
		{Text: "➕ Создать новый профиль", CallbackData: profilesCBNew},
	})
	return &tgInlineKeyboardMarkup{InlineKeyboard: rows}
}

// handleProfilesCallback routes inline-button taps from the /profiles
// screen. Called by the main callback dispatcher in bot.go. Returns true
// when the data was recognised and handled.
func (b *Bot) handleProfilesCallback(cb *tgCallbackQuery, data string) bool {
	if b == nil || b.profileMgr == nil {
		return false
	}
	if !strings.HasPrefix(data, profilesCBPrefix) {
		return false
	}
	if b.profileWait == nil {
		b.profileWait = make(map[int64]*profileBotState)
	}
	rest := strings.TrimPrefix(data, profilesCBPrefix)

	switch {
	case rest == "new":
		b.profileWait[cb.From.ID] = &profileBotState{Step: 1}
		b.answerCallback(cb.ID, "")
		b.sendMsg(cb.Message.Chat.ID, "Введите имя профиля (3-32 символа, латиница / цифры):", nil)

	case strings.HasPrefix(rest, "setpin:"):
		pid := strings.TrimPrefix(rest, "setpin:")
		b.profileWait[cb.From.ID] = &profileBotState{Step: 2, PendingProfileID: pid}
		b.answerCallback(cb.ID, "")
		b.sendMsg(cb.Message.Chat.ID, "Введите новый PIN (4-8 цифр). Отправьте «нет» чтобы убрать PIN:", nil)

	case strings.HasPrefix(rest, "delete:"):
		pid := strings.TrimPrefix(rest, "delete:")
		if err := b.profileMgr.OwnedDelete(pid, cb.From.ID); err != nil {
			b.answerCallback(cb.ID, "Не удалось удалить: "+err.Error())
			return true
		}
		b.answerCallback(cb.ID, "Удалено")
		// Re-render the screen with the updated list.
		if cb.Message != nil {
			fakeMsg := &tgMessage{From: cb.From, Chat: cb.Message.Chat}
			b.handleProfilesCommand(fakeMsg)
		}
	default:
		return false
	}
	return true
}

// handleProfileWaitInput consumes a chat message when the user is mid-way
// through the /profiles dialog. Returns true iff the message was consumed.
func (b *Bot) handleProfileWaitInput(msg *tgMessage, text string) bool {
	if b == nil || b.profileMgr == nil || b.profileWait == nil {
		return false
	}
	st, ok := b.profileWait[msg.From.ID]
	if !ok {
		return false
	}

	switch st.Step {
	case 1:
		username := strings.TrimSpace(text)
		if username == "" {
			delete(b.profileWait, msg.From.ID)
			b.sendMsg(msg.Chat.ID, "Имя пустое — отменено.", nil)
			return true
		}
		id, err := b.profileMgr.OwnedCreate(msg.From.ID, username, "")
		if err != nil {
			delete(b.profileWait, msg.From.ID)
			b.sendMsg(msg.Chat.ID, "Не удалось создать профиль: "+err.Error(), nil)
			return true
		}
		st.Step = 2
		st.PendingProfileID = id
		b.sendMsg(msg.Chat.ID, "Профиль создан. Теперь введите PIN (4-8 цифр), который вы будете вводить на устройстве для входа в этот профиль:", nil)
		return true

	case 2:
		pid := st.PendingProfileID
		delete(b.profileWait, msg.From.ID)
		pin := strings.TrimSpace(text)
		if pin == "нет" || pin == "no" || pin == "-" {
			pin = ""
		}
		if err := b.profileMgr.OwnedSetPIN(pid, msg.From.ID, pin); err != nil {
			b.sendMsg(msg.Chat.ID, "Не удалось установить PIN: "+err.Error(), nil)
			return true
		}
		if pin == "" {
			b.sendMsg(msg.Chat.ID, "PIN снят. Вход по PIN отключён для этого профиля.", nil)
		} else {
			b.sendMsg(msg.Chat.ID, "PIN сохранён. Введите его на новом устройстве в разделе Настройки → Синхронизация → Войти по PIN.", nil)
		}
		b.handleProfilesCommand(msg)
		return true
	}

	return false
}
