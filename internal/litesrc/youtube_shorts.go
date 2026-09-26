package litesrc

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Shorts detection + the per-account «hide Shorts» preference.
//
// There is NO Shorts marker in what we get from yt-dlp: a flat-playlist search returns
// `url: https://www.youtube.com/watch?v=…` for Shorts and regular videos alike, thumbnails are
// letterboxed to 360x202 for both, and duration doesn't separate them either — plenty of ordinary
// videos are under a minute (verified on the box: a 59-second clip is NOT a Short, while a Short
// from a channel's /shorts tab carries no duration at all).
//
// What DOES separate them is asking YouTube: GET /shorts/<id> answers 200 for a real Short and
// 303 (redirect to /watch) for everything else. Calibrated against 4 known Shorts and 2 known
// regular videos. It costs one cheap request per video, so results are cached — a video never
// stops being a Short.

const (
	// shortsCacheTTL is long because the answer is immutable in practice; the TTL exists only so a
	// misclassification (network hiccup answering oddly) eventually self-heals.
	shortsCacheTTL = 14 * 24 * time.Hour
	// shortsProbeTimeout keeps one probe from holding a feed hostage.
	shortsProbeTimeout = 6 * time.Second
	// shortsProbeParallel bounds the fan-out for a whole feed page.
	shortsProbeParallel = 8
	// shortsBrowserUA — YouTube answers the API-ish default with a consent redirect that hides the
	// 200/303 difference; a normal browser UA (plus the cookie jar) gets the real answer.
	shortsBrowserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

type shortsVerdict struct {
	Short   bool      `json:"short"`
	Expires time.Time `json:"expires"`
}

var (
	shortsMu    sync.RWMutex
	shortsCache = map[string]shortsVerdict{}
	// shortsClient is separate from the extraction clients: this is a plain HEAD-ish GET to
	// youtube.com, and it must NOT follow the redirect — the redirect IS the answer.
	shortsClient = &http.Client{
		Timeout: shortsProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

// IsShort reports whether a video is a YouTube Short, consulting the cache first.
// Unknown/unreachable → false: showing a Short by mistake is better than hiding a normal video.
func (y *YoutubeChecker) IsShort(ctx context.Context, videoID string) bool {
	if videoID == "" {
		return false
	}
	shortsMu.RLock()
	v, ok := shortsCache[videoID]
	shortsMu.RUnlock()
	if ok && time.Now().Before(v.Expires) {
		return v.Short
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.youtube.com/shorts/"+videoID, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", shortsBrowserUA)
	if y.cookiePath != "" {
		// The cookie jar carries YouTube's consent cookie; without it every request answers with a
		// consent redirect and Shorts become indistinguishable from regular videos.
		if c := ytConsentCookie(y.cookiePath); c != "" {
			req.Header.Set("Cookie", c)
		}
	}
	resp, err := shortsClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	isShort := resp.StatusCode == http.StatusOK

	shortsMu.Lock()
	shortsCache[videoID] = shortsVerdict{Short: isShort, Expires: time.Now().Add(shortsCacheTTL)}
	shortsMu.Unlock()
	return isShort
}

// annotateShorts fills the "short" field on every card, probing in parallel.
// Cards already carrying a verdict (cache hit) cost nothing.
func (y *YoutubeChecker) annotateShorts(ctx context.Context, cards []map[string]any) {
	if len(cards) == 0 {
		return
	}
	sem := make(chan struct{}, shortsProbeParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range cards {
		id, _ := cards[i]["video_id"].(string)
		if id == "" {
			continue
		}
		wg.Add(1)
		go func(idx int, vid string) {
			defer wg.Done()
			sem <- struct{}{}
			short := y.IsShort(ctx, vid)
			<-sem
			mu.Lock()
			cards[idx]["short"] = short
			mu.Unlock()
		}(i, id)
	}
	wg.Wait()
}

// filterShorts applies a Shorts mode to an already-annotated card list.
//
//	"hide" — drop Shorts (the «отключить» setting)
//	"only" — keep ONLY Shorts (backs a dedicated Shorts section in the clients)
//	""/"all" — untouched
func filterShorts(cards []map[string]any, mode string) []map[string]any {
	if mode != "hide" && mode != "only" {
		return cards
	}
	out := make([]map[string]any, 0, len(cards))
	for _, c := range cards {
		short, _ := c["short"].(bool)
		if (mode == "hide" && short) || (mode == "only" && !short) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ── per-account «Shorts» preference (set from the Telegram mini-app) ──
//
// Stored per Telegram account rather than per device so the choice follows the user across TV,
// phone and browser. Same shape as the «Свой TorrServer» pref: md5-hashed key, 0600 file.

type shortsPrefStore struct {
	path string
	mu   sync.Mutex
	m    map[string]string // md5("ytshorts:"+tgID) → mode ("hide" | "only" | "all")
}

var (
	shortsPrefOnce sync.Once
	shortsPrefInst *shortsPrefStore
)

// ShortsPrefs returns the process-wide preference store, loading it on first use.
func ShortsPrefs(repoRoot string) *shortsPrefStore {
	shortsPrefOnce.Do(func() {
		s := &shortsPrefStore{
			path: filepath.Join(repoRoot, "database", "youtube_shorts_prefs.json"),
			m:    map[string]string{},
		}
		if b, err := os.ReadFile(s.path); err == nil {
			if err := json.Unmarshal(b, &s.m); err != nil {
				log.Warn().Err(err).Str("path", s.path).Msg("youtube: shorts prefs unreadable — starting empty")
			}
		}
		shortsPrefInst = s
	})
	return shortsPrefInst
}

func shortsPrefHash(tgID int64) string {
	h := md5.Sum(fmt.Appendf(nil, "ytshorts:%d", tgID))
	return hex.EncodeToString(h[:])
}

// Get returns the stored mode, or "" when the user never chose one.
func (s *shortsPrefStore) Get(tgID int64) string {
	if s == nil || tgID == 0 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[shortsPrefHash(tgID)]
}

// Set stores the mode; "" or "all" clears it (default = show everything).
func (s *shortsPrefStore) Set(tgID int64, mode string) error {
	if s == nil || tgID == 0 {
		return nil
	}
	s.mu.Lock()
	key := shortsPrefHash(tgID)
	if mode == "" || mode == "all" {
		delete(s.m, key)
	} else {
		s.m[key] = mode
	}
	b, _ := json.MarshalIndent(s.m, "", " ")
	path := s.path
	s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// shortsModeForRequest resolves the mode for a request: an explicit ?shorts= wins (a client asking
// for a dedicated Shorts section), otherwise the account's stored preference applies. Resolving it
// server-side means every client — Lampa, web, Android, tvOS — honours the setting without needing
// to know it exists.
func (y *YoutubeChecker) shortsModeForRequest(r *http.Request) string {
	switch q := r.URL.Query().Get("shorts"); q {
	case "hide", "only", "all":
		return q
	}
	return ShortsPrefs(y.repoRoot).Get(y.tgIDFromRequest(r))
}

// ytConsentCookie pulls the cookies YouTube needs to answer /shorts/<id> straight instead of
// bouncing to consent.youtube.com. Read from the same cookies.txt yt-dlp uses; cached because the
// probe runs per video. Netscape format: domain \t flag \t path \t secure \t expiry \t name \t value.
var (
	consentOnce  sync.Once
	consentValue string
)

func ytConsentCookie(path string) string {
	consentOnce.Do(func() {
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var parts []string
		for _, line := range strings.Split(string(b), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Split(line, "\t")
			if len(f) < 7 || !strings.Contains(f[0], "youtube.com") {
				continue
			}
			parts = append(parts, f[5]+"="+f[6])
		}
		consentValue = strings.Join(parts, "; ")
	})
	return consentValue
}

// ensureCookieWork returns the cookie file to hand to yt-dlp, keeping the master untouched.
//
// yt-dlp REWRITES the file given to --cookies after every run, and its rewrite is LOSSY: measured
// on prod 2026-08-17, a master with SID/SAPISID/LOGIN_INFO came back with none of them (22 lines
// → 14). So the file it is given degrades into an anonymous jar within a run or two, YouTube starts
// answering «Sign in to confirm you're not a bot», and eventually the file is empty — which is
// exactly what happened to the master on 2026-08-14, with no mysterious writer involved.
//
// Locking the master with `chattr +i` is NOT the fix: yt-dlp then dies with «Operation not
// permitted» after printing its JSON, and the search path (ytSearchN/ytPlaylistN) treats a non-zero
// exit as failure — on 2026-08-17 that emptied the whole home feed while OAuth-backed subscriptions
// kept working.
//
// So every run gets a FRESH copy of the master. The write is atomic (temp + rename) so a concurrent
// yt-dlp always opens a complete file, and the master is never handed out.
func (y *YoutubeChecker) ensureCookieWork() string {
	if y.cookiePath == "" {
		return ""
	}
	if y.cookieWorkPath == "" {
		y.cookieWorkPath = y.cookiePath + ".work"
	}
	data, err := os.ReadFile(y.cookiePath)
	if err != nil || len(data) == 0 {
		// Master gone/empty — fall back to it rather than silently dropping authentication.
		return y.cookiePath
	}
	// The temp name must be unique per CALL, not per process. ensureCookieWork runs
	// on EVERY yt-dlp invocation and those overlap (ytdlpSem admits several at once),
	// so a PID-named temp gave every concurrent caller the same path: the first
	// Rename consumed it and the rest failed with ENOENT. Measured on prod
	// 2026-09-01 — «cannot publish cookie work copy» fired ~10 times in 3 hours.
	//
	// The fallback below is what made that race destructive. Handing yt-dlp the
	// master is never safe: its rewrite is lossy (see the doc comment above), so
	// every lost race stripped auth cookies from the master itself. That is how the
	// master decayed from 8780 to 1887 bytes and lost SID/SIDTS/__Secure-1PSID/
	// LOGIN_INFO, which is what makes the tv client answer «The page needs to be
	// reloaded». On failure we reuse the existing work copy — stale but complete,
	// and disposable — and only give up cookies entirely if there is none.
	f, err := os.CreateTemp(filepath.Dir(y.cookieWorkPath), filepath.Base(y.cookieWorkPath)+".*.tmp")
	if err != nil {
		log.Warn().Err(err).Msg("youtube: cannot stage cookie work copy — reusing the previous one")
		return y.existingCookieWork()
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		log.Warn().Err(err).Msg("youtube: cannot stage cookie work copy — reusing the previous one")
		return y.existingCookieWork()
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		log.Warn().Err(err).Msg("youtube: cannot stage cookie work copy — reusing the previous one")
		return y.existingCookieWork()
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		log.Warn().Err(err).Msg("youtube: cannot stage cookie work copy — reusing the previous one")
		return y.existingCookieWork()
	}
	if err := os.Rename(tmp, y.cookieWorkPath); err != nil {
		_ = os.Remove(tmp)
		log.Warn().Err(err).Msg("youtube: cannot publish cookie work copy — reusing the previous one")
		return y.existingCookieWork()
	}
	return y.cookieWorkPath
}

// existingCookieWork is the fallback when staging a fresh copy fails. It returns
// the previous work copy when one exists, and otherwise "" (no --cookies) — never
// the master, because yt-dlp rewrites whatever it is given and that rewrite drops
// authentication cookies for good.
func (y *YoutubeChecker) existingCookieWork() string {
	if st, err := os.Stat(y.cookieWorkPath); err == nil && st.Size() > 0 {
		return y.cookieWorkPath
	}
	return ""
}

// ── learned byte window of a POT-less googlevideo URL ──
//
// A URL without a PO Token serves a fixed number of BYTES and then answers 403/416 forever
// (measured 2026-08-17 with sequential 1 MiB chunks: ~15 MiB on mweb, ~22 MiB on android_vr; the
// same URL with a POT served 20/20 without a hiccup). The size is YouTube's to change, so it is
// learned at runtime rather than hard-coded: every reactive 403 reports how far the dead URL got,
// and the estimate walks down to the smallest observed window.
//
// Downloads re-mint proactively a little before this mark, which turns "two 403s, a wait and a
// retry per window" into a plain URL swap.
var ytWindow struct {
	mu   sync.Mutex
	est  int64
	low  int // consecutive observations below the current estimate
	last time.Time
}

const (
	// ytWindowDefault sits below the smallest window ever measured (15 MiB on mweb, 22 on
	// android_vr) so the very first download already swaps in time.
	ytWindowDefault = 12 << 20
	// ytWindowFloor is a REAL floor, not a formality. It was 4 MiB, and prod 2026-08-17 promptly
	// collapsed the estimate to 3.5 MiB off a single unrelated 403 — after which every download
	// re-minted every 3.5 MiB. Each re-mint is a yt-dlp extraction, so that became an extraction
	// storm: «yt-dlp returned 0 formats» × 256 in ten minutes (anti-bot), torn chunk seams and
	// «Invalid NAL unit size» in ffmpeg. No real window has ever been observed near this value.
	ytWindowFloor = 8 << 20
	// ytWindowShrinkAfter requires the wall to be seen repeatedly before believing it. One 403 can
	// be a dead route, a stale URL or a proxy hiccup; three in a row is a pattern.
	ytWindowShrinkAfter = 3
	// ytRemintMinInterval bounds how often ONE download may swap its URL, regardless of what the
	// window estimate claims. This is the circuit breaker that keeps a bad estimate from turning
	// into a yt-dlp extraction storm.
	ytRemintMinInterval = 8 * time.Second
	// mintFailRetries is how many times a download waits for extraction to recover before it
	// truncates the file. Anti-bot bursts are short; a paused pipe costs the viewer a stall,
	// a closed one costs them an error.
	mintFailRetries = 3
	// ytMuxRetryAfter / ytMuxMaxAttempts bound the retry of a mux job that died. ffmpeg failures
	// are usually transient (an anti-bot burst truncated the download), but a genuinely dead
	// video must not spin ffmpeg and yt-dlp for ever.
	ytMuxRetryAfter  = 5 * time.Second
	ytMuxMaxAttempts = 3
)

// ytWindowEstimate returns the current best guess at the POT-less byte window.
func ytWindowEstimate() int64 {
	ytWindow.mu.Lock()
	defer ytWindow.mu.Unlock()
	if ytWindow.est <= 0 {
		ytWindow.est = ytWindowDefault
	}
	return ytWindow.est
}

// ytLearnWindow records how many bytes a URL served before the CDN cut it off.
//
// Shrinking is deliberately reluctant: the estimate drives how often we re-mint, and re-minting
// costs a yt-dlp extraction, so an over-eager estimate does far more damage (extraction storm →
// anti-bot → nothing plays) than an over-generous one (a few reactive 403s, which the download
// already recovers from).
func ytLearnWindow(served int64) {
	if served < ytWindowFloor {
		return // implausible as a window — treat as an unrelated 403
	}
	target := served - (served / 8) // aim a little under what was actually served
	if target < ytWindowFloor {
		target = ytWindowFloor
	}

	ytWindow.mu.Lock()
	defer ytWindow.mu.Unlock()
	if ytWindow.est <= 0 {
		ytWindow.est = ytWindowDefault
	}
	if target >= ytWindow.est {
		// The URL lasted at least as long as we expected — the estimate is fine, and any earlier
		// low readings were noise.
		ytWindow.low = 0
		return
	}
	ytWindow.low++
	if ytWindow.low < ytWindowShrinkAfter {
		return
	}
	ytWindow.low = 0
	ytWindow.est = target
	ytWindow.last = time.Now()
	log.Info().Int64("window_bytes", target).Msg("youtube: learned a smaller POT-less byte window")
}
