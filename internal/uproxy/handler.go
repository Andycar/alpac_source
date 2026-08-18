package uproxy

import (
	"io"
	"net"
	"net/http"
	"lampac-go/internal/httpclient"
	"net/url"
	"strings"
	"time"

	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

var hopHeaders = map[string]struct{}{
	"Connection":          {},
	"Proxy-Connection":    {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

type Handler struct {
	client *http.Client
	links  *proxylink.Manager
}

func New(links *proxylink.Manager) *Handler {
	return &Handler{
		links:  links,
		client: httpclient.NewNoRedirect(45 * time.Second),
	}
}

func (h *Handler) ServeProxy(w http.ResponseWriter, r *http.Request, prefix string) {
	rawTarget := strings.TrimPrefix(r.URL.Path, prefix)
	rawQuery := r.URL.RawQuery
	plugin := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("pl"))) // direct-mode carries ?pl=<balancer>
	target, ok := decodeDirectTarget(rawTarget, r.URL.RawQuery)
	if !ok && h.links != nil {
		reqIP := requestIP(r)
		if model := h.links.Decrypt(rawTarget, reqIP); model != nil && strings.TrimSpace(model.URI) != "" {
			target = strings.TrimSpace(model.URI)
			if plugin == "" {
				plugin = strings.ToLower(strings.TrimSpace(model.Plugin))
			}
			// Lampa multi-CDN "<url1> or <url2>" — keep the primary (decodeDirectTarget does the
			// same for the keyless path; the encrypted path must not fetch the joined string).
			if i := strings.Index(target, " or "); i > 0 {
				target = strings.TrimSpace(target[:i])
			}
			if rawQuery != "" {
				if strings.Contains(target, "?") {
					target += "&" + rawQuery
				} else {
					target += "?" + rawQuery
				}
			}
			ok = true
		}
	}
	if !ok {
		h.fallback(w, r)
		return
	}

	// Fetch through the BALANCER's egress when one is registered. Sources like filmix mint a
	// CDN url with an IP-bound `?hash=…` tied to the IP that RESOLVED the link; the segment fetch
	// must leave from that SAME IP or the CDN 403s. If filmix resolves through a SOCKS proxy, the
	// proxy fetch has to use it too — else the hash is bound to the proxy IP but we'd fetch direct.
	client := h.client
	if plugin != "" && httpclient.TransportForBalancer(plugin) != nil {
		client = httpclient.NewForBalancerNoRedirect(plugin, 45*time.Second)
	}

	if err := h.passThrough(w, r, target, client); err != nil {
		log.Error().Err(err).Str("path", r.URL.Path).Str("target", target).Msg("proxy passthrough failed")
		if isClientCancelErr(err) {
			return
		}
		http.Error(w, "proxy upstream unavailable", http.StatusBadGateway)
	}
}

func (h *Handler) fallback(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

func (h *Handler) passThrough(w http.ResponseWriter, r *http.Request, target string, client *http.Client) error {
	if client == nil {
		client = h.client
	}
	u, err := url.Parse(target)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), r.Body)
	if err != nil {
		return err
	}

	copyHeaders(req.Header, r.Header)
	req.Host = u.Host
	req.Header.Set("Host", u.Host)
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Diagnostic: the proxy relays the upstream status verbatim, so a 4xx the client sees here is the
	// SOURCE/CDN's, not ours. Surface the "blocked" statuses with the upstream host so we can see which
	// source breaks playback (vs. our own decrypt failures, which return 404/502 from ServeProxy).
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusUnavailableForLegalReasons:
		log.Warn().Int("status", resp.StatusCode).Str("upstream", u.Host).Str("path", u.Path).Msg("proxy: upstream blocked playback")
	}

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}

func decodeDirectTarget(pathPart, rawQuery string) (string, bool) {
	pathPart = strings.TrimSpace(pathPart)
	if pathPart == "" {
		return "", false
	}

	if i := strings.Index(pathPart, " or "); i > 0 {
		pathPart = strings.TrimSpace(pathPart[:i])
	}

	candidates := []string{pathPart}
	if unescaped, err := url.PathUnescape(pathPart); err == nil && unescaped != pathPart {
		candidates = append(candidates, unescaped)
	}
	if unescaped, err := url.QueryUnescape(pathPart); err == nil && unescaped != pathPart {
		candidates = append(candidates, unescaped)
	}

	for _, cand := range candidates {
		cand = strings.TrimSpace(cand)
		if !strings.HasPrefix(cand, "http://") && !strings.HasPrefix(cand, "https://") {
			continue
		}
		if rawQuery != "" {
			if strings.Contains(cand, "?") {
				cand += "&" + rawQuery
			} else {
				cand += "?" + rawQuery
			}
		}
		return cand, true
	}

	return "", false
}

func copyHeaders(dst, src http.Header) {
	for k, vals := range src {
		if _, skip := hopHeaders[k]; skip {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func isClientCancelErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") || strings.Contains(msg, "broken pipe")
}

func requestIP(r *http.Request) string {
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xf != "" {
		if i := strings.Index(xf, ","); i > 0 {
			return strings.TrimSpace(xf[:i])
		}
		return xf
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
