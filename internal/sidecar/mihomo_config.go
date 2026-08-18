package sidecar

import "strings"

// GenerateMihomoConfig builds a mihomo YAML config for any supported protocol.
func GenerateMihomoConfig(out ProxyOutbound, socksPort int) map[string]any {
	proxy := buildMihomoProxy(out)

	return map[string]any{
		"mixed-port":          socksPort,
		"allow-lan":           false,
		"mode":                "global",
		"log-level":           "warning",
		"ipv6":                false,
		"external-controller": "",
		"proxies":             []any{proxy},
		"rules":               []string{"MATCH,proxy"},
	}
}

func buildMihomoProxy(out ProxyOutbound) map[string]any {
	switch out.Protocol {
	case "ss":
		return buildMihomoSS(out)
	case "hysteria2":
		return buildMihomoHysteria2(out)
	case "tuic":
		return buildMihomoTUIC(out)
	case "wireguard":
		return buildMihomoWireGuard(out)
	case "vless":
		return buildMihomoVLESS(out)
	case "vmess":
		return buildMihomoVMess(out)
	case "trojan":
		return buildMihomoTrojan(out)
	default:
		// Fallback: try as generic
		return map[string]any{
			"name":   "proxy",
			"type":   out.Protocol,
			"server": out.Server,
			"port":   out.Port,
		}
	}
}

func buildMihomoSS(out ProxyOutbound) map[string]any {
	m := map[string]any{
		"name":     "proxy",
		"type":     "ss",
		"server":   out.Server,
		"port":     out.Port,
		"cipher":   out.Encryption,
		"password": out.Password,
		"udp":      true,
	}
	return m
}

func buildMihomoHysteria2(out ProxyOutbound) map[string]any {
	m := map[string]any{
		"name":     "proxy",
		"type":     "hysteria2",
		"server":   out.Server,
		"port":     out.Port,
		"password": out.Password,
		"udp":      true,
	}
	if out.SNI != "" {
		m["sni"] = out.SNI
	}
	if out.Fingerprint != "" {
		m["fingerprint"] = out.Fingerprint
	}
	if out.Obfs != "" {
		m["obfs"] = out.Obfs
		if out.ObfsPassword != "" {
			m["obfs-password"] = out.ObfsPassword
		}
	}
	if out.UpMbps > 0 {
		m["up"] = out.UpMbps
	}
	if out.DownMbps > 0 {
		m["down"] = out.DownMbps
	}
	if out.ALPN != "" {
		m["alpn"] = splitALPN(out.ALPN)
	}
	return m
}

func buildMihomoTUIC(out ProxyOutbound) map[string]any {
	m := map[string]any{
		"name":     "proxy",
		"type":     "tuic",
		"server":   out.Server,
		"port":     out.Port,
		"uuid":     out.UUID,
		"password": out.Password,
		"udp":      true,
	}
	if out.SNI != "" {
		m["sni"] = out.SNI
	}
	cc := out.CongestionCtrl
	if cc == "" {
		cc = "bbr"
	}
	m["congestion-controller"] = cc
	relay := out.UDPRelayMode
	if relay == "" {
		relay = "native"
	}
	m["udp-relay-mode"] = relay
	if out.ALPN != "" {
		m["alpn"] = splitALPN(out.ALPN)
	}
	return m
}

func buildMihomoWireGuard(out ProxyOutbound) map[string]any {
	m := map[string]any{
		"name":            "proxy",
		"type":            "wireguard",
		"server":          out.Server,
		"port":            out.Port,
		"private-key":     out.PrivateKey,
		"peer-public-key": out.PeerPublicKey,
		"udp":             true,
	}
	if out.PreSharedKey != "" {
		m["pre-shared-key"] = out.PreSharedKey
	}
	// Local addresses
	if len(out.LocalAddr) > 0 {
		m["ip"] = out.LocalAddr[0]
		if len(out.LocalAddr) > 1 {
			m["ipv6"] = out.LocalAddr[1]
		}
	}
	if out.MTU > 0 {
		m["mtu"] = out.MTU
	} else {
		m["mtu"] = 1280
	}
	if len(out.Reserved) > 0 {
		m["reserved"] = out.Reserved
	}
	return m
}

func buildMihomoVLESS(out ProxyOutbound) map[string]any {
	m := map[string]any{
		"name":   "proxy",
		"type":   "vless",
		"server": out.Server,
		"port":   out.Port,
		"uuid":   out.UUID,
		"udp":    true,
	}
	if out.Flow != "" {
		m["flow"] = out.Flow
	}
	if out.Network != "" && out.Network != "tcp" {
		m["network"] = out.Network
	}
	// TLS
	if out.Security == "tls" || out.Security == "reality" {
		m["tls"] = true
		if out.SNI != "" {
			m["servername"] = out.SNI
		}
		if out.Fingerprint != "" {
			m["client-fingerprint"] = out.Fingerprint
		}
		if out.ALPN != "" {
			m["alpn"] = splitALPN(out.ALPN)
		}
	}
	if out.Security == "reality" {
		reality := map[string]any{}
		if out.PublicKey != "" {
			reality["public-key"] = out.PublicKey
		}
		if out.ShortID != "" {
			reality["short-id"] = out.ShortID
		}
		m["reality-opts"] = reality
	}
	// Transport
	addMihomoTransport(m, out)
	return m
}

func buildMihomoVMess(out ProxyOutbound) map[string]any {
	m := map[string]any{
		"name":    "proxy",
		"type":    "vmess",
		"server":  out.Server,
		"port":    out.Port,
		"uuid":    out.UUID,
		"alterId": out.AlterID,
		"cipher":  "auto",
		"udp":     true,
	}
	if out.Network != "" && out.Network != "tcp" {
		m["network"] = out.Network
	}
	if out.Security == "tls" {
		m["tls"] = true
		if out.SNI != "" {
			m["servername"] = out.SNI
		}
		if out.Fingerprint != "" {
			m["client-fingerprint"] = out.Fingerprint
		}
		if out.ALPN != "" {
			m["alpn"] = splitALPN(out.ALPN)
		}
	}
	addMihomoTransport(m, out)
	return m
}

func buildMihomoTrojan(out ProxyOutbound) map[string]any {
	m := map[string]any{
		"name":     "proxy",
		"type":     "trojan",
		"server":   out.Server,
		"port":     out.Port,
		"password": out.Password,
		"udp":      true,
	}
	if out.SNI != "" {
		m["sni"] = out.SNI
	}
	if out.Fingerprint != "" {
		m["client-fingerprint"] = out.Fingerprint
	}
	if out.ALPN != "" {
		m["alpn"] = splitALPN(out.ALPN)
	}
	if out.Network != "" && out.Network != "tcp" {
		m["network"] = out.Network
	}
	addMihomoTransport(m, out)
	return m
}

// addMihomoTransport adds ws-opts, grpc-opts, or h2-opts based on network.
func addMihomoTransport(m map[string]any, out ProxyOutbound) {
	switch out.Network {
	case "ws":
		ws := map[string]any{}
		if out.Path != "" {
			ws["path"] = out.Path
		}
		if out.Host != "" {
			ws["headers"] = map[string]any{"Host": out.Host}
		}
		if len(ws) > 0 {
			m["ws-opts"] = ws
		}
	case "grpc":
		grpc := map[string]any{}
		if out.Path != "" {
			grpc["grpc-service-name"] = out.Path
		}
		if len(grpc) > 0 {
			m["grpc-opts"] = grpc
		}
	case "h2":
		h2 := map[string]any{}
		if out.Path != "" {
			h2["path"] = out.Path
		}
		if out.Host != "" {
			h2["host"] = []string{out.Host}
		}
		if len(h2) > 0 {
			m["h2-opts"] = h2
		}
	}
}

// splitALPN splits a comma-separated ALPN string into a slice.
func splitALPN(alpn string) []string {
	var out []string
	for s := range strings.SplitSeq(alpn, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
