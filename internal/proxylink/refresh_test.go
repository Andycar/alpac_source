package proxylink

import (
	"context"
	"testing"
)

func TestTargetRefresherRegistry(t *testing.T) {
	RegisterTargetRefresher("testsrc", func(_ context.Context, stale string) (string, bool) {
		if stale == "bad" {
			return "", false
		}
		if stale == "same" {
			return "same", true // источник вернул то же самое
		}
		return stale + "&fresh=1", true
	})

	if !HasTargetRefresher("TestSrc") {
		t.Fatal("регистрация должна быть нечувствительна к регистру")
	}
	if HasTargetRefresher("нет-такого") {
		t.Fatal("незарегистрированный плагин не должен считаться поддержанным")
	}

	if got, ok := RefreshTarget(context.Background(), "testsrc", "u"); !ok || got != "u&fresh=1" {
		t.Fatalf("переминт: got %q ok=%v", got, ok)
	}
	// Отказ источника, тот же самый адрес и пустые входные данные — всё это
	// «повторять бессмысленно», иначе прокси зациклится на одном и том же 401.
	for _, c := range []struct{ plugin, stale string }{
		{"testsrc", "bad"}, {"testsrc", "same"}, {"testsrc", ""},
		{"", "u"}, {"нет-такого", "u"},
	} {
		if _, ok := RefreshTarget(context.Background(), c.plugin, c.stale); ok {
			t.Errorf("ждали отказ для plugin=%q stale=%q", c.plugin, c.stale)
		}
	}
	// nil и пустое имя не должны ронять регистрацию.
	RegisterTargetRefresher("", nil)
	RegisterTargetRefresher("x", nil)
	if HasTargetRefresher("x") {
		t.Fatal("nil-переминт не должен регистрироваться")
	}
}
