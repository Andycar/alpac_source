/**
 * LoudnessNormalizer — keeps perceived volume consistent across titles.
 *
 * Uses a rolling-window K-weighted RMS approximation (a simplified
 * Lufs-like measurement; we use AnalyserNode rather than the formal
 * BS.1770 filter chain because the goal is "no jarring volume swings",
 * not a broadcast-quality measurement).
 *
 * Per-URL gain offsets are cached in localStorage with a TTL so seeking
 * back into a previously-watched title applies the same compensation
 * instantly without a fresh measurement window.
 */

const STORAGE_KEY = 'lwp_loudness_cache_v1';
const TTL_MS = 30 * 24 * 60 * 60 * 1000; // 30 days
const MAX_CACHE_ENTRIES = 200;
const MEASURE_WINDOW_SEC = 30;

export type LoudnessTarget = 'off' | -23 | -16 | -14;

interface CacheEntry {
  lufsApprox: number;
  measuredAt: number;
}

type Cache = Record<string, CacheEntry>;

export class LoudnessNormalizer {
  private ctx: AudioContext;
  private input: AudioNode;
  private output: GainNode;
  private analyser: AnalyserNode;
  private buffer: Float32Array<ArrayBuffer>;
  private rafId: number | null = null;
  private startedAt = 0;
  private samples: number[] = [];
  private targetLufs: LoudnessTarget = 'off';
  private currentUrl: string | null = null;
  private currentMeasured = false;

  constructor(ctx: AudioContext, input: AudioNode) {
    this.ctx = ctx;
    this.input = input;
    this.output = ctx.createGain();
    this.analyser = ctx.createAnalyser();
    this.analyser.fftSize = 1024;
    this.analyser.smoothingTimeConstant = 0.4;
    this.buffer = new Float32Array(this.analyser.fftSize);

    // Pass-through with measurement tap.
    input.connect(this.output);
    input.connect(this.analyser);
  }

  get node(): AudioNode {
    return this.output;
  }

  /** Latest computed value (test/debug). */
  get measuredLufs(): number | null {
    if (this.samples.length === 0) return null;
    const mean = this.samples.reduce((a, b) => a + b, 0) / this.samples.length;
    // RMS → "lufs-ish" scale. Real BS.1770 needs K-weighting + gating;
    // this approximation lines up close enough for relative balancing.
    return 20 * Math.log10(Math.max(mean, 1e-6));
  }

  setTarget(target: LoudnessTarget): void {
    this.targetLufs = target;
    if (target === 'off') {
      this.output.gain.setTargetAtTime(1.0, this.ctx.currentTime, 0.1);
      this.stopMeasurement();
      return;
    }
    // Re-apply cached gain immediately if we have it.
    if (this.currentUrl) {
      const cached = readCache()[this.currentUrl];
      if (cached) {
        this.applyFromMeasured(cached.lufsApprox);
        return;
      }
    }
    this.startMeasurement();
  }

  /** Tell the normalizer about a new content URL — checks cache + restarts. */
  setContent(url: string): void {
    this.currentUrl = url;
    this.currentMeasured = false;
    this.samples = [];
    if (this.targetLufs === 'off' || !url) return;
    const cached = readCache()[url];
    if (cached) {
      this.applyFromMeasured(cached.lufsApprox);
      this.currentMeasured = true;
    } else {
      this.startMeasurement();
    }
  }

  destroy(): void {
    this.stopMeasurement();
    try { this.input.disconnect(this.output); } catch { /* ignore */ }
    try { this.input.disconnect(this.analyser); } catch { /* ignore */ }
    try { this.output.disconnect(); } catch { /* ignore */ }
  }

  private startMeasurement(): void {
    if (this.rafId !== null) return;
    this.startedAt = this.ctx.currentTime;
    this.samples = [];
    const tick = () => {
      const elapsed = this.ctx.currentTime - this.startedAt;
      this.analyser.getFloatTimeDomainData(this.buffer);
      let sum = 0;
      for (let i = 0; i < this.buffer.length; i++) {
        sum += this.buffer[i] * this.buffer[i];
      }
      const rms = Math.sqrt(sum / this.buffer.length);
      this.samples.push(rms);
      // Cap window: ~MEASURE_WINDOW_SEC × 60fps samples.
      if (this.samples.length > MEASURE_WINDOW_SEC * 60) {
        this.samples.shift();
      }
      if (elapsed >= MEASURE_WINDOW_SEC && !this.currentMeasured) {
        const lufs = this.measuredLufs;
        if (lufs !== null && this.currentUrl) {
          this.currentMeasured = true;
          this.applyFromMeasured(lufs);
          writeCache(this.currentUrl, { lufsApprox: lufs, measuredAt: Date.now() });
        }
      }
      this.rafId = requestAnimationFrame(tick);
    };
    this.rafId = requestAnimationFrame(tick);
  }

  private stopMeasurement(): void {
    if (this.rafId !== null) {
      cancelAnimationFrame(this.rafId);
      this.rafId = null;
    }
  }

  private applyFromMeasured(measuredLufs: number): void {
    if (this.targetLufs === 'off') return;
    const target = this.targetLufs;
    // Gain in dB needed to bring measured to target.
    const gainDb = target - measuredLufs;
    // Clamp to ±12dB to avoid pumping or clipping outliers.
    const clamped = Math.max(-12, Math.min(12, gainDb));
    const gainLinear = Math.pow(10, clamped / 20);
    this.output.gain.setTargetAtTime(gainLinear, this.ctx.currentTime, 0.4);
  }
}

function readCache(): Cache {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed: Cache = JSON.parse(raw);
    // Drop stale entries lazily.
    const now = Date.now();
    for (const k of Object.keys(parsed)) {
      if (!parsed[k] || now - parsed[k].measuredAt > TTL_MS) delete parsed[k];
    }
    return parsed;
  } catch {
    return {};
  }
}

function writeCache(url: string, entry: CacheEntry): void {
  try {
    const cache = readCache();
    cache[url] = entry;
    // Trim to MAX_CACHE_ENTRIES, dropping oldest.
    const keys = Object.keys(cache);
    if (keys.length > MAX_CACHE_ENTRIES) {
      keys.sort((a, b) => cache[a].measuredAt - cache[b].measuredAt);
      for (let i = 0; i < keys.length - MAX_CACHE_ENTRIES; i++) delete cache[keys[i]];
    }
    localStorage.setItem(STORAGE_KEY, JSON.stringify(cache));
  } catch {
    // ignore quota errors
  }
}
