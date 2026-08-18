package sisihttp

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/torrs"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/rs/zerolog/log"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

var (
	// Tracker listing: <a class="med tLink bold" href="./viewtopic.php?t=3270113">Title [...]</a>
	plabTopicRe = regexp.MustCompile(`(?is)<a\s+class="med tLink[^"]*"\s+href="\./viewtopic\.php\?t=(\d+)"[^>]*>(.+?)</a>`)
	// Download link + size: <a class="small tr-dl dl-stub" href="dl.php?t=3270113">2.83&nbsp;GB</a>
	plabDLRe = regexp.MustCompile(`(?is)<a\s+class="[^"]*dl-stub[^"]*"\s+href="dl\.php\?t=(\d+)"[^>]*>([^<]+)</a>`)
	// Seeds: <b class="seedmed">78</b>
	plabSeedsRe = regexp.MustCompile(`(?is)<b\s+class="seedmed">(\d+)</b>`)
	// Topic page poster: class="postImg postImgAligned img-right" title="https://..."
	plabPosterRe = regexp.MustCompile(`(?is)class="postImg[^"]*postImgAligned[^"]*"\s+title="(https?://[^"]+)"`)
	// Any postImg for fallback: class="postImg" title="https://..."
	plabAnyImgRe     = regexp.MustCompile(`(?is)class="postImg[^"]*"\s+title="(https?://[^"]+(?:\.jpe?g|\.png|\.webp)[^"]*)"`)
	plabTopicTitleRe = regexp.MustCompile(`(?is)<title>\s*(.+?)\s*(?:::?\s*.+?)?\s*</title>`)

	validPlabPlugin = map[string]struct{}{"plab": {}}
)

type sisiPornLabSource struct {
	cfg    config.Config
	client *http.Client
	host   string

	mu        sync.Mutex
	authed    bool
	authAt    time.Time
	cookieJar http.CookieJar
}

func newSisiPornLabSource(cfg config.Config) *sisiPornLabSource {
	jar, _ := cookiejar.New(nil)
	return &sisiPornLabSource{
		cfg:       cfg,
		client:    &http.Client{Timeout: 20 * time.Second, Jar: jar},
		host:      SisiSourceHost("PornLab", "https://pornolab.net"),
		cookieJar: jar,
	}
}

// requestClient returns a per-user HTTP client if Kit has a PornLab cookie override,
// otherwise returns the global client (after ensuring global auth).
// The boolean indicates whether per-user auth was used.
func (s *sisiPornLabSource) requestClient(r *http.Request) (*http.Client, bool) {
	if perUserCookie, ok := kit.CookieOverride(r.Context(), "PornLab"); ok && perUserCookie != "" {
		jar, _ := cookiejar.New(nil)
		u, _ := url.Parse(s.host)
		jar.SetCookies(u, []*http.Cookie{
			{Name: "bb_data", Value: perUserCookie, Path: "/forum/"},
		})
		return &http.Client{Timeout: 20 * time.Second, Jar: jar}, true
	}
	return nil, false
}

// ensureAuth authenticates if cookie is configured or login/password provided.
func (s *sisiPornLabSource) ensureAuth(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-auth every 30 min.
	if s.authed && time.Since(s.authAt) < 30*time.Minute {
		return nil
	}

	state := loadSisiRuntimeCfg()
	srcCfg, ok := state.Sources["PornLab"]
	if !ok {
		return fmt.Errorf("PornLab not configured")
	}

	cookie := strings.TrimSpace(srcCfg.Cookie)
	login := strings.TrimSpace(srcCfg.Login)
	password := strings.TrimSpace(srcCfg.Password)

	log.Debug().Str("login", login).Bool("has_password", password != "").Bool("has_cookie", cookie != "").Msg("pornlab: auth credentials")

	if cookie != "" {
		// Set cookie directly.
		u, _ := url.Parse(s.host)
		s.cookieJar.SetCookies(u, []*http.Cookie{
			{Name: "bb_data", Value: cookie, Path: "/forum/"},
		})
		s.authed = true
		s.authAt = time.Now()
		return nil
	}

	if login != "" && password != "" {
		return s.loginWithCredentials(ctx, login, password)
	}

	return fmt.Errorf("PornLab: no cookie or login/password configured")
}

func (s *sisiPornLabSource) loginWithCredentials(ctx context.Context, login, password string) error {
	form := url.Values{
		"login_username": {login},
		"login_password": {password},
		"login":          {"\xC2\xF5\xEE\xE4"}, // "Вход" in windows-1251
	}

	loginURL := strings.TrimRight(s.host, "/") + "/forum/login.php"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	// Check if bb_data cookie was set (cookie has path=/forum/).
	u, _ := url.Parse(s.host + "/forum/")
	for _, c := range s.cookieJar.Cookies(u) {
		if c.Name == "bb_data" && len(c.Value) > 10 {
			s.authed = true
			s.authAt = time.Now()
			return nil
		}
	}
	return fmt.Errorf("PornLab login failed: no bb_data cookie received")
}

func (s *sisiPornLabSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := validPlabPlugin[sisiSourcePluginFromPath(r.URL.Path)]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		// Check for per-user Kit cookie first.
		perClient, hasPerUser := s.requestClient(r)
		if !hasPerUser {
			if err := s.ensureAuth(r.Context()); err != nil {
				log.Warn().Err(err).Msg("pornlab: auth failed")
				writeJSON(w, http.StatusOK, map[string]any{
					"list":        []any{},
					"total_pages": 1,
				})
				return
			}
		}

		search := strings.TrimSpace(r.URL.Query().Get("search"))
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)

		// Unified category filter (PornLab uses search fallback with Russian terms).
		if cat := r.URL.Query().Get("cat"); cat != "" {
			res := resolveSisiCategory("plab", cat)
			if res.SearchTerm != "" {
				search = res.SearchTerm
			}
		}

		targetURL := s.listURL(search, pg)
		log.Debug().Str("url", targetURL).Bool("per_user", hasPerUser).Msg("pornlab: fetching tracker")

		fetchClient := s.client
		if hasPerUser {
			fetchClient = perClient
		}
		html, err := s.fetchHTMLWith(r.Context(), fetchClient, targetURL)
		if err != nil {
			log.Warn().Err(err).Str("url", targetURL).Msg("pornlab: fetch failed")
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		log.Debug().Int("html_len", len(html)).Msg("pornlab: HTML fetched")
		items := s.parseTrackerList(r.Context(), hostFromRequest(r), html)
		log.Debug().Int("items", len(items)).Msg("pornlab: parsed items")

		writeJSON(w, http.StatusOK, map[string]any{
			"list":        items,
			"total_pages": 10,
		})
	}
}

func (s *sisiPornLabSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		topicID := strings.TrimSpace(r.URL.Query().Get("t"))
		if topicID == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}})
			return
		}

		// Check for per-user Kit cookie first.
		perClient, hasPerUser := s.requestClient(r)
		if !hasPerUser {
			if err := s.ensureAuth(r.Context()); err != nil {
				log.Warn().Err(err).Str("topic", topicID).Msg("pornlab: auth failed in viewHandler")
				writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}})
				return
			}
		}

		fetchClient := s.client
		if hasPerUser {
			fetchClient = perClient
		}

		host := hostFromRequest(r)

		// Fetch topic page for poster + title.
		topicHTML, _ := s.fetchHTMLWith(r.Context(), fetchClient, strings.TrimRight(s.host, "/")+"/forum/viewtopic.php?t="+topicID)
		poster := strings.TrimSpace(submatch1(plabPosterRe, topicHTML))
		if poster == "" {
			poster = strings.TrimSpace(submatch1(plabAnyImgRe, topicHTML))
		}
		title := strings.TrimSpace(submatch1(plabTopicTitleRe, topicHTML))
		if title == "" {
			title = "PornLab #" + topicID
		}

		// Download .torrent file from pornolab (with auth cookies).
		torrentData, err := s.downloadTorrentWith(r.Context(), fetchClient, topicID)
		if err != nil {
			log.Warn().Err(err).Str("topic", topicID).Msg("pornlab: torrent download failed")
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}})
			return
		}

		log.Debug().Str("topic", topicID).Int("bytes", len(torrentData)).Msg("pornlab: torrent downloaded")

		// Try embedded TorrServer first (built with -torrs tag).
		tsSrv := getTorrsServer()
		if tsSrv != nil {
			log.Debug().Str("topic", topicID).Msg("pornlab: using embedded TorrServer")
			info, err := tsSrv.AddFromBytes(torrentData, title, poster, "", false)
			if err != nil {
				log.Warn().Err(err).Str("topic", topicID).Msg("pornlab: embedded TorrServer add failed")
				writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}})
				return
			}

			qualitys := plabBuildQualitys(host, info)
			writeJSON(w, http.StatusOK, map[string]any{
				"qualitys": qualitys,
				"title":    title,
				"poster":   poster,
			})
			return
		}

		log.Debug().Str("topic", topicID).Msg("pornlab: using external TorrServer")

		// Upload .torrent directly to external TorrServer via multipart /torrent/upload.
		// Magnet links don't work for private trackers (need tracker auth cookies).
		// Data URIs not supported by TorrServer. Direct upload provides metadata immediately.
		info, err := plabUploadToExternalTS(r.Context(), s.cfg, torrentData, title, poster)
		if err != nil {
			log.Warn().Err(err).Str("topic", topicID).Msg("pornlab: external TorrServer add failed")
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}})
			return
		}

		qualitys := plabBuildQualitys(host, info)
		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys": qualitys,
			"title":    title,
			"poster":   poster,
		})
	}
}

// downloadTorrent downloads .torrent using the global client.
func (s *sisiPornLabSource) downloadTorrent(ctx context.Context, topicID string) ([]byte, error) {
	return s.downloadTorrentWith(ctx, s.client, topicID)
}

// downloadTorrentWith downloads .torrent using the specified client (per-user or global).
func (s *sisiPornLabSource) downloadTorrentWith(ctx context.Context, client *http.Client, topicID string) ([]byte, error) {
	dlURL := strings.TrimRight(s.host, "/") + "/forum/dl.php?t=" + topicID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	// Manually attach cookies from the client's jar.
	if client.Jar != nil {
		u, _ := url.Parse(strings.TrimRight(s.host, "/") + "/forum/")
		for _, c := range client.Jar.Cookies(u) {
			req.AddCookie(c)
		}
	}

	// Use a non-redirect client: if dl.php returns 302 → login.php, we want to detect it.
	noRedirectClient := &http.Client{
		Timeout:   20 * time.Second,
		Jar:       client.Jar,
		Transport: client.Transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := noRedirectClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 302 means auth failed — dl.php redirects to login page.
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		return nil, fmt.Errorf("dl.php redirected (auth expired?), status %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dl.php returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50MB limit
	if err != nil {
		return nil, err
	}
	if len(data) < 20 {
		return nil, fmt.Errorf("torrent file too small (%d bytes)", len(data))
	}
	// Validate it's actually a torrent file (bencode starts with 'd').
	if data[0] != 'd' {
		preview := string(data[:min(200, len(data))])
		log.Warn().Str("topic", topicID).Int("bytes", len(data)).
			Str("preview", preview).Msg("pornlab: dl.php returned non-torrent data (likely HTML auth page)")
		return nil, fmt.Errorf("dl.php returned HTML instead of torrent (%d bytes)", len(data))
	}
	return data, nil
}

// plabTorrentCache stores downloaded .torrent bytes for external TorrServer flow.
var plabTorrentCache = struct {
	mu   sync.RWMutex
	data map[string][]byte
}{data: make(map[string][]byte)}

// torrentProxyHandler serves .torrent files to external TorrServer.
// First checks in-memory cache (from viewHandler), then fetches from pornolab.
func (s *sisiPornLabSource) torrentProxyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		topicID := strings.TrimSpace(r.URL.Query().Get("t"))
		if topicID == "" {
			http.Error(w, "missing t param", http.StatusBadRequest)
			return
		}

		// Check in-memory cache first (set by viewHandler for external TorrServer).
		plabTorrentCache.mu.RLock()
		cached := plabTorrentCache.data[topicID]
		plabTorrentCache.mu.RUnlock()

		if len(cached) > 0 {
			w.Header().Set("Content-Type", "application/x-bittorrent")
			w.Header().Set("Content-Length", strconv.Itoa(len(cached)))
			_, _ = w.Write(cached)
			return
		}

		// Fallback: fetch from pornolab.
		if err := s.ensureAuth(r.Context()); err != nil {
			http.Error(w, "auth failed", http.StatusUnauthorized)
			return
		}

		dlURL := strings.TrimRight(s.host, "/") + "/forum/dl.php?t=" + topicID
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, dlURL, nil)
		if err != nil {
			http.Error(w, "request error", http.StatusInternalServerError)
			return
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

		resp, err := s.client.Do(req)
		if err != nil {
			http.Error(w, "download error", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", "application/x-bittorrent")
		if ct := resp.Header.Get("Content-Disposition"); ct != "" {
			w.Header().Set("Content-Disposition", ct)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

// plabVideoExts contains recognized video file extensions.
var plabVideoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".wmv": true,
	".mov": true, ".flv": true, ".webm": true, ".m4v": true,
	".mpeg": true, ".mpg": true, ".ts": true, ".m2ts": true,
	".vob": true, ".divx": true, ".3gp": true, ".ogv": true,
}

// plabIsVideoFile checks if a file path has a video extension.
func plabIsVideoFile(path string) bool {
	name := strings.ToLower(path)
	for ext := range plabVideoExts {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// plabBuildQualitys builds quality map from TorrentInfo file list.
func plabBuildQualitys(host string, info *torrs.TorrentInfo) map[string]any {
	qualitys := map[string]any{}
	if info == nil || len(info.FileStats) == 0 {
		log.Debug().Msg("pornlab: no file stats in torrent info")
		return qualitys
	}

	log.Debug().Int("files", len(info.FileStats)).Str("hash", info.Hash).Msg("pornlab: building qualitys")

	videoIndex := 0
	for _, f := range info.FileStats {
		if !plabIsVideoFile(f.Path) {
			continue
		}
		label := f.Path
		if f.Length > 0 {
			label = fmt.Sprintf("%s (%.1f GB)", f.Path, float64(f.Length)/(1024*1024*1024))
		}
		streamURL := host + "/ts/stream?link=" + info.Hash + "&index=" + fmt.Sprintf("%d", f.ID) + "&play"
		qualitys[label] = streamURL
		if videoIndex == 0 {
			qualitys["auto"] = streamURL
		}
		videoIndex++
	}

	// Fallback: use the largest file regardless of extension.
	if len(qualitys) == 0 {
		best := info.FileStats[0]
		for _, f := range info.FileStats[1:] {
			if f.Length > best.Length {
				best = f
			}
		}
		log.Debug().Str("file", best.Path).Int64("size", best.Length).Msg("pornlab: no video ext matched, using largest file")
		qualitys["auto"] = host + "/ts/stream?link=" + info.Hash + "&index=" + fmt.Sprintf("%d", best.ID) + "&play"
	}

	return qualitys
}

// plabAddViaHTTPAPI adds a torrent to external TorrServer via its HTTP API.
// For magnet links, metadata may not be available immediately — polls until file_stats appear.
func plabAddViaHTTPAPI(ctx context.Context, lampacHost, link, title, poster string) (*torrs.TorrentInfo, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	tsURL := lampacHost + "/ts/torrents"

	// Step 1: Add the torrent.
	addPayload := map[string]any{
		"action":     "add",
		"link":       link,
		"title":      title,
		"poster":     poster,
		"save_to_db": false,
	}
	info, err := plabTSPost(ctx, client, tsURL, addPayload)
	if err != nil {
		return nil, fmt.Errorf("add: %w", err)
	}
	if info.Hash == "" {
		return nil, fmt.Errorf("TorrServer returned empty hash")
	}

	// If file_stats already present (e.g. torrent URL or already cached), return immediately.
	if len(info.FileStats) > 0 {
		return info, nil
	}

	// Step 2: Poll "get" until metadata (file_stats) appears.
	log.Debug().Str("hash", info.Hash).Msg("pornlab: waiting for TorrServer metadata...")

	getPayload := map[string]any{
		"action": "get",
		"hash":   info.Hash,
	}

	deadline := time.After(30 * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("metadata timeout for %s (30s)", info.Hash)
		case <-ticker.C:
			got, err := plabTSPost(ctx, client, tsURL, getPayload)
			if err != nil {
				log.Debug().Err(err).Msg("pornlab: TS get poll error")
				continue
			}
			if len(got.FileStats) > 0 {
				log.Debug().Int("files", len(got.FileStats)).Str("hash", info.Hash).Msg("pornlab: metadata received")
				got.Title = info.Title
				got.Poster = info.Poster
				return got, nil
			}
		}
	}
}

// plabTSPost sends a JSON POST to the TorrServer /torrents endpoint and parses the response.
func plabTSPost(ctx context.Context, client *http.Client, tsURL string, payload map[string]any) (*torrs.TorrentInfo, error) {
	body, _ := stdjson.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tsURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var info torrs.TorrentInfo
	if err := stdjson.Unmarshal(respBody, &info); err != nil {
		return nil, fmt.Errorf("parse response: %w (body: %s)", err, string(respBody[:min(len(respBody), 200)]))
	}
	return &info, nil
}

// plabUploadToExternalTS uploads .torrent bytes directly to external TorrServer
// via POST /torrent/upload (multipart form). Returns torrent info with file_stats.
func plabUploadToExternalTS(ctx context.Context, cfg config.Config, torrentData []byte, title, poster string) (*torrs.TorrentInfo, error) {
	// Determine external TorrServer URL.
	tsBase := strings.TrimSpace(cfg.TorrServer.URL)
	if tsBase == "" {
		tsBase = fmt.Sprintf("http://127.0.0.1:%d", cfg.TorrServer.Port)
	}
	tsBase = strings.TrimRight(tsBase, "/")

	// Build multipart form with torrent file.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "torrent.torrent")
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := fw.Write(torrentData); err != nil {
		return nil, fmt.Errorf("write torrent data: %w", err)
	}
	// Add title and poster as form fields.
	_ = w.WriteField("title", title)
	_ = w.WriteField("poster", poster)
	_ = w.WriteField("save_to_db", "false")
	w.Close()

	uploadURL := tsBase + "/torrent/upload"
	log.Debug().Str("url", uploadURL).Int("torrentBytes", len(torrentData)).Msg("pornlab: uploading to external TS")

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	// Add auth header if configured.
	if cfg.TorrServer.Login != "" && cfg.TorrServer.Password != "" {
		req.SetBasicAuth(cfg.TorrServer.Login, cfg.TorrServer.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	log.Debug().Int("status", resp.StatusCode).Int("bodyLen", len(respBody)).Msg("pornlab: TS upload response")

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upload HTTP %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var info torrs.TorrentInfo
	if err := stdjson.Unmarshal(respBody, &info); err != nil {
		return nil, fmt.Errorf("parse upload response: %w (body: %s)", err, string(respBody[:min(len(respBody), 200)]))
	}

	if info.Hash == "" {
		return nil, fmt.Errorf("TorrServer returned empty hash after upload")
	}

	// Upload should return file_stats immediately since metadata is in the .torrent file.
	// If not, brief poll.
	if len(info.FileStats) > 0 {
		return &info, nil
	}

	log.Debug().Str("hash", info.Hash).Msg("pornlab: upload OK but no file_stats, polling...")

	// Poll via /torrents API (direct to external TS).
	torrentsURL := tsBase + "/torrents"
	getPayload := map[string]any{"action": "get", "hash": info.Hash}
	deadline := time.After(10 * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return &info, nil // return what we have
		case <-ticker.C:
			got, err := plabTSPostDirect(ctx, client, torrentsURL, getPayload, cfg)
			if err != nil {
				continue
			}
			if len(got.FileStats) > 0 {
				got.Title = title
				got.Poster = poster
				return got, nil
			}
		}
	}
}

// plabTSPostDirect sends a JSON POST directly to external TorrServer (not through /ts/ proxy).
func plabTSPostDirect(ctx context.Context, client *http.Client, tsURL string, payload map[string]any, cfg config.Config) (*torrs.TorrentInfo, error) {
	body, _ := stdjson.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tsURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.TorrServer.Login != "" && cfg.TorrServer.Password != "" {
		req.SetBasicAuth(cfg.TorrServer.Login, cfg.TorrServer.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var info torrs.TorrentInfo
	if err := stdjson.Unmarshal(respBody, &info); err != nil {
		return nil, fmt.Errorf("parse response: %w (body: %s)", err, string(respBody[:min(len(respBody), 200)]))
	}
	return &info, nil
}

// plabTorrentToMagnet converts raw .torrent bytes to a magnet link.
// Includes tracker announce URLs so external TorrServer can find peers.
func plabTorrentToMagnet(data []byte) (string, error) {
	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("parse torrent: %w", err)
	}

	hash := mi.HashInfoBytes()
	magnet := "magnet:?xt=urn:btih:" + hash.HexString()

	info, err := mi.UnmarshalInfo()
	if err == nil && info.Name != "" {
		magnet += "&dn=" + url.QueryEscape(info.Name)
	}

	// Add trackers from announce + announce-list.
	seen := map[string]bool{}
	addTracker := func(tr string) {
		tr = strings.TrimSpace(tr)
		if tr != "" && !seen[tr] {
			seen[tr] = true
			magnet += "&tr=" + url.QueryEscape(tr)
		}
	}
	addTracker(mi.Announce)
	for _, tier := range mi.AnnounceList {
		for _, tr := range tier {
			addTracker(tr)
		}
	}

	return magnet, nil
}

func (s *sisiPornLabSource) fetchHTML(ctx context.Context, target string) (string, error) {
	return s.fetchHTMLWith(ctx, s.client, target)
}

func (s *sisiPornLabSource) fetchHTMLWith(ctx context.Context, client *http.Client, target string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// pornolab uses windows-1251 encoding — always decode.
	reader := transform.NewReader(resp.Body, charmap.Windows1251.NewDecoder())
	body, err := io.ReadAll(io.LimitReader(reader, 2<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (s *sisiPornLabSource) listURL(search string, pg int) string {
	base := strings.TrimRight(s.host, "/") + "/forum/tracker.php"
	params := url.Values{}

	if search != "" {
		params.Set("nm", search)
	}

	// Default porn forums.
	forums := []string{"1670", "1768", "487", "1111", "508", "555"}
	for _, f := range forums {
		params.Add("f[]", f)
	}

	// Sort by seeds descending.
	params.Set("o", "10") // seeds
	params.Set("s", "2")  // desc

	if pg > 1 {
		params.Set("start", strconv.Itoa((pg-1)*50))
	}

	return base + "?" + params.Encode()
}

type plabItem struct {
	topicID string
	title   string
	size    string
	seeds   string
}

func (s *sisiPornLabSource) parseTrackerList(ctx context.Context, host, html string) []map[string]any {
	if html == "" {
		return []map[string]any{}
	}

	// Build maps: topicID → {title, size, seeds}
	topics := plabTopicRe.FindAllStringSubmatch(html, -1)
	downloads := plabDLRe.FindAllStringSubmatch(html, -1)
	seeds := plabSeedsRe.FindAllStringSubmatch(html, -1)

	items := make([]plabItem, 0, len(topics))
	for i, t := range topics {
		if len(t) < 3 {
			continue
		}

		item := plabItem{
			topicID: strings.TrimSpace(t[1]),
			title:   plabCleanTitle(t[2]),
		}

		// Match download by topicID.
		for _, d := range downloads {
			if len(d) >= 3 && strings.TrimSpace(d[1]) == item.topicID {
				item.size = strings.TrimSpace(strings.ReplaceAll(d[2], "&nbsp;", " "))
				break
			}
		}

		// Seeds — matched by index (same row order as topics).
		if i < len(seeds) && len(seeds[i]) >= 2 {
			item.seeds = strings.TrimSpace(seeds[i][1])
		}

		items = append(items, item)
	}

	// Fetch posters from topic pages in parallel (max 6 concurrent).
	posters := make([]string, len(items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)

	for i, item := range items {
		wg.Add(1)
		go func(idx int, topicID string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			topicURL := strings.TrimRight(s.host, "/") + "/forum/viewtopic.php?t=" + topicID
			topicHTML, err := s.fetchHTML(ctx, topicURL)
			if err != nil {
				return
			}
			poster := strings.TrimSpace(submatch1(plabPosterRe, topicHTML))
			if poster == "" {
				poster = strings.TrimSpace(submatch1(plabAnyImgRe, topicHTML))
			}
			posters[idx] = poster
		}(i, item.topicID)
	}
	wg.Wait()

	out := make([]map[string]any, 0, len(items))
	for i, item := range items {
		quality := ""
		if item.seeds != "" {
			quality = item.seeds + " seeds"
		}

		picture := host + "/img/sisi/torrent.png"
		if posters[i] != "" {
			picture = host + "/proxy/" + url.QueryEscape(posters[i])
		}

		out = append(out, map[string]any{
			"name":    item.title,
			"video":   host + "/plab/vidosik?t=" + item.topicID,
			"picture": picture,
			"time":    item.size,
			"quality": quality,
			"json":    true,
			"bookmark": map[string]any{
				"site":  "plab",
				"href":  item.topicID,
				"image": posters[i],
			},
		})
	}

	return out
}

// plabCleanTitle strips HTML tags and extra whitespace from title.
func plabCleanTitle(raw string) string {
	// Remove <b>...</b>, <wbr>, etc.
	re := regexp.MustCompile(`<[^>]+>`)
	s := re.ReplaceAllString(raw, "")
	s = strings.ReplaceAll(s, "&#039;", "'")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.TrimSpace(s)
	return s
}
