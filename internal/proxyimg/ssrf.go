package proxyimg

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"
)

// errBlockedTarget is returned when the image proxy is asked to reach an
// address it must never touch. It closes the SSRF hole on the unsigned
// direct-URL branch of resolveTarget (e.g. /proxyimg/http://169.254.169.254/…
// or http://127.0.0.1:<internal-port>/…).
var errBlockedTarget = errors.New("proxyimg: blocked internal target address")

// isBlockedIP reports whether ip falls in a range the image proxy must not
// reach: loopback, private (RFC1918/ULA), link-local (incl. the 169.254.169.254
// cloud-metadata endpoint), the unspecified address, and interface/link-local
// multicast. A nil IP (unparseable) is treated as blocked.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast()
}

// ssrfGuardControl runs at dial time against the *already-resolved* IP:port,
// so it also defeats DNS-rebinding (we check the IP we are about to connect to,
// not the hostname in the URL) and is applied to every redirect hop.
func ssrfGuardControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errBlockedTarget
	}
	if isBlockedIP(net.ParseIP(host)) {
		return errBlockedTarget
	}
	return nil
}

// newGuardedClient builds an http.Client whose dialer refuses internal
// addresses. No upstream Proxy is configured on purpose: routing through a
// proxy would move the guard onto the proxy's address and defeat it.
func newGuardedClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   ssrfGuardControl,
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}
