package wasmmodules

import (
	"net/url"
	"path"
	"strings"
)

// Permission grammar:
//
//   "http:*"               — any HTTP/HTTPS request (no allowlist)
//   "http:example.com"     — exact host match
//   "http:*.example.com"   — wildcard subdomain (matches a.example.com,
//                            does NOT match example.com itself)
//   "cache"                — host_cache_get/set allowed
//   "proxy"                — host_proxy_url allowed
//   "log"                  — host_log allowed (always granted; here for
//                            symmetry with documentation)
//   "config"               — host_config allowed (always granted)
//
// A plugin with no `permissions` field defaults to legacy "everything"
// access — preserves backward-compatibility with the demo plugin and any
// pre-permission manifests. Once an explicit list is set, requests that
// don't match are rejected with "permission denied".

type permissionSet struct {
	allHTTP    bool
	httpHosts  []string // exact hosts
	httpSuffix []string // wildcard ".example.com" → suffix match
	allowAll   bool     // empty list → permissive default
	cache      bool
	proxy      bool
}

func newPermissionSet(perms []string) *permissionSet {
	p := &permissionSet{}
	if len(perms) == 0 {
		p.allowAll = true
		return p
	}
	for _, raw := range perms {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		switch raw {
		case "cache":
			p.cache = true
			continue
		case "proxy":
			p.proxy = true
			continue
		case "log", "config":
			// Always granted — listing is documentation.
			continue
		}
		if strings.HasPrefix(raw, "http:") {
			host := strings.TrimSpace(strings.TrimPrefix(raw, "http:"))
			switch {
			case host == "" || host == "*":
				p.allHTTP = true
			case strings.HasPrefix(host, "*."):
				p.httpSuffix = append(p.httpSuffix, strings.ToLower(host[1:])) // ".example.com"
			default:
				p.httpHosts = append(p.httpHosts, strings.ToLower(host))
			}
		}
	}
	return p
}

// allowHTTP reports whether the given URL is reachable for the plugin.
func (p *permissionSet) allowHTTP(rawURL string) (bool, string) {
	if p.allowAll || p.allHTTP {
		return true, ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, "invalid URL: " + err.Error()
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false, "no host in URL"
	}
	for _, h := range p.httpHosts {
		if h == host {
			return true, ""
		}
		// Allow exact wildcards via direct host match; pattern matching
		// (path.Match) is overkill for the simple grammar above.
		if matched, _ := path.Match(h, host); matched {
			return true, ""
		}
	}
	for _, suf := range p.httpSuffix {
		if strings.HasSuffix(host, suf) {
			return true, ""
		}
	}
	return false, "host " + host + " not in allowlist"
}

func (p *permissionSet) allowCache() bool { return p.allowAll || p.cache }
func (p *permissionSet) allowProxy() bool { return p.allowAll || p.proxy }
