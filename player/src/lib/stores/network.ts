/**
 * NetworkProfile store — populated by NetworkProfile.ts (Wave 2 C1).
 * Read by BufferStrategy.pick() and CMCDReporter to drive adaptive buffer.
 */

import { writable } from 'svelte/store';
import type { NetworkProfile } from '../engine/core/types';

const initial: NetworkProfile = {
  effectiveType: 'unknown',
  downlinkMbps: 0,
  rttMs: 0,
  saveData: false,
  batteryLevel: NaN,
  batteryCharging: true,
};

export const networkProfile = writable<NetworkProfile>(initial);
