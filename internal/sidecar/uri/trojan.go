package uri

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"lampac-go/internal/sidecar"
)

// ParseTrojan parses a trojan:// URI into ProxyOutbound.
// Format: trojan://password@host:port?security=tls&sni=...&type=tcp&path=/ws#tag
func ParseTrojan(rawURI string) (sidecar.ProxyOutbound, error) {
	if !strings.HasPrefix(rawURI, "trojan://") {
		return sidecar.ProxyOutbound{}, fmt.Errorf("not a trojan:// URI")
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
			return sidecar.ProxyOutbound{}, fmt.Errorf("trojan: invalid port: %s", portStr)
		}
	}

	password := ""
	if u.User != nil {
		password = u.User.Username()
	}
	if password == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("trojan: missing password")
	}
	if u.Hostname() == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("trojan: missing host")
	}

	q := u.Query()

	network := q.Get("type")
	if network == "" {
		network = "tcp"
	}

	security := q.Get("security")
	if security == "" {
		security = "tls" // Trojan defaults to TLS
	}

	sni := q.Get("sni")
	if sni == "" {
		sni = u.Hostname() // Default SNI is server hostname
	}

	return sidecar.ProxyOutbound{
		Protocol:    "trojan",
		Server:      u.Hostname(),
		Port:        port,
		Password:    password,
		Security:    security,
		SNI:         sni,
		Fingerprint: q.Get("fp"),
		Network:     network,
		Path:        q.Get("path"),
		Host:        q.Get("host"),
		ALPN:        q.Get("alpn"),
	}, nil
}
