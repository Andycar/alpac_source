package httpapi

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/custbal"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/tgauth"
)

// --- Balancers API (full fields) ---

var knownBalancers = []string{
	"Rezka", "Rhsprem", "AhueRezka", "Collaps", "Collaps-dash", "Kinotochka",
	"RutubeMovie", "Anwap", "VkMovie", "Plvideo",
	"CDNvideohub", "Kubikvkube", "Redheadsound", "iRemux", "Zetflix", "ZetflixDB", "VideoDB", "CDNmovies",
	"VDBmovies", "FanCDN", "Kinobase", "VideoCDN", "Lumex", "VoKino", "Zona",
	"IframeVideo", "HDVB", "Vibix", "Videoseed", "KinoPub", "Alloha",
	"GetsTV", "Hydraflix", "Vidsrc", "VidLink", "Videasy", "Autoembed",
	"Rgshows", "Kodik", "Mirage", "Aladdin", "Kinogo", "IptvOnline", "Filmix",
	"MoonAnime", "AnilibriaOnline", "AniLiberty", "Animebesst", "AniMedia",
	"Animevost", "AnimeGo", "AnimeLib", "Kinoukr", "Ashdi", "Eneyida",
	"Bamboo", "Unimay", "StarLight", "KlonFUN", "Uaflix", "AnimeON",
	"Mikai", "LeProduction",
	"VeoVeo", "FilmixPartner",
	"PidTor", "Mirkino",
	// Реализованы в localCorePlugins, но исторически не попали сюда — из-за
	// этого их не было ни в пер-юзерной видимости, ни в healthcheck, ни в
	// телеметрии, а в whitelist-режиме кита они пропадали из выдачи.
	"Lift", "Kinovod", "Turbo", "SakhTV", "FEMD", "Gencit", "Kinobadi",
	"UaKino",
	"FlixCDN", "TwoEmbed", "Uafilm", "Youtube",
}

// balancerPluginKey maps PascalCase config names to the actual lowercase
// plugin key used in /lite/events and with_search. Only entries where
// strings.ToLower(name) differs from the actual plugin key are listed.
var balancerPluginKey = map[string]string{
	"AnilibriaOnline": "anilibria",
	"SakhTV":          "sakhtv",
	"FEMD":            "femd",
	"FlixCDN":         "flixcdn",
	"TwoEmbed":        "twoembed",
	"Collaps-dash":    "collaps-dash",
	"Rhsprem":         "rhsprem",
	"KlonFUN":         "klonfun",
	"AnimeON":         "animeon",
	"PidTor":          "pidtor",
}

// PluginKeyFor returns the actual lowercase plugin key for a knownBalancer entry.
func PluginKeyFor(name string) string {
	if k, ok := balancerPluginKey[name]; ok {
		return k
	}
	return strings.ToLower(name)
}

var balancerStatusTags = map[string]string{
	"Anwap":        "working",
	"Kinotochka":   "working",
	"AhueRezka":    "working",
	"Collaps":      "working",
	"Mirage":       "working",
	"Aladdin":      "working",
	"Videoseed":    "working",
	"Filmix":       "intermittent",
	"Kinobase":     "search_only",
	"Redheadsound": "working",
	"VDBmovies":    "blocked",
	"Kinogo":       "blocked",
	"CDNmovies":    "dead",
	"Zetflix":      "working",
	"ZetflixDB":    "working",
	"VideoDB":      "intermittent",
	"Kinoukr":      "working",
	"Vibix":        "needs_token",
	"Ashdi":        "working",
	"Bamboo":       "working",
	"Unimay":       "working",
	"StarLight":    "working",
	"KlonFUN":      "working",
	"Uaflix":       "working",
	"AnimeON":      "working",
	"Mikai":        "working",
	"Eneyida":      "working",
	"PidTor":       "working",
	"LeProduction": "working",
	"Zona":         "working",
	"Mirkino":      "needs_token",
	"Lift":         "working",
	"Kinovod":      "working",
	"Turbo":        "working",
	"SakhTV":       "working",
	"FEMD":         "working",
	"Gencit":       "working",
	"Kinobadi":     "working",
	"FlixCDN":      "working",
	"TwoEmbed":     "working",
	"Uafilm":       "working",
	"Youtube":      "working",
	"UaKino":       "working",
}

type balancerFullInfo struct {
	Name      string         `json:"name"`
	Fields    map[string]any `json:"fields"`
	StatusTag string         `json:"status_tag,omitempty"`
	Group     string         `json:"group,omitempty"`
	Quality   string         `json:"quality,omitempty"`
}

// balancerGroupMap categorises each known balancer into a UI group.
var balancerGroupMap = map[string]string{
	// Русские (основные)
	"Rezka": "ru", "Rhsprem": "ru", "AhueRezka": "ru", "Collaps": "ru", "Collaps-dash": "ru",
	"Kinotochka":   "ru",
	"Redheadsound": "ru", "Zetflix": "ru", "ZetflixDB": "ru", "Filmix": "ru",
	"FilmixPartner": "ru", "CDNmovies": "ru", "VDBmovies": "ru",
	"FanCDN": "ru", "Kinobase": "ru", "Mirage": "ru", "Aladdin": "ru",
	"Kinogo": "ru", "VoKino": "ru", "Lumex": "ru", "Zona": "ru",
	"IframeVideo": "ru", "HDVB": "ru", "Vibix": "ru",
	"Videoseed": "ru", "KinoPub": "ru", "Alloha": "ru", "VideoDB": "ru",
	"VideoCDN": "ru", "iRemux": "ru", "CDNvideohub": "ru",
	"Kodik": "ru", "PidTor": "ru", "LeProduction": "ru", "Mirkino": "ru",
	"Kubikvkube": "ru",
	"Lift":       "ru", "Kinovod": "ru", "Turbo": "ru", "SakhTV": "ru",
	"FEMD": "ru", "Kinobadi": "ru", "FlixCDN": "ru",
	// Аниме
	"MoonAnime": "anime", "AnilibriaOnline": "anime", "AniLiberty": "anime",
	"Animebesst": "anime", "AniMedia": "anime", "Animevost": "anime",
	"AnimeGo": "anime", "AnimeLib": "anime",
	// Украинские
	"Kinoukr": "ua", "Ashdi": "ua", "Eneyida": "ua",
	"Bamboo": "ua", "Unimay": "ua", "StarLight": "ua", "KlonFUN": "ua",
	"Uaflix": "ua", "AnimeON": "ua", "Mikai": "ua",
	"Uafilm": "ua", "Gencit": "ua", "UaKino": "ua",
	// Видео / ТВ
	"RutubeMovie": "video", "Anwap": "ru", "VkMovie": "video", "Plvideo": "video",
	"GetsTV": "video", "IptvOnline": "video", "VeoVeo": "video",
	"Youtube": "video",
	// Английские
	"Hydraflix": "en", "Vidsrc": "en", "VidLink": "en",
	"Videasy": "en", "Autoembed": "en", "Rgshows": "en", "TwoEmbed": "en",
}

// balancerGroupOrder defines the rendering order and display names of groups.
var balancerGroupOrder = []struct {
	Key   string
	Label string
	Icon  string
}{
	{"ru", "Русские", "\U0001F3AC"},              // 🎬
	{"anime", "Аниме", "\U0001F38C"},             // 🎌
	{"ua", "Украинские", "\U0001F1FA\U0001F1E6"}, // 🇺🇦
	{"video", "Видео / ТВ", "\U0001F4FA"},        // 📺
	{"en", "Английские", "\U0001F30D"},           // 🌍
	{"other", "Другие", "\U00002699\U0000FE0F"},  // ⚙️
}

// adminQualityRank maps quality badge to a sort rank (lower = better).
var adminQualityRank = map[string]int{"4K": 0, "FHD": 1, "SD": 2, "": 3}

func tgAdminBalancersHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, pool *custbal.Pool, dynRoutes *DynamicRouteRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			root := loadMergedConf()

			// Build no_stream_proxy set from config.
			noProxySet := map[string]struct{}{}
			if serverReady() {
				for _, p := range liveConfig(config.Config{}).Online.NoStreamProxy {
					noProxySet[strings.ToLower(strings.TrimSpace(p))] = struct{}{}
				}
			}

			// Build balancer→proxy label map from proxy config entries.
			proxyLabelMap := map[string]string{} // balancer name (case-insensitive) → label
			var proxyLabels []string
			if pvSection, ok := root["ProxyVless"].(map[string]any); ok {
				if rawEntries, ok := pvSection["entries"].([]any); ok {
					for _, rawEntry := range rawEntries {
						entryMap, ok := rawEntry.(map[string]any)
						if !ok {
							continue
						}
						label := toStringAny(entryMap["label"])
						if label != "" {
							proxyLabels = append(proxyLabels, label)
						}
						if bl, ok := entryMap["balancers"].([]any); ok {
							for _, item := range bl {
								if s, ok := item.(string); ok && s != "" {
									proxyLabelMap[strings.ToLower(s)] = label
								}
							}
						}
					}
				}
			}
			if pdSection, ok := root["ProxyDirect"].(map[string]any); ok {
				if rawEntries, ok := pdSection["entries"].([]any); ok {
					for _, rawEntry := range rawEntries {
						entryMap, ok := rawEntry.(map[string]any)
						if !ok {
							continue
						}
						label := toStringAny(entryMap["label"])
						if label != "" {
							proxyLabels = append(proxyLabels, label)
						}
						if bl, ok := entryMap["balancers"].([]any); ok {
							for _, item := range bl {
								if s, ok := item.(string); ok && s != "" {
									proxyLabelMap[strings.ToLower(s)] = label
								}
							}
						}
					}
				}
			}

			out := make([]balancerFullInfo, 0, len(knownBalancers)+8)
			for _, name := range knownBalancers {
				grp := balancerGroupMap[name]
				if grp == "" {
					grp = "other"
				}
				q := pluginQualityBadgeGet(strings.ToLower(name))
				bi := balancerFullInfo{Name: name, StatusTag: balancerStatusTags[name], Group: grp, Quality: q}
				section, ok := root[name].(map[string]any)
				if ok {
					// Copy the full section
					bi.Fields = make(map[string]any, len(section))
					maps.Copy(bi.Fields, section)
				} else {
					bi.Fields = map[string]any{}
				}
				// Add proxy assignment info.
				if label, ok := proxyLabelMap[strings.ToLower(name)]; ok {
					bi.Fields["_proxy_label"] = label
				}
				// Stream proxy toggle: true = proxy enabled (default), false = bypass.
				pluginKey := PluginKeyFor(name)
				_, noProxy := noProxySet[pluginKey]
				bi.Fields["_stream_proxy"] = !noProxy
				out = append(out, bi)
			}
			// Append custom balancers from pool.
			if pool != nil {
				builtIn := make(map[string]struct{}, len(knownBalancers))
				for _, n := range knownBalancers {
					builtIn[strings.ToLower(n)] = struct{}{}
				}
				for _, st := range pool.List() {
					if _, ok := builtIn[strings.ToLower(st.Name)]; ok {
						continue // already in built-in list
					}
					proc, _ := pool.Get(st.Name)
					upstreamHost := ""
					autoStart := false
					if proc != nil {
						upstreamHost = proc.Config.Host
						autoStart = proc.Config.AutoStart
					}
					bi := balancerFullInfo{
						Name:      st.Name,
						StatusTag: "custom_" + string(st.State),
						Fields: map[string]any{
							"enabled":       st.State == custbal.StateRunning,
							"host":          fmt.Sprintf("127.0.0.1:%d", st.Port),
							"port":          st.Port,
							"quality_badge": st.QualityBadge,
							"display_name":  st.DisplayName,
							"upstream_host": upstreamHost,
							"auto_start":    autoStart,
							"custom":        true,
						},
					}
					out = append(out, bi)
				}
			}
			// Get current with_search order and sort preference.
			var withSearch []string
			customOrder := false
			if serverReady() {
				withSearch = liveConfig(config.Config{}).Online.WithSearch
				customOrder = liveConfig(config.Config{}).Online.CustomOrder
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"balancers":    out,
				"proxy_labels": proxyLabels,
				"with_search":  withSearch,
				"custom_order": customOrder,
			})

		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 131072))
			var req struct {
				Action      string         `json:"action"` // "save", "test", or "reorder"
				Name        string         `json:"name"`
				Fields      map[string]any `json:"fields,omitempty"`
				Order       []string       `json:"order,omitempty"`        // for "reorder" action
				CustomOrder *bool          `json:"custom_order,omitempty"` // for "reorder" action
			}
			if stdjson.Unmarshal(body, &req) != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}

			// Check if this is a custom balancer.
			isCustom := false
			if pool != nil {
				if _, found := pool.Get(req.Name); found {
					isCustom = true
				}
			}

			switch req.Action {
			case "save":
				if req.Name == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
					return
				}

				if isCustom {
					// Custom balancer: save settings to config.json in custom_balancers/{name}/
					proc, _ := pool.Get(req.Name)
					cfg := proc.Config
					if v, ok := req.Fields["quality_badge"]; ok {
						cfg.QualityBadge = fmt.Sprint(v)
					}
					if v, ok := req.Fields["display_name"]; ok {
						cfg.DisplayName = fmt.Sprint(v)
					}
					if v, ok := req.Fields["content_type"]; ok {
						cfg.ContentType = fmt.Sprint(v)
					}
					if v, ok := req.Fields["auto_start"]; ok {
						cfg.AutoStart = toBoolAny(v)
					}
					if v, ok := req.Fields["upstream_host"]; ok {
						cfg.Host = fmt.Sprint(v)
					}
					if err := custbal.SaveConfig(proc.Dir, cfg); err != nil {
						writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
						return
					}
					// Update in-memory config.
					proc.Config = cfg
					// Update quality badge in global map.
					if cfg.QualityBadge != "" {
						pluginQualityBadgeSet(cfg.Name, cfg.QualityBadge)
					}

					// Handle enable/disable toggle — start/stop subprocess.
					wantEnabled := toBoolAny(req.Fields["enabled"]) || toBoolAny(req.Fields["enable"])
					isRunning := proc.State() == custbal.StateRunning
					if wantEnabled && !isRunning {
						if err := proc.Start(); err != nil {
							writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "start failed: " + err.Error()})
							return
						}
						if dynRoutes != nil {
							dynRoutes.Register(req.Name, proc.Addr())
						}
					} else if !wantEnabled && isRunning {
						proc.Stop()
						if dynRoutes != nil {
							dynRoutes.Unregister(req.Name)
						}
					}

					needRestart := false
					if v, ok := req.Fields["upstream_host"]; ok && fmt.Sprint(v) != "" {
						needRestart = proc.State() == custbal.StateRunning
					}
					writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_needed": needRestart})
					return
				}

				// Extract _proxy_label before saving (it's not a balancer field).
				wantProxy := ""
				hasProxyField := false
				if v, ok := req.Fields["_proxy_label"]; ok {
					hasProxyField = true
					wantProxy = toStringAny(v)
					delete(req.Fields, "_proxy_label")
				}

				// Extract _stream_proxy before saving (it's not a balancer field).
				hasStreamProxy := false
				streamProxyEnabled := true
				if v, ok := req.Fields["_stream_proxy"]; ok {
					hasStreamProxy = true
					streamProxyEnabled = toBoolAny(v)
					delete(req.Fields, "_stream_proxy")
				}

				// Normalize enable/enabled: TOML uses "enable", remove legacy "enabled" duplicate.
				if _, hasEn := req.Fields["enable"]; hasEn {
					delete(req.Fields, "enabled")
				} else if v, hasEnd := req.Fields["enabled"]; hasEnd {
					// Convert legacy "enabled" → "enable".
					req.Fields["enable"] = v
					delete(req.Fields, "enabled")
				}

				// Built-in balancer: save fields into config.toml using TOML path mapping.
				tomlPath, pathOK := jsonToTOMLPath[req.Name]
				if !pathOK {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "unknown balancer: " + req.Name})
					return
				}
				if err := updateConfigTOMLMap(func(root map[string]any) {
					section := setNestedMap(root, tomlPath)
					maps.Copy(section, req.Fields)
					// Clean up legacy "enabled" key if present in TOML section.
					delete(section, "enabled")

					// Sync with_search list when enable/disable changes.
					enableVal, hasEnable := req.Fields["enable"]
					if hasEnable {
						pluginKey := PluginKeyFor(req.Name)
						online := setNestedMap(root, "online")
						syncWithSearch(online, pluginKey, toBoolAny(enableVal))
					}

					// Update proxy assignment.
					if hasProxyField {
						syncProxyBalancer(root, req.Name, wantProxy)
					}

					// Update stream proxy bypass (no_stream_proxy list).
					if hasStreamProxy {
						pluginKey := PluginKeyFor(req.Name)
						online := setNestedMap(root, "online")
						syncNoStreamProxy(online, pluginKey, !streamProxyEnabled)
					}
				}); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_needed": hasProxyField})

			case "test":
				if req.Name == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
					return
				}

				if isCustom {
					// Custom balancer: test via checksearch to localhost subprocess.
					proc, _ := pool.Get(req.Name)
					st := proc.Status()
					if st.State != custbal.StateRunning {
						writeJSON(w, http.StatusOK, map[string]any{
							"ok": false, "error": "subprocess not running (state: " + string(st.State) + ")",
						})
						return
					}
					start := time.Now()
					testURL := fmt.Sprintf("http://127.0.0.1:%d/?checksearch=true&title=test", st.Port)
					client := httpclient.New(10 * time.Second)
					resp, err := client.Get(testURL)
					latency := time.Since(start).Milliseconds()
					if err != nil {
						writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "latency_ms": latency})
						return
					}
					resp.Body.Close()
					writeJSON(w, http.StatusOK, map[string]any{
						"ok": resp.StatusCode < 500, "status": resp.StatusCode, "latency_ms": latency,
						"custom": true, "port": st.Port, "pid": st.PID,
					})
					return
				}

				// Built-in balancer: test upstream host.
				root := loadMergedConf()
				section, ok := root[req.Name].(map[string]any)
				if !ok {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "not configured"})
					return
				}
				host := toStringAny(section["host"])
				if host == "" {
					host = toStringAny(section["apihost"])
				}
				if host == "" {
					host = toStringAny(section["api_host"])
				}
				if host == "" {
					host = toStringAny(section["linkhost"])
				}
				if host == "" {
					host = toStringAny(section["link_host"])
				}
				if host == "" {
					host = toStringAny(section["redapi"])
				}
				if host == "" {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "no host"})
					return
				}
				start := time.Now()
				client := httpclient.New(10 * time.Second)
				testURL := host
				if !strings.HasPrefix(testURL, "http") {
					testURL = "https://" + testURL
				}
				resp, err := client.Get(testURL)
				latency := time.Since(start).Milliseconds()
				if err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "latency_ms": latency})
					return
				}
				resp.Body.Close()
				// API hosts (e.g. kodik-api.com) may return 500 on bare GET /
				// when no token is provided — that still means the host is alive.
				// Accept any response code < 502 (Bad Gateway) as reachable.
				writeJSON(w, http.StatusOK, map[string]any{
					"ok": resp.StatusCode < 502, "status": resp.StatusCode, "latency_ms": latency,
				})

			case "reorder":
				// Reorder the with_search list and/or toggle custom_order.
				if err := updateConfigTOMLMap(func(root map[string]any) {
					online := setNestedMap(root, "online")
					if len(req.Order) > 0 {
						result := make([]any, len(req.Order))
						for i, s := range req.Order {
							result[i] = s
						}
						online["with_search"] = result
					}
					if req.CustomOrder != nil {
						online["custom_order"] = *req.CustomOrder
					}
					// Clean up legacy key.
					delete(online, "sort_by_quality")
				}); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})

			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
			}

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// syncWithSearch adds or removes a plugin key from the with_search list
// in the [online] section of the TOML config.
func syncWithSearch(online map[string]any, pluginKey string, enabled bool) {
	rawList, _ := online["with_search"].([]any)

	// Convert to string slice.
	ws := make([]string, 0, len(rawList))
	for _, v := range rawList {
		if s, ok := v.(string); ok {
			ws = append(ws, s)
		}
	}

	found := -1
	for i, s := range ws {
		if strings.EqualFold(strings.TrimSpace(s), pluginKey) {
			found = i
			break
		}
	}

	if enabled && found < 0 {
		ws = append(ws, pluginKey)
	} else if !enabled && found >= 0 {
		ws = append(ws[:found], ws[found+1:]...)
	} else {
		return // no change needed
	}

	// Write back as []any for TOML marshal.
	result := make([]any, len(ws))
	for i, s := range ws {
		result[i] = s
	}
	online["with_search"] = result
}

// syncProxyBalancer adds or moves a balancer to the proxy entry with the given
// label, or removes it from all entries if wantLabel is empty.
// Operates on the TOML root map inside updateConfigTOMLMap.
func syncProxyBalancer(root map[string]any, balancerName, wantLabel string) {
	// Helper: update balancers list in a single proxy entry.
	updateEntry := func(entryMap map[string]any, add bool) {
		rawBals, _ := entryMap["balancers"].([]any)
		var bals []string
		for _, v := range rawBals {
			if s, ok := v.(string); ok && !strings.EqualFold(s, balancerName) {
				bals = append(bals, s)
			}
		}
		if add {
			bals = append(bals, balancerName)
		}
		result := make([]any, len(bals))
		for i, s := range bals {
			result[i] = s
		}
		entryMap["balancers"] = result
	}

	// Process proxy.vless.entries.
	for _, sectionKey := range []string{"proxy"} {
		proxySection, ok := root[sectionKey].(map[string]any)
		if !ok {
			continue
		}
		for _, subKey := range []string{"vless", "direct"} {
			sub, ok := proxySection[subKey].(map[string]any)
			if !ok {
				continue
			}
			rawEntries, ok := sub["entries"].([]any)
			if !ok {
				continue
			}
			for _, rawEntry := range rawEntries {
				entryMap, ok := rawEntry.(map[string]any)
				if !ok {
					continue
				}
				label := toStringAny(entryMap["label"])
				if wantLabel != "" && label == wantLabel {
					updateEntry(entryMap, true)
				} else {
					updateEntry(entryMap, false)
				}
			}
		}
	}
}

// syncNoStreamProxy adds or removes a plugin key from the no_stream_proxy list
// in the [online] section of the TOML config.
// disabled=true → add to list (bypass proxy), disabled=false → remove from list.
func syncNoStreamProxy(online map[string]any, pluginKey string, disabled bool) {
	rawList, _ := online["no_stream_proxy"].([]any)

	// Convert to string slice, filtering out the target key.
	var nsp []string
	for _, v := range rawList {
		if s, ok := v.(string); ok && !strings.EqualFold(strings.TrimSpace(s), pluginKey) {
			nsp = append(nsp, s)
		}
	}

	if disabled {
		nsp = append(nsp, pluginKey)
	}

	// Write back as []any for TOML marshal.
	result := make([]any, len(nsp))
	for i, s := range nsp {
		result[i] = s
	}
	online["no_stream_proxy"] = result
}
