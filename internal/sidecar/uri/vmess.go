package uri

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"lampac-go/internal/sidecar"
)

// ParseVMess parses a vmess:// URI into ProxyOutbound.
// Supports both base64 JSON format and standard URL format.
func ParseVMess(rawURI string) (sidecar.ProxyOutbound, error) {
	if !strings.HasPrefix(rawURI, "vmess://") {
		return sidecar.ProxyOutbound{}, fmt.Errorf("not a vmess:// URI")
	}

	payload := rawURI[len("vmess://"):]

	// Try base64 JSON format first (most common).
	if decoded, err := tryDecodeBase64(payload); err == nil {
		return parseVMessJSON(decoded)
	}

	// Fallback: standard URL format vmess://uuid@host:port?params...
	return parseVMessURL(rawURI)
}

func tryDecodeBase64(s string) ([]byte, error) {
	// Remove fragment if present
	if idx := strings.Index(s, "#"); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)

	// Try standard base64
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	// Try raw (no padding)
	if data, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	// Try URL-safe base64
	if data, err := base64.URLEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

func parseVMessJSON(data []byte) (sidecar.ProxyOutbound, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return sidecar.ProxyOutbound{}, fmt.Errorf("vmess: invalid JSON: %w", err)
	}

	server := toStr(raw["add"])
	if server == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("vmess: missing server address (add)")
	}

	uuid := toStr(raw["id"])
	if uuid == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("vmess: missing id (uuid)")
	}

	port := toInt(raw["port"])
	if port == 0 {
		port = 443
	}

	alterID := toInt(raw["aid"])

	network := toStr(raw["net"])
	if network == "" {
		network = "tcp"
	}

	security := toStr(raw["tls"])
	if security == "" {
		security = "none"
	}

	return sidecar.ProxyOutbound{
		Protocol:    "vmess",
		Server:      server,
		Port:        port,
		UUID:        uuid,
		AlterID:     alterID,
		Network:     network,
		Security:    security,
		SNI:         toStr(raw["sni"]),
		Fingerprint: toStr(raw["fp"]),
		Path:        toStr(raw["path"]),
		Host:        toStr(raw["host"]),
		ALPN:        toStr(raw["alpn"]),
	}, nil
}

func parseVMessURL(rawURI string) (sidecar.ProxyOutbound, error) {
	// Remove fragment
	if idx := strings.Index(rawURI, "#"); idx >= 0 {
		rawURI = rawURI[:idx]
	}

	u, err := url.Parse(rawURI)
	if err != nil {
		return sidecar.ProxyOutbound{}, err
	}

	portStr := u.Port()
	port := 443
	if portStr != "" {
		port, err = strconv.Atoi(portStr)
		if err != nil {
			return sidecar.ProxyOutbound{}, fmt.Errorf("vmess: invalid port: %s", portStr)
		}
	}

	uuid := ""
	if u.User != nil {
		uuid = u.User.Username()
	}
	if uuid == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("vmess: missing UUID")
	}
	if u.Hostname() == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("vmess: missing host")
	}

	q := u.Query()
	network := q.Get("type")
	if network == "" {
		network = "tcp"
	}
	security := q.Get("security")
	if security == "" {
		security = "none"
	}

	return sidecar.ProxyOutbound{
		Protocol:    "vmess",
		Server:      u.Hostname(),
		Port:        port,
		UUID:        uuid,
		AlterID:     toInt(q.Get("aid")),
		Network:     network,
		Security:    security,
		SNI:         q.Get("sni"),
		Fingerprint: q.Get("fp"),
		Path:        q.Get("path"),
		Host:        q.Get("host"),
		ALPN:        q.Get("alpn"),
	}, nil
}

// toStr safely converts any value to string.
func toStr(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case json.Number:
		return val.String()
	default:
		return fmt.Sprintf("%v", val)
	}
}

// toInt safely converts any value to int.
func toInt(v any) int {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return int(val)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(val))
		return n
	case json.Number:
		n, _ := val.Int64()
		return int(n)
	case int:
		return val
	default:
		return 0
	}
}
