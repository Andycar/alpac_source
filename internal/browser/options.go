package browser

import "time"

// SessionOptions configures a new browser session. Engines ignore
// fields they cannot honour (e.g. ExtraFlags is Chromium-only).
type SessionOptions struct {
	// UserDataDir is the on-disk profile directory. Empty means an
	// ephemeral profile (deleted on session close).
	UserDataDir string

	// UserAgent overrides navigator.userAgent. Empty means engine
	// default.
	UserAgent string

	// SocksProxy is a "host:port" SOCKS5 proxy. Empty means direct.
	SocksProxy string

	// Headless forces headless mode. Default true.
	Headless bool

	// InitScripts run on every new document before page scripts (used
	// for stealth patches like mirageStealthJS).
	InitScripts []string

	// ExtraFlags are Chromium command-line flags appended verbatim.
	// Ignored by non-Chromium engines.
	ExtraFlags []string

	// NavigationTimeout caps Session.Navigate. Zero means engine
	// default (typically 30s).
	NavigationTimeout time.Duration

	// IgnoreCertErrors disables TLS verification for the browser.
	IgnoreCertErrors bool

	// UseStealth injects github.com/go-rod/stealth's stealth.JS as an
	// init script (port of puppeteer-extra-plugin-stealth). Implemented
	// by the rod engine natively; the chromedp engine applies it
	// alongside any caller-supplied InitScripts. Helps against
	// fingerprint-based bot detection (sannysoft, fpscanner) but does
	// NOT bypass Cloudflare JS-challenge or Turnstile.
	UseStealth bool
}

// withDefaults returns a copy of opts with sensible defaults applied.
func (o SessionOptions) withDefaults() SessionOptions {
	if o.NavigationTimeout == 0 {
		o.NavigationTimeout = 30 * time.Second
	}
	return o
}
