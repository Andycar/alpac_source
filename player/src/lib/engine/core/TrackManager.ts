/**
 * TrackManager — quality / audio / text track listing & selection.
 *
 * Refactored out of PlaybackEngine.ts (lines 583-687, 367-429).
 * Calls BufferStrategy.setAbrEnabled() instead of touching player.configure
 * directly to avoid duplication of streaming/abr config sources.
 */

import type { BufferStrategy } from './BufferStrategy';
import type { QualityTrack, AudioTrack, TextTrack, PlayElement } from './types';

declare const shaka: any;

export class TrackManager {
  private currentElement: PlayElement | null = null;

  constructor(
    private player: any,
    private buffer: BufferStrategy,
  ) {}

  setCurrentElement(el: PlayElement | null): void {
    this.currentElement = el;
  }

  // --- Quality ---

  quality(): QualityTrack[] {
    if (!this.player?.getVariantTracks) return [];
    const tracks = this.player.getVariantTracks();
    const seen = new Map<number, QualityTrack>();

    for (const t of tracks) {
      const h = t.height || 0;
      const existing = seen.get(h);
      if (!existing || (t.bandwidth || 0) > (existing.bandwidth || 0)) {
        seen.set(h, {
          id: t.id,
          label: qualityLabel(h),
          height: h,
          bandwidth: t.bandwidth || 0,
          active: t.active || false,
        });
      }
    }

    return Array.from(seen.values()).sort((a, b) => b.height - a.height);
  }

  selectQuality(trackId: number | 'auto'): void {
    if (!this.player) return;
    if (trackId === 'auto') {
      this.buffer.setAbrEnabled(true);
      return;
    }
    this.buffer.setAbrEnabled(false);
    const tracks = this.player.getVariantTracks();
    const target = tracks.find((t: any) => t.id === trackId);
    if (target) this.player.selectVariantTrack(target, true);
  }

  applyPreferredQuality(pref: 'auto' | 'highest' | number): void {
    if (!this.player) return;
    const tracks = this.quality();
    if (tracks.length === 0) return;

    if (pref === 'auto') {
      this.buffer.setAbrEnabled(true);
      return;
    }

    if (pref === 'highest') {
      this.buffer.setAbrEnabled(false);
      const best = tracks[0]; // sorted desc by height
      if (best) {
        const all = this.player.getVariantTracks();
        const target = all.find((t: any) => t.id === best.id);
        if (target) this.player.selectVariantTrack(target, true);
      }
      return;
    }

    // pref is a specific height
    this.buffer.setAbrEnabled(false);
    const match = tracks.find((t) => t.height === pref) || tracks[0];
    if (match) {
      const all = this.player.getVariantTracks();
      const target = all.find((t: any) => t.id === match.id);
      if (target) this.player.selectVariantTrack(target, true);
    }
  }

  /** External quality map (multiple separate URLs per quality, from balancer). */
  getQualityMap(): Record<string, string> | null {
    return this.currentElement?.quality || null;
  }

  // --- Audio ---

  audio(): AudioTrack[] {
    if (!this.player?.getVariantTracks) return [];
    const tracks = this.player.getVariantTracks();
    // Dedup by the most stable identity we can: prefer label, then
    // language. shaka's `audioId` is unstable across some HLS manifests
    // (returned per-variant rather than per-audio-track), which produces
    // N×M duplicate rows. The `active` flag is OR'd across the variants
    // so the menu highlight tracks the active dub correctly.
    const seen = new Map<string, AudioTrack>();
    for (const t of tracks) {
      const lang = t.language || 'und';
      const lbl = (t.label as string | undefined)?.trim() || '';
      const key = lbl ? `lbl:${lbl.toLowerCase()}` : `lang:${lang.toLowerCase()}`;
      const existing = seen.get(key);
      if (existing) {
        if (t.active) existing.active = true;
        continue;
      }
      seen.set(key, {
        id: t.id,
        label: lbl || lang || 'Unknown',
        language: lang,
        active: !!t.active,
      });
    }
    return Array.from(seen.values());
  }

  /**
   * Switch the active audio track. We don't naively select the variant
   * id we exposed via audio() — that variant is an arbitrary one for the
   * given audio (often the lowest-bitrate combo), so naive selection
   * also drops video quality. Instead we find the variant that matches
   * the CURRENT video height, falling back to the closest height.
   */
  selectAudio(trackId: number): void {
    if (!this.player) return;
    const all = this.player.getVariantTracks();
    const reference = all.find((t: any) => t.id === trackId);
    if (!reference) return;

    const refLabel = (reference.label || '').toLowerCase();
    const refLang = reference.language;

    // Determine the height we want to keep (current active variant).
    const activeVariant = all.find((t: any) => t.active);
    const desiredHeight = activeVariant?.height || 0;

    // Filter to variants that match the requested audio (by label first,
    // then language as fallback).
    const candidates = all.filter((t: any) => {
      const tLabel = (t.label || '').toLowerCase();
      if (refLabel && tLabel) return tLabel === refLabel;
      return t.language === refLang;
    });
    if (candidates.length === 0) {
      this.player.selectVariantTrack(reference, true);
      return;
    }

    // Prefer the variant whose height matches the active height; on tie,
    // highest bandwidth wins.
    candidates.sort((a: any, b: any) => {
      if (desiredHeight) {
        const da = Math.abs((a.height || 0) - desiredHeight);
        const db = Math.abs((b.height || 0) - desiredHeight);
        if (da !== db) return da - db;
      }
      return (b.bandwidth || 0) - (a.bandwidth || 0);
    });
    this.player.selectVariantTrack(candidates[0], true);
  }

  applyPreferredAudioLang(lang: string): void {
    if (!this.player || !lang) return;
    const tracks = this.audio();
    const match = tracks.find(
      (t) => t.language.toLowerCase() === lang.toLowerCase(),
    );
    if (match) this.selectAudio(match.id);
  }

  // --- Text / Subtitles ---

  text(): TextTrack[] {
    if (!this.player?.getTextTracks) return [];
    return this.player.getTextTracks().map((t: any) => ({
      id: t.id,
      label: t.label || t.language || 'Unknown',
      language: t.language || 'und',
      active: t.active || false,
    }));
  }

  /**
   * Optional observer invoked after every selectText call with the picked
   * track's label (null when subtitles were turned off). CorePlayback uses it
   * to swap Shaka's text display for the JASSUB overlay on ASS tracks.
   */
  onTextSelected: ((label: string | null) => void) | null = null;

  selectText(trackId: number | 'off'): void {
    if (!this.player) return;
    if (trackId === 'off') {
      this.player.setTextTrackVisibility(false);
      this.onTextSelected?.(null);
      return;
    }
    const tracks = this.player.getTextTracks();
    const target = tracks.find((t: any) => t.id === trackId);
    if (target) {
      this.player.selectTextTrack(target);
      this.player.setTextTrackVisibility(true);
      this.onTextSelected?.(target.label || target.language || null);
    }
  }

  applyPreferredSubLang(lang: string): void {
    if (!this.player || !lang) return;
    const tracks = this.text();
    const match = tracks.find(
      (t) => t.language.toLowerCase() === lang.toLowerCase(),
    );
    if (match) this.selectText(match.id);
  }

  /** Add an external subtitle file. Uses shaka.addTextTrackAsync. */
  async addExternalSubtitle(
    url: string,
    language: string,
    label: string,
    mimeOverride?: string,
  ): Promise<void> {
    if (!this.player?.addTextTrackAsync) return;
    const mime = mimeOverride
      ? mimeOverride
      : url.endsWith('.vtt')
        ? 'text/vtt'
        : url.endsWith('.srt')
          ? 'text/srt'
          : 'text/vtt';
    try {
      await this.player.addTextTrackAsync(
        url,
        language || 'und',
        'subtitle',
        mime,
        undefined,
        label || language || 'Subtitles',
      );
    } catch (err) {
      console.warn('[LWP] addExternalSubtitle failed:', err);
    }
  }
}

function qualityLabel(height: number): string {
  if (height >= 2160) return '4K';
  if (height >= 1440) return '2K';
  if (height >= 1080) return '1080p';
  if (height >= 720) return '720p';
  if (height >= 480) return '480p';
  if (height >= 360) return '360p';
  if (height > 0) return `${height}p`;
  return 'Авто';
}
