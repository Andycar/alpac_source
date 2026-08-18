package sisihttp

import (
	"net/http"
	"strings"

	"lampac-go/internal/jsmodules"
	"lampac-go/internal/proxylink"
)

// sisiCustHandler routes /sisi/cust/{name}[/*] requests to JS SISI sources
// (goja).
func sisiCustHandler(
	jsMgr *jsmodules.Manager,
	proxyLinks *proxylink.Manager,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/sisi/cust/")
		parts := strings.SplitN(rest, "/", 2)
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		subpath := ""
		if len(parts) > 1 {
			subpath = strings.TrimRight(parts[1], "/")
		}

		if jsMgr != nil {
			if mod, ok := jsMgr.Get(name); ok && mod.Enabled {
				sisiJSInvoke(w, r, jsMgr, name, subpath, proxyLinks)
				return
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{"list": []any{}, "total_pages": 1})
	}
}

// sisiJSInvoke invokes a single JS SISI module and writes the response.
// subpath "video" or "vidosik" → action=video; anything else → action=list.
func sisiJSInvoke(
	w http.ResponseWriter,
	r *http.Request,
	mgr *jsmodules.Manager,
	name, subpath string,
	proxyLinks *proxylink.Manager,
) {
	action := "list"
	if subpath == "video" || subpath == "vidosik" {
		action = "video"
	}

	// Build query map from URL query params, then inject action.
	q := make(map[string]string, len(r.URL.Query())+1)
	for k, vals := range r.URL.Query() {
		if len(vals) > 0 {
			q[k] = vals[0]
		}
	}
	q["action"] = action

	inv := jsmodules.Invocation{
		Query:     q,
		Host:      hostFromRequest(r),
		RequestIP: clientIP(r),
		Path:      r.URL.Path,
	}

	resp, err := mgr.Invoke(r.Context(), name, inv)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"list": []any{}, "total_pages": 1})
		return
	}

	body := resp.Body

	// Video responses: inject proxied quality URLs so CDNs requiring
	// custom Origin/Referer headers serve through /proxy/{aes}.
	if action == "video" && proxyLinks != nil {
		body = injectQualitysProxyURLs(body, proxyLinks, "sisi_"+name, clientIP(r), hostFromRequest(r))
	}

	// List responses: wrap raw upstream poster URLs through /proxyimg/ —
	// adult CDNs are RKN-blocked / hotlink-guarded, so unproxied pictures
	// render as blank tiles on the client.
	if action == "list" && proxyLinks != nil {
		body = injectSisiPosterProxy(body, proxyLinks, "sisi_"+name, clientIP(r), hostFromRequest(r))
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
