package adminhttp

import (
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// rePluginName validates filesystem-safe ASCII names (used for .js filename and map key).
var rePluginName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// rePluginDisplayName allows Cyrillic + Latin + digits + spaces + punctuation for display names.
var rePluginDisplayName = regexp.MustCompile(`^[\p{L}\p{N}\s_\-.,!?()]+$`)

// cyrTranslit maps Cyrillic lowercase letters to Latin equivalents (GOST 7.79-2000 simplified).
var cyrTranslit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "j", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// transliterate converts a Unicode string to an ASCII filesystem-safe name.
// Cyrillic letters are transliterated, spaces become underscores, non-ASCII/non-alnum removed.
func transliterate(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else if r == ' ' || r == '_' {
			b.WriteByte('_')
		} else if lat, ok := cyrTranslit[r]; ok {
			b.WriteString(lat)
		} else if unicode.IsUpper(r) {
			lr := unicode.ToLower(r)
			if lat, ok := cyrTranslit[lr]; ok {
				b.WriteString(lat)
			}
		}
		// skip other characters
	}
	result := b.String()
	// Collapse multiple underscores.
	for strings.Contains(result, "__") {
		result = strings.ReplaceAll(result, "__", "_")
	}
	result = strings.Trim(result, "_")
	if result == "" {
		result = "plugin"
	}
	return result
}

// toFSName converts a user-provided name to a filesystem-safe ASCII key.
// If already ASCII, returns as-is. If contains non-ASCII (Cyrillic), transliterates.
func toFSName(displayName string) (fsName string, needsTranslit bool) {
	if rePluginName.MatchString(displayName) {
		return displayName, false
	}
	return transliterate(displayName), true
}

func tgAdminCustomPluginsHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, registry CustomPlugins) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore)
		if !isSuper {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"forbidden"}`))
			return
		}

		if r.Method == http.MethodGet {
			list := registry.ListAny()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"plugins": list})
			return
		}

		// POST — multipart for upload, or JSON for delete/toggle
		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "multipart/form-data") {
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				http.Error(w, `{"error":"parse form failed"}`, http.StatusBadRequest)
				return
			}
			action := strings.TrimSpace(r.FormValue("action"))
			if action == "upload_image" {
				handleCustomPluginImageUpload(w, r, registry)
				return
			}
			handleCustomPluginUpload(w, r, registry)
			return
		}

		// JSON body: {"action":"delete"|"toggle"|"toggle_autoload"|"update_meta", "name":"...", ...}
		var body struct {
			Action   string `json:"action"`
			Name     string `json:"name"`
			Enabled  bool   `json:"enabled"`
			Autoload bool   `json:"autoload"`
			Public   bool   `json:"public"`
			Descr    string `json:"descr"`
			Author   string `json:"author"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
			return
		}

		name := strings.TrimSpace(body.Name)
		if name == "" || !rePluginName.MatchString(name) {
			http.Error(w, `{"error":"invalid name"}`, http.StatusBadRequest)
			return
		}

		switch body.Action {
		case "delete":
			registry.Unregister(name)
		case "toggle":
			registry.SetEnabled(name, body.Enabled)
		case "toggle_autoload":
			registry.SetAutoload(name, body.Autoload)
		case "update_meta":
			registry.UpdateMeta(name, body.Public, strings.TrimSpace(body.Descr), strings.TrimSpace(body.Author))
		case "delete_image":
			registry.DeleteImage(name)
		default:
			http.Error(w, `{"error":"unknown action"}`, http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}
}

func handleCustomPluginUpload(w http.ResponseWriter, r *http.Request, registry CustomPlugins) {
	inputName := strings.TrimSpace(r.FormValue("name"))
	if inputName == "" || !rePluginDisplayName.MatchString(inputName) {
		http.Error(w, `{"error":"invalid plugin name"}`, http.StatusBadRequest)
		return
	}

	fsName, wasTranslit := toFSName(inputName)
	if !rePluginName.MatchString(fsName) {
		http.Error(w, `{"error":"cannot transliterate name to valid ASCII"}`, http.StatusBadRequest)
		return
	}

	displayName := ""
	if wasTranslit {
		displayName = inputName
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, `{"error":"file required"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, 2<<20))
	if err != nil {
		http.Error(w, `{"error":"read failed"}`, http.StatusInternalServerError)
		return
	}

	if err := registry.Register(fsName, displayName, content); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": fsName, "display_name": displayName})
}

var allowedImageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".svg": true,
}

func handleCustomPluginImageUpload(w http.ResponseWriter, r *http.Request, registry CustomPlugins) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || !rePluginName.MatchString(name) {
		http.Error(w, `{"error":"invalid plugin name"}`, http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, `{"error":"image file required"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedImageExts[ext] {
		http.Error(w, `{"error":"unsupported image format (jpg, png, gif, webp, svg)"}`, http.StatusBadRequest)
		return
	}

	content, err := io.ReadAll(io.LimitReader(file, 2<<20))
	if err != nil || len(content) == 0 {
		http.Error(w, `{"error":"read image failed"}`, http.StatusInternalServerError)
		return
	}

	if err := registry.SaveImage(name, content, ext); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func CustomPluginImageHandler(registry CustomPlugins) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if registry == nil {
			http.NotFound(w, r)
			return
		}
		filename := chi.URLParam(r, "filename")
		path := registry.ImagePath(filename)
		if path == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, path)
	}
}
