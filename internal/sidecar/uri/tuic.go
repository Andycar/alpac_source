package uri

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"lampac-go/internal/sidecar"
)

// ParseTUIC parses a tuic:// URI into ProxyOutbound.
// Format: tuic://uuid:password@host:port?sni=xxx&congestion_control=bbr&udp_relay_mode=native&alpn=h3#tag
func ParseTUIC(rawURI string) (sidecar.ProxyOutbound, error) {
	if !strings.HasPrefix(rawURI, "tuic://") {
		return sidecar.ProxyOutbound{}, fmt.Errorf("not a tuic:// URI")
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
			return sidecar.ProxyOutbound{}, fmt.Errorf("tuic: invalid port: %s", portStr)
		}
	}

	uuid := ""
	password := ""
	if u.User != nil {
		uuid = u.User.Username()
		password, _ = u.User.Password()
	}
	if uuid == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("tuic: missing UUID")
	}
	if u.Hostname() == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("tuic: missing host")
	}

	q := u.Query()

	sni := q.Get("sni")
	if sni == "" {
		sni = u.Hostname()
	}

	cc := q.Get("congestion_control")
	if cc == "" {
		cc = q.Get("congestion-control")
	}
	if cc == "" {
		cc = "bbr"
	}

	relay := q.Get("udp_relay_mode")
	if relay == "" {
		relay = q.Get("udp-relay-mode")
	}
	if relay == "" {
		relay = "native"
	}

	return sidecar.ProxyOutbound{
		Protocol:       "tuic",
		Server:         u.Hostname(),
		Port:           port,
		UUID:           uuid,
		Password:       password,
		SNI:            sni,
		Fingerprint:    q.Get("fp"),
		ALPN:           q.Get("alpn"),
		CongestionCtrl: cc,
		UDPRelayMode:   relay,
	}, nil
}
