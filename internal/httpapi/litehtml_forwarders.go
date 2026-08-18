package httpapi

// Shared lite render helpers — canonical implementations live in
// internal/litehtml (leaf pkg). These thin forwarders keep the remaining
// in-package sources (youtube/alloha/pidtor/mirage) call-site-stable;
// they die when those files move to litesrc.

import (
	"strings"

	"lampac-go/internal/litehtml"
)

func getsTVAppendMovieHTML(sb *strings.Builder, data map[string]any, text string, focused bool, season, episode int) {
	litehtml.AppendMovieHTML(sb, data, text, focused, season, episode)
}

func getsTVAppendSeasonHTML(sb *strings.Builder, data map[string]any, text string, focused bool) {
	litehtml.AppendSeasonHTML(sb, data, text, focused)
}

func getsTVBool(v bool) string {
	return litehtml.Bool(v)
}

func getsTVJoinName(ru, en string) string {
	return litehtml.JoinName(ru, en)
}
