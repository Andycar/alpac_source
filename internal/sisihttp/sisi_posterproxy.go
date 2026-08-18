package sisihttp

import (
	"bytes"
	stdjson "encoding/json"
	"net/url"
	"strings"

	"lampac-go/internal/proxylink"
)

// injectQualitysProxyURLs walks the JSON response from a custom SISI source
// and adds proxied variants of every stream URL.
//
// View payloads: when "qualitys" is a string→URL map, a sibling
// "qualitys_proxy" map is added where each URL is wrapped through
// /proxy/{aes}. Optional "qualitys_headers" lets the source attach
// custom upstream headers per quality (Origin, Referer, Authorization)
// — these are embedded in the AES payload via EncryptURIWithHeaders.
//
// Returns the input bytes unchanged when JSON parsing fails or the
// payload doesn't contain a qualitys map.
func injectQualitysProxyURLs(body []byte, proxyLinks *proxylink.Manager, plugin, clientIP, host string) []byte {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return body
	}
	var raw map[string]any
	if err := stdjson.Unmarshal(body, &raw); err != nil {
		return body
	}

	qsAny, hasQ := raw["qualitys"]
	if !hasQ {
		return body
	}
	qs, _ := qsAny.(map[string]any)
	if len(qs) == 0 {
		return body
	}

	headersAll, _ := raw["qualitys_headers"].(map[string]any)
	proxied := make(map[string]any, len(qs))

	for q, v := range qs {
		uri := strings.TrimSpace(toString(v))
		if uri == "" {
			continue
		}

		var hdrs map[string]string
		if hraw, ok := headersAll[q].(map[string]any); ok && len(hraw) > 0 {
			hdrs = make(map[string]string, len(hraw))
			for hk, hv := range hraw {
				if s, ok := hv.(string); ok && s != "" {
					hdrs[hk] = s
				}
			}
		}

		var hash string
		if len(hdrs) > 0 {
			hash = proxyLinks.EncryptURIWithHeaders(uri, clientIP, plugin, hdrs)
		} else {
			hash = proxyLinks.EncryptURI(uri, clientIP, plugin, true, false, false)
		}
		if hash == "" {
			continue
		}
		if host != "" {
			proxied[q] = host + "/proxy/" + hash
		} else {
			proxied[q] = "/proxy/" + hash
		}
	}

	if len(proxied) > 0 {
		raw["qualitys_proxy"] = proxied
	}
	// qualitys_headers is internal protocol — strip from outgoing JSON.
	delete(raw, "qualitys_headers")

	out, err := stdjson.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}

// injectSisiPosterProxy walks a SISI list payload and wraps every poster
// URL through /proxyimg/{aes}. Adult CDNs are typically RKN-blocked and/or
// hotlink-guarded, so the TV can't fetch them directly — every custom
// source (JS module) that emitted raw upstream picture URLs rendered as
// blank tiles. /proxyimg/ also brings the LRU + disk cache + on-demand
// resize pipeline. A Referer matching the image's own origin rides in the
// AES payload to pass same-origin hotlink checks.
//
// Only absolute http(s) URLs pointing AWAY from our host are wrapped;
// already-proxied and relative URLs pass through. No-op for payloads
// without a "list" array (e.g. video responses) or on parse failure.
func injectSisiPosterProxy(body []byte, proxyLinks *proxylink.Manager, plugin, clientIP, host string) []byte {
	if proxyLinks == nil {
		return body
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body
	}
	var raw map[string]any
	if err := stdjson.Unmarshal(trimmed, &raw); err != nil {
		return body
	}
	listAny, ok := raw["list"].([]any)
	if !ok || len(listAny) == 0 {
		return body
	}

	wrap := func(uri string) string {
		uri = strings.TrimSpace(uri)
		if uri == "" || !strings.HasPrefix(uri, "http") {
			return uri
		}
		if host != "" && strings.HasPrefix(uri, host) {
			return uri // already served by us
		}
		hdrs := map[string]string{}
		if u, err := url.Parse(uri); err == nil && u.Scheme != "" && u.Host != "" {
			hdrs["Referer"] = u.Scheme + "://" + u.Host + "/"
		}
		hash := proxyLinks.EncryptURIWithHeaders(uri, clientIP, plugin, hdrs)
		if hash == "" {
			return uri
		}
		if host != "" {
			return host + "/proxyimg/" + hash
		}
		return "/proxyimg/" + hash
	}

	changed := false
	for _, itAny := range listAny {
		it, ok := itAny.(map[string]any)
		if !ok {
			continue
		}
		if pic := strings.TrimSpace(toString(it["picture"])); pic != "" {
			if w := wrap(pic); w != pic {
				it["picture"] = w
				changed = true
			}
		}
		// bookmark.image is what the client's bookmarks page renders later —
		// leave it raw and every saved bookmark shows a blank tile too.
		if bm, ok := it["bookmark"].(map[string]any); ok {
			if img := strings.TrimSpace(toString(bm["image"])); img != "" {
				if w := wrap(img); w != img {
					bm["image"] = w
					changed = true
				}
			}
		}
	}
	if !changed {
		return body
	}
	out, err := stdjson.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}
