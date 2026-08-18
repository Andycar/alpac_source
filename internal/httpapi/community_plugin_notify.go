package httpapi

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Marketplace v2 — admin TG notifications.
//
// Hooks into the existing community plugin install/update flow to surface
// changes to admins via Bot.NotifyAdmins (added in Phase 1 telemetry work).
// All functions are best-effort: they no-op when the bot or admin store
// isn't available, and never block the calling install/update path.
// ---------------------------------------------------------------------------

// adminNotifierForPlugins returns a notifier or nil if not wired.
// We reuse the same Bot.NotifyAdmins API that telemetry alerts use.
func adminNotifierForPlugins() interface{ NotifyAdmins(text string) } {
	bot := liveTGBot()
	if bot == nil {
		return nil
	}
	return bot
}

func summarizeFindings(val PluginValidationResult) string {
	if len(val.Findings) == 0 {
		return ""
	}
	tags := make([]string, 0, len(val.Findings))
	for _, f := range val.Findings {
		switch f.Severity {
		case "fail":
			tags = append(tags, "❌ "+f.Code)
		case "warn":
			tags = append(tags, "⚠ "+f.Code)
		case "info":
			tags = append(tags, "ℹ "+f.Code)
		}
	}
	return strings.Join(tags, ", ")
}

func notifyAdminsPluginInstalled(entry CatalogEntry, val PluginValidationResult) {
	n := adminNotifierForPlugins()
	if n == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📦 <b>Плагин установлен</b>: <code>%s</code> v%s\n", entry.Name, entry.Version)
	if entry.Author != "" {
		fmt.Fprintf(&b, "Автор: %s\n", entry.Author)
	}
	if entry.Homepage != "" {
		fmt.Fprintf(&b, "Источник: %s\n", entry.Homepage)
	}
	fmt.Fprintf(&b, "Размер: %d байт · SHA256: <code>%s…</code>", val.SizeBytes, val.SHA256[:16])
	if s := summarizeFindings(val); s != "" {
		fmt.Fprintf(&b, "\nПроверка: %s", s)
	}
	n.NotifyAdmins(b.String())
}

func notifyAdminsPluginUpdated(entry CatalogEntry, prevVersion string, val PluginValidationResult) {
	n := adminNotifierForPlugins()
	if n == nil {
		return
	}
	var b strings.Builder
	if prevVersion != "" && prevVersion != entry.Version {
		fmt.Fprintf(&b, "♻ <b>Плагин обновлён</b>: <code>%s</code> %s → <b>%s</b>\n", entry.Name, prevVersion, entry.Version)
	} else {
		fmt.Fprintf(&b, "♻ <b>Плагин переустановлен</b>: <code>%s</code> v%s\n", entry.Name, entry.Version)
	}
	fmt.Fprintf(&b, "SHA256: <code>%s…</code>", val.SHA256[:16])
	if s := summarizeFindings(val); s != "" {
		fmt.Fprintf(&b, "\nПроверка: %s", s)
	}
	n.NotifyAdmins(b.String())
}

func notifyAdminsPluginUpdateBlocked(entry CatalogEntry, val PluginValidationResult) {
	n := adminNotifierForPlugins()
	if n == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🛑 <b>Авто-обновление плагина заблокировано</b>: <code>%s</code> v%s\n", entry.Name, entry.Version)
	fmt.Fprintf(&b, "Валидатор обнаружил проблемы:\n")
	count := 0
	for _, f := range val.Findings {
		if f.Severity != "fail" {
			continue
		}
		fmt.Fprintf(&b, "• <b>%s</b>: %s\n", f.Code, f.Message)
		count++
		if count >= 3 {
			break
		}
	}
	fmt.Fprintf(&b, "\nЕсли это ложное срабатывание — установите вручную через админку с force=1.")
	n.NotifyAdmins(b.String())
}
