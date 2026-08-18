package litesrc

import (
	"testing"
	"time"
)

// Инцидент 2026-08-06: 26 полных логинов за сутки при лимите в 5 устройств. Каждая новая сессия
// вытесняет прежнюю, и ссылки у смотрящих превращаются в 403 посреди фильма.
func TestLoginIntervalProtectsDeviceSlots(t *testing.T) {
	if fxLoginMinInterval < time.Hour {
		t.Fatalf("интервал между логинами слишком мал: %v", fxLoginMinInterval)
	}
	// Одиночного отказа мало, чтобы объявить сессию мёртвой и пойти на логин.
	if fxAliveFailsToDie < 2 {
		t.Fatalf("сессию нельзя хоронить с первой неудачи: %d", fxAliveFailsToDie)
	}
}

// Живая сессия не должна перевыпускаться, каким бы старым ни был hash.
func TestAliveSessionKeepsHash(t *testing.T) {
	f := &filmixChecker{fxUser: "u", fxPasswd: "p"}
	f.fxStateLoaded = true
	f.fxHash = "h"
	f.fxHashAt = time.Now().Add(-30 * 24 * time.Hour)
	if got := f.fxEnsureHash(t.Context()); got != "h" {
		t.Fatalf("живой hash обязан переиспользоваться, получено %q", got)
	}
	if f.fxAliveFails != 0 {
		t.Fatalf("счётчик неудач не должен расти без проверок: %d", f.fxAliveFails)
	}
}
