// Package uri parses proxy URI strings into sidecar.ProxyOutbound structs.
package uri

import (
	"fmt"
	"strings"

	"lampac-go/internal/sidecar"
)

// Parse parses a proxy URI and returns the outbound config and the default engine type.
// Supported schemes: vless://, vmess://, trojan://, ss://, hysteria2://, hy2://,
// tuic://, wg://, socks5://, socks://, http://, https://
//
// socks5/http return EngineProxyCore — these are handled inline by the
// proxycore dialers (no sidecar binary needed).
func Parse(rawURI string) (sidecar.ProxyOutbound, sidecar.EngineType, error) {
	before, _, ok := strings.Cut(rawURI, "://")
	if !ok {
		return sidecar.ProxyOutbound{}, "", fmt.Errorf("missing scheme in URI: %q", rawURI)
	}
	scheme := strings.ToLower(before)

	switch scheme {
	case "vless":
		out, err := ParseVLESS(rawURI)
		return out, sidecar.EngineXray, err
	case "vmess":
		out, err := ParseVMess(rawURI)
		return out, sidecar.EngineXray, err
	case "trojan":
		out, err := ParseTrojan(rawURI)
		return out, sidecar.EngineXray, err
	case "ss":
		out, err := ParseShadowsocks(rawURI)
		return out, sidecar.EngineMihomo, err
	case "hysteria2", "hy2":
		out, err := ParseHysteria2(rawURI)
		return out, sidecar.EngineMihomo, err
	case "tuic":
		out, err := ParseTUIC(rawURI)
		return out, sidecar.EngineMihomo, err
	case "wg", "wireguard":
		out, err := ParseWireGuard(rawURI)
		return out, sidecar.EngineMihomo, err
	case "socks5", "socks":
		out, err := ParseSOCKS5(rawURI)
		return out, sidecar.EngineProxyCore, err
	case "http", "https":
		out, err := ParseHTTPProxy(rawURI)
		return out, sidecar.EngineProxyCore, err
	default:
		return sidecar.ProxyOutbound{}, "", fmt.Errorf("unsupported proxy scheme: %s", scheme)
	}
}
