// Package litehtml holds the shared Lampa "lite" HTML/JSON response builders
// used by the online-source checkers (rezka, alloha, getstv, …). The markup
// matches what Lampa's online.js component expects (videos__item /
// videos__button selector blocks with data-json attributes).
//
// It is a LEAF package: stdlib + internal/httpx only. httpapi keeps thin
// unexported forwarders (getstv.go) so the ~60 staying source files need no
// call-site changes; extracted source packages call litehtml directly (or via
// their own local forwarders).
package litehtml

import (
	stdjson "encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"lampac-go/internal/httpx"
)

// AppendVoiceHTML appends a voice/translation selector button.
func AppendVoiceHTML(sb *strings.Builder, data map[string]any) {
	sb.WriteString(`<div class="videos__button selector `)
	if BoolAny(data["active"]) {
		sb.WriteString(`active`)
	}
	sb.WriteString(`" data-json='`)
	sb.WriteString(AttrJSON(map[string]any{
		"method": "link",
		"url":    toString(data["url"]),
	}))
	sb.WriteString(`'>`)
	sb.WriteString(html.EscapeString(toString(data["name"])))
	sb.WriteString(`</div>`)
}

// AppendMovieHTML appends a movie/episode card.
func AppendMovieHTML(sb *strings.Builder, data map[string]any, text string, focused bool, season, episode int) {
	sb.WriteString(`<div class="videos__item videos__movie selector `)
	if focused {
		sb.WriteString(`focused`)
	}
	sb.WriteString(`" media=""`)
	if season > 0 {
		sb.WriteString(` s="`)
		sb.WriteString(strconv.Itoa(season))
		sb.WriteString(`"`)
	}
	if episode > 0 {
		sb.WriteString(` e="`)
		sb.WriteString(strconv.Itoa(episode))
		sb.WriteString(`"`)
	}
	sb.WriteString(` data-json='`)
	sb.WriteString(AttrJSON(data))
	sb.WriteString(`'><div class="videos__item-imgbox videos__movie-imgbox"></div><div class="videos__item-title">`)
	sb.WriteString(html.EscapeString(text))
	sb.WriteString(`</div></div>`)
}

// AppendSeasonHTML appends a season card.
func AppendSeasonHTML(sb *strings.Builder, data map[string]any, text string, focused bool) {
	sb.WriteString(`<div class="videos__item videos__season selector `)
	if focused {
		sb.WriteString(`focused`)
	}
	sb.WriteString(`" data-json='`)
	sb.WriteString(AttrJSON(data))
	sb.WriteString(`'><div class="videos__season-layers"></div><div class="videos__item-imgbox videos__season-imgbox"><div class="videos__item-title videos__season-title">`)
	sb.WriteString(html.EscapeString(text))
	sb.WriteString(`</div></div></div>`)
}

// WriteEmpty writes the canonical empty lite response ({} for rjson, "" for HTML).
func WriteEmpty(w http.ResponseWriter, rjson bool) {
	if rjson {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{})
		return
	}
	httpx.WriteHTML(w, http.StatusOK, "")
}

// AttrJSON marshals v and HTML-escapes it for embedding in a data-json attribute.
func AttrJSON(v any) string {
	b, err := stdjson.Marshal(v)
	if err != nil {
		return "{}"
	}
	return html.EscapeString(string(b))
}

// Bool renders a bool as the string "true"/"false".
func Bool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// BoolAny interprets bools and truthy strings ("1", "true", "yes", "on").
func BoolAny(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return parseBoolParam(t)
	default:
		return false
	}
}

// JoinName joins RU/EN titles as "ru / en" with fallbacks.
func JoinName(ru, en string) string {
	ru = strings.TrimSpace(ru)
	en = strings.TrimSpace(en)
	switch {
	case ru != "" && en != "":
		return ru + " / " + en
	case ru != "":
		return ru
	case en != "":
		return en
	default:
		return "Untitled"
	}
}

// QueryInt parses a positive query int; ok=false on empty/invalid.
func QueryInt(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

// toString mirrors httpapi's pure any→string helper (bookmark_api.go).
func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// parseBoolParam mirrors httpapi's pure truthy-string helper (lite_events.go).
func parseBoolParam(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
