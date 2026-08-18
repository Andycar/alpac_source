/**
 * BufferStrategy — owns every shaka.player.configure({ streaming, abr, manifest }) call.
 *
 * Why a dedicated module? Two reasons:
 *  1. Keeps ABR/buffer logic out of CorePlayback so we can A/B profiles cleanly.
 *  2. Avoids cycles with TrackManager: TrackManager calls
 *     `buffer.setAbrEnabled(false)` instead of touching `player.configure`
 *     directly. Only this module writes streaming/abr config.
 *
 * Profiles map roughly to:
 *   - vod-hd     : default for VOD up to 1080p
 *   - vod-4k     : 4K HEVC/AV1 (deep buffer)
 *   - live       : live HLS/DASH (low latency)
 *   - tv-low     : Smart TV (Tizen/WebOS) — small buffer, modest goals
 *   - savedata   : Save-Data header set OR battery < 20% on battery — cap 720p
 *   - auto       : let pick() infer from active variant + NetworkProfile
 */

import type { NetworkProfile } from './types';

export type BufferProfile =
  | 'vod-hd'
  | 'vod-4k'
  | 'live'
  | 'tv-low'
  | 'savedata'
  | 'auto';

const RETRY = {
  maxAttempts: 5,
  baseDelay: 500,
  backoffFactor: 2,
  fuzzFactor: 0.5,
};

interface ProfileConfig {
  streaming: {
    bufferingGoal: number;
    rebufferingGoal: number;
    bufferBehind: number;
    retryParameters: typeof RETRY;
    lowLatencyMode?: boolean;
  };
  abr: {
    enabled: boolean;
    defaultBandwidthEstimate: number;
    restrictions?: {
      maxHeight?: number;
      maxBandwidth?: number;
    };
  };
  manifest: { retryParameters: typeof RETRY };
}

const PROFILES: Record<Exclude<BufferProfile, 'auto'>, ProfileConfig> = {
  'vod-hd': {
    streaming: {
      bufferingGoal: 30,
      rebufferingGoal: 2,
      bufferBehind: 60,
      retryParameters: RETRY,
    },
    abr: { enabled: true, defaultBandwidthEstimate: 10_000_000 },
    manifest: { retryParameters: RETRY },
  },
  'vod-4k': {
    streaming: {
      bufferingGoal: 60,
      rebufferingGoal: 4,
      bufferBehind: 90,
      retryParameters: RETRY,
    },
    abr: { enabled: true, defaultBandwidthEstimate: 25_000_000 },
    manifest: { retryParameters: RETRY },
  },
  'live': {
    streaming: {
      bufferingGoal: 6,
      rebufferingGoal: 1,
      bufferBehind: 30,
      retryParameters: RETRY,
      lowLatencyMode: true,
    },
    abr: { enabled: true, defaultBandwidthEstimate: 5_000_000 },
    manifest: { retryParameters: RETRY },
  },
  'tv-low': {
    streaming: {
      bufferingGoal: 12,
      rebufferingGoal: 1,
      bufferBehind: 30,
      retryParameters: RETRY,
    },
    abr: {
      enabled: true,
      defaultBandwidthEstimate: 5_000_000,
      restrictions: { maxHeight: 1080 },
    },
    manifest: { retryParameters: RETRY },
  },
  'savedata': {
    streaming: {
      bufferingGoal: 15,
      rebufferingGoal: 1.5,
      bufferBehind: 30,
      retryParameters: RETRY,
    },
    abr: {
      enabled: true,
      defaultBandwidthEstimate: 1_500_000,
      restrictions: { maxHeight: 720, maxBandwidth: 2_500_000 },
    },
    manifest: { retryParameters: RETRY },
  },
};

export interface PickContext {
  isLive?: boolean;
  activeHeight?: number;
  isLowPowerTV?: boolean;
  network?: NetworkProfile;
}

export class BufferStrategy {
  private current: BufferProfile = 'vod-hd';

  constructor(private player: any) {}

  /** Apply a profile by name. Idempotent — re-applies if called twice. */
  apply(profile: BufferProfile, ctx?: PickContext): void {
    const resolved = profile === 'auto' ? this.pick(ctx ?? {}) : profile;
    const cfg = PROFILES[resolved];
    if (!cfg || !this.player) return;
    try {
      this.player.configure({
        streaming: cfg.streaming,
        abr: cfg.abr,
        manifest: cfg.manifest,
      });
      this.current = resolved;
    } catch (err) {
      console.warn('[LWP] BufferStrategy.apply failed:', err);
    }
  }

  /**
   * Pick a profile from runtime context. Used when caller passes 'auto'.
   * Preference order: tv-low → live → savedata → vod-4k → vod-hd.
   */
  pick(ctx: PickContext): Exclude<BufferProfile, 'auto'> {
    if (ctx.isLowPowerTV) return 'tv-low';
    if (ctx.isLive) return 'live';
    if (ctx.network?.saveData) return 'savedata';
    if (
      ctx.network &&
      isFinite(ctx.network.batteryLevel) &&
      ctx.network.batteryLevel < 0.2 &&
      !ctx.network.batteryCharging
    ) {
      return 'savedata';
    }
    if (ctx.activeHeight && ctx.activeHeight >= 2160) return 'vod-4k';
    return 'vod-hd';
  }

  /**
   * Toggle ABR. Used by TrackManager.selectQuality() — when the user picks
   * a specific track we disable ABR so shaka stops auto-switching.
   * When set back to 'auto' TrackManager calls setAbrEnabled(true).
   */
  setAbrEnabled(enabled: boolean): void {
    if (!this.player) return;
    try {
      this.player.configure('abr.enabled', enabled);
    } catch (err) {
      console.warn('[LWP] setAbrEnabled failed:', err);
    }
  }

  /** Cap maximum height (used by adaptive battery/saveData logic externally). */
  setMaxHeight(height: number | null): void {
    if (!this.player) return;
    try {
      this.player.configure(
        'abr.restrictions.maxHeight',
        height === null ? Infinity : height,
      );
    } catch (err) {
      console.warn('[LWP] setMaxHeight failed:', err);
    }
  }

  get profile(): BufferProfile {
    return this.current;
  }
}
