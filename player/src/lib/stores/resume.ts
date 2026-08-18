/**
 * Resume prompt store — set by Player.svelte when a saved playback
 * position is detected; consumed by ResumePrompt.svelte to display
 * the "Continue from N:NN?" overlay.
 *
 * Lives outside the .svelte file so other components / tests can import
 * the writable directly (Svelte 5 doesn't allow non-component exports
 * from .svelte modules).
 */

import { writable } from 'svelte/store';

export const pendingResume = writable<number | null>(null);
