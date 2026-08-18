/**
 * Track stores — quality, audio, text, autoQuality.
 *
 * Split out of stores/player.ts. Re-exported from there for back-compat.
 */

import { writable } from 'svelte/store';
import type { QualityTrack, AudioTrack, TextTrack } from '../engine/core/types';

export const qualityTracks = writable<QualityTrack[]>([]);
export const audioTracks = writable<AudioTrack[]>([]);
export const textTracks = writable<TextTrack[]>([]);
export const autoQuality = writable(true);
