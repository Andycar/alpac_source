package httpapi

// Voice enumeration for the calendar's new-translation tracking.
//
// The calendar package can't reach the balancers itself (importing httpapi
// would be a cycle), so it takes a calendar.VoiceLister and this supplies it.
//
// The reference implementation this feature is modelled on polls three hardcoded
// sources and compares the highest episode number each one exposes. We reuse the
// aggregator instead: capiResolveStreams already drills every source the admin
// curated in resolve_sources, dedupes by voice and hands back the merged voice
// list — so tracking automatically covers whatever sources the deployment has,
// with no per-source code and no second definition of "what counts as a voice".

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strconv"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

// calendarVoiceLister builds the callback the calendar cron uses to ask "which
// translations exist for this title right now".
func calendarVoiceLister(cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry) func(ctx context.Context, tmdbID int, kind string, season, episode int) []string {
	return func(ctx context.Context, tmdbID int, kind string, season, episode int) []string {
		if tmdbID <= 0 {
			return nil
		}

		// Synthetic request, same shape the checksearch probe uses: the resolve
		// path is HTTP-shaped but runs in-process, and capiEnrichSearchMeta fills
		// title/year from TMDB, so an id plus a media type is enough input.
		q := url.Values{}
		q.Set("id", strconv.Itoa(tmdbID))
		q.Set("tmdb_id", strconv.Itoa(tmdbID))
		if kind == "movie" {
			q.Set("type", "movie")
		} else {
			q.Set("type", "tv")
			q.Set("serial", "1")
			// ★s/e are MANDATORY for a series. Without them the drill stops at the
			// season/episode listing and hands back episode titles, which the caller
			// cannot tell apart from voice names.
			if season <= 0 || episode <= 0 {
				return nil
			}
			q.Set("s", strconv.Itoa(season))
			q.Set("e", strconv.Itoa(episode))
		}

		r := httptest.NewRequest("GET", "http://127.0.0.1/capi/streams?"+q.Encode(), nil)
		r = r.WithContext(ctx)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Lampac-Go", "1")

		// cacheKey "" = don't write the streams cache. A background poll must not
		// evict or shape what real viewers get served; it only wants the names.
		streams := capiResolveStreams(r, liveConfig(cfg), proxyLinks, dynRoutes, "127.0.0.1", "")

		out := make([]string, 0, len(streams.Voices))
		seen := make(map[string]struct{}, len(streams.Voices))
		for _, v := range streams.Voices {
			name := v.Name
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
		return out
	}
}
