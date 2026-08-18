/**
 * Ambilight color sample store — populated by AmbilightLayer (Wave 1 B3).
 * Each sample is the smoothed dominant color of one of 4 video edges.
 */

import { writable } from 'svelte/store';
import type { AmbilightColors } from '../engine/core/types';

const initial: AmbilightColors = {
  top: 'rgb(0,0,0)',
  right: 'rgb(0,0,0)',
  bottom: 'rgb(0,0,0)',
  left: 'rgb(0,0,0)',
  accent: 'rgb(0,0,0)',
};

export const ambilightColors = writable<AmbilightColors>(initial);
