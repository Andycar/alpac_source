package userdata

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	stdjson "encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"lampac-go/internal/auth"

	"github.com/go-chi/chi/v5"
)

type storageOptions struct {
	Enable  bool
	MaxSize int64
	MD5Name bool
}

type storageFileInfo struct {
	Name       string `json:"Name"`
	Path       string `json:"path"`
	Length     int64  `json:"Length"`
	ChangeTime int64  `json:"changeTime"`
}

type storagePath struct {
	logical string
	fs      string
}

var storageNameSanitizer = regexp.MustCompile(`(?i)[^a-z0-9\-]`)

func StorageGetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts := loadStorageOptions()
		if !opts.Enable {
			writeStorageError(w, http.StatusOK, "disabled")
			return
		}

		path := r.URL.Query().Get("path")
		pathfile := r.URL.Query().Get("pathfile")
		userUID := storageUserUID(r)
		out, ok := resolveStoragePath(path, pathfile, false, userUID, opts)
		if !ok {
			writeStorageError(w, http.StatusOK, "outFile")
			return
		}

		info, err := storageStat(out)
		if err != nil {
			writeStorageError(w, http.StatusOK, "outFile")
			return
		}

		if parseBoolLike(r.URL.Query().Get("responseInfo")) {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":  true,
				"uid":      userUID,
				"fileInfo": info,
			})
			return
		}

		data, err := os.ReadFile(out.fs)
		if err != nil {
			writeStorageError(w, http.StatusServiceUnavailable, "fileLock")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"uid":      userUID,
			"fileInfo": info,
			"data":     string(data),
		})
	}
}

func StorageSetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts := loadStorageOptions()
		if !opts.Enable {
			writeStorageError(w, http.StatusOK, "disabled")
			return
		}

		if r.ContentLength > opts.MaxSize {
			writeStorageError(w, http.StatusOK, "max_size")
			return
		}

		path := r.URL.Query().Get("path")
		pathfile := r.URL.Query().Get("pathfile")
		userUID := storageUserUID(r)
		out, ok := resolveStoragePath(path, pathfile, true, userUID, opts)
		if !ok {
			writeStorageError(w, http.StatusOK, "outFile")
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeStorageError(w, http.StatusServiceUnavailable, "Request.Body.CopyToAsync")
			return
		}
		if int64(len(body)) > opts.MaxSize {
			writeStorageError(w, http.StatusOK, "max_size")
			return
		}

		if err := os.WriteFile(out.fs, body, 0o644); err != nil {
			writeStorageError(w, http.StatusServiceUnavailable, "fileLock")
			return
		}

		info, err := storageStat(out)
		if err != nil {
			writeStorageError(w, http.StatusServiceUnavailable, "fileLock")
			return
		}

		// Broadcast storage change event to other devices of the same user via NWS.
		connID := r.URL.Query().Get("connectionId")
		storageEventName, storageEventData := resolveStorageEvent(r, path, pathfile)
		deps.NwsBroadcast(connID, userUID, storageEventName, storageEventData)

		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"uid":      userUID,
			"fileInfo": info,
		})
	}
}

func StorageTempGetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts := loadStorageOptions()
		if !opts.Enable {
			writeStorageError(w, http.StatusOK, "disabled")
			return
		}

		key := strings.TrimSpace(strings.Trim(chiURLParam(r, "key"), "/"))
		out, ok := resolveStoragePath("temp", "", false, key, opts)
		if !ok {
			writeStorageError(w, http.StatusOK, "outFile")
			return
		}

		info, err := storageStat(out)
		if err != nil {
			writeStorageError(w, http.StatusOK, "outFile")
			return
		}

		if parseBoolLike(r.URL.Query().Get("responseInfo")) {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":  true,
				"uid":      storageUserUID(r),
				"fileInfo": info,
			})
			return
		}

		data, err := os.ReadFile(out.fs)
		if err != nil {
			writeStorageError(w, http.StatusServiceUnavailable, "fileLock")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"uid":      storageUserUID(r),
			"fileInfo": info,
			"data":     string(data),
		})
	}
}

func StorageTempSetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts := loadStorageOptions()
		if !opts.Enable {
			writeStorageError(w, http.StatusOK, "disabled")
			return
		}

		if r.ContentLength > opts.MaxSize {
			writeStorageError(w, http.StatusOK, "max_size")
			return
		}

		key := strings.TrimSpace(strings.Trim(chiURLParam(r, "key"), "/"))
		out, ok := resolveStoragePath("temp", "", true, key, opts)
		if !ok {
			writeStorageError(w, http.StatusOK, "outFile")
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeStorageError(w, http.StatusServiceUnavailable, "Request.Body.CopyToAsync")
			return
		}
		if int64(len(body)) > opts.MaxSize {
			writeStorageError(w, http.StatusOK, "max_size")
			return
		}

		if err := os.WriteFile(out.fs, body, 0o644); err != nil {
			writeStorageError(w, http.StatusServiceUnavailable, "fileLock")
			return
		}

		info, err := storageStat(out)
		if err != nil {
			writeStorageError(w, http.StatusServiceUnavailable, "fileLock")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"uid":      storageUserUID(r),
			"fileInfo": info,
		})
	}
}

func storageStat(path storagePath) (storageFileInfo, error) {
	st, err := os.Stat(path.fs)
	if err != nil {
		return storageFileInfo{}, err
	}
	return storageFileInfo{
		Name:       st.Name(),
		Path:       path.logical,
		Length:     st.Size(),
		ChangeTime: st.ModTime().UTC().UnixMilli(),
	}, nil
}

func resolveStoragePath(path, pathfile string, createDirectory bool, userUID string, opts storageOptions) (storagePath, bool) {
	if path == "temp" && userUID == "" {
		return storagePath{}, false
	}

	cleanPath := storageNameSanitizer.ReplaceAllString(path, "")
	if cleanPath == "" {
		return storagePath{}, false
	}

	if userUID == "" {
		return storagePath{}, false
	}

	id := userUID + pathfile
	key := ""
	if opts.MD5Name {
		sum := md5.Sum([]byte(id))
		key = hex.EncodeToString(sum[:])
	} else {
		key = storageNameSanitizer.ReplaceAllString(id, "")
		if key == "" {
			return storagePath{}, false
		}
	}

	logicalBase := filepath.ToSlash(filepath.Join("database", "storage"))
	fsBase := relToRuntime(filepath.Join("database", "storage"))

	if cleanPath == "temp" {
		logical := filepath.ToSlash(filepath.Join(logicalBase, cleanPath, key))
		fs := filepath.Join(fsBase, cleanPath, key)
		if createDirectory {
			_ = os.MkdirAll(filepath.Dir(fs), 0o755)
		}
		return storagePath{logical: logical, fs: fs}, true
	}

	if len(key) < 3 {
		key = key + strings.Repeat("0", 3-len(key))
	}

	dirPrefix := key[:2]
	fileSuffix := key[2:]
	logicalDir := filepath.ToSlash(filepath.Join(logicalBase, cleanPath, dirPrefix))
	fsDir := filepath.Join(fsBase, cleanPath, dirPrefix)
	if createDirectory {
		if err := os.MkdirAll(fsDir, 0o755); err != nil {
			return storagePath{}, false
		}
	}
	return storagePath{
		logical: filepath.ToSlash(filepath.Join(logicalDir, fileSuffix)),
		fs:      filepath.Join(fsDir, fileSuffix),
	}, true
}

func loadStorageOptions() storageOptions {
	opts := storageOptions{
		Enable: true,
		// 50 MB default. Full-localStorage backups from a long-running
		// TV install (a year of view history + bookmarks + plugin caches)
		// can easily run 5-20 MB. The previous 7 MB default was hitting
		// `max_size` for active users; nginx still needs
		// `client_max_body_size 50m;` to match — surface a 413 message
		// in the client when the proxy is the bottleneck.
		MaxSize: 50000000,
		MD5Name: true,
	}

	data, ok := deps.ReadFileAny("init.conf")
	if !ok || len(data) == 0 {
		return opts
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return opts
	}

	rawStorage, ok := root["storage"].(map[string]any)
	if !ok {
		return opts
	}

	if v, ok := rawStorage["enable"].(bool); ok {
		opts.Enable = v
	}
	if v, ok := rawStorage["max_size"]; ok {
		switch vv := v.(type) {
		case float64:
			opts.MaxSize = int64(vv)
		case int64:
			opts.MaxSize = vv
		case int:
			opts.MaxSize = int64(vv)
		}
	}
	if v, ok := rawStorage["md5name"].(bool); ok {
		opts.MD5Name = v
	}

	return opts
}

func storageUserUID(r *http.Request) string {
	// Priority 1: authenticated user from auth middleware.
	// This ensures all devices of the same user share one data set.
	if user, ok := auth.UserFromContext(r.Context()); ok && user != nil && user.ID != "" {
		return user.ID
	}

	// Priority 2: client-supplied identifiers (backward compat).
	// `account_email` is intentionally NOT a candidate here — it is an
	// unauthenticated query parameter that any caller can spoof, which
	// would let them read/write another user's storage bucket. Legacy
	// callers that historically used it must migrate to ?uid= or rely on
	// the authenticated user from the lampac_token cookie.
	q := r.URL.Query()
	candidates := []string{
		q.Get("uid"),
		q.Get("user_uid"),
		q.Get("id"),
		r.Header.Get("X-User-Uid"),
		cookieValue(r, "uid"),
	}
	for _, v := range candidates {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return deps.ClientIP(r)
}

func parseBoolLike(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return false
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return v == "1" || v == "yes" || v == "on"
}

func writeStorageError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"msg":     msg,
	})
}

func chiURLParam(r *http.Request, key string) string {
	val := strings.TrimSpace(chi.URLParam(r, key))
	if val != "" {
		return val
	}
	return strings.TrimSpace(r.URL.Query().Get(key))
}

// resolveStorageEvent determines the NWS event name and data for a storage
// write. The .NET version supports a base64-encoded JSON "events" query param
// that overrides the default "storage" event name.
func resolveStorageEvent(r *http.Request, path, pathfile string) (string, string) {
	// Check for explicit events parameter (base64-encoded JSON).
	if evB64 := strings.TrimSpace(r.URL.Query().Get("events")); evB64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(evB64)
		if err == nil && len(decoded) > 0 {
			var ev map[string]any
			if err := stdjson.Unmarshal(decoded, &ev); err == nil {
				name, _ := ev["name"].(string)
				data, _ := ev["data"].(string)
				if name == "" {
					name = "storage"
				}
				if data == "" {
					raw, _ := stdjson.Marshal(map[string]string{
						"path":     path,
						"pathfile": pathfile,
					})
					data = string(raw)
				}
				return name, data
			}
		}
	}

	// Default: "storage" event with path info.
	raw, _ := stdjson.Marshal(map[string]string{
		"path":     path,
		"pathfile": pathfile,
	})
	return "storage", string(raw)
}
