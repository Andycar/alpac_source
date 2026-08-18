/**
 * Shared engine types — extracted from PlaybackEngine for the core split.
 *
 * Keep this file dependency-free (no shaka, no DOM mutation). Only TS types.
 */

import type { DRMOptions } from '../DRMConfig';

// ---------------------------------------------------------------------------
// PlayElement — the unit of "what to play". Mapped from a Lampa play() arg.
// ---------------------------------------------------------------------------

export interface PlayElement {
  url: string;
  title?: string;
  quality?: Record<string, string>;
  subtitles?: Array<{ language: string; url: string; label?: string }>;
  playlist?: PlayElement[];
  season?: number;
  episode?: number;
  voice_name?: string;
  voices?: Array<{ name: string; index: number }>;
  voice_index?: number;
  headers?: Record<string, string>;
  timeline?: number | { time?: number; duration?: number; percent?: number };
  poster?: string;
  thumbnail?: string;
  hls_manifest_timeout?: number;
  callback?: () => void;
  /** Balancer/source identifier for per-source settings */
  source?: string;
  /** DRM fields (optional, passed from server) */
  drm?: DRMOptions;
}

// ---------------------------------------------------------------------------
// Track types
// ---------------------------------------------------------------------------

export interface QualityTrack {
  id: number;
  label: string;
  height: number;
  bandwidth: number;
  active: boolean;
}

export interface AudioTrack {
  id: number;
  label: string;
  language: string;
  active: boolean;
}

export interface TextTrack {
  id: number;
  label: string;
  language: string;
  active: boolean;
}

// ---------------------------------------------------------------------------
// Chapters & thumbnails (Wave 1 — feature stores will use these)
// ---------------------------------------------------------------------------

export interface Chapter {
  start: number;
  end: number;
  title: string;
  class?: string;
}

export interface ThumbnailSheet {
  url: string;
  cols: number;
  rows: number;
  cellW: number;
  cellH: number;
}

export interface ThumbnailCue {
  start: number;
  end: number;
  sheetIndex: number;
  cellIndex: number;
}

export interface ThumbnailManifest {
  type: 'hls-iframe' | 'dash-tiled' | 'vtt-sprite' | 'shaka-image';
  spriteSheets?: ThumbnailSheet[];
  cues: ThumbnailCue[];
}

// ---------------------------------------------------------------------------
// Ambilight color sample (4 edges + dominant accent)
// ---------------------------------------------------------------------------

export interface AmbilightColors {
  top: string;
  right: string;
  bottom: string;
  left: string;
  accent: string;
}

// ---------------------------------------------------------------------------
// Watchparty
// ---------------------------------------------------------------------------

export type PartyCmd =
  | { type: 'time'; t: number; sentAt: number; src: string }
  | { type: 'play'; t: number; sentAt: number; src: string }
  | { type: 'pause'; t: number; sentAt: number; src: string }
  | { type: 'seek'; t: number; sentAt: number; src: string }
  | { type: 'episode'; index: number; url: string; sentAt: number; src: string }
  | { type: 'hello'; nick: string; peers: number; src: string }
  | { type: 'bye'; nick: string; peers: number; src: string }
  | { type: 'chat'; text: string; nick: string; sentAt: number; src: string };

/** Single chat message rendered by the WatchpartyPanel. */
export interface ChatMessage {
  id: string;
  text: string;
  nick: string;
  /** True when the message originated from the local peer. */
  self: boolean;
  at: number;
}

// ---------------------------------------------------------------------------
// Network/Battery profile (Wave 2 — adaptive buffer)
// ---------------------------------------------------------------------------

export interface NetworkProfile {
  effectiveType: '2g' | '3g' | '4g' | '5g' | 'slow-2g' | 'unknown';
  downlinkMbps: number;
  rttMs: number;
  saveData: boolean;
  batteryLevel: number; // 0..1, NaN if unknown
  batteryCharging: boolean;
}

// ---------------------------------------------------------------------------
// Engine event map — typed payloads for EventBus<EngineEvents>
// ---------------------------------------------------------------------------

export interface EngineEvents {
  // Core lifecycle
  loaded: void;
  playing: void;
  paused: void;
  seeking: void;
  seeked: void;
  timeupdate: { currentTime: number; duration: number };
  ended: void;
  error: { code?: string | number; severity?: number; data?: unknown };
  buffering: boolean;
  qualitychanged: { height?: number; bandwidth?: number };
  trackschanged: void;

  // Wave 1
  preload_ready: { episodeIndex: number };
  chapter_enter: { chapter: Chapter };
  chapter_exit: { chapter: Chapter };
  ambilight_sample: { colors: AmbilightColors };

  // Wave 2
  party_sync: { sourcePeer: string; cmd: PartyCmd };
  cmcd_emit: { keys: Record<string, string | number> };
  drm_event: { kind: 'license' | 'key' | 'error'; detail: unknown };
}

export type EngineEvent = keyof EngineEvents;
