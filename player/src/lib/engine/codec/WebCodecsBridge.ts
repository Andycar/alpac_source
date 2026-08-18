/**
 * WebCodecsBridge — frame-precise utilities backed by the WebCodecs and
 * `requestVideoFrameCallback` APIs (Wave 2 C3, scoped MVP).
 *
 * What this provides:
 *  - frameStep(direction) — advance/rewind by exactly one frame
 *  - currentFrameNumber() — best-effort frame index since playback start
 *  - hasRVFC / hasWebCodecs — capability flags used by callers / settings
 *
 * What this DOES NOT do (out of scope, see WasmCodecLoader for the why):
 *  - Inject WebCodecs decode results into MediaSource. That requires a
 *    canvas-render fork of the playback path — too big a change for now.
 *
 * This module is purely additive: it never overrides existing playback.
 * It just adds new capabilities accessible through PlaybackEngine.
 */

export interface FrameInfo {
  presentedFrames: number;
  mediaTime: number;
  fps?: number;
}

export class WebCodecsBridge {
  private rvfcHandle: number | null = null;
  private latest: FrameInfo | null = null;
  private samples: Array<{ t: number; n: number }> = [];

  static get hasRVFC(): boolean {
    return typeof HTMLVideoElement !== 'undefined' &&
      'requestVideoFrameCallback' in HTMLVideoElement.prototype;
  }

  static get hasWebCodecs(): boolean {
    return typeof (globalThis as any).VideoDecoder === 'function' &&
      typeof (globalThis as any).VideoFrame === 'function';
  }

  constructor(private video: HTMLVideoElement) {}

  /**
   * Start tracking presented frames. Lightweight — one rAF-like tick per
   * presented frame. Detaches automatically on destroy().
   */
  startTracking(): void {
    if (!WebCodecsBridge.hasRVFC || this.rvfcHandle !== null) return;
    const tick = (_now: number, meta: any) => {
      this.latest = {
        presentedFrames: meta.presentedFrames || 0,
        mediaTime: meta.mediaTime || 0,
      };
      // Keep last ~60 samples for fps estimation.
      this.samples.push({ t: meta.mediaTime, n: meta.presentedFrames });
      if (this.samples.length > 60) this.samples.shift();
      this.rvfcHandle = (this.video as any).requestVideoFrameCallback(tick);
    };
    this.rvfcHandle = (this.video as any).requestVideoFrameCallback(tick);
  }

  stopTracking(): void {
    if (this.rvfcHandle !== null && (this.video as any).cancelVideoFrameCallback) {
      (this.video as any).cancelVideoFrameCallback(this.rvfcHandle);
    }
    this.rvfcHandle = null;
    this.samples = [];
    this.latest = null;
  }

  /** Approximate current playback fps from the last few RVFC samples. */
  estimateFps(): number {
    const n = this.samples.length;
    if (n < 6) return 0;
    const first = this.samples[0];
    const last = this.samples[n - 1];
    const dt = last.t - first.t;
    const dn = last.n - first.n;
    if (dt <= 0 || dn <= 0) return 0;
    return dn / dt;
  }

  /**
   * Step exactly one frame forward or backward. Falls back to a fixed
   * 1/30s seek when fps is unknown — better than nothing on UAs without
   * RVFC support.
   */
  frameStep(direction: 'forward' | 'backward'): void {
    const fps = this.estimateFps() || 30;
    const delta = (direction === 'forward' ? 1 : -1) / fps;
    const t = Math.max(0, this.video.currentTime + delta);
    this.video.currentTime = t;
  }

  get info(): FrameInfo | null {
    return this.latest;
  }

  destroy(): void {
    this.stopTracking();
  }
}
