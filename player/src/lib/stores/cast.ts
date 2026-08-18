/**
 * Cast/AirPlay stores — split out of stores/player.ts.
 */

import { writable } from 'svelte/store';

export const castAvailable = writable(false);
export const castConnected = writable(false);
