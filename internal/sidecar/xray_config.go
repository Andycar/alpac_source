package sidecar

import (
	"fmt"
	"strings"
)

// GenerateXrayConfig builds the xray JSON config for any supported protocol.
func GenerateXrayConfig(out ProxyOutbound, socksPort int) (map[string]any, error) {
	var outboundCfg map[string]any

	switch out.Protocol {
	case "vless":
		outboundCfg = generateXrayVLESS(out)
	case "vmess":
		outboundCfg = generateXrayVMess(out)
	case "trojan":
		outboundCfg = generateXrayTrojan(out)
	default:
		return nil, fmt.Errorf("xray: unsupported protocol %q", out.Protocol)
	}

	return map[string]any{
		"log": map[string]any{
			"loglevel": "warning",
		},
		"inbounds": []any{
			map[string]any{
				"port":     socksPort,
				"listen":   "127.0.0.1",
				"protocol": "socks",
				"settings": map[string]any{
					"auth": "noauth",
					"udp":  false,
				},
			},
		},
		"outbounds": []any{outboundCfg},
	}, nil
}

// generateXrayVLESS builds xray outbound config for VLESS protocol.
func generateXrayVLESS(out ProxyOutbound) map[string]any {
	user := map[string]any{
		"id":         out.UUID,
		"encryption": out.Encryption,
	}
	if out.Flow != "" {
		user["flow"] = out.Flow
	}

	vnext := map[string]any{
		"address": out.Server,
		"port":    out.Port,
		"users":   []any{user},
	}

	stream := buildXrayStreamSettings(out)

	return map[string]any{
		"protocol": "vless",
		"settings": map[string]any{
			"vnext": []any{vnext},
		},
		"streamSettings": stream,
	}
}

// generateXrayVMess builds xray outbound config for VMess protocol.
func generateXrayVMess(out ProxyOutbound) map[string]any {
	user := map[string]any{
		"id":       out.UUID,
		"alterId":  out.AlterID,
		"security": "auto",
	}

	vnext := map[string]any{
		"address": out.Server,
		"port":    out.Port,
		"users":   []any{user},
	}

	stream := buildXrayStreamSettings(out)

	return map[string]any{
		"protocol": "vmess",
		"settings": map[string]any{
			"vnext": []any{vnext},
		},
		"streamSettings": stream,
	}
}

// generateXrayTrojan builds xray outbound config for Trojan protocol.
func generateXrayTrojan(out ProxyOutbound) map[string]any {
	server := map[string]any{
		"address":  out.Server,
		"port":     out.Port,
		"password": out.Password,
	}

	stream := buildXrayStreamSettings(out)

	return map[string]any{
		"protocol": "trojan",
		"settings": map[string]any{
			"servers": []any{server},
		},
		"streamSettings": stream,
	}
}

// buildXrayStreamSettings builds the streamSettings shared across protocols.
func buildXrayStreamSettings(out ProxyOutbound) map[string]any {
	stream := map[string]any{
		"network": out.Network,
	}

	// Security (TLS / REALITY / none).
	switch out.Security {
	case "tls":
		stream["security"] = "tls"
		tls := map[string]any{}
		if out.SNI != "" {
			tls["serverName"] = out.SNI
		}
		if out.Fingerprint != "" {
			tls["fingerprint"] = out.Fingerprint
		}
		if out.ALPN != "" {
			tls["alpn"] = strings.Split(out.ALPN, ",")
		}
		stream["tlsSettings"] = tls

	case "reality":
		stream["security"] = "reality"
		reality := map[string]any{}
		if out.SNI != "" {
			reality["serverName"] = out.SNI
		}
		if out.Fingerprint != "" {
			reality["fingerprint"] = out.Fingerprint
		}
		if out.PublicKey != "" {
			reality["publicKey"] = out.PublicKey
		}
		if out.ShortID != "" {
			reality["shortId"] = out.ShortID
		}
		if out.PQV != "" {
			reality["pqv"] = out.PQV
		}
		stream["realitySettings"] = reality
	}

	// Transport (tcp / ws / grpc).
	switch out.Network {
	case "ws":
		ws := map[string]any{}
		if out.Path != "" {
			ws["path"] = out.Path
		}
		if out.Host != "" {
			ws["headers"] = map[string]any{"Host": out.Host}
		}
		stream["wsSettings"] = ws

	case "grpc":
		grpc := map[string]any{}
		if out.Path != "" {
			grpc["serviceName"] = out.Path
		}
		stream["grpcSettings"] = grpc
	}

	return stream
}
