package httpapi

import (
	"regexp"
	"testing"
)

func mkRule(raw string, limit int) wafCompiledLimitRule {
	return wafCompiledLimitRule{pattern: regexp.MustCompile(raw), raw: raw, limit: limit}
}

// Catch-all уходит в конец, специфичные правила остаются главными.
// Иначе ".*" затеняет всё, и /proxy/ вместо своих 50 получает общие 10.
func TestSortWAFLimitRulesCatchAllLast(t *testing.T) {
	rules := []wafCompiledLimitRule{
		mkRule(`.*`, 10),
		mkRule(`^/(proxy|tmdb|cub)`, 50),
		mkRule(`^/lite/`, 10),
		mkRule(`^/capi/`, 50),
	}
	sortWAFLimitRules(rules)

	if got := rules[len(rules)-1].raw; got != `.*` {
		t.Fatalf("catch-all должен быть последним, а последний — %q", got)
	}
	// Первое совпадение для /proxy/x обязано быть специфичным правилом.
	for _, r := range rules {
		if r.pattern.MatchString("/proxy/x") {
			if r.limit != 50 {
				t.Fatalf("/proxy/x поймало правило %q с лимитом %d, ожидалось 50", r.raw, r.limit)
			}
			break
		}
	}
}

// Порядок не должен зависеть от того, как правила пришли из map.
func TestSortWAFLimitRulesDeterministic(t *testing.T) {
	order := func(in []string) []string {
		rules := make([]wafCompiledLimitRule, 0, len(in))
		for _, p := range in {
			rules = append(rules, mkRule(p, 10))
		}
		sortWAFLimitRules(rules)
		out := make([]string, 0, len(rules))
		for _, r := range rules {
			out = append(out, r.raw)
		}
		return out
	}
	a := order([]string{`.*`, `^/lite/`, `^/capi/`})
	b := order([]string{`^/capi/`, `.*`, `^/lite/`})
	if len(a) != len(b) {
		t.Fatal("разная длина")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("порядок зависит от входа: %v против %v", a, b)
		}
	}
}

// ".+" — тоже catch-all, хотя пустую строку не матчит.
func TestSortWAFLimitRulesRecognisesPlusCatchAll(t *testing.T) {
	rules := []wafCompiledLimitRule{mkRule(`.+`, 10), mkRule(`^/capi/`, 50)}
	sortWAFLimitRules(rules)
	if rules[0].raw != `^/capi/` {
		t.Fatalf("специфичное правило должно быть первым, получено %q", rules[0].raw)
	}
}
