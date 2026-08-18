package httpapi

import (
	"fmt"
	"lampac-go/internal/adminhttp"
	"net"
	"strings"

	"lampac-go/internal/proxycore"
)

// proxyCoreManagerBridge implements tgauth.ProxyCoreManager.
// Reads the server via the deps.go accessors (liveProxyCorePool / serverReady /
// reloadProxyCore), so it works even though the Bot is configured before the
// Server is fully initialized (the accessors return nil/no-op until it is).
type proxyCoreManagerBridge struct{}

func (b *proxyCoreManagerBridge) ProxyCoreStatus() string {
	if !serverReady() {
		return "ProxyCore: сервер не запущен"
	}
	pool := liveProxyCorePool()
	if pool == nil {
		return "ProxyCore: не запущен"
	}

	statuses := pool.Status()
	if len(statuses) == 0 {
		return "ProxyCore: нет активных прокси"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🛡 ProxyCore — %d прокси\n\n", len(statuses)))

	for _, s := range statuses {
		alive := "🔴"
		if s.Alive {
			alive = "🟢"
		}
		sb.WriteString(fmt.Sprintf("%s %s [%s]\n", alive, s.Label, strings.ToUpper(s.Protocol)))
		sb.WriteString(fmt.Sprintf("   Server: %s\n", s.Server))
		sb.WriteString(fmt.Sprintf("   Latency: %dms (avg %dms)\n", s.LatencyMs, s.AvgLatMs))
		sb.WriteString(fmt.Sprintf("   Conns: %d active / %d total\n", s.ActiveConns, s.TotalConns))
		sb.WriteString(fmt.Sprintf("   Traffic: ↑%s ↓%s\n", formatBytesGo(s.BytesUp), formatBytesGo(s.BytesDown)))
		if len(s.Balancers) > 0 {
			sb.WriteString(fmt.Sprintf("   Балансеры: %s\n", strings.Join(s.Balancers, ", ")))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (b *proxyCoreManagerBridge) ProxyCoreTest(uri string) string {
	result := proxycore.TestURI(uri)
	if result.Success {
		return fmt.Sprintf("✅ %s [%s]\nServer: %s\nIP: %s\nLatency: %dms",
			result.Protocol, "OK", result.Server, result.IP, result.LatencyMs)
	}
	return fmt.Sprintf("❌ %s\nServer: %s\nIP: %s\nError: %s\nLatency: %dms",
		result.Protocol, result.Server, result.IP, result.Error, result.LatencyMs)
}

func (b *proxyCoreManagerBridge) ProxyCoreReload() error {
	if !serverReady() {
		return fmt.Errorf("server not initialized")
	}
	return reloadProxyCore()
}

// pcResolveGeo resolves GeoIP info for all instances in a pool.
func pcResolveGeo(pool *proxycore.Pool) {
	for _, inst := range pool.Instances() {
		host, _, _ := net.SplitHostPort(inst.Server())
		if host == "" {
			continue
		}
		// Resolve IP.
		ips, err := net.LookupHost(host)
		if err != nil || len(ips) == 0 {
			continue
		}
		ip := ips[0]
		country, flag := adminhttp.LookupGeoIP(ip)
		// Extract country code from flag (reverse of countryCodeToFlag).
		code := ""
		runes := []rune(flag)
		if len(runes) == 2 {
			code = string([]byte{byte(runes[0]-0x1F1E6) + 'A', byte(runes[1]-0x1F1E6) + 'A'})
		}
		inst.SetGeo(country, code, flag, ip)
	}
}

func formatBytesGo(b int64) string {
	if b == 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(b)/float64(div), []string{"KB", "MB", "GB", "TB"}[exp])
}
