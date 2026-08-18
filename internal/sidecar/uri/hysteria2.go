package uri

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"lampac-go/internal/sidecar"
)

// ParseHysteria2 parses a hysteria2:// or hy2:// URI into ProxyOutbound.
// Format: hysteria2://password@host:port?sni=xxx&obfs=salamander&obfs-password=xxx&insecure=0#tag
func ParseHysteria2(rawURI string) (sidecar.ProxyOutbound, error) {
	if !strings.HasPrefix(rawURI, "hysteria2://") && !strings.HasPrefix(rawURI, "hy2://") {
		return sidecar.ProxyOutbound{}, fmt.Errorf("not a hysteria2:// URI")
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
			return sidecar.ProxyOutbound{}, fmt.Errorf("hysteria2: invalid port: %s", portStr)
		}
	}

	password := ""
	if u.User != nil {
		password = u.User.Username()
	}
	if password == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("hysteria2: missing password")
	}
	if u.Hostname() == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("hysteria2: missing host")
	}

	q := u.Query()

	sni := q.Get("sni")
	if sni == "" {
		sni = u.Hostname()
	}

	out := sidecar.ProxyOutbound{
		Protocol:     "hysteria2",
		Server:       u.Hostname(),
		Port:         port,
		Password:     password,
		SNI:          sni,
		Fingerprint:  q.Get("fp"),
		Obfs:         q.Get("obfs"),
		ObfsPassword: q.Get("obfs-password"),
		ALPN:         q.Get("alpn"),
	}

	if up := q.Get("up"); up != "" {
		out.UpMbps, _ = strconv.Atoi(up)
	}
	if down := q.Get("down"); down != "" {
		out.DownMbps, _ = strconv.Atoi(down)
	}

	return out, nil
}
