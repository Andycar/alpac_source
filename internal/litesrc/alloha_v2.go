package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Alloha v2 API (https://apbugall.org/v2/) — Bearer-auth, documented endpoints:
//   /v2/movies/kp/{id}      → single by kinopoisk id
//   /v2/movies/imdb/{id}    → single by imdb id
//   /v2/movies/tmdb/{id}    → single by tmdb id
//   /v2/movies/token/{tok}  → single by movie token (a.k.a. orid/token_movie)
//   /v2/movies/name/list?name=&year= → array of matches
//
// Response single shape: {"data": {...}, possibly "error" on 404}
// Response list shape:   {"data": [...]}
//
// The legacy api.apbugall.org/?token=X&kp= endpoint still works but is
// undocumented and uses a different schema. v2 is the source of truth.

// allohaV2Fetch performs an authenticated v2 API GET. Returns body and ok flag.
// Treats 200-299 as success; 404/4xx → ok=false (caller decides fallback).
func (a *allohaChecker) allohaV2Fetch(ctx context.Context, path string) ([]byte, bool) {
	if a.token == "" {
		return nil, false
	}
	full := a.apiHost + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, false
	}
	return body, true
}

// allohaV2GetSingle fetches a single movie/serial entry by lookup path and
// returns it converted to the legacy shape expected by writeMovie/writeSerial
// and the search helpers.
func (a *allohaChecker) allohaV2GetSingle(ctx context.Context, path string) map[string]any {
	body, ok := a.allohaV2Fetch(ctx, path)
	if !ok {
		return nil
	}
	var root struct {
		Data map[string]any `json:"data"`
	}
	if err := stdjson.Unmarshal(body, &root); err != nil || root.Data == nil {
		return nil
	}
	return allohaV2ToLegacy(root.Data)
}

// allohaV2GetByKP looks up a movie/serial by KinoPoisk id.
func (a *allohaChecker) allohaV2GetByKP(ctx context.Context, id string) map[string]any {
	if id == "" || !isInt64(id) {
		return nil
	}
	return a.allohaV2GetSingle(ctx, "/v2/movies/kp/"+url.PathEscape(id))
}

// allohaV2GetByTMDB looks up a movie/serial by TMDB id. The card carried by
// /capi and Lampa is TMDB-keyed, so this lets Alloha resolve directly from the
// id every card already has — no dependency on the (fallible) TMDB→Kinopoisk
// resolver that kp-only sources need.
func (a *allohaChecker) allohaV2GetByTMDB(ctx context.Context, id string) map[string]any {
	if id == "" || !isInt64(id) {
		return nil
	}
	return a.allohaV2GetSingle(ctx, "/v2/movies/tmdb/"+url.PathEscape(id))
}

// allohaV2GetByIMDB looks up a movie/serial by IMDB id (tt... format expected).
func (a *allohaChecker) allohaV2GetByIMDB(ctx context.Context, id string) map[string]any {
	if id == "" {
		return nil
	}
	return a.allohaV2GetSingle(ctx, "/v2/movies/imdb/"+url.PathEscape(id))
}

// allohaV2GetByToken looks up by movie token (a.k.a. "orid" in old Lampac API).
func (a *allohaChecker) allohaV2GetByToken(ctx context.Context, token string) map[string]any {
	if token == "" {
		return nil
	}
	return a.allohaV2GetSingle(ctx, "/v2/movies/token/"+url.PathEscape(token))
}

// allohaV2SearchByName uses /v2/movies/name/list to find matches by title.
// Returns list of items in legacy-list shape: each item has name/year/country/
// category_id/token_movie. Year filter is server-side when provided.
func (a *allohaChecker) allohaV2SearchByName(ctx context.Context, name string, year int) []map[string]any {
	if name == "" {
		return nil
	}
	q := url.Values{}
	q.Set("name", name)
	if year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	body, ok := a.allohaV2Fetch(ctx, "/v2/movies/name/list?"+q.Encode())
	if !ok {
		return nil
	}
	var root struct {
		Data []map[string]any `json:"data"`
	}
	if err := stdjson.Unmarshal(body, &root); err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(root.Data))
	for _, item := range root.Data {
		out = append(out, allohaV2ToLegacyListItem(item))
	}
	return out
}

// allohaV2ToLegacy converts a v2 movie/serial detail object to the map shape
// that writeMovie/writeSerial/lookupByIDsWithQuality already understand.
//
// Mappings:
//
//	v2.token                  → legacy.token_movie
//	v2.category.slug          → legacy.category (1=movie, 2=serial)
//	v2.translations[]         → legacy.translation_iframe{id→{name,iframe,quality,uhd,lgbt,adv}}
//	v2.seasons[]              → legacy.seasons{season→{episodes{episode→{translation{id→{translation:name,iframe,quality,uhd,lgbt}}}}}}
//	v2.iframe / iframe_trailer pass through
//
// has_ads → adv (bool→bool kept as bool; old code uses fmt.Sprint to compare)
func allohaV2ToLegacy(v2 map[string]any) map[string]any {
	if v2 == nil {
		return nil
	}
	out := map[string]any{}
	// passthroughs
	for _, k := range []string{"name", "original_name", "alternative_name", "year",
		"country", "genre", "poster", "description", "tagline", "runtime", "date",
		"iframe", "iframe_trailer"} {
		if v, ok := v2[k]; ok {
			out[k] = v
		}
	}
	// token → token_movie
	if tok, ok := v2["token"].(string); ok {
		out["token_movie"] = tok
	}
	// category {slug, name} → int
	out["category"] = allohaV2CategoryInt(v2["category"])
	// quality array → string (first non-empty for old API compat)
	if qArr, ok := v2["quality"].([]any); ok && len(qArr) > 0 {
		if s, _ := qArr[0].(string); s != "" {
			out["quality"] = s
		}
	} else if q, ok := v2["quality"].(string); ok {
		out["quality"] = q
	}
	// translations array → translation_iframe map (for movies)
	if tArr, ok := v2["translations"].([]any); ok {
		ti := map[string]any{}
		for _, t := range tArr {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			id := allohaV2IDKey(tm["id"])
			if id == "" {
				continue
			}
			ti[id] = allohaV2TranslationToLegacy(tm)
		}
		if len(ti) > 0 {
			out["translation_iframe"] = ti
		}
	}
	// seasons array → seasons map (for serials)
	if sArr, ok := v2["seasons"].([]any); ok {
		seasons := map[string]any{}
		for _, s := range sArr {
			sm, ok := s.(map[string]any)
			if !ok {
				continue
			}
			seasonNum := allohaInt(sm["season"])
			if seasonNum <= 0 {
				continue
			}
			seasons[strconv.Itoa(seasonNum)] = allohaV2SeasonToLegacy(sm)
		}
		if len(seasons) > 0 {
			out["seasons"] = seasons
		}
	}
	return out
}

// allohaV2SeasonToLegacy converts one season{season, episodes_count, iframe, episodes[]}
// to legacy {episodes: {episodeNum: {translation: {id: {translation, iframe, quality, uhd, lgbt}}}}}.
func allohaV2SeasonToLegacy(season map[string]any) map[string]any {
	out := map[string]any{}
	if v, ok := season["episodes_count"]; ok {
		out["episodes_count"] = v
	}
	if v, ok := season["iframe"]; ok {
		out["iframe"] = v
	}
	epArr, _ := season["episodes"].([]any)
	if len(epArr) == 0 {
		return out
	}
	eps := map[string]any{}
	for _, ep := range epArr {
		em, ok := ep.(map[string]any)
		if !ok {
			continue
		}
		epNum := allohaInt(em["episode"])
		if epNum <= 0 {
			continue
		}
		epOut := map[string]any{"episode": epNum}
		if v, ok := em["iframe"]; ok {
			epOut["iframe"] = v
		}
		if tArr, ok := em["translations"].([]any); ok && len(tArr) > 0 {
			tm := map[string]any{}
			for _, t := range tArr {
				tmap, ok := t.(map[string]any)
				if !ok {
					continue
				}
				id := allohaV2IDKey(tmap["id"])
				if id == "" {
					continue
				}
				// Episode-level translation uses "translation" as the display
				// name field (vs "name" at movie/serial top level). writeSerial
				// reads inner["translation"] for the voice name.
				inner := allohaV2TranslationToLegacy(tmap)
				if name, ok := inner["name"].(string); ok && name != "" {
					inner["translation"] = name
				}
				tm[id] = inner
			}
			if len(tm) > 0 {
				epOut["translation"] = tm
			}
		}
		eps[strconv.Itoa(epNum)] = epOut
	}
	if len(eps) > 0 {
		out["episodes"] = eps
	}
	return out
}

// allohaV2TranslationToLegacy normalizes a v2 translation object into the legacy
// inner map shape (name, iframe, quality, uhd, lgbt, adv).
func allohaV2TranslationToLegacy(t map[string]any) map[string]any {
	out := map[string]any{}
	if v, ok := t["name"]; ok {
		out["name"] = v
	}
	if v, ok := t["iframe"]; ok {
		out["iframe"] = v
	}
	if v, ok := t["quality"]; ok {
		out["quality"] = v
	}
	if v, ok := t["uhd"]; ok {
		out["uhd"] = v
	}
	if v, ok := t["lgbt"]; ok {
		out["lgbt"] = v
	}
	if v, ok := t["has_ads"]; ok {
		out["adv"] = v
	}
	if v, ok := t["resolutions"]; ok {
		out["resolutions"] = v
	}
	return out
}

// allohaV2ToLegacyListItem converts a search-list item (used by name/list and
// catalog endpoints) into the legacy list shape (name, year, country, category_id, token_movie).
func allohaV2ToLegacyListItem(v2 map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"name", "original_name", "year", "country", "genre", "poster"} {
		if v, ok := v2[k]; ok {
			out[k] = v
		}
	}
	if tok, ok := v2["token"].(string); ok {
		out["token_movie"] = tok
	}
	// Flatten ids.kp / ids.imdb for callers that promote list → full detail by kp.
	if ids, ok := v2["ids"].(map[string]any); ok {
		if v, ok := ids["kp"]; ok {
			out["ids_kp"] = v
		}
		if v, ok := ids["imdb"]; ok {
			out["ids_imdb"] = v
		}
	}
	out["category_id"] = allohaV2CategoryInt(v2["category"])
	// list items also carry translations, expose as translation_iframe so
	// allohaMaxQuality (used by checkSearch) can still produce a quality badge.
	if tArr, ok := v2["translations"].([]any); ok {
		ti := map[string]any{}
		for _, t := range tArr {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			id := allohaV2IDKey(tm["id"])
			if id == "" {
				continue
			}
			ti[id] = allohaV2TranslationToLegacy(tm)
		}
		if len(ti) > 0 {
			out["translation_iframe"] = ti
		}
	}
	return out
}

// allohaV2CategoryInt maps v2 category {slug,name} to the legacy int used by
// writeMovie/writeSerial dispatch:
//
//	slug=movie         → 1
//	slug=serial        → 2
//	slug=cartoon       → 3 (Lampac convention; same int the legacy API returns)
//	slug=animation     → 4
//	anything else      → 1 (default to movie)
func allohaV2CategoryInt(v any) int {
	if v == nil {
		return 0
	}
	m, ok := v.(map[string]any)
	if !ok {
		// Already an int from a legacy passthrough.
		return allohaInt(v)
	}
	slug, _ := m["slug"].(string)
	switch strings.ToLower(slug) {
	case "movie", "film":
		return 1
	case "serial", "series", "tv-show":
		return 2
	case "cartoon", "cartoons":
		return 3
	case "animation", "anime":
		return 4
	default:
		// Fallback: if slug looks numeric (defensive), parse it.
		if n, err := strconv.Atoi(slug); err == nil && n > 0 {
			return n
		}
		return 1
	}
}

// allohaV2IDKey converts a v2 translation id (number or numeric string) to a
// map-key string, matching the legacy translation_iframe[id] index.
func allohaV2IDKey(v any) string {
	switch x := v.(type) {
	case float64:
		return strconv.Itoa(int(x))
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return x.String()
	}
	return ""
}
