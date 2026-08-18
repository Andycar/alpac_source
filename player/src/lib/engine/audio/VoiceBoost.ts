/**
 * VoiceBoost — emphasizes the speech band (300-3400Hz) and lightly ducks
 * the rest of the spectrum so dialog cuts through music + SFX.
 *
 * Implementation:
 *  - Splits the input into two parallel paths via a BiquadFilter chain:
 *      voicePath:  HP@300 → Peak@2k Q=1.0 +6dB → LP@3400  (boosts speech)
 *      restPath:   notch around 1.5k                       (carries everything else)
 *  - An AnalyserNode on the voicePath drives a GainNode on the restPath,
 *    emulating sidechain ducking: when speech energy is high, music dips.
 *
 * The chain is built lazily and bypassable — when disable() is called
 * we route the input straight through with a single pass-through gain.
 */

export class VoiceBoost {
  private ctx: AudioContext;
  private input: AudioNode;
  private output: GainNode;
  private bypassGain: GainNode;
  private wetGain: GainNode;
  private speechBandHi: BiquadFilterNode;
  private speechBandPeak: BiquadFilterNode;
  private speechBandLo: BiquadFilterNode;
  private restNotch: BiquadFilterNode;
  private restGain: GainNode;
  private analyser: AnalyserNode | null = null;
  private envelopeRaf: number | null = null;
  private buffer: Float32Array<ArrayBuffer>;
  private enabled = false;

  constructor(ctx: AudioContext, input: AudioNode) {
    this.ctx = ctx;
    this.input = input;
    this.output = ctx.createGain();
    this.bypassGain = ctx.createGain();
    this.wetGain = ctx.createGain();

    // Speech-band path (boost)
    this.speechBandHi = ctx.createBiquadFilter();
    this.speechBandHi.type = 'highpass';
    this.speechBandHi.frequency.value = 300;
    this.speechBandHi.Q.value = 0.7;

    this.speechBandPeak = ctx.createBiquadFilter();
    this.speechBandPeak.type = 'peaking';
    this.speechBandPeak.frequency.value = 2000;
    this.speechBandPeak.Q.value = 1.0;
    this.speechBandPeak.gain.value = 6;

    this.speechBandLo = ctx.createBiquadFilter();
    this.speechBandLo.type = 'lowpass';
    this.speechBandLo.frequency.value = 3400;
    this.speechBandLo.Q.value = 0.7;

    // Rest path (gets ducked when speech is loud)
    this.restNotch = ctx.createBiquadFilter();
    this.restNotch.type = 'notch';
    this.restNotch.frequency.value = 1500;
    this.restNotch.Q.value = 0.5;
    this.restGain = ctx.createGain();
    this.restGain.gain.value = 1.0;

    this.buffer = new Float32Array(256);

    this.disable(); // start in pass-through
  }

  enable(): void {
    if (this.enabled) return;
    this.enabled = true;

    // Disconnect everything so we can rewire.
    this.safeDisconnect(this.input);
    this.safeDisconnect(this.bypassGain);
    this.safeDisconnect(this.wetGain);

    // Speech path: input → HP → Peak → LP → wetGain → output
    this.input.connect(this.speechBandHi);
    this.speechBandHi.connect(this.speechBandPeak);
    this.speechBandPeak.connect(this.speechBandLo);
    this.speechBandLo.connect(this.wetGain);
    this.wetGain.gain.value = 1.0;
    this.wetGain.connect(this.output);

    // Rest path: input → notch → restGain → output
    this.input.connect(this.restNotch);
    this.restNotch.connect(this.restGain);
    this.restGain.connect(this.output);

    // Sidechain envelope follower
    this.analyser = this.ctx.createAnalyser();
    this.analyser.fftSize = 512;
    this.analyser.smoothingTimeConstant = 0.6;
    this.speechBandPeak.connect(this.analyser);

    this.startEnvelopeFollower();
  }

  disable(): void {
    if (this.enabled) {
      this.enabled = false;
      this.stopEnvelopeFollower();
      this.safeDisconnect(this.speechBandHi);
      this.safeDisconnect(this.speechBandPeak);
      this.safeDisconnect(this.speechBandLo);
      this.safeDisconnect(this.restNotch);
      this.safeDisconnect(this.restGain);
      this.safeDisconnect(this.wetGain);
      this.safeDisconnect(this.analyser ?? undefined);
      this.analyser = null;
    }
    // Pass-through wiring
    this.safeDisconnect(this.input);
    this.input.connect(this.bypassGain);
    this.bypassGain.gain.value = 1.0;
    this.bypassGain.connect(this.output);
  }

  get node(): AudioNode {
    return this.output;
  }

  destroy(): void {
    this.stopEnvelopeFollower();
    this.safeDisconnect(this.input);
    this.safeDisconnect(this.bypassGain);
    this.safeDisconnect(this.speechBandHi);
    this.safeDisconnect(this.speechBandPeak);
    this.safeDisconnect(this.speechBandLo);
    this.safeDisconnect(this.restNotch);
    this.safeDisconnect(this.restGain);
    this.safeDisconnect(this.wetGain);
    this.safeDisconnect(this.output);
    this.safeDisconnect(this.analyser ?? undefined);
    this.analyser = null;
  }

  private startEnvelopeFollower(): void {
    if (!this.analyser) return;
    const a = this.analyser;
    const buf = this.buffer;
    const tick = () => {
      if (!this.enabled || !this.analyser) return;
      a.getFloatTimeDomainData(buf);
      // RMS energy of the speech band
      let sum = 0;
      for (let i = 0; i < buf.length; i++) sum += buf[i] * buf[i];
      const rms = Math.sqrt(sum / buf.length);
      // Map RMS [0..0.3] → ducking [1.0 .. 0.55].
      const duck = Math.max(0.55, 1.0 - rms * 1.5);
      // Smooth via Web Audio's built-in time constant.
      this.restGain.gain.setTargetAtTime(duck, this.ctx.currentTime, 0.05);
      this.envelopeRaf = requestAnimationFrame(tick);
    };
    this.envelopeRaf = requestAnimationFrame(tick);
  }

  private stopEnvelopeFollower(): void {
    if (this.envelopeRaf !== null) {
      cancelAnimationFrame(this.envelopeRaf);
      this.envelopeRaf = null;
    }
    // Restore rest gain so a re-enable starts clean.
    this.restGain.gain.value = 1.0;
  }

  private safeDisconnect(n?: AudioNode): void {
    if (!n) return;
    try { n.disconnect(); } catch { /* ignore */ }
  }
}
