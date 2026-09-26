package litesrc

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"golang.org/x/sync/singleflight"
	"net/http"
	"os"
	"strings"
)

// YouTube self-test.
//
// Every YouTube outage this month was discovered by a user, not by us: the anti-bot challenge, the
// emptied cookie file, the byte window, the POT slowdown. Each looked different from the outside
// but all of them show up in the same two numbers — can we still resolve a video, and can we still
// pull more bytes than one URL window holds.
//
// So the server checks that itself on a timer and reports state transitions. The point is not to
// auto-fix everything (that is what the proxy failover and the learned window do) but to make a
// regression visible immediately instead of a day later.

type ytHealth struct {
	OK        bool
	Detail    string
	CheckedAt time.Time
	// Formats/Bytes are what the last probe actually achieved — useful in the admin panel and in
	// the alert text, because "resolve works but bytes stop at 16 MiB" is a completely different
	// failure from "resolve returns nothing".
	Formats int
	Bytes   int64
}

var (
	ytHealthMu    sync.RWMutex
	ytHealthState ytHealth
	// ytAlert is set by the composition root to a function that reaches the admin (TG bot).
	// nil = no alerting configured; the watchdog still records state and logs.
	ytAlert func(text string)
)

// SetYouTubeAlert wires an alert sink (the TG bot admin notifier).
func SetYouTubeAlert(fn func(text string)) { ytAlert = fn }

// YouTubeHealth returns the last self-test result.
func YouTubeHealth() ytHealth {
	ytHealthMu.RLock()
	defer ytHealthMu.RUnlock()
	return ytHealthState
}

const (
	ytWatchdogEvery = 10 * time.Minute
	// ytWatchdogProbeBytes must exceed the POT-less window, otherwise the check passes while real
	// playback still dies a few seconds in — the exact blind spot that let the «500 after 23
	// seconds» bug reach users.
	ytWatchdogProbeBytes = 40 << 20
	// A stable, always-available video. Short enough to be cheap, long enough to exceed the window.
	ytWatchdogVideoID = "dQw4w9WgXcQ"
)

// StartYouTubeWatchdog runs the self-test loop. Safe to call once at startup.
func (y *YoutubeChecker) StartYouTubeWatchdog() {
	go func() {
		// Give the process time to finish booting (cookies, proxies, yt-dlp discovery).
		time.Sleep(90 * time.Second)
		for {
			y.runSelfTest()
			time.Sleep(ytWatchdogEvery)
		}
	}()
}

func (y *YoutubeChecker) runSelfTest() {
	// Самотест — вспомогательный код: его сбой НЕ имеет права ронять сервер. 22.08 nil-writer в
	// fetchChunk уложил прод в crash-loop на 20 минут (28 рестартов) — recover здесь страховка
	// от любого следующего такого сюрприза: логируем и живём дальше.
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("youtube: self-test PANIC (подавлена, сервер продолжает работу)")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	res := ytHealth{CheckedAt: time.Now()}
	formats, _, _, ok := y.extractFormats(ytWatchdogVideoID)
	res.Formats = len(formats)
	switch {
	case !ok || len(formats) == 0:
		res.Detail = "извлечение не дало форматов (анти-бот, куки или прокси)"
	default:
		// Pull past the window: this is what separates a healthy stream from one that dies
		// mid-playback. Uses the same route stack the mux uses, so a proxy problem shows up here.
		urls := pickProbeURLs(formats)
		if len(urls) == 0 {
			res.Detail = "нет прямых URL для проверки"
			break
		}
		n, err := y.probeBytes(ctx, urls[len(urls)-1], ytWatchdogProbeBytes)
		res.Bytes = n
		switch {
		case err != nil && n < ytWatchdogProbeBytes/2:
			res.Detail = fmt.Sprintf("поток оборвался на %.1f МиБ: %v", float64(n)/(1<<20), err)
		case n < ytWatchdogProbeBytes/2:
			res.Detail = fmt.Sprintf("поток отдал лишь %.1f МиБ", float64(n)/(1<<20))
		default:
			res.OK = true
			res.Detail = fmt.Sprintf("%d форматов, поток %.0f МиБ", res.Formats, float64(n)/(1<<20))
		}
	}

	ytHealthMu.Lock()
	prev := ytHealthState
	ytHealthState = res
	ytHealthMu.Unlock()

	if res.OK {
		log.Info().Str("detail", res.Detail).Msg("youtube: self-test ok")
	} else {
		log.Warn().Str("detail", res.Detail).Msg("youtube: SELF-TEST FAILED")
	}
	// Alert only on transitions, so a long outage doesn't spam and a recovery is announced once.
	if ytAlert == nil || (prev.CheckedAt.IsZero() && res.OK) || prev.OK == res.OK {
		return
	}
	if res.OK {
		ytAlert("✅ YouTube снова работает: " + res.Detail)
	} else {
		ytAlert("🔴 YouTube сломался: " + res.Detail)
	}
}

// probeBytes pulls up to `want` bytes through the same routes the mux uses, re-minting when a URL
// hits its window — i.e. it exercises the real download path, not just a single request.
func (y *YoutubeChecker) probeBytes(ctx context.Context, url string, want int64) (int64, error) {
	attempts := y.buildStreamAttempts()
	if len(attempts) == 0 {
		return 0, fmt.Errorf("нет маршрутов")
	}
	var got int64
	const chunk = 1 << 20
	for got < want {
		end := got + chunk - 1
		ua, origin, referer := ytCDNHeaders(url)
		// io.Discard, не nil: fetchChunk пишет в writer — nil ронял процесс (см. fetchChunk)
		n, err := y.fetchChunk(ctx, attempts[0].client, url, ua, origin, referer, got, end, io.Discard)
		got += n
		if err != nil {
			return got, err
		}
		if n == 0 {
			break
		}
	}
	return got, nil
}

// ── mux key registry bounds ──

const (
	// ytMuxKeyTTL must comfortably exceed ytCacheTTL (4h): a player can hold a `?key=…` playlist URL
	// for as long as the quality map that produced it is valid, and longer if the page stays open.
	// Anything shorter reintroduces the permanent «mux job not found» 404 this outlives.
	ytMuxKeyTTL = 12 * time.Hour
	// ytMuxKeyMax bounds memory regardless of TTL. Each entry is a handful of short strings, so this
	// is cheap; it exists only so a scraping burst cannot grow the maps without end.
	ytMuxKeyMax = 20000
)

// pruneMuxKeys drops mux recipes that are far too old to be referenced, and enforces a hard cap.
// Caller must NOT hold muxMu.
func (y *YoutubeChecker) pruneMuxKeys(now time.Time) {
	y.pruneWinners(now) // same tick, same job: keep the long-lived maps bounded

	y.muxMu.Lock()
	defer y.muxMu.Unlock()

	drop := func(hk string) {
		delete(y.muxKeys, hk)
		delete(y.muxVCodecs, hk)
		delete(y.muxProxy, hk)
		delete(y.muxTransA, hk)
		delete(y.muxFMP4, hk)
		delete(y.muxVideoID, hk)
		delete(y.muxKeyAt, hk)
	}

	for hk, at := range y.muxKeyAt {
		if now.Sub(at) > ytMuxKeyTTL {
			drop(hk)
		}
	}
	// A key registered before this field existed has no timestamp — adopt it now rather than
	// treating it as ancient, so an upgrade doesn't 404 everything currently playing.
	for hk := range y.muxKeys {
		if _, ok := y.muxKeyAt[hk]; !ok {
			y.muxKeyAt[hk] = now
		}
	}
	if len(y.muxKeys) <= ytMuxKeyMax {
		return
	}
	// Over the cap: drop the oldest first.
	type aged struct {
		k  string
		at time.Time
	}
	all := make([]aged, 0, len(y.muxKeyAt))
	for hk, at := range y.muxKeyAt {
		all = append(all, aged{hk, at})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	for i := 0; i < len(all)-ytMuxKeyMax; i++ {
		drop(all[i].k)
	}
	log.Info().Int("kept", len(y.muxKeys)).Msg("youtube: mux key registry pruned to the cap")
}

// registerMuxKey stores one hash-key → mux recipe mapping.
//
// It exists so the write happens in ONE place under a DEFERRED unlock. The previous inline version
// held y.muxMu and unlocked at the end of the block, which turned a trivial bug into a total
// outage on 2026-08-17: a nil map (a botched edit left it uninitialised) panicked mid-block, the
// mutex was never released, and from then on nothing could touch the mux state — resolves hung
// forever and ffmpeg never started. With defer, the same panic costs one request instead of the
// whole YouTube subsystem.
func (y *YoutubeChecker) registerMuxKey(muxKey, cacheKey, videoID, vcodec string, usedProxy, transAudio, fmp4 bool) {
	y.muxMu.Lock()
	defer y.muxMu.Unlock()
	if y.muxKeyAt == nil {
		// Defensive: a checker built by a path that forgot the map must not panic here.
		y.muxKeyAt = make(map[string]time.Time)
	}
	y.muxKeys[muxKey] = cacheKey
	y.muxKeyAt[muxKey] = time.Now()
	y.muxVideoID[muxKey] = videoID
	y.muxVCodecs[muxKey] = vcodec
	y.muxProxy[muxKey] = usedProxy
	y.muxTransA[muxKey] = transAudio
	y.muxFMP4[muxKey] = fmp4
}

// --- re-mint deduplication -------------------------------------------------
//
// A mux job runs TWO download pipes (video and audio), each with its own re-mint
// callback — yet a single yt-dlp run returns the formats for both. Left alone, every
// URL swap therefore costs two extractions instead of one, and every concurrent viewer
// of the same video multiplies that again. Extractions are the scarce resource here:
// run too many and YouTube's anti-bot answers with «yt-dlp returned 0 formats» for
// everyone, which is exactly what prod saw on 2026-08-17 (256 of them in ten minutes).
//
// So re-mints collapse: concurrent callers for one video share one extraction, and its
// result is reusable for a few seconds so the sibling pipe swaps to a URL from the same
// mint — which also keeps the audio and video seams aligned.

var (
	ytMintGroup  singleflight.Group
	ytMintMu     sync.Mutex
	ytMintRecent = map[string]ytMintEntry{}
)

type ytMintEntry struct {
	formats []ytFormat
	at      time.Time
}

// ytMintReuse is deliberately short: it only has to span the gap between the video and
// audio pipes noticing the same wall, not act as a real cache.
const ytMintReuse = 12 * time.Second

func (y *YoutubeChecker) cachedMint(videoID string) []ytFormat {
	ytMintMu.Lock()
	defer ytMintMu.Unlock()
	if e, ok := ytMintRecent[videoID]; ok && time.Since(e.at) < ytMintReuse {
		return e.formats
	}
	return nil
}

func (y *YoutubeChecker) storeMint(videoID string, formats []ytFormat) {
	ytMintMu.Lock()
	defer ytMintMu.Unlock()
	ytMintRecent[videoID] = ytMintEntry{formats: formats, at: time.Now()}
	if len(ytMintRecent) > 512 {
		for k, e := range ytMintRecent {
			if time.Since(e.at) >= ytMintReuse {
				delete(ytMintRecent, k)
			}
		}
	}
}

func (y *YoutubeChecker) dropMint(videoID string) {
	ytMintMu.Lock()
	defer ytMintMu.Unlock()
	delete(ytMintRecent, videoID)
}

// mintFreshURL returns a new CDN URL for the same itag as oldURL, or "" if extraction
// failed. A cached mint is only handed back when it actually differs from oldURL —
// otherwise the caller would be told "here's your fresh URL" and hand ffmpeg the very
// URL that just died.
func (y *YoutubeChecker) mintFreshURL(videoID, oldURL string) string {
	itag := ytURLItag(oldURL)
	if videoID == "" || itag == "" {
		return ""
	}
	pick := func(fmts []ytFormat) string {
		for _, f := range fmts {
			if f.URL != "" && ytURLItag(f.URL) == itag {
				return f.URL
			}
		}
		return ""
	}

	if u := pick(y.cachedMint(videoID)); u != "" && u != oldURL {
		return u
	}
	y.dropMint(videoID) // stale or exhausted — force a real extraction

	v, _, _ := ytMintGroup.Do(videoID, func() (any, error) {
		out := mintExtract(y, videoID, itag)
		// An empty result is cached too — a failing video must not be retried by every
		// pipe and every viewer at once.
		y.storeMint(videoID, out)
		return out, nil
	})
	fmts, _ := v.([]ytFormat)
	return pick(fmts)
}

// mintExtract is the extraction step of mintFreshURL, held in a variable so unit tests can
// exercise the caching rules without spawning yt-dlp (which on the prod host meant a test
// run that hung until the 10-minute go-test timeout).
var mintExtract = func(y *YoutubeChecker, videoID, wantItag string) []ytFormat {
	ytURL := "https://www.youtube.com/watch?v=" + videoID
	// Ask the client that actually won extraction for THIS video first, then walk the rest of
	// the ladder. Pinning re-mint to one hardcoded client is what broke prod on 2026-08-27:
	// android was winning extraction, every re-mint asked default, got nothing, and the
	// download was truncated at its first byte wall — «plays for a minute, then 500».
	winner, known := y.winnerFor(videoID)
	clients := ytRemintLadder(winner, known, y.fetchPotNever)
	return mintPickFormats(func(client string) []ytFormat {
		fmts, _, ok := y.runExtract(videoID, ytURL, client, y.curProxy() != "")
		if !ok {
			return nil
		}
		return fmts
	}, clients, wantItag)
}

// mintPickFormats обходит лестницу клиентов и выбирает, чьими форматами
// перевыписывать ссылку. Приоритет — ТОКЕНИЗИРОВАННЫЙ URL нужного itag.
//
// Зачем: ссылка без pot= отдаётся CDN лишь тизерным окном (~17 МиБ), дальше
// 403. Прежний обход возвращал первого клиента, у которого itag просто ЕСТЬ, —
// а это тот же default с той же голой ссылкой; склейка крутилась на одном
// байте 28 раз подряд (прод 2026-09-04, «видео упало на 4:18»), хотя у mweb
// тот же itag лежал с токеном. Тот же itag = тот же файл, докачка с того же
// смещения продолжается без шва.
func mintPickFormats(fetch func(client string) []ytFormat, clients []string, wantItag string) []ytFormat {
	var first, withItag []ytFormat
	for _, client := range clients {
		fmts := fetch(client)
		if len(fmts) == 0 {
			continue
		}
		if first == nil {
			first = fmts
		}
		if wantItag == "" {
			return fmts
		}
		if ytFormatsHaveTokenizedItag(fmts, wantItag) {
			return fmts
		}
		// A client can return a full-looking list that simply lacks the itag we need; the
		// original loop fell through to the next client in that case, so keep doing that.
		if withItag == nil && ytFormatsHaveItag(fmts, wantItag) {
			withItag = fmts
		}
	}
	if withItag != nil {
		return withItag
	}
	return first
}

// ytFormatsHaveTokenizedItag — есть ли для itag ссылка с PO Token (pot=).
func ytFormatsHaveTokenizedItag(fmts []ytFormat, itag string) bool {
	for _, f := range fmts {
		if f.URL != "" && ytURLItag(f.URL) == itag && ytHasPOT(f.URL) {
			return true
		}
	}
	return false
}

// ytFormatsHaveItag reports whether the list carries a usable URL for the given itag.
func ytFormatsHaveItag(fmts []ytFormat, itag string) bool {
	for _, f := range fmts {
		if f.URL != "" && ytURLItag(f.URL) == itag {
			return true
		}
	}
	return false
}

// --- winning-client memory ----------------------------------------------------
//
// Which yt-dlp client works swings from hour to hour (DRM experiments, POT walls, anti-bot).
// Extraction already races the ladder and picks a winner; re-mint has to ask the SAME client,
// or it gets an empty list exactly when the download needs a fresh URL.

var (
	ytWinnerMu sync.Mutex
	ytWinners  = map[string]ytWinnerEntry{} // videoID -> winning client ("" = the default mix)
)

type ytWinnerEntry struct {
	client string
	at     time.Time
}

// ytWinnerTTL — which client works swings within hours, so a stale winner is worse than none;
// it would send every re-mint down a dead ladder rung first.
const (
	ytWinnerTTL = 3 * time.Hour
	ytWinnerMax = 5000
)

func (y *YoutubeChecker) rememberWinner(videoID, client string) {
	if videoID == "" {
		return
	}
	ytWinnerMu.Lock()
	defer ytWinnerMu.Unlock()
	ytWinners[videoID] = ytWinnerEntry{client: client, at: time.Now()}
	if len(ytWinners) > ytWinnerMax {
		y.pruneWinnersLocked(time.Now())
	}
}

// winnerFor returns the client that last won extraction for this video, and whether one is
// known — "" is a meaningful client name (the default mix), so it cannot signal "unknown".
func (y *YoutubeChecker) winnerFor(videoID string) (string, bool) {
	ytWinnerMu.Lock()
	defer ytWinnerMu.Unlock()
	e, ok := ytWinners[videoID]
	if !ok || time.Since(e.at) > ytWinnerTTL {
		return "", false
	}
	return e.client, true
}

// pruneWinnersLocked drops expired entries; if that is not enough it clears the map outright
// rather than growing without bound. Losing the memory only costs one ladder walk per video.
func (y *YoutubeChecker) pruneWinnersLocked(now time.Time) {
	for k, e := range ytWinners {
		if now.Sub(e.at) > ytWinnerTTL {
			delete(ytWinners, k)
		}
	}
	if len(ytWinners) > ytWinnerMax {
		ytWinners = map[string]ytWinnerEntry{}
	}
}

func (y *YoutubeChecker) pruneWinners(now time.Time) {
	ytWinnerMu.Lock()
	defer ytWinnerMu.Unlock()
	y.pruneWinnersLocked(now)
}

// ytRemintLadder orders the clients a re-mint should try: the known winner first, then the
// rest of the ladder as a fallback, never repeating one.
func ytRemintLadder(winner string, known, potNever bool) []string {
	rest := []string{"mweb", "", "tv", "android"}
	if potNever {
		// POT-less: a fresh android_vr URL resets the byte window, so the default mix leads.
		rest = []string{"", "mweb", "tv", "android"}
	}
	out := make([]string, 0, len(rest)+1)
	seen := map[string]bool{}
	if known {
		out = append(out, winner)
		seen[winner] = true
	}
	for _, c := range rest {
		if !seen[c] {
			out = append(out, c)
			seen[c] = true
		}
	}
	return out
}

// --- partial playback after a dead mux ---------------------------------------

// muxSegmentCount reports how many media segments a mux dir actually holds. A failed mux that
// already wrote segments is not a lost cause: the viewer can watch what completed.
func muxSegmentCount(dir string) int {
	if dir == "" {
		return 0
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, "seg") && (strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".m4s")) {
			n++
		}
	}
	return n
}

// serveTruncatedPlaylist hands back the playlist ffmpeg managed to write, closed with
// #EXT-X-ENDLIST so the player treats it as a finished (short) video rather than a stream that
// broke. Without ENDLIST an EVENT playlist keeps being re-fetched and the player eventually
// errors out anyway.
func (y *YoutubeChecker) serveTruncatedPlaylist(w http.ResponseWriter, req *http.Request, entry *ytMuxEntry) {
	data, err := os.ReadFile(entry.M3U8)
	if err != nil || len(data) == 0 {
		http.Error(w, "mux failed: "+entry.Err.Error(), http.StatusInternalServerError)
		return
	}
	body := string(data)
	if !strings.Contains(body, "#EXT-X-ENDLIST") {
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += "#EXT-X-ENDLIST\n"
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, body)
	_ = req
}

// ytProgressiveOnlyClient reports whether a yt-dlp client can only ever return the lone
// progressive format 18 (360p). Such a client is a last resort: it keeps playback alive but
// cannot carry quality, so its health must not be read as "the ladder is fine".
func ytProgressiveOnlyClient(client string) bool {
	return client == "android"
}

// everyLadderClientCooling reports whether every client capable of a real quality ladder is in
// cooldown. The progressive-only client is excluded on purpose: it is a last resort that rarely
// cools, and counting it kept the fail-open from ever firing (see the call site).
func (y *YoutubeChecker) everyLadderClientCooling(now time.Time) bool {
	for _, client := range []string{"", "mweb", "tv", "android"} {
		if ytProgressiveOnlyClient(client) {
			continue
		}
		label := client
		if label == "" {
			label = "default"
		}
		y.mu.RLock()
		coolUntil, cooling := y.stratCooldown[label]
		y.mu.RUnlock()
		if !cooling || !now.Before(coolUntil) {
			return false
		}
	}
	return true
}
