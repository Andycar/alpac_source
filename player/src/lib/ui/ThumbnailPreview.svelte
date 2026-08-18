<script lang="ts">
  import { onDestroy } from 'svelte';
  import { currentElement } from '../stores/player';
  import { thumbnailManifest } from '../stores/thumbnails';
  import { settings } from '../stores/settings';
  import { TrickplayManifest } from '../engine/features/TrickplayManifest';
  import type { ThumbnailManifest, ThumbnailCue, ThumbnailSheet } from '../engine/core/types';

  let {
    time = 0,
    x = 0,
    visible = false,
    containerWidth = 0,
  }: {
    time: number;
    x: number;
    visible: boolean;
    containerWidth: number;
  } = $props();

  // Only show on devices with hover capability (no touch-only)
  const hasHover = typeof window !== 'undefined' && window.matchMedia('(hover: hover)').matches;

  let canvasEl = $state<HTMLCanvasElement>(undefined!);
  let hiddenVideo: HTMLVideoElement | null = null;
  let shakaInstance: any = null;
  let lastSeekTime = -999;
  let seeking = false;
  let ready = $state(false);
  let initialized = false;

  // --- Sprite path state (Wave 1 B2) ---
  // Set when thumbnailManifest store has a usable manifest. When non-null
  // we render a <div> with a background-image fragment instead of the
  // canvas-extracted frame. Falls back to canvas when manifest is null.
  let spriteStyle = $state<string | null>(null);

  const THUMB_W = 160;
  const THUMB_H = 90;
  const MIN_SEEK_DELTA = 2; // minimum seconds between seeks (canvas path)

  // Clamp left position so thumbnail doesn't go off-screen
  const clampedLeft = $derived(() => {
    const halfW = THUMB_W / 2;
    if (containerWidth <= 0) return x;
    return Math.max(halfW, Math.min(containerWidth - halfW, x));
  });

  /**
   * Build a CSS background style for a manifest cue. Handles both the
   * shaka-image packed coordinate format and standard sprite-grid layout.
   */
  function buildSpriteStyle(
    manifest: ThumbnailManifest,
    cue: ThumbnailCue,
    sheet: ThumbnailSheet,
  ): string {
    let bgX = 0;
    let bgY = 0;
    let cellW = sheet.cellW;
    let cellH = sheet.cellH;
    let bgSizeX: number | 'cover' = 'cover';
    let bgSizeY: number | 'cover' = 'cover';

    if (manifest.type === 'shaka-image') {
      // cellIndex packed: (positionX << 16) | positionY
      bgX = (cue.cellIndex >>> 16) & 0xffff;
      bgY = cue.cellIndex & 0xffff;
      // Sheet pixel size unknown — use natural size; rely on sheet cell dims.
    } else {
      // Grid layout: row-major
      const col = cue.cellIndex % sheet.cols;
      const row = Math.floor(cue.cellIndex / sheet.cols);
      bgX = col * sheet.cellW;
      bgY = row * sheet.cellH;
      bgSizeX = sheet.cols * sheet.cellW;
      bgSizeY = sheet.rows * sheet.cellH;
    }

    // Scale the sprite to our preview box size while preserving aspect.
    const scaleX = THUMB_W / cellW;
    const scaleY = THUMB_H / cellH;
    const scale = Math.min(scaleX, scaleY);
    const scaledBgX = -bgX * scale;
    const scaledBgY = -bgY * scale;

    let bgSize: string;
    if (bgSizeX === 'cover') {
      bgSize = `${cellW * scale}px ${cellH * scale}px`;
    } else {
      bgSize = `${(bgSizeX as number) * scale}px ${(bgSizeY as number) * scale}px`;
    }

    return [
      `background-image: url("${sheet.url}")`,
      `background-position: ${scaledBgX}px ${scaledBgY}px`,
      `background-size: ${bgSize}`,
      `background-repeat: no-repeat`,
      `width: ${THUMB_W}px`,
      `height: ${THUMB_H}px`,
    ].join('; ');
  }

  // React to manifest+time: update spriteStyle synchronously (no fetch).
  $effect(() => {
    const manifest = $thumbnailManifest;
    const useExternal = $settings.useExternalThumbnails;
    if (!visible || !manifest || !useExternal) {
      spriteStyle = null;
      return;
    }
    const cue = TrickplayManifest.cueAt(manifest, time);
    if (!cue) { spriteStyle = null; return; }
    const sheet = manifest.spriteSheets?.[cue.sheetIndex];
    if (!sheet) { spriteStyle = null; return; }
    spriteStyle = buildSpriteStyle(manifest, cue, sheet);
    ready = true;
  });

  /**
   * Lazy init: create hidden video on first hover (CANVAS PATH only).
   * Skipped when sprite manifest is available — saves a duplicate stream.
   */
  async function initHiddenVideo(): Promise<void> {
    if (initialized || !$currentElement?.url) return;
    if (spriteStyle !== null) return; // sprite path active, no video needed
    initialized = true;

    hiddenVideo = document.createElement('video');
    hiddenVideo.muted = true;
    hiddenVideo.preload = 'metadata';
    hiddenVideo.crossOrigin = 'anonymous';
    hiddenVideo.style.display = 'none';
    document.body.appendChild(hiddenVideo);

    const url = $currentElement.url;
    const isHLS = url.includes('.m3u8');

    if (isHLS && typeof (window as any).shaka !== 'undefined') {
      // HLS in Chrome needs shaka — raw <video> can't play HLS
      try {
        const shakaLib = (window as any).shaka;
        shakaInstance = new shakaLib.Player();
        await shakaInstance.attach(hiddenVideo);
        shakaInstance.configure({
          streaming: { bufferingGoal: 5, rebufferingGoal: 1 },
          abr: { enabled: false },
        });
        await shakaInstance.load(url);
        ready = true;
      } catch (err) {
        console.warn('[LWP] Thumbnail shaka init failed:', err);
        cleanup();
      }
    } else {
      // MP4 or Safari (native HLS)
      hiddenVideo.src = url;
      hiddenVideo.addEventListener('loadedmetadata', () => {
        ready = true;
      }, { once: true });
    }
  }

  function drawFrame(): void {
    if (!hiddenVideo || !canvasEl) return;
    try {
      const ctx = canvasEl.getContext('2d');
      if (!ctx) return;
      ctx.drawImage(hiddenVideo, 0, 0, THUMB_W, THUMB_H);
    } catch {
      // Cross-origin or other canvas error — silently fail
    }
  }

  async function seekAndDraw(t: number): Promise<void> {
    if (!hiddenVideo || !ready || seeking) return;
    if (Math.abs(t - lastSeekTime) < MIN_SEEK_DELTA) return;

    seeking = true;
    lastSeekTime = t;

    try {
      hiddenVideo.currentTime = t;
      await new Promise<void>((resolve) => {
        const onSeeked = () => {
          hiddenVideo?.removeEventListener('seeked', onSeeked);
          resolve();
        };
        hiddenVideo?.addEventListener('seeked', onSeeked);
        // Timeout in case seeked never fires
        setTimeout(resolve, 1000);
      });
      drawFrame();
    } catch {
      // ignore
    } finally {
      seeking = false;
    }
  }

  // React to time changes (canvas fallback path)
  $effect(() => {
    if (visible && hasHover && time >= 0 && spriteStyle === null) {
      if (!initialized) {
        initHiddenVideo();
      }
      if (ready) {
        seekAndDraw(time);
      }
    }
  });

  // Reset when URL changes
  $effect(() => {
    void $currentElement?.url;
    cleanup();
    initialized = false;
    ready = false;
    lastSeekTime = -999;
    spriteStyle = null;
  });

  function cleanup(): void {
    if (shakaInstance) {
      try { shakaInstance.destroy(); } catch {}
      shakaInstance = null;
    }
    if (hiddenVideo) {
      hiddenVideo.pause();
      hiddenVideo.removeAttribute('src');
      hiddenVideo.remove();
      hiddenVideo = null;
    }
  }

  onDestroy(cleanup);
</script>

{#if visible && hasHover && ready}
  <div
    class="lwp-thumbnail-preview"
    style="left: {clampedLeft()}px;"
  >
    {#if spriteStyle}
      <!-- Sprite path: zero-cost background-image fragment -->
      <div class="lwp-thumbnail-sprite" style={spriteStyle}></div>
    {:else}
      <!-- Canvas path: extracted from a hidden duplicate <video> -->
      <canvas
        bind:this={canvasEl}
        width={THUMB_W}
        height={THUMB_H}
      ></canvas>
    {/if}
  </div>
{/if}
