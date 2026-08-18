package proxylink

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Options struct {
	CacheDir   string
	VerifyIP   bool
	EncryptAES bool
	// SharedSecret, when non-empty, deterministically derives the AES key
	// (SHA256(secret) → 32 bytes for AES-256). All lampac-go instances using
	// the same secret can decrypt each other's /proxy/ URLs. Used in
	// cluster mode where a forwarded request may land on any node.
	SharedSecret string
}

type Model struct {
	ReqIP    string
	URI      string
	Plugin   string
	VerifyIP bool
	Expires  time.Time
	Headers  map[string]string
	CBCSKey  []byte // CBCS decryption key (16 bytes AES-128)
	CBCSIV   []byte // CBCS constant IV (16 bytes)
}

// keyMaterial holds the AES key + IV used for /proxy/<enc> tokens. Pointed
// to atomically by Manager.keys so it can be swapped at runtime (cluster
// shared_secret rotation, hot-reload after first secret generation).
type keyMaterial struct {
	key []byte
	iv  []byte
}

type Manager struct {
	mu         sync.RWMutex
	links      map[string]Model
	verifyIP   bool
	encryptAES bool
	keys       atomic.Pointer[keyMaterial]
}

// currentKey returns a snapshot of the active AES key + IV. Cheap (atomic load).
func (m *Manager) currentKey() ([]byte, []byte) {
	km := m.keys.Load()
	if km == nil {
		return nil, nil
	}
	return km.key, km.iv
}

type aesPayload struct {
	P string            `json:"p,omitempty"`
	U string            `json:"u,omitempty"`
	I string            `json:"i,omitempty"`
	V bool              `json:"v,omitempty"`
	E time.Time         `json:"e"`
	H map[string]string `json:"h,omitempty"`
	K []byte            `json:"K,omitempty"` // CBCS decryption key
	W []byte            `json:"W,omitempty"` // CBCS constant IV
}

func New(opts Options) (*Manager, error) {
	km, err := buildKeyMaterial(opts)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		links:      make(map[string]Model, 1024),
		verifyIP:   opts.VerifyIP,
		encryptAES: opts.EncryptAES,
	}
	m.keys.Store(km)
	return m, nil
}

// Reload swaps the AES key/iv with values derived from `opts`. Safe to call
// concurrently — encryption/decryption operations atomically pick up the new
// material on their next call. Used to recover from the cluster bootstrap
// case where shared_secret was empty at startup and got set later.
//
// Caveat: tokens minted before Reload become undecryptable. Active streams
// will break and need to re-resolve. This is expected — call Reload only
// when the cluster setup actually changes (e.g. shared_secret rotation).
func (m *Manager) Reload(opts Options) error {
	km, err := buildKeyMaterial(opts)
	if err != nil {
		return err
	}
	m.keys.Store(km)
	return nil
}

// buildKeyMaterial produces a fresh keyMaterial from Options.
func buildKeyMaterial(opts Options) (*keyMaterial, error) {
	if strings.TrimSpace(opts.SharedSecret) != "" {
		// Cluster mode — derive deterministic 32-byte AES-256 key + IV from
		// the shared secret so every node ends up with the same encryption
		// material. The IV is a separate hash to avoid using the key bytes
		// for both purposes.
		k := sha256.Sum256([]byte("proxylink-key:" + opts.SharedSecret))
		i := sha256.Sum256([]byte("proxylink-iv:" + opts.SharedSecret))
		key := append([]byte(nil), k[:]...)
		iv := append([]byte(nil), i[:16]...)
		return &keyMaterial{key: key, iv: iv}, nil
	}
	cacheDir := resolveCacheDir(opts.CacheDir)
	key, iv, err := loadOrCreateAESKey(cacheDir)
	if err != nil {
		return nil, err
	}
	return &keyMaterial{key: key, iv: iv}, nil
}

func (m *Manager) EncryptURI(uri, reqip, plugin string, verifyip, forceMD5, isProxyImg bool) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}

	uriClear := uri
	if before, _, ok := strings.Cut(uri, "#"); ok {
		uriClear = strings.TrimSpace(before)
	}

	hash := ""
	md5Mode := false
	if plugin == "posterapi" {
		payload, _ := json.Marshal(aesPayload{U: uriClear})
		hash = m.encryptAESPayload(string(payload))
	} else if !forceMD5 && m.encryptAES && !strings.Contains(uriClear, " or ") {
		if verifyip && m.verifyIP {
			payload, _ := json.Marshal(aesPayload{
				P: plugin,
				U: uriClear,
				I: reqip,
				V: true,
				E: time.Now().Add(36 * time.Hour),
			})
			hash = m.encryptAESPayload(string(payload))
		} else {
			payload, _ := json.Marshal(aesPayload{P: plugin, U: uriClear})
			hash = m.encryptAESPayload(string(payload))
		}
	} else {
		md5Mode = true
		ipPart := ""
		if verifyip && m.verifyIP {
			ipPart = reqip
		}
		hash = md5Hex(uriClear + ipPart)
	}

	if hash == "" {
		hash = uriClear
	}

	hash += detectExt(uri, isProxyImg)
	if md5Mode {
		expires := time.Now().Add(20 * time.Hour)
		m.mu.Lock()
		m.links[hash] = Model{
			ReqIP:    reqip,
			URI:      uriClear,
			Plugin:   plugin,
			VerifyIP: verifyip,
			Expires:  expires,
		}
		m.mu.Unlock()
	}

	return hash
}

// EncryptURIWithHeaders works like EncryptURI but embeds custom upstream
// headers into the AES payload. The proxy handler reads these and sets them
// on the upstream request (e.g. Origin, Referer for CDNs that require them).
func (m *Manager) EncryptURIWithHeaders(uri, reqip, plugin string, headers map[string]string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	uriClear := uri
	if before, _, ok := strings.Cut(uri, "#"); ok {
		uriClear = strings.TrimSpace(before)
	}

	payload, _ := json.Marshal(aesPayload{
		P: plugin,
		U: uriClear,
		H: headers,
	})
	hash := m.encryptAESPayload(string(payload))
	if hash == "" {
		return ""
	}
	return hash + detectExt(uri, false)
}

// EncryptURIWithCBCS embeds custom headers AND CBCS decryption key/IV into
// the AES payload. Used for Kinescope fMP4 segments that need server-side
// CBCS decryption in the proxy handler.
func (m *Manager) EncryptURIWithCBCS(uri, reqip, plugin string, headers map[string]string, cbcsKey, cbcsIV []byte) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	uriClear := uri
	if before, _, ok := strings.Cut(uri, "#"); ok {
		uriClear = strings.TrimSpace(before)
	}

	payload, _ := json.Marshal(aesPayload{
		P: plugin,
		U: uriClear,
		H: headers,
		K: cbcsKey,
		W: cbcsIV,
	})
	hash := m.encryptAESPayload(string(payload))
	if hash == "" {
		return ""
	}
	return hash + detectExt(uri, false)
}

func (m *Manager) Decrypt(hash, reqip string) *Model {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return nil
	}

	if q := strings.IndexAny(hash, "?#"); q >= 0 {
		hash = hash[:q]
	}

	if IsAes(hash) {
		key := hash
		if dot := strings.LastIndex(key, "."); dot > 0 {
			key = key[:dot]
		}
		dec := m.decryptAESPayload(key)
		if dec == "" {
			return nil
		}

		var payload aesPayload
		if err := json.Unmarshal([]byte(dec), &payload); err != nil {
			return nil
		}
		if payload.U == "" {
			return nil
		}

		if payload.V {
			if reqip != "" && payload.I != reqip {
				return nil
			}
			if !payload.E.IsZero() && time.Now().After(payload.E) {
				return nil
			}
		}

		return &Model{
			ReqIP:    reqip,
			URI:      payload.U,
			Plugin:   payload.P,
			VerifyIP: payload.V,
			Expires:  payload.E,
			Headers:  payload.H,
			CBCSKey:  payload.K,
			CBCSIV:   payload.W,
		}
	}

	m.mu.RLock()
	val, ok := m.links[hash]
	m.mu.RUnlock()
	if !ok {
		return nil
	}

	if !val.Expires.IsZero() && time.Now().After(val.Expires) {
		m.mu.Lock()
		delete(m.links, hash)
		m.mu.Unlock()
		return nil
	}

	if val.VerifyIP && m.verifyIP && val.ReqIP != "" && reqip != "" && val.ReqIP != reqip {
		return nil
	}
	cp := val
	return &cp
}

// DebugDecrypt returns a human-readable reason why Decrypt failed, or "ok" on
// success. It mirrors Decrypt's verify-IP and expiry checks so the reason is
// accurate: previously it reported "ok (aes)" whenever the ciphertext merely
// decrypted to valid JSON, even when Decrypt actually rejected the link on an
// IP mismatch or expiry — which masked the real cause of /proxy 404s. Pass the
// same reqip Decrypt would see (the requester IP) so mismatches are explicit.
func (m *Manager) DebugDecrypt(hash, reqip string) string {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return "empty hash"
	}
	if q := strings.IndexAny(hash, "?#"); q >= 0 {
		hash = hash[:q]
	}
	if !IsAes(hash) {
		// MD5 path
		m.mu.RLock()
		val, ok := m.links[hash]
		m.mu.RUnlock()
		if !ok {
			return "md5: not found in map (len=" + fmt.Sprintf("%d", len(hash)) + ")"
		}
		if !val.Expires.IsZero() && time.Now().After(val.Expires) {
			return fmt.Sprintf("md5: expired at %s (now %s)", val.Expires.Format(time.RFC3339), time.Now().Format(time.RFC3339))
		}
		if val.VerifyIP && m.verifyIP && val.ReqIP != "" && reqip != "" && val.ReqIP != reqip {
			return fmt.Sprintf("md5: ip mismatch (minted for %s, request from %s)", val.ReqIP, reqip)
		}
		return "ok (md5)"
	}
	key := hash
	if dot := strings.LastIndex(key, "."); dot > 0 {
		key = key[:dot]
	}
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		raw2, err2 := base64.StdEncoding.DecodeString(key)
		if err2 != nil {
			return "base64 decode failed: " + err.Error()
		}
		raw = raw2
	}
	if len(raw) == 0 {
		return "base64 decoded to empty"
	}
	aesKey, aesIV := m.currentKey()
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "aes cipher init failed: " + err.Error()
	}
	if len(raw)%block.BlockSize() != 0 {
		return "block size mismatch: decoded=" + fmt.Sprintf("%d", len(raw)) + " blockSize=16"
	}
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, aesIV).CryptBlocks(out, raw)
	out, err = pkcs7Unpad(out, block.BlockSize())
	if err != nil {
		return "pkcs7 unpad failed: " + err.Error()
	}
	var payload aesPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		return "json unmarshal failed: " + err.Error()
	}
	if payload.U == "" {
		return "payload.U is empty"
	}
	if payload.V {
		if reqip != "" && payload.I != reqip {
			return fmt.Sprintf("ip mismatch (minted for %s, request from %s)", payload.I, reqip)
		}
		if !payload.E.IsZero() && time.Now().After(payload.E) {
			return fmt.Sprintf("expired at %s (now %s)", payload.E.Format(time.RFC3339), time.Now().Format(time.RFC3339))
		}
	}
	return "ok (aes)"
}

func IsAes(hash string) bool {
	if hash == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(hash), "http") {
		return false
	}

	if i := strings.IndexAny(hash, "?&."); i >= 0 {
		hash = hash[:i]
	}
	return len(hash) != 32
}

func (m *Manager) encryptAESPayload(plain string) string {
	if strings.TrimSpace(plain) == "" {
		return plain
	}

	aesKey, aesIV := m.currentKey()
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return plain
	}

	p := pkcs7Pad([]byte(plain), block.BlockSize())
	out := make([]byte, len(p))
	cipher.NewCBCEncrypter(block, aesIV).CryptBlocks(out, p)
	// Use RawURLEncoding (no padding, URL-safe alphabet: - and _ instead of + and /)
	// to prevent base64 characters from being interpreted as URL path separators
	// or query parameter delimiters by browsers and HLS players.
	return base64.RawURLEncoding.EncodeToString(out)
}

func (m *Manager) decryptAESPayload(cipherText string) string {
	// Try RawURLEncoding first (new format: URL-safe, no padding).
	// Fall back to StdEncoding for backward compatibility with existing tokens.
	raw, err := base64.RawURLEncoding.DecodeString(cipherText)
	if err != nil || len(raw) == 0 {
		raw, err = base64.StdEncoding.DecodeString(cipherText)
		if err != nil || len(raw) == 0 {
			return ""
		}
	}

	aesKey, aesIV := m.currentKey()
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return ""
	}
	if len(raw)%block.BlockSize() != 0 {
		return ""
	}

	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, aesIV).CryptBlocks(out, raw)
	out, err = pkcs7Unpad(out, block.BlockSize())
	if err != nil {
		return ""
	}
	return string(out)
}

func resolveCacheDir(cacheDir string) string {
	cacheDir = strings.TrimSpace(cacheDir)
	candidates := []string{}
	if cacheDir != "" {
		candidates = append(candidates, cacheDir)
	}
	candidates = append(candidates, "cache", "/home/cache")

	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return candidates[0]
}

func loadOrCreateAESKey(cacheDir string) ([]byte, []byte, error) {
	if cacheDir == "" {
		return nil, nil, errors.New("empty cache dir")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, nil, err
	}

	path := filepath.Join(cacheDir, "aeskey")
	if data, err := os.ReadFile(path); err == nil {
		parts := strings.Split(strings.TrimSpace(string(data)), "/")
		if len(parts) == 2 && len(parts[0]) == 16 && len(parts[1]) == 16 {
			return []byte(parts[0]), []byte(parts[1]), nil
		}
	}

	key, err := randomString(16)
	if err != nil {
		return nil, nil, err
	}
	iv, err := randomString(16)
	if err != nil {
		return nil, nil, err
	}

	if err := os.WriteFile(path, []byte(key+"/"+iv), 0o600); err != nil {
		return nil, nil, err
	}
	return []byte(key), []byte(iv), nil
}

func randomString(n int) (string, error) {
	const alphabet = "qwertyuioplkjhgfdsazxcvbnmQWERTYUIOPLKJHGFDSAZXCVBNM1234567890"
	if n <= 0 {
		return "", nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i := range buf {
		out[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(out), nil
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func detectExt(uri string, isProxyImg bool) string {
	u := strings.ToLower(uri)
	if isProxyImg {
		switch {
		case strings.Contains(u, ".png"):
			return ".png"
		case strings.Contains(u, ".webp"):
			return ".webp"
		default:
			return ".jpg"
		}
	}

	extensions := []string{
		".m3u8", ".m3u", ".mpd", ".webm", ".ts", ".m4s", ".mp4",
		".mov", ".mkv", ".aac", ".vtt", ".srt", ".jpg", ".jpeg", ".png", ".webp",
	}
	for _, ext := range extensions {
		if strings.Contains(u, ext) {
			if ext == ".jpeg" {
				return ".jpg"
			}
			return ext
		}
	}
	return ""
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - (len(data) % blockSize)
	if pad == 0 {
		pad = blockSize
	}
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid block size")
	}
	pad := int(data[len(data)-1])
	if pad <= 0 || pad > blockSize || pad > len(data) {
		return nil, errors.New("invalid padding")
	}
	for i := len(data) - pad; i < len(data); i++ {
		if int(data[i]) != pad {
			return nil, errors.New("invalid padding")
		}
	}
	return data[:len(data)-pad], nil
}

// --- Cache management ---

const maxLinkEntries = 50_000

// Cleanup removes expired entries and enforces max size.
func (m *Manager) Cleanup() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	removed := 0
	for k, v := range m.links {
		if !v.Expires.IsZero() && now.After(v.Expires) {
			delete(m.links, k)
			removed++
		}
	}
	if len(m.links) > maxLinkEntries {
		over := len(m.links) - maxLinkEntries
		for k := range m.links {
			if over <= 0 {
				break
			}
			delete(m.links, k)
			over--
			removed++
		}
	}
	return removed
}

// Len returns the current number of cached entries.
func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.links)
}
