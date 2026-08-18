package uri

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"lampac-go/internal/sidecar"
)

// ParseVLESS parses a vless:// URI into ProxyOutbound.
// Format: vless://uuid@host:port?encryption=none&security=tls&sni=...&type=tcp&flow=...#tag
func ParseVLESS(rawURI string) (sidecar.ProxyOutbound, error) {
	if !strings.HasPrefix(rawURI, "vless://") {
		return sidecar.ProxyOutbound{}, fmt.Errorf("not a vless:// URI")
	}

	// Remove fragment (#tag)
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
			return sidecar.ProxyOutbound{}, fmt.Errorf("invalid port: %s", portStr)
		}
	}

	q := u.Query()

	encryption := q.Get("encryption")
	if encryption == "" {
		encryption = "none"
	}
	network := q.Get("type")
	if network == "" {
		network = "tcp"
	}

	uuid := ""
	if u.User != nil {
		uuid = u.User.Username()
	}
	if uuid == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("missing UUID in vless URI")
	}
	if u.Hostname() == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("missing address in vless URI")
	}

	return sidecar.ProxyOutbound{
		Protocol:    "vless",
		Server:      u.Hostname(),
		Port:        port,
		UUID:        uuid,
		Encryption:  encryption,
		Flow:        q.Get("flow"),
		Security:    q.Get("security"),
		SNI:         q.Get("sni"),
		Fingerprint: q.Get("fp"),
		Network:     network,
		Path:        q.Get("path"),
		Host:        q.Get("host"),
		ALPN:        q.Get("alpn"),
		PublicKey:   q.Get("pbk"),
		ShortID:     q.Get("sid"),
		PQV:         q.Get("pqv"),
	}, nil
}
