# internal/httpapi — architecture & strangler plan

`internal/httpapi` is the monolith: **555 `.go` files, ~210k LOC, one `package
httpapi`**, with **~233 package-level `var` globals** (15 of them the shared
`*Ref` singletons: `serverRef`, `syncBridgeRef`, `capiAccountStore`,
`traktStore`, `ccState`, …). Every file can touch every other file's
unexported symbols and the globals, so nothing is independently testable or
reusable. This is the #1 architectural debt.

**Do not big-bang this.** It is a multi-quarter effort with zero user-facing
value, on the core request path. Split it incrementally, each step verified by
`go build ./... && go test ./...` **and a real app run** — not blind.

## Dependency direction (the rule)

Extract **pure logic downward** into cohesive sub-packages that `httpapi`
imports; never make a sub-package import `httpapi` back. Handlers stay in
`httpapi` until the shared base + globals are untangled (they all use
`writeJSON`, the `capi*Gate`s, config helpers, and the globals).

## Sequenced plan

### Step 1 — `internal/transcode` (pure decision/planner logic) — DONE
Lowest-risk first extraction. `internal/transcode` now owns the pure
decision/planner logic: `SelectMode`, `ClientCaps`, `ModeDecision`,
`TranscodingMode` + consts, `PlanABRLadder`/`ABRRung`, the UA→caps DB
(`EnrichCapsFromUA`, `DeviceProfile`), plus the carved-out pure helpers
`PickVideoBitrate` (from `transcoding_hwaccel.go`) and `DoviFromProbe` (from
`transcoding_hls_codec.go`). The `*_test.go` for mode/abr/caps moved with the
code.

**Dedup follow-up — DONE.** The extraction initially left the old
`pickVideoBitrate` / `doviFromProbe` copies behind in `httpapi` (transitional).
Those are now removed: all call sites use `transcode.PickVideoBitrate` /
`transcode.DoviFromProbe`, and the `TestPickVideoBitrate_Tiers` tier-test was
relocated to `internal/transcode/bitrate_test.go` (net-new coverage there —
the package had none). `probeIntField` intentionally stays in `httpapi`: it's
still used by `transcoding_master.go` (level parsing) and its own test, so it's
a genuine shared helper, not a stale copy — `transcode` keeps its own unexported
copy to stay self-contained.

### Step 2 — `internal/httpx` (shared HTTP base) — DONE (base + forwarders)
`internal/httpx` now holds the canonical leaf response helpers `WriteJSON` /
`WriteHTML`. It uses the SAME encoder as the monolith
(`jsoniter.ConfigCompatibleWithStandardLibrary`, mirrored from server.go) so
output is byte-identical. `httpapi.writeJSON` / `writeHTML` are now thin
forwarders to it — so the **~1800 `writeJSON` + ~240 `writeHTML` call sites were
NOT touched** (a big-bang blind rewrite of 2000 sites is not worth the risk).

Remaining (incremental, safe): migrate call sites to `httpx.WriteJSON` directly,
**per handler cluster as it is extracted in Step 4** — not as one mega-diff.
New/extracted code should call `httpx` directly today.

### Step 3 — globals → DI (the real blocker) — ✅ DONE
Convert the `*Ref` singletons + hot globals into fields on the `Server`
struct (or a `Deps` bundle) passed to handler constructors. Do a few per PR,
build-green each time. **Prerequisite for extracting any handler cluster.**

**Outcome.** `serverRef` (the #1 coupling — ~340 reads / ~58 files) is fully
encapsulated: in production code it now appears in **exactly one file, `deps.go`**,
which owns the singleton (`var serverRef *Server`), publishes it once via
`bindServer(s)` from `NewServer`, and exposes it only through ~27 nil-tolerant
accessors (`liveConfig`, `serverReady`, `reloadServer`, `live<Subsystem>()`,
`liveConfigGen`/`storeLiveConfig`). No handler — and no longer even `server.go` —
references the global directly. The other `*Ref` singletons converted earlier in
this step (transcodeSvcRef, kitStoreRef, pendingAdminIDStore→`s.adminIDStore`,
audiobotRef deleted as dead, and the cluster/jacred/tgBot/proxyPool/updater/… set)
are likewise behind accessors or Server fields. Tests still set `serverRef` directly
(in-package white-box isolation) — acceptable and unchanged.

The global was intentionally kept (a bound singleton behind accessors is proper Go
DI) rather than threaded through 300+ call sites. Swapping its source for a
fully-injected value is now a self-contained change touching only `deps.go`.
Effort: L (multi-PR).

**Method:** rank every service-pointer singleton by `refs / files / writes`
(one-liner in `make monolith-stats` territory), then take the most-bounded,
set-before-read ones first. Never big-bang a global read across many files — each
is init-order sensitive. Concurrency primitives (`sync.Mutex/Map/Once`,
`atomic.Pointer`, `singleflight.Group`) are idiomatic package state and are NOT
DI candidates — leave them.

**Done so far** (service-pointer singletons: ~21 → 17):
1. `transcodeSvcRef` → threaded `transSvc *TranscodingService`. Set in
   `registerTranscodingRoutes` (server.go) BEFORE its only readers wire in
   `registerIPTVRoutes`, so a parameter preserves init-order + nil-when-disabled
   exactly. Path: `registerTranscodingRoutes` returns svc → `registerIPTVRoutes`
   → `iptvPlayHandler` → `iptvEconomPlaylist` / `econEnabled`.
2. `kitStoreRef` → threaded `kitStore *kit.Store` into
   `mirrorOriginValidateHandler` (its one reader). `kitStore` is already a
   function-scope local in server.go set before the `/api/auth/validate`
   registration, so the thread is a single call-site change; nil-when-kit-disabled
   preserved.
3. `audiobotRef` → **deleted** (dead): written in `initAudiobot` but never read
   in production (the `/audiobot/*` handlers capture the returned `*audiobotState`
   directly). Only the test-isolation reset harness touched it. Removed the global,
   its write, and the 3 harness lines.
4. `pendingAdminIDStore` → **`Server` field `s.adminIDStore`** (the pattern that
   scales to the hard tail). It only ever existed to smuggle a local out of
   `registerAdminRoutes` back to the constructor. Fix: `registerAdminRoutes` now
   returns the `*tgauth.AdminIDStore` as a 3rd value → the constructor captures it
   as a local (used by the in-constructor `studioGuard` closure, which pre-dates
   `s := &Server{}`) AND stashes it on `s.adminIDStore` for `StartBackground`'s
   `wireBalancerStatsAlerts`. No handler-closure fan-out.

**Lesson from this pass — not every low-ref global is a bounded win.** The var
can live in one file while its *reader* is a helper called from deep inside many
handler closures — threading then fans out into handler construction. Verify the
reader's caller graph, not just the var's refs:
- `kpIDStoreSingleton` (3 refs, 1 file) looks trivial but its reader `resolveKpID`
  is called from a `capi.go` handler closure; also it's a genuine process-singleton
  cache (own `flushLoop` goroutine) — leave it or move to a `Deps` field later.
- `mirrorClientRef` (6 refs) is read by `lite_events.go` group-resolution helpers
  in the SAME expression as `groupStoreRef` + `tgTokenStoreRef` — it's one
  group-resolution DI unit, not a standalone thread.

**Remaining, ranked** — the cheap standalone wins are now spent; the rest need the
`Deps`/`Server`-field pattern: `tsBalancerPoolRef` (8) · `traktStore` (10, 1 file,
process-singleton) · `capiAccountStore` (36) · `ccState` (62, 2 writes) ·
`serverRef` (**the crux** — see below). `bkitSessions` is only 2 reads BUT its
reader `kitAuthFromRequest` has 24 callers and is paired with `kitTGTokenStore`
(read in 6+ files) — treat that as one auth-DI unit, not a quick win.

#### `serverRef` — the Deps-bundle seam (IN PROGRESS, `deps.go`)
`serverRef *Server` is the #1 coupling: ~340 non-test occurrences / 57 files,
mostly inside handler closures. Analysis of what handlers actually pull:
- **~123 nil-checks** (`serverRef != nil` ×84 / `== nil` ×39) — defensive guards.
- **~85 `serverRef.Cfg()`** — LIVE (hot-reloaded) config. This is the subtle part:
  `Cfg()` reads `s.cfgPtr` (an `atomic.Pointer` a reload swaps), so it is NOT
  interchangeable with a closure-captured `cfg` snapshot.
- the rest — ~24 subsystem handles (`clusterPool` 25, `transSvc` 16, `jacredMgr`
  12, `tgBot` 7, `proxyPool` 7, `Reload`/`ReloadProxy*` 11, …).

So **two-thirds of the coupling is just "give me live config, nil-tolerant"** — the
first bundle member to extract. `deps.go` introduces the single chokepoint
`liveConfig(fallback config.Config)` (generalised from the old capi-only
`capiLiveCfg`; the captured snapshot becomes the fallback). Done so far:
- `capiLiveCfg` → `liveConfig` (AST-renamed via `gofmt -r`, 23 capi sites now flow
  through the seam).
- migrated the 5 pure-config sites matching the exact recipe
  `liveCfg := cfg; if serverRef != nil { liveCfg = serverRef.Cfg() }` →
  `liveCfg := liveConfig(cfg)` (`app_assets_api.go` ×2, `plugins.go` ×2,
  `admin_constructor.go`). 28 real call sites total now behind the config seam.
- **first SUBSYSTEM member: `liveTransSvc() *TranscodingService`.** All 16
  `serverRef.transSvc` handler sites (`admin_inspector.go`, `admin_stats.go`,
  `media_gateway.go`) migrated — the `serverRef != nil && serverRef.transSvc != nil`
  guard becomes `if ts := liveTransSvc(); ts != nil`, and the `== nil ||` early-return
  becomes `ts := liveTransSvc(); if ts == nil`. The ONLY production read of
  `serverRef.transSvc` is now the accessor itself. This is the member that unblocks
  extracting the `transcode/` handler cluster (Step 4) — those handlers can take a
  `*TranscodingService` (or a narrow interface) instead of the global. serverRef
  occurrences: 340 → 327.
- **`transcoding_*.go` is now 100% serverRef-free (code).** The last holdout was
  `transcodingHost` in `transcoding_api.go`: `if serverRef != nil { full =
  serverRef.Cfg().Host.StreamHostFor(full) }`. `StreamHostFor` is a proven no-op on
  a zero `Host` (returns its input when there are no aliases — `config.go:280`), so
  `liveConfig(config.Config{}).Host.StreamHostFor(full)` is behaviour-identical in
  both branches. Also drained `payment_landing.go`'s bot-name lookup (zero-config
  `BotName` → `TrimPrefix` yields `""`, matching the old nil fallback). serverRef
  occurrences: 327 → 325. **Milestone: one full cluster is serverRef-free — the
  strongest extraction prerequisite for Step 4's first target is met.** (Extraction
  still needs the response-helper/type-ownership untangle: `TranscodingService`
  itself lives in `httpapi`; moving the cluster means moving or interfacing it.)

**Per-site nuance — do NOT mass-`sed`.** Not every `serverRef.Cfg()` is a plain
fallback: `diag_endpoints.go` uses a nil `serverRef` as an *error* signal (writes
`"error"`, returns); `admin_jacred_status.go`'s `if serverRef != nil` guards a whole
response-building block (nil path must keep the default `{enabled:false}`), not a
config fallback. Migrate only sites whose nil-branch is genuinely "use the captured
snapshot". Mixed config+subsystem sites migrate once their subsystem accessor
exists — e.g. `jacred_api.go` (config + `jacredMgr`) was cleared together with
`liveJacredMgr` below.

- **cluster members: `liveClusterPool()` / `liveClusterFwd()` / `liveClusterStore()`.**
  All 25 `serverRef.cluster*` sites migrated across `admin_cluster.go`,
  `lite_events.go`, `lite_sources.go`, `servers_api.go`. Paired sites capture both
  once — `cp, cf := liveClusterPool(), liveClusterFwd()` — so
  `serverRef != nil && serverRef.clusterPool != nil && serverRef.clusterFwd != nil`
  becomes `cp != nil && cf != nil` (identical: each accessor folds the serverRef-nil
  check in). Repeated `serverRef.clusterPool.X()` in one scope now reuse a single
  `cp` local. `servers_api.go`'s `publicServerListHandler` and `selfAdvertise` are
  now fully serverRef-free; its `serversInfo` handler keeps `serverRef.Cfg()` +
  `serverRef.version` for their own future members. serverRef occurrences: 325 → 300.

- **`liveJacredMgr()`.** All 10 `serverRef.jacredMgr` sites migrated across
  `admin_panel_deps.go`, `jacred_api.go`, `jacred_webui.go`, `pidtor.go`. This also
  cleared the one deferred **mixed config+subsystem** site (`jacred_api.go`): its
  `if serverRef != nil { liveCfg = serverRef.Cfg(); …jacredMgr… }` collapsed to
  `liveCfg := liveConfig(cfg)` + `if mgr := liveJacredMgr(); … mgr != nil …`. The
  `/jacred/` webui guard became `mgr := liveJacredMgr(); if mgr == nil ||
  !liveConfig(config.Config{}).Parser.JacRedLocal`. serverRef occurrences: 300 → 286.

- **`liveTGBot()` / `liveProxyPool()`.** All 7 `serverRef.tgBot` sites
  (`community_plugin_notify.go`, `ts_balancer.go`) and all 7 `serverRef.proxyPool`
  sites (`admin_stats.go`, `admin_inspector.go`) migrated. Uniform nil-guard shape;
  repeated `serverRef.proxyPool.X()` in one scope reuse a single `pp` local.
- **`liveUpdater()` + build-info `liveVersion()`/`liveCommit()`/`liveBuildDate()`.**
  6 `serverRef.updater` sites (`admin_selfupdate.go`, `admin_refconfig.go`) and the
  version/commit/buildDate reads (`admin_stats.go`, `servers_api.go`,
  `lampainit_api.go`) migrated. `lampainit_api.go` dropped its `if serverRef != nil`
  wrapper entirely — the accessors return "" when unwired, so the cache-bust block
  no-ops on its own. (Build-info strings are immutable; a later cleanup could make
  them plain package vars set once from `opts`, removing even the accessor's
  serverRef read.)

**Metric note.** Counting *all* `serverRef` tokens understates progress: each
accessor MOVES a read into `deps.go`, which then accumulates them (comments + the
`serverRef != nil` body). Track **`serverRef` outside `deps.go`** instead — that is
the handler coupling the migration actually removes. Current: **204 occurrences
across 48 files** outside `deps.go`; **55 consolidated inside `deps.go`**. The end
state points those 55 at an injected `Deps` value and deletes the global.

**Config drain (in progress).** The big remaining chunk outside `deps.go` is
`serverRef.Cfg()` (~74 handler sites). Draining into `liveConfig` doesn't inflate
`deps.go` (it's one existing function). Batch 1 done: the clean bare/guarded
`x := serverRef.Cfg()` sites in files that already import `config` —
`admin_panel_helpers.go` (4), `cluster_api.go` (3), `admin_inspector.go` (2),
`cluster_secrets.go`, `admin_config_toml.go`. All are `if serverRef == nil {…} …
cfg := serverRef.Cfg()`: past the nil-guard the config is live, and
`liveConfig(config.Config{})` is behaviour-identical (zero-safe in tests). Remaining
~67. NOTE: fully freeing a handler for Step-4 extraction needs ALL its serverRef
patterns gone — `Cfg()` **and** the `if serverRef == nil` readiness guards **and**
`serverRef.Reload*()`. The config drain removes only the first; guards + `Reload*`
are their own passes. Files needing a new `config` import for the last `Cfg()` site
(media_gateway, stream_proxy, mirror_origin, …) were deferred to avoid 1-site import
churn. serverRef outside `deps.go`: 204 → 193.

**Readiness + lifecycle accessors (done).** Added `serverReady() bool` (replaces
`if serverRef == nil` guards) and `reloadServer()` / `reloadProxies()` /
`reloadProxyCore()` (nil-safe wrappers for the `*Server` reload methods). With
these plus the config drain, a handler file whose ONLY serverRef uses were
{readiness guards + Cfg + Reload} drops to **zero** serverRef and becomes
extractable. **First 4 files fully freed:** `cluster_api.go`, `cluster_secrets.go`,
`admin_panel_helpers.go`, `admin_selfupdate.go`. serverRef outside `deps.go`:
204 → 177; files touching it: 48 → 44.

**Reload* drain (done).** All `Reload*` sites migrated to the wrappers, plus a new
`liveProxyCorePool()` member. **4 more files freed:** `proxycore_bridge.go`,
`admin_inspector.go`, `admin_config_toml.go`, and `server_routes_admin.go` (code;
one history *comment* still names serverRef). serverRef outside `deps.go`: 177 →
156; files touching it: 44 → 41. `admin_stats.go` was the last `Reload*` holder — freed with a new
`liveCustBalPool()` accessor + config import + `serverReady`/`reloadServer`/config
drains.

**Last subsystem pointers (done).** Added the final single-consumer accessors —
`liveAntiDPI` · `liveAlertEngine` · `liveTSBalancerStore`/`liveTSBalancerPool` ·
`liveNwsHub` · `liveDrochubPool` · `liveSisiSources` · `liveProxyLinks`. With these,
NO handler reaches a Server field directly anymore — every subsystem access goes
through a `deps.go` accessor. Freed `admin_panel_telemetry`, `nws_events`,
`sisi_sources_sexstudentki`, `admin_torrbalancer` (+ reduced `sisi_api`,
`admin_panel_plugins`). serverRef outside `deps.go`: 121 → 100; files: 26 → 21.

**Config-drain batch 3 (done) — 15 more files freed via `gofmt -r`.** All remaining
serverRef in these files was `serverRef.Cfg()` + readiness guards, so a 3-rule AST
rewrite (`gofmt -r`) did it safely: `serverRef.Cfg() → liveConfig(config.Config{})`,
`serverRef != nil → serverReady()`, `serverRef == nil → !serverReady()` (plus a
`config` import where absent, and a few straggler comments reworded). Freed:
`admin_branding`, `admin_panel_appreplace`, `admin_panel_healthcheck`, `kit_api`,
`admin_panel_balancers`, `admin_panel_plugins`, `lite_events`, `plugins`,
`servers_api`, `stream_proxy`, `lampainit_api`, `diag_endpoints`, `admin_cluster`,
`admin_refconfig`, `web_installer_api`. **serverRef outside `deps.go`: 100 → 35;
files: 21 → 6.**

**Tail cleared — handlers are now 100% serverRef-free.** Added two `cfgPtr`
accessors: `liveConfigGen() *config.Config` (the atomic-pointer identity capi keys
its handler cache on) and `storeLiveConfig(cfg)` (flaresolverr's in-memory
config write without a full Reload). Migrated `capi.go`, `admin_flaresolverr.go`,
`admin_jacred_status.go`, `accsdb_api.go` (compound guard → `serverReady() &&
kitTGTokenStore != nil`) and reworded the last comments.

### ✅ MILESTONE — serverRef confined to `deps.go` + `server.go`
**No handler references the `serverRef` global anymore** (0 occurrences outside
`deps.go`/`server.go`, down from ~340 across ~58 files). It survives only in:
- **`deps.go`** (~89) — the ~27 accessors + their docs. The single chokepoint.
- **`server.go`** (~19) — the `Server` struct, the setter `serverRef = s`, and a
  handful of composition-root/setup uses.

Every handler cluster is now free to move to its own package (Step 4): it depends
on the `deps.go` accessors, not a package global. **Endgame is now a contained
change**: give the accessors an injected source (a `Deps` value threaded from
`NewServer`, or an interface the composition root satisfies) and drop the global —
touching only `deps.go` + `server.go`, with every call site already stable.

**Config-drain batch 2 (done) — 14 more files freed.** Swept the remaining
low-ref files: bare/guarded `x := serverRef.Cfg()` and `if serverRef != nil { … }`
config blocks migrated to `serverReady()` + `liveConfig(config.Config{})` (adding a
`config` import where absent), and three files whose only remaining serverRef was a
*comment* had it reworded. Freed: `lampa_index_api`, `alloha`, `iptv_econom`,
`admin_panel_telegram`, `admin_init`, `admin_manifest`, `media_gateway`,
`mirror_origin`, `admin_v2_tg_auth`, `admin_lampa_update`, `admin_community_plugins`,
`admin_capi_sources`, `payment_landing`, `transcoding_api`. serverRef outside
`deps.go`: 156 → 121; **files touching it: 41 → 26.**

Endgame: once handler files hold no serverRef, flip the `deps.go` accessors to read
an injected `Deps` value (or keep them but source `serverRef` from a single wiring
point) and delete the package global. `server.go` itself keeps its serverRef uses
(it is the composition root / setter) until last.

**Bundle so far** (`deps.go`): `liveConfig` · `liveTransSvc` · `liveClusterPool` ·
`liveClusterFwd` · `liveClusterStore` · `liveJacredMgr` · `liveTGBot` ·
`liveProxyPool` · `liveUpdater` · `liveVersion`/`liveCommit`/`liveBuildDate` ·
`serverReady` · `reloadServer`/`reloadProxies`/`reloadProxyCore` · `liveProxyCorePool` ·
`liveCustBalPool` · `liveAntiDPI` · `liveAlertEngine` ·
`liveTSBalancerStore`/`liveTSBalancerPool` · `liveNwsHub` · `liveDrochubPool` ·
`liveSisiSources` · `liveProxyLinks`. **Remaining serverRef outside `deps.go` is
now only inline `Cfg().field` reads (~53) + `server.go` (the composition root).**

### Step 4 — handler clusters (only after 2+3) — IN PROGRESS
Extract in ascending coupling, each cluster wrapping its already-existing
`internal/<feature>` package. Keep `httpapi` as the thin router/composition root.
Effort: L (per cluster M).

**Proven pattern (first extraction done: `internal/audiobothttp`).** The
`audiobot_api.go` cluster (372 LOC — handlers + the shared scraper/decryptor state
+ the TG music-searcher adapter) is now its own package. Recipe, validated:
1. `mv` the handler file(s) to `internal/<cluster>http/`; `sed` the package name.
2. Add a tiny `deps.go` with LOCAL FORWARDERS for the thin things it used from
   httpapi (`writeJSON → httpx.WriteJSON`, `var json = jsoniter…`). The moved
   handlers then compile with ZERO call-site changes — this is the key trick.
3. Expose ONE composition-root entry that encapsulates all internal wiring —
   `Register(router, cfg, hostFn) tgauth.MusicSearcher` here (mounts routes,
   returns the bot adapter, nil when disabled). Everything else stays unexported.
4. Rewrite the composition root (`server.go`) to call the entry; delete the old
   in-package code. Build-green in one shot (a package move is atomic).

Result: httpapi shed a whole cluster; the new package's only httpapi coupling is
the two forwarders (both to `httpx`/`jsoniter`, i.e. genuinely shared infra).

**Extraction 2 — `internal/subvtt` (shared util) + `internal/opensubs` (cluster).**
Demonstrates the shared-util-first variant: the opensubs handlers used
subtitle-conversion helpers (`convertSubtitlesToVTT`, `buildFragmentedVTTSegments`,
`parseSubtitles`) that live in the transcode/subtitle domain, so those (plus the
`srtToVTTLegacy` fallback they call) were first extracted to `internal/subvtt` and
exported (`ParseSubtitles`/`ConvertSubtitlesToVTT`/`BuildFragmentedVTTSegments`),
its test moved with it. Then `opensubs_api.go` → `internal/opensubs` via the same
recipe (local `writeJSON`/`extractPathParam`/`json` forwarders, a `RegisterRoutes`
entry, `registerOpenSubsRoutes` removed from `server_routes_features.go`). Net: two
more clusters out of httpapi, both with tests green. Lesson: `grep` for a symbol
name can conflate unrelated same-named methods (femd/gencit have their OWN
`parseSubtitles` HTML-scraper method — NOT the subtitle-file parser) — verify the
receiver, not just the name, before wiring an import.

**Extraction 3 — `internal/transcodesvc` (the big one: 29 files, ~17.9k LOC).**
Done and green. Key lessons for the large clusters:
- **Trust the compiler, not grep, for the coupling tail.** grep found ~4 deps;
  the compiler surfaced ~15 (batched, 10 errors at a time — use `go build -gcflags="-e"`
  to see the whole tail). It converged: trivial helpers became local copies/forwarders
  in `deps.go` (`writeJSON`→httpx, `requestScheme`, `fileExists`, `json`, `relToRuntime`,
  `sanitizeForwardedReferer`); anything reaching the live server was INJECTED via a
  `Deps` struct set once in `RegisterRoutes` (`LiveConfig`, `PluginJS`, `TSBalancerPool`,
  and the torrs/pidtor bridge funcs — injection also sidesteps the torrs/notorrs build tags).
- **Functions that read a type's UNEXPORTED fields must move WITH the type.**
  `snapshotTranscodingJobs`/`killTranscodingJobByID` lived in `media_gateway.go` but read
  `svc.jobs`/`job.exitCode` — they (and their `JobSummary` DTO) had to move into transcodesvc
  and be exported.
- **Relocate internal-touching tests; split cross-package ones.** Tests using
  `svc.stopJob`/`scheduler`/`useCapiPool` moved into transcodesvc; the pidtor↔transcode
  agreement test kept its httpapi half and checked the pool's HRW directly instead of the
  moved helper.
- **A shared helper pulled into the new package can leave the host reaching back in.**
  `pidtorGetTorrServer` (httpapi) called `transcodesvc.TSPoolBackendFor`, which reads the
  INJECTED pool — nil in httpapi's own unit test. Fixed by giving httpapi its own
  `tsPoolBackendFor` over its `tsBalancerPoolRef` (same object as the injected one in prod).
  Rule: the host must not depend on the extracted package for something it already owns.

**Extraction 4 — `internal/dlnahttp` (5 files, ~1.6k LOC).** A smaller transcode:
inbound coupling was `writeJSON` (forwarder) + `genericPluginJSHandler` (injected via
`Deps`). Outbound is 3 long-lived managers (`DLNATorrentManager`/`DLNACoverGenerator`/
`DLNAUPnPServer`) the composition root Start/Stop/Closes — already exported types with
exported lifecycle methods, so `server.go` just prefixes the two field types with
`dlnahttp.` and its Start/Stop calls are unchanged. `registerDLNARoutes` →
`RegisterRoutes(router, cfg, Deps)`; its `dlna_test.go` moved with the cluster. Clean,
no design surprises.

**Extraction 5 — `internal/sisihttp` (17 files, ~10.5k LOC).** The big utility-tail
case. `go build -gcflags="-e"` showed 21 inbound symbols: 11 pure helpers copied to a
`helpers.go` (`submatch1`, `hostFromRequest`, `toString`/`toBool`, `parseBool*`,
`writePlain`/`writeRawJSON`, `relToRuntime`, `maxInt`, `cookieValue`) and 10 injected via
`Deps` (`liveConfig`, `serverReady`, `kitAuthFromRequest`, `clientIP`, `readFileAny`,
`drochubRouteKey`, the sisi/proxy/drochub pool accessors, `getTorrsServer`). `clientIP`
and `readFileAny` were INJECTED not copied — their bodies pull dep-chains
(`directRemoteIP`/`isRemoteFromTrustedProxy`, `readConfigFromTOMLAsJSON`). `RegisterSISIRoutes`
→ exported `RegisterRoutes(router, cfg, Deps, …stores)`; httpapi passes its own functions
straight into the `Deps` fields (signatures match). Outbound: only `sisiSourceHost` (→
exported `SisiSourceHost`, used by `kit_bind.go`). The 10 test files moved with the cluster;
`resetHTTPAPIGlobals` got a sisihttp-local version doing just the `httpclient.ClearRegistry()`
the sisi live-source tests actually need.

**Lesson — an injected forwarder that unit tests call directly needs a real unwired
default, not `nil→zero`.** The sisi state/history tests invoke handlers directly
(`sisiHistoryAddHandler(cfg).ServeHTTP(...)`) without going through `RegisterRoutes`, so
`setDeps` is never called and `deps.ReadFileAny` is nil. My first cut had `readFileAny`
return `nil,false` when unwired — which silently dropped the `init.conf` fallback, so
`loadSisiRuntimeCfg` kept its defaults (`HistoryEnable:true`, `spider:true`) and two tests
that write `init.conf` to `LAMPAC_GO_HOME` failed (403-when-disabled → 200; phub-filtered →
present). Fix: the unwired branch now does `os.ReadFile(relToRuntime(rel))`, which is exactly
what the host `readFileAny` does when `serverReady()==false` (the TOML-as-JSON bridge is
guarded by `serverReady()`, false in tests). Rule of thumb: an injected dep whose host body
has a meaningful `serverReady()==false` path must reproduce that path in its unwired default,
or direct-handler unit tests see empty behavior.

**Extraction 6 — `internal/collectionshttp` (1 file, ~27k chars).** The cleanest cut so
far — the `/api/collections/*` TMDB smart-collections feature (`collectionsHandler` + all
person/genre/studio/pin/stats handlers, `collCache`, `pinnedPersons`) lived entirely in one
`collections_api.go`, imported only `config` + `tmdbcache`, and `newCollectionsHandler` already
took explicit params (`pool, apiKey, cfg.Collections, repoRoot`) — so **zero injected deps**.
`go build -gcflags="-e"` showed exactly one inbound symbol: `writeJSON` (×48), replicated as a
one-line local forwarder to `httpx.WriteJSON` in the new `register.go`. `registerCollectionsRoutes`
lifted verbatim into exported `RegisterRoutes(router, cfg, tmdbPool)`; server.go call swapped;
the stub in `server_routes_features.go` deleted. **No tests moved** — the only collections test
(`capi_collections_test.go`) covers the *different* CUB `/api/collections/view/{id}` endpoint in
the `capi` family, which stays in httpapi (shared URL prefix, distinct chi patterns → no conflict;
verified that test still green). Gate: build/vet/gofmt clean, cross-compile torrs/win/arm all 0.

**Extraction 7 — `internal/iptvhttp` (5 files + 3 tests).** The `/api/iptv/*` playlist/EPG/preview
manager (`iptv_api`, `iptv_econom`, `iptv_epg_api`, `iptv_preview`, `iptv_proxy`). Key scoping call:
`iptvonline.go`/`iptvonline_test.go` (the `iptvOnlineChecker` for the iptv.online *paid content
source*, bound via `/api/kit/bind/iptvonline`) is a balancer-family provider, NOT part of the
playlist manager — it **stays in httpapi**. `go build -gcflags="-e"` tail: `writeJSON`×29 + `json`
(copied), `hostFromRequest`×3 + `loopbackHostPort` + `calendarTgID` (pure helpers → helpers.go),
`clientIP`/`liveConfig`/`serverReady` (injected via `Deps`). The ledger's feared "iptv↔calendar"
coupling was a false alarm: `calendarTgID` is misleadingly named — it's a pure `tgauth.Store`
TG-ID resolver taking explicit args, zero calendar-cluster state, so it's a copy not an injection.
`registerIPTVRoutes` → exported `RegisterRoutes(router, cfg, Deps, tgTokenStore, proxyLinks,
transSvc)`; `transSvc *transcodesvc.TranscodingService` is the already-extracted package (no cycle:
iptvhttp→transcodesvc). Removing the 8 files orphaned 6 imports in `server_routes_features.go`
(`context`/`os/exec`/`time`/`iptv`/`proxylink`/`transcodesvc`) — cleaned. Gate: iptvhttp tests
green first try, httpapi test-binary still compiles, build/vet/gofmt clean, cross torrs/win/arm 0.

**Extractions 8 & 9 — `internal/skiphttp` + `internal/calendarhttp`.** These proved the
**admin-auth-as-injected-function-value** pattern that de-risks the whole admin surface. Both
clusters have a public route-registrar (`/api/skip*`, `/api/calendar/*`) AND admin routes
(`/{adminPath}/api/skip*`, `.../calendar*`) registered from a *different* site
(`server_routes_admin.go`). The one host coupling in the admin handlers is
`tgAdminAuthCheck(w, r, *tgauth.Store, *tgauth.AdminIDStore) (int64,bool,bool)` — it takes
explicit args (both exported `tgauth` types) so it is **injected as a `Deps.AdminAuthCheck`
function value**, not extracted. The closure is built in the host (server.go) where it closes
over the real admin config, and the extracted package calls it through a `tgAdminAuthCheck`
forwarder whose unwired default **fail-closes** (403, ok=false). No full `internal/adminauth`
extraction was needed — the 49-file ripple the ledger feared is avoided entirely for these two.
Structure per package: `RegisterRoutes(router, cfg, Deps, …)` returns the store(s) the host's
Server lifecycle + admin router need (`*skipdb.DB`; `*calendar.Store,*calendar.Cron`) and calls
`setDeps`; a separate exported `RegisterAdminRoutes(router, adminPath, tgStore, adminStore, …)`
encapsulates the admin route wiring (replacing 5 + 2 inline lines in `server_routes_admin.go`
with one call each). Safe because admin routes register only when the store is non-nil, i.e.
after `RegisterRoutes` ran `setDeps`. Notable: `calendarTgID` **originated** in `calendar_api.go`
(hence the name) — moving that file removed it from httpapi, which is fine because iptvhttp took
its own copy during Extraction 7 (verified no other httpapi caller). `newCalendarBotAdapter` +
`calendarBotAdapter` moved with the cluster (defined in-file). Seam otherwise: `writeJSON`/`json`
copied. No tests existed for either. Gate: build/vet/gofmt clean, httpapi test-binary compiles,
cross torrs/win/arm 0.

**Extraction 10 — `internal/adminhttp` (IN PROGRESS, incremental).** The admin panel is
huge — **51 files, ~22k LOC, a 40-field `adminRouteDeps` struct** — so it is extracted one
cohesive slice at a time into a single growing `adminhttp` package (not one atomic move). A
scoping pass produced the coupling map; the key findings:
- The real blocker is NOT type cycles (the registries `DynamicRouteRegistry` /
  `CustomPluginRegistry` / `CommunityUpdateCron` are all interface-injectable). It is
  **reverse coupling**: `admin_init.go` and `admin_panel_helpers.go` are misnamed *shared-helper
  drawers* — `writeHTML` (used by ~70 non-admin files), `readFileAny` (16), `relToRuntime` (12),
  `loadMergedConf`, `toBoolAny/toStringAny`, etc. A slice that uses one of these can't move until
  the helper is relocated to a leaf pkg OR the helper is injected as a `Deps` func value.
- `tgAdminAuthCheck` is injected as a `Deps.AdminAuthCheck` function value (same pattern as
  skip/calendar), fail-closing 403 when unwired.
- `HealthChecker` is the one genuinely hard type (admin_inspector reaches its private `hc.mu`/
  `hc.statuses`) — its slices (inspector/stats/healthcheck) are deferred until healthcheck is
  refactored to expose accessors.
Seam mechanics: `adminhttp/deps.go` holds a package-global `Deps` + exported `SetDeps` (called
once at the top of `registerAdminRoutes`, before any slice registers) + per-dep forwarders; the
`Deps` struct GROWS as slices move (each adds only the fields it needs). Each moved slice gets a
`RegisterXxxRoutes(router, adminPath, tgStore, adminStore)` that `server_routes_admin.go` calls
in place of the old inline registration. **Slices done so far (9):** `admin_flaresolverr.go` (pilot — seam: writeJSON copied; liveConfig/
serverReady/storeLiveConfig/AdminAuthCheck/UpdateConfigTOMLMapNoReload injected; proxyableBalancers
injected), `admin_jacred_status.go` (JacredStatusSnapshot → external `jacred.Status`), `admin_panel_bans.go`
(zero new deps), and a batch: `admin_panel_logs.go`, `admin_refconfig.go`, `admin_panel_groups.go`,
`admin_torrbalancer.go`, `admin_panel_envpresets.go`, `admin_selfupdate.go` (added the `json` var +
injected liveUpdater/liveVersion/liveTSBalancerPool/liveTSBalancerStore/buildUpdaterConfig/
checkTSHealthWithAuth — all external/primitive returns).

**Batch-2 lesson — the easy admin slices are now exhausted; pre-vet before moving.** A trial batch
(cluster/torrs/drochub/telemetry/promo) had to be entirely pulled back: `admin_drochub` needs
`DynamicRouteRegistry` (interface-inject — deferred); `admin_cluster` returns internal
`ensureClusterSecretsResult`; `admin_torrs` uses internal `tsExternalAuthState` (via an
`atomic.Pointer`); `admin_promo` has reverse coupling (`promoRedeemHandler` is called by
`server_routes_userauth.go`) + `setAuthCookies`; `admin_panel_telemetry` pulls balancer-stats-domain
funcs (`GetGlobalBalancerStats`, `liveAlertEngine`, `knownBalancers`) that belong with the deferred
lite/balancer core. Remaining admin work is per-file untangling (interface-inject the 3 registries;
relocate/externalize a handful of internal result types; sensitive auth/payments last) — slower,
bespoke, no longer batchable. Recommend vetting each file's undefined-symbol tail (types + reverse
coupling) individually before the move.

**Slice 10 — `admin_drochub.go` (+helpers) — proved the interface-injection + reverse-coupling
untangling.** `internal/adminhttp/registry_iface.go` now defines `DynRoutes` (the exported-method
subset of `*DynamicRouteRegistry`: Register/RegisterHandler/Unregister/Lookup/Names); the moved
handler's `dynRoutes *DynamicRouteRegistry` param became `dynRoutes DynRoutes`, and the composition
root passes the concrete registry straight in (it satisfies the interface) — the registry TYPE stays
in httpapi, no cycle. This interface is reusable for the other registry-users. Reverse coupling
handled two ways: `drochubRouteKey` (defined in the moved file but used by server.go's sisi wiring)
→ a pure copy left behind in `httpapi/drochub_routekey.go`; `isValidCustBalName`+`reCustBalName`
(defined in admin_constructor, which stays) → pure copies into `registry_iface.go`. Note capi_sources
looked like a DynRoutes win but is actually entangled with the capi/lite EVENT-RESOLUTION core
(`resolveEventsPlugins`/`buildEventItems`/`capiResolveStreamsDiag`, internal returns) — deferred with
the balancer core.

**Shared-drawer seam added (`internal/adminhttp/helpers.go` + deps.go) — no 70-file relocation.**
Copied the pure host helpers (`writeHTML`=httpx wrapper, `writeRawJSON`, `cookieValue`,
`relToRuntime`, `toBoolAny`, `toStringAny`, `ensureMapChild`, `setNestedMap`, `randomAlphaNum`,
`adminPathFile` const) and injected the config-TOML-bridge ones (`readFileAny`→`([]byte,bool)`,
`loadMergedConf`/`updateConfigTOMLMap`/`updateInitConfMap`→primitives, `writePrettyJSON`→error,
`applyLegacyMapToTOML`→map mutate). This unblocks the ~18 writeHTML/config-using admin files at
their seam layer. With it, extracted 3 more slices: `admin_panel_config.go`, `admin_panel_appreplace.go`
(+customcode), `admin_panel_telegram.go` (tgsettings/broadcast/admins — deps.tgBot + memberChecker
external). Note `admin_panel_users` looked
eligible but is balancer-domain (`knownBalancers`/`balancerGroupMap`/`pluginQualityBadgeGet`/
`PluginKeyFor`) → deferred with the balancer core.

**Batch A (+6, all green): `admin_config_toml`, `admin_panel_proxycore`, `admin_lampa_update`,
`admin_panel_plugins`, `admin_panel_proxy`, `admin_panel_jsmodules`.** Surfaced the REVERSE-COUPLING
WEB — utilities defined in moved files but called by CORE httpapi: `configRepoRoot`/`createBackup`
(cluster_secrets), `generateAnnotatedTOML` (server.go), `lookupGeoIP` (proxycore_bridge). Fixed with
`internal/adminhttp/exports.go` — thin exported wrappers the core calls up into (adminhttp never
imports httpapi → no cycle; flagged as tech-debt: these config/geo leaf utilities really want a
shared leaf pkg). Also: moving `admin_panel_proxy` (owner of `proxyableBalancers`) let the earlier
injection be removed (real var now in-package); `proxycoreWARPBinDir` computed inside
`RegisterProxycoreRoutes(repoRoot)`.

**19 admin slice files extracted; 30 admin files remain. The remaining set is the HARD TAIL — pure
mechanical slicing is exhausted:**
- **3 PIN** (`admin_init`, `admin_panel_helpers`, `admin_panel_auth`) — shared-drawers used by ~70
  non-admin files / the injected-gate source. Can't move without first relocating them to a leaf pkg.
- **6 BALANCER-blocked** (`admin_inspector`, `admin_panel_balancers`, `admin_panel_users`,
  `admin_panel_telemetry`, `admin_panel_healthcheck`, `admin_capi_sources`) — need the lite/balancer
  core extracted first.
- **Internal-type-blocked** (most of the rest): the interface-inject trick only works when EVERY
  interface method returns external/primitive types — `DynRoutes` qualified, but `CustomPluginRegistry.List()
  []CustomPlugin` does NOT (confirmed by trying admin_custom_plugins → pulled back). Same wall for
  `admin_capilog`→`StoredWebLog`, `admin_cluster`→`ensureClusterSecretsResult`, `admin_torrs`→
  `tsExternalAuthState`, `admin_branding`→`Branding`, `admin_waf`→`wafConfig`, `admin_panel_feedback`→
  `FeedbackStore`, `admin_stats`→`ProcessOSStats`/`HealthStatus`, `admin_community_plugins`→
  `CatalogEntry`. Each needs its type relocated to a shared/leaf pkg (a cascading refactor, since those
  types are used by non-admin code too).
- **Huge & coupled**: `admin_tg_panel` (3131), `admin_constructor` (2869), `admin_panel_deps` (1112).
- **Sensitive**: `admin_login`, `admin_webauthn`, `admin_password_users`, `admin_v2_tg_auth`,
  `admin_payments_api` — movable but need careful verification (auth/2FA/payments).
Recommendation: the type-relocation refactor (move the shared value types to a leaf pkg) is the
next unlock; do the sensitive + huge files last with focused verification.
**Correction on the shared-drawer helpers:** they are NOT a hard blocker and do NOT require the
70-file relocation the scoping agent proposed. The relocation would only avoid DUPLICATION, but
duplicating a 1-line wrapper is already how writeJSON is handled. So per-slice: COPY the pure ones
(`writeHTML` = 1-line `httpx.WriteHTML`; `relToRuntime`/`cookieValue`/`toBoolAny`/`toStringAny`/
`writeRawJSON` = pure) into adminhttp's helpers, and INJECT the config-TOML-bridge ones
(`readFileAny`→`([]byte,bool)`, `loadMergedConf`→`map[string]any`, `loadConfigTOMLAsMap`,
`saveConfigTOMLFromMap`) as `Deps` func values — they return primitives/external types, so no cycle.
The non-admin files keep httpapi's originals untouched. The ONLY genuinely-hard blockers remain:
(1) injected funcs returning httpapi-INTERNAL types (StoredWebLog, ProcessOSStats, Branding,
wafConfig) — need the type external or moved; (2) `HealthChecker` private-member access; (3) the
shared registry types → interface-inject; (4) sensitive auth/payments slices → last. **Lesson:** the
scoping agent's "clean pilot" ranking was optimistic — several candidates (capilog, stats) inject
functions that return httpapi-INTERNAL types (`StoredWebLog`, `ProcessOSStats`), which reintroduces
the cycle; verify each injected function's RETURN type is external/primitive before moving a slice.
Gate per slice: build/vet/gofmt clean, httpapi test-binary compiles, cross torrs/win/arm 0.

### Step 4b — ONLINE-SOURCE FRONT (started; pilot DONE)

**Pilot — rezka family → `internal/litesrc` (+2 leaf pkgs). VERDICT: mechanical
after all.** The ledger feared the source front was "qualitatively different /
not mechanical"; the compiler probe disproved that for the pilot: the FULL
rezka cluster (rezka.go 2791 + ahuerezka.go 811 + rezka_browser_pool.go 806 +
rezka_browser.go 78 + 2 test files, ~4.5k LOC) had a 17-symbol undefined tail,
of which only TWO needed injection. Probe first, fear later.

**Seam packages created (both leaf):**
- **`internal/litehtml`** — the shared Lampa "lite" render family that lived at
  the bottom of `getstv.go` (a misnamed shared drawer, same disease as
  admin_init.go): `AppendVoiceHTML`/`AppendMovieHTML`/`AppendSeasonHTML`/
  `WriteEmpty`/`AttrJSON`/`Bool`/`BoolAny`/`JoinName`/`QueryInt` + unexported
  copies of `toString`/`parseBoolParam`. httpapi's `getstv.go` keeps thin
  unexported forwarders (`getsTVAppendMovieHTML` → `litehtml.AppendMovieHTML`)
  so the ~60 staying source files needed ZERO call-site changes (the writeJSON
  trick). `getsTVPoster`/`getsTVYearFromRaw` stayed (getstv-specific).
- **`internal/browsergate`** — the process-wide Chrome concurrency subsystem:
  `browser_gate.go` verbatim + `dynSemaphore` carved out of
  `browser_pool_settings.go` (mirage settings stayed). Exported: `Gate`/
  `NewGate`/`Global`/`ChromeSem`/`NewChromeSem`/`DynSemaphore`/
  `NewDynSemaphore`/`AcquireBrowserExclusive`. Only 2 real consumer sites
  existed (`mirageBrowserSem`, `pidorezkaBrowserSem` package vars) — qualified
  directly, no forwarders needed. Unblocks every browser-using source
  (mirage, vibix, zona, turbo, eng_sources, kinobase, zetflix, alloha…).

**`internal/litesrc` — the growing source package (adminhttp pattern).**
`deps.go`: 2-field `Deps` + `SetDeps` called ONCE at the top of
`liteSourceHandler` (lite_sources.go) before any checker is constructed —
injected: `StreamProxyDirectURL` (host body pulls isStreamProxyDisabled/
streamHostFromRequest → live config; unwired default returns the URL
unproxied = the no-server path) and `PluginQualityBadgeGet` (unwired → "").
`helpers.go`: pure copies (`hostFromRequest`, `parseBoolParam`,
`normalizeSearchTitle`+replacer, `submatch1`) + litehtml forwarders so moved
files need zero call-site edits. Constructors exported
(`NewPidoRezkaChecker`/`NewRhspremChecker`/`NewAhueRezkaChecker`), method
`handle`→`Handle`; lite_sources.go calls `litesrc.NewX(cfg).Handle(...)`.

**Test policy (per admin_manifest precedent):** integration tests that route
through `authedHandler(liteSourceHandler(...))` STAY in httpapi
(`rezka_integration_test.go`, `ahuerezka_test.go` — they now exercise the real
litesrc wiring through the route table); white-box tests move with the code
(`litesrc/rezka_test.go` promo detection, `ahuerezka_live_test.go` env-gated).
Found pre-existing test bug while splitting: `TestRhspremChecksearchNative`
sets `PidoRezka.Host` but the rhsprem checker reads `Online.Rhsprem` → falls
back to live hdrzk.org (slow/flaky, even starts Chrome). Flagged, not fixed.

**Fan-out recipe for the remaining ~60 sources** (per source file):
1. Compiler-probe it (copy to a scratch pkg, `go build -gcflags="-e"`).
2. Tail symbols resolve as: litehtml forwarders (already in litesrc/helpers.go)
   · pure helpers (copy into helpers.go if new) · browsergate (import) ·
   host-reaching funcs → new `Deps` fields (verify primitive/external returns).
3. Move + export constructor/Handle + rewire the one lite_sources.go line.
4. Integration tests stay in httpapi; white-box tests move.
Expected hard cases: alloha (mirage-subsystem tail: mirageBrowserSem,
newMirageWSClient, mirageStealthJS… + edge-hash/guard family + capiResolveRequest
— extract mirage browser infra or defer alloha until mirage itself moves),
youtube (4137 LOC + yt-dlp/mux subsystem), the capi/lite_events core (LAST —
it IS the shared front). httpapi after pilot: 412 files / ~150.4k LOC.

**Fan-out waves A–C (done): redheadsound, uaflix, zetflix(+browser),
kinobase(+browser+facade).** Batch-probed 5 candidates first; `videodb`
DEFERRED — its tail is the collaps family (9 `collaps*` symbols + externalIDs
cache), it moves with the collaps cluster. Seam grew once: +5 `Deps` fields
(`ClientIP`, `IsStreamProxyDisabled`, `StreamHostFromRequest`,
`StreamProxyURL`, `StreamProxyURLWithHeaders` — the last two take
`*proxylink.Manager`, an external type, so full injection not copies; unwired
defaults mirror the host's no-server paths) + pure copies (quality/checksearch
family: `normalizeQualityBadge`/`sanitizeQualityBadge`/
`writeCheckSearchResponse(+NoRCH)`/`qualityBadge`/`normalizeQualityLabel`,
`cdnmoviesNumber`, `toString`). Notable per-file surgery:
- **zetflix reverse coupling solved by EXPORT-UP, not copy**: turbo.go + hdvb.go
  use the obrut player-format decoders defined in zetflix.go
  (`ZetflixPlayerConfig`/`ZetflixExtractPlayerConfig`/`ZetflixDecodeObrutBase64`/
  `ZetflixCleanVoiceName`/`ZetflixObrutIsSerial`/`ObrutSeasonNumRe`/
  `ObrutVoiceTitleRe`). Since httpapi already imports litesrc, the family was
  exported from litesrc and the two staying consumers qualified — ZERO
  duplication (adminhttp exports.go pattern, but in the extracted package).
- **kinobase was the shared-drawer case**: it DEFINED `submatch1`/
  `normalizeSearchTitle`(+replacer) used by ~30 staying sources → copies left
  in `httpapi/search_helpers.go` (litesrc already had its own). xsearch
  coupling solved by exporting `KinobaseChecker` + `Search` (the adapter
  stores the checker in a struct field, so the TYPE had to be exported;
  `kinobaseSearchItem` stayed unexported — its exported FIELDS are enough for
  the adapter's range loop).
- Integration tests (all of redheadsound/zetflix/kinobase _test.go — every
  test routes via `authedHandler(liteSourceHandler)`) stayed in httpapi
  untouched; only the white-box `kinobase_browser_facade_test.go` moved.
After waves A–C: httpapi **405 files / ~142k LOC**; litesrc 16 files /
~13.4k LOC. Next easy probes: vibix, kinogo, kinovod, eneyida, ashdi, kinoukr;
collaps+videodb as one cluster; alloha/mirage/youtube still the hard tail.

**Wave D (done): kinovod + the UA family (ashdi, eneyida, kinoukr) — 4 sources
in one move.** Probe findings that shaped the wave: **vibix DEFERRED** (tail
pulls mirage/turbo browser infra: `mirageBrowserSem`, `turboBrowser`,
`turboEvalAsync`, `kinomixGetPlayerIframe`); **kinogo DEFERRED** — it is
collaps-based (14 `collaps*` tail symbols), joins the collaps+videodb+kinogo
cluster. eneyida/kinoukr consume ashdi helpers (`ashdiParseSubtitleList`,
`ashdiFixStream`, `ashdiSeason`…) — moving the family together made those
in-package again, zero surgery. New seam:
- **First interface-inject in litesrc**: kinovod's remote-checksearch uses
  `rchGate(w,r,bool)bool` (all-external → plain func inject) and
  `newRchClient(r)` returning internal `*rchClient` → `RchFetcher` interface
  (`IsConnected`, `Get`) + `NewRchClient func(*http.Request) RchFetcher` Deps
  field; host closure boxes the concrete client; unwired default is a
  `rchDisconnected{}` stub (nil interface would panic — the waf typed-nil
  lesson applied preemptively).
- `balancer_retry.go` copied VERBATIM as `litesrc/balancer_retry.go` (used by
  ashdi + 8 staying sources; first simplified re-write attempt was
  behaviorally wrong — retryable-status/net-error classification matters:
  mirror files verbatim, don't paraphrase). `vdbmoviesSubtitleRe` copied.
- Test split: `ashdi_test.go` was MIXED — 3 white-box tests (set unexported
  `checker.wormholeHost`) → `litesrc/ashdi_whitebox_test.go`; 4 harness tests
  stayed. `kinovod_test.go` pure white-box → moved whole. kinoukr's staying
  harness test needed `kinoukrReverseString` for fixture-building → tiny local
  copy in the test file (not worth exporting).
After wave D: httpapi **400 files / ~138.2k LOC**; litesrc **23 files /
~17.4k LOC** (9 sources: rezka, rhsprem, ahuerezka, redheadsound, uaflix,
zetflix, kinobase, kinovod, ashdi, eneyida, kinoukr — 11 checkers).

**Wave E (done): the collaps CLUSTER — collaps + videodb + kinogo + lift in
one move.** Probing the trio surfaced that **lift.go is also collaps-based**
(13 `collaps*` tail symbols) — always reverse-scan before fixing the cluster
boundary; the family was 4 files, not 3. Combined tail: 25 symbols, only 3
new. Seam:
- `ExternalKPForIMDB func(cfgRoot, imdbID string) string` Deps field —
  high-level-injection collapsing videodb's 4-line reach into
  `externalIDsCache` internals (mutex+map fields) into one primitive-typed
  call; host closure does loadExternalIDs+lookup. externalids_api.go stays.
- Copies: `parseHLSQualities`+`hlsStreamInfRe` (verbatim), `truncURL`;
  `parseInt64` was DEFINED in collaps.go and used by staying flixcdn/
  kinotochka → drawer copy added to httpapi/search_helpers.go (the kinobase
  precedent, reversed direction).
- **xsearch UNBLOCKED**: the ledger's old blocker "*collapsChecker wraps in
  xsearch_adapters" dissolved the same way as kinobase — exported
  `CollapsChecker` + `SearchList` + a 1-line `Token()` accessor (the adapter
  read the unexported `token` field). Both xsearch checker-adapters (kinobase,
  collaps) now wrap litesrc exported types; the xsearch cluster itself is now
  movable if desired.
- Tests: collaps_test/videodb_test all-harness → stayed; kinogo_test MIXED →
  3 white-box tests + fixture consts + the `newKinogoTestUpstream` helper
  copied into `litesrc/kinogo_whitebox_test.go` (fixtures used by BOTH halves
  get duplicated — test data, acceptable); kinogo's `testdata/kinogo_la_*.html`
  moved to `litesrc/testdata/`. lift_test.go ALL-white-box → moved whole,
  **including TestMain** (sets LAMPAC_LIFT_TEST env; litesrc had none — a
  package takes at most one TestMain, watch this on future moves).
After wave E: httpapi **395 files / ~133.5k LOC**; litesrc **29 files /
~22.2k LOC** (15 checkers).

**Wave F (done): 14 small sources in ONE wave** — lumex, klonfun, iframevideo,
cdnmovies, moonanime, bamboo, starlight, unimay, leproduction, mikai, animeon,
femd, gencit, kinobadi (families: bamboo+klonfun, animeon+mikai). The seam has
MATURED: probing all 14 against the existing litesrc surface showed almost
every tail fully covered — the only additions were the `json` jsoniter var
(mirrors server.go), a `firstQuery` copy (defined in staying iptvonline.go),
and a `decodeJSONLimited` drawer copy back into httpapi/search_helpers.go
(defined in moving moonanime.go, used by staying mirage.go). Gotcha hit:
cdnmovies.go moving IN brought the original `cdnmoviesNumber` — the earlier
wave-B copy in helpers.go had to be DELETED (when a definer file finally moves
in, drop the package's own mirror). Test splits: femd_test/kinobadi_test
(3W+3H each; fixture helpers like `femdSampleEmbedHTML` duplicated into the
white-box half). **Pre-existing red discovered: ALL 4 TestMoonAnime\* fail**
(vod-resolution feature the tests expect is unimplemented in video(); search
fixture contract drift → live-host timeout) — proven move-independent
(video()/checkSearch untouched by the mechanical renames; code+tests both
last edited Jul 18 together), flagged as a spawn-task, not chased.
After wave F: httpapi **381 files / ~123.5k LOC**; litesrc **45 files /
~32.3k LOC** (29 checkers).

**Wave G (done): 18 mid-tail sources in one wave** — vokino, veoveo, sakhtv,
turbo, hdvb, kodik(+kodik_index), rutubemovie, vkmovie, plvideo, vdbmovies,
cdnvideohub, kubikvkube, remux, mirkino, videoseed, uafilm_me, **vibix**.
Key unlocks:
- **turbo is the turbo-browser home** (kinomixGetPlayerIframe/turboBrowser/
  turboEvalAsync used by vibix) — moving turbo let the previously-DEFERRED
  vibix join the same wave (its whole tail became in-package + one shared
  injection). Cross-source helper webs resolve themselves when the family
  moves together.
- Only ONE new Deps field: `MirageBrowserSem *browsergate.ChromeSem` (turbo+
  vibix route Chrome work through the host's mirage pool; external type, so a
  direct value field; unwired default = own pool on the shared Global gate).
  `rchFetch` needed NO field — it composes the already-injected `newRchClient`;
  `rchClientStreamCapable` is pure (copied).
- turbo/hdvb had wave-B `litesrc.Zetflix*` qualifiers — stripped on move-in
  (the httpapi side no longer references the obrut exports at all).
- kodik's xsearch adapter snapshots 3 unexported fields at construction →
  exported `KodikChecker` + `Token()`/`APIHost()`/`Client()` getters;
  `kodikVideoHandler` → exported (satellite handler used by lite_sources).
- vibix joined late and MISSED the reverse-scan → build caught
  `vibixM3U8Handler`/`vibixEmbedHandler` registered directly in server.go +
  proxy_middleware.go (exported + qualified). Lesson: re-run the reverse scan
  when the wave's file set grows.
- vdbmovies arriving killed the helpers.go `vdbmoviesSubtitleRe` copy (wave-F
  lesson applied); drawer copies left in httpapi: `vokinoIntFromMap`
  (animevost/animelib_index), `isInt64` (kinopub/alloha) — both verbatim
  (first paraphrased draft diverged — wave-D lesson re-applied).
- Tests: sakhtv/kubikvkube/uafilm_me all-white-box → moved whole; splits:
  veoveo (2W), remux_full (1W), vibix (2W); fixture helpers copied where both
  halves need them (`vibixEncryptPayload`+`vibixDecoderKey`,
  `vdbmoviesTrashEncoded`, `videoseedEncode`).
- **Third pre-existing red test found: TestRemuxMovieRowsAndMovieEndpoint** —
  it swaps `http.DefaultTransport` to intercept cloud.mail.ru, but the remux
  checker uses `httpclient.NewForBalancer` → `SharedTransport` (own transport,
  own dialer) — the swap never intercepted; the test hits REAL cloud.mail.ru.
  Move-independent (client construction untouched); flagged as spawn-task.
  Pattern now clear: sources migrated to `httpclient.*` clients leave
  DefaultTransport-swapping tests silently broken.
After wave G: httpapi **360 files / ~109.9k LOC**; litesrc **69 files /
~46.1k LOC** (47 checkers).

**Wave H (done): the PAID-source block — filmix+filmixtv+fxapi, kinopub(+token),
zona(+browser), kinotochka.** Probing kinotochka pulled it INTO the wave (its
tail = buildFilmixStories from filmix.go + 2 pure hls_quality helpers).
Seam additions: `rchWithRequest`/`rchFetchCtx`/`rchReqCtxKey` copied into
litesrc deps.go (composed on the already-injected newRchClient — the earlier
"covered" list had claimed rchFetchCtx prematurely; verify the package
actually HAS a symbol before listing it covered), `evaluateSearchResult`+
`checksearchQualityRe`, `bestQualityLabel`/`maxQualityFromMap` copies.
Export-up: `KinopubAPIHost`/`KinopubRequestDeviceCode`/
`KinopubExchangeDeviceToken` (kit_bind.go qualifies — the zetflix-obrut
pattern again), and `ZonaSubBalancers` promoted from an anonymous-struct var
to the NAMED exported type `ZonaSubBalancer{Key,Display,Extractor,Supported}`
(lite_events iterates it cross-package — anonymous structs with unexported
fields cannot cross; naming+exporting the type is the fix). Drawer copy:
`filmixCDNUserAgent` (capi.go). zona routes Chrome via `mirageSem()`.
GOTCHA: the blanket `sub.key→sub.Key` sed hit lite_events' OTHER
sub-balancer loop (vokinoSubBalancers, same receiver name `sub`) — scope
field-rename seds to the exact range/function, never file-wide.
Tests: kinopub_token/kinopub_watching/zona/zona_live moved whole (all
white-box); filmix_test split (1W), kinotochka_test split (2W).
After wave H: httpapi **348 files / ~102k LOC**; litesrc **83 files /
~54.2k LOC** (53 checkers).

**Wave I (done): anime cluster + ENG family + the CDN trio — 16 source files
in one wave** (anilibria, aniliberty, animebesst, animedia, animevost+index,
animego, animelib+index, eng_sources+eng_browser+engbase, videocdn, fancdn,
flixcdn+browser). The seam is fully mature: the combined tails added ZERO new
Deps fields and zero new copies — only in-package resolutions
(`anilibriaInt`, `vokinoIntFromMap` — whose httpapi drawer copy DIED with its
last consumers, the reverse lifecycle working as designed). Exports: 5
`*VideoHandler` satellites (lite_sources wires them), `VideoCDNChecker` +
`Client()/IframeHost()/Token()` getters + `NormalizeVideoCDNBase` — the 4th
xsearch checker-adapter unlock (kinobase→collaps→kodik→videocdn, all four
now wrap litesrc exports). Drawer copy: `truncate` (flixcdn_browser →
youtube/mirage). Tests divided CLEANLY for the first time — zero splits:
9 all-harness files stayed, 4 all-white-box files moved (fancdn's 16 W
tests incl. geo-block/transport-cache logic); flixcdn testdata fixtures
moved with them.
**Fourth pre-existing stale test found: TestAnilibria* ×3** — the checker
migrated to the v1 API (`/api/v1/app/search/releases`, v2 = 410 Gone) but
the fixtures still serve `/v2/searchTitles` → 404 → ~30s → rch:false.
Same class as rhsprem/moonanime/remux: source evolved, hermetic test
contract silently rotted. Flagged as spawn-task.
After wave I: httpapi **328 files / ~94.8k LOC** (UNDER 100k — from 555
files/~210k at the start of the effort); litesrc **103 files / ~61.5k LOC**
(~70 checkers). Source-front remaining in httpapi: alloha(+v2+browser),
mirage(+browser+ws+guard), youtube(+vot), getstv(+bind), aladdin,
iptvonline, kinopub…done — i.e. only the HARD mirage/browser-coupled tail +
the capi/lite_events/lite_sources core (LAST).

**Wave J (done): getstv(+bind) + iptvonline** — both fully seam-covered.
The one twist: getstv.go was the HOME of the litehtml forwarders created in
the leaf extraction — still used by youtube/alloha/pidtor/mirage — so the
forwarder block was re-homed to `httpapi/litehtml_forwarders.go` (stays,
dies when those files move) and STRIPPED from the moved getstv.go (litesrc
already had its own; redeclaration otherwise). getstv_bind_test split (2W).

**Wave K (done): the mirage family — the feared hard tail, 10 files in one
wave** (mirage + browser + facade + guard + ws, alloha + v2 + browser,
aladdin, browser_pool_settings). The "qualitatively different" fear mostly
dissolved — reverse coupling was TINY:
- Deps += `LiveConfig`/`ServerReady`/`CapiResolveRequest` (alloha was a
  config-drain-era file using httpapi's deps.go accessors directly; the capi
  flag is a trivial ctx-value reader → primitive injections, unwired = not
  ready / false).
- **The wave-G `MirageBrowserSem` Deps field DIED**: the real semaphore
  arrived in-package with mirage_browser.go, `mirageSem()` now returns it
  directly — injected deps retire when their owner moves in, same lifecycle
  as drawer copies.
- `browser_pool_settings.go` (mirage runtime knobs, already-exported names)
  moved whole; server.go / server_routes_admin.go / admin_stats.go qualify
  `litesrc.SetMirage*`/`MirageBrowserStats`/`GetMirageStreamCache*`; the two
  admin sites that read the INTERNAL atomic (`mirageStreamCacheTTLH.Load()`)
  got a new exported `GetMirageStreamCacheTTLHours()`.
- 5th and FINAL xsearch adapter unlock (mirage): exported `MirageChecker` +
  `Token()` + `SearchByTitle`; `admin_stats`'s `mirageBrowser.allocCtx != nil`
  → exported `MirageAllocatorActive()` (1-line interface-surgery).
- Exports for server.go routes: `NewAllohaPlayerAPIProxy`, `NewCDNStreamProxy`
  (+`GetAnyEdgeHash` already exported, now litesrc-qualified).
- `firstNonEmpty` copied (servers_api.go stays). Splits: mirage_test (1W),
  alloha_test (4W); facade+helpers tests moved whole.
**Fifth stale test found: TestMirageVideoEndpoints** — the checker's video
flow now does guard-token + player-HTML fetches BEFORE the browser replay;
the test mock serves only the /movies/{id} XHR → 404 → {}. Env-dependent
(only fails где Chrome есть). Flagged as spawn-task.
After waves J+K: httpapi **314 files / ~83.2k LOC**; litesrc **121 files /
~73.1k LOC**. The ONLINE-SOURCE FRONT IS ESSENTIALLY DONE — remaining
source-ish files in httpapi: youtube(+vot/mux subsystem), pidtor,
spider_local, plus the capi/lite_events/lite_sources/checksearch core and
xsearch (now fully unblocked). Every /lite/* checker except youtube lives
in litesrc.

**Wave L (done): xsearch (adapters + api) → litesrc.** The milestone the
5 checker-adapter unlocks were building toward: since all 5 adapters wrap
litesrc-exported types, xsearch had NO remaining httpapi coupling except
`loopbackHostPort` (copied) + `writeJSON` (local forwarder). Moving it IN
made the litesrc.-qualifiers on the 5 checker types redundant (same package
now). Exported `BuildXSearchAdapters`/`XSearchHandler`/`XSearchSourcesHandler`;
server_routes_features.go qualifies. No tests. Clean, zero surprises.

**Wave M (done): youtube (+vot +trailer +global checker) → litesrc — THE
FRONT IS FULLY CLOSED.** The 4137-line youtube.go probed at ZERO undefined
tail (a fully self-contained subsystem: yt-dlp subprocess + ffmpeg mux +
its own JSON/http). Coupling was only the global-checker seam:
- `globalYTChecker atomic.Pointer` + `Get/SetGlobalYTChecker` + `getYtdlpVersion`
  (+its version-cache vars) MOVED from admin_stats.go into youtube.go —
  youtube owns its own process singleton now.
- admin_stats' 12-line yt-dlp stats block (reading 4 unexported fields +
  MuxStats + version) → collapsed to a new exported `(*YoutubeChecker).StatsInfo()
  map[string]any` that internalises all of it; admin_stats just calls it.
- 11 handler methods used as method-VALUES from lite_sources
  (`ytChecker.handleFeed` etc — method values REQUIRE exported names across
  packages) → exported (handle→Handle, handleDashMPD→HandleDashMPD, …); all
  had ZERO internal callers so it was a pure def-rename + call-site update.
- Exported `YoutubeChecker`/`SharedYoutubeChecker`/`YtImgProxyHandler`/
  `YtVotHandler`/`TrailerHandler`; server.go/lite_sources qualify.
- **The `//go:embed votjs/vot.bundle.cjs` directory had to move WITH
  youtube_vot.go** — embed dirs are path-relative to the .go file; vet caught
  "no matching files" until `votjs/` was relocated. LESSON: grep moved files
  for `//go:embed` and relocate the embedded dirs.
- `flexInt` (kp_catalog.go, stays for capi) copied into litesrc; the moved
  dash test's unaliased `encoding/json` collided with litesrc's package-level
  `var json` → aliased to stdjson. sprint3's white-box youtube auth test
  moved to litesrc.
After wave M: httpapi **306 files / ~77.9k LOC** (from 555/~210k — down 63%);
litesrc **130 files / ~78.5k LOC, ~77 checkers** — litesrc now OUTWEIGHS the
residual httpapi. **EVERY /lite/* source checker + xsearch aggregator now
lives in internal/litesrc.** httpapi retains only: the capi/lite_events/
lite_sources/checksearch ROUTING+RESOLUTION core (the shared front that
dispatches to litesrc checkers), pidtor/spider_local, and all the non-source
subsystems. The Step-4 online-source-front extraction is COMPLETE; what
remains (capi/lite core) is routing/composition, a separate future effort.

**Post-front scoping (probed, recorded for the next effort):**
- **capi CORE (capi.go+capi_crypto, 37-symbol tail)** is the true hot-path —
  woven with lite_events resolution (`buildEventItems`, `resolveEventsPlugins`,
  `resolveKpID`, `liteSourceHandler`), the account store, DynamicRouteRegistry.
  NOT cleanly separable without moving lite_events too. `capi_crypto.go` is the
  HMAC/AES signed-session layer (security-sensitive) — the satellites
  (trakt/nowwatching/collections/profiles/reviews/watchparty) all hang off its
  `capiSignedGate`/`writeCapiEnc`/`liveConfig` seam (3–10 tail each). Extracting
  them needs a `capiauth` leaf first + careful verification + a live run.
  Deferred as the next planned effort.
- Clean non-source leaves available: kp_catalog (done, below), and by-probe
  alice(5)/bookmark(8)/nws(11)/myshows(4) — feature subsystems, bespoke each.

**Wave N (done): `internal/kpcatalog` (leaf KP client).** `kp_catalog.go`
(1053 LOC — the kinopoiskapiunofficial.tech catalog/search client) probed at a
2-symbol tail ({json, writeJSON}) — a genuinely reusable client, not a /lite/*
source, so it earned its own leaf (like litehtml/browsergate). Exported the
type `KPClient`+`NewKPClient` and the 6 cross-package methods
(`BuildCatalogDef`/`HandleList`/`HandleCard`/`SearchByKeyword`/`DoGet`/
`ResolveKinopoiskID`); consumers (catalog_api/alice/capi_reviews/kpid_resolver/
server) qualify. `kpClientSingleton` global stays in catalog_api.go, retyped
`*kpcatalog.KPClient`. flexInt moved with the file (litesrc keeps its wave-M
copy). LESSON reaffirmed: grep ALL consumers before assuming the exported-method
set is complete — kpid_resolver's `resolveKinopoiskID` call surfaced only at
build after the first 5-method export. httpapi: **304 files / ~76.8k LOC**.

**Wave O (done): `internal/alicevoice` (Yandex Alice voice assistant).** A
complete self-contained FEATURE (device pairing store, PIN store,
/api/alice/webhook, admin management) — 1112 LOC, probed at a 5-symbol tail
{json, writeJSON, kpClientSingleton, nwsHub, tgAdminAuthCheck}. Three seam
patterns combined:
- **interface-surgery**: alice calls exactly ONE nwsHub method
  (`SendEventToUIDs`) → a 1-method `NwsSender` interface; the `nws *nwsHub`
  params become `nws NwsSender`, httpapi's `*nwsHub` satisfies it (no nws
  extraction needed).
- **mirror-struct + high-level inject**: kp search returns kpcatalog's
  unexported `*kpSearchResponse`; instead of widening kpcatalog's surface,
  injected `Deps.KPSearch func(query,page)([]AliceKPFilm,error)` where
  `AliceKPFilm` is an alicevoice-owned mirror; the host closure
  (`aliceKPSearch` in server_routes_userauth) does the search + field-maps
  kpFilm→AliceKPFilm. alicevoice imports NOTHING from kpcatalog.
- **admin-auth inject** (skip/calendar pattern): `Deps.AdminAuthCheck`,
  fail-closes 403 unwired.
No import cycle risk: tgauth's `SetAliceStores` already takes INTERFACES
(`AlicePINHandler`/`AlicePairHandler`), so the moved stores satisfy them
unchanged. Exported the 6 reverse-coupled symbols
(`AlicePairingStore`/`NewAlicePairingStore`/`NewAlicePINStore`/
`AliceWebhookHandler`/`TgAdminAliceHandler`/`TgAdminAliceDeleteHandler`);
`registerAliceRoutes` (stays in httpapi) qualifies + calls SetDeps once;
admin `deps.alicePairs` field retyped `*alicevoice.AlicePairingStore`. No tests
existed. httpapi: **303 files / ~75.7k LOC** (from 555/~210k — down 64%).

**STATE OF THE EXTRACTION (end of this session):** the high-value, clean,
safe extractions are EXHAUSTED. Probed the remainder:
- **Storage/sync cluster** (timecode/bookmark/storage/media_api) — NOT clean:
  bidirectionally tangled with sync_bridge.go + migrate_api.go (the data-access
  fns loadTimecodeUser/saveBookmarkUser + their mutexes are SHARED between the
  HTTP handlers AND the sync/migrate layer). Needs the whole persistence layer
  refactored together; sync_bridge pulls in capistore/capi. Deferred tier.
- **capi core + satellites** — hot-path + HMAC/AES security layer; needs a
  capiauth leaf + live testing. Deferred tier.
- Remaining clean-ish singles are marginal (yandex_metrika 0-tail,
  error_doc 1-tail) — package sprawl, low value; or bespoke medium
  (nws 11-tail — the WS hub, intertwined with rch_client).
**Wave P (done): `internal/capiauth` (signed-session security leaf).** The
prerequisite the capi effort named — extracted safely WITHOUT touching the
resolution core. `capi_crypto.go` (per-session AES-GCM keys, HMAC request-sig +
nonce/replay, rate limiter) probed at a 4-symbol tail {capiGate, json,
liveConfig, writeJSON}. The auth POLICY gate (`capiGate` — enable/app-key/
auth/premium, with its `capiPremiumActive`→`tgTokenStoreRef` chain) STAYS in
capi.go and is INJECTED (`Deps.Gate`); only the crypto/session machinery moved.
Exported `SignedGate`/`WriteEnc`/`Session`/`SessionHandler`; the 7 consumers
(capi.go + 5 satellites + watchparty_rooms) qualify. SetDeps in
`registerCapiRoutes`. **Verification: the 4 crypto tests moved with the file
(encrypt round-trip, sig verify, signed-enc flow, plain-session flow) + a
`TestMain` wiring a faithful `capiGate` double** — all pass, so the relocation
is behavior-preserving on the security path. This is a pure relocation (zero
logic change), the safe way to touch the security layer.

**Wave Q (done): `internal/watchparty` (virtual-cinema feature).** capiauth
unblocked it: `watchparty_api` (the /webplayer WS relay hub — wpHub/wpConn/
wpRoom) was already 0-tail; `watchparty_rooms` (the /capi/room/* metadata
endpoints) needed only capiauth (now a leaf) + `liveConfig` (inject) + a
`capiInt` copy. Zero syncBridge/trakt/profiles coupling — a genuinely clean
cluster. Exported `WatchpartyHandler`/`WatchpartyHealthHandler`/
`RegisterRoomRoutes`; server_routes_plugins + capi.go qualify. httpapi:
**298 files / ~74.5k LOC** (under 300 files; from 555/~210k — down 65%).

**Wave R (done): `internal/capihttp` (the capi FEATURE endpoints).** With
capiauth already a leaf and `capiAccountStore` relocated to capi_state.go,
{capi_trakt + capi_nowwatching + capi_profiles} (~916 LOC + 5 white-box tests)
moved cleanly. ALL coupling was via EXTERNAL types (capistore.Store/HistoryItem/
Ref, trakt.Store/Client) so it is mechanically injectable — the "tangled core"
fear was about the RESOLUTION path, not these account/profile endpoints. Deps:
`AccountStore func() *capistore.Store` (getter over the relocated global),
`LiveConfig`, and 4 sync-bridge func values (`SyncEnsureBackfill`/
`SyncHistoryChanged`/`SyncFavoriteChanged`/`SyncBookmarkChanged`) — the host
closures nil-guard `syncBridgeRef` internally, so the moved code calls them
unconditionally. `capiAccountStore` reads → `deps.AccountStore()`;
`syncBridgeRef.X` guarded blocks → single forwarder calls. Exported the 5
reverse-coupled symbols (`EnsureTraktStore`/`RegisterTraktRoutes`/
`RegisterProfileRoutes`/`CapiNowWatchingHandler`/`CapiProfilesPayload`);
capi.go's `registerCapiRoutes` calls `capihttp.SetDeps` + qualifies; profiles_api.go
qualifies `CapiProfilesPayload`; `capiInt` copied to capi_state.go (capi.go uses
it). **The one test snag**: `profiles_api_test.go` sets the httpapi
`capiAccountStore` global directly but calls the façade that now reads through
capihttp's injected getter — fixed by adding `capihttp.SetDeps` to
`profilesTestSetup`. **VALIDATION: build/vet/gofmt + 5 capihttp white-box tests
+ httpapi capi/profiles tests + cross torrs/win/arm + a LIVE BOOT** — the
composition-root change (capi.go now builds the sync closures + registers via
capihttp) boots clean, no panic; `/capi/nowwatching` + `/capi/profiles` respond
at the gate (404 = capi disabled in the test config, i.e. routes ARE wired).
httpapi: **294 files / ~73.7k LOC**. capi.go now holds only the RESOLUTION core
(buildEventItems/resolveEventsPlugins/liteSourceHandler) + the account-store
singleton — the account/profile/trakt/nowwatching feature surface is out.

**Waves S + T (done): the REST of the capi feature surface → capihttp.**
- **S — `capi_collections`**: joined capihttp. Its one non-trivial coupling was
  `syncBridgeRef.meta(typ,tmdbID)` reading `.Title`/`.PosterPath` (internal
  `*syncMeta`) — field-flattened to `Deps.SyncMeta func(typ,tmdbID)(title,poster
  string,ok bool)` (host closure extracts the 2 fields). `posterURL` copied.
- **T — the reviews CLUSTER `{capi_reviews + myshows + irecommend}`**: the
  combined probe was CLEAN (nothing new beyond infra; capi_reviews DEFINES the
  shared `capiReview`/`capiDateOnly` used by myshows/irecommend, so all three
  moved together and those became in-package). One inject: `Deps.KPClient
  func() *kpcatalog.KPClient` (reviews calls `.DoGet`). `firstNonEmpty` copied.
  6 files (3 src + 3 white-box tests) moved. Export `RegisterReviewRoutes`.
Both validated with a LIVE BOOT (capi registration clean, no panic; all capihttp
routes respond at the gate). httpapi: **286 files / ~72.3k LOC**; capihttp:
**14 files / ~2.3k LOC** (trakt, nowwatching, profiles, collections, reviews +
myshows/irecommend sources).

**MILESTONE — the entire /capi FEATURE surface is extracted.** `capi.go` (1885
LOC) now holds ONLY the RESOLUTION CORE: `/capi/streams`, `/capi/quality`,
`/capi/litestreams`, `/capi/transcode-url` (buildEventItems / resolveEventsPlugins
/ liteSourceHandler dispatching to litesrc checkers) + `/capi/me` + the
composition root that wires capiauth/capihttp. Its ~13 `capi_*_test.go` files
test that resolution logic (anime/cache/classify/deviceprofile/live/multiurl/
similar/techflags/verifyip). This IS the hot serving path — extracting it
further (splitting resolution from the router) buys little and would need a
network-connected, capi-enabled run to validate real stream resolution. The
capi decomposition is COMPLETE at the feature/core boundary.

**Wave U (done): `internal/userdata` — the persistence layer (the "deferred
tangle", now the session's biggest extraction).** {sync_bridge + migrate_api +
timecode_api + bookmark_api + storage_api + media_api} (6 src + 6 tests, ~2.5k
LOC). The earlier "bidirectionally tangled — can't extract" verdict was WRONG
about the boundary: the tangle (loadTimecodeUser/saveBookmarkUser + their
mutexes shared between HTTP handlers AND sync_bridge/migrate) DISSOLVES when the
whole layer moves together — the shared data-access becomes in-package. Probed
as a unit, the external tail was ONLY infra + nwsBroadcast + requestProfileID.
Seam: inject `ClientIP`/`ReadFileAny`/`NwsBroadcast` (the 3 with dep-chains);
copy the pure helpers (relToRuntime/cookieValue/writeRawJSON/fileExists/
requestProfileID/hostFromRequest/firstNonEmpty/writeJSON/json). Exported 15
handlers + `SyncBridge` type + `NewSyncBridge` + `BookmarkUserID` + a
`LookupMeta`/`MetaEnrich` mirror-struct method (capi.go resolution + collections
read 5-6 syncMeta fields → flattened to an exported struct, keeping the internal
`*syncMeta` with its mutexes/hash-maps unexported). `syncBridgeRef` global STAYS
in httpapi (retyped `*userdata.SyncBridge`, in capi_state.go — cross-package
state like capiAccountStore). Drawer copy-back: `toString`/`parseBoolLike`/
`chiURLParam` (used by ~10 staying files). `TestLampaBucket` moved to userdata;
a no-op `resetHTTPAPIGlobals` shim added there. **Two unwired-default bugs
caught by the moved tests (the sisi lesson, twice)**: `ClientIP` default must
return the RemoteAddr peer IP (not "" — else BookmarkUserID drops the IP-user
fallback → 401); `ReadFileAny` default must do `os.ReadFile(relToRuntime(rel))`
(not nil/false — else the media handler can't read its init.conf auth token →
401). **VALIDATION: all 6 userdata tests + httpapi tests + cross + LIVE BOOT
(`/timecode/all` + `/bookmark/list` → 200 with real data; sync-bridge wiring
boots clean, no panic).** httpapi: **275 files / ~68k LOC** (from 555/~210k —
down 68% LOC).

**Post-R analysis (pre-S/T, kept for reference):**
- `capi_trakt`, `capi_nowwatching` — 0-tail individually BUT mutually coupled
  with `capi_profiles` (profiles calls traktClientFor/traktSyncFinish/
  nowWatching), so they only move as a bundle WITH profiles.
- **`capi_profiles` is a CORE-STATE HOLDER**: it DEFINES `capiAccountStore`
  (the account-store global used by capi.go/server.go/profiles_api.go) — a
  misplaced core singleton, like the admin PIN drawers. Moving profiles drags
  the account store out from under the resolution core. Plus profiles calls
  `syncBridgeRef.{EnsureBackfill,CapiHistoryChanged,CapiFavoriteChanged,
  CapiBookmarkChanged}` (the persistence tangle).
- `capi_reviews` — coupled to myshows (myShowsLookup/MovieExtra) + kpcatalog +
  irecommend.
So the capi satellites are inseparable from {account store + resolution core +
sync persistence}. That whole unit — capi.go resolution (buildEventItems/
resolveEventsPlugins/liteSourceHandler) + capiAccountStore + sync_bridge +
migrate + the timecode/bookmark/storage data layer — is ONE tangled core that
must be refactored together, with a LIVE RUN. That is the (single) remaining
effort, and it is not autonomous-session continuation work.

**Packages extracted this session (15 waves + 2 leaves + this):** litehtml,
browsergate, litesrc (~77 checkers), kpcatalog, alicevoice, capiauth,
watchparty. httpapi 555/~210k → **298/~74.5k (−46% files-in-httpapi-terms is
misleading; true monolith shrink is −65% LOC)**.

**LIVE VALIDATION (done — the run the handoff kept asking for).** Built
`./cmd/lampac-go` (clean, 101 MB) and ran it against the real repo root
(`LAMPAC_GO_LOCAL_CORE=true LAMPAC_GO_REPO_ROOT=/Users/kirillz/lampac`,
listens `:888`; HTTP bind is ~85 s in because mirage-guard does blocking
sarn fetches at boot — probe AFTER "lampac-go started"). Server boots
through ALL extracted-source init (redheadsound/mirkino/pidorezka-browser/
turbo/mirage-guard) with zero panic/undefined. Smoke test on :888:
`/lite/rezka?checksearch` → 200, `/lite/kinopub` → 200, `/lite/youtube` →
200, `/webplayer/health/watchparty` → `{"ok":true,rooms:0,peers:0}` (the
extracted watchparty pkg serves), `/lite/events` → the full balancer list
dispatching to litesrc checkers with 「4K」 badges (the lite_events routing
core → litesrc dispatch works end-to-end), `POST /capi/session` → 404
(capiauth's INJECTED gate correctly `http.NotFound`s when capi is disabled —
the seam is wired right). No 500s/panics. The 17-wave refactor is
LIVE-VALIDATED for boot + routing + the extracted-package surfaces. (Still
NOT a full prod deploy — real upstream sources, kinopub failover with a live
token, and browser-source resolution need a network-connected run.) Source-front remaining: filmix(+tv/fxapi),
kinopub(+token), zona(+browser), anime cluster (anilibria/aniliberty/
animebesst/animedia/animevost/animego/animelib + *_index satellites),
eng_sources family(+engbase+browser), videocdn, sakhtv…done, fancdn(+register),
flixcdn, iframevideo…done, gencit…done, klonfun…done, plus hard tail:
alloha(+v2+browser, mirage-coupled), mirage(+browser+ws+guard), youtube
(+vot/sidecar), iptvonline (kit-bound), getstv (bind), aladdin, starlight…done,
capi/lite_events/lite_sources core LAST. Remaining source-front: vokino, veoveo, sakhtv,
turbo, hdvb, filmix(+tv/fxapi), kinopub, kodik, anime cluster (anilibria/
aniliberty/animebesst/animedia/animevost/animego/animelib), rutubemovie,
vkmovie, plvideo, vdbmovies, cdnvideohub, kubikvkube, zona, remux, mirkino,
videoseed, iframevideo…done, eng_sources family, uafilm, starlight…done — plus
the hard tail (vibix/alloha/mirage/youtube/turbo-browser, capi/lite_events
core last).

**Coupling survey for the remaining clusters** (done):
- **`transcode/`** (~27 files, ~17k LOC) — fully scoped and READY: inbound coupling
  is tiny (`writeJSON` ×48 → forwarder, `liveConfig` ×2, `requestScheme` ×1); the
  consumer-facing fields/methods are already exported; only 4 symbols need
  exporting (`reqHLSOpts→HLSOpts`, `snapshotTranscodingJobs`, `killTranscodingJobByID`,
  `registerTranscodingRoutes→RegisterRoutes`). Large but low-risk — its own focused
  PR (a 27-file atomic move).
- `opensubs` — clean handler cluster BUT shares subtitle-conversion utils
  (`convertSubtitlesToVTT`, `buildFragmentedVTTSegments`) with the transcode domain;
  extract those to a shared `internal/subvtt` first.
- Next by size/cleanliness after audiobot: `opensubs` (+subvtt), `calendar`,
  `skipintro`, then the bigger `iptv`/`dlna`/`sisi`/`admin` clusters.

Ordering principle: smallest-coupling first (audiobot proved it), shared utils
extracted to their own `internal/` package before the clusters that need them.

**Wave V (done): `internal/feedback` (user feedback / support tickets).** The
FeedbackStore + public /api/feedback[/my|/{id}] submission endpoints. Clean:
forward tail = writeJSON/json (forwarder) + relToRuntime (copy) +
userdata.BookmarkUserID (external import); the DTOs/store were already exported
(the adminhttp feedback extraction had exported them). Exported the 3 handlers +
ValidFBStatuses/ValidFBPriorities; server_routes_admin.go qualifies feedback.*
(its registerAdminRoutes returns *feedback.FeedbackStore + builds the adminhttp
FeedbackOps closures over it). No cycle (tgauth.SetFeedbackStore takes the
FeedbackSubmitter interface). Boot-clean. httpapi: **274 files / ~67.4k LOC**.

## DECOMPOSITION COMPLETE (this effort) — the cohesive/separable extraction is done

httpapi went from **555 files / ~210k LOC (one god-package)** to **274 files /
~67.4k LOC (−68% LOC)** — now the thin HTTP composition/routing layer the plan
above always targeted. **10 sibling packages carry what was cohesive & separable:**
- `internal/litesrc` (~77 online-source checkers + xsearch) — the whole /lite front
- `internal/litehtml`, `internal/browsergate` — shared source leaves
- `internal/kpcatalog` (KP client), `internal/capiauth` (signed-session crypto)
- `internal/alicevoice` (Yandex Alice), `internal/watchparty` (virtual cinema)
- `internal/capihttp` (capi trakt/nowwatching/profiles/collections/reviews+sources)
- `internal/userdata` (timecode/bookmark/storage/media + sync bridge + migrate)
- `internal/feedback` (support tickets)
Every extraction verified: build/vet/gofmt + package tests + cross (torrs/win/arm)
+ **live boot smoke tests** (sources serve, lite_events dispatches to litesrc,
userdata handlers return real data, capi/feedback routes wire clean).

**What REMAINS in httpapi is the HTTP SKELETON — it belongs here, by design:**
- **Composition root**: server.go + server_routes_*.go (wires every package).
- **The /lite + /capi ROUTING/RESOLUTION core**: lite_events / lite_sources /
  capi.go (`buildEventItems`/`resolveEventsPlugins`/`liteSourceHandler` dispatch
  to litesrc). Hot serving path; splitting router-from-resolver buys little and
  needs a network-connected, capi-enabled run to validate real stream resolution.
- **Request-path infra**: waf.go (consulted via check/allow/add on ~15 files
  every request), nws.go (WS hub + rch transport).
- **Auth/UI infra**: auth_tg_api, kit_* (premium binding), plugins, web_installer.
- **Shared-helper drawers**: admin_init/admin_panel_helpers/admin_panel_auth
  (writeHTML/readFileAny/relToRuntime/PIN gate — used by ~70 files); relocating
  them to a leaf would only de-dup the tiny copies, not extract a feature.
Further "extraction" from here means carving up the HTTP layer's own skeleton —
lower value, higher risk, and it is what an HTTP composition layer should contain.
The monolith is decomposed.

## Guardrail
`make monolith-stats` reports file count / LOC / globals so the debt stays
visible and doesn't silently grow. Prefer adding new features as their own
`internal/<feature>` package + a thin handler, not as more flat files here.

**Layering guard (`internal/archguard`).** `TestExtractedPackagesDoNotImportHttpapi`
parses every extracted sibling package's imports and FAILS if any imports
`lampac-go/internal/httpapi`. Go's compiler only catches import *cycles*; a
one-directional wrong-way import (an extracted pkg → httpapi) compiles fine and
silently re-couples the monolith. This test is the backstop — it runs in
`go test ./...`. When you add a new extraction, append its dir name to
`extractedPkgs`. If it fires, invert the dependency (inject the needed value via
a `Deps` field), don't import back.
