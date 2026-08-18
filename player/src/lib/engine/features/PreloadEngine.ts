/**
 * PreloadEngine — zero-gap episode transitions.
 *
 * Idea: while the current episode is still playing, create a second
 * <video> element + a parallel PlaybackEngine for the *next* episode and
 * load it silently in the background. When the active episode ends (or
 * is interrupted by user "next"), swap the two video elements with a
 * short crossfade. The result: the next episode starts in < 100ms
 * instead of the usual 1-3 seconds of load+buffer.
 *
 * State machine:
 *   idle      → no preload active
 *   preparing → secondary core constructed and load() in flight
 *   ready     → secondary core has emitted 'loaded' and is buffered
 *   swapping  → we handed the secondary off; main is being torn down
 *
 * Gates (callable via shouldArm()):
 *   - preload feature enabled
 *   - hasNext() returned a PlayElement
 *   - remaining < settings.preloadStartSeconds
 *   - active stream healthy: bufferedEnd >= currentTime + 25
 *   - networkProfile.effectiveType !== '2g'
 *
 * The Player.svelte caller is expected to:
 *   1. Call `armForEpisode(remaining)` from its `timeupdate` handler.
 *   2. On `ended` (or user "next"), call `consumeIfReady(activeCore)`.
 *   3. If consume returns a swap result, do the visual crossfade and
 *      then `await activeCore.destroy()`.
 *   4. Otherwise fall back to the regular playItem() path.
 */

import { PlaybackEngine } from '../PlaybackEngine';
import type { PlayElement } from '../core/types';

export type PreloadState = 'idle' | 'preparing' | 'ready' | 'swapping';

export interface PreloadGateContext {
  enabled: boolean;
  startSeconds: number;
  remaining: number;
  bufferedAhead: number;
  networkType?: string;
}

export interface PreloadSwapResult {
  /** Newly active core — Player.svelte should bind it. */
  newEngine: PlaybackEngine;
  /** Newly visible video element — Player.svelte should crossfade in. */
  newVideo: HTMLVideoElement;
  /** The element that was just preloaded (informational). */
  element: PlayElement;
  /** Index that was preloaded (informational). */
  episodeIndex: number;
}

type GetNext = () => { element: PlayElement; episodeIndex: number } | null;

export class PreloadEngine {
  private state: PreloadState = 'idle';
  private secondaryVideo: HTMLVideoElement | null = null;
  private secondaryEngine: PlaybackEngine | null = null;
  private preparedFor: { url: string; index: number } | null = null;
  private prepareInFlight: Promise<void> | null = null;

  constructor(
    private parentEl: HTMLElement,
    private getNext: GetNext,
  ) {}

  get currentState(): PreloadState {
    return this.state;
  }

  get preparedElement(): PlayElement | null {
    return this.preparedFor ? this.getNext()?.element ?? null : null;
  }

  /**
   * Pure gating logic — exposed so callers can also use it for
   * "should we even bother peeking" checks. No side effects.
   *
   * The gates are intentionally conservative: a healthy active stream
   * is more important than zero-gap nice-to-have. If buffer is ANY
   * tighter than ~comfortable, we skip preload to avoid bandwidth
   * contention that would stall the visible video.
   */
  static shouldArm(ctx: PreloadGateContext): boolean {
    if (!ctx.enabled) return false;
    if (ctx.remaining > ctx.startSeconds) return false;
    if (ctx.remaining < 5) return false;          // too late, fallback path
    if (ctx.bufferedAhead < 30) return false;     // require comfortable buffer
    // Skip on any non-4G connection — preload halves bandwidth available
    // to the active stream, and we'd rather not glitch what's playing.
    const t = ctx.networkType;
    if (t === '2g' || t === 'slow-2g' || t === '3g') return false;
    return true;
  }

  /**
   * Consider arming preload for the upcoming episode. Idempotent —
   * subsequent calls during the same window are no-ops.
   */
  armForEpisode(ctx: PreloadGateContext): void {
    if (this.state !== 'idle') return;
    if (!PreloadEngine.shouldArm(ctx)) return;

    const next = this.getNext();
    if (!next || !next.element?.url) return;

    // Avoid re-preparing the same URL
    if (this.preparedFor?.url === next.element.url) return;

    this.state = 'preparing';
    this.preparedFor = { url: next.element.url, index: next.episodeIndex };
    this.prepareInFlight = this.prepare(next.element).catch((err) => {
      console.warn('[LWP] Preload failed:', err);
      this.cleanupSecondary();
      this.state = 'idle';
      this.preparedFor = null;
    });
  }

  private async prepare(element: PlayElement): Promise<void> {
    // Build hidden video sibling. We deliberately do NOT use object-fit
    // styles here — Player.svelte's CSS handles that on .lwp-video.
    const v = document.createElement('video');
    v.className = 'lwp-video lwp-video-next';
    v.muted = true;
    v.playsInline = true;
    (v as any).crossOrigin = 'anonymous';
    v.style.position = 'absolute';
    v.style.inset = '0';
    v.style.width = '100%';
    v.style.height = '100%';
    v.style.opacity = '0';
    v.style.pointerEvents = 'none';
    // Sit just above the active video so we can fade in cleanly.
    v.style.zIndex = '2';
    this.parentEl.appendChild(v);
    this.secondaryVideo = v;

    const core = new PlaybackEngine(v);
    this.secondaryEngine = core;
    await core.init();

    // Load (this resolves once shaka has the manifest + initial segments).
    await core.load(element);

    // Wait briefly for canplay so the swap is truly seamless. We don't
    // call play() yet — the secondary video stays paused until swap.
    await waitForReadyState(v, HTMLMediaElement.HAVE_FUTURE_DATA, 5000);

    this.state = 'ready';
    console.log('[LWP] Preload ready for episode index:', this.preparedFor?.index);
  }

  /**
   * Hand off the prepared secondary as the new active. Returns null if
   * nothing is ready yet — caller should fall back to its normal path.
   *
   * After a successful swap the PreloadEngine resets to idle and the
   * caller is responsible for destroying the previous active core.
   */
  consumeIfReady(): PreloadSwapResult | null {
    if (this.state !== 'ready') return null;
    if (!this.secondaryEngine || !this.secondaryVideo || !this.preparedFor) return null;

    const next = this.getNext();
    if (!next || next.element.url !== this.preparedFor.url) {
      // Playlist changed under us — discard and let caller use normal path.
      this.cancel();
      return null;
    }

    const result: PreloadSwapResult = {
      newEngine: this.secondaryEngine,
      newVideo: this.secondaryVideo,
      element: next.element,
      episodeIndex: this.preparedFor.index,
    };

    // Reset state but DO NOT destroy — ownership has transferred.
    this.secondaryEngine = null;
    this.secondaryVideo = null;
    this.preparedFor = null;
    this.prepareInFlight = null;
    this.state = 'swapping';

    // Caller will eventually trigger something that lands us back in idle
    // via reset(). We move there explicitly here so the next timeupdate
    // can immediately re-arm for the *new* "next" episode.
    queueMicrotask(() => { this.state = 'idle'; });

    return result;
  }

  /**
   * Cancel an in-flight or ready preload. Used when the user manually
   * jumps to a different episode, when settings disable preload mid-
   * session, or when the playlist changes.
   */
  async cancel(): Promise<void> {
    if (this.state === 'idle') return;
    const inFlight = this.prepareInFlight;
    this.cleanupSecondary();
    this.state = 'idle';
    this.preparedFor = null;
    if (inFlight) {
      try { await inFlight; } catch { /* swallowed already */ }
    }
  }

  private cleanupSecondary(): void {
    const core = this.secondaryEngine;
    const v = this.secondaryVideo;
    this.secondaryEngine = null;
    this.secondaryVideo = null;
    this.prepareInFlight = null;
    if (core) {
      core.destroy().catch(() => { /* ignore */ });
    }
    if (v?.parentElement) {
      v.parentElement.removeChild(v);
    }
  }

  destroy(): void {
    this.cleanupSecondary();
    this.state = 'idle';
    this.preparedFor = null;
  }
}

function waitForReadyState(
  v: HTMLVideoElement,
  target: number,
  timeoutMs: number,
): Promise<void> {
  if (v.readyState >= target) return Promise.resolve();
  return new Promise((resolve) => {
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      v.removeEventListener('canplay', finish);
      v.removeEventListener('canplaythrough', finish);
      v.removeEventListener('loadeddata', finish);
      clearTimeout(timer);
      resolve();
    };
    v.addEventListener('canplay', finish);
    v.addEventListener('canplaythrough', finish);
    v.addEventListener('loadeddata', finish);
    const timer = setTimeout(finish, timeoutMs);
  });
}
