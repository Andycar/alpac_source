package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"strings"
)

type openstatSettings struct {
	Enable bool
	Token  string
}

func openstatBrowserContextHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !openstatAllowed(w, r) {
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"Chromium": map[string]any{
				"open":           0,
				"req_keepopen":   0,
				"req_newcontext": 0,
				"ping": map[string]any{
					"status": "",
					"time":   "",
					"ex":     "",
				},
			},
			"Firefox": map[string]any{
				"open":           0,
				"req_keepopen":   0,
				"req_newcontext": 0,
			},
		})
	}
}

func openstatRequestsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !openstatAllowed(w, r) {
			return
		}

		snap := runtimeRequestStats.snapshot()
		responseMs := map[string]any{
			"avg": snap.LatencyAvgMs,
		}
		for key, val := range snap.Percentiles {
			responseMs[key] = val
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"req_min":           snap.ReqMin,
			"req_hour":          snap.ReqHour,
			"tcpConnections":    0,
			"nws_online":        0,
			"soks_online":       0,
			"http_active":       snap.Active,
			"http_response_ms":  responseMs,
			"top_routes":        snap.TopRoutes,
			"top_proxy_plugins": snap.TopProxyPlugins,
		})
	}
}

func openstatRchHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !openstatAllowed(w, r) {
			return
		}

		rchClientsMu.RLock()
		clients := len(rchClients)
		rchClientsMu.RUnlock()

		writeJSON(w, http.StatusOK, map[string]any{
			"clients": clients,
			"counter": map[string]any{
				"receive": 0,
				"send":    0,
			},
			"rchIds": clients,
		})
	}
}

func openstatTempDBHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !openstatAllowed(w, r) {
			return
		}

		rchClientsMu.RLock()
		clients := len(rchClients)
		rchClientsMu.RUnlock()

		writeJSON(w, http.StatusOK, map[string]any{
			"HybridCache":     0,
			"HybridFileCache": 0,
			"ProxyLink":       0,
			"ProxyAPI":        0,
			"ProxyTmdb":       0,
			"ProxyImg":        0,
			"ProxyCub":        0,
			"SemaphorManager": 0,
			"rch": map[string]any{
				"clients": clients,
				"Ids":     clients,
			},
			"pool": map[string]any{
				"msm": map[string]any{
					"SmallPoolInUseSize": 0,
					"LargePoolInUseSize": 0,
					"SmallBlocksFree":    0,
					"SmallPoolFreeSize":  0,
					"LargeBuffersFree":   0,
					"LargePoolFreeSize":  0,
				},
				"StringBuilder": map[string]any{
					"Rent": 0,
					"Free": 0,
					"GC":   0,
				},
				"MemoryStream": nil,
			},
			"memoryCache": nil,
		})
	}
}

func openstatAllowed(w http.ResponseWriter, r *http.Request) bool {
	settings := loadOpenstatSettings()
	if !settings.Enable {
		writePlain(w, http.StatusOK, "Включите openstat в init.conf\n\n\"openstat\": {\n   \"enable\": true\n}")
		return false
	}

	token := strings.TrimSpace(settings.Token)
	if token != "" && token != strings.TrimSpace(r.URL.Query().Get("token")) {
		writePlain(w, http.StatusOK, "Используйте /stats/request?token=my_key\n\n\"openstat\": {\n   \"enable\": true,\n   \"token\": \"my_key\"\n}")
		return false
	}
	return true
}

func loadOpenstatSettings() openstatSettings {
	data, ok := readFileAny("init.conf")
	if !ok {
		return openstatSettings{}
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return openstatSettings{}
	}

	openstat, ok := root["openstat"].(map[string]any)
	if !ok {
		return openstatSettings{}
	}

	return openstatSettings{
		Enable: toBool(openstat["enable"]),
		Token:  strings.TrimSpace(toString(openstat["token"])),
	}
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
