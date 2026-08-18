/**
 * MediaFeatures — small video-element utilities that don't fit elsewhere:
 *   - togglePiP
 *   - takeScreenshot
 *   - waitForCanPlay (used before play() to avoid stuck frames)
 *   - scheduleStuckCheck / nudgePlayback (HLS pipeline kick)
 *
 * Refactored out of PlaybackEngine.ts (lines 469-528, 816-834).
 */

export class MediaFeatures {
  private stuckCheckTimer: ReturnType<typeof setTimeout> | null = null;
  private destroyed = false;

  constructor(private videoEl: HTMLVideoElement) {}

  /** Wait until the video element has enough decoded data to begin playback. */
  waitForCanPlay(timeoutMs: number): Promise<void> {
    if (this.videoEl.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) {
      return Promise.resolve();
    }
    return new Promise<void>((resolve) => {
      let done = false;
      const finish = () => {
        if (done) return;
        done = true;
        this.videoEl.removeEventListener('canplay', finish);
        clearTimeout(timer);
        resolve();
      };
      this.videoEl.addEventListener('canplay', finish);
      const timer = setTimeout(finish, timeoutMs);
    });
  }

  /**
   * After play() resolves, verify playback is actually progressing.
   * Some HLS streams stall silently — the decode pipeline needs a nudge.
   */
  scheduleStuckCheck(): void {
    this.cancelStuckCheck();
    const t0 = this.videoEl.currentTime;
    this.stuckCheckTimer = setTimeout(() => {
      this.stuckCheckTimer = null;
      if (this.destroyed || this.videoEl.paused || this.videoEl.ended) return;
      if (this.videoEl.seeking) return;
      const t1 = this.videoEl.currentTime;
      if (
        Math.abs(t1 - t0) < 0.05 &&
        this.videoEl.readyState >= HTMLMediaElement.HAVE_CURRENT_DATA
      ) {
        console.log('[LWP] Playback stuck after play(), nudging pipeline');
        this.nudgePlayback();
      }
    }, 800);
  }

  cancelStuckCheck(): void {
    if (this.stuckCheckTimer) {
      clearTimeout(this.stuckCheckTimer);
      this.stuckCheckTimer = null;
    }
  }

  /** Force the decode pipeline to re-evaluate by briefly toggling playbackRate. */
  nudgePlayback(): void {
    const rate = this.videoEl.playbackRate;
    this.videoEl.playbackRate = rate + 0.1;
    requestAnimationFrame(() => {
      this.videoEl.playbackRate = rate;
    });
  }

  async togglePiP(): Promise<void> {
    if (document.pictureInPictureElement) {
      await document.exitPictureInPicture();
    } else if ((this.videoEl as any).requestPictureInPicture) {
      await (this.videoEl as any).requestPictureInPicture();
    }
  }

  takeScreenshot(): string | null {
    const canvas = document.createElement('canvas');
    canvas.width = this.videoEl.videoWidth;
    canvas.height = this.videoEl.videoHeight;
    const ctx = canvas.getContext('2d');
    if (!ctx) return null;
    try {
      ctx.drawImage(this.videoEl, 0, 0);
      return canvas.toDataURL('image/png');
    } catch {
      // CORS-tainted (DRM stream) — drawImage throws
      return null;
    }
  }

  destroy(): void {
    this.destroyed = true;
    this.cancelStuckCheck();
  }
}
