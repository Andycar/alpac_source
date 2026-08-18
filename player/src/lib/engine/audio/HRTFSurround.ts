/**
 * HRTFSurround — virtual 5.1-style spatial spread for headphone playback.
 *
 * Strategy: ConvolverNode loaded with a head-related impulse response
 * (HRIR). When no IR file is available we fall back to a tiny synthetic
 * impulse that just adds gentle width — better than nothing.
 *
 * The asset path is `/webplayer/assets/hrtf-irc.wav`. If the request
 * fails (asset not deployed), we silently use the synthetic IR.
 *
 * This module does NOT detect headphones — that decision is left to the
 * caller (it's a UX choice: some users want surround on speakers too).
 */

const ASSET_URL = '/webplayer/assets/hrtf-irc.wav';

export class HRTFSurround {
  private ctx: AudioContext;
  private input: AudioNode;
  private output: GainNode;
  private convolver: ConvolverNode;
  private wetGain: GainNode;
  private dryGain: GainNode;
  private enabled = false;
  private bufferReady: Promise<void> | null = null;

  constructor(ctx: AudioContext, input: AudioNode) {
    this.ctx = ctx;
    this.input = input;
    this.output = ctx.createGain();
    this.convolver = ctx.createConvolver();
    this.wetGain = ctx.createGain();
    this.dryGain = ctx.createGain();

    // Pass-through by default
    input.connect(this.dryGain);
    this.dryGain.gain.value = 1.0;
    this.wetGain.gain.value = 0.0;
    this.dryGain.connect(this.output);
    this.wetGain.connect(this.output);

    // Convolver wiring is set up in enable() once the IR buffer is ready.
  }

  get node(): AudioNode {
    return this.output;
  }

  async enable(): Promise<void> {
    if (this.enabled) return;
    if (!this.bufferReady) {
      this.bufferReady = this.loadOrSynthesize();
    }
    await this.bufferReady;
    if (!this.convolver.buffer) return; // load failed AND synth failed
    try {
      this.input.connect(this.convolver);
      this.convolver.connect(this.wetGain);
    } catch { /* already connected */ }
    // Crossfade dry → wet over 250ms.
    const t = this.ctx.currentTime;
    this.wetGain.gain.setTargetAtTime(0.65, t, 0.1);
    this.dryGain.gain.setTargetAtTime(0.6, t, 0.1);
    this.enabled = true;
  }

  disable(): void {
    if (!this.enabled) return;
    const t = this.ctx.currentTime;
    this.wetGain.gain.setTargetAtTime(0.0, t, 0.1);
    this.dryGain.gain.setTargetAtTime(1.0, t, 0.1);
    setTimeout(() => {
      try { this.input.disconnect(this.convolver); } catch { /* ignore */ }
      try { this.convolver.disconnect(this.wetGain); } catch { /* ignore */ }
    }, 250);
    this.enabled = false;
  }

  destroy(): void {
    this.disable();
    try { this.input.disconnect(this.dryGain); } catch { /* ignore */ }
    try { this.dryGain.disconnect(); } catch { /* ignore */ }
    try { this.wetGain.disconnect(); } catch { /* ignore */ }
    try { this.output.disconnect(); } catch { /* ignore */ }
  }

  private async loadOrSynthesize(): Promise<void> {
    try {
      const resp = await fetch(ASSET_URL, { signal: AbortSignal.timeout(5000) });
      if (!resp.ok) throw new Error(`HRIR fetch failed: ${resp.status}`);
      const arrayBuf = await resp.arrayBuffer();
      const audioBuf = await this.ctx.decodeAudioData(arrayBuf.slice(0));
      this.convolver.buffer = audioBuf;
      console.log('[LWP] HRTF IR loaded from asset');
    } catch (err) {
      console.log('[LWP] HRTF IR asset unavailable, using synthetic:', err);
      this.convolver.buffer = synthesizeIR(this.ctx);
    }
  }
}

/**
 * Build a synthetic stereo HRIR-like impulse. This is NOT a real HRTF
 * measurement — but it now models the three perceptual cues that matter
 * most for headphone "out-of-head" externalization:
 *
 *   1. ITD (Inter-aural Time Difference) — phantom sources slightly
 *      offset between ears. We sum 5 virtual point sources at azimuths
 *      ±60°, ±25°, 0° to simulate a wider soundstage.
 *   2. ILD (Inter-aural Level Difference) — head shadow gives ipsilateral
 *      ear ~3-6 dB more energy at frequencies above ~1.5kHz. We model
 *      this as a per-source amplitude bias.
 *   3. Early reflections — a sparse comb of attenuated taps in the first
 *      ~30ms simulates pinna/torso echoes that the brain uses to lock in
 *      a sense of room space.
 *
 * Result is a stable, deterministic IR (no per-call randomness) so the
 * sound is consistent across sessions. For studio-grade HRTF, drop a
 * real HRIR .wav into /webplayer/assets/hrtf-irc.wav (see fetch above).
 */
function synthesizeIR(ctx: AudioContext): AudioBuffer {
  const sr = ctx.sampleRate;
  const length = Math.floor(sr * 0.08); // 80ms IR
  const buf = ctx.createBuffer(2, length, sr);
  const left = buf.getChannelData(0);
  const right = buf.getChannelData(1);

  // Five virtual sources at azimuths (left ear delay, right ear delay, gain L, gain R).
  // Delays are in seconds — converted to sample offsets below. Gains include
  // ILD bias (ipsilateral ear gets more energy).
  // Speed of sound = 343 m/s; head radius ~0.0875 m → max ITD ≈ 0.88ms.
  const sources: Array<{ delayLeft: number; delayRight: number; gainL: number; gainR: number }> = [
    // Hard left  (-60°)  — left arrives first, much louder.
    { delayLeft: 0.00000, delayRight: 0.00076, gainL: 0.55, gainR: 0.18 },
    // Half left  (-25°)
    { delayLeft: 0.00005, delayRight: 0.00040, gainL: 0.50, gainR: 0.32 },
    // Center     (  0°)  — phantom mono component.
    { delayLeft: 0.00020, delayRight: 0.00020, gainL: 0.45, gainR: 0.45 },
    // Half right (+25°)
    { delayLeft: 0.00040, delayRight: 0.00005, gainL: 0.32, gainR: 0.50 },
    // Hard right (+60°)
    { delayLeft: 0.00076, delayRight: 0.00000, gainL: 0.18, gainR: 0.55 },
  ];

  // Direct sources — a peak at the per-ear delay sample.
  for (const s of sources) {
    const li = Math.floor(s.delayLeft * sr);
    const ri = Math.floor(s.delayRight * sr);
    if (li < length) left[li] += s.gainL;
    if (ri < length) right[ri] += s.gainR;
  }

  // Early reflections: a sparse comb of taps mimicking pinna + first-order
  // wall reflections (5-30ms, exponentially decaying). Pseudo-random offsets
  // are seeded deterministically (mulberry32) so repeated synthesis is stable.
  const rng = mulberry32(0xACE0FBA1);
  const reflMin = Math.floor(sr * 0.005);
  const reflMax = Math.floor(sr * 0.030);
  for (let i = 0; i < 24; i++) {
    const tL = reflMin + Math.floor(rng() * (reflMax - reflMin));
    const tR = reflMin + Math.floor(rng() * (reflMax - reflMin));
    const decayL = Math.pow(0.4, tL / (sr * 0.030));
    const decayR = Math.pow(0.4, tR / (sr * 0.030));
    const sign = (i & 1) ? -1 : 1;
    if (tL < length) left[tL] += sign * 0.06 * decayL;
    if (tR < length) right[tR] += sign * 0.06 * decayR;
  }

  // Diffuse tail: low-amplitude noise envelope shaped by 1/t decay so the
  // result has a sense of room ambience without sounding noisy.
  const tailStart = Math.floor(sr * 0.025);
  for (let i = tailStart; i < length; i++) {
    const t = (i - tailStart) / (length - tailStart);
    const env = Math.pow(0.02, t);
    const nL = (rng() - 0.5);
    const nR = (rng() - 0.5);
    left[i]  += nL * env * 0.04;
    right[i] += nR * env * 0.04;
  }

  // Normalize peak to ≤ 0.95 to avoid clipping after convolution.
  let peak = 0;
  for (let i = 0; i < length; i++) {
    peak = Math.max(peak, Math.abs(left[i]), Math.abs(right[i]));
  }
  if (peak > 0.95) {
    const scale = 0.95 / peak;
    for (let i = 0; i < length; i++) {
      left[i] *= scale;
      right[i] *= scale;
    }
  }
  return buf;
}

/**
 * Deterministic PRNG (mulberry32) — used so synthesizeIR is reproducible.
 * Stability matters: a player open across multiple tabs / sessions should
 * sound identical, not change every refresh.
 */
function mulberry32(seed: number): () => number {
  let state = seed >>> 0;
  return function () {
    state = (state + 0x6D2B79F5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
