package httpapi

import (
	"lampac-go/internal/cluster"
	"lampac-go/internal/proxyapi"
	"net/http"
	"strings"

	"lampac-go/internal/config"
	"lampac-go/internal/litesrc"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

func liteSourceHandler(cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry) http.HandlerFunc {
	litesrc.SetDeps(litesrc.Deps{
		StreamProxyDirectURL: streamProxyDirectURL,
		RchGate:              rchGate,
		NewRchClient:         func(r *http.Request) litesrc.RchFetcher { return newRchClient(r) },
		LiveConfig:           liveConfig,
		ServerReady:          serverReady,
		CapiResolveRequest:   capiResolveRequest,
		ExternalKPForIMDB: func(cfgRoot, imdbID string) string {
			loadExternalIDs(cfgRoot)
			externalIDsCache.mu.RLock()
			kpStr := externalIDsCache.items[imdbID]
			externalIDsCache.mu.RUnlock()
			return kpStr
		},
		PluginQualityBadgeGet:     pluginQualityBadgeGet,
		ClientIP:                  clientIP,
		IsStreamProxyDisabled:     isStreamProxyDisabled,
		StreamHostFromRequest:     streamHostFromRequest,
		StreamProxyURL:            streamProxyURL,
		StreamProxyURLWithHeaders: streamProxyURLWithHeaders,
		PurgeStreamCache:          capiPurgeStreamsCache,
	})
	kinobase := litesrc.NewKinobaseChecker(cfg).Handle(cfg, proxyLinks)
	rezka := litesrc.NewPidoRezkaChecker(cfg).Handle(cfg, "rezka", proxyLinks)
	rhsprem := litesrc.NewRhspremChecker(cfg).Handle(cfg, "rhsprem", proxyLinks)
	ahuerezka := litesrc.NewAhueRezkaChecker(cfg).Handle(cfg, "ahuerezka", proxyLinks)
	kinopub := litesrc.NewKinoPubChecker(cfg).Handle(cfg, proxyLinks)
	redheadsound := litesrc.NewRedheadsoundChecker(cfg).Handle(cfg, proxyLinks)
	anilibria := litesrc.NewAnilibriaChecker(cfg).Handle(cfg, proxyLinks)
	aniliberty := litesrc.NewAnilibertyChecker(cfg).Handle(cfg, proxyLinks)
	animebesstC := litesrc.NewAnimebesstChecker(cfg)
	animebesst := animebesstC.Handle(cfg, proxyLinks)
	animebesstVideo := litesrc.AnimebesstVideoHandler(animebesstC, proxyLinks)
	animediaC := litesrc.NewAnimediaChecker(cfg)
	animedia := animediaC.Handle(cfg, proxyLinks)
	animediaVideo := litesrc.AnimediaVideoHandler(animediaC, proxyLinks)
	animevostC := litesrc.NewAnimevostChecker(cfg)
	animevost := animevostC.Handle(cfg, proxyLinks)
	animevostVideo := litesrc.AnimevostVideoHandler(animevostC, proxyLinks)
	animego := litesrc.NewAnimegoChecker(cfg).Handle(cfg)
	remux := litesrc.NewRemuxChecker(cfg).Handle(cfg, proxyLinks)
	mirkino := litesrc.NewMirkinoChecker(cfg).Handle(cfg, proxyLinks)
	animelibC := litesrc.NewAnimelibChecker(cfg)
	animelib := animelibC.Handle(cfg, proxyLinks)
	animelibVideo := litesrc.AnimelibVideoHandler(animelibC, proxyLinks)
	kinoukr := litesrc.NewKinoukrChecker(cfg).Handle(cfg, proxyLinks)
	filmix := litesrc.NewFilmixChecker(cfg).Handle(cfg, proxyLinks)
	filmixtv := litesrc.NewFilmixTVChecker(cfg).Handle(cfg)
	fxapi := litesrc.NewFXAPIChecker(cfg).Handle(cfg)
	kinotochka := litesrc.NewKinotochkaChecker(cfg).Handle(cfg, proxyLinks)
	collaps := litesrc.NewCollapsChecker(cfg).Handle(cfg, "collaps", proxyLinks)
	collapsDash := litesrc.NewCollapsChecker(cfg).Handle(cfg, "collaps-dash", proxyLinks)
	liftC := litesrc.NewLiftChecker(cfg)
	lift := liftC.Handle(cfg, "lift", proxyLinks)
	rutubemovie := litesrc.NewRutubeMovieChecker(cfg).Handle(cfg, proxyLinks)
	anwap := litesrc.NewAnwapChecker(cfg).Handle(cfg, proxyLinks)
	rudub := litesrc.NewRudubChecker(cfg).Handle(cfg, proxyLinks)
	anidub := litesrc.NewAnidubChecker(cfg).Handle(cfg, proxyLinks)
	smotrim := litesrc.NewSmotrimChecker(cfg).Handle(cfg, proxyLinks)
	vkmovie := litesrc.NewVKMovieChecker(cfg).Handle(cfg, proxyLinks)
	hdvb := litesrc.NewHDVBChecker(cfg).Handle(cfg, proxyLinks)
	plvideo := litesrc.NewPlvideoChecker(cfg).Handle(cfg)
	vdbmovies := litesrc.NewVDBmoviesChecker(cfg).Handle(cfg, proxyLinks)
	kodikC := litesrc.NewKodikChecker(cfg)
	kodik := kodikC.Handle(cfg, proxyLinks)
	kodikVideo := litesrc.KodikVideoHandler(kodikC, proxyLinks)
	lumex := litesrc.NewLumexChecker(cfg).Handle(cfg, proxyLinks)
	videoCDN := litesrc.NewVideoCDNChecker(cfg).Handle(cfg, "videocdn")
	vcdn := litesrc.NewVideoCDNChecker(cfg).Handle(cfg, "vcdn")
	alloha := litesrc.NewAllohaChecker(cfg).Handle(cfg, proxyLinks)
	veoveo := litesrc.NewVeoVeoChecker(cfg).Handle(cfg, proxyLinks)
	liteJS := liteJSHandler(cfg)
	ashdi := litesrc.NewAshdiChecker(cfg).Handle(cfg, proxyLinks)
	eneyida := litesrc.NewEneyidaChecker(cfg).HandleWithLinks(cfg, proxyLinks)
	kinogo := litesrc.NewKinogoChecker(cfg).Handle(cfg, proxyLinks)
	kinovod := litesrc.NewKinovodChecker(cfg).Handle(cfg, proxyLinks)
	fancdnC := litesrc.NewFancdnChecker(cfg)
	fancdn := fancdnC.Handle(cfg, proxyLinks)
	fancdnVideo := litesrc.FancdnVideoHandler(fancdnC, proxyLinks)
	videoseed := litesrc.NewVideoseedChecker(cfg).Handle(cfg, proxyLinks)
	zetflix := litesrc.NewZetflixChecker(cfg).Handle(cfg, proxyLinks)
	videodb := litesrc.NewVideodbChecker(cfg).Handle(cfg, proxyLinks)
	zetflixdb := litesrc.NewZetflixDBChecker(cfg).Handle(cfg, proxyLinks)
	uakino := litesrc.NewUaKinoChecker(cfg).Handle(cfg, proxyLinks)
	cdnmovies := litesrc.NewCDNmoviesChecker(cfg).Handle(cfg)
	cdnvideohub := litesrc.NewCDNvideohubChecker(cfg).Handle(cfg, proxyLinks)
	kubikvkube := litesrc.NewKubikvkubeChecker(cfg).Handle(cfg, proxyLinks)
	vibix := litesrc.NewVibixChecker(cfg).Handle(cfg, proxyLinks)
	turbo := litesrc.NewTurboChecker(cfg).Handle(cfg, proxyLinks)
	zonaC := litesrc.NewZonaChecker(cfg)
	zona := zonaC.Handle(cfg, proxyLinks)
	iframevideo := litesrc.NewIframevideoChecker(cfg).Handle(cfg)
	getstv := litesrc.NewGetsTVChecker(cfg).Handle(cfg)
	mirage := litesrc.NewMirageChecker(cfg).Handle(cfg, proxyLinks)
	aladdin := litesrc.NewAladdinChecker(cfg).HandleWithPrefix("aladdin", cfg, proxyLinks)
	pidtorHandler := newPidTorHandler(cfg, proxyLinks)
	moonanime := litesrc.NewMoonAnimeChecker(cfg).Handle(cfg, proxyLinks)
	iptvonline := litesrc.NewIptvOnlineChecker(cfg).Handle(cfg)
	vokino := litesrc.NewVokinoChecker(cfg).Handle(cfg, proxyLinks)
	gencit := litesrc.NewGencitChecker(cfg).Handle(cfg, proxyLinks)
	femd := litesrc.NewFemdChecker(cfg).Handle(cfg, proxyLinks)
	kinobadi := litesrc.NewKinobadiChecker(cfg).Handle(cfg, proxyLinks)
	zagonka := litesrc.NewZagonkaChecker(cfg).Handle(cfg, proxyLinks)
	vidsrc := litesrc.NewEngBaseChecker("vidsrc").Handle(cfg)
	playembed := litesrc.NewEngBaseChecker("playembed").Handle(cfg)
	autoembed := litesrc.NewEngBaseChecker("autoembed").Handle(cfg)
	movpi := litesrc.NewEngBaseChecker("movpi").Handle(cfg)
	smashystream := litesrc.NewEngBaseChecker("smashystream").Handle(cfg)
	rgshows := litesrc.NewEngBaseChecker("rgshows").Handle(cfg)
	// Reachable ENG sources with a real headless-browser m3u8 sniffer.
	vidlink := litesrc.NewEngSourceChecker("vidlink").Handle(cfg, proxyLinks)
	videasy := litesrc.NewEngSourceChecker("videasy").Handle(cfg, proxyLinks)
	hydraflix := litesrc.NewEngSourceChecker("hydraflix").Handle(cfg, proxyLinks)
	twoembed := litesrc.NewEngSourceChecker("twoembed").Handle(cfg, proxyLinks)
	bamboo := litesrc.NewBambooChecker(cfg).Handle(cfg, proxyLinks)
	uafilm := litesrc.NewUAFilmChecker(cfg).Handle(cfg, proxyLinks)
	unimay := litesrc.NewUnimayChecker(cfg).Handle(cfg, proxyLinks)
	starlight := litesrc.NewStarlightChecker(cfg).Handle(cfg, proxyLinks)
	klonfun := litesrc.NewKlonFUNChecker(cfg).Handle(cfg, proxyLinks)
	uaflix := litesrc.NewUaflixChecker(cfg).Handle(cfg, proxyLinks)
	animeon := litesrc.NewAnimeonChecker(cfg).Handle(cfg, proxyLinks)
	mikai := litesrc.NewMikaiChecker(cfg).Handle(cfg, proxyLinks)
	leproduction := litesrc.NewLeproductionChecker(cfg).Handle(cfg, proxyLinks)
	flixcdn := litesrc.NewFlixcdnChecker(cfg).Handle(cfg, proxyLinks)
	// Process singleton — SakhTV's account is single-session upstream, so a
	// per-rebuild checker would log in again and evict whoever is watching.
	sakhtv := litesrc.SharedSakhTVChecker(cfg).Handle(cfg, proxyLinks)
	scts := litesrc.SharedSCTSChecker(cfg).Handle(cfg, proxyLinks)
	kbteam := litesrc.NewKBTeamChecker(cfg).Handle(cfg, proxyLinks)
	krasview := litesrc.NewKrasviewChecker(cfg).Handle(cfg, proxyLinks)
	ytChecker := litesrc.SharedYoutubeChecker(cfg) // process singleton — survives capiLiteSource rebuilds (keeps live mux state)
	litesrc.SetGlobalYTChecker(ytChecker)          // expose for admin stats
	youtube := youtubeWithNodeFallback(ytChecker.Handle(cfg, proxyLinks), cfg.YouTube.NodeFallback)
	getsTVBind := litesrc.GetsTVBindHandler(cfg)

	// Exact-match route table: O(1) lookup instead of 74 sequential if-statements.
	routes := map[string]http.Handler{
		// Balancers
		"kinobase":              kinobase,
		"rezka":                 rezka,
		"rezka/movie":           rezka,
		"rezka/movie.m3u8":      rezka,
		"rezka/serial":          rezka,
		"rhsprem":               rhsprem,
		"rhsprem/movie":         rhsprem,
		"rhsprem/movie.m3u8":    rhsprem,
		"rhsprem/serial":        rhsprem,
		"rhs":                   rhsprem,
		"rhs/movie":             rhsprem,
		"rhs/movie.m3u8":        rhsprem,
		"rhs/serial":            rhsprem,
		"rc/rhs":                rhsprem,
		"rc/rhs/movie":          rhsprem,
		"rc/rhs/serial":         rhsprem,
		"ahuerezka":             ahuerezka,
		"ahuerezka/movie":       ahuerezka,
		"ahuerezka/movie.m3u8":  ahuerezka,
		"ahuerezka/serial":      ahuerezka,
		"kinopub":               kinopub,
		"kinopubpro":            kinopub,
		"redheadsound":          redheadsound,
		"anilibria":             anilibria,
		"aniliberty":            aniliberty,
		"animebesst":            animebesst,
		"animebesst/video.m3u8": animebesstVideo,
		"animedia":              animedia,
		"animedia/video.m3u8":   animediaVideo,
		"animevost":             animevost,
		"animevost/video":       animevostVideo,
		"animego":               animego,
		"animego/video":         animego,
		"animego/video.m3u8":    animego,
		"remux":                 remux,
		"remux/movie":           remux,
		"iremux":                remux, // config/admin/with_search spelling → same handler
		"iremux/movie":          remux,
		"mirkino":               mirkino,
		"animelib":              animelib,
		"animelib/video":        animelibVideo,
		"kinoukr":               kinoukr,
		"filmix":                filmix,
		"filmixpro":             filmix,
		"rc/filmix":             filmix,
		"filmixtv":              filmixtv,
		"fxapi":                 fxapi,
		"rc/fxapi":              fxapi,
		"kinotochka":            kinotochka,
		"collaps":               collaps,
		"collaps-dash":          collapsDash,
		"collaps-search":        collaps,
		"lift":                  lift,
		"lift/embed":            lift,
		"lift-search":           lift,
		"rutubemovie":           rutubemovie,
		"rutubemovie/play":      rutubemovie,
		"anwap":                 anwap,
		"anwap/play":            anwap,
		"anwap/serial":          anwap,
		"rudub":                 rudub,
		"rudub/play":            rudub,
		"rudub/serial":          rudub,
		"anidub":                anidub,
		"anidub/play":           anidub,
		"anidub/serial":         anidub,
		"smotrim":               smotrim,
		"smotrim/play":          smotrim,
		"smotrim/serial":        smotrim,
		"vkmovie":               vkmovie,
		"hdvb":                  hdvb,
		"hdvb-search":           hdvb,
		"hdvb/serial":           hdvb,
		"plvideo":               plvideo,
		"vdbmovies":             vdbmovies,
		"kodik":                 kodik,
		"kodik/video":           kodikVideo,
		"kodik/video.m3u8":      kodikVideo,
		"lumex":                 lumex,
		"videocdn":              videoCDN,
		"vcdn":                  vcdn,
		"alloha":                alloha,
		"alloha/video":          alloha,
		"alloha/video.m3u8":     alloha,
		"alloha/player":         alloha,
		"alloha/resolved":       alloha,
		"alloha/stream":         alloha,
		"alloha/stream.m3u8":    alloha,
		"alloha/auth":           alloha,
		"alloha-search":         alloha,
		"veoveo":                veoveo,
		"veoveo-spider":         veoveo,
		"ashdi":                 ashdi,
		"eneyida":               eneyida,
		"kinogo":                kinogo,
		"kinovod":               kinovod,
		"fancdn":                fancdn,
		"fancdn/video":          fancdnVideo,
		"fancdn/video.m3u8":     fancdnVideo,
		// xvideocdn = legacy plugin name from the closed r.xsmart.tv plugin
		// era; same backend (lomont.site) under the same handler.
		"xvideocdn":               fancdn,
		"xvideocdn/video":         fancdnVideo,
		"xvideocdn/video.m3u8":    fancdnVideo,
		"videoseed":               videoseed,
		"zetflix":                 zetflix,
		"zetflix/manifest":        zetflix,
		"zetflix/manifest.m3u8":   zetflix,
		"zetflix/manifest.mp4":    zetflix,
		"videodb":                 videodb,
		"videodb/manifest":        videodb,
		"videodb/manifest.m3u8":   videodb,
		"videodb/manifest.mp4":    videodb,
		"zetflixdb":               zetflixdb,
		"zetflixdb/manifest":      zetflixdb,
		"zetflixdb/manifest.m3u8": zetflixdb,
		"zetflixdb/manifest.mp4":  zetflixdb,
		"uakino":                  uakino,
		// Internal embed resolvers (upstream Tortuga/HdvbUA): reachable by URL,
		// but deliberately NOT in localCorePlugins — they are not sources.
		"tortuga":                uakino,
		"hdvbua":                 uakino,
		"cdnmovies":              cdnmovies,
		"cdnvideohub":            cdnvideohub,
		"cdnvideohub/video":      cdnvideohub,
		"cdnvideohub/video.m3u8": cdnvideohub,
		"kubikvkube":             kubikvkube,
		"kubikvkube/video":       kubikvkube,
		"kubikvkube/video.m3u8":  kubikvkube,
		"vibix":                  vibix,
		"vibix/stream":           vibix,
		"vibix/stream.m3u8":      vibix,
		"turbo":                  turbo,
		"zona":                   zona,
		"zona-mobilink":          zona,
		"zona-hdvb":              zona,
		"zona-filmix":            zona,
		"zona-takedwn":           zona,
		"iframevideo":            iframevideo,
		"iframevideo/video":      iframevideo,
		"iframevideo/video.m3u8": iframevideo,
		"getstv":                 getstv,
		"getstv/video.m3u8":      getstv,
		"getstv-search":          getstv,
		"mirage":                 mirage,
		"mirage/video":           mirage,
		"mirage/video.m3u8":      mirage,
		"mirage-search":          mirage,
		"mirage/stream":          mirage,
		"mirage/stream.m3u8":     mirage,
		"mirage/edge_hash":       mirage,
		"aladdin":                aladdin,
		"aladdin/video":          aladdin,
		"aladdin/video.m3u8":     aladdin,
		"aladdin-search":         aladdin,
		"aladdin/stream":         aladdin,
		"aladdin/stream.m3u8":    aladdin,
		"bamboo":                 bamboo,
		"uafilm":                 uafilm,
		"uafilm/video":           uafilm,
		"uafilm/video.m3u8":      uafilm,
		"unimay":                 unimay,
		"starlight":              starlight,
		"starlight/play":         starlight,
		"klonfun":                klonfun,
		"uaflix":                 uaflix,
		"animeon":                animeon,
		"animeon/play":           animeon,
		"mikai":                  mikai,
		"mikai/play":             mikai,
		"leproduction":           leproduction,
		"moonanime":              moonanime,
		"moonanime/video":        moonanime,
		"moonanime/video.m3u8":   moonanime,
		"iptvonline":             iptvonline,
		"vokino":                 vokino,
		"vokinotk":               vokino,
		"gencit":                 gencit,
		"gencit/video":           gencit,
		"femd":                   femd,
		"kinobadi":               kinobadi,
		"zagonka":                zagonka,
		"zagonka/serial":         zagonka,
		"zagonka/play":           zagonka,
		"vidsrc":                 vidsrc,
		"playembed":              playembed,
		"videasy":                videasy,
		"autoembed":              autoembed,
		"movpi":                  movpi,
		"vidlink":                vidlink,
		"vidlink/video":          vidlink,
		"vidlink/video.m3u8":     vidlink,
		"videasy/video":          videasy,
		"videasy/video.m3u8":     videasy,
		"hydraflix":              hydraflix,
		"hydraflix/video":        hydraflix,
		"hydraflix/video.m3u8":   hydraflix,
		"smashystream":           smashystream,
		"rgshows":                rgshows,
		"twoembed":               twoembed,
		"twoembed/video":         twoembed,
		"twoembed/video.m3u8":    twoembed,
		"sakhtv":                 sakhtv,
		"scts":                   scts,
		"kbteam":                 kbteam,
		"krasview":               krasview,
		"youtube":                youtube,
		// Special routes
		"getstv/bind":     getsTVBind,
		"iptvonline/bind": iptvonline,
		"js":              liteJS,
		// Synthetic balancer emitted by /lite/events when the request is
		// unauthenticated. Lampa's source-picker probes this URL like any
		// other balancer; the handler always returns an accsdb JSON which
		// Lampa renders as a blue "auth required" banner. See lite_auth_gate.go.
		"_auth_required": http.HandlerFunc(authRequiredLiteHandler),
		"flixcdn":        flixcdn,
		"flixcdn/video":  flixcdn,
	}

	// YouTube sub-handlers use typed functions, wrap them.
	ytFeedH := http.HandlerFunc(ytChecker.HandleFeed)
	ytDashH := http.HandlerFunc(ytChecker.HandleDashMPD)
	ytImgH := http.HandlerFunc(litesrc.YtImgProxyHandler)
	ytMuxH := http.HandlerFunc(ytChecker.HandleMux)
	// С менеджером ссылок: он нужен, чтобы пересобрать мастер после перезапуска.
	ytMuxMasterH := ytChecker.MuxMasterHandler(proxyLinks)
	trailerH := litesrc.TrailerHandler(ytChecker, proxyLinks)
	routes["trailer"] = trailerH
	// Поиск трейлера для тайтлов без видео в TMDB — для веба, который ходит в
	// TMDB мимо нашего прокси (Android/Lampa получают то же через сам прокси).
	routes["trailer/find"] = litesrc.TrailerFindHandler(litesrc.SharedTrailerFinder(cfg))
	routes["youtube/vot"] = litesrc.YtVotHandler(cfg, proxyLinks)
	routes["youtube/feed"] = ytFeedH
	routes["youtube/dash.mpd"] = ytDashH
	routes["youtube/img"] = ytImgH
	routes["youtube/mux"] = ytMuxH
	routes["youtube/mux/index.m3u8"] = ytMuxH
	routes["youtube/mux/seg"] = ytMuxH
	routes["youtube/mux/init.mp4"] = ytMuxH
	routes["youtube/mux/master.m3u8"] = ytMuxMasterH

	// YouTube OAuth feed routes (personalized).
	routes["youtube/feed/subscriptions"] = http.HandlerFunc(ytChecker.HandleFeedSubscriptions)
	routes["youtube/feed/playlists"] = http.HandlerFunc(ytChecker.HandleFeedPlaylists)
	routes["youtube/feed/playlist"] = http.HandlerFunc(ytChecker.HandleFeedPlaylist)
	routes["youtube/channels"] = http.HandlerFunc(ytChecker.HandleChannels)   // subscribed-channels list (sidebar)
	routes["youtube/channel"] = http.HandlerFunc(ytChecker.HandleChannel)     // a channel's videos (grid)
	routes["youtube/recommend"] = http.HandlerFunc(ytChecker.HandleRecommend) // trending = «Главная» recommendations

	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")
		if raw == "" || raw == "events" {
			http.NotFound(w, r)
			return
		}
		if raw == "withsearch" {
			writeJSON(w, http.StatusOK, cfg.Online.WithSearch)
			return
		}

		// Source-discovery gate was rolled back 2026-05-28 — see the
		// matching comment in liteEventsHandler. lite_auth_gate.go's
		// helpers (isLiteStreamPath, hasStreamExt) stay because future
		// per-balancer signed-URL work will reuse them.
		isCS := parseBoolParam(r.URL.Query().Get("checksearch"))

		// Torrent/spider routes.
		if raw == "jac" || raw == "spider" || raw == "spider/anime" {
			if raw == "spider" || raw == "spider/anime" {
				title := strings.TrimSpace(r.URL.Query().Get("title"))
				out := buildLocalSpiderMap(cfg, hostFromRequest(r), title, raw == "spider/anime")
				writeJSON(w, http.StatusOK, out)
				return
			}
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"error":    "lite source is not implemented in local mode",
				"balanser": raw,
			})
			return
		}

		// PidTor (torrent search via RedAPI + TorrServer).
		if raw == "pidtor" || strings.HasPrefix(raw, "pidtor/") {
			pidtorHandler.ServeHTTP(w, r)
			return
		}

		// Admin-disable / healthcheck auto-disable enforcement. Without this,
		// a user could bypass admin-disabled balancers by hitting /lite/<bal>
		// directly (e.g. via kit _balancerVisibility=true on a disabled source).
		// Canonical key matches the group-check normalization so aliases like
		// "rhs", "filmixpro", "rc/<bal>" and sub-paths can't slip through.
		// isCS is declared in the auth-gate block above; reuse it here.
		if isBalancerDisabled(tgauth.CanonicalPluginKey(raw)) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if isCS {
				_, _ = w.Write([]byte(`{"rch":false}`))
			} else {
				_, _ = w.Write([]byte(`{}`))
			}
			return
		}

		// Отключённые зрителем ноды — в запрос до развилки: ниже он может уехать
		// на ноду, и та соберёт ссылку уже сама, своего хранилища настроек не имея.
		stampEdgeSkip(r)
		// Закреплённая нода — туда же и по той же причине. Порядок важен: запрет
		// проставляется первым, и если человек закрепил ноду, которую же и отключил,
		// хранилище такого противоречия просто не хранит (см. edgeprefs).
		stampEdgePin(r)

		// Cluster forwarding: for non-checksearch requests, primary may forward
		// to a backend node with fewer active connections.
		//
		// liteMainNodeOnly routes (YouTube, trailers) are excluded: they depend on
		// yt-dlp + deno + per-user OAuth and on outbound googlevideo/ggpht access
		// that the thin mirror nodes don't have. Forwarding one to a mirror yields
		// a 4xx (missing sidecar / stale build / blocked egress) the forwarder
		// won't retry, so the client gets a hard error from a node that can never
		// serve YouTube. Keep them on the node that received the request.
		cp, cf := liveClusterPool(), liveClusterFwd()
		if !isCS && !liteMainNodeOnly(raw) && cp != nil && cf != nil {
			// PickFor honours per-balancer routing rules first, then strategy.
			// Запрет зрителя учитывается и здесь: у источников с правилом «edge» ссылку
			// отдаёт тот, кто добыл, поэтому выбор добытчика — это и выбор отдающего.
			node, localForced := cp.PickForSkipKey(raw, proxyapi.EdgeHintFrom(r), cluster.StickyKey(r.URL.Query()), proxyapi.EdgeSkipSet(r))
			if node != nil {
				// Источник с добором: пустой ответ выбранной ноды — не окончательный.
				if h, ok := routes[raw]; ok && liteFailoverBalancer(raw) {
					serveLiteWithFailover(w, r, raw, h, cp, cf, node)
					return
				}
				if cf.Forward(w, r, node) {
					return
				}
				// Forward failed — fall through to local handling.
				_ = localForced // if local was forbidden by rule but forward failed, we still handle locally as safety
			} else {
				// nil = handle locally (rule said so, or primary won the race).
				cp.IncrLocal()
				defer cp.DecrLocal()
				// Record the local /lite/<balancer> request for stats + feed.
				clientAddr := clientIP(r)
				cp.RecordLocalRequest(raw, clientAddr, 0, false)
				cp.RecordLocalEvent(raw, clientAddr, 0)
			}
		} else if cp != nil {
			// Cluster active but no nodes / forwarder — still track local stats.
			clientAddr := clientIP(r)
			cp.RecordLocalRequest(raw, clientAddr, 0, false)
			cp.RecordLocalEvent(raw, clientAddr, 0)
		}

		// Group-based balancer access control: block direct requests to denied plugins.
		// CanonicalPluginKey normalizes route aliases ("rhs" → "rhsprem",
		// "filmixpro" → "filmix", "zona-mobilink" → "zona", ...) and strips
		// sub-paths and the "rc/" premium prefix, so the check can't be
		// bypassed by hitting an alternate URL for the same handler.
		//
		// Skipped for /capi aggregation drills: there the admin's resolve_sources is
		// the authority (same rule as eventPluginVisible's kit/group skip). The drill
		// carries no user token, so it resolves to the DEFAULT group — which would
		// silently `{}` any source not in that group's whitelist (e.g. a freshly-added
		// alloha), even though the admin listed it in resolve_sources.
		//
		// Skipped for stream-DELIVERY paths (video/stream.m3u8/segments): external
		// players (Vimu/ExoPlayer on Android TV) fetch these WITHOUT the cookie/token,
		// so they resolve to the DEFAULT group and a denied source would return `{}`
		// instead of the playlist → "Input does not start with #EXTM3U". The auth gate
		// already exempts these paths (isLiteStreamPath); access is enforced on the
		// SOURCE-LISTING path (/lite/<bal>), which the authenticated client hits first.
		// Skipped for our own checksearch probes: they carry no user token, so
		// they would resolve to the DEFAULT group and `{}` any source that group
		// denies — greying it out for every user whose group DOES allow it. The
		// plugin list was already filtered per-caller before the probe ran.
		if !capiResolveRequest(r) && !isChecksearchProbe(r) && !isLiteStreamPath(raw) && groupStoreRef != nil && tgTokenStoreRef != nil {
			if group := resolveUserGroup(r); group != nil {
				pluginKey := tgauth.CanonicalPluginKey(raw)
				if !group.BalancerAllowed(pluginKey) {
					log.Debug().Str("raw", raw).Str("plugin", pluginKey).Str("group", group.ID).
						Msg("lite: balancer denied by group")
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{}`))
					return
				}
			}
		}

		// Dynamic routes (JS modules, custom balancer subprocesses) win over
		// built-ins so an enabled JS module can override a Go balancer with
		// the same name. Disable or remove the module to revert to the Go
		// implementation.
		if dynRoutes != nil {
			// Strip sub-path so e.g. `/lite/starlight/play` still hits the
			// starlight JS module, which dispatches internally.
			routeKey := raw
			if i := strings.Index(routeKey, "/"); i > 0 {
				routeKey = routeKey[:i]
			}
			if h, ok := dynRoutes.Lookup(routeKey); ok {
				h.ServeHTTP(w, r)
				return
			}
		}

		// O(1) exact-match lookup for built-in Go balancers.
		if h, ok := routes[raw]; ok {
			if liteFailoverBalancer(raw) {
				// checksearch у AhueRezka смотрит на каталог, а не на здоровье
				// потоков main: поток при нажатии доберёт нода.
				if strings.HasPrefix(raw, "ahuerezka") {
					litesrc.SetAhueRezkaFailover(cp != nil && cf != nil)
				}
				if !isCS && cp != nil && cf != nil {
					serveLiteWithFailover(w, r, raw, h, cp, cf, nil)
					return
				}
			}
			h.ServeHTTP(w, r)
			return
		}

		// Prefix-match routes (videoseed sub-paths, etc.).
		if strings.HasPrefix(raw, "alloha/player") {
			alloha.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(raw, "videoseed/") {
			videoseed.ServeHTTP(w, r)
			return
		}

		if parseBoolParam(r.URL.Query().Get("checksearch")) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "lite source is not implemented in local mode",
			"balanser": raw,
		})
	}
}

// liteMainNodeOnly reports whether a /lite/<raw> request must be served by the
// node that received it rather than load-balanced to a cluster mirror. YouTube
// (and trailers, which reuse the YouTube extractor) need yt-dlp, deno, the
// per-user OAuth tokens and direct googlevideo/ggpht egress that only the main
// node has — mirror nodes are thin and reply with a non-retriable 4xx, which is
// exactly how the channel-avatar proxy (/lite/youtube/img?url=) surfaced as a
// 400 from a RU mirror. raw is the path after "/lite/" so we key on its first
// segment ("youtube/img", "youtube/dash.mpd", "trailer/…" all collapse here).
func liteMainNodeOnly(raw string) bool {
	seg := raw
	if i := strings.IndexAny(seg, "/?#"); i >= 0 {
		seg = seg[:i]
	}
	switch seg {
	case "youtube", "trailer":
		return true
	// Phantom-CDN browser-resolve balancers: each needs headless Chrome, the
	// affiliate token and a live CDN/WS session that only the main node has.
	// A thin mirror returns empty ({}) — which is exactly how Alloha surfaced as
	// "0 voices, ms:0" in /capi (the in-process drill was forwarded to a mirror,
	// so the local handler never ran). Keep them on the receiving node.
	case "alloha", "mirage", "aladdin":
		return true
	}
	return false
}

// liteSkipNodeFanout — не спрашивать ноды «есть ли у тебя этот источник» при
// проверке карточки. Бессмысленно для всего, что нода обслуживать не будет:
// закреплённого за main и источников с явным правилом «local»/«nodes». Для
// AhueRezka опрос бил вдвойне — каждый показ карточки на main спрашивал воркер
// ещё с девяти адресов, и воркер за объём банил их (прод 21.09.2026).
func liteSkipNodeFanout(balancer string) bool {
	if liteMainNodeOnly(balancer) {
		return true
	}
	if cp := liveClusterPool(); cp != nil {
		switch cp.RuleTarget(balancer) {
		case "local", "nodes":
			return true
		}
	}
	return false
}
