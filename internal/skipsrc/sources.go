package skipsrc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Endpoints. All are public, imdb/tmdb-keyed APIs — no user credentials are
// involved. Kept together so a dead host or a rotated key is a one-place edit
// (variables only so tests can point a source at a local server).
//
// Gone for good, checked 2026-09-25: SkipMe.db answers 410 "Service termination",
// IntroHater 503 "suspended by its owner". Both had been failing for weeks
// without a trace — back then a failure and a miss looked the same (see errNoData).
// TheIntroDB is out by choice: its terms (2026-09-20, §4.3/§6) forbid server-side
// aggregation on behalf of many viewers and commercial use without a written
// license, and its 500 requests/day per IP ran out every morning (429).
var (
	epSkipDB  = "https://api.skipdb.tv/api/segments"
	epIntroDB = "https://api.introdb.app/segments"
	epARM     = "https://arm.haglund.dev/api/v2/imdb" // imdb → MAL, per season
	epAniskip = "https://api.aniskip.com/v2/skip-times"
)

// Query is one lookup: what to find timings for, and — crucially — how long the
// actual file is.
type Query struct {
	ImdbID   string
	TmdbID   int64 // -1 when unknown
	Season   int
	Episode  int
	Duration float64 // seconds of THIS file; 0 = unknown
	IsMovie  bool
}

func (q Query) isEpisode() bool { return q.Season >= 1 && q.Episode >= 1 }

// result is what one source returned, before voting.
type result struct {
	name     string
	segments []Segment
	signal   float64
	err      error // the source could not answer (never errNoData — a miss is not a failure)
}

type fetcher func(context.Context, *http.Client, Query) result

// sourcesFor returns the fetchers to probe, anime first when it might be anime:
// Aniskip is the most accurate source there, and it is cheap to fail (the ARM
// lookup simply finds no MAL id for non-anime).
func sourcesFor(q Query) []fetcher {
	base := []fetcher{fetchSkipDB, fetchIntroDB}
	if q.isEpisode() {
		return append([]fetcher{fetchAniskip}, base...)
	}
	return base
}

// failed is the empty result for a source that could not answer. A plain miss
// (errNoData) is not a failure and is not reported.
func failed(name string, err error) result {
	if errors.Is(err, errNoData) {
		return result{name: name}
	}
	return result{name: name, err: err}
}

// ── individual sources ──

// SkipDB: {segments:{intro,recap,outro}}, each {start_ms,end_ms,confidence}.
// Accepts the runtime, so its answers are already in this file's coordinates.
func fetchSkipDB(ctx context.Context, c *http.Client, q Query) result {
	u, _ := url.Parse(epSkipDB)
	v := url.Values{"imdb_id": {q.ImdbID}}
	if q.isEpisode() {
		v.Set("season", strconv.Itoa(q.Season))
		v.Set("episode", strconv.Itoa(q.Episode))
	}
	if q.Duration > 0 {
		v.Set("duration", strconv.FormatInt(int64(q.Duration), 10))
	}
	u.RawQuery = v.Encode()

	var body struct {
		Segments map[string]*struct {
			StartMs    float64 `json:"start_ms"`
			EndMs      float64 `json:"end_ms"`
			Confidence float64 `json:"confidence"`
		} `json:"segments"`
	}
	if err := getJSON(ctx, c, u.String(), nil, &body); err != nil {
		return failed("skipdb", err)
	}
	out := make([]Segment, 0, 3)
	sum, n := 0.0, 0
	for key, cat := range map[string]Category{"intro": CatIntro, "recap": CatRecap, "outro": CatCredits} {
		s := body.Segments[key]
		if s == nil {
			continue
		}
		out = appendSeg(out, s.StartMs/1000, s.EndMs/1000, cat, BaseDurationAware, TrustDurationAware, "skipdb")
		conf := s.Confidence
		if conf == 0 {
			conf = 0.5
		}
		sum, n = sum+conf, n+1
	}
	sig := 0.5
	if n > 0 {
		sig = sum / float64(n)
	}
	return result{name: "skipdb", segments: out, signal: sig}
}

// IntroDB.app: imdb-keyed, seconds, with a submission count worth weighing — a
// single submission is one person's guess. Episodes by season/episode, movies by
// is_movie=true. The answer carries scalar fields (imdb_id, media_type, is_movie…)
// next to the segments since mid-2026, so it is decoded into a struct: the old
// map decode failed on them and the source went silent. post_credits is a scene
// worth watching, not skipping — ignored.
func fetchIntroDB(ctx context.Context, c *http.Client, q Query) result {
	if q.ImdbID == "" {
		return result{name: "introdb"}
	}
	v := url.Values{"imdb_id": {q.ImdbID}}
	switch {
	case q.isEpisode():
		v.Set("season", strconv.Itoa(q.Season))
		v.Set("episode", strconv.Itoa(q.Episode))
	case q.IsMovie:
		v.Set("is_movie", "true")
	default:
		return result{name: "introdb"} // a season without an episode: nothing to ask
	}
	u, _ := url.Parse(epIntroDB)
	u.RawQuery = v.Encode()

	type seg struct {
		StartSec   float64 `json:"start_sec"`
		EndSec     float64 `json:"end_sec"`
		Confidence float64 `json:"confidence"`
		Subs       float64 `json:"submission_count"`
	}
	var body struct {
		Intro *seg `json:"intro"`
		Recap *seg `json:"recap"`
		Outro *seg `json:"outro"`
	}
	if err := getJSON(ctx, c, u.String(), nil, &body); err != nil {
		return failed("introdb", err)
	}
	out := make([]Segment, 0, 3)
	best := 0.0
	for _, p := range []struct {
		s   *seg
		cat Category
	}{{body.Intro, CatIntro}, {body.Recap, CatRecap}, {body.Outro, CatCredits}} {
		if p.s == nil {
			continue
		}
		out = appendSeg(out, p.s.StartSec, p.s.EndSec, p.cat, BaseAbsolute, TrustAbsolute, "introdb")
		conf := p.s.Confidence
		if conf == 0 {
			conf = 0.5
		}
		subs := p.s.Subs
		if subs == 0 {
			subs = 1
		}
		if v := conf * min1(subs/3.0); v > best {
			best = v
		}
	}
	return result{name: "introdb", segments: out, signal: best}
}

// Aniskip, via ARM for the imdb→MAL mapping. Anime releases are typically the
// broadcast cut, which is why its absolute times outrank duration-aware sources
// here (TrustAnime) instead of the other way round.
func fetchAniskip(ctx context.Context, c *http.Client, q Query) result {
	if q.Episode < 1 {
		return result{name: "aniskip"}
	}
	// NB: never pass ?include= to ARM — it drops the *-season fields we match on.
	armURL := epARM + "?" + url.Values{"id": {q.ImdbID}}.Encode()
	var entries []struct {
		MAL     *int64 `json:"myanimelist"`
		TmdbSea *int   `json:"themoviedb-season"`
		TvdbSea *int   `json:"thetvdb-season"`
	}
	if err := getJSON(ctx, c, armURL, nil, &entries); err != nil {
		return failed("aniskip", fmt.Errorf("arm: %w", err))
	}
	var malID int64 = -1
	for _, e := range entries {
		if e.MAL == nil {
			continue
		}
		season := -1
		switch {
		case e.TmdbSea != nil:
			season = *e.TmdbSea
		case e.TvdbSea != nil:
			season = *e.TvdbSea
		}
		if season == q.Season {
			malID = *e.MAL
			break
		}
	}
	if malID < 0 {
		return result{name: "aniskip"} // not anime, or no MAL entry for this season
	}
	skipURL := fmt.Sprintf("%s/%d/%d?%s", epAniskip, malID, q.Episode,
		"types=op&types=ed&types=recap&episodeLength=0")
	var body struct {
		Results []struct {
			SkipType string `json:"skipType"`
			Interval struct {
				Start float64 `json:"startTime"`
				End   float64 `json:"endTime"`
			} `json:"interval"`
		} `json:"results"`
	}
	if err := getJSON(ctx, c, skipURL, nil, &body); err != nil {
		return failed("aniskip", err)
	}
	out := make([]Segment, 0, len(body.Results))
	for _, r := range body.Results {
		out = appendSeg(out, r.Interval.Start, r.Interval.End,
			normCategory(r.SkipType), BaseAbsolute, TrustAnime, "aniskip")
	}
	return result{name: "aniskip", segments: out, signal: 0.9}
}

// ── plumbing ──

func appendSeg(out []Segment, start, end float64, cat Category, base CoordBase, trust int, src string) []Segment {
	// An end at or before the start is not a segment; an "open" end (the 99999
	// sentinel some APIs use for "till the end of file") is only meaningful for
	// credits — anywhere else it would swallow the rest of the episode.
	if start < 0 || end <= start {
		return out
	}
	if end >= 99000 && cat != CatCredits {
		return out
	}
	return append(out, Segment{
		Category: cat, Start: start, End: end,
		Base: base, Trust: trust, Source: src, Votes: 1,
	})
}

// errNoData is the normal miss: a 404, an empty body or a JSON null — the source
// is fine, it just knows nothing about this title. Everything else getJSON
// returns is a failure worth reporting: a dead host (410/503), a Cloudflare wall
// (403), an HTML error page, or a reshaped answer we can no longer decode.
var errNoData = errors.New("no data")

func getJSON(ctx context.Context, c *http.Client, rawURL string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doJSON(c, req, out)
}

func doJSON(c *http.Client, req *http.Request, out any) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return errNoData // the usual "nothing known about this title" everywhere here
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	t := bytes.TrimSpace(body)
	if len(t) == 0 || string(t) == "null" {
		return errNoData
	}
	if t[0] == '<' {
		return fmt.Errorf("HTML instead of JSON: %s", snippet(body))
	}
	if err := json.Unmarshal(t, out); err != nil {
		return fmt.Errorf("answer no longer decodes: %w", err)
	}
	return nil
}

var (
	reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reSpace = regexp.MustCompile(`\s+`)
)

// snippet is what goes into the log about a bad answer: an HTML page's title
// ("Service Suspended"), otherwise the start of the body on one line.
func snippet(body []byte) string {
	s := string(body)
	if m := reTitle.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	s = strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return s
}

func min1(v float64) float64 { return min(1.0, v) }
