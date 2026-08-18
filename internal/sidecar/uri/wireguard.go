package uri

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"lampac-go/internal/sidecar"
)

// ParseWireGuard parses a wg:// or wireguard:// URI into ProxyOutbound.
//
// Standard format:
//
//	wg://private-key@host:port?publickey=xxx&address=172.16.0.2/32,fd01::2/128&mtu=1280&reserved=0,0,0&presharedkey=xxx#tag
//
// Special WARP value:
//
//	wg://warp — triggers auto-registration with Cloudflare WARP API.
func ParseWireGuard(rawURI string) (sidecar.ProxyOutbound, error) {
	if !strings.HasPrefix(rawURI, "wg://") && !strings.HasPrefix(rawURI, "wireguard://") {
		return sidecar.ProxyOutbound{}, fmt.Errorf("not a wg:// URI")
	}

	// Remove fragment (#tag)
	clean := rawURI
	if idx := strings.Index(clean, "#"); idx >= 0 {
		clean = clean[:idx]
	}

	// Extract body after scheme
	var body string
	if strings.HasPrefix(clean, "wg://") {
		body = clean[len("wg://"):]
	} else {
		body = clean[len("wireguard://"):]
	}
	body = strings.TrimSpace(body)

	// Special case: wg://warp
	if body == "warp" || body == "WARP" {
		return sidecar.ProxyOutbound{
			Protocol: "wireguard",
			Server:   "warp", // sentinel value, handled by WARP registration logic
		}, nil
	}

	u, err := url.Parse(clean)
	if err != nil {
		return sidecar.ProxyOutbound{}, err
	}

	portStr := u.Port()
	port := 51820
	if portStr != "" {
		port, err = strconv.Atoi(portStr)
		if err != nil {
			return sidecar.ProxyOutbound{}, fmt.Errorf("wg: invalid port: %s", portStr)
		}
	}

	privateKey := ""
	if u.User != nil {
		privateKey = u.User.Username()
	}
	if u.Hostname() == "" {
		return sidecar.ProxyOutbound{}, fmt.Errorf("wg: missing host")
	}

	q := u.Query()

	// Parse local addresses (comma-separated)
	var localAddr []string
	if addrs := q.Get("address"); addrs != "" {
		for a := range strings.SplitSeq(addrs, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				localAddr = append(localAddr, a)
			}
		}
	}

	// Parse reserved bytes (comma-separated ints)
	var reserved []int
	if res := q.Get("reserved"); res != "" {
		for s := range strings.SplitSeq(res, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				n, _ := strconv.Atoi(s)
				reserved = append(reserved, n)
			}
		}
	}

	mtu := 1280
	if m := q.Get("mtu"); m != "" {
		if n, err := strconv.Atoi(m); err == nil && n > 0 {
			mtu = n
		}
	}

	return sidecar.ProxyOutbound{
		Protocol:      "wireguard",
		Server:        u.Hostname(),
		Port:          port,
		PrivateKey:    privateKey,
		PeerPublicKey: q.Get("publickey"),
		PreSharedKey:  q.Get("presharedkey"),
		LocalAddr:     localAddr,
		MTU:           mtu,
		Reserved:      reserved,
	}, nil
}
