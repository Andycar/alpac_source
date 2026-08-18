/**
 * CorePlayback — the load/play/pause/seek heart of the player.
 *
 * Owns the shaka.Player instance and orchestrates BufferStrategy,
 * TrackManager, StatsCollector, MediaFeatures, TranscodingOrchestrator.
 *
 * Adds attach()/detach() methods so PreloadEngine (Wave 1 B1) can swap
 * the active video element seamlessly.
 *
 * Public API mirrors the old PlaybackEngine (lines 79-901). The thin
 * PlaybackEngine.ts facade re-exposes everything with identical signatures
 * so Player.svelte and bindEngineToStores() don't change.
 */

import { BufferStrategy } from './BufferStrategy';
import { TrackManager } from './TrackManager';
import { StatsCollector } from './StatsCollector';
import { EventBus } from './EventBus';
import { MediaFeatures } from '../features/MediaFeatures';
import { TranscodingOrchestrator } from '../features/TranscodingOrchestrator';
import { TrickplayManifest } from '../features/TrickplayManifest';
import { AssRenderer } from '../features/AssRenderer';
import { ChapterParser } from '../features/ChapterParser';
import { CMCDReporter } from '../features/CMCDReporter';
import { NetworkProfileMonitor } from '../features/NetworkProfile';
import { DRMDiagnostics } from '../features/DRMDiagnostics';
import { WebCodecsBridge } from '../codec/WebCodecsBridge';
import { isUnsupportedCodecError, whatBrowserCannotPlay } from '../codec/WasmCodecLoader';
import { buildDRMConfig } from '../DRMConfig';
import { thumbnailManifest } from '../../stores/thumbnails';
import { chapterMarkers, activeChapter } from '../../stores/chapters';
import { networkProfile } from '../../stores/network';
import { get } from 'svelte/store';
import type { PlayElement, EngineEvents, Chapter, NetworkProfile } from './types';

declare const shaka: any;

export class CorePlayback {
  readonly bus = new EventBus<EngineEvents>();
  readonly transcoding = new TranscodingOrchestrator();

  private _player: any | null = null;
  private _videoEl: HTMLVideoElement;
  private _media: MediaFeatures;
  private _buffer!: BufferStrategy;
  private _tracks!: TrackManager;
  private _stats!: StatsCollector;

  private currentElement: PlayElement | null = null;
  private destroyed = false;
  private fwdCleanups: Array<() => void> = [];
  private currentChapters: Chapter[] = [];
  private lastActiveChapterStart = -1;
  private cmcd: CMCDReporter | null = null;
  private network: NetworkProfileMonitor | null = null;
  private drmDiag: DRMDiagnostics | null = null;
  private webCodecs: WebCodecsBridge | null = null;
  private lowPowerTV = false;
  // On-device ASS rendering (JASSUB) — populated per-load from the
  // transcoding subtitles list; keyed by track label.
  private assRenderer: AssRenderer | null = null;
  private assTracks = new Map<string, { assUrl: string; fontsUrl?: string }>();
  private assFontsCache: string[] | null = null;

  constructor(videoEl: HTMLVideoElement) {
    this._videoEl = videoEl;
    this._media = new MediaFeatures(videoEl);
    shaka.polyfill.installAll();
  }

  // --- subsystems (read-only access for facade & features) ---
  get tracks(): TrackManager { return this._tracks; }
  get stats(): StatsCollector { return this._stats; }
  get buffer(): BufferStrategy { return this._buffer; }
  get media(): MediaFeatures { return this._media; }
  get player(): any { return this._player; }
  get video(): HTMLVideoElement { return this._videoEl; }
  get currentEl(): PlayElement | null { return this.currentElement; }

  // --- lifecycle ---

  async init(opts?: { lowPowerTV?: boolean }): Promise<void> {
    this._player = new shaka.Player();
    await this._player.attach(this._videoEl);

    this.lowPowerTV = !!opts?.lowPowerTV;
    this._buffer = new BufferStrategy(this._player);
    this._tracks = new TrackManager(this._player, this._buffer);
    this._stats = new StatsCollector(this._player, this._videoEl);

    // Default profile — switched per-content in load() based on type.
    // Low-power TVs get the leaner profile right away.
    this._buffer.apply(this.lowPowerTV ? 'tv-low' : 'vod-hd');

    // CMCD is on for every playback. The old worry — that the extra
    // CMCD-* headers reach a strict CDN and earn a 403 — no longer applies:
    // the server strips them before the upstream fetch and keeps the data
    // for itself (internal/proxyapi/handler.go, internal/cmcd). What it buys
    // is the only view we have of how playback actually feels per source:
    // rebuffers, buffer level and measured throughput, aggregated per
    // balancer. Direct-to-CDN playback (no_stream_proxy) simply reports
    // nothing — nobody is there to read it.
    this.cmcd = new CMCDReporter(this._player, this.bus);
    this.cmcd.enable();

    // Wave 2 C1 — start network/battery monitor.
    this.network = new NetworkProfileMonitor();
    this.network.start();

    // Wave 2 C4 — DRM diagnostics observer (always-on, cheap).
    this.drmDiag = new DRMDiagnostics(this._player);
    this.drmDiag.attach();

    // Wave 2 C3 — frame-precise utilities. Cheap (only RVFC tracking).
    this.webCodecs = new WebCodecsBridge(this._videoEl);
    if (WebCodecsBridge.hasRVFC) this.webCodecs.startTracking();

    // Forward shaka events
    this._player.addEventListener('error', (e: any) => {
      this.bus.emit('error', { code: e?.detail?.code, severity: e?.detail?.severity, data: e?.detail });
    });
    this._player.addEventListener('buffering', (e: any) => {
      this.bus.emit('buffering', !!e.buffering);
    });
    this._player.addEventListener('adaptation', () => {
      const active = this._tracks.quality().find((t) => t.active);
      this.bus.emit('qualitychanged', { height: active?.height, bandwidth: active?.bandwidth });
    });
    this._player.addEventListener('trackschanged', () => {
      this.bus.emit('trackschanged', undefined);
    });

    this.attachVideoEvents();
  }

  /** Re-attach shaka to a new <video> element (used by PreloadEngine swap). */
  async attach(videoEl: HTMLVideoElement): Promise<void> {
    this.detachVideoEvents();
    this._videoEl = videoEl;
    this._media.destroy();
    this._media = new MediaFeatures(videoEl);
    if (this._player) {
      try { await this._player.attach(videoEl); } catch (err) {
        console.warn('[LWP] CorePlayback.attach failed:', err);
      }
      this._stats = new StatsCollector(this._player, videoEl);
    }
    this.attachVideoEvents();
  }

  async detach(): Promise<void> {
    this.detachVideoEvents();
    if (this._player) {
      try { await this._player.detach(); } catch { /* ignore */ }
    }
  }

  private attachVideoEvents(): void {
    const fwd = (evt: string, engineEvt: keyof EngineEvents) => {
      const handler = () => {
        if (this.destroyed) return;
        if (engineEvt === 'timeupdate') {
          this.bus.emit('timeupdate', {
            currentTime: this._videoEl.currentTime,
            duration: this._videoEl.duration || 0,
          });
        } else {
          this.bus.emit(engineEvt as any, undefined as any);
        }
      };
      this._videoEl.addEventListener(evt, handler);
      this.fwdCleanups.push(() => this._videoEl.removeEventListener(evt, handler));
    };
    fwd('playing', 'playing');
    fwd('pause', 'paused');
    fwd('seeking', 'seeking');
    fwd('seeked', 'seeked');
    fwd('timeupdate', 'timeupdate');
    fwd('ended', 'ended');
  }

  private detachVideoEvents(): void {
    for (const cleanup of this.fwdCleanups) cleanup();
    this.fwdCleanups = [];
  }

  // --- load / play / pause / seek ---

  async load(element: PlayElement): Promise<void> {
    if (!this._player) throw new Error('CorePlayback not initialized');
    this.currentElement = element;
    this._tracks.setCurrentElement(element);

    // Fresh start: stop any previous transcoding heartbeat
    this.transcoding.stopHeartbeat();

    let url = element.url;
    const isHLS = /\.m3u8(\?|$)/i.test(url);
    const isDASH = /\.mpd(\?|$)/i.test(url);

    // Apply DRM config if provided.
    // Wave 2 C4: also wire optional license headers via NetworkingEngine
    // request filter (since shaka doesn't accept headers in drm config).
    if (element.drm) {
      try {
        const drmConfig = buildDRMConfig(element.drm);
        const headers = drmConfig.drm?.__licenseHeaders;
        if (headers) {
          delete drmConfig.drm.__licenseHeaders;
          try {
            const NE = (globalThis as any).shaka?.net?.NetworkingEngine?.RequestType;
            const LICENSE = NE?.LICENSE ?? 2;
            const ne = this._player.getNetworkingEngine?.();
            ne?.registerRequestFilter((type: number, request: any) => {
              if (type === LICENSE) {
                request.headers = { ...request.headers, ...headers };
              }
            });
          } catch (err) {
            console.warn('[LWP] license header filter setup failed:', err);
          }
        }
        this._player.configure(drmConfig);
      } catch (err) {
        console.warn('[LWP] DRM configure failed:', err);
      }
    }

    // Direct URL → check transcoding need
    if (!isHLS && !isDASH) {
      const transcoded = await this.transcoding.maybeStart(url);
      if (transcoded) {
        url = transcoded.playlistUrl;
        if (transcoded.subtitlesUrl) {
          // The subtitlesUrl is a JSON LIST endpoint, not a subtitle file —
          // expand it properly (adds each .vtt track + wires on-device ASS
          // rendering for entries that carry an assUrl).
          void this.ingestTranscodingSubs(transcoded.subtitlesUrl);
        }
      }
    }

    // Try shaka first (handles HLS, DASH, MP4 — sometimes URL extension lies)
    let codecGapHint: string | null = null;
    try {
      try {
        const me = this._player.getMediaElement();
        if (!me) await this._player.attach(this._videoEl);
      } catch {
        await this._player.attach(this._videoEl);
      }
      await this._player.load(url);
      console.log('[LWP] Loaded via shaka:', url.substring(0, 80));
    } catch (shakaErr: any) {
      console.warn('[LWP] shaka.load failed, trying native:', shakaErr?.message || shakaErr);
      // If shaka explicitly says the codec is unsupported, capture a hint
      // so we can warn the user when the native fallback also fails.
      if (isUnsupportedCodecError(shakaErr)) {
        const gap = whatBrowserCannotPlay();
        if (gap.av1) codecGapHint = 'AV1';
        else if (gap.hevc) codecGapHint = 'HEVC';
        else codecGapHint = 'неизвестный кодек';
      }
      try { await this._player.detach(); } catch { /* ignore */ }
      this._videoEl.src = url;
      this._videoEl.load();
      console.log('[LWP] Loaded via native:', url.substring(0, 80));

      // If we suspected a codec gap and native is going to fail too,
      // surface it via the bus so UI can toast a useful message instead
      // of leaving the user staring at a black frame.
      if (codecGapHint) {
        const onErr = () => {
          this.bus.emit('error', {
            code: 'CODEC_UNSUPPORTED',
            severity: 2,
            data: { codec: codecGapHint, hint: 'wasm-fallback-not-wired' },
          });
          this._videoEl.removeEventListener('error', onErr);
        };
        this._videoEl.addEventListener('error', onErr);
      }
    }

    // External subtitles
    if (element.subtitles?.length) {
      for (const sub of element.subtitles) {
        await this._tracks.addExternalSubtitle(
          sub.url,
          sub.language || 'und',
          sub.label || sub.language || 'Subtitles',
        );
      }
    }

    // Adapt buffer profile to content. The 'auto' picker considers
    // isLive, active variant height, low-power TV flag, and the latest
    // network profile (effectiveType, saveData, battery level).
    try {
      const isLive = !!this._player.isLive?.();
      const active = this._tracks.quality().find((t) => t.active);
      const net: NetworkProfile = get(networkProfile);
      this._buffer.apply('auto', {
        isLive,
        activeHeight: active?.height,
        isLowPowerTV: this.lowPowerTV,
        network: net,
      });
    } catch { /* non-critical */ }

    // Update CMCD content ID only if CMCD is actually enabled. Constructing
    // the reporter is cheap but we don't want to call enable() implicitly.
    // Callers who opted in via settings flip it on separately.
    try { this.cmcd?.setContent(url); } catch { /* ignore */ }

    // Reset previous trickplay/chapter/ASS state (per-load)
    this.currentChapters = [];
    this.lastActiveChapterStart = -1;
    thumbnailManifest.set(null);
    chapterMarkers.set([]);
    activeChapter.set(null);
    this.resetAssState();

    // Discover thumbnail manifest + chapters in the background — never
    // blocks playback. These calls fail silently when the source has none.
    void this.discoverTrickplay(url);
    void this.discoverChapters(url);

    // Resume position (Lampa.Timeline shape)
    const resumeTime = typeof element.timeline === 'number'
      ? element.timeline
      : (element.timeline?.time ?? 0);
    if (resumeTime > 0) {
      this._videoEl.currentTime = resumeTime;
    }

    this.bus.emit('loaded', undefined);
  }

  /**
   * Load a pre-prepared transcoded batch episode playlist URL directly.
   * Used by PlaybackEngine.loadBatchEpisode (kept for back-compat).
   */
  async loadBatchUrl(playlistUrl: string, subtitlesUrl?: string): Promise<void> {
    if (!this._player) throw new Error('CorePlayback not initialized');
    this.transcoding.beginBatchEpisode();

    try {
      const me = this._player.getMediaElement();
      if (!me) await this._player.attach(this._videoEl);
    } catch {
      await this._player.attach(this._videoEl);
    }
    await this._player.load(playlistUrl);

    this.resetAssState();
    if (subtitlesUrl) {
      await this.ingestTranscodingSubs(subtitlesUrl);
    }

    this.bus.emit('loaded', undefined);
  }

  /**
   * Expand the /transcoding/{id}/subtitles JSON list: each entry's WebVTT
   * twin becomes a Shaka text track (styling-stripped fallback), and entries
   * carrying an `assUrl` (ASS/SSA source tracks — see lampac-go
   * transcoding_fonts.go) are remembered so selecting that track swaps
   * Shaka's text display for a JASSUB overlay with the embedded fonts.
   */
  private async ingestTranscodingSubs(subtitlesUrl: string): Promise<void> {
    try {
      const resp = await fetch(subtitlesUrl);
      if (!resp.ok) return;
      const subs: Array<{
        label: string; url: string; codec?: string; assUrl?: string; fontsUrl?: string;
      }> = await resp.json();
      for (const sub of subs) {
        const label = sub.label || 'Subtitles';
        await this._tracks.addExternalSubtitle(sub.url, 'und', label);
        if (sub.assUrl && AssRenderer.isSupported()) {
          this.assTracks.set(label, { assUrl: sub.assUrl, fontsUrl: sub.fontsUrl });
        }
      }
      if (this.assTracks.size > 0) {
        this._tracks.onTextSelected = (label) => { void this.switchAssTrack(label); };
      }
    } catch { /* non-critical */ }
  }

  /** Start/stop the JASSUB overlay to match the selected text track. */
  private async switchAssTrack(label: string | null): Promise<void> {
    const entry = label ? this.assTracks.get(label) : undefined;
    if (!entry) {
      this.assRenderer?.stop();
      return;
    }
    try {
      if (!this.assRenderer) this.assRenderer = new AssRenderer();
      if (this.assRenderer.activeUrl === entry.assUrl) {
        // Same track re-selected — just keep JASSUB and mute Shaka's display.
        this._player?.setTextTrackVisibility(false);
        return;
      }
      let fonts = this.assFontsCache;
      if (fonts == null) {
        fonts = [];
        if (entry.fontsUrl) {
          try {
            const fr = await fetch(entry.fontsUrl);
            if (fr.ok) {
              fonts = ((await fr.json()) as Array<{ url: string }>).map((f) => f.url);
            }
          } catch { /* treated as no fonts below */ }
        }
        this.assFontsCache = fonts;
      }
      if (fonts.length === 0) {
        // jassub v2 ships no fallback font — a fontless render risks blank
        // glyphs. The styling-stripped WebVTT twin at least guarantees text.
        this.assRenderer?.stop();
        this._player?.setTextTrackVisibility(true);
        return;
      }
      await this.assRenderer.start(this._videoEl, entry.assUrl, fonts);
      // JASSUB draws the dialogue; suppress the styling-stripped VTT twin.
      this._player?.setTextTrackVisibility(false);
    } catch (err) {
      console.warn('[LWP] ASS render failed — falling back to WebVTT:', err);
      this.assRenderer?.stop();
      this._player?.setTextTrackVisibility(true);
    }
  }

  private resetAssState(): void {
    this.assRenderer?.stop();
    this.assTracks.clear();
    this.assFontsCache = null;
    if (this._tracks) this._tracks.onTextSelected = null;
  }

  async play(): Promise<void> {
    this._media.cancelStuckCheck();

    try {
      // Wait for enough buffered data before play() to avoid stuck-frame state.
      if (this._videoEl.readyState < HTMLMediaElement.HAVE_FUTURE_DATA) {
        await this._media.waitForCanPlay(8000);
      }
      await this._videoEl.play();
      this._media.scheduleStuckCheck();
    } catch (e: any) {
      if (e?.name === 'AbortError') {
        // play() interrupted by concurrent load/seek — retry once
        console.log('[LWP] play() aborted, retrying after canplay...');
        try {
          await this._media.waitForCanPlay(5000);
          await this._videoEl.play();
          this._media.scheduleStuckCheck();
        } catch {
          console.warn('[LWP] play() retry also failed');
        }
        return;
      }
      // NotAllowedError = autoplay policy — user must tap play
      if (e?.name !== 'NotAllowedError') {
        console.warn('[LWP] play() failed:', e?.name, e?.message);
      }
    }
  }

  pause(): void {
    this._videoEl.pause();
  }

  seek(time: number): void {
    this._videoEl.currentTime = Math.max(0, Math.min(time, this.duration));
  }

  // --- properties ---

  get currentTime(): number { return this._videoEl.currentTime; }
  get duration(): number { return this._videoEl.duration || 0; }
  get paused(): boolean { return this._videoEl.paused; }
  get volume(): number { return this._videoEl.volume; }
  set volume(v: number) { this._videoEl.volume = Math.max(0, Math.min(1, v)); }
  get muted(): boolean { return this._videoEl.muted; }
  set muted(m: boolean) { this._videoEl.muted = m; }
  get playbackRate(): number { return this._videoEl.playbackRate; }
  set playbackRate(rate: number) { this._videoEl.playbackRate = rate; }
  get buffered(): TimeRanges { return this._videoEl.buffered; }
  get manifestUri(): string | null {
    try { return this._player?.getAssetUri?.() ?? null; } catch { return null; }
  }

  /** Switch to a separate quality URL (multi-URL balancers). */
  async switchQualityUrl(url: string): Promise<void> {
    if (!this._player) return;
    const time = this.currentTime;
    await this._player.load(url);
    this._videoEl.currentTime = time;
    await this.play();
  }

  // --- Wave 1 B2: trickplay + chapters ---

  /** Wave 2 C3 — frame-precise step (forward / backward by one frame). */
  frameStep(direction: 'forward' | 'backward'): void {
    this.webCodecs?.frameStep(direction);
  }

  /** Wave 2 C3 — best-effort decoded fps from requestVideoFrameCallback. */
  get decodedFps(): number {
    return this.webCodecs?.estimateFps() ?? 0;
  }

  /** Quick read access for chapter-aware UI like Prev/Next chapter buttons. */
  get chapters(): Chapter[] { return this.currentChapters; }

  /** Find the chapter immediately before currentTime (for Prev button). */
  prevChapterStart(): number | null {
    if (this.currentChapters.length === 0) return null;
    const t = this._videoEl.currentTime;
    // 'Prev' jumps to the start of the current chapter unless we are
    // already very close to it, in which case it goes to the previous.
    const eps = 1.5;
    let candidate: number | null = null;
    for (const c of this.currentChapters) {
      if (c.start <= t - eps) candidate = c.start;
    }
    return candidate;
  }

  /** Find the next chapter start after currentTime (for Next button). */
  nextChapterStart(): number | null {
    if (this.currentChapters.length === 0) return null;
    const t = this._videoEl.currentTime;
    for (const c of this.currentChapters) {
      if (c.start > t + 0.5) return c.start;
    }
    return null;
  }

  private async discoverTrickplay(manifestUri: string): Promise<void> {
    if (!this._player) return;
    try {
      // Wait one tick so shaka has finished initial track parsing.
      await new Promise((r) => setTimeout(r, 50));
      const manifest = await TrickplayManifest.detect(this._player, manifestUri);
      if (manifest && !this.destroyed) {
        thumbnailManifest.set(manifest);
      }
    } catch (err) {
      console.warn('[LWP] Trickplay discovery failed:', err);
    }
  }

  private async discoverChapters(manifestUri: string): Promise<void> {
    if (!this._player) return;
    try {
      // 1. Try shaka first (synchronous-ish).
      let chapters = ChapterParser.fromShaka(this._player, this.duration);

      // 2. Fallback: parse HLS manifest directly.
      if (chapters.length === 0 && /\.m3u8(\?|$)/i.test(manifestUri)) {
        chapters = await ChapterParser.fromHlsManifest(manifestUri);
      }

      if (this.destroyed) return;
      this.currentChapters = chapters;
      chapterMarkers.set(chapters);

      if (chapters.length > 0) {
        // Push activeChapter on each timeupdate via the bus subscription.
        const updateActive = () => {
          const ch = ChapterParser.activeAt(this.currentChapters, this._videoEl.currentTime);
          const start = ch?.start ?? -1;
          if (start !== this.lastActiveChapterStart) {
            this.lastActiveChapterStart = start;
            activeChapter.set(ch);
            if (ch) this.bus.emit('chapter_enter', { chapter: ch });
          }
        };
        this.bus.on('timeupdate', updateActive);
      }
    } catch (err) {
      console.warn('[LWP] Chapter discovery failed:', err);
    }
  }

  async destroy(): Promise<void> {
    this.destroyed = true;
    this.detachVideoEvents();
    this.resetAssState();
    this._media.destroy();
    this.transcoding.destroy();
    this.network?.stop();
    this.network = null;
    this.cmcd = null;
    this.drmDiag?.detach();
    this.drmDiag = null;
    this.webCodecs?.destroy();
    this.webCodecs = null;
    if (this._player) {
      try { await this._player.destroy(); } catch { /* ignore */ }
      this._player = null;
    }
    this.bus.clear();
  }

  // --- Wave 2 C1: react to live network profile changes ---

  /**
   * Re-apply the buffer strategy with the latest network profile.
   * Player.svelte should call this when the networkProfile store
   * fires a change so battery drops or saveData toggle take effect
   * mid-playback.
   */
  refreshBufferProfile(): void {
    if (!this._player || !this._buffer || !this._tracks) return;
    try {
      const isLive = !!this._player.isLive?.();
      const active = this._tracks.quality().find((t) => t.active);
      const net: NetworkProfile = get(networkProfile);
      this._buffer.apply('auto', {
        isLive,
        activeHeight: active?.height,
        isLowPowerTV: this.lowPowerTV,
        network: net,
      });
    } catch { /* ignore */ }
  }
}
