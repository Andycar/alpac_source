package proxyimg

import (
	"bytes"
	"container/list"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// ---------------------------------------------------------------------------
// In-memory LRU cache for hot images (posters, thumbnails).
// Avoids repeated disk reads / upstream fetches for the same image.
// ---------------------------------------------------------------------------

type lruEntry struct {
	key         string
	data        []byte
	contentType string
	addedAt     time.Time
}

type lruCache struct {
	mu       sync.Mutex
	maxBytes int64
	curBytes int64
	ttl      time.Duration
	items    map[string]*list.Element
	order    *list.List // front = most recently used
}

func newLRUCache(maxBytes int64, ttl time.Duration) *lruCache {
	return &lruCache{
		maxBytes: maxBytes,
		ttl:      ttl,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

func (c *lruCache) get(key string) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, "", false
	}
	e := el.Value.(*lruEntry)
	if time.Since(e.addedAt) > c.ttl {
		c.removeElement(el)
		return nil, "", false
	}
	c.order.MoveToFront(el)
	return e.data, e.contentType, true
}

func (c *lruCache) put(key string, data []byte, contentType string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing entry.
	if el, ok := c.items[key]; ok {
		old := el.Value.(*lruEntry)
		c.curBytes -= int64(len(old.data))
		old.data = data
		old.contentType = contentType
		old.addedAt = time.Now()
		c.curBytes += int64(len(data))
		c.order.MoveToFront(el)
		c.evict()
		return
	}

	e := &lruEntry{key: key, data: data, contentType: contentType, addedAt: time.Now()}
	el := c.order.PushFront(e)
	c.items[key] = el
	c.curBytes += int64(len(data))
	c.evict()
}

func (c *lruCache) evict() {
	for c.curBytes > c.maxBytes && c.order.Len() > 0 {
		c.removeElement(c.order.Back())
	}
}

func (c *lruCache) removeElement(el *list.Element) {
	e := el.Value.(*lruEntry)
	c.order.Remove(el)
	delete(c.items, e.key)
	c.curBytes -= int64(len(e.data))
}

const maxImageBytes = 25 << 20

type Handler struct {
	links                 *proxylink.Manager
	client                *http.Client
	cacheDir              string
	cacheTTL              time.Duration
	cacheEnabled          bool
	cacheResizeEnabled    bool
	responseContentLength bool
	memCache              *lruCache // in-memory LRU for hot images
}

func New(cfg config.Config, links *proxylink.Manager) (*Handler, error) {
	cacheRoot := strings.TrimSpace(cfg.ProxyLink.CacheDir)
	if cacheRoot == "" {
		cacheRoot = "cache"
	}
	cacheDir := filepath.Join(cacheRoot, "img")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}

	cacheTTL := time.Duration(cfg.ServerProxy.Image.CacheTime) * time.Minute
	if cacheTTL <= 0 {
		cacheTTL = 60 * time.Minute
	}

	// In-memory LRU: 100 MB, same TTL as disk cache.
	memLRU := newLRUCache(100<<20, cacheTTL)

	return &Handler{
		links:                 links,
		client:                newGuardedClient(35 * time.Second),
		cacheDir:              cacheDir,
		cacheTTL:              cacheTTL,
		cacheEnabled:          cfg.ServerProxy.Image.Cache,
		cacheResizeEnabled:    cfg.ServerProxy.Image.CacheRSize,
		responseContentLength: cfg.ServerProxy.ResponseContentLength,
		memCache:              memLRU,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	width, height, rawTarget, ok := parsePath(r.URL.Path)
	if !ok {
		h.fallback(w, r)
		return
	}

	reqIP := requestIP(r)
	target, upstreamHeaders, ok := h.resolveTarget(rawTarget, r.URL.RawQuery, reqIP)
	if !ok {
		h.fallback(w, r)
		return
	}

	mainURL, reserveURL := splitReserveURL(target)
	cacheOn := h.cacheEnabled && ((width == 0 && height == 0) || h.cacheResizeEnabled)
	cacheKey := md5Hex(fmt.Sprintf("%s:%d:%d", mainURL, width, height))
	cachePath := filepath.Join(h.cacheDir, cacheKey)

	// 1. In-memory LRU check (fastest path).
	if cacheOn {
		if data, memCT, ok := h.memCache.get(cacheKey); ok {
			w.Header().Set("Content-Type", memCT)
			w.Header().Set("X-Cache-Status", "MEM")
			if h.responseContentLength {
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
	}

	// 2. Disk cache check.
	if cacheOn {
		if served := h.serveFromCache(w, cachePath, cacheKey); served {
			return
		}
	}

	body, ct, err := h.fetch(mainURL, r, upstreamHeaders)
	if err != nil && reserveURL != "" {
		body, ct, err = h.fetch(reserveURL, r, upstreamHeaders)
		mainURL = reserveURL
	}
	if err != nil {
		log.Warn().Err(err).Str("target", mainURL).Msg("proxyimg upstream failed")
		h.fallback(w, r)
		return
	}

	out := body
	contentType := normalizeContentType(ct, mainURL)
	if width > 0 || height > 0 {
		out, contentType, err = resizeImage(body, width, height, mainURL)
		if err != nil {
			log.Warn().Err(err).Str("target", mainURL).Msg("proxyimg resize failed")
			out = body
			contentType = normalizeContentType(ct, mainURL)
		}
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Cache-Status", map[bool]string{true: "MISS", false: "bypass"}[cacheOn])
	if h.responseContentLength {
		w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)

	if cacheOn {
		// Store in memory LRU (fast path for subsequent requests).
		h.memCache.put(cacheKey, out, contentType)
		// Persist to disk (survives restart).
		if err := writeAtomic(cachePath, out); err != nil {
			log.Debug().Err(err).Str("path", cachePath).Msg("proxyimg cache write failed")
		}
	}
}

func (h *Handler) resolveTarget(rawTarget, rawQuery, reqIP string) (string, map[string]string, bool) {
	if target, ok := decodeDirectTarget(rawTarget, rawQuery); ok {
		return target, nil, true
	}
	if h.links != nil {
		if model := h.links.Decrypt(rawTarget, reqIP); model != nil && strings.TrimSpace(model.URI) != "" {
			target := strings.TrimSpace(model.URI)
			if rawQuery != "" {
				if strings.Contains(target, "?") {
					target += "&" + rawQuery
				} else {
					target += "?" + rawQuery
				}
			}
			return target, model.Headers, true
		}
	}
	return "", nil, false
}

func (h *Handler) fetch(target string, in *http.Request, upstreamHeaders map[string]string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(in.Context(), http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("X-Lampac-Go", "1")
	req.Header.Set("User-Agent", in.UserAgent())
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
	// Apply per-link upstream headers (e.g. Referer/Origin from EncryptURIWithHeaders).
	// Set after defaults so module-supplied values win.
	for k, v := range upstreamHeaders {
		if strings.TrimSpace(k) == "" || v == "" {
			continue
		}
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("upstream status %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, maxImageBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxImageBytes {
		return nil, "", errors.New("image too large")
	}
	return body, resp.Header.Get("Content-Type"), nil
}

func (h *Handler) serveFromCache(w http.ResponseWriter, path, cacheKey string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	if time.Since(st.ModTime()) > h.cacheTTL {
		_ = os.Remove(path)
		return false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}

	ct := http.DetectContentType(data)
	if strings.TrimSpace(ct) == "" {
		ct = "image/jpeg"
	}

	// Promote to in-memory cache for next hit.
	h.memCache.put(cacheKey, data, ct)

	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Cache-Status", "HIT")
	if h.responseContentLength {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return true
}

func (h *Handler) fallback(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

func parsePath(path string) (int, int, string, bool) {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(strings.ToLower(path), "/proxyimg:") {
		rest := path[len("/proxyimg:"):]
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 {
			return 0, 0, "", false
		}
		wh := strings.SplitN(parts[0], ":", 2)
		if len(wh) != 2 {
			return 0, 0, "", false
		}
		w, errW := strconv.Atoi(strings.TrimSpace(wh[0]))
		h, errH := strconv.Atoi(strings.TrimSpace(wh[1]))
		if errW != nil || errH != nil {
			return 0, 0, "", false
		}
		return w, h, strings.TrimSpace(parts[1]), true
	}

	if strings.HasPrefix(strings.ToLower(path), "/proxyimg/") {
		return 0, 0, strings.TrimSpace(path[len("/proxyimg/"):]), true
	}
	return 0, 0, "", false
}

func decodeDirectTarget(pathPart, rawQuery string) (string, bool) {
	pathPart = strings.TrimSpace(pathPart)
	if pathPart == "" {
		return "", false
	}

	if i := strings.Index(pathPart, " or "); i > 0 {
		pathPart = strings.TrimSpace(pathPart[:i])
	}

	candidates := []string{pathPart}
	if unescaped, err := url.PathUnescape(pathPart); err == nil && unescaped != pathPart {
		candidates = append(candidates, unescaped)
	}
	if unescaped, err := url.QueryUnescape(pathPart); err == nil && unescaped != pathPart {
		candidates = append(candidates, unescaped)
	}

	for _, cand := range candidates {
		cand = strings.TrimSpace(cand)
		if !strings.HasPrefix(cand, "http://") && !strings.HasPrefix(cand, "https://") {
			continue
		}
		if rawQuery != "" {
			if strings.Contains(cand, "?") {
				cand += "&" + rawQuery
			} else {
				cand += "?" + rawQuery
			}
		}
		return cand, true
	}
	return "", false
}

func splitReserveURL(uri string) (string, string) {
	parts := strings.SplitN(uri, " or ", 2)
	main := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return main, ""
	}
	return main, strings.TrimSpace(parts[1])
}

func normalizeContentType(ct, target string) string {
	ct = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	if strings.HasPrefix(ct, "image/") {
		return ct
	}
	u := strings.ToLower(target)
	switch {
	case strings.Contains(u, ".png"):
		return "image/png"
	case strings.Contains(u, ".webp"):
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

func resizeImage(src []byte, width, height int, target string) ([]byte, string, error) {
	img, format, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, "", err
	}
	b := img.Bounds()
	ow := b.Dx()
	oh := b.Dy()
	if ow <= 0 || oh <= 0 {
		return nil, "", errors.New("invalid source size")
	}

	nw, nh := width, height
	switch {
	case nw <= 0 && nh <= 0:
		nw, nh = ow, oh
	case nw <= 0:
		nw = max(1, int(float64(ow)*float64(nh)/float64(oh)))
	case nh <= 0:
		nh = max(1, int(float64(oh)*float64(nw)/float64(ow)))
	}
	if nw == ow && nh == oh {
		return src, normalizeContentType("", target), nil
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)

	var out bytes.Buffer
	wantPNG := strings.Contains(strings.ToLower(target), ".png") || strings.EqualFold(format, "png")
	if wantPNG {
		if err := png.Encode(&out, dst); err != nil {
			return nil, "", err
		}
		return out.Bytes(), "image/png", nil
	}
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, "", err
	}
	return out.Bytes(), "image/jpeg", nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func requestIP(r *http.Request) string {
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xf != "" {
		if i := strings.Index(xf, ","); i > 0 {
			return strings.TrimSpace(xf[:i])
		}
		return xf
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
