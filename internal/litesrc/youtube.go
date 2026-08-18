package litesrc

import (
	"bytes"
	"context"
	"crypto/md5"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/antidpi"
	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/ytauth"

	"github.com/rs/zerolog/log"
)

func md5Hash(s string) [16]byte {
	return md5.Sum([]byte(s))
}

type YoutubeChecker struct {
	ytdlpPath      string
	ffmpegPath     string
	cookiePath     string // master cookies.txt — read-only for us, never handed to yt-dlp
	cookieWorkPath string // disposable copy actually passed to --cookies (see ensureCookieWork)
	jsRuntime      string // "node", "deno", or "" (auto)
	pluginDirs     string // comma-separated dirs for yt-dlp --plugin-dirs (pip plugin locations)
	proxyAddr      string // SOCKS5 proxy address for yt-dlp extraction (AntiDPI or VLESS)
	vlessProxyAddr string // VLESS SOCKS5 proxy for CDN stream fallback (encrypted tunnel bypasses DPI)
	fetchPotNever  bool   // [youtube] fetch_pot="never" — запретить bgutil PO Token (см. config)
	// Авто-фейловер YouTube-выхода: [youtube] proxy = "a:1,b:2,c:3" — список SOCKS5
	// кандидатов. ytProxyActive хранит текущий живой (atomic — читается на hot-path
	// экстракции/скачивания), health-горутина пере-выбирает его, когда выход умирает
	// (инциденты 14-15.08: не-RU vless-узел :40002/:40010 отваливался за ночь → все
	// стратегии в hard-cooldown, YouTube мёртв до ручной правки конфига).
	ytProxyCandidates []string
	ytProxyActive     atomic.Pointer[string]
	extractFail       atomic.Int32 // подряд «all strategies failed» → триггер ротации выхода
	proxyRotating     atomic.Bool  // один rotate за раз
	repoRoot          string
	ytdlpMu           sync.Mutex
	// ytdlpSem caps concurrent yt-dlp invocations across all VIDs. Without
	// it, opening "Subscriptions" in Lampa (12 trailers × N strategies)
	// floods YouTube with parallel requests and triggers anti-bot/429.
	ytdlpSem chan struct{}
	mu       sync.RWMutex
	cache    map[string]*ytFormatCache
	// stratCooldown blocks an extraction client whose stream URLs failed the
	// download probe (see probeStreamURL). YouTube poisons whole client
	// sessions at once (mweb URLs with a rejected PO Token, tv DRM
	// experiment), so one probed failure predicts the next N videos — skip
	// the client for a while instead of re-extracting and re-probing it on
	// every cache-missed VID. Key: client label ("mweb", "default", ...).
	stratCooldown map[string]time.Time

	muxMu      sync.Mutex
	muxCache   map[string]*ytMuxEntry // cacheKey (videoURL\x00audioURL) → mux entry
	muxKeys    map[string]string      // hash key → cacheKey
	muxVideoID map[string]string      // hash key → videoID (for POT-flap URL re-mint mid-download)
	muxVCodecs map[string]string      // hash key → video codec (avc1, vp09, av01)
	muxProxy   map[string]bool        // hash key → true if URL extracted via proxy (ffmpeg needs proxy too)
	muxTransA  map[string]bool        // hash key → true if audio must be transcoded to AAC for HLS/TS
	muxFMP4    map[string]bool        // hash key → true for VP9/AV1 (fMP4 segments)

	// Master playlist storage: maps masterKey → list of HLS variants.
	// Used by /lite/youtube/mux/master.m3u8 to return a multi-bitrate master,
	// so hls.js shows the quality picker in the player.
	masterCache map[string]ytMasterEntry

	// PO Token cache (bgutil-ytdlp-pot-provider HTTP API on :4416).
	// Key format: "<client>:<videoID>", e.g. "IOS:SBmYseRTV_o".
	potMu    sync.Mutex
	potCache map[string]potEntry

	// Auth fields are set from server bootstrap AFTER the checker is
	// published in globalYTChecker (server.go), and read by /api/youtube/*
	// handlers. atomic.Pointer keeps reads lock-free and publication safe.
	// Access via ytAPI()/tgStore() helpers below.
	ytAPIPtr   atomic.Pointer[ytauth.APIClient]
	tgStorePtr atomic.Pointer[tgauth.Store]
}

// SetAuth atomically replaces the auth fields after the checker has been
// published. Safe to call from any goroutine.
func (y *YoutubeChecker) SetAuth(api *ytauth.APIClient, store *tgauth.Store) {
	y.ytAPIPtr.Store(api)
	y.tgStorePtr.Store(store)
}

// ytAPI returns the current YouTube Data API client (nil before OAuth init).
func (y *YoutubeChecker) ytAPI() *ytauth.APIClient { return y.ytAPIPtr.Load() }

// tgStore returns the current tgauth store (nil before bot init).
func (y *YoutubeChecker) tgStore() *tgauth.Store { return y.tgStorePtr.Load() }

// ytMuxEntry tracks an in-progress or completed ffmpeg HLS mux job.
type ytMuxEntry struct {
	VideoID    string        // for POT-flap re-mint of stream URLs mid-download
	Dir        string        // temp directory with HLS segments
	M3U8       string        // path to playlist.m3u8
	Ready      chan struct{} // closed when first segment is ready
	Done       chan struct{} // closed when ffmpeg finishes
	Err        error
	Expires    time.Time
	NeedsProxy bool // true if URL was extracted via SOCKS5 proxy (ffmpeg must also use proxy)
	TransA     bool // true if audio must be transcoded to AAC for compatibility
	FMP4       bool // true for VP9/AV1 — use fMP4 segments instead of TS
	// truncated is set by a pipeDownloadStream that gave up mid-file (written < total).
	// ffmpeg then exits 0 on pipe EOF, so without this flag a cut-off mux is
	// indistinguishable from a completed one.
	truncated atomic.Bool
}

// ytMasterEntry holds the list of HLS variants for a master playlist.
// Master playlists let hls.js render a quality picker in the Lampa player.
type ytMasterEntry struct {
	Variants []ytMasterVariant
	Expires  time.Time
}

type ytMasterVariant struct {
	Label      string // "1080p"
	Height     int
	Width      int
	Bandwidth  int    // bits/sec for EXT-X-STREAM-INF
	Codecs     string // "avc1.640028,mp4a.40.2" — informational; ok to leave empty
	VariantURL string // full URL to /lite/youtube/mux/index.m3u8?key=...
}

const (
	ytMuxTTL        = 30 * time.Minute // reduced from 4h to limit /tmp disk usage
	ytMuxMaxSize    = 10               // reduced from 20 to limit /tmp disk usage
	ytMuxMaxTimeout = 14 * time.Hour   // kill ffmpeg if it runs longer than this (allows 12h+ videos)
	ytMaxDuration   = 43200            // refuse to mux videos longer than 12 hours (seconds)
	// ytMuxMaxDirBytes stops a mux whose /tmp dir grows past this. ytMaxDuration
	// guards VODs by duration, but LIVESTREAMS report no duration and slip through:
	// with -hls_list_size 0 they append segments for the whole 14h timeout (prod saw
	// a single yt-mux dir reach 28GB). A size guard bounds the worst case regardless.
	ytMuxMaxDirBytes int64 = 8 << 30 // 8 GiB — kills runaway live/oversized muxes, allows normal movies
	// ytChunkSize is the byte-range chunk for downloading googlevideo streams. A single continuous GET
	// of a googlevideo URL gets progressively throttled (the stream slows below real-time after the
	// first few MB → ffmpeg starves → segments stop → 404 flood). Fetching in fresh ranged chunks
	// dodges the throttle — this is exactly what yt-dlp does (--http-chunk-size).
	// ★2026-08-14: googlevideo начал 403-ить POT-less запросы с Range БОЛЬШЕ ~1 МиБ (порог замерен
	// на проде: 0-1048575 → 206, 0-2097151 → 403; пробы по 1 КБ проходили, отсюда «probe ok, mux
	// failed»). 1 МиБ — максимальный чанк, который CDN сейчас отдаёт без PO Token.
	ytChunkSize = 1 << 20 // 1 MiB
	// ytDefaultMuxHeight caps the DEFAULT auto-played quality. The player gets a single media playlist
	// (one ffmpeg mux job), not a multi-bitrate master — so it no longer fans out one real-time mux per
	// quality (6+ parallel googlevideo downloads → wasted bandwidth, 403 rate-limits, /tmp churn). The
	// full `qualitys` list is still returned, so the manual quality picker can switch on demand (one job
	// at a time). 1080p keeps default load sane; copy-mux is cheap, but a 4K real-time mux can't keep up.
	ytDefaultMuxHeight = 1080
)

// potEntry caches a PO Token fetched from bgutil-ytdlp-pot-provider.
type potEntry struct {
	Token   string
	Expires time.Time
}

// fetchPOT requests a PO Token from bgutil-ytdlp-pot-provider (HTTP mode on
// 127.0.0.1:4416). Returns "" on any failure — caller should fall back to
// non-POT extraction. Cached per client+videoID until token expiry.
// Required for `ios` / `android` clients that YouTube otherwise serves only
// 360p progressive without a GVS PO Token.
func (y *YoutubeChecker) fetchPOT(client, videoID string) string {
	cacheKey := client + ":" + videoID
	y.potMu.Lock()
	if e, ok := y.potCache[cacheKey]; ok && time.Now().Before(e.Expires.Add(-60*time.Second)) {
		tok := e.Token
		y.potMu.Unlock()
		return tok
	}
	y.potMu.Unlock()

	body, _ := stdjson.Marshal(map[string]string{
		"client":          client,
		"content_binding": videoID,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:4416/get_pot", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	var out struct {
		PoToken   string `json:"poToken"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := stdjson.NewDecoder(resp.Body).Decode(&out); err != nil || out.PoToken == "" {
		return ""
	}
	expires := time.Now().Add(2 * time.Hour)
	if t, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil {
		expires = t
	}
	y.potMu.Lock()
	if y.potCache == nil {
		y.potCache = make(map[string]potEntry)
	}
	y.potCache[cacheKey] = potEntry{Token: out.PoToken, Expires: expires}
	y.potMu.Unlock()
	return out.PoToken
}

// ytCDNHeaders returns the User-Agent / Origin / Referer that must be sent
// when fetching a YouTube googlevideo.com stream URL. YouTube binds stream
// URLs to the client that extracted them (via the &c= parameter) and returns
// 403 Forbidden if the downloading client's User-Agent doesn't match.
func ytCDNHeaders(streamURL string) (ua, origin, referer string) {
	ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	origin = "https://www.youtube.com"
	referer = "https://www.youtube.com/"
	switch {
	case strings.Contains(streamURL, "c=MWEB"):
		ua = "Mozilla/5.0 (Linux; Android 14; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36"
		origin = "https://m.youtube.com"
		referer = "https://m.youtube.com/"
	case strings.Contains(streamURL, "c=ANDROID_VR"):
		ua = "com.google.android.apps.youtube.vr.oculus/1.56.21 (Linux; U; Android 12; Quest 3) gzip"
	case strings.Contains(streamURL, "c=IOS"):
		ua = "com.google.ios.youtube/19.45.4 (iPhone16,2; U; CPU iOS 18_1_0 like Mac OS X;)"
	case strings.Contains(streamURL, "c=ANDROID"):
		ua = "com.google.android.youtube/19.44.38 (Linux; U; Android 14; SM-S918B) gzip"
	}
	return
}

// ytFormatCache holds extracted formats for a video.
type ytFormatCache struct {
	Formats   []ytFormat
	Duration  float64
	Expires   time.Time
	UsedProxy bool // true if formats were extracted via SOCKS5 proxy
}

// ytFormat represents a single stream from yt-dlp -j output.
type ytFormat struct {
	FormatID string  `json:"format_id"`
	Ext      string  `json:"ext"`
	URL      string  `json:"url"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	VCodec   string  `json:"vcodec"`
	ACodec   string  `json:"acodec"`
	ABR      float64 `json:"abr"`
	TBR      float64 `json:"tbr"`
	VBR      float64 `json:"vbr"`
	FileSize int64   `json:"filesize"`
	Protocol string  `json:"protocol"`
	// DASH byte ranges (yt-dlp adaptive YouTube formats) — needed to build a valid on-demand
	// MPD SegmentBase so MSE players can index the fragmented MP4. Absent on progressive formats.
	IndexRange *ytByteRange `json:"index_range"`
	InitRange  *ytByteRange `json:"init_range"`
}

// ytByteRange is a yt-dlp {start,end} byte range. yt-dlp has emitted these as JSON numbers in some
// versions and as strings in others, so the fields tolerate both (and degrade to 0 on anything else).
type ytByteRange struct {
	Start flexInt `json:"start"`
	End   flexInt `json:"end"`
}

// rangeAttr renders the range as the "start-end" string DASH expects, or "" when unusable.
// (flexInt — declared in kp_catalog.go — tolerates yt-dlp emitting start/end as number or string.)
func (r *ytByteRange) rangeAttr() string {
	if r == nil || r.End <= 0 || r.End < r.Start {
		return ""
	}
	return strconv.Itoa(int(r.Start)) + "-" + strconv.Itoa(int(r.End))
}

func (f ytFormat) isVideoOnly() bool {
	// Exclude "audio only" (yt-dlp HLS audio marker) and require real resolution.
	if f.VCodec == "" || f.VCodec == "none" || f.VCodec == "audio only" {
		return false
	}
	if f.Height <= 0 && f.Width <= 0 {
		return false
	}
	return f.ACodec == "" || f.ACodec == "none"
}

func (f ytFormat) isAudioOnly() bool {
	// "No video" means it's an audio-only stream. HLS audio (iOS 233/234) often
	// reports acodec=null in JSON (empty string in Go) — we don't require an
	// explicit ACodec to classify a no-video format as audio-only. Non-audio
	// formats (subtitles, storyboards) are filtered out elsewhere by Ext/Protocol.
	if f.VCodec != "" && f.VCodec != "none" && f.VCodec != "audio only" {
		return false
	}
	return f.Height <= 0 && f.Width <= 0
}

func (f ytFormat) isCombined() bool {
	return f.VCodec != "" && f.VCodec != "none" && f.ACodec != "" && f.ACodec != "none"
}

// ytSearchResult maps the JSON from yt-dlp --flat-playlist --dump-single-json.
// The top-level playlist fields (channel/channel_id/thumbnails) are filled when the
// target is a channel's /videos page — that's where the channel header meta comes from.
type ytSearchResult struct {
	Entries    []ytEntry `json:"entries"`
	Title      string    `json:"title"`
	Channel    string    `json:"channel"`
	ChannelID  string    `json:"channel_id"`
	Uploader   string    `json:"uploader"`
	Thumbnails []struct {
		URL string `json:"url"`
		ID  string `json:"id"`
	} `json:"thumbnails"`
}

type ytEntry struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Duration float64 `json:"duration"`
	URL      string  `json:"url"`
	Channel  string  `json:"channel"`
	Uploader string  `json:"uploader"`
	// Search cards showed only title/channel/duration; clients asked for views + age.
	// yt-dlp's flat-playlist search fills view_count and channel_id reliably; the timestamp
	// fields are best-effort (often absent in flat mode) and simply stay empty when missing.
	ViewCount        int64  `json:"view_count"`
	ChannelID        string `json:"channel_id"`
	Timestamp        int64  `json:"timestamp"`
	ReleaseTimestamp int64  `json:"release_timestamp"`
	UploadDate       string `json:"upload_date"` // YYYYMMDD
}

// ytDumpJSON maps the JSON from yt-dlp -j
type ytDumpJSON struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Duration float64    `json:"duration"`
	Formats  []ytFormat `json:"formats"`
}

const (
	ytCacheTTL     = 4 * time.Hour
	ytCacheMaxSize = 500
	ytSearchLimit  = 10
	// stratCooldownTTL — клиент не вернул форматов (DRM-эксперимент, POT-стена,
	// ИЛИ выход дал сбой у всех клиентов сразу). Раньше было 15м — но проблема чаще
	// в выходе, чем в самом клиенте, и 15-минутный блэкаут при живом ютубе бил больно
	// (инцидент 15.08). 5м: и повторную экстракцию на cache-miss не жжём, и быстро
	// оживаем; ротация выхода при стрике сбрасывает кулдаун сразу.
	stratCooldownTTL = 5 * time.Minute
	// stratProbeCooldownTTL — экстракция прошла, но URL не скачались (probe-fail).
	// ★2026-08-14 это МИГАНИЕ, а не смерть: mweb-POT и POT-less «грейс-окно» то
	// отдают файл целиком, то 403-ят за окном в пределах минут. 15-минутный
	// кулдаун здесь сажал ЕДИНСТВЕННЫЙ рабочий клиент (mweb) на четверть часа при
	// одном мигании → весь трафик валился в android=360p. Короткий кулдаун даёт
	// mweb быстро вернуться; POT-less default тоже ретраится (он валит вторую
	// фазу пробы для длинных видео, но короткие целиком в окне — их отдаёт).
	stratProbeCooldownTTL = 90 * time.Second
)

// ytExecTimeout is the yt-dlp extraction timeout. Configurable via [youtube] extract_timeout.
var ytExecTimeout = 60 * time.Second

// ytQualityOrder defines quality labels sorted from best to worst.
var ytQualityOrder = []string{"2160p", "1440p", "1080p", "720p", "480p", "360p", "240p", "144p"}

var (
	ytCheckerOnce sync.Once
	ytCheckerInst *YoutubeChecker
)

// SharedYoutubeChecker returns the process-wide YouTube checker, building it exactly once.
//
// liteSourceHandler is rebuilt repeatedly — at server boot for the /lite routes AND again by
// capiLiteSource on every config hot-reload — and it used to mint a FRESH YoutubeChecker each time.
// That was fatal: the checker owns the live mux state (muxKeys → cacheKey, muxCache → ffmpeg jobs,
// the extraction cache). A new empty checker taking over mid-playback can't resolve the in-flight
// mux key → "mux job not found — video may have expired" + duplicate muxing + redundant /tmp sweeps.
// One shared instance keeps that state stable across rebuilds.
func SharedYoutubeChecker(cfg config.Config) *YoutubeChecker {
	ytCheckerOnce.Do(func() {
		ytCheckerInst = NewYoutubeChecker(cfg)
	})
	return ytCheckerInst
}

func NewYoutubeChecker(cfg config.Config) *YoutubeChecker {
	// Apply configurable extract timeout.
	if cfg.YouTube.ExtractTimeout > 0 {
		ytExecTimeout = time.Duration(cfg.YouTube.ExtractTimeout) * time.Second
	}
	y := &YoutubeChecker{
		repoRoot:      cfg.Compat.RepoRoot,
		cache:         make(map[string]*ytFormatCache),
		stratCooldown: make(map[string]time.Time),
		muxCache:      make(map[string]*ytMuxEntry),
		muxKeys:       make(map[string]string),
		muxVideoID:    make(map[string]string),
		muxVCodecs:    make(map[string]string),
		muxProxy:      make(map[string]bool),
		muxTransA:     make(map[string]bool),
		muxFMP4:       make(map[string]bool),
		masterCache:   make(map[string]ytMasterEntry),
		// Cap global yt-dlp concurrency. 3 is enough to make use of any
		// per-VID parallelism while keeping the request flow well below
		// YouTube's per-IP soft limit (which kicks in around ~10/sec).
		ytdlpSem: make(chan struct{}, 3),
	}
	y.ytdlpPath = y.findYtdlp(cfg.Compat.RepoRoot)
	if y.ytdlpPath != "" {
		log.Info().Str("path", y.ytdlpPath).Msg("youtube: yt-dlp found")
		if ver := getYtdlpVersion(y.ytdlpPath); ver != "" {
			log.Info().Str("version", ver).Msg("youtube: yt-dlp version")
		} else {
			log.Warn().Str("path", y.ytdlpPath).Msg("youtube: yt-dlp is not executable")
			y.ytdlpPath = ""
		}
	} else {
		log.Warn().Msg("youtube: yt-dlp not found, will auto-download on first youtube request")
	}

	// Detect JS runtime for yt-dlp (needed for YouTube signature decryption).
	// yt-dlp 2025+ requires a JS runtime; deno is default but node also works.
	if _, err := exec.LookPath("deno"); err == nil {
		y.jsRuntime = "deno"
	} else if _, err := exec.LookPath("node"); err == nil {
		y.jsRuntime = "node"
	}
	if y.jsRuntime != "" {
		log.Info().Str("runtime", y.jsRuntime).Msg("youtube: JS runtime found")
	} else {
		log.Warn().Msg("youtube: no JS runtime (deno/node) found, yt-dlp may fail")
	}

	// Look for cookies.txt for YouTube authentication.
	// Without cookies, YouTube may block requests with "Sign in to confirm you're not a bot".
	// Search in multiple locations: repo root, next to binary, bin/ dir.
	binDir := ""
	if y.ytdlpPath != "" {
		binDir = filepath.Dir(y.ytdlpPath)
	}
	selfBinDir := ""
	if exe, err := os.Executable(); err == nil {
		selfBinDir = filepath.Dir(exe)
	}
	var cookieCandidates []string
	for _, dir := range []string{cfg.Compat.RepoRoot, selfBinDir, binDir, filepath.Join(cfg.Compat.RepoRoot, "bin")} {
		if dir == "" {
			continue
		}
		cookieCandidates = append(cookieCandidates,
			filepath.Join(dir, "cookies.txt"),
			filepath.Join(dir, "youtube_cookies.txt"),
		)
	}
	for _, cp := range cookieCandidates {
		if info, err := os.Stat(cp); err == nil && !info.IsDir() && info.Size() > 0 {
			y.cookiePath = cp
			y.cookieWorkPath = cp + ".work"
			log.Info().Str("path", cp).Msg("youtube: cookies file found")
			break
		}
	}
	if y.cookiePath == "" {
		log.Warn().Msg("youtube: no cookies.txt found, YouTube may block requests")
	}

	// Detect pip-installed yt-dlp plugins (e.g. bgutil-ytdlp-pot-provider).
	// Standalone yt-dlp binary doesn't see pip packages — we need --plugin-dirs.
	y.pluginDirs = detectYtdlpPluginDirs()
	if y.pluginDirs != "" {
		log.Info().Str("dirs", y.pluginDirs).Msg("youtube: yt-dlp plugin dirs detected")
	}

	// Proxy selection for yt-dlp/ffmpeg:
	// [youtube] proxy = "auto" (default) | "none" | "antidpi" | "vless" | "<host:port>"
	// Явный адрес ("127.0.0.1:40002" / "socks5://…") нужен, когда пригодный для
	// YouTube аплинк — НЕ первый в [proxy.vless]: режим "vless" прибит к сайдкару
	// первого entry (:40000), а на нём может висеть RU-выход, с которого YouTube
	// как раз задушен (инцидент 2026-08-14: жив только не-RU exit на :40002).
	// Извлечение и скачивание идут через ОДИН адрес — подпись URL по egress-IP
	// остаётся консистентной.
	ytProxy := strings.ToLower(strings.TrimSpace(cfg.YouTube.Proxy))
	if ytProxy == "" {
		ytProxy = "auto"
	}
	// ★2026-08-14: googlevideo отдаёт POT-less URL только первые ~20 МиБ файла (дальше 403
	// на любой Range) — жёсткий fetch_pot=never, спасавший 04.08, теперь сам ломает длинные
	// видео. Дефолт «auto»: bgutil-плагин решает сам; проба ловит отравленные варианты.
	y.fetchPotNever = strings.EqualFold(strings.TrimSpace(cfg.YouTube.FetchPot), "never")
	if y.fetchPotNever {
		log.Info().Msg("youtube: PO Token disabled by config ([youtube] fetch_pot = \"never\")")
	}
	// Явный адрес или СПИСОК кандидатов через запятую ("a:1,b:2,c:3") — авто-фейловер.
	var cands []string
	for _, part := range strings.Split(ytProxy, ",") {
		p := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "socks5://"))
		if strings.Contains(p, ":") {
			cands = append(cands, p)
		}
	}
	if len(cands) > 0 {
		y.ytProxyCandidates = cands
		pick := y.pickLiveProxy()
		if pick == "" {
			pick = cands[0] // никто не пингуется — берём первый, health-горутина переберёт
		}
		y.proxyAddr = pick
		y.vlessProxyAddr = pick
		y.setActiveProxy(pick)
		if len(cands) > 1 {
			go y.proxyHealthLoop()
		}
		log.Info().Str("proxy", pick).Int("candidates", len(cands)).Msg("youtube: using SOCKS5 proxy from config (failover)")
	} else if ytProxy != "none" {
		// 1. AntiDPI SOCKS5 proxy (DPI bypass for YouTube in Russia — no external VPN needed)
		if (ytProxy == "auto" || ytProxy == "antidpi") && cfg.AntiDPI.Enable {
			addr := cfg.AntiDPI.Listen
			if addr == "" {
				addr = "127.0.0.1:9898"
			}
			y.proxyAddr = addr
			log.Info().Str("proxy", y.proxyAddr).Msg("youtube: using AntiDPI SOCKS5 proxy")
		} else if (ytProxy == "auto" || ytProxy == "vless") && len(cfg.Proxy.Vless.Entries) > 0 {
			// 2. VLESS SOCKS5 proxy (external VPN for bot detection bypass)
			y.proxyAddr = "127.0.0.1:40000"
			log.Info().Str("proxy", y.proxyAddr).Msg("youtube: using VLESS SOCKS5 proxy")
		}
		// VLESS as fallback for CDN stream downloads. When DPI blocks direct+AntiDPI
		// connections to CDN edge servers (googlevideo.com), VLESS encrypted tunnel
		// bypasses it completely.
		if ytProxy != "antidpi" && len(cfg.Proxy.Vless.Entries) > 0 {
			y.vlessProxyAddr = "127.0.0.1:40000"
			if cfg.AntiDPI.Enable {
				log.Info().Str("vless", y.vlessProxyAddr).Msg("youtube: VLESS available as CDN stream fallback")
			}
		}
	} else {
		log.Info().Msg("youtube: proxy disabled by config ([youtube] proxy = \"none\")")
	}

	// Thumbnails (i.ytimg.com) are DPI-blocked on RU hosts exactly like the rest of
	// YouTube, so YtImgProxyHandler must reuse the same SOCKS5 bypass — a direct fetch
	// returns 502 and every client shows blank previews.
	setYtImgProxyAddr(y.proxyAddr)

	// Find ffmpeg for server-side muxing (video-only + audio → mp4).
	// Prefer local binary (may be a newer static build with SOCKS5 support).
	localFF := filepath.Join(cfg.Compat.RepoRoot, "bin", "ffmpeg")
	if info, err := os.Stat(localFF); err == nil && !info.IsDir() {
		y.ffmpegPath = localFF
		log.Info().Str("path", localFF).Msg("youtube: ffmpeg found (local)")
	} else if p, err := exec.LookPath("ffmpeg"); err == nil {
		y.ffmpegPath = p
		log.Info().Str("path", p).Msg("youtube: ffmpeg found")
	} else {
		log.Warn().Msg("youtube: ffmpeg not found, video-only formats will have no audio")
	}

	// Remove ALL leftover /tmp/yt-mux-* dirs immediately. A freshly started
	// process has an empty muxCache, so every such dir is an orphan from a
	// previous (now-dead) instance — these hold full remuxed videos (multi-GB
	// for 4K), so after a restart they're pure /tmp leak. The periodic sweep
	// only removes orphans older than ytMuxTTL (30m); do it now without waiting.
	y.sweepStartupMuxDirs()

	// Start background cleanup of expired mux entries and stale /tmp/yt-mux-* dirs.
	go y.muxCleanupLoop()

	// One line per process if the singleton holds; more than one ⇒ multiple checkers (stale binary).
	log.Info().Str("ptr", fmt.Sprintf("%p", y)).Msg("youtube: checker instance created")

	return y
}

// muxStartupSweepMinAge — at startup we only delete yt-mux dirs whose mtime is older than this.
// The guard exists for SECOND lampac processes sharing /tmp (overlapping restart, or a separate
// instance like the lampac-tmdb proxy unit): their startup sweep must never nuke a live dir of
// the main instance. 90s was tuned for an actively-WRITING mux (ffmpeg bumps mtime every few
// seconds) — but a COMPLETED mux being watched stops writing, its mtime freezes, and 90s later a
// sibling-instance restart deleted it under the playing client (prod 2026-07-02: segsOnDisk=-1
// ~100s after «HLS mux done» → 404 storm; the removal was logged in the SIBLING unit's journal).
// Leased dirs now get their mtime bumped every ≤2 min (cleanup loop + on-access), so anything
// older than 15 min is a true orphan; fresher leftovers are still collected later by the 30-min
// orphan sweep in muxCleanupOnce.
const muxStartupSweepMinAge = 15 * time.Minute

// removeMuxDir is the single choke point for deleting a yt-mux temp dir. It logs which sweep removed
// it and how old the dir's mtime was, so `journalctl -u lampac | grep "yt-mux removed"` pinpoints
// exactly what deleted a still-playing dir if the mux-failed 500 ever recurs (a small age = a live
// dir was killed). dir == "" is a no-op.
// muxDirSize sums the sizes of the regular files in a mux dir (HLS segments +
// playlist + init). Segments are flat files, so ReadDir (no recursion) suffices.
// Best-effort: any error yields 0 so the size guard never aborts a healthy mux.
func muxDirSize(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}

func removeMuxDir(dir, by string) {
	if dir == "" {
		return
	}
	age := time.Duration(-1)
	if info, err := os.Stat(dir); err == nil {
		age = time.Since(info.ModTime())
	}
	_ = os.RemoveAll(dir)
	log.Warn().Str("dir", dir).Str("by", by).Dur("age", age).Msg("youtube: yt-mux removed")
}

// muxStartupSweepOnce guarantees the startup sweep runs only ONCE per process. NewYoutubeChecker is
// built more than once in a process: at server boot (the /lite routes) AND lazily/again by
// capiLiteSource, which rebuilds the whole lite handler on every config hot-reload. Without this
// guard, that rebuild re-ran sweepStartupMuxDirs and deleted the OTHER checker's live /tmp/yt-mux-*
// dir mid-mux → ffmpeg "Failed to open seg0000N.ts: No such file or directory" → 500 + restart loop.
var muxStartupSweepOnce sync.Once

// sweepStartupMuxDirs deletes leftover /tmp/yt-mux-* directories at process start. Runs once per
// process (muxStartupSweepOnce) and only on dirs idle longer than muxStartupSweepMinAge — so neither
// a checker rebuild nor a concurrent instance can nuke a live mux out from under a running ffmpeg.
func (y *YoutubeChecker) sweepStartupMuxDirs() {
	muxStartupSweepOnce.Do(y.doSweepStartupMuxDirs)
}

func (y *YoutubeChecker) doSweepStartupMuxDirs() {
	entries, err := filepath.Glob(filepath.Join(os.TempDir(), "yt-mux-*"))
	if err != nil || len(entries) == 0 {
		return
	}
	var removed, skipped int
	for _, dir := range entries {
		info, err := os.Stat(dir)
		if err == nil && time.Since(info.ModTime()) < muxStartupSweepMinAge {
			skipped++ // recently active — likely a live mux of another process; leave it
			continue
		}
		removeMuxDir(dir, "startup-sweep")
		removed++
	}
	if removed > 0 || skipped > 0 {
		log.Info().Int("removed", removed).Int("skipped_active", skipped).Msg("youtube: startup mux-dir sweep")
	}
}

func (y *YoutubeChecker) findOrDownloadYtdlp(repoRoot string) string {
	if found := y.findYtdlp(repoRoot); found != "" {
		return found
	}

	binDir := filepath.Join(repoRoot, "bin")
	localPath := filepath.Join(binDir, "yt-dlp")

	// Use the stable channel. We were pinned to nightly because stable (2026.03.x)
	// lagged YouTube's client/SABR rollouts and returned "page needs to be reloaded"
	// on mweb/tv/web. A current stable now tracks those changes and extracts up to
	// 4K HDR AV1, and stable avoids the regression risk of an arbitrary nightly that
	// "latest" would otherwise pull. The admin "update deps" button uses stable too
	// (see admin_panel_deps.go), so both code paths now agree.
	var dlURL string
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		dlURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux"
	case runtime.GOOS == "darwin":
		dlURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_macos"
	default:
		log.Warn().Str("os", runtime.GOOS).Str("arch", runtime.GOARCH).Msg("youtube: auto-download not supported for this platform")
		return ""
	}

	_ = os.MkdirAll(binDir, 0o755)
	log.Info().Str("url", dlURL).Msg("youtube: downloading yt-dlp")

	client := httpclient.New(60 * time.Second)
	resp, err := client.Get(dlURL)
	if err != nil {
		log.Warn().Err(err).Msg("youtube: failed to download yt-dlp")
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Warn().Int("status", resp.StatusCode).Msg("youtube: yt-dlp download bad status")
		return ""
	}

	f, err := os.Create(localPath)
	if err != nil {
		log.Warn().Err(err).Msg("youtube: failed to create yt-dlp file")
		return ""
	}
	_, copyErr := io.Copy(f, io.LimitReader(resp.Body, 100<<20)) // max 100MB
	f.Close()
	if copyErr != nil {
		os.Remove(localPath)
		log.Warn().Err(copyErr).Msg("youtube: failed to write yt-dlp")
		return ""
	}

	if err := os.Chmod(localPath, 0o755); err != nil {
		os.Remove(localPath)
		log.Warn().Err(err).Msg("youtube: failed to chmod yt-dlp")
		return ""
	}

	// Verify it works
	cmd := exec.Command(localPath, "--version")
	out, err := cmd.Output()
	if err != nil {
		os.Remove(localPath)
		log.Warn().Err(err).Msg("youtube: downloaded yt-dlp does not work")
		return ""
	}
	log.Info().Str("version", strings.TrimSpace(string(out))).Msg("youtube: yt-dlp downloaded")
	return localPath
}

func (y *YoutubeChecker) findYtdlp(repoRoot string) string {
	// 1. Check PATH
	if p, err := exec.LookPath("yt-dlp"); err == nil {
		if ver := getYtdlpVersion(p); ver != "" {
			return p
		}
		log.Warn().Str("path", p).Msg("youtube: yt-dlp in PATH is not executable")
	}

	// 2. Check {repoRoot}/bin/yt-dlp
	binDir := filepath.Join(repoRoot, "bin")
	localPath := filepath.Join(binDir, "yt-dlp")
	if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
		if ver := getYtdlpVersion(localPath); ver != "" {
			return localPath
		}
		log.Warn().Str("path", localPath).Msg("youtube: local yt-dlp is not executable")
	}
	return ""
}

func (y *YoutubeChecker) ensureYtdlp() string {
	y.ytdlpMu.Lock()
	defer y.ytdlpMu.Unlock()

	if y.ytdlpPath != "" {
		return y.ytdlpPath
	}
	y.ytdlpPath = y.findOrDownloadYtdlp(y.repoRoot)
	return y.ytdlpPath
}

// baseArgs returns common yt-dlp CLI flags (JS runtime and cookies).
// detectYtdlpPluginDirs finds pip-installed yt-dlp plugins and returns
// their parent directories (comma-separated) for --plugin-dirs.
// This is needed when yt-dlp is a standalone binary (not pip-installed)
// but plugins like bgutil-ytdlp-pot-provider are installed via pip.
func detectYtdlpPluginDirs() string {
	// Try to find yt_dlp_plugins directory from pip packages.
	// pip show gives us the Location: which is the site-packages dir.
	// Check both yt-dlp-ejs (nsig challenge solver) and bgutil-ytdlp-pot-provider (PO tokens).
	pipPackages := []string{"yt-dlp-ejs", "bgutil-ytdlp-pot-provider"}
	for _, pip := range []string{"pip", "pip3"} {
		for _, pkg := range pipPackages {
			out, err := exec.Command(pip, "show", pkg).Output()
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "Location:") {
					loc := strings.TrimSpace(strings.TrimPrefix(line, "Location:"))
					if loc == "" {
						continue
					}
					// Verify the yt_dlp_plugins directory exists there.
					pluginDir := filepath.Join(loc, "yt_dlp_plugins")
					if info, err := os.Stat(pluginDir); err == nil && info.IsDir() {
						return loc
					}
					return loc
				}
			}
		}
	}

	// venv-установки: системный pip про них не знает («pip show» выше молчит), а на
	// проде yt-dlp-ejs/bgutil живут именно в /opt/ytdlp-env. Без --plugin-dirs
	// standalone-бинарь плагинов не видит → POT не выдаётся даже в auto-режиме
	// (2026-08-14: из-за этого «auto» вёл себя как never и длинные видео резались
	// тизерным окном CDN).
	for _, venv := range []string{"/opt/ytdlp-env"} {
		if matches, _ := filepath.Glob(filepath.Join(venv, "lib", "python3*", "site-packages")); len(matches) > 0 {
			for _, sp := range matches {
				if info, err := os.Stat(filepath.Join(sp, "yt_dlp_plugins")); err == nil && info.IsDir() {
					return sp
				}
			}
		}
	}

	// Fallback: check common site-packages locations.
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/usr/lib/python3/dist-packages",
		"/usr/local/lib/python3/dist-packages",
	}
	if home != "" {
		candidates = append(candidates, filepath.Join(home, ".local", "lib"))
	}
	for _, base := range candidates {
		// Walk one level to find pythonX.Y directories.
		entries, err := os.ReadDir(base)
		if err != nil {
			// Try direct path.
			pluginDir := filepath.Join(base, "yt_dlp_plugins")
			if info, statErr := os.Stat(pluginDir); statErr == nil && info.IsDir() {
				return base
			}
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "python") {
				continue
			}
			sp := filepath.Join(base, e.Name(), "site-packages")
			pluginDir := filepath.Join(sp, "yt_dlp_plugins")
			if info, err := os.Stat(pluginDir); err == nil && info.IsDir() {
				return sp
			}
		}
	}

	return ""
}

func (y *YoutubeChecker) baseArgs() []string {
	var args []string
	// История POT — два разворота на 180°:
	// 2026-08-03: --plugin-dirs НАМЕРЕННО выключали — bgutil-POT (session-bound) травил
	//   mweb/tv URL, CDN 403-ил их у нашего внепроцессного загрузчика.
	// ★2026-08-14: Google перевернул стол — БЕЗ POT CDN отдаёт лишь первые ~20-25 МиБ
	//   файла (Range за офсетом → 403 на всех маршрутах; замерено на проде), длинные
	//   видео умирали на середине при зелёной 1-КБ пробе. С плагином URL качается целиком
	//   (проверено ranged-тестами за окном). Отравленные комбинации отсекает двухфазная
	//   probeStreamURL. Выключатель на случай следующего разворота: [youtube] fetch_pot="never".
	if y.pluginDirs != "" && !y.fetchPotNever {
		args = append(args, "--plugin-dirs", y.pluginDirs)
	}
	if y.jsRuntime != "" {
		args = append(args, "--js-runtimes", y.jsRuntime)
	}
	if w := y.ensureCookieWork(); w != "" {
		args = append(args, "--cookies", w)
	}
	return args
}

// cookieArgs returns yt-dlp CLI flags for search requests (uses TV client + proxy fallback).
func (y *YoutubeChecker) cookieArgs() []string {
	args := y.baseArgs()
	// TV client gives the best format variety and doesn't need cookies.
	args = append(args, "--extractor-args", "youtube:player_client=tv")
	return args
}

func (y *YoutubeChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if y.ensureYtdlp() == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "yt-dlp not available",
			})
			return
		}

		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := y.checkSearch(req)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if show {
				_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		videoID := strings.TrimSpace(req.URL.Query().Get("videoID"))
		if videoID != "" {
			y.stream(w, req, videoID, links)
			return
		}

		y.index(w, req, links)
	}
}

func (y *YoutubeChecker) checkSearch(req *http.Request) bool {
	// YouTube always has results for any title — skip the slow yt-dlp probe
	// and just return true if there's a non-empty title.
	title := strings.TrimSpace(req.URL.Query().Get("title"))
	if title == "" {
		title = strings.TrimSpace(req.URL.Query().Get("original_title"))
	}
	return title != ""
}

func (y *YoutubeChecker) ytSearch(title string) ([]ytEntry, error) {
	return y.ytSearchN(title, ytSearchLimit)
}

func (y *YoutubeChecker) ytSearchN(title string, limit int) ([]ytEntry, error) {
	ytdlpPath := y.ensureYtdlp()
	if ytdlpPath == "" {
		return nil, fmt.Errorf("yt-dlp not available")
	}

	searchQuery := title
	args := []string{
		"--flat-playlist",
		"--dump-single-json",
		"--no-warnings",
		"--socket-timeout", "10",
		"--no-check-certificates",
		// Force IPv4: googlevideo stream URLs returned by yt-dlp are signed with
		// the egress IP (&ip=...&sparams=...,ip,...&sig=...). Our antidpi direct
		// dialers (split-tlsrec, split-sni, no-sni, split-1) and the AntiDPI
		// SOCKS5 server are all hard-coded to "tcp4". On a dual-stack host where
		// Python prefers IPv6, yt-dlp would sign the URL for v6 while the mux/
		// pipe-download paths egress on v4 — CDN returns 403. Pinning yt-dlp to
		// IPv4 keeps extraction-IP and download-IP in the same family.
		"-4",
	}
	args = append(args, y.cookieArgs()...)
	args = append(args, fmt.Sprintf("ytsearch%d:%s", limit, searchQuery))
	cmd := exec.Command(ytdlpPath, args...)
	cmd.Env = os.Environ()

	y.ytdlpSem <- struct{}{}
	out, err := runWithTimeout(cmd, ytExecTimeout)
	<-y.ytdlpSem
	if err != nil {
		return nil, err
	}

	var result ytSearchResult
	if err := stdjson.Unmarshal(out, &result); err != nil {
		return nil, err
	}
	return result.Entries, nil
}

// ytPlaylistN runs yt-dlp --flat-playlist against an arbitrary URL (a channel's /videos page or a
// playlist) and returns up to `limit` entries. Same machinery as ytSearchN, just a URL target +
// --playlist-end instead of ytsearch:. Used by the channel page (YouTube Phase 2).
func (y *YoutubeChecker) ytPlaylistN(targetURL string, limit int) ([]ytEntry, error) {
	result, err := y.ytPlaylistFull(targetURL, limit)
	if err != nil {
		return nil, err
	}
	return result.Entries, nil
}

// ytPlaylistFull is ytPlaylistN keeping the top-level playlist fields too (channel meta).
func (y *YoutubeChecker) ytPlaylistFull(targetURL string, limit int) (*ytSearchResult, error) {
	ytdlpPath := y.ensureYtdlp()
	if ytdlpPath == "" {
		return nil, fmt.Errorf("yt-dlp not available")
	}
	args := []string{
		"--flat-playlist",
		"--dump-single-json",
		"--no-warnings",
		"--socket-timeout", "10",
		"--no-check-certificates",
		"-4", // keep extraction-IP family stable (see ytSearchN)
		"--playlist-end", strconv.Itoa(limit),
	}
	args = append(args, y.cookieArgs()...)
	args = append(args, targetURL)
	cmd := exec.Command(ytdlpPath, args...)
	cmd.Env = os.Environ()

	y.ytdlpSem <- struct{}{}
	out, err := runWithTimeout(cmd, ytExecTimeout)
	<-y.ytdlpSem
	if err != nil {
		return nil, err
	}
	var result ytSearchResult
	if err := stdjson.Unmarshal(out, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ytEntriesToCards turns yt-dlp flat-playlist entries into the feed card shape the web client reads
// ({title,img,url,video_id,duration,channel}). Shared by the channel page and the category feed.
func ytEntriesToCards(entries []ytEntry, host string) []map[string]any {
	results := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if entry.ID == "" {
			continue
		}
		durStr := ""
		if entry.Duration > 0 {
			durStr = fmt.Sprintf("%d:%02d", int(entry.Duration)/60, int(entry.Duration)%60)
		}
		channel := entry.Channel
		if channel == "" {
			channel = entry.Uploader
		}
		card := map[string]any{
			"title":    entry.Title,
			"img":      ytImgProxyURL(host, entry.ID),
			"url":      host + "/lite/youtube?videoID=" + entry.ID + "&title=" + url.QueryEscape(entry.Title),
			"video_id": entry.ID,
			"duration": durStr,
			"channel":  channel,
		}
		if entry.ChannelID != "" {
			card["channel_id"] = entry.ChannelID
		}
		results = append(results, card)
	}
	return results
}

// channelVideos lists a channel's recent uploads via yt-dlp (works for any channel id, no OAuth).
// Returns the cards plus the channel header meta ({id,title,thumb}) so the channel page can render
// a title/avatar for channels the user is NOT subscribed to. Cached in ytFeedStore under "ch:<id>".
func (y *YoutubeChecker) channelVideos(channelID, host string) ([]map[string]any, map[string]any) {
	// Host is part of the key: the cached value is RENDERED cards whose img/url fields are absolute
	// and built from this host. Without it, whoever fills the cache first decides the host for
	// everyone — a request that arrives as 127.0.0.1:888 (a local health check or a curl on the
	// box) poisons every real client's thumbnails for the whole TTL.
	cacheKey := "ch:" + host + "|" + channelID
	ytFeedMu.RLock()
	cached, ok := ytFeedStore[cacheKey]
	ytFeedMu.RUnlock()
	if ok && time.Now().Before(cached.Expires) {
		return cached.Data, cached.Meta
	}

	full, err := y.ytPlaylistFull("https://www.youtube.com/channel/"+channelID+"/videos", 30)
	if err != nil {
		log.Warn().Err(err).Str("channel", channelID).Msg("youtube: channel listing failed")
		return nil, nil
	}
	results := ytEntriesToCards(full.Entries, host)

	title := full.Channel
	if title == "" {
		title = full.Uploader
	}
	if title == "" {
		// yt-dlp playlist title for /videos is "<Channel> - Videos"
		title = strings.TrimSuffix(full.Title, " - Videos")
	}
	thumb := ""
	for _, t := range full.Thumbnails {
		if t.URL == "" {
			continue
		}
		thumb = t.URL
		if t.ID == "avatar_uncropped" { // prefer the square avatar over the banner
			break
		}
	}
	meta := map[string]any{"id": channelID, "title": title}
	if thumb != "" {
		meta["thumb"] = ytImgProxyRawURL(host, thumb)
	}

	ytFeedMu.Lock()
	ytFeedStore[cacheKey] = &ytFeedCacheEntry{Data: results, Meta: meta, Expires: time.Now().Add(ytFeedCacheTTL)}
	ytFeedMu.Unlock()
	return results, meta
}

// handleChannels returns the user's subscribed channels for the sidebar «Каналы» list.
// GET /lite/youtube/channels?token=xxx
func (y *YoutubeChecker) HandleChannels(w http.ResponseWriter, r *http.Request) {
	api := y.ytAPI()
	if api == nil {
		writeJSON(w, http.StatusOK, map[string]any{"channels": []any{}})
		return
	}
	tgID := y.tgIDFromRequest(r)
	if tgID == 0 || !api.IsLinked(tgID) {
		writeJSON(w, http.StatusOK, map[string]any{"channels": []any{}})
		return
	}
	chans, err := api.GetSubscriptions(r.Context(), tgID)
	if err != nil {
		log.Warn().Err(err).Int64("tg_id", tgID).Msg("youtube: subscriptions channels error")
		writeJSON(w, http.StatusOK, map[string]any{"channels": []any{}})
		return
	}
	host := hostFromRequest(r)
	out := make([]map[string]any, 0, len(chans))
	for _, c := range chans {
		out = append(out, map[string]any{"id": c.ChannelID, "title": c.Title, "thumb": ytImgProxyRawURL(host, c.Thumbnail)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

// recommendVideos lists YouTube trending — the closest no-cookie equivalent of the youtube.com home
// recommendations (personalized recs need the browser session, not the OAuth Data API). yt-dlp
// resolves /feed/trending; region follows the server's egress IP. Cached in ytFeedStore.
func (y *YoutubeChecker) recommendVideos(host string) []map[string]any {
	cacheKey := "rec:trending|" + host // host in the key — see channelVideos
	ytFeedMu.RLock()
	cached, ok := ytFeedStore[cacheKey]
	ytFeedMu.RUnlock()
	if ok && time.Now().Before(cached.Expires) {
		return cached.Data
	}

	entries, err := y.ytPlaylistN("https://www.youtube.com/feed/trending", 40)
	if err != nil || len(entries) == 0 {
		if err != nil {
			log.Warn().Err(err).Msg("youtube: trending listing failed")
		}
		return nil
	}
	results := ytEntriesToCards(entries, host)

	ytFeedMu.Lock()
	ytFeedStore[cacheKey] = &ytFeedCacheEntry{Data: results, Expires: time.Now().Add(ytFeedCacheTTL)}
	ytFeedMu.Unlock()
	return results
}

// handleRecommend returns the recommendations feed (trending) for the YouTube tab's «Главная».
// GET /lite/youtube/recommend
func (y *YoutubeChecker) HandleRecommend(w http.ResponseWriter, r *http.Request) {
	results := y.recommendVideos(hostFromRequest(r))
	if mode := y.shortsModeForRequest(r); mode != "" && mode != "all" {
		y.annotateShorts(r.Context(), results)
		results = filterShorts(results, mode)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// handleChannel returns a channel's recent videos for the channel page (grid).
// GET /lite/youtube/channel?id=UCxxxx
func (y *YoutubeChecker) HandleChannel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id required"})
		return
	}
	results, meta := y.channelVideos(id, hostFromRequest(r))
	resp := map[string]any{"results": results}
	if meta != nil {
		resp["channel"] = meta
	}
	writeJSON(w, http.StatusOK, resp)
}

func (y *YoutubeChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))

	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	if title == "" {
		title = originalTitle
	}
	if title == "" {
		if similar {
			writeJSON(w, http.StatusOK, map[string]any{"type": "similar", "data": []any{}})
			return
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	entries, err := y.ytSearch(title)
	if err != nil {
		log.Warn().Err(err).Str("title", title).Msg("youtube: search failed")
		if similar {
			writeJSON(w, http.StatusOK, map[string]any{"type": "similar", "data": []any{}})
			return
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	if len(entries) == 0 {
		if similar {
			writeJSON(w, http.StatusOK, map[string]any{"type": "similar", "data": []any{}})
			return
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)

	// Spider-compatible "similar" response: cards for the search catalog
	if similar {
		cards := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			if entry.ID == "" {
				continue
			}
			cardTitle := entry.Title
			channel := entry.Channel
			if channel == "" {
				channel = entry.Uploader
			}
			if channel != "" {
				cardTitle += " — " + channel
			}
			cards = append(cards, map[string]any{
				"title":          cardTitle,
				"original_title": entry.Title,
				"img":            ytImgProxyURL(host, entry.ID),
				"youtube":        entry.ID,
				"balanser":       "youtube",
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "similar",
			"data": cards,
		})
		return
	}
	baseTitle := title
	if originalTitle != "" && originalTitle != title {
		baseTitle = title + " / " + originalTitle
	}

	data := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if entry.ID == "" {
			continue
		}

		// Duration formatting
		durStr := ""
		if entry.Duration > 0 {
			m := int(entry.Duration) / 60
			s := int(entry.Duration) % 60
			durStr = fmt.Sprintf("%d:%02d", m, s)
		}

		// Label
		name := entry.Title
		if durStr != "" {
			name += " [" + durStr + "]"
		}
		channel := entry.Channel
		if channel == "" {
			channel = entry.Uploader
		}
		if channel != "" {
			name += " — " + channel
		}

		// The URL that Lampa will call when user selects this video.
		// method:"call" means Lampa will fetch this URL first, then play the result.
		callURL := host + "/lite/youtube?videoID=" + entry.ID + "&title=" + url.QueryEscape(title)

		row := map[string]any{
			"method": "call",
			"url":    callURL,
			"stream": callURL,
			"name":   name,
			"title":  baseTitle,
			"img":    ytImgProxyURL(host, entry.ID),
		}
		data = append(data, row)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": data,
		})
		return
	}

	// HTML fallback
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		label := fmt.Sprint(row["name"])
		getsTVAppendMovieHTML(&sb, row, label, i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (y *YoutubeChecker) stream(w http.ResponseWriter, req *http.Request, videoID string, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		title = videoID
	}

	formats, duration, usedProxy, ok := y.getFormats(videoID)
	if !ok || len(formats) == 0 {
		log.Warn().Str("videoID", videoID).Bool("ok", ok).Msg("youtube: stream — no formats extracted (PO Token?)")
		errMsg := "yt-dlp не смог извлечь форматы видео. Проверьте PO Token (pip install bgutil-ytdlp-pot-provider)"
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"data": []any{}, "error": errMsg})
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"error": errMsg})
		}
		return
	}

	// Refuse to mux very long videos to prevent resource exhaustion.
	if duration > float64(ytMaxDuration) {
		log.Warn().Float64("duration", duration).Str("videoID", videoID).Msg("youtube: video too long, refusing mux")
		errMsg := fmt.Sprintf("Видео слишком длинное (%.0f мин)", duration/60)
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"data": []any{}, "error": errMsg})
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"error": errMsg})
		}
		return
	}

	ytHeaders := map[string]string{
		"Origin":     "https://www.youtube.com",
		"Referer":    "https://www.youtube.com/",
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	}

	// Build quality map: label → proxy URL.
	// Prefer combined (video+audio) formats, fallback to video-only direct URLs.
	qr := y.buildQualityMap(req, formats, links, ytHeaders, usedProxy)
	if len(qr.Qualities) == 0 {
		// Dump format details for debugging.
		for i, f := range formats {
			log.Debug().
				Int("i", i).
				Str("format_id", f.FormatID).Str("ext", f.Ext).
				Str("protocol", f.Protocol).Int("height", f.Height).Int("width", f.Width).
				Str("vcodec", f.VCodec).Str("acodec", f.ACodec).
				Float64("tbr", f.TBR).Bool("hasURL", f.URL != "").
				Str("videoID", videoID).
				Msg("youtube: rejected format detail")
		}
		log.Warn().Str("videoID", videoID).Int("formats", len(formats)).Msg("youtube: stream — no usable qualities from formats")
		errMsg := "Нет подходящих качеств для воспроизведения"
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"data": []any{}, "error": errMsg})
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"error": errMsg})
		}
		return
	}

	// Pick the single default-played quality (capped at 1080p). We deliberately hand the client ONE
	// media playlist, NOT a multi-bitrate master: a master makes the player fan out a separate
	// real-time ffmpeg mux per quality (6+ parallel googlevideo downloads → wasted bandwidth, 403
	// rate-limits, /tmp churn). The full `qualitys` map is still returned for the manual picker, which
	// starts a mux on demand — one at a time.
	bestLabel := ytBestLabelCapped(qr.Qualities, ytDefaultMuxHeight)

	// Pre-start mux ONLY for the default quality in background.
	// Other qualities start on demand when the user picks them.
	if pair, ok := qr.MuxPairs[bestLabel]; ok {
		cacheKey := pair.VideoURL + "\x00" + pair.AudioURL
		y.getOrStartMux(cacheKey, pair.VideoURL, pair.AudioURL, pair.VCodec, strings.TrimSpace(req.URL.Query().Get("videoID")), usedProxy, pair.TransAudio, pair.FMP4)
	}
	bestURL := qr.Qualities[bestLabel]

	qualitysOut := make(map[string]any, len(qr.Qualities))
	for label, u := range qr.Qualities {
		qualitysOut[label] = u
	}

	host := hostFromRequest(req)

	// DASH alternative for MSE players (web Shaka / webOS / Tizen): native YouTube video+audio served
	// via /proxy (no real-time ffmpeg mux) → SEEKABLE + no "outran the muxer" freeze. Best quality for
	// now (multi-rep picker = TODO). iOS Safari (no MSE) and the native ExoPlayer (no media3-dash) keep
	// the HLS mux. ⚠️ NOT yet verified against live YouTube — client falls back to `stream` on error.
	dashURL := y.buildBestDashURL(host, qr, links, clientIP(req))

	// rjson mode: Lampa Search onSelect expects {data: [{stream: "url"}]}
	if rjson {
		row := map[string]any{
			"method":               "play",
			"stream":               bestURL,
			"url":                  bestURL,
			"quality":              qualitysOut,
			"qualitys":             qualitysOut,
			"title":                title,
			"name":                 title,
			"hls_manifest_timeout": 180000,
			// Real length in seconds. While ffmpeg still muxes, the HLS playlist is EVENT-typed
			// and players report duration=Infinity — the client uses this to show a real seekbar.
			"duration": duration,
		}
		if qr.AudioURL != "" {
			row["audio"] = qr.AudioURL
		}
		if dashURL != "" {
			row["dash"] = dashURL
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": []any{row},
		})
		return
	}

	resp := map[string]any{
		"method":   "play",
		"url":      bestURL,
		"quality":  qualitysOut,
		"qualitys": qualitysOut,
		"title":    title,
		"name":     title,
		// Give hls.js extra time: ffmpeg mux needs time to download and remux.
		// Lampa reads this in: new Hls({ manifestLoadTimeout: Player.playdata().hls_manifest_timeout })
		"hls_manifest_timeout": 180000, // 3 minutes
		// Real length in seconds (EVENT playlist → players say Infinity; see rjson row above).
		"duration": duration,
	}

	// Provide separate audio track URL. Lampa/ExoPlayer can use this to
	// play audio alongside video-only streams.
	if qr.AudioURL != "" {
		resp["audio"] = qr.AudioURL
	}
	if dashURL != "" {
		resp["dash"] = dashURL
	}

	writeJSON(w, http.StatusOK, resp)
}

// ytQualityResult holds the quality map and optional audio track URL.
type ytQualityResult struct {
	Qualities map[string]string // label → proxy URL
	AudioURL  string            // best audio track proxy URL (for video-only entries)
	// Raw direct URLs for ffmpeg mux (bypass proxy for speed).
	MuxPairs map[string]ytMuxPair // label → raw video+audio URLs
}

// ytMuxPair holds raw YouTube URLs for server-side mux (no proxy).
type ytMuxPair struct {
	VideoURL   string
	AudioURL   string
	VCodec     string // video codec (avc1, vp09, av01) — determines segment type
	ACodec     string // audio codec (mp4a, opus) — used for HLS CODECS attribute
	TransAudio bool   // transcode audio to AAC for HLS/TS compatibility
	FMP4       bool   // true for VP9/AV1 — use fMP4 segments instead of TS
	Height     int    // for HLS RESOLUTION attribute
	Width      int    // for HLS RESOLUTION attribute
	Bandwidth  int    // video TBR + audio TBR in bits/sec, for HLS BANDWIDTH attribute
	// DASH SegmentBase byte ranges ("start-end") for the video & audio streams — empty on progressive
	// formats. Only pairs with all four set can produce a valid on-demand MPD (see buildBestDashURL).
	VInitRange  string
	VIndexRange string
	AInitRange  string
	AIndexRange string
}

// buildQualityMap creates a quality label → proxy URL map from extracted formats.
// YouTube only provides combined (video+audio) mp4 at low resolutions (≤360p).
// Higher resolutions come as video-only streams. We provide direct proxy URLs
// for both combined and video-only formats. The audio track URL is returned
// separately so the player can use it alongside video-only streams.
func (y *YoutubeChecker) buildQualityMap(req *http.Request, formats []ytFormat, links *proxylink.Manager, headers map[string]string, usedProxy bool) ytQualityResult {
	host := hostFromRequest(req)
	muxVideoID := strings.TrimSpace(req.URL.Query().Get("videoID")) // for POT-flap re-mint

	log.Debug().Int("total_formats", len(formats)).Msg("youtube: buildQualityMap start")

	// When iOS client extraction produces HLS audio formats (233/234), YouTube
	// serves those via manifest.googlevideo.com which bypasses the main-CDN
	// IP ban. DASH formats from the same response go through the banned CDN
	// and return 403 when ffmpeg tries to mux. So if any HLS audio exists,
	// drop every DASH (https protocol) format — HLS-only mode.
	hasHLSAudio := false
	for _, f := range formats {
		if f.isAudioOnly() && f.Protocol == "m3u8_native" && f.URL != "" {
			hasHLSAudio = true
			break
		}
	}
	if hasHLSAudio {
		filtered := formats[:0:0]
		for _, f := range formats {
			if f.Protocol == "m3u8_native" {
				filtered = append(filtered, f)
			}
		}
		log.Debug().Int("before", len(formats)).Int("after", len(filtered)).Msg("youtube: HLS-only mode (iOS client) — filtered DASH formats")
		formats = filtered
	}

	// Collect best video-only per height.
	// Prefer H.264 (avc1) for TS HLS compatibility. Accept VP9/AV1 for heights
	// where avc1 is unavailable (1440p, 2160p) — these use fMP4 HLS segments
	// which hls.js/ExoPlayer support without transcoding.
	videoByHeight := make(map[int]ytFormat)
	for _, f := range formats {
		if !f.isVideoOnly() || f.URL == "" || !ytAcceptProtocol(f.Protocol) {
			continue
		}
		if f.Ext != "mp4" && f.Ext != "webm" {
			continue
		}
		isAVC := strings.HasPrefix(f.VCodec, "avc1")
		isVP9 := strings.HasPrefix(f.VCodec, "vp9") || strings.HasPrefix(f.VCodec, "vp09")
		isAV1 := strings.HasPrefix(f.VCodec, "av01")
		if !isAVC && !isVP9 && !isAV1 {
			continue
		}
		h := f.Height
		if h <= 0 {
			continue
		}
		existing, ok := videoByHeight[h]
		if !ok {
			videoByHeight[h] = f
			continue
		}
		existIsAVC := strings.HasPrefix(existing.VCodec, "avc1")
		// Always prefer avc1 over VP9/AV1 at same height (TS HLS compat).
		if isAVC && !existIsAVC {
			videoByHeight[h] = f
			continue
		}
		if !isAVC && existIsAVC {
			continue // keep existing avc1
		}
		// Same codec family — prefer higher bitrate.
		if f.TBR > existing.TBR {
			videoByHeight[h] = f
		}
	}

	log.Debug().Int("video_only_heights", len(videoByHeight)).Str("ffmpeg", y.ffmpegPath).Msg("youtube: buildQualityMap video-only collected")

	// Find best audio track. Prefer m4a (AAC), fallback to webm (opus).
	bestAudio := pickBestAudio(formats)

	// Collect combined formats.
	combinedByHeight := make(map[int]ytFormat)
	for _, f := range formats {
		if !f.isCombined() || f.URL == "" || !ytAcceptProtocol(f.Protocol) {
			continue
		}
		if f.Ext != "mp4" && f.Ext != "webm" {
			continue
		}
		h := f.Height
		if h <= 0 {
			continue
		}
		if existing, ok := combinedByHeight[h]; !ok || f.TBR > existing.TBR {
			combinedByHeight[h] = f
		}
	}

	log.Debug().Int("combined_heights", len(combinedByHeight)).Bool("has_audio", bestAudio.URL != "").Str("audio_ext", bestAudio.Ext).Msg("youtube: buildQualityMap combined collected")

	result := make(map[string]string)
	muxPairs := make(map[string]ytMuxPair)

	// For each available height, add a quality entry.
	// Merge both video-only and combined formats.
	allHeights := make(map[int]bool)
	for h := range videoByHeight {
		allHeights[h] = true
	}
	for h := range combinedByHeight {
		allHeights[h] = true
	}

	for h := range allHeights {
		label := strconv.Itoa(h) + "p"

		if y.ffmpegPath != "" {
			// When ffmpeg is available, ALL qualities go through HLS mux.
			// ffmpeg downloads from YouTube with server IP (matching yt-dlp extraction IP),
			// avoiding the 403 that occurs when proxying IP-bound URLs to a different client.
			if bestAudio.URL != "" {
				// Prefer video-only + separate best audio (better quality).
				if vf, ok := videoByHeight[h]; ok {
					cacheKey := vf.URL + "\x00" + bestAudio.URL
					muxKey := fmt.Sprintf("%x", md5Hash(cacheKey))[:16]
					muxURL := host + "/lite/youtube/mux/index.m3u8?key=" + muxKey
					result[label] = muxURL
					transAudio := true
					if strings.Contains(bestAudio.ACodec, "aac") || strings.Contains(bestAudio.ACodec, "mp4a") {
						transAudio = false
					}
					needFMP4 := !strings.HasPrefix(vf.VCodec, "avc1")
					muxPairs[label] = ytMuxPair{
						VideoURL:    vf.URL,
						AudioURL:    bestAudio.URL,
						VCodec:      vf.VCodec,
						ACodec:      bestAudio.ACodec,
						TransAudio:  transAudio,
						FMP4:        needFMP4,
						Height:      vf.Height,
						Width:       vf.Width,
						Bandwidth:   int((vf.TBR + bestAudio.ABR) * 1000),
						VInitRange:  vf.InitRange.rangeAttr(),
						VIndexRange: vf.IndexRange.rangeAttr(),
						AInitRange:  bestAudio.InitRange.rangeAttr(),
						AIndexRange: bestAudio.IndexRange.rangeAttr(),
					}
					y.muxMu.Lock()
					y.muxKeys[muxKey] = cacheKey
					y.muxVideoID[muxKey] = muxVideoID
					y.muxVCodecs[muxKey] = vf.VCodec
					y.muxProxy[muxKey] = usedProxy
					y.muxTransA[muxKey] = transAudio
					y.muxFMP4[muxKey] = needFMP4
					y.muxMu.Unlock()
					continue
				}
			}
			// Combined format or no separate audio — mux single input into HLS.
			if cf, ok := combinedByHeight[h]; ok {
				audioURL := bestAudio.URL
				if audioURL == "" {
					audioURL = cf.URL // use combined as both video and audio source
				}
				cacheKey := cf.URL + "\x00" + audioURL
				muxKey := fmt.Sprintf("%x", md5Hash(cacheKey))[:16]
				muxURL := host + "/lite/youtube/mux/index.m3u8?key=" + muxKey
				result[label] = muxURL
				muxPairs[label] = ytMuxPair{
					VideoURL:   cf.URL,
					AudioURL:   audioURL,
					VCodec:     cf.VCodec,
					ACodec:     cf.ACodec,
					TransAudio: false,
					Height:     cf.Height,
					Width:      cf.Width,
					Bandwidth:  int(cf.TBR * 1000),
				}
				y.muxMu.Lock()
				y.muxKeys[muxKey] = cacheKey
				y.muxVideoID[muxKey] = muxVideoID
				y.muxVCodecs[muxKey] = cf.VCodec
				y.muxProxy[muxKey] = usedProxy
				y.muxTransA[muxKey] = false
				y.muxMu.Unlock()
				continue
			}
		}

		// No ffmpeg — serve direct YouTube URLs (not proxied, because YouTube
		// binds stream URLs to the requester's IP; proxying changes the IP → 403).
		if cf, ok := combinedByHeight[h]; ok {
			result[label] = cf.URL
			continue
		}
		if vf, ok := videoByHeight[h]; ok {
			result[label] = vf.URL
		}
	}

	var audioProxyURL string
	if bestAudio.URL != "" {
		if y.ffmpegPath != "" {
			// With ffmpeg, audio is already muxed into HLS — no separate URL needed.
			audioProxyURL = ""
		} else {
			// Without ffmpeg, give direct YouTube URL for separate audio track.
			audioProxyURL = bestAudio.URL
		}
	}

	return ytQualityResult{
		Qualities: result,
		AudioURL:  audioProxyURL,
		MuxPairs:  muxPairs,
	}
}

// proxyYTURL wraps a raw YouTube stream URL through /proxy/ with required
// headers. `host` is expected to be the value returned by
// streamHostFromRequest so the emitted URL bypasses any fronting CDN.
func (y *YoutubeChecker) proxyYTURL(rawURL, reqIP string, links *proxylink.Manager, headers map[string]string, host string) string {
	if links == nil {
		return rawURL
	}
	encrypted := links.EncryptURIWithHeaders(rawURL, reqIP, "youtube", headers)
	if encrypted == "" {
		return rawURL
	}
	return host + "/proxy/" + encrypted
}

// ytAcceptProtocol returns true for protocols we can handle.
// "https" = direct download, "m3u8_native" = HLS stream (common with cookies/SABR).
func ytAcceptProtocol(proto string) bool {
	return proto == "https" || proto == "m3u8_native"
}

// ytBestLabel returns the best quality label from a map.
func ytBestLabel(quals map[string]string) string {
	return ytBestLabelCapped(quals, 0)
}

// ytBestLabelCapped returns the highest available quality label whose height is ≤ maxHeight
// (maxHeight ≤ 0 means no cap). Used to choose the single default-played quality without ever
// auto-selecting an oversized stream (e.g. 4K) for the real-time mux.
func ytBestLabelCapped(quals map[string]string, maxHeight int) string {
	for _, q := range ytQualityOrder {
		if _, ok := quals[q]; !ok {
			continue
		}
		if maxHeight > 0 {
			if h, err := strconv.Atoi(strings.TrimSuffix(q, "p")); err == nil && h > maxHeight {
				continue
			}
		}
		return q
	}
	// Fallback: any label present (covers non-standard labels not in ytQualityOrder).
	best, bestH := "", -1
	for q := range quals {
		h, _ := strconv.Atoi(strings.TrimSuffix(q, "p"))
		if maxHeight > 0 && h > maxHeight {
			continue
		}
		if h > bestH {
			best, bestH = q, h
		}
	}
	if best != "" {
		return best
	}
	// Everything is above the cap → fall back to the overall best so we never return "".
	for _, q := range ytQualityOrder {
		if _, ok := quals[q]; ok {
			return q
		}
	}
	for q := range quals {
		return q
	}
	return ""
}

// getFormats returns cached or freshly-extracted formats and duration for a video.
func (y *YoutubeChecker) getFormats(videoID string) ([]ytFormat, float64, bool, bool) {
	y.mu.RLock()
	cached, ok := y.cache[videoID]
	y.mu.RUnlock()

	if ok && time.Now().Before(cached.Expires) {
		// Negative cache hit: empty Formats means "extraction known to fail" —
		// don't burn yt-dlp slots re-trying the same VID while YouTube anti-bot
		// is still on. The TTL is short (see extractFormats fail path).
		if len(cached.Formats) == 0 {
			return nil, 0, false, false
		}
		return cached.Formats, cached.Duration, cached.UsedProxy, true
	}

	formats, duration, usedProxy, extractOK := y.extractFormats(videoID)
	if !extractOK {
		// Negative-cache the failure for 2 minutes. This is the typical
		// blast-radius window for anti-bot triggers on a single page open
		// (Subscriptions = 12 trailers) — short enough that a manual refresh
		// after the IP cools down re-tries, long enough that hls.js error
		// retry / Lampa preload doesn't re-flood the rate limiter.
		y.mu.Lock()
		y.cleanupCacheLocked()
		y.cache[videoID] = &ytFormatCache{
			Expires: time.Now().Add(2 * time.Minute),
		}
		y.mu.Unlock()
		return nil, 0, false, false
	}

	y.mu.Lock()
	y.cleanupCacheLocked()
	y.cache[videoID] = &ytFormatCache{
		Formats:   formats,
		Duration:  duration,
		Expires:   time.Now().Add(ytCacheTTL),
		UsedProxy: usedProxy,
	}
	y.mu.Unlock()

	return formats, duration, usedProxy, true
}

// hasRealFormats returns true if the format list contains at least one
// real video or audio format (not just storyboards/thumbnails).
// Storyboard formats have vcodec="none", acodec="none", protocol="mhtml".
func hasRealFormats(fmts []ytFormat) bool {
	for _, f := range fmts {
		hasVideo := f.VCodec != "" && f.VCodec != "none"
		hasAudio := f.ACodec != "" && f.ACodec != "none"
		if (hasVideo || hasAudio) && f.URL != "" {
			return true
		}
	}
	return false
}

// ytHasPOT reports whether a stream URL carries a PO Token. POT-bound URLs
// are session-bound (the bgutil plugin's session, not ours) — the CDN may
// accept them from yt-dlp yet 403 our external downloader, so POT-less
// alternatives are always preferable for the mux.
func ytHasPOT(u string) bool {
	return strings.Contains(u, "pot=")
}

// pickBestAudio returns the audio track buildQualityMap pairs with every
// video rung: m4a over webm, POT-less over POT-bound (see ytHasPOT — live
// 2026-08-03 the CDN 403'd mweb's POT audio while the same video's POT-less
// android_vr audio downloaded fine), then highest ABR.
func pickBestAudio(formats []ytFormat) ytFormat {
	var bestAudio ytFormat
	for _, f := range formats {
		if !f.isAudioOnly() || f.URL == "" || !ytAcceptProtocol(f.Protocol) {
			continue
		}
		// ext="mp4" appears on HLS audio-only formats (iOS client) — accept them too.
		if f.Ext != "m4a" && f.Ext != "webm" && f.Ext != "mp4" {
			continue
		}
		if bestAudio.URL == "" {
			bestAudio = f
			continue
		}
		// Prefer m4a over webm (better compatibility).
		if f.Ext == "m4a" && bestAudio.Ext != "m4a" {
			bestAudio = f
			continue
		}
		if f.Ext != bestAudio.Ext {
			continue
		}
		switch {
		case ytHasPOT(bestAudio.URL) && !ytHasPOT(f.URL):
			bestAudio = f
		case ytHasPOT(bestAudio.URL) == ytHasPOT(f.URL) && f.ABR > bestAudio.ABR:
			bestAudio = f
		}
	}
	return bestAudio
}

// isStrongResult reports whether an extraction offers a real DASH ladder —
// at least one video-only rung — rather than just the lone progressive
// fallback (format 18). A client reduced to one combined 360p (mweb behind
// the GVS-POT wall, android under SABR) must not beat the default client's
// full ladder.
func isStrongResult(fmts []ytFormat) bool {
	for _, f := range fmts {
		if f.isVideoOnly() && f.URL != "" && ytAcceptProtocol(f.Protocol) {
			return true
		}
	}
	return false
}

// probeFormats verifies the exact URLs the mux will consume (shared audio +
// top video rung). True when nothing is probeable — the probe can only vouch
// for direct googlevideo URLs.
func (y *YoutubeChecker) probeFormats(fmts []ytFormat) bool {
	for _, u := range pickProbeURLs(fmts) {
		if !y.probeStreamURL(u) {
			return false
		}
	}
	return true
}

// pickProbeURLs returns the direct googlevideo URLs the mux will actually
// download — the shared audio track (pickBestAudio) and the top video-only
// rung — for probeStreamURL. Both must be verified: live 2026-08-03 mweb's
// video URLs downloaded fine while its audio URLs 403'd, so probing just one
// side declares a poisoned session healthy. m3u8 URLs are excluded (a ranged
// GET of a playlist answers 200, which the probe would misread as poison).
func pickProbeURLs(fmts []ytFormat) []string {
	var urls []string
	if a := pickBestAudio(fmts); a.URL != "" && a.Protocol == "https" && !strings.Contains(a.URL, "m3u8") {
		urls = append(urls, a.URL)
	}
	var video ytFormat
	for _, f := range fmts {
		if f.URL == "" || f.Protocol != "https" || strings.Contains(f.URL, "m3u8") {
			continue
		}
		if !f.isVideoOnly() || (f.Ext != "mp4" && f.Ext != "webm") {
			continue
		}
		if f.Height > video.Height {
			video = f
		}
	}
	if video.URL != "" {
		urls = append(urls, video.URL)
	}
	return urls
}

// extractFormats runs yt-dlp -j strategies sequentially and returns the first
// result whose stream URLs both exist and actually download (verified by
// probeStreamURL; see the strategy comments inside for the current client
// health picture).
func (y *YoutubeChecker) extractFormats(videoID string) (formats []ytFormat, duration float64, usedProxy bool, ok bool) {
	ytURL := "https://www.youtube.com/watch?v=" + videoID

	type strategy struct {
		client   string
		proxy    bool
		priority int
	}

	type extractResult struct {
		formats   []ytFormat
		duration  float64
		usedProxy bool
		priority  int // lower = better
	}

	// Build strategy list (parallel execution, first viable result by priority wins).
	//
	// As of yt-dlp 2026.06.09 + server-side test 2026-08-03 (all clients run
	// with fetch_pot=never — see runExtract):
	//   - ""  (default client mix): lands on android_vr — full DASH ladder up
	//                  to 4K AV1, POT-less, URLs download fine on every request.
	//                  The ONLY fully healthy client, hence primary.
	//   - "mweb" + cookies: 4K HDR AV1 when healthy. POT-less it currently
	//                  hides its https formats ("requires a GVS PO Token") →
	//                  only progressive 18 → weak result, falls through.
	//   - "tv" (no cookies): 4K AV1 when healthy; currently hits the DRM
	//                  experiment (yt-dlp#12563) → no https formats → falls through.
	//   - "android" (no cookies): SABR-only experiment strips URLs → at best
	//                  progressive 18 (360p, not IP-bound) — last-ditch fallback.
	//
	// REMOVED:
	//   - "ios": requires GVS POT outright.
	//
	// Extraction success no longer implies playable URLs (a poisoned client
	// still returns a full-looking format list) — each candidate is verified
	// with a tiny ranged download (probeStreamURL) and skipped for
	// stratCooldownTTL when its URLs don't actually download.
	//
	// When proxyAddr is set (AntiDPI or VLESS), ALL strategies use the proxy.
	// On Russian servers YouTube is DPI-blocked, so direct connections always fail.
	proxy := y.curProxy() != ""
	strategies := []strategy{
		{"", proxy, 0},        // default client mix — android_vr: 4K AV1, POT-less, healthy 2026-08
		{"mweb", proxy, 1},    // mweb + cookies — 4K HDR AV1 if YouTube drops the GVS-POT wall
		{"tv", proxy, 2},      // tv — 4K AV1 if the DRM experiment ends
		{"android", proxy, 3}, // android — 360p format 18 when everything else fails
	}
	// ★2026-08-14 (второй разворот стола): POT-less URL (android_vr) отдают только
	// «грейс-окно» ~20-25 МиБ, причём МИГАЮЩЕЕ — проба может пройти, а mux упереться
	// в 403 на середине. mweb с bgutil-POT (pot= в URL) качается детерминированно
	// целиком (чанк-тесты за окном 206). Когда POT доступен — mweb первым, дефолтный
	// микс фолбэком; двухфазная probeStreamURL отбраковывает мигающие варианты.
	if y.pluginDirs != "" && !y.fetchPotNever {
		strategies = []strategy{
			{"mweb", proxy, 0},
			{"", proxy, 1},
			{"tv", proxy, 2},
			{"android", proxy, 3},
		}
	}

	// Run strategies SEQUENTIALLY with early-return: try the first, return
	// on success; only fall through to the next on failure. This used to be
	// parallel, but parallel × 12 trailers on the Subscriptions page meant 36
	// yt-dlp invocations against YouTube within ~2s, triggering anti-bot
	// (HTTP 429 + "Sign in to confirm you're not a bot") that broke every
	// subsequent request from the same IP for tens of minutes.
	// The no-cookies default-client extraction that used to run only AFTER the
	// winner was chosen (to backfill DASH heights + audio for mux) now runs
	// lazily BEFORE probing: its merged POT-less audio is what makes a
	// POT-bound winner playable, so the probe must judge the merged list.
	// Memoized — at most one run per extractFormats call.
	var hiFmts []ytFormat
	var hiOK, hiRan bool
	noCookieFormats := func() ([]ytFormat, bool) {
		if !hiRan {
			hiRan = true
			hiFmts, _, hiOK = y.runExtractNoCookies(videoID, ytURL, proxy)
		}
		return hiFmts, hiOK
	}

	var best *extractResult
	var weak *extractResult     // extraction that downloads but offers no DASH ladder (lone format 18)
	var unproven *extractResult // extraction that failed only the probe
	probeRetried := map[string]bool{}
	// FAIL-OPEN over the cooldowns.
	//
	// The cooldown exists to skip a client that is individually dead (DRM experiment, POT wall).
	// But when YouTube challenges the whole egress IP («Sign in to confirm you're not a bot»),
	// EVERY client fails within seconds and all of them get retired at once — after which every
	// request short-circuits and returns «all extraction strategies failed» instantly, for the
	// full TTL, even though the block has long passed. Live incident 2026-08-17: yt-dlp extracted
	// 27 formats by hand while the app was still refusing to try.
	//
	// So the cooldowns are advisory: if they would skip EVERYTHING, run the pass again ignoring
	// them. Same lesson as the torrent balancer's fail-open — a health check that can take the
	// whole pool down is worse than no health check.
	// Strategies that hit an IP-level challenge rather than failing on their own merits. The
	// challenge is intermittent, so these are worth one more go before settling for a weaker
	// client (see the retry below).
	var blockedStrats []strategy

	allCooling := true
	for _, s := range strategies {
		label := s.client
		if label == "" {
			label = "default"
		}
		y.mu.RLock()
		coolUntil, cooling := y.stratCooldown[label]
		y.mu.RUnlock()
		if !cooling || !time.Now().Before(coolUntil) {
			allCooling = false
			break
		}
	}
	if allCooling {
		log.Warn().Str("videoID", videoID).Msg("youtube: every client is in cooldown — ignoring cooldowns for this attempt (fail-open)")
	}

	for _, s := range strategies {
		label := s.client
		if label == "" {
			label = "default"
		}
		if !allCooling {
			y.mu.RLock()
			coolUntil, cooling := y.stratCooldown[label]
			y.mu.RUnlock()
			if cooling && time.Now().Before(coolUntil) {
				log.Debug().Str("videoID", videoID).Str("client", label).Time("until", coolUntil).Msg("youtube: strategy in cooldown — skipping")
				continue
			}
		}
		fmts, dur, extractOK, blocked := y.runExtractDetailed(videoID, ytURL, s.client, s.proxy)
		if !extractOK || !hasRealFormats(fmts) {
			// An IP-level refusal says nothing about THIS client — parking it would just retire
			// every client in turn and turn a passing block into a self-inflicted outage.
			if blocked {
				log.Warn().Str("videoID", videoID).Str("client", label).
					Msg("youtube: IP-level block (anti-bot/429) — not retiring the client")
				blockedStrats = append(blockedStrats, s)
				continue
			}
			// Hard-dead client (extraction error, DRM experiment, POT wall
			// hiding every format) — skip it for a while so cache-missed VIDs
			// don't re-pay this extraction before reaching a live client.
			y.mu.Lock()
			y.stratCooldown[label] = time.Now().Add(stratCooldownTTL)
			y.mu.Unlock()
			continue
		}
		// A client that just worked is healthy — clear any stale cooldown so a single bad
		// minute doesn't keep it sidelined for the rest of the TTL.
		y.mu.Lock()
		delete(y.stratCooldown, label)
		y.mu.Unlock()
		// Backfill DASH heights and POT-less audio from the default client
		// (skipped when the winner IS the default client — same family).
		if y.cookiePath != "" && s.client != "" {
			if hi, ok := noCookieFormats(); ok {
				fmts = mergeDefaultClientFormats(videoID, fmts, hi)
			}
		}
		res := &extractResult{
			formats:   fmts,
			duration:  dur,
			usedProxy: s.proxy,
			priority:  s.priority,
		}
		// A client reduced to a lone progressive fallback (format 18) must
		// not beat a later client's full DASH ladder — keep it as a fallback
		// and move on. No cooldown: it stays usable as the last resort.
		if !isStrongResult(res.formats) {
			log.Debug().Str("videoID", videoID).Str("client", label).Int("formats", len(res.formats)).Msg("youtube: weak result (no DASH video) — trying next client")
			if weak == nil {
				weak = res
			}
			continue
		}
		// Verify the exact URLs the mux will consume (shared audio + top video)
		// actually download before declaring a winner. No probeable direct URL
		// (e.g. HLS-only result) → accept as-is: the probe can only vouch for
		// direct googlevideo URLs.
		if y.probeFormats(res.formats) {
			best = res
			break
		}
		// ★Немедленный ОДИН ретрай mweb со свежей экстракцией: POT/URL мигают, и
		// свежий POT часто качается там, где предыдущий только что 403-ил. Без
		// этого одно мигание роняло юзера в android=360p, хотя следующая же
		// попытка mweb дала бы HD. Ретраим до кулдауна — mweb наш primary клиент.
		if s.client == "mweb" && !probeRetried["mweb"] {
			probeRetried["mweb"] = true
			log.Debug().Str("videoID", videoID).Msg("youtube: mweb probe failed — one immediate retry with a fresh POT")
			if fmts2, dur2, ok2 := y.runExtract(videoID, ytURL, s.client, s.proxy); ok2 && hasRealFormats(fmts2) {
				if y.cookiePath != "" {
					if hi, ok := noCookieFormats(); ok {
						fmts2 = mergeDefaultClientFormats(videoID, fmts2, hi)
					}
				}
				res2 := &extractResult{formats: fmts2, duration: dur2, usedProxy: s.proxy, priority: s.priority}
				if isStrongResult(res2.formats) && y.probeFormats(res2.formats) {
					best = res2
					break
				}
			}
		}
		log.Warn().Str("videoID", videoID).Str("client", label).Int("formats", len(res.formats)).
			Msg("youtube: extraction ok but stream URLs don't download (403/DRM/POT poison) — trying next client")
		y.mu.Lock()
		y.stratCooldown[label] = time.Now().Add(stratProbeCooldownTTL) // мигание, не смерть — короткий кулдаун
		y.mu.Unlock()
		if unproven == nil {
			unproven = res
		}
	}

	// Settling for a weak result while the BEST client was merely challenged wastes the good
	// ladder: live 2026-08-17 the default client returned 6 heights by hand at the same moment
	// the app was serving a 360p mweb fallback, because default had caught one anti-bot refusal.
	// The challenge is intermittent, so give the blocked clients one more pass — in priority
	// order — before accepting the downgrade.
	if best == nil && len(blockedStrats) > 0 {
		for _, s := range blockedStrats {
			label := s.client
			if label == "" {
				label = "default"
			}
			log.Info().Str("videoID", videoID).Str("client", label).Msg("youtube: retrying a client that hit an IP-level block")
			fmts, dur, ok, _ := y.runExtractDetailed(videoID, ytURL, s.client, s.proxy)
			if !ok || !hasRealFormats(fmts) {
				continue
			}
			if y.cookiePath != "" && s.client != "" {
				if hi, hiOK := noCookieFormats(); hiOK {
					fmts = mergeDefaultClientFormats(videoID, fmts, hi)
				}
			}
			res := &extractResult{formats: fmts, duration: dur, usedProxy: s.proxy, priority: s.priority}
			if !isStrongResult(res.formats) {
				if weak == nil {
					weak = res
				}
				continue
			}
			if y.probeFormats(res.formats) {
				best = res
				break
			}
			if unproven == nil {
				unproven = res
			}
		}
	}

	// No full-ladder winner: prefer a weak-but-downloadable result (lone
	// 360p), and only then an unproven one (probe said its URLs don't
	// download — e.g. DPI server with no working route, where the mux shares
	// the same routes and nothing would play either way; if the probe was
	// wrong, playback still works).
	if best == nil && weak != nil {
		if y.probeFormats(weak.formats) {
			log.Warn().Str("videoID", videoID).Msg("youtube: no full-ladder strategy — using weak (progressive-only) result")
			best = weak
		} else if unproven == nil {
			unproven = weak
		}
	}
	if best == nil && unproven != nil {
		log.Warn().Str("videoID", videoID).Msg("youtube: no strategy passed the download probe — using best unproven result")
		best = unproven
	}

	if best == nil {
		log.Warn().Str("videoID", videoID).Msg("youtube: all extraction strategies failed")
		// Экстракция сдохла у ВСЕХ клиентов сразу — почти всегда виноват выход (умер/
		// rate-limit), а не сам ютуб. Копим стрик и после порога ротируем выход (это
		// поймает флапающий WARP, который проходит ping-health, но душится под нагрузкой).
		if y.extractFail.Add(1) >= 3 {
			y.extractFail.Store(0)
			go y.rotateProxyOnFailure()
		}
		return nil, 0, false, false
	}
	y.extractFail.Store(0) // успех — стрик сбрасываем

	proxyLabel := ""
	if proxy {
		proxyLabel = "+proxy"
	}
	stratLabels := map[int]string{0: "default" + proxyLabel, 1: "mweb" + proxyLabel, 2: "tv" + proxyLabel, 3: "android" + proxyLabel}
	log.Debug().Str("videoID", videoID).Str("winner", stratLabels[best.priority]).Int("formats", len(best.formats)).Msg("youtube: extraction winner")

	return best.formats, best.duration, best.usedProxy, true
}

// mergeDefaultClientFormats backfills into fmts every video-only height and
// audio-only track from the no-cookies default-client extraction that fmts
// doesn't already have. The default/web client returns a richer DASH ladder
// than the mobile clients (populates the 240/480/720/1080p rungs when the
// winner is android's lone format 18), and its audio is POT-less — which
// pickBestAudio prefers, keeping the mux playable when the winner client's
// own audio is POT-bound.
func mergeDefaultClientFormats(videoID string, fmts, hiFmts []ytFormat) []ytFormat {
	haveHeight := make(map[int]bool)
	for _, f := range fmts {
		if f.isVideoOnly() && f.Height > 0 {
			haveHeight[f.Height] = true
		}
	}
	var addedV int
	for _, f := range hiFmts {
		if !f.isVideoOnly() || f.URL == "" || f.Protocol != "https" {
			continue
		}
		if haveHeight[f.Height] {
			continue
		}
		fmts = append(fmts, f)
		haveHeight[f.Height] = true
		addedV++
	}
	// Always merge audio-only too — the mux pairs hi/mid video with the
	// best audio we can find, and the winner client often returns only
	// id 140 m4a without alternatives.
	haveAudio := make(map[string]bool)
	for _, f := range fmts {
		if f.isAudioOnly() && f.URL != "" {
			haveAudio[f.URL] = true
		}
	}
	var addedA int
	for _, f := range hiFmts {
		if !f.isAudioOnly() || f.URL == "" || f.Protocol != "https" {
			continue
		}
		if haveAudio[f.URL] {
			continue
		}
		fmts = append(fmts, f)
		addedA++
	}
	if addedV > 0 || addedA > 0 {
		log.Info().Int("video", addedV).Int("audio", addedA).Str("videoID", videoID).Msg("youtube: merged DASH formats from default client")
	}
	return fmts
}

// runExtractNoCookies runs yt-dlp without cookies to get DASH video-only formats
// (1440p, 2160p in VP9/AV1) that are unavailable with cookies (SABR m3u8).
func (y *YoutubeChecker) runExtractNoCookies(videoID, ytURL string, useProxy bool) ([]ytFormat, float64, bool) {
	ytdlpPath := y.ensureYtdlp()
	if ytdlpPath == "" {
		return nil, 0, false
	}

	args := []string{
		"-j",
		// Force IPv4 — keep extraction-IP and ffmpeg/pipe-download-IP in the same
		// family so the &ip= signature in stream URLs matches what the antidpi
		// tcp4 dialers actually use. See ytSearchN comment for full rationale.
		"-4",
		"--no-check-certificates",
		"--no-check-formats",
		"--ignore-no-formats-error",
		"--socket-timeout", "15",
	}
	// baseArgs without cookies — JS runtime + plugin-dirs (POT обязателен с 2026-08-14,
	// см. baseArgs; без него URL режутся тизерным окном CDN).
	if y.pluginDirs != "" && !y.fetchPotNever {
		args = append(args, "--plugin-dirs", y.pluginDirs)
	}
	if y.jsRuntime != "" {
		args = append(args, "--js-runtimes", y.jsRuntime)
	}
	// No POT ever — POT'd URLs break the mux's chunked downloader (see runExtract).
	if y.fetchPotNever {
		args = append(args, "--extractor-args", "youtube:fetch_pot=never")
	}
	if useProxy && y.curProxy() != "" {
		args = append(args, "--proxy", "socks5://"+y.curProxy())
	}
	args = append(args, ytURL)

	cmd := exec.Command(ytdlpPath, args...)
	cmd.Env = os.Environ()
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// Wait for a global yt-dlp slot so we don't flood YouTube with parallel
	// extractions across multiple VIDs.
	y.ytdlpSem <- struct{}{}
	err := runWithTimeoutNoOutput(cmd, ytExecTimeout)
	<-y.ytdlpSem
	out := stdoutBuf.Bytes()

	if err != nil && len(out) == 0 {
		log.Debug().Err(err).Str("videoID", videoID).Msg("youtube: no-cookies extract failed")
		return nil, 0, false
	}

	out = sanitizeYtdlpJSON(out)
	var dump ytDumpJSON
	if err := stdjson.Unmarshal(out, &dump); err != nil {
		return nil, 0, false
	}

	log.Debug().Int("formats", len(dump.Formats)).Str("videoID", videoID).Msg("youtube: no-cookies formats extracted")
	return dump.Formats, dump.Duration, true
}

// runExtract executes a single yt-dlp extraction attempt.
// playerClient can be empty ("") to let yt-dlp pick the default client
// (no --extractor-args), or a specific client name like "tv", "mweb", etc.
// ytAntiBotStderr matches YouTube's IP-level refusals. These say nothing about the client we
// asked with — the whole egress IP is being challenged — so they must NOT retire a client.
func ytAntiBotStderr(stderr string) bool {
	l := strings.ToLower(stderr)
	return strings.Contains(l, "sign in to confirm") ||
		strings.Contains(l, "confirm you") && strings.Contains(l, "bot") ||
		strings.Contains(l, "http error 429") ||
		strings.Contains(l, "too many requests")
}

func (y *YoutubeChecker) runExtract(videoID, ytURL, playerClient string, useProxy bool) ([]ytFormat, float64, bool) {
	fmts, dur, ok, _ := y.runExtractDetailed(videoID, ytURL, playerClient, useProxy)
	return fmts, dur, ok
}

// runExtractDetailed additionally reports whether the failure was an IP-level refusal
// («Sign in to confirm you're not a bot», HTTP 429) rather than something about this client.
// The distinction matters: retiring a client for an IP-wide block takes every client out at once.
func (y *YoutubeChecker) runExtractDetailed(videoID, ytURL, playerClient string, useProxy bool) ([]ytFormat, float64, bool, bool) {
	ytdlpPath := y.ensureYtdlp()
	if ytdlpPath == "" {
		return nil, 0, false, false
	}

	clientLabel := playerClient
	if clientLabel == "" {
		clientLabel = "default"
	}

	args := []string{
		"-j",
		// Force IPv4 — keep extraction-IP and ffmpeg/pipe-download-IP in the same
		// family so the &ip= signature in stream URLs matches what the antidpi
		// tcp4 dialers actually use. See ytSearchN comment for full rationale.
		"-4",
		"--no-check-certificates",
		"--no-check-formats",
		"--ignore-no-formats-error",
		"--socket-timeout", "15",
	}
	// tv / android / android_vr / tv_simply / ios don't accept cookies —
	// pass JS runtime only. yt-dlp would otherwise skip these clients silently
	// with "client X does not support cookies". (plugin-dirs intentionally
	// omitted everywhere — see baseArgs comment.)
	switch playerClient {
	case "tv", "android", "android_vr", "tv_simply", "ios":
		// без кук, но плагины нужны и тут — POT-less URL упираются в тизерное окно CDN
		if y.pluginDirs != "" && !y.fetchPotNever {
			args = append(args, "--plugin-dirs", y.pluginDirs)
		}
		if y.jsRuntime != "" {
			args = append(args, "--js-runtimes", y.jsRuntime)
		}
	default:
		args = append(args, y.baseArgs()...)
	}
	// PO Token policy — конфигурируемая ([youtube] fetch_pot), история двух инцидентов:
	// 2026-08-03: POT-URL от bgutil были ядом (CDN 403-ил после первых запросов) → never.
	// 2026-08-14: Google перевернул стол — БЕЗ POT CDN отдаёт только первые ~20 МиБ
	// (замерено: Range за офсетом ~20МиБ → 403 на любом маршруте), и never стал ломать
	// длинные видео. Дефолт auto; отравленные комбинации отсекает probeStreamURL,
	// который проверяет и офсет ЗА тизерным окном.
	extractorArgs := ""
	if playerClient != "" {
		extractorArgs = "youtube:player_client=" + playerClient
	}
	if y.fetchPotNever {
		if extractorArgs == "" {
			extractorArgs = "youtube:fetch_pot=never"
		} else {
			extractorArgs += ";fetch_pot=never"
		}
	}
	if extractorArgs != "" {
		args = append(args, "--extractor-args", extractorArgs)
	}
	if useProxy && y.curProxy() != "" {
		args = append(args, "--proxy", "socks5://"+y.curProxy())
	}
	args = append(args, ytURL)

	cmd := exec.Command(ytdlpPath, args...)
	cmd.Env = os.Environ()
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	y.ytdlpSem <- struct{}{}
	err := runWithTimeoutNoOutput(cmd, ytExecTimeout)
	<-y.ytdlpSem
	out := stdoutBuf.Bytes()

	// Even on error, try to parse stdout — yt-dlp may output JSON before failing.
	if err != nil && len(out) == 0 {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if len(stderrStr) > 500 {
			stderrStr = stderrStr[len(stderrStr)-500:]
		}
		log.Warn().Err(err).Str("videoID", videoID).Str("client", clientLabel).Bool("proxy", useProxy).Str("stderr", stderrStr).Msg("youtube: extract attempt failed")
		return nil, 0, false, ytAntiBotStderr(stderrStr)
	}

	// yt-dlp may emit WARNING lines before JSON despite --no-warnings.
	// Strip everything before the first '{' to get clean JSON.
	out = sanitizeYtdlpJSON(out)

	var dump ytDumpJSON
	if err := stdjson.Unmarshal(out, &dump); err != nil {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if len(stderrStr) > 300 {
			stderrStr = stderrStr[len(stderrStr)-300:]
		}
		log.Warn().Err(err).Str("videoID", videoID).Str("client", clientLabel).Int("outLen", len(out)).Str("stderr", stderrStr).Msg("youtube: parse attempt failed")
		return nil, 0, false, ytAntiBotStderr(stderrStr)
	}

	if len(dump.Formats) == 0 {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if len(stderrStr) > 1000 {
			stderrStr = stderrStr[len(stderrStr)-1000:]
		}
		log.Warn().Str("videoID", videoID).Str("client", clientLabel).Bool("proxy", useProxy).Str("stderr", stderrStr).Msg("youtube: yt-dlp returned 0 formats")
		return nil, 0, false, ytAntiBotStderr(stderrStr)
	}

	// Log stderr when format count is suspiciously low (possible PO token issue).
	if len(dump.Formats) < 10 {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if len(stderrStr) > 1000 {
			stderrStr = stderrStr[len(stderrStr)-1000:]
		}
		if stderrStr != "" {
			log.Warn().Str("videoID", videoID).Str("client", clientLabel).Int("formats", len(dump.Formats)).Str("stderr", stderrStr).Msg("youtube: low format count — check stderr")
		} else {
			log.Warn().Str("videoID", videoID).Str("client", clientLabel).Int("formats", len(dump.Formats)).Msg("youtube: low format count, no stderr (PO token plugin may not be loaded)")
		}
	}

	log.Debug().Str("videoID", videoID).Str("client", clientLabel).Bool("proxy", useProxy).Int("formats", len(dump.Formats)).Float64("duration", dump.Duration).Msg("youtube: formats extracted")
	return dump.Formats, dump.Duration, true, false
}

// sanitizeYtdlpJSON strips any non-JSON prefix from yt-dlp output.
// yt-dlp sometimes emits WARNING/ERROR lines before the JSON object
// (despite --no-warnings), which breaks json.Unmarshal.
func sanitizeYtdlpJSON(data []byte) []byte {
	if idx := bytes.IndexByte(data, '{'); idx > 0 {
		return data[idx:]
	}
	return data
}

// ytImgProxyURL returns a proxied thumbnail URL via our server instead of i.ytimg.com.
// Clients with blocked YouTube can still see thumbnails this way.
func ytImgProxyURL(host, videoID string) string {
	return host + "/lite/youtube/img?id=" + videoID
}

// ytImgProxyRawURL proxies an arbitrary YouTube image (e.g. a channel avatar on ggpht/
// googleusercontent) through /lite/youtube/img?url= so the client never fetches Google directly
// (avoids DNS-block / client-IP leak — same reason video thumbs go through the proxy). Host is
// validated by isYouTubeImageHost in the handler.
func ytImgProxyRawURL(host, raw string) string {
	if raw == "" {
		return ""
	}
	return host + "/lite/youtube/img?url=" + url.QueryEscape(raw)
}

// isYouTubeImageHost reports whether h is one of the YouTube image/avatar hosts the proxy is allowed
// to fetch via ?url= (SSRF guard — keeps the open-ended url param from reaching arbitrary hosts).
func isYouTubeImageHost(h string) bool {
	h = strings.ToLower(h)
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i] // strip :port
	}
	for _, suf := range []string{"ytimg.com", "ggpht.com", "googleusercontent.com"} {
		if h == suf || strings.HasSuffix(h, "."+suf) {
			return true
		}
	}
	return false
}

// ytImgProxyMaybe returns a proxied thumbnail URL (/lite/youtube/img?url=) for a raw YouTube image
// URL (channel/playlist avatars, whose URL can't be derived from a video id), or "" when raw is empty
// or not a YouTube image host — so the caller can fall back rather than leak the raw external URL.
func ytImgProxyMaybe(host, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !isYouTubeImageHost(u.Host) {
		return ""
	}
	return host + "/lite/youtube/img?url=" + url.QueryEscape(raw)
}

// ytImgProxyAddr / ytImgProxyClient route thumbnail fetches through the same anti-DPI SOCKS5
// proxy the checker uses for extraction. Set once at checker construction (setYtImgProxyAddr);
// empty ⇒ direct. Without this, i.ytimg.com is unreachable on DPI-blocked hosts and previews
// come back blank even though the (server-muxed) videos play fine.
var (
	ytImgProxyAddr   string
	ytImgClientOnce  sync.Once
	ytImgProxyClient *http.Client
)

func setYtImgProxyAddr(addr string) { ytImgProxyAddr = addr }

// ytImgFetchClient returns the proxied uTLS client when a bypass is configured, else the
// default client. Built lazily so a "proxy = none" server pays nothing.
func ytImgFetchClient() *http.Client {
	if ytImgProxyAddr == "" {
		return http.DefaultClient
	}
	ytImgClientOnce.Do(func() {
		ytImgProxyClient = httpclient.NewTLSClientViaSOCKS5(ytImgProxyAddr, 10*time.Second)
	})
	if ytImgProxyClient == nil {
		return http.DefaultClient
	}
	return ytImgProxyClient
}

// YtImgProxyHandler proxies YouTube video thumbnails (i.ytimg.com) and channel/playlist avatars
// (yt3.ggpht.com / *.googleusercontent.com) through the server. Registered as a public endpoint
// (no auth required) so clients with blocked/slow YouTube can still see thumbnails. Accepts either
// ?id=VIDEOID (→ hqdefault.jpg) or ?url=RAW (restricted to YouTube image hosts).
func YtImgProxyHandler(w http.ResponseWriter, req *http.Request) {
	var imgURL string

	if raw := strings.TrimSpace(req.URL.Query().Get("url")); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !isYouTubeImageHost(u.Host) {
			http.Error(w, "bad url", http.StatusBadRequest)
			return
		}
		imgURL = raw
	} else {
		videoID := strings.TrimSpace(req.URL.Query().Get("id"))
		if videoID == "" || len(videoID) > 20 {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		// Sanitize: only allow alphanumeric, dash, underscore.
		for _, c := range videoID {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				http.Error(w, "bad id", http.StatusBadRequest)
				return
			}
		}
		imgURL = "https://i.ytimg.com/vi/" + videoID + "/hqdefault.jpg"
	}

	ctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
	defer cancel()

	hreq, _ := http.NewRequestWithContext(ctx, "GET", imgURL, nil)
	hreq.Header.Set("User-Agent", "Mozilla/5.0")

	client := ytImgFetchClient()
	resp, err := client.Do(hreq)
	if err != nil && client != http.DefaultClient {
		// Proxy path failed (proxy down / not yet up) — last-resort direct fetch so a
		// working direct route still serves the thumbnail.
		hreq2, _ := http.NewRequestWithContext(ctx, "GET", imgURL, nil)
		hreq2.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err = http.DefaultClient.Do(hreq2)
	}
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// buildBestDashURL builds a single-best-quality DASH manifest URL (→ handleDashMPD) from the
// extracted formats: the highest-res video-only stream + best audio, each served through /proxy so
// the client fetches via the SERVER IP (googlevideo URLs are bound to the yt-dlp extraction IP).
// Returns "" when there's nothing to build. ⚠️ NOT yet verified against live YouTube.
func (y *YoutubeChecker) buildBestDashURL(host string, qr ytQualityResult, links *proxylink.Manager, reqIP string) string {
	if links == nil || len(qr.MuxPairs) == 0 {
		return ""
	}
	// Multi-representation MPD: ONE <Representation> per height (prefer avc1 for MSE compat), all
	// sharing the best audio track. This is what lets the player offer a real quality picker instead
	// of just «auto» — Shaka reads every height from the manifest. Only pairs that carry DASH byte
	// ranges qualify (no init+index range ⇒ no SegmentBase ⇒ the MP4 can't be indexed).
	byHeight := map[int]ytMuxPair{}
	var audio ytMuxPair
	for _, p := range qr.MuxPairs {
		if p.AudioURL != "" && p.AInitRange != "" && p.AIndexRange != "" && audio.AudioURL == "" {
			audio = p // best audio (MuxPairs share one) — first range-indexed one
		}
		if p.VideoURL == "" || p.VInitRange == "" || p.VIndexRange == "" {
			continue
		}
		cur, ok := byHeight[p.Height]
		if !ok || (strings.HasPrefix(p.VCodec, "avc") && !strings.HasPrefix(cur.VCodec, "avc")) {
			byHeight[p.Height] = p // prefer avc1 at a given height
		}
	}
	if audio.AudioURL == "" {
		return "" // no range-indexed audio → caller falls back to the HLS mux (res.stream)
	}
	heights := make([]int, 0, len(byHeight))
	for h := range byHeight {
		heights = append(heights, h)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(heights)))

	q := url.Values{}
	for _, h := range heights {
		p := byHeight[h]
		vTok := links.EncryptURI(p.VideoURL, reqIP, "youtube", false, false, false)
		if vTok == "" {
			continue
		}
		vbr := p.Bandwidth * 8 / 10
		if vbr <= 0 {
			vbr = 1_000_000
		}
		// height~codec~bw~vinit~vindex~url — the /proxy token is [A-Za-z0-9_-] (no '~'), so a
		// SplitN(rep,"~",6) in handleDashMPD recovers the fields with the URL intact.
		q.Add("vr", fmt.Sprintf("%d~%s~%d~%s~%s~%s", h, p.VCodec, vbr, p.VInitRange, p.VIndexRange, host+"/proxy/"+vTok))
	}
	if len(q["vr"]) == 0 {
		return ""
	}
	aTok := links.EncryptURI(audio.AudioURL, reqIP, "youtube", false, false, false)
	if aTok == "" {
		return ""
	}
	q.Set("audio", host+"/proxy/"+aTok)
	q.Set("acodec", audio.ACodec)
	q.Set("abr", "128000")
	q.Set("air", audio.AInitRange)
	q.Set("aix", audio.AIndexRange)
	// Diagnostic: how many quality rungs the multi-rep DASH carries. 1 here ⇒ the client picker shows
	// a single height — usually means yt-dlp returned only one byte-range-indexed video format.
	log.Debug().Int("reps", len(q["vr"])).Int("muxpairs", len(qr.MuxPairs)).Msg("youtube: buildBestDashURL multi-rep")
	return host + "/lite/youtube/dash.mpd?" + q.Encode()
}

// handleDashMPD generates a minimal DASH MPD manifest combining a video-only and
// audio-only stream into a single presentation that players like ExoPlayer can play.
func (y *YoutubeChecker) HandleDashMPD(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	// Multi-representation path (quality picker): one video AdaptationSet per codec family, each with a
	// Representation per height, plus the shared audio. Falls through to the legacy single-rep path
	// (video=/audio= params) when no vr[] is present.
	if reps := q["vr"]; len(reps) > 0 {
		y.writeMultiDashMPD(w, q, reps)
		return
	}
	videoURL := strings.TrimSpace(q.Get("video"))
	audioURL := strings.TrimSpace(q.Get("audio"))
	height := q.Get("height")
	vcodec := q.Get("vcodec")
	acodec := q.Get("acodec")
	vbr := q.Get("vbr")
	abr := q.Get("abr")
	vInit, vIndex := q.Get("vir"), q.Get("vix")
	aInit, aIndex := q.Get("air"), q.Get("aix")

	if videoURL == "" || audioURL == "" {
		http.Error(w, "missing video/audio params", http.StatusBadRequest)
		return
	}

	// Determine width from height (assume 16:9).
	h, _ := strconv.Atoi(height)
	if h <= 0 {
		h = 720
	}
	widthVal := h * 16 / 9

	// Map codec strings to DASH codecs attribute.
	videoCodecs := ytDashCodec(vcodec, true)
	audioCodecs := ytDashCodec(acodec, false)

	if vbr == "" {
		vbr = "3000000"
	}
	if abr == "" {
		abr = "128000"
	}

	// Escape URLs for XML.
	videoURLEsc := xmlEscape(videoURL)
	audioURLEsc := xmlEscape(audioURL)

	mpd := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011"
     profiles="urn:mpeg:dash:profile:isoff-on-demand:2011"
     type="static"
     minBufferTime="PT2S">
  <Period>
    <AdaptationSet mimeType="video/mp4" contentType="video" subsegmentAlignment="true">
      <Representation id="video" bandwidth="%s" codecs="%s" width="%d" height="%d">
        <BaseURL>%s</BaseURL>%s
      </Representation>
    </AdaptationSet>
    <AdaptationSet mimeType="audio/mp4" contentType="audio" subsegmentAlignment="true">
      <Representation id="audio" bandwidth="%s" codecs="%s" audioSamplingRate="44100">
        <AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" value="2"/>
        <BaseURL>%s</BaseURL>%s
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`,
		vbr, videoCodecs, widthVal, h, videoURLEsc, dashSegmentBase(vIndex, vInit),
		abr, audioCodecs, audioURLEsc, dashSegmentBase(aIndex, aInit),
	)

	w.Header().Set("Content-Type", "application/dash+xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(mpd)))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(mpd))
}

// dashSegmentBase renders the <SegmentBase>/<Initialization> block for an on-demand ISOBMFF
// representation, or "" when ranges are missing/malformed. Without it players can't index the
// fragmented MP4 and won't play. Ranges are validated as "digits-digits" (defence against the
// query params injecting XML, even though we generate them ourselves).
// writeMultiDashMPD renders a multi-quality on-demand MPD: video Representations grouped into one
// AdaptationSet PER CODEC FAMILY (avc1/vp09/av01 — mixing codecs in a single set confuses some
// players), plus the shared audio. Shaka then exposes every height so the player's quality picker has
// real choices instead of just «auto». Each `vr` = "height~codec~bw~vinit~vindex~url".
func (y *YoutubeChecker) writeMultiDashMPD(w http.ResponseWriter, q url.Values, reps []string) {
	audioURL := strings.TrimSpace(q.Get("audio"))
	if audioURL == "" {
		http.Error(w, "missing audio param", http.StatusBadRequest)
		return
	}
	abr := q.Get("abr")
	if abr == "" {
		abr = "128000"
	}

	type vrep struct {
		h, bw       int
		codec       string // DASH codecs attribute
		init, index string
		url         string
	}
	groups := map[string][]vrep{} // codec family (avc1/vp09/av01) → reps
	var order []string            // stable family order (first-seen)
	for _, rep := range reps {
		p := strings.SplitN(rep, "~", 6)
		if len(p) != 6 {
			continue
		}
		h, _ := strconv.Atoi(p[0])
		if h <= 0 || p[5] == "" {
			continue
		}
		bw, _ := strconv.Atoi(p[2])
		if bw <= 0 {
			bw = 1_000_000
		}
		dc := ytDashCodec(p[1], true)
		fam := dc
		if i := strings.IndexByte(dc, '.'); i > 0 {
			fam = dc[:i] // avc1 / vp09 / av01
		}
		if _, ok := groups[fam]; !ok {
			order = append(order, fam)
		}
		groups[fam] = append(groups[fam], vrep{h: h, bw: bw, codec: dc, init: p[3], index: p[4], url: p[5]})
	}
	if len(order) == 0 {
		http.Error(w, "no usable reps", http.StatusBadRequest)
		return
	}

	var sets strings.Builder
	for _, fam := range order {
		sets.WriteString("\n    <AdaptationSet mimeType=\"video/mp4\" contentType=\"video\" subsegmentAlignment=\"true\">")
		for i, r := range groups[fam] {
			fmt.Fprintf(&sets, `
      <Representation id="%s%d" bandwidth="%d" codecs="%s" width="%d" height="%d">
        <BaseURL>%s</BaseURL>%s
      </Representation>`, fam, i, r.bw, r.codec, r.h*16/9, r.h, xmlEscape(r.url), dashSegmentBase(r.index, r.init))
		}
		sets.WriteString("\n    </AdaptationSet>")
	}

	mpd := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011"
     profiles="urn:mpeg:dash:profile:isoff-on-demand:2011"
     type="static"
     minBufferTime="PT2S">
  <Period>%s
    <AdaptationSet mimeType="audio/mp4" contentType="audio" subsegmentAlignment="true">
      <Representation id="audio" bandwidth="%s" codecs="%s" audioSamplingRate="44100">
        <AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" value="2"/>
        <BaseURL>%s</BaseURL>%s
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`,
		sets.String(),
		abr, ytDashCodec(q.Get("acodec"), false), xmlEscape(audioURL), dashSegmentBase(q.Get("aix"), q.Get("air")),
	)

	w.Header().Set("Content-Type", "application/dash+xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(mpd)))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(mpd))
}

func dashSegmentBase(indexRange, initRange string) string {
	if !isByteRange(indexRange) || !isByteRange(initRange) {
		return ""
	}
	return "\n        <SegmentBase indexRange=\"" + indexRange + "\">" +
		"\n          <Initialization range=\"" + initRange + "\"/>" +
		"\n        </SegmentBase>"
}

// isByteRange reports whether s looks like "start-end" (decimal digits both sides).
func isByteRange(s string) bool {
	dash := strings.IndexByte(s, '-')
	if dash <= 0 || dash == len(s)-1 {
		return false
	}
	for i, c := range s {
		if i == dash {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ytDashCodec converts yt-dlp codec string to DASH codecs attribute.
func ytDashCodec(codec string, isVideo bool) string {
	if codec == "" || codec == "none" {
		if isVideo {
			return "avc1.640028"
		}
		return "mp4a.40.2"
	}
	// yt-dlp already gives codec strings in DASH-compatible format.
	return codec
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// handleMux serves HLS content for server-side muxed video+audio.
// ffmpeg generates HLS segments in a temp directory. This handler serves
// the m3u8 playlist and .ts segments.
//
// URL patterns:
//
//	/lite/youtube/mux?key=HASH           → serves playlist.m3u8
//	/lite/youtube/mux?key=HASH&seg=NAME  → serves a .ts segment
func (y *YoutubeChecker) HandleMux(w http.ResponseWriter, req *http.Request) {
	if y.ffmpegPath == "" {
		http.Error(w, "ffmpeg not available", http.StatusServiceUnavailable)
		return
	}

	key := strings.TrimSpace(req.URL.Query().Get("key"))
	seg := strings.TrimSpace(req.URL.Query().Get("seg"))
	isInitMP4 := strings.HasSuffix(req.URL.Path, "/mux/init.mp4")

	// Also detect segment request from URL path.
	if strings.Contains(req.URL.Path, "/mux/seg") && seg == "" {
		http.Error(w, "missing seg param", http.StatusBadRequest)
		return
	}

	if key == "" {
		http.Error(w, "missing key param", http.StatusBadRequest)
		return
	}

	entry := y.findOrStartMuxByKey(key)
	if entry == nil {
		// Diagnostic: a missing key on a checker with very few muxKeys (or a different ptr than the one
		// that started the mux) means a SECOND YoutubeChecker is serving this request — i.e. the
		// singleton isn't in effect (stale binary). `ptr` should be identical to "checker instance
		// created" at startup; `keys` should be >0 while anything is playing.
		y.muxMu.Lock()
		nkeys := len(y.muxKeys)
		y.muxMu.Unlock()
		log.Warn().Str("key", key).Str("ptr", fmt.Sprintf("%p", y)).Int("muxKeys", nkeys).
			Msg("youtube: mux job not found (key absent from this checker)")
		http.Error(w, "mux job not found — video may have expired, please reload", http.StatusNotFound)
		return
	}

	// For segment requests, wait for first segment to be ready.
	// For m3u8 playlist requests, also wait but with a shorter timeout.
	waitTimeout := 120 * time.Second // segments: up to 2 minutes
	if seg == "" {
		waitTimeout = 60 * time.Second // playlist: up to 60 seconds
	}
	select {
	case <-entry.Ready:
	case <-req.Context().Done():
		http.Error(w, "request cancelled", http.StatusRequestTimeout)
		return
	case <-time.After(waitTimeout):
		http.Error(w, "mux timeout waiting for ffmpeg", http.StatusGatewayTimeout)
		return
	}

	if entry.Err != nil {
		log.Warn().Err(entry.Err).Str("key", key).Msg("youtube: mux failed")
		http.Error(w, "mux failed: "+entry.Err.Error(), http.StatusInternalServerError)
		return
	}

	// Watching extends the lease. Expires is otherwise set ONCE when ffmpeg finishes, so a
	// video watched longer than ytMuxTTL (long video, or simply a pause) outlived its own mux
	// dir — the cleanup loop removed segments under the playing client → 404 flood → «через
	// какое-то время падает видео». Slide the TTL on every playlist/segment access instead;
	// the 30-min window now means 30 min of IDLE, not 30 min of total playback.
	y.muxMu.Lock()
	entry.Expires = time.Now().Add(ytMuxTTL)
	y.muxMu.Unlock()
	// Also freshen the dir mtime on access: external mtime-based tmp cleaners (find -mmin cron,
	// systemd-tmpfiles) killed completed dirs ~2 min after the last segment write. The cleanup
	// loop bumps every 2 min, but an aggressive `-mmin +1` can strike between cycles — an active
	// player hits this handler every few seconds, so this keeps the dir visibly "in use".
	if entry.Dir != "" {
		now := time.Now()
		_ = os.Chtimes(entry.Dir, now, now)
	}

	// Serve fMP4 init segment (VP9/AV1 HLS).
	if isInitMP4 {
		initPath := filepath.Join(entry.Dir, "init.mp4")
		// Wait for init.mp4 to appear (ffmpeg writes it early).
		for i := 0; i < 100; i++ { // up to 10 seconds
			if _, err := os.Stat(initPath); err == nil {
				break
			}
			select {
			case <-entry.Done:
				i = 100
			case <-req.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "max-age=3600")
		http.ServeFile(w, req, initPath)
		return
	}

	if seg != "" {
		// Serve a specific .ts segment file.
		// Sanitize: only allow alphanumeric, dash, dot.
		safeSeg := seg
		for _, c := range safeSeg {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_') {
				http.Error(w, "invalid segment name", http.StatusBadRequest)
				return
			}
		}
		segPath := filepath.Join(entry.Dir, safeSeg)
		// Wait until the segment file exists (ffmpeg may still be writing it).
		// Poll briefly since ffmpeg generates segments in real-time.
		// Stop waiting if ffmpeg finishes (segment won't be created).
		waited := time.Now()
		for i := 0; i < 300; i++ { // up to 30 seconds
			if _, err := os.Stat(segPath); err == nil {
				break
			}
			select {
			case <-entry.Done:
				// ffmpeg finished — if segment still doesn't exist, it won't appear.
				i = 300
			case <-req.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		// A missing segment here becomes a bare 404 with no server-side trace — and 404s on a
		// listed segment are exactly the «падает после N сегментов» class. Log the full picture:
		// which dir, was ffmpeg done, how long we polled, and how many segments DO exist.
		if _, err := os.Stat(segPath); err != nil {
			done := false
			select {
			case <-entry.Done:
				done = true
			default:
			}
			have := 0
			if names, derr := os.ReadDir(entry.Dir); derr == nil {
				for _, n := range names {
					if strings.HasPrefix(n.Name(), "seg") {
						have++
					}
				}
			} else {
				have = -1 // dir itself is gone
			}
			log.Warn().Str("key", key).Str("seg", safeSeg).Str("dir", entry.Dir).
				Bool("ffmpegDone", done).Int("segsOnDisk", have).
				Dur("waited", time.Since(waited)).AnErr("statErr", err).
				Msg("youtube: mux segment 404")
		}
		http.ServeFile(w, req, segPath)
		return
	}

	// Serve the m3u8 playlist.
	// We need to rewrite segment URLs to go through our handler.
	// Wait for ffmpeg to generate some segments.
	// The playlist is ready when entry.Ready is closed.
	host := hostFromRequest(req)
	baseURL := host + "/lite/youtube/mux/seg?key=" + key + "&seg="
	initURL := host + "/lite/youtube/mux/init.mp4?key=" + key

	y.serveRewrittenM3U8(w, req, entry, baseURL, initURL)
}

// serveRewrittenM3U8 reads the ffmpeg-generated m3u8, rewrites segment filenames
// registerMasterPlaylist stores the variant list for a videoID and returns
// the URL of the corresponding master playlist. Returns "" when fewer than
// two variants would land in the master (in which case the caller should fall
// back to a single-variant URL — no quality picker needed).
func (y *YoutubeChecker) registerMasterPlaylist(host, videoID string, qr ytQualityResult) string {
	if len(qr.MuxPairs) < 2 {
		return ""
	}

	variants := make([]ytMasterVariant, 0, len(qr.MuxPairs))
	for label, pair := range qr.MuxPairs {
		variantURL, ok := qr.Qualities[label]
		if !ok {
			continue
		}
		bw := pair.Bandwidth
		if bw <= 0 {
			// Fallback bandwidth heuristic from height when TBR was unknown.
			bw = pair.Height * pair.Height * 3
			if bw < 200_000 {
				bw = 200_000
			}
		}
		variants = append(variants, ytMasterVariant{
			Label:      label,
			Height:     pair.Height,
			Width:      pair.Width,
			Bandwidth:  bw,
			Codecs:     hlsCodecsString(pair.VCodec, pair.ACodec, pair.TransAudio),
			VariantURL: variantURL,
		})
	}
	if len(variants) < 2 {
		return ""
	}

	masterKey := fmt.Sprintf("%x", md5Hash("master:"+videoID))[:16]

	y.muxMu.Lock()
	y.masterCache[masterKey] = ytMasterEntry{
		Variants: variants,
		Expires:  time.Now().Add(ytMuxTTL),
	}
	// Drop expired master entries opportunistically.
	now := time.Now()
	for k, e := range y.masterCache {
		if now.After(e.Expires) {
			delete(y.masterCache, k)
		}
	}
	y.muxMu.Unlock()

	return host + "/lite/youtube/mux/master.m3u8?key=" + masterKey
}

// hlsCodecsString returns the CODECS attribute value for EXT-X-STREAM-INF.
// Output format follows RFC 6381 — yt-dlp already provides the right strings
// for video (avc1.xxxx, vp09.xx.xx.xx, av01.xx.xxM.xx); audio is normalised to
// "mp4a.40.2" when we know we'll re-encode the track to AAC for the HLS mux.
func hlsCodecsString(vcodec, acodec string, transcodedToAAC bool) string {
	vc := strings.TrimSpace(vcodec)
	if vc == "none" {
		vc = ""
	}
	ac := strings.TrimSpace(acodec)
	if ac == "none" {
		ac = ""
	}
	if transcodedToAAC {
		ac = "mp4a.40.2"
	} else if strings.HasPrefix(ac, "opus") {
		// Some players don't recognise plain "opus" in CODECS — leave it as is.
	}
	switch {
	case vc != "" && ac != "":
		return vc + "," + ac
	case vc != "":
		return vc
	case ac != "":
		return ac
	}
	return ""
}

// handleMuxMaster serves the master HLS playlist with EXT-X-STREAM-INF for
// each registered quality variant. hls.js reads it once and shows a level
// picker; switching levels triggers requests to the per-quality m3u8 handler.
func (y *YoutubeChecker) HandleMuxMaster(w http.ResponseWriter, req *http.Request) {
	key := strings.TrimSpace(req.URL.Query().Get("key"))
	if key == "" {
		http.Error(w, "missing key param", http.StatusBadRequest)
		return
	}

	y.muxMu.Lock()
	entry, ok := y.masterCache[key]
	y.muxMu.Unlock()
	if !ok || time.Now().After(entry.Expires) {
		http.Error(w, "master playlist not found — video may have expired, please reload", http.StatusNotFound)
		return
	}

	// Sort variants by height ascending so hls.js builds the level list in
	// the order players usually expect (low → high).
	sortedVariants := append([]ytMasterVariant(nil), entry.Variants...)
	sort.Slice(sortedVariants, func(i, j int) bool {
		return sortedVariants[i].Height < sortedVariants[j].Height
	})

	var sb strings.Builder
	sb.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, v := range sortedVariants {
		sb.WriteString("#EXT-X-STREAM-INF:BANDWIDTH=")
		sb.WriteString(strconv.Itoa(v.Bandwidth))
		if v.Width > 0 && v.Height > 0 {
			sb.WriteString(",RESOLUTION=")
			sb.WriteString(strconv.Itoa(v.Width))
			sb.WriteByte('x')
			sb.WriteString(strconv.Itoa(v.Height))
		}
		if v.Codecs != "" {
			sb.WriteString(`,CODECS="`)
			sb.WriteString(v.Codecs)
			sb.WriteString(`"`)
		}
		sb.WriteString(`,NAME="`)
		sb.WriteString(v.Label)
		sb.WriteString("\"\n")
		sb.WriteString(v.VariantURL)
		sb.WriteByte('\n')
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, sb.String())
}

// to go through our handler, and serves the result.
//
// Strategy: Serve the current playlist state immediately. While ffmpeg is still
// running, use EVENT type so hls.js re-fetches for new segments (live-like).
// Once ffmpeg finishes, switch to VOD with ENDLIST for seekable playback.
// This way the player starts playing within seconds, not minutes.
func (y *YoutubeChecker) serveRewrittenM3U8(w http.ResponseWriter, req *http.Request, entry *ytMuxEntry, baseURL, initURL string) {
	// Check if ffmpeg is done.
	ffmpegDone := false
	select {
	case <-entry.Done:
		ffmpegDone = true
	default:
	}

	// Read the playlist file.
	data, err := os.ReadFile(entry.M3U8)
	if err != nil || len(data) < 20 {
		// This 503 on a key that served segments moments ago means the mux was RESTARTED
		// (entry lost → fresh dir) or its dir vanished — log enough to tell which.
		log.Warn().Str("m3u8", entry.M3U8).Int("len", len(data)).Bool("ffmpegDone", ffmpegDone).
			AnErr("readErr", err).Msg("youtube: mux playlist not ready")
		// Self-heal: ffmpeg is done but its output is GONE (external tmp cleaner, manual rm).
		// Serving 503 until the lease expires bricks the key for up to 30 min — drop the dead
		// entry instead, so the NEXT request (players retry within seconds) restarts the mux
		// via the still-registered muxKeys mapping.
		if ffmpegDone && os.IsNotExist(err) {
			y.muxMu.Lock()
			for k, e := range y.muxCache {
				if e == entry {
					delete(y.muxCache, k)
					log.Warn().Str("dir", entry.Dir).Msg("youtube: mux output vanished — dropping entry for restart")
					break
				}
			}
			y.muxMu.Unlock()
		}
		http.Error(w, "playlist not ready", http.StatusServiceUnavailable)
		return
	}

	// Rewrite segment filenames to use our URL.
	lines := strings.Split(string(data), "\n")
	var sb strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Pin the start point to the beginning of the video.
		//
		// While ffmpeg is still muxing, the playlist is EVENT with no ENDLIST,
		// which every HLS player reads as "still growing" and therefore starts
		// at the LIVE EDGE — i.e. wherever the mux happens to have reached,
		// not at 00:00. That is the "new videos don't start from 0 / start at
		// a random point" bug: the offset was simply however far ahead ffmpeg
		// had muxed by the time the player attached. EXT-X-START makes the
		// intended start position explicit; it is honoured by hls.js,
		// ExoPlayer and AVPlayer, and is a no-op once the playlist is VOD.
		if trimmed == "#EXTM3U" {
			sb.WriteString("#EXTM3U\n")
			sb.WriteString("#EXT-X-START:TIME-OFFSET=0,PRECISE=YES\n")
			continue
		}
		// Skip ENDLIST while ffmpeg is still running — hls.js will re-fetch.
		if trimmed == "#EXT-X-ENDLIST" {
			if ffmpegDone {
				sb.WriteString(trimmed)
				sb.WriteString("\n")
			}
			continue
		}
		// Keep EVENT while in progress; switch to VOD when done.
		if trimmed == "#EXT-X-PLAYLIST-TYPE:EVENT" {
			if ffmpegDone {
				sb.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
			} else {
				sb.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
			}
			continue
		}
		// Rewrite #EXT-X-MAP:URI="init.mp4" for fMP4 init segment.
		if strings.HasPrefix(trimmed, "#EXT-X-MAP:") {
			sb.WriteString(`#EXT-X-MAP:URI="` + initURL + `"`)
			sb.WriteString("\n")
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			sb.WriteString(line)
			sb.WriteString("\n")
			continue
		}
		// This is a segment filename — rewrite to proxy URL.
		sb.WriteString(baseURL)
		sb.WriteString(url.QueryEscape(trimmed))
		sb.WriteString("\n")
	}
	// Add ENDLIST only when ffmpeg is fully done.
	if ffmpegDone {
		if !strings.Contains(sb.String(), "#EXT-X-ENDLIST") {
			sb.WriteString("#EXT-X-ENDLIST\n")
		}
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if ffmpegDone {
		w.Header().Set("Cache-Control", "max-age=3600")
	} else {
		// No cache — player must re-fetch to get new segments.
		w.Header().Set("Cache-Control", "no-cache, no-store")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(sb.String()))
}

// findOrStartMuxByKey finds an existing mux entry or starts a new one by hash key.
func (y *YoutubeChecker) findOrStartMuxByKey(key string) *ytMuxEntry {
	y.muxMu.Lock()

	// Check if cacheKey is registered for this hash.
	cacheKey, ok := y.muxKeys[key]
	if !ok {
		y.muxMu.Unlock()
		return nil
	}
	vcodec := y.muxVCodecs[key]
	needsProxy := y.muxProxy[key]
	transAudio := y.muxTransA[key]
	fmp4 := y.muxFMP4[key]
	videoID := y.muxVideoID[key]

	// Check if mux is already running/done.
	if entry, ok := y.muxCache[cacheKey]; ok {
		y.muxMu.Unlock()
		return entry
	}

	y.muxMu.Unlock()

	// Start mux on demand. Split cacheKey to get video+audio URLs.
	parts := strings.SplitN(cacheKey, "\x00", 2)
	if len(parts) != 2 {
		return nil
	}

	return y.getOrStartMux(cacheKey, parts[0], parts[1], vcodec, videoID, needsProxy, transAudio, fmp4)
}

// getOrStartMux returns an existing mux entry or starts a new ffmpeg HLS mux job.
func (y *YoutubeChecker) getOrStartMux(key, videoURL, audioURL, vcodec, videoID string, needsProxy, transAudio, fmp4 bool) *ytMuxEntry {
	y.muxMu.Lock()
	defer y.muxMu.Unlock()

	// Cleanup expired entries.
	now := time.Now()
	for k, e := range y.muxCache {
		select {
		case <-e.Done:
			if now.After(e.Expires) {
				removeMuxDir(e.Dir, "getmux-expired")
				delete(y.muxCache, k)
			}
		default:
			// still in progress — keep
		}
	}

	// Return existing (in-progress or completed).
	if entry, ok := y.muxCache[key]; ok {
		return entry
	}

	// Evict oldest if over limit.
	if len(y.muxCache) >= ytMuxMaxSize {
		for k, e := range y.muxCache {
			select {
			case <-e.Done:
				removeMuxDir(e.Dir, "eviction")
				delete(y.muxCache, k)
			default:
			}
			if len(y.muxCache) < ytMuxMaxSize {
				break
			}
		}
	}

	// Hard limit: refuse new mux jobs if cache is still full (all in-progress).
	// Prevents unbounded /tmp growth.
	if len(y.muxCache) >= ytMuxMaxSize {
		entry := &ytMuxEntry{
			Ready: make(chan struct{}),
			Done:  make(chan struct{}),
			Err:   fmt.Errorf("too many active mux jobs (%d), try later", len(y.muxCache)),
		}
		close(entry.Ready)
		close(entry.Done)
		log.Warn().Int("active", len(y.muxCache)).Msg("youtube: mux limit reached, rejecting new job")
		return entry
	}

	// Create temp directory for HLS output.
	tmpDir, err := os.MkdirTemp("", "yt-mux-*")
	if err != nil {
		entry := &ytMuxEntry{Ready: make(chan struct{}), Done: make(chan struct{}), Err: err}
		close(entry.Ready)
		close(entry.Done)
		return entry
	}

	m3u8Path := filepath.Join(tmpDir, "playlist.m3u8")

	entry := &ytMuxEntry{
		VideoID:    videoID,
		Dir:        tmpDir,
		M3U8:       m3u8Path,
		Ready:      make(chan struct{}),
		Done:       make(chan struct{}),
		NeedsProxy: needsProxy,
		TransA:     transAudio,
		FMP4:       fmp4,
	}
	y.muxCache[key] = entry

	// Start ffmpeg in background goroutine.
	go y.runMux(entry, videoURL, audioURL)

	return entry
}

// runMux executes ffmpeg to mux video+audio into HLS segments.
func (y *YoutubeChecker) runMux(entry *ytMuxEntry, videoURL, audioURL string) {
	defer close(entry.Done)

	logURL := func(s string) string {
		if len(s) > 100 {
			return s[:100] + "…"
		}
		return s
	}
	log.Info().
		Str("video", logURL(videoURL)).
		Str("audio", logURL(audioURL)).
		Str("dir", entry.Dir).
		Msg("youtube: starting HLS mux")

	start := time.Now()

	// YouTube CDN requires proper headers. ffmpeg -headers applies to all
	// subsequent inputs. Each header line must end with \r\n. UA must match
	// the client used for extraction (&c= param in URL) — mismatch → 403.
	ua, origin, referer := ytCDNHeaders(videoURL)
	hdrs := "User-Agent: " + ua + "\r\n" +
		"Origin: " + origin + "\r\n" +
		"Referer: " + referer + "\r\n"

	// fMP4 for VP9/AV1 (no transcoding needed), TS for H.264.
	segExt := ".ts"
	if entry.FMP4 {
		segExt = ".m4s"
	}
	segmentPath := filepath.Join(entry.Dir, "seg%05d"+segExt)

	// Build ffmpeg args. When videoURL == audioURL (combined format with both
	// video and audio), use a single input. Otherwise use separate inputs.
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
	}
	args = append(args,
		"-headers", hdrs,
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "5",
		"-i", videoURL,
	)
	if videoURL != audioURL {
		args = append(args,
			"-reconnect", "1",
			"-reconnect_streamed", "1",
			"-reconnect_delay_max", "5",
			"-i", audioURL,
			"-map", "0:v:0",
			"-map", "1:a:0",
		)
	}
	// Copy video stream. Audio may need AAC transcode for HLS/TS compatibility
	// when yt-dlp returns webm/opus audio-only tracks.
	// For fMP4 containers, opus audio can be muxed directly (no AAC transcode needed).
	if entry.TransA && videoURL != audioURL && !entry.FMP4 {
		args = append(args,
			"-c:v", "copy",
			"-c:a", "aac",
			"-b:a", "160k",
			"-ac", "2",
		)
	} else {
		args = append(args, "-c", "copy")
		// HLS source audio ships as AAC in ADTS; remuxing to another HLS output
		// requires aac_adtstoasc to strip ADTS headers. Apply whenever we're
		// copying audio from a separate HLS input.
		if videoURL != audioURL && strings.Contains(audioURL, "m3u8") {
			args = append(args, "-bsf:a", "aac_adtstoasc")
		}
	}
	args = append(args,
		"-f", "hls",
		"-hls_time", "4", // 4-second segments for fast startup
		"-hls_list_size", "0", // keep all segments in playlist
		"-hls_segment_filename", segmentPath,
		"-hls_flags", "independent_segments",
		"-hls_playlist_type", "event", // allow appending; handler adds ENDLIST when done
	)
	if entry.FMP4 {
		args = append(args, "-hls_segment_type", "fmp4")
	}
	args = append(args, entry.M3U8)
	ctx, cancel := context.WithTimeout(context.Background(), ytMuxMaxTimeout)
	defer cancel()

	// Route YouTube downloads through Go's native HTTP clients (pipe mode).
	//
	// Why pipe mode is ALWAYS used now (not only when proxy is configured):
	//
	// YouTube CDN signs stream URLs with the egress IP of the extractor:
	// "&ip=...&sparams=...,ip,...&sig=...". The CDN rejects (HTTP 403) any
	// request whose source IP differs from this signed `ip=`.
	//
	// yt-dlp is pinned to "-4" (see runExtract). That makes URLs signed for
	// the server's IPv4 egress. To keep the download side on the same family,
	// we route bytes through Go's HTTP clients — every direct + SOCKS5 client
	// in this codebase is hard-coded to "tcp4" (httpclient/utls.go:411,
	// antidpi/dial.go:20, antidpi/server.go:290). ffmpeg only does muxing,
	// no network I/O, so it cannot pick IPv6 by libc preference and 403.
	//
	// This avoids relying on `RES_OPTIONS=no-aaaa` (glibc 2.36+) or system-
	// wide `/etc/gai.conf` precedence rules, which don't work on Ubuntu 20.04
	// (glibc 2.31) and older.
	//
	// Side benefit: the racing strategies in pipeDownloadStream still help
	// when DPI is present (Russian VPS) or when a CDN node is slow.
	var cmd *exec.Cmd
	var pipeCleanup []func() // close write-ends after ffmpeg exits
	videoR, videoW, err1 := os.Pipe()
	audioR, audioW, err2 := os.Pipe()
	if err1 == nil && err2 == nil {
		// Build ffmpeg args with pipe inputs instead of URLs.
		// pipe:3 = first ExtraFile (video), pipe:4 = second ExtraFile (audio).
		pipeArgs := []string{
			"-hide_banner",
			"-loglevel", "error",
			"-y",
			"-i", "pipe:3",
		}
		if videoURL != audioURL {
			pipeArgs = append(pipeArgs, "-i", "pipe:4", "-map", "0:v:0", "-map", "1:a:0")
		}
		if entry.TransA && videoURL != audioURL && !entry.FMP4 {
			pipeArgs = append(pipeArgs, "-c:v", "copy", "-c:a", "aac", "-b:a", "160k", "-ac", "2")
		} else {
			pipeArgs = append(pipeArgs, "-c", "copy")
			if videoURL != audioURL && strings.Contains(audioURL, "m3u8") {
				pipeArgs = append(pipeArgs, "-bsf:a", "aac_adtstoasc")
			}
		}
		pipeArgs = append(pipeArgs,
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "0",
			"-hls_segment_filename", segmentPath,
			"-hls_flags", "independent_segments",
			"-hls_playlist_type", "event",
		)
		if entry.FMP4 {
			pipeArgs = append(pipeArgs, "-hls_segment_type", "fmp4")
		}
		pipeArgs = append(pipeArgs, entry.M3U8)

		cmd = exec.CommandContext(ctx, y.ffmpegPath, pipeArgs...)
		cmd.ExtraFiles = []*os.File{videoR, audioR}

		// Start goroutines that download YouTube streams via Go's tcp4 HTTP
		// clients (pipeDownloadStream races VLESS/direct/SOCKS5 strategies)
		// and write to the pipe write-ends. ffmpeg reads from pipe:3/pipe:4.
		go y.pipeDownloadStream(ctx, entry, videoURL, videoW, "video", y.remintFunc(entry.VideoID))
		if videoURL != audioURL {
			go y.pipeDownloadStream(ctx, entry, audioURL, audioW, "audio", y.remintFunc(entry.VideoID))
		} else {
			audioW.Close()
		}

		pipeCleanup = append(pipeCleanup, func() {
			videoR.Close()
			audioR.Close()
		})

		log.Info().
			Bool("hasProxy", y.curProxy() != "" || y.curVless() != "").
			Str("ffmpeg", y.ffmpegPath).
			Msg("youtube: running ffmpeg with Go pipe streaming (tcp4)")
	} else {
		// os.Pipe failed (FD exhaustion?). Close any opened ones and fall
		// through to direct ffmpeg URL download below.
		log.Warn().AnErr("err1", err1).AnErr("err2", err2).Msg("youtube: os.Pipe failed, falling back to direct ffmpeg")
		if videoR != nil {
			videoR.Close()
		}
		if videoW != nil {
			videoW.Close()
		}
		if audioR != nil {
			audioR.Close()
		}
		if audioW != nil {
			audioW.Close()
		}
	}
	if cmd == nil {
		// Pipe setup failed (rare — FD exhaustion). Last-resort direct ffmpeg
		// download. On dual-stack hosts where yt-dlp signed URL for IPv4 and
		// libc prefers IPv6, this WILL 403, but we have no better option here.
		cmd = exec.CommandContext(ctx, y.ffmpegPath, args...)
	}

	var stderr strings.Builder
	cmd.Stderr = &stderr

	// Monitor for first segment to signal Ready.
	go func() {
		firstSegExt := ".ts"
		if entry.FMP4 {
			firstSegExt = ".m4s"
		}
		firstSeg := filepath.Join(entry.Dir, "seg00000"+firstSegExt)
		for {
			select {
			case <-entry.Done:
				return
			case <-time.After(200 * time.Millisecond):
			}
			if info, err := os.Stat(firstSeg); err == nil && info.Size() > 0 {
				close(entry.Ready)
				return
			}
		}
	}()

	// Use Start+Wait instead of Run so we can close parent's pipe read-ends
	// immediately after ffmpeg inherits them. This ensures the pipe breaks
	// properly (goroutines get EPIPE) if ffmpeg exits while they're still writing.
	if err := cmd.Start(); err != nil {
		entry.Err = fmt.Errorf("ffmpeg start: %w", err)
		y.setEntryExpires(entry, 2*time.Minute) // clean up failed dir quickly
		log.Warn().Err(entry.Err).Msg("youtube: HLS mux — ffmpeg start failed")
		for _, fn := range pipeCleanup {
			fn()
		}
		// Signal Ready so waiting handler unblocks.
		select {
		case <-entry.Ready:
		default:
			close(entry.Ready)
		}
		return
	}

	// Close parent's copies of pipe read-ends now that ffmpeg has inherited them.
	// This ensures proper pipe semantics: when download goroutines close write-ends,
	// ffmpeg gets EOF. When ffmpeg exits, goroutines get broken pipe.
	for _, fn := range pipeCleanup {
		fn()
	}

	// Size guard: a livestream (or pathological video) muxed with -hls_list_size 0
	// appends segments unbounded until ytMuxMaxTimeout (14h) — prod saw a single
	// /tmp/yt-mux-* dir reach 28GB and fill the disk. Watch the dir and stop ffmpeg
	// (via cancel → CommandContext kill) once it crosses ytMuxMaxDirBytes; cmd.Wait
	// then takes the error path below → 2-minute quick cleanup. Exits when ffmpeg
	// finishes normally (the defer cancel above closes ctx).
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if muxDirSize(entry.Dir) > ytMuxMaxDirBytes {
					entry.truncated.Store(true)
					log.Warn().Str("dir", entry.Dir).Int64("cap_bytes", ytMuxMaxDirBytes).
						Msg("youtube: mux exceeded size cap — stopping (livestream or oversized video)")
					cancel()
					return
				}
			}
		}
	}()

	if err := cmd.Wait(); err != nil {
		entry.Err = fmt.Errorf("ffmpeg: %w: %s", err, stderr.String())
		y.setEntryExpires(entry, 2*time.Minute) // clean up failed dir quickly
		log.Warn().Err(entry.Err).Msg("youtube: HLS mux failed")
		// Signal Ready in case first segment never appeared.
		select {
		case <-entry.Ready:
		default:
			close(entry.Ready)
		}
		return
	}

	// Signal Ready in case it was never signaled (very short videos).
	select {
	case <-entry.Ready:
	default:
		close(entry.Ready)
	}

	// A dead download closes the pipe → ffmpeg sees EOF and exits 0 — a TRUNCATED mux that
	// looks complete. Don't let it masquerade as a finished VOD (the handler would add
	// ENDLIST at the cut point and the video would silently "end" mid-film): surface it as
	// an error and expire fast so a reload starts a fresh mux instead of replaying the stub.
	if entry.truncated.Load() {
		entry.Err = fmt.Errorf("mux truncated: stream download died mid-file (all routes failed)")
		y.setEntryExpires(entry, 2*time.Minute)
		log.Warn().Str("dir", entry.Dir).Msg("youtube: HLS mux truncated — marking failed")
		return
	}

	y.setEntryExpires(entry, ytMuxTTL)

	elapsed := time.Since(start)
	log.Info().
		Dur("elapsed", elapsed).
		Str("dir", entry.Dir).
		Msg("youtube: HLS mux done")
}

// setEntryExpires updates an entry's lease under muxMu — Expires is read by the cleanup
// loop and slid forward by the serving handler, so unsynchronized writes would race.
func (y *YoutubeChecker) setEntryExpires(entry *ytMuxEntry, ttl time.Duration) {
	y.muxMu.Lock()
	entry.Expires = time.Now().Add(ttl)
	y.muxMu.Unlock()
}

// streamAttempt is one racing route to the googlevideo CDN.
type streamAttempt struct {
	name   string
	client *http.Client
}

// ── YouTube proxy failover (explicit-list mode) ─────────────────────────────
// The configured non-RU vless exit dies unpredictably (a sidecar node goes down
// for the night); when it does, every extraction hard-fails and YouTube is dead
// until someone edits the config. With a candidate LIST we probe and switch to a
// live exit automatically. When no list is configured, ytProxyActive stays nil
// and cur*() return the static fields — non-failover modes behave exactly as before.

func (y *YoutubeChecker) setActiveProxy(addr string) {
	a := addr
	y.ytProxyActive.Store(&a)
	setYtImgProxyAddr(addr) // thumbnails ride the same exit
}

func (y *YoutubeChecker) curProxy() string {
	if p := y.ytProxyActive.Load(); p != nil && *p != "" {
		return *p
	}
	return y.proxyAddr
}

func (y *YoutubeChecker) curVless() string {
	if p := y.ytProxyActive.Load(); p != nil && *p != "" {
		return *p
	}
	return y.vlessProxyAddr
}

// proxyProbe returns true if `addr` (a SOCKS5 host:port) can reach YouTube. RU
// exits connect but time out on youtube.com (DPI), so a plain 204 is the signal.
func (y *YoutubeChecker) proxyProbe(addr string) bool {
	cl := httpclient.NewStreamClientViaSOCKS5(addr)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://www.youtube.com/generate_204", nil)
	if err != nil {
		return false
	}
	resp, err := cl.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 400
}

// pickLiveProxy returns the first candidate that reaches YouTube, or "".
func (y *YoutubeChecker) pickLiveProxy() string {
	for _, c := range y.ytProxyCandidates {
		if y.proxyProbe(c) {
			return c
		}
	}
	return ""
}

// clearStratCooldowns wipes the per-client extraction cooldowns — used when the
// exit changes: a fresh IP deserves a fresh try of every client.
func (y *YoutubeChecker) clearStratCooldowns() {
	y.mu.Lock()
	for k := range y.stratCooldown {
		delete(y.stratCooldown, k)
	}
	y.mu.Unlock()
}

// rotateProxyOnFailure switches to a DIFFERENT live candidate after repeated
// extraction failures. A shared WARP exit (104.28.x) passes the generate_204
// health ping but rate-limits YouTube under the service's real concurrent load,
// so ping-based health never rotates it — extraction health must. Runs async,
// one at a time.
func (y *YoutubeChecker) rotateProxyOnFailure() {
	if len(y.ytProxyCandidates) < 2 || !y.proxyRotating.CompareAndSwap(false, true) {
		return
	}
	defer y.proxyRotating.Store(false)
	cur := y.curProxy()
	// Кандидаты по кругу, начиная со СЛЕДУЮЩЕГО за текущим — чтобы не залипнуть на
	// одном флапающем WARP-выходе, а перебрать остальные.
	start := 0
	for i, c := range y.ytProxyCandidates {
		if c == cur {
			start = i + 1
			break
		}
	}
	n := len(y.ytProxyCandidates)
	for off := 0; off < n; off++ {
		c := y.ytProxyCandidates[(start+off)%n]
		if c == cur {
			continue
		}
		if y.proxyProbe(c) {
			y.setActiveProxy(c)
			y.clearStratCooldowns()
			log.Warn().Str("from", cur).Str("to", c).Msg("youtube: rotating proxy after extraction failures")
			return
		}
	}
}

// proxyHealthLoop watches the active exit and switches to a live candidate when
// it dies. Runs only when >1 candidate is configured.
func (y *YoutubeChecker) proxyHealthLoop() {
	fails := 0
	for {
		time.Sleep(2 * time.Minute)
		cur := y.curProxy()
		if cur != "" && y.proxyProbe(cur) {
			fails = 0
			continue
		}
		fails++
		if fails < 2 {
			continue // одно мимо — не дёргаемся (мог быть транзиентный сбой)
		}
		fails = 0
		if next := y.pickLiveProxy(); next != "" && next != cur {
			y.setActiveProxy(next)
			y.clearStratCooldowns() // свежий выход — пробуем все клиенты заново
			log.Warn().Str("from", cur).Str("to", next).Msg("youtube: proxy exit died — switched to live candidate")
		} else {
			log.Warn().Str("cur", cur).Msg("youtube: proxy exit down and no live candidate found")
		}
	}
}

// buildStreamAttempts returns the download routes raced by pipeDownloadStream
// and probeStreamURL. VLESS goes first (most reliable when DPI blocks
// everything else), then direct DPI-bypass strategies, then the AntiDPI
// SOCKS5 proxy.
func (y *YoutubeChecker) buildStreamAttempts() []streamAttempt {
	var attempts []streamAttempt

	// VLESS encrypted tunnel — bypasses DPI completely.
	if y.curVless() != "" && y.curVless() != y.curProxy() {
		attempts = append(attempts, streamAttempt{"vless/" + y.curVless(), httpclient.NewStreamClientViaSOCKS5(y.curVless())})
	}

	// Direct DPI bypass strategies (work without external proxy when DPI is
	// not too aggressive, or when zapret is installed at the kernel level).
	attempts = append(attempts,
		streamAttempt{"direct/split-tlsrec", httpclient.NewDirectSplitStreamClient(antidpi.StrategySplitTLSRec)},
		streamAttempt{"direct/split-tlsrec-40", httpclient.NewDirectSplitStreamClient(antidpi.StrategySplitTLSRec40)},
		streamAttempt{"direct/split-sni", httpclient.NewDirectSplitStreamClient(antidpi.StrategySplitSNI)},
		streamAttempt{"direct/no-sni", httpclient.NewDirectNoSNIStreamClient()},
		streamAttempt{"direct/split-1", httpclient.NewDirectSplitStreamClient(antidpi.StrategySplit1)},
	)

	// AntiDPI SOCKS5 proxy with server-wide strategy.
	if y.curProxy() != "" {
		attempts = append(attempts, streamAttempt{"socks5/" + y.curProxy(), httpclient.NewStreamClientViaSOCKS5(y.curProxy())})
	}
	return attempts
}

// probeStreamURL fetches the first KB of a googlevideo stream URL through the
// same route stack the mux downloader uses and reports whether any route
// returns a valid 206. A successful yt-dlp extraction no longer proves the
// URLs are playable: YouTube poisons whole client sessions at once while
// still returning a full-looking format list (2026-08: mweb URLs carry a
// bgutil PO Token the CDN rejects — even yt-dlp's own downloader gets 403 —
// and the tv client hits the DRM experiment). Without this check the
// strategy loop stops at the first "successful" extraction and every mux
// then dies on empty pipes with "Invalid data found when processing input".
// ytURLDesc summarizes a googlevideo URL's identifying params for logs.
func ytURLDesc(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "?"
	}
	q := u.Query()
	d := "c=" + q.Get("c") + " itag=" + q.Get("itag")
	if q.Get("pot") != "" {
		d += " pot"
	}
	return d
}

func (y *YoutubeChecker) probeStreamURL(streamURL string) (ok bool) {
	start := time.Now()
	defer func() {
		log.Debug().Bool("ok", ok).Str("stream", ytURLDesc(streamURL)).Dur("elapsed", time.Since(start)).Msg("youtube: stream URL probe")
	}()
	// Двухфазная проба. Первый килобайт ловит мёртвые URL/маршруты, но НЕ ловит
	// «тизерное окно» (2026-08-14: POT-less URL отдаёт первые ~20 МиБ и 403-ит
	// дальше — mux умирал на середине файла при зелёной пробе). Вторая фаза бьёт
	// за окно: 206 = полный доступ, 416 = файл короче офсета (тоже ок), 403 = яд.
	if !y.raceProbe(streamURL, "bytes=0-1023", false) {
		return false
	}
	// Вторая фаза (за тизерным окном) нужна ТОЛЬКО в POT-режиме через прокси, где окно
	// коварно мигает. В прямом POT-less режиме окно нормальное и лечится переминтом на
	// лету (свежая ссылка сбрасывает окно — проверено), поэтому упреждающе браковать URL
	// по офсету НЕЛЬЗЯ: это гнало живой android_vr в кулдаун → фолбэк в 360p.
	if y.fetchPotNever {
		return true
	}
	const teaserProbeOffset = 24 << 20
	return y.raceProbe(streamURL, fmt.Sprintf("bytes=%d-%d", teaserProbeOffset, teaserProbeOffset+1023), true)
}

// raceProbe races every download route for one ranged request. allow416 —
// для пробы за тизерным окном: Range Not Satisfiable значит лишь «файл короче»,
// это не отказ CDN.
func (y *YoutubeChecker) raceProbe(streamURL, rangeHdr string, allow416 bool) bool {
	attempts := y.buildStreamAttempts()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	resCh := make(chan bool, len(attempts))
	for _, a := range attempts {
		go func(a streamAttempt) {
			req, err := http.NewRequestWithContext(ctx, "GET", streamURL, nil)
			if err != nil {
				resCh <- false
				return
			}
			ua, origin, referer := ytCDNHeaders(streamURL)
			req.Header.Set("User-Agent", ua)
			req.Header.Set("Origin", origin)
			req.Header.Set("Referer", referer)
			req.Header.Set("Range", rangeHdr)
			resp, err := a.client.Do(req)
			if err != nil {
				resCh <- false
				return
			}
			defer resp.Body.Close()
			if allow416 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
				resCh <- true
				return
			}
			if resp.StatusCode != http.StatusPartialContent {
				resCh <- false
				return
			}
			buf := make([]byte, 1)
			n, _ := resp.Body.Read(buf)
			resCh <- n > 0
		}(a)
	}
	for range attempts {
		select {
		case ok := <-resCh:
			if ok {
				return true
			}
		case <-ctx.Done():
			return false
		}
	}
	return false
}

// pipeDownloadStream downloads a YouTube stream URL and writes the response body
// to the given os.File (pipe write-end). Closes w when done.
//
// All DPI bypass strategies are tried IN PARALLEL — the first successful
// connection wins and all others are cancelled. This reduces worst-case
// latency from N×timeout to just timeout.
//
// If no direct/AntiDPI strategy works, an encrypted tunnel (VLESS) is needed.
// Configure [proxy.vless] with "youtube" in balancers to enable it.
func (y *YoutubeChecker) pipeDownloadStream(ctx context.Context, entry *ytMuxEntry, streamURL string, w *os.File, label string, remint func(string) string) {
	defer w.Close()

	attempts := y.buildStreamAttempts()

	log.Info().
		Str("label", label).
		Int("strategies", len(attempts)).
		Msg("youtube: pipe download — racing all strategies in parallel")

	// Race all strategies in parallel — first success wins.
	type raceResult struct {
		resp    *http.Response
		idx     int
		name    string
		elapsed time.Duration
	}

	winCh := make(chan raceResult, 1)
	var failCount atomic.Int32

	// Per-attempt cancels with INDEPENDENT contexts (NOT a shared raceCtx).
	// The winner's request context must NOT be canceled until io.Copy of its
	// body has finished — otherwise the body read aborts instantly with
	// "context canceled" (0 bytes), which is exactly what the standard-library
	// no-SNI client does (its transport honors request-context cancellation,
	// unlike the uTLS transports). That left ffmpeg's pipe empty → "Invalid
	// data found when processing input" → mux 500 on non-DPI servers where the
	// no-SNI strategy wins the race. Losers are canceled explicitly below; the
	// winner only after the copy completes.
	var cancelMu sync.Mutex
	cancels := make([]context.CancelFunc, len(attempts))

	totalAttempts := int32(len(attempts))
	for i, a := range attempts {
		go func(i int, a streamAttempt) {
			reqCtx, reqCancel := context.WithCancel(ctx)
			cancelMu.Lock()
			cancels[i] = reqCancel
			cancelMu.Unlock()

			// Connect/TTFB guard: abort if no response headers arrive within
			// 20s. Stopped the moment headers arrive so it never cuts off a
			// long body download (4K can take minutes to stream).
			guard := time.AfterFunc(20*time.Second, reqCancel)

			req, err := http.NewRequestWithContext(reqCtx, "GET", streamURL, nil)
			if err != nil {
				guard.Stop()
				reqCancel()
				failCount.Add(1)
				return
			}
			ua, origin, referer := ytCDNHeaders(streamURL)
			req.Header.Set("User-Agent", ua)
			req.Header.Set("Origin", origin)
			req.Header.Set("Referer", referer)
			// Fetch only the first chunk while racing: picks a working route fast AND lets us learn the
			// total size from Content-Range, so we can continue in throttle-dodging ranged chunks below.
			req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", ytChunkSize-1))

			t0 := time.Now()
			resp, err := a.client.Do(req)
			guard.Stop()
			elapsed := time.Since(t0)

			if err != nil {
				errMsg := err.Error()
				if idx := strings.LastIndex(errMsg, "\": "); idx > 0 && idx < len(errMsg)-3 {
					errMsg = errMsg[idx+3:]
				}
				log.Debug().
					Str("error", errMsg).
					Str("label", label).
					Str("method", a.name).
					Dur("elapsed", elapsed).
					Msg("youtube: pipe download — strategy failed")
				reqCancel()
				failCount.Add(1)
				return
			}

			// Require 206: we always send Range, and googlevideo answers a valid stream with 206
			// (even files < the chunk size). A 200 (or anything else) is an error/non-video response —
			// e.g. a 403 page returned as 200 with a few KB of HTML. Accepting it fed garbage to ffmpeg
			// → "exit 183: Invalid data found / Error opening input pipe:3". Treat non-206 as a failed
			// strategy so the race tries another route (and fails cleanly if none work) instead.
			if resp.StatusCode != http.StatusPartialContent {
				resp.Body.Close()
				log.Debug().
					Int("status", resp.StatusCode).
					Str("label", label).
					Str("method", a.name).
					Dur("elapsed", elapsed).
					Msg("youtube: pipe download — bad status (want 206)")
				reqCancel()
				failCount.Add(1)
				return
			}

			// Try to be the winner. From here the winner's reqCancel is owned
			// by the main goroutine (called only after io.Copy finishes).
			select {
			case winCh <- raceResult{resp, i, a.name, elapsed}:
				// Won the race — do NOT cancel.
			default:
				// Someone else won — clean up.
				resp.Body.Close()
				reqCancel()
				failCount.Add(1)
			}
		}(i, a)
	}

	// Wait for either a winner or all failures.
	var winner *raceResult
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case r := <-winCh:
			winner = &r
		case <-ticker.C:
			if failCount.Load() >= totalAttempts {
				// All failed.
			}
		case <-ctx.Done():
		}
		if winner != nil || failCount.Load() >= totalAttempts || ctx.Err() != nil {
			break
		}
	}

	// Cancel every loser's request context now (frees their connections). The
	// winner is skipped — its body is about to be streamed.
	cancelMu.Lock()
	for i, c := range cancels {
		if c == nil || (winner != nil && i == winner.idx) {
			continue
		}
		c()
	}
	cancelMu.Unlock()

	if winner == nil {
		log.Warn().Str("label", label).Int("strategies", int(totalAttempts)).
			Msg("youtube: pipe download — all strategies failed (no working route to googlevideo.com CDN; on RU/DPI servers configure [proxy.vless] with youtube balancer or install zapret)")
		return
	}

	// Cancel the winner's context only after its body is fully copied.
	defer func() {
		cancelMu.Lock()
		if c := cancels[winner.idx]; c != nil {
			c()
		}
		cancelMu.Unlock()
	}()

	log.Info().
		Int64("content_length", winner.resp.ContentLength).
		Str("label", label).
		Str("method", winner.name).
		Dur("connect_time", winner.elapsed).
		Msg("youtube: pipe download started")

	// Copy the first chunk (the racing request asked for bytes=0-ytChunkSize-1).
	total := parseContentRangeTotal(winner.resp.Header.Get("Content-Range"))
	written, err := io.Copy(w, winner.resp.Body)
	winner.resp.Body.Close()

	// If the server honored the Range (206 + a known total), keep pulling the rest in fresh ranged
	// chunks on the winning route. Each chunk is a new short request, which sidesteps googlevideo's
	// single-connection throttling that otherwise starved ffmpeg mid-video. If Range was ignored
	// (200, total<0), the winner's body already held the whole file — nothing more to do.
	if err == nil && winner.resp.StatusCode == http.StatusPartialContent && total > 0 {
		// Route rotation: the race winner goes first, but if it dies mid-file (proxy flap,
		// googlevideo node gone) we move to the NEXT strategy for the remaining bytes instead
		// of giving up. Giving up closed the pipe → ffmpeg EOF'd "successfully" → truncated
		// video that either ended mid-film or, while ffmpeg still waited out per-chunk
		// timeouts, starved the player («сегмент один и через какое-то время падает»).
		clients := make([]*http.Client, 0, len(attempts))
		clients = append(clients, attempts[winner.idx].client)
		for i := range attempts {
			if i != winner.idx {
				clients = append(clients, attempts[i].client)
			}
		}
		ua, origin, referer := ytCDNHeaders(streamURL)
		ci := 0       // current route
		fails := 0    // consecutive failures on the current route
		cycles := 0   // full rotations through all routes with zero progress
		potTries := 0 // 403s on this offset before a re-mint (POT flap)
		reminted := 0 // how many times we re-minted the URL this download
		const potBeforeRemint = 2
		// Кап переминтов держим низким: каждый = отдельная yt-dlp-экстракция через
		// тот же прокси, а их всплеск ловит anti-bot 429 (и роняет ВСЕ клиенты в
		// 15-мин кулдаун → всё падает в 360p). 6 × окно ~25МиБ ≈ 150МиБ покрывает
		// подавляющее большинство роликов; сверх того — truncate, не бан.
		const maxReminted = 6
		for written < total && ctx.Err() == nil {
			start := written
			end := start + ytChunkSize - 1
			if end >= total {
				end = total - 1
			}
			cn, cerr := y.fetchChunk(ctx, clients[ci], streamURL, ua, origin, referer, start, end, w)
			written += cn
			if cn > 0 {
				fails, cycles, potTries = 0, 0, 0 // made progress → reset every retry budget
			}
			if cerr == nil {
				if cn == 0 {
					break // clean end of stream
				}
				continue
			}
			// ★2026-08-14: POT-less «грейс-окно» и mweb-POT URL отдают файл лишь до
			// какого-то офсета, дальше 403 УСТОЙЧИВО (не мигание секундами — ротация
			// маршрутов и ожидание не помогают: 403 от CDN, URL один). Лечение —
			// ПЕРЕМИНТ свежего URL того же itag: он качается с того же offset (байтовые
			// диапазоны идентичны). Пара 403 подряд → remint, продолжаем с `written`.
			if chunkStatus(cerr) == http.StatusForbidden {
				potTries++
				if potTries < potBeforeRemint {
					select {
					case <-ctx.Done():
					case <-time.After(400 * time.Millisecond):
					}
					continue
				}
				potTries = 0
				if remint == nil || reminted >= maxReminted {
					err = cerr
					break
				}
				// пауза перед переминтом — не долбить экстракцией в упор (anti-bot)
				select {
				case <-ctx.Done():
				case <-time.After(1500 * time.Millisecond):
				}
				fresh := remint(streamURL)
				reminted++
				if fresh == "" || fresh == streamURL {
					err = cerr
					break
				}
				log.Warn().Int64("bytes", written).Int64("total", total).Str("label", label).
					Int("remint", reminted).Msg("youtube: pipe download — 403 past window, re-minted fresh URL")
				streamURL = fresh
				ua, origin, referer = ytCDNHeaders(streamURL)
				continue
			}
			// Transport failure → retry the REMAINING bytes (from the updated `written`, so no
			// gap/overlap). After a few failures rotate to the next route; only when EVERY route has
			// failed a full budget twice do we give up — one googlevideo hiccup must not truncate the
			// file (a truncated/!206 input is exactly what made ffmpeg exit 183 "partial file").
			fails++
			if fails >= 3 {
				fails = 0
				ci = (ci + 1) % len(clients)
				if ci == 0 {
					cycles++
					if cycles >= 2 {
						err = cerr
						break
					}
				}
				log.Warn().Err(cerr).Int64("bytes", written).Int64("total", total).Str("label", label).
					Int("route", ci).Msg("youtube: pipe download — rotating route after chunk failures")
			}
			select {
			case <-ctx.Done():
			case <-time.After(300 * time.Millisecond):
			}
		}
	}

	if err != nil && ctx.Err() == nil {
		if total > 0 && written < total {
			entry.truncated.Store(true)
		}
		log.Warn().Err(err).Int64("bytes", written).Int64("total", total).Str("label", label).Msg("youtube: pipe download — chunk error")
	} else {
		if ctx.Err() == nil && total > 0 && written < total {
			entry.truncated.Store(true) // clean EOF before the advertised size is still a truncation
		}
		log.Info().Int64("bytes", written).Int64("total", total).Str("label", label).Msg("youtube: pipe download complete")
	}
}

// remintFunc returns a closure that re-extracts a FRESH stream URL for the same
// itag as `oldURL` — the fix for POT flaps that outlast the wait budget: the CDN
// permanently 403s a given (URL, offset past the grace window), but a freshly
// minted POT URL for the same itag downloads from where the old one died (byte
// ranges are identical across URLs of one itag). Empty videoID or itag → no-op.
func (y *YoutubeChecker) remintFunc(videoID string) func(oldURL string) string {
	return func(oldURL string) string {
		if videoID == "" {
			return ""
		}
		itag := ytURLItag(oldURL)
		if itag == "" {
			return ""
		}
		ytURL := "https://www.youtube.com/watch?v=" + videoID
		// POT-less режим (прямой маршрут): свежий android_vr URL сбрасывает окно —
		// берём только default. POT-режим (прокси): mweb с POT, default фолбэком.
		clients := []string{"mweb", ""}
		if y.fetchPotNever {
			clients = []string{""}
		}
		for _, client := range clients {
			fmts, _, ok := y.runExtract(videoID, ytURL, client, y.curProxy() != "")
			if !ok {
				continue
			}
			for _, f := range fmts {
				if f.URL != "" && ytURLItag(f.URL) == itag {
					return f.URL
				}
			}
		}
		return ""
	}
}

// ytURLItag pulls the itag query param off a googlevideo URL ("" if absent).
func ytURLItag(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Query().Get("itag")
}

// chunkHTTPError carries the CDN status so the download loop can tell a POT-flap
// 403 (wait it out — the URL is fine, the token is momentarily rejected) from a
// transport failure (rotate route).
type chunkHTTPError struct {
	status     int
	start, end int64
}

func (e *chunkHTTPError) Error() string {
	return fmt.Sprintf("chunk %d-%d: HTTP %d (want 206)", e.start, e.end, e.status)
}

func chunkStatus(err error) int {
	var ce *chunkHTTPError
	if errors.As(err, &ce) {
		return ce.status
	}
	return 0
}

// fetchChunk downloads a single byte range [start,end] of streamURL on the given client and writes it
// to w (the ffmpeg pipe). Returns bytes written. A per-chunk timeout guards against a stalled range.
func (y *YoutubeChecker) fetchChunk(ctx context.Context, client *http.Client, streamURL, ua, origin, referer string, start, end int64, w io.Writer) (int64, error) {
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", streamURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", referer)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// MUST be 206 with the exact range we asked for. A 200 means the server ignored Range and is
	// streaming the WHOLE file from byte 0 — appending that into the middle of the pipe corrupts the
	// stream (ffmpeg then hits "Invalid NAL unit size" at the splice). Reject anything but a matching
	// 206 so the caller retries instead of writing garbage.
	if resp.StatusCode != http.StatusPartialContent {
		return 0, &chunkHTTPError{status: resp.StatusCode, start: start, end: end}
	}
	if got := parseContentRangeStart(resp.Header.Get("Content-Range")); got >= 0 && got != start {
		return 0, fmt.Errorf("chunk %d-%d: server returned range at %d", start, end, got)
	}
	return io.Copy(w, resp.Body)
}

// parseContentRangeStart extracts START from a "bytes START-END/TOTAL" Content-Range header,
// or -1 when absent/unparseable.
func parseContentRangeStart(cr string) int64 {
	i := strings.IndexByte(cr, ' ')
	j := strings.IndexByte(cr, '-')
	if i < 0 || j < 0 || j <= i+1 {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(cr[i+1:j]), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// parseContentRangeTotal extracts TOTAL from a "bytes START-END/TOTAL" Content-Range header.
// Returns -1 when absent/unparseable/unknown ("*").
func parseContentRangeTotal(cr string) int64 {
	i := strings.LastIndexByte(cr, '/')
	if i < 0 || i == len(cr)-1 {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(cr[i+1:]), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// MuxStats returns the number of active (in-progress) mux jobs and total cache size.
func (y *YoutubeChecker) MuxStats() (activeJobs int, cacheSize int) {
	y.muxMu.Lock()
	defer y.muxMu.Unlock()
	cacheSize = len(y.muxCache)
	for _, e := range y.muxCache {
		select {
		case <-e.Done:
			// completed
		default:
			activeJobs++
		}
	}
	return
}

// muxCleanupLoop periodically removes expired mux entries and their temp dirs.
// Also sweeps orphaned /tmp/yt-mux-* directories that lost their cache entry
// (e.g. after a server restart).
func (y *YoutubeChecker) muxCleanupLoop() {
	// First cleanup after 1 minute, then every 2 minutes.
	time.Sleep(1 * time.Minute)
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for ; ; <-ticker.C {
		y.muxCleanupOnce()
	}
}

func (y *YoutubeChecker) muxCleanupOnce() {
	now := time.Now()
	var removed int

	// 1. Clean expired entries from muxCache.
	y.muxMu.Lock()
	knownDirs := make(map[string]bool, len(y.muxCache))
	for k, e := range y.muxCache {
		knownDirs[e.Dir] = true
		expired := false
		select {
		case <-e.Done:
			expired = now.After(e.Expires)
		default:
		}
		if expired {
			removeMuxDir(e.Dir, "cleanup-expired")
			delete(y.muxCache, k)
			// Clean up associated lookup maps.
			for hk, ck := range y.muxKeys {
				if ck == k {
					delete(y.muxKeys, hk)
					delete(y.muxVCodecs, hk)
					delete(y.muxProxy, hk)
					delete(y.muxTransA, hk)
				}
			}
			removed++
			continue
		}
		// Keep the temp dir's mtime fresh for EVERY live entry — in-progress AND completed-but-
		// leased. External mtime-based tmp cleaners (`find /tmp -name 'yt-mux-*' -mmin +N -delete`
		// cron, systemd-tmpfiles) judge by mtime; a COMPLETED mux stops writing, so ~N minutes after
		// the last segment the whole dir vanished under the playing client (prod 2026-07-02:
		// segsOnDisk=-1 ~100s after «HLS mux done» → 404 on every segment → playlist not ready →
		// «после N сегментов 404, потом 502/503»). The old bump covered only in-progress dirs — that
		// fixed the mid-write kill (ffmpeg exit 254 on seg00002.ts) but left every done dir exposed
		// for its whole 30-min lease.
		if e.Dir != "" {
			_ = os.Chtimes(e.Dir, now, now)
		}
	}
	y.muxMu.Unlock()

	// 2. Sweep orphaned /tmp/yt-mux-* dirs (left after restart or crash).
	entries, err := filepath.Glob(os.TempDir() + "/yt-mux-*")
	if err == nil {
		for _, dir := range entries {
			if knownDirs[dir] {
				continue // tracked by muxCache, skip
			}
			info, err := os.Stat(dir)
			if err != nil {
				continue
			}
			// Remove orphan dirs older than ytMuxTTL.
			if now.Sub(info.ModTime()) > ytMuxTTL {
				removeMuxDir(dir, "orphan-sweep")
				removed++
			}
		}
	}

	if removed > 0 {
		log.Info().Int("removed", removed).Msg("youtube: mux cleanup done")
	}
}

func (y *YoutubeChecker) cleanupCacheLocked() {
	if len(y.cache) < ytCacheMaxSize {
		return
	}
	now := time.Now()
	for k, v := range y.cache {
		if now.After(v.Expires) {
			delete(y.cache, k)
		}
	}
	// Still over limit — evict oldest
	if len(y.cache) >= ytCacheMaxSize {
		for k := range y.cache {
			delete(y.cache, k)
			if len(y.cache) < ytCacheMaxSize {
				break
			}
		}
	}
}

// ytQualitySortKeys returns quality labels sorted from best to worst.
func ytQualitySortKeys(quals map[string]string) []string {
	keys := make([]string, 0, len(quals))
	for k := range quals {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		hi, _ := strconv.Atoi(strings.TrimSuffix(keys[i], "p"))
		hj, _ := strconv.Atoi(strings.TrimSuffix(keys[j], "p"))
		return hi > hj
	})
	return keys
}

// --- YouTube Feed (category-based discovery) ---

// ytFeedCategory defines a category for the YouTube feed page.
type ytFeedCategory struct {
	Title string `json:"title"`
	Query string `json:"query"`
}

var ytFeedCategories = []ytFeedCategory{
	{Title: "Музыка", Query: "music hits 2026"},
	{Title: "Игры", Query: "gaming highlights 2026"},
	{Title: "Фильмы", Query: "фильм трейлер 2026"},
	{Title: "Новости", Query: "новости сегодня"},
	{Title: "Спорт", Query: "sport highlights 2026"},
	{Title: "Наука", Query: "science documentary 2026"},
	{Title: "Юмор", Query: "comedy funny 2026"},
}

const (
	// A feed category row shows a handful; an explicit search should feel like a search.
	ytFeedRowLimit    = 5
	ytFeedSearchLimit = 25
)

// ytHumanDuration formats seconds as h:mm:ss (or m:ss under an hour). The old m:ss form printed a
// 2h54m video as «174:05», which nobody parses at a glance.
func ytHumanDuration(sec float64) string {
	total := int(sec)
	if total <= 0 {
		return ""
	}
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// ytHumanViews renders a RU short view count («1,2 млн», «340 тыс.»).
func ytHumanViews(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1f млрд", float64(n)/1e9)
	case n >= 1_000_000:
		return strings.Replace(fmt.Sprintf("%.1f млн", float64(n)/1e6), ".", ",", 1)
	case n >= 1_000:
		return fmt.Sprintf("%d тыс.", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// ytHumanPublished renders a relative age («3 дня назад»). Flat-playlist search often omits the
// timestamps entirely — an empty string then keeps the field out of the card.
func ytHumanPublished(e ytEntry) string {
	ts := e.Timestamp
	if ts == 0 {
		ts = e.ReleaseTimestamp
	}
	if ts == 0 && len(e.UploadDate) == 8 {
		if t, err := time.Parse("20060102", e.UploadDate); err == nil {
			ts = t.Unix()
		}
	}
	if ts == 0 {
		return ""
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Hour:
		return "только что"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d ч назад", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d дн. назад", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%d мес. назад", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%d г. назад", int(d.Hours()/(24*365)))
	}
}

// ytFeedCache holds cached feed results.
type ytFeedCacheEntry struct {
	Data    []map[string]any
	Meta    map[string]any // channel header meta for "ch:" keys, nil otherwise
	Expires time.Time
}

var (
	ytFeedMu    sync.RWMutex
	ytFeedStore = make(map[string]*ytFeedCacheEntry) // query → cached results
)

const ytFeedCacheTTL = 30 * time.Minute

// handleFeed returns categorized YouTube video rows for the feed page.
// GET /lite/youtube/feed?category=music (or empty = all categories)
func (y *YoutubeChecker) HandleFeed(w http.ResponseWriter, req *http.Request) {
	if y.ensureYtdlp() == "" {
		writeJSON(w, http.StatusOK, map[string]any{"categories": []any{}})
		return
	}

	host := hostFromRequest(req)
	requestedCat := strings.TrimSpace(req.URL.Query().Get("category"))
	searchQuery := strings.TrimSpace(req.URL.Query().Get("search"))

	// Shorts: an explicit ?shorts= (a client's dedicated Shorts section) or the account setting.
	shortsMode := y.shortsModeForRequest(req)

	// Search mode: user typed a query in the feed search bar.
	if searchQuery != "" {
		results := y.feedSearch(searchQuery, host, ytFeedSearchLimit)
		if shortsMode != "" && shortsMode != "all" {
			y.annotateShorts(req.Context(), results)
			results = filterShorts(results, shortsMode)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"categories": []map[string]any{
				{
					"title":   "Результаты: " + searchQuery,
					"results": results,
				},
			},
		})
		return
	}

	// NB: subscriptions are deliberately NOT part of this feed.
	//
	// This used to prepend a "Подписки" row, which cost the home screen dearly:
	// building it is one API call per subscribed channel (~52 quota units for
	// 50 subs), and it ran INSIDE this request, so every home open blocked on
	// it. Subscriptions now live on their own screen, served by
	// /lite/youtube/feed/subscriptions — home is recommendations only.

	// Category mode: return rows per category.
	categories := ytFeedCategories
	if requestedCat != "" {
		for _, c := range ytFeedCategories {
			if strings.EqualFold(c.Title, requestedCat) ||
				strings.EqualFold(c.Query, requestedCat) {
				categories = []ytFeedCategory{c}
				break
			}
		}
	}

	// Fetch categories with limited concurrency (max 2 parallel yt-dlp).
	type catResult struct {
		Index   int
		Title   string
		Results []map[string]any
	}

	const maxParallel = 2
	sem := make(chan struct{}, maxParallel)
	ch := make(chan catResult, len(categories))
	for i, cat := range categories {
		go func(idx int, c ytFeedCategory) {
			sem <- struct{}{} // acquire
			results := y.feedSearch(c.Query, host, ytFeedRowLimit)
			<-sem // release
			// Filter AFTER the shared cache: the cached row stays the unfiltered truth, so two
			// users with opposite Shorts settings can't serve each other a pre-filtered row.
			if shortsMode != "" && shortsMode != "all" {
				y.annotateShorts(req.Context(), results)
				results = filterShorts(results, shortsMode)
			}
			ch <- catResult{Index: idx, Title: c.Title, Results: results}
		}(i, cat)
	}

	catResults := make([]catResult, 0, len(categories))
	for range categories {
		catResults = append(catResults, <-ch)
	}
	// Sort by original order.
	sort.Slice(catResults, func(i, j int) bool {
		return catResults[i].Index < catResults[j].Index
	})

	out := make([]map[string]any, 0, len(catResults))
	for _, cr := range catResults {
		if len(cr.Results) == 0 {
			continue
		}
		out = append(out, map[string]any{
			"title":   cr.Title,
			"results": cr.Results,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"categories": out})
}

// feedSearch runs ytsearch for a query and returns feed-formatted results.
// limit: a feed CATEGORY row only needs a handful, but an explicit user search returning 5 hits
// looks broken — callers pass what they need. NB: the limit is part of the cache key, otherwise a
// cached 5-item category row would be served to a search (and vice versa).
func (y *YoutubeChecker) feedSearch(query, host string, limit int) []map[string]any {
	if limit <= 0 {
		limit = ytFeedRowLimit
	}
	// Check cache.
	cacheKey := fmt.Sprintf("%d|%s|%s", limit, host, strings.ToLower(strings.TrimSpace(query)))
	ytFeedMu.RLock()
	cached, ok := ytFeedStore[cacheKey]
	ytFeedMu.RUnlock()
	if ok && time.Now().Before(cached.Expires) {
		return cached.Data
	}

	entries, err := y.ytSearchN(query, limit)
	if err != nil {
		log.Warn().Err(err).Str("query", query).Msg("youtube feed: search failed")
		return nil
	}

	results := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if entry.ID == "" {
			continue
		}
		channel := entry.Channel
		if channel == "" {
			channel = entry.Uploader
		}
		card := map[string]any{
			"title":    entry.Title,
			"img":      ytImgProxyURL(host, entry.ID),
			"url":      host + "/lite/youtube?videoID=" + entry.ID + "&title=" + url.QueryEscape(entry.Title),
			"video_id": entry.ID,
			"duration": ytHumanDuration(entry.Duration),
			"channel":  channel,
		}
		if entry.ChannelID != "" {
			card["channel_id"] = entry.ChannelID
		}
		if entry.ViewCount > 0 {
			card["views"] = ytHumanViews(entry.ViewCount)
		}
		if p := ytHumanPublished(entry); p != "" {
			card["published"] = p
		}
		results = append(results, card)
	}

	// Cache results.
	ytFeedMu.Lock()
	// Simple eviction: if too many entries, clear all.
	if len(ytFeedStore) > 50 {
		now := time.Now()
		for k, v := range ytFeedStore {
			if now.After(v.Expires) {
				delete(ytFeedStore, k)
			}
		}
	}
	ytFeedStore[cacheKey] = &ytFeedCacheEntry{
		Data:    results,
		Expires: time.Now().Add(ytFeedCacheTTL),
	}
	ytFeedMu.Unlock()

	return results
}

// runWithTimeout executes a command with a hard timeout and returns stdout.
func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) ([]byte, error) {
	done := make(chan struct{})
	var out []byte
	var cmdErr error

	go func() {
		out, cmdErr = cmd.Output()
		close(done)
	}()

	select {
	case <-done:
		return out, cmdErr
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, fmt.Errorf("timeout after %s", timeout)
	}
}

// runWithTimeoutNoOutput executes a command with a hard timeout.
// Unlike runWithTimeout, it uses cmd.Run() instead of cmd.Output(),
// so the caller must set cmd.Stdout/cmd.Stderr beforehand.
func runWithTimeoutNoOutput(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan struct{})
	var cmdErr error

	go func() {
		cmdErr = cmd.Run()
		close(done)
	}()

	select {
	case <-done:
		return cmdErr
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return fmt.Errorf("timeout after %s", timeout)
	}
}

// ---------------------------------------------------------------------------
//  YouTube OAuth — personalized feed handlers
// ---------------------------------------------------------------------------

// tgIDFromRequest resolves the Lampa auth token to a Telegram user ID.
func (y *YoutubeChecker) tgIDFromRequest(r *http.Request) int64 {
	store := y.tgStore()
	if store == nil {
		return 0
	}
	token := r.URL.Query().Get("token")
	// Fallback: resolve uid → token via device UID mapping.
	if token == "" {
		uid := r.URL.Query().Get("uid")
		if uid != "" {
			token = store.FindTokenByDeviceUID(uid)
		}
	}
	// Cookie fallback: the ALPAC web client authenticates by cookie (same model as /capi), not via
	// a ?token= query param — without this the feed can't identify the user and "Подписки" never shows.
	if token == "" {
		for _, name := range []string{"alpac_token", "lampac_token", "_lampac_auth"} {
			if c, err := r.Cookie(name); err == nil && strings.TrimSpace(c.Value) != "" {
				token = strings.TrimSpace(c.Value)
				break
			}
		}
	}
	if token == "" {
		return 0
	}
	at, ok := store.Lookup(token)
	if !ok {
		return 0
	}
	return at.TelegramID
}

// handleFeedSubscriptions returns recent videos from the user's YouTube subscriptions.
// GET /lite/youtube/feed/subscriptions?token=xxx
func (y *YoutubeChecker) HandleFeedSubscriptions(w http.ResponseWriter, r *http.Request) {
	api := y.ytAPI()
	if api == nil {
		writeJSON(w, http.StatusOK, map[string]any{"categories": []any{}})
		return
	}

	tgID := y.tgIDFromRequest(r)
	if tgID == 0 || !api.IsLinked(tgID) {
		writeJSON(w, http.StatusOK, map[string]any{
			"categories": []any{},
			"msg":        "YouTube не подключён. Используйте /youtube_auth в боте.",
		})
		return
	}

	host := hostFromRequest(r)
	videos, err := api.GetSubscriptionsFeed(r.Context(), tgID)
	if err != nil {
		log.Warn().Err(err).Int64("tg_id", tgID).Msg("youtube: subscriptions feed error")
		writeJSON(w, http.StatusOK, map[string]any{
			"categories": []any{},
			"msg":        "Ошибка: " + err.Error(),
		})
		return
	}

	// Return flat chronological feed (already sorted newest-first by API).
	results := ytVideosToCards(videos, host)
	if mode := y.shortsModeForRequest(r); mode != "" && mode != "all" {
		y.annotateShorts(r.Context(), results)
		results = filterShorts(results, mode)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
	})
}

// handleFeedPlaylists returns the user's YouTube playlists.
// GET /lite/youtube/feed/playlists?token=xxx
func (y *YoutubeChecker) HandleFeedPlaylists(w http.ResponseWriter, r *http.Request) {
	api := y.ytAPI()
	if api == nil {
		writeJSON(w, http.StatusOK, map[string]any{"results": []any{}})
		return
	}

	tgID := y.tgIDFromRequest(r)
	if tgID == 0 || !api.IsLinked(tgID) {
		writeJSON(w, http.StatusOK, map[string]any{
			"results": []any{},
			"msg":     "YouTube не подключён.",
		})
		return
	}

	host := hostFromRequest(r)
	playlists, err := api.GetPlaylists(r.Context(), tgID)
	if err != nil {
		log.Warn().Err(err).Int64("tg_id", tgID).Msg("youtube: playlists error")
		writeJSON(w, http.StatusOK, map[string]any{"results": []any{}})
		return
	}

	results := make([]map[string]any, 0, len(playlists))
	for _, pl := range playlists {
		// Channel/playlist avatars have no video id → proxy via the ?url= path (don't leak the raw
		// ggpht/googleusercontent URL to the client).
		img := ytImgProxyMaybe(host, pl.Thumbnail)
		if img == "" {
			img = host + "/lite/youtube/img?id=default"
		}
		results = append(results, map[string]any{
			"title":      pl.Title,
			"img":        img,
			"url":        host + "/lite/youtube/feed/playlist?playlist_id=" + url.QueryEscape(pl.PlaylistID) + "&title=" + url.QueryEscape(pl.Title),
			"item_count": pl.ItemCount,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// handleFeedPlaylist returns videos from a specific user playlist.
// GET /lite/youtube/feed/playlist?token=xxx&playlist_id=PLxxx
func (y *YoutubeChecker) HandleFeedPlaylist(w http.ResponseWriter, r *http.Request) {
	api := y.ytAPI()
	if api == nil {
		writeJSON(w, http.StatusOK, map[string]any{"categories": []any{}})
		return
	}

	tgID := y.tgIDFromRequest(r)
	playlistID := strings.TrimSpace(r.URL.Query().Get("playlist_id"))

	if tgID == 0 || !api.IsLinked(tgID) || playlistID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"categories": []any{}})
		return
	}

	host := hostFromRequest(r)
	videos, err := api.GetPlaylistItems(r.Context(), tgID, playlistID)
	if err != nil {
		log.Warn().Err(err).Str("playlist", playlistID).Msg("youtube: playlist items error")
		writeJSON(w, http.StatusOK, map[string]any{"categories": []any{}})
		return
	}

	title := r.URL.Query().Get("title")
	if title == "" {
		title = "Плейлист"
	}

	results := ytVideosToCards(videos, host)
	writeJSON(w, http.StatusOK, map[string]any{
		"categories": []map[string]any{
			{"title": title, "results": results},
		},
	})
}

// ytVideoToCard converts a single YTVideo to a Lampa card map, or nil if empty.
func ytVideoToCard(v ytauth.YTVideo, host string) map[string]any {
	if v.VideoID == "" {
		return nil
	}
	// Always proxy through our server (never emit the raw googleusercontent/ytimg URL from yt-dlp) so
	// subscription/playlist thumbnails load for clients with blocked/slow YouTube, same as feed/search.
	img := ytImgProxyURL(host, v.VideoID)
	card := map[string]any{
		"title":    v.Title,
		"img":      img,
		"url":      host + "/lite/youtube?videoID=" + v.VideoID + "&title=" + url.QueryEscape(v.Title),
		"video_id": v.VideoID,
		"duration": v.Duration,
		"channel":  v.Channel,
	}
	if v.ChannelID != "" {
		card["channel_id"] = v.ChannelID
	}
	return card
}

// ytVideosToCards converts ytauth.YTVideo slice to Lampa-compatible card maps.
func ytVideosToCards(videos []ytauth.YTVideo, host string) []map[string]any {
	cards := make([]map[string]any, 0, len(videos))
	for _, v := range videos {
		card := ytVideoToCard(v, host)
		if card != nil {
			cards = append(cards, card)
		}
	}
	return cards
}

// ---------------------------------------------------------------------------
// Process-wide checker singleton (published from liteSourceHandler; read by
// admin stats + server bootstrap). Moved from httpapi/admin_stats.go with the
// checker it wraps. The inner struct is effectively immutable once published.
// ---------------------------------------------------------------------------

var globalYTChecker atomic.Pointer[YoutubeChecker]

// GetGlobalYTChecker returns the current checker or nil.
func GetGlobalYTChecker() *YoutubeChecker { return globalYTChecker.Load() }

// SetGlobalYTChecker atomically publishes the checker process-wide.
func SetGlobalYTChecker(yt *YoutubeChecker) { globalYTChecker.Store(yt) }

// StatsInfo builds the admin yt-dlp status payload, or nil when yt-dlp is
// unavailable (caller shows available:false). Internalises the field reads
// + version probe that used to live in httpapi/admin_stats.go.
func (y *YoutubeChecker) StatsInfo() map[string]any {
	if y == nil || y.ytdlpPath == "" {
		return nil
	}
	info := map[string]any{
		"available":  true,
		"path":       y.ytdlpPath,
		"js_runtime": y.jsRuntime,
		"cookies":    y.cookiePath != "",
		"ffmpeg":     y.ffmpegPath,
	}
	if ver := getYtdlpVersion(y.ytdlpPath); ver != "" {
		info["version"] = ver
	}
	activeJobs, cacheSize := y.MuxStats()
	info["active_mux_jobs"] = activeJobs
	info["mux_cache_size"] = cacheSize
	return info
}

// ytdlpVersionCache caches the yt-dlp version string.
var (
	ytdlpVersionStr    string
	ytdlpVersionPath   string
	ytdlpVersionExpiry time.Time
)

// getYtdlpVersion returns the yt-dlp version, cached for 1 hour.
func getYtdlpVersion(path string) string {
	if path == "" {
		return ""
	}
	if path == ytdlpVersionPath && time.Now().Before(ytdlpVersionExpiry) {
		return ytdlpVersionStr
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return ""
	}
	ytdlpVersionStr = strings.TrimSpace(string(out))
	ytdlpVersionPath = path
	ytdlpVersionExpiry = time.Now().Add(1 * time.Hour)
	return ytdlpVersionStr
}
