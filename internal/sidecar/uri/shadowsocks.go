package uri

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"lampac-go/internal/sidecar"
)

// ParseShadowsocks parses a ss:// URI into ProxyOutbound.
// Supports SIP002 format: ss://BASE64(method:password)@host:port#tag
// And legacy format: ss://BASE64(method:password@host:port)#tag
func ParseShadowsocks(rawURI string) (sidecar.ProxyOutbound, error) {
	if !strings.HasPrefix(rawURI, "ss://") {
		return sidecar.ProxyOutbound{}, fmt.Errorf("not a ss:// URI")
	}

	// Remove fragment (#tag)
	if idx := strings.Index(rawURI, "#"); idx >= 0 {
		rawURI = rawURI[:idx]
	}

	body := rawURI[len("ss://"):]

	// SIP002: base64userinfo@host:port
	if atIdx := strings.LastIndex(body, "@"); atIdx >= 0 {
		userinfo := body[:atIdx]
		hostport := body[atIdx+1:]

		// Try to decode base64 userinfo
		decoded, err := decodeBase64Flexible(userinfo)
		if err != nil {
			// Not base64 — treat as plain method:password
			decoded = userinfo
		}

		method, password, err := splitMethodPassword(decoded)
		if err != nil {
			return sidecar.ProxyOutbound{}, fmt.Errorf("ss: %w", err)
		}

		host, port, err := parseHostPort(hostport)
		if err != nil {
			return sidecar.ProxyOutbound{}, fmt.Errorf("ss: %w", err)
		}

		return sidecar.ProxyOutbound{
			Protocol:   "ss",
			Server:     host,
			Port:       port,
			Encryption: method,
			Password:   password,
		}, nil
	}

	// Legacy: entire body is base64(method:password@host:port)
	decoded, err := decodeBase64Flexible(body)
	if err != nil {
		return sidecar.ProxyOutbound{}, fmt.Errorf("ss: cannot decode base64: %w", err)
	}

	atIdx := strings.LastIndex(decoded, "@")
	if atIdx < 0 {
		return sidecar.ProxyOutbound{}, fmt.Errorf("ss: missing @ in decoded URI")
	}

	method, password, err := splitMethodPassword(decoded[:atIdx])
	if err != nil {
		return sidecar.ProxyOutbound{}, fmt.Errorf("ss: %w", err)
	}

	host, port, err := parseHostPort(decoded[atIdx+1:])
	if err != nil {
		return sidecar.ProxyOutbound{}, fmt.Errorf("ss: %w", err)
	}

	return sidecar.ProxyOutbound{
		Protocol:   "ss",
		Server:     host,
		Port:       port,
		Encryption: method,
		Password:   password,
	}, nil
}

func decodeBase64Flexible(s string) (string, error) {
	s = strings.TrimSpace(s)
	// Try standard
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return string(data), nil
	}
	// Try raw (no padding)
	if data, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return string(data), nil
	}
	// Try URL-safe
	if data, err := base64.URLEncoding.DecodeString(s); err == nil {
		return string(data), nil
	}
	if data, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return string(data), nil
	}
	return "", fmt.Errorf("not valid base64")
}

func splitMethodPassword(s string) (method, password string, err error) {
	// URL-decode first in case of percent-encoding
	s, _ = url.QueryUnescape(s)

	before, after, ok := strings.Cut(s, ":")
	if !ok {
		return "", "", fmt.Errorf("missing method:password separator")
	}
	method = strings.TrimSpace(before)
	password = after
	if method == "" {
		return "", "", fmt.Errorf("empty cipher method")
	}
	if password == "" {
		return "", "", fmt.Errorf("empty password")
	}
	return method, password, nil
}

func parseHostPort(s string) (host string, port int, err error) {
	s = strings.TrimSpace(s)
	// Handle IPv6: [::1]:8388
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", 0, fmt.Errorf("malformed IPv6 address")
		}
		host = s[1:end]
		rest := s[end+1:]
		if strings.HasPrefix(rest, ":") {
			port, err = strconv.Atoi(rest[1:])
			if err != nil {
				return "", 0, fmt.Errorf("invalid port: %s", rest[1:])
			}
		} else {
			port = 8388 // default SS port
		}
		return host, port, nil
	}

	// Regular host:port
	lastColon := strings.LastIndex(s, ":")
	if lastColon < 0 {
		return s, 8388, nil // default SS port
	}
	host = s[:lastColon]
	port, err = strconv.Atoi(s[lastColon+1:])
	if err != nil {
		return "", 0, fmt.Errorf("invalid port: %s", s[lastColon+1:])
	}
	return host, port, nil
}
