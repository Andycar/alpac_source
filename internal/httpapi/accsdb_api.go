package httpapi

import (
	stdjson "encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
)

type accsdbSettings struct {
	SharedPasswd  string
	SharedDaytime int
}

func testAccsdbHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If TG-auth is active, check via cookie/token first.
		// The middleware already sets the cookie, but /testaccsdb is
		// whitelisted (kept for any external probes / legacy clients).
		// If no valid TG token → accsdb:true with auth message.
		if serverReady() && kitTGTokenStore != nil {
			cfg := liveConfig(config.Config{})
			if cfg.TelegramAuth.BotName != "" {
				hasTGToken := false
				if c, err := r.Cookie("lampac_token"); err == nil {
					token := strings.TrimSpace(c.Value)
					if token != "" {
						if _, ok := kitTGTokenStore.Lookup(token); ok {
							hasTGToken = true
						}
					}
				}
				if !hasTGToken {
					if qToken := strings.TrimSpace(r.URL.Query().Get("token")); qToken != "" {
						if _, ok := kitTGTokenStore.Lookup(qToken); ok {
							hasTGToken = true
						}
					}
				}
				if !hasTGToken {
					writeJSON(w, http.StatusOK, map[string]any{
						"accsdb": true,
						"msg":    "Авторизация\n\nОтправьте код боту @" + cfg.TelegramAuth.BotName,
					})
					return
				}
				// TG-authorized — pass through
				writeJSON(w, http.StatusOK, map[string]any{
					"accsdb":  false,
					"success": true,
				})
				return
			}
		}

		// Legacy accsdb flow (shared password)
		settings := loadAccsdbSettings()

		accountEmail := strings.TrimSpace(r.URL.Query().Get("account_email"))
		uid := strings.TrimSpace(r.URL.Query().Get("uid"))

		if settings.SharedPasswd != "" && uid == settings.SharedPasswd {
			writeJSON(w, http.StatusOK, map[string]any{
				"accsdb": true,
				"newuid": true,
			})
			return
		}

		if uid != "" && accountEmail != "" && accountEmail == settings.SharedPasswd {
			added, err := appendAccsdbUser(uid, settings.SharedDaytime)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"accsdb": true})
				return
			}
			if !added {
				writeJSON(w, http.StatusOK, map[string]any{"accsdb": false})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"accsdb":  false,
				"success": true,
				"uid":     uid,
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"accsdb":  false,
			"success": true,
		})
	}
}

func loadAccsdbSettings() accsdbSettings {
	out := accsdbSettings{
		SharedDaytime: 1,
	}

	data, ok := readFileAny("init.conf")
	if !ok {
		return out
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return out
	}

	node, ok := root["accsdb"].(map[string]any)
	if !ok {
		return out
	}

	out.SharedPasswd = strings.TrimSpace(toString(node["shared_passwd"]))
	if v, ok := node["shared_daytime"]; ok {
		switch t := v.(type) {
		case float64:
			out.SharedDaytime = int(math.Max(1, t))
		case int:
			if t > 0 {
				out.SharedDaytime = t
			}
		case string:
			if parsed := strings.TrimSpace(t); parsed != "" {
				if n, err := strconv.Atoi(parsed); err == nil && n > 0 {
					out.SharedDaytime = n
				}
			}
		}
	}
	if out.SharedDaytime < 1 {
		out.SharedDaytime = 1
	}
	return out
}

func usersPath() string {
	return relToRuntime("users.json")
}

func readUsers() ([]map[string]any, error) {
	path := usersPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []map[string]any{}, nil
		}
		return nil, err
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return []map[string]any{}, nil
	}

	var arr []map[string]any
	if err := stdjson.Unmarshal(data, &arr); err != nil {
		return []map[string]any{}, nil
	}
	return arr, nil
}

func writeUsers(arr []map[string]any) error {
	path := usersPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	data, err := stdjson.MarshalIndent(arr, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func appendAccsdbUser(uid string, daytime int) (bool, error) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return false, nil
	}

	arr, err := readUsers()
	if err != nil {
		return false, err
	}

	for _, obj := range arr {
		if strings.EqualFold(strings.TrimSpace(toString(obj["id"])), uid) {
			return false, nil
		}
		if rawIDs, ok := obj["ids"].([]any); ok {
			for _, one := range rawIDs {
				if strings.EqualFold(strings.TrimSpace(toString(one)), uid) {
					return false, nil
				}
			}
		}
	}

	if daytime < 1 {
		daytime = 1
	}

	arr = append(arr, map[string]any{
		"id":      uid,
		"expires": time.Now().Add(time.Duration(daytime) * 24 * time.Hour).Format(time.RFC3339),
	})

	if err := writeUsers(arr); err != nil {
		return false, err
	}
	return true, nil
}
