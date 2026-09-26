package kit

import (
	"encoding/json"
	"sort"
	"strings"
	"sync/atomic"
)

// catalog.go — «а знал ли пользователь про этот источник, когда настраивал?»
//
// Белый список видимости (_balancerVisibility) прячет ВСЁ, чего в карте нет.
// Пока каталог источников не растёт, это ровно то, чего хочет пользователь.
// Но каждый новый балансер попадает в ту же яму: его нет в карте, значит он
// скрыт — не потому, что его выключили, а потому что его не существовало в
// день настройки. Прод 20.09.2026: 2731 карта из 3858 в режиме белого списка,
// и rudub нет у 99%, anidub у 98%, gencit у 88%.
// То есть новый источник не доходил до трёх четвертей пользователей вообще.
//
// Лечится снимком каталога: при сохранении набора рядом кладётся список
// балансеров, которые сервер знал в тот момент (_balancerCatalog). Источник,
// которого нет ни в карте, ни в снимке, — новый: пользователь его не выключал,
// и показать его честнее, чем спрятать. Свой выбор при этом не трогается:
// всё, что было в снимке и не попало в карту, остаётся скрытым.

// BalancerCatalogKey — имя служебного поля со снимком каталога.
const BalancerCatalogKey = "_balancerCatalog"

// BalancerVisibilityKey — имя карты видимости, которую пишет клиент.
const BalancerVisibilityKey = "_balancerVisibility"

var balancerCatalog atomic.Pointer[[]string]

// SetBalancerCatalog публикует текущий список известных балансеров. Зовётся из
// httpapi при старте: kit не может импортировать httpapi — вышел бы цикл.
func SetBalancerCatalog(keys []string) {
	clean := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		clean = append(clean, k)
	}
	sort.Strings(clean)
	balancerCatalog.Store(&clean)
}

// currentCatalog returns the published catalogue (nil when nothing was set —
// тогда стамп не пишется и поведение остаётся прежним).
func currentCatalog() []string {
	if p := balancerCatalog.Load(); p != nil && len(*p) > 0 {
		return *p
	}
	return nil
}

// stampCatalog adds the catalogue snapshot to a kit map that carries a
// visibility choice. Пользовательских полей не трогает.
func stampCatalog(m map[string]json.RawMessage) {
	if m == nil {
		return
	}
	if raw, ok := m[BalancerVisibilityKey]; !ok || len(raw) == 0 {
		return
	}
	cat := currentCatalog()
	if cat == nil {
		return
	}
	if enc, err := json.Marshal(cat); err == nil {
		m[BalancerCatalogKey] = enc
	}
}

// knownAtSave reports whether the balancer existed (as far as the server knew)
// when this user last saved their visibility map. Без снимка ответ «да» —
// старое поведение для карт, записанных до появления стампа.
func knownAtSave(m map[string]json.RawMessage, pluginKey string) bool {
	raw, ok := m[BalancerCatalogKey]
	if !ok || len(raw) == 0 {
		return true
	}
	var cat []string
	if err := json.Unmarshal(raw, &cat); err != nil || len(cat) == 0 {
		return true
	}
	for _, k := range cat {
		if strings.EqualFold(strings.TrimSpace(k), pluginKey) {
			return true
		}
	}
	return false
}
