/**
 * Playback stores — engine, time, dur, playing, buffering, volume, muted,
 * playbackRate, bufferedEnd, playlist, playlistIndex.
 *
 * Split out of the original stores/player.ts. The legacy player.ts file
 * re-exports everything from here so existing imports keep working.
 *
 * Track stores live in ./tracks.ts and cast in ./cast.ts.
 */

import { writable, derived } from 'svelte/store';
import type { PlaybackEngine } from '../engine/PlaybackEngine';
import type { PlayElement } from '../engine/core/types';

// Core playback state
export const engine = writable<PlaybackEngine | null>(null);
export const currentElement = writable<PlayElement | null>(null);
export const currentTime = writable(0);
export const duration = writable(0);
export const playing = writable(false);
export const buffering = writable(false);
export const volume = writable(1);
export const muted = writable(false);
export const playbackRate = writable(1);

// Buffered ranges
export const bufferedEnd = writable(0);

// Playlist
export const playlist = writable<PlayElement[]>([]);
export const playlistIndex = writable(0);

// Derived
export const progress = derived(
  [currentTime, duration],
  ([$currentTime, $duration]) => ($duration > 0 ? ($currentTime / $duration) * 100 : 0),
);

export const bufferedProgress = derived(
  [bufferedEnd, duration],
  ([$bufferedEnd, $duration]) => ($duration > 0 ? ($bufferedEnd / $duration) * 100 : 0),
);

export const hasNext = derived(
  [playlist, playlistIndex],
  ([$playlist, $idx]) => $idx < $playlist.length - 1,
);

export const hasPrev = derived(
  [playlistIndex],
  ([$idx]) => $idx > 0,
);
