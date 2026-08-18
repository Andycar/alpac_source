package tgauth

import "testing"

// TestFeedbackContextCombines: строка контекста — то, что видит отвечающий первой
// строкой тикета, и она обязана читаться без расшифровки.
func TestFeedbackContextCombines(t *testing.T) {
	got := feedbackContext(LangRU, &feedbackState{Client: "app", Platform: "samsung"})
	if got == "" {
		t.Fatal("контекст пуст при заполненных клиенте и платформе")
	}
	if got != T(LangRU).CliApp+" · "+T(LangRU).PlatSamsung {
		t.Fatalf("неожиданная строка: %q", got)
	}
}

// TestFeedbackContextWithoutPlatform: у плагина и MSX платформы не спрашиваем —
// строка не должна оканчиваться висящим разделителем.
func TestFeedbackContextWithoutPlatform(t *testing.T) {
	got := feedbackContext(LangRU, &feedbackState{Client: "lampa"})
	if got != T(LangRU).CliLampa {
		t.Fatalf("лишний разделитель или платформа: %q", got)
	}
}

// TestFeedbackContextEmptyForThanks: благодарности эти вопросы не задаются вовсе,
// и в тикет не должна попадать пустая строка с иконкой.
func TestFeedbackContextEmptyForThanks(t *testing.T) {
	if got := feedbackContext(LangRU, &feedbackState{Category: "thanks"}); got != "" {
		t.Fatalf("контекст должен быть пустым: %q", got)
	}
}

// TestPlatformLabelsExistInEveryLanguage: пропущенный перевод превращается в пустую
// строку, и контекст молча теряет платформу.
func TestPlatformLabelsExistInEveryLanguage(t *testing.T) {
	plats := []string{"androidtv", "phone", "samsung", "lg", "hisense", "appletv", "browser", "other"}
	clients := []string{"app", "lampa", "msx", "other"}
	for _, lang := range []Lang{LangRU, LangUK, LangEN} {
		for _, p := range plats {
			if FbPlatformLabel(lang, p) == "" {
				t.Errorf("нет подписи платформы %q для языка %v", p, lang)
			}
		}
		for _, c := range clients {
			if FbClientLabel(lang, c) == "" {
				t.Errorf("нет подписи клиента %q для языка %v", c, lang)
			}
		}
	}
}

// TestUnknownPlatformIsSilent: незнакомый идентификатор не должен подставлять
// «Другое» — иначе в тикете появится платформа, которую никто не выбирал.
func TestUnknownPlatformIsSilent(t *testing.T) {
	if got := FbPlatformLabel(LangRU, "нет-такой"); got != "" {
		t.Fatalf("выдумал платформу: %q", got)
	}
}
