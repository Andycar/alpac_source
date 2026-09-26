package kit

import (
	"context"
	"encoding/json"
	"testing"
)

func ctxWith(t *testing.T, fields map[string]any) context.Context {
	t.Helper()
	m := map[string]json.RawMessage{}
	for k, v := range fields {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		m[k] = b
	}
	return WithConfig(context.Background(), m)
}

// Белый список пользователя + снимок каталога того дня. Источник из снимка,
// которого нет в карте, пользователь ВЫКЛЮЧИЛ — прячем. Источник, которого в
// снимке нет, появился позже — он «не настроен», и решает глобальный дефолт.
func TestBalancerVisibleTreatsNewSourceAsUnconfigured(t *testing.T) {
	ctx := ctxWith(t, map[string]any{
		BalancerVisibilityKey: map[string]bool{"rezka": true, "kodik": true},
		BalancerCatalogKey:    []string{"rezka", "kodik", "filmix"},
	})
	cases := []struct {
		plugin              string
		visible, configured bool
		why                 string
	}{
		{"rezka", true, true, "включён руками"},
		{"filmix", false, true, "был в каталоге и не включён — значит выключен"},
		{"xsmart", false, false, "появился позже снимка — не настроен"},
	}
	for _, c := range cases {
		vis, conf := BalancerVisible(ctx, c.plugin)
		if vis != c.visible || conf != c.configured {
			t.Errorf("%s (%s): получили (%v,%v), ждали (%v,%v)", c.plugin, c.why, vis, conf, c.visible, c.configured)
		}
	}
}

// Карта без снимка (записана до появления стампа) работает как раньше:
// белый список прячет всё, чего в ней нет.
func TestBalancerVisibleWithoutCatalogKeepsOldBehaviour(t *testing.T) {
	ctx := ctxWith(t, map[string]any{
		BalancerVisibilityKey: map[string]bool{"rezka": true},
	})
	if vis, conf := BalancerVisible(ctx, "xsmart"); vis || !conf {
		t.Fatalf("без снимка ожидали (false,true), получили (%v,%v)", vis, conf)
	}
}

// Чёрный список (одни false) снимок не меняет: отсутствующий источник и так
// решается глобальным дефолтом.
func TestBalancerVisibleBlacklistUnaffected(t *testing.T) {
	ctx := ctxWith(t, map[string]any{
		BalancerVisibilityKey: map[string]bool{"mirage": false},
		BalancerCatalogKey:    []string{"mirage", "rezka"},
	})
	if vis, conf := BalancerVisible(ctx, "rezka"); vis || conf {
		t.Fatalf("чёрный список: ожидали (false,false), получили (%v,%v)", vis, conf)
	}
	if vis, conf := BalancerVisible(ctx, "mirage"); vis || !conf {
		t.Fatalf("явное выключение обязано сохраниться: (%v,%v)", vis, conf)
	}
}

// Сохранение набора кладёт рядом снимок каталога — и только когда выбор есть.
func TestSaveStampsCatalogOnlyWithVisibility(t *testing.T) {
	SetBalancerCatalog([]string{"Rezka", "rezka", " Kodik ", ""})
	t.Cleanup(func() { balancerCatalog.Store(nil) })

	m := map[string]json.RawMessage{BalancerVisibilityKey: json.RawMessage(`{"rezka":true}`)}
	stampCatalog(m)
	var cat []string
	if err := json.Unmarshal(m[BalancerCatalogKey], &cat); err != nil {
		t.Fatalf("снимок не записан: %v", err)
	}
	if len(cat) != 2 || cat[0] != "kodik" || cat[1] != "rezka" {
		t.Fatalf("снимок должен быть нормализован и без дублей: %v", cat)
	}

	// Настроек видимости нет — и снимка быть не должно: он бы соврал, что
	// пользователь уже видел весь каталог.
	empty := map[string]json.RawMessage{"Filmix": json.RawMessage(`{"enable":true}`)}
	stampCatalog(empty)
	if _, ok := empty[BalancerCatalogKey]; ok {
		t.Fatal("снимок записан в карту без выбора видимости")
	}
}
