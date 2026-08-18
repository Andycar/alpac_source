/**
 * Trickplay thumbnail manifest store — populated by TrickplayManifest
 * (Wave 1 B2). When non-null, ThumbnailPreview.svelte renders sprites
 * from the sheet instead of canvas-extracting frames.
 */

import { writable } from 'svelte/store';
import type { ThumbnailManifest } from '../engine/core/types';

export const thumbnailManifest = writable<ThumbnailManifest | null>(null);
