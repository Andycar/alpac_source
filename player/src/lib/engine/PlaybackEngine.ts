/**
 * PlaybackEngine — public facade.
 *
 * After the Phase A refactor, this class is a thin wrapper that delegates
 * to the focused modules under ./core/ and ./features/. Public method
 * signatures are preserved so Player.svelte and bindEngineToStores() in
 * stores/player.ts continue to work without changes.
 *
 * If you're adding a new feature: prefer touching CorePlayback or one of
 * the subsystem modules. Use this facade only to expose a method to UI.
 */

import { CorePlayback } from './core/CorePlayback';
import type { BatchEpisode } from './BatchTranscoding';
import type {
  PlayElement,
  QualityTrack,
  AudioTrack,
  TextTrack,
  EngineEvents,
} from './core/types';
import type { CompressorPreset, LoudnessTarget } from './AudioProcessor';
import { AudioProcessor } from './AudioProcessor';
import type { Listener } from './core/EventBus';

// Re-export legacy types so existing imports keep resolving
export type { PlayElement, QualityTrack, AudioTrack, TextTrack };
// Loose engine event union — kept for legacy `on(event: EngineEvent)` typing
export type EngineEvent = keyof EngineEvents;

type LegacyCallback = (...args: any[]) => void;

export class PlaybackEngine {
  private core: CorePlayback;
  private audioProcessor: AudioProcessor | null = null;

  constructor(videoEl: HTMLVideoElement) {
    this.core = new CorePlayback(videoEl);
  }

  // --- subsystem exposure (for advanced consumers / future features) ---
  /** Direct access to the underlying core (use sparingly). */
  get $core(): CorePlayback { return this.core; }

  // --- lifecycle ---

  async init(opts?: { lowPowerTV?: boolean }): Promise<void> {
    return this.core.init(opts);
  }

  /** Wave 2 C1 — re-apply buffer strategy when network profile changes. */
  refreshBufferProfile(): void {
    this.core.refreshBufferProfile();
  }

  async load(element: PlayElement): Promise<void> { return this.core.load(element); }

  async play(): Promise<void> {
    this.audioProcessor?.resume();
    return this.core.play();
  }

  pause(): void { this.core.pause(); }

  seek(time: number): void { this.core.seek(time); }

  // --- properties ---

  get currentTime(): number { return this.core.currentTime; }
  get duration(): number { return this.core.duration; }
  get paused(): boolean { return this.core.paused; }
  get volume(): number { return this.core.volume; }
  set volume(v: number) { this.core.volume = v; }
  get muted(): boolean { return this.core.muted; }
  set muted(m: boolean) { this.core.muted = m; }
  get playbackRate(): number { return this.core.playbackRate; }
  set playbackRate(rate: number) { this.core.playbackRate = rate; }
  get buffered(): TimeRanges { return this.core.buffered; }
  get video(): HTMLVideoElement { return this.core.video; }

  get isTranscoding(): boolean { return this.core.transcoding.isTranscoding; }
  get batch() { return this.core.transcoding.batch; }

  // --- track management (delegated to TrackManager) ---

  getQualityTracks(): QualityTrack[] { return this.core.tracks.quality(); }
  selectQuality(trackId: number | 'auto'): void { this.core.tracks.selectQuality(trackId); }
  applyPreferredQuality(pref: 'auto' | 'highest' | number): void {
    this.core.tracks.applyPreferredQuality(pref);
  }
  getQualityMap(): Record<string, string> | null { return this.core.tracks.getQualityMap(); }

  async switchQualityUrl(url: string): Promise<void> {
    return this.core.switchQualityUrl(url);
  }

  getAudioTracks(): AudioTrack[] { return this.core.tracks.audio(); }
  selectAudioTrack(trackId: number): void { this.core.tracks.selectAudio(trackId); }
  applyPreferredAudioLang(lang: string): void { this.core.tracks.applyPreferredAudioLang(lang); }

  getTextTracks(): TextTrack[] { return this.core.tracks.text(); }
  selectTextTrack(trackId: number | 'off'): void { this.core.tracks.selectText(trackId); }
  applyPreferredSubLang(lang: string): void { this.core.tracks.applyPreferredSubLang(lang); }

  // --- stats ---

  getStats(): Record<string, string> {
    return this.core.stats.collect({
      isTranscoding: this.core.transcoding.isTranscoding,
      manifestUri: this.core.manifestUri ?? undefined,
    });
  }

  // --- media features ---

  async togglePiP(): Promise<void> { return this.core.media.togglePiP(); }
  takeScreenshot(): string | null { return this.core.media.takeScreenshot(); }

  // --- audio processing (lazy AudioProcessor) ---

  setAudioBoost(gain: number): void {
    this.ensureAudioProcessor();
    this.audioProcessor?.setGain(gain);
  }

  setAudioCompressor(preset: CompressorPreset): void {
    this.ensureAudioProcessor();
    this.audioProcessor?.setCompressor(preset);
  }

  /** Wave 1 B4 — voice boost (300-3400Hz emphasis + sidechain duck). */
  setVoiceBoost(enabled: boolean): void {
    this.ensureAudioProcessor();
    this.audioProcessor?.setVoiceBoost(enabled);
  }

  /** Wave 1 B4 — loudness normalization target ('off' to disable). */
  setLoudnessTarget(target: LoudnessTarget): void {
    this.ensureAudioProcessor();
    this.audioProcessor?.setLoudnessTarget(target);
    const url = this.core.currentEl?.url;
    if (url) this.audioProcessor?.setContentForLoudness(url);
  }

  /** Wave 1 B4 — HRTF surround (headphones). */
  async setHRTF(enabled: boolean): Promise<void> {
    this.ensureAudioProcessor();
    await this.audioProcessor?.setHRTF(enabled);
  }

  /** Wave 1 B4 debug — last computed loudness measurement (or null). */
  get measuredLufs(): number | null {
    return this.audioProcessor?.measuredLufs ?? null;
  }

  // --- Wave 2 C3: frame-precise step ---
  frameStep(direction: 'forward' | 'backward'): void {
    this.core.frameStep(direction);
  }

  /** Best-effort decoded fps via requestVideoFrameCallback (or 0). */
  get decodedFps(): number {
    return this.core.decodedFps;
  }

  private ensureAudioProcessor(): void {
    if (this.audioProcessor?.connected) return;
    if (!this.audioProcessor) this.audioProcessor = new AudioProcessor();
    this.audioProcessor.connect(this.core.video);
  }

  // --- batch transcoding (kept for API back-compat) ---

  async startBatchTranscoding(
    episodes: BatchEpisode[],
    opts?: { subtitles?: boolean },
  ) {
    return this.core.transcoding.startBatch(episodes, opts);
  }

  async loadBatchEpisode(episodeIndex: number): Promise<boolean> {
    const session = this.core.transcoding.batch;
    if (!session?.active) return false;
    const info = await session.waitForEpisode(episodeIndex, 90000);
    if (!info?.playlistUrl) {
      console.warn('[LWP] Batch episode not ready, falling back to single transcoding');
      return false;
    }
    console.log('[LWP] Loading batch episode:', episodeIndex, info.state, info.playlistUrl);
    try {
      await this.core.loadBatchUrl(info.playlistUrl, info.subtitlesUrl);
      return true;
    } catch (e: any) {
      console.warn('[LWP] Batch episode load failed:', e?.message || e);
      return false;
    }
  }

  // --- events (legacy-compatible signatures) ---
  // The original engine called listeners with positional args
  // (e.g. `(b: boolean)` for buffering). The new bus uses typed payloads,
  // but most events have a single payload value, so this adapter just
  // unwraps where needed.

  on(event: EngineEvent, cb: LegacyCallback): void {
    const adapter: Listener<EngineEvents[typeof event]> = (payload: any) => {
      if (payload === undefined) {
        cb();
      } else {
        cb(payload);
      }
    };
    // Stash adapter on the cb so off() can find it
    (cb as any).__adapter = adapter;
    this.core.bus.on(event as keyof EngineEvents, adapter);
  }

  off(event: EngineEvent, cb: LegacyCallback): void {
    const adapter = (cb as any).__adapter as Listener<EngineEvents[typeof event]> | undefined;
    if (adapter) {
      this.core.bus.off(event as keyof EngineEvents, adapter);
    }
  }

  // --- cleanup ---

  async destroy(): Promise<void> {
    this.audioProcessor?.destroy();
    this.audioProcessor = null;
    return this.core.destroy();
  }
}
