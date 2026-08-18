/**
 * BatchTranscoding — pre-transcode multiple episodes at once.
 *
 * When a series episode needs transcoding, this module:
 * 1. Sends the full episode list to /transcoding/batch
 * 2. Server starts transcoding first N episodes concurrently
 * 3. Returns per-episode stream IDs and playlist URLs
 * 4. As user switches episodes, notifies server to pre-load next batch
 * 5. Single heartbeat keeps all jobs alive
 *
 * Usage:
 *   const batch = new BatchSession(episodes, { subtitles: true });
 *   await batch.start();
 *   const info = await batch.getEpisode(0); // { playlistUrl, subtitlesUrl, streamId }
 *   batch.setCurrentEpisode(1); // pre-loads next episodes
 *   batch.stop(); // cleanup
 */

export interface BatchEpisode {
  url: string;
  title?: string;
  audioIndex?: number;
  headers?: Record<string, string>;
}

export interface BatchEpisodeInfo {
  index: number;
  state: string; // "pending" | "starting" | "running" | "ready" | "error"
  streamId?: string;
  playlistUrl?: string;
  subtitlesUrl?: string;
  duration?: number;
  error?: string;
  title?: string;
}

export interface BatchStartResponse {
  batchId: string;
  masterUrl: string;
  statusUrl: string;
  episodeCount: number;
  episodes: BatchEpisodeInfo[];
}

export interface BatchStatusResponse {
  batchId: string;
  currentIndex: number;
  episodes: BatchEpisodeInfo[];
}

export class BatchSession {
  private batchId: string | null = null;
  private episodes: BatchEpisode[];
  private subtitles: boolean;
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null;
  private _active = false;

  constructor(episodes: BatchEpisode[], opts?: { subtitles?: boolean }) {
    this.episodes = episodes;
    this.subtitles = opts?.subtitles ?? true;
  }

  get active(): boolean {
    return this._active;
  }

  get id(): string | null {
    return this.batchId;
  }

  /**
   * Start batch transcoding. Sends episode list to server,
   * server begins transcoding first N episodes.
   */
  async start(): Promise<BatchStartResponse | null> {
    try {
      const body = {
        episodes: this.episodes.map((ep) => ({
          url: ep.url,
          title: ep.title || '',
          audioIndex: ep.audioIndex || 0,
          headers: ep.headers || {},
        })),
        subtitles: this.subtitles,
      };

      const resp = await fetch('/transcoding/batch', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(15000),
      });

      if (!resp.ok) {
        console.warn('[LWP] batch start failed:', resp.status);
        return null;
      }

      const data: BatchStartResponse = await resp.json();
      this.batchId = data.batchId;
      this._active = true;

      // Start heartbeat
      this.startHeartbeat();

      console.log('[LWP] Batch transcoding started:', data.batchId, 'episodes:', data.episodeCount);
      return data;
    } catch (e) {
      console.warn('[LWP] batch start error:', e);
      return null;
    }
  }

  /**
   * Get current status of a specific episode.
   * If the episode is already transcoding/ready, returns its info.
   * Also notifies the server that we're watching this episode.
   */
  async getEpisode(index: number): Promise<BatchEpisodeInfo | null> {
    if (!this.batchId) return null;

    try {
      const resp = await fetch(`/transcoding/batch/${this.batchId}/episode/${index}`, {
        method: 'POST',
        signal: AbortSignal.timeout(10000),
      });

      if (!resp.ok) return null;
      return await resp.json();
    } catch {
      return null;
    }
  }

  /**
   * Notify server that user is now watching episode at index.
   * Server will pre-load upcoming episodes.
   */
  async setCurrentEpisode(index: number): Promise<BatchEpisodeInfo | null> {
    return this.getEpisode(index);
  }

  /**
   * Get full batch status with all episodes.
   */
  async getStatus(): Promise<BatchStatusResponse | null> {
    if (!this.batchId) return null;

    try {
      const resp = await fetch(`/transcoding/batch/${this.batchId}/status`, {
        signal: AbortSignal.timeout(10000),
      });
      if (!resp.ok) return null;
      return await resp.json();
    } catch {
      return null;
    }
  }

  /**
   * Wait for a specific episode to be ready (running or ready state).
   * Polls every 2 seconds, timeout after maxWaitMs.
   */
  async waitForEpisode(index: number, maxWaitMs = 60000): Promise<BatchEpisodeInfo | null> {
    const start = Date.now();

    while (Date.now() - start < maxWaitMs) {
      const info = await this.getEpisode(index);
      if (!info) return null;

      if (info.state === 'running' || info.state === 'ready') {
        return info;
      }

      if (info.state === 'error') {
        console.warn('[LWP] batch episode error:', info.error);
        return null;
      }

      // Wait 2 seconds before polling again
      await new Promise((r) => setTimeout(r, 2000));
    }

    console.warn('[LWP] batch episode timeout waiting for index:', index);
    return null;
  }

  /**
   * Stop all batch transcoding.
   */
  stop(): void {
    this._active = false;
    this.stopHeartbeat();

    if (this.batchId) {
      fetch(`/transcoding/batch/${this.batchId}/stop`).catch(() => {});
      this.batchId = null;
    }
  }

  private startHeartbeat(): void {
    this.stopHeartbeat();
    this.heartbeatTimer = setInterval(async () => {
      if (!this.batchId) return;
      try {
        await fetch(`/transcoding/batch/${this.batchId}/heartbeat`, {
          signal: AbortSignal.timeout(5000),
        });
      } catch {
        // Non-critical
      }
    }, 10000);
  }

  private stopHeartbeat(): void {
    if (this.heartbeatTimer) {
      clearInterval(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
  }
}

/**
 * Check if an array of episodes can benefit from batch transcoding.
 * Returns true if there are 2+ episodes and any of them are non-HLS/DASH URLs.
 */
export function canBatchTranscode(episodes: BatchEpisode[]): boolean {
  if (!episodes || episodes.length < 2) return false;
  return episodes.some((ep) => !/\.(m3u8|mpd)(\?|$)/i.test(ep.url));
}
