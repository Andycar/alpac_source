package adminhttp

import (
	stdjson "encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"lampac-go/internal/modules"
)

// admin_manifest.go — the /admin/manifest/install first-run setup page (moved
// from httpapi). The shared config-migration helpers it used to sit next to
// (updateInitConfMap/applyLegacyMapToTOML/ensureMapChild/fileExists) stayed in
// httpapi; this package reaches them via the seam (updateInitConfMap forwarder,
// ensureMapChild/fileExists copies) and injects authorizeAdmin/readRootPasswd/
// randomLowerAlnum.

// AdminManifestInstallHandler serves GET/POST /admin/manifest/install. Registered
// by the composition root (server.go), not the admin router.
func AdminManifestInstallHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		manifestPath := relToRuntime(filepath.Join("module", "manifest.json"))
		isEditManifest := fileExists(manifestPath)

		if isEditManifest && !authorizeAdmin(w, r) {
			return
		}

		if readRootPasswd() == "termux" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("В termux операция недоступна"))
			return
		}

		if strings.EqualFold(r.Method, http.MethodPost) {
			if err := r.ParseForm(); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
				return
			}

			jacMode := strings.TrimSpace(r.FormValue("jac"))
			if jacMode == "" {
				jacMode = "webapi"
			}

			modulesOut := make([]modules.RootModule, 0, 10)

			if r.FormValue("online") == "on" {
				modulesOut = append(modulesOut, modules.RootModule{Enable: true, Dll: "Online.dll"})
			}
			if r.FormValue("sisi") == "on" {
				modulesOut = append(modulesOut, modules.RootModule{Enable: true, Dll: "SISI.dll"})
			}
			if jacMode != "" {
				modulesOut = append(modulesOut, modules.RootModule{
					Enable:    true,
					Initspace: "Jackett.ModInit",
					Dll:       "JacRed.dll",
				})
				if jacMode == "fdb" {
					if err := writeJacRedConfigFdb(); err != nil {
						writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
						return
					}
				}
			}
			if r.FormValue("dlna") == "on" {
				modulesOut = append(modulesOut, modules.RootModule{Enable: true, Dll: "DLNA.dll"})
			}
			if r.FormValue("tracks") == "on" {
				modulesOut = append(modulesOut, modules.RootModule{
					Enable:    true,
					Initspace: "Tracks.ModInit",
					Dll:       "Tracks.dll",
				})
			}
			if r.FormValue("ts") == "on" {
				modulesOut = append(modulesOut, modules.RootModule{
					Enable:    true,
					Initspace: "TorrServer.ModInit",
					Dll:       "TorrServer.dll",
				})
			}
			if r.FormValue("catalog") == "on" {
				modulesOut = append(modulesOut, modules.RootModule{
					Enable:    true,
					Initspace: "Catalog.ModInit",
					Dll:       "Catalog.dll",
				})
			}
			if r.FormValue("merch") == "on" {
				// Keep legacy behavior: Merchant is added disabled by default.
				modulesOut = append(modulesOut, modules.RootModule{Enable: false, Dll: "Merchant.dll"})
			}

			if err := writeManifestModules(modulesOut); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
				return
			}

			if r.FormValue("eng") != "on" {
				if err := updateInitConfMap(func(root map[string]any) {
					root["disableEng"] = true
				}); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
					return
				}
			}

			if isEditManifest {
				writeHTML(w, http.StatusOK, "Перезагрузите lampac для изменения настроек")
				return
			}

			if strings.TrimSpace(r.Header.Get("CF-Connecting-IP")) != "" {
				if err := updateInitConfMap(func(root map[string]any) {
					listen := ensureMapChild(root, "listen")
					listen["frontend"] = "cloudflare"
				}); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
					return
				}
			}

			sharedPass := strings.ToLower(randomLowerAlnum(8))
			if err := updateInitConfMap(func(root map[string]any) {
				accsdb := ensureMapChild(root, "accsdb")
				accsdb["enable"] = true
				accsdb["shared_passwd"] = sharedPass
			}); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
				return
			}

			writeHTML(w, http.StatusOK, renderManifestSuccessHTML(hostFromRequest(r), readRootPasswd(), sharedPass, r.Host))
			return
		}

		existing := map[string]bool{}
		if mods, err := loadManifestModules(); err == nil {
			for _, mod := range mods {
				existing[strings.ToLower(strings.TrimSpace(mod.Dll))] = mod.Enable
			}
		}

		writeHTML(w, http.StatusOK, renderManifestFormHTML(existing, isEditManifest))
	}
}

func writeManifestModules(mods []modules.RootModule) error {
	path := relToRuntime(filepath.Join("module", "manifest.json"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := stdjson.Marshal(mods)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func loadManifestModules() ([]modules.RootModule, error) {
	path := relToRuntime(filepath.Join("module", "manifest.json"))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []modules.RootModule
	if err := stdjson.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeJacRedConfigFdb() error {
	path := relToRuntime(filepath.Join("module", "JacRed.conf"))
	conf := map[string]any{}

	if data, err := os.ReadFile(path); err == nil {
		raw := strings.TrimSpace(string(data))
		if raw != "" {
			if !strings.HasPrefix(raw, "{") {
				raw = "{" + raw + "}"
			}
			_ = stdjson.Unmarshal([]byte(raw), &conf)
		}
	}

	conf["typesearch"] = "red"

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := stdjson.MarshalIndent(conf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func renderManifestFormHTML(existing map[string]bool, editMode bool) string {
	isChecked := func(dll string, def bool) string {
		key := strings.ToLower(strings.TrimSpace(dll))
		if v, ok := existing[key]; ok {
			if v {
				return "checked"
			}
			return ""
		}
		if def {
			return "checked"
		}
		return ""
	}

	btn := "Завершить настройку"
	if editMode {
		btn = "Изменить настройки"
	}

	return "<!DOCTYPE html><html><head><meta charset='utf-8'><title>Модули</title></head><body>" +
		"<style type='text/css'>*{box-sizing:border-box;outline:none}body{padding:40px;font-family:sans-serif}" +
		".flex{display:flex;align-items:center}input,select{margin:10px;margin-left:0}button{padding:10px}" +
		"form>*+*{margin-top:30px}</style>" +
		"<form method='post' action='/admin/manifest/install'>" +
		"<label>Установка модулей</label>" +
		"<div class='flex'><input name='online' type='checkbox' " + isChecked("Online.dll", true) + " /> Онлайн балансеры Rezka, Filmix, etc</div>" +
		"<div class='flex'>&nbsp; &nbsp; &nbsp; <input name='eng' type='checkbox' checked /> ENG балансеры</div>" +
		"<div class='flex'><input name='sisi' type='checkbox' " + isChecked("SISI.dll", true) + " /> Клубничка 18+, PornHub, Xhamster, etc</div>" +
		"<div class='flex'><input name='catalog' type='checkbox' " + isChecked("Catalog.dll", true) + " /> Альтернативные источники каталога cub и tmdb</div>" +
		"<div class='flex'><input name='dlna' type='checkbox' " + isChecked("DLNA.dll", true) + " /> DLNA - Загрузка торрентов и просмотр медиа файлов</div>" +
		"<div class='flex'><input name='ts' type='checkbox' " + isChecked("TorrServer.dll", true) + " /> TorrServer - просмотр торрентов онлайн</div>" +
		"<div class='flex'><input name='tracks' type='checkbox' " + isChecked("Tracks.dll", true) + " /> Tracks - транскодинг и названия аудиодорожек</div>" +
		"<div class='flex'><input name='merch' type='checkbox' " + isChecked("Merchant.dll", false) + " /> Автоматизация оплаты</div>" +
		"<br><br><label>Поиск торрентов</label>" +
		"<div class='flex'><input name='jac' type='radio' value='webapi' checked /> Быстрый поиск по внешним базам JacRed</div>" +
		"<div class='flex'><input name='jac' type='radio' value='fdb' /> Локальный jacred.xyz</div>" +
		"<button type='submit'>" + btn + "</button></form></body></html>"
}

func renderManifestSuccessHTML(host, adminPass, sharedPass, requestHost string) string {
	sharedBlock := "<div class='block'><b>Авторизация в Lampa</b><br /><br />Пароль: " + sharedPass + "</div><hr />"
	return "<!DOCTYPE html><html><head><meta charset='utf-8'><title>Настройка завершена</title></head><body>" +
		"<style type='text/css'>*{box-sizing:border-box;outline:none}body{padding:40px;font-family:sans-serif}" +
		"h1{color:#2b7a78;margin-bottom:1em;text-align:center}.block{margin-top:20px}hr{margin-top:1em;margin-bottom:2em}</style>" +
		"<h1>Настройка завершена</h1>" + sharedBlock +
		"<div class='block'><b>Админ панель</b><br /><br />Aдрес: " + host + "/admin<br />Пароль: " + adminPass + "</div><hr />" +
		"<div class='block'><b>Media Station X</b><br /><br />Settings -> Start Parameter -> Setup<br />" +
		"Enter current ip address and port: " + requestHost + "</div><hr />" +
		"<div class='block'><b>Виджет для Samsung</b><br /><br />" + host + "/samsung.wgt</div><hr />" +
		"<div class='block'><b>Для android apk</b><br /><br />Введите новый адрес: " + host + "</div><hr />" +
		"<div class='block'><b>Плагины для Lampa</b><br /><br />Добавьте плагин: " + host + "/on.js</div><hr />" +
		"<div class='block'><b>TorrServer (если установлен)</b><br /><br />" + host + "/ts</div>" +
		"</body></html>"
}
