package dlnahttp

import (
	"path/filepath"

	"lampac-go/internal/config"
	"lampac-go/internal/dlna"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// registerDLNARoutes wires /dlna.js plus (when cfg.DLNA.Enable is true) the
// full /dlna/* stack: file browser, stream, torrent manager endpoints, cover
// generator, and UPnP SSDP discovery. Returns the torrent manager, cover
// generator, and UPnP server so NewServer can hook them into Server for
// lifecycle (Start/Stop) coordination.
// RegisterRoutes wires /dlna.js and (when enabled) the DLNA/UPnP API, returning
// the long-lived managers the composition root starts/stops. deps injects the
// host's plugin-JS renderer.
func RegisterRoutes(router chi.Router, cfg config.Config, deps Deps) (tm *dlna.TorrentManager, cg *DLNACoverGenerator, upnp *DLNAUPnPServer) {
	pluginJSProvider = deps.PluginJS
	router.Get("/dlna.js", genericPluginJSHandler("dlna.js", "", cfg))
	router.Get("/dlna/js/{token}", genericPluginJSHandler("dlna.js", "", cfg))

	if !cfg.DLNA.Enable {
		return nil, nil, nil
	}

	router.Get("/dlna", dlnaIndexHandler(cfg))
	router.Get("/dlna/stream", dlnaStreamHandler(cfg))
	router.Get("/dlna/delete", dlnaDeleteHandler(cfg))

	// Torrent manager.
	dlnaRoot := resolveDLNARoot(cfg)
	torrentDir := filepath.Join(dlnaRoot, "torrents")
	trackerPath := filepath.Join(cfg.Compat.RepoRoot, "cache", "trackers.txt")
	trackers := dlna.LoadTrackers(trackerPath)
	if len(trackers) == 0 {
		trackers = dlna.DefaultTrackers
	}
	tmInst, err := dlna.NewTorrentManager(dlna.TorrentConfig{
		DownloadDir:   torrentDir,
		DownloadSpeed: cfg.DLNA.DownloadSpeed,
		UploadSpeed:   cfg.DLNA.UploadSpeed,
		Trackers:      trackers,
	})
	if err != nil {
		log.Warn().Err(err).Msg("dlna: torrent manager init failed")
	} else {
		tm = tmInst
		log.Info().Str("dir", torrentDir).Msg("dlna: torrent manager ready")
	}

	// Torrent API routes.
	router.Get("/dlna/tracker/managers", dlnaTorrentManagersHandler(cfg, tm))
	router.Get("/dlna/tracker/show", dlnaTorrentShowHandler(cfg, tm))
	router.Get("/dlna/tracker/download", dlnaTorrentDownloadHandler(cfg, tm))
	router.Get("/dlna/tracker/stream", dlnaTorrentStreamHandler(cfg, tm))
	router.Get("/dlna/tracker/stop", dlnaTorrentDeleteHandler(cfg, tm))
	router.Get("/dlna/tracker/delete", dlnaTorrentDeleteHandler(cfg, tm))

	// Cover generator.
	cg = NewDLNACoverGenerator(cfg)

	// UPnP DLNA resource streaming routes (served from main HTTP server).
	router.Get("/dlna/upnp/res", dlnaUPnPResHandler(cfg))
	router.Get("/dlna/upnp/torrent-stream", dlnaUPnPTorrentStreamHandler(tm))

	// UPnP/DLNA server (SSDP discovery + ContentDirectory).
	if cfg.DLNA.UPnP {
		upnpSrv, upnpErr := NewDLNAUPnPServer(cfg, tm)
		if upnpErr != nil {
			log.Warn().Err(upnpErr).Msg("dlna-upnp: init failed")
		} else {
			upnp = upnpSrv
		}
	}
	return tm, cg, upnp
}
