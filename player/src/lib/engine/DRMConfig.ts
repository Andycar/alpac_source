/**
 * DRM configuration for shaka-player.
 * Widevine (Chrome, Firefox, Android) + FairPlay (Safari) + ClearKey.
 *
 * Wave 2 C4 additions: robustness levels, session type, persistent
 * licenses. Fields are additive — legacy {widevineUrl, fairplayUrl, ...}
 * callers keep working untouched.
 */

export type RobustnessLevel =
  | 'SW_SECURE_CRYPTO'
  | 'SW_SECURE_DECODE'
  | 'HW_SECURE_CRYPTO'
  | 'HW_SECURE_DECODE'
  | 'HW_SECURE_ALL';

export interface DRMOptions {
  widevineUrl?: string;
  fairplayUrl?: string;
  fairplayCertUrl?: string;
  clearkeys?: Record<string, string>;

  // --- Wave 2 C4 ---
  /** Per-track robustness hints. Values are the EME strings. */
  robustness?: {
    audio?: RobustnessLevel | '';
    video?: RobustnessLevel | '';
  };
  /** Prefer persistent licenses (requires shaka offline storage setup). */
  persistentLicense?: boolean;
  /** Shaka sessionType override ('temporary' | 'persistent-license'). */
  sessionType?: 'temporary' | 'persistent-license';
  /** Any extra headers to attach to the license request. */
  licenseHeaders?: Record<string, string>;
}

export function buildDRMConfig(opts: DRMOptions): Record<string, any> {
  const servers: Record<string, string> = {};
  if (opts.widevineUrl) servers['com.widevine.alpha'] = opts.widevineUrl;
  if (opts.fairplayUrl) servers['com.apple.fps'] = opts.fairplayUrl;

  const drm: Record<string, any> = {};
  if (Object.keys(servers).length > 0) drm.servers = servers;
  if (opts.clearkeys && Object.keys(opts.clearkeys).length > 0) {
    drm.clearKeys = opts.clearkeys;
  }

  // Robustness — shaka uses `drm.advanced[keySystem].{audio,video}Robustness`.
  // We populate both Widevine + PlayReady + FairPlay slots when present.
  const ra = opts.robustness?.audio;
  const rv = opts.robustness?.video;
  if (ra || rv) {
    drm.advanced = drm.advanced || {};
    for (const ks of ['com.widevine.alpha', 'com.microsoft.playready', 'com.apple.fps']) {
      drm.advanced[ks] = drm.advanced[ks] || {};
      if (ra) drm.advanced[ks].audioRobustness = ra;
      if (rv) drm.advanced[ks].videoRobustness = rv;
    }
  }

  // Session type (persistent license vs temporary).
  if (opts.persistentLicense || opts.sessionType === 'persistent-license') {
    drm.advanced = drm.advanced || {};
    for (const ks of ['com.widevine.alpha', 'com.microsoft.playready', 'com.apple.fps']) {
      drm.advanced[ks] = drm.advanced[ks] || {};
      drm.advanced[ks].sessionType = 'persistent-license';
      drm.advanced[ks].persistentStateRequired = true;
    }
  } else if (opts.sessionType === 'temporary') {
    drm.advanced = drm.advanced || {};
    for (const ks of ['com.widevine.alpha', 'com.microsoft.playready', 'com.apple.fps']) {
      drm.advanced[ks] = drm.advanced[ks] || {};
      drm.advanced[ks].sessionType = 'temporary';
    }
  }

  const config: Record<string, any> = {};
  if (Object.keys(drm).length > 0) config.drm = drm;

  // License request headers are applied via shaka's network filter, not
  // drm config directly. We expose them via config.drm.headers so the
  // caller (CorePlayback) can register a RequestFilter.
  if (opts.licenseHeaders) {
    config.drm = config.drm || {};
    config.drm.__licenseHeaders = opts.licenseHeaders;
  }

  return config;
}

/**
 * Probe the browser for supported robustness levels. Returns the highest
 * level that `navigator.requestMediaKeySystemAccess` accepts for the
 * given key system. Used by DRMDiagnostics to avoid asking for
 * HW_SECURE_ALL on devices that don't support it (shaka would throw).
 */
export async function probeRobustness(
  keySystem: string,
  preferHardware: boolean,
): Promise<RobustnessLevel | null> {
  if (typeof navigator === 'undefined' || !navigator.requestMediaKeySystemAccess) {
    return null;
  }
  const order: RobustnessLevel[] = preferHardware
    ? ['HW_SECURE_ALL', 'HW_SECURE_DECODE', 'HW_SECURE_CRYPTO', 'SW_SECURE_DECODE', 'SW_SECURE_CRYPTO']
    : ['SW_SECURE_DECODE', 'SW_SECURE_CRYPTO'];

  for (const level of order) {
    try {
      const cfg: any = [{
        initDataTypes: ['cenc'],
        videoCapabilities: [{
          contentType: 'video/mp4;codecs="avc1.42E01E"',
          robustness: level,
        }],
      }];
      const access = await navigator.requestMediaKeySystemAccess(keySystem, cfg);
      if (access) return level;
    } catch {
      // try next level
    }
  }
  return null;
}
