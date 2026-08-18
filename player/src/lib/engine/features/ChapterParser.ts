/**
 * ChapterParser — extracts chapter markers (Wave 1 B2).
 *
 * Sources:
 *  1. Shaka chapter API when available (player.getChapters('lang') in
 *     Shaka 4.7+) — uses ID3 / EMSG / MP4 chapter atoms when present.
 *  2. Manifest text fallback — parse `#EXT-X-DATERANGE:CLASS="..."` from
 *     HLS playlists. We look for the iTunes/Apple chapter convention
 *     (`com.apple.hls.chapter`) but also accept any DATERANGE that has
 *     `X-CHAPTER-TITLE`.
 *
 * Returns chapters sorted by start time. Empty array means "no chapters".
 */

import type { Chapter } from '../core/types';

export class ChapterParser {
  static fromShaka(player: any, durationSec: number): Chapter[] {
    if (!player) return [];

    // Try common language codes; some manifests label chapters with a
    // specific language tag, others use "und". We try a few in order.
    const langs = ['', 'en', 'und', 'ru', 'uk'];
    for (const lang of langs) {
      try {
        const raw = lang
          ? player.getChapters?.(lang)
          : player.getChapters?.();
        if (Array.isArray(raw) && raw.length > 0) {
          return normalizeShaka(raw, durationSec);
        }
      } catch {
        // some shaka versions throw on missing track — try next lang
      }
    }
    return [];
  }

  static async fromHlsManifest(masterUri: string): Promise<Chapter[]> {
    let resp: Response;
    try {
      resp = await fetch(masterUri, { signal: AbortSignal.timeout(8000) });
      if (!resp.ok) return [];
    } catch {
      return [];
    }
    const text = await resp.text();
    return this.parseHlsDateRange(text);
  }

  /**
   * Pure parser exposed for tests. Walks the manifest collecting
   * `#EXT-X-DATERANGE` tags that look like chapters.
   */
  static parseHlsDateRange(text: string): Chapter[] {
    const lines = text.split(/\r?\n/);
    const chapters: Chapter[] = [];
    // We need a reference start time for offset math. The first DATERANGE
    // with START-DATE establishes the playlist origin.
    let originMs: number | null = null;

    for (const line of lines) {
      if (!line.startsWith('#EXT-X-DATERANGE:')) continue;
      const attrs = parseHlsAttrList(line.substring('#EXT-X-DATERANGE:'.length));

      const cls = attrs['CLASS'] || '';
      const titleAttr = attrs['X-CHAPTER-TITLE'] || attrs['X-TITLE'] || '';
      // Accept either Apple chapters or anything with an explicit title.
      if (cls !== 'com.apple.hls.chapter' && !titleAttr) continue;

      const startDateStr = attrs['START-DATE'];
      const durStr = attrs['DURATION'] || attrs['PLANNED-DURATION'];
      if (!startDateStr) continue;

      const startMs = Date.parse(startDateStr);
      if (!isFinite(startMs)) continue;
      if (originMs === null) originMs = startMs;

      const start = (startMs - originMs) / 1000;
      const dur = durStr ? parseFloat(durStr) : 0;
      const title = (titleAttr || cls.split('.').pop() || `Chapter ${chapters.length + 1}`).trim();

      chapters.push({
        start: Math.max(0, start),
        end: dur > 0 ? start + dur : Infinity,
        title,
        class: cls || undefined,
      });
    }

    // Resolve open-ended end times to the next chapter's start.
    chapters.sort((a, b) => a.start - b.start);
    for (let i = 0; i < chapters.length; i++) {
      if (!isFinite(chapters[i].end)) {
        chapters[i].end = i + 1 < chapters.length ? chapters[i + 1].start : chapters[i].start + 60;
      }
    }
    return chapters;
  }

  /** Find the chapter containing a given playback time, or null. */
  static activeAt(chapters: Chapter[], time: number): Chapter | null {
    for (const c of chapters) {
      if (time >= c.start && time < c.end) return c;
    }
    return null;
  }
}

function normalizeShaka(raw: any[], durationSec: number): Chapter[] {
  const chapters: Chapter[] = raw
    .map((c) => ({
      start: typeof c.startTime === 'number' ? c.startTime : 0,
      end: typeof c.endTime === 'number' ? c.endTime : 0,
      title: String(c.title || c.id || 'Chapter'),
    }))
    .filter((c) => isFinite(c.start) && c.start >= 0)
    .sort((a, b) => a.start - b.start);

  // Ensure end times are sane.
  for (let i = 0; i < chapters.length; i++) {
    if (!chapters[i].end || chapters[i].end <= chapters[i].start) {
      chapters[i].end = i + 1 < chapters.length
        ? chapters[i + 1].start
        : Math.max(chapters[i].start + 60, durationSec);
    }
  }
  return chapters;
}

/**
 * Tiny attribute-list parser for HLS tag bodies. Handles quoted strings
 * containing commas. Not a full HLS parser, but enough for DATERANGE.
 */
function parseHlsAttrList(body: string): Record<string, string> {
  const out: Record<string, string> = {};
  let i = 0;
  while (i < body.length) {
    // skip whitespace
    while (i < body.length && body[i] === ' ') i++;
    // read key
    const keyStart = i;
    while (i < body.length && body[i] !== '=') i++;
    if (i >= body.length) break;
    const key = body.substring(keyStart, i).trim();
    i++; // skip '='
    let value: string;
    if (body[i] === '"') {
      i++;
      const valStart = i;
      while (i < body.length && body[i] !== '"') i++;
      value = body.substring(valStart, i);
      i++; // skip closing '"'
    } else {
      const valStart = i;
      while (i < body.length && body[i] !== ',') i++;
      value = body.substring(valStart, i).trim();
    }
    out[key] = value;
    if (body[i] === ',') i++;
  }
  return out;
}
