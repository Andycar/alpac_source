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
	tmp := fmt.Sprintf("%s.%d.tmp", y.cookieWorkPath, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Warn().Err(err).Msg("youtube: cannot stage cookie work copy — using the master")
		return y.cookiePath
	}
	if err := os.Rename(tmp, y.cookieWorkPath); err != nil {
		_ = os.Remove(tmp)
		log.Warn().Err(err).Msg("youtube: cannot publish cookie work copy — using the master")
		return y.cookiePath
	}
	return y.cookieWorkPath
}
