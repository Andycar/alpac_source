/**
 * PlayElementMapper — pure function that converts Lampa's raw play() argument
 * into our strongly-typed PlayElement.
 *
 * Extracted from LampaBridge.handlePlay() so that:
 *   1. The bridge is easier to unit-test (mapper is dependency-free).
 *   2. Future playback sources (watchparty, remote-triggered) can reuse it.
 *
 * Lampa's play() argument is loose — historically it accepted both
 * `quality` and the legacy `qualitys` spelling, and carries a mix of
 * balancer/plugin fields. This mapper normalizes everything.
 */

import type { PlayElement } from '../engine/core/types';

export function mapLampaToPlayElement(raw: any): PlayElement {
  if (!raw) {
    return { url: '' };
  }
  return {
    url: raw.url || '',
    title: raw.title || '',
    quality: raw.quality || raw.qualitys,
    subtitles: raw.subtitles,
    playlist: raw.playlist,
    season: raw.season,
    episode: raw.episode,
    voice_name: raw.voice_name,
    voices: raw.voices,
    voice_index: raw.voice_index,
    headers: raw.headers,
    timeline: raw.timeline,
    poster: raw.poster,
    thumbnail: raw.thumbnail,
    hls_manifest_timeout: raw.hls_manifest_timeout,
    callback: raw.callback,
    // Prefer `plugin` (balancer identifier set by backends) over `source`.
    source: raw.plugin || raw.source || '',
    drm: raw.drm,
  };
}
