package uri

import (
	"fmt"
	"net/url"
	"strings"

	"lampac-go/internal/sidecar"
)

// ParseSOCKS5 parses a socks5:// or socks:// URI into ProxyOutbound.
//
// Format: socks5://[username:password@]host:port[?param=value]
//
// Username goes into ProxyOutbound.UUID (the field socks5OutDialer reads as
// username — see internal/proxycore/socks5_out.go:26). Password is plain.
//
// Unlike VLESS/Trojan/etc., SOCKS5 needs no sidecar binary: proxycore dials
// the upstream directly via socks5OutDialer. The DetectEngine call elsewhere
// returns EngineProxyCore for this protocol.
func ParseSOCKS5(rawURI string) (sidecar.ProxyOutbound, error) {
	if !strings.HasPrefix(rawURI, "socks5://") && !strings.HasPrefix(rawURI, "socks://") {
		return sidecar.ProxyOutbound{}, fmt.Errorf("not a socks5:// URI")
	}

	// Normalize socks:// → socks5:// so url.Parse picks the right host/userinfo.
	if strings.HasPrefix(rawURI, "socks://") {
		rawURI = "socks5://" + rawURI[len("socks://"):]
	}

	u, err := url.Parse(rawURI)
	if err != nil {
		return sidecar.ProxyOutbound{}, fmt.Errorf("socks5: parse: %w", err)
	}
	if u.Hostname() == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("socks5: missing host")
	}

	host, port, err := parseHostPort(u.Host)
	if err != nil {
		return sidecar.ProxyOutbound{}, fmt.Errorf("socks5: %w", err)
	}

	out := sidecar.ProxyOutbound{
		Protocol: "socks5",
		Server:   host,
		Port:     port,
	}
	if u.User != nil {
		out.UUID = u.User.Username() // socks5OutDialer reuses UUID as username
		if pw, ok := u.User.Password(); ok {
			out.Password = pw
		}
	}
	return out, nil
}

// ParseHTTPProxy parses an http:// or https:// proxy URI into ProxyOutbound.
//
// Format: http(s)://[username:password@]host:port
//
// https:// implies the proxy itself accepts TLS (HTTPS proxy); the
// httpConnectDialer sets out.Security="tls" when this flag is present.
func ParseHTTPProxy(rawURI string) (sidecar.ProxyOutbound, error) {
	useTLS := strings.HasPrefix(rawURI, "https://")
	if !useTLS && !strings.HasPrefix(rawURI, "http://") {
		return sidecar.ProxyOutbound{}, fmt.Errorf("not an http(s):// URI")
	}

	u, err := url.Parse(rawURI)
	if err != nil {
		return sidecar.ProxyOutbound{}, fmt.Errorf("http: parse: %w", err)
	}
	if u.Hostname() == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("http: missing host")
	}

	hostport := u.Host
	// Default ports if omitted.
	if u.Port() == "" {
		if useTLS {
			hostport = u.Hostname() + ":443"
		} else {
			hostport = u.Hostname() + ":80"
		}
	}

	host, port, err := parseHostPort(hostport)
	if err != nil {
		return sidecar.ProxyOutbound{}, fmt.Errorf("http: %w", err)
	}

	out := sidecar.ProxyOutbound{
		Protocol: "http",
		Server:   host,
		Port:     port,
	}
	if useTLS {
		out.Security = "tls"
	}
	if u.User != nil {
		out.UUID = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			out.Password = pw
		}
	}
	return out, nil
}
