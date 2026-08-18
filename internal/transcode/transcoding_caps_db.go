package transcode

import (
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// User-Agent → ClientCaps fallback database.
//
// Most Lampa clients (web, older Android builds, Smart-TV native apps) do
// not send a `client` capability payload to /transcoding/start.  Without
// it selectMode() can't pick the right pipeline and falls back to "play
// the source as-is", which produces the very symptoms users report on
// 4PDA / Telegram / GitHub:
//
//   - Tizen < 2020 → "звука нет" on EAC3/DTS multichannel MKV
//   - WebOS < 5     → "видео не найдено или повреждено" on HEVC content
//   - Apple TV pre-17 → no audio on EAC3 (jellyfin-web #2262, plex 212143)
//   - generic Smart-TV → black screen on 10-bit HEVC
//
// detectClientCapsFromUA() pattern-matches the User-Agent against a curated
// device profile DB and returns a conservative ClientCaps that smart-mode
// can use to pick the cheapest viable pipeline (usually remux / audio-only).
//
// When the request already carries an explicit `client` payload it wins;
// UA detection only fills in *missing* clients.  Explicit caps from a
// modern Lampa plugin always override UA heuristics.
// ---------------------------------------------------------------------------

// DeviceProfile describes a known client family — capabilities + a stable
// label exposed in /transcoding/start responses for diagnostics.
type DeviceProfile struct {
	// Label is a short identifier ("tizen-2018", "webos-5", "apple-tv-15",
	// "android-tv-generic", "chromecast", "browser-chrome").  Surfaced in
	// the start response and stats panel so operators can see why smart-mode
	// chose what it chose.
	Label string

	// Caps describes what the device reliably plays. Conservative on
	// purpose — it's better to remux unnecessarily than serve broken audio.
	Caps ClientCaps

	// KnownIssues is shown to the user via /transcoding/start when the
	// chosen mode is impacted by a documented platform bug.  Empty for
	// healthy profiles.
	KnownIssues []string
}

// detectClientCapsFromUA returns a DeviceProfile for the given User-Agent
// string. Always returns a usable profile — falls through to a conservative
// "unknown" baseline (H264 + AAC only) when nothing matches.
func detectClientCapsFromUA(ua string) DeviceProfile {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return profileUnknown()
	}

	uaLower := strings.ToLower(ua)

	// Model-specific overrides come BEFORE OS-version detection because a
	// known-bad model needs its workaround applied even when the rest of
	// the OS profile would otherwise pass.
	if override, ok := lookupModelOverride(uaLower); ok {
		return override
	}

	// Software-decoding standalone players (VLC / Kodi / mpv / Infuse / MX
	// Player / PotPlayer) play MKV plus every common codec natively — server
	// side transcoding for them is pure wasted CPU.  Checked before the OS /
	// browser branches because the player identity wins over the host OS.
	if label, ok := omnivorousPlayerLabel(uaLower); ok {
		return profileOmnivorousPlayer(label)
	}

	// Order matters: more specific matches first.

	// --- Samsung Tizen Smart-TV ---------------------------------------
	if strings.Contains(uaLower, "tizen") || (strings.Contains(uaLower, "smart-tv") && strings.Contains(uaLower, "samsung")) {
		return profileTizen(extractVersion(ua, reTizenVer))
	}

	// --- LG WebOS ------------------------------------------------------
	if strings.Contains(uaLower, "web0s") || strings.Contains(uaLower, "webos") {
		return profileWebOS(extractVersion(ua, reWebOSVer))
	}

	// --- Apple TV / tvOS -----------------------------------------------
	if strings.Contains(uaLower, "appletv") || strings.Contains(uaLower, "tvos") {
		return profileAppleTV(extractVersion(ua, reTvOSVer))
	}

	// --- iOS / iPadOS Safari --------------------------------------------
	if (strings.Contains(uaLower, "iphone") || strings.Contains(uaLower, "ipad") || strings.Contains(uaLower, "ipod")) && strings.Contains(uaLower, "safari") {
		return profileIOS(extractVersion(ua, reIOSVer))
	}

	// --- macOS Safari ---------------------------------------------------
	if strings.Contains(uaLower, "macintosh") && strings.Contains(uaLower, "safari") && !strings.Contains(uaLower, "chrome") {
		return profileMacSafari()
	}

	// --- Chromecast -----------------------------------------------------
	if strings.Contains(uaLower, "crkey") || strings.Contains(uaLower, "chromecast") {
		return profileChromecast()
	}

	// --- Android TV (boxes, including budget vendors named in research) -
	if isAndroidTVUA(uaLower) {
		return profileAndroidTV()
	}

	// --- Lampa native app (mobile/Android) ------------------------------
	if strings.Contains(uaLower, "lampa") {
		return profileLampaNative()
	}

	// --- Desktop / mobile browsers --------------------------------------
	if strings.Contains(uaLower, "chrome") || strings.Contains(uaLower, "chromium") {
		return profileChromeBrowser()
	}
	if strings.Contains(uaLower, "firefox") {
		return profileFirefoxBrowser()
	}
	if strings.Contains(uaLower, "safari") {
		return profileMacSafari() // iPad/iPhone caught above; this covers stragglers
	}

	// --- ffmpeg / curl / probe-ish UAs ----------------------------------
	if strings.Contains(uaLower, "ffmpeg") || strings.Contains(uaLower, "curl") || strings.Contains(uaLower, "wget") {
		// CLI / probe tools — no client decoding, force transcode-friendly defaults.
		p := profileUnknown()
		p.Label = "cli-probe"
		return p
	}

	return profileUnknown()
}

// ---------------------------------------------------------------------------
// Version extraction
// ---------------------------------------------------------------------------

// reTizenVer matches "Tizen 5.5", "Tizen 6", "Tizen/4.0".
var reTizenVer = regexp.MustCompile(`(?i)tizen[\s/]+(\d+)(?:\.(\d+))?`)

// reWebOSVer matches "WebOS 5.0", "webOS/4.5", "Web0S 6".
var reWebOSVer = regexp.MustCompile(`(?i)web0?os[\s/]+(\d+)(?:\.(\d+))?`)

// reTvOSVer matches "tvOS 17.2", "AppleTV/17", and the Apple-style
// "AppleTV6,2/15.5.1" / "AppleTV13,1/17.4" pattern (model number
// separated from tvOS version by a slash).
var reTvOSVer = regexp.MustCompile(`(?i)(?:tvos[\s/]+|appletv[\d,]*[\s/]+)(\d+)(?:[\._](\d+))?`)

// reIOSVer matches "OS 17_2 like Mac OS X", "iPhone OS 16_4".
var reIOSVer = regexp.MustCompile(`(?i)(?:os|iphone\s+os|ipad\s+os)\s+(\d+)[\._](\d+)`)

// extractVersion returns the major version int from a UA substring.
// Returns 0 when no match.
func extractVersion(ua string, re *regexp.Regexp) int {
	m := re.FindStringSubmatch(ua)
	if len(m) < 2 {
		return 0
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return v
}

// isAndroidTVUA returns true when the UA looks like an Android TV box.
// Lampa is heavily used on cheap boxes (X96, HD BOX, Mecool, TX*, Tanix,
// Ugoos) which all share the "Android" + "TV" + WebView pattern.
func isAndroidTVUA(ua string) bool {
	if !strings.Contains(ua, "android") {
		return false
	}
	keywords := []string{
		" tv ", "androidtv", "android tv", "smarttv", "smart-tv",
		"mibox", "mi box", "mecool", "x96", "hd box", "h96", "tanix",
		"ugoos", "shield", "bravia", "aft" /* Amazon Fire */, "aftn", "aftt",
		"chromecast", "google tv",
	}
	for _, k := range keywords {
		if strings.Contains(ua, k) {
			return true
		}
	}
	// Mobile Android — return false (treated as browser/Lampa app).
	return false
}

// ---------------------------------------------------------------------------
// Profile definitions
// ---------------------------------------------------------------------------

// omnivorousPlayerLabel detects software-decoding players that handle MKV and
// the full codec matrix natively.  Returns (label, true) on a match.
func omnivorousPlayerLabel(uaLower string) (string, bool) {
	switch {
	case strings.Contains(uaLower, "vlc") || strings.Contains(uaLower, "libvlc"):
		return "vlc", true
	case strings.Contains(uaLower, "kodi"):
		return "kodi", true
	case strings.Contains(uaLower, "infuse"):
		return "infuse", true
	case strings.Contains(uaLower, "potplayer"):
		return "potplayer", true
	case strings.Contains(uaLower, "mxplayer") || strings.Contains(uaLower, "mx player"):
		return "mxplayer", true
	case strings.Contains(uaLower, "libmpv") || strings.Contains(uaLower, "mpv/") || strings.HasPrefix(uaLower, "mpv "):
		return "mpv", true
	}
	return "", false
}

// profileOmnivorousPlayer is the full-capability profile for software players
// (VLC / Kodi / mpv …): everything plays natively, so selectMode short-circuits
// to native (direct URL, no ffmpeg).
func profileOmnivorousPlayer(label string) DeviceProfile {
	return DeviceProfile{
		Label: label,
		Caps: ClientCaps{
			Lang:          "ru",
			CanPlayMKV:    true,
			CanPlayH264:   true,
			CanPlayHEVC:   true,
			CanPlayHEVC10: true,
			CanPlayAV1:    true,
			CanPlayVP9:    true,
			CanPlayAAC:    true,
			CanPlayAC3:    true,
			CanPlayEAC3:   true,
			CanPlayDTS:    true,
			CanPlayTrueHD: true,
			CanPlayFLAC:   true,
			CanPlayOpus:   true,
			CanPlayMP3:    true,
		},
	}
}

// profileUnknown is the conservative baseline.  H264 + AAC only — every
// other codec triggers transcoding.  Used when the UA is empty or doesn't
// match any known profile.
func profileUnknown() DeviceProfile {
	return DeviceProfile{
		Label: "unknown",
		Caps: ClientCaps{
			Lang:        "ru",
			CanPlayMKV:  false,
			CanPlayH264: true,
			CanPlayAAC:  true,
			CanPlayMP3:  true,
		},
	}
}

// profileTizen returns a Samsung Tizen profile keyed by major version.
//
// References (from research pass on 4PDA, webos-forums, samsung docs):
//   - Tizen < 5: HEVC limited, no EAC3 in MKV, no DTS, no PGS subs.
//   - Tizen 5.x (2019-2020): HEVC 4K main profile OK, EAC3 unreliable, DTS no.
//   - Tizen 6+ (2021+): HEVC10 partially, EAC3 OK in MP4 only, DTS no.
//
// Caveat: "формально поддерживает H.265 4K, но только AAC/MP3/AC3, не DTS"
// — that's the recurring 4PDA quote.
func profileTizen(major int) DeviceProfile {
	caps := ClientCaps{
		Lang:        "ru",
		Platform:    "tizen",
		CanPlayMKV:  true,
		CanPlayH264: true,
		CanPlayAAC:  true,
		CanPlayAC3:  true,
		CanPlayMP3:  true,
	}
	label := "tizen"
	var issues []string

	switch {
	case major <= 0: // unknown version — assume mid-range 2019
		caps.CanPlayHEVC = true
		label = "tizen-unknown"
		issues = append(issues, "Tizen version unknown — HEVC10/EAC3/DTS will be transcoded")
	case major <= 3: // 2017 and older
		label = "tizen-legacy"
		issues = append(issues, "Tizen 3 and older: HEVC, EAC3, DTS, PGS subs not supported — server transcode required")
	case major == 4: // 2018
		caps.CanPlayHEVC = true
		label = "tizen-4"
		issues = append(issues, "Tizen 4: 10-bit HEVC and EAC3 unreliable — will be transcoded")
	case major == 5: // 2019
		caps.CanPlayHEVC = true
		label = "tizen-5"
		issues = append(issues, "Tizen 5: 10-bit HEVC and EAC3 unreliable — will be transcoded")
	case major >= 6: // 2021+
		caps.CanPlayHEVC = true
		caps.CanPlayHEVC10 = true
		caps.CanPlayEAC3 = true // works in MP4; remux to HLS-MP4 covers this
		label = "tizen-" + strconv.Itoa(major)
	}

	// DTS, TrueHD, PGS subs: never supported on Tizen reliably.
	// HEVC10 only marked safe on Tizen 6+.

	return DeviceProfile{Label: label, Caps: caps, KnownIssues: issues}
}

// profileWebOS returns LG WebOS profile by major version.
//
//   - WebOS < 4: H.265 + EAC3 + DTS broken — see webos-forums topic5073.
//   - WebOS 4-5 (2018-2019): HEVC 8-bit OK, no EAC3, no DTS.
//   - WebOS 6+ (2021+): HEVC 10-bit limited, EAC3 OK, DTS no.
func profileWebOS(major int) DeviceProfile {
	caps := ClientCaps{
		Lang:        "ru",
		Platform:    "webos",
		CanPlayMKV:  true,
		CanPlayH264: true,
		CanPlayAAC:  true,
		CanPlayAC3:  true,
		CanPlayMP3:  true,
	}
	label := "webos"
	var issues []string

	switch {
	case major <= 0:
		caps.CanPlayHEVC = true
		label = "webos-unknown"
	case major <= 3:
		label = "webos-legacy"
		issues = append(issues, "WebOS 3 and older: HEVC and multichannel audio require server transcode")
	case major == 4:
		caps.CanPlayHEVC = true
		label = "webos-4"
	case major == 5:
		caps.CanPlayHEVC = true
		label = "webos-5"
	case major >= 6:
		caps.CanPlayHEVC = true
		caps.CanPlayHEVC10 = true
		caps.CanPlayEAC3 = true
		label = "webos-" + strconv.Itoa(major)
	}

	return DeviceProfile{Label: label, Caps: caps, KnownIssues: issues}
}

// profileAppleTV returns Apple TV profile by tvOS major version.
//
// Bug: tvOS < 17 silently drops audio when EAC3 is in the source — confirmed
// across jellyfin-web #2262, Plex 212143, and the Apple developer forum
// thread 252214909.  Forcing remux to AAC restores audio.
func profileAppleTV(major int) DeviceProfile {
	caps := ClientCaps{
		Lang:        "ru",
		Platform:    "apple-tv",
		CanPlayMKV:  false, // tvOS prefers MP4/HLS containers
		CanPlayH264: true,
		CanPlayHEVC: true,
		CanPlayAAC:  true,
		CanPlayAC3:  true,
		CanPlayMP3:  true,
	}
	label := "apple-tv"
	var issues []string

	switch {
	case major >= 17:
		caps.CanPlayHEVC10 = true
		caps.CanPlayEAC3 = true
		label = "apple-tv-" + strconv.Itoa(major)
	case major > 0:
		label = "apple-tv-" + strconv.Itoa(major)
		issues = append(issues, "tvOS < 17: EAC3 audio is silently dropped — server will remux to AAC")
	default:
		caps.CanPlayHEVC10 = true
		label = "apple-tv-unknown"
	}

	return DeviceProfile{Label: label, Caps: caps, KnownIssues: issues}
}

// profileIOS returns iPhone/iPad Safari profile.  Same EAC3 caveat as
// Apple TV up to iOS 17.
func profileIOS(major int) DeviceProfile {
	caps := ClientCaps{
		Lang:        "ru",
		Platform:    "ios",
		CanPlayMKV:  false,
		CanPlayH264: true,
		CanPlayHEVC: true,
		CanPlayAAC:  true,
		CanPlayAC3:  true,
		CanPlayMP3:  true,
	}
	label := "ios"
	if major >= 17 {
		caps.CanPlayHEVC10 = true
		caps.CanPlayEAC3 = true
		label = "ios-" + strconv.Itoa(major)
	} else if major > 0 {
		label = "ios-" + strconv.Itoa(major)
	}
	return DeviceProfile{Label: label, Caps: caps}
}

// profileMacSafari covers macOS Safari desktop.  No DTS, no PGS, no MKV.
func profileMacSafari() DeviceProfile {
	return DeviceProfile{
		Label: "safari-mac",
		Caps: ClientCaps{
			Lang:          "ru",
			Platform:      "browser",
			CanPlayMKV:    false,
			CanPlayH264:   true,
			CanPlayHEVC:   true,
			CanPlayHEVC10: true,
			CanPlayAAC:    true,
			CanPlayAC3:    true,
			CanPlayEAC3:   true,
			CanPlayMP3:    true,
		},
	}
}

// profileChromecast covers all generations of Chromecast / Google TV
// devices.  Wide codec support; remux is usually the right path.
func profileChromecast() DeviceProfile {
	return DeviceProfile{
		Label: "chromecast",
		Caps: ClientCaps{
			Lang:        "ru",
			Platform:    "chromecast",
			CanPlayMKV:  true,
			CanPlayH264: true,
			CanPlayHEVC: true,
			CanPlayVP9:  true,
			CanPlayAAC:  true,
			CanPlayAC3:  true,
			CanPlayEAC3: true,
			CanPlayOpus: true,
			CanPlayFLAC: true,
			CanPlayMP3:  true,
		},
	}
}

// profileAndroidTV covers generic Android TV boxes.  Conservative because
// the budget hardware named in the research pass (X96, Mecool, HD BOX,
// Tanix) ships with a wildly inconsistent codec matrix.
func profileAndroidTV() DeviceProfile {
	return DeviceProfile{
		Label: "android-tv",
		Caps: ClientCaps{
			Lang:        "ru",
			Platform:    "android-tv",
			CanPlayMKV:  true,
			CanPlayH264: true,
			CanPlayHEVC: true,
			CanPlayVP9:  true,
			CanPlayAAC:  true,
			CanPlayAC3:  true,
			CanPlayEAC3: true,
			CanPlayOpus: true,
			CanPlayFLAC: true,
			CanPlayMP3:  true,
		},
		KnownIssues: []string{
			"Android TV boxes vary widely — DTS/TrueHD/HEVC10 may need server transcode",
		},
	}
}

// profileLampaNative covers the Lampa native app (Android phone/tablet)
// where playback goes through ExoPlayer.  Wide codec support.
func profileLampaNative() DeviceProfile {
	return DeviceProfile{
		Label: "lampa-native",
		Caps: ClientCaps{
			Lang:          "ru",
			Platform:      "android-tv",
			CanPlayMKV:    true,
			CanPlayH264:   true,
			CanPlayHEVC:   true,
			CanPlayHEVC10: true,
			CanPlayVP9:    true,
			CanPlayAAC:    true,
			CanPlayAC3:    true,
			CanPlayEAC3:   true,
			CanPlayOpus:   true,
			CanPlayFLAC:   true,
			CanPlayMP3:    true,
		},
	}
}

// profileChromeBrowser covers desktop Chrome/Chromium.
//
// Chrome generally rejects raw HEVC and AC3/EAC3 in HLS — most users hit
// "manifestLoadError" until we re-encode video to H264 and audio to AAC.
func profileChromeBrowser() DeviceProfile {
	return DeviceProfile{
		Label: "browser-chrome",
		Caps: ClientCaps{
			Lang:        "ru",
			Platform:    "browser",
			CanPlayMKV:  false,
			CanPlayH264: true,
			CanPlayVP9:  true,
			CanPlayAAC:  true,
			CanPlayMP3:  true,
			CanPlayOpus: true,
		},
	}
}

// profileFirefoxBrowser covers desktop Firefox.  Even more restrictive
// than Chrome — no HEVC under any circumstances.
func profileFirefoxBrowser() DeviceProfile {
	return DeviceProfile{
		Label: "browser-firefox",
		Caps: ClientCaps{
			Lang:        "ru",
			Platform:    "browser",
			CanPlayMKV:  false,
			CanPlayH264: true,
			CanPlayVP9:  true,
			CanPlayAAC:  true,
			CanPlayMP3:  true,
			CanPlayOpus: true,
		},
	}
}

// ---------------------------------------------------------------------------
// Model-specific overrides (P3.H)
//
// The OS-version DB above is enough for ~80% of failures, but specific TV
// models keep showing up in 4PDA / tv-ch.ru reports with reproducible
// firmware bugs that the OS class alone doesn't predict.  Each entry here
// is keyed on a substring (case-insensitive) found in the User-Agent and
// returns a tightened DeviceProfile that the chosen pipeline respects.
//
// The list is intentionally small — we only encode bugs that have been
// independently confirmed by multiple sources, because the cost of a
// wrong override (forcing transcode for a TV that doesn't need it) is
// non-trivial CPU.
// ---------------------------------------------------------------------------

type modelOverride struct {
	uaSubstring string
	build       func() DeviceProfile
}

var modelOverrides = []modelOverride{
	// Samsung T24N390SIX — black screen on HEVC.
	// Sources: tv-ch.ru, spravka.net.  Workaround: force H264 transcode
	// by clearing HEVC caps.
	{"t24n390si", func() DeviceProfile {
		p := profileTizen(4) // ~2018 vintage
		p.Caps.CanPlayHEVC = false
		p.Caps.CanPlayHEVC10 = false
		p.Label = "tizen-t24n390-blackscreen-fix"
		p.KnownIssues = append(p.KnownIssues,
			"Samsung T24N390SIX: HEVC produces black screen — forcing H264 transcode")
		return p
	}},

	// TCL 50C655 / similar mid-range TCL — manifestLoadError after 6-12 min
	// on long sessions.  Sources: tv-ch.ru.  Workaround: rely on the
	// auto-restart cascade (P3.E) plus shorter HLS segments — the latter
	// is set globally so we just append a known-issue note here.
	{"tcl 50c655", func() DeviceProfile {
		p := profileAndroidTV()
		p.Label = "android-tv-tcl-c655"
		p.KnownIssues = append(p.KnownIssues,
			"TCL 50C655: known manifestLoadError after 6-12 minutes — auto-restart cascade (P3.E) is enabled")
		return p
	}},

	// Haier FF Pro — same crash signature as TCL above on 4PDA.
	{"haier", func() DeviceProfile {
		p := profileAndroidTV()
		p.Label = "android-tv-haier"
		p.KnownIssues = append(p.KnownIssues,
			"Haier Android TV: known mid-playback drop — auto-restart cascade (P3.E) is enabled")
		return p
	}},

	// LG LM5772PLA — webOS 1.x, basically nothing works without transcode.
	// Sources: webos-forums.ru topic5073.
	{"lm5772p", func() DeviceProfile {
		p := profileWebOS(1)
		p.Label = "webos-lm5772-legacy"
		p.KnownIssues = append(p.KnownIssues,
			"LG LM5772PLA: 2013 vintage, requires server transcode for everything beyond H264 baseline")
		return p
	}},

	// Samsung Q9FNA — yumata/lampa #212: ASS subtitles lose formatting.
	// Already handled by profileTizen + profileNeedsAssBurnIn, but we
	// log a stable override label for diagnostics.
	{"q9fn", func() DeviceProfile {
		p := profileTizen(4)
		p.Label = "tizen-q9fn-ass-fix"
		p.KnownIssues = append(p.KnownIssues,
			"Samsung Q9FNA: ASS styling lost — burn-in pipeline forced (yumata/lampa#212)")
		return p
	}},

	// Cheap Android boxes named in research — X96, Mecool, HD BOX, Tanix
	// often ship with broken HEVC decoders despite advertising support.
	// Tighten to H264 + AAC only.
	{"x96", func() DeviceProfile {
		p := profileAndroidTV()
		p.Caps.CanPlayHEVC10 = false
		p.Label = "android-tv-x96-budget"
		p.KnownIssues = append(p.KnownIssues,
			"Cheap Android TV box (X96 family): HEVC10 decoder unreliable — capping at HEVC8")
		return p
	}},
	{"mecool", func() DeviceProfile {
		p := profileAndroidTV()
		p.Caps.CanPlayHEVC10 = false
		p.Label = "android-tv-mecool"
		p.KnownIssues = append(p.KnownIssues,
			"Mecool box: HEVC10 decoder unreliable — capping at HEVC8")
		return p
	}},
	{"hd box", func() DeviceProfile {
		p := profileAndroidTV()
		p.Caps.CanPlayHEVC10 = false
		p.Caps.CanPlayDTS = false
		p.Label = "android-tv-hdbox"
		p.KnownIssues = append(p.KnownIssues,
			"HD BOX: HEVC10/DTS decoders unreliable — forcing transcode")
		return p
	}},
}

// lookupModelOverride checks the UA against the model overrides table.
// First match wins.  Returns false when no override applies.
func lookupModelOverride(uaLower string) (DeviceProfile, bool) {
	for _, ov := range modelOverrides {
		if strings.Contains(uaLower, ov.uaSubstring) {
			return ov.build(), true
		}
	}
	return DeviceProfile{}, false
}

// ---------------------------------------------------------------------------
// Caps merging
// ---------------------------------------------------------------------------

// enrichCapsFromUA fills in unspecified ClientCaps from the UA-based DB.
//
// Rules:
//   - if explicit caps come from the request (req.Client != nil), they win;
//     UA detection only runs to set `profileLabel` for diagnostics.
//   - if no explicit caps — UA caps become the active caps.
//   - if UA detection returns "unknown" — we keep transcoding behaviour
//     conservative (H264 + AAC only).
//
// Returns (effective caps, profile label, list of known issues for that profile).
func EnrichCapsFromUA(client *ClientCaps, ua string) (ClientCaps, string, []string) {
	profile := detectClientCapsFromUA(ua)
	if client != nil {
		// Explicit caps win.  We still inherit Lang/Platform from UA when
		// the explicit payload left them blank — those are diagnostic-only.
		out := *client
		if strings.TrimSpace(out.Lang) == "" {
			out.Lang = profile.Caps.Lang
		}
		if strings.TrimSpace(out.Platform) == "" {
			out.Platform = profile.Caps.Platform
		}
		return out, profile.Label, profile.KnownIssues
	}
	return profile.Caps, profile.Label, profile.KnownIssues
}
