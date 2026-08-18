package httpapi

import (
	"lampac-go/internal/antidpi"
	"lampac-go/internal/balancerstats"
	"lampac-go/internal/cluster"
	"lampac-go/internal/config"
	"lampac-go/internal/custbal"
	"lampac-go/internal/jacred"
	"lampac-go/internal/jsmodules"
	"lampac-go/internal/proxycore"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/rutracker"
	"lampac-go/internal/sidecar"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/torrbalancer"
	"lampac-go/internal/transcodesvc"
	"lampac-go/internal/updater"
)

// deps.go — the seam that dismantles the `serverRef` package global.
//
// `serverRef *Server` is the monolith's #1 coupling: ~300 non-test reads across
// ~57 files, most of them inside handler closures reaching back into the server
// singleton. Two-thirds of those reads are the SAME thing — "give me the live
// (hot-reloaded) config, tolerating a nil serverRef" — so that is the first
// dependency to extract behind an injectable seam. Subsystem handles
// (transSvc, clusterPool, jacredMgr, …) follow as further members of the bundle.
//
// Migration recipe (strangler, a few files per PR, build-green each time):
//
//	// before — direct global read + hand-rolled nil guard:
//	cfg := capturedCfg
//	if serverRef != nil {
//	    cfg = serverRef.Cfg()
//	}
//	// after:
//	cfg := liveConfig(capturedCfg)
//
// The captured `cfg` (the closure's startup snapshot) becomes the fallback, so
// behaviour is preserved exactly: production (server wired) gets live config;
// an unwired server (tests, pre-wiring) gets the snapshot instead of panicking —
// strictly safer than a bare field read.
//
// STATUS: migration complete. No handler references `serverRef` directly anymore —
// every access goes through the accessors below. `deps.go` now OWNS the singleton
// (declared here, published once via bindServer from NewServer), so the "global"
// is a private implementation detail of this seam, not free-floating package
// state. Swapping the source for a threaded/injected value later touches only this
// file, with every call site already stable.

// serverRef is the running Server, published once by bindServer after NewServer
// wires everything. It is the private backing store for the accessors in this
// file; nothing outside deps.go (and the bindServer call in server.go) touches it.
var serverRef *Server

// bindServer publishes the constructed Server to the accessors. Called exactly
// once, from NewServer, after the Server is fully assembled.
func bindServer(s *Server) { serverRef = s }

// liveConfig returns the hot-reloaded configuration when the server is wired
// (admin config edits apply without a restart), else the supplied fallback —
// normally the config snapshot the caller captured at registration time.
//
// This is the single chokepoint for live-config access. Handlers must NOT read
// serverRef.Cfg() directly; route through here so the serverRef global has one
// well-known reader to eventually inject.
func liveConfig(fallback config.Config) config.Config {
	if serverRef != nil {
		return serverRef.Cfg()
	}
	return fallback
}

// liveConfigGen returns the current live-config POINTER (identity), used as a
// cheap generation key to detect hot-reloads — callers compare pointer identity,
// not config values. May be nil even when the server is wired (no reload has
// swapped the pointer yet — Cfg() then falls back to the startup snapshot).
func liveConfigGen() *config.Config {
	if serverRef != nil {
		return serverRef.cfgPtr.Load()
	}
	return nil
}

// storeLiveConfig swaps the live (hot-reloadable) config pointer to cfg WITHOUT a
// full Reload — for light in-memory settings tweaks that must not restart proxy
// pools etc. No-op when the server isn't wired.
func storeLiveConfig(cfg config.Config) {
	if serverRef != nil {
		serverRef.cfgPtr.Store(&cfg)
	}
}

// liveTransSvc returns the transcoding service when the server is wired, else
// nil (transcoding disabled, or serverRef not set — e.g. in tests). This is the
// first SUBSYSTEM member of the bundle: it centralises the serverRef.transSvc
// read so handlers stop reaching through the global. Callers keep their own
// nil-guard, exactly replacing the hand-rolled
// `serverRef != nil && serverRef.transSvc != nil` dance:
//
//	if ts := liveTransSvc(); ts != nil { ts.StatsSnapshot() }   // was: if serverRef != nil && serverRef.transSvc != nil { serverRef.transSvc.StatsSnapshot() }
//	ts := liveTransSvc(); if ts == nil { return }               // was: if serverRef == nil || serverRef.transSvc == nil { return }
func liveTransSvc() *transcodesvc.TranscodingService {
	if serverRef != nil {
		return serverRef.transSvc
	}
	return nil
}

// liveClusterPool / liveClusterFwd / liveClusterStore return the cluster
// subsystem handles when the server is wired, else nil (cluster disabled, or no
// serverRef in tests). Callers keep their nil-guard; a paired site captures both
// once — `cp, cf := liveClusterPool(), liveClusterFwd()` — then guards on the
// locals, matching the old `serverRef != nil && serverRef.clusterPool != nil &&
// serverRef.clusterFwd != nil` chain exactly.
func liveClusterPool() *cluster.Pool {
	if serverRef != nil {
		return serverRef.clusterPool
	}
	return nil
}

func liveClusterFwd() *cluster.Forwarder {
	if serverRef != nil {
		return serverRef.clusterFwd
	}
	return nil
}

func liveClusterStore() *cluster.Store {
	if serverRef != nil {
		return serverRef.clusterStore
	}
	return nil
}

// liveJacredMgr returns the local jacred manager when the server is wired and
// the local parser was constructed, else nil (local parser off, or no serverRef
// in tests). Centralises the serverRef.jacredMgr read; callers keep their
// nil-guard exactly as before.
func liveJacredMgr() *jacred.Manager {
	if serverRef != nil {
		return serverRef.jacredMgr
	}
	return nil
}

// liveRutracker returns the native rutracker indexer. It is always
// constructed, so a nil here only means "no serverRef" (tests, pre-wiring);
// whether the source actually runs is decided by Client.Enabled().
func liveRutracker() *rutracker.Client {
	if serverRef != nil {
		return serverRef.rutracker
	}
	return nil
}

// liveTGBot returns the Telegram bot when the server is wired and TG auth is
// enabled, else nil. Centralises the serverRef.tgBot read; callers keep their
// nil-guard (a nil bot means admin notifications are simply skipped).
func liveTGBot() *tgauth.Bot {
	if serverRef != nil {
		return serverRef.tgBot
	}
	return nil
}

// liveProxyPool returns the proxy sidecar pool when the server is wired, else
// nil (no proxy sidecars configured, or no serverRef in tests). Centralises the
// serverRef.proxyPool read.
func liveProxyPool() *sidecar.Pool {
	if serverRef != nil {
		return serverRef.proxyPool
	}
	return nil
}

// liveProxyCorePool returns the built-in proxy-engine pool when the server is
// wired, else nil (no serverRef, or the engine isn't running).
func liveProxyCorePool() *proxycore.Pool {
	if serverRef != nil {
		return serverRef.proxyCorePool
	}
	return nil
}

// liveCustBalPool returns the custom-balancer subprocess pool when the server is
// wired, else nil (no custom balancers configured, or no serverRef).
func liveCustBalPool() *custbal.Pool {
	if serverRef != nil {
		return serverRef.custBalPool
	}
	return nil
}

// The remaining single-consumer subsystem accessors. Same nil-tolerant contract:
// return the handle when the server is wired, else nil.

func liveAntiDPI() *antidpi.Server {
	if serverRef != nil {
		return serverRef.antiDPI
	}
	return nil
}

func liveAlertEngine() *balancerstats.AlertEngine {
	if serverRef != nil {
		return serverRef.alertEngine
	}
	return nil
}

func liveTSBalancerStore() *torrbalancer.Store {
	if serverRef != nil {
		return serverRef.tsBalancerStore
	}
	return nil
}

func liveTSBalancerPool() *torrbalancer.Pool {
	if serverRef != nil {
		return serverRef.tsBalancerPool
	}
	return nil
}

func liveNwsHub() *nwsHub {
	if serverRef != nil {
		return serverRef.nwsHub
	}
	return nil
}

func liveSisiSources() *jsmodules.Manager {
	if serverRef != nil {
		return serverRef.sisiSources
	}
	return nil
}

func liveProxyLinks() *proxylink.Manager {
	if serverRef != nil {
		return serverRef.proxyLinks
	}
	return nil
}

// liveUpdater returns the self-update service when the server is wired, else nil.
// Centralises the serverRef.updater read; admin handlers keep their own
// `svc == nil` guard ("updater not initialized").
func liveUpdater() *updater.Service {
	if serverRef != nil {
		return serverRef.updater
	}
	return nil
}

// liveVersion / liveCommit / liveBuildDate return the immutable build-info
// strings the server was constructed with, or "" when it isn't wired (tests).
// These never change after construction; the accessors just remove the direct
// serverRef read from handlers.
func liveVersion() string {
	if serverRef != nil {
		return serverRef.version
	}
	return ""
}

func liveCommit() string {
	if serverRef != nil {
		return serverRef.commit
	}
	return ""
}

func liveBuildDate() string {
	if serverRef != nil {
		return serverRef.buildDate
	}
	return ""
}

// serverReady reports whether the server singleton is wired. Handlers use it in
// place of a direct `serverRef == nil` readiness guard so they hold no reference
// to the global — which is what blocks moving a handler cluster to its own package.
func serverReady() bool { return serverRef != nil }

// reloadServer / reloadProxies / reloadProxyCore invoke the matching Server
// lifecycle reload, or no-op with a nil error when the server isn't wired.
func reloadServer() error {
	if serverRef != nil {
		return serverRef.Reload()
	}
	return nil
}

func reloadProxies() error {
	if serverRef != nil {
		return serverRef.ReloadProxies()
	}
	return nil
}

func reloadProxyCore() error {
	if serverRef != nil {
		return serverRef.ReloadProxyCore()
	}
	return nil
}
