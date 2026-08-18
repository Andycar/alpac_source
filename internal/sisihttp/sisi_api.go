package sisihttp

import (
	stdjson "encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

type sisiChannelItem struct {
	Title       string `json:"title"`
	PlaylistURL string `json:"playlist_url"`
	DisplayIdx  int    `json:"displayindex"`
}

type sisiSourceRule struct {
	ConfKey      string
	Title        string
	Path         string
	DisplayOrder int
}

type sisiSourceCfg struct {
	Enable       bool
	RIP          bool
	Spider       bool
	DisplayName  string
	DisplayIndex int
	Login        string
	Password     string
	Cookie       string
}

type sisiRuntimeCfg struct {
	LGBT          bool
	XDB           bool
	HistoryEnable bool
	Sources       map[string]sisiSourceCfg
}

var sisiLocalSources = []sisiSourceRule{
	{ConfKey: "PornHubPremium", Title: "pornhubpremium.com", Path: "phubprem"},
	{ConfKey: "PornHub", Title: "pornhub.com", Path: "phub"},
	{ConfKey: "Xvideos", Title: "xvideos.com", Path: "xds"},
	{ConfKey: "Xhamster", Title: "xhamster.com", Path: "xmr"},
	{ConfKey: "Ebalovo", Title: "ebalovo.porn", Path: "elo"},
	{ConfKey: "HQporner", Title: "hqporner.com", Path: "hqr"},
	{ConfKey: "Spankbang", Title: "spankbang.com", Path: "sbg"},
	{ConfKey: "Eporner", Title: "eporner.com", Path: "epr"},
	{ConfKey: "XvideosRED", Title: "xdsred", Path: "xdsred"},
	{ConfKey: "Xnxx", Title: "xnxx.com", Path: "xnx"},
	{ConfKey: "Tizam", Title: "tizam.pw", Path: "tizam"},
	{ConfKey: "BongaCams", Title: "bongacams.com", Path: "bgs"},
	{ConfKey: "Runetki", Title: "runetki.com", Path: "runetki"},
	{ConfKey: "Chaturbate", Title: "chaturbate.com", Path: "chu"},
	{ConfKey: "PornLab", Title: "PornLab", Path: "plab"},
	{ConfKey: "SexStudentki", Title: "Sex Studentki", Path: "sxst"},
}

func sisiIndexHandler(_ config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := hostFromRequest(r)
		spiderOnly := parseBoolParam(r.URL.Query().Get("spder"))
		state := loadSisiRuntimeCfg()

		channels := make([]sisiChannelItem, 0, 24)
		channels = append(channels, sisiChannelItem{
			Title:       "Bookmarks",
			PlaylistURL: host + "/sisi/bookmarks",
			DisplayIdx:  0,
		})
		if state.HistoryEnable {
			channels = append(channels, sisiChannelItem{
				Title:       "History",
				PlaylistURL: host + "/sisi/historys",
				DisplayIdx:  1,
			})
		}

		appendSource := func(rule sisiSourceRule) {
			cfg, ok := state.Sources[rule.ConfKey]
			if !ok {
				return
			}
			if !cfg.Enable || cfg.RIP {
				return
			}
			if spiderOnly && !cfg.Spider {
				return
			}

			title := rule.Title
			if strings.TrimSpace(cfg.DisplayName) != "" {
				title = strings.TrimSpace(cfg.DisplayName)
			}
			displayIdx := cfg.DisplayIndex
			if rule.DisplayOrder > 0 {
				displayIdx = rule.DisplayOrder
			}
			if displayIdx == 0 {
				displayIdx = 20 + len(channels)
			}

			channels = append(channels, sisiChannelItem{
				Title:       title,
				PlaylistURL: host + "/" + rule.Path,
				DisplayIdx:  displayIdx,
			})
		}

		for _, rule := range sisiLocalSources {
			appendSource(rule)
		}

		// Append JS SISI sources (goja-based).
		if ss := liveSisiSources(); ss != nil {
			idx := 300 // display below built-in sources
			for _, mod := range ss.List() {
				if !mod.Enabled {
					continue
				}
				title := mod.Manifest.Name
				if title == "" {
					title = mod.Manifest.ID
				}
				channels = append(channels, sisiChannelItem{
					Title:       title,
					PlaylistURL: host + "/sisi/cust/" + mod.Manifest.ID,
					DisplayIdx:  idx,
				})
				idx++
			}
		}

		sort.SliceStable(channels, func(i, j int) bool {
			if channels[i].DisplayIdx != channels[j].DisplayIdx {
				return channels[i].DisplayIdx < channels[j].DisplayIdx
			}
			return channels[i].Title < channels[j].Title
		})

		writeJSON(w, http.StatusOK, map[string]any{
			"title":    "sisi",
			"channels": channels,
		})
	}
}

func sisiModificationHandler(cfg config.Config) http.HandlerFunc {
	placeholderRe := regexp.MustCompile(`\{localhost\}/?`)

	return func(w http.ResponseWriter, r *http.Request) {
		var raw []byte
		var ok bool
		for _, candidate := range []string{
			filepath.Join(cfg.Compat.RepoRoot, "wwwroot", "sisi", "plugins", "modification.js"),
			filepath.Join("wwwroot", "sisi", "plugins", "modification.js"),
			filepath.Join("/home", "wwwroot", "sisi", "plugins", "modification.js"),
		} {
			b, err := os.ReadFile(candidate)
			if err == nil {
				raw = b
				ok = true
				break
			}
		}

		if !ok {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		src := string(raw)
		state := loadSisiRuntimeCfg()
		if !state.XDB {
			src = strings.ReplaceAll(src, "addId();", "")
		}
		src = placeholderRe.ReplaceAllString(src, hostFromRequest(r)+"/sisi")

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(src))
	}
}

func loadSisiRuntimeCfg() sisiRuntimeCfg {
	out := sisiRuntimeCfg{
		LGBT:          true,
		XDB:           false,
		HistoryEnable: true,
		Sources:       make(map[string]sisiSourceCfg, len(sisiLocalSources)),
	}

	// Defaults: all sources enabled.
	for _, src := range sisiLocalSources {
		if _, ok := out.Sources[src.ConfKey]; !ok {
			out.Sources[src.ConfKey] = sisiSourceCfg{
				Enable: true,
				Spider: true,
			}
		}
	}

	// --- Priority 1: TOML config [sisi] + [sisi.sources.*] ---
	if serverReady() {
		sisiCfg := liveConfig(config.Config{}).Sisi
		out.HistoryEnable = sisiCfg.HistoryEnable

		log.Debug().Int("toml_sources", len(sisiCfg.Sources)).Msg("sisi: loading TOML sources")
		for key, src := range sisiCfg.Sources {
			cur, ok := out.Sources[key]
			if !ok {
				cur = sisiSourceCfg{Enable: true, Spider: true}
			}
			cur.Enable = src.Enable
			cur.RIP = src.RIP
			cur.Spider = src.Spider
			if src.DisplayName != "" {
				cur.DisplayName = src.DisplayName
			}
			if src.DisplayIndex > 0 {
				cur.DisplayIndex = src.DisplayIndex
			}
			if src.Login != "" {
				cur.Login = src.Login
			}
			if src.Password != "" {
				cur.Password = src.Password
			}
			if src.Cookie != "" {
				cur.Cookie = src.Cookie
			}
			out.Sources[key] = cur
		}
	}

	// --- Priority 2: init.conf fallback (legacy JSON) ---
	data, ok := readFileAny("init.conf")
	if !ok {
		return out
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return out
	}

	if sisi, ok := root["sisi"].(map[string]any); ok {
		if b, ok := boolField(sisi, "lgbt"); ok {
			out.LGBT = b
		}
		if b, ok := boolField(sisi, "xdb"); ok {
			out.XDB = b
		}
		if history, ok := sisi["history"].(map[string]any); ok {
			if b, ok := boolField(history, "enable", "enabled"); ok {
				out.HistoryEnable = b
			}
		}
	}

	for key := range out.Sources {
		raw, ok := root[key].(map[string]any)
		if !ok {
			continue
		}

		cur := out.Sources[key]
		if b, ok := boolField(raw, "enable", "enabled"); ok {
			cur.Enable = b
		}
		if b, ok := boolField(raw, "rip", "disabled"); ok {
			cur.RIP = b
		}
		if b, ok := boolField(raw, "spider"); ok {
			cur.Spider = b
		}
		if name := strings.TrimSpace(toString(raw["displayname"])); name != "" {
			cur.DisplayName = name
		} else if name := strings.TrimSpace(toString(raw["display_name"])); name != "" {
			cur.DisplayName = name
		}
		if idx, ok := intField(raw, "displayindex", "display_index"); ok {
			cur.DisplayIndex = idx
		}
		if v := strings.TrimSpace(toString(raw["login"])); v != "" {
			cur.Login = v
		}
		if v := strings.TrimSpace(toString(raw["password"])); v != "" {
			cur.Password = v
		}
		if v := strings.TrimSpace(toString(raw["cookie"])); v != "" {
			cur.Cookie = v
		}
		out.Sources[key] = cur
	}

	return out
}

func boolField(raw map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		v, ok := raw[key]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case bool:
			return t, true
		case string:
			s := strings.TrimSpace(t)
			if s == "" {
				continue
			}
			return parseBoolLike(s), true
		default:
			s := strings.TrimSpace(toString(v))
			if s == "" {
				continue
			}
			return parseBoolLike(s), true
		}
	}
	return false, false
}

func intField(raw map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		v, ok := raw[key]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int(t), true
		case float32:
			return int(t), true
		case int:
			return t, true
		case int64:
			return int(t), true
		case string:
			s := strings.TrimSpace(t)
			if s == "" {
				continue
			}
			if n, err := strconv.Atoi(s); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}
