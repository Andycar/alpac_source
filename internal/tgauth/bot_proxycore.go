package tgauth

import (
	"fmt"
	"strings"
)

// ProxyCoreManager is the interface for managing proxycore from the TG bot.
// Implemented by the httpapi server to avoid circular imports.
type ProxyCoreManager interface {
	// ProxyCoreStatus returns status of all proxy instances as formatted text.
	ProxyCoreStatus() string
	// ProxyCoreTest tests a URI and returns result text.
	ProxyCoreTest(uri string) string
	// ProxyCoreReload reloads all proxy instances.
	ProxyCoreReload() error
}

// proxyCoreManager is set via SetProxyCoreManager.
var proxyCoreManagerRef ProxyCoreManager

// SetProxyCoreManager registers the proxycore manager for bot commands.
func (b *Bot) SetProxyCoreManager(mgr ProxyCoreManager) {
	proxyCoreManagerRef = mgr
}

// handleProxyCommand handles /proxy, /proxy_test, /proxy_reload commands.
// Returns true if the command was handled.
func (b *Bot) handleProxyCommand(msg *tgMessage) bool {
	text := msg.Text

	// Only admin can use proxy commands.
	if msg.From.ID != b.cfg.AdminID {
		return false
	}

	if text == "/proxy" || text == "/proxy_status" {
		b.handleProxyStatus(msg)
		return true
	}
	if strings.HasPrefix(text, "/proxy_test") {
		b.handleProxyTest(msg)
		return true
	}
	if text == "/proxy_reload" {
		b.handleProxyReload(msg)
		return true
	}

	return false
}

func (b *Bot) handleProxyStatus(msg *tgMessage) {
	if proxyCoreManagerRef == nil {
		b.sendMsg(msg.Chat.ID, "ProxyCore не инициализирован", nil)
		return
	}
	status := proxyCoreManagerRef.ProxyCoreStatus()
	if status == "" {
		status = "Нет активных прокси"
	}
	b.sendMsg(msg.Chat.ID, status, nil)
}

func (b *Bot) handleProxyTest(msg *tgMessage) {
	if proxyCoreManagerRef == nil {
		b.sendMsg(msg.Chat.ID, "ProxyCore не инициализирован", nil)
		return
	}

	// /proxy_test <URI>
	parts := strings.SplitN(msg.Text, " ", 2)
	if len(parts) < 2 || parts[1] == "" {
		b.sendMsg(msg.Chat.ID, "Использование: /proxy_test <URI>\nПример: /proxy_test vless://uuid@server:443?...", nil)
		return
	}

	uri := strings.TrimSpace(parts[1])
	b.sendMsg(msg.Chat.ID, "⏳ Тестирование...", nil)

	result := proxyCoreManagerRef.ProxyCoreTest(uri)
	b.sendMsg(msg.Chat.ID, result, nil)
}

func (b *Bot) handleProxyReload(msg *tgMessage) {
	if proxyCoreManagerRef == nil {
		b.sendMsg(msg.Chat.ID, "ProxyCore не инициализирован", nil)
		return
	}

	err := proxyCoreManagerRef.ProxyCoreReload()
	if err != nil {
		b.sendMsg(msg.Chat.ID, fmt.Sprintf("❌ Ошибка reload: %s", err), nil)
		return
	}
	b.sendMsg(msg.Chat.ID, "✅ ProxyCore перезагружен", nil)
}
