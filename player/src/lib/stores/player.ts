/**
 * stores/player — re-exports the split stores so existing imports
 * (`import { engine, currentTime, ... } from '../stores/player'`) keep
 * resolving without changes.
 *
 * After Phase A4 the actual writable definitions live in:
 *   - playback.ts  — engine, time, dur, playing, buffering, volume,
 *                    muted, playbackRate, bufferedEnd, playlist,
 *                    playlistIndex, derived: progress/hasNext/hasPrev
 *   - tracks.ts    — qualityTracks, audioTracks, textTracks, autoQuality
 *   - cast.ts      — castAvailable, castConnected
 */

export * from './playback';
export * from './tracks';
export * from './cast';

import { get } from 'svelte/store';
import type { PlaybackEngine } from '../engine/PlaybackEngine';
import {
  engine,
  currentTime,
  duration,
  playing,
  buffering,
  bufferedEnd,
} from './playback';
import { qualityTracks, audioTracks, textTracks } from './tracks';

/**
 * Bind engine events to the split stores. Call once after engine.init().
 *
 * The signature and behavior are identical to the pre-refactor version
 * so Player.svelte keeps working unchanged.
 */
export function bindEngineToStores(eng: PlaybackEngine): void {
  engine.set(eng);

  eng.on('timeupdate', () => {
    currentTime.set(eng.currentTime);
    duration.set(eng.duration);

    const buf = eng.buffered;
    if (buf.length > 0) {
      bufferedEnd.set(buf.end(buf.length - 1));
    }
  });

  eng.on('playing', () => playing.set(true));
  eng.on('paused', () => playing.set(false));
  eng.on('buffering', (isBuffering: boolean) => buffering.set(isBuffering));
  eng.on('ended', () => playing.set(false));

  eng.on('trackschanged', () => {
    qualityTracks.set(eng.getQualityTracks());
    audioTracks.set(eng.getAudioTracks());
    textTracks.set(eng.getTextTracks());
  });

  eng.on('qualitychanged', () => {
    qualityTracks.set(eng.getQualityTracks());
  });

  eng.on('loaded', () => {
    duration.set(eng.duration);
    qualityTracks.set(eng.getQualityTracks());
    audioTracks.set(eng.getAudioTracks());
    textTracks.set(eng.getTextTracks());
  });
}

// Re-export helper used internally so callers that imported it via
// `from '../stores/player'` keep working.
export { get };
