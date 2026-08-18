// Package zapret integrates bol-van/zapret (https://github.com/bol-van/zapret)
// as a kernel-level DPI bypass. Unlike the userspace antidpi SOCKS5 (which
// rewrites TLS records inside our own dialer), zapret hooks into nftables /
// iptables and rewrites packets via NFQUEUE — so the bypass applies to ALL
// traffic to the configured hosts, regardless of which client originates it
// (yt-dlp, our own uTLS path, the OS resolver, etc).
package zapret

import (
	"bufio"
	"os"
	"strings"
)

// DefaultHosts is the host suffix list nfqws will desync. Mirrors the legacy
// antidpi.DefaultHosts so behaviour stays consistent on the YouTube path.
var DefaultHosts = []string{
	"googlevideo.com",
	"youtube.com",
	"youtu.be",
	"ytimg.com",
	"ggpht.com",
	"googleapis.com",
	"googleusercontent.com",
	"gstatic.com",
	"google.com",
}

// LoadHostlist reads a one-host-per-line file (with `#` comments). Returns the
// merged list of explicit hosts plus DefaultHosts when extend=true and dedupes.
// Empty path → just DefaultHosts (when extend) or nil (when !extend).
func LoadHostlist(path string, extend bool) ([]string, error) {
	var out []string
	seen := map[string]struct{}{}
	add := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || strings.HasPrefix(h, "#") {
			return
		}
		if _, ok := seen[h]; ok {
			return
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	if extend {
		for _, h := range DefaultHosts {
			add(h)
		}
	}
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		add(sc.Text())
	}
	return out, sc.Err()
}

// WriteHostlist dumps hosts to a file in the format nfqws expects (--hostlist).
func WriteHostlist(path string, hosts []string) error {
	if len(hosts) == 0 {
		return os.WriteFile(path, nil, 0644)
	}
	var b strings.Builder
	for _, h := range hosts {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}
