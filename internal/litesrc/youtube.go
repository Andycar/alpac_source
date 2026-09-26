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
	cookiePath     string  // master cookies.txt — read-only for us, never handed to yt-dlp
	cookieWorkPath string  // disposable copy actually passed to --cookies (see ensureCookieWork)
	jsRuntime      string  // "node", "deno", or "" (auto)
	pluginDirs     string  // comma-separated dirs for yt-dlp --plugin-dirs (pip plugin locations)
	proxyAddr      string  // SOCKS5 proxy address for yt-dlp extraction (AntiDPI or VLESS)
	vlessProxyAddr string  // VLESS SOCKS5 proxy for CDN stream fallback (encrypted tunnel bypasses DPI)
	fetchPotNever  bool    // [youtube] fetch_pot="never" — запретить bgutil PO Token (см. config)
	hlsMinDuration float64 // [youtube] hls_min_duration — с какой длительности отдавать HLS вместо склейки; <0 — никогда
	// potReady — POT-провайдер реально доступен yt-dlp: найден через pip
	// (pluginDirs) ЛИБО лежит в каталоге, куда yt-dlp смотрит сам. Раньше эту
	// роль играл непустой pluginDirs, из-за чего глобально установленный
	// провайдер оставался невостребованным.
	potReady bool
	// sabrURL — база sabr-сервиса ([youtube] sabr_url, обычно http://127.0.0.1:4417).
	// Пусто = выключено, работает только прежний путь через googlevideo-ссылки.
	sabrURL string
	// parChunks — сколько диапазонов качать одновременно ([youtube] parallel_chunks).
	parChunks int
	// maxAutoHeight — потолок КАЧЕСТВА ПО УМОЛЧАНИЮ ([youtube] max_auto_height).
	maxAutoHeight int
	// SponsorBlock: пропуск спонсорских вставок ([youtube] sponsorblock*).
	sbEnable     bool
	sbCategories []string
	// Авто-фейловер YouTube-выхода: [youtube] proxy = "a:1,b:2,c:3" — список SOCKS5
	// кандидатов. ytProxyActive хранит текущий живой (atomic — читается на hot-path
	// экстракции/скачивания), health-горутина пере-выбирает его, когда выход умирает
	// (инциденты 14-15.08: не-RU vless-узел :40002/:40010 отваливался за ночь → все
	// стратегии в hard-cooldown, YouTube мёртв до ручной правки конфига).
	ytProxyCandidates []string
	ytProxyActive     atomic.Pointer[string]
	extractFail       atomic.Int32 // подряд «all strategies failed» → триггер ротации выхода
	// flatClientIdx — индекс в ytFlatClients для поиска/трендов/плейлистов.
	// Липкий: сломанный клиент стоит одного провального вызова на процесс.
	flatClientIdx atomic.Int32
	proxyRotating atomic.Bool // один rotate за раз
	repoRoot      string
	ytdlpMu       sync.Mutex
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
	// muxKeyAt records when each hash key was registered. The registry deliberately OUTLIVES the
	// mux directory so an expired mux can be restarted instead of 404ing (see muxCleanupOnce), so
	// it needs its own, much longer bound — that is what pruneMuxKeys uses.
	muxKeyAt   map[string]time.Time
	muxVCodecs map[string]string // hash key → video codec (avc1, vp09, av01)
	muxProxy   map[string]bool   // hash key → true if URL extracted via proxy (ffmpeg needs proxy too)
	muxTransA  map[string]bool   // hash key → true if audio must be transcoded to AAC for HLS/TS
	muxFMP4    map[string]bool   // hash key → true for VP9/AV1 (fMP4 segments)

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
	// attempts counts how many times this key has been (re)muxed after a failure, and failedAt
	// records when the last attempt died — together they bound the retry.
	attempts int
	failedAt time.Time
}

// finished reports whether the mux job has stopped running.
func (e *ytMuxEntry) finished() bool {
	select {
	case <-e.Done:
		return true
	default:
		return false
	}
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
	// AudioURL — отдельная аудиодорожка для варианта БЕЗ звука (режим HLS-only:
	// YouTube отдаёт там video-only плейлист, а звук — вторым плейлистом). Мастер
	// связывает их группой EXT-X-MEDIA, и плеер получает одну ссылку с обеими
	// дорожками. Отдавать звук отдельным полем ответа оказалось недостаточно —
	// клиент его не подхватывал, зритель смотрел немое видео.
	AudioURL string
	// VideoRange — «PQ»/«HLG» для HDR-вариантов, пусто или «SDR» для обычных.
	VideoRange string
}

const (
	ytMuxTTL = 30 * time.Minute // reduced from 4h to limit /tmp disk usage
	// ytMuxMaxSize — потолок ОДНОВРЕМЕННЫХ mux-задач. Упирается не в процессор,
	// а в /tmp: одна задача с 4K-видео весит до 2.5 ГБ, так что 14 задач это уже
	// ~35 ГБ в худшем случае. Поднято с 10 после 86 отказов «mux limit reached»
	// за сутки; выше не идём, пока лимит не станет считаться по объёму, а не по
	// числу задач.
	ytMuxMaxSize     = 14
	ytMuxMaxTimeout  = 14 * time.Hour // kill ffmpeg if it runs longer than this (allows 12h+ videos)
	ytMaxDuration    = 43200          // refuse to mux videos longer than 12 hours (seconds)
	ytHLSMinDuration = 90.0           // с этой длительности (сек) длинный ролик идёт HLS-лестницей, не склейкой
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
	UsedProxy bool      // true if formats were extracted via SOCKS5 proxy
	Born      time.Time // когда запись создана — по ней ограничиваем частоту refresh
}

// hlsOnlyFormats — по этому набору форматов buildQualityMap отдаст зрителю ГОЛЫЙ
// HLS, то есть ссылки на manifest.googlevideo.com. Условие повторяет ДВА решения
// buildQualityMap, и оба обязательны:
//
//  1. хоть один АУДИО-формат пришёл по m3u8 (клиент iOS/tv) — тогда там же, выше,
//     из списка выбрасываются ВСЕ https-форматы разом, и прогрессивное видео в
//     ответе уже не спасает;
//  2. среди видео не осталось ни одного прогрессивного.
//
// Считать признак по всему списку («есть хоть один https») было бы ошибкой: у
// ролика из инцидента 01.09 прогрессивные форматы В СПИСКЕ БЫЛИ, но одно HLS-аудио
// увело выдачу в HLS-режим — проверка не сработала бы ни разу.
func hlsOnlyFormats(formats []ytFormat) bool {
	for _, f := range formats {
		if f.isAudioOnly() && f.Protocol == "m3u8_native" && f.URL != "" {
			return true
		}
	}
	for _, f := range formats {
		if f.Protocol == "https" && f.URL != "" && !f.isAudioOnly() {
			return false
		}
	}
	return true
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
	// DynamicRange — «SDR», «HDR10», «HLG» и т.п. из yt-dlp. YouTube отдаёт HDR
	// только в VP9 profile 2 и AV1 с 10 битами; avc1 всегда SDR.
	DynamicRange string `json:"dynamic_range"`
	// DASH byte ranges (yt-dlp adaptive YouTube formats) — needed to build a valid on-demand
	// MPD SegmentBase so MSE players can index the fragmented MP4. Absent on progressive formats.
	IndexRange *ytByteRange `json:"index_range"`
	InitRange  *ytByteRange `json:"init_range"`

	// Multi-language audio. YouTube ships dubbed tracks alongside the original,
	// and yt-dlp exposes each as its own audio-only format. Measured on prod
	// 2026-09-01, one MrBeast video carried 44 distinct tracks:
	//   id=140-18 lang=ru pref=-1 note="Russian, medium"
	//   id=140-21 lang=en pref=10 note="English original (default), medium"
	// The original is the one yt-dlp scores at language_preference 10 and labels
	// "original (default)"; every dub sits at -1.
	Language           string `json:"language"`
	LanguagePreference int    `json:"language_preference"`
	FormatNote         string `json:"format_note"`
}

// isOriginalAudio reports whether this is the track the uploader actually
// recorded, as opposed to one of YouTube's dubs.
func (f ytFormat) isOriginalAudio() bool {
	if f.LanguagePreference >= 10 {
		return true
	}
	return strings.Contains(strings.ToLower(f.FormatNote), "original")
}

// isHDR reports whether the format carries a high dynamic range picture.
//
// Полагаться на один dynamic_range нельзя: у HLS-форматов (режим HLS-only) yt-dlp
// его НЕ проставляет. Из-за этого HDR-поток не опознавался, а выбор внутри высоты
// идёт по битрейту — у HDR он выше, и он молча выигрывал, уезжая к зрителю под
// меткой обычного «2160p». В браузере без 10-битного декодера это чёрный экран.
// Поэтому HDR определяется ещё и по строке кодека.
func (f ytFormat) isHDR() bool {
	// avc1 у YouTube всегда SDR, что бы ни говорили остальные поля.
	if strings.HasPrefix(f.VCodec, "avc1") {
		return false
	}
	dr := strings.ToUpper(strings.TrimSpace(f.DynamicRange))
	if dr != "" && dr != "SDR" && dr != "NONE" {
		return true
	}
	return hdrByCodec(f.VCodec)
}

// hdrByCodec распознаёт HDR по RFC 6381-строке кодека.
//
//	vp9.2 / vp09.02…  — VP9 profile 2 (10 бит). YouTube отдаёт этот профиль только
//	                    под HDR, так что профиля достаточно.
//	av01.P.LLT.DD.M.CCC.cp.tc.mc.F — у AV1 десять бит сами по себе HDR не означают, но
//	                    поле передаточной характеристики означает: 16 = PQ, 18 = HLG.
//	                    ВНИМАНИЕ на порядок полей (RFC 6381 §3.4): после CCC идут СНАЧАЛА
//	                    цветовые примарии (cp), и только потом передаточная (tc). Здесь
//	                    проверялся индекс 6 — то есть примарии: у HDR-потока там 09 (BT.2020),
//	                    ни 16, ни 18 не бывает, поэтому ветка не срабатывала НИКОГДА и AV1 HDR
//	                    опознавался только по dynamic_range от yt-dlp — которого в режиме
//	                    HLS-only как раз и нет. Правильный индекс — 7.
func hdrByCodec(vcodec string) bool {
	c := strings.ToLower(strings.TrimSpace(vcodec))
	if strings.HasPrefix(c, "vp9.2") || strings.HasPrefix(c, "vp09.02") {
		return true
	}
	if strings.HasPrefix(c, "av01") {
		p := strings.Split(c, ".")
		if len(p) >= 8 {
			switch p[7] {
			case "16", "18":
				return true
			}
		}
	}
	return false
}

// videoRangeOf returns the HLS VIDEO-RANGE attribute value for a format.
// Плееры по нему решают, включать ли HDR-конвейер; без него поток считается SDR
// даже когда в CODECS стоит av01…10 с bt2020.
func videoRangeOf(f ytFormat) string {
	if !f.isHDR() {
		return "SDR"
	}
	if strings.Contains(strings.ToUpper(f.DynamicRange), "HLG") {
		return "HLG"
	}
	return "PQ" // HDR10 и HDR10+ передаются как PQ
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
	ytCacheTTL = 4 * time.Hour
	// ytDegradedCacheTTL — срок жизни ДЕГРАДИРОВАННОГО результата экстракции: в нём
	// нет ни одного прогрессивного формата, только HLS (см. hlsOnlyFormats). Такой
	// ответ означает, что основной CDN нас не пустил, и всё качество держится на
	// ссылках manifest.googlevideo.com. Они мрут заметно раньше собственного expire:
	// инцидент 01.09 — до срока оставалось 297 минут, а ссылка уже отдавала 404 при
	// запросе С САМОГО сервера, чей IP в неё и вшит. Держать такой ответ 4 часа
	// нельзя: пока он лежит в кеше, каждый повтор клиента получает те же мёртвые
	// ссылки за 65 мс, и восстановиться ролик не может в принципе.
	ytDegradedCacheTTL = 4 * time.Minute
	ytCacheMaxSize     = 500
	ytSearchLimit      = 10
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

// ytLinkHost — публичный адрес, от которого нода строит ВСЕ свои YouTube-ссылки
// (mux/master/proxy/feed). Форвардер main нарочно шлёт ноде X-Forwarded-Host
// primary, чтобы /proxy-обёртки других источников вели на main (он сам решает,
// кому отдавать). Но mux- и master-плейлисты YouTube живут в памяти добывшего
// процесса, а /proxy-ссылки на googlevideo привязаны к его IP — main их отдать
// не может. Поэтому нода с [cluster] edge_url подписывает ссылки своим адресом,
// и зритель идёт на неё напрямую.
var ytLinkHost string

func ytHost(r *http.Request) string {
	if ytLinkHost != "" {
		return ytLinkHost
	}
	return hostFromRequest(r)
}

func ytStreamHost(r *http.Request) string {
	if ytLinkHost != "" {
		return ytLinkHost
	}
	return streamHostFromRequest(r)
}

func NewYoutubeChecker(cfg config.Config) *YoutubeChecker {
	if cfg.Cluster.Enable && strings.EqualFold(strings.TrimSpace(cfg.Cluster.Mode), "node") {
		if eu := strings.TrimRight(strings.TrimSpace(cfg.Cluster.EdgeURL), "/"); eu != "" {
			ytLinkHost = eu
			log.Info().Str("edge_url", eu).Msg("youtube: node mode — links signed with edge_url")
		}
	}
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
		muxKeyAt:      make(map[string]time.Time),
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
	y.potReady = y.pluginDirs != "" || potProviderInstalled()
	if y.potReady {
		log.Info().Bool("explicit_dirs", y.pluginDirs != "").
			Msg("youtube: PO Token provider available")
	} else {
		log.Warn().Msg("youtube: PO Token provider NOT found — длинные видео будут резаться грейс-окном CDN")
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
	y.hlsMinDuration = ytHLSMinDuration
	if cfg.YouTube.HLSMinDuration != 0 {
		y.hlsMinDuration = float64(cfg.YouTube.HLSMinDuration)
	}
	if y.fetchPotNever {
		log.Info().Msg("youtube: PO Token disabled by config ([youtube] fetch_pot = \"never\")")
	}
	y.parChunks = cfg.YouTube.ParallelChunks
	y.maxAutoHeight = cfg.YouTube.MaxAutoHeight
	y.sbEnable = cfg.YouTube.SponsorBlockEnabled()
	y.sbCategories = cfg.YouTube.SponsorBlockCategories
	if y.sbEnable {
		log.Info().Strs("категории", y.sponsorCategories()).Msg("youtube: SponsorBlock включён")
	}
	y.sabrURL = strings.TrimRight(strings.TrimSpace(cfg.YouTube.SabrURL), "/")
	if y.sabrURL != "" {
		log.Info().Str("sabr", y.sabrURL).Msg("youtube: видео качаем по SABR (обычные ссылки — только запасной путь)")
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

// ytdlpDefaultPluginPaths — места, куда yt-dlp заглядывает за плагинами САМ.
// Плагин, установленный сюда (а не через pip), детектом выше не находится,
// потому что тот опрашивает только pip. Раньше это молча выключало POT: путь
// пустой → стратегия с POT не поднималась, хотя провайдер работал.
func ytdlpDefaultPluginPaths() []string {
	paths := []string{"/etc/yt-dlp/plugins", "/etc/yt-dlp-plugins"}
	if home, _ := os.UserHomeDir(); home != "" {
		paths = append(paths,
			filepath.Join(home, ".config", "yt-dlp", "plugins"),
			filepath.Join(home, ".yt-dlp", "plugins"),
		)
	}
	return paths
}

// potProviderInstalled reports whether a PO Token provider is reachable by
// yt-dlp WITHOUT an explicit --plugin-dirs. Отдельно от pluginDirs намеренно:
// «провайдер есть» и «нужно передать путь» — разные вопросы, и путать их
// нельзя. Передавать --plugin-dirs, когда плагин лежит в дефолтном каталоге,
// даже вредно: флаг ЗАМЕНЯЕТ список каталогов, и yt-dlp перестаёт видеть то,
// что нашёл бы сам.
func potProviderInstalled() bool {
	for _, base := range ytdlpDefaultPluginPaths() {
		// Каталог плагинов бывает вложен ещё на уровень (yt_dlp_plugins/yt_dlp_plugins).
		for _, probe := range []string{base, filepath.Join(base, "yt_dlp_plugins")} {
			entries, err := os.ReadDir(probe)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if strings.Contains(strings.ToLower(e.Name()), "yt_dlp_plugins") ||
					strings.Contains(strings.ToLower(e.Name()), "bgutil") {
					return true
				}
			}
		}
	}
	return false
}

// appendPluginDirs adds --plugin-dirs when the POT provider was found through
// pip. Каталог из pip передаём ЯВНО (standalone-бинарь туда не смотрит), но
// вместе с "default": флаг ЗАМЕНЯЕТ список каталогов, и без этого yt-dlp
// перестал бы видеть плагины, установленные в свой стандартный путь.
func (y *YoutubeChecker) appendPluginDirs(args []string) []string {
	if y.fetchPotNever || y.pluginDirs == "" {
		return args
	}
	return append(args, "--plugin-dirs", "default", "--plugin-dirs", y.pluginDirs)
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
	args = y.appendPluginDirs(args)
	if y.jsRuntime != "" {
		args = append(args, "--js-runtimes", y.jsRuntime)
	}
	if w := y.ensureCookieWork(); w != "" {
		args = append(args, "--cookies", w)
	}
	return args
}

// ytFlatClients is the client chain for flat-playlist calls (search, trending,
// channel pages). "tv" stays first because it gives the richest listing, but it
// is also the one client that hard-requires a COMPLETE cookie jar: once the jar
// degrades it answers «The page needs to be reloaded» for every request, and
// with no fallback that took search and the whole trending feed down with it.
// Measured on prod 2026-09-01, same three videos: tv 0/3, mweb 3/3,
// tv_embedded 3/3 — while playback stayed healthy, because the streaming path
// (runExtractDetailed) already picks its client dynamically.
var ytFlatClients = []string{"tv", "mweb", "tv_embedded"}

// cookieArgs returns yt-dlp CLI flags for search requests, using whichever
// client last worked (see flatClient).
func (y *YoutubeChecker) cookieArgs() []string {
	return y.cookieArgsFor(y.flatClient())
}

func (y *YoutubeChecker) cookieArgsFor(client string) []string {
	args := y.baseArgs()
	args = append(args, "--extractor-args", "youtube:player_client="+client)
	return args
}

// flatClient is the currently preferred flat-playlist client. It is sticky: once
// a client fails we move on and stay there, so a broken "tv" costs one failed
// call per process rather than one per request.
func (y *YoutubeChecker) flatClient() string {
	i := int(y.flatClientIdx.Load())
	if i < 0 || i >= len(ytFlatClients) {
		return ytFlatClients[0]
	}
	return ytFlatClients[i]
}

// demoteFlatClient advances to the next candidate after `failed` lost a call.
// It compares against the current value so concurrent failures of the same
// client only advance once.
func (y *YoutubeChecker) demoteFlatClient(failed string) {
	for {
		cur := y.flatClientIdx.Load()
		if int(cur) >= len(ytFlatClients)-1 || ytFlatClients[cur] != failed {
			return
		}
		if y.flatClientIdx.CompareAndSwap(cur, cur+1) {
			log.Warn().Str("failed", failed).Str("switched_to", ytFlatClients[cur+1]).
				Msg("youtube: flat-playlist client demoted")
			return
		}
	}
}

// runFlatJSON runs a --dump-single-json yt-dlp call, walking the client chain
// until one answers. base must NOT already carry --extractor-args.
func (y *YoutubeChecker) runFlatJSON(ytdlpPath string, base []string, target string) ([]byte, error) {
	var lastErr error
	start := int(y.flatClientIdx.Load())
	if start < 0 || start >= len(ytFlatClients) {
		start = 0
	}
	for i := start; i < len(ytFlatClients); i++ {
		client := ytFlatClients[i]
		args := append(append([]string{}, base...), y.cookieArgsFor(client)...)
		args = append(args, target)
		cmd := exec.Command(ytdlpPath, args...)
		cmd.Env = os.Environ()

		y.ytdlpSem <- struct{}{}
		out, err := runWithTimeout(cmd, ytExecTimeout)
		<-y.ytdlpSem
		if err == nil {
			return out, nil
		}
		lastErr = err
		y.demoteFlatClient(client)
	}
	return nil, lastErr
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
	out, err := y.runFlatJSON(ytdlpPath, args, fmt.Sprintf("ytsearch%d:%s", limit, searchQuery))
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
	out, err := y.runFlatJSON(ytdlpPath, args, targetURL)
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
	host := ytHost(r)
	out := make([]map[string]any, 0, len(chans))
	for _, c := range chans {
		out = append(out, map[string]any{"id": c.ChannelID, "title": c.Title, "thumb": ytImgProxyRawURL(host, c.Thumbnail)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

// ytTrendingSources are tried in order until one yields entries.
//
// "https://www.youtube.com/feed/trending" led this list until YouTube retired
// the page: as of 2026-09-01 it — and /feed/explore with it — redirects to the
// home page, and yt-dlp reports «The channel/playlist does not exist and the URL
// redirected to youtube.com home» on EVERY player client. That is why swapping
// clients could not rescue the feed; the URL itself was gone. Verified the same
// day from prod, same cookies, same flags: the Trending playlist returns 40
// entries and @YouTube/videos another 40, while plain ytsearch kept working
// throughout — which is what ruled the cookie jar out as the cause.
//
// Keep this a list. The endpoint that died was hardcoded, so its removal took
// the whole «Главная» feed down instead of costing one candidate.
var ytTrendingSources = []string{
	"https://www.youtube.com/playlist?list=PLrEnWoR732-BHrPp_Pm8_VleD68f9s14-",
	"https://www.youtube.com/@YouTube/videos",
}

// trendingEntries walks ytTrendingSources and returns the first non-empty
// listing, so one dead URL costs a candidate rather than the feed.
func (y *YoutubeChecker) trendingEntries(limit int) ([]ytEntry, error) {
	var lastErr error
	for _, src := range ytTrendingSources {
		entries, err := y.ytPlaylistN(src, limit)
		if err == nil && len(entries) > 0 {
			return entries, nil
		}
		if err != nil {
			lastErr = err
			log.Debug().Err(err).Str("source", src).Msg("youtube: trending source failed, trying the next")
		}
	}
	return nil, lastErr
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

	entries, err := y.trendingEntries(40)
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
	results := y.recommendVideos(ytHost(r))
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
	results, meta := y.channelVideos(id, ytHost(r))
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

	host := ytHost(req)

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

	// refresh=1 — плеер уткнулся в мёртвые ссылки (все качества дали 404) и просит
	// пере-извлечь ролик. Без этого повтор попадал в тот же кеш за 65 мс и получал
	// те же мёртвые ссылки: восстановиться было нечем, оставалось ждать TTL.
	if parseBoolParam(q.Get("refresh")) {
		if y.dropFormatsForRefresh(videoID) {
			log.Warn().Str("videoID", videoID).Msg("youtube: клиент просит пере-извлечение — кеш форматов сброшен")
		} else {
			log.Debug().Str("videoID", videoID).Msg("youtube: refresh проигнорирован, запись кеша ещё свежая")
		}
	}

	formats, duration, usedProxy, ok := y.getFormats(videoID)
	if !ok || len(formats) == 0 {
		log.Warn().Str("videoID", videoID).Bool("ok", ok).Msg("youtube: stream — no formats extracted (PO Token?)")
		errMsg := extractReasonFor(videoID)
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

	ytHeaders := ytProxyHeaders()

	// Multi-language audio: the picker pins a track with ?alang=<code>. Filtering
	// the format set here — before the quality map is built — is all it takes:
	// pickBestAudio then sees one language and everything downstream is unchanged.
	// Absent or unknown alang leaves the set alone, so the default stays the
	// uploader's original track.
	audioTracks := ytAudioTracks(formats, ytSelfTrackURL(req))
	wantLang := strings.TrimSpace(req.URL.Query().Get("alang"))
	formats = ytFilterAudioLang(formats, wantLang)

	// Build quality map: label → proxy URL.
	// Prefer combined (video+audio) formats, fallback to video-only direct URLs.
	qr := y.buildQualityMap(req, formats, links, ytHeaders, usedProxy)
	// Страховка: наружу не должно уходить НИ ОДНОЙ прямой googlevideo-ссылки. У CDN
	// нет Access-Control-Allow-Origin, поэтому браузер упрётся в CORS, что бы там ни
	// лежало. Заворачиваем всё, что просочилось, и пишем в лог — по метке видно,
	// какая ветка построения списка дала течь.
	y.proxyLeakedQualities(&qr, req, links, ytHeaders)
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
	bestLabel := ytBestLabelCapped(qr.Qualities, y.autoHeightCap())

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

	host := ytHost(req)

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
		// Selectable audio languages. Only present when the video actually has
		// more than one, so a client can show the picker exactly when it matters.
		if len(audioTracks) > 0 {
			row["audio_tracks"] = audioTracks
			row["audio_lang"] = ytActiveTrackLang(audioTracks, wantLang)
		}
		if dashURL != "" {
			row["dash"] = dashURL
		}
		if sb := y.SponsorBlockFor(videoID, duration); len(sb) > 0 {
			row["sponsorblock"] = sb
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
	// Разметка SponsorBlock: клиент сам перематывает эти отрезки. Ответ не ждёт
	// сети дольше своего таймаута, а отсутствие разметки — обычное дело.
	if sb := y.SponsorBlockFor(videoID, duration); len(sb) > 0 {
		resp["sponsorblock"] = sb
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
	// VideoRange — «SDR», «PQ» или «HLG» для атрибута VIDEO-RANGE мастер-плейлиста.
	// Без него плеер считает поток SDR, даже когда в CODECS стоит av01…10 с bt2020,
	// и HDR-конвейер на телевизоре не включается.
	VideoRange string
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
	host := ytHost(req)
	muxVideoID := strings.TrimSpace(req.URL.Query().Get("videoID")) // for POT-flap re-mint

	log.Debug().Int("total_formats", len(formats)).Msg("youtube: buildQualityMap start")

	// When iOS client extraction produces HLS audio formats (233/234), YouTube
	// serves those via manifest.googlevideo.com which bypasses the main-CDN
	// IP ban. DASH formats from the same response go through the banned CDN
	// and return 403 when ffmpeg tries to mux. So if any HLS audio exists,
	// drop every DASH (https protocol) format — HLS-only mode.
	hasHLSAudio := ytHasHLSAudio(formats)
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
	hdrByHeight := make(map[int]ytFormat)
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
		// HDR копим ОТДЕЛЬНО. В общей карте при равной высоте всегда выигрывает avc1
		// (ради TS-совместимости), а он у YouTube только SDR — то есть HDR там
		// гарантированно проигрывает и до зрителя не доходит.
		// HDR копим из ЛЮБОГО протокола: прогрессивный уйдёт в склейку, а HLS —
		// мастер-плейлистом через прокси, как и обычные качества в режиме HLS-only.
		// Раньше здесь стояло ограничение на прогрессивные, и на 35 из 35 просмотров
		// в HLS-режиме HDR-метка не появлялась вовсе.
		if f.isHDR() && h >= ytHDRMinHeight {
			if cur, seen := hdrByHeight[h]; !seen || hdrBetter(f, cur) {
				hdrByHeight[h] = f
			}
		}
		existing, ok := videoByHeight[h]
		if !ok {
			videoByHeight[h] = f
			continue
		}
		// Прогрессивный формат важнее кодека: HLS-вариант той же высоты качается
		// не по диапазонам, а значит мимо SABR и мимо ranged-докачки, и на длинном
		// ролике обрывается. Берём m3u8, только если прогрессивного на эту высоту нет.
		isProg, existIsProg := f.Protocol == "https", existing.Protocol == "https"
		if isProg != existIsProg {
			if isProg {
				videoByHeight[h] = f
			}
			continue
		}
		// SDR важнее битрейта. Иначе на высоте, где avc1 нет, побеждает HDR-вариант
		// (он всегда «жирнее») и уезжает к зрителю под обычной меткой — а браузер
		// без 10-битного декодера показывает чёрный экран. HDR доступен отдельной
		// меткой, и это осознанный выбор.
		isSDR, existIsSDR := !f.isHDR(), !existing.isHDR()
		if isSDR != existIsSDR {
			if isSDR {
				videoByHeight[h] = f
			}
			continue
		}
		// ★PO Token важнее кодека. Ссылка без pot= отдаётся CDN лишь тизерным окном
		// (~17 МиБ ≈ 4 минуты 1080p), дальше 403 — и склейка умирает, а перевыписка
		// просит тот же itag и получает ту же голую ссылку (28 кругов подряд на
		// проде 2026-09-04, «видео упало на 4:18»). Голый avc1 приходит из
		// подмешанного DASH клиента default, а токенизированный av01/vp9 той же
		// высоты — из mweb; прежнее «всегда avc1» выбирало именно обрывающийся.
		// Аудио этому правилу подчиняется давно (pickBestAudio) — теперь и видео.
		// Стоит ПОСЛЕ SDR/HDR: токенизированный HDR не должен уезжать под обычной
		// меткой к зрителю без 10-битного декодера.
		hasPOT, existHasPOT := ytHasPOT(f.URL), ytHasPOT(existing.URL)
		if hasPOT != existHasPOT {
			if hasPOT {
				videoByHeight[h] = f
			}
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

	// Голый HLS отдаём плееру ТОЛЬКО когда прогрессивных форматов нет вовсе.
	// Иначе часть качеств пришла бы склейкой (со звуком внутри), а часть —
	// отдельной видеодорожкой, которой звук нужен снаружи: плеер получил бы либо
	// тишину, либо двойное аудио. Есть прогрессивный — играем по нему, он ещё и
	// качается параллельно.
	hlsOnly := true
	for _, m := range []map[int]ytFormat{videoByHeight, combinedByHeight} {
		for _, f := range m {
			if f.Protocol == "https" {
				hlsOnly = false
			}
		}
	}
	// Звук нужен снаружи, только если отданная дорожка — video-only.
	hlsNeedsAudio := false

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

		// HLS мимо ffmpeg, но ЧЕРЕЗ наш /proxy. Склеивать его нечем: этот билд
		// ffmpeg на HLS-входе ПАДАЕТ (segmentation fault на смене узла CDN между
		// сегментами; -http_persistent/-multiple_requests не спасают), а прежний
		// путь через байтовый пайп скармливал ему текст плейлиста вместо видео.
		// HLS плеер играет нативно — но сырую googlevideo-ссылку ему давать нельзя:
		// у CDN нет Access-Control-Allow-Origin, и веб-клиент упирается в CORS
		// («Ошибка CORS» на каждом index.m3u8, плеер стоит на 0:00). Прокси даёт
		// ссылку на наш же домен и подставляет нужные заголовки.
		//
		// Напомню, зачем HLS вообще нужен: при бане нашего IP на основном CDN
		// buildQualityMap выше переключается в режим HLS-only, и других форматов
		// у ролика попросту не остаётся.
		if f, videoOnly, ok := hlsFormatFor(h, videoByHeight, combinedByHeight); ok && !hlsOnly {
			// У этой высоты только HLS, но у ролика есть и прогрессивные форматы.
			// Отдавать её нельзя: склейка на m3u8 падает, а голый HLS вперемешку с
			// mux-качествами даёт то тишину, то двойное аудио. Высоту пропускаем —
			// зритель получит соседние, которые играют.
			continue
		} else if ok && hlsOnly {
			vURL := y.proxyYTURL(f.URL, clientIP(req), links, headers, ytStreamHost(req))
			if videoOnly && bestAudio.URL != "" {
				// Видеодорожка без звука. Отдавать её вместе с адресом аудио отдельным
				// полем ответа мало — клиент его не подхватывает, зритель смотрит немое
				// видео. Поэтому собираем МАСТЕР-плейлист: он связывает видео и аудио
				// группой EXT-X-MEDIA, и плеер получает одну ссылку с обеими дорожками.
				aURL := y.proxyYTURL(bestAudio.URL, clientIP(req), links, headers, ytStreamHost(req))
				result[label] = y.registerHLSMaster(host, muxVideoID, label, f, bestAudio, vURL, aURL)
				hlsNeedsAudio = true
				continue
			}
			result[label] = vURL
			continue
		}

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
					y.registerMuxKey(muxKey, cacheKey, muxVideoID, vf.VCodec, usedProxy, transAudio, needFMP4)
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
				y.registerMuxKey(muxKey, cacheKey, muxVideoID, cf.VCodec, usedProxy, false, false)
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

	// HDR-качества добавляются отдельными метками, поверх обычных. Идут той же
	// склейкой: замер 2026-08-31 показал, что -c copy в fMP4 сохраняет колориметрию
	// без потерь (bt2020nc / smpte2084 / bt2020 / yuv420p10le до и после совпадают),
	// поэтому перекодировать ничего не нужно.
	if bestAudio.URL != "" {
		for h, vf := range hdrByHeight {
			label := strconv.Itoa(h) + "p" + ytHDRSuffix
			if vf.Protocol != "https" {
				// HLS-HDR. Склеивать его нечем (ffmpeg на плейлисте из пайпа падает),
				// зато ровно так же, как обычные качества в этом режиме, он отдаётся
				// мастер-плейлистом: видео и аудио связаны группой EXT-X-MEDIA, обе
				// ссылки проксированы. Вне режима HLS-only не отдаём: смешивать голый
				// HLS с mux-качествами уже пробовали — получается либо тишина, либо
				// двойное аудио.
				if !hlsOnly {
					continue
				}
				vURL := y.proxyYTURL(vf.URL, clientIP(req), links, headers, ytStreamHost(req))
				aURL := y.proxyYTURL(bestAudio.URL, clientIP(req), links, headers, ytStreamHost(req))
				result[label] = y.registerHLSMaster(host, muxVideoID, label, vf, bestAudio, vURL, aURL)
				hlsNeedsAudio = true
				continue
			}
			if y.ffmpegPath == "" {
				continue
			}
			cacheKey := vf.URL + "\x00" + bestAudio.URL
			muxKey := fmt.Sprintf("%x", md5Hash(cacheKey))[:16]
			result[label] = host + "/lite/youtube/mux/index.m3u8?key=" + muxKey
			transAudio := !strings.Contains(bestAudio.ACodec, "aac") && !strings.Contains(bestAudio.ACodec, "mp4a")
			// HDR бывает только в VP9.2/AV1, а они в TS не укладываются — всегда fMP4.
			muxPairs[label] = ytMuxPair{
				VideoURL:    vf.URL,
				AudioURL:    bestAudio.URL,
				VCodec:      vf.VCodec,
				ACodec:      bestAudio.ACodec,
				TransAudio:  transAudio,
				FMP4:        true,
				Height:      vf.Height,
				Width:       vf.Width,
				Bandwidth:   int((vf.TBR + bestAudio.ABR) * 1000),
				VideoRange:  videoRangeOf(vf),
				VInitRange:  vf.InitRange.rangeAttr(),
				VIndexRange: vf.IndexRange.rangeAttr(),
				AInitRange:  bestAudio.InitRange.rangeAttr(),
				AIndexRange: bestAudio.IndexRange.rangeAttr(),
			}
			y.registerMuxKey(muxKey, cacheKey, muxVideoID, vf.VCodec, usedProxy, transAudio, true)
		}
	}

	var audioProxyURL string
	if bestAudio.URL != "" {
		if hlsNeedsAudio {
			// Голый HLS идёт мимо склейки, поэтому звук отдаём ОТДЕЛЬНОЙ дорожкой —
			// иначе зритель получает видео без звука. Через прокси: у googlevideo нет
			// Access-Control-Allow-Origin, и веб-клиент упёрся бы в CORS.
			audioProxyURL = y.proxyYTURL(bestAudio.URL, clientIP(req), links, headers, ytStreamHost(req))
		} else if y.ffmpegPath != "" {
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

// registerHLSMaster публикует мастер-плейлист на одно качество: вариант с видео плюс
// связанная с ним аудиодорожка. Ключ включает высоту — у каждого качества свой мастер,
// потому что качество здесь выбирает не плеер, а вызывающий код.
func (y *YoutubeChecker) registerHLSMaster(host, videoID, label string, v, a ytFormat, vURL, aURL string) string {
	bw := int((v.TBR + a.ABR) * 1000)
	if bw <= 0 {
		bw = v.Height * v.Height * 3
		if bw < 200_000 {
			bw = 200_000
		}
	}
	key := fmt.Sprintf("%x", md5Hash("hlsmaster:"+videoID+":"+label))[:16]
	y.muxMu.Lock()
	y.masterCache[key] = ytMasterEntry{
		Variants: []ytMasterVariant{{
			Label:      label,
			Height:     v.Height,
			Width:      v.Width,
			Bandwidth:  bw,
			Codecs:     hlsCodecsString(v.VCodec, hlsAudioCodec(a), false),
			VariantURL: vURL,
			AudioURL:   aURL,
			VideoRange: videoRangeOf(v),
		}},
		Expires: time.Now().Add(ytMuxTTL),
	}
	now := time.Now()
	for k, e := range y.masterCache {
		if now.After(e.Expires) {
			delete(y.masterCache, k)
		}
	}
	y.muxMu.Unlock()
	// videoID в ссылке — чтобы мастер можно было ПЕРЕСОБРАТЬ, если его нет в
	// памяти. Реестр живёт в процессе, поэтому любой перезапуск раньше отдавал
	// зрителю 404 «master playlist not found» посреди просмотра, а клиент нёс этот
	// мёртвый адрес в транскодер (замер на проде 2026-08-31).
	return host + "/lite/youtube/mux/master.m3u8?key=" + key + "&v=" + url.QueryEscape(videoID)
}

// proxyLeakedQualities заворачивает в /proxy всё, что осталось прямой ссылкой на
// googlevideo. Это именно страховка, а не основной путь: каждая такая находка —
// ошибка в построении списка качеств, поэтому она попадает в лог с меткой.
func (y *YoutubeChecker) proxyLeakedQualities(qr *ytQualityResult, req *http.Request,
	links *proxylink.Manager, headers map[string]string) {
	if links == nil {
		return
	}
	host := ytStreamHost(req)
	ip := clientIP(req)
	for label, u := range qr.Qualities {
		if !strings.Contains(u, "googlevideo.com") {
			continue
		}
		log.Warn().Str("label", label).Msg("youtube: прямая googlevideo-ссылка в списке качеств — заворачиваю в прокси")
		qr.Qualities[label] = y.proxyYTURL(u, ip, links, headers, host)
	}
	if strings.Contains(qr.AudioURL, "googlevideo.com") {
		log.Warn().Msg("youtube: прямая googlevideo-ссылка в аудиодорожке — заворачиваю в прокси")
		qr.AudioURL = y.proxyYTURL(qr.AudioURL, ip, links, headers, host)
	}
}

// hlsFormatFor returns the HLS format chosen for a height, preferring the combined
// one: у него уже есть звук, тогда как video-only HLS пришлось бы сводить с
// отдельной аудиодорожкой — а сводить нечем, ffmpeg на HLS падает.
func hlsFormatFor(h int, videoByHeight, combinedByHeight map[int]ytFormat) (f ytFormat, videoOnly, ok bool) {
	if c, found := combinedByHeight[h]; found && c.Protocol == "m3u8_native" {
		return c, false, true
	}
	if v, found := videoByHeight[h]; found && v.Protocol == "m3u8_native" {
		return v, true, true
	}
	return ytFormat{}, false, false
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
	// m3u8 отбрасывать НЕЛЬЗЯ. Это не запасной вариант, а несущий: когда у ролика
	// есть HLS-аудио от iOS-клиента, buildQualityMap намеренно выкидывает ВСЕ
	// DASH-форматы и работает только по HLS — основной CDN на нашем IP забанен и
	// отвечает 403, а manifest.googlevideo.com отдаёт. Отбраковка m3u8 (моя правка
	// 2026-08-31) обнулила у таких роликов список качеств целиком: «Нет подходящих
	// качеств для воспроизведения», 2284 отклонённых формата за 40 минут.
	//
	// Прогрессивный формат всё равно предпочтительнее при равной высоте (см.
	// videoByHeight): по нему работает параллельная докачка, а по HLS — нет.
	return proto == "https" || proto == "m3u8_native"
}

// ytBestLabel returns the best quality label from a map.
func ytBestLabel(quals map[string]string) string {
	return ytBestLabelCapped(quals, 0)
}

// ytHDRMinHeight — ниже этой высоты HDR-качества не показываем. YouTube отдаёт HDR
// вплоть до 144p, и без порога список удваивался бы строчками вроде «144p HDR»,
// которых никто не выберет.
const ytHDRMinHeight = 1080

// hdrBetter выбирает между двумя HDR-форматами одной высоты. AV1 предпочтительнее
// VP9.2: при равном качестве он заметно легче по битрейту, а на приставках, где
// HDR вообще есть, AV1 обычно тоже есть.
func hdrBetter(a, b ytFormat) bool {
	aAV1 := strings.HasPrefix(a.VCodec, "av01")
	bAV1 := strings.HasPrefix(b.VCodec, "av01")
	if aAV1 != bAV1 {
		return aAV1
	}
	return a.TBR > b.TBR
}

// ytHDRSuffix помечает HDR-качества в списке. Отдельная метка, а НЕ замена SDR:
// HDR у YouTube существует только в VP9.2/AV1 с 10 битами, а их декодирует не
// всякая приставка — подменив 1080p на HDR-вариант, мы бы отняли рабочее видео у
// части зрителей. Так выбор остаётся за ними.
const ytHDRSuffix = " HDR"

// ytLabelHeight извлекает высоту из метки качества, включая «2160p HDR».
func ytLabelHeight(label string) int {
	l := strings.TrimSuffix(label, ytHDRSuffix)
	h, _ := strconv.Atoi(strings.TrimSuffix(l, "p"))
	return h
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
		// HDR по умолчанию не включаем НИКОГДА: это осознанный выбор зрителя, а на
		// приставке без 10-битного VP9/AV1 такой поток просто не проиграется.
		if strings.HasSuffix(q, ytHDRSuffix) {
			continue
		}
		h := ytLabelHeight(q)
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
	// HDR и здесь пропускаем: лучше отдать SDR выше потолка, чем поток, который
	// приставка может вообще не декодировать.
	for _, q := range ytQualityOrder {
		if _, ok := quals[q]; ok {
			return q
		}
	}
	for q := range quals {
		if !strings.HasSuffix(q, ytHDRSuffix) {
			return q
		}
	}
	// Кроме HDR не осталось ничего — тогда уж он, чем пустота.
	for q := range quals {
		return q
	}
	return ""
}

// formatsCacheTTL — сколько держать результат экстракции. Деградированный (без
// прогрессивных форматов) живёт минуты, а не часы: см. ytDegradedCacheTTL.
func formatsCacheTTL(formats []ytFormat) time.Duration {
	if hlsOnlyFormats(formats) {
		return ytDegradedCacheTTL
	}
	return ytCacheTTL
}

// ytRefreshMinAge — насколько старой должна быть запись кеша, чтобы клиент имел
// право её сбросить. Плеер зовёт refresh из аварийного пути (все качества упали с
// «манифест 404»), а этот путь легко зацикливается: без нижней границы каждый
// повтор запускал бы новый yt-dlp и съел бы все слоты экстракции.
const ytRefreshMinAge = 45 * time.Second

// dropFormatsForRefresh выбрасывает закешированные форматы ролика, чтобы следующий
// resolve сходил в yt-dlp заново. Возвращает false, если запись слишком свежая
// (значит, кто-то уже пере-извлёк её только что) — тогда отдаём, что есть.
func (y *YoutubeChecker) dropFormatsForRefresh(videoID string) bool {
	y.mu.Lock()
	defer y.mu.Unlock()
	cached, ok := y.cache[videoID]
	if !ok {
		return false
	}
	if !cached.Born.IsZero() && time.Since(cached.Born) < ytRefreshMinAge {
		return false
	}
	delete(y.cache, videoID)
	return true
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
			Born:    time.Now(),
		}
		y.mu.Unlock()
		return nil, 0, false, false
	}

	ttl := formatsCacheTTL(formats)
	if ttl != ytCacheTTL {
		log.Warn().Str("videoID", videoID).Int("formats", len(formats)).
			Dur("ttl", ttl).Msg("youtube: экстракция без прогрессивных форматов — кешируем ненадолго")
	}

	now := time.Now()
	y.mu.Lock()
	y.cleanupCacheLocked()
	y.cache[videoID] = &ytFormatCache{
		Formats:   formats,
		Duration:  duration,
		Expires:   now.Add(ttl),
		UsedProxy: usedProxy,
		Born:      now,
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
	// Multi-language videos: keep ONLY the original track when the video has one.
	//
	// Without this the choice fell through to container/bitrate alone, and on a
	// video with 44 tracks whichever dub happened to score best won — a Russian
	// creator's video would play in English. The original is what the uploader
	// recorded, so it is never a surprise; a dub always can be.
	//
	// Videos with a single language mark no format as original (both of Rick
	// Astley's audio formats sit at language_preference -1), so the filter stays
	// inert there and the old selection applies unchanged.
	hasOriginal := false
	for _, f := range formats {
		if f.isAudioOnly() && f.URL != "" && f.isOriginalAudio() {
			hasOriginal = true
			break
		}
	}

	var bestAudio ytFormat
	for _, f := range formats {
		if !f.isAudioOnly() || f.URL == "" || !ytAcceptProtocol(f.Protocol) {
			continue
		}
		if hasOriginal && !f.isOriginalAudio() {
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
// ytHasHLSAudio — есть ли в выдаче HLS-аудиодорожка (itag 233/234 с
// manifest.googlevideo.com). Её наличие переводит buildQualityMap в HLS-only
// режим: все DASH-форматы выбрасываются, качества идут мастер-плейлистом.
func ytHasHLSAudio(fmts []ytFormat) bool {
	for _, f := range fmts {
		if f.isAudioOnly() && f.Protocol == "m3u8_native" && f.URL != "" {
			return true
		}
	}
	return false
}

// preferHLSForLong решает, подменять ли выдачу победителя (DASH) HLS-лестницей
// default-клиента: только для ролика не короче minDur, только если у победителя
// HLS-аудио нет, а у кандидата есть и аудио, и хоть одна видеодорожка HLS.
// Возвращает кандидата целиком — https-форматы из него выкинет сам
// buildQualityMap (HLS-only режим).
func preferHLSForLong(best, hi []ytFormat, dur, minDur float64) ([]ytFormat, bool) {
	if minDur < 0 || dur < minDur || ytHasHLSAudio(best) || !ytHasHLSAudio(hi) {
		return nil, false
	}
	for _, f := range hi {
		if f.isVideoOnly() && f.URL != "" && f.Protocol == "m3u8_native" {
			return hi, true
		}
	}
	return nil, false
}

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
	_, ok := y.verifyFormats(fmts)
	return ok
}

// verifyFormats проверяет ТО, ЧТО РЕАЛЬНО УЕДЕТ ЗРИТЕЛЮ, и возвращает набор форматов,
// пригодный к выдаче, — иногда подрезанный.
//
// Развилку задаёт `hasHLSAudio` в buildQualityMap: хватает ОДНОГО audio-only формата
// по m3u8, чтобы из ответа выбросило все прямые googlevideo-ссылки и зритель получил
// голые манифесты. Правило написано под бан нашего IP на основном CDN — тогда DASH
// отвечает 403, а манифесты живут. Но 01.09 всё оказалось наоборот: прогрессив отдавал
// 206 на обоих концах гигабайтного файла, а манифесты — 404. Прежняя проба смотрела на
// прогрессив, видела 206 и объявляла результат здоровым, хотя выдача уходила в HLS.
//
// Поэтому решаем по факту, а не по наличию HLS-аудио:
//
//  1. HLS-аудио нет — обычная проба прямых ссылок;
//  2. HLS-аудио есть, но есть и прогрессивное видео — сначала пробуем прогрессив. Качается
//     — ВЫРЕЗАЕМ HLS-форматы, чтобы buildQualityMap не увёл выдачу с рабочего пути;
//  3. прогрессив не качается (вот он, настоящий бан) — проверяем сами манифесты и
//     оставляем HLS-режим. Бракуем ТОЛЬКО при 404/410: 403, таймаут или чужая геозона
//     уликой смерти не считаем, иначе уйдём с последнего рабочего пути.
func (y *YoutubeChecker) verifyFormats(fmts []ytFormat) ([]ytFormat, bool) {
	if hlsOnlyFormats(fmts) {
		if trimmed, ok := y.tryProgressive(fmts); ok {
			return trimmed, true
		}
		for _, u := range pickHLSProbeURLs(fmts) {
			if y.manifestGone(u) {
				log.Warn().Str("stream", ytURLDesc(u)).
					Msg("youtube: HLS-манифест мёртв (404) — результат клиента бракуем")
				return fmts, false
			}
		}
		return fmts, true
	}
	return fmts, y.probeDirect(fmts)
}

// tryProgressive — у набора есть и HLS-аудио, и прямые ссылки. Проверяем прямые: если
// качаются, отдаём набор БЕЗ HLS-форматов (иначе одно HLS-аудио уведёт выдачу в режим
// голых манифестов и выбросит 22 рабочих качества из-за четырёх).
func (y *YoutubeChecker) tryProgressive(fmts []ytFormat) ([]ytFormat, bool) {
	if len(pickProbeFormats(fmts)) == 0 {
		return nil, false // прямых ссылок нет вовсе — проверять нечего
	}
	if !y.probeDirect(fmts) {
		return nil, false
	}
	trimmed := make([]ytFormat, 0, len(fmts))
	for _, f := range fmts {
		if f.Protocol == "m3u8_native" {
			continue
		}
		trimmed = append(trimmed, f)
	}
	log.Info().Int("было", len(fmts)).Int("стало", len(trimmed)).
		Msg("youtube: прогрессив качается — вырезаем HLS-форматы, чтобы выдача не ушла на манифесты")
	return trimmed, true
}

// probeDirect — прежняя проба прямых googlevideo-ссылок (общее аудио + верхняя видеодорожка).
func (y *YoutubeChecker) probeDirect(fmts []ytFormat) bool {
	sel := pickProbeFormats(fmts)
	if len(sel) < 2 {
		for _, f := range sel {
			if !y.probeStreamURL(f.URL, f.FileSize) {
				return false
			}
		}
		return true
	}
	// Аудио и видео проверяем ОДНОВРЕМЕННО. Проверка та же, но раньше они шли по
	// очереди, и вместе с двумя фазами внутри каждой пробы это давало четыре
	// последовательных сетевых круга на стратегию. Замер на проде 2026-08-31:
	// 5.1 пробы на резолв, медиана резолва 26.8 с при том, что один прогон
	// yt-dlp занимает 3 с — то есть время уходило именно в ожидание проб.
	res := make([]bool, len(sel))
	var wg sync.WaitGroup
	for i, f := range sel {
		wg.Add(1)
		go func(i int, f ytFormat) {
			defer wg.Done()
			res[i] = y.probeStreamURL(f.URL, f.FileSize)
		}(i, f)
	}
	wg.Wait()
	for _, ok := range res {
		if !ok {
			return false
		}
	}
	return true
}

// pickHLSProbeURLs — манифесты, которые в HLS-режиме реально получит плеер: верхняя
// видеодорожка и общее аудио. Именно их живучесть и решает, играет ролик или нет.
func pickHLSProbeURLs(fmts []ytFormat) []string {
	var out []string
	var video ytFormat
	for _, f := range fmts {
		if f.URL == "" || !f.isVideoOnly() || f.Protocol != "m3u8_native" {
			continue
		}
		if f.Height > video.Height {
			video = f
		}
	}
	if video.URL != "" {
		out = append(out, video.URL)
	}
	if a := pickBestAudio(fmts); a.URL != "" && a.Protocol == "m3u8_native" {
		out = append(out, a.URL)
	}
	return out
}

// manifestGone — сервер отвечает по этому манифесту «его нет» (404/410). Пробы
// диапазонами тут не годятся: raceProbe ждёт строго 206, а плейлист отдаётся 200.
// Возвращает true, только если ВСЕ маршруты дали 404/410 — один неудачный выход не
// повод хоронить ссылку.
func (y *YoutubeChecker) manifestGone(rawURL string) (gone bool) {
	start := time.Now()
	defer func() {
		log.Debug().Bool("gone", gone).Str("stream", ytURLDesc(rawURL)).
			Dur("elapsed", time.Since(start)).Msg("youtube: проба HLS-манифеста")
	}()
	attempts := y.buildStreamAttempts()
	if len(attempts) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	type verdict struct{ gone, answered bool }
	resCh := make(chan verdict, len(attempts))
	for _, a := range attempts {
		go func(a streamAttempt) {
			req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
			if err != nil {
				resCh <- verdict{}
				return
			}
			ua, origin, referer := ytCDNHeaders(rawURL)
			req.Header.Set("User-Agent", ua)
			req.Header.Set("Origin", origin)
			req.Header.Set("Referer", referer)
			resp, err := a.client.Do(req)
			if err != nil {
				resCh <- verdict{}
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			dead := resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone
			resCh <- verdict{gone: dead, answered: true}
		}(a)
	}

	answered, deadCount := 0, 0
	for range attempts {
		select {
		case v := <-resCh:
			if !v.answered {
				continue
			}
			answered++
			if !v.gone {
				return false // хоть один маршрут видит манифест живым
			}
			deadCount++
		case <-ctx.Done():
			return false
		}
	}
	return answered > 0 && deadCount == answered
}

// pickProbeURLs — URL-обёртка над pickProbeFormats для вызовов, которым размер не нужен.
func pickProbeURLs(fmts []ytFormat) []string {
	sel := pickProbeFormats(fmts)
	out := make([]string, 0, len(sel))
	for _, f := range sel {
		out = append(out, f.URL)
	}
	return out
}

// pickProbeURLs returns the direct googlevideo URLs the mux will actually
// download — the shared audio track (pickBestAudio) and the top video-only
// rung — for probeStreamURL. Both must be verified: live 2026-08-03 mweb's
// video URLs downloaded fine while its audio URLs 403'd, so probing just one
// side declares a poisoned session healthy. m3u8 URLs are excluded (a ranged
// GET of a playlist answers 200, which the probe would misread as poison).
func pickProbeFormats(fmts []ytFormat) []ytFormat {
	var urls []ytFormat
	if a := pickBestAudio(fmts); a.URL != "" && a.Protocol == "https" && !strings.Contains(a.URL, "m3u8") {
		urls = append(urls, a)
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
		urls = append(urls, video)
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
		priority  int        // lower = better
		raw       []ytFormat // выдача клиента ДО verifyFormats: там tryProgressive вырезает HLS, а для длинного ролика он нужен
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
	if y.potReady && !y.fetchPotNever {
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

	// Fail open when every client that can deliver a real quality ladder is cooling — not only
	// when literally every client is.
	//
	// `android` is a 360p-only last resort, and it rarely cools down, so it used to keep the
	// fail-open from ever firing: the moment default/mweb/tv were all parked on a probe flap,
	// extraction quietly settled for progressive 360p. Prod 2026-08-27 was doing exactly that —
	// android won 23 of 26 extractions while a hand-run of the default client on the same host
	// returned a full 37-format ladder. A short cooldown is a hint, not a verdict; a 4K→360p
	// cliff is worth one more attempt.
	allCooling := y.everyLadderClientCooling(time.Now())
	if allCooling {
		log.Warn().Str("videoID", videoID).Msg("youtube: every full-ladder client is in cooldown — ignoring cooldowns for this attempt (fail-open)")
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
			raw:       fmts,
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
		if trimmed, ok := y.verifyFormats(res.formats); ok {
			res.formats = trimmed
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
				res2 := &extractResult{formats: fmts2, raw: fmts2, duration: dur2, usedProxy: s.proxy, priority: s.priority}
				if isStrongResult(res2.formats) {
					if trimmed, ok := y.verifyFormats(res2.formats); ok {
						res2.formats = trimmed
						best = res2
						break
					}
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
			res := &extractResult{formats: fmts, raw: fmts, duration: dur, usedProxy: s.proxy, priority: s.priority}
			if !isStrongResult(res.formats) {
				if weak == nil {
					weak = res
				}
				continue
			}
			if trimmed, ok := y.verifyFormats(res.formats); ok {
				res.formats = trimmed
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
		if trimmed, ok := y.verifyFormats(weak.formats); ok {
			weak.formats = trimmed
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
	// Derive the winning client from the strategy list itself. The old hardcoded
	// priority→label map assumed the default ordering and mislabelled the winner whenever
	// the POT ordering put mweb first.
	winner := ""
	for _, st := range strategies {
		if st.priority == best.priority {
			winner = st.client
			break
		}
	}
	winnerLabel := winner
	if winnerLabel == "" {
		winnerLabel = "default"
	}
	// Remember it: a re-mint that asks the wrong client gets nothing back, the download
	// dies at its first byte wall and the viewer sees a 500 about a minute in. Prod
	// 2026-08-27: android was winning extraction while re-mint still asked default only.
	// ★2026-09-04: длинный ролик — HLS вместо склейки. DASH-URL (и с POT) отдаёт
	// ~60 с медиа и упирается в 403 «past window»; переминт двигает стену на минуту,
	// зритель всё равно падает на 4:18. HLS с manifest.googlevideo.com стены не
	// имеет (1080p, 415 МиБ / 10 мин, 0 отказов), а лестницу с HLS-аудио 233/234
	// отдаёт только default-клиент — и её наличие включает HLS-only режим в
	// buildQualityMap. mweb с POT побеждает первым и HLS не несёт, поэтому для
	// длинного ролика дотягиваем default-экстракцию и подменяем ею победителя.
	// Сначала смотрим в СЫРУЮ выдачу победителя: default-клиент несёт HLS сам, но
	// tryProgressive внутри verifyFormats вырезает его, когда прогрессив качается
	// (пробе хватает первых байт, стену на 60-й секунде она не видит). Вторая
	// экстракция нужна только победителю без HLS вовсе (mweb).
	if y.hlsMinDuration >= 0 && best.duration >= y.hlsMinDuration && !ytHasHLSAudio(best.formats) {
		cand, src := best.raw, winnerLabel+" raw"
		if !ytHasHLSAudio(cand) {
			if hi, ok := noCookieFormats(); ok {
				cand, src = hi, "default no-cookies"
			}
		}
		if swapped, ok := preferHLSForLong(best.formats, cand, best.duration, y.hlsMinDuration); ok {
			log.Info().Str("videoID", videoID).Str("winner", winnerLabel).Str("source", src).Float64("duration", best.duration).
				Int("hls_formats", len(swapped)).Msg("youtube: long video — HLS ladder instead of DASH mux")
			best.formats = swapped
		}
	}
	y.rememberWinner(videoID, winner)
	log.Debug().Str("videoID", videoID).Str("winner", winnerLabel+proxyLabel).Int("formats", len(best.formats)).Msg("youtube: extraction winner")

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
	args = y.appendPluginDirs(args)
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
	// Куки отдаём ВСЕМ клиентам. Раньше tv/android/android_vr/tv_simply/ios шли
	// без них, потому что yt-dlp когда-то молча пропускал такой клиент. Он этого
	// больше не делает: клиенту, который куки не поддерживает, yt-dlp их просто не
	// отправляет (поэтому запрет на куки в мобильных клиентах соблюдается сам собой),
	// а tv их принимает и без них не работает вовсе.
	//
	// Замер на проде 2026-08-31, когда IP словил бот-чек:
	//   tv БЕЗ кук   → «The page needs to be reloaded», 0 форматов;
	//   tv С куками  → ссылка получена.
	// Из-за старого исключения основной клиент оставался без единственного, что
	// проводило его через проверку, извлечение скатывалось до android и получало
	// «Sign in to confirm you're not a bot» — за 25 минут 11 провалов и ни одного mux.
	args = append(args, y.baseArgs()...)
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
		rememberExtractReason(videoID, stderrStr)
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
		rememberExtractReason(videoID, stderrStr)
		return nil, 0, false, ytAntiBotStderr(stderrStr)
	}

	if len(dump.Formats) == 0 {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if len(stderrStr) > 1000 {
			stderrStr = stderrStr[len(stderrStr)-1000:]
		}
		log.Warn().Str("videoID", videoID).Str("client", clientLabel).Bool("proxy", useProxy).Str("stderr", stderrStr).Msg("youtube: yt-dlp returned 0 formats")
		rememberExtractReason(videoID, stderrStr)
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
	ytImgMu          sync.Mutex
	ytImgProxyAddr   string
	ytImgClientAddr  string // выход, под который собран ytImgProxyClient
	ytImgProxyClient *http.Client
)

func setYtImgProxyAddr(addr string) {
	ytImgMu.Lock()
	ytImgProxyAddr = addr
	ytImgMu.Unlock()
}

// ytImgFetchClient returns the proxied uTLS client when a bypass is configured, else the
// default client. Built lazily so a "proxy = none" server pays nothing.
//
// Клиент пересобирается при смене выхода. Раньше он строился один раз
// (sync.Once) под адрес на момент ПЕРВОГО превью — обычно статический выход из
// конфига, ещё до того как перебор (setActiveProxy) нашёл живой. Мёртвый выход
// прибивался до следующего перезапуска: после рестарта 21.09.2026 20:28Z
// /lite/youtube/img отдавал 377 × 502 с медианой ровно 10 с (таймаут) при
// 84 × 200 из кэша.
func ytImgFetchClient() *http.Client {
	ytImgMu.Lock()
	defer ytImgMu.Unlock()
	if ytImgProxyAddr == "" {
		return http.DefaultClient
	}
	if ytImgProxyClient == nil || ytImgClientAddr != ytImgProxyAddr {
		// Таймаут самого клиента = бюджет попытки: uTLS-клиент через SOCKS не
		// слушает контекст запроса и держал свои 10 с до провала (после правки
		// с бюджетом 4 с медиана 200-ответов осталась ровно 10 с).
		ytImgProxyClient = httpclient.NewTLSClientViaSOCKS5(ytImgProxyAddr, ytImgProxyBudget)
		ytImgClientAddr = ytImgProxyAddr
	}
	if ytImgProxyClient == nil {
		return http.DefaultClient
	}
	return ytImgProxyClient
}

const (
	// Бюджет попытки через выход YouTube. Раньше запасной прямой запрос делил с
	// ней один контекст на 10 с: мёртвый выход съедал всё, и «запасной» умирал
	// на старте с deadline exceeded — 502 ровно через 10 с на каждое превью
	// (после рестарта 21.09.2026: 70 × 502 против 4 × 200). Превью не привязано
	// к адресу, прямой путь с main работает — ему свой бюджет.
	ytImgProxyBudget  = 4 * time.Second
	ytImgDirectBudget = 8 * time.Second
)

// ytImgFallbacks — сколько превью ушли напрямую после провала выхода (диагностика).
var ytImgFallbacks atomic.Int64

// ytImgProxyDownUntil — до какого момента (unix-нано) выход считаем мёртвым и
// идём сразу напрямую: платить бюджет выхода за каждую картинку, пока он лежит,
// незачем — превью не привязано к адресу.
var ytImgProxyDownUntil atomic.Int64

const ytImgProxyDownFor = time.Minute

// ytImgFetch — превью через выход YouTube с коротким бюджетом, при провале —
// напрямую; после провала минуту идём напрямую сразу. cancel вызывать после
// чтения тела.
func ytImgFetch(parent context.Context, imgURL string) (*http.Response, context.CancelFunc, error) {
	one := func(c *http.Client, d time.Duration) (*http.Response, context.CancelFunc, error) {
		ctx, cancel := context.WithTimeout(parent, d)
		hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
		if err != nil {
			cancel()
			return nil, nil, err
		}
		hreq.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := c.Do(hreq)
		if err != nil {
			cancel()
			return nil, nil, err
		}
		return resp, cancel, nil
	}
	c := ytImgFetchClient()
	now := time.Now()
	if c != http.DefaultClient && now.UnixNano() >= ytImgProxyDownUntil.Load() {
		if resp, cancel, err := one(c, ytImgProxyBudget); err == nil {
			return resp, cancel, nil
		}
		ytImgFallbacks.Add(1)
		ytImgProxyDownUntil.Store(now.Add(ytImgProxyDownFor).UnixNano())
	}
	return one(http.DefaultClient, ytImgDirectBudget)
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

	resp, cancel, err := ytImgFetch(req.Context(), imgURL)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer cancel()
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
		// The mux died — but if it already produced segments, those are perfectly playable.
		// Handing the viewer a 500 mid-playback throws away everything that WAS downloaded;
		// serving the partial playlist with ENDLIST instead lets them watch on to wherever the
		// download reached and then end cleanly. A retry for the full video is started
		// separately by getOrStartMux.
		if n := muxSegmentCount(entry.Dir); n > 0 {
			log.Warn().Err(entry.Err).Str("key", key).Int("segments", n).
				Msg("youtube: mux failed — serving the segments that did complete")
			y.serveTruncatedPlaylist(w, req, entry)
			return
		}
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
	host := ytHost(req)
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
			VideoRange: pair.VideoRange,
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

	return host + "/lite/youtube/mux/master.m3u8?key=" + masterKey + "&v=" + url.QueryEscape(videoID)
}

// rebuildMaster пересобирает мастер после перезапуска: свежая экстракция → та же
// карта качеств → тот же детерминированный ключ. Дорого, поэтому только на промах.
func (y *YoutubeChecker) rebuildMaster(req *http.Request, links *proxylink.Manager, key string) (ytMasterEntry, bool) {
	videoID := strings.TrimSpace(req.URL.Query().Get("v"))
	if videoID == "" || links == nil {
		return ytMasterEntry{}, false
	}
	formats, _, usedProxy, ok := y.getFormats(videoID)
	if !ok || len(formats) == 0 {
		log.Warn().Str("videoID", videoID).Msg("youtube: мастер пересобрать не вышло — форматов нет")
		return ytMasterEntry{}, false
	}
	// buildQualityMap читает videoID из запроса и попутно регистрирует мастера.
	q := req.URL.Query()
	q.Set("videoID", videoID)
	req2 := req.Clone(req.Context())
	req2.URL.RawQuery = q.Encode()
	y.buildQualityMap(req2, formats, links, ytProxyHeaders(), usedProxy)

	y.muxMu.Lock()
	entry, found := y.masterCache[key]
	y.muxMu.Unlock()
	if !found {
		return ytMasterEntry{}, false
	}
	log.Info().Str("videoID", videoID).Msg("youtube: мастер-плейлист пересобран после перезапуска")
	return entry, true
}

// hlsAudioCodec возвращает кодек звуковой дорожки для атрибута CODECS.
//
// У HLS-форматов YouTube (режим HLS-only) yt-dlp сплошь и рядом не заполняет
// acodec, и в мастер уходило `CODECS="vp09.00.51.08"` — только видео, при
// объявленной аудиогруппе. Плеер строит буферы MSE по этой строке: не зная кодека
// звука, он не поднимает аудиодорожку, а с ней встаёт весь вариант — сегменты
// исправно качаются, картинки нет.
//
// Запасное значение не выдумано: сегменты этой дорожки — ADTS AAC (проверено
// ffprobe: заголовок ID3, поток aac), то есть mp4a.40.2.
func hlsAudioCodec(a ytFormat) string {
	c := strings.TrimSpace(a.ACodec)
	if c == "" || c == "none" {
		return "mp4a.40.2"
	}
	return c
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
// MuxMasterHandler returns the master-playlist handler bound to the proxy-link
// manager: без него пересобрать мастер нельзя — ссылки внутри проксируются.
func (y *YoutubeChecker) MuxMasterHandler(links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) { y.handleMuxMaster(w, req, links) }
}

// HandleMuxMaster serves the master playlist without rebuild support (совместимость
// со старой регистрацией маршрута; предпочтительнее MuxMasterHandler).
func (y *YoutubeChecker) HandleMuxMaster(w http.ResponseWriter, req *http.Request) {
	y.handleMuxMaster(w, req, nil)
}

func (y *YoutubeChecker) handleMuxMaster(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	key := strings.TrimSpace(req.URL.Query().Get("key"))
	if key == "" {
		http.Error(w, "missing key param", http.StatusBadRequest)
		return
	}

	y.muxMu.Lock()
	entry, ok := y.masterCache[key]
	y.muxMu.Unlock()
	if !ok || time.Now().After(entry.Expires) {
		// Реестр живёт в памяти процесса: перезапуск сервиса стирает его, и зритель
		// получал 404 посреди просмотра. Ключ детерминированный, а videoID лежит в
		// самой ссылке — значит мастер можно собрать заново, а не отфутболивать.
		if e2, ok2 := y.rebuildMaster(req, links, key); ok2 {
			entry = e2
		} else {
			http.Error(w, "master playlist not found — video may have expired, please reload", http.StatusNotFound)
			return
		}
	}

	// Sort variants by height ascending so hls.js builds the level list in
	// the order players usually expect (low → high).
	sortedVariants := append([]ytMasterVariant(nil), entry.Variants...)
	sort.Slice(sortedVariants, func(i, j int) bool {
		return sortedVariants[i].Height < sortedVariants[j].Height
	})

	// Звуковая группа объявляется один раз на весь мастер: у всех вариантов
	// HLS-only режима аудиодорожка общая.
	audioURL := ""
	for _, v := range sortedVariants {
		if v.AudioURL != "" {
			audioURL = v.AudioURL
			break
		}
	}

	var sb strings.Builder
	sb.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	if audioURL != "" {
		sb.WriteString(`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Audio",` +
			`DEFAULT=YES,AUTOSELECT=YES,URI="` + audioURL + "\"\n")
	}
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
		// Без VIDEO-RANGE плеер считает поток SDR даже при av01…10 с bt2020 в CODECS,
		// и HDR-конвейер телевизора не включается. SDR не пишем: атрибут появился в
		// HLS позже, и старые плееры на незнакомом значении спотыкаются.
		if v.VideoRange == "PQ" || v.VideoRange == "HLG" {
			sb.WriteString(",VIDEO-RANGE=")
			sb.WriteString(v.VideoRange)
		}
		if audioURL != "" {
			sb.WriteString(`,AUDIO="aud"`)
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
		// A job that FAILED must not be cached forever. ffmpeg dies when the download is
		// truncated by an anti-bot burst — a transient condition — yet the failed entry was
		// kept, so every later playlist request for that key returned 500 for the rest of the
		// key's life. That is what the viewer experiences as «plays for a minute, then error»:
		// the player re-fetches the growing EVENT playlist and gets a hard 500.
		//
		// Retry instead, but not in a hot loop: one attempt per ytMuxRetryAfter, and only a few
		// in total, so a genuinely dead video cannot spin ffmpeg and yt-dlp forever.
		if entry.Err != nil && entry.finished() && time.Since(entry.failedAt) >= ytMuxRetryAfter &&
			entry.attempts < ytMuxMaxAttempts {
			log.Info().Str("key", key).Int("attempt", entry.attempts+1).
				Msg("youtube: mux failed earlier — starting a fresh attempt")
			removeMuxDir(entry.Dir, "failed-retry")
			delete(y.muxCache, key)
			defer func(prev int) {
				if e, ok := y.muxCache[key]; ok {
					e.attempts = prev + 1
				}
			}(entry.attempts)
		} else {
			return entry
		}
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
	// HLS сюда доходить не должен: buildQualityMap отдаёт такие ссылки плееру
	// напрямую. Если всё же дошёл — пусть идёт прежним путём через пайп: там он
	// умрёт чисто («Invalid data found»), а не сегфолтом с дампом ядра, как это
	// делает ffmpeg, когда HLS скармливают ему URL'ом.
	if ytIsHLSManifest(videoURL) || ytIsHLSManifest(audioURL) {
		log.Warn().Msg("youtube: HLS-манифест дошёл до склейки — так быть не должно")
	}

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
		entry.failedAt = time.Now()
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
		entry.failedAt = time.Now()
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
		entry.failedAt = time.Now()
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

func (y *YoutubeChecker) probeStreamURL(streamURL string, fileSize int64) (ok bool) {
	start := time.Now()
	defer func() {
		log.Debug().Bool("ok", ok).Str("stream", ytURLDesc(streamURL)).
			Int64("filesize", fileSize).Dur("elapsed", time.Since(start)).
			Msg("youtube: stream URL probe")
	}()
	// Двухфазная проба. Первый килобайт ловит мёртвые URL/маршруты, но НЕ ловит
	// «тизерное окно» (2026-08-14: POT-less URL отдаёт первые ~20 МиБ и 403-ит
	// дальше — mux умирал на середине файла при зелёной пробе). Вторая фаза бьёт
	// за окно: 206 = полный доступ, 416 = файл короче офсета (тоже ок), 403 = яд.
	// Обе фазы запускаем СРАЗУ. Вторая нужна только когда прошла первая, но её
	// результат всё равно ждать — а последовательный запуск удваивал задержку
	// удачной пробы, то есть обычного случая. Ценой одного лишнего запроса по
	// килобайту на неудачной первой фазе.
	// Вторая фаза нужна ТОЛЬКО в POT-режиме через прокси, где окно коварно мигает.
	// В прямом POT-less режиме окно нормальное и лечится переминтом на лету
	// (свежая ссылка сбрасывает окно), поэтому упреждающе браковать URL по офсету
	// НЕЛЬЗЯ: это гнало живой android_vr в кулдаун и роняло зрителя в 360p.
	second := make(chan bool, 1)
	if offset, allow416, skip := probeSecondPhase(fileSize); !y.fetchPotNever && !skip {
		go func() {
			second <- y.raceProbe(streamURL, fmt.Sprintf("bytes=%d-%d", offset, offset+1023), allow416)
		}()
	} else {
		second <- true
	}
	if !y.raceProbe(streamURL, "bytes=0-1023", false) {
		return false
	}
	return <-second
}

// probeSecondPhase выбирает точку второй фазы пробы. Вынесено отдельно, чтобы
// правило «офсет обязан лежать ВНУТРИ файла» можно было проверить тестом.
func probeSecondPhase(fileSize int64) (offset int64, allow416, skip bool) {
	if fileSize <= 0 {
		// Размер неизвестен: бить можно только вслепую, и 416 остаётся законным
		// ответом («файл короче офсета»).
		return int64(teaserProbeOffset), true, false
	}
	if fileSize <= probeTailGap {
		return 0, false, true // файл целиком помещается в грейс-окно
	}
	// Бьём в САМЫЙ КОНЕЦ файла, а не в фиксированные 24 МиБ. Окно у разных
	// клиентов разное (mweb режется на ~16 МиБ, tvhtml5/visionos переживают 24
	// и падают дальше), поэтому единственная честная проверка «отдаётся ли
	// формат целиком» — попросить его последний килобайт. Доступен конец —
	// доступен и весь файл: окно всегда отсчитывается от начала.
	return fileSize - probeTailGap, false, false
}

const (
	// teaserProbeOffset — точка за «тизерным окном» googlevideo (замер 2026-08-30:
	// DASH-форматы отдаются до ~16-18 МиБ, дальше 403 на любом маршруте и с любой
	// свежей ссылкой).
	teaserProbeOffset = 24 << 20
	// probeTailGap — отступ от конца файла, чтобы проба заведомо попала ВНУТРЬ.
	probeTailGap = 64 << 10
)

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

// ytIsHLSManifest reports whether a stream URL is an HLS playlist rather than a
// progressive byte range. Такие адреса нельзя качать по диапазонам — из них ffmpeg
// должен читать сам.
func ytIsHLSManifest(rawURL string) bool {
	return strings.Contains(rawURL, "/api/manifest/hls") || strings.Contains(rawURL, ".m3u8")
}

// ── причина неудачного извлечения ──
//
// Раньше на ЛЮБОЙ провал зрителю показывали «Проверьте PO Token (pip install
// bgutil-ytdlp-pot-provider)». Для возрастного ролика это прямая дезинформация:
// POT в порядке, чинить нечего, а совет уводит в сторону. Поэтому запоминаем
// stderr последней попытки и переводим его в человеческую причину.
var extractReasons sync.Map // videoID → extractReason

type extractReason struct {
	stderr string
	at     time.Time
}

func rememberExtractReason(videoID, stderr string) {
	if videoID == "" {
		return
	}
	// Клиенты пробуются по очереди, и последний обычно падает невнятнее первого
	// («Requested format is not available» вместо «Video unavailable»). Уже
	// распознанную причину не затираем нераспознанной.
	if v, ok := extractReasons.Load(videoID); ok {
		if prev, _ := v.(extractReason); time.Since(prev.at) < time.Minute &&
			classifyExtractStderr(prev.stderr) != "" && classifyExtractStderr(stderr) == "" {
			return
		}
	}
	extractReasons.Store(videoID, extractReason{stderr: stderr, at: time.Now()})
}

// extractReasonFor возвращает текст для зрителя. Причина живёт недолго: ролик мог
// стать доступным, а старое объяснение только запутает.
func extractReasonFor(videoID string) string {
	const fallback = "yt-dlp не смог извлечь форматы видео. Проверьте PO Token (pip install bgutil-ytdlp-pot-provider)"
	v, ok := extractReasons.Load(videoID)
	if !ok {
		return fallback
	}
	r, _ := v.(extractReason)
	if time.Since(r.at) > 5*time.Minute {
		extractReasons.Delete(videoID)
		return fallback
	}
	if msg := classifyExtractStderr(r.stderr); msg != "" {
		return msg
	}
	return fallback
}

// classifyExtractStderr переводит stderr yt-dlp в причину для зрителя. Пустая
// строка — причина не распознана.
func classifyExtractStderr(stderr string) string {
	l := strings.ToLower(stderr)
	switch {
	case strings.Contains(l, "confirm your age"), strings.Contains(l, "age-restricted"):
		return "Видео с возрастным ограничением: YouTube отдаёт его только подтверждённому аккаунту. " +
			"Нужны куки аккаунта, прошедшего проверку возраста."
	case strings.Contains(l, "not a bot"), strings.Contains(l, "too many requests"):
		return "YouTube требует подтверждения, что запрос не от робота (IP под ограничением). " +
			"Обычно проходит само; если нет — сменить выход в [youtube] proxy."
	case strings.Contains(l, "private video"):
		return "Приватное видео — доступа нет."
	case strings.Contains(l, "members-only"), strings.Contains(l, "join this channel"):
		return "Видео только для спонсоров канала."
	case strings.Contains(l, "video unavailable"), strings.Contains(l, "removed by the uploader"):
		return "Видео недоступно: удалено или скрыто автором."
	case strings.Contains(l, "not available in your country"), strings.Contains(l, "blocked it in your country"):
		return "Видео заблокировано в стране сервера."
	case strings.Contains(l, "live event will begin"), strings.Contains(l, "premieres in"):
		return "Трансляция ещё не началась."
	}
	return ""
}

// ytProxyHeaders — заголовки, с которыми /proxy ходит на googlevideo. Вынесены из
// stream(), потому что тем же набором пересобирает ссылки обработчик мастера.
func ytProxyHeaders() map[string]string {
	return map[string]string{
		"Origin":     "https://www.youtube.com",
		"Referer":    "https://www.youtube.com/",
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	}
}

// ytItagOf returns the itag encoded in a googlevideo URL, or 0.
func ytItagOf(rawURL string) int {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(u.Query().Get("itag"))
	if err != nil {
		return 0
	}
	return n
}

// sabrDownload качает дорожку через sabr-сервис ([youtube] sabr_url) и пишет её в
// пайп ffmpeg. Возвращает true, если поток отдан (пусть даже оборванным на середине).
//
// Зачем вообще: обычная googlevideo-ссылка отдаёт ~16 МиБ и дальше отвечает 403 на
// любой Range — ролик рвётся на четвёртой минуте. SABR — тот же протокол, которым
// качает сам плеер YouTube: сессию можно переоткрыть с нужной позиции, а участки
// тянуть параллельно (троттлинг у YouTube посессионный, поэтому 12 сессий дают
// ×11 к скорости).
//
// Только видеодорожка. Аудио оставлено прежнему пути: во-первых, audio-only режим
// SABR у вещателя нестабилен (сервер перестаёт слать сегменты через ~45 с), во-вторых,
// у ролика бывает несколько аудиодорожек с ОДНИМ itag (дубляжи), и sabr-сервис выбрал
// бы не ту, что взял yt-dlp, — зритель получил бы чужой язык. Аудио весит копейки,
// и старый путь с ranged-докачкой его тянет без проблем.
//
// Провал ДО первого байта — не ошибка: возвращаем false и уходим на прежний путь,
// как будто SABR не настраивали. После первого байта отката уже нет (в пайпе лежат
// данные), поэтому обрыв помечается truncated ровно как у обычной загрузки.
func (y *YoutubeChecker) sabrDownload(ctx context.Context, entry *ytMuxEntry, streamURL string, w *os.File, label string) bool {
	if y.sabrURL == "" || label != "video" || entry == nil || entry.VideoID == "" {
		return false
	}
	itag := ytItagOf(streamURL)
	if itag == 0 {
		return false // не googlevideo-ссылка (HLS-манифест, например) — не наш случай
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(
		"%s/video?id=%s&track=video&itag=%d", y.sabrURL, url.QueryEscape(entry.VideoID), itag), nil)
	if err != nil {
		return false
	}
	// Сервис локальный, но первый байт ждёт инициализации сессии SABR, поэтому
	// таймаут только на заголовки — тело льётся сколько нужно.
	cl := &http.Client{Timeout: 0, Transport: &http.Transport{ResponseHeaderTimeout: 90 * time.Second}}
	resp, err := cl.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("video", entry.VideoID).Msg("youtube: sabr недоступен — идём обычным путём")
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Warn().Int("status", resp.StatusCode).Str("ответ", strings.TrimSpace(string(body))).
			Str("video", entry.VideoID).Msg("youtube: sabr отказал — идём обычным путём")
		return false
	}

	total := resp.ContentLength
	t0 := time.Now()
	written, cerr := io.Copy(w, resp.Body)
	if written == 0 {
		log.Warn().AnErr("err", cerr).Str("video", entry.VideoID).
			Msg("youtube: sabr не отдал ни байта — идём обычным путём")
		return false
	}
	if total > 0 && written < total {
		entry.truncated.Store(true)
	}
	sec := time.Since(t0).Seconds()
	ev := log.Info()
	if cerr != nil && ctx.Err() == nil {
		ev = log.Warn().Err(cerr)
	}
	ev.Int64("bytes", written).Int64("total", total).Int("itag", itag).
		Str("скорость", fmt.Sprintf("%.2f МБ/с", float64(written)/1048576/max(sec, 0.001))).
		Msg("youtube: sabr — видео скачано")
	return true
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

	// SABR — основной путь для видеодорожки, см. sabrDownload. Возвращает false,
	// не записав ни байта, если сервис недоступен или не смог начать: тогда
	// работает прежняя гонка стратегий, как будто SABR и не настраивали.
	if y.sabrDownload(ctx, entry, streamURL, w, label) {
		return
	}

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
		// Re-mint budget derived from the FILE, not a constant.
		//
		// It used to be a flat 6. But a POT-less URL dies after a fixed number of BYTES (measured
		// 2026-08-17: ~15 MiB on mweb, ~22 MiB on android_vr), so the number of re-mints a download
		// needs grows with file size: a 134 MB video needs ~8 and got 6 → the download stopped
		// mid-file, ffmpeg was handed a truncated stream and died with «Invalid NAL unit size».
		// That was 37 mux failures in 10 minutes. Budget = what the file actually needs, plus slack,
		// with a ceiling so a pathological case can't hammer the extractor forever.
		// The budget is derived from the FLOOR, not from the current estimate. Deriving it from
		// the estimate is self-defeating: an over-optimistic estimate both delays the proactive
		// swap AND shrinks the budget for recovering from the wall it failed to predict — which
		// is how prod ended up with truncated downloads and ffmpeg «partial file» / «Invalid NAL
		// unit size». Being generous here is cheap: re-mints are rate-limited and shared between
		// the two pipes, so the ceiling only has to stop a runaway loop.
		maxReminted := int(total/ytWindowFloor) + 8
		if maxReminted < 12 {
			maxReminted = 12
		}
		if maxReminted > 80 {
			maxReminted = 80
		}
		// Offset at which the current URL started serving — proactive re-mint uses it.
		mintedAt := written
		// When we last swapped the URL — feeds the rate limit below.
		lastRemint := time.Time{}
		// Consecutive failed re-mints; reset by any successful swap.
		mintFails := 0
		// Окно параллельных диапазонов. Ширина берётся из конфига ([youtube]
		// parallel_chunks): память под окно — width×1 МиБ на каждый пайп, а пайпов
		// вдвое больше числа mux-задач, поэтому потолок ставится осознанно.
		pf := &ytPrefetch{width: y.parallelChunks(), url: streamURL, ci: ci, next: written}
		defer pf.reset(0) // не оставлять висящих запросов, если вышли по ошибке
		for written < total && ctx.Err() == nil {
			// Proactive re-mint: swap the URL just BEFORE it hits its byte window instead of
			// discovering the wall with two 403s and a wait. The window size is learned at runtime
			// (ytWindowEstimate), so this keeps working if YouTube changes it.
			// The rate limit is the safety net that makes a wrong window estimate survivable.
			// Every re-mint is a yt-dlp extraction; on 2026-08-17 a collapsed estimate (3.5 MiB)
			// turned that into an extraction storm — «yt-dlp returned 0 formats» × 256 in ten
			// minutes as YouTube's anti-bot kicked in, torn seams, dead muxes. Whatever the
			// estimate says, one download re-mints at most this often; between swaps the reactive
			// 403 path still carries the file.
			if remint != nil && reminted < maxReminted && written-mintedAt >= ytWindowEstimate() &&
				time.Since(lastRemint) >= ytRemintMinInterval {
				if fresh := remint(streamURL); fresh != "" && fresh != streamURL {
					lastRemint = time.Now()
					streamURL = fresh
					ua, origin, referer = ytCDNHeaders(streamURL)
					mintedAt = written
					reminted++
					log.Debug().Int64("bytes", written).Int("remint", reminted).Str("label", label).
						Msg("youtube: pipe download — proactive re-mint before the byte window")
				} else {
					// Extraction failed. Do NOT advance mintedAt — that pretends a swap happened,
					// silences the proactive path for another full window and sails the download
					// straight into the wall (prod 2026-08-24: 161 reactive re-mints against 56
					// proactive ones, i.e. we were mostly paying for the wall we meant to avoid).
					// Keep the threshold armed and simply retry after the rate limit.
					lastRemint = time.Now()
					log.Debug().Int64("bytes", written).Str("label", label).
						Msg("youtube: pipe download — proactive re-mint unavailable, will retry")
				}
			}
			// Окно набирается заново, как только сменились ссылка или маршрут:
			// куски, добытые прежними параметрами, доверия не заслуживают.
			if pf.url != streamURL || pf.ci != ci {
				pf.reset(written)
				pf.url, pf.ci = streamURL, ci
			}
			// Не забегаем за байтовое окно текущей ссылки — иначе в 403 упрётся
			// сразу всё окно, а не один запрос.
			limit := total
			if remint != nil {
				if wall := mintedAt + ytWindowEstimate(); wall < limit {
					limit = wall
				}
			}
			pf.fill(ctx, y, clients[ci], streamURL, ua, origin, referer, limit, total)
			cn, cerr := pf.take(ctx, w)
			written += cn
			if cerr != nil {
				pf.reset(written) // хвост окна добыт тем же, что сейчас не сработало
			}
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
					log.Warn().Int64("bytes", written).Int64("total", total).Str("label", label).
						Int("remint", reminted).Int("budget", maxReminted).
						Msg("youtube: pipe download — giving up: re-mint budget exhausted")
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
					// Extraction failed right when we needed it — YouTube's anti-bot comes and
					// goes in bursts. Closing the pipe here truncates the file and ffmpeg exits
					// with «partial file» / «Invalid NAL unit size», i.e. the viewer gets a hard
					// 500. A pipe that merely PAUSES is far cheaper: the player keeps its buffer
					// and usually never notices. So back off and try again before giving up.
					mintFails++
					if mintFails <= mintFailRetries {
						// Hold the rate limit too, or the proactive block at the top of the loop
						// fires its own extraction the moment we come back round.
						lastRemint = time.Now()
						log.Warn().Int64("bytes", written).Str("label", label).Int("attempt", mintFails).
							Msg("youtube: pipe download — no fresh URL, backing off")
						select {
						case <-ctx.Done():
						case <-time.After(time.Duration(mintFails) * 2500 * time.Millisecond):
						}
						continue
					}
					log.Warn().Int64("bytes", written).Int64("total", total).Str("label", label).
						Int("remint", reminted).Msg("youtube: pipe download — giving up: no fresh URL")
					err = cerr
					break
				}
				mintFails = 0
				// The 403 tells us how many bytes this URL actually served — feed it back so the
				// proactive path swaps earlier next time and we stop paying for the wall.
				ytLearnWindow(written - mintedAt)
				mintedAt = written
				lastRemint = time.Now()
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

// ---------------------------------------------------------------------------
//  Параллельная докачка диапазонов
// ---------------------------------------------------------------------------

// ytParallelChunksDefault — во сколько потоков докачивать файл. Восемь: замер дал
// 8.4 МБ/с на шести и 20 МБ/с на двенадцати против 2.1 на одном, а платим за это
// памятью (width×1 МиБ на пайп, пайпов вдвое больше mux-задач: 8 → до 224 МиБ на
// полностью забитом ytMuxMaxSize). Восемь — примерно 10-кратный запас над битрейтом
// 1080p при вменяемом потолке памяти.
const ytParallelChunksDefault = 8

// ytChunkSlots — потолок ОДНОВРЕМЕННЫХ ranged-запросов по всем загрузкам сразу.
// Без него ширина окна множится на число пайпов (ytMuxMaxSize×2 = 28) и даёт под
// 224 запроса и столько же мегабайт буферов — а мы только что видели, чем YouTube
// отвечает на всплеск активности с одного IP. 64 — вчетверо больше замеренной
// точки насыщения (×12 дали 20 МБ/с), так что скорость это не ограничивает.
var ytChunkSlots = make(chan struct{}, 64)

// autoHeightCap — до какой высоты разрешено авто-качество. Потолок 1080p ставился,
// когда докачка была последовательной (0.34-2 МБ/с) и 4K физически не успевал за
// воспроизведением. С параллельной докачкой (8-20 МБ/с) 4K укладывается, но по
// умолчанию потолок оставлен прежним: каждая склейка — это реальная закачка файла,
// и переводить всех зрителей на 4K молча нельзя. Поднимается через
// [youtube] max_auto_height = 2160.
func (y *YoutubeChecker) autoHeightCap() int {
	if y.maxAutoHeight > 0 {
		return y.maxAutoHeight
	}
	return ytDefaultMuxHeight
}

func (y *YoutubeChecker) parallelChunks() int {
	n := y.parChunks
	if n <= 0 {
		n = ytParallelChunksDefault
	}
	if n > 32 {
		n = 32 // выше начинается уже не ускорение, а анти-бот
	}
	return n
}

// ytPrefetch — окно ranged-запросов, идущих ОДНОВРЕМЕННО, при строго упорядоченной
// выдаче в пайп ffmpeg.
//
// Зачем. googlevideo душит каждое соединение по отдельности, поэтому один
// последовательный поток упирается в ~2 МБ/с, а на длинном ролике — и в 0.3.
// Замер на проде 2026-08-31 (1080p, через тот же маршрут, каким ходит lampac):
//
//	последовательно   2.09 МБ/с
//	параллельно ×6    8.42 МБ/с
//	параллельно ×12  20.26 МБ/с
//
// Мультиплексирования тут нет: клиенту нужен непрерывный файл, а не куски
// вразнобой, поэтому чанки скачиваются вперёд в память, а отдаются строго по
// порядку. Первый чанк стартует сразу, так что плеер начинает играть не дожидаясь
// остальных.
//
// Окно набирается ТОЛЬКО в пределах байтового окна текущей ссылки (см. mintedAt в
// вызывающем коде): забежать за него — значит нарваться на 403 всеми запросами
// разом вместо одного и потратить впустую бюджет ретраев.
type ytPrefetch struct {
	width int
	next  int64 // офсет, с которого набирается следующий чанк
	jobs  []*ytChunkJob
	url   string // параметры, на которых набрано окно; их смена обнуляет его
	ci    int
}

type ytChunkJob struct {
	start, end int64
	buf        bytes.Buffer
	n          int64
	err        error
	done       chan struct{}
	cancel     context.CancelFunc
}

// reset бросает всё набранное и переставляет окно на offset. Вызывается при смене
// ссылки/маршрута и после любой ошибки: продолжать с уже набранными кусками нельзя —
// они добыты тем, что только что не сработало.
func (p *ytPrefetch) reset(offset int64) {
	for _, j := range p.jobs {
		j.cancel()
	}
	p.jobs = nil
	p.next = offset
}

// fill догоняет окно до width заданий, не заходя за limit.
func (p *ytPrefetch) fill(ctx context.Context, y *YoutubeChecker, client *http.Client,
	streamURL, ua, origin, referer string, limit, total int64) {
	// Один чанк ставится ВСЕГДА, даже за limit: иначе, если оценка окна занижена,
	// докачка встанет насмерть, так и не дав реактивному пути поймать 403 и
	// поправить оценку. Ограничение сдерживает только забег вперёд.
	for len(p.jobs) < p.width && p.next < total && (len(p.jobs) == 0 || p.next < limit) {
		end := p.next + ytChunkSize - 1
		if end >= total {
			end = total - 1
		}
		jctx, cancel := context.WithCancel(ctx)
		j := &ytChunkJob{start: p.next, end: end, done: make(chan struct{}), cancel: cancel}
		p.jobs = append(p.jobs, j)
		p.next = end + 1
		go func() {
			defer close(j.done)
			select {
			case ytChunkSlots <- struct{}{}:
				defer func() { <-ytChunkSlots }()
			case <-jctx.Done():
				j.err = jctx.Err()
				return
			}
			j.n, j.err = y.fetchChunk(jctx, client, streamURL, ua, origin, referer, j.start, j.end, &j.buf)
		}()
	}
}

// take отдаёт голову окна в w, дождавшись её. Возвращает (0, nil), когда окно пусто.
func (p *ytPrefetch) take(ctx context.Context, w io.Writer) (int64, error) {
	if len(p.jobs) == 0 {
		return 0, nil
	}
	j := p.jobs[0]
	select {
	case <-j.done:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	p.jobs = p.jobs[1:]
	j.cancel()
	if j.err != nil {
		return 0, j.err
	}
	// Записываем ТОЛЬКО целиком добранный чанк: половина куска в середине пайпа —
	// это шов, на котором ffmpeg спотыкается «Invalid NAL unit size».
	if j.n != j.end-j.start+1 {
		return 0, fmt.Errorf("chunk %d-%d: получено %d из %d", j.start, j.end, j.n, j.end-j.start+1)
	}
	n, err := w.Write(j.buf.Bytes())
	return int64(n), err
}

// remintFunc returns a closure that re-extracts a FRESH stream URL for the same
// itag as `oldURL` — the fix for POT flaps that outlast the wait budget: the CDN
// permanently 403s a given (URL, offset past the grace window), but a freshly
// minted POT URL for the same itag downloads from where the old one died (byte
// ranges are identical across URLs of one itag). Empty videoID or itag → no-op.
func (y *YoutubeChecker) remintFunc(videoID string) func(oldURL string) string {
	// The extraction itself lives in mintFreshURL, which collapses the video and audio
	// pipes of one mux job (and any concurrent viewers) into a single yt-dlp run.
	return func(oldURL string) string {
		return y.mintFreshURL(videoID, oldURL)
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
	// ★22.08: nil-writer = «скачать и выбросить» (самотест watchdog'а). Раньше io.Copy(nil, …)
	// ронял ВЕСЬ процесс паникой, как только YouTube начинал отдавать честные 206 — 28 рестартов
	// подряд каждые ~2 минуты (90с прогрев + первый чанк самотеста).
	if w == nil {
		w = io.Discard
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
	y.pruneMuxKeys(now)

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
			// The DIRECTORY goes (that is the disk cost). The KEY REGISTRY stays.
			//
			// Deleting the recipe together with the files is what produced 7765 hard 404s in two
			// hours on prod: a player holds a `?key=…` URL, the quality map it came from lives 4h
			// (ytCacheTTL) and the page may live longer still, but the mux dir expires after 30
			// minutes — and with the mapping gone findOrStartMuxByKey can no longer even RESTART
			// the mux, so every later request is a permanent «mux job not found». Keeping the
			// mapping turns that dead end into a transparent re-mux (the code below already starts
			// one whenever the key is known but no entry exists). The registry is a few strings per
			// key and is bounded separately by pruneMuxKeys.
			removeMuxDir(e.Dir, "cleanup-expired")
			delete(y.muxCache, k)
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

	host := ytHost(req)
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

	host := ytHost(r)
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

	host := ytHost(r)
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

	host := ytHost(r)
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
