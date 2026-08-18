/**
 * TrickplayManifest — sprite/I-frame thumbnail discovery for the timeline
 * preview (Wave 1 B2).
 *
 * Three sources, in priority order:
 *  1. Shaka image tracks (`player.getImageTracks()` → `getThumbnails(id, t)`).
 *     Covers HLS `EXT-X-IMAGE-STREAM-INF` and DASH `<AdaptationSet
 *     contentType="image">` natively. This is by far the most reliable.
 *  2. External VTT sprite playlist hinted by the play element. Format:
 *     each cue points at `image.jpg#xywh=X,Y,W,H` for a single thumbnail.
 *  3. Manual HLS I-frame playlist parsing (fallback for streams that
 *     declare `#EXT-X-I-FRAMES-ONLY` without an image track).
 *
 * The detect() function returns a `ThumbnailManifest` ready to feed the
 * `thumbnailManifest` store so ThumbnailPreview.svelte can render
 * sprite-fragment backgrounds instead of canvas-extracted frames.
 */

import type { ThumbnailManifest, ThumbnailCue, ThumbnailSheet } from '../core/types';

declare const shaka: any;

export class TrickplayManifest {
  /**
   * Probe a player + manifest URL for a thumbnail manifest.
   * Returns null if no thumbnails are available.
   */
  static async detect(player: any, manifestUri: string): Promise<ThumbnailManifest | null> {
    // 0. lampac-go server-side trickplay for /transcoding/ sessions. The
    //    server generates the sprite lazily (sparse ffmpeg seeks, ~30-90s),
    //    so this kicks generation and long-polls until ready. Our transcoded
    //    HLS never carries image tracks, so this is authoritative for these
    //    URLs — skip the other probes.
    const tp = manifestUri.match(/^(.*)\/transcoding\/([^/]+)\/(?:main|master)\.m3u8/i);
    if (tp) {
      try {
        return await this.fromLampacTranscoding(`${tp[1]}/transcoding/${tp[2]}/trickplay`);
      } catch (err) {
        console.warn('[LWP] lampac trickplay probe failed:', err);
        return null;
      }
    }

    // 1. Shaka image tracks (preferred)
    try {
      const shakaManifest = await this.fromShaka(player);
      if (shakaManifest) return shakaManifest;
    } catch (err) {
      console.warn('[LWP] Shaka image-track probe failed:', err);
    }

    // 2/3. Manual HLS parsing — only attempt for plain m3u8 URLs.
    if (/\.m3u8(\?|$)/i.test(manifestUri)) {
      try {
        const hls = await this.fromHlsImagePlaylist(manifestUri);
        if (hls) return hls;
      } catch (err) {
        console.warn('[LWP] HLS image playlist parse failed:', err);
      }
    }

    return null;
  }

  /**
   * Poll the lampac-go trickplay endpoint until the sprite sheet is ready,
   * then convert its grid manifest into a ThumbnailManifest. The first GET
   * starts server-side generation; `ready:false, failed:false` means "in
   * progress" — retry on a slow cadence. Gives up after ~4 minutes or when
   * the server reports a permanent failure.
   */
  private static async fromLampacTranscoding(url: string): Promise<ThumbnailManifest | null> {
    const POLL_MS = 15_000;
    const MAX_ATTEMPTS = 16;
    for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
      const resp = await fetch(url);
      if (!resp.ok) return null;
      const m = await resp.json();
      if (m?.failed) return null;
      if (m?.ready && m.spriteUrl && m.count > 0 && m.interval > 0) {
        const sheet: ThumbnailSheet = {
          url: m.spriteUrl,
          cols: m.tileCols || 10,
          rows: m.tileRows || Math.ceil(m.count / (m.tileCols || 10)),
          cellW: m.thumbWidth || 320,
          cellH: m.thumbHeight || 180,
        };
        const cues: ThumbnailCue[] = [];
        for (let i = 0; i < m.count; i++) {
          cues.push({
            start: i * m.interval,
            end: (i + 1) * m.interval,
            sheetIndex: 0,
            cellIndex: i, // row-major grid — matches ThumbnailPreview's math
          });
        }
        return { type: 'vtt-sprite', spriteSheets: [sheet], cues };
      }
      await new Promise((r) => setTimeout(r, POLL_MS));
    }
    return null;
  }

  /**
   * Materialize a thumbnail manifest from Shaka's parsed image tracks.
   * We pre-resolve thumbnail metadata for every cue point so the hover
   * path is synchronous (no async fetches during scrub).
   */
  private static async fromShaka(player: any): Promise<ThumbnailManifest | null> {
    if (!player?.getImageTracks) return null;
    const tracks = player.getImageTracks() || [];
    if (tracks.length === 0) return null;

    // Pick the highest-resolution image track available.
    const track = tracks.reduce(
      (best: any, t: any) => (!best || (t.height || 0) > (best.height || 0) ? t : best),
      null,
    );
    if (!track) return null;

    const duration = isFinite(player.seekRange?.()?.end ?? NaN)
      ? player.seekRange().end
      : (player.getMediaElement?.()?.duration ?? 0);
    if (!isFinite(duration) || duration <= 0) return null;

    // Sample at fixed intervals (8s default — typical I-frame cadence).
    const SAMPLE_STEP = 8;
    const cues: ThumbnailCue[] = [];
    const sheetsByUri = new Map<string, { index: number; sheet: ThumbnailSheet }>();

    for (let t = 0; t < duration; t += SAMPLE_STEP) {
      let meta: any = null;
      try {
        meta = await player.getThumbnails(track.id, t);
      } catch {
        continue;
      }
      if (!meta) continue;
      // Shaka returns { startTime, duration, height, positionX, positionY, width, uris }
      const uri = (meta.uris && meta.uris[0]) || '';
      if (!uri) continue;

      let sheetEntry = sheetsByUri.get(uri);
      if (!sheetEntry) {
        const sheet: ThumbnailSheet = {
          url: uri,
          // We don't actually need cols/rows since cue coords are pixel-exact.
          cols: 1,
          rows: 1,
          cellW: meta.width || 160,
          cellH: meta.height || 90,
        };
        sheetEntry = { index: sheetsByUri.size, sheet };
        sheetsByUri.set(uri, sheetEntry);
      }

      cues.push({
        start: meta.startTime,
        end: meta.startTime + (meta.duration || SAMPLE_STEP),
        sheetIndex: sheetEntry.index,
        // We encode pixel coords into cellIndex by storing the index of
        // a derived cue map below; instead expose positionX/Y inline by
        // reusing cellIndex as a packed integer (X * 100000 + Y).
        cellIndex: ((meta.positionX || 0) << 16) | (meta.positionY & 0xffff),
      });

      // Also remember pixel sizing on the sheet entry — use largest seen.
      sheetEntry.sheet.cellW = Math.max(sheetEntry.sheet.cellW, meta.width || 0);
      sheetEntry.sheet.cellH = Math.max(sheetEntry.sheet.cellH, meta.height || 0);
    }

    if (cues.length === 0) return null;
    const sheets = Array.from(sheetsByUri.values())
      .sort((a, b) => a.index - b.index)
      .map((e) => e.sheet);

    return {
      type: 'shaka-image',
      spriteSheets: sheets,
      cues,
    };
  }

  /**
   * Parse an external HLS playlist that contains `#EXT-X-IMAGE-STREAM-INF`
   * or DASH-style `tile` thumbnail playlists. Fallback for legacy streams
   * not exposed via shaka's image-track API.
   */
  private static async fromHlsImagePlaylist(masterUri: string): Promise<ThumbnailManifest | null> {
    let resp: Response;
    try {
      resp = await fetch(masterUri, { signal: AbortSignal.timeout(8000) });
      if (!resp.ok) return null;
    } catch {
      return null;
    }
    const text = await resp.text();

    // Extract image-stream-inf URI lines.
    const lines = text.split(/\r?\n/);
    let imageUri: string | null = null;
    for (let i = 0; i < lines.length; i++) {
      if (lines[i].startsWith('#EXT-X-IMAGE-STREAM-INF') && i + 1 < lines.length) {
        imageUri = lines[i + 1].trim();
        break;
      }
    }
    if (!imageUri) return null;

    const absUrl = new URL(imageUri, masterUri).toString();
    let imgResp: Response;
    try {
      imgResp = await fetch(absUrl, { signal: AbortSignal.timeout(8000) });
      if (!imgResp.ok) return null;
    } catch {
      return null;
    }
    const imgText = await imgResp.text();

    return this.parseHlsImageMediaPlaylist(imgText, absUrl);
  }

  /**
   * Parse a media playlist body containing `#EXTINF` segments that point
   * at sprite sheets. Each segment URI is a sheet, and each sprite is
   * laid out left-to-right, top-to-bottom inside it.
   *
   * Public so it can be unit-tested cheaply.
   */
  static parseHlsImageMediaPlaylist(text: string, baseUri: string): ThumbnailManifest | null {
    const lines = text.split(/\r?\n/);
    const sheets: ThumbnailSheet[] = [];
    const cues: ThumbnailCue[] = [];

    let currentDuration = 0;
    let currentTilesPerImage: { cols: number; rows: number; w: number; h: number } | null = null;
    let acc = 0;

    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];
      if (line.startsWith('#EXT-X-TILES')) {
        // EXAMPLE: #EXT-X-TILES:RESOLUTION=320x180,LAYOUT=10x10,DURATION=8.0
        const layoutMatch = /LAYOUT=(\d+)x(\d+)/.exec(line);
        const resMatch = /RESOLUTION=(\d+)x(\d+)/.exec(line);
        const durMatch = /DURATION=([\d.]+)/.exec(line);
        if (layoutMatch && resMatch) {
          currentTilesPerImage = {
            cols: parseInt(layoutMatch[1], 10),
            rows: parseInt(layoutMatch[2], 10),
            w: parseInt(resMatch[1], 10),
            h: parseInt(resMatch[2], 10),
          };
          if (durMatch) currentDuration = parseFloat(durMatch[1]);
        }
      } else if (line.startsWith('#EXTINF')) {
        const m = /^#EXTINF:([\d.]+)/.exec(line);
        if (m) currentDuration = parseFloat(m[1]);
      } else if (line && !line.startsWith('#')) {
        if (!currentTilesPerImage || currentDuration <= 0) continue;
        const sheetUrl = new URL(line.trim(), baseUri).toString();
        const sheet: ThumbnailSheet = {
          url: sheetUrl,
          cols: currentTilesPerImage.cols,
          rows: currentTilesPerImage.rows,
          cellW: currentTilesPerImage.w,
          cellH: currentTilesPerImage.h,
        };
        const sheetIndex = sheets.length;
        sheets.push(sheet);

        const tilesInSheet = sheet.cols * sheet.rows;
        const perTileDur = currentDuration / tilesInSheet;
        for (let cell = 0; cell < tilesInSheet; cell++) {
          cues.push({
            start: acc + cell * perTileDur,
            end: acc + (cell + 1) * perTileDur,
            sheetIndex,
            cellIndex: cell,
          });
        }
        acc += currentDuration;
      }
    }

    if (cues.length === 0) return null;
    return { type: 'hls-iframe', spriteSheets: sheets, cues };
  }

  /**
   * Find the cue covering a given playback time. Used by ThumbnailPreview.
   * Linear scan is fine — typical manifests have <1000 cues.
   */
  static cueAt(manifest: ThumbnailManifest, time: number): ThumbnailCue | null {
    for (const c of manifest.cues) {
      if (time >= c.start && time < c.end) return c;
    }
    // Fallback: nearest-by-start (handles tiny gaps).
    let best: ThumbnailCue | null = null;
    let bestDelta = Infinity;
    for (const c of manifest.cues) {
      const d = Math.abs(c.start - time);
      if (d < bestDelta) { best = c; bestDelta = d; }
    }
    return best;
  }
}
