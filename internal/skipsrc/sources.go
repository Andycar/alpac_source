package skipsrc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Endpoints. All are public, imdb/tmdb-keyed APIs — no user credentials are
// involved. Kept together so a dead host or a rotated key is a one-place edit.
const (
	epSkipDB     = "https://api.skipdb.tv/api/segments"
	epSkipMe     = "https://db.skipme.workers.dev/v1/movies"
	epSkipMeUA   = "SkipMe.db/0.0" // the published client string; anything else gets 403
	epIntroHater = "https://introhater.com/api/v1/segments/"
	epIntroHKey  = "introhater_mpv_client" // public read key; without it the API answers 401
	epIntroDB    = "https://api.introdb.app/segments"
	epARM        = "https://arm.haglund.dev/api/v2/imdb" // imdb → MAL, per season
	epAniskip    = "https://api.aniskip.com/v2/skip-times"
	epTheIntroDB = "https://api.theintrodb.org/v3/media"
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
}

type fetcher func(context.Context, *http.Client, Query) result

// sourcesFor returns the fetchers to probe, anime first when it might be anime:
// Aniskip is the most accurate source there, and it is cheap to fail (the ARM
// lookup simply finds no MAL id for non-anime).
func sourcesFor(q Query) []fetcher {
	base := []fetcher{fetchSkipDB, fetchSkipMe, fetchIntroDB, fetchIntroHater, fetchTheIntroDB}
	if q.isEpisode() {
		return append([]fetcher{fetchAniskip}, base...)
	}
	return base
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
	if !getJSON(ctx, c, u.String(), nil, &body) {
		return result{name: "skipdb"}
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

// SkipMe.db: POST with a one-item array; multi-id, so it answers even when only
// a tmdb id is known. Also duration-aware (duration_ms in the request).
func fetchSkipMe(ctx context.Context, c *http.Client, q Query) result {
	req := map[string]any{}
	if q.ImdbID != "" {
		req["imdb_id"] = q.ImdbID
	}
	if q.TmdbID >= 0 {
		req["tmdb_id"] = q.TmdbID
	}
	if q.isEpisode() {
		req["season"], req["episode"] = q.Season, q.Episode
	}
	if q.Duration > 0 {
		req["duration_ms"] = int64(q.Duration * 1000)
	}
	payload, err := json.Marshal([]any{req})
	if err != nil {
		return result{name: "skipme"}
	}
	var arr []struct {
		Intro   []msRange `json:"intro"`
		Recap   []msRange `json:"recap"`
		Credits []msRange `json:"credits"`
	}
	if !postJSON(ctx, c, epSkipMe, payload, epSkipMeUA, &arr) || len(arr) == 0 {
		return result{name: "skipme"}
	}
	out := make([]Segment, 0, 4)
	maxSub := 0
	for cat, list := range map[Category][]msRange{
		CatIntro: arr[0].Intro, CatRecap: arr[0].Recap, CatCredits: arr[0].Credits,
	} {
		for _, r := range list {
			out = appendSeg(out, r.StartMs/1000, r.EndMs/1000, cat, BaseDurationAware, TrustSkipMe, "skipme")
			if r.Submissions > maxSub {
				maxSub = r.Submissions
			}
		}
	}
	return result{name: "skipme", segments: out, signal: min1(float64(maxSub) / 5.0)}
}

type msRange struct {
	StartMs     float64 `json:"start_ms"`
	EndMs       float64 `json:"end_ms"`
	Submissions int     `json:"submissions"`
}

// IntroDB.app: TV only, seconds, with a submission count worth weighing — a
// single submission is one person's guess.
func fetchIntroDB(ctx context.Context, c *http.Client, q Query) result {
	if !q.isEpisode() {
		return result{name: "introdb"}
	}
	u, _ := url.Parse(epIntroDB)
	u.RawQuery = url.Values{
		"imdb_id": {q.ImdbID},
		"season":  {strconv.Itoa(q.Season)},
		"episode": {strconv.Itoa(q.Episode)},
	}.Encode()

	var body map[string]*struct {
		StartSec   float64 `json:"start_sec"`
		EndSec     float64 `json:"end_sec"`
		Confidence float64 `json:"confidence"`
		Subs       float64 `json:"submission_count"`
	}
	if !getJSON(ctx, c, u.String(), nil, &body) {
		return result{name: "introdb"}
	}
	out := make([]Segment, 0, 3)
	best := 0.0
	for key, cat := range map[string]Category{"intro": CatIntro, "recap": CatRecap, "outro": CatCredits} {
		s := body[key]
		if s == nil {
			continue
		}
		out = appendSeg(out, s.StartSec, s.EndSec, cat, BaseAbsolute, TrustAbsolute, "introdb")
		conf := s.Confidence
		if conf == 0 {
			conf = 0.5
		}
		subs := s.Subs
		if subs == 0 {
			subs = 1
		}
		if v := conf * min1(subs/3.0); v > best {
			best = v
		}
	}
	return result{name: "introdb", segments: out, signal: best}
}

// IntroHater: community DB keyed imdb[:season:episode], seconds, free-text label.
func fetchIntroHater(ctx context.Context, c *http.Client, q Query) result {
	id := q.ImdbID
	if q.isEpisode() {
		id = fmt.Sprintf("%s:%d:%d", q.ImdbID, q.Season, q.Episode)
	}
	var arr []struct {
		Start    float64 `json:"start"`
		End      float64 `json:"end"`
		Label    string  `json:"label"`
		Votes    int     `json:"votes"`
		Verified bool    `json:"verified"`
	}
	hdr := map[string]string{"x-api-key": epIntroHKey}
	if !getJSON(ctx, c, epIntroHater+url.PathEscape(id), hdr, &arr) {
		return result{name: "introhater"}
	}
	out := make([]Segment, 0, len(arr))
	sig := 0.0
	for _, s := range arr {
		out = appendSeg(out, s.Start, s.End, normCategory(s.Label), BaseAbsolute, TrustAbsolute, "introhater")
		v := 0.3
		if s.Verified {
			v = 0.5
		}
		if v += min(0.3, float64(s.Votes)*0.1); v > sig {
			sig = v
		}
	}
	return result{name: "introhater", segments: out, signal: sig}
}

// TheIntroDB: tmdb-keyed. Skipped when we have no tmdb id — resolving one costs
// an extra TMDB round trip for a source that is often behind Cloudflare anyway.
func fetchTheIntroDB(ctx context.Context, c *http.Client, q Query) result {
	if q.TmdbID < 0 {
		return result{name: "theintrodb"}
	}
	u, _ := url.Parse(epTheIntroDB)
	v := url.Values{"tmdb_id": {strconv.FormatInt(q.TmdbID, 10)}}
	if !q.IsMovie && q.isEpisode() {
		v.Set("season", strconv.Itoa(q.Season))
		v.Set("episode", strconv.Itoa(q.Episode))
	}
	u.RawQuery = v.Encode()

	var body struct {
		Intro   []msRange `json:"intro"`
		Recap   []msRange `json:"recap"`
		Credits []msRange `json:"credits"`
	}
	if !getJSON(ctx, c, u.String(), nil, &body) {
		return result{name: "theintrodb"}
	}
	out := make([]Segment, 0, 4)
	for cat, list := range map[Category][]msRange{
		CatIntro: body.Intro, CatRecap: body.Recap, CatCredits: body.Credits,
	} {
		for _, r := range list {
			out = appendSeg(out, r.StartMs/1000, r.EndMs/1000, cat, BaseAbsolute, TrustAbsolute, "theintrodb")
		}
	}
	return result{name: "theintrodb", segments: out, signal: 0.8}
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
	if !getJSON(ctx, c, armURL, nil, &entries) {
		return result{name: "aniskip"}
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
	if !getJSON(ctx, c, skipURL, nil, &body) {
		return result{name: "aniskip"}
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

func getJSON(ctx context.Context, c *http.Client, rawURL string, headers map[string]string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doJSON(c, req, out)
}

func postJSON(ctx context.Context, c *http.Client, rawURL string, payload []byte, ua string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	return doJSON(c, req, out)
}

func doJSON(c *http.Client, req *http.Request, out any) bool {
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// 404 is the normal "nothing known about this title" answer everywhere here,
	// and a 403 from Cloudflare is just as uninteresting — both are "no data".
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(string(body)), "<") {
		return false // an HTML error page, not JSON
	}
	return json.Unmarshal(body, out) == nil
}

func min1(v float64) float64 { return min(1.0, v) }
