package sisihttp

import (
	"lampac-go/internal/config"
	"lampac-go/internal/jsmodules"
	"lampac-go/internal/kit"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// registerSISIRoutes wires the 18+ adult-content "SISI" source routes plus
// the sisi index, bookmarks, history, and parental-PIN endpoints. Kept in
// one block because all sources are constructed up front and share the same
// registration shape (list + view endpoint per source).
// RegisterRoutes wires the /sisi/* API. hostDeps injects everything this package
// needs from the composition root (see Deps); the trailing params are the
// long-lived stores/managers the handlers close over.
func RegisterRoutes(router chi.Router, cfg config.Config, hostDeps Deps, kitStore *kit.Store, tgBot *tgauth.Bot, sisiSources *jsmodules.Manager, proxyLinks *proxylink.Manager) {
	setDeps(hostDeps)
	sisiViewStub := sisiViewStubHandler(cfg)
	sisiXvideos := newSisiXvideosSource(cfg)
	sisiPornHub := newSisiPornHubSource(cfg)
	sisiXnxx := newSisiXnxxSource(cfg)
	sisiXhamster := newSisiXhamsterSource(cfg)
	sisiSpankbang := newSisiSpankbangSource(cfg)
	sisiRunetki := newSisiRunetkiSource(cfg)
	sisiEbalovo := newSisiEbalovoSource(cfg)
	sisiEporner := newSisiEpornerSource(cfg)
	sisiHQporner := newSisiHQpornerSource(cfg)
	sisiBonga := newSisiBongaSource(cfg)
	sisiChaturbate := newSisiChaturbateSource(cfg)
	sisiTizam := newSisiTizamSource(cfg)
	sisiPornLab := newSisiPornLabSource(cfg)
	sisiSexStudentki := newSisiSexStudentkiSource(cfg)
	nextHub := newNextHubSource(cfg)

	router.Get("/bgs", sisiBonga.listHandler())
	router.Get("/bgs/potok.m3u8", sisiViewStub)
	router.Get("/chu", sisiChaturbate.listHandler())
	router.Get("/chu/potok", sisiChaturbate.viewHandler())
	router.Get("/elo", sisiEbalovo.listHandler())
	router.Get("/elo/vidosik", sisiEbalovo.viewHandler())
	router.Get("/epr", sisiEporner.listHandler())
	router.Get("/epr/vidosik", sisiEporner.viewHandler())
	router.Get("/hqr", sisiHQporner.listHandler())
	router.Get("/hqr/vidosik", sisiHQporner.viewHandler())
	router.Get("/nexthub", nextHub.listHandler())
	router.Get("/nexthub/vidosik", nextHub.viewHandler())
	router.Get("/phub", sisiPornHub.listHandler())
	router.Get("/phub/vidosik", sisiPornHub.viewHandler())

	router.Get("/ptx", sisiListStubHandler(cfg))
	router.Get("/ptx/strem", sisiViewStub)
	router.Get("/ptx/vidosik", sisiViewStub)
	router.Get("/runetki", sisiRunetki.listHandler())
	router.Get("/sbg", sisiSpankbang.listHandler())
	router.Get("/sbg/vidosik", sisiSpankbang.viewHandler())
	router.Get("/sisi", sisiIndexHandler(cfg))
	router.Post("/sisi/bookmark/add", sisiBookmarkAddHandler(cfg))
	router.Post("/sisi/bookmark/remove", sisiBookmarkRemoveHandler(cfg))
	router.Get("/sisi/bookmarks", sisiBookmarksHandler(cfg))
	router.Get("/sisi/categories", sisiCategoriesHandler())

	// Parental control PIN for sisi section.
	if kitStore != nil {
		router.Get("/sisi/pin/status", sisiPinStatusHandler(kitStore, cfg))
		router.Post("/sisi/pin/set", sisiPinSetHandler(kitStore, cfg))
		router.Post("/sisi/pin/check", sisiPinCheckHandler(kitStore, cfg))
		router.Post("/sisi/pin/remove", sisiPinRemoveHandler(kitStore, cfg))
		router.Post("/sisi/pin/reset", sisiPinResetHandler(kitStore, cfg, tgBot))
		router.Post("/sisi/pin/tg-confirm", sisiPinTGConfirmHandler(kitStore, cfg, tgBot))
		router.Get("/sisi/pin/tg-confirm", sisiPinTGConfirmHandler(kitStore, cfg, tgBot))
		if tgBot != nil {
			tgBot.SetSisiConfirmCallback(SisiConfirmCallback)
		}
	}
	router.Post("/sisi/history/add", sisiHistoryAddHandler(cfg))
	router.Get("/sisi/history/remove", sisiHistoryRemoveHandler(cfg))
	router.Get("/sisi/historys", sisiHistoryListHandler(cfg))
	router.Get("/sisi/plugins/modification.js", sisiModificationHandler(cfg))
	router.Get("/tizam", sisiTizam.listHandler())
	router.Get("/tizam/vidosik", sisiTizam.viewHandler())
	router.Get("/plab", sisiPornLab.listHandler())
	router.Get("/plab/vidosik", sisiPornLab.viewHandler())
	router.Get("/plab/torrent", sisiPornLab.torrentProxyHandler())
	router.Get("/sxst", sisiSexStudentki.listHandler())
	router.Get("/sxst/vidosik", sisiSexStudentki.viewHandler())
	router.Get("/xds", sisiXvideos.listHandler())
	router.Get("/xds/stars", sisiXvideos.starsHandler())
	router.Get("/xds/vidosik", sisiXvideos.viewHandler())

	router.Get("/xmr", sisiXhamster.listHandler())
	router.Get("/xmr/vidosik", sisiXhamster.viewHandler())

	router.Get("/xnx", sisiXnxx.listHandler())
	router.Get("/xnx/vidosik", sisiXnxx.viewHandler())

	// Custom adult sources: JS-based (goja), living under the
	// /sisi/cust/{name} namespace.
	custProxy := sisiCustHandler(sisiSources, proxyLinks)
	router.Get("/sisi/cust/{name}", custProxy)
	router.Get("/sisi/cust/{name}/*", custProxy)
}
