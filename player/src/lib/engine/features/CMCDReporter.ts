/**
 * CMCDReporter — turns on Common Media Client Data telemetry in shaka.
 *
 * CMCD is an industry-standard set of headers / query params describing what
 * the player is doing right now: buffer fullness, measured throughput, the
 * bitrate it settled on, and whether it just ran dry.
 *
 * The consumer is OUR server, not the CDN. Our proxy reads the CMCD-* headers
 * off each segment request, aggregates them per balancer (internal/cmcd) and
 * strips them before fetching upstream — so a strict origin never sees a header
 * it did not expect. There is no separate upload: the telemetry rides on the
 * request the player was making anyway, which is also why it keeps working on
 * TVs where an extra POST would be one more thing to fail.
 *
 * Privacy:
 *   - `contentId` is a 32-bit hash of the URL (NEVER the URL itself).
 *   - `sessionId` is a per-tab UUID — not tied to any user account.
 *   - The server keys its aggregate by (source, platform) and stores no ids.
 */

import type { EventBus } from '../core/EventBus';
import type { EngineEvents } from '../core/types';

export interface CMCDOptions {
  /** Shaka requires version 1 (header mode is the safer default). */
  version?: number;
  /** Use header mode (true, default) or query-param mode (false). */
  useHeaders?: boolean;
}

export class CMCDReporter {
  private sessionId: string;
  private contentId = '';

  constructor(private player: any, private bus: EventBus<EngineEvents>) {
    this.sessionId = makeSessionId();
  }

  /**
   * Configure shaka with CMCD enabled. Idempotent.
   * Returns true if shaka accepted the config (some older versions don't
   * support cmcd config at all).
   */
  enable(opts?: CMCDOptions): boolean {
    if (!this.player?.configure) return false;
    try {
      this.player.configure({
        cmcd: {
          enabled: true,
          version: opts?.version ?? 1,
          useHeaders: opts?.useHeaders ?? true,
          sessionId: this.sessionId,
          contentId: this.contentId,
        },
      });
      return true;
    } catch (err) {
      // Older shaka uses streaming.cmcd, try that instead.
      try {
        this.player.configure({
          streaming: {
            cmcd: {
              enabled: true,
              version: opts?.version ?? 1,
              useHeaders: opts?.useHeaders ?? true,
              sessionId: this.sessionId,
              contentId: this.contentId,
            },
          },
        });
        return true;
      } catch (err2) {
        console.warn('[LWP] CMCD configure failed (both shapes):', err, err2);
        return false;
      }
    }
  }

  /** Update the content ID (called on each load with a fresh hash). */
  setContent(url: string): void {
    this.contentId = hashContentId(url);
    // Re-apply config so shaka picks up the new contentId mid-session.
    this.enable();
    // Emit so listeners (e.g. opt-in upload) can record the change.
    this.bus.emit('cmcd_emit', {
      keys: { sid: this.sessionId, cid: this.contentId, ev: 'content' },
    });
  }

  disable(): void {
    if (!this.player?.configure) return;
    try {
      this.player.configure({ cmcd: { enabled: false } });
    } catch { /* ignore */ }
    try {
      this.player.configure({ streaming: { cmcd: { enabled: false } } });
    } catch { /* ignore */ }
  }
}

function makeSessionId(): string {
  // crypto.randomUUID is in all modern browsers; fall back if missing.
  const c: any = (globalThis as any).crypto;
  if (c?.randomUUID) return c.randomUUID();
  // RFC 4122 v4-ish fallback
  let s = '';
  for (let i = 0; i < 32; i++) {
    if (i === 8 || i === 12 || i === 16 || i === 20) s += '-';
    s += Math.floor(Math.random() * 16).toString(16);
  }
  return s;
}

/**
 * Tiny non-cryptographic hash of the URL — used as opaque contentId.
 * 32-bit FNV-1a, hex-encoded. Keeps the URL out of logs without
 * shipping the WebCrypto SubtleCrypto async ceremony just for this.
 */
function hashContentId(url: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < url.length; i++) {
    h ^= url.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return ('00000000' + (h >>> 0).toString(16)).slice(-8);
}
