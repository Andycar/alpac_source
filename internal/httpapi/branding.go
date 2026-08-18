package httpapi

import (
	stdjson "encoding/json"
	"regexp"
	"strings"
	"sync/atomic"
)

// Branding holds the runtime-customisable brand strings injected into
// plugin scripts and HTML pages. Defaults match the historical hard-
// coded values so a fresh install renders identically to before.
//
// Loaded from [branding] in config.toml, then optionally overridden at
// runtime by database/branding.json (admin UI writes here without a
// restart, like healthcheck/security settings).
type Branding struct {
	Name          string // global brand (replaces "Lampac" in JS)
	Version       string // string in window.lampac_version
	Major         int    // numeric major for lampac_version
	Minor         int    // numeric minor for lampac_version
	OnlineNameRU  string // title_online["ru"]
	OnlineNameUK  string // title_online["uk"]
	OnlineNameEN  string // title_online["en"]
	OnlineNameZH  string // title_online["zh"]
	HTMLTitleV2   string // <title> for lampa-v2last/lampa-main
	HTMLTitleLite string // <title> for lampa-lite
}

var (
	brandingStore atomic.Pointer[Branding]

	// Defaults preserve the pre-config behaviour byte-for-byte. Don't change
	// these without thinking about it — they're the fallback when neither
	// config.toml [branding] nor database/branding.json is set.
	brandingDefaults = &Branding{
		Name:          "Alpac",
		Version:       "0.5",
		Major:         153,
		Minor:         0,
		OnlineNameRU:  "Alpac Онлайн",
		OnlineNameUK:  "Alpac Онлайн",
		OnlineNameEN:  "Alpac Online",
		OnlineNameZH:  "Alpac 在线",
		HTMLTitleV2:   "Alpac - Каталог фильмов и сериалов",
		HTMLTitleLite: "Alpac",
	}
)

func init() { brandingStore.Store(brandingDefaults) }

// currentBranding returns the active Branding snapshot. Never nil.
func currentBranding() *Branding {
	if p := brandingStore.Load(); p != nil {
		return p
	}
	return brandingDefaults
}

// SetBranding atomically swaps in a new Branding. Empty fields fall back
// to defaults so partial overrides (e.g. only Name set) work.
func SetBranding(b Branding) {
	merged := *brandingDefaults
	if v := strings.TrimSpace(b.Name); v != "" {
		merged.Name = v
	}
	if v := strings.TrimSpace(b.Version); v != "" {
		merged.Version = v
	}
	if b.Major > 0 {
		merged.Major = b.Major
	}
	if b.Minor > 0 {
		merged.Minor = b.Minor
	}
	if v := strings.TrimSpace(b.OnlineNameRU); v != "" {
		merged.OnlineNameRU = v
	}
	if v := strings.TrimSpace(b.OnlineNameUK); v != "" {
		merged.OnlineNameUK = v
	}
	if v := strings.TrimSpace(b.OnlineNameEN); v != "" {
		merged.OnlineNameEN = v
	}
	if v := strings.TrimSpace(b.OnlineNameZH); v != "" {
		merged.OnlineNameZH = v
	}
	if v := strings.TrimSpace(b.HTMLTitleV2); v != "" {
		merged.HTMLTitleV2 = v
	}
	if v := strings.TrimSpace(b.HTMLTitleLite); v != "" {
		merged.HTMLTitleLite = v
	}
	brandingStore.Store(&merged)
}

// Convenience getters keep call sites readable.
func publicBrandName() string     { return currentBranding().Name }
func publicBrandVersion() string  { return currentBranding().Version }
func publicBrandMajor() int       { return currentBranding().Major }
func publicBrandMinor() int       { return currentBranding().Minor }
func publicVersionText() string   { return publicBrandName() + " " + publicBrandVersion() }
func publicOnlineNameRU() string  { return currentBranding().OnlineNameRU }
func publicHTMLTitleV2() string   { return currentBranding().HTMLTitleV2 }
func publicHTMLTitleLite() string { return currentBranding().HTMLTitleLite }

var (
	onlineManifestVersionSingleRe = regexp.MustCompile(`(?m)(version:\s*)'[^']*'`)
	onlineManifestVersionDoubleRe = regexp.MustCompile(`(?m)(version:\s*)"[^"]*"`)

	// titleOnlineBlockRe spans the entire title_online object literal so we
	// can rewrite all four locales atomically. The pattern is intentionally
	// non-greedy and tolerant of trailing-comma variations.
	titleOnlineBlockRe = regexp.MustCompile(`title_online:\s*\{[^}]*\}`)

	// htmlTitleTagRe matches "<title>...anything...</title>" regardless of
	// the current content. This lets us rewrite a customised index.html
	// where the upstream string is no longer "Alpac …" (e.g. operators who
	// already shipped their own brand).
	htmlTitleTagRe = regexp.MustCompile(`(?is)<title>[^<]*</title>`)
)

// applyPublicBrandingJS rewrites "Lampac" → Branding.Name everywhere, plus
// rewrites the title_online {ru,uk,en,zh} block and the manifest version
// in online.js. Returns src unchanged when empty.
func applyPublicBrandingJS(src string) string {
	if src == "" {
		return src
	}
	b := currentBranding()

	out := src
	// HTTP header names like "X-Lampac-Token" must survive the brand rewrite
	// verbatim: the server middleware (and CORS allow-list) match them literally,
	// so a blanket "Lampac"→Name replace would corrupt them. Worse, when
	// Name == "Alpac" it rewrites "X-Lampac-Token" into "X-Alpac-Token" — colliding
	// with the sibling "X-Alpac-Token" key in online.js (lampacAuthHeaders). Two
	// identical keys in one object literal are legal in ES2015+ but a SyntaxError
	// under ES5 strict mode, so old webOS/Tizen browsers fail to parse the whole
	// plugin ("SyntaxError: Unexpected token '}'") and online stops working at
	// launch. Shield the X-Lampac- header family across the brand rewrite, restore
	// it after.
	const lampacHdrShield = "\x00X-LAMPAC-HDR\x00"
	out = strings.ReplaceAll(out, "X-Lampac-", lampacHdrShield)
	out = strings.ReplaceAll(out, "Lampac", b.Name)
	out = strings.ReplaceAll(out, lampacHdrShield, "X-Lampac-")
	out = strings.ReplaceAll(out, "title: 'Lampac - '", "title: '"+b.Name+" - '")
	out = strings.ReplaceAll(out, "name: 'Lampac'", "name: '"+b.Name+"'")

	// Online plugin advertises its own version in the manifest object.
	if strings.Contains(out, "window.lampac_plugin = true") {
		out = onlineManifestVersionSingleRe.ReplaceAllString(out, "${1}'"+b.Version+"'")
		out = onlineManifestVersionDoubleRe.ReplaceAllString(out, `${1}"`+b.Version+`"`)
	}

	// title_online: 4 locales. Replace the whole block so per-locale edits
	// stay coherent even if the JS source order changes. Only fires when the
	// block exists, leaving unrelated scripts alone.
	if titleOnlineBlockRe.MatchString(out) {
		replacement := "title_online: { ru: '" + jsEscapeSingle(b.OnlineNameRU) +
			"', uk: '" + jsEscapeSingle(b.OnlineNameUK) +
			"', en: '" + jsEscapeSingle(b.OnlineNameEN) +
			"', zh: '" + jsEscapeSingle(b.OnlineNameZH) + "' }"
		out = titleOnlineBlockRe.ReplaceAllString(out, replacement)
	}

	return out
}

// applyPublicBrandingHTML rewrites <title> (and the stock og:title form) in
// lampa-*/index.html. `variant` is "v2" for lampa-v2last / lampa-main,
// "lite" for lampa-lite. Other variants get the v2 title (it's the more
// descriptive default).
//
// <title> is rewritten through a regex so the substitution works even when
// the on-disk index.html no longer carries the upstream "Alpac …" string
// (e.g. an operator already shipped a custom build). og:title is replaced
// only when it matches the upstream form, since attribute ordering varies
// across hand-edited HTML and we don't want to wreck unrelated <meta> tags.
func applyPublicBrandingHTML(src, variant string) string {
	if src == "" {
		return src
	}
	b := currentBranding()
	title := b.HTMLTitleV2
	if variant == "lite" {
		title = b.HTMLTitleLite
	}
	escaped := htmlEscape(title)
	out := htmlTitleTagRe.ReplaceAllString(src, "<title>"+escaped+"</title>")
	out = strings.ReplaceAll(out, `content="Alpac - Каталог фильмов и сериалов"`, `content="`+escaped+`"`)
	return out
}

// jsEscapeSingle escapes single quotes and backslashes for embedding inside
// a JS single-quoted string literal. Newlines are turned into spaces — the
// title strings are short labels, never multiline.
func jsEscapeSingle(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// jsonStringLiteral returns s as a properly-quoted JSON string literal
// (with embedded quotes), so it can be slotted into hand-written JSON
// blobs without breaking on Unicode or special characters.
func jsonStringLiteral(s string) string {
	b, err := stdjson.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
