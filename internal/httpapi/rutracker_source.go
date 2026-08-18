package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/auth"
	"lampac-go/internal/config"
	"lampac-go/internal/rutracker"

	"github.com/rs/zerolog/log"
)

// rutracker_source.go — the seam that puts RuTracker rows into the answers
// clients already consume.
//
// jacred never carries rutracker (the tracker needs an account, so it cannot
// live in a public open-sync base and JacRed's parser gives up before its first
// request without credentials). So instead of replacing the parser we MERGE:
// the jacred answer is buffered, our rows are appended, and the client sees one
// list. Every surface benefits at once — the web torrent browser and native
// clients through /api/v2.0 and /api/v1.0, Lampa through /lite/pidtor.

// rutrackerConfigFrom maps the TOML section onto the package config.
func rutrackerConfigFrom(cfg config.Config) rutracker.Config {
	rt := cfg.Parser.RuTracker
	useFS := true
	if rt.UseFlareSolverr != nil {
		useFS = *rt.UseFlareSolverr
	}
	repoRoot := strings.TrimSpace(cfg.Compat.RepoRoot)
	if repoRoot == "" {
		repoRoot = "."
	}
	return rutracker.Config{
		Enable:           rt.Enable,
		Host:             rt.Host,
		Login:            rt.Login,
		Password:         rt.Password,
		Cookie:           rt.Cookie,
		UseFlareSolverr:  useFS,
		OnlyAuthorized:   rt.OnlyAuthorized,
		TimeoutSec:       rt.TimeoutSec,
		MaxResults:       rt.MaxResults,
		ResolveTop:       rt.ResolveTop,
		ResolveBudgetSec: rt.ResolveBudgetSec,
		SearchTTLMin:     rt.SearchTTLMin,
		MinIntervalMs:    rt.MinIntervalMs,
		DataDir:          filepath.Join(repoRoot, "database", "rutracker"),
	}
}

// rutrackerReady returns the client with the live config applied, or nil when
// the source is off / has no credentials. Callers stay unconditional.
func rutrackerReady(cfg config.Config) *rutracker.Client {
	c := liveRutracker()
	if c == nil {
		return nil
	}
	c.SetConfig(rutrackerConfigFrom(cfg))
	if !c.Enabled() {
		return nil
	}
	return c
}

// rutrackerAllowed enforces [parser.rutracker] only_authorized. The torrent
// endpoints are deliberately pre-auth (Lampa WebViews lose cookies), so an
// anonymous request is normal traffic — but it still spends OUR single
// account's request budget, which is why the knob exists.
func rutrackerAllowed(r *http.Request, cfg config.Config) bool {
	if !cfg.Parser.RuTracker.OnlyAuthorized {
		return true
	}
	if u, ok := auth.UserFromContext(r.Context()); ok && u != nil {
		return true
	}
	for _, name := range []string{"_lampac_auth", "lampac_token"} {
		if c, err := r.Cookie(name); err == nil && strings.TrimSpace(c.Value) != "" {
			return true
		}
	}
	q := r.URL.Query()
	return strings.TrimSpace(q.Get("token")) != "" || strings.TrimSpace(q.Get("uid")) != ""
}

// rutrackerSearch runs the query, falling back to the original title when the
// Russian one finds nothing (rutracker titles carry both, but a localized name
// that differs from TMDB's spelling does happen).
func rutrackerSearch(ctx context.Context, client *rutracker.Client, title, originalTitle string) []rutracker.Release {
	title = strings.TrimSpace(title)
	originalTitle = strings.TrimSpace(originalTitle)

	rows, err := client.Search(ctx, title)
	if err != nil {
		log.Debug().Err(err).Str("title", title).Msg("rutracker: search failed")
	}
	if len(rows) == 0 && originalTitle != "" && !strings.EqualFold(originalTitle, title) {
		if alt, altErr := client.Search(ctx, originalTitle); altErr == nil {
			rows = alt
		} else {
			log.Debug().Err(altErr).Str("title", originalTitle).Msg("rutracker: search failed (original title)")
		}
	}
	return rows
}

// ---------------------------------------------------------------------------
//  row conversion
// ---------------------------------------------------------------------------

// parselink is what an unresolved row carries instead of a magnet: an HTTP URL
// to our own resolver. TorrServer accepts an HTTP link to a .torrent, so such a
// row still plays; pidtor skips it (it needs a real btih) and that is fine —
// the top rows are resolved before the answer is built.
func rutrackerParselink(base string, topicID int) string {
	return strings.TrimRight(base, "/") + "/parse/rutracker/" + strconv.Itoa(topicID)
}

func rutrackerLink(r rutracker.Release, base string) string {
	if r.Resolved() {
		return r.Magnet
	}
	return rutrackerParselink(base, r.TopicID)
}

// rutrackerRowV1 mirrors JacRed's /api/v1.0/torrents element
// (JacRed/ApiController.cs:238-256), including its `magnet = magnet ?? parselink`
// substitution.
func rutrackerRowV1(r rutracker.Release, base string) map[string]any {
	row := map[string]any{
		"tracker":  "rutracker",
		"url":      r.URL,
		"title":    r.Title,
		"size":     r.SizeBytes,
		"sizeName": r.SizeName,
		"sid":      r.Seeders,
		"pir":      r.Leechers,
		"magnet":   rutrackerLink(r, base),
	}
	if !r.CreatedAt.IsZero() {
		row["createTime"] = r.CreatedAt.Format("2006-01-02T15:04:05")
	}
	if len(r.Types) > 0 {
		row["types"] = r.Types
	}
	if q := rutrackerQuality(r.Title); q > 0 {
		row["quality"] = q
	}
	if s := rutrackerSeasons(r.Title); len(s) > 0 {
		row["seasons"] = s
	}
	return row
}

// rutrackerRowV2 mirrors the Jackett-compatible element
// (JacRed/ApiController.cs:126-150). MagnetUri carries the parselink for
// unresolved rows: the web client drops rows with an empty MagnetUri
// (ddd-client/web/src/lib/torrents.ts:145), and TorrServer resolves the URL.
func rutrackerRowV2(r rutracker.Release, base string) map[string]any {
	link := rutrackerLink(r, base)
	info := map[string]any{"sizeName": r.SizeName}
	if len(r.Types) > 0 {
		info["types"] = r.Types
	}
	if q := rutrackerQuality(r.Title); q > 0 {
		info["quality"] = q
	}
	if s := rutrackerSeasons(r.Title); len(s) > 0 {
		info["seasons"] = s
	}

	row := map[string]any{
		"Tracker":   "rutracker",
		"Details":   r.URL,
		"Title":     r.Title,
		"Size":      r.SizeBytes,
		"Seeders":   r.Seeders,
		"Peers":     r.Leechers,
		"MagnetUri": link,
		"Link":      link,
		"info":      info,
	}
	if !r.CreatedAt.IsZero() {
		row["PublishDate"] = r.CreatedAt.Format("2006-01-02T15:04:05")
	}
	return row
}

var (
	reRTQuality = regexp.MustCompile(`(?i)(2160|1080|720|480)[pр]|\b(4k|uhd)\b`)
	// "S01", "S01-S03", "Сезон 1", "1-8 сезоны", "сезоны 1-3"
	reRTSeasonS   = regexp.MustCompile(`(?i)\bS(\d{1,2})(?:\s*-\s*S?(\d{1,2}))?\b`)
	reRTSeasonRU1 = regexp.MustCompile(`(?i)сезон[ыа]?\s*:?\s*(\d{1,2})(?:\s*[-–]\s*(\d{1,2}))?`)
	reRTSeasonRU2 = regexp.MustCompile(`(?i)(\d{1,2})(?:\s*[-–]\s*(\d{1,2}))?\s*сезон`)
)

// rutrackerQuality returns jacred's numeric quality (2160/1080/720/480), 0 when
// the title says nothing. The listing has no dedicated field, so the title is
// all we have — which is also where pidtor reads quality from.
func rutrackerQuality(title string) int {
	m := reRTQuality.FindStringSubmatch(title)
	if m == nil {
		return 0
	}
	if m[1] != "" {
		v, _ := strconv.Atoi(m[1])
		return v
	}
	return 2160
}

// rutrackerSeasons extracts season numbers from the title. pidtor's per-season
// menu drops any row with an empty Info.Seasons (internal/httpapi/pidtor.go:1091),
// so without this a serial from rutracker would never reach the season picker.
func rutrackerSeasons(title string) []int {
	seen := map[int]bool{}
	add := func(from, to int) {
		if from <= 0 || from > 50 {
			return
		}
		if to < from || to > 50 {
			to = from
		}
		for s := from; s <= to; s++ {
			seen[s] = true
		}
	}
	for _, re := range []*regexp.Regexp{reRTSeasonRU1, reRTSeasonRU2, reRTSeasonS} {
		for _, m := range re.FindAllStringSubmatch(title, -1) {
			from, _ := strconv.Atoi(m[1])
			to := from
			if len(m) > 2 && m[2] != "" {
				to, _ = strconv.Atoi(m[2])
			}
			add(from, to)
		}
		if len(seen) > 0 {
			break
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]int, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Ints(out)
	return out
}

// rutrackerSearchTimeout bounds the merge stage. A search must never make the
// torrent list slower than it is today: on timeout the upstream answer ships
// unchanged.
const rutrackerSearchTimeout = 12 * time.Second

func rutrackerCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), rutrackerSearchTimeout)
}
