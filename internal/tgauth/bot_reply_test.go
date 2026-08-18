package tgauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestRelayReplyBidirectional drives a full incident thread over Telegram with a mock Bot API:
// notify admin → admin replies (→ user) → user replies (→ admin). Each hop must route to the right
// chat and register the next reply target so the conversation can continue.
func TestRelayReplyBidirectional(t *testing.T) {
	const adminID = int64(42)
	const userID = int64(100)

	var mu sync.Mutex
	type sent struct {
		chat int64
		text string
		id   int64
	}
	var sends []sent
	var nextID int64 = 1000

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal(body, &p)
		mu.Lock()
		nextID++
		id := nextID
		sends = append(sends, sent{chat: p.ChatID, text: p.Text, id: id})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, id)
	}))
	defer srv.Close()

	b := &Bot{
		cfg:       BotConfig{Token: "test", AdminID: adminID},
		client:    srv.Client(),
		baseURL:   srv.URL,
		langStore: NewLangStore(t.TempDir()),
	}

	lastTo := func(chat int64) sent {
		mu.Lock()
		defer mu.Unlock()
		for i := len(sends) - 1; i >= 0; i-- {
			if sends[i].chat == chat {
				return sends[i]
			}
		}
		t.Fatalf("no message sent to chat %d; sends=%+v", chat, sends)
		return sent{}
	}

	// 1) incident notification → admin chat, registered so an admin reply reaches the user.
	b.NotifyUserContext("инцидент произошёл", userID, "инцидент #X")
	adminNotif := lastTo(adminID)
	if !strings.Contains(adminNotif.text, "инцидент") {
		t.Fatalf("admin notification missing body: %q", adminNotif.text)
	}

	// 2) admin REPLIES to that notification → relayed to the user.
	if !b.tryHandleRelayReply(&tgMessage{
		Chat: &tgChat{ID: adminID, Type: "private"}, From: &tgUser{ID: adminID},
		ReplyToMessage: &tgMessage{MessageID: adminNotif.id}, Text: "Здравствуйте, разбираемся!",
	}) {
		t.Fatal("admin reply was not handled")
	}
	userMsg := lastTo(userID)
	if !strings.Contains(userMsg.text, "Здравствуйте") {
		t.Fatalf("admin reply not relayed to user: %q", userMsg.text)
	}

	// 3) the USER replies to what they received → must come back to the admin (the thread continues).
	if !b.tryHandleRelayReply(&tgMessage{
		Chat: &tgChat{ID: userID, Type: "private"}, From: &tgUser{ID: userID},
		ReplyToMessage: &tgMessage{MessageID: userMsg.id}, Text: "Спасибо за ответ!",
	}) {
		t.Fatal("user reply was not handled")
	}
	adminMsg := lastTo(adminID)
	if !strings.Contains(adminMsg.text, "Спасибо за ответ") {
		t.Fatalf("user reply not relayed to admin: %q", adminMsg.text)
	}

	// 4) admin can reply AGAIN to the user's message → back to the user, closing the loop.
	if !b.tryHandleRelayReply(&tgMessage{
		Chat: &tgChat{ID: adminID, Type: "private"}, From: &tgUser{ID: adminID},
		ReplyToMessage: &tgMessage{MessageID: adminMsg.id}, Text: "Готово, проверьте.",
	}) {
		t.Fatal("second admin reply was not handled")
	}
	if got := lastTo(userID); !strings.Contains(got.text, "Готово") {
		t.Fatalf("second admin reply not relayed to user: %q", got.text)
	}
}

// TestRelayReplyChatScopedKey guards the composite (chat,msg) key: identical message ids in different
// chats must NOT collide (Telegram ids are per-chat).
func TestRelayReplyChatScopedKey(t *testing.T) {
	b := &Bot{}
	b.registerReplyTarget(10 /*adminChat*/, 555 /*msg*/, 100 /*peer user*/, "A", false)
	b.registerReplyTarget(100 /*userChat*/, 555 /*same msg id*/, 42 /*peer admin*/, "B", true)

	a := b.replyTargets[replyKey{chat: 10, msg: 555}]
	c := b.replyTargets[replyKey{chat: 100, msg: 555}]
	if a.peerTG != 100 || a.toAdmin {
		t.Fatalf("admin-chat entry clobbered: %+v", a)
	}
	if c.peerTG != 42 || !c.toAdmin {
		t.Fatalf("user-chat entry clobbered: %+v", c)
	}
}

// TestRelayReplyIgnoresUntracked: a reply to an unknown message is a no-op (falls through to normal
// command handling).
func TestRelayReplyIgnoresUntracked(t *testing.T) {
	b := &Bot{cfg: BotConfig{AdminID: 42}}
	if b.tryHandleRelayReply(&tgMessage{
		Chat: &tgChat{ID: 42, Type: "private"}, From: &tgUser{ID: 42},
		ReplyToMessage: &tgMessage{MessageID: 999}, Text: "hi",
	}) {
		t.Fatal("reply to an untracked message should not be handled")
	}
}
