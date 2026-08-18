/**
 * Chapter markers store — populated by ChapterParser (Wave 1 B2).
 * Timeline.svelte renders ticks for each entry.
 */

import { writable } from 'svelte/store';
import type { Chapter } from '../engine/core/types';

export const chapterMarkers = writable<Chapter[]>([]);

/** Currently active chapter (the one containing currentTime), or null. */
export const activeChapter = writable<Chapter | null>(null);
