package litesrc

import (
	"context"
	stdjson "encoding/json"
	"net/url"
	"strings"

	"github.com/chromedp/cdproto/network"
	"github.com/rs/zerolog/log"
)

// setBrowserAuthCookies injects the current auth cookies into a chromedp context.
// Used by browser-pool methods before navigation.
func (r *pidorezkaChecker) setBrowserAuthCookies(ctx context.Context) {
	cookie := strings.TrimSpace(r.currentAuthCookie())
	if cookie == "" {
		return
	}
	u, err := url.Parse(r.host)
	if err != nil || u.Hostname() == "" {
		return
	}
	domain := u.Hostname()
	secure := strings.EqualFold(u.Scheme, "https")

	// Ensure basic navigation cookies are set.
	_ = network.SetCookie("hdmbbs", "1").
		WithDomain(domain).WithPath("/").WithSecure(secure).Do(ctx)
	_ = network.SetCookie("dle_user_taken", "1").
		WithDomain(domain).WithPath("/").WithSecure(secure).Do(ctx)

	for part := range strings.SplitSeq(cookie, ";") {
		part = strings.TrimSpace(part)
		if part == "" || !strings.Contains(part, "=") {
			continue
		}
		eq := strings.Index(part, "=")
		name := strings.TrimSpace(part[:eq])
		value := strings.TrimSpace(part[eq+1:])
		if name == "" || value == "" {
			continue
		}
		_ = network.SetCookie(name, value).
			WithDomain(domain).
			WithPath("/").
			WithSecure(secure).
			Do(ctx)
	}
}

// movieFromBrowser is the legacy browser fallback wrapper.
// Now delegates to browserAjaxPost from the persistent pool.
func (r *pidorezkaChecker) movieFromBrowser(
	reqCtx context.Context,
	id, t, action, director, favs, s, e, href string,
) (string, string, bool) {
	raw, ok := r.browserAjaxPost(reqCtx, id, t, action, director, favs, s, e, href)
	if !ok {
		return "", "", false
	}

	var parsed struct {
		URL      any `json:"url"`
		Subtitle any `json:"subtitle"`
	}
	if err := stdjson.Unmarshal([]byte(raw), &parsed); err != nil {
		log.Debug().Err(err).Str("id", id).Int("bodyLen", len(raw)).Msg("pidorezka-browser: movieFromBrowser unmarshal failed")
		return "", "", false
	}
	urlStr, _ := parsed.URL.(string)
	subtitleStr, _ := parsed.Subtitle.(string)
	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" || strings.EqualFold(urlStr, "false") {
		return "", "", false
	}
	return urlStr, subtitleStr, true
}
