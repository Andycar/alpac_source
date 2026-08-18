package proxyapi

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

// Zona's TAKEDWN / REZKA extractor returns time-boxed CDN URLs that only
// work when rewritten through the server's `/x-en-x/<cipher>` path. The
// player's service worker normally handles this — it installs a JS function
// `transformUrl` that applies a substitution-cipher over base64-encoded
// "hours/path?query" strings. We port that function to Go so we can cipher
// segment URLs inside our /proxy/ handler instead of running a browser SW.
//
// The transform the player installs looks like:
//
//	"use strict";
//	var E = "DlChEXitLONYRkFjAsnBbymWzSHMqKPgQZpvwerofJTVdIuUcxaG"; // subst alphabet
//	var L = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"; // base alphabet
//	var P = "1776262615";                                            // baked timestamp
//	var m = Date.now();
//	var g = 1000*P - m;
//	function b(n) {
//	    if (n.includes("/x-en-x/")) return n;
//	    var r = Date.now() + g;                 // ≈ server time (ms)
//	    var e = new URL(n);
//	    return e.origin + "/x-en-x/" +
//	           w(btoa(Math.round(r/1000/60/60) + "/" + e.pathname + e.search));
//	}
//
// The crucial parts for us:
//   * P is baked into the JS at stream-resolve time by zona's backend. For
//     every call to b(), `hours` = round(P / 3600). That value is stable
//     for the whole hour that P falls in — so the cipher is deterministic
//     per resolve session.
//   * E is the substitution alphabet; each char of base64 output in range
//     A-Za-z is mapped via E[L.index(c)]. Non-letter chars (digits, '/',
//     '+', '=') pass through.
//   * e.pathname always starts with a leading '/' — the ptr passed to
//     btoa therefore contains a double slash right after the hour count
//     ("<hours>//<path>…").
//
// Both P and E can in principle vary per-session; we capture them from the
// urlTransform string zona embeds in every TAKEDWN stream.

// Private proxylink header names used to carry cipher state between the
// zona balancer and the /proxy/ handler. The proxy strips them before
// sending the upstream request so they never leak to the CDN. They are
// exported so that internal/httpapi/zona_browser.go can stamp them on the
// stream metadata without duplicating the constant.
const (
	ZonaCipherHoursHeader    = "X-Zona-Cipher-Hours"
	ZonaCipherAlphabetHeader = "X-Zona-Cipher-E"
)

// ZonaCipherParams holds the per-session cipher state extracted from
// urlTransform. Exported so the httpapi package can populate it from the
// urlTransform JS string returned by getStreams.
type ZonaCipherParams struct {
	Hours    int64  // round(P / 3600)
	Alphabet string // substitution alphabet E
}

// zonaBaseAlphabet is the hardcoded "L" constant — both JS and Go stay in
// sync here because it's just uppercase + lowercase ASCII letters.
const zonaBaseAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// zonaTakedwnTsRe matches the path extension of a URL to decide if the
// cipher needs to be applied before fetching it upstream. Mirrors what
// the service worker (/sw-streams.js) intercepts. Playlists (.m3u8) do
// NOT require ciphering — the CDN serves them directly.
var zonaTakedwnTsRe = regexp.MustCompile(`(?i)\.(?:ts|m4s|webm|aac|mpd|mp4)(?:$|[?#])`)

// ZonaTransformURL applies the /x-en-x/<cipher> substitution to a raw
// upstream URL. If the URL already contains "/x-en-x/" the function
// returns it unchanged (matches the JS `v(n)` guard). On any parse
// failure we return the original URL — we'd rather attempt a direct fetch
// and fall back gracefully than 500.
func ZonaTransformURL(rawURL string, p ZonaCipherParams) string {
	if p.Hours == 0 || p.Alphabet == "" {
		return rawURL
	}
	if strings.Contains(rawURL, "/x-en-x/") {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return rawURL
	}
	// Replicate JS: e.pathname (leading '/') + e.search (leading '?' or '').
	var sb strings.Builder
	sb.Grow(64)
	sb.WriteString(strconv.FormatInt(p.Hours, 10))
	sb.WriteByte('/')
	sb.WriteString(u.Path)
	if u.RawQuery != "" {
		sb.WriteByte('?')
		sb.WriteString(u.RawQuery)
	}
	plain := sb.String()
	// btoa() uses standard base64 with padding.
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	cipher := zonaSubstituteCipher(encoded, p.Alphabet)
	return u.Scheme + "://" + u.Host + "/x-en-x/" + cipher
}

// zonaSubstituteCipher runs the L → E character map over a string,
// passing through any char not in L (digits, '+', '/', '=' from base64).
func zonaSubstituteCipher(s, alphabet string) string {
	if len(alphabet) < len(zonaBaseAlphabet) {
		// Unexpected short alphabet — pass through.
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		idx := strings.IndexByte(zonaBaseAlphabet, c)
		if idx < 0 || idx >= len(alphabet) {
			sb.WriteByte(c)
			continue
		}
		sb.WriteByte(alphabet[idx])
	}
	return sb.String()
}

// Regexes for pulling the baked constants out of the urlTransform JS.
// The JS compresses variable declarations with comma lists
// (`var m=...,P="...",g=...`) so we can't rely on a leading `var` before
// each identifier — we anchor on a word boundary instead.
var (
	zonaCipherAlphabetRe  = regexp.MustCompile(`\bE\s*=\s*"([A-Za-z]{40,})"`)
	zonaCipherTimestampRe = regexp.MustCompile(`\bP\s*=\s*"(\d{8,})"`)
)

// ZonaParseCipherParams pulls P and E out of the urlTransform JS. Returns
// zero-valued params on any mismatch — callers treat that as "no cipher
// available" and return URLs unchanged.
func ZonaParseCipherParams(js string) ZonaCipherParams {
	var p ZonaCipherParams
	if m := zonaCipherAlphabetRe.FindStringSubmatch(js); len(m) == 2 {
		p.Alphabet = m[1]
	}
	if m := zonaCipherTimestampRe.FindStringSubmatch(js); len(m) == 2 {
		if ts, err := strconv.ParseInt(m[1], 10, 64); err == nil && ts > 0 {
			// hours = round(P / 3600).
			p.Hours = (ts + 1800) / 3600
		}
	}
	return p
}

// zonaNeedsCipher reports whether a URL's path/extension suggests it must
// go through the /x-en-x/ cipher. Playlists (.m3u8) do NOT need it — the
// CDN serves them directly.
func zonaNeedsCipher(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return zonaTakedwnTsRe.MatchString(u.Path)
}

// applyZonaCipher inspects the proxylink meta and, if this is a TAKEDWN
// segment URL with cipher params attached, returns the ciphered URL.
//
// The meta.headers map is NOT mutated — cipher pseudo-headers must still
// be present when rewriteM3U re-encrypts sub-URLs so that every segment
// URL carries the params forward. Removing the pseudo-headers from the
// upstream HTTP request itself is handled inside fetchWithMeta by
// zonaCipherHeaderBlacklist.
//
// Non-TAKEDWN streams (MOBILINK, HDVB) pass through untouched because
// their meta never gets the cipher headers stamped on it.
func applyZonaCipher(target string, meta *linkMeta) string {
	if meta == nil || len(meta.headers) == 0 {
		return target
	}
	hoursStr, ok := meta.headers[ZonaCipherHoursHeader]
	if !ok {
		return target
	}
	alphabet := meta.headers[ZonaCipherAlphabetHeader]
	if !zonaNeedsCipher(target) {
		return target
	}
	hours, err := strconv.ParseInt(hoursStr, 10, 64)
	if err != nil || hours == 0 || alphabet == "" {
		log.Warn().Err(err).Int64("hours", hours).Int("alphaLen", len(alphabet)).Msg("zona cipher: bad params")
		return target
	}
	out := ZonaTransformURL(target, ZonaCipherParams{Hours: hours, Alphabet: alphabet})
	log.Debug().
		Str("in", truncURL(target, 120)).
		Str("out", truncURL(out, 120)).
		Int64("hours", hours).
		Msg("zona cipher: applied")
	return out
}

// isZonaCipherHeader reports whether a proxylink meta.headers key is one of
// our private pseudo-headers (prefix: X-Zona-Cipher-). These must never be
// forwarded to the upstream CDN — fetchWithMeta skips them when copying
// meta.headers onto the outgoing request.
func isZonaCipherHeader(name string) bool {
	return strings.EqualFold(name, ZonaCipherHoursHeader) ||
		strings.EqualFold(name, ZonaCipherAlphabetHeader)
}

func truncURL(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// applyPreFetchRewrite is the per-plugin URL rewriter invoked by the proxy
// handler immediately before opening an upstream request. It picks the
// right transformation based on meta.plugin and any pseudo-headers carried
// in the proxylink payload. Currently only zona/TAKEDWN segments need a
// transform; other plugins are no-ops.
//
// Headers stored on `meta.headers` are mutated in-place — `meta` is passed
// by value but the map field is a reference type, so the caller observes
// the cleanup of any X-Zona-Cipher-* pseudo-headers and they never leak
// into the upstream request's actual HTTP headers.
func applyPreFetchRewrite(target string, meta linkMeta) string {
	switch strings.ToLower(strings.TrimSpace(meta.plugin)) {
	case "zona":
		return applyZonaCipher(target, &meta)
	case "filmix", "filmixtv":
		out := fixFilmixDoubleQuery(target)
		// Greppable filmix marker: `journalctl -u lampac -f | grep filmix` shows the EXACT
		// upstream url we are about to fetch, whether the double-"?" was present, the UA the
		// token carries, and whether we egress via the balancer proxy (xray). Together with
		// "proxy: upstream blocked playback" (plugin=filmix) this pins the 403 cause.
		evt := log.Warn().
			Str("plugin", meta.plugin).
			Str("upstream", target).
			Bool("double_query_fixed", out != target).
			Str("token_ua", meta.headers["User-Agent"]).
			Int("token_headers", len(meta.headers))
		if out != target {
			evt = evt.Str("rewritten", out)
		}
		evt.Msg("proxy: filmix upstream")
		return out
	}
	return target
}

// fixFilmixDoubleQuery normalizes filmix's inline-origin double-"?" CDN url right before the
// upstream fetch — the single chokepoint, so it catches the bad url no matter which resolve path
// or cache produced it:
//
//	https://nl03.werkecdn.me/hls/.../Teen.Spirit_1080.mp4?vs3-origin/index.m3u8?hash=H
//	→ https://nl03.werkecdn.me/hls/.../Teen.Spirit_1080.mp4/index.m3u8?vs3-origin&hash=H
//
// On the wire the path truncates at the FIRST "?", so the CDN never sees "/index.m3u8" and its
// secure_link hash (computed over ".../index.m3u8") fails → 403. Idempotent: a clean single-"?"
// url (segments, normal index.m3u8) is returned unchanged.
func fixFilmixDoubleQuery(u string) string {
	i := strings.IndexByte(u, '?')
	if i < 0 {
		return u
	}
	rest := u[i+1:]
	slash := strings.IndexByte(rest, '/') // path continues AFTER the first query → the double-? signature
	if slash < 0 {
		return u
	}
	q2 := strings.IndexByte(rest, '?')
	if q2 < 0 || q2 < slash {
		return u // no second "?", or the "?" precedes the "/" → ordinary url
	}
	originQ := rest[:slash]  // "vs3-origin"
	pathAndQ := rest[slash:] // "/index.m3u8?hash=H"
	j := strings.IndexByte(pathAndQ, '?')
	return u[:i] + pathAndQ[:j] + "?" + originQ + "&" + pathAndQ[j+1:]
}
