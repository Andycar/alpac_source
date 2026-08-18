package httpapi

import (
	"strings"
	"testing"
)

func TestSanitizeYandexMetrikaID(t *testing.T) {
	cases := map[string]string{
		"109590048":          "109590048",
		" 109590048 ":        "109590048",
		"id=109590048":       "109590048",
		"96237757,95034372":  "9623775795034372",
		"":                   "",
		"abc":                "",
		"</script>109590048": "109590048",
	}
	for in, want := range cases {
		if got := sanitizeYandexMetrikaID(in); got != want {
			t.Errorf("sanitizeYandexMetrikaID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestYandexMetrikaSnippet(t *testing.T) {
	// Empty / invalid ID => feature disabled (no output).
	if s := yandexMetrikaSnippet(""); s != "" {
		t.Fatalf("empty id should yield no snippet, got %q", s)
	}
	if s := yandexMetrikaSnippet("notanumber"); s != "" {
		t.Fatalf("non-numeric id should yield no snippet, got %q", s)
	}

	s := yandexMetrikaSnippet("109590048")
	if s == "" {
		t.Fatal("valid id should yield a snippet")
	}
	if strings.Count(s, "109590048") < 1 {
		t.Errorf("counter id not interpolated: %q", s)
	}
	if strings.Contains(s, "__COUNTER__") {
		t.Errorf("placeholder left unsubstituted: %q", s)
	}
	// Must load the official tag and report the channel param.
	for _, want := range []string{"mc.yandex.ru/metrika/tag.js", "'init'", "channel", "window.__lampacYM"} {
		if !strings.Contains(s, want) {
			t.Errorf("snippet missing %q", want)
		}
	}
}
