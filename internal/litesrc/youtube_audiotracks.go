package litesrc

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Multi-language audio on YouTube.
//
// A video can carry the uploader's original track plus dozens of dubs — measured
// on prod 2026-09-01, one MrBeast video had 44 distinct tracks. pickBestAudio
// plays the original by default (never a surprise), and this file exposes the
// rest so a client can offer the choice.

// ytAudioTrack is one selectable audio language.
type ytAudioTrack struct {
	Lang     string `json:"lang"`     // yt-dlp language code: "ru", "en", "zh-Hans"
	Name     string `json:"name"`     // display name for the picker
	Original bool   `json:"original"` // the track the uploader actually recorded
	URL      string `json:"url"`      // this same endpoint, pinned to this track
}

// ytLangNames covers the languages YouTube dubs into most often. Anything else
// falls back to the label YouTube itself put on the format.
var ytLangNames = map[string]string{
	"ru": "Русский", "en": "Английский", "uk": "Украинский", "de": "Немецкий",
	"fr": "Французский", "es": "Испанский", "it": "Итальянский", "pt": "Португальский",
	"pl": "Польский", "tr": "Турецкий", "ar": "Арабский", "hi": "Хинди",
	"ja": "Японский", "ko": "Корейский", "vi": "Вьетнамский", "th": "Тайский",
	"id": "Индонезийский", "bn": "Бенгальский", "ta": "Тамильский", "te": "Телугу",
	"ml": "Малаялам", "zh-Hans": "Китайский (упр.)", "zh-Hant": "Китайский (трад.)",
	"nl": "Нидерландский", "sv": "Шведский", "cs": "Чешский", "ro": "Румынский",
	"hu": "Венгерский", "el": "Греческий", "he": "Иврит", "fa": "Персидский",
}

// ytTrackDisplayName prefers our own Russian label and falls back to YouTube's
// note with the bitrate tier stripped ("Russian, medium" → "Russian").
func ytTrackDisplayName(lang, note string) string {
	if n, ok := ytLangNames[lang]; ok {
		return n
	}
	if i := strings.LastIndex(note, ","); i > 0 {
		note = note[:i]
	}
	note = strings.TrimSpace(note)
	if note != "" {
		return note
	}
	if lang != "" {
		return lang
	}
	return "Оригинал"
}

// ytAudioTracks lists the distinct audio languages in a format set, original
// first and the rest alphabetical by display name. Returns nil when the video
// has fewer than two tracks — there is nothing to choose between.
func ytAudioTracks(formats []ytFormat, selfURL string) []ytAudioTrack {
	seen := make(map[string]ytAudioTrack, 8)
	for _, f := range formats {
		if !f.isAudioOnly() || f.URL == "" || f.Language == "" {
			continue
		}
		if _, ok := seen[f.Language]; ok {
			continue
		}
		seen[f.Language] = ytAudioTrack{
			Lang:     f.Language,
			Name:     ytTrackDisplayName(f.Language, f.FormatNote),
			Original: f.isOriginalAudio(),
		}
	}
	if len(seen) < 2 {
		return nil
	}
	out := make([]ytAudioTrack, 0, len(seen))
	for _, t := range seen {
		if selfURL != "" {
			sep := "?"
			if strings.Contains(selfURL, "?") {
				sep = "&"
			}
			t.URL = selfURL + sep + "alang=" + url.QueryEscape(t.Lang)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Original != out[j].Original {
			return out[i].Original // the original heads the list
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ytFilterAudioLang keeps every video format and only the audio of `lang`.
//
// Filtering the slice BEFORE the quality map is built is what makes track
// selection a two-line change: pickBestAudio then has only one language to
// choose from, and nothing downstream needs to know about languages at all.
// An unknown or empty lang leaves the set untouched, so the default path keeps
// picking the original.
func ytFilterAudioLang(formats []ytFormat, lang string) []ytFormat {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return formats
	}
	match := false
	for _, f := range formats {
		if f.isAudioOnly() && f.URL != "" && strings.EqualFold(f.Language, lang) {
			match = true
			break
		}
	}
	if !match {
		return formats // asked for a track this video doesn't have — stay on the original
	}
	out := make([]ytFormat, 0, len(formats))
	for _, f := range formats {
		if f.isAudioOnly() && !strings.EqualFold(f.Language, lang) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// ytSelfTrackURL rebuilds this request's own URL so each track can carry a
// ready-to-use link. Query params that pin a previous choice are dropped.
func ytSelfTrackURL(req *http.Request) string {
	if req == nil {
		return ""
	}
	q := req.URL.Query()
	q.Del("alang")
	base := hostFromRequest(req) + "/lite/youtube"
	if enc := q.Encode(); enc != "" {
		return base + "?" + enc
	}
	return base
}

// ytActiveTrackLang reports which track the response actually carries: the
// requested one when the video has it, otherwise the original.
func ytActiveTrackLang(tracks []ytAudioTrack, want string) string {
	want = strings.TrimSpace(want)
	for _, t := range tracks {
		if want != "" && strings.EqualFold(t.Lang, want) {
			return t.Lang
		}
	}
	for _, t := range tracks {
		if t.Original {
			return t.Lang
		}
	}
	return ""
}
