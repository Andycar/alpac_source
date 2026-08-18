package tgauth

import (
	"strings"
	"testing"
	"time"
)

// The reported "даты подписок не сходятся": a deadline stored at 23:00 UTC is
// already the next calendar day for the user, and the bot printed the UTC day.
func TestFmtDateUsesDisplayZoneNotUTC(t *testing.T) {
	// 29 Sep 23:00 UTC = 30 Sep 02:00 in Moscow.
	deadline := time.Date(2026, 9, 29, 23, 0, 0, 0, time.UTC)

	if got := fmtDate(deadline); got != "30.09.2026" {
		t.Errorf("fmtDate = %q, want 30.09.2026 (the day the user is living in)", got)
	}
	if got := fmtDateShort(deadline); got != "30.09" {
		t.Errorf("fmtDateShort = %q", got)
	}
	if got := fmtDateTime(deadline); got != "30.09 02:00" {
		t.Errorf("fmtDateTime = %q", got)
	}
}

func TestFmtDateZeroTime(t *testing.T) {
	// A never-set deadline must not render as "01.01.0001".
	for name, got := range map[string]string{
		"date":  fmtDate(time.Time{}),
		"short": fmtDateShort(time.Time{}),
		"time":  fmtDateTime(time.Time{}),
	} {
		if got != "—" {
			t.Errorf("%s of zero time = %q", name, got)
		}
	}
}

// The profile card and the payment webhook must agree — they are two messages
// about the same date, and disagreeing is what people report.
func TestExportedFormatDateMatchesInternal(t *testing.T) {
	when := time.Date(2026, 1, 31, 22, 30, 0, 0, time.UTC)
	if FormatDate(when) != fmtDate(when) {
		t.Errorf("FormatDate=%q fmtDate=%q", FormatDate(when), fmtDate(when))
	}
	if FormatDate(when) != "01.02.2026" {
		t.Errorf("FormatDate = %q, want 01.02.2026", FormatDate(when))
	}
}

// "продлена на 1 дней" read as a bug; the profile card already had the forms.
func TestPluralDaysRU(t *testing.T) {
	cases := map[int]string{1: "день", 2: "дня", 3: "дня", 4: "дня", 5: "дней",
		11: "дней", 14: "дней", 21: "день", 22: "дня", 30: "дней", 101: "день"}
	for n, want := range cases {
		if got := PluralDaysRU(n); got != want {
			t.Errorf("PluralDaysRU(%d) = %q, want %q", n, got, want)
		}
	}
}

// One entrance: «Обратная связь» opens the section, tickets live inside it.
// A second top-level button would be a second door to the same room.
func TestMainMenuHasSingleSupportEntrance(t *testing.T) {
	b := &Bot{}
	if menuHas(b.mainMenuKeyboard(LangRU), messagesRU.BtnFeedback) {
		t.Error("support button shown while feedback is disabled")
	}

	b.feedbackSub = stubFeedback{}
	kb := b.mainMenuKeyboard(LangRU)
	if !menuHas(kb, messagesRU.BtnFeedback) {
		t.Error("«Обратная связь» missing from the main keyboard")
	}
	if menuHas(kb, messagesRU.BtnTickets) {
		t.Error("«Мои обращения» should live inside the section, not in the main menu")
	}
}

// stubFeedback satisfies FeedbackSubmitter; the keyboard only checks for nil.
type stubFeedback struct{ tickets []FeedbackTicketInfo }

func (stubFeedback) CreateTicket(_, _ string, _, _, _, _ string) (string, error) { return "", nil }
func (f stubFeedback) UserTickets(string) []FeedbackTicketInfo                   { return f.tickets }
func (stubFeedback) GetTicket(string) (FeedbackTicketDetail, bool) {
	return FeedbackTicketDetail{}, false
}
func (stubFeedback) AddUserReply(_, _, _, _ string) error { return nil }

func menuHas(kb *tgReplyKeyboardMarkup, label string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.Keyboard {
		for _, btn := range row {
			if strings.EqualFold(btn.Text, label) {
				return true
			}
		}
	}
	return false
}

// Tickets store RFC3339; the bot used to print the raw string (and a truncated
// one at that), so the same date looked different depending on the screen.
func TestISOTicketDatesMatchTheRestOfTheBot(t *testing.T) {
	iso := "2026-09-29T23:15:00Z" // 30 Sep 02:15 Moscow

	if got := fmtISODate(iso); got != "30.09.2026" {
		t.Errorf("fmtISODate = %q", got)
	}
	if got := fmtISODateTime(iso); got != "30.09 02:15" {
		t.Errorf("fmtISODateTime = %q", got)
	}
	// Same instant, same day, whichever screen shows it.
	if fmtISODate(iso) != fmtDate(time.Date(2026, 9, 29, 23, 15, 0, 0, time.UTC)) {
		t.Error("ticket list and profile card disagree about the day")
	}
}

func TestISODatesSurviveGarbage(t *testing.T) {
	// An unparseable timestamp must be shown as-is, not swallowed: a ticket with
	// a broken date is still a ticket the user needs to open.
	for _, in := range []string{"", "не дата", "2026-13-45T99:99:99Z"} {
		if got := fmtISODate(in); got != in {
			t.Errorf("fmtISODate(%q) = %q, want it unchanged", in, got)
		}
	}
}

// The section must show what you already wrote — that is the whole point of the
// rework: the wizard used to start immediately and the tickets had no entrance.
func TestFeedbackHubOffersTicketsWhenThereAreAny(t *testing.T) {
	b := &Bot{feedbackSub: stubFeedback{}}

	text, kb := b.feedbackHub(777, LangRU)
	if kbHasCallback(kb, "fbmine") {
		t.Error("«Мои обращения» offered with no tickets")
	}
	if !kbHasCallback(kb, "fbnew") || text == "" {
		t.Errorf("hub must always offer a new ticket: %q %+v", text, kb)
	}

	b.feedbackSub = stubFeedback{tickets: []FeedbackTicketInfo{
		{ID: "t1", Subject: "Не играет", Status: "open", Category: "bug",
			CreatedAt: "2026-09-29T23:15:00Z", Replies: 2, LastReplyAdmin: true},
		{ID: "t2", Subject: "Спасибо", Status: "closed", Category: "thanks", CreatedAt: "2026-09-01T10:00:00Z"},
	}}
	text, kb = b.feedbackHub(777, LangRU)
	if !kbHasCallback(kb, "fbmine") {
		t.Fatal("«Мои обращения» missing with two tickets")
	}
	if !strings.Contains(text, "2") {
		t.Errorf("hub should state how many tickets there are: %q", text)
	}
	// One thread is answered and unread; the closed one is not news.
	if !strings.Contains(text, "💬 1") {
		t.Errorf("answered-thread badge missing from the hub: %q", text)
	}
}

// A list you can only leave by typing a command is a dead end on a phone.
func TestTicketsViewHasBackToHub(t *testing.T) {
	b := &Bot{feedbackSub: stubFeedback{tickets: []FeedbackTicketInfo{
		{ID: "t1", Subject: "Не играет", Status: "open", Category: "bug", CreatedAt: "2026-09-29T23:15:00Z"},
	}}}
	text, kb := b.ticketsView(777, LangRU)
	if kb == nil {
		t.Fatal("expected a list")
	}
	if !kbHasCallback(kb, "fbhub") {
		t.Error("no way back into the section")
	}
	// And the date is the bot-wide one, not the raw ISO string.
	if !strings.Contains(text, "30.09.2026") {
		t.Errorf("ticket date not normalised: %q", text)
	}

	empty := &Bot{feedbackSub: stubFeedback{}}
	if _, kb := empty.ticketsView(777, LangRU); kb != nil {
		t.Error("empty list must report itself as empty (nil keyboard)")
	}
}

func kbHasCallback(kb *tgInlineKeyboardMarkup, data string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == data {
				return true
			}
		}
	}
	return false
}

// The author asks one question: did anybody answer me. The admin taxonomy
// ("🟡 В работе") never answered it.
func TestFbUserStatus(t *testing.T) {
	cases := []struct {
		status    string
		lastAdmin bool
		want      string
	}{
		{"open", false, messagesRU.StatusUserWaiting},
		{"in_progress", false, messagesRU.StatusUserWaiting}, // "в работе" is still waiting, for the user
		{"open", true, messagesRU.StatusUserAnswered},
		{"in_progress", true, messagesRU.StatusUserAnswered},
		{"resolved", true, messagesRU.StatusUserClosed},
		{"closed", false, messagesRU.StatusUserClosed}, // closed stays closed even mid-thread
	}
	for _, c := range cases {
		if got := FbUserStatus(LangRU, c.status, c.lastAdmin); got != c.want {
			t.Errorf("FbUserStatus(%q, lastAdmin=%v) = %q, want %q", c.status, c.lastAdmin, got, c.want)
		}
	}
}

// Ordering: what needs reading, then what is still open, then what is over.
func TestTicketOrdering(t *testing.T) {
	list := []FeedbackTicketInfo{
		{ID: "closed-new", Status: "closed", UpdatedAt: "2026-09-30T10:00:00Z"},
		{ID: "waiting-old", Status: "open", UpdatedAt: "2026-09-01T10:00:00Z"},
		{ID: "answered-old", Status: "open", LastReplyAdmin: true, UpdatedAt: "2026-09-02T10:00:00Z"},
		{ID: "waiting-new", Status: "in_progress", UpdatedAt: "2026-09-29T10:00:00Z"},
		{ID: "answered-new", Status: "in_progress", LastReplyAdmin: true, UpdatedAt: "2026-09-28T10:00:00Z"},
	}
	sortUserTickets(list)

	got := make([]string, len(list))
	for i, t := range list {
		got[i] = t.ID
	}
	want := []string{"answered-new", "answered-old", "waiting-new", "waiting-old", "closed-new"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestTicketActivityFallsBackToCreation(t *testing.T) {
	// A ticket nobody has touched has no UpdatedAt; it must still sort by date
	// rather than sink to the bottom on an empty string.
	fresh := FeedbackTicketInfo{CreatedAt: "2026-09-30T10:00:00Z"}
	old := FeedbackTicketInfo{CreatedAt: "2026-09-01T10:00:00Z"}
	list := []FeedbackTicketInfo{old, fresh}
	sortUserTickets(list)
	if list[0].CreatedAt != fresh.CreatedAt {
		t.Errorf("untouched tickets not ordered by creation: %+v", list)
	}
}

// The hub badge must count threads waiting to be READ, not every message in
// them — including the user's own.
func TestHubCountsAnsweredNotAllReplies(t *testing.T) {
	b := &Bot{feedbackSub: stubFeedback{tickets: []FeedbackTicketInfo{
		{ID: "a", Status: "open", Replies: 4, LastReplyAdmin: false}, // user wrote last: nothing to read
		{ID: "b", Status: "open", Replies: 1, LastReplyAdmin: true},  // one answer waiting
		{ID: "c", Status: "closed", Replies: 3, LastReplyAdmin: true},
	}}}
	text, _ := b.feedbackHub(1, LangRU)
	if !strings.Contains(text, "💬 1") {
		t.Errorf("hub badge = %q, want one answered thread", text)
	}
}
