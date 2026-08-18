package sisihttp

import (
	"net/http"
	"net/url"
	"strings"
)

// sisiProxyQualitys builds /proxy/ URLs for SISI quality links to improve
// playback compatibility on devices with strict CORS behavior.
func sisiProxyQualitys(req *http.Request, qualitys map[string]any) map[string]any {
	if req == nil || len(qualitys) == 0 {
		return map[string]any{}
	}

	host := hostFromRequest(req)
	out := make(map[string]any, len(qualitys))
	for q, raw := range qualitys {
		u := strings.TrimSpace(toString(raw))
		if u == "" {
			continue
		}
		out[q] = host + "/proxy/" + url.QueryEscape(u)
	}
	return out
}

// sisiPreferProxyQualitys reports whether "qualitys" should prefer proxied links.
// Browser HLS players (hls.js) often require same-origin manifests due CORS.
func sisiPreferProxyQualitys(req *http.Request) bool {
	if req == nil {
		return false
	}
	if strings.TrimSpace(req.Header.Get("Sec-Fetch-Mode")) != "" {
		return true
	}
	if strings.TrimSpace(req.Header.Get("Origin")) != "" {
		return true
	}
	ua := strings.ToLower(strings.TrimSpace(req.UserAgent()))
	return strings.Contains(ua, "mozilla/") ||
		strings.Contains(ua, "chrome/") ||
		strings.Contains(ua, "safari/") ||
		strings.Contains(ua, "firefox/")
}
