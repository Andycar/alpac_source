package httpapi

import (
	"bytes"
	"crypto/subtle"
	stdjson "encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"lampac-go/internal/httpx"
)

const adminAuthHTML = `<!DOCTYPE html>
<html>
<head>
	<title>Authorization</title>
</head>
<body>
<style type="text/css">
	* { box-sizing: border-box; outline: none; }
	body { padding: 40px; font-family: sans-serif; }
	input { width: 340px; padding: 8px; }
	button { padding: 10px; }
	form > * + * { margin-top: 20px; }
</style>
<form method="post" action="/admin/auth" id="form">
	<div><input type="text" name="parol" placeholder="пароль из файла passwd"></div>
	<button type="submit">войти</button>
</form>
</body>
</html>
`

const adminSyncHTML = `<!DOCTYPE html>
<html>
<head>
	<title>Редактор sync.conf</title>
</head>
<body>
<style type="text/css">
	* { box-sizing: border-box; outline: none; }
	body { padding: 40px; font-family: sans-serif; }
	textarea { width: 100%; padding: 10px; }
	button { padding: 10px; }
</style>
<form method="post" action="" id="form">
	<div>
		<label>Ваш sync.conf</label>
		<textarea id="value" name="value" rows="30">{conf}</textarea>
	</div>
	<button type="submit">Сохранить</button>
</form>
<script type="text/javascript">
document.getElementById('form').addEventListener("submit", (e) => {
	let json = document.getElementById('value').value
	e.preventDefault()
	let formData = new FormData()
	formData.append('json', json)
	fetch('/admin/sync/init/save', { method: "POST", body: formData })
	.then((response) => response.json())
	.then((data) => {
		if (data.success) alert('Сохранено')
		else alert(data.ex || 'Не удалось сохранить настройки')
	})
	.catch((err) => alert(err.message))
})
</script>
</body>
</html>
`

func adminAuthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		passwd := strings.TrimSpace(r.FormValue("parol"))
		if passwd == "" {
			passwd = strings.TrimSpace(cookieValue(r, "passwd"))
		}
		rootPasswd := readRootPasswd()

		if passwd == "" {
			writeHTML(w, http.StatusOK, adminAuthHTML)
			return
		}

		if rootPasswd != "" && passwd == rootPasswd {
			http.SetCookie(w, &http.Cookie{
				Name:     "passwd",
				Value:    passwd,
				Path:     "/",
				HttpOnly: true,
			})
			renderAdminPage(w)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:   "passwd",
			Value:  "",
			Path:   "/",
			MaxAge: -1,
		})

		http.Redirect(w, r, "/admin/auth", http.StatusFound)
	}
}

func adminInitCurrentHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAdmin(w, r) {
			return
		}

		if data, ok := readFileAny("current.conf"); ok {
			writeRawJSON(w, data)
			return
		}
		if data, ok := readFileAny("init.conf"); ok {
			writeRawJSON(w, data)
			return
		}

		writeRawJSON(w, []byte("{}"))
	}
}

func adminInitCustomHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAdmin(w, r) {
			return
		}

		if data, ok := readFileAny("init.conf"); ok {
			trimmed := bytes.TrimSpace(data)
			if len(trimmed) == 0 {
				writeRawJSON(w, []byte("{}"))
				return
			}
			if trimmed[0] != '{' {
				trimmed = append([]byte("{"), append(trimmed, '}')...)
			}

			var obj map[string]any
			if err := stdjson.Unmarshal(trimmed, &obj); err != nil {
				writeRawJSON(w, []byte("{}"))
				return
			}
			out, _ := stdjson.Marshal(obj)
			writeRawJSON(w, out)
			return
		}

		writeRawJSON(w, []byte("{}"))
	}
}

func adminInitDefaultHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAdmin(w, r) {
			return
		}

		if data, ok := readFileAny("example.conf"); ok {
			writeRawJSON(w, data)
			return
		}

		writeRawJSON(w, []byte("{}"))
	}
}

func adminInitExampleHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAdmin(w, r) {
			return
		}

		data, _ := readFileAny("example.conf")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func adminInitSaveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAdmin(w, r) {
			return
		}

		payload := strings.TrimSpace(r.FormValue("json"))
		if payload == "" {
			writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": "json is empty"})
			return
		}

		var root map[string]any
		if err := stdjson.Unmarshal([]byte(payload), &root); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
			return
		}

		if accsdb, ok := root["accsdb"].(map[string]any); ok {
			if users, ok := accsdb["users"]; ok {
				writePrettyJSON("users.json", users)
				delete(accsdb, "users")
			}
		}

		if err := writePrettyJSON("init.conf", root); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

func adminSyncInitHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAdmin(w, r) {
			return
		}

		conf := ""
		if data, ok := readFileAny("sync.conf"); ok {
			conf = string(data)
		}
		writeHTML(w, http.StatusOK, strings.ReplaceAll(adminSyncHTML, "{conf}", conf))
	}
}

func adminSyncSaveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAdmin(w, r) {
			return
		}

		payload := strings.TrimSpace(r.FormValue("json"))
		if payload == "" {
			writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": "json is empty"})
			return
		}

		testJSON := payload
		if !strings.HasPrefix(strings.TrimSpace(testJSON), "{") {
			testJSON = "{" + testJSON + "}"
		}

		var parsed map[string]any
		if err := stdjson.Unmarshal([]byte(testJSON), &parsed); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
			return
		}

		path := relToRuntime("sync.conf")
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

func authorizeAdmin(w http.ResponseWriter, r *http.Request) bool {
	rootPasswd := readRootPasswd()
	passwd := strings.TrimSpace(cookieValue(r, "passwd"))
	// Constant-time compare; an empty root password never authorizes (the
	// former rootPasswd=="termux" auto-grant was a backdoor and is removed).
	if passwd != "" && rootPasswd != "" &&
		subtle.ConstantTimeCompare([]byte(passwd), []byte(rootPasswd)) == 1 {
		return true
	}

	http.Redirect(w, r, "/admin/auth", http.StatusFound)
	return false
}

func renderAdminPage(w http.ResponseWriter) {
	if data, ok := readFileAny(filepath.Join("wwwroot", "mycontrol", "index.html")); ok {
		writeHTML(w, http.StatusOK, string(data))
		return
	}
	if data, ok := readFileAny(filepath.Join("wwwroot", "control", "index.html")); ok {
		writeHTML(w, http.StatusOK, string(data))
		return
	}
	writeHTML(w, http.StatusOK, "<html><body>admin ok</body></html>")
}

func readRootPasswd() string {
	if v := strings.TrimSpace(os.Getenv("LAMPAC_GO_ADMIN_PASSWORD")); v != "" {
		return v
	}
	if data, ok := readFileAny("passwd"); ok {
		return strings.TrimSpace(string(data))
	}
	return ""
}

func readFileAny(rel string) ([]byte, bool) {
	// For init.conf / current.conf, serve config from TOML instead of legacy JSON.
	if rel == "init.conf" || rel == "current.conf" {
		if data, ok := readConfigFromTOMLAsJSON(); ok {
			return data, true
		}
	}
	path := relToRuntime(rel)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

// readConfigFromTOMLAsJSON returns the config.toml content marshaled as JSON
// with legacy-compatible PascalCase keys. This bridges runtime handlers that
// still call readFileAny("init.conf") to use TOML as the source of truth.
func readConfigFromTOMLAsJSON() ([]byte, bool) {
	if !serverReady() {
		return nil, false
	}
	merged := loadMergedConf()
	if len(merged) == 0 {
		return nil, false
	}
	// Also include raw TOML keys for non-mapped runtime settings
	// (e.g., corseu, cmd, weblog, storage, openstat, etc.).
	tomlRoot := loadConfigTOMLAsMap()
	for k, v := range tomlRoot {
		if _, exists := merged[k]; !exists {
			merged[k] = v
		}
	}
	data, err := stdjson.Marshal(merged)
	if err != nil {
		return nil, false
	}
	return data, true
}

func writePrettyJSON(rel string, value any) error {
	path := relToRuntime(rel)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)

	data, err := stdjson.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func relToRuntime(rel string) string {
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_HOME")); root != "" {
		return filepath.Join(root, rel)
	}
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_REPO_ROOT")); root != "" {
		return filepath.Join(root, rel)
	}
	return rel
}

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func writeRawJSON(w http.ResponseWriter, payload []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

// writeHTML forwards to httpx.WriteHTML (see internal/httpx). Thin wrapper so
// existing call sites are untouched; new/extracted code should use httpx.
func writeHTML(w http.ResponseWriter, code int, html string) {
	httpx.WriteHTML(w, code, html)
}
