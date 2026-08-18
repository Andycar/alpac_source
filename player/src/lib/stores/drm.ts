/**
 * DRM diagnostics store — populated by DRMDiagnostics.ts (Wave 2 C4).
 * DRMDiagnosticsOverlay subscribes; null when no DRM session active.
 */

import { writable } from 'svelte/store';

export interface DRMDiagnosticsState {
  keySystem: string;
  robustnessAchieved?: string;
  licenseRequestTimeMs?: number;
  keyStatuses: Record<string, string>;
  errors: Array<{ code?: string | number; message: string; at: number }>;
  lastEventAt: number;
}

export const drmDiagnostics = writable<DRMDiagnosticsState | null>(null);
