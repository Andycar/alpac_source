package httpclient

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/rs/zerolog/log"
)

// RotatingRoundTripper wraps multiple http.RoundTripper instances (one per proxy)
// and rotates between them. Used by balancers like Alloha whose CDN traffic goes
// through the generic /proxy/ handler — rotation happens transparently at the
// transport level without changes to the proxy handler.
//
// Rotation strategies:
//   - Proactive (time-based): switches to next proxy every rotateInterval.
//   - Forced (error-based): on 403/500/502/503 immediately rotates so the
//     proxy handler's retry loop uses a different proxy.
type RotatingRoundTripper struct {
	transports     []http.RoundTripper
	labels         []string
	current        atomic.Int64
	rotateInterval int64 // nanoseconds; 0 = no proactive rotation
	lastRotated    atomic.Int64
	prefix         string // log prefix, e.g. "alloha"
}

// NewRotatingRoundTripper builds a rotating pool of uTLS+SOCKS5 transports.
//
// Pool layout: [0] = default (direct uTLS), [1..N] = SOCKS5 proxies from cdnProxies.
// cdnProxies entries can be:
//   - proxy label (resolved via SocksAddrForLabel)
//   - raw "socks5://host:port"
//   - "direct" keyword (explicit no-proxy uTLS)
func NewRotatingRoundTripper(prefix string, cdnProxies []string, rotateMin float64) *RotatingRoundTripper {
	// [0] = default direct uTLS.
	transports := []http.RoundTripper{UTLSTransport}
	labels := []string{"default"}

	for _, ref := range cdnProxies {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}

		if strings.EqualFold(ref, "direct") {
			transports = append(transports, UTLSTransport)
			labels = append(labels, "direct")
			continue
		}

		// Try resolving as a proxy label first.
		socksAddr := SocksAddrForLabel(ref)
		if socksAddr == "" {
			// Fallback: raw socks5:// address.
			raw := strings.TrimPrefix(strings.TrimPrefix(ref, "socks5://"), "socks://")
			if raw == ref {
				log.Warn().Str("ref", ref).
					Msg(prefix + ": unknown CDN proxy ref, skipping")
				continue
			}
			socksAddr = raw
		}

		rt := newUTLSRoundTripperSOCKS5(utls.HelloChrome_120, socksAddr)
		transports = append(transports, rt)
		labels = append(labels, ref)
	}

	interval := int64(0)
	if rotateMin > 0 {
		interval = int64(time.Duration(rotateMin * float64(time.Minute)))
	}

	rrt := &RotatingRoundTripper{
		transports:     transports,
		labels:         labels,
		rotateInterval: interval,
		prefix:         prefix,
	}

	if len(transports) > 1 {
		log.Info().
			Int("poolSize", len(transports)).
			Strs("proxies", labels).
			Float64("rotateMin", rotateMin).
			Msg(prefix + ": CDN proxy rotating transport initialized")
	}

	return rrt
}

// RoundTrip implements http.RoundTripper. Performs proactive time-based rotation
// and forced rotation on CDN errors (403/5xx).
func (rt *RotatingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	n := int64(len(rt.transports))
	if n == 0 {
		return http.DefaultTransport.RoundTrip(req)
	}

	idx := rt.current.Load() % n

	// Proactive time-based rotation.
	if rt.rotateInterval > 0 && n > 1 {
		now := time.Now().UnixNano()
		last := rt.lastRotated.Load()
		if last > 0 && now-last >= rt.rotateInterval {
			if rt.lastRotated.CompareAndSwap(last, now) {
				newIdx := (idx + 1) % n
				rt.current.Store(newIdx)
				log.Info().
					Str("from", rt.labels[idx]).
					Str("to", rt.labels[newIdx]).
					Msg(rt.prefix + ": proactive proxy rotation")
				idx = newIdx
			}
		}
		if last == 0 {
			rt.lastRotated.CompareAndSwap(0, time.Now().UnixNano())
		}
	}

	resp, err := rt.transports[idx].RoundTrip(req)

	// Forced rotation on CDN errors so the next request (retry or new segment)
	// uses a different proxy.
	if n > 1 {
		needRotate := err != nil
		if !needRotate && resp != nil {
			switch resp.StatusCode {
			case http.StatusForbidden,
				http.StatusInternalServerError,
				http.StatusBadGateway,
				http.StatusServiceUnavailable:
				needRotate = true
			}
		}
		if needRotate {
			newIdx := (idx + 1) % n
			rt.current.Store(newIdx)
			rt.lastRotated.Store(time.Now().UnixNano())
			log.Warn().
				Str("from", rt.labels[idx]).
				Str("to", rt.labels[newIdx]).
				Msg(rt.prefix + ": forced proxy rotation on CDN error")
		}
	}

	return resp, err
}
