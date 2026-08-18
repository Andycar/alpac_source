/**
 * CastManager — Remote Playback API + AirPlay support.
 *
 * Uses the standard Remote Playback API (Chrome 55+, Samsung Internet)
 * for Chromecast and similar devices.
 * AirPlay is handled natively by Safari via `x-webkit-airplay="allow"`
 * attribute on the <video> element.
 */

export class CastManager {
  private video: HTMLVideoElement;
  private remote: any = null;
  private _available = false;
  private _connected = false;
  private watchId: number | null = null;

  onAvailabilityChange?: (available: boolean) => void;
  onConnectionChange?: (connected: boolean) => void;

  constructor(video: HTMLVideoElement) {
    this.video = video;
  }

  async init(): Promise<void> {
    // Ensure remote playback is not disabled
    this.video.disableRemotePlayback = false;

    // Check Remote Playback API support
    if (!('remote' in this.video)) {
      // Check Safari AirPlay
      if ('webkitShowPlaybackTargetPicker' in this.video) {
        this._available = true;
        this.onAvailabilityChange?.(true);
      }
      return;
    }

    this.remote = (this.video as any).remote;

    // Watch for cast device availability
    try {
      this.watchId = await this.remote.watchAvailability((available: boolean) => {
        this._available = available;
        this.onAvailabilityChange?.(available);
      });
    } catch {
      // watchAvailability not supported — assume available if API exists
      this._available = true;
      this.onAvailabilityChange?.(true);
    }

    // Connection state events
    this.remote.onconnecting = () => {
      console.log('[LWP] Cast: connecting...');
    };
    this.remote.onconnect = () => {
      this._connected = true;
      this.onConnectionChange?.(true);
      console.log('[LWP] Cast: connected');
    };
    this.remote.ondisconnect = () => {
      this._connected = false;
      this.onConnectionChange?.(false);
      console.log('[LWP] Cast: disconnected');
    };
  }

  /**
   * Open the cast device picker dialog.
   * For Safari, shows the AirPlay picker.
   * For Chrome, shows the Remote Playback device picker.
   */
  async prompt(): Promise<void> {
    // Safari AirPlay
    if (!this.remote && 'webkitShowPlaybackTargetPicker' in this.video) {
      (this.video as any).webkitShowPlaybackTargetPicker();
      return;
    }

    if (!this.remote) return;

    try {
      await this.remote.prompt();
    } catch (err: any) {
      // User cancelled or no devices found
      if (err.name !== 'NotAllowedError') {
        console.warn('[LWP] Cast prompt failed:', err);
      }
    }
  }

  get available(): boolean {
    return this._available;
  }

  get connected(): boolean {
    return this._connected;
  }

  destroy(): void {
    if (this.remote && this.watchId !== null) {
      try {
        this.remote.cancelWatchAvailability(this.watchId);
      } catch {
        // ignore
      }
    }
    this.remote = null;
    this._available = false;
    this._connected = false;
  }
}
