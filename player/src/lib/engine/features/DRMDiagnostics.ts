/**
 * DRMDiagnostics — observes shaka DRM events and pushes a summary into
 * the `drmDiagnostics` store for the DRMDiagnosticsOverlay UI.
 *
 * Tracks:
 *   - keySystem (com.widevine.alpha, com.apple.fps, com.microsoft.playready)
 *   - keyStatuses (per-key 'usable' | 'output-restricted' | 'expired' | ...)
 *   - last 10 errors with timestamp
 *   - license request time (when measurable)
 *   - robustness level the platform actually granted
 *
 * Wave 2 C4. Always-on (cheap) — overlay decides whether to render.
 */

import { drmDiagnostics, type DRMDiagnosticsState } from '../../stores/drm';

export class DRMDiagnostics {
  private state: DRMDiagnosticsState = {
    keySystem: '',
    keyStatuses: {},
    errors: [],
    lastEventAt: 0,
  };
  private licenseRequestStart: number | null = null;

  constructor(private player: any) {}

  attach(): void {
    if (!this.player?.addEventListener) return;
    this.player.addEventListener('drmsessionupdate', () => this.onSessionUpdate());
    this.player.addEventListener('keystatuschanged', (e: any) => this.onKeyStatus(e));
    this.player.addEventListener('error', (e: any) => this.onError(e?.detail));

    // Hook the license request via shaka network filter.
    try {
      const NetEngineRequestType = (globalThis as any).shaka?.net?.NetworkingEngine?.RequestType;
      const LICENSE = NetEngineRequestType?.LICENSE ?? 2;
      const ne = this.player.getNetworkingEngine?.();
      if (ne) {
        ne.registerRequestFilter((type: number) => {
          if (type === LICENSE) this.licenseRequestStart = performance.now();
        });
        ne.registerResponseFilter((type: number) => {
          if (type === LICENSE && this.licenseRequestStart !== null) {
            this.state.licenseRequestTimeMs = performance.now() - this.licenseRequestStart;
            this.licenseRequestStart = null;
            this.touch('license');
          }
        });
      }
    } catch (err) {
      console.warn('[LWP] DRMDiagnostics: license filter setup failed:', err);
    }

    // Initial probe — fill keySystem if shaka already knows it.
    try {
      const drmInfo = this.player.drmInfo?.();
      if (drmInfo?.keySystem) {
        this.state.keySystem = drmInfo.keySystem;
        this.touch('initial');
      }
    } catch { /* ignore */ }
  }

  detach(): void {
    drmDiagnostics.set(null);
  }

  private onSessionUpdate(): void {
    try {
      const info = this.player.drmInfo?.();
      if (info?.keySystem) this.state.keySystem = info.keySystem;
      const sessions = this.player.getActiveSessionsMetadata?.();
      if (Array.isArray(sessions) && sessions.length > 0) {
        this.state.robustnessAchieved =
          sessions[0].videoRobustness ||
          sessions[0].audioRobustness ||
          this.state.robustnessAchieved;
      }
    } catch { /* ignore */ }
    this.touch('session');
  }

  private onKeyStatus(_evt: any): void {
    try {
      const map = this.player.getKeyStatuses?.();
      if (!map) return;
      const out: Record<string, string> = {};
      // shaka returns either Map or plain object depending on version
      if (typeof map.forEach === 'function') {
        map.forEach((status: string, keyId: string) => {
          out[keyId] = status;
        });
      } else if (typeof map === 'object') {
        for (const k of Object.keys(map)) out[k] = map[k];
      }
      this.state.keyStatuses = out;
    } catch { /* ignore */ }
    this.touch('keystatus');
  }

  private onError(detail: any): void {
    if (!detail) return;
    const code = detail.code;
    // Shaka error category 6 = DRM (see shaka.util.Error.Category.DRM = 6).
    if (detail.category !== 6 && detail.category !== 'DRM') return;
    const message = String(detail.message || `code ${code}`);
    this.state.errors.unshift({ code, message, at: Date.now() });
    if (this.state.errors.length > 10) this.state.errors.length = 10;
    this.touch('error');
  }

  private touch(_reason: string): void {
    this.state.lastEventAt = Date.now();
    drmDiagnostics.set({ ...this.state, keyStatuses: { ...this.state.keyStatuses }, errors: [...this.state.errors] });
  }
}
