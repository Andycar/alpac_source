package proxylink

import (
	"context"
	"strings"
	"sync"
)

// Переминт протухшей ссылки апстрима.
//
// Часть источников отдаёт не ссылку, а ОДНОРАЗОВЫЙ БИЛЕТ с коротким сроком:
// «Мир кино» (Jellyfin) выдаёт новый mediaSourceId на каждый вызов
// PlaybackInfo, и через ~60 секунд прежний перестаёт работать — сервер
// отвечает 401 (замер 16.09.2026: 307 сразу, 307 на 30-й секунде, 401 на
// 60-й). Мы же прячем этот билет внутрь /proxy-токена, который живёт 36
// часов, и отдаём зрителю. Пока плеер тянет один непрерывный поток, всё
// хорошо; но любая перемотка, пауза дольше минуты или переподключение
// сегмента бьётся о 401 — «фильм поиграл и отвалился».
//
// Здесь реестр, через который источник объясняет прокси, как выпросить
// свежий билет вместо протухшего. Прокси НЕ знает про Jellyfin, источник
// НЕ знает про /proxy — они встречаются на одной строке URL.
//
// Живёт в proxylink, потому что это единственный общий пакет: litesrc
// импортирует proxyapi, поэтому обратный импорт невозможен.

// TargetRefresher получает протухший адрес апстрима и возвращает свежий.
// ok=false — «переминтить не смог» (источник молчит, ссылка не наша, билет
// не опознан); тогда прокси отдаёт исходный ответ апстрима как есть.
//
// Реализация обязана быть потокобезопасной и быстрой: её зовут на горячем
// пути запроса зрителя, пока тот смотрит.
type TargetRefresher func(ctx context.Context, stale string) (fresh string, ok bool)

var (
	refreshMu  sync.RWMutex
	refreshers = map[string]TargetRefresher{}
)

// RegisterTargetRefresher подключает переминт для плагина. Зовётся один раз
// при инициализации источника. Пустое имя или nil игнорируются.
func RegisterTargetRefresher(plugin string, fn TargetRefresher) {
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin == "" || fn == nil {
		return
	}
	refreshMu.Lock()
	refreshers[plugin] = fn
	refreshMu.Unlock()
}

// HasTargetRefresher — есть ли у плагина переминт. Нужен прокси, чтобы не
// городить ретрай там, где он всё равно ничего не даст.
func HasTargetRefresher(plugin string) bool {
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin == "" {
		return false
	}
	refreshMu.RLock()
	_, ok := refreshers[plugin]
	refreshMu.RUnlock()
	return ok
}

// RefreshTarget просит у источника свежий адрес взамен протухшего.
// Возвращает ok=false, если переминта нет, он не справился или отдал тот же
// самый адрес (повторять запрос с ним бессмысленно — получим тот же отказ).
func RefreshTarget(ctx context.Context, plugin, stale string) (string, bool) {
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin == "" || strings.TrimSpace(stale) == "" {
		return "", false
	}
	refreshMu.RLock()
	fn := refreshers[plugin]
	refreshMu.RUnlock()
	if fn == nil {
		return "", false
	}
	fresh, ok := fn(ctx, stale)
	fresh = strings.TrimSpace(fresh)
	if !ok || fresh == "" || fresh == stale {
		return "", false
	}
	return fresh, true
}
