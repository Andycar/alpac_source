/**
 * TranscodingOrchestrator — single entry point for everything related to
 * server-side transcoding fallback.
 *
 * Wraps:
 *   - CodecProbe (browser codec capabilities)
 *   - TranscodingFallback.checkAndStartTranscoding + startHeartbeat (single)
 *   - BatchTranscoding.BatchSession + canBatchTranscode (series)
 *
 * Owns the active heartbeat lifecycle so callers (CorePlayback) only see
 * a clean isTranscoding() flag and stop() method.
 */

import { codecProbe, type CodecSupport } from '../CodecProbe';
import {
  checkAndStartTranscoding,
  startHeartbeat,
  type TranscodingResult,
} from '../TranscodingFallback';
import {
  BatchSession,
  canBatchTranscode,
  type BatchEpisode,
} from '../BatchTranscoding';

export class TranscodingOrchestrator {
  private codecSupport: CodecSupport | null = null;
  private heartbeat: { stop: () => void } | null = null;
  private _isTranscoding = false;
  private batchSession: BatchSession | null = null;

  /** Lazily probe codecs on first use. */
  ensureCodecSupport(): CodecSupport {
    if (!this.codecSupport) this.codecSupport = codecProbe();
    return this.codecSupport;
  }

  get isTranscoding(): boolean {
    return this._isTranscoding;
  }

  get batch(): BatchSession | null {
    return this.batchSession;
  }

  /**
   * Probe a media URL and start single-stream transcoding if needed.
   * Returns the transcoded URL + subtitles, or null when no transcoding
   * was needed. Caller is responsible for replacing the load() URL.
   */
  async maybeStart(mediaUrl: string): Promise<TranscodingResult | null> {
    this.stopHeartbeat();
    this._isTranscoding = false;

    const support = this.ensureCodecSupport();
    const transcoded = await checkAndStartTranscoding(mediaUrl, support);
    if (!transcoded) return null;

    this._isTranscoding = true;
    this.heartbeat = startHeartbeat(transcoded.streamId);
    console.log('[LWP] Transcoding started:', transcoded.streamId);
    return transcoded;
  }

  /**
   * Start batch transcoding for a series playlist.
   * Returns the BatchSession (so caller can drive episode pre-loading) or
   * null if batching not applicable for this list.
   */
  async startBatch(
    episodes: BatchEpisode[],
    opts?: { subtitles?: boolean },
  ): Promise<BatchSession | null> {
    this.stopBatchSession();
    if (!canBatchTranscode(episodes)) return null;

    const session = new BatchSession(episodes, opts);
    const result = await session.start();
    if (!result) return null;

    this.batchSession = session;
    console.log('[LWP] Batch session started:', result.batchId, 'episodes:', result.episodeCount);
    return session;
  }

  /**
   * Mark current playback as using a batch playlist URL — ensures
   * single-heartbeat is stopped (batch manages its own per-episode HB).
   */
  beginBatchEpisode(): void {
    this.stopHeartbeat();
    this._isTranscoding = true;
  }

  stopHeartbeat(): void {
    if (this.heartbeat) {
      this.heartbeat.stop();
      this.heartbeat = null;
    }
    this._isTranscoding = false;
  }

  stopBatchSession(): void {
    if (this.batchSession) {
      this.batchSession.stop();
      this.batchSession = null;
    }
  }

  destroy(): void {
    this.stopHeartbeat();
    this.stopBatchSession();
  }
}
