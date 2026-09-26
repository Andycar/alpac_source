package litesrc

import "testing"

// Реальный список озвучек zetflixdb по фильму «Обсессия» (2026).
func TestVideodbCleanVoiceNamesObsession(t *testing.T) {
	in := []string{
		"(RU) DUB | Paragraph Media",
		"(RU) DUB | HDrezka Studio",
		"(RU) MVO | HDrezka Studio",
		"(RU) MVO | HDrezka Studio (18+)",
		"(RU) DVO | Кубик в Кубе | Kubik³ (18+)",
		"(RU) DVO | Кубик в Кубе | Kubik³",
		"(RU) VO | Яроцкий Михаил | Kyberpunk",
		"Original",
		"(BE) DUB | Official",
	}
	want := []string{
		"Paragraph Media",
		"DUB | HDrezka Studio", // сталкивается с MVO той же студии — код сохраняем
		"MVO | HDrezka Studio",
		"HDrezka Studio (18+)",
		"Кубик в Кубе | Kubik³ (18+)", // режем только по ПЕРВОМУ каналу
		"Кубик в Кубе | Kubik³",
		"Яроцкий Михаил | Kyberpunk",
		"Original", // без тега — не трогаем
		"Official",
	}
	got := videodbCleanVoiceNames(in)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] %q -> %q, ожидалось %q", i, in[i], got[i], want[i])
		}
	}
}

// Все имена должны остаться различимыми — иначе capi схлопнет дорожки.
func TestVideodbCleanVoiceNamesStayUnique(t *testing.T) {
	in := []string{
		"(RU) DUB | Studio", "(RU) MVO | Studio", "(RU) DVO | Studio",
	}
	got := videodbCleanVoiceNames(in)
	seen := map[string]bool{}
	for _, g := range got {
		if seen[g] {
			t.Fatalf("дубликат после очистки: %q (всё: %v)", g, got)
		}
		seen[g] = true
	}
}

func TestVideodbCleanVoiceNamesEdgeCases(t *testing.T) {
	got := videodbCleanVoiceNames([]string{"DUB | ", "", "Просто студия"})
	if got[0] != "DUB |" && got[0] != "DUB | " {
		t.Errorf("пустой остаток: %q — ожидалось сохранение исходника", got[0])
	}
	if got[2] != "Просто студия" {
		t.Errorf("имя без тега изменено: %q", got[2])
	}
}
