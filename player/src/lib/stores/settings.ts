import { writable } from 'svelte/store';

export interface PlayerSettings {
  volume: number;
  muted: boolean;
  playbackRate: number;
  subtitleFontSize: number; // 0.5 - 2.0 multiplier
  subtitleColor: string;
  subtitleBgOpacity: number; // 0 - 1
  preferredQuality: 'auto' | 'highest' | number; // height or 'auto'/'highest'
  preferredAudioLang: string;
  preferredSubLang: string;
  // Per-balancer quality memory
  qualityPerSource: Record<string, 'auto' | 'highest' | number>;
  // Skip intro
  skipIntroEnabled: boolean;
  skipIntroDuration: number; // seconds to skip
  skipIntroWindow: number;   // show button for first N seconds
  // Audio processing
  audioBoost: number;        // 1.0 - 3.0
  audioCompressor: 'off' | 'light' | 'heavy';
  // Subtitle preset
  subtitlePreset: string;    // 'contrast' | 'netflix' | 'cinema' | 'minimal'
  // Long-press speed boost (0 = disabled)
  longPressSpeedBoost: number;
  // Next episode auto-play
  autoNextEpisode: boolean;
  autoNextCountdown: number; // seconds
  // Mini player
  miniPlayerEnabled: boolean;

  // --- Wave 1 additions ---
  /** Pre-attach next episode for zero-gap transitions (B1). */
  enablePreload: boolean;
  /** Trigger preload when remaining playback time drops below this (sec). */
  preloadStartSeconds: number;
  /** Show chapter markers on timeline (B2). */
  showChapterMarkers: boolean;
  /** Use sprite-based thumbnails when manifest provides them (B2). */
  useExternalThumbnails: boolean;
  /** Ambilight intensity (B3). */
  ambilightMode: 'off' | 'subtle' | 'rich';
  /** UI density mode (B3). */
  density: 'compact' | 'normal' | 'spacious';
  /** Active visual theme (B3). */
  themeName: 'cinematic' | 'minimal' | 'classic' | 'oled-black';
  /** Edge glow halo around video (B3). */
  edgeGlow: boolean;
  /** Voice boost (300-3400Hz bandpass + ducker, B4). */
  voiceBoost: boolean;
  /** EBU R128 loudness target ('off' = disabled). */
  loudnessTarget: 'off' | -23 | -16 | -14;
  /** HRTF surround for headphones (B4). */
  hrtfEnabled: boolean;

  // --- Wave 2 additions ---
  /**
   * Legacy: used to gate a separate CMCD upload that was never implemented.
   * CMCD now rides on the proxied segment requests themselves and is read
   * server-side (internal/cmcd), so this flag controls nothing.
   */
  shareCmcd: boolean;
  /** Cap quality at 720p when battery <20% / saveData=true (C1). */
  batteryAdaptiveCap: boolean;
  /** Auto-follow host time in watchparty room (C2). */
  watchpartyAutoFollowHost: boolean;
  /** Drift threshold (ms) before applying remote sync (C2). */
  watchpartyDriftThresholdMs: number;
  /** Enable WASM dav1d / libde265 fallback decoders (C3). */
  enableWasmCodecs: boolean;
  /** Preferred DRM robustness level (C4). */
  drmRobustness: 'auto' | 'sw' | 'hw';
  /** Persist DRM licenses across sessions (C4). */
  persistentLicenses: boolean;

  // --- Pack 3 polish ---
  /** Subtitle vertical position. */
  subtitlePosition: 'bottom' | 'middle' | 'top';
  /** Subtitle vertical offset in vh units (-30..30). */
  subtitleOffset: number;
  /** Manual accent color override ('' = use auto-extracted from poster). */
  accentOverride: string;
  /** Preferred audio output device id (set by user via picker). */
  audioOutputDeviceId: string;
}

const STORAGE_KEY = 'lwp_settings';

const defaults: PlayerSettings = {
  volume: 1,
  muted: false,
  playbackRate: 1,
  subtitleFontSize: 1,
  subtitleColor: '#ffffff',
  subtitleBgOpacity: 0.75,
  preferredQuality: 'highest',
  preferredAudioLang: '',
  preferredSubLang: '',
  qualityPerSource: {},
  skipIntroEnabled: true,
  skipIntroDuration: 90,
  skipIntroWindow: 180,
  audioBoost: 1.0,
  audioCompressor: 'off',
  subtitlePreset: 'contrast',
  longPressSpeedBoost: 2.0,
  autoNextEpisode: true,
  autoNextCountdown: 5,
  miniPlayerEnabled: true,

  // Wave 1
  enablePreload: true,
  preloadStartSeconds: 30,
  showChapterMarkers: true,
  useExternalThumbnails: true,
  // Ambilight is OFF by default — it reads video frames via canvas
  // (drawImage + getImageData), which on some GPUs / streams stalls
  // the decode pipeline and causes stuttering. Opt-in from Settings.
  ambilightMode: 'off',
  density: 'normal',
  themeName: 'cinematic',
  edgeGlow: false,
  voiceBoost: false,
  loudnessTarget: 'off',
  hrtfEnabled: false,

  // Wave 2
  shareCmcd: false,
  batteryAdaptiveCap: true,
  watchpartyAutoFollowHost: true,
  watchpartyDriftThresholdMs: 2000,
  enableWasmCodecs: false,
  drmRobustness: 'auto',
  persistentLicenses: false,

  // Pack 3
  subtitlePosition: 'bottom',
  subtitleOffset: 0,
  accentOverride: '',
  audioOutputDeviceId: '',
};

/**
 * Settings migrations — applied after loading from storage. Each entry
 * is keyed by a version token that's written back alongside the user
 * settings. If the token is absent or older than CURRENT_VERSION, the
 * listed migration fns run once and the token is bumped.
 *
 * Current migrations:
 *   v2 — force `ambilightMode: 'off'` for users whose settings still
 *        have the old default `'subtle'`. The old default stalled video
 *        playback on slow GPUs (~1.5s GPU readback cycle); the new
 *        default is 'off' but stored values override defaults, so
 *        existing users had to opt in again. This migration resets it
 *        so the fix reaches everyone.
 */
const CURRENT_SETTINGS_VERSION = 2;

function applyMigrations(s: any): PlayerSettings {
  const fromVersion = typeof s.__version === 'number' ? s.__version : 0;

  if (fromVersion < 2) {
    // Stale ambilight default → reset to off. If the user actually
    // picked 'rich' themselves we leave it alone; only the exact legacy
    // default 'subtle' is reset.
    if (s.ambilightMode === 'subtle') s.ambilightMode = 'off';
    // Stale edgeGlow default (previously true, now false).
    if (s.edgeGlow === true) s.edgeGlow = false;
  }

  s.__version = CURRENT_SETTINGS_VERSION;
  return { ...defaults, ...s } as PlayerSettings;
}

function loadSettings(): PlayerSettings {
  try {
    // Try Lampa.Storage first
    const lampa = (window as any).Lampa;
    if (lampa?.Storage) {
      const saved = lampa.Storage.get(STORAGE_KEY, '');
      if (saved) return applyMigrations(JSON.parse(saved));
    }
    // Fallback to localStorage
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) return applyMigrations(JSON.parse(raw));
  } catch {
    // ignore
  }
  return { ...defaults, __version: CURRENT_SETTINGS_VERSION } as PlayerSettings;
}

function saveSettings(s: PlayerSettings): void {
  const json = JSON.stringify(s);
  try {
    const lampa = (window as any).Lampa;
    if (lampa?.Storage) {
      lampa.Storage.set(STORAGE_KEY, json);
    }
    localStorage.setItem(STORAGE_KEY, json);
  } catch {
    // ignore
  }
}

export const settings = writable<PlayerSettings>(loadSettings());

// Auto-persist on change
settings.subscribe((s) => saveSettings(s));

export function updateSetting<K extends keyof PlayerSettings>(
  key: K,
  value: PlayerSettings[K],
): void {
  settings.update((s) => ({ ...s, [key]: value }));
}

/** Get remembered quality for a specific balancer/source */
export function getQualityForSource(source: string): 'auto' | 'highest' | number | null {
  if (!source) return null;
  let s: PlayerSettings | undefined;
  settings.subscribe((v) => (s = v))();
  return s?.qualityPerSource?.[source] ?? null;
}

/** Remember quality selection for a specific balancer/source */
export function setQualityForSource(source: string, quality: 'auto' | 'highest' | number): void {
  if (!source) return;
  settings.update((s) => ({
    ...s,
    qualityPerSource: { ...s.qualityPerSource, [source]: quality },
  }));
}
