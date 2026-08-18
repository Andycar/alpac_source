/**
 * AudioProcessor — Web Audio API wrapper for the player audio graph.
 *
 * Original chain: source → gain → compressor → destination.
 *
 * After Wave 1 B4 refactor (additive — back-compat preserved):
 *   source
 *     → preGain
 *     → [VoiceBoost: HP/Peak/LP + sidechain ducker]   (opt-in)
 *     → [LoudnessNormalizer: gain compensation]       (opt-in)
 *     → compressor (existing setCompressor preset)
 *     → [HRTFSurround: convolver]                     (opt-in)
 *     → masterGain
 *     → destination
 *
 * Each opt-in stage is constructed lazily on first enable() and bypassed
 * (transparent) when disabled. This keeps the graph minimal for users
 * who never touch audio settings.
 *
 * IMPORTANT: createMediaElementSource() can only be called ONCE per
 * <video> element. After that, audio routes through Web Audio exclusively.
 */

import { VoiceBoost } from './audio/VoiceBoost';
import { LoudnessNormalizer, type LoudnessTarget } from './audio/LoudnessNormalizer';
import { HRTFSurround } from './audio/HRTFSurround';

export type { LoudnessTarget };
export type CompressorPreset = 'off' | 'light' | 'heavy';

const COMPRESSOR_PARAMS: Record<CompressorPreset, {
  threshold: number;
  knee: number;
  ratio: number;
  attack: number;
  release: number;
} | null> = {
  off: null,
  light: { threshold: -24, knee: 30, ratio: 4, attack: 0.003, release: 0.25 },
  heavy: { threshold: -40, knee: 10, ratio: 12, attack: 0.001, release: 0.1 },
};

export class AudioProcessor {
  private ctx: AudioContext | null = null;
  private source: MediaElementAudioSourceNode | null = null;
  private gainNode: GainNode | null = null;
  private compressor: DynamicsCompressorNode | null = null;
  private masterGain: GainNode | null = null;
  private voiceStage: VoiceBoost | null = null;
  private loudnessStage: LoudnessNormalizer | null = null;
  private hrtfStage: HRTFSurround | null = null;
  // Tail of the lazy-stage chain — what feeds the compressor input.
  // Initially equals gainNode; updated as stages are inserted/removed.
  private gainTail: AudioNode | null = null;
  private _connected = false;
  private _gain = 1.0;
  private _preset: CompressorPreset = 'off';
  private _voiceEnabled = false;
  private _loudnessTarget: LoudnessTarget = 'off';
  private _hrtfEnabled = false;

  /**
   * Connect audio processing chain to a video element.
   * Must only be called ONCE per video element.
   * Returns false if Web Audio API is not supported.
   */
  connect(video: HTMLVideoElement): boolean {
    if (this._connected) return true;

    try {
      const AudioCtx = window.AudioContext || (window as any).webkitAudioContext;
      if (!AudioCtx) return false;

      this.ctx = new AudioCtx();
      this.source = this.ctx.createMediaElementSource(video);
      this.gainNode = this.ctx.createGain();
      this.compressor = this.ctx.createDynamicsCompressor();
      this.masterGain = this.ctx.createGain();
      this.masterGain.gain.value = 1.0;

      // Default chain: source → gain → compressor → masterGain → destination.
      // The gainTail starts at gainNode; lazy stages splice between
      // gainTail and compressor when enabled.
      this.source.connect(this.gainNode);
      this.gainTail = this.gainNode;
      this.gainNode.connect(this.compressor);
      this.compressor.connect(this.masterGain);
      this.masterGain.connect(this.ctx.destination);

      // Apply current settings
      this.gainNode.gain.value = this._gain;
      this.applyCompressorPreset(this._preset);

      this._connected = true;
      console.log('[LWP] AudioProcessor connected');
      return true;
    } catch (err) {
      console.warn('[LWP] AudioProcessor failed to connect:', err);
      return false;
    }
  }

  /** Set gain value (1.0 = normal, 3.0 = 300% boost) */
  setGain(value: number): void {
    this._gain = Math.max(0, Math.min(5, value));
    if (this.gainNode) {
      this.gainNode.gain.value = this._gain;
    }
  }

  getGain(): number {
    return this._gain;
  }

  /** Set compressor preset */
  setCompressor(preset: CompressorPreset): void {
    this._preset = preset;
    this.applyCompressorPreset(preset);
  }

  getCompressorPreset(): CompressorPreset {
    return this._preset;
  }

  /** Resume AudioContext (required after user gesture for autoplay policy) */
  async resume(): Promise<void> {
    if (this.ctx && this.ctx.state === 'suspended') {
      await this.ctx.resume();
    }
  }

  get connected(): boolean {
    return this._connected;
  }

  // --- Wave 1 B4: voice boost / loudness / HRTF ---

  /** Toggle the voice boost stage (lazy construct on first enable). */
  setVoiceBoost(enabled: boolean): void {
    if (!this.ctx || !this.gainNode || !this.compressor) return;
    if (enabled === this._voiceEnabled) return;
    this._voiceEnabled = enabled;
    if (enabled) {
      if (!this.voiceStage) {
        this.voiceStage = new VoiceBoost(this.ctx, this.gainTail!);
      }
      this.rewireLazyChain();
      this.voiceStage.enable();
    } else {
      this.voiceStage?.disable();
      this.rewireLazyChain();
    }
  }

  /** Set the loudness normalization target ('off' to disable). */
  setLoudnessTarget(target: LoudnessTarget): void {
    if (!this.ctx || !this.gainNode || !this.compressor) return;
    this._loudnessTarget = target;
    if (target !== 'off' && !this.loudnessStage) {
      this.loudnessStage = new LoudnessNormalizer(this.ctx, this.gainTail!);
      this.rewireLazyChain();
    }
    this.loudnessStage?.setTarget(target);
  }

  /** Notify loudness about a content URL change (cache-aware). */
  setContentForLoudness(url: string): void {
    this.loudnessStage?.setContent(url);
  }

  async setHRTF(enabled: boolean): Promise<void> {
    if (!this.ctx || !this.compressor) return;
    if (enabled === this._hrtfEnabled) return;
    this._hrtfEnabled = enabled;
    if (enabled) {
      if (!this.hrtfStage) {
        this.hrtfStage = new HRTFSurround(this.ctx, this.compressor);
      }
      this.rewirePostCompressorChain();
      await this.hrtfStage.enable();
    } else {
      this.hrtfStage?.disable();
      this.rewirePostCompressorChain();
    }
  }

  /** Reads — used by debug overlays / stats. */
  get measuredLufs(): number | null {
    return this.loudnessStage?.measuredLufs ?? null;
  }

  /**
   * Rebuild [gainNode] → [voice?] → [loudness?] → compressor.
   * WebAudio disconnect is destructive; this is the single source of truth.
   */
  private rewireLazyChain(): void {
    if (!this.gainNode || !this.compressor) return;
    try { this.gainNode.disconnect(); } catch { /* ignore */ }
    let tail: AudioNode = this.gainNode;
    if (this._voiceEnabled && this.voiceStage) {
      tail = this.voiceStage.node;
    }
    if (this._loudnessTarget !== 'off' && this.loudnessStage) {
      try { tail.disconnect(); } catch { /* ignore */ }
      tail.connect(this.loudnessStage.node);
      tail = this.loudnessStage.node;
    }
    try { tail.disconnect(); } catch { /* ignore */ }
    tail.connect(this.compressor);
    this.gainTail = tail;
  }

  private rewirePostCompressorChain(): void {
    if (!this.compressor || !this.masterGain) return;
    try { this.compressor.disconnect(); } catch { /* ignore */ }
    if (this._hrtfEnabled && this.hrtfStage) {
      this.compressor.connect(this.hrtfStage.node);
      try { this.hrtfStage.node.disconnect(); } catch { /* ignore */ }
      this.hrtfStage.node.connect(this.masterGain);
    } else {
      this.compressor.connect(this.masterGain);
    }
  }

  destroy(): void {
    try {
      this.voiceStage?.destroy();
      this.loudnessStage?.destroy();
      this.hrtfStage?.destroy();
      this.source?.disconnect();
      this.gainNode?.disconnect();
      this.compressor?.disconnect();
      this.masterGain?.disconnect();
      this.ctx?.close();
    } catch {
      // ignore
    }
    this.voiceStage = null;
    this.loudnessStage = null;
    this.hrtfStage = null;
    this.ctx = null;
    this.source = null;
    this.gainNode = null;
    this.compressor = null;
    this.masterGain = null;
    this.gainTail = null;
    this._connected = false;
  }

  private applyCompressorPreset(preset: CompressorPreset): void {
    if (!this.compressor) return;

    const params = COMPRESSOR_PARAMS[preset];
    if (!params) {
      // "off" — set threshold to 0 dB (effectively bypass)
      this.compressor.threshold.value = 0;
      this.compressor.knee.value = 40;
      this.compressor.ratio.value = 1;
      this.compressor.attack.value = 0;
      this.compressor.release.value = 0.25;
    } else {
      this.compressor.threshold.value = params.threshold;
      this.compressor.knee.value = params.knee;
      this.compressor.ratio.value = params.ratio;
      this.compressor.attack.value = params.attack;
      this.compressor.release.value = params.release;
    }
  }
}
