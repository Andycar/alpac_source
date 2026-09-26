package config

import (
	"testing"

	"github.com/rs/zerolog"
)

func TestResolveLogLevel(t *testing.T) {
	for _, c := range []struct {
		in   string
		want zerolog.Level
	}{
		{"debug", zerolog.DebugLevel},
		{"DEBUG", zerolog.DebugLevel},
		{"warn", zerolog.WarnLevel},
		{"warning", zerolog.WarnLevel},
		{"error", zerolog.ErrorLevel},
		{"info", zerolog.InfoLevel},
		{"", zerolog.InfoLevel},
		{"нечто", zerolog.InfoLevel}, // опечатка не должна глушить сервер
	} {
		if got := ResolveLogLevel(c.in); got != c.want {
			t.Errorf("ResolveLogLevel(%q) = %v, ожидалось %v", c.in, got, c.want)
		}
	}
}

// LOG_LEVEL из окружения главнее config.toml — тот же порядок, что при старте.
func TestResolveLogLevelEnvWins(t *testing.T) {
	t.Setenv("LOG_LEVEL", "error")
	if got := ResolveLogLevel("debug"); got != zerolog.ErrorLevel {
		t.Fatalf("окружение должно побеждать конфиг, получено %v", got)
	}
}

// ApplyLogLevel сообщает о факте изменения — Reload по этому флагу решает, писать ли в лог.
func TestApplyLogLevelReportsChange(t *testing.T) {
	prev := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if lvl, changed := ApplyLogLevel("debug"); !changed || lvl != zerolog.DebugLevel {
		t.Fatalf("переход info→debug: lvl=%v changed=%v", lvl, changed)
	}
	if zerolog.GlobalLevel() != zerolog.DebugLevel {
		t.Fatal("глобальный уровень не применился")
	}
	if _, changed := ApplyLogLevel("debug"); changed {
		t.Fatal("повторный вызов с тем же уровнем не должен считаться изменением")
	}
}
