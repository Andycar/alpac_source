package adminhttp

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

const maxBackups = 20

// tgAdminConfigTOMLHandler serves the TOML config editor API.
//
//	GET  /api/config/toml      — read current TOML (or generate from running config)
//	POST /api/config/toml      — validate + backup + save + reload
//	POST /api/config/validate  — validate TOML without saving
//	GET  /api/config/backups   — list backup files
//	POST /api/config/rollback  — restore a specific backup
func tgAdminConfigTOMLHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore)
		if !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}

		// Sub-path routing: /api/config/toml, /api/config/validate, etc.
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/config/toml"):
			if r.Method == http.MethodGet {
				handleConfigTOMLGet(w)
			} else if r.Method == http.MethodPost {
				handleConfigTOMLSave(w, r)
			} else {
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		case strings.HasSuffix(path, "/config/validate"):
			if r.Method == http.MethodPost {
				handleConfigValidate(w, r)
			} else {
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		case strings.HasSuffix(path, "/config/backups"):
			if r.Method == http.MethodGet {
				handleConfigBackups(w)
			} else {
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		case strings.HasSuffix(path, "/config/rollback"):
			if r.Method == http.MethodPost {
				handleConfigRollback(w, r)
			} else {
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func configRepoRoot() string {
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_HOME")); root != "" {
		return root
	}
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_REPO_ROOT")); root != "" {
		return root
	}
	return "."
}

func configBackupDir() string {
	return filepath.Join(configRepoRoot(), "config_backups")
}

// handleConfigTOMLGet reads config.toml if it exists, or generates annotated TOML from the running config.
func handleConfigTOMLGet(w http.ResponseWriter) {
	root := configRepoRoot()
	tomlPath := config.TOMLFilePath(root)

	data, err := os.ReadFile(tomlPath)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"toml":   string(data),
			"path":   tomlPath,
			"source": "file",
		})
		return
	}

	// No TOML file — generate annotated template from running config.
	if !serverReady() {
		writeJSON(w, http.StatusOK, map[string]any{
			"toml":   "",
			"path":   tomlPath,
			"source": "empty",
		})
		return
	}

	cfg := liveConfig(config.Config{})
	generated := generateAnnotatedTOML(cfg)

	writeJSON(w, http.StatusOK, map[string]any{
		"toml":   generated,
		"path":   tomlPath,
		"source": "generated",
	})
}

// generateAnnotatedTOML creates a human-readable TOML with comments from the running config.
func generateAnnotatedTOML(cfg config.Config) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(s); b.WriteByte('\n') }
	wf := func(format string, args ...any) { fmt.Fprintf(&b, format, args...); b.WriteByte('\n') }
	blank := func() { b.WriteByte('\n') }

	str := func(v string) string {
		if strings.ContainsAny(v, "\"\\") {
			return "'" + v + "'"
		}
		return "\"" + v + "\""
	}
	strArr := func(vals []string) string {
		if len(vals) == 0 {
			return "[]"
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = str(v)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	boolStr := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}

	// --- Header ---
	w("# =============================================================================")
	w("# Lampac-Go — конфигурация")
	w("# Приоритет загрузки: env > config.toml > init.conf (JSON) > current.conf")
	w("# Документация: https://wiki.alcopa.cc/")
	w("# =============================================================================")
	blank()

	// --- Server ---
	w("# Сервер")
	w("[server]")
	wf("addr = %s  # HTTP-адрес (\":порт\" или \"хост:порт\")", str(cfg.Server.Addr))
	blank()

	// --- Compat ---
	w("[compat]")
	wf("repo_root = %s", str(cfg.Compat.RepoRoot))
	blank()

	// --- Web plugins ---
	w("# Плагины в веб-интерфейсе Lampa")
	w("[web.plugins]")
	wf("dlna = %s", boolStr(cfg.Web.InitPlugins.DLNA))
	wf("tracks = %s", boolStr(cfg.Web.InitPlugins.Tracks))
	wf("transcoding = %s", boolStr(cfg.Web.InitPlugins.Transcoding))
	wf("tmdb_proxy = %s", boolStr(cfg.Web.InitPlugins.TMDBProxy))
	wf("online = %s", boolStr(cfg.Web.InitPlugins.Online))
	wf("catalog = %s", boolStr(cfg.Web.InitPlugins.Catalog))
	wf("sisi = %s", boolStr(cfg.Web.InitPlugins.SISI))
	wf("torrserver = %s", boolStr(cfg.Web.InitPlugins.TorrServer))
	wf("backup = %s", boolStr(cfg.Web.InitPlugins.Backup))
	wf("sync = %s", boolStr(cfg.Web.InitPlugins.Sync))
	wf("bookmark = %s", boolStr(cfg.Web.InitPlugins.Bookmark))
	wf("timecode = %s", boolStr(cfg.Web.InitPlugins.Timecode))
	wf("ads_free = %s", boolStr(cfg.Web.InitPlugins.AdsFree))
	wf("youtube_feed = %s", boolStr(cfg.Web.InitPlugins.YouTubeFeed))
	blank()

	// --- WebSocket ---
	w("[websocket]")
	wf("type = %s", str(cfg.WebSocket.Type))
	blank()

	// --- SISI ---
	w("# SISI (18+ контент)")
	w("[sisi]")
	wf("spider = %s", boolStr(cfg.Sisi.Spider))
	wf("component = %s", str(cfg.Sisi.Component))
	wf("push_all = %s", boolStr(cfg.Sisi.PushAll))
	wf("history_enable = %s", boolStr(cfg.Sisi.HistoryEnable))
	blank()

	// --- Online ---
	w("# =============================================================================")
	w("# Онлайн-источники (балансеры)")
	w("# =============================================================================")
	blank()
	w("[online]")
	wf("check_online_search = %s", boolStr(cfg.Online.CheckOnlineSearch))
	wf("with_search = %s", strArr(cfg.Online.WithSearch))
	blank()

	// --- Balancers ---
	type balEntry struct {
		name    string
		comment string
		lines   []string
	}
	bals := []balEntry{
		{name: "rezka", comment: "PidoRezka (HDRezka)", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.PidoRezka.Host)),
			fmt.Sprintf("login = %s", str(cfg.Online.PidoRezka.Login)),
			fmt.Sprintf("password = %s", str(cfg.Online.PidoRezka.Password)),
			fmt.Sprintf("premium = %s", boolStr(cfg.Online.PidoRezka.Premium)),
			fmt.Sprintf("hls = %s", boolStr(cfg.Online.PidoRezka.HLS)),
			fmt.Sprintf("cookie = %s", str(cfg.Online.PidoRezka.Cookie)),
		}},
		{name: "anwap", comment: "Anwap (без выбора качества, ~SD)", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.Anwap.Host)),
		}},
		{name: "smotrim", comment: "Смотрим/ВГТРК (открытый архив; подписочное не отдаётся)", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.Smotrim.Host)),
			fmt.Sprintf("player_api = %s", str(cfg.Online.Smotrim.PlayerAPI)),
		}},
		{name: "anidub", comment: "AniDUB Online (аниме, дорамы, азиатское кино)", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.Anidub.Host)),
		}},
		{name: "rudub", comment: "RuDub.TV (только сериалы, озвучка студии)", lines: []string{
			fmt.Sprintf("host = %s  # плеерный сайт, номер в домене растёт", str(cfg.Online.Rudub.Host)),
			fmt.Sprintf("tracker_host = %s  # витрина, у неё спрашивается новый адрес плеера", str(cfg.Online.Rudub.TrackerHost)),
		}},
		{name: "ahuerezka", comment: "AhueRezka (HDRezka через воркер по Kinopoisk ID)", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.AhueRezka.Host)),
			fmt.Sprintf("hosts = %s  # запасные зеркала воркера", strArr(cfg.Online.AhueRezka.Hosts)),
			fmt.Sprintf("kp_host = %s", str(cfg.Online.AhueRezka.KpHost)),
			fmt.Sprintf("kp_hosts = %s", strArr(cfg.Online.AhueRezka.KpHosts)),
			fmt.Sprintf("premium = %s  # true только если воркер проксирует премиум-аккаунт, иначе 2K/4K = заглушка", boolStr(cfg.Online.AhueRezka.Premium)),
			fmt.Sprintf("hls = %s", boolStr(cfg.Online.AhueRezka.HLS)),
			fmt.Sprintf("socks_proxy = %s", str(cfg.Online.AhueRezka.SocksProxy)),
		}},
		{name: "kinopub", comment: "KinoPub", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.KinoPub.Host)),
			fmt.Sprintf("token = %s", str(cfg.Online.KinoPub.Token)),
			fmt.Sprintf("tokens = %s", strArr(cfg.Online.KinoPub.Tokens)),
			fmt.Sprintf("filetype = %s  # hls, hls4, hls2, http", str(cfg.Online.KinoPub.Filetype)),
		}},
		{name: "filmix", comment: "Filmix", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.Filmix.Host)),
			fmt.Sprintf("token = %s", str(cfg.Online.Filmix.Token)),
			fmt.Sprintf("tokens = %s", strArr(cfg.Online.Filmix.Tokens)),
			fmt.Sprintf("reserve = %s", boolStr(cfg.Online.Filmix.Reserve)),
			fmt.Sprintf("pro = %s  # filmixpro", boolStr(cfg.Online.Filmix.Pro)),
			fmt.Sprintf("hls = %s", boolStr(cfg.Online.Filmix.HLS)),
			fmt.Sprintf("fx_mode = %s  # fallback (по умолчанию): legacy первым, api-fx только при отказе; primary: api-fx первым", str(cfg.Online.Filmix.FXMode)),
			fmt.Sprintf("user_apitv = %s  # логин аккаунта, нужен для 4K через api.filmix.tv", str(cfg.Online.Filmix.UserAPITV)),
			fmt.Sprintf("passwd_apitv = %s", str(cfg.Online.Filmix.PasswdAPITV)),
		}},
		{name: "filmix_tv", comment: "Filmix TV", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.FilmixTV.Host)),
		}},
		{name: "filmix_partner", comment: "Filmix Partner API", lines: []string{
			fmt.Sprintf("token = %s", str(cfg.Online.FilmixPartner.Token)),
		}},
		{name: "collaps", comment: "Collaps", lines: []string{
			fmt.Sprintf("api_host = %s", str(cfg.Online.Collaps.APIHost)),
			fmt.Sprintf("list_host = %s", str(cfg.Online.Collaps.ListHost)),
			fmt.Sprintf("token = %s", str(cfg.Online.Collaps.Token)),
		}},
		{name: "lift", comment: "Lift", lines: []string{
			fmt.Sprintf("api_host = %s", str(cfg.Online.Lift.APIHost)),
			fmt.Sprintf("primary_hosts = %s", strArr(cfg.Online.Lift.PrimaryHosts)),
			fmt.Sprintf("embed_host = %s", str(cfg.Online.Lift.EmbedHost)),
			fmt.Sprintf("consumer_host = %s", str(cfg.Online.Lift.ConsumerHost)),
			fmt.Sprintf("embed_referer = %s", str(cfg.Online.Lift.EmbedReferer)),
			fmt.Sprintf("basic_auth_user = %s", str(cfg.Online.Lift.BasicAuthUser)),
			fmt.Sprintf("basic_auth_pass = %s", str(cfg.Online.Lift.BasicAuthPass)),
			fmt.Sprintf("sourcecraft_url = %s", str(cfg.Online.Lift.SourcecraftURL)),
			fmt.Sprintf("gist_id = %s", str(cfg.Online.Lift.GistID)),
		}},
		{name: "mirage", comment: "Mirage (4K)", lines: []string{
			fmt.Sprintf("api_host = %s", str(cfg.Online.Mirage.APIHost)),
			fmt.Sprintf("link_host = %s", str(cfg.Online.Mirage.LinkHost)),
			fmt.Sprintf("token = %s  # обязательный", str(cfg.Online.Mirage.Token)),
		}},
		{name: "aladdin", comment: "Aladdin (4K)", lines: []string{
			fmt.Sprintf("api_host = %s", str(cfg.Online.Aladdin.APIHost)),
			fmt.Sprintf("link_host = %s", str(cfg.Online.Aladdin.LinkHost)),
			fmt.Sprintf("token = %s", str(cfg.Online.Aladdin.Token)),
		}},
		{name: "alloha", comment: "Alloha", lines: []string{
			fmt.Sprintf("api_host = %s", str(cfg.Online.Alloha.APIHost)),
			fmt.Sprintf("token = %s", str(cfg.Online.Alloha.Token)),
		}},
		{name: "kinobase", comment: "Kinobase", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.Kinobase.Host)),
			fmt.Sprintf("playerjs = %s", boolStr(cfg.Online.Kinobase.PlayerJS)),
			fmt.Sprintf("hdr = %s", boolStr(cfg.Online.Kinobase.HDR)),
		}},
		{name: "videoseed", comment: "Videoseed", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.Videoseed.Host)),
			fmt.Sprintf("token = %s  # обязательный (ключ API)", str(cfg.Online.Videoseed.Token)),
			fmt.Sprintf("player_token = %s  # ключ API плеера (для embed; опционален)", str(cfg.Online.Videoseed.PlayerToken)),
		}},
		{name: "zetflix", comment: "Zetflix", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.Zetflix.Host)),
			fmt.Sprintf("hls = %s", boolStr(cfg.Online.Zetflix.HLS)),
			fmt.Sprintf("stream_proxy = %s", boolStr(cfg.Online.Zetflix.StreamProxy)),
			fmt.Sprintf("cdn = %s  # obrut, fotpro, auto", str(cfg.Online.Zetflix.CDN)),
		}},
		{name: "zetflixdb", comment: "ZetflixDB (obrut-каталог AO на движке videodb)", lines: []string{
			fmt.Sprintf("host = %s  # obrut api host", str(cfg.Online.ZetflixDB.Host)),
			fmt.Sprintf("hls = %s", boolStr(cfg.Online.ZetflixDB.HLS)),
		}},
		{name: "kinotochka", comment: "Kinotochka", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.Kinotochka.Host)),
			fmt.Sprintf("cookie = %s", str(cfg.Online.Kinotochka.Cookie)),
		}},
		{name: "redheadsound", comment: "RedHeadSound", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.Redheadsound.Host)),
			fmt.Sprintf("login = %s", str(cfg.Online.Redheadsound.Login)),
			fmt.Sprintf("password = %s", str(cfg.Online.Redheadsound.Password)),
		}},
		{name: "hdvb", comment: "HDVB", lines: []string{
			fmt.Sprintf("api_host = %s", str(cfg.Online.HDVB.APIHost)),
			fmt.Sprintf("token = %s", str(cfg.Online.HDVB.Token)),
		}},
		{name: "kodik", comment: "Kodik", lines: []string{
			fmt.Sprintf("api_host = %s", str(cfg.Online.Kodik.APIHost)),
			fmt.Sprintf("token = %s", str(cfg.Online.Kodik.Token)),
		}},
		{name: "lumex", comment: "Lumex", lines: []string{
			fmt.Sprintf("api_host = %s", str(cfg.Online.Lumex.APIHost)),
			fmt.Sprintf("token = %s", str(cfg.Online.Lumex.Token)),
			fmt.Sprintf("client_id = %s", str(cfg.Online.Lumex.ClientID)),
			fmt.Sprintf("iframe_host = %s", str(cfg.Online.Lumex.IframeHost)),
		}},
		{name: "videocdn", comment: "VideoCDN (Lumex API)", lines: []string{
			fmt.Sprintf("iframe_host = %s", str(cfg.Online.VideoCDN.IframeHost)),
			fmt.Sprintf("token = %s", str(cfg.Online.VideoCDN.Token)),
		}},
		{name: "iframe_video", comment: "IframeVideo", lines: []string{
			fmt.Sprintf("api_host = %s", str(cfg.Online.IframeVideo.APIHost)),
			fmt.Sprintf("cdn_host = %s", str(cfg.Online.IframeVideo.CDNHost)),
			fmt.Sprintf("token = %s", str(cfg.Online.IframeVideo.Token)),
		}},
		{name: "vokino", comment: "VoKino", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.Vokino.Host)),
			fmt.Sprintf("token = %s", str(cfg.Online.Vokino.Token)),
		}},
		{name: "getstv", comment: "GetsTV", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.GetsTV.Host)),
			fmt.Sprintf("token = %s", str(cfg.Online.GetsTV.Token)),
		}},
		{name: "iptv_online", comment: "IPTV Online", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.IptvOnline.Host)),
			fmt.Sprintf("token = %s", str(cfg.Online.IptvOnline.Token)),
		}},
		{name: "vibix", comment: "Vibix", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.Vibix.Host)),
			fmt.Sprintf("token = %s  # обязательный", str(cfg.Online.Vibix.Token)),
			fmt.Sprintf("iframe_mode = %t  # true = плеер vibix встраивается у клиента (доход+статистика); домен должен быть зарегистрирован в кабинете vibix", cfg.Online.Vibix.IframeMode),
		}},
		{name: "moonanime", comment: "MoonAnime", lines: []string{
			fmt.Sprintf("host = %s", str(cfg.Online.MoonAnime.Host)),
			fmt.Sprintf("token = %s", str(cfg.Online.MoonAnime.Token)),
		}},
	}

	// Simple host-only balancers
	simpleHost := []struct {
		name, comment, host string
	}{
		{"anilibria", "Анилибрия", cfg.Online.Anilibria.Host},
		{"aniliberty", "AniLiberty", cfg.Online.AniLiberty.Host},
		{"animebesst", "AnimeBesst", cfg.Online.Animebesst.Host},
		{"animedia", "AniMedia", cfg.Online.Animedia.Host},
		{"animevost", "Animevost", cfg.Online.Animevost.Host},
		{"animego", "AnimeGo", cfg.Online.AnimeGo.Host},
		{"rutube_movie", "Rutube Movie", cfg.Online.RutubeMovie.Host},
		{"plvideo", "Plvideo", cfg.Online.Plvideo.Host},
		{"vdbmovies", "VDBmovies", cfg.Online.VDBmovies.Host},
		{"ashdi", "Ashdi", cfg.Online.Ashdi.Host},
		{"eneyida", "Eneyida", cfg.Online.Eneyida.Host},
		{"kinogo", "Kinogo", cfg.Online.Kinogo.Host},
		{"fancdn", "FanCDN", cfg.Online.FanCDN.Host},
		{"cdnmovies", "CDNmovies", cfg.Online.CDNmovies.Host},
		{"cdnvideohub", "CDNvideohub", cfg.Online.CDNvideohub.Host},
		{"kubikvkube", "KubikVKube", cfg.Online.Kubikvkube.Host},
	}

	for _, e := range bals {
		wf("# %s", e.comment)
		wf("[online.%s]", e.name)
		for _, line := range e.lines {
			w(line)
		}
		blank()
	}

	// Специальные типы
	w("# AnimeLib")
	w("[online.animelib]")
	wf("host = %s", str(cfg.Online.AnimeLib.Host))
	wf("token = %s", str(cfg.Online.AnimeLib.Token))
	blank()

	w("# iRemux (Megaoblako)")
	w("[online.iremux]")
	wf("host = %s", str(cfg.Online.Remux.Host))
	wf("cookie = %s", str(cfg.Online.Remux.Cookie))
	blank()

	w("# Kinoukr")
	w("[online.kinoukr]")
	wf("host = %s", str(cfg.Online.Kinoukr.Host))
	blank()

	w("# VK Movie")
	w("[online.vk_movie]")
	wf("host = %s", str(cfg.Online.VKMovie.Host))
	wf("token_url = %s", str(cfg.Online.VKMovie.TokenURL))
	blank()

	w("# VeoVeo")
	w("[online.veoveo]")
	wf("host = %s", str(cfg.Online.VeoVeo.Host))
	wf("data_path = %s", str(cfg.Online.VeoVeo.DataPath))
	blank()

	w("# VideoDB")
	w("[online.videodb]")
	wf("host = %s", str(cfg.Online.VideoDB.Host))
	wf("api_host = %s", str(cfg.Online.VideoDB.APIHost))
	blank()

	// Simple host-only
	w("# --- Простые балансеры (только host) ---")
	blank()
	for _, e := range simpleHost {
		wf("# %s", e.comment)
		wf("[online.%s]", e.name)
		wf("host = %s", str(e.host))
		blank()
	}

	// --- CUB ---
	w("# =============================================================================")
	w("# CUB")
	w("# =============================================================================")
	blank()
	w("[cub]")
	wf("enable = %s", boolStr(cfg.Cub.Enable))
	wf("scheme = %s", str(cfg.Cub.Scheme))
	wf("domain = %s", str(cfg.Cub.Domain))
	wf("mirror = %s", str(cfg.Cub.Mirror))
	wf("view_ru = %s", boolStr(cfg.Cub.ViewRU))
	blank()

	// --- ProxyLink ---
	w("# Настройки прокси-ссылок (шифрование URL потоков)")
	w("[proxy_link]")
	wf("cache_dir = %s", str(cfg.ProxyLink.CacheDir))
	wf("verify_ip = %s", boolStr(cfg.ProxyLink.VerifyIP))
	wf("encrypt_aes = %s", boolStr(cfg.ProxyLink.EncryptAES))
	blank()

	// --- ServerProxy ---
	w("# Серверный прокси (потоковое видео)")
	w("[server_proxy]")
	wf("response_content_length = %s", boolStr(cfg.ServerProxy.ResponseContentLength))
	wf("max_length_m3u = %d", cfg.ServerProxy.MaxLengthM3U)
	blank()
	w("[server_proxy.image]")
	wf("cache = %s", boolStr(cfg.ServerProxy.Image.Cache))
	wf("cache_rsize = %s", boolStr(cfg.ServerProxy.Image.CacheRSize))
	wf("cache_time = %d  # минуты", cfg.ServerProxy.Image.CacheTime)
	blank()

	// --- Parser (JacRed / Jackett) ---
	w("[parser]")
	wf("jacred_host = %s  # JacRed/Jackett торрент-поиск (внешний; фолбэк при jacred_local)", str(cfg.Parser.JacRedHost))
	wf("jacred_apikey = %s  # API ключ (опционально)", str(cfg.Parser.JacRedKey))
	if cfg.Parser.JacRedHost2 != "" {
		wf("jacred_host2 = %s  # второй парсер: опрашивается ПАРАЛЛЕЛЬНО, выдача объединяется", str(cfg.Parser.JacRedHost2))
		wf("jacred_apikey2 = %s  # API ключ второго парсера (опционально)", str(cfg.Parser.JacRedKey2))
	} else {
		w("# jacred_host2 = \"\"  # второй парсер: опрашивается ПАРАЛЛЕЛЬНО, выдача объединяется")
		w("# jacred_apikey2 = \"\"")
	}
	wf("jacred_local = %s  # свой jacred-fdb: скачивается и запускается сервером", boolStr(cfg.Parser.JacRedLocal))
	wf("jacred_local_port = %d  # порт локального инстанса (только 127.0.0.1)", cfg.Parser.JacRedLocalPort)
	if cfg.Parser.JacRedHome != "" {
		wf("jacred_home = %s", str(cfg.Parser.JacRedHome))
	} else {
		w("# jacred_home = \"\"  # по умолчанию {repo_root}/jacred")
	}
	if cfg.Parser.JacRedSyncAPI != "" {
		wf("jacred_syncapi = %s  # синк базы с upstream вместо своего парсинга", str(cfg.Parser.JacRedSyncAPI))
	} else {
		w("# jacred_syncapi = \"\"  # upstream с opensync=true; пусто = свой парсинг трекеров")
	}
	if cfg.Parser.JacRedBootstrapDB != nil {
		wf("jacred_bootstrap_db = %s  # скачать готовую базу FDB при первом старте", boolStr(*cfg.Parser.JacRedBootstrapDB))
	} else {
		w("# jacred_bootstrap_db = true  # скачать готовую базу FDB при первом старте")
	}
	blank()

	// --- Parser → RuTracker (свой индексер) ---
	// Печатается всегда: редактор сохраняет ровно то, что отрисовано, поэтому
	// пропуск секции стёр бы учётку при первом же сохранении.
	w("# RuTracker: свой индексер, результаты подмешиваются к jacred.")
	w("# jacred его не отдаёт в принципе — трекер требует авторизации, поэтому")
	w("# в публичную open-sync базу не попадает. Нужен свой аккаунт.")
	w("[parser.rutracker]")
	wf("enable = %s", boolStr(cfg.Parser.RuTracker.Enable))
	wf("host = %s  # зеркала: rutracker.net, rutracker.nl", str(cfg.Parser.RuTracker.Host))
	wf("login = %s", str(cfg.Parser.RuTracker.Login))
	wf("password = %s", str(cfg.Parser.RuTracker.Password))
	wf("cookie = %s  # готовый bb_session; приоритетнее логина (обход капчи/CF)", str(cfg.Parser.RuTracker.Cookie))
	if cfg.Parser.RuTracker.UseFlareSolverr != nil {
		wf("use_flaresolverr = %s  # обход Cloudflare-челленджа", boolStr(*cfg.Parser.RuTracker.UseFlareSolverr))
	} else {
		w("# use_flaresolverr = true  # обход Cloudflare-челленджа через [online] flaresolverr")
	}
	wf("only_authorized = %s  # не подмешивать rutracker в анонимные ответы", boolStr(cfg.Parser.RuTracker.OnlyAuthorized))
	wf("resolve_top = %d  # сколько верхних раздач получают настоящий magnet сразу", cfg.Parser.RuTracker.ResolveTop)
	wf("search_ttl_min = %d  # кэш выдачи поиска", cfg.Parser.RuTracker.SearchTTLMin)
	wf("min_interval_ms = %d  # минимальный интервал между запросами к трекеру", cfg.Parser.RuTracker.MinIntervalMs)
	blank()

	// --- TorrServer ---
	w("# TorrServer")
	w("[torrserver]")
	wf("port = %d", cfg.TorrServer.Port)
	if cfg.TorrServer.URL != "" {
		wf("url = %s", str(cfg.TorrServer.URL))
	}
	if cfg.TorrServer.Login != "" {
		wf("login = %s  # по умолчанию: ts", str(cfg.TorrServer.Login))
	} else {
		w("# login = \"\"  # по умолчанию: ts")
	}
	w("# password = \"\"  # автоопределение из accs.db")
	w("# home_dir = \"\"  # по умолчанию: {repo_root}/torrserver/")
	blank()

	// --- Telegram ---
	w("# =============================================================================")
	w("# Telegram авторизация")
	w("# =============================================================================")
	blank()
	w("[telegram]")
	wf("enable = %s", boolStr(cfg.TelegramAuth.Enable))
	wf("bot_token = %s", str(cfg.TelegramAuth.BotToken))
	wf("admin_id = %d", cfg.TelegramAuth.AdminID)
	if cfg.TelegramAuth.BotName != "" {
		wf("bot_name = %s", str(cfg.TelegramAuth.BotName))
	}
	wf("max_devices_per_user = %d", cfg.TelegramAuth.MaxDevicesPerUser)
	wf("auto_approve = %s", boolStr(cfg.TelegramAuth.AutoApprove))
	wf("auto_approve_days = %d", cfg.TelegramAuth.AutoApproveDays)
	blank()

	// --- Admin ---
	w("# Парольная авторизация (когда TG выключен)")
	w("[admin]")
	wf("password = %s", str(cfg.AdminAuth.Password))
	blank()

	// --- Proxy VLESS ---
	w("# =============================================================================")
	w("# VLESS прокси (обход блокировок)")
	w("# =============================================================================")
	blank()
	if len(cfg.Proxy.Vless.Entries) > 0 {
		for _, e := range cfg.Proxy.Vless.Entries {
			w("[[proxy.vless.entries]]")
			wf("uri = %s", str(e.URI))
			wf("balancers = %s", strArr(e.Balancers))
			if e.Label != "" {
				wf("label = %s", str(e.Label))
			}
			if e.Engine != "" {
				wf("engine = %s  # xray, mihomo", str(e.Engine))
			}
			blank()
		}
	} else {
		w("# [[proxy.vless.entries]]")
		w("# uri = \"vless://...\"")
		w("# balancers = [\"Zetflix\", \"Kinobase\"]")
		w("# label = \"RU\"")
		blank()
	}

	// --- Sync ---
	w("# Синхронизация конфигов")
	w("[sync]")
	wf("enable = %s", boolStr(cfg.Sync.Enable))
	if cfg.Sync.Type != "" {
		wf("type = %s", str(cfg.Sync.Type))
	}
	if cfg.Sync.APIHost != "" {
		wf("api_host = %s", str(cfg.Sync.APIHost))
	}
	if cfg.Sync.APIPasswd != "" {
		wf("api_passwd = %s", str(cfg.Sync.APIPasswd))
	}
	blank()

	// --- Transcoding ---
	w("# =============================================================================")
	w("# Транскодирование")
	w("# =============================================================================")
	blank()
	w("[transcoding]")
	wf("enable = %s", boolStr(cfg.Transcoding.Enable))
	wf("ffmpeg = %s", str(cfg.Transcoding.FFmpeg))
	wf("max_concurrent_jobs = %d", cfg.Transcoding.MaxConcurrent)
	wf("temp_root = %s", str(cfg.Transcoding.TempRoot))
	if cfg.Transcoding.DiskBudgetMB > 0 {
		wf("disk_budget_mb = %d", cfg.Transcoding.DiskBudgetMB)
	}
	// P3.O policy knobs — only emit lines for non-default values so the
	// generated TOML stays readable for the common case.
	if cfg.Transcoding.DisableABRLadder {
		wf("disable_abr_ladder = %s", boolStr(true))
	}
	if cfg.Transcoding.DisableSubtitleBurnIn {
		wf("disable_subtitle_burn_in = %s", boolStr(true))
	}
	if cfg.Transcoding.DisableFastStart {
		wf("disable_fast_start = %s", boolStr(true))
	}
	if cfg.Transcoding.DisablePrewarmProbe {
		wf("disable_prewarm_probe = %s", boolStr(true))
	}
	if cfg.Transcoding.MaxLadderRungs > 0 {
		wf("max_ladder_rungs = %d", cfg.Transcoding.MaxLadderRungs)
	}
	blank()

	// --- Observability ---
	w("# Мониторинг")
	w("[observability]")
	wf("access_log = %s", boolStr(cfg.Observability.AccessLog))
	wf("metrics_path = %s", str(cfg.Observability.MetricsPath))
	wf("health_path = %s", str(cfg.Observability.HealthPath))
	blank()

	// --- LLM ---
	w("# LLM (авто-портирование балансеров)")
	w("[llm]")
	wf("endpoint = %s", str(cfg.LLM.Endpoint))
	wf("api_key = %s", str(cfg.LLM.ApiKey))
	wf("model = %s", str(cfg.LLM.Model))
	blank()

	// --- Kit ---
	w("# Kit")
	w("[kit]")
	wf("enable = %s", boolStr(cfg.Kit.Enable))
	wf("server_host = %s", str(cfg.Kit.ServerHost))
	wf("cache_to_seconds = %d", cfg.Kit.CacheToSeconds)
	wf("encrypt = %s", boolStr(cfg.Kit.Encrypt))
	wf("binds_disabled = %s", boolStr(cfg.Kit.BindsDisabled))
	blank()

	// --- Browser Pool ---
	w("# Browser Pool (headless Chrome)")
	w("[browser_pool]")
	wf("max_concurrent = %d", cfg.BrowserPool.MaxConcurrent)
	wf("stream_cache_max = %d", cfg.BrowserPool.StreamCacheMax)
	wf("stream_cache_ttl_h = %d", cfg.BrowserPool.StreamCacheTTLH)
	engineName := cfg.BrowserPool.Engine
	if engineName == "" {
		engineName = "chromedp"
	}
	wf("engine = %s  # chromedp | rod | playwright (только если вкомпилировано)", str(engineName))
	if len(cfg.BrowserPool.BalancerEngines) > 0 {
		blank()
		w("[browser_pool.balancer_engines]")
		keys := make([]string, 0, len(cfg.BrowserPool.BalancerEngines))
		for k := range cfg.BrowserPool.BalancerEngines {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			wf("%s = %s", k, str(cfg.BrowserPool.BalancerEngines[k]))
		}
	}
	blank()

	return b.String()
}

// handleConfigTOMLSave validates TOML, creates a backup, writes config.toml, and hot-reloads.
func handleConfigTOMLSave(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20)) // 2MB limit
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
		return
	}

	var req struct {
		TOML string `json:"toml"`
	}
	if err := stdjson.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json: " + err.Error()})
		return
	}

	tomlData := strings.TrimSpace(req.TOML)
	if tomlData == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "toml is empty"})
		return
	}

	// Sanitize whole-number floats (e.g., "9080.0" → "9080") before validation
	// so the TOML parser doesn't reject float→int assignments.
	tomlData = string(config.SanitizeTOMLInts([]byte(tomlData)))

	// Step 1: Validate
	if _, err := config.ValidateTOML([]byte(tomlData)); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"error":   "validation",
			"message": err.Error(),
		})
		return
	}

	root := configRepoRoot()
	tomlPath := config.TOMLFilePath(root)

	// Step 2: Backup existing file
	backupName := ""
	if existingData, err := os.ReadFile(tomlPath); err == nil {
		backupName, err = createBackup(existingData)
		if err != nil {
			log.Warn().Err(err).Msg("admin: failed to create config backup")
		}
	}

	// Step 3: Write new config.toml (atomic via temp file)
	dir := filepath.Dir(tomlPath)
	_ = os.MkdirAll(dir, 0o755)

	tmpFile, err := os.CreateTemp(dir, "config-*.toml.tmp")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "create temp: " + err.Error()})
		return
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write([]byte(tomlData)); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "write temp: " + err.Error()})
		return
	}
	tmpFile.Close()

	if err := os.Rename(tmpPath, tomlPath); err != nil {
		os.Remove(tmpPath)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "rename: " + err.Error()})
		return
	}

	log.Info().Str("path", tomlPath).Str("backup", backupName).Msg("admin: config.toml saved")

	// Step 4: Hot-reload
	reloadOK := true
	reloadError := ""
	needRestart := false
	restartReason := ""

	if serverReady() {
		oldAddr := liveConfig(config.Config{}).Server.Addr
		if err := reloadServer(); err != nil {
			reloadOK = false
			reloadError = err.Error()
			log.Error().Err(err).Msg("admin: reload after config save failed")
		}
		newAddr := liveConfig(config.Config{}).Server.Addr
		if oldAddr != newAddr {
			needRestart = true
			restartReason = fmt.Sprintf("Порт изменён: %s → %s. Для применения требуется перезапуск сервера.", oldAddr, newAddr)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"backup":         backupName,
		"reload_ok":      reloadOK,
		"reload_error":   reloadError,
		"need_restart":   needRestart,
		"restart_reason": restartReason,
	})
}

// handleConfigValidate parses TOML without saving.
func handleConfigValidate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
		return
	}

	var req struct {
		TOML string `json:"toml"`
	}
	if err := stdjson.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json: " + err.Error()})
		return
	}

	if _, err := config.ValidateTOML([]byte(req.TOML)); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"valid":   false,
			"message": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"valid":   true,
		"message": "OK",
	})
}

// handleConfigBackups lists available backup files.
func handleConfigBackups(w http.ResponseWriter) {
	dir := configBackupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"backups": []any{}})
		return
	}

	type backupInfo struct {
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		Created string `json:"created"`
	}

	var backups []backupInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, backupInfo{
			Name:    e.Name(),
			Size:    info.Size(),
			Created: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	// Sort newest first.
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Name > backups[j].Name
	})

	writeJSON(w, http.StatusOK, map[string]any{"backups": backups})
}

// handleConfigRollback restores a backup file as config.toml and reloads.
func handleConfigRollback(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := stdjson.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json: " + err.Error()})
		return
	}

	// Security: prevent path traversal.
	name := filepath.Base(req.Name)
	if name == "" || name == "." || name == ".." || !strings.HasSuffix(name, ".toml") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid backup name"})
		return
	}

	backupPath := filepath.Join(configBackupDir(), name)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "backup not found: " + name})
		return
	}

	// Validate the backup before restoring.
	if _, err := config.ValidateTOML(data); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"error":   "backup is invalid TOML: " + err.Error(),
		})
		return
	}

	root := configRepoRoot()
	tomlPath := config.TOMLFilePath(root)

	// Backup current before rollback.
	if currentData, err := os.ReadFile(tomlPath); err == nil {
		if _, err := createBackup(currentData); err != nil {
			log.Warn().Err(err).Msg("admin: failed to backup current before rollback")
		}
	}

	// Write the rollback.
	if err := os.WriteFile(tomlPath, data, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "write: " + err.Error()})
		return
	}

	log.Info().Str("backup", name).Msg("admin: config rolled back")

	// Hot-reload.
	reloadOK := true
	reloadError := ""
	if err := reloadServer(); err != nil {
		reloadOK = false
		reloadError = err.Error()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"restored":     name,
		"reload_ok":    reloadOK,
		"reload_error": reloadError,
	})
}

// createBackup saves data to config_backups/ with a timestamped name.
// Returns the backup filename. Also prunes old backups beyond maxBackups.
func createBackup(data []byte) (string, error) {
	dir := configBackupDir()
	_ = os.MkdirAll(dir, 0o755)

	name := fmt.Sprintf("config_%s.toml", time.Now().Format("2006-01-02_15-04-05"))
	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}

	// Prune old backups.
	entries, err := os.ReadDir(dir)
	if err == nil {
		var tomlFiles []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
				tomlFiles = append(tomlFiles, e.Name())
			}
		}
		sort.Strings(tomlFiles)
		for len(tomlFiles) > maxBackups {
			oldest := tomlFiles[0]
			os.Remove(filepath.Join(dir, oldest))
			tomlFiles = tomlFiles[1:]
		}
	}

	return name, nil
}
