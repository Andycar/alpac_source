package tgauth

import (
	"strings"
	"testing"
	"time"
)

// fixedTime gives every test a deterministic "now" so the % bars and
// day counts don't drift with the wall clock.
var fixedNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// TestRenderProfileMessage_RUStandardOnly is the simplest happy path —
// no premium, no integrations. Used to lock the base layout.
func TestRenderProfileMessage_RUStandardOnly(t *testing.T) {
	vm := profileViewModel{
		FirstName:    "Иван",
		Username:     "ivan_test",
		TelegramID:   201955082,
		CreatedAt:    fixedNow.AddDate(-1, 0, 0), // 365 days ago
		ExpiresAt:    fixedNow.AddDate(0, 0, 30), // 30 days from now
		PremiumUntil: time.Time{},                // never premium
		DeviceCount:  2,
		MaxDevices:   5,
		DeviceLabels: []string{"Android Phone", "Web Chrome"},
		Integrations: nil,
		Now:          fixedNow,
	}
	got := renderProfileMessage(vm, T("ru"))

	// Hero block.
	mustContain(t, got, "✨  <b>Иван</b>")
	mustContain(t, got, "<i>@ivan_test</i>")
	mustContain(t, got, "🟢  <b>Активен</b>")
	// No premium pill when never premium.
	mustNotContain(t, got, "⭐")

	// Base block.
	mustContain(t, got, "▌  📅  <b>Стандарт</b>")
	mustContain(t, got, "до <b>01.07.2026</b>")
	mustContain(t, got, "ещё <b>30</b> дней")
	// Bar should be in <code> for monospace alignment.
	mustContain(t, got, "<code>▰") // at least one filled cell

	// Devices.
	mustContain(t, got, "📱  <b>Устройства</b>   <b>2</b> / 5")
	// Icon row: 🤖 + 💻 + 3 empty slots.
	mustContain(t, got, "🤖 💻 ◌ ◌ ◌")

	// No integrations block.
	mustNotContain(t, got, "🔗")

	// Footer.
	mustContain(t, got, "<i>TG: <code>201955082</code>")
	mustContain(t, got, "с 01.06.2025") // CreatedAt year-1
}

// TestRenderProfileMessage_RUWithActivePremium is the full premium UX —
// hero has the ⭐ pill, premium block renders, group integration shows.
func TestRenderProfileMessage_RUWithActivePremium(t *testing.T) {
	vm := profileViewModel{
		FirstName:    "Кирилл",
		Username:     "kirill",
		TelegramID:   42,
		CreatedAt:    fixedNow.AddDate(0, -6, 0),
		ExpiresAt:    fixedNow.AddDate(0, 6, 0), // 6 months out
		PremiumUntil: fixedNow.AddDate(0, 3, 0), // 3 months overlay
		DeviceCount:  3,
		MaxDevices:   5,
		DeviceLabels: []string{"iPhone 15", "Samsung Smart TV", "Android Phone"},
		Integrations: []string{
			"🏷   Премиум",
			"🎬   YouTube — <b>My Channel</b>",
			"🔵   Алиса",
			"📅   <b>12</b> сериалов  ·  уведомления вкл",
		},
		Now: fixedNow,
	}
	got := renderProfileMessage(vm, T("ru"))

	// Premium pill in the hero.
	mustContain(t, got, "🟢  <b>Активен</b>")
	mustContain(t, got, "⭐ <b>Премиум</b>")
	mustContain(t, got, "до <b>01.09</b>") // 3 months from 01.06 = 01.09

	// Premium overlay block must follow the base block.
	mustContain(t, got, "▌  ⭐  <b>Премиум-доступ</b>")
	mustContain(t, got, "до <b>01.09.2026</b>")

	// Device icon row mixes 🍎 / 📺 / 🤖.
	mustContain(t, got, "🍎 📺 🤖 ◌ ◌")

	// Integrations.
	mustContain(t, got, "🔗  <b>Интеграции</b>")
	mustContain(t, got, "▌   🏷   Премиум")
	mustContain(t, got, "▌   🎬   YouTube — <b>My Channel</b>")
	mustContain(t, got, "▌   🔵   Алиса")
	mustContain(t, got, "▌   📅   <b>12</b> сериалов  ·  уведомления вкл")

	// Ordering: base block before premium block before devices.
	baseIdx := strings.Index(got, "Стандарт")
	premIdx := strings.Index(got, "Премиум-доступ")
	devIdx := strings.Index(got, "Устройства")
	intIdx := strings.Index(got, "Интеграции")
	if !(baseIdx < premIdx && premIdx < devIdx && devIdx < intIdx) {
		t.Errorf("section order broken: base=%d prem=%d dev=%d int=%d",
			baseIdx, premIdx, devIdx, intIdx)
	}
}

// TestRenderProfileMessage_RUExpired shows the strike-through state for
// a subscription that has run out — both base and premium.
func TestRenderProfileMessage_RUExpired(t *testing.T) {
	vm := profileViewModel{
		FirstName:    "Test",
		Username:     "",
		TelegramID:   1,
		CreatedAt:    fixedNow.AddDate(-2, 0, 0),
		ExpiresAt:    fixedNow.AddDate(0, 0, -30), // expired 30 days ago
		PremiumUntil: fixedNow.AddDate(0, 0, -60), // premium expired 60 days ago
		DeviceCount:  0,
		MaxDevices:   3,
		Now:          fixedNow,
	}
	got := renderProfileMessage(vm, T("ru"))

	mustContain(t, got, "🔴  <b>Истёк</b>")
	mustNotContain(t, got, "⭐ <b>Премиум</b>") // pill only when premium active
	// Strikethrough expiry dates.
	mustContain(t, got, "<s>истёк 02.05.2026</s>") // base, 30 days before fixedNow
	mustContain(t, got, "<s>истёк 02.04.2026</s>") // premium overlay
	// Devices row: 0 / 3, all empty.
	mustContain(t, got, "📱  <b>Устройства</b>   <b>0</b> / 3")
	mustContain(t, got, "◌ ◌ ◌")
}

// TestRenderProfileMessage_EN — make sure English uses single-form
// plurals and English labels.
func TestRenderProfileMessage_EN(t *testing.T) {
	vm := profileViewModel{
		FirstName:    "John",
		Username:     "john",
		TelegramID:   7,
		CreatedAt:    fixedNow.AddDate(0, -1, 0),
		ExpiresAt:    fixedNow.AddDate(0, 0, 1), // 1 day left
		PremiumUntil: time.Time{},
		DeviceCount:  1,
		MaxDevices:   2,
		DeviceLabels: []string{"Chrome Browser"},
		Now:          fixedNow,
	}
	got := renderProfileMessage(vm, T("en"))

	mustContain(t, got, "<b>John</b>")
	mustContain(t, got, "🟢  <b>Active</b>")
	mustContain(t, got, "<b>Standard</b>")
	mustContain(t, got, "until <b>02.06.2026</b>")
	// "still 1 day" — singular form picked.
	mustContain(t, got, "still <b>1</b> day")
	mustNotContain(t, got, "still <b>1</b> days")
}

// TestRenderProfileMessage_OverflowDevices — many devices, no cap shown:
// row is truncated to 8 icons + "+N" suffix so the message stays compact.
func TestRenderProfileMessage_OverflowDevices(t *testing.T) {
	labels := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		labels = append(labels, "Android Phone")
	}
	vm := profileViewModel{
		FirstName:    "Bulk",
		TelegramID:   100,
		CreatedAt:    fixedNow.AddDate(0, -3, 0),
		ExpiresAt:    fixedNow.AddDate(0, 1, 0),
		DeviceCount:  12,
		MaxDevices:   0, // unknown / "premium tier" with very high cap
		DeviceLabels: labels,
		Now:          fixedNow,
	}
	got := renderProfileMessage(vm, T("ru"))
	mustContain(t, got, "<b>12</b>")
	mustContain(t, got, "<i>+4</i>") // 12 - 8 shown = 4 hidden
}

// TestRenderProfileMessage_VisualPreview prints the full message bodies
// to test output so a maintainer can sanity-check the visual feel after
// changing the layout. NOT an assertion — purely informational.
func TestRenderProfileMessage_VisualPreview(t *testing.T) {
	if testing.Short() {
		t.Skip("visual preview suppressed in -short mode")
	}
	scenarios := []struct {
		name string
		vm   profileViewModel
		lang Lang
	}{
		{
			name: "RU · standard only · half-spent · 2 devices",
			vm: profileViewModel{
				FirstName:    "Иван",
				Username:     "ivan_test",
				TelegramID:   201955082,
				CreatedAt:    fixedNow.AddDate(0, -2, 0),
				ExpiresAt:    fixedNow.AddDate(0, 2, 0),
				DeviceCount:  2,
				MaxDevices:   5,
				DeviceLabels: []string{"Android Phone", "Web Chrome"},
				Now:          fixedNow,
			},
			lang: "ru",
		},
		{
			name: "RU · premium · all integrations",
			vm: profileViewModel{
				FirstName:    "Кирилл",
				Username:     "kirill",
				TelegramID:   42,
				CreatedAt:    fixedNow.AddDate(0, -6, 0),
				ExpiresAt:    fixedNow.AddDate(0, 9, 0),
				PremiumUntil: fixedNow.AddDate(0, 3, 0),
				DeviceCount:  3,
				MaxDevices:   5,
				DeviceLabels: []string{"iPhone 15", "Samsung Smart TV", "Android Phone"},
				Integrations: []string{
					"🏷   Премиум",
					"🎬   YouTube — <b>My Channel</b>",
					"🔵   Алиса",
					"📅   <b>12</b> сериалов  ·  уведомления вкл",
				},
				Now: fixedNow,
			},
			lang: "ru",
		},
		{
			name: "RU · expired · had premium",
			vm: profileViewModel{
				FirstName:    "Olga",
				Username:     "olga",
				TelegramID:   333,
				CreatedAt:    fixedNow.AddDate(-1, 0, 0),
				ExpiresAt:    fixedNow.AddDate(0, 0, -10),
				PremiumUntil: fixedNow.AddDate(0, 0, -30),
				DeviceCount:  1,
				MaxDevices:   3,
				DeviceLabels: []string{"iPad Air"},
				Now:          fixedNow,
			},
			lang: "ru",
		},
		{
			name: "EN · single day left",
			vm: profileViewModel{
				FirstName:    "John",
				Username:     "john",
				TelegramID:   7,
				CreatedAt:    fixedNow.AddDate(0, -1, 0),
				ExpiresAt:    fixedNow.AddDate(0, 0, 1),
				DeviceCount:  1,
				MaxDevices:   2,
				DeviceLabels: []string{"Chrome Browser"},
				Now:          fixedNow,
			},
			lang: "en",
		},
	}
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			out := renderProfileMessage(s.vm, T(s.lang))
			t.Logf("\n──────── %s ────────\n%s\n────────────────────────",
				s.name, out)
		})
	}
}

// mustContain / mustNotContain — tiny substring assert helpers.
func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("missing substring %q\nin output:\n%s", needle, haystack)
	}
}
func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("unexpected substring %q\nin output:\n%s", needle, haystack)
	}
}
