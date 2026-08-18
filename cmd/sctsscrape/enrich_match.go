package main

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// enrichOne ищет TMDB-кандидата для одного фильма и обновляет m в случае успеха.
// Возвращает (true, nil) если найден надёжный матч, (false, nil) если ничего не подошло,
// (false, err) при ошибке сети.
func enrichOne(ctx context.Context, t *tmdbClient, m *Movie, yearSlack int) (bool, error) {
	kind, season, cleanName := classifyMovie(m)

	queries := buildQueries(m, cleanName)
	var best *tmdbResult

	for _, q := range queries {
		var results []tmdbResult
		var err error
		switch kind {
		case "tv":
			results, err = t.SearchTV(ctx, q, m.Year)
			if len(results) == 0 && err == nil {
				// fallback на movie если tv пусто (бывает что в SCTS "(N сезон)"
				// поставлено ошибочно).
				results, err = t.SearchMovie(ctx, q, m.Year)
			}
		default:
			results, err = t.SearchMovie(ctx, q, m.Year)
			if len(results) == 0 && err == nil {
				results, err = t.SearchTV(ctx, q, m.Year)
			}
		}
		if err != nil {
			return false, err
		}

		// year-tolerance retry без year
		if len(results) == 0 && m.Year > 0 && yearSlack > 0 {
			results, _ = t.SearchMovie(ctx, q, 0)
			if r2, _ := t.SearchTV(ctx, q, 0); len(r2) > 0 {
				results = append(results, r2...)
			}
		}

		best = pickBest(results, m, cleanName, yearSlack)
		if best != nil {
			break
		}
	}

	if best == nil {
		m.EnrichedAt = time.Now().UTC().Format(time.RFC3339)
		return false, nil
	}

	kindForExt := best.MediaType
	if kindForExt == "" {
		if best.Name != "" || best.FirstAirDate != "" {
			kindForExt = "tv"
		} else {
			kindForExt = "movie"
		}
	}
	m.TMDBID = best.ID
	m.Type = kindForExt
	if season > 0 {
		m.Season = season
	}
	if m.IntlName == "" {
		if kindForExt == "tv" {
			m.IntlName = pickIntl(best.OriginalName, best.Name)
		} else {
			m.IntlName = pickIntl(best.OriginalTitle, best.Title)
		}
	}

	// external_ids — для IMDB ID.
	if ext, err := t.ExternalIDs(ctx, kindForExt, best.ID); err == nil && ext != nil && ext.IMDBID != "" {
		m.IMDBID = ext.IMDBID
	}

	m.EnrichedAt = time.Now().UTC().Format(time.RFC3339)
	return true, nil
}

var (
	reSeason     = regexp.MustCompile(`(?i)\s*\(\s*(\d+)\s*(сезон|season|серия|season)\s*\)\s*`)
	reSeasonRu2  = regexp.MustCompile(`\s*\d+-?[йя]?\s*сезон\s*`)
	rePlus3D     = regexp.MustCompile(`(?i)\s*\+\s*3d\s*$`)
	reExtraWS    = regexp.MustCompile(`\s+`)
	reQuotes     = regexp.MustCompile(`[«»"„“”'']`)
)

func classifyMovie(m *Movie) (kind string, season int, cleanName string) {
	name := m.Name
	cleanName = name

	if mm := reSeason.FindStringSubmatch(name); len(mm) >= 2 {
		kind = "tv"
		if n, err := strconv.Atoi(mm[1]); err == nil {
			season = n
		}
		cleanName = reSeason.ReplaceAllString(name, " ")
	} else if reSeasonRu2.MatchString(name) {
		kind = "tv"
		cleanName = reSeasonRu2.ReplaceAllString(name, " ")
	}

	cleanName = rePlus3D.ReplaceAllString(cleanName, "")
	cleanName = reQuotes.ReplaceAllString(cleanName, "")
	cleanName = reExtraWS.ReplaceAllString(cleanName, " ")
	cleanName = strings.TrimSpace(cleanName)
	return
}

func buildQueries(m *Movie, cleanName string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			return
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	add(m.IntlName)
	add(cleanName)
	// slug часто это транслит англ. имени или транслит русского — иногда даёт совпадение
	if m.Slug != "" {
		slug := strings.ReplaceAll(m.Slug, "_", " ")
		// убрать год из slug-а если он там
		slug = regexp.MustCompile(`\s+\d{4}\s*$`).ReplaceAllString(slug, "")
		add(slug)
	}
	return out
}

func pickIntl(original, localized string) string {
	if original != "" {
		return original
	}
	return localized
}

// pickBest выбирает лучший матч по {year + title similarity + popularity}.
func pickBest(results []tmdbResult, m *Movie, cleanName string, yearSlack int) *tmdbResult {
	if len(results) == 0 {
		return nil
	}
	var best *tmdbResult
	var bestScore float64

	target := normalize(cleanName)
	target2 := normalize(m.IntlName)

	for i := range results {
		r := &results[i]

		ry := releaseYear(r)
		yearDelta := 999
		if m.Year > 0 && ry > 0 {
			d := m.Year - ry
			if d < 0 {
				d = -d
			}
			yearDelta = d
		}
		// strict: year должен быть либо точный, либо в slack
		if m.Year > 0 && ry > 0 && yearDelta > yearSlack {
			continue
		}

		title := normalize(firstNonEmpty(r.Title, r.Name))
		origTitle := normalize(firstNonEmpty(r.OriginalTitle, r.OriginalName))

		var titleScore float64
		for _, q := range []string{target, target2} {
			if q == "" {
				continue
			}
			for _, cand := range []string{title, origTitle} {
				if cand == "" {
					continue
				}
				if s := similarity(q, cand); s > titleScore {
					titleScore = s
				}
			}
		}

		// score: similarity 0..1, бонус за точный year (-0.0 за каждый delta), бонус за популярность
		yearPenalty := float64(yearDelta) * 0.05
		popBoost := 0.0
		if r.VoteCount > 50 {
			popBoost = 0.05
		}
		score := titleScore - yearPenalty + popBoost

		if score > bestScore {
			bestScore = score
			best = r
		}
	}

	// порог: похожее имя ≥0.55 (с учётом транслита/перевода — толерантно)
	if bestScore < 0.55 {
		return nil
	}
	return best
}

func releaseYear(r *tmdbResult) int {
	d := r.ReleaseDate
	if d == "" {
		d = r.FirstAirDate
	}
	if len(d) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(d[:4])
	return n
}

func firstNonEmpty(args ...string) string {
	for _, s := range args {
		if s != "" {
			return s
		}
	}
	return ""
}

func normalize(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSpace = false
		default:
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// similarity — Jaccard по словам + bonus за совпадение по началу.
func similarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	wa := strings.Fields(a)
	wb := strings.Fields(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, w := range wa {
		set[w] = true
	}
	overlap := 0
	for _, w := range wb {
		if set[w] {
			overlap++
		}
	}
	union := len(wa) + len(wb) - overlap
	if union == 0 {
		return 0
	}
	jac := float64(overlap) / float64(union)
	// bonus если b — подстрока a или наоборот
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		jac += 0.2
	}
	if jac > 1 {
		jac = 1
	}
	return jac
}
