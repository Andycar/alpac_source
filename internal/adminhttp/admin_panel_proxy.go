package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"lampac-go/internal/httpclient"
	"lampac-go/internal/tgauth"
)

// --- Proxy API ---

// proxyableBalancers is the full list of balancers available for VLESS proxy routing.
// Any balancer can benefit from proxy (geo-bypass, IP rotation, CDN unblocking).
var proxyableBalancers = []string{
	"Rezka", "RHSprem", "Collaps", "CollapsDash", "Kinotochka",
	"RutubeMovie", "VkMovie", "Plvideo", "CDNvideohub", "Kubikvkube",
	"Redheadsound", "iRemux", "Zetflix", "CDNmovies",
	"VDBmovies", "FanCDN", "Kinobase", "VideoCDN", "VCDN", "Lumex",
	"VoKino", "IframeVideo", "HDVB", "Vibix", "Videoseed", "Turbo", "Turbo_API",
	"KinoPub", "Alloha", "GetsTV", "Kodik", "Mirage",
	"Kinogo", "IptvOnline", "Filmix", "FilmixTV", "FXAPI", "FilmixPartner",
	"MoonAnime", "AnilibriaOnline", "AniLiberty", "Animebesst",
	"AniMedia", "Animevost", "AnimeGo", "AnimeLib",
	"Kinoukr", "Ashdi", "Eneyida", "VeoVeo", "Ebalovo",
	"Hydraflix", "Vidsrc", "VidLink", "Videasy", "Autoembed",
	"Rgshows", "Playembed", "Movpi", "Smashystream", "TwoEmbed",
	"YouTube", "tmdb", "Telegram",
}

// proxyEntryInfo describes a running VLESS proxy entry for the admin API.
type proxyEntryInfo struct {
	Index     int      `json:"index"`
	Label     string   `json:"label"`
	URI       string   `json:"uri"`
	Balancers []string `json:"balancers"`
	Engine    string   `json:"engine,omitempty"` // "xray", "mihomo", or "" (auto)
	Active    bool     `json:"active"`
	IP        string   `json:"ip,omitempty"`
	Country   string   `json:"country,omitempty"`
	Flag      string   `json:"flag,omitempty"`
}

func tgAdminProxyHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, proxyReloader ...func() error) http.HandlerFunc {
	var reloadFn func() error
	if len(proxyReloader) > 0 {
		reloadFn = proxyReloader[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			root := loadMergedConf()

			// Try multi-entry format first.
			var entries []proxyEntryInfo
			if section, ok := root["ProxyVless"].(map[string]any); ok {
				if rawEntries, ok := section["entries"].([]any); ok && len(rawEntries) > 0 {
					for i, rawEntry := range rawEntries {
						entryMap, ok := rawEntry.(map[string]any)
						if !ok {
							continue
						}
						uri := toStringAny(entryMap["uri"])
						label := toStringAny(entryMap["label"])
						engine := toStringAny(entryMap["engine"])
						var balancers []string
						if bl, ok := entryMap["balancers"].([]any); ok {
							for _, item := range bl {
								if s, ok := item.(string); ok && s != "" {
									balancers = append(balancers, s)
								}
							}
						}
						// Check if any of these balancers are active (have registered proxy transport).
						active := slices.ContainsFunc(balancers, httpclient.IsBalancerProxied)
						entries = append(entries, proxyEntryInfo{
							Index:     i,
							Label:     label,
							URI:       uri,
							Balancers: balancers,
							Engine:    engine,
							Active:    active,
						})
					}
				}
			}

			// Fallback to legacy single-entry format.
			if len(entries) == 0 {
				uri := ""
				var balancers []string
				if section, ok := root["ProxyVless"].(map[string]any); ok {
					uri = toStringAny(section["uri"])
					if bl, ok := section["balancers"].([]any); ok {
						for _, item := range bl {
							if s, ok := item.(string); ok && s != "" {
								balancers = append(balancers, s)
							}
						}
					}
				}
				if uri == "" {
					if zet, ok := root["Zetflix"].(map[string]any); ok {
						uri = toStringAny(zet["proxy"])
						if uri != "" && len(balancers) == 0 {
							balancers = []string{"Zetflix"}
						}
					}
				}
				if uri != "" {
					active := httpclient.ProxiedTransport != nil
					entries = append(entries, proxyEntryInfo{
						Index:     0,
						URI:       uri,
						Balancers: balancers,
						Active:    active,
					})
				}
			}

			// Also include legacy flat fields for backward compat.
			legacyURI := ""
			var legacyBalancers []string
			if len(entries) > 0 {
				legacyURI = entries[0].URI
				legacyBalancers = entries[0].Balancers
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"uri":                 legacyURI,
				"balancers":           legacyBalancers,
				"available_balancers": proxyableBalancers,
				"active":              httpclient.ProxiedTransport != nil,
				"entries":             entries,
			})

		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 131072))
			var req struct {
				Action    string   `json:"action"`
				URI       string   `json:"uri"`
				Balancers []string `json:"balancers"`
				// Multi-entry support:
				Entries []struct {
					URI       string   `json:"uri"`
					Balancers []string `json:"balancers"`
					Label     string   `json:"label"`
					Engine    string   `json:"engine"`
				} `json:"entries"`
				// Test specific entry by index:
				EntryIndex int `json:"entry_index"`
			}
			if stdjson.Unmarshal(body, &req) != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}

			switch req.Action {
			case "save":
				if err := updateConfigTOMLMap(func(root map[string]any) {
					section := setNestedMap(root, "proxy.vless")

					if len(req.Entries) > 0 {
						// Multi-entry save.
						var rawEntries []map[string]any
						for _, e := range req.Entries {
							entry := map[string]any{
								"uri":       strings.TrimSpace(e.URI),
								"balancers": e.Balancers,
								"label":     strings.TrimSpace(e.Label),
							}
							if eng := strings.TrimSpace(e.Engine); eng != "" {
								entry["engine"] = eng
							}
							rawEntries = append(rawEntries, entry)
						}
						section["entries"] = rawEntries
						// Clear legacy fields.
						delete(section, "uri")
						delete(section, "balancers")
					} else {
						// Legacy single-entry save.
						section["uri"] = strings.TrimSpace(req.URI)
						section["balancers"] = req.Balancers
					}
				}); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_needed": false})

			case "test":
				// Test a specific proxy entry by its index.
				// Look up the entry's label → SOCKS5 address, or fall back to its
				// first balancer's transport. This ensures we test the actual proxy
				// the user clicked, not always the first one.
				var transport *http.Transport
				if req.EntryIndex >= 0 {
					root := loadMergedConf()
					if section, ok := root["ProxyVless"].(map[string]any); ok {
						if rawEntries, ok := section["entries"].([]any); ok && req.EntryIndex < len(rawEntries) {
							if entryMap, ok := rawEntries[req.EntryIndex].(map[string]any); ok {
								// 1. Try label → SocksAddrForLabel (most reliable).
								label := toStringAny(entryMap["label"])
								if label != "" {
									if socksAddr := httpclient.SocksAddrForLabel(label); socksAddr != "" {
										transport = httpclient.NewSOCKS5TransportPublic(socksAddr)
									}
								}
								// 2. Fall back to first balancer's registered transport.
								if transport == nil {
									if bl, ok := entryMap["balancers"].([]any); ok && len(bl) > 0 {
										if s, ok := bl[0].(string); ok {
											transport = httpclient.TransportForBalancer(s)
										}
									}
								}
							}
						}
					}
				}
				if transport == nil {
					transport = httpclient.ProxiedTransport
				}
				if transport == nil {
					writeJSON(w, http.StatusOK, map[string]any{
						"ok": false, "error": "Прокси не активен. Сохраните URI и перезапустите сервер.",
					})
					return
				}

				client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
				ip, latency, err := probeExitIP(client)
				if err != nil {
					writeJSON(w, http.StatusOK, map[string]any{
						"ok": false, "error": err.Error(), "latency_ms": latency,
					})
					return
				}

				// Fetch GeoIP info for the exit IP.
				country, flag := lookupGeoIP(ip)

				writeJSON(w, http.StatusOK, map[string]any{
					"ok":         true,
					"ip":         ip,
					"country":    country,
					"flag":       flag,
					"latency_ms": latency,
				})

			case "reload":
				if reloadFn == nil {
					writeJSON(w, http.StatusOK, map[string]any{
						"ok": false, "error": "Hot-reload не поддерживается (reloader не инициализирован).",
					})
					return
				}
				if err := reloadFn(); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{
						"ok": false, "error": err.Error(),
					})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":      true,
					"message": "Прокси перезагружены. Изменения применены без перезапуска сервера.",
				})

			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
			}

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// probeExitIP tries a cascade of IP-echo endpoints through the given client and
// returns the first successful exit IP, latency of that attempt, and the last error.
// Cascade exists because httpbin.org frequently RSTs through residential SOCKS5 exits.
func probeExitIP(client *http.Client) (string, int64, error) {
	probes := []struct {
		url   string
		parse func([]byte) string
	}{
		{"https://api.ipify.org/?format=json", func(b []byte) string {
			var v struct {
				IP string `json:"ip"`
			}
			_ = stdjson.Unmarshal(b, &v)
			return v.IP
		}},
		{"https://ifconfig.me/ip", func(b []byte) string {
			return strings.TrimSpace(string(b))
		}},
		{"https://httpbin.org/ip", func(b []byte) string {
			var v struct {
				Origin string `json:"origin"`
			}
			_ = stdjson.Unmarshal(b, &v)
			return v.Origin
		}},
	}

	var lastErr error
	var lastLatency int64
	for _, p := range probes {
		start := time.Now()
		resp, err := client.Get(p.url)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			lastErr = err
			lastLatency = latency
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		ip := p.parse(body)
		if ip != "" {
			return ip, latency, nil
		}
		lastLatency = latency
	}
	if lastErr == nil {
		lastErr = errEmptyIP
	}
	return "", lastLatency, lastErr
}

var errEmptyIP = &probeError{"empty IP response"}

type probeError struct{ msg string }

func (e *probeError) Error() string { return e.msg }

// lookupGeoIP fetches country info for an IP address using ip-api.com.
// Returns (country name, flag emoji). Returns ("", "") on failure.
func lookupGeoIP(ip string) (string, string) {
	if ip == "" {
		return "", ""
	}
	client := httpclient.New(5 * time.Second)
	resp, err := client.Get("http://ip-api.com/json/" + ip + "?fields=country,countryCode")
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var geo struct {
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
	}
	if stdjson.Unmarshal(body, &geo) != nil {
		return "", ""
	}
	flag := countryCodeToFlag(geo.CountryCode)
	return geo.Country, flag
}

// countryCodeToFlag converts a 2-letter country code (e.g. "KZ") to a flag emoji (e.g. "🇰🇿").
func countryCodeToFlag(code string) string {
	if len(code) != 2 {
		return ""
	}
	code = strings.ToUpper(code)
	// Regional indicator symbols: U+1F1E6 (A) to U+1F1FF (Z)
	r1 := rune(0x1F1E6 + (rune(code[0]) - 'A'))
	r2 := rune(0x1F1E6 + (rune(code[1]) - 'A'))
	return string([]rune{r1, r2})
}
